package native

import (
	"testing"
)

func TestTrayMenu(t *testing.T) {
	menuWithoutOpts := makeTrayMenu(TrayOptions{}, 3)
	for _, item := range menuWithoutOpts.Items() {
		if item.Label == "检查更新…" {
			t.Fatal("makeTrayMenu(TrayOptions{}, 3) 菜单不应包含『检查更新…』")
		}
	}

	quitCalled := false
	opts := TrayOptions{
		OnQuit: func() {
			quitCalled = true
		},
	}
	menu := makeTrayMenu(opts, 3)

	var hasShowWindow, hasRunningCount, hasQuit bool
	for _, item := range menu.Items() {
		switch item.Label {
		case "检查更新…":
			t.Fatal("makeTrayMenu 菜单不应包含『检查更新…』")
		case "显示窗口":
			hasShowWindow = true
		case "正在运行 3 个会话":
			hasRunningCount = true
		case "退出 MCode":
			hasQuit = true
			if item.Click != nil {
				item.Click(item, nil)
			}
		}
	}

	if !hasShowWindow {
		t.Fatal("makeTrayMenu 菜单应包含『显示窗口』")
	}
	if !hasRunningCount {
		t.Fatal("makeTrayMenu 菜单应包含『正在运行 3 个会话』")
	}
	if !hasQuit {
		t.Fatal("makeTrayMenu 菜单应包含『退出 MCode』")
	}
	if !quitCalled {
		t.Fatal("点击『退出 MCode』应调用 OnQuit")
	}
}

func TestTrayMenuSettings(t *testing.T) {
	for _, item := range makeTrayMenu(TrayOptions{}, 0).Items() {
		if item.Label == "设置…" {
			t.Fatal("没有设置回调时不应显示设置菜单")
		}
	}

	called := false
	items := makeTrayMenu(TrayOptions{OnOpenSettings: func() { called = true }}, 0).Items()
	if len(items) < 2 || items[0].Label != "显示窗口" || items[1].Label != "设置…" || items[1].Click == nil {
		t.Fatal("设置菜单应位于显示窗口之后并带有回调")
	}
	items[1].Click(items[1], nil)
	if !called {
		t.Fatal("点击设置菜单应调用 OnOpenSettings")
	}
}
