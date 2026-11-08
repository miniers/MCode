package mcodeui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

type fakeQueueUpdaterClient struct {
	*FakeClient
	updateCalls []struct {
		threadID   string
		followUpID string
		text       string
	}
}

func (f *fakeQueueUpdaterClient) UpdateQueued(ctx context.Context, threadID, followUpID, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls = append(f.updateCalls, struct {
		threadID   string
		followUpID string
		text       string
	}{threadID: threadID, followUpID: followUpID, text: text})
	return nil
}

type fakeSteerSenderClient struct {
	*FakeClient
	interruptAndSendCalls []struct {
		threadID      string
		text          string
		attachmentIDs []string
	}
}

func (f *fakeSteerSenderClient) InterruptAndSend(ctx context.Context, threadID, text string, attachmentIDs []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interruptAndSendCalls = append(f.interruptAndSendCalls, struct {
		threadID      string
		text          string
		attachmentIDs []string
	}{threadID: threadID, text: text, attachmentIDs: attachmentIDs})
	return nil
}

func TestComposer_StopOrRecall_RecallTurnSuccess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.recallTurnText = "user prompt to retract"
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.messages = []Message{{Role: "user", Text: "user prompt to retract"}}
	s.draft = ""
	s.mu.Unlock()

	s.stopOrRecall()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		text, ok := s.consumePendingDraftFillLocked()
		s.mu.Unlock()
		if ok {
			if text != "user prompt to retract" {
				t.Fatalf("expected pendingDraftFill %q, got %q", "user prompt to retract", text)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for pendingDraftFill")
}

func TestComposer_StopOrRecall_RecallTurnNonEmptyDraftKeepsOriginal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.recallTurnText = "retracted user prompt"
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.messages = []Message{{Role: "user", Text: "retracted user prompt"}}
	s.draft = "existing uncommitted draft"
	s.mu.Unlock()

	s.stopOrRecall()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		s.consumePendingDraftFillLocked()
		toast := s.toastMessage
		fill := s.pendingDraftFill
		s.mu.Unlock()
		if toast == "已撤回，原输入框内容保留" {
			if fill != nil {
				t.Fatalf("expected pendingDraftFill to be nil, got %v", *fill)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for toast")
}

func TestComposer_StopOrRecall_FallbackToInterrupt(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.recallTurnErr = ErrNotImplemented
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", IsRunning: true}}
	s.messages = []Message{{Role: "user", Text: "prompt"}}
	s.mu.Unlock()

	s.stopOrRecall()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		interrupts := len(fake.interruptCalls)
		fake.mu.Unlock()
		s.mu.Lock()
		interrupted := s.interrupted["t1"]
		s.mu.Unlock()
		if interrupts > 0 && interrupted {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for interrupt fallback")
}

func TestComposer_BeginEditAndSaveQueuedEdit_UpdateQueuedSupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeQueueUpdaterClient{FakeClient: NewFakeClient()}
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.queue["t1"] = []QueuedFollowUp{{ID: "q1", Text: "original queue text"}}
	s.draft = "stashed composer draft"
	s.mu.Unlock()

	s.beginEditQueued("q1")

	editingID := s.editingQueuedID()
	s.mu.Lock()
	stash := s.editStashDraft
	fill, hasFill := s.consumePendingDraftFillLocked()
	s.mu.Unlock()

	if editingID != "q1" {
		t.Fatalf("expected editingQueuedID %q, got %q", "q1", editingID)
	}
	if stash != "stashed composer draft" {
		t.Fatalf("expected editStashDraft %q, got %q", "stashed composer draft", stash)
	}
	if !hasFill || fill != "original queue text" {
		t.Fatalf("expected pendingDraftFill %q, got %v (%v)", "original queue text", fill, hasFill)
	}

	s.saveQueuedEdit("modified queue text")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		callCount := len(fake.updateCalls)
		var lastCall struct {
			threadID   string
			followUpID string
			text       string
		}
		if callCount > 0 {
			lastCall = fake.updateCalls[0]
		}
		fake.mu.Unlock()

		s.mu.Lock()
		activeEdit := s.editingFollowUpID
		restoredDraft, draftRestored := s.consumePendingDraftFillLocked()
		s.mu.Unlock()

		if callCount > 0 && activeEdit == "" && draftRestored {
			if lastCall.threadID != "t1" || lastCall.followUpID != "q1" || lastCall.text != "modified queue text" {
				t.Fatalf("unexpected UpdateQueued call: %+v", lastCall)
			}
			if restoredDraft != "stashed composer draft" {
				t.Fatalf("expected restored draft %q, got %q", "stashed composer draft", restoredDraft)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for saveQueuedEdit with UpdateQueued")
}

func TestComposer_UnsupportedEditPreservesQueue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.queue["t1"] = []QueuedFollowUp{{ID: "q2", Text: "original"}}
	s.draft = "my draft"
	s.beginEditQueued("q2")
	s.saveQueuedEdit("updated")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.editingFollowUpID != "q2" || s.toastMessage == "" {
		t.Fatal("unsupported edit must retain the editor and show an error")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.removeQueuedCalls) != 0 || len(fake.enqueueCalls) != 0 {
		t.Fatal("unsupported update must not remove or re-enqueue the original")
	}
}

func TestComposer_CancelQueuedEdit_RestoresStash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.queue["t1"] = []QueuedFollowUp{{ID: "q1", Text: "queued item"}}
	s.draft = "stash text before edit"
	s.mu.Unlock()

	s.beginEditQueued("q1")

	s.mu.Lock()
	if s.editingFollowUpID != "q1" {
		t.Fatalf("expected editingFollowUpID %q, got %q", "q1", s.editingFollowUpID)
	}
	_, _ = s.consumePendingDraftFillLocked()
	s.mu.Unlock()

	s.cancelQueuedEdit()

	editingID := s.editingQueuedID()
	s.mu.Lock()
	restored, ok := s.consumePendingDraftFillLocked()
	s.mu.Unlock()

	if editingID != "" {
		t.Fatalf("expected editingQueuedID to be empty, got %q", editingID)
	}
	if !ok || restored != "stash text before edit" {
		t.Fatalf("expected restored draft %q, got %q (ok=%v)", "stash text before edit", restored, ok)
	}
}

func TestComposer_ClearQueue_RemovesAllIDsInOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.queue["t1"] = []QueuedFollowUp{
		{ID: "q1", Text: "first"},
		{ID: "q2", Text: "second"},
		{ID: "q3", Text: "third"},
	}
	s.mu.Unlock()

	s.clearQueue()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		calls := append([]fakeQueueCall(nil), fake.removeQueuedCalls...)
		fake.mu.Unlock()

		if len(calls) == 3 {
			if calls[0].FollowUpID != "q1" || calls[1].FollowUpID != "q2" || calls[2].FollowUpID != "q3" {
				t.Fatalf("expected remove calls for q1, q2, q3, got: %+v", calls)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for clearQueue calls")
}

func TestComposer_SteerMessage_UsesInterruptAndSendWhenSupported(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := &fakeSteerSenderClient{FakeClient: NewFakeClient()}
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.interrupted["t1"] = true
	s.mu.Unlock()

	s.steerMessage("steer text message")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		callCount := len(fake.interruptAndSendCalls)
		var call struct {
			threadID      string
			text          string
			attachmentIDs []string
		}
		if callCount > 0 {
			call = fake.interruptAndSendCalls[0]
		}
		fake.mu.Unlock()

		s.mu.Lock()
		interrupted := s.interrupted["t1"]
		sentDraft := s.sentDraft
		s.mu.Unlock()

		if callCount == 1 && !interrupted && sentDraft != nil && *sentDraft == "steer text message" {
			if call.threadID != "t1" || call.text != "steer text message" {
				t.Fatalf("unexpected InterruptAndSend call: %+v", call)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for steerMessage via InterruptAndSend")
}

func TestComposer_UnsupportedSteerPreservesDraft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.selectedThreadID = "t1"
	s.draft = "steer draft"
	s.steerMessage(s.draft)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sentDraft != nil || s.draft != "steer draft" || s.toastMessage == "" {
		t.Fatal("unsupported steer must retain the draft and show an error")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.enqueueCalls) != 0 {
		t.Fatal("disabled queue must not be used as a steer fallback")
	}
}

func TestComposer_StopKeepsContinueThroughRunningUpdates(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	fake.TriggerRunningUpdate("t1", true)
	waitFor(t, tt, func() bool { return tt.HasText("停止") })
	tt.Key(ui.Cmd, ui.KeyPeriod)
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.interrupted["t1"]
	})

	// 停止确认后，流中仍可能残留同一轮运行中的消息。
	fake.TriggerRunningUpdate("t1", true)
	fake.TriggerRunningUpdate("t1", false)
	waitFor(t, tt, func() bool { return tt.HasText("继续") })

	// 真正开始新一轮时才应清掉旧的继续入口。
	fake.TriggerRunningUpdate("t1", true)
	fake.TriggerRunningUpdate("t1", false)
	waitFor(t, tt, func() bool { return !tt.HasText("继续") })
}

type retryContinueClient struct{ *FakeClient }

func (f *retryContinueClient) Send(ctx context.Context, threadID, text string) (bool, error) {
	_, _ = f.FakeClient.Send(ctx, threadID, text)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sendCalls) == 1 {
		return false, errors.New("暂时不可用")
	}
	return false, nil
}

// continueCapsuleText 按胶囊的实际文案（状态 + " · 继续"）拼出测试定位文字。
func continueCapsuleText(kind continuePillKind) string {
	status, _ := continuePillLabels(kind)
	return status + " · 继续"
}

// visibleContinueCapsuleText 返回当前帧展示的继续胶囊文案；没有则返回空串。
func visibleContinueCapsuleText(tt *ui.Tester) string {
	for _, kind := range []continuePillKind{continuePillInterrupted, continuePillFailed} {
		text := continueCapsuleText(kind)
		if tt.HasText(text) {
			return text
		}
	}
	return ""
}

func TestComposer_ContinueFailureCanRetry(t *testing.T) {
	fake := &retryContinueClient{NewFakeClient()}
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	s.mu.Lock()
	s.interrupted["t1"] = true
	s.mu.Unlock()
	waitFor(t, tt, func() bool { return visibleContinueCapsuleText(tt) != "" })
	if err := tt.Click(visibleContinueCapsuleText(tt)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		calls := len(fake.sendCalls)
		fake.mu.Unlock()
		s.mu.Lock()
		defer s.mu.Unlock()
		return calls == 1 && !s.sending
	})
	if visibleContinueCapsuleText(tt) == "" {
		t.Fatal("继续失败后应保留重试入口")
	}
	if err := tt.Click(visibleContinueCapsuleText(tt)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendCalls) == 2
	})
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.interrupted["t1"] && !s.sending
	})
}

