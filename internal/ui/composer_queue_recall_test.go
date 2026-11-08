package mcodeui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// waitRecallAllQueued 通过包级钩子等待「全部退回编辑」的后台 goroutine 结束，
// 避免测试用 sleep 猜测异步完成时间。
func waitRecallAllQueued(t *testing.T, run func()) {
	t.Helper()
	prev := recallAllQueuedDoneHook
	done := make(chan struct{}, 1)
	recallAllQueuedDoneHook = func() {
		select {
		case done <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { recallAllQueuedDoneHook = prev })
	run()
	select {
	case <-done:
	case <-time.After(uiWaitTimeout):
		t.Fatal("等待「全部退回编辑」完成超时")
	}
}

// queueSeeder 让测试把排队项同步给 FakeClient 及其包装类型。
type queueSeeder interface {
	setQueue(threadID string, items []QueuedFollowUp)
}

// newRecallTestState 造一个选中 t1、队列已同步到 chatState 与 FakeClient 的实例。
func newRecallTestState(t *testing.T, client Client, items []QueuedFollowUp) *chatState {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s := newChatState(client)
	t.Cleanup(s.close)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	s.startQueueWatch()
	seeder, ok := client.(queueSeeder)
	if !ok {
		t.Fatalf("测试客户端不支持 setQueue: %T", client)
	}
	seeder.setQueue("t1", items)
	s.mu.Lock()
	got := len(s.queue["t1"])
	s.mu.Unlock()
	if got != len(items) {
		t.Fatalf("排队队列未同步: got %d, want %d", got, len(items))
	}
	return s
}

func recallTestDraft(s *chatState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draft
}

func recallTestToast(s *chatState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.toastMessage
}

func recallTestQueueIDs(s *chatState, tid string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.queue[tid]))
	for _, item := range s.queue[tid] {
		ids = append(ids, item.ID)
	}
	return ids
}

func recallTestRemoveIDs(fake *FakeClient) []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	ids := make([]string, 0, len(fake.removeQueuedCalls))
	for _, call := range fake.removeQueuedCalls {
		ids = append(ids, call.FollowUpID)
	}
	return ids
}

func recallTestFakeQueueIDs(fake *FakeClient, tid string) []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	ids := make([]string, 0, len(fake.queue[tid]))
	for _, item := range fake.queue[tid] {
		ids = append(ids, item.ID)
	}
	return ids
}

// recallFailClient 让指定排队项的 RemoveQueued 失败，其余行为沿用 FakeClient。
type recallFailClient struct {
	*FakeClient
	failID string
}

func (f *recallFailClient) RemoveQueued(ctx context.Context, threadID, followUpID string) error {
	if followUpID == f.failID {
		// 也要记录这次调用，断言失败发生在哪一条。
		f.mu.Lock()
		f.removeQueuedCalls = append(f.removeQueuedCalls, fakeQueueCall{ThreadID: threadID, FollowUpID: followUpID})
		f.mu.Unlock()
		return errors.New("模拟删除失败")
	}
	return f.FakeClient.RemoveQueued(ctx, threadID, followUpID)
}

func TestQueuedIntentLabel(t *testing.T) {
	cases := []struct {
		name string
		item QueuedFollowUp
		want string
	}{
		{"系统消息不显示标签", QueuedFollowUp{System: true}, ""},
		{"被降级的插入显示插入未生效", QueuedFollowUp{Intent: "insert"}, "插入未生效"},
		{"普通排队显示排队", QueuedFollowUp{}, "排队"},
		{"显式 queue 显示排队", QueuedFollowUp{Intent: "queue"}, "排队"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := queuedIntentLabel(tc.item); got != tc.want {
				t.Fatalf("queuedIntentLabel(%+v) = %q, want %q", tc.item, got, tc.want)
			}
		})
	}
}

func TestMergeQueuedDraftText(t *testing.T) {
	cases := []struct {
		name  string
		draft string
		texts []string
		want  string
	}{
		{"空草稿与空消息", "", nil, ""},
		{"纯空白草稿不产生空块", "   ", []string{"a", "b"}, "a\n\nb"},
		{"草稿行尾空白被裁掉", "hello\n", []string{"x", "y"}, "hello\n\nx\n\ny"},
		{"结果没有尾部空行", "hello\n\n", []string{"x\n"}, "hello\n\nx"},
		{"消息本身为空则跳过", "draft", []string{"", "x"}, "draft\n\nx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeQueuedDraftText(tc.draft, tc.texts); got != tc.want {
				t.Fatalf("mergeQueuedDraftText(%q, %q) = %q, want %q", tc.draft, tc.texts, got, tc.want)
			}
		})
	}
}

