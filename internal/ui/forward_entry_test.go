package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/testenv"
)

// TestChatHeaderForwardButtonVisibilityAndAction 验证顶部栏「转发到新会话」一键按钮：
// 空会话和总管会话不显示；有消息的普通会话显示在新建会话旁，点击触发转发；转发中禁用防连点。
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
		t.Fatalf("空会话不应显示顶部栏转发按钮")
	}

	// 加入消息后显示，且转发进行中时按钮切换为「正在转发…」并禁用。
	s.mu.Lock()
	s.messages = []Message{{ID: "m1", Role: "user", Text: "hello"}}
	if s.forwardInflight == nil {
		s.forwardInflight = make(map[string]bool)
	}
	s.forwardInflight["t1"] = true
	s.mu.Unlock()
	tt.Frame()

	info, ok := tt.AccessByRole(ui.RoleButton, "正在转发…")
	if !ok {
		t.Fatalf("转发进行中顶部栏按钮应切换为「正在转发…」")
	}
	if !info.Disabled {
		t.Fatalf("转发进行中顶部栏按钮应处于禁用态")
	}

	// 空闲态显示正常文案，点击触发 ForwardThread。
	s.mu.Lock()
	s.forwardInflight["t1"] = false
	s.mu.Unlock()
	tt.Frame()

	if _, ok := tt.Find(fwdLabel); !ok {
		t.Fatalf("有消息的会话应显示顶部栏转发按钮 %q", fwdLabel)
	}
	if err := tt.Click(fwdLabel); err != nil {
		t.Fatalf("点击顶部栏转发按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return forwardCallsContain(fake, "t1")
	})

	// 总管会话即使有消息也不显示顶部栏转发按钮。
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
	if _, ok := ttSteward.Find(fwdLabel); ok {
		t.Fatalf("总管会话不应显示顶部栏转发按钮")
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
