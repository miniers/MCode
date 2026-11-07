package nmem

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testKey = "nmem_testkey_1234567890"

// newServer 起一个 httptest 服务，先统一校验两种鉴权头，再交给 handler。
func newServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testKey {
			t.Errorf("Authorization = %q, 期望 Bearer %s", got, testKey)
		}
		if got := r.Header.Get("X-NMEM-API-Key"); got != testKey {
			t.Errorf("X-NMEM-API-Key = %q, 期望 %s", got, testKey)
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealthNoAuth(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	defer srv.Close()

	client := New(srv.URL, "")
	if err := client.Health(context.Background()); err != nil {
		t.Fatalf("Health 返回错误: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("未配置密钥时不应带鉴权头，实际 %q", gotAuth)
	}
}

func TestSpacesAuthHeaders(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/spaces" {
			t.Errorf("请求 = %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"enabled":true,"spaces":[
			{"id":"default","name":"Default","key":"default","aliases":[]},
			{"id":"mcode","name":"mcode","key":null,"aliases":["MC","Code"]}
		]}`)
	})

	spaces, err := New(srv.URL+"/", testKey).Spaces(context.Background())
	if err != nil {
		t.Fatalf("Spaces 返回错误: %v", err)
	}
	if len(spaces) != 2 {
		t.Fatalf("空间数 = %d, 期望 2", len(spaces))
	}
	if spaces[0].ID != "default" || spaces[0].Name != "Default" || spaces[0].Key != "default" {
		t.Errorf("首个空间 = %+v", spaces[0])
	}
	if spaces[1].Key != "" {
		t.Errorf("null key 应映射为空串，实际 %q", spaces[1].Key)
	}
	if len(spaces[1].Aliases) != 2 {
		t.Errorf("aliases = %v", spaces[1].Aliases)
	}
}

func TestDefaultFallback(t *testing.T) {
	// 环境变量优先。
	t.Run("环境变量", func(t *testing.T) {
		home := t.TempDir()
		writeConfig(t, home, `{"apiUrl":"https://from-file.example","apiKey":"nmem_file"}`)
		t.Setenv("HOME", home)
		t.Setenv(envAPIURL, "https://from-env.example/")
		t.Setenv(envAPIKey, "nmem_env")

		client, err := Default()
		if err != nil {
			t.Fatalf("Default 返回错误: %v", err)
		}
		if client.baseURL != "https://from-env.example" {
			t.Errorf("baseURL = %q", client.baseURL)
		}
		if client.apiKey != "nmem_env" {
			t.Errorf("apiKey = %q", client.apiKey)
		}
	})

	// 环境变量为空时读配置文件。
	t.Run("配置文件", func(t *testing.T) {
		home := t.TempDir()
		writeConfig(t, home, `{"apiUrl":"https://from-file.example/","apiKey":"nmem_file"}`)
		t.Setenv("HOME", home)
		t.Setenv(envAPIURL, "")
		t.Setenv(envAPIKey, "")

		client, err := Default()
		if err != nil {
			t.Fatalf("Default 返回错误: %v", err)
		}
		if client.baseURL != "https://from-file.example" {
			t.Errorf("baseURL = %q", client.baseURL)
		}
		if client.apiKey != "nmem_file" {
			t.Errorf("apiKey = %q", client.apiKey)
		}
	})

	// 有 key 但没写地址时回落到本机默认地址。
	t.Run("缺少地址回落默认地址", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv(envAPIURL, "")
		t.Setenv(envAPIKey, "nmem_env")

		client, err := Default()
		if err != nil {
			t.Fatalf("Default 返回错误: %v", err)
		}
		if client.baseURL != DefaultBaseURL {
			t.Errorf("baseURL = %q, 期望 %q", client.baseURL, DefaultBaseURL)
		}
		if client.apiKey != "nmem_env" {
			t.Errorf("apiKey = %q", client.apiKey)
		}
	})

	// 环境变量和配置文件都没有 key 时不创建客户端，也不发请求。
	t.Run("缺少密钥不创建客户端", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		t.Setenv(envAPIURL, "")
		t.Setenv(envAPIKey, "")

		client, err := Default()
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("缺少密钥时应返回 ErrNotConfigured，实际 %v", err)
		}
		if client != nil {
			t.Errorf("缺少密钥时客户端应为 nil，实际 %+v", client)
		}
	})

	// 配置文件存在但没有 key 时同样不创建客户端。
	t.Run("配置文件无密钥", func(t *testing.T) {
		home := t.TempDir()
		writeConfig(t, home, `{"apiUrl":"https://from-file.example"}`)
		t.Setenv("HOME", home)
		t.Setenv(envAPIURL, "")
		t.Setenv(envAPIKey, "")

		client, err := Default()
		if !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("配置文件无密钥时应返回 ErrNotConfigured，实际 %v", err)
		}
		if client != nil {
			t.Errorf("配置文件无密钥时客户端应为 nil，实际 %+v", client)
		}
	})

	// 配置文件损坏时报错。
	t.Run("配置文件损坏", func(t *testing.T) {
		home := t.TempDir()
		writeConfig(t, home, `{not json`)
		t.Setenv("HOME", home)
		t.Setenv(envAPIURL, "")
		t.Setenv(envAPIKey, "")

		if _, err := Default(); err == nil {
			t.Fatal("配置文件损坏时应返回错误")
		}
	})
}

func writeConfig(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, configDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
}

func TestResolveSpace(t *testing.T) {
	spaces := []Space{
		{ID: "default", Name: "Default", Key: "default"},
		{ID: "work-1", Name: "Work", Key: "work", Aliases: []string{"Office", "day-job"}},
	}
	cases := []struct {
		name  string
		want  string
		found bool
	}{
		{"default", "default", true},
		{"DEFAULT", "default", true},
		{"work", "work-1", true},
		{"WORK-1", "work-1", true},
		{"office", "work-1", true},
		{"Day-Job", "work-1", true},
		{"  Work  ", "work-1", true},
		{"missing", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := ResolveSpace(spaces, c.name)
		if ok != c.found {
			t.Errorf("ResolveSpace(%q) 命中 = %v, 期望 %v", c.name, ok, c.found)
			continue
		}
		if ok && got.ID != c.want {
			t.Errorf("ResolveSpace(%q) = %q, 期望 %q", c.name, got.ID, c.want)
		}
	}

	// ID 匹配优先于 Name/Key/Aliases。
	tie := []Space{{ID: "b", Name: "a", Key: "a"}, {ID: "a", Name: "b", Key: "b"}}
	if got, ok := ResolveSpace(tie, "a"); !ok || got.ID != "a" {
		t.Errorf("ID 应优先匹配，实际 %+v %v", got, ok)
	}
}

func TestWorkingMemory(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/working-memory" {
			t.Errorf("path = %q", r.URL.Path)
		}
		// 空 space 应回落到 default。
		if got := r.URL.Query().Get("space_id"); got != "default" {
			t.Errorf("space_id = %q, 期望 default", got)
		}
		_, _ = io.WriteString(w, `{"exists":true,"content":"# Working Memory\n\n## Focus Areas\n- a","date":"2026-10-28","space_id":"default"}`)
	})

	got, err := New(srv.URL, testKey).WorkingMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("WorkingMemory 返回错误: %v", err)
	}
	if !strings.Contains(got, "## Focus Areas") {
		t.Errorf("正文 = %q", got)
	}
}

func TestWorkingMemoryCustomSpace(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("space_id"); got != "mcode" {
			t.Errorf("space_id = %q, 期望 mcode", got)
		}
		_, _ = io.WriteString(w, `{"exists":false,"content":"","space_id":"mcode"}`)
	})

	got, err := New(srv.URL, testKey).WorkingMemory(context.Background(), "mcode")
	if err != nil {
		t.Fatalf("WorkingMemory 返回错误: %v", err)
	}
	if got != "" {
		t.Errorf("不存在的空间应返回空正文，实际 %q", got)
	}
}

func TestImportThreadRequest(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/threads/import" {
			t.Errorf("请求 = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if body["thread_id"] != "t-1" {
			t.Errorf("thread_id = %v", body["thread_id"])
		}
		if body["title"] != "标题" {
			t.Errorf("title = %v", body["title"])
		}
		if body["source"] != "mcode" {
			t.Errorf("source = %v", body["source"])
		}
		if body["space_id"] != "mcode" {
			t.Errorf("space_id = %v", body["space_id"])
		}
		msgs, _ := body["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("messages 数 = %d, 期望 2", len(msgs))
		}
		first, _ := msgs[0].(map[string]any)
		if first["role"] != "user" || first["content"] != "你好" {
			t.Errorf("首条消息 = %v", first)
		}
		if _, ok := first["timestamp"]; ok {
			t.Errorf("零值时间不应发 timestamp，实际 %v", first["timestamp"])
		}
		second, _ := msgs[1].(map[string]any)
		if ts, _ := second["timestamp"].(string); ts != "2026-10-28T06:30:00Z" {
			t.Errorf("timestamp = %v", second["timestamp"])
		}
		meta, _ := body["metadata"].(map[string]any)
		if meta["session"] != "s-1" {
			t.Errorf("metadata = %v", body["metadata"])
		}

		_, _ = io.WriteString(w, `{"success":true,"imported_count":1,"skipped_count":0,"failed_count":0,
			"results":[{"success":true,"thread_id":"t-1","message_count":2,"messages_added":2,"already_existed":false}],
			"auto_review":null}`)
	})

	err := New(srv.URL, testKey).ImportThread(context.Background(), Thread{
		ID:     "t-1",
		Title:  "标题",
		Source: "mcode",
		Space:  "mcode",
		Messages: []Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "在", Timestamp: time.Date(2026, 10, 28, 6, 30, 0, 0, time.UTC)},
		},
		Metadata: map[string]any{"session": "s-1"},
	})
	if err != nil {
		t.Fatalf("ImportThread 返回错误: %v", err)
	}
}

func TestImportThreadFailure(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"success":false,"imported_count":0,"skipped_count":0,"failed_count":1,
			"results":[{"success":false,"thread_id":"t-2","message_count":0,"messages_added":0,
			"already_existed":false,"error":"thread 已存在"}]}`)
	})

	err := New(srv.URL, testKey).ImportThread(context.Background(), Thread{ID: "t-2"})
	if err == nil {
		t.Fatal("success=false 时应返回错误")
	}
	if !strings.Contains(err.Error(), "thread 已存在") {
		t.Errorf("错误信息 = %v", err)
	}
}