func TestQueuedRecallBlockedReason(t *testing.T) {
	if got := queuedRecallBlockedReason(nil, 0, true, config.DefaultComposerAttachmentLimit); got != "没有可退回的排队消息" {
		t.Fatalf("空队列原因 = %q", got)
	}
	if got := queuedRecallBlockedReason([]QueuedFollowUp{{ID: "s", System: true}}, 0, true, config.DefaultComposerAttachmentLimit); got != "没有可退回的排队消息" {
		t.Fatalf("只有系统消息原因 = %q", got)
	}
	if got := queuedRecallBlockedReason([]QueuedFollowUp{{ID: "q", Text: "x", AttachmentCount: 1}}, 0, false, config.DefaultComposerAttachmentLimit); got != "当前客户端不支持退回带附件的排队消息" {
		t.Fatalf("不支持附件原因 = %q", got)
	}
	if got := queuedRecallBlockedReason([]QueuedFollowUp{{ID: "q", Text: "x"}}, 0, false, config.DefaultComposerAttachmentLimit); got != "" {
		t.Fatalf("无附件的条目不应因客户端不支持附件被阻断，got %q", got)
	}
	// 上限跟随默认配置。
	over := []QueuedFollowUp{{ID: "q", Text: "x", AttachmentCount: config.DefaultComposerAttachmentLimit}}
	if got := queuedRecallBlockedReason(over, 1, true, config.DefaultComposerAttachmentLimit); got == "" {
		t.Fatal("超过附件上限时应给出原因")
	}
	if got := queuedRecallBlockedReason(over, 0, true, config.DefaultComposerAttachmentLimit); got != "" {
		t.Fatalf("恰好达到上限时应允许退回，got %q", got)
	}
}

func TestRecallAllQueued_RemovesInOrderAndMergesDraft(t *testing.T) {
	fake := NewFakeClient()
	s := newRecallTestState(t, fake, []QueuedFollowUp{
		{ID: "q1", Text: "a"},
		{ID: "q2", Text: "b"},
		{ID: "q3", Text: "c"},
	})

	waitRecallAllQueued(t, s.recallAllQueued)

	if got := recallTestDraft(s); got != "a\n\nb\n\nc" {
		t.Fatalf("草稿 = %q, want %q", got, "a\n\nb\n\nc")
	}
	if got := strings.Join(recallTestRemoveIDs(fake), ","); got != "q1,q2,q3" {
		t.Fatalf("删除顺序 = %q, want q1,q2,q3", got)
	}
	if ids := recallTestQueueIDs(s, "t1"); len(ids) != 0 {
		t.Fatalf("退回后队列应为空，got %v", ids)
	}
	if got := recallTestToast(s); got != "已退回 3 条排队消息" {
		t.Fatalf("toast = %q", got)
	}
}

func TestRecallAllQueued_AppendsToExistingDraft(t *testing.T) {
	fake := NewFakeClient()
	s := newRecallTestState(t, fake, []QueuedFollowUp{
		{ID: "q1", Text: "x"},
		{ID: "q2", Text: "y"},
	})
	s.mu.Lock()
	s.draft = "draft"
	s.mu.Unlock()

	waitRecallAllQueued(t, s.recallAllQueued)

	if got := recallTestDraft(s); got != "draft\n\nx\n\ny" {
		t.Fatalf("草稿 = %q, want %q", got, "draft\n\nx\n\ny")
	}
	s.mu.Lock()
	pending := s.pendingDraftFill
	s.mu.Unlock()
	if pending != nil {
		t.Fatalf("全部退回不应使用 pendingDraftFill，got %q", *pending)
	}
}

func TestRecallAllQueued_StopsOnRemoveFailure(t *testing.T) {
	fake := &recallFailClient{FakeClient: NewFakeClient(), failID: "q2"}
	s := newRecallTestState(t, fake, []QueuedFollowUp{
		{ID: "q1", Text: "第一条"},
		{ID: "q2", Text: "第二条"},
		{ID: "q3", Text: "第三条"},
	})

	waitRecallAllQueued(t, s.recallAllQueued)

	// 失败中止：只有第 1 条成功退回，草稿只累积已成功的条目。
	if got := recallTestDraft(s); got != "第一条" {
		t.Fatalf("草稿 = %q, want 第一条", got)
	}
	if got := strings.Join(recallTestRemoveIDs(fake.FakeClient), ","); got != "q1,q2" {
		t.Fatalf("删除调用 = %q, want q1,q2", got)
	}
	if got := strings.Join(recallTestFakeQueueIDs(fake.FakeClient, "t1"), ","); got != "q2,q3" {
		t.Fatalf("失败后队列应保留第 2、3 条，got %q", got)
	}
	if toast := recallTestToast(s); !strings.Contains(toast, "失败") {
		t.Fatalf("失败 toast = %q", toast)
	}
}

