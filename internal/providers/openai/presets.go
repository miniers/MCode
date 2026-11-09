package openai

import (
	"os"

	"mcode/internal/apisource"
	"mcode/internal/config"
)

// Preset 是统一 API 来源中的预设元数据。
type Preset = apisource.Preset

var (
	DeepSeek = apisource.DeepSeek
	Zai      = apisource.Zai
	Meta     = apisource.Meta
)

// Magpie 返回指定槽位的 Magpie 预设。
func Magpie(slot int) (Preset, bool) { return apisource.Magpie(slot) }

// PresetByID 按预设 ID 查找内置预设。
func PresetByID(id string) (Preset, bool) { return apisource.PresetByID(id) }

// PresetOptions 按预设构造旧客户端的 Options。
func PresetOptions(p Preset, secrets config.SecretStore, baseURLOverride string, codingPlan bool) Options {
	return Options{
		Kind:           p.Kind,
		DisplayName:    p.DisplayName,
		BaseURL:        p.ResolveBaseURL(baseURLOverride, codingPlan),
		Model:          p.DefaultModel,
		APIKeyAccount:  p.APIKeyAccount,
		Secrets:        secrets,
		FallbackAPIKey: fallbackAPIKey(p.APIKeyEnv),
		ExtraHeaders:   p.ExtraHeaders,
	}
}

// fallbackAPIKey 读取环境变量回退，未配置时返回空串。
func fallbackAPIKey(env string) string {
	if env == "" {
		return ""
	}
	return os.Getenv(env)
}
