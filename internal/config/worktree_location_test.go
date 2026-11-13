package config

import "testing"

func TestNormalizeWorktreeLocation(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"", WorktreeLocationCentral}, {"project", WorktreeLocationProject},
		{" project\t", WorktreeLocationProject}, {"unknown", WorktreeLocationCentral},
	} {
		cfg := Defaults()
		cfg.SourceControl.WorktreeLocation = tc.input
		if got := Normalize(cfg).SourceControl.WorktreeLocation; got != tc.want {
			t.Errorf("输入 %q：得到 %q，期望 %q", tc.input, got, tc.want)
		}
	}
	if got := Defaults().SourceControl.WorktreeLocation; got != WorktreeLocationCentral {
		t.Fatalf("默认应为集中存储：%q", got)
	}
}

func TestWorktreeLocationConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, location := range []string{WorktreeLocationProject, WorktreeLocationCentral} {
		cfg := Defaults()
		cfg.SourceControl.WorktreeLocation = location
		if err := Save(dir, cfg); err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.SourceControl.WorktreeLocation != location {
			t.Fatalf("配置往返后不一致：%+v", loaded.SourceControl)
		}
	}
}