type delayedQueueClient struct {
	*FakeClient
	started chan struct{}
	release chan struct{}
}

func (f *delayedQueueClient) Queue(ctx context.Context, threadID string) ([]QueuedFollowUp, error) {
	items, err := f.FakeClient.Queue(ctx, threadID)
	close(f.started)
	<-f.release
	return items, err
}

func TestComposer_QueueLoadDoesNotOverwriteNewEvent(t *testing.T) {
	fake := &delayedQueueClient{NewFakeClient(), make(chan struct{}), make(chan struct{})}
	s := newChatState(fake)
	t.Cleanup(s.close)
	s.startQueueWatch()
	s.selectThread("t1")
	select {
	case <-fake.started:
	case <-time.After(2 * time.Second):
		t.Fatal("等待加载队列超时")
	}
	fake.setQueue("t1", []QueuedFollowUp{{ID: "new", Text: "新快照"}})
	close(fake.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		loaded := s.watchStop != nil
		items := append([]QueuedFollowUp(nil), s.queue["t1"]...)
		s.mu.Unlock()
		if loaded {
			if len(items) != 1 || items[0].ID != "new" {
				t.Fatalf("旧加载覆盖了新快照: %+v", items)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("等待会话加载完成超时")
}

func TestComposer_CloseUnsubscribesQueue(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.startQueueWatch()
	fake.mu.Lock()
	if len(fake.queueWatchers) != 1 {
		fake.mu.Unlock()
		t.Fatal("未注册队列监听")
	}
	var pending func(string, []QueuedFollowUp)
	for _, fn := range fake.queueWatchers {
		pending = fn
	}
	fake.mu.Unlock()
	s.close()
	s.close()
	fake.mu.Lock()
	count := len(fake.queueWatchers)
	fake.mu.Unlock()
	if count != 0 {
		t.Fatal("关闭窗口后仍保留队列监听")
	}
	// 注销前已被复制的回调也不得再写入关闭的窗口。
	pending("t1", []QueuedFollowUp{{ID: "late"}})
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue["t1"]) != 0 {
		t.Fatal("关闭后仍处理队列通知")
	}
}

// TestComposer_IdleReturnSends 测试空闲时按 Return 走 Send 发送。
func TestComposer_IdleReturnSends(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	tt.Type("hello world")
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendCalls) == 1 && fake.sendCalls[0].Text == "hello world" && fake.sendCalls[0].ThreadID == "t1"
	})
}

