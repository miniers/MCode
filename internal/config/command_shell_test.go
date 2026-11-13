package config

import "testing"

func TestNormalizeCommandShell(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", ""}, {"git-bash", "git-bash"}, {" pwsh\t", "pwsh"},
		{" powershell ", "powershell"}, {"unknown", ""}, {"bash", ""},
	} {
		cfg := Defaults()
		cfg.Workbench.CommandShell = tc.input
		if got := Normalize(cfg).Workbench.CommandShell; got != tc.want {
			t.Errorf("输入 %q：得到 %q，期望 %q", tc.input, got, tc.want)
		}
	}
	if got := Defaults().Workbench.CommandShell; got != "" {
		t.Errorf("默认偏好应为自动：%q", got)
	}
}

func TestCommandShellConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, pref := range []string{"", "git-bash", "pwsh", "powershell"} {
		cfg := Defaults()
		cfg.Workbench.CommandShell = pref
		cfg.Workbench.TerminalShell = "external-terminal"
		if err := Save(dir, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Workbench.CommandShell != pref || loaded.Workbench.TerminalShell != "external-terminal" {
			t.Fatalf("配置往返后不一致：%+v", loaded.Workbench)
		}
	}
}
