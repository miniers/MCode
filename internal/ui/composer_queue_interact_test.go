package mcodeui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// fakeQueueFailClient 让 Send/MoveQueued 按需失败，其余行为沿用 FakeClient。
type fakeQueueFailClient struct {
	*FakeClient
	sendErr error
	moveErr error
}

func (f *fakeQueueFailClient) Send(ctx context.Context, threadID, text string) (bool, error) {
	f.mu.Lock()
	f.sendCalls = append(f.sendCalls, sentCall{ThreadID: threadID, Text: text})
	err := f.sendErr
	f.mu.Unlock()
	if err != nil {
		return false, err
	}
	return false, nil
}

func (f *fakeQueueFailClient) MoveQueued(ctx context.Context, threadID, followUpID, beforeFollowUpID string) error {
	f.mu.Lock()
	f.moveQueuedCalls = append(f.moveQueuedCalls, fakeQueueCall{
		ThreadID:         threadID,
		FollowUpID:       followUpID,
		BeforeFollowUpID: beforeFollowUpID,
	})
	err := f.moveErr
	f.mu.Unlock()
	return err
}

// newQueueTestState 造一个选中 t1、带排队消息且正在跑的 chatState。
func newQueueTestState(t *testing.T, client Client, provider string, items []QueuedFollowUp) *chatState {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	state := newChatState(client)
	t.Cleanup(state.close)
	state.mu.Lock()
	state.selectedThreadID = "t1"
	state.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "测试会话1", Provider: provider, IsRunning: true}}
	state.queue = map[string][]QueuedFollowUp{"t1": append([]QueuedFollowUp(nil), items...)}
	state.mu.Unlock()
	return state
}

// waitQueueCond 循环等待后台调用路径落实（这些路径都在 goroutine 里跑）。
func waitQueueCond(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待队列调用超时")
}

// queuedIDs 返回当前本地队列顺序的 ID 列表（避免与 fake_client_move_test.go 的 queueIDs 重名）。
func queuedIDs(s *chatState, threadID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.queue[threadID]
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// TestQueueSteerRemovesThenInjects 验证插话走「先移除队列项、再普通发送」，不打断当前轮。
func TestQueueSteerRemovesThenInjects(t *testing.T) {
	fake := NewFakeClient()
	state := newQueueTestState(t, fake, "codex", []QueuedFollowUp{{ID: "q1", Text: "补一句"}})

	state.steerQueued(QueuedFollowUp{ID: "q1", Text: "补一句"})

	waitQueueCond(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.removeQueuedCalls) == 1 && len(fake.sendCalls) == 1
	})
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if got := fake.removeQueuedCalls[0]; got.ThreadID != "t1" || got.FollowUpID != "q1" {
		t.Fatalf("RemoveQueued 参数应为 t1/q1，got %+v", got)
	}
	if got := fake.sendCalls[0]; got.ThreadID != "t1" || got.Text != "补一句" {
		t.Fatalf("Send 参数应为 t1/补一句，got %+v", got)
	}
}

// TestQueueSteerUnsupportedProviderKeepsItem 验证不支持插话的 provider 不做任何改动、原地留在队列并提示。
func TestQueueSteerUnsupportedProviderKeepsItem(t *testing.T) {
	fake := NewFakeClient()
	state := newQueueTestState(t, fake, "copilot", []QueuedFollowUp{{ID: "q1", Text: "补一句"}})

	state.steerQueued(QueuedFollowUp{ID: "q1", Text: "补一句"})

	fake.mu.Lock()
	removes, sends := len(fake.removeQueuedCalls), len(fake.sendCalls)
	fake.mu.Unlock()
	if removes != 0 || sends != 0 {
		t.Fatalf("不支持的 provider 不应改动队列，remove=%d send=%d", removes, sends)
	}
	if ids := queuedIDs(state, "t1"); len(ids) != 1 || ids[0] != "q1" {
		t.Fatalf("消息应原地留在队列，got %v", ids)
	}
	state.mu.Lock()
	toast := state.toastMessage
	state.mu.Unlock()
	if toast == "" {
		t.Fatal("应提示当前 Agent 不支持插入")
	}
}

// TestQueueSteerRequeuesOnSendFailure 验证发送失败时把文本重新入队，不丢内容。
func TestQueueSteerRequeuesOnSendFailure(t *testing.T) {
	base := NewFakeClient()
	fake := &fakeQueueFailClient{FakeClient: base, sendErr: errors.New("boom")}
	state := newQueueTestState(t, fake, "claude", []QueuedFollowUp{{ID: "q1", Text: "补一句"}})

	state.steerQueued(QueuedFollowUp{ID: "q1", Text: "补一句"})

	waitQueueCond(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.enqueueCalls) == 1
	})
	fake.mu.Lock()
	call := fake.enqueueCalls[0]
	fake.mu.Unlock()
	if call.ThreadID != "t1" || call.Text != "补一句" || len(call.AttachmentIDs) != 0 {
		t.Fatalf("回填应把原文重新入队，got %+v", call)
	}
	state.mu.Lock()
	toast := state.toastMessage
	state.mu.Unlock()
	if toast == "" {
		t.Fatal("回填后应提示插入失败")
	}
}

