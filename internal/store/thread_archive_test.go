package store

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// coldArchiveTestNow 是冷归档测试的固定时钟，7 天边界都由它推算，不依赖真实时间。
var coldArchiveTestNow = time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)

// caNewStore 打开一个临时库，建好一个项目。
func caNewStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "cold-archive.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.UpsertProject(context.Background(), Project{
		ID: "p1", Name: "P1", Path: "/p1", CreatedAt: coldArchiveTestNow, UpdatedAt: coldArchiveTestNow,
	}); err != nil {
		t.Fatalf("UpsertProject 失败: %v", err)
	}
	return s, "p1"
}

// caCreateThread 建一个会话；updatedAt 决定候选 SQL 的静默期判断。
func caCreateThread(t *testing.T, s *Store, projectID, id string, archived bool, updatedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if err := s.CreateThread(ctx, Thread{
		ID: id, ProjectID: projectID, Title: "会话 " + id, Provider: "openai",
		CreatedAt: updatedAt, UpdatedAt: updatedAt,
	}); err != nil {
		t.Fatalf("CreateThread %s 失败: %v", id, err)
	}
	if archived {
		if err := s.SetThreadArchived(ctx, id, true); err != nil {
			t.Fatalf("SetThreadArchived %s 失败: %v", id, err)
		}
	}
}

// caAppendMessage 追加一条消息；AppendMessage 会把 threads.updated_at 改成 createdAt。
func caAppendMessage(t *testing.T, s *Store, id, threadID, role, kind, text string, createdAt time.Time) Message {
	t.Helper()
	m, err := s.AppendMessage(context.Background(), Message{
		ID: id, ThreadID: threadID, Role: role, Kind: kind, Text: text,
		TurnIndex: 1, CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("AppendMessage %s 失败: %v", id, err)
	}
	return m
}

// caCount 直接查表计数，绕过读路径的归档解码。
func caCount(t *testing.T, s *Store, table, threadID string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+table+" WHERE thread_id = ?", threadID).Scan(&n); err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return n
}

// caListAll 用 ListMessages 分页读出全部消息（每页 pageSize 条）。
func caListAll(t *testing.T, s *Store, threadID string, pageSize int) []Message {
	t.Helper()
	ctx := context.Background()
	var all []Message
	var beforeSeq int64
	for {
		page, hasMore, err := s.ListMessages(ctx, threadID, beforeSeq, pageSize)
		if err != nil {
			t.Fatalf("ListMessages %s 失败: %v", threadID, err)
		}
		all = append(page, all...)
		if !hasMore {
			break
		}
		if len(page) == 0 {
			t.Fatalf("ListMessages %s 返回 hasMore 但空页", threadID)
		}
		beforeSeq = page[0].Seq
	}
	return all
}

// caListAllTolerant 用 ListMessagesTolerant 分页读出全部消息。
func caListAllTolerant(t *testing.T, s *Store, threadID string, pageSize int) []Message {
	t.Helper()
	ctx := context.Background()
	var all []Message
	var beforeSeq int64
	for {
		page, hasMore, badIDs, err := s.ListMessagesTolerant(ctx, threadID, beforeSeq, pageSize)
		if err != nil {
			t.Fatalf("ListMessagesTolerant %s 失败: %v", threadID, err)
		}
		if len(badIDs) != 0 {
			t.Fatalf("ListMessagesTolerant %s 返回坏行 %v", threadID, badIDs)
		}
		all = append(page, all...)
		if !hasMore {
			break
		}
		beforeSeq = page[0].Seq
	}
	return all
}

// caRequireSameMessages 逐字段比较两批消息。
func caRequireSameMessages(t *testing.T, what string, want, got []Message) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s 不一致:\nwant %#v\ngot  %#v", what, want, got)
	}
}

// caArchiveCfg 返回一份注入固定时钟的默认配置。
func caArchiveCfg() ColdArchiveConfig {
	return ColdArchiveConfig{Now: coldArchiveTestNow, BatchPause: time.Millisecond}
}

