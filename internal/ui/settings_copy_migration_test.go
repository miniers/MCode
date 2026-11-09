package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsTLSCopyMigration(t *testing.T) {
	st := &SettingsState{}
	st.remote.tls.StartedURL = "https://mcode.example:8443"
	tester := ui.NewTester(func(c *ui.Context) {
		remoteTLSEntryControl{}.render(c, st, setting{})
	}, 500, 100)
	if tester.Clipboard() != "" {
		t.Fatal("点击前不应写入剪贴板")
	}
	if err := tester.Click("复制 HTTPS 入口"); err != nil {
		t.Fatal(err)
	}
	if got := tester.Clipboard(); got != st.remote.tls.StartedURL {
		t.Fatalf("复制的 TLS 入口 = %q", got)
	}
}

func TestSettingsRemoteEntryCopyMigration(t *testing.T) {
	if remoteCurrentLANIP() == "" {
		t.Skip("当前没有局域网地址，入口复制按钮不可用")
	}
	st := &SettingsState{}
	st.remote.status = RemoteStatus{State: RemoteStateRunning, Port: 43117, StartedListen: "192.0.2.1:43117"}
	tester := ui.NewTester(func(c *ui.Context) {
		remoteEntryControl{}.render(c, st, setting{})
	}, 600, 100)
	if tester.Clipboard() != "" {
		t.Fatal("点击前不应写入剪贴板")
	}
	if err := tester.Click("复制入口地址"); err != nil {
		t.Fatal(err)
	}
	if got := tester.Clipboard(); got != "http://192.0.2.1:43117" {
		t.Fatalf("复制的远程入口 = %q", got)
	}
}

func TestSettingsArchiveEmptyMigration(t *testing.T) {
	resetArchivePageStateForTest()
	t.Cleanup(resetArchivePageStateForTest)
	globalArchiveState.mu.Lock()
	globalArchiveState.loaded = true
	globalArchiveState.mu.Unlock()
	tester := ui.NewTester(func(c *ui.Context) {
		renderPageArchive(c, nil)
	}, 1000, 1000)
	if !tester.HasText("暂无已归档会话") {
		t.Fatalf("未显示归档空态标题：%v", tester.Texts())
	}
}
