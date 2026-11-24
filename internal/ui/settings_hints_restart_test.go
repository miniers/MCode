package mcodeui

import (
	"strings"
	"testing"
)

func TestRestartHint_SettingHints(t *testing.T) {
	keys := []string{
		"row-claude-bin",
		"row-codex-bin",
		"row-provider-auth",
	}
	for _, key := range keys {
		got := settingHint(key)
		if !strings.Contains(got, "必须重启应用") {
			t.Errorf("settingHint(%q) = %q, want containing %q", key, got, "必须重启应用")
		}
	}
}

func TestRestartHint_ProviderNeedsRestart(t *testing.T) {
	wantTrue := []string{"droid", "cline", "cursor", "opencode", "grok", "devin"}
	for _, id := range wantTrue {
		if !providerNeedsRestart(id) {
			t.Errorf("providerNeedsRestart(%q) = false, want true", id)
		}
	}

	wantFalse := []string{"claude", "codex", "agy"}
	for _, id := range wantFalse {
		if providerNeedsRestart(id) {
			t.Errorf("providerNeedsRestart(%q) = true, want false", id)
		}
	}
}
