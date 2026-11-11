package remote

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/store"
)

// 非主宿主加载数据库不能收尾持锁进程的轮次、工具消息、审批或投递。
func TestAttachStoreSecondarySkipsBootReconcile(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcode.db")
	primary, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = primary.Close() })
	if !primary.IsPrimary() {
		t.Fatal("首次打开应取得主宿主锁")
	}
	now := time.Now().UTC()
	if err := primary.UpsertProject(ctx, store.Project{ID: "p1", Name: "项目", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := primary.CreateThread(ctx, store.Thread{ID: "t1", ProjectID: "p1", Title: "活跃会话", Provider: "memory", LastStatus: "running", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"running", "starting", "completed"} {
		turn := store.Turn{ID: status, ThreadID: "t1", TurnIndex: i, Status: status, StartedAt: now}
		if status == "completed" {
			turn.CompletedAt = &now
		}
		if err := primary.CreateTurn(ctx, turn); err != nil {
			t.Fatal(err)
		}
	}
	for _, msg := range []store.Message{
		{ID: "active-tool", TurnID: "running", ToolStatus: new("running")},
		{ID: "orphan-tool", TurnID: "completed", TurnIndex: 2, ToolStatus: new("running")},
		{ID: "background-tool", TurnID: "completed", TurnIndex: 2, ToolStatus: new("background")},
	} {
		msg.ThreadID, msg.Role, msg.Kind, msg.CreatedAt = "t1", "tool", "tool", now
		if _, err := primary.AppendMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := primary.InsertPendingAsk(ctx, store.PendingAsk{RequestID: "ask", ThreadID: "t1", TurnID: "running", Kind: string(harness.RequestKindCommand), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := primary.InsertDelivery(ctx, store.Delivery{MessageID: "delivery", ThreadID: "t1", FollowUpID: "follow-up", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	secondary, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = secondary.Close() })
	if secondary.IsPrimary() {
		t.Fatal("第二次打开应为非主宿主")
	}
	backend := newAttachedBackend(t, ctx, secondary)
	for _, status := range []string{"running", "starting"} {
		turn, err := primary.GetTurn(ctx, status)
		if err != nil {
			t.Fatal(err)
		}
		if turn.Status != status || turn.CompletedAt != nil {
			t.Fatalf("非主宿主不应收尾 %s 轮次: %+v", status, turn)
		}
	}
	for _, id := range []string{"active-tool", "orphan-tool", "background-tool"} {
		msg, err := primary.GetMessage(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		want := "running"
		if id == "background-tool" {
			want = "background"
		}
		if msg.ToolStatus == nil || *msg.ToolStatus != want || msg.ToolCompletedAt != nil {
			t.Fatalf("非主宿主不应收尾工具消息 %s: %+v", id, msg)
		}
	}
	if state, resolution := pendingAskRow(t, dbPath, "ask"); state != store.PendingAskPending || resolution != "" {
		t.Fatalf("非主宿主不应使审批过期: %s/%s", state, resolution)
	}
	if state := deliveryRowState(t, dbPath, "delivery"); state != store.DeliveryDispatching {
		t.Fatalf("非主宿主不应结算在途投递: %s", state)
	}
	if notices := infoNoticeTexts(t, backend, "t1"); len(notices) != 0 {
		t.Fatalf("非主宿主不应写入启动对账提示: %q", notices)
	}
	thread, err := primary.GetThread(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if thread.LastStatus != "running" {
		t.Fatalf("非主宿主不应改写会话最近状态: %s", thread.LastStatus)
	}
}
