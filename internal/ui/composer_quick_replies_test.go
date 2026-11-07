package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// configQuickReplyStub 模拟任务 4 将要加到 config.Config 里的 QuickReplies 元素结构。
type configQuickReplyStub struct {
	Label   string
	Message string
}

// TestDecodeQuickReplies 验证配置解码清洗：空正文的项丢弃、空标签退回正文首行、过长标签截断。
func TestDecodeQuickReplies(t *testing.T) {
	items := decodeQuickReplies([]configQuickReplyStub{
		{Label: "继续", Message: " 继续按计划推进 "},
		{Label: "", Message: "第一行\n第二行"},
		{Label: "空正文", Message: "   "},
		{Label: "这是一个超过四十个字上限的非常非常长的快捷回复标签用来验证截断逻辑是否正确生效", Message: "正文"},
	})
	if len(items) != 3 {
		t.Fatalf("应保留 3 条（丢弃空正文），got %d: %+v", len(items), items)
	}
	if items[0].Label != "继续" || items[0].Message != "继续按计划推进" {
		t.Fatalf("第 1 条应去掉首尾空格，got %+v", items[0])
	}
	if items[1].Label != "第一行" {
		t.Fatalf("空标签应退回正文首行，got %q", items[1].Label)
	}
	if got := len([]rune(items[2].Label)); got > quickReplyMaxLabelRunes {
		t.Fatalf("过长标签应截断到 %d 字，got %d", quickReplyMaxLabelRunes, got)
	}
}

// TestQuickReplies_ClickFillsDraftWithoutSending 验证点击快捷回复只把正文填进草稿、不发请求，
// 且填入后这一行因为草稿不为空而消失。
func TestQuickReplies_ClickFillsDraftWithoutSending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := quickReplyItemsFor
	quickReplyItemsFor = func(*chatState) []quickReplyItem {
		return []quickReplyItem{{Label: "快捷一", Message: "继续按计划推进"}}
	}
	t.Cleanup(func() { quickReplyItemsFor = prev })

	fake := NewFakeClient()
	fake.SetMessages("t1", []Message{{ID: "m1", Role: "user", Text: "先做个开头", TurnIndex: 1}})
	s, view := buildRoot(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("快捷一") })

	if err := tt.Click("快捷一"); err != nil {
		t.Fatalf("点击快捷回复失败: %v", err)
	}

	waitFor(t, tt, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.draft == "继续按计划推进"
	})

	fake.mu.Lock()
	sends := len(fake.sendCalls)
	fake.mu.Unlock()
	if sends != 0 {
		t.Fatalf("点击快捷回复不应发送，got %d 次 Send", sends)
	}

	// 草稿非空后这一行不再出现（按计划只在空草稿空闲时提示）。
	waitFor(t, tt, func() bool { return !tt.HasText("快捷一") })
}

// TestQuickRepliesVisibleRules 验证显示条件：无历史消息、运行中、草稿非空都不显示。
func TestQuickRepliesVisibleRules(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	state := newChatState(fake)
	t.Cleanup(state.close)

	base := func() {
		state.mu.Lock()
		state.selectedThreadID = "t1"
		state.threads = []Thread{{ID: "t1", ProjectID: "p1", IsRunning: false}}
		state.messages = []Message{{ID: "m1", Role: "user", Text: "你好"}}
		state.draft = ""
		state.attachments = nil
		state.mu.Unlock()
	}

	base()
	if !state.quickRepliesVisible() {
		t.Fatal("有历史消息且空闲、草稿为空时应显示")
	}

	base()
	state.mu.Lock()
	state.messages = nil
	state.mu.Unlock()
	if state.quickRepliesVisible() {
		t.Fatal("没有历史消息时不应显示")
	}

	base()
	state.mu.Lock()
	state.draft = "正在写"
	state.mu.Unlock()
	if state.quickRepliesVisible() {
		t.Fatal("草稿非空时不应显示")
	}

	base()
	state.mu.Lock()
	state.threads[0].IsRunning = true
	state.mu.Unlock()
	if state.quickRepliesVisible() {
		t.Fatal("轮次运行中时不应显示")
	}

	base()
	state.mu.Lock()
	state.sending = true
	state.mu.Unlock()
	if state.quickRepliesVisible() {
		t.Fatal("正在发送时不应显示")
	}
}