func TestSearchMemories(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/memories/search" {
			t.Errorf("请求 = %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Query   string `json:"query"`
			Limit   int    `json:"limit"`
			SpaceID string `json:"space_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if body.Query != "worktree" || body.Limit != 3 || body.SpaceID != "mcode" {
			t.Errorf("请求体 = %+v", body)
		}
		_, _ = io.WriteString(w, `[
			{"memory":{"id":"m1","title":"标题","content":"正文","unit_type":"decision",
			 "importance":0.7,"created_at":"2026-10-16T06:39:32+00:00",
			 "metadata":{"labels":["构建"]}},"similarity_score":0.9},
			{"memory":null,"similarity_score":0.1}
		]`)
	})

	memories, err := New(srv.URL, testKey).SearchMemories(context.Background(), "mcode", "worktree", 3)
	if err != nil {
		t.Fatalf("SearchMemories 返回错误: %v", err)
	}
	if len(memories) != 1 {
		t.Fatalf("结果数 = %d, 期望 1", len(memories))
	}
	m := memories[0]
	if m.ID != "m1" || m.Title != "标题" || m.Content != "正文" {
		t.Errorf("记忆 = %+v", m)
	}
	if m.UnitType != "decision" || m.Importance != 0.7 {
		t.Errorf("记忆字段 = %+v", m)
	}
	if len(m.Labels) != 1 || m.Labels[0] != "构建" {
		t.Errorf("labels 应回落到 metadata，实际 %v", m.Labels)
	}
	if m.CreatedAt.IsZero() || m.CreatedAt.Year() != 2026 {
		t.Errorf("created_at = %v", m.CreatedAt)
	}
}

func TestAddMemory(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/memories" {
			t.Errorf("请求 = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if body["content"] != "记住这条" {
			t.Errorf("content = %v", body["content"])
		}
		if body["unit_type"] != "decision" {
			t.Errorf("unit_type = %v", body["unit_type"])
		}
		if body["space_id"] != "mcode" {
			t.Errorf("space_id = %v", body["space_id"])
		}
		if body["importance"] != 0.8 {
			t.Errorf("importance = %v", body["importance"])
		}
		_, _ = io.WriteString(w, `{"memory":{"id":"m2","title":"标题","content":"记住这条",
			"unit_type":"decision","importance":0.8,"created_at":"2026-10-28T06:30:00Z"},
			"assigned_labels":["架构"],"action":"created"}`)
	})

	m, err := New(srv.URL, testKey).AddMemory(context.Background(), "mcode", MemoryInput{
		Title:      "标题",
		Content:    "记住这条",
		UnitType:   "decision",
		Importance: 0.8,
	})
	if err != nil {
		t.Fatalf("AddMemory 返回错误: %v", err)
	}
	if m.ID != "m2" || m.Content != "记住这条" || m.Importance != 0.8 {
		t.Errorf("记忆 = %+v", m)
	}
	if len(m.Labels) != 1 || m.Labels[0] != "架构" {
		t.Errorf("labels 应取 assigned_labels，实际 %v", m.Labels)
	}
}

func TestAPIErrorNoKey(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		// 极端情况：服务端把密钥回显在错误体里。
		_, _ = io.WriteString(w, `{"detail":"bad key `+testKey+`"}`+strings.Repeat("x", 4096))
	})

	_, err := New(srv.URL, testKey).Spaces(context.Background())
	if err == nil {
		t.Fatal("非 2xx 应返回错误")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("错误类型 = %T, 期望 *APIError", err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d", apiErr.Status)
	}
	if len(apiErr.Body) > maxErrorBody {
		t.Errorf("Body 长度 = %d, 不应超过 %d", len(apiErr.Body), maxErrorBody)
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("错误信息泄露了密钥: %v", err)
	}
	if strings.Contains(apiErr.Body, testKey) {
		t.Errorf("Body 泄露了密钥: %q", apiErr.Body)
	}
}

func TestDistillPreview(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/memories/distill/preview" {
			t.Errorf("请求 = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if body["thread_id"] != "t-3" || body["space_id"] != "mcode" {
			t.Errorf("请求体 = %v", body)
		}
		// 另一个写端点绝不能出现在请求里。
		_, _ = io.WriteString(w, `{"success":true,"cache_key":"c1","distillation_type":"simple_llm",
			"processing_time":1.2,"summary":"s","entities":[],"relationships":[],"insights":[],
			"memories":[{"title":"标题","content":"正文","unit_type":"procedure","importance":0.6,"labels":["流程"]},
			            {"title":"","content":""}]}`)
	})

	inputs, err := New(srv.URL, testKey).DistillPreview(context.Background(), "mcode", "t-3")
	if err != nil {
		t.Fatalf("DistillPreview 返回错误: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("预览条数 = %d, 期望 1", len(inputs))
	}
	if inputs[0].Title != "标题" || inputs[0].Content != "正文" || inputs[0].UnitType != "procedure" {
		t.Errorf("预览 = %+v", inputs[0])
	}
	if len(inputs[0].Labels) != 1 || inputs[0].Labels[0] != "流程" {
		t.Errorf("labels = %v", inputs[0].Labels)
	}
}

func TestDistillPreviewUnsupported(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	_, err := New(srv.URL, testKey).DistillPreview(context.Background(), "mcode", "t-4")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("错误 = %v, 期望 ErrUnsupported", err)
	}
}

func TestUnitTypes(t *testing.T) {
	want := []string{"fact", "preference", "decision", "plan", "procedure", "learning", "context", "event"}
	if len(UnitTypes) != len(want) {
		t.Fatalf("UnitTypes 长度 = %d, 期望 %d", len(UnitTypes), len(want))
	}
	for i, v := range want {
		if UnitTypes[i] != v {
			t.Errorf("UnitTypes[%d] = %q, 期望 %q", i, UnitTypes[i], v)
		}
	}
}

func TestNewTrimsTrailingSlash(t *testing.T) {
	client := New("https://mem.m.lk.mk///", "k")
	if client.baseURL != "https://mem.m.lk.mk" {
		t.Errorf("baseURL = %q", client.baseURL)
	}
	if client.http == nil || client.http.Timeout != requestTimeout {
		t.Errorf("http 客户端超时 = %v, 期望 %v", client.http, requestTimeout)
	}
}

func TestSearchSendsFilters(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		if body["unit_type"] != "decision" || body["mode"] != "fast" || body["space_id"] != "mcode" {
			t.Errorf("过滤条件未发送: %v", body)
		}
		if labels, _ := body["filter_labels"].([]any); len(labels) != 1 || labels[0] != "ui" {
			t.Errorf("filter_labels = %v", body["filter_labels"])
		}
		_, _ = io.WriteString(w, `[{"memory":{"id":"m1","title":"T","space_id":"global"},"similarity_score":0.7}]`)
	})
	mems, err := New(srv.URL, testKey).Search(context.Background(), "mcode", SearchQuery{Query: "q", UnitType: "decision", Labels: []string{"ui"}, Mode: "fast"})
	if err != nil {
		t.Fatalf("Search 返回错误: %v", err)
	}
	if len(mems) != 1 || mems[0].Space != "global" || mems[0].Score != 0.7 {
		t.Fatalf("结果 = %+v", mems)
	}
}

func TestGetAndUpdateMemory(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Path != "/memories/m1" || r.URL.Query().Get("space_id") != "mcode" {
				t.Errorf("GET = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"id":"m1","title":"T","content":"C","unit_type":"fact","space_id":"mcode","labels":["a"]}`)
		case http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path != "/memories/m1" || body["title"] != "T2" || body["content"] != "C2" || body["importance"] != 0.8 {
				t.Errorf("PATCH %s body = %v", r.URL.Path, body)
			}
			_, _ = io.WriteString(w, `{"id":"m1","title":"T2","content":"C2","space_id":"global"}`)
		}
	})
	c := New(srv.URL, testKey)
	m, err := c.GetMemory(context.Background(), "mcode", "m1")
	if err != nil || m.Title != "T" || m.Space != "mcode" || len(m.Labels) != 1 {
		t.Fatalf("GetMemory = %+v, %v", m, err)
	}
	m, err = c.UpdateMemory(context.Background(), "m1", MemoryInput{Title: "T2", Content: "C2", Importance: 0.8})
	if err != nil || m.Content != "C2" || m.Space != "global" {
		t.Fatalf("UpdateMemory = %+v, %v", m, err)
	}
}

