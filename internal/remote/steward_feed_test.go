package remote

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type testStewardDesk struct{ id, messageID, text string }

func (d *testStewardDesk) IsFeed(id string) bool { return id == d.id }
func (d *testStewardDesk) Enqueue(_, messageID, text string, _ []string) {
	d.messageID, d.text = messageID, text
}

func TestStewardFeed(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err = b.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "项目", Path: t.TempDir()})
	id, err := b.EnsureStewardFeedThread(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	again, err := b.EnsureStewardFeedThread(ctx, "p")
	if err != nil || again != id {
		t.Fatalf("重复创建: %q %v", again, err)
	}
	desk := &testStewardDesk{id: id}
	b.SetStewardDesk(desk)
	result, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: id, Text: "/plan", ActionID: "action"})
	if err != nil {
		t.Fatal(err)
	}
	if result.MessageID == nil || desk.text != "/plan" {
		t.Fatalf("消息未送达: %+v %q", result, desk.text)
	}
	messages, _, err := db.ListMessages(ctx, id, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Text != "/plan" || messages[0].Role != "user" {
		t.Fatalf("消息未落库: %+v %v", messages, err)
	}
	if desk.messageID != messages[0].ID || desk.messageID != *result.MessageID {
		t.Fatalf("消息 ID 不一致: 接收 %q，落库 %q，返回 %q", desk.messageID, messages[0].ID, *result.MessageID)
	}
	if err = b.AppendStewardMessage(ctx, id, "assistant", "steward_reply", "收到"); err != nil {
		t.Fatal(err)
	}
	thread, err := db.GetThread(ctx, id)
	if err != nil || thread.HasUnread {
		t.Fatalf("回复不应置未读: %+v %v", thread, err)
	}
	if err = b.AppendStewardMessage(ctx, id, "assistant", "steward_result", "完成"); err != nil {
		t.Fatal(err)
	}
	if _, err = b.BulkMarkRead(ctx, "p", []string{id}); err != nil {
		t.Fatal(err)
	}
	summary, err := b.GetThread(ctx, id)
	if err != nil || summary.HasUnread {
		t.Fatalf("标记已读后内存摘要仍未读: %+v %v", summary, err)
	}
	thread, err = db.GetThread(ctx, id)
	if err != nil || thread.HasUnread {
		t.Fatalf("标记已读后存储仍未读: %+v %v", thread, err)
	}
	if err = b.AppendStewardMessage(ctx, id, "system", "steward_result", "任务\n完成"); err != nil {
		t.Fatal(err)
	}
	thread, err = db.GetThread(ctx, id)
	if err != nil || !thread.HasUnread {
		t.Fatalf("系统结果应置未读: %+v %v", thread, err)
	}
	if err = b.AppendStewardMessage(ctx, id, "system", "other", "提醒"); err != nil {
		t.Fatal(err)
	}
	thread, err = db.GetThread(ctx, id)
	if err != nil || !thread.HasUnread {
		t.Fatalf("其他系统消息应置未读: %+v %v", thread, err)
	}
	b.AddThread(wire.ThreadSummary{ID: "ordinary", ProjectID: "p", Provider: "memory"}, nil)
	if _, err = b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "ordinary", Text: "/plan"}); err != nil {
		t.Fatal(err)
	}
	if desk.text != "/plan" {
		t.Fatal("普通会话进入前台")
	}
	ordinary, err := b.GetThread(ctx, "ordinary")
	if err != nil || ordinary.InteractionMode != "plan" {
		t.Fatalf("普通会话未执行原有命令: %+v %v", ordinary, err)
	}
}

func TestResetStewardFeedThreadArchivesHistory(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "项目", Path: t.TempDir()})
	oldID, err := b.EnsureStewardFeedThread(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AppendStewardMessage(ctx, oldID, "user", "", "保留的历史"); err != nil {
		t.Fatal(err)
	}
	newID, err := b.ResetStewardFeedThread(ctx, "p")
	if err != nil || newID == "" || newID == oldID {
		t.Fatalf("重置结果错误: old=%q new=%q err=%v", oldID, newID, err)
	}
	old, err := db.GetThread(ctx, oldID)
	if err != nil {
		t.Fatal(err)
	}
	wantTitle := "总管 · 归档 " + time.Now().UTC().Format("2006-01-02")
	if old.Title != wantTitle || !old.Archived {
		t.Fatalf("旧会话未归档: title=%q archived=%v", old.Title, old.Archived)
	}
	messages, _, err := db.ListMessages(ctx, oldID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Text != "保留的历史" {
		t.Fatalf("旧消息未保留: %+v %v", messages, err)
	}
	stored, _, err := db.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil || stored != newID {
		t.Fatalf("新会话设置错误: %q %v", stored, err)
	}
}

func TestStewardConcurrentMarkRead(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "项目", Path: t.TempDir()})
	id, err := b.EnsureStewardFeedThread(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		done := make(chan error, 1)
		go func() { done <- b.AppendStewardMessage(ctx, id, "system", "other", "提醒") }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			summary, err := b.GetThread(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if summary.HasUnread {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("等待未读状态超时")
			}
			runtime.Gosched()
		}
		if _, err := b.BulkMarkRead(ctx, "p", []string{id}); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		summary, err := b.GetThread(ctx, id)
		if err != nil || summary.HasUnread {
			t.Fatalf("内存状态不为已读: %+v %v", summary, err)
		}
		thread, err := db.GetThread(ctx, id)
		if err != nil || thread.HasUnread {
			t.Fatalf("存储状态不为已读: %+v %v", thread, err)
		}
	}
}

func TestStewardUnreadInterleaved(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	if err := b.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "项目", Path: t.TempDir()})
	id, err := b.EnsureStewardFeedThread(ctx, "p")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		done := make(chan error, 2)
		go func() { done <- b.AppendStewardMessage(ctx, id, "system", "other", "提醒") }()
		go func() { _, err := b.BulkMarkRead(ctx, "p", []string{id}); done <- err }()
		for j := 0; j < 2; j++ {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
		memory, err := b.GetThread(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		stored, err := db.GetThread(ctx, id)
		if err != nil || stored.HasUnread != memory.HasUnread {
			t.Fatalf("未读状态不一致: 内存=%v 存储=%v 错误=%v", memory.HasUnread, stored.HasUnread, err)
		}
	}
}

func TestStewardMessageFailureRecordsAction(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	defer b.Close()
	ctx := context.Background()
	_, err := b.acceptStewardMessage(ctx, wire.SendMessageParams{ThreadID: "missing", ActionID: "action"}, &testStewardDesk{})
	if err == nil {
		t.Fatal("预期发送失败")
	}
	status, statusErr := b.ActionStatus(ctx, "action")
	if statusErr != nil || status.State != wire.ActionStateFailed || status.Error == nil || status.Error.Message != err.Error() {
		t.Fatalf("失败状态未记录: %+v %v", status, statusErr)
	}
}
