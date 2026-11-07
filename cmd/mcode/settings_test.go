package main

import (
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/config"
)

func TestSettingsResolution(t *testing.T) {
	// 1. 三者都没有时用默认值
	t.Run("defaults when no config, no env, no flags", func(t *testing.T) {
		emptyDir := t.TempDir()
		cfg, s, err := loadSettings(emptyDir, map[string]bool{}, flagValues{
			Provider:  "claude",
			Mode:      "fullAccess",
			ClaudeBin: "claude",
			CodexBin:  "codex",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		defaults := config.Defaults()
		if s.Provider != defaults.Models.Provider {
			t.Fatalf("expected provider %q, got %q", defaults.Models.Provider, s.Provider)
		}
		if s.Mode != defaults.Permissions.Mode {
			t.Fatalf("expected mode %q, got %q", defaults.Permissions.Mode, s.Mode)
		}
		if s.ClaudeBin != "claude" {
			t.Fatalf("expected claudeBin %q, got %q", "claude", s.ClaudeBin)
		}
		if s.CodexBin != "codex" {
			t.Fatalf("expected codexBin %q, got %q", "codex", s.CodexBin)
		}
		_ = cfg
	})

	// 2. 只有配置文件时用配置的值
	t.Run("config file values when no env and no flags", func(t *testing.T) {
		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.json")
		configData := `{
  "version": 1,
  "general": {
    "provider": "codex",
    "permission_mode": "readonly"
  },
  "providers": {
    "claude": {"enabled": true, "bin": "/custom/bin/claude"},
    "codex": {"enabled": true, "bin": "/custom/bin/codex"}
  }
}`
		if err := os.WriteFile(configFile, []byte(configData), 0o600); err != nil {
			t.Fatalf("failed to write config.json: %v", err)
		}

		_, s, err := loadSettings(dir, map[string]bool{}, flagValues{
			Provider:  "claude",
			Mode:      "fullAccess",
			ClaudeBin: "claude",
			CodexBin:  "codex",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if s.Provider != "codex" {
			t.Fatalf("expected provider %q, got %q", "codex", s.Provider)
		}
		if s.Mode != "readonly" {
			t.Fatalf("expected mode %q, got %q", "readonly", s.Mode)
		}
		if s.ClaudeBin != "/custom/bin/claude" {
			t.Fatalf("expected claudeBin %q, got %q", "/custom/bin/claude", s.ClaudeBin)
		}
		if s.CodexBin != "/custom/bin/codex" {
			t.Fatalf("expected codexBin %q, got %q", "/custom/bin/codex", s.CodexBin)
		}
	})

	// 3. 同时设了环境变量时用环境变量
	t.Run("env overrides config file when no flags", func(t *testing.T) {
		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.json")
		configData := `{
  "version": 1,
  "general": {
    "provider": "codex",
    "permission_mode": "readonly"
  },
  "providers": {
    "claude": {"enabled": true, "bin": "/custom/bin/claude"},
    "codex": {"enabled": true, "bin": "/custom/bin/codex"}
  }
}`
		if err := os.WriteFile(configFile, []byte(configData), 0o600); err != nil {
			t.Fatalf("failed to write config.json: %v", err)
		}

		t.Setenv("MCODE_PROVIDER", "memory")
		t.Setenv("MCODE_MODE", "supervised")
		t.Setenv("MCODE_CLAUDE_BIN", "/env/bin/claude")
		t.Setenv("MCODE_CODEX_BIN", "/env/bin/codex")

		_, s, err := loadSettings(dir, map[string]bool{}, flagValues{
			Provider:  "claude",
			Mode:      "fullAccess",
			ClaudeBin: "claude",
			CodexBin:  "codex",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if s.Provider != "memory" {
			t.Fatalf("expected provider %q, got %q", "memory", s.Provider)
		}
		if s.Mode != "supervised" {
			t.Fatalf("expected mode %q, got %q", "supervised", s.Mode)
		}
		if s.ClaudeBin != "/env/bin/claude" {
			t.Fatalf("expected claudeBin %q, got %q", "/env/bin/claude", s.ClaudeBin)
		}
		if s.CodexBin != "/env/bin/codex" {
			t.Fatalf("expected codexBin %q, got %q", "/env/bin/codex", s.CodexBin)
		}
	})

	// 4. 显式传了 flag 时用 flag
	t.Run("flags override both env and config file", func(t *testing.T) {
		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.json")
		configData := `{
  "version": 1,
  "general": {
    "provider": "codex",
    "permission_mode": "readonly"
  },
  "providers": {
    "claude": {"enabled": true, "bin": "/custom/bin/claude"},
    "codex": {"enabled": true, "bin": "/custom/bin/codex"}
  }
}`
		if err := os.WriteFile(configFile, []byte(configData), 0o600); err != nil {
			t.Fatalf("failed to write config.json: %v", err)
		}

		t.Setenv("MCODE_PROVIDER", "memory")
		t.Setenv("MCODE_MODE", "supervised")
		t.Setenv("MCODE_CLAUDE_BIN", "/env/bin/claude")
		t.Setenv("MCODE_CODEX_BIN", "/env/bin/codex")

		setFlags := map[string]bool{
			"provider":   true,
			"mode":       true,
			"claude-bin": true,
			"codex-bin":  true,
		}
		flags := flagValues{
			Provider:  "claude",
			Mode:      "autoAcceptEdits",
			ClaudeBin: "/flag/bin/claude",
			CodexBin:  "/flag/bin/codex",
		}

		_, s, err := loadSettings(dir, setFlags, flags)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if s.Provider != "claude" {
			t.Fatalf("expected provider %q, got %q", "claude", s.Provider)
		}
		if s.Mode != "autoAcceptEdits" {
			t.Fatalf("expected mode %q, got %q", "autoAcceptEdits", s.Mode)
		}
		if s.ClaudeBin != "/flag/bin/claude" {
			t.Fatalf("expected claudeBin %q, got %q", "/flag/bin/claude", s.ClaudeBin)
		}
		if s.CodexBin != "/flag/bin/codex" {
			t.Fatalf("expected codexBin %q, got %q", "/flag/bin/codex", s.CodexBin)
		}
	})

	// 5. 部分传入 flag 时，未传的按 env/config 回退
	t.Run("partial flags only override specified keys", func(t *testing.T) {
		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.json")
		configData := `{
  "version": 1,
  "general": {
    "provider": "codex",
    "permission_mode": "readonly"
  },
  "providers": {
    "claude": {"enabled": true, "bin": "/custom/bin/claude"},
    "codex": {"enabled": true, "bin": "/custom/bin/codex"}
  }
}`
		if err := os.WriteFile(configFile, []byte(configData), 0o600); err != nil {
			t.Fatalf("failed to write config.json: %v", err)
		}

		t.Setenv("MCODE_MODE", "supervised")

		setFlags := map[string]bool{
			"provider": true,
		}
		flags := flagValues{
			Provider:  "memory",
			Mode:      "auto",
			ClaudeBin: "/ignored/claude",
			CodexBin:  "/ignored/codex",
		}

		_, s, err := loadSettings(dir, setFlags, flags)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if s.Provider != "memory" {
			t.Fatalf("expected provider %q from flag, got %q", "memory", s.Provider)
		}
		if s.Mode != "supervised" {
			t.Fatalf("expected mode %q from env, got %q", "supervised", s.Mode)
		}
		if s.ClaudeBin != "/custom/bin/claude" {
			t.Fatalf("expected claudeBin %q from config, got %q", "/custom/bin/claude", s.ClaudeBin)
		}
	})
}

// finderCall 记录桩 finder 收到的一次调用。
type finderCall struct {
	names        []string
	fallbackDirs []string
}

// stubProviderBinaryFinder 用桩替换 providerBinaryFinder：available 中的候选视为可解析到，
// 其余视为缺失，并记录每次调用；测试结束自动恢复原实现。
func stubProviderBinaryFinder(t *testing.T, available ...string) *[]finderCall {
	t.Helper()
	found := make(map[string]bool, len(available))
	for _, name := range available {
		found[name] = true
	}
	calls := &[]finderCall{}
	original := providerBinaryFinder
	providerBinaryFinder = func(names, fallbackDirs []string) (string, bool) {
		*calls = append(*calls, finderCall{
			names:        append([]string(nil), names...),
			fallbackDirs: append([]string(nil), fallbackDirs...),
		})
		for _, name := range names {
			if found[name] {
				return "/stub/" + name, true
			}
		}
		return "", false
	}
	t.Cleanup(func() { providerBinaryFinder = original })
	return calls
}

func TestProviderBinaryAvailable(t *testing.T) {
	t.Run("configured absolute path that is executable", func(t *testing.T) {
		calls := stubProviderBinaryFinder(t)
		bin := filepath.Join(t.TempDir(), "claude")
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatalf("failed to write stub binary: %v", err)
		}

		if !providerBinaryAvailable(bin, "claude") {
			t.Fatalf("expected configured path %q to be reported available", bin)
		}
		if len(*calls) != 0 {
			t.Fatalf("expected the configured path to be checked without the finder, got %+v", *calls)
		}
	})

	t.Run("configured absolute path that does not exist falls back to names", func(t *testing.T) {
		calls := stubProviderBinaryFinder(t, "claude")
		missing := filepath.Join(t.TempDir(), "claude")

		if !providerBinaryAvailable(missing, "claude") {
			t.Fatalf("expected the fallback name %q to be found", "claude")
		}
		if len(*calls) != 1 || len((*calls)[0].names) != 1 || (*calls)[0].names[0] != "claude" {
			t.Fatalf("expected a single lookup of %q, got %+v", "claude", *calls)
		}
	})

	t.Run("configured command name is looked up before names", func(t *testing.T) {
		calls := stubProviderBinaryFinder(t, "claude-custom")

		if !providerBinaryAvailable("claude-custom", "claude") {
			t.Fatalf("expected configured command %q to be found", "claude-custom")
		}
		if len(*calls) != 1 || (*calls)[0].names[0] != "claude-custom" {
			t.Fatalf("expected a single lookup of the configured command, got %+v", *calls)
		}
	})

	t.Run("configured command name missing falls back to names", func(t *testing.T) {
		calls := stubProviderBinaryFinder(t, "claude")

		if !providerBinaryAvailable("claude-custom", "claude") {
			t.Fatalf("expected fallback name %q to be found", "claude")
		}
		if len(*calls) != 2 {
			t.Fatalf("expected the configured command then the fallback name to be looked up, got %+v", *calls)
		}
		if (*calls)[0].names[0] != "claude-custom" || (*calls)[1].names[0] != "claude" {
			t.Fatalf("unexpected lookup order: %+v", *calls)
		}
	})

	t.Run("nothing available", func(t *testing.T) {
		calls := stubProviderBinaryFinder(t)

		if providerBinaryAvailable("", "claude", "codex") {
			t.Fatal("expected no available binary")
		}
		if len(*calls) != 1 || len((*calls)[0].names) != 2 {
			t.Fatalf("expected one lookup of both names, got %+v", *calls)
		}
	})

	t.Run("no configured path and no names", func(t *testing.T) {
		calls := stubProviderBinaryFinder(t, "claude")

		if providerBinaryAvailable("") {
			t.Fatal("expected no available binary without candidates")
		}
		if len(*calls) != 0 {
			t.Fatalf("expected the finder to be skipped, got %+v", *calls)
		}
	})
}