func TestSearchAndFetchThreads(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/threads/search":
			if q.Get("query") != "merge" || q.Get("space_id") != "mcode" || q.Get("limit") != "3" {
				t.Errorf("search query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"threads":[{"thread_id":"mcode-t1","title":"Fix","source":"mcode","message_count":9,
				"last_activity":"2026-10-28T23:34:32.500755Z","matched_messages":[{"message_index":4,"role":"user","snippet":"why"}]}],"total_found":1}`)
		case "/threads/mcode-t1":
			if q.Get("space_id") != "mcode" || q.Get("offset") != "4" || q.Get("limit") != "2" {
				t.Errorf("fetch query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"thread":{"thread_id":"mcode-t1","title":"Fix","message_count":9},"total_messages":9,
				"messages":[{"role":"user","content":"why","timestamp":"2026-10-28T14:21:42.761604+00:00"},{"role":"assistant","content":"because"}]}`)
		default:
			t.Errorf("意外请求 %s", r.URL.Path)
		}
	})
	c := New(srv.URL, testKey)
	hits, err := c.SearchThreads(context.Background(), "mcode", "merge", 3)
	if err != nil || len(hits) != 1 || hits[0].ThreadID != "mcode-t1" || hits[0].LastActivity.IsZero() ||
		len(hits[0].Matches) != 1 || hits[0].Matches[0].Index != 4 {
		t.Fatalf("SearchThreads = %+v, %v", hits, err)
	}
	page, err := c.FetchThread(context.Background(), "mcode", "mcode-t1", 4, 2)
	if err != nil || page.Total != 9 || page.Offset != 4 || len(page.Messages) != 2 || page.Messages[0].Timestamp.IsZero() {
		t.Fatalf("FetchThread = %+v, %v", page, err)
	}
}

