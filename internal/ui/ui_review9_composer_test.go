package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestUIReview9EnterBlocksFailedAttachment(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.draft = "/fast"
	s.attachments = []composerAttachment{{
		LocalID:  "fail-1",
		Name:     "bad.png",
		MimeType: "image/png",
		Err:      "上传失败",
	}}

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.composerFocused
	})

	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()

	if fake.SendCalls() != 0 {
		t.Fatalf("失败附件时不应调用发送，got %d", fake.SendCalls())
	}
	fake.mu.Lock()
	enqueues := len(fake.enqueueCalls)
	fake.mu.Unlock()
	if enqueues != 0 {
		t.Fatalf("失败附件时不应入队，got %d", enqueues)
	}
	s.mu.Lock()
	draft := s.draft
	s.mu.Unlock()
	if draft != "/fast" {
		t.Fatalf("草稿应保持不变，got %q", draft)
	}
}

func TestUIReview9QueuedEditRespectsDisabled(t *testing.T) {
	testenv.TempHome(t)
	fake := &fakeQueueUpdaterClient{FakeClient: NewFakeClient()}
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}
	s.queue["t1"] = []QueuedFollowUp{{ID: "q1", Text: "修改后排队消息"}}
	s.beginEditQueued("q1")

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposer(c, s)
	}, 900, 500)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.composerFocused
	})

	s.mu.Lock()
	if s.composerPrefsSaving == nil {
		s.composerPrefsSaving = make(map[string]bool)
	}
	s.composerPrefsSaving["t1"] = true
	s.mu.Unlock()
	tt.Frame()

	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()

	fake.mu.Lock()
	callsWhileDisabled := len(fake.updateCalls)
	fake.mu.Unlock()
	if callsWhileDisabled != 0 {
		t.Fatalf("发送禁用时 Enter 不应保存排队编辑，got %d 次更新", callsWhileDisabled)
	}
	if got := s.editingQueuedID(); got != "q1" {
		t.Fatalf("发送禁用时应保持在编辑态 q1，got %q", got)
	}

	// 恢复可用后 Enter 应正常保存。
	s.mu.Lock()
	s.composerPrefsSaving["t1"] = false
	s.mu.Unlock()
	tt.Frame()

	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.updateCalls) == 1
	})
}

func TestUIReview9ModelPickerResetOnSwitch(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	resetComposerModelPickerForTest(t)

	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode", Path: t.TempDir()}},
		map[string][]Thread{
			"p1": {
				{ID: "t1", ProjectID: "p1", Title: "会话一", UpdatedAt: now},
				{ID: "t2", ProjectID: "p1", Title: "会话二", UpdatedAt: now},
			},
		},
	)
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("会话一") })

	waitFor(t, tt, func() bool { return tt.HasText("选择模型") })
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("打开模型弹层失败: %v", err)
	}
	tt.Frame()
	composerModelPickerState.searchQuery = "claude"
	composerModelPickerState.TierEditing = "fast"
	composerModelPickerState.setListOpen("t1", ModelPickerSideLead, true)
	if !composerModelPickerState.MenuOpen {
		t.Fatal("打开模型弹层后 MenuOpen 应为 true")
	}

	s.selectThread("t2")
	tt.Frame()

	if composerModelPickerState.MenuOpen {
		t.Fatal("切换会话后 MenuOpen 应重置为 false")
	}
	if composerModelPickerState.searchQuery != "" {
		t.Fatalf("切换会话后 searchQuery 应清空，got %q", composerModelPickerState.searchQuery)
	}
	if composerModelPickerState.TierEditing != "" {
		t.Fatalf("切换会话后 TierEditing 应清空，got %q", composerModelPickerState.TierEditing)
	}
	if len(composerModelPickerState.ListOpen) != 0 {
		t.Fatalf("切换会话后 ListOpen 应清空，got %v", composerModelPickerState.ListOpen)
	}
}

func TestUIReview9PlanPillCached(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", IsRunning: true}}
	s.messages = []Message{{
		ID:        "m1",
		Role:      "tool",
		ToolName:  "todo_write",
		ToolInput: `{"todos":[{"content":"第一项","status":"in_progress"},{"content":"第二项","status":"pending"}]}`,
	}}
	s.messagesRev = 1

	tt := ui.NewTester(func(c *ui.Context) {
		renderComposer(c, s)
	}, 900, 500)
	tt.Frame()

	before := planPillBuilds.Load()
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if got := planPillBuilds.Load(); got != before {
		t.Fatalf("messagesRev 未变化时构建计数不应增长: before=%d, got=%d", before, got)
	}

	s.mu.Lock()
	s.messages[0].ToolInput = `{"todos":[{"content":"第一项","status":"completed"},{"content":"第二项","status":"in_progress"}]}`
	s.messagesRev++
	s.mu.Unlock()
	tt.Frame()

	afterMsg := planPillBuilds.Load()
	if afterMsg <= before {
		t.Fatalf("messagesRev 增长后构建计数应增长: before=%d, after=%d", before, afterMsg)
	}
	if !tt.HasText("第二项") || !tt.HasText("1/2") {
		t.Fatalf("更新后的待办胶囊文案未刷新: %v", tt.Texts())
	}

	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if got := planPillBuilds.Load(); got != afterMsg {
		t.Fatalf("再次静止渲染时构建计数不应增长: want=%d, got=%d", afterMsg, got)
	}
}

func TestUIReview9TrimmedLabelsAndAttachmentTags(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	s.attachments = []composerAttachment{
		{LocalID: "code-1", ID: "att-1", Name: "main.go", MimeType: "text/plain"},
		{LocalID: "bin-1", ID: "att-2", Name: "blob", MimeType: "application/octet-stream"},
	}

	tt := composerTester(s)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.composerFocused
	})

	if tt.HasText("GO") {
		t.Fatalf("带扩展名的非图片附件不应重复显示格式 tag，texts=%v", tt.Texts())
	}
	if !tt.HasText("FILE") {
		t.Fatalf("无扩展名的非图片附件应保留 FILE tag，texts=%v", tt.Texts())
	}
}
