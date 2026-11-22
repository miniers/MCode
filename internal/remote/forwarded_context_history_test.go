package remote

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// TestThreadHistoryIncludesForwardedContext 验证转发后的新会话在 threads.history 到会话开头（!hasMore）时
// 返回 forwardedContext，普通会话不返回，且超过一页时仅在翻到首页才返回。
func TestThreadHistoryIncludesForwardedContext(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()

	projectID := newUUID()
	sourceID := newUUID()
	title := "原会话排查记录"
	b.AddProject(wire.ProjectSummary{
		ID:   projectID,
		Name: "demo",
		Path: t.TempDir(),
	})
	b.AddThread(wire.ThreadSummary{
		ID:        sourceID,
		ProjectID: projectID,
		Title:     title,
	}, []wire.ThreadMessage{
		{ID: newUUID(), Role: "user", Text: "帮我检查上下文为什么太长"},
		{ID: newUUID(), Role: "assistant", Text: "可以把结论转发到新会话。"},
	})

	// 1. 普通非转发会话不应返回 forwardedContext。
	srcHistory, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: sourceID})
	if err != nil {
		t.Fatalf("ThreadHistory(source) failed: %v", err)
	}
	if srcHistory.ForwardedContext != nil {
		t.Fatalf("普通会话不应返回 forwardedContext，实际 %+v", srcHistory.ForwardedContext)
	}
	rawSrc, err := json.Marshal(srcHistory)
	if err != nil {
		t.Fatalf("marshal srcHistory: %v", err)
	}
	if strings.Contains(string(rawSrc), "forwardedContext") {
		t.Fatalf("普通会话 JSON 不应序列化 forwardedContext 字段: %s", rawSrc)
	}

	// 2. 转发到新会话：刚创建且暂无新消息时，threads.history 应直接返回上个会话的内容。
	fwd, err := b.ForwardThread(ctx, wire.ThreadsContinueParams{
		ThreadID: sourceID,
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("ForwardThread failed: %v", err)
	}

	page, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: fwd.ThreadID})
	if err != nil {
		t.Fatalf("ThreadHistory(forwarded) failed: %v", err)
	}
	if page.HasMore {
		t.Fatalf("空转发会话 hasMore 应为 false")
	}
	if page.ForwardedContext == nil {
		t.Fatalf("转发会话到达会话开头时应返回 forwardedContext")
	}
	if page.ForwardedContext.SourceTitle != title {
		t.Fatalf("sourceTitle = %q, want %q", page.ForwardedContext.SourceTitle, title)
	}
	if !strings.Contains(page.ForwardedContext.Content, "帮我检查上下文为什么太长") ||
		!strings.Contains(page.ForwardedContext.Content, "可以把结论转发到新会话。") {
		t.Fatalf("forwardedContext.content 缺少原会话正文: %q", page.ForwardedContext.Content)
	}

	// 3. 新会话有超过 1 页消息时：最新页（hasMore=true）不带 forwardedContext，翻到会话开头（hasMore=false）才带上。
	b.mu.Lock()
	b.threads[fwd.ThreadID].messages = []wire.ThreadMessage{
		{ID: newUUID(), Role: "user", Text: "新会话第 1 条"},
		{ID: newUUID(), Role: "assistant", Text: "新会话第 2 条"},
		{ID: newUUID(), Role: "user", Text: "新会话第 3 条"},
	}
	b.mu.Unlock()

	limit := 2
	latestPage, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: fwd.ThreadID, Limit: &limit})
	if err != nil {
		t.Fatalf("ThreadHistory(latest page) failed: %v", err)
	}
	if !latestPage.HasMore || latestPage.NextCursor == nil {
		t.Fatalf("应存在更早页，hasMore=%v nextCursor=%v", latestPage.HasMore, latestPage.NextCursor)
	}
	if latestPage.ForwardedContext != nil {
		t.Fatalf("尚未翻到会话开头时不应返回 forwardedContext")
	}

	firstPage, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: fwd.ThreadID, Cursor: latestPage.NextCursor, Limit: &limit})
	if err != nil {
		t.Fatalf("ThreadHistory(first page) failed: %v", err)
	}
	if firstPage.HasMore {
		t.Fatalf("翻到最早页后 hasMore 应为 false")
	}
	if firstPage.ForwardedContext == nil || firstPage.ForwardedContext.SourceTitle != title {
		t.Fatalf("翻到最早页后应返回 forwardedContext，实际 %+v", firstPage.ForwardedContext)
	}
}

// TestBuiltinSlashCatalogIncludesForward 验证 /forward 已纳入内置斜杠命令目录。
func TestBuiltinSlashCatalogIncludesForward(t *testing.T) {
	cmds := builtinCommandsForProvider("claude")
	found := false
	for _, cmd := range cmds {
		if cmd.Name == "forward" && cmd.IsBuiltIn && cmd.Kind == "builtin" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("builtinCommandsForProvider 应包含 forward 命令，实际 %+v", cmds)
	}
}
