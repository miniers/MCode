package main

import (
	"fmt"
	"net"
	"os"
	"strconv"

	"mcode/internal/config"
)

type flagValues struct {
	Provider  string
	Mode      string
	ClaudeBin string
	CodexBin  string
	Listen    string
	Port      int
}

type settings struct {
	Provider   string
	Mode       string
	ClaudeBin  string
	CodexBin   string
	ACP        map[string]config.ProviderConfig
	ACPBins    map[string]string
	OpenAI     config.OpenAIConfig
	ListenAddr string
	ExtraHosts []string
}

func validateProvider(provider string, cfg config.Config, secrets config.SecretStore) error {
	switch provider {
	case "memory", "claude", "codex":
		return nil
	}
	for _, source := range serverAPISources(cfg, secrets) {
		if source.Kind == provider {
			return nil
		}
	}
	return fmt.Errorf("unsupported or disabled provider %q", provider)
}

func resolveSettings(cfg config.Config, setFlags map[string]bool, flags flagValues) settings {
	acpBins := make(map[string]string)
	for k, v := range cfg.Providers.ACP {
		if v.Bin != "" {
			acpBins[k] = v.Bin
		}
	}

	s := settings{
		Provider:   cfg.Models.Provider,
		Mode:       cfg.Permissions.Mode,
		ClaudeBin:  cfg.Providers.Claude.Bin,
		CodexBin:   cfg.Providers.Codex.Bin,
		ACP:        cfg.Providers.ACP,
		ACPBins:    acpBins,
		OpenAI:     cfg.Providers.OpenAI,
		ListenAddr: cfg.Remote.Listen,
		ExtraHosts: cfg.Remote.ExtraHosts,
	}

	if setFlags != nil {
		if setFlags["provider"] {
			s.Provider = flags.Provider
		}
		if setFlags["mode"] {
			s.Mode = flags.Mode
		}
		if setFlags["claude-bin"] {
			s.ClaudeBin = flags.ClaudeBin
		}
		if setFlags["codex-bin"] {
			s.CodexBin = flags.CodexBin
		}
		if setFlags["listen"] {
			s.ListenAddr = flags.Listen
		}
	}

	if s.ListenAddr == "" {
		s.ListenAddr = config.DefaultRemoteListen
	}

	// 如果指定了 port 参数，覆盖 listen 的端口
	if flags.Port > 0 {
		host, _, err := net.SplitHostPort(s.ListenAddr)
		if err != nil {
			host = "127.0.0.1"
		}
		s.ListenAddr = net.JoinHostPort(host, strconv.Itoa(flags.Port))
	}

	if s.ClaudeBin == "" {
		s.ClaudeBin = "claude"
	}
	if s.CodexBin == "" {
		s.CodexBin = "codex"
	}

	return s
}

func loadSettings(dataDir string, setFlags map[string]bool, flags flagValues) (config.Config, settings, error) {
	cfg, err := config.Load(dataDir)
	if err != nil {
		return config.Config{}, settings{}, fmt.Errorf("load config from %s: %w", config.ConfigPath(dataDir), err)
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	if err := config.Validate(cfg); err != nil {
		return config.Config{}, settings{}, fmt.Errorf("validate config: %w", err)
	}
	return cfg, resolveSettings(cfg, setFlags, flags), nil
}