// TestComposer_RunningCmdReturnEnqueues 覆盖开关开启时 Return 与 ⌘Return 入队。
func TestComposer_RunningCmdReturnEnqueues(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake, func(s *chatState) {
		cfg := config.Defaults()
		cfg.Session.QueueWhileRunning = true
		cfg.Session.InsertIntoRunningTurn = false
		setChatSettings(s, &SettingsState{cfg: cfg})
	}), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.TriggerRunningUpdate("t1", true)
	// 运行中的提示已并入占位符：卡片顶部不再有独立的运行提示行。
	waitFor(t, tt, func() bool {
		_, running := tt.Find("停止")
		return running && !tt.HasText("运行中：⌘Return 加入队列，⌘. 停止")
	})
	// 占位符文本不进 labels，HasText 读不到，这里直接断言返回值。
	if got := composerPlaceholder("build", true, sendStateQueue, "", config.ComposerSendKeyEnter); got != "回复中 · Enter 加入队列，⌘. 停止" {
		t.Fatalf("unexpected running queue placeholder: %q", got)
	}
	if got := composerPlaceholder("build", true, sendStateSteer, "", config.ComposerSendKeyEnter); got != "回复中 · Enter 打断并发送，⌘. 停止" {
		t.Fatalf("unexpected running steer placeholder: %q", got)
	}
	if got := composerPlaceholder("build", true, sendStateInject, "", config.ComposerSendKeyEnter); got != "回复中 · Enter 插入当前回合，⌘. 停止" {
		t.Fatalf("unexpected running inject placeholder: %q", got)
	}

	tt.Type("task 1")
	waitFor(t, tt, func() bool {
		// 有草稿且开启排队时，主按钮变为「排队」胶囊。
		_, ok := tt.Find("排队")
		return ok
	})

	// 开启运行中排队后，普通 Return 不调用 Send，而是 Enqueue。
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.enqueueCalls) == 1 && fake.enqueueCalls[0].Text == "task 1" && fake.enqueueCalls[0].ThreadID == "t1"
	})

	fake.mu.Lock()
	sendCount := len(fake.sendCalls)
	fake.mu.Unlock()
	if sendCount != 0 {
		t.Fatalf("expected 0 sendCalls, got %d", sendCount)
	}

	// 随后 ⌘Return 如常入队
	tt.Type("task 2")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("排队")
		return ok
	})
	tt.Key(ui.Cmd, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.enqueueCalls) == 2 && fake.enqueueCalls[1].Text == "task 2" && fake.enqueueCalls[1].ThreadID == "t1"
	})

	fake.mu.Lock()
	sendCount = len(fake.sendCalls)
	fake.mu.Unlock()
	if sendCount != 0 {
		t.Fatalf("expected 0 sendCalls, got %d", sendCount)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("排队 2") || tt.HasText("排队 1")
	})
	if !tt.HasText("立即发送") {
		if tt.HasText("排队 2") {
			_ = tt.Click("排队 2")
		} else if tt.HasText("排队 1") {
			_ = tt.Click("排队 1")
		}
	}
	waitFor(t, tt, func() bool { return tt.HasText("task 1") && tt.HasText("立即发送") })
}

