package mcodeui

import (
	"strconv"
	"testing"
	"time"

	"mcode/internal/config"
)

// TestSettingsDraftResync_ResetRemotePage 验证远程控制页修改监听端口并「恢复本页默认」后，
// 监听端口输入框显示的是默认端口而不是旧草稿。
func TestSettingsDraftResync_ResetRemotePage(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	tt, st := openRemoteSettingsPageState(t, fake, dataDir)
	tt.SetSize(1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("监听端口") && tt.HasText("恢复本页默认") })

	clickRemoteSettingsLabel(t, tt, "编辑监听端口")
	tt.Command("selectAll")
	tt.Type("50000")
	tt.Advance(600 * time.Millisecond)
	waitFor(t, tt, func() bool {
		loaded, err := config.Load(dataDir)
		return err == nil && loaded.Remote.Listen == "0.0.0.0:50000"
	})

	openSettingsResetDialog(t, tt, st, "remote")
	confirmSettingsReset(t, tt, st)

	wantPort := strconv.Itoa(remoteListenPort(config.DefaultRemoteListen))
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.remote.portInit && st.remote.portText == wantPort
	})
	tt.Frame()

	clickRemoteSettingsLabel(t, tt, "编辑监听端口")
	tt.Command("selectAll")
	tt.Command("copy")
	if got := tt.Clipboard(); got != wantPort {
		t.Fatalf("重置后监听端口输入框 = %q，期望 %q", got, wantPort)
	}
}

// TestSettingsDraftResync_OpenReloadsGlobalShortcut 验证磁盘上的全局快捷键被外部修改后，
// 重新调用 Open() 会把 *st.globalShortcutDraft 同步为最新值。
func TestSettingsDraftResync_OpenReloadsGlobalShortcut(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	st.Open()

	const want = "CmdOrCtrl+Shift+K"
	if err := config.Update(dataDir, func(cfg *config.Config) error {
		cfg.Shortcuts.GlobalShortcut = want
		return nil
	}); err != nil {
		t.Fatalf("config.Update 失败: %v", err)
	}

	st.Open()

	st.cfgMu.Lock()
	var got string
	if st.globalShortcutDraft != nil {
		got = *st.globalShortcutDraft
	}
	st.cfgMu.Unlock()
	if got != want {
		t.Fatalf("*st.globalShortcutDraft = %q，期望 %q", got, want)
	}
}
