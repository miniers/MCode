package magpiecfg

import (
	"os"
	"path/filepath"
	"testing"
)

// withClaudeSandbox 把 home 和 CLAUDE_CONFIG_DIR 指到临时位置，返回 home。
// 这些是包级变量，涉及它们的测试必须串行（不能 t.Parallel）。
func withClaudeSandbox(t *testing.T) (home string) {
	t.Helper()
	home = t.TempDir()
	oldHome, oldLookup, oldOS := homeDir, lookupEnv, hostOS
	homeDir = func() string { return home }
	lookupEnv = func(key string) (string, bool) {
		if key == "CLAUDE_CONFIG_DIR" {
			return "", false
		}
		return os.LookupEnv(key)
	}
	t.Cleanup(func() { homeDir, lookupEnv, hostOS = oldHome, oldLookup, oldOS })
	return home
}

func TestClaudeSettingsPath(t *testing.T) {
	home := withClaudeSandbox(t)

	t.Run("CLAUDE_CONFIG_DIR 是绝对路径", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "claude-cfg")
		lookupEnv = func(key string) (string, bool) {
			if key == "CLAUDE_CONFIG_DIR" {
				return dir, true
			}
			return "", false
		}
		got, err := ClaudeSettingsPath()
		if err != nil {
			t.Fatalf("ClaudeSettingsPath: %v", err)
		}
		if want := filepath.Join(dir, "settings.json"); got != want {
			t.Fatalf("ClaudeSettingsPath() = %q，想要 %q", got, want)
		}
	})

	t.Run("CLAUDE_CONFIG_DIR 是相对路径时忽略", func(t *testing.T) {
		lookupEnv = func(key string) (string, bool) {
			if key == "CLAUDE_CONFIG_DIR" {
				return "relative/cfg", true
			}
			return "", false
		}
		got, err := ClaudeSettingsPath()
		if err != nil {
			t.Fatalf("ClaudeSettingsPath: %v", err)
		}
		if want := filepath.Join(home, ".claude", "settings.json"); got != want {
			t.Fatalf("ClaudeSettingsPath() = %q，想要 %q", got, want)
		}
	})

	t.Run("未设置时用 home", func(t *testing.T) {
		lookupEnv = func(string) (string, bool) { return "", false }
		got, err := ClaudeSettingsPath()
		if err != nil {
			t.Fatalf("ClaudeSettingsPath: %v", err)
		}
		if want := filepath.Join(home, ".claude", "settings.json"); got != want {
			t.Fatalf("ClaudeSettingsPath() = %q，想要 %q", got, want)
		}
	})
}

func TestClaudeManagedSettingsPath(t *testing.T) {
	oldOS := hostOS
	t.Cleanup(func() { hostOS = oldOS })

	hostOS = "windows"
	want := `C:\ProgramData\ClaudeCode\managed-settings.json`
	if got := ClaudeManagedSettingsPath(); got != want {
		t.Fatalf("windows 上 ClaudeManagedSettingsPath() = %q，想要 %q", got, want)
	}

	hostOS = "darwin"
	if got := ClaudeManagedSettingsPath(); got != "" {
		t.Fatalf("darwin 上 ClaudeManagedSettingsPath() = %q，想要空串", got)
	}
}