// caRunArchive 跑一轮冷归档并返回报告。
func caRunArchive(t *testing.T, s *Store) ColdArchiveReport {
	t.Helper()
	rep, err := s.RunColdArchive(context.Background(), caArchiveCfg())
	if err != nil {
		t.Fatalf("RunColdArchive 失败: %v", err)
	}
	return rep
}

// TestColdArchiveRoundTrip 验证归档后读路径逐字段一致，包括已压缩的 tool 行和旧 gzip 行。
func TestColdArchiveRoundTrip(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)

	caAppendMessage(t, s, "m1", "t1", "user", "message", "你好", old)
	caAppendMessage(t, s, "m2", "t1", "assistant", "message", "收到了", old.Add(time.Second))
	caAppendMessage(t, s, "m3", "t1", "tool", "tool", "tool output", old.Add(2*time.Second))
	longInput := strings.Repeat("工具输入", 200)
	if _, err := s.db.ExecContext(ctx, "UPDATE messages SET tool_input = ? WHERE id = ?", longInput, "m3"); err != nil {
		t.Fatalf("写长 tool_input 失败: %v", err)
	}
	caAppendMessage(t, s, "m4", "t1", "assistant", "thinking", strings.Repeat("思考", 200), old.Add(3*time.Second))
	caAppendMessage(t, s, "m5", "t1", "assistant", "message", "legacy gzip body", old.Add(4*time.Second))
	gz, err := gzipMessageContent("legacy gzip body")
	if err != nil {
		t.Fatalf("构造 gzip 数据失败: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE messages SET text = '', text_encoding = 'gzip', text_z = ? WHERE id = 'm5'`, gz); err != nil {
		t.Fatalf("写旧 gzip 行失败: %v", err)
	}

	// 先跑一次消息保留压缩，让 m3 变成真正的 zstd 行。
	_, _, compressedRows, err := s.compressMessageBatch(ctx, "t1", 0, BeeRetentionConfig{Now: coldArchiveTestNow}.withDefaults(), nil)
	if err != nil {
		t.Fatalf("compressMessageBatch 失败: %v", err)
	}
	if compressedRows < 2 {
		t.Fatalf("本批压缩行数 = %d，期望至少 2（m3 与 m4）", compressedRows)
	}
	var encoding string
	if err := s.db.QueryRowContext(ctx, "SELECT text_encoding FROM messages WHERE id = 'm3'").Scan(&encoding); err != nil {
		t.Fatalf("读 m3 编码失败: %v", err)
	}
	if encoding != messageEncodingZstd {
		t.Fatalf("m3 没有被压缩: encoding=%q", encoding)
	}

	wantAll := caListAll(t, s, "t1", 2)
	wantTolerant := caListAllTolerant(t, s, "t1", 2)
	wantPage, wantHasMore, err := s.ListMessages(ctx, "t1", 4, 2)
	if err != nil {
		t.Fatalf("归档前 ListMessages 失败: %v", err)
	}

	rep := caRunArchive(t, s)
	if rep.ThreadsArchived != 1 || rep.MessagesArchived != 5 {
		t.Fatalf("归档统计不符: %+v", rep)
	}
	if rep.PayloadBytes <= 0 || rep.RawBytes <= rep.PayloadBytes {
		t.Fatalf("归档字节统计不符: %+v", rep)
	}
	if got := caCount(t, s, "messages", "t1"); got != 0 {
		t.Fatalf("归档后 messages 仍有 %d 行", got)
	}
	if got := caCount(t, s, "thread_archives", "t1"); got != 1 {
		t.Fatalf("归档行数 = %d", got)
	}

	caRequireSameMessages(t, "归档后整页", wantAll, caListAll(t, s, "t1", 2))
	caRequireSameMessages(t, "归档后 tolerant", wantTolerant, caListAllTolerant(t, s, "t1", 2))
	gotPage, gotHasMore, err := s.ListMessages(ctx, "t1", 4, 2)
	if err != nil {
		t.Fatalf("归档后 ListMessages 失败: %v", err)
	}
	if gotHasMore != wantHasMore {
		t.Fatalf("beforeSeq 分页 hasMore = %v, want %v", gotHasMore, wantHasMore)
	}
	caRequireSameMessages(t, "归档后 beforeSeq 页", wantPage, gotPage)
}

