package store

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newMessageSearchTestStore 打开一个临时库，建好一个项目和一个会话，返回库句柄与项目 ID。
func newMessageSearchTestStore(t *testing.T, dbName string) (*Store, string) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), dbName))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, Project{ID: "p1", Name: "P1", Path: "/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	return s, "p1"
}

// appendSearchMessage 追加一条消息，失败即终止测试。
func appendSearchMessage(t *testing.T, s *Store, id, threadID, role, kind, text string, at time.Time) {
	t.Helper()
	if _, err := s.AppendMessage(context.Background(), Message{
		ID:        id,
		ThreadID:  threadID,
		Role:      role,
		Kind:      kind,
		Text:      text,
		TurnIndex: 1,
		CreatedAt: at,
	}); err != nil {
		t.Fatalf("AppendMessage %s 失败: %v", id, err)
	}
}

// 测试命中：正文包含关键字的消息被搜到，标题、项目、角色、片段都带回。
func TestSearchMessagesHit(t *testing.T) {
	ctx := context.Background()
	s, projectID := newMessageSearchTestStore(t, "message_search_hit.db")
	now := time.Now().UTC()
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: projectID, Title: "会话标题一", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	appendSearchMessage(t, s, "m1", "t1", "user", "text", "这里提到了颜色混合的问题", now)
	appendSearchMessage(t, s, "m2", "t1", "assistant", "text", "完全无关的内容", now.Add(time.Second))

	hits, err := s.SearchMessages(ctx, "颜色混合", 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("期望 1 条命中，实际 %d 条", len(hits))
	}
	hit := hits[0]
	if hit.MessageID != "m1" {
		t.Fatalf("期望命中 m1，实际 %q", hit.MessageID)
	}
	if hit.ThreadID != "t1" || hit.ThreadTitle != "会话标题一" || hit.ProjectID != projectID {
		t.Fatalf("命中会话信息不对: %+v", hit)
	}
	if hit.Role != "user" {
		t.Fatalf("期望角色为 user，实际 %q", hit.Role)
	}
	if hit.Seq != 1 {
		t.Fatalf("期望 seq 为 1，实际 %d", hit.Seq)
	}
	if !strings.Contains(hit.Snippet, "颜色混合") {
		t.Fatalf("片断未包含关键字: %q", hit.Snippet)
	}
	if hit.CreatedAt.IsZero() {
		t.Fatal("期望 CreatedAt 已被解析")
	}
}

// 测试大小写不敏感（ASCII）与片段里的换行处理。
func TestSearchMessagesCaseInsensitive(t *testing.T) {
	ctx := context.Background()
	s, projectID := newMessageSearchTestStore(t, "message_search_case.db")
	now := time.Now().UTC()
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: projectID, Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	appendSearchMessage(t, s, "m1", "t1", "assistant", "text", "先说明要点\n再讲 Prompt Engineering 细节", now)

	hits, err := s.SearchMessages(ctx, "prompt engineering", 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("期望 1 条命中，实际 %d 条", len(hits))
	}
	if strings.Contains(hits[0].Snippet, "\n") {
		t.Fatalf("片断不应包含换行: %q", hits[0].Snippet)
	}
	if !strings.Contains(strings.ToLower(hits[0].Snippet), "prompt engineering") {
		t.Fatalf("片断未包含关键字: %q", hits[0].Snippet)
	}
}

