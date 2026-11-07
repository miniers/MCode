package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// splitStewardMessages 是分割用例的基线历史：有一条用户消息，所以总管会话可以分割。
func splitStewardMessages() []Message {
	return []Message{{ID: "m1", Role: "user", Text: "在吗"}}
}

// openSplitPanel 打开上下文用量面板。
func openSplitPanel(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("上下文用量"); err != nil {
		t.Fatalf("打开上下文用量失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("上下文窗口") })
}

// splitSessionViewFor 的三条门槛：总管会话、已有消息、这一轮已结束。
func TestSplitSessionViewFor(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	s := newChatState(newStewardFakeClient("t1"))
	s.selectedThreadID = "t1"
	idle := &Thread{ID: "t1"}

	if v := splitSessionViewFor(s, idle); v.Show {
		t.Fatalf("expected no split button without messages, got %+v", v)
	}

	s.mu.Lock()
	s.messages = splitStewardMessages()
	s.mu.Unlock()
	if v := splitSessionViewFor(s, idle); !v.Show || v.Disabled {
		t.Fatalf("expected enabled split button on idle steward thread, got %+v", v)
	}
	if !canSplitThread(s, idle) {
		t.Fatalf("expected canSplitThread true on idle steward thread")
	}

	running := &Thread{ID: "t1", IsRunning: true}
	if v := splitSessionViewFor(s, running); !v.Show || !v.Disabled {
		t.Fatalf("expected disabled split button while running, got %+v", v)
	}
	if canSplitThread(s, running) {
		t.Fatalf("expected canSplitThread false while running")
	}

	if v := splitSessionViewFor(s, &Thread{ID: "t2"}); v.Show {
		t.Fatalf("expected no split button on non-steward thread, got %+v", v)
	}
}

// 总管会话有消息又空闲时，用量面板提供拆分。
func TestUsagePanel_SplitShownForIdleStewardThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := newStewardFakeClient("t1")
	fake.SetMessages("t1", splitStewardMessages())

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("在吗") })
	openSplitPanel(t, tt)
	if err := tt.Click("拆分会话"); err != nil {
		t.Fatalf("点击拆分失败: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fake.splitSessionCalls()) == 1 })
}

// 普通会话（总管是另一个会话）的拆分项不可用。
func TestSplitButton_HiddenForNormalThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := newStewardFakeClient(stewardTestThreadID)
	addStewardTestThread(fake)
	fake.SetMessages("t1", splitStewardMessages())

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("在吗") })
	openSplitPanel(t, tt)
	if tt.HasText("拆分会话") {
		t.Fatal("普通会话不应显示拆分")
	}
}

// 还没有消息的总管会话没有可分割的历史，拆分项不可用。
func TestSplitButton_HiddenWithoutMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := newStewardFakeClient("t1")

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	tt.Frame()
	openSplitPanel(t, tt)
	if tt.HasText("拆分会话") {
		t.Fatal("没有消息时不应显示拆分")
	}
}

// 运行中菜单项保留但禁用：点不动，也不会发起分割请求。
func TestSplitButton_DisabledWhileRunning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := newStewardFakeClient("t1")
	fake.SetMessages("t1", splitStewardMessages())

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("在吗") })
	fake.TriggerRunningUpdate("t1", true)
	waitFor(t, tt, func() bool { return s.isThreadRunning("t1") })

	openSplitPanel(t, tt)
	if err := tt.Click("拆分会话"); err != nil {
		t.Fatalf("点击拆分失败: %v", err)
	}
	tt.Frame()
	tt.Frame()
	if calls := fake.splitSessionCalls(); len(calls) != 0 {
		t.Fatalf("expected no SplitSession call while running, got %v", calls)
	}
}

// 点击按钮后后端收到正确的会话 ID，并提示已分割。
func TestSplitButton_ClickSplitsSelectedThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := newStewardFakeClient("t1")
	fake.SetMessages("t1", splitStewardMessages())

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("在吗") })
	openSplitPanel(t, tt)
	if err := tt.Click("拆分会话"); err != nil {
		t.Fatalf("点击拆分失败: %v", err)
	}

	waitFor(t, tt, func() bool { return len(fake.splitSessionCalls()) == 1 })
	if calls := fake.splitSessionCalls(); calls[0] != "t1" {
		t.Fatalf("expected SplitSession on t1, got %v", calls)
	}
	waitFor(t, tt, func() bool { return tt.HasText("已分割，下一条消息不再带上面的历史") })
}

// /split 在总管会话里等同按钮：调用一次分割，且不把文本发给后端。
func TestSlashSplit_OnStewardThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := newStewardFakeClient("t1")

	s := newChatState(fake)
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话1"}}
	s.selectedThreadID = "t1"

	s.mu.Lock()
	s.draft = "/split"
	s.mu.Unlock()
	if !s.runLocalSlashCommand("t1", "/split") {
		t.Fatalf("/split on steward thread should be handled locally")
	}
	s.mu.Lock()
	draft := s.draft
	s.mu.Unlock()
	if draft != "" {
		t.Fatalf("/split should clear draft, got %q", draft)
	}

	deadline := len(fake.splitSessionCalls()) == 1
	for i := 0; i < 500 && !deadline; i++ {
		time.Sleep(10 * time.Millisecond)
		deadline = len(fake.splitSessionCalls()) == 1
	}
	if !deadline {
		t.Fatalf("expected exactly one SplitSession call, got %v", fake.splitSessionCalls())
	}
	if calls := fake.splitSessionCalls(); calls[0] != "t1" {
		t.Fatalf("expected SplitSession on t1, got %v", calls)
	}
}

