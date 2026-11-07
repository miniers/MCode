package mcodeui

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// newSendKeyTestState 给会话注册一个发送键已固定的设置状态：
// 有设置状态时 composerSendKey 只读内存配置，不会回退去读本机的持久化配置。
func newSendKeyTestState(s *chatState, sendKey string) {
	st := &SettingsState{}
	// 用默认配置打底，避免空 cfg 影响其他设置项在渲染时的取值。
	st.cfg = config.Defaults()
	st.cfg.Session.ComposerSendKey = sendKey
	setChatSettings(s, st)
}

// clearSendKeyTestState 注销测试期间注册的设置状态，避免影响同包其他用例。
func clearSendKeyTestState(t *testing.T, s *chatState) {
	t.Helper()
	t.Cleanup(func() {
		settingsMu.Lock()
		delete(settingsList, s)
		settingsMu.Unlock()
	})
}

// TestComposerSendMods 覆盖两种发送键对应的修饰键：Enter 不需要修饰键，Shift+Enter 匹配 Shift。
func TestComposerSendMods(t *testing.T) {
	if got := composerSendMods(config.ComposerSendKeyEnter); got != 0 {
		t.Fatalf("Enter 发送不应要求修饰键，got %v", got)
	}
	if got := composerSendMods(config.ComposerSendKeyShiftEnter); got != ui.Shift {
		t.Fatalf("Shift+Enter 发送应匹配 ui.Shift，got %v", got)
	}
	// 空值与非法值都按 Enter 发送处理。
	if got := composerSendMods(""); got != 0 {
		t.Fatalf("空发送键应按 Enter 处理，got %v", got)
	}
	if got := composerSendMods("shift"); got != 0 {
		t.Fatalf("非法发送键应按 Enter 处理，got %v", got)
	}
}

// TestComposerSendKeyFromSettings 覆盖发送键的读取：优先读设置面板内存配置，非法值归为 Enter。
func TestComposerSendKeyFromSettings(t *testing.T) {
	state := &chatState{}
	newSendKeyTestState(state, config.ComposerSendKeyShiftEnter)
	clearSendKeyTestState(t, state)

	if got := state.composerSendKey(); got != config.ComposerSendKeyShiftEnter {
		t.Fatalf("应读到 Shift+Enter 发送，got %q", got)
	}

	newSendKeyTestState(state, "bogus")
	if got := state.composerSendKey(); got != config.ComposerSendKeyEnter {
		t.Fatalf("非法发送键应归为 Enter 发送，got %q", got)
	}

	// 没有会话状态时按 Enter 发送。
	var nilState *chatState
	if got := nilState.composerSendKey(); got != config.ComposerSendKeyEnter {
		t.Fatalf("无会话状态时应按 Enter 发送，got %q", got)
	}
}

// 场景 1：默认配置下 Enter 发送，Shift+Enter 只换行。
func TestRootSendKeyEnterSends(t *testing.T) {
	fake := NewFakeClient()
	var state *chatState
	tt := ui.NewTester(Root(fake, func(s *chatState) {
		state = s
		newSendKeyTestState(s, config.ComposerSendKeyEnter)
	}), 1000, 700)
	clearSendKeyTestState(t, state)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	fake.ResetCalls()

	tt.Type("hello enter key")
	// Shift+Enter 在默认配置下只换行。
	tt.Key(ui.Shift, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return strings.Contains(state.draft, "\n")
	})
	if fake.SendCalls() != 0 {
		t.Fatalf("默认配置下 Shift+Enter 不应发送，got %d", fake.SendCalls())
	}

	// Enter 发送，尾随换行被去掉。
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		return fake.LastSentText() == "hello enter key"
	})
}

// 场景 2：切到 Shift+Enter 发送后，普通 Enter 只换行，Shift+Enter 才发送。
func TestRootSendKeyShiftEnterSends(t *testing.T) {
	fake := NewFakeClient()
	var state *chatState
	tt := ui.NewTester(Root(fake, func(s *chatState) {
		state = s
		newSendKeyTestState(s, config.ComposerSendKeyShiftEnter)
	}), 1000, 700)
	clearSendKeyTestState(t, state)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	fake.ResetCalls()

	tt.Type("hello shift enter")
	// 普通 Enter 只换行，不发送。
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return strings.Contains(state.draft, "\n")
	})
	if fake.SendCalls() != 0 {
		t.Fatalf("Shift+Enter 发送模式下普通 Enter 不应发送，got %d", fake.SendCalls())
	}

	// Shift+Enter 发送，尾随换行被去掉。
	tt.Key(ui.Shift, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		return fake.LastSentText() == "hello shift enter"
	})
}
