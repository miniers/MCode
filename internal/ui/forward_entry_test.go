package mcodeui

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/testenv"
)

// TestChatHeaderForwardButtonVisibilityAndAction 验证「转发到新会话」已从顶栏独立按钮迁入标题右键菜单：
// 顶栏不再显示独立转发按钮；空会话和总管会话的标题右键菜单不含该项；有消息的普通会话显示该项，
// 转发进行中禁用，空闲点击触发转发。
func TestChatHeaderForwardButtonVisibilityAndAction(t *testing.T) {
	testenv.TempHome(t)

	fake := NewFakeClient()
	s := newChatState(fake)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "长对话"}}
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderChatHeader(c, s, "t1", ThreadUsage{}, 0)
	}, 1000, 120)

	fwdLabel := "转发到新会话 " + shortcutKeysDisplay("forward-thread", "⇧⌘D")
	if _, ok := tt.Find(fwdLabel); ok {
		t.Fatalf("顶栏不应再显示独立转发按钮")
	}
	if err := tt.RightClick("会话标题拖动区"); err != nil {
		t.Fatalf("右键标题失败: %v", err)
	}
	if slices.Contains(tt.Menu(), "转发到新会话") {
		t.Fatalf("空会话标题右键菜单不应包含「转发到新会话」: %v", tt.Menu())
	}
	tt.Key(0, ui.KeyEscape)

	// 加入消息后显示；转发进行中时该菜单项禁用，选择不会触发转发。
	s.mu.Lock()
	s.messages = []Message{{ID: "m1", Role: "user", Text: "hello"}}
	if s.forwardInflight == nil {
		s.forwardInflight = make(map[string]bool)
	}
	s.forwardInflight["t1"] = true
	s.mu.Unlock()
	tt.Frame()

	if _, ok := tt.Find(fwdLabel); ok {
		t.Fatalf("有消息时顶栏也不应显示独立转发按钮")
	}
	if err := tt.RightClick("会话标题拖动区"); err != nil {
		t.Fatalf("右键标题失败: %v", err)
	}
	if !slices.Contains(tt.Menu(), "转发到新会话") {
		t.Fatalf("有消息的会话标题右键菜单应包含「转发到新会话」: %v", tt.Menu())
	}
	_ = tt.ChooseMenuItem("转发到新会话")
	tt.Frame()
	if forwardCallsContain(fake, "t1") {
		t.Fatalf("转发进行中菜单项应禁用，不应触发转发")
	}

	// 空闲态选择「转发到新会话」触发 ForwardThread。
	s.mu.Lock()
	s.forwardInflight["t1"] = false
	s.mu.Unlock()
	tt.Frame()

	if err := tt.RightClick("会话标题拖动区"); err != nil {
		t.Fatalf("右键标题失败: %v", err)
	}
	if err := tt.ChooseMenuItem("转发到新会话"); err != nil {
		t.Fatalf("选择标题右键菜单「转发到新会话」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return forwardCallsContain(fake, "t1")
	})

	// 总管会话即使有消息，标题右键菜单也不显示「转发到新会话」。
	stewardClient := &stewardFakeClient{FakeClient: NewFakeClient(), threadID: "steward-1"}
	sSteward := newChatState(stewardClient)
	sSteward.mu.Lock()
	sSteward.selectedThreadID = "steward-1"
	sSteward.threads = []Thread{{ID: "steward-1", ProjectID: "p1", Title: "MoCo · 总管"}}
	sSteward.messages = []Message{{ID: "m1", Role: "user", Text: "hello"}}
	sSteward.mu.Unlock()

	ttSteward := ui.NewTester(func(c *ui.Context) {
		renderChatHeader(c, sSteward, "steward-1", ThreadUsage{}, 0)
	}, 1000, 120)
	if err := ttSteward.RightClick("会话标题拖动区"); err != nil {
		t.Fatalf("右键总管标题失败: %v", err)
	}
	if slices.Contains(ttSteward.Menu(), "转发到新会话") {
		t.Fatalf("总管会话标题右键菜单不应显示「转发到新会话」: %v", ttSteward.Menu())
	}
}

// TestRenderUsagePanel_ForwardAction 验证上下文用量浮层内置「转发到新会话」操作行：
// 有消息时显示并附带快捷键，点击关闭浮层并触发转发；空会话不显示。
func TestRenderUsagePanel_ForwardAction(t *testing.T) {
	testenv.TempHome(t)

	fake := NewFakeClient()
	s := newChatState(fake)
	th := &Thread{ID: "t1", ProjectID: "p1", Provider: "claude", Model: "claude-sonnet-4-6"}
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{*th}
	s.composerView.usageOpen = true
	s.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		renderUsagePanel(c, s, th)
	}, 420, 420)

	if tt.HasText("转发到新会话") {
		t.Fatalf("空会话用量浮层不应显示转发按钮")
	}

	s.mu.Lock()
	s.messages = []Message{{ID: "m1", Role: "user", Text: "hello"}}
	s.mu.Unlock()
	tt.Frame()

	if !tt.HasText("转发到新会话") {
		t.Fatalf("有消息时用量浮层应显示「转发到新会话」，实际 %v", tt.Texts())
	}
	shortcutText := shortcutKeysDisplay("forward-thread", "⇧⌘D")
	if !tt.HasText(shortcutText) {
		t.Fatalf("用量浮层转发操作应展示快捷键 %q，实际 %v", shortcutText, tt.Texts())
	}
	if err := tt.Click("转发到新会话"); err != nil {
		t.Fatalf("点击用量浮层转发按钮失败: %v", err)
	}
	if s.composerView.usageOpen {
		t.Fatalf("点击转发后应关闭上下文用量浮层")
	}
	waitFor(t, tt, func() bool {
		return forwardCallsContain(fake, "t1")
	})
}

// TestSlashForwardLocalCommand 验证 /forward 为无参数本地命令、别名 handoff/fw 能命中补全，且执行触发转发并清空草稿。
func TestSlashForwardLocalCommand(t *testing.T) {
	testenv.TempHome(t)

	if !isNoArgLocalSlashCommand("/forward") {
		t.Fatalf("/forward 应被识别为无参数本地命令")
	}

	cmds := []SlashCommand{
		{Name: "compact", Detail: "Summarize"},
		{Name: "forward", Detail: "Forward conversation to a new thread"},
		{Name: "clear", Detail: "Clear"},
	}
	for _, alias := range []string{"handoff", "fw"} {
		matched := filterCommands(cmds, alias, 8)
		if len(matched) == 0 || matched[0].Name != "forward" {
			t.Fatalf("别名 %q 应匹配到 /forward，实际 %+v", alias, matched)
		}
	}

	fake := NewFakeClient()
	s := newChatState(fake)
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "旧会话"}}
	s.draft = "/forward"
	s.mu.Unlock()

	if ok := s.runLocalSlashCommand("t1", "/forward"); !ok {
		t.Fatalf("runLocalSlashCommand 应拦截 /forward")
	}
	s.mu.Lock()
	draftAfter := s.draft
	s.mu.Unlock()
	if strings.TrimSpace(draftAfter) != "" {
		t.Fatalf("执行 /forward 后应清空草稿，实际 %q", draftAfter)
	}

	tt := ui.NewTester(func(c *ui.Context) {}, 100, 100)
	waitFor(t, tt, func() bool {
		return forwardCallsContain(fake, "t1")
	})
}