// TestDeleteMemoryAndForget 验证服务端支持删除时 ForgetMemory 走 DELETE 并返回已删除。
func TestDeleteMemoryAndForget(t *testing.T) {
	var calls []string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Method != http.MethodDelete || r.URL.Path != "/memories/m1" {
			t.Errorf("请求 = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})

	deleted, err := New(srv.URL, testKey).ForgetMemory(context.Background(), "mcode", "m1")
	if err != nil {
		t.Fatalf("ForgetMemory 返回错误: %v", err)
	}
	if !deleted {
		t.Fatal("DELETE 成功时 deleted 应为 true")
	}
	if len(calls) != 1 {
		t.Fatalf("期望只发一次 DELETE，实际调用 %v", calls)
	}
}

// TestForgetMemoryMarksDeprecated 验证服务端不支持删除（405）时改为给标题打废弃前缀。
func TestForgetMemoryMarksDeprecated(t *testing.T) {
	var patched string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodGet:
			if r.URL.Path != "/memories/m1" || r.URL.Query().Get("space_id") != "mcode" {
				t.Errorf("GET = %s?%s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"id":"m1","title":"旧标题","content":"正文","importance":0.7}`)
		case http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patched, _ = body["title"].(string)
			if body["content"] != "正文" || body["importance"] != 0.7 {
				t.Errorf("内容与重要度不应改变: %v", body)
			}
			_, _ = io.WriteString(w, `{"id":"m1","title":"[已废弃] 旧标题","content":"正文"}`)
		default:
			t.Errorf("意外请求 %s %s", r.Method, r.URL.Path)
		}
	})

	deleted, err := New(srv.URL, testKey).ForgetMemory(context.Background(), "mcode", "m1")
	if err != nil {
		t.Fatalf("ForgetMemory 返回错误: %v", err)
	}
	if deleted {
		t.Fatal("服务端不支持删除时 deleted 应为 false")
	}
	if patched != DeprecatedTitlePrefix+" 旧标题" {
		t.Fatalf("PATCH 标题 = %q, 期望带废弃前缀", patched)
	}
}

// TestForgetMemoryKeepsExistingPrefix 验证标题已有废弃前缀时不再重复添加。
func TestForgetMemoryKeepsExistingPrefix(t *testing.T) {
	var patched string
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusNotImplemented)
		case http.MethodGet:
			_, _ = io.WriteString(w, `{"id":"m1","title":"[已废弃] 旧标题","content":"正文"}`)
		case http.MethodPatch:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			patched, _ = body["title"].(string)
			_, _ = io.WriteString(w, `{"id":"m1","title":"[已废弃] 旧标题"}`)
		}
	})

	deleted, err := New(srv.URL, testKey).ForgetMemory(context.Background(), "mcode", "m1")
	if err != nil {
		t.Fatalf("ForgetMemory 返回错误: %v", err)
	}
	if deleted {
		t.Fatal("服务端不支持删除时 deleted 应为 false")
	}
	if patched != "[已废弃] 旧标题" {
		t.Fatalf("PATCH 标题 = %q, 不应重复加前缀", patched)
	}
}

