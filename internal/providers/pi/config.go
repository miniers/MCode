package pi

// 本文件读取 Pi 的登录位置、登录状态与 settings.json。
// Pi 按 provider 把登录写在 <config>/auth.json
// （<config> 默认是 <home>/.pi/agent，PI_CODING_AGENT_DIR 可覆盖）。

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ConfigDir 返回 Pi 的配置目录：PI_CODING_AGENT_DIR 优先，否则 <home>/.pi/agent。
// env 为 nil 时读进程环境；home 与 env["HOME"] 都为空时用 os.UserHomeDir。
func ConfigDir(env map[string]string, home string) string {
	if directory := strings.TrimSpace(piEnvValue(env, "PI_CODING_AGENT_DIR")); directory != "" {
		return directory
	}
	base := strings.TrimSpace(home)
	if base == "" {
		base = strings.TrimSpace(piEnvValue(env, "HOME"))
	}
	if base == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			base = resolved
		}
	}
	return filepath.Join(base, ".pi", "agent")
}

// AuthStatus 读 <config>/auth.json：内容是一个至少有一个键的对象就算已登录，
// providers 是排序后的键名（anthropic、openai-codex 之类）；没登录时 providers 为 nil。
//
// 文件不存在或为空视为未登录且不算错误；文件内容不是 JSON 对象则返回错误。
// 注意：环境里的 provider API key 是否算已登录由调用方约定，这里只读 auth.json。
func AuthStatus(env map[string]string, home string) (signedIn bool, providers []string, err error) {
	file := filepath.Join(ConfigDir(env, home), "auth.json")
	data, readErr := os.ReadFile(file)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("pi: 读取 %s: %w", file, readErr)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return false, nil, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return false, nil, fmt.Errorf("pi: 解析 %s: %w", file, err)
	}
	if len(object) == 0 {
		return false, nil, nil
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return true, keys, nil
}

// Settings 读 <config>/settings.json 并返回顶层对象；文件缺失或无法解析成对象时返回 nil。
// 每次目录加载只读一次，供每个模型的默认思考等级共用。
func Settings(env map[string]string, home string) map[string]json.RawMessage {
	file := filepath.Join(ConfigDir(env, home), "settings.json")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	return piRawObject(data)
}

// piEnvValue 读环境变量：env 为 nil 时读进程环境，非 nil 时只查这张表。
func piEnvValue(env map[string]string, key string) string {
	if env == nil {
		return os.Getenv(key)
	}
	return env[key]
}

// piHome 返回 Pi 子进程的工作目录：home 参数优先，其次 env["HOME"]，最后 os.UserHomeDir。
func piHome(env map[string]string, home string) string {
	if base := strings.TrimSpace(home); base != "" {
		return base
	}
	if base := strings.TrimSpace(piEnvValue(env, "HOME")); base != "" {
		return base
	}
	resolved, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return resolved
}
