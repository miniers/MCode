package remote

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// TestAttachStoreTolerantToCorruptRows 验证单个会话或单条消息解码失败不会让 AttachStore 整体失败：
// 坏会话写入 quarantined_threads，正常会话与坏会话中可读的消息照常加载。
func TestAttachStoreTolerantToCorruptRows(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcode.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	now := time.Now().UTC()
	if err := s1.UpsertProject(ctx, store.Project{ID: "p1", Name: "P1", Path: "/tmp/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	for _, id := range []string{"t-good", "t-bad-msg", "t-bad-fu", "t-bad-row"} {
		if err := s1.CreateThread(ctx, store.Thread{ID: id, ProjectID: "p1", Title: id, Provider: "memory", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatalf("create thread %s: %v", id, err)
		}
	}
	appendMsg := func(threadID, id string) {
		t.Helper()
		if _, err := s1.AppendMessage(ctx, store.Message{ID: id, ThreadID: threadID, Role: "user", Kind: "text", Text: id, TurnIndex: 1, CreatedAt: now}); err != nil {
			t.Fatalf("append message %s: %v", id, err)
		}
	}
	appendMsg("t-good", "m-good-1")
	appendMsg("t-good", "m-good-2")
	appendMsg("t-bad-msg", "m-bad-1")
	appendMsg("t-bad-msg", "m-bad-2")
	appendMsg("t-bad-fu", "m-fu-1")
	appendMsg("t-bad-row", "m-row-1")

	if err := s1.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// 用原生 SQL 制造坏数据：坏 media、坏 follow-up 行、坏线程行
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
		UPDATE messages SET media = '{bad json' WHERE id = 'm-bad-1';
		INSERT INTO thread_follow_ups (id, thread_id, position, text, attachments, created_at, updated_at, attachment_meta)
		VALUES ('fu-bad', 't-bad-fu', 0, 'x', '[]', '2026-10-18T00:00:00Z', '2026-10-18T00:00:00Z', '{bad json');
		UPDATE threads SET created_at = 'not-a-time' WHERE id = 't-bad-row';
	`); err != nil {
		t.Fatalf("corrupt rows: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if err := backend.AttachStore(ctx, s2); err != nil {
		t.Fatalf("AttachStore 不应因坏数据报错: %v", err)
	}

	// 正常会话消息齐全
	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "t-good"})
	if err != nil {
		t.Fatalf("thread history t-good: %v", err)
	}
	if len(hist.Messages) != 2 {
		t.Fatalf("期望 t-good 有 2 条消息，实际 %d: %+v", len(hist.Messages), hist.Messages)
	}

	// 只有一条消息坏掉的会话照常加载，坏消息的 media 降级为 nil
	histBad, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "t-bad-msg"})
	if err != nil {
		t.Fatalf("thread history t-bad-msg: %v", err)
	}
	if len(histBad.Messages) != 2 {
		t.Fatalf("期望 t-bad-msg 有 2 条消息，实际 %d: %+v", len(histBad.Messages), histBad.Messages)
	}

	// 线程行读不出的会话被跳过
	if _, err := backend.GetThread(ctx, "t-bad-row"); err == nil {
		t.Fatalf("期望 t-bad-row 被跳过，实际仍可读取")
	}

	// 三个损坏会话都进入隔离表
	quarantined, err := s2.ListQuarantinedThreads(ctx)
	if err != nil {
		t.Fatalf("list quarantined threads: %v", err)
	}
	byID := make(map[string]store.QuarantinedThread, len(quarantined))
	for _, q := range quarantined {
		byID[q.ThreadID] = q
	}
	if len(quarantined) != 3 {
		t.Fatalf("期望 3 条隔离记录，实际 %d: %+v", len(quarantined), quarantined)
	}
	if q, ok := byID["t-bad-msg"]; !ok || q.Reason != "message decode failed" || len(q.RawMessageIDs) != 1 || q.RawMessageIDs[0] != "m-bad-1" {
		t.Fatalf("t-bad-msg 隔离记录不符: %+v", byID["t-bad-msg"])
	}
	if q, ok := byID["t-bad-fu"]; !ok || q.Reason != "follow ups decode failed" {
		t.Fatalf("t-bad-fu 隔离记录不符: %+v", byID["t-bad-fu"])
	}
	if q, ok := byID["t-bad-row"]; !ok || q.Reason != "thread row decode failed" {
		t.Fatalf("t-bad-row 隔离记录不符: %+v", byID["t-bad-row"])
	}
}

// 含坏 media 消息的会话被淘汰后再回载，不能因解码失败而不可访问（C-ST-06 与 C-ST-07 的交互）。
func TestEnsureMessagesTolerantToCorruptMedia(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcode.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Now().UTC()
	if err := s1.UpsertProject(ctx, store.Project{ID: "p1", Name: "P1", Path: "/tmp/p1", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	if err := s1.CreateThread(ctx, store.Thread{ID: "t1", ProjectID: "p1", Title: "t1", Provider: "memory", Archived: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	for _, id := range []string{"m1", "m2"} {
		if _, err := s1.AppendMessage(ctx, store.Message{ID: id, ThreadID: "t1", Role: "user", Kind: "text", Text: id, TurnIndex: 1, CreatedAt: now}); err != nil {
			t.Fatalf("append message %s: %v", id, err)
		}
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE messages SET media = '{bad json' WHERE id = 'm1'`); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s2); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}

	backend.mu.Lock()
	td := backend.threads["t1"]
	td.messages = nil
	td.messagesEvicted = true
	backend.mu.Unlock()

	hist, err := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "t1"})
	if err != nil {
		t.Fatalf("回载含坏 media 的会话不应报错: %v", err)
	}
	if len(hist.Messages) != 2 {
		t.Fatalf("期望回载 2 条消息，实际 %d", len(hist.Messages))
	}
}