// TestQueueSteerHiddenForAttachmentItem 验证带附件的排队项不渲染「插入」按钮，无附件的才渲染。
func TestQueueSteerHiddenForAttachmentItem(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.TriggerRunningUpdate("t1", true)
	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "无附件排队项"},
		{ID: "q2", Text: "带附件排队项", AttachmentCount: 1},
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("排队 2")
	})
	if err := tt.Click("排队 2"); err != nil {
		t.Fatalf("展开排队面板失败: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("插入到当前轮次 无附件排队项")
	})
	if tt.HasText("插入到当前轮次 带附件排队项") {
		t.Fatal("带附件的排队项不应渲染插入按钮")
	}
}

// TestQueueMoveCallsClientWithTargetParams 验证拖拽结束回调用 MoveQueued 时参数正确、本地顺序即时更新。
func TestQueueMoveCallsClientWithTargetParams(t *testing.T) {
	fake := NewFakeClient()
	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "第一条"},
		{ID: "q2", Text: "第二条"},
		{ID: "q3", Text: "第三条"},
	})
	state := newQueueTestState(t, fake, "claude", []QueuedFollowUp{
		{ID: "q1", Text: "第一条"},
		{ID: "q2", Text: "第二条"},
		{ID: "q3", Text: "第三条"},
	})

	state.moveQueued("q3", "q1")

	if ids := queuedIDs(state, "t1"); len(ids) != 3 || ids[0] != "q3" || ids[1] != "q1" || ids[2] != "q2" {
		t.Fatalf("本地顺序应乐观重排为 q3,q1,q2，got %v", ids)
	}
	waitQueueCond(t, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.moveQueuedCalls) == 1
	})
	fake.mu.Lock()
	call := fake.moveQueuedCalls[0]
	fake.mu.Unlock()
	if call.ThreadID != "t1" || call.FollowUpID != "q3" || call.BeforeFollowUpID != "q1" {
		t.Fatalf("MoveQueued 参数应为 t1/q3/在 q1 之前，got %+v", call)
	}
}

// TestQueueMoveFailureReloadsQueue 验证后端拒绝重排时重新拉取队列，本地顺序回到后端状态。
func TestQueueMoveFailureReloadsQueue(t *testing.T) {
	base := NewFakeClient()
	original := []QueuedFollowUp{
		{ID: "q1", Text: "第一条"},
		{ID: "q2", Text: "第二条"},
	}
	base.setQueue("t1", original)
	fake := &fakeQueueFailClient{FakeClient: base, moveErr: errors.New("conflict")}
	state := newQueueTestState(t, fake, "claude", original)

	state.moveQueued("q2", "q1")
	if ids := queuedIDs(state, "t1"); len(ids) != 2 || ids[0] != "q2" {
		t.Fatalf("乐观重排应先生效，got %v", ids)
	}

	waitQueueCond(t, func() bool {
		ids := queuedIDs(state, "t1")
		return len(ids) == 2 && ids[0] == "q1" && ids[1] == "q2"
	})
	state.mu.Lock()
	toast := state.toastMessage
	state.mu.Unlock()
	if toast == "" {
		t.Fatal("重排失败应提示")
	}
}

// TestReorderQueueItems 验证重排纯函数：向后拖与向前拖都落到目标行之前，非法输入不改动。
func TestReorderQueueItems(t *testing.T) {
	items := []QueuedFollowUp{{ID: "q1"}, {ID: "q2"}, {ID: "q3"}}

	next, ok := reorderQueueItems(items, "q1", "q3")
	if !ok || next[0].ID != "q2" || next[1].ID != "q1" || next[2].ID != "q3" {
		t.Fatalf("q1 拖到 q3 之前应得到 q2,q1,q3，got %v ok=%v", next, ok)
	}

	next, ok = reorderQueueItems(items, "q3", "q1")
	if !ok || next[0].ID != "q3" || next[1].ID != "q1" || next[2].ID != "q2" {
		t.Fatalf("q3 拖到 q1 之前应得到 q3,q1,q2，got %v ok=%v", next, ok)
	}

	if _, ok := reorderQueueItems(items, "q1", "q1"); ok {
		t.Fatal("拖到自己身上不应触发重排")
	}
	if _, ok := reorderQueueItems(items, "q1", "missing"); ok {
		t.Fatal("目标不存在时不应触发重排")
	}
}
