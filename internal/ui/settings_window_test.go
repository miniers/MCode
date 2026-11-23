package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsWindowInlineRendersInTests(t *testing.T) {
	resetSettingsWindowForTest()
	t.Cleanup(resetSettingsWindowForTest)
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(settingsTestDir(t), nil)), 1000, 3000)

	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("点击设置失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("通用") })
}

func TestSettingsWindowHeadlessOpenDoesNotBlock(t *testing.T) {
	resetSettingsWindowForTest()
	t.Cleanup(resetSettingsWindowForTest)
	owner := newChatState(NewFakeClient())
	defer owner.close()
	st := NewSettingsState(settingsTestDir(t), nil, owner)
	setChatSettings(owner, st)

	openSettingsWindow(owner, "")
	if currentSettingsWindowState() != getChatSettings(owner) {
		t.Fatal("设置窗口应使用所属窗口的设置状态")
	}
	settingsWindowMu.Lock()
	entry := settingsWindow
	settingsWindowMu.Unlock()

	openSettingsWindow(owner, "appearance")
	settingsWindowMu.Lock()
	reused := settingsWindow == entry
	settingsWindowMu.Unlock()
	if !reused {
		t.Fatal("再次打开设置时应复用单例")
	}
	st.mu.Lock()
	page := st.currentPage
	st.mu.Unlock()
	if page != "appearance" {
		t.Fatalf("设置页 = %q，期望 appearance", page)
	}
}

func TestSettingsWindowRejectsMissingOwner(t *testing.T) {
	if openSettingsWindow(nil, "") != nil {
		t.Fatal("缺少所属窗口时不应创建设置窗口")
	}
	owner := newChatState(NewFakeClient())
	defer owner.close()
	if openSettingsWindow(owner, "") != nil {
		t.Fatal("缺少设置状态时不应创建设置窗口")
	}
}

func TestSettingsWindowCloseUnrelatedOwner(t *testing.T) {
	if !closeSettingsWindowForOwner(newChatState(NewFakeClient())) {
		t.Fatal("没有关联窗口时应允许关闭")
	}
}