func TestRecallAllQueued_KeepsSystemItems(t *testing.T) {
	fake := NewFakeClient()
	s := newRecallTestState(t, fake, []QueuedFollowUp{
		{ID: "q1", Text: "普通一"},
		{ID: "q2", Text: "系统汇报", System: true},
		{ID: "q3", Text: "普通二"},
	})

	waitRecallAllQueued(t, s.recallAllQueued)

	if got := recallTestDraft(s); got != "普通一\n\n普通二" {
		t.Fatalf("草稿 = %q, want 普通一\\n\\n普通二", got)
	}
	if got := strings.Join(recallTestRemoveIDs(fake), ","); got != "q1,q3" {
		t.Fatalf("系统条目不应被删除，删除调用 = %q", got)
	}
	if ids := recallTestQueueIDs(s, "t1"); strings.Join(ids, ",") != "q2" {
		t.Fatalf("系统条目应留在队列，got %v", ids)
	}
	if got := recallTestToast(s); got != "已退回 2 条排队消息，系统消息已保留" {
		t.Fatalf("toast = %q", got)
	}
}

func TestRecallAllQueued_BlockedWhileEditing(t *testing.T) {
	fake := NewFakeClient()
	s := newRecallTestState(t, fake, []QueuedFollowUp{
		{ID: "q1", Text: "第一条"},
		{ID: "q2", Text: "第二条"},
	})
	s.beginEditQueued("q1")
	if got := s.editingQueuedID(); got != "q1" {
		t.Fatalf("进入编辑态失败: editingQueuedID = %q", got)
	}

	// 编辑态下应直接拒绝，不删除任何条目（同步返回，无需等待 goroutine）。
	s.recallAllQueued()

	if ids := recallTestRemoveIDs(fake); len(ids) != 0 {
		t.Fatalf("编辑态下不应删除排队项，got %v", ids)
	}
	if ids := recallTestQueueIDs(s, "t1"); len(ids) != 2 {
		t.Fatalf("编辑态下队列应保持不变，got %v", ids)
	}
	if toast := recallTestToast(s); !strings.Contains(toast, "编辑") {
		t.Fatalf("阻断 toast = %q", toast)
	}
}

func TestComposerQueue_RecallAllButton(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "第一条排队跟进"},
		{ID: "q2", Text: "第二条排队跟进"},
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("排队 2")
	})
	if err := tt.Click("排队 2"); err != nil {
		t.Fatalf("展开排队面板失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("全部退回编辑")
	})

	waitRecallAllQueued(t, func() {
		if err := tt.Click("全部退回编辑"); err != nil {
			t.Fatalf("点击全部退回编辑失败: %v", err)
		}
	})

	if got := recallTestDraft(s); got != "第一条排队跟进\n\n第二条排队跟进" {
		t.Fatalf("草稿 = %q", got)
	}
	if got := strings.Join(recallTestRemoveIDs(fake), ","); got != "q1,q2" {
		t.Fatalf("删除调用 = %q", got)
	}
	waitFor(t, tt, func() bool {
		return !tt.HasText("全部退回编辑")
	})
}

func TestComposerQueue_IntentLabels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "降级消息", Intent: "insert"},
		{ID: "q2", Text: "普通消息"},
		{ID: "q3", Text: "系统汇报", System: true},
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("排队 3")
	})
	if err := tt.Click("排队 3"); err != nil {
		t.Fatalf("展开排队面板失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("插入未生效")
	})

	texts := tt.Texts()
	if got := countExactText(texts, "插入未生效"); got != 1 {
		t.Fatalf("插入未生效标签出现 %d 次，want 1；texts: %q", got, texts)
	}
	if got := countExactText(texts, "排队"); got != 1 {
		t.Fatalf("排队标签出现 %d 次，want 1（系统项不应有标签）；texts: %q", got, texts)
	}
	if !tt.HasText("系统消息") {
		t.Fatalf("系统条目应显示系统消息标签；texts: %q", texts)
	}
}

func TestComposerQueue_RecallAllLabelUnique(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "第一条排队跟进"},
		{ID: "q2", Text: "第二条排队跟进"},
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("排队 2")
	})
	if err := tt.Click("排队 2"); err != nil {
		t.Fatalf("展开排队面板失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("全部退回编辑")
	})

	// 一个按钮会同时登记 a11y Label 与子 Text（各一次）；若被其它按钮复用，计数会继续增加。
	if got := countExactText(tt.Texts(), "全部退回编辑"); got != 2 {
		t.Fatalf("全部退回编辑出现 %d 次，want 2（Label + 可见文字）；texts: %q", got, tt.Texts())
	}
}