// TestColdArchiveSkipsUnqualified 验证未归档、太新、有进行中轮次的会话都不会被归档。
func TestColdArchiveSkipsUnqualified(t *testing.T) {
	s, projectID := caNewStore(t)
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "not-archived", false, old)
	caAppendMessage(t, s, "n1", "not-archived", "user", "message", "hi", old)

	caCreateThread(t, s, projectID, "too-new", true, coldArchiveTestNow.Add(-6*24*time.Hour))
	caAppendMessage(t, s, "n2", "too-new", "user", "message", "hi", coldArchiveTestNow.Add(-6*24*time.Hour))

	caCreateThread(t, s, projectID, "running-turn", true, old)
	caAppendMessage(t, s, "n3", "running-turn", "user", "message", "hi", old)
	if err := s.CreateTurn(context.Background(), Turn{
		ID: "turn-running", ThreadID: "running-turn", TurnIndex: 1,
		Status: "running", StartedAt: old,
	}); err != nil {
		t.Fatalf("CreateTurn 失败: %v", err)
	}

	rep := caRunArchive(t, s)
	if rep.ThreadsArchived != 0 {
		t.Fatalf("不该归档: %+v", rep)
	}
	for _, id := range []string{"not-archived", "too-new", "running-turn"} {
		if got := caCount(t, s, "messages", id); got == 0 {
			t.Fatalf("%s 的消息被搬走了", id)
		}
		if got := caCount(t, s, "thread_archives", id); got != 0 {
			t.Fatalf("%s 出现归档行", id)
		}
	}
}

// TestColdArchiveAppendAfterArchive 验证冷会话追加消息时 seq 连续、无 UNIQUE 冲突。
func TestColdArchiveAppendAfterArchive(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	caAppendMessage(t, s, "m1", "t1", "user", "message", "a", old)
	caAppendMessage(t, s, "m2", "t1", "assistant", "message", "b", old)
	caAppendMessage(t, s, "m3", "t1", "assistant", "message", "c", old)
	caRunArchive(t, s)

	appended := caAppendMessage(t, s, "m4", "t1", "user", "message", "d", coldArchiveTestNow)
	if appended.Seq != 4 {
		t.Fatalf("追加消息 seq = %d, want 4", appended.Seq)
	}
	if got := caCount(t, s, "thread_archives", "t1"); got != 0 {
		t.Fatalf("回填后归档行仍在: %d", got)
	}
	if got := caCount(t, s, "messages", "t1"); got != 4 {
		t.Fatalf("回填后 messages = %d 行", got)
	}
	if _, hasMore, err := s.ListMessages(ctx, "t1", 0, 10); err != nil || hasMore {
		t.Fatalf("回填后 ListMessages 失败: %v hasMore=%v", err, hasMore)
	}
}

// TestColdArchiveUpdateOldMessage 验证冷会话可以更新旧消息并读回新内容。
func TestColdArchiveUpdateOldMessage(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	caAppendMessage(t, s, "m1", "t1", "user", "message", "old text", old)
	caAppendMessage(t, s, "m2", "t1", "assistant", "message", "keep", old)
	caRunArchive(t, s)

	msgs, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("ListMessages 失败: %v", err)
	}
	updated := msgs[0]
	updated.Text = "new text"
	if err := s.UpdateMessage(ctx, updated); err != nil {
		t.Fatalf("UpdateMessage 失败: %v", err)
	}
	got, _, err := s.ListMessages(ctx, "t1", 0, 10)
	if err != nil {
		t.Fatalf("更新后 ListMessages 失败: %v", err)
	}
	if len(got) != 2 || got[0].Text != "new text" {
		t.Fatalf("更新后内容不符: %#v", got)
	}
}