// /split 在普通会话里只提示，不发起分割，同样返回 true 表示已拦截。
func TestSlashSplit_OnNormalThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()

	s := newChatState(fake)
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话1"}}
	s.selectedThreadID = "t1"

	if !s.runLocalSlashCommand("t1", "/split") {
		t.Fatalf("/split should be intercepted on normal thread too")
	}
	s.mu.Lock()
	toast := s.toastMessage
	s.mu.Unlock()
	if toast != "分割目前只对总管会话开放" {
		t.Fatalf("unexpected toast: %q", toast)
	}
	if calls := fake.splitSessionCalls(); len(calls) != 0 {
		t.Fatalf("expected no SplitSession call on normal thread, got %v", calls)
	}
}

// 时间线里 Kind 为 split_session 的消息渲染成分割线，线上的历史照常显示。
func TestSplitDivider_RendersAndKeepsEarlierMessages(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := newStewardFakeClient("t1")
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Text: "分割前的问题"},
		{ID: "m2", Role: "assistant", Text: "分割前的回答"},
		{ID: "m3", Role: "system", Kind: splitSessionKind, Text: splitSessionNotice},
		{ID: "m4", Role: "user", Text: "分割后的问题"},
	})

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("分割前的问题") && tt.HasText("分割后的问题")
	})
	if !tt.HasText(splitSessionNotice) {
		t.Fatalf("expected split divider text, texts: %q", tt.Texts())
	}
	if !tt.HasText("分割前的回答") {
		t.Fatalf("expected history above the divider to stay visible, texts: %q", tt.Texts())
	}
}

// splitDividerText 把正文拆成标题与说明：空正文退回常量，单行只有标题，
// 多行的剩余内容压成一行说明。
func TestSplitDividerText(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantTitle  string
		wantDetail string
	}{
		{name: "空正文退回常量", text: "  ", wantTitle: splitSessionNotice},
		{name: "单行只有标题", text: " " + splitSessionNotice + "\n", wantTitle: splitSessionNotice},
		{name: "两行拆出说明", text: splitSessionNotice + "\n已带走：账本 2 条、话题 1 个、待处理 3 条", wantTitle: splitSessionNotice, wantDetail: "已带走：账本 2 条、话题 1 个、待处理 3 条"},
		{name: "多行说明压成一行", text: splitSessionNotice + "\n已带走：账本 2 条、\n话题 1 个", wantTitle: splitSessionNotice, wantDetail: "已带走：账本 2 条、 话题 1 个"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, detail := splitDividerText(tc.text)
			if title != tc.wantTitle || detail != tc.wantDetail {
				t.Fatalf("splitDividerText = (%q, %q), want (%q, %q)", title, detail, tc.wantTitle, tc.wantDetail)
			}
		})
	}
}

// 带计数的分割线在时间线上渲染出标题与「已带走」两段文字。
func TestSplitDivider_RendersCarriedCounts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	const counts = "已带走：账本 12 条、话题 3 个、待处理 2 条"
	fake := newStewardFakeClient("t1")
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Text: "分割前的问题"},
		{ID: "m2", Role: "system", Kind: splitSessionKind, Text: splitSessionNotice + "\n" + counts},
	})

	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("分割前的问题") && tt.HasText(counts)
	})
	if !tt.HasText(splitSessionNotice) {
		t.Fatalf("expected divider title line, texts: %q", tt.Texts())
	}
	if !tt.HasText(counts) {
		t.Fatalf("expected carried counts line, texts: %q", tt.Texts())
	}
}

// completionHasLabel 判断补全候选里有没有某一项标签。
func completionHasLabel(items []completionItem, label string) bool {
	for _, it := range items {
		if it.Label == label {
			return true
		}
	}
	return false
}

// /split 只出现在总管会话的补全候选里；普通会话的候选不含它，免得选了只弹一句提示。
func TestSplitCompletion_OnlyOnStewardThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(waitUIStateWrites)

	steward := newChatState(newStewardFakeClient("t1"))
	steward.commandsCache["t1"] = []SlashCommand{{Name: "plan"}}
	steward.mu.Lock()
	steward.draft = "/"
	steward.mu.Unlock()
	steward.updateCompletion("t1")
	if !completionHasLabel(steward.completionItems, "/split") {
		t.Fatalf("总管会话的补全候选应含 /split，got %+v", steward.completionItems)
	}

	normal := newChatState(NewFakeClient())
	normal.commandsCache["t1"] = []SlashCommand{{Name: "plan"}}
	normal.mu.Lock()
	normal.draft = "/"
	normal.mu.Unlock()
	normal.updateCompletion("t1")
	if completionHasLabel(normal.completionItems, "/split") {
		t.Fatalf("普通会话的补全候选不应含 /split，got %+v", normal.completionItems)
	}
}
