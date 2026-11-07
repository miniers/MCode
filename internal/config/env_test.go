package config

import (
	"testing"
)

// 7. ApplyEnv 对每个支持的 MCODE_* 变量生效，未设置时不改值。
func TestApplyEnv(t *testing.T) {
	t.Run("overrides present and non-empty env vars", func(t *testing.T) {
		cfg := Defaults()

		envMap := map[string]string{
			"MCODE_PROVIDER":   "codex",
			"MCODE_MODE":       "readonly",
			"MCODE_CLAUDE_BIN": "/custom/bin/claude",
			"MCODE_CODEX_BIN":  "/custom/bin/codex",
			"MCODE_LISTEN":     "192.168.1.100:9999",
		}

		lookup := func(key string) (string, bool) {
			v, ok := envMap[key]
			return v, ok
		}

		ApplyEnv(&cfg, lookup)

		if cfg.Models.Provider != "codex" {
			t.Errorf("expected General.Provider 'codex', got %q", cfg.Models.Provider)
		}
		if cfg.Permissions.Mode != ModeReadOnly {
			t.Errorf("expected General.PermissionMode %q, got %q", ModeReadOnly, cfg.Permissions.Mode)
		}
		if cfg.Providers.Claude.Bin != "/custom/bin/claude" {
			t.Errorf("expected Providers.Claude.Bin '/custom/bin/claude', got %q", cfg.Providers.Claude.Bin)
		}
		if cfg.Providers.Codex.Bin != "/custom/bin/codex" {
			t.Errorf("expected Providers.Codex.Bin '/custom/bin/codex', got %q", cfg.Providers.Codex.Bin)
		}
		if cfg.Remote.Listen != "192.168.1.100:9999" {
			t.Errorf("expected Remote.Listen '192.168.1.100:9999', got %q", cfg.Remote.Listen)
		}
	})

	t.Run("leaves values untouched when env var is absent or empty", func(t *testing.T) {
		cfg := Defaults()
		cfg.Models.Provider = "memory"
		cfg.Permissions.Mode = ModeFullAccess
		cfg.Providers.Claude.Bin = "/default/claude"
		cfg.Providers.Codex.Bin = "/default/codex"
		cfg.Remote.Listen = DefaultRemoteListen

		envMap := map[string]string{
			"MCODE_PROVIDER":   "", // 空值不覆盖
			"MCODE_CLAUDE_BIN": "", // 空值不覆盖
			// MCODE_MODE, MCODE_CODEX_BIN, MCODE_LISTEN 缺失
		}

		lookup := func(key string) (string, bool) {
			v, ok := envMap[key]
			return v, ok
		}

		ApplyEnv(&cfg, lookup)

		if cfg.Models.Provider != "memory" {
			t.Errorf("empty MCODE_PROVIDER should not override, got %q", cfg.Models.Provider)
		}
		if cfg.Permissions.Mode != ModeFullAccess {
			t.Errorf("absent MCODE_MODE should not override, got %q", cfg.Permissions.Mode)
		}
		if cfg.Providers.Claude.Bin != "/default/claude" {
			t.Errorf("empty MCODE_CLAUDE_BIN should not override, got %q", cfg.Providers.Claude.Bin)
		}
		if cfg.Providers.Codex.Bin != "/default/codex" {
			t.Errorf("absent MCODE_CODEX_BIN should not override, got %q", cfg.Providers.Codex.Bin)
		}
		if cfg.Remote.Listen != DefaultRemoteListen {
			t.Errorf("absent MCODE_LISTEN should not override, got %q", cfg.Remote.Listen)
		}
	})

	t.Run("handles nil config safely", func(t *testing.T) {
		// 不应发生 panic
		ApplyEnv(nil, func(key string) (string, bool) {
			return "value", true
		})
	})
}