// TestComposer_RunningStopKeyPeriod 测试运行中无字时显示“停止”，按 ⌘. 后 interruptCalls 加 1。
func TestComposer_RunningStopKeyPeriod(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.TriggerRunningUpdate("t1", true)
	waitFor(t, tt, func() bool {
		return tt.HasText("停止")
	})

	tt.Key(ui.Cmd, ui.KeyPeriod)

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.interruptCalls) == 1 && fake.interruptCalls[0] == "t1"
	})
}

// TestComposer_InterruptedContinue 测试中断成功后会话变为未运行时出现“继续”，点击后 Send 收到 continuePrompt，“继续”随之消失。
func TestComposer_InterruptedContinue(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.TriggerRunningUpdate("t1", true)
	waitFor(t, tt, func() bool {
		return tt.HasText("停止")
	})

	// 按 ⌘. 触发中断
	tt.Key(ui.Cmd, ui.KeyPeriod)
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.interruptCalls) == 1
	})

	// 模拟中断完成，会话变为未运行
	fake.TriggerRunningUpdate("t1", false)

	// 出现“继续”按钮：中断后胶囊文案是「上一轮已中断 · 继续」。
	continueText := continueCapsuleText(continuePillInterrupted)
	waitFor(t, tt, func() bool {
		return tt.HasText(continueText)
	})

	// 点击“继续”
	if err := tt.Click(continueText); err != nil {
		t.Fatalf("click 继续 failed: %v", err)
	}

	// Send 收到 continuePrompt
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendCalls) == 1 && fake.sendCalls[0].Text == continuePrompt
	})

	// “继续”随之消失
	waitFor(t, tt, func() bool {
		return !tt.HasText("继续")
	})
}