// TestForgetMemoryNotFound 验证删除与读取都返回 404 时返回错误。
func TestForgetMemoryNotFound(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	deleted, err := New(srv.URL, testKey).ForgetMemory(context.Background(), "mcode", "missing")
	if err == nil {
		t.Fatalf("GET 也返回 404 时应报错，实际 deleted=%v", deleted)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Fatalf("期望 404 的 *APIError，实际 %v", err)
	}
}

// TestForgetMemoryOtherError 验证其它状态码（如 500）直接返回，不改写标题。
func TestForgetMemoryOtherError(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("意外请求 %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusInternalServerError)
	})

	if _, err := New(srv.URL, testKey).ForgetMemory(context.Background(), "mcode", "m1"); err == nil {
		t.Fatal("500 时应返回错误")
	}
}

// TestListMemories 验证分页参数与两条记录的解析。
func TestListMemories(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/memories" {
			t.Errorf("请求 = %s %s", r.Method, r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("space_id") != "mcode" || q.Get("limit") != "100" || q.Get("offset") != "200" {
			t.Errorf("查询参数 = %s", r.URL.RawQuery)
		}
		_, _ = io.WriteString(w, `{"memories":[
			{"id":"m1","title":"第一条","content":"正文一","unit_type":"fact","importance":0.6,
			 "created_at":"2026-10-16T06:39:32+00:00","updated_at":"2026-10-20T08:00:00+00:00"},
			{"id":"m2","title":"[已废弃] 第二条","content":"正文二"}
		],"total":2}`)
	})

	mems, err := New(srv.URL, testKey).ListMemories(context.Background(), "mcode", 100, 200)
	if err != nil {
		t.Fatalf("ListMemories 返回错误: %v", err)
	}
	if len(mems) != 2 {
		t.Fatalf("结果数 = %d, 期望 2", len(mems))
	}
	if mems[0].ID != "m1" || mems[0].Title != "第一条" || mems[0].Importance != 0.6 || mems[0].UpdatedAt.IsZero() {
		t.Fatalf("第一条 = %+v", mems[0])
	}
	if mems[1].ID != "m2" || !IsDeprecated(mems[1]) {
		t.Fatalf("第二条应被识别为已废弃: %+v", mems[1])
	}
	if IsDeprecated(mems[0]) {
		t.Fatal("第一条不应被识别为已废弃")
	}
}

// TestListMemoriesBareArray 验证服务端返回裸数组时也能解析。
func TestListMemoriesBareArray(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"id":"m1","title":"T"}]`)
	})

	mems, err := New(srv.URL, testKey).ListMemories(context.Background(), "", 0, 0)
	if err != nil {
		t.Fatalf("ListMemories 返回错误: %v", err)
	}
	if len(mems) != 1 || mems[0].ID != "m1" {
		t.Fatalf("结果 = %+v", mems)
	}
}
