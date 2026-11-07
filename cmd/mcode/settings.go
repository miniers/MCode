package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mcode/internal/config"
	"mcode/internal/providers/loginenv"
)

type flagValues struct {
	Provider  string
	Mode      string
	ClaudeBin string
	CodexBin  string
}

type settings struct {
	Provider  string
	Mode      string
	ClaudeBin string
	CodexBin  string
}

func resolveSettings(cfg config.Config, setFlags map[string]bool, flags flagValues) settings {
	s := settings{
		Provider:  cfg.Models.Provider,
		Mode:      cfg.Permissions.Mode,
		ClaudeBin: cfg.Providers.Claude.Bin,
		CodexBin:  cfg.Providers.Codex.Bin,
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

// providerBinaryFinder 解析 provider CLI 的可执行文件：先按当前进程 PATH 快速查找，找不到再回退
// 到登录 shell 的 PATH。main 已用 augmentedPath 补齐 Homebrew、~/.local/bin 等目录，所以多数情况
// 快路径就能命中；首次调用 loginenv 会同步执行登录 shell（200～430ms），这条快路径让 provider 选择
// 不必等它。providerBinaryAvailable 只判断二进制是否存在，查找顺序变化不影响结果语义。测试中可替换为桩。
var providerBinaryFinder = findProviderBinary

// findProviderBinary 先在当前进程 PATH 中依次查找 names，都找不到时再用 loginenv.FindBinary 兜底：
// 登录环境含 GUI 进程 PATH 之外的目录，只有登录 shell 才能拿到。
func findProviderBinary(names []string, fallbackDirs []string) (string, bool) {
	for _, name := range names {
		if name == "" {
			continue
		}
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return path, true
		}
	}
	return loginenv.FindBinary(names, fallbackDirs)
}

// providerBinaryAvailable 判断 provider 的 CLI 是否可用：显式配置的路径优先（绝对路径直接
// 校验是否可执行，其余按命令名查找），再依次用 finder 查找 names。
func providerBinaryAvailable(configured string, names ...string) bool {
	if configured = strings.TrimSpace(configured); configured != "" {
		if filepath.IsAbs(configured) {
			if info, err := os.Stat(configured); err == nil && !info.IsDir() && info.Mode().Perm()&0o111 != 0 {
				return true
			}
		} else if _, ok := providerBinaryFinder([]string{configured}, nil); ok {
			return true
		}
	}
	if len(names) == 0 {
		return false
	}
	_, ok := providerBinaryFinder(names, nil)
	return ok
}
