package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// 开关点击后要把值写回 st.cfg 并落盘，且写入时持锁、save 在锁外。
// mygo 的 Tester.Click 按显示文本或 Label 匹配，不认组件 key，所以这里按 Label 点击。
const (
	confirmDeleteSwitchLabel  = "切换删除会话前确认"
	notifyFinishedSwitchLabel = "切换完成后通知"
)

// TestConfirmDeleteSwitchWritesConfig 覆盖「删除会话前确认」开关：默认开启，
// 点一下变成关闭并写进 config.json，再点一下恢复开启。
func TestConfirmDeleteSwitchWritesConfig(t *testing.T) {
	s := newChatState(NewFakeClient())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, s)
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageConversation(c, st)
	}, 1000, 2200)

	if !st.cfg.Archive.ConfirmBeforeDeleting {
		t.Fatalf("默认应显示为开启")
	}
	if err := tt.Click(confirmDeleteSwitchLabel); err != nil {
		t.Fatalf("关闭删除会话前确认失败(%s): %v", confirmDeleteSwitchLabel, err)
	}
	waitFor(t, tt, func() bool { return !st.cfg.Archive.ConfirmBeforeDeleting })
	settingsSaveWriters.Wait()

	saved, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if saved.Archive.ConfirmBeforeDeleting {
		t.Fatalf("开关关闭后未写入 config.json: %+v", saved.Archive)
	}

	if err := tt.Click(confirmDeleteSwitchLabel); err != nil {
		t.Fatalf("重新打开删除会话前确认失败(%s): %v", confirmDeleteSwitchLabel, err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Archive.ConfirmBeforeDeleting })
	settingsSaveWriters.Wait()

	saved, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if !saved.Archive.ConfirmBeforeDeleting {
		t.Fatalf("开关重新打开后未写入 config.json: %+v", saved.Archive)
	}
}

// TestNotifySwitchWritesConfig 覆盖「完成后通知」开关：默认开启，
// 点一下变成关闭并写进 config.json，再点一下恢复开启。
func TestNotifySwitchWritesConfig(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	dataDir := st.dataDir
	tt := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 2400)

	if !st.cfg.Notifications.NotifyWhenFinished {
		t.Fatalf("默认应显示为开启")
	}
	if !tt.HasText("完成后通知") {
		t.Fatalf("通知分节缺少开关行: %v", tt.Texts())
	}
	if err := tt.Click(notifyFinishedSwitchLabel); err != nil {
		t.Fatalf("关闭完成后通知失败(%s): %v", notifyFinishedSwitchLabel, err)
	}
	waitFor(t, tt, func() bool { return !st.cfg.Notifications.NotifyWhenFinished })
	settingsSaveWriters.Wait()

	saved, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if saved.Notifications.NotifyWhenFinished {
		t.Fatalf("开关关闭后未写入 config.json: %+v", saved.Notifications)
	}

	if err := tt.Click(notifyFinishedSwitchLabel); err != nil {
		t.Fatalf("重新打开完成后通知失败(%s): %v", notifyFinishedSwitchLabel, err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Notifications.NotifyWhenFinished })
	settingsSaveWriters.Wait()

	saved, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("读取已保存的配置失败: %v", err)
	}
	if !saved.Notifications.NotifyWhenFinished {
		t.Fatalf("开关重新打开后未写入 config.json: %+v", saved.Notifications)
	}
}