// TestComposer_QueueBubbleActions 测试 setQueue 后气泡显示文本，点“立即发送”记录 sendQueuedNowCalls；再入队后点“删除”记录 removeQueuedCalls 且删除后气泡消失。
func TestComposer_QueueBubbleActions(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	fake.setQueue("t1", []QueuedFollowUp{
		{ID: "q1", Text: "follow up bubble", AttachmentCount: 2},
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("排队 1")
	})

	if err := tt.Click("排队 1"); err != nil {
		t.Fatalf("click 排队 1 tab failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("follow up bubble") && tt.HasText("立即发送") && tt.HasText("删除")
	})

	if err := tt.Click("立即发送"); err != nil {
		t.Fatalf("click 立即发送 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendQueuedNowCalls) == 1 && fake.sendQueuedNowCalls[0].FollowUpID == "q1"
	})

	fake.setQueue("t1", []QueuedFollowUp{{ID: "q2", Text: "second bubble"}})

	waitFor(t, tt, func() bool {
		return tt.HasText("second bubble") && tt.HasText("删除")
	})

	if err := tt.Click("删除"); err != nil {
		t.Fatalf("click 删除 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.removeQueuedCalls) == 1 && fake.removeQueuedCalls[0].FollowUpID == "q2"
	})

	waitFor(t, tt, func() bool {
		return !tt.HasText("second bubble")
	})
}

// TestComposer_Conflict_FocusedCmdReturnEnqueuesOnly 冲突测试一：运行中、无审批、输入框聚焦时按 ⌘Return 只入队、不调用 Respond。
func TestComposer_Conflict_FocusedCmdReturnEnqueuesOnly(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.composerFocused
	})

	fake.TriggerRunningUpdate("t1", true)
	tt.Type("queued text")

	tt.Key(ui.Cmd, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.enqueueCalls) == 1 && fake.enqueueCalls[0].Text == "queued text"
	})

	respondCalls := fake.RespondCalls()
	if len(respondCalls) != 0 {
		t.Fatalf("expected 0 respondCalls, got %d", len(respondCalls))
	}
}

// TestComposer_Conflict_PendingApprovalCmdReturnApprovesOnly 冲突测试二：运行中且有待处理审批时输入框禁用，按 ⌘Return 只调用 Respond 批准、不入队。
func TestComposer_Conflict_PendingApprovalCmdReturnApprovesOnly(t *testing.T) {
	fake := NewFakeClient()
	s, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.composerFocused
	})

	fake.TriggerRunningUpdate("t1", true)
	fake.TriggerApprovals("t1", []Approval{{
		ThreadID:  "t1",
		RequestID: "req-conflict",
		Kind:      ApprovalKindCommand,
		Title:     "Bash",
		Detail:    "echo 1",
		Options:   allowDenyOptions(),
	}})

	// 等待审批到达，输入框应变为非聚焦（禁用）
	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.approvals) == 1 && !s.composerFocused
	})

	// 待审批到达后审批卡片出现；草稿为空时禁用原因只在输入框占位符里，不再占卡片顶部一行。
	waitFor(t, tt, func() bool {
		return tt.HasText("执行命令 · Bash")
	})
	if tt.HasText("存在待处理的审批请求") {
		t.Fatal("disabled reason should be shown in the placeholder, not as a top row")
	}

	tt.Key(ui.Cmd, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		calls := fake.RespondCalls()
		return len(calls) == 1 && calls[0].Approval.RequestID == "req-conflict" && calls[0].OptionID == "allow"
	})

	fake.mu.Lock()
	enqueueCount := len(fake.enqueueCalls)
	fake.mu.Unlock()
	if enqueueCount != 0 {
		t.Fatalf("expected 0 enqueueCalls when approval pending, got %d", enqueueCount)
	}
}