// TestColdArchiveRecall 验证 RecallTurn 与 RecallMessage 在冷会话上生效且不会复活消息。
func TestColdArchiveRecall(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	if err := s.CreateTurn(ctx, Turn{
		ID: "turn-1", ThreadID: "t1", TurnIndex: 1, Status: "completed", StartedAt: old,
	}); err != nil {
		t.Fatalf("CreateTurn 失败: %v", err)
	}
	if _, err := s.AppendMessage(ctx, Message{
		ID: "m1", ThreadID: "t1", Role: "user", Kind: "message", Text: "撤回我",
		TurnIndex: 1, TurnID: "turn-1", CreatedAt: old,
	}); err != nil {
		t.Fatalf("AppendMessage 失败: %v", err)
	}
	if _, err := s.AppendMessage(ctx, Message{
		ID: "m2", ThreadID: "t1", Role: "assistant", Kind: "message", Text: "回复",
		TurnIndex: 1, TurnID: "turn-1", CreatedAt: old.Add(time.Second),
	}); err != nil {
		t.Fatalf("AppendMessage 失败: %v", err)
	}
	caRunArchive(t, s)

	if err := s.RecallTurn(ctx, "t1", "turn-1"); err != nil {
		t.Fatalf("RecallTurn 失败: %v", err)
	}
	for _, id := range []string{"m1", "m2"} {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM messages WHERE id = ?", id).Scan(&n); err != nil {
			t.Fatalf("查 %s 失败: %v", id, err)
		}
		if n != 0 {
			t.Fatalf("被撤回的消息 %s 仍在 messages", id)
		}
	}
	// 再 hydrate 一次也不能把撤回的消息复活。
	if err := s.HydrateThread(ctx, "t1"); err != nil {
		t.Fatalf("HydrateThread 失败: %v", err)
	}
	if got := caCount(t, s, "messages", "t1"); got != 0 {
		t.Fatalf("hydrate 复活了 %d 条消息", got)
	}

	// RecallMessage：冷会话最后一条 user 消息可以被撤回。
	caCreateThread(t, s, projectID, "t2", true, old)
	caAppendMessage(t, s, "m3", "t2", "user", "message", "撤回提示词", old)
	caRunArchive(t, s)
	if err := s.RecallMessage(ctx, "t2", "m3", "", 1); err != nil {
		t.Fatalf("RecallMessage 失败: %v", err)
	}
	if got := caCount(t, s, "messages", "t2"); got != 0 {
		t.Fatalf("撤回后 messages = %d 行", got)
	}
}

// TestColdArchiveUnarchiveHydrates 验证取消归档会在同事务内回填消息。
func TestColdArchiveUnarchiveHydrates(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	caAppendMessage(t, s, "m1", "t1", "user", "message", "a", old)
	caAppendMessage(t, s, "m2", "t1", "assistant", "message", "b", old)
	want := caListAll(t, s, "t1", 10)
	caRunArchive(t, s)

	if err := s.SetThreadArchived(ctx, "t1", false); err != nil {
		t.Fatalf("SetThreadArchived 失败: %v", err)
	}
	if got := caCount(t, s, "thread_archives", "t1"); got != 0 {
		t.Fatalf("归档行仍在: %d", got)
	}
	caRequireSameMessages(t, "取消归档后消息", want, caListAll(t, s, "t1", 10))
}

// TestColdArchiveDeleteThread 验证删除会话后没有孤儿归档行。
func TestColdArchiveDeleteThread(t *testing.T) {
	s, projectID := caNewStore(t)
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	caAppendMessage(t, s, "m1", "t1", "user", "message", "a", old)
	caRunArchive(t, s)

	if err := s.DeleteThread(context.Background(), "t1"); err != nil {
		t.Fatalf("DeleteThread 失败: %v", err)
	}
	if got := caCount(t, s, "thread_archives", "t1"); got != 0 {
		t.Fatalf("删除会话后仍有 %d 条归档", got)
	}
}

