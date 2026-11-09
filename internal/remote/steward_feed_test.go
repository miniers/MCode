package remote

import (
	"context"
	"path/filepath"
	"testing"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

type testStewardDesk struct{ id, text string }

func (d *testStewardDesk) IsFeed(id string) bool                     { return id == d.id }
func (d *testStewardDesk) Enqueue(_ string, text string, _ []string) { d.text = text }

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
	if err = b.AppendStewardMessage(ctx, id, "system", "steward_result", "任务\n完成"); err != nil { t.Fatal(err) }
	thread, err = db.GetThread(ctx, id)
	if err != nil || thread.HasUnread { t.Fatalf("系统结果不应置未读: %+v %v", thread, err) }
	if err = b.AppendStewardMessage(ctx, id, "system", "other", "提醒"); err != nil { t.Fatal(err) }
	thread, err = db.GetThread(ctx, id)
	if err != nil || !thread.HasUnread { t.Fatalf("其他系统消息应置未读: %+v %v", thread, err) }
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