// TestCmdReturnApprovalTarget_ComposerFocusedReturnsFalse 测试 responding=false、composerFocused=true 时 cmdReturnApprovalTarget 返回 false。
func TestCmdReturnApprovalTarget_ComposerFocusedReturnsFalse(t *testing.T) {
	approvals := []Approval{{
		RequestID: "req-1",
		Kind:      ApprovalKindCommand,
	}}
	_, ok := cmdReturnApprovalTarget(approvals, false, true)
	if ok {
		t.Fatalf("expected ok to be false when composerFocused=true, got true")
	}
}

// TestComposer_StopCurrent_AlreadyStoppedClearsStopping 测试后端返回 already_stopped 时清掉停止态并按空闲处理。
func TestComposer_StopCurrent_AlreadyStoppedClearsStopping(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.interruptAlreadyStopped = true
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", IsRunning: true}}
	s.optimisticRunning["t1"] = true
	s.mu.Unlock()

	s.interruptCurrent()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		interrupts := len(fake.interruptCalls)
		fake.mu.Unlock()
		s.mu.Lock()
		stopping := s.stopping
		interrupted := s.interrupted["t1"]
		running := false
		for _, th := range s.threads {
			if th.ID == "t1" {
				running = th.IsRunning
			}
		}
		s.mu.Unlock()
		if interrupts > 0 && !stopping && !running {
			if interrupted {
				t.Fatal("already-stopped thread must not be marked as interrupted")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for already-stopped to settle")
}

// TestSendMessage_DelegatedSkipsOptimisticRunning 测试消息被交给子线程时主线程不做乐观运行中标记。
func TestSendMessage_DelegatedSkipsOptimisticRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.sendDelegated = true
	s := newChatState(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1"}}
	s.mu.Unlock()

	s.sendMessage("hello")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		fake.mu.Lock()
		sent := len(fake.sendCalls)
		fake.mu.Unlock()
		s.mu.Lock()
		done := sent > 0 && !s.sending
		optimistic := s.optimisticRunning["t1"]
		running := false
		for _, th := range s.threads {
			if th.ID == "t1" {
				running = th.IsRunning
			}
		}
		s.mu.Unlock()
		if done {
			if optimistic || running {
				t.Fatalf("delegated send should not mark thread running: optimistic=%v running=%v", optimistic, running)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for send")
}

// TestComposer_SwarmBusyKeepsRunningState 覆盖 R7-012：主会话这一轮已结束但名下子线程仍在跑时，
// 这一轮并没有真正结束，输入区的忙态（停止按钮）必须保留，不能提前放开输入。
func TestComposer_SwarmBusyKeepsRunningState(t *testing.T) {
	fake := NewFakeClient()
	var state *chatState
	tt := ui.NewTester(Root(fake, func(s *chatState) { state = s }), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	// 空闲时主按钮是「发送」。
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("发送")
		return ok
	})

	// 后端摘要：主会话 IsRunning 为假、SwarmBusy 为真（子线程还在跑）。
	state.mu.Lock()
	var cur Thread
	for _, th := range state.threads {
		if th.ID == "t1" {
			cur = th
			break
		}
	}
	state.mu.Unlock()
	cur.IsRunning = false
	cur.SwarmBusy = true
	state.onWatchUpdate(ThreadUpdate{ThreadID: "t1", Thread: cur})
	tt.Frame()

	// 忙态下主按钮变成「停止」，说明输入区仍按运行中处理。
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("停止")
		return ok
	})
	if _, ok := tt.Find("发送"); ok {
		t.Fatal("SwarmBusy 时不应回到空闲的发送按钮")
	}

	// 子线程也结束后才真正空闲。
	cur.SwarmBusy = false
	state.onWatchUpdate(ThreadUpdate{ThreadID: "t1", Thread: cur})
	tt.Frame()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("发送")
		return ok
	})
}

// TestProviderSupportsSteer 校验中途插话能力表与各 provider 的实际实现一致：
// mcode 实现 harness.Steerer，pi 的端点前缀先归一后再查表。
func TestProviderSupportsSteer(t *testing.T) {
	cases := []struct {
		provider string
		want     bool
	}{
		{"claude", true},
		{"codex", true},
		{"mcode", true},
		{"omp", true},
		{"pi", true},
		{"pi:auto", true},
		{"acp", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			if got := providerSupportsSteer(tc.provider); got != tc.want {
				t.Fatalf("providerSupportsSteer(%q) = %v，想要 %v", tc.provider, got, tc.want)
			}
		})
	}
}
