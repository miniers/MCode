package acp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mcode/internal/fsutil"
	"mcode/internal/providers/loginenv"
)

// 本文件处理 Cline 的上游提供方标识与推理强度：
// Cline 的 ACP 模式默认列出整个 openrouter 目录，只有 CLINE_PROVIDER 指向 cline-pass 时才只给订阅模型；
// 推理强度没有 ACP 配置项，只能写进 Cline 自己的 settings/providers.json。

// clineProvidersFile 返回存在的 providers.json 路径：
// $CLINE_DATA_DIR/settings/providers.json，其次 ~/.cline/data/settings/providers.json，
// 最后 ~/.cline/settings/providers.json。
func clineProvidersFile() string {
	home := loginenv.HomeDirectory()
	dataDir := strings.TrimSpace(os.Getenv("CLINE_DATA_DIR"))
	if dataDir == "" {
		dataDir = filepath.Join(home, ".cline", "data")
	}
	for _, path := range []string{
		filepath.Join(dataDir, "settings", "providers.json"),
		filepath.Join(home, ".cline", "settings", "providers.json"),
	} {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

func readClineProviders(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return root, nil
}

// ClineProviderID 解析当前生效的 Cline 提供方（如 cline-pass 或 cline）：
// 先看 CLINE_PROVIDER 环境变量，再看 providers.json 的 lastUsedProvider，
// 最后在只存了 cline-pass 时取 cline-pass；都没有时返回空串。
func ClineProviderID() string {
	if explicit := strings.TrimSpace(os.Getenv("CLINE_PROVIDER")); explicit != "" {
		return explicit
	}
	path := clineProvidersFile()
	if path == "" {
		return ""
	}
	root, err := readClineProviders(path)
	if err != nil {
		return ""
	}
	providers, _ := root["providers"].(map[string]any)
	if len(providers) == 0 {
		return ""
	}
	if last, _ := root["lastUsedProvider"].(string); strings.TrimSpace(last) != "" {
		return last
	}
	if providers["cline-pass"] != nil && providers["cline"] == nil {
		return "cline-pass"
	}
	return ""
}

// UpdateClineReasoning 把推理强度写进 Cline 的 providers.json（none/off 表示关闭思考）。
// 没有 providers.json（未登录）时什么也不做。
func UpdateClineReasoning(effort string) error {
	path := clineProvidersFile()
	if path == "" {
		return nil
	}
	root, err := readClineProviders(path)
	if err != nil {
		return err
	}
	providers, _ := root["providers"].(map[string]any)
	if providers == nil {
		return nil
	}

	active := ClineProviderID()
	if active == "" {
		active = "cline-pass"
	}
	effort = strings.TrimSpace(effort)
	off := effort == "none" || effort == "off"
	chosen := effort
	if chosen == "" || off {
		chosen = "medium"
	}
	reasoning := map[string]any{"effort": chosen, "enabled": !off}

	updated := false
	for _, key := range []string{active, "cline-pass", "cline"} {
		entry, _ := providers[key].(map[string]any)
		if entry == nil {
			continue
		}
		settings, _ := entry["settings"].(map[string]any)
		if settings == nil {
			settings = map[string]any{}
		}
		settings["reasoning"] = reasoning
		entry["settings"] = settings
		updated = true
	}
	if !updated {
		return nil
	}

	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, data, 0o600)
}
