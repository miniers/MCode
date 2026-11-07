package config

import "os"

// ApplyEnv 用 MCODE_* 环境变量覆盖 cfg 中对应的值。
// 若 lookup 为 nil，则默认使用 os.LookupEnv。
// 仅当环境变量存在且非空时才覆盖，未设置时不改变已有值。
func ApplyEnv(cfg *Config, lookup func(key string) (string, bool)) {
	if cfg == nil {
		return
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}

	if val, ok := lookup("MCODE_PROVIDER"); ok && val != "" {
		cfg.Models.Provider = val
	}
	if val, ok := lookup("MCODE_MODE"); ok && val != "" {
		cfg.Permissions.Mode = val
	}
	if val, ok := lookup("MCODE_CLAUDE_BIN"); ok && val != "" {
		cfg.Providers.Claude.Bin = val
	}
	if val, ok := lookup("MCODE_CODEX_BIN"); ok && val != "" {
		cfg.Providers.Codex.Bin = val
	}
	if val, ok := lookup("MCODE_LISTEN"); ok && val != "" {
		cfg.Remote.Listen = val
	}
}
