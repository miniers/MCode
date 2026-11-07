package magpiecfg

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"mcode/internal/providers/loginenv"
)

// 这三个变量包起来只为让测试替换环境：真实运行时就是 os.LookupEnv、
// loginenv.HomeDirectory 与 runtime.GOOS。
var (
	lookupEnv = os.LookupEnv
	homeDir   = loginenv.HomeDirectory
	hostOS    = runtime.GOOS
)

// ClaudeSettingsPath 返回 Claude Code 全局配置文件 settings.json 的路径。
//
// CLAUDE_CONFIG_DIR 只是绝对路径时才生效：Claude Code 会把相对路径当成
// 未设置，继续用默认配置目录，写入相对路径下的文件 CLI 根本不会读。
// 目录不存在不算错误，写入时由 SetJSON 负责创建。
func ClaudeSettingsPath() (string, error) {
	if dir, ok := lookupEnv("CLAUDE_CONFIG_DIR"); ok {
		dir = strings.TrimSpace(dir)
		if filepath.IsAbs(dir) {
			return filepath.Join(dir, "settings.json"), nil
		}
	}
	home := strings.TrimSpace(homeDir())
	if home == "" {
		return "", errors.New("magpiecfg: 无法解析用户主目录")
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// CodexConfigPath 返回 codex 全局配置文件 config.toml 的路径。$CODEX_HOME
// 优先（与 codex 自己的规则一致），否则是 ~/.codex/config.toml。目录不存在
// 不算错误，写入时由 WriteAtomic 负责创建。
func CodexConfigPath() (string, error) {
	if dir, ok := lookupEnv("CODEX_HOME"); ok && strings.TrimSpace(dir) != "" {
		return filepath.Join(dir, "config.toml"), nil
	}
	home := strings.TrimSpace(homeDir())
	if home == "" {
		return "", errors.New("magpiecfg: 无法解析用户主目录")
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// PiPaths 返回 pi 的 models.json 与 settings.json 路径。目录规则与 pi 自己的
// ConfigDir 一致：$PI_CODING_AGENT_DIR 非空时优先，否则 ~/.pi/agent。覆盖目录
// 必须是绝对路径，否则 pi 写入与 mcode 写入会落到不同位置。目录不存在不算
// 错误，写入时由 SetJSON 负责创建。
func PiPaths() (modelsPath, settingsPath string, err error) {
	dir := ""
	if v, ok := lookupEnv("PI_CODING_AGENT_DIR"); ok {
		v = strings.TrimSpace(v)
		if v != "" {
			if !filepath.IsAbs(v) {
				return "", "", fmt.Errorf("magpiecfg: PI_CODING_AGENT_DIR 必须是绝对路径，当前为 %q", v)
			}
			dir = v
		}
	}
	if dir == "" {
		home := strings.TrimSpace(homeDir())
		if home == "" {
			return "", "", errors.New("magpiecfg: 无法解析用户主目录")
		}
		dir = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(dir, "models.json"), filepath.Join(dir, "settings.json"), nil
}

// claudeManagedSettingsPath 是 Claude Code 在 Windows 上的系统管理员配置。
const claudeManagedSettingsPath = `C:\ProgramData\ClaudeCode\managed-settings.json`

// ClaudeManagedSettingsPath 返回系统的 Claude Code managed-settings.json 路径：
// 只有 Windows 有这一层配置，其它平台返回空串。
func ClaudeManagedSettingsPath() string {
	if hostOS != "windows" {
		return ""
	}
	return claudeManagedSettingsPath
}

// ClaudeManagedSettingsPresent 报告系统管理员配置是否存在。存在时它的优先级
// 高于用户自己的 settings.json，界面要提示用户这里的改动可能被覆盖。
func ClaudeManagedSettingsPresent() (string, bool) {
	path := ClaudeManagedSettingsPath()
	if path == "" {
		return "", false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return "", false
	}
	return path, true
}