// 测试通配符转义：查询里的 % 与 _ 按字面匹配，不能命中别的消息。
func TestSearchMessagesEscapesWildcards(t *testing.T) {
	ctx := context.Background()
	s, projectID := newMessageSearchTestStore(t, "message_search_escape.db")
	now := time.Now().UTC()
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: projectID, Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	appendSearchMessage(t, s, "m-literal-percent", "t1", "user", "text", "完成度 50% 就够了", now)
	appendSearchMessage(t, s, "m-plain", "t1", "user", "text", "完成度 5012 就够了", now.Add(time.Second))
	appendSearchMessage(t, s, "m-literal-underscore", "t1", "user", "text", "变量名是 a_b", now.Add(2*time.Second))
	appendSearchMessage(t, s, "m-plain-underscore", "t1", "user", "text", "变量名是 axb", now.Add(3*time.Second))

	percentHits, err := s.SearchMessages(ctx, "50%", 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(percentHits) != 1 || percentHits[0].MessageID != "m-literal-percent" {
		t.Fatalf("%% 未被按字面匹配，实际命中: %+v", percentHits)
	}

	underscoreHits, err := s.SearchMessages(ctx, "a_b", 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(underscoreHits) != 1 || underscoreHits[0].MessageID != "m-literal-underscore" {
		t.Fatalf("_ 未被按字面匹配，实际命中: %+v", underscoreHits)
	}

	// 反斜杠自身也要按字面匹配。
	appendSearchMessage(t, s, "m-backslash", "t1", "user", "text", `路径 C:\tmp 而已`, now.Add(4*time.Second))
	backslashHits, err := s.SearchMessages(ctx, `C:\tmp`, 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(backslashHits) != 1 || backslashHits[0].MessageID != "m-backslash" {
		t.Fatalf("反斜杠未被按字面匹配，实际命中: %+v", backslashHits)
	}
}

// 测试排除工具输出与思考块，也排除非用户/助手角色的消息。
func TestSearchMessagesExcludesToolAndReasoning(t *testing.T) {
	ctx := context.Background()
	s, projectID := newMessageSearchTestStore(t, "message_search_kinds.db")
	now := time.Now().UTC()
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: projectID, Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	appendSearchMessage(t, s, "m-user", "t1", "user", "text", "关键片段在这里", now)
	appendSearchMessage(t, s, "m-thinking", "t1", "assistant", "thinking", "关键片段在这里（思考）", now.Add(time.Second))
	appendSearchMessage(t, s, "m-reasoning", "t1", "assistant", "reasoning", "关键片段在这里（推理）", now.Add(2*time.Second))
	appendSearchMessage(t, s, "m-tool", "t1", "tool", "tool", "关键片段在这里（工具输出）", now.Add(3*time.Second))
	appendSearchMessage(t, s, "m-error", "t1", "assistant", "error", "关键片段在这里（错误）", now.Add(4*time.Second))

	hits, err := s.SearchMessages(ctx, "关键片段", 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(hits) != 1 || hits[0].MessageID != "m-user" {
		t.Fatalf("期望只命中 m-user，实际: %+v", hits)
	}
}

// 测试已归档会话不参与搜索。
func TestSearchMessagesExcludesArchivedThread(t *testing.T) {
	ctx := context.Background()
	s, projectID := newMessageSearchTestStore(t, "message_search_archived.db")
	now := time.Now().UTC()
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: projectID, Title: "活跃会话", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	if err := s.CreateThread(ctx, Thread{ID: "t2", ProjectID: projectID, Title: "归档会话", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	appendSearchMessage(t, s, "m-active", "t1", "user", "text", "共享关键字 alpha", now)
	appendSearchMessage(t, s, "m-archived", "t2", "user", "text", "共享关键字 alpha", now.Add(time.Second))
	if err := s.SetThreadArchived(ctx, "t2", true); err != nil {
		t.Fatalf("SetThreadArchived 失败: %v", err)
	}

	hits, err := s.SearchMessages(ctx, "共享关键字", 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(hits) != 1 || hits[0].MessageID != "m-active" {
		t.Fatalf("期望只命中活跃会话的消息，实际: %+v", hits)
	}
}

// 测试 limit：缺省与夹取，以及按 created_at 倒序返回。
func TestSearchMessagesLimit(t *testing.T) {
	ctx := context.Background()
	s, projectID := newMessageSearchTestStore(t, "message_search_limit.db")
	now := time.Now().UTC()
	if err := s.CreateThread(ctx, Thread{ID: "t1", ProjectID: projectID, Title: "T1", Provider: "openai", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	for i := 0; i < 5; i++ {
		appendSearchMessage(t, s, fmt.Sprintf("m%d", i), "t1", "user", "text", fmt.Sprintf("重复关键字 %d", i), now.Add(time.Duration(i)*time.Second))
	}

	all, err := s.SearchMessages(ctx, "重复关键字", 3)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("期望 3 条，实际 %d 条", len(all))
	}
	// 最新的排最前：m4、m3、m2
	if all[0].MessageID != "m4" || all[2].MessageID != "m2" {
		t.Fatalf("期望按时间倒序，实际: %s / %s", all[0].MessageID, all[2].MessageID)
	}

	// limit <= 0 走缺省 30，5 条全覆盖。
	def, err := s.SearchMessages(ctx, "重复关键字", 0)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(def) != 5 {
		t.Fatalf("期望缺省 limit 返回 5 条，实际 %d 条", len(def))
	}

	// limit 超过上限时夹取到 100（这里只有 5 条，主要验证不报错）。
	capped, err := s.SearchMessages(ctx, "重复关键字", 500)
	if err != nil {
		t.Fatalf("SearchMessages 失败: %v", err)
	}
	if len(capped) != 5 {
		t.Fatalf("期望 5 条，实际 %d 条", len(capped))
	}

	// 空查询返回 nil。
	if empty, err := s.SearchMessages(ctx, "   ", 0); err != nil || empty != nil {
		t.Fatalf("空查询应返回 nil, nil，实际 %v, %v", empty, err)
	}
}

// 测试片段截断：命中位置前后按 rune 截取，两端被截断时补省略号。
func TestMessageSearchSnippet(t *testing.T) {
	long := strings.Repeat("前", 50) + "目标" + strings.Repeat("后", 100)
	snippet := messageSearchSnippet(long, "目标")
	if !strings.HasPrefix(snippet, "…") {
		t.Fatalf("前段被截断应补省略号: %q", snippet)
	}
	if !strings.HasSuffix(snippet, "…") {
		t.Fatalf("后段被截断应补省略号: %q", snippet)
	}
	if !strings.Contains(snippet, "目标") {
		t.Fatalf("片段应包含命中内容: %q", snippet)
	}
	// 省略号之外是命中前 20 与命中后 60 个 rune。
	body := []rune(strings.Trim(snippet, "…"))
	if len(body) != 20+2+60 {
		t.Fatalf("期望片段主体 %d 个 rune，实际 %d", 20+2+60, len(body))
	}

	// 短文本原样返回、不加省略号。
	if got := messageSearchSnippet("短文本命中", "命中"); got != "短文本命中" {
		t.Fatalf("短文本不应被截断，实际 %q", got)
	}
}
