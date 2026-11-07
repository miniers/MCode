package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mcode/internal/config"
)

func TestServerSettingsResolution(t *testing.T) {
	// 1. 三者都没有时用默认值
	t.Run("defaults when no config, no env, no flags", func(t *testing.T) {
		emptyDir := t.TempDir()
		cfg, s, err := loadSettings(emptyDir, map[string]bool{}, flagValues{
			Provider:  "claude",
			Mode:      "fullAccess",
			ClaudeBin: "claude",
			CodexBin:  "codex",
			Listen:    config.DefaultRemoteListen,
			Port:      0,
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
		if s.ListenAddr != defaults.Remote.Listen {
			t.Fatalf("expected listen %q, got %q", defaults.Remote.Listen, s.ListenAddr)
		}
		if len(s.ExtraHosts) != 0 {
			t.Fatalf("expected empty extra_hosts, got %v", s.ExtraHosts)
		}
		if !s.OpenAI.Enabled {
			t.Fatalf("expected default openAI.enabled true, got false")
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
    "codex": {"enabled": true, "bin": "/custom/bin/codex"},
    "acp": {
      "cursor": {"enabled": true, "bin": "/custom/bin/cursor-agent"}
    },
    "openai": {
      "enabled": true,
      "baseURL": "https://api.openai.example.com/v1",
      "model": "gpt-4o"
    }
  },
  "remote": {
    "listen": "0.0.0.0:9000",
    "extra_hosts": ["remote.internal", "code.example.com"]
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
			Listen:    config.DefaultRemoteListen,
			Port:      0,
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
		if s.ACP["cursor"].Bin != "/custom/bin/cursor-agent" {
			t.Fatalf("expected ACP cursor bin %q, got %q", "/custom/bin/cursor-agent", s.ACP["cursor"].Bin)
		}
		if s.ACPBins["cursor"] != "/custom/bin/cursor-agent" {
			t.Fatalf("expected ACPBins cursor %q, got %q", "/custom/bin/cursor-agent", s.ACPBins["cursor"])
		}
		if s.OpenAI.BaseURL != "https://api.openai.example.com/v1" {
			t.Fatalf("expected openAI baseURL %q, got %q", "https://api.openai.example.com/v1", s.OpenAI.BaseURL)
		}
		if s.OpenAI.Model != "gpt-4o" {
			t.Fatalf("expected openAI model %q, got %q", "gpt-4o", s.OpenAI.Model)
		}
		if s.ListenAddr != "0.0.0.0:9000" {
			t.Fatalf("expected listenAddr %q, got %q", "0.0.0.0:9000", s.ListenAddr)
		}
		expectedHosts := []string{"remote.internal", "code.example.com"}
		if !reflect.DeepEqual(s.ExtraHosts, expectedHosts) {
			t.Fatalf("expected extra_hosts %v, got %v", expectedHosts, s.ExtraHosts)
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
  },
  "remote": {
    "listen": "0.0.0.0:9000",
    "extra_hosts": ["remote.internal"]
  }
}`
		if err := os.WriteFile(configFile, []byte(configData), 0o600); err != nil {
			t.Fatalf("failed to write config.json: %v", err)
		}

		t.Setenv("MCODE_PROVIDER", "memory")
		t.Setenv("MCODE_MODE", "supervised")
		t.Setenv("MCODE_CLAUDE_BIN", "/env/bin/claude")
		t.Setenv("MCODE_CODEX_BIN", "/env/bin/codex")
		t.Setenv("MCODE_LISTEN", "127.0.0.1:9999")

		_, s, err := loadSettings(dir, map[string]bool{}, flagValues{
			Provider:  "claude",
			Mode:      "fullAccess",
			ClaudeBin: "claude",
			CodexBin:  "codex",
			Listen:    config.DefaultRemoteListen,
			Port:      0,
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
		if s.ListenAddr != "127.0.0.1:9999" {
			t.Fatalf("expected listenAddr %q, got %q", "127.0.0.1:9999", s.ListenAddr)
		}
		// extra_hosts 没有被环境变量覆盖，保持配置文件中的值
		expectedHosts := []string{"remote.internal"}
		if !reflect.DeepEqual(s.ExtraHosts, expectedHosts) {
			t.Fatalf("expected extra_hosts %v, got %v", expectedHosts, s.ExtraHosts)
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
  },
  "remote": {
    "listen": "0.0.0.0:9000",
    "extra_hosts": ["remote.internal"]
  }
}`
		if err := os.WriteFile(configFile, []byte(configData), 0o600); err != nil {
			t.Fatalf("failed to write config.json: %v", err)
		}

		t.Setenv("MCODE_PROVIDER", "memory")
		t.Setenv("MCODE_MODE", "supervised")
		t.Setenv("MCODE_CLAUDE_BIN", "/env/bin/claude")
		t.Setenv("MCODE_CODEX_BIN", "/env/bin/codex")
		t.Setenv("MCODE_LISTEN", "127.0.0.1:9999")

		setFlags := map[string]bool{
			"provider":   true,
			"mode":       true,
			"claude-bin": true,
			"codex-bin":  true,
			"listen":     true,
		}
		flags := flagValues{
			Provider:  "claude",
			Mode:      "autoAcceptEdits",
			ClaudeBin: "/flag/bin/claude",
			CodexBin:  "/flag/bin/codex",
			Listen:    "192.168.1.100:8080",
			Port:      0,
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
		if s.ListenAddr != "192.168.1.100:8080" {
			t.Fatalf("expected listenAddr %q, got %q", "192.168.1.100:8080", s.ListenAddr)
		}
	})

	// 5. port 参数覆盖 listen 中的端口
	t.Run("port flag overrides listen port", func(t *testing.T) {
		dir := t.TempDir()
		configFile := filepath.Join(dir, "config.json")
		configData := `{
  "version": 1,
  "remote": {
    "listen": "0.0.0.0:9000"
  }
}`
		if err := os.WriteFile(configFile, []byte(configData), 0o600); err != nil {
			t.Fatalf("failed to write config.json: %v", err)
		}

		flags := flagValues{
			Provider:  "claude",
			Mode:      "fullAccess",
			ClaudeBin: "claude",
			CodexBin:  "codex",
			Listen:    config.DefaultRemoteListen,
			Port:      9876,
		}

		// 未显式传入 listen flag，从配置文件获取 0.0.0.0:9000，然后 port=9876 覆盖为 0.0.0.0:9876
		_, s, err := loadSettings(dir, map[string]bool{"port": true}, flags)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.ListenAddr != "0.0.0.0:9876" {
			t.Fatalf("expected listenAddr %q, got %q", "0.0.0.0:9876", s.ListenAddr)
		}

		// 显式传入 listen flag 与 port flag
		setFlags := map[string]bool{
			"listen": true,
			"port":   true,
		}
		flags.Listen = "10.0.0.1:8000"
		flags.Port = 8888
		_, s, err = loadSettings(dir, setFlags, flags)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if s.ListenAddr != "10.0.0.1:8888" {
			t.Fatalf("expected listenAddr %q, got %q", "10.0.0.1:8888", s.ListenAddr)
		}
	})
}
