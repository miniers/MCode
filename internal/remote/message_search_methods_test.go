package remote

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

const (
	searchTestProjectID   = "search-project"
	searchTestThreadID    = "11111111-1111-1111-1111-111111111111"
	searchTestMessageID   = "22222222-2222-2222-2222-222222222222"
	searchTestOtherThread = "33333333-3333-3333-3333-333333333333"
)

func searchTestRig(t *testing.T) (*Dispatcher, *recordingConn) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, store.Project{
		ID: searchTestProjectID, Name: "搜索项目", Path: "/search", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	for _, thread := range []store.Thread{
		{ID: searchTestThreadID, ProjectID: searchTestProjectID, Title: "会话搜索用例", Provider: "openai", CreatedAt: now, UpdatedAt: now},
		{ID: searchTestOtherThread, ProjectID: searchTestProjectID, Title: "另一个会话", Provider: "openai", CreatedAt: now, UpdatedAt: now},
	} {
		if err := s.CreateThread(ctx, thread); err != nil {
			t.Fatalf("CreateThread %s 失败: %v", thread.ID, err)
		}
	}
	for _, message := range []store.Message{
		{ID: searchTestMessageID, ThreadID: searchTestThreadID, Role: "assistant", Kind: "text", Text: "这里是助手回复正文"},
		{ID: "44444444-4444-4444-4444-444444444444", ThreadID: searchTestThreadID, Role: "user", Kind: "text", Text: "这里提到了颜色混合的问题"},
		{ID: "55555555-5555-5555-5555-555555555555", ThreadID: searchTestOtherThread, Role: "user", Kind: "text", Text: "另一个会话也提到颜色混合"},
	} {
		message.TurnIndex = 1
		message.CreatedAt = now
		if _, err := s.AppendMessage(ctx, message); err != nil {
			t.Fatalf("AppendMessage %s 失败: %v", message.ID, err)
		}
	}

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}

	d := NewDispatcher(&fakeAuth{}, backend)
	rec := &recordingConn{info: ClientInfo{Authenticated: true, DeviceID: "search-device"}}
	d.addAuthedConn(rec)
	return d, rec
}

func searchCallMethod(t *testing.T, d *Dispatcher, conn Conn, method, params string, id int, v any) wire.Response {
	t.Helper()
	raw := `{"jsonrpc":"2.0","method":"` + method + `","params":` + params + `,"id":` + strconv.Itoa(id) + `}`
	resp := callExtension(t, d, conn, raw)
	if resp.Error == nil && v != nil {
		out, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		if err := json.Unmarshal(out, v); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
	}
	return resp
}

func searchAuthConn() *fakeConn {
	return &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "search-device"}}
}

func TestMessagesSearchHitAndLimit(t *testing.T) {
	d, _ := searchTestRig(t)
	conn := searchAuthConn()

	var result wire.MessageSearchResult
	resp := searchCallMethod(t, d, conn, "messages.search", `{"query":"颜色混合"}`, 1, &result)
	if resp.Error != nil {
		t.Fatalf("messages.search 失败: %+v", resp.Error)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("应命中 2 条消息，实际: %+v", result.Hits)
	}
	var other *wire.MessageSearchHit
	for i := range result.Hits {
		if result.Hits[i].ThreadID == searchTestOtherThread {
			other = &result.Hits[i]
		}
	}
	if other == nil {
		t.Fatalf("缺少另一个会话的命中: %+v", result.Hits)
	}
	if other.ThreadTitle != "另一个会话" || other.Role != "user" {
		t.Fatalf("命中字段不对: %+v", other)
	}
	if !strings.Contains(other.Snippet, "颜色混合") {
		t.Fatalf("snippet 应包含关键词: %q", other.Snippet)
	}

	// 显式 limit 生效，且上限 100 不报错。
	var limited wire.MessageSearchResult
	resp = searchCallMethod(t, d, conn, "messages.search", `{"query":"颜色混合","limit":1}`, 2, &limited)
	if resp.Error != nil {
		t.Fatalf("带 limit 的搜索失败: %+v", resp.Error)
	}
	if len(limited.Hits) != 1 {
		t.Fatalf("limit=1 应只返回 1 条，实际: %+v", limited.Hits)
	}
	var capped wire.MessageSearchResult
	resp = searchCallMethod(t, d, conn, "messages.search", `{"query":"颜色混合","limit":1000}`, 3, &capped)
	if resp.Error != nil {
		t.Fatalf("超上限的 limit 应被夹到 100: %+v", resp.Error)
	}
}

func TestMessageSearchInvalidParams(t *testing.T) {
	d, _ := searchTestRig(t)
	conn := searchAuthConn()

	cases := []struct {
		name   string
		method string
		params string
	}{
		{"messages.search 空查询", "messages.search", `{"query":"   "}`},
		{"messages.search 未知字段", "messages.search", `{"query":"颜色","unknown":1}`},
	}
	for i, tc := range cases {
		resp := searchCallMethod(t, d, conn, tc.method, tc.params, i+1, nil)
		if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
			t.Fatalf("%s: 期望 InvalidParams，实际: %+v", tc.name, resp.Error)
		}
	}
}