// TestColdArchiveIdempotent 验证 HydrateThread 与 RunColdArchive 重复执行都是幂等的。
func TestColdArchiveIdempotent(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	caAppendMessage(t, s, "m1", "t1", "user", "message", "a", old)
	caAppendMessage(t, s, "m2", "t1", "assistant", "message", "b", old)

	if rep := caRunArchive(t, s); rep.ThreadsArchived != 1 {
		t.Fatalf("首轮归档统计不符: %+v", rep)
	}
	if rep := caRunArchive(t, s); rep.ThreadsArchived != 0 {
		t.Fatalf("重复归档不应再搬消息: %+v", rep)
	}
	if got := caCount(t, s, "thread_archives", "t1"); got != 1 {
		t.Fatalf("归档行数 = %d", got)
	}

	want := caListAll(t, s, "t1", 10)
	if err := s.HydrateThread(ctx, "t1"); err != nil {
		t.Fatalf("首次 HydrateThread 失败: %v", err)
	}
	if err := s.HydrateThread(ctx, "t1"); err != nil {
		t.Fatalf("重复 HydrateThread 失败: %v", err)
	}
	if got := caCount(t, s, "messages", "t1"); got != len(want) {
		t.Fatalf("重复 hydrate 后 messages = %d 行", got)
	}
	caRequireSameMessages(t, "hydrate 后消息", want, caListAll(t, s, "t1", 10))
}

// TestColdArchiveThreadMessagesPage 验证 ThreadMessagesPage 在冷会话上的 total 与页内容不变。
func TestColdArchiveThreadMessagesPage(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	caAppendMessage(t, s, "m1", "t1", "user", "message", "q1", old)
	caAppendMessage(t, s, "m2", "t1", "assistant", "message", "a1", old.Add(time.Second))
	caAppendMessage(t, s, "m3", "t1", "assistant", "thinking", "思考", old.Add(2*time.Second))
	caAppendMessage(t, s, "m4", "t1", "tool", "tool", "工具输出", old.Add(3*time.Second))
	caAppendMessage(t, s, "m5", "t1", "user", "message", "q2", old.Add(4*time.Second))
	caAppendMessage(t, s, "m6", "t1", "assistant", "message", "a2", old.Add(5*time.Second))

	projectPaths := []string{"/p1"}
	wantTitle, wantTotal, wantPage, err := s.ThreadMessagesPage(ctx, projectPaths, "t1", 1, 2)
	if err != nil {
		t.Fatalf("归档前 ThreadMessagesPage 失败: %v", err)
	}
	if wantTotal != 4 {
		t.Fatalf("归档前 total = %d, want 4", wantTotal)
	}
	caRunArchive(t, s)

	gotTitle, gotTotal, gotPage, err := s.ThreadMessagesPage(ctx, projectPaths, "t1", 1, 2)
	if err != nil {
		t.Fatalf("归档后 ThreadMessagesPage 失败: %v", err)
	}
	if gotTitle != wantTitle || gotTotal != wantTotal {
		t.Fatalf("归档后 title/total 不符: %q/%d vs %q/%d", gotTitle, gotTotal, wantTitle, wantTotal)
	}
	caRequireSameMessages(t, "归档后 ThreadMessagesPage 页", wantPage, gotPage)
}

// TestColdArchiveCorruptPayload 验证 payload 被篡改时读路径返回带会话 ID 的错误。
func TestColdArchiveCorruptPayload(t *testing.T) {
	s, projectID := caNewStore(t)
	ctx := context.Background()
	old := coldArchiveTestNow.Add(-8 * 24 * time.Hour)
	caCreateThread(t, s, projectID, "t1", true, old)
	caAppendMessage(t, s, "m1", "t1", "user", "message", "a", old)
	caRunArchive(t, s)

	if _, err := s.db.ExecContext(ctx, "UPDATE thread_archives SET payload = x'00010203' WHERE thread_id = 't1'"); err != nil {
		t.Fatalf("篡改 payload 失败: %v", err)
	}
	if _, _, err := s.ListMessages(ctx, "t1", 0, 10); err == nil || !strings.Contains(err.Error(), "t1") {
		t.Fatalf("ListMessages 应返回带会话 ID 的错误，实际: %v", err)
	}
	if _, _, _, err := s.ListMessagesTolerant(ctx, "t1", 0, 10); err == nil || !strings.Contains(err.Error(), "t1") {
		t.Fatalf("ListMessagesTolerant 应返回带会话 ID 的错误，实际: %v", err)
	}
	if _, _, _, err := s.ThreadMessagesPage(ctx, []string{"/p1"}, "t1", 0, 10); err == nil || !strings.Contains(err.Error(), "t1") {
		t.Fatalf("ThreadMessagesPage 应返回带会话 ID 的错误，实际: %v", err)
	}
}
