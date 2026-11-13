//go:build windows

package app

import (
	"os"
	"reflect"
	"sync"
	"testing"

	"mcode/internal/config"
	"mcode/internal/providers/loginenv"
)

func TestReloadCommandShell(t *testing.T) {
	t.Cleanup(func() { loginenv.SetCommandShell("") })
	client := newRemoteSettingsTestClient(t, nil)
	for _, pref := range append([]string{""}, loginenv.AvailableCommandShells()...) {
		loginenv.SetCommandShell(pref)
		wantName, wantArgs, wantKind := loginenv.CommandShell("echo hi")
		loginenv.SetCommandShell("")
		if err := config.Update(client.dataDir, func(cfg *config.Config) error {
			cfg.Workbench.CommandShell = pref
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		client.ReloadCommandShell()
		name, args, kind := loginenv.CommandShell("echo hi")
		if name != wantName || kind != wantKind || !reflect.DeepEqual(args, wantArgs) {
			t.Fatalf("偏好 %q 未生效：%q %q %q", pref, name, args, kind)
		}
	}
	name, args, kind := loginenv.CommandShell("echo hi")
	if err := os.WriteFile(config.ConfigPath(client.dataDir), []byte("invalid-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	client.ReloadCommandShell()
	gotName, gotArgs, gotKind := loginenv.CommandShell("echo hi")
	if gotName != name || gotKind != kind || !reflect.DeepEqual(gotArgs, args) {
		t.Fatal("配置读取失败改变了当前 shell")
	}
}

func TestNotifyConfigChangedAppliesCommandShell(t *testing.T) {
	t.Cleanup(func() { loginenv.SetCommandShell("") })
	client := newRemoteSettingsTestClient(t, func(cfg *config.Config) {
		cfg.Workbench.CommandShell = "powershell"
	})
	loginenv.SetCommandShell("powershell")
	_, _, wantKind := loginenv.CommandShell("")
	loginenv.SetCommandShell("")
	called := false
	client.WatchConfig(func() {
		called = true
		if _, _, kind := loginenv.CommandShell(""); kind != wantKind {
			t.Errorf("配置订阅者收到通知时 shell 尚未生效：%q", kind)
		}
	})
	client.notifyConfigChanged()
	if !called {
		t.Fatal("配置订阅者未收到通知")
	}
}

func TestReloadCommandShellConcurrent(t *testing.T) {
	t.Cleanup(func() { loginenv.SetCommandShell("") })
	client := newRemoteSettingsTestClient(t, nil)
	var wg sync.WaitGroup
	for _, pref := range []string{"", "git-bash", "pwsh", "powershell"} {
		wg.Go(func() {
			for range 5 {
				if err := config.Update(client.dataDir, func(cfg *config.Config) error {
					cfg.Workbench.CommandShell = pref
					return nil
				}); err != nil {
					t.Error(err)
					return
				}
				client.ReloadCommandShell()
			}
		})
	}
	wg.Wait()
	gotName, gotArgs, gotKind := loginenv.CommandShell("echo hi")
	cfg, err := config.Load(client.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	loginenv.SetCommandShell(cfg.Workbench.CommandShell)
	wantName, wantArgs, wantKind := loginenv.CommandShell("echo hi")
	if gotName != wantName || gotKind != wantKind || !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("并发保存后生效值与磁盘配置不一致：%q %q %q", gotName, gotArgs, gotKind)
	}
}
