package apisource

import (
	"fmt"
	"strings"

	"mcode/internal/harness"
)

// Preset 描述一个内置的 OpenAI 兼容提供者预设：默认端点、模型与凭据位置。
type Preset struct {
	ID                string
	Kind              harness.Kind
	DisplayName       string
	DefaultBaseURL    string
	CodingPlanBaseURL string // 订阅版端点，空表示该家没有单独的 Coding Plan 地址
	DefaultModel      string
	Models            []string
	APIKeyAccount     string // keyring 中的账号名
	APIKeyEnv         string // keyring 无值时的环境变量回退
	SignupURL         string
	ExtraHeaders      map[string]string
}

// DeepSeek 预设。默认模型与模型列表取自 DeepSeekAPI.option(for:)；
// deepseek-chat 与 deepseek-reasoner 已由 DeepSeek 下线，仅作兼容保留。
var DeepSeek = Preset{
	ID: "deepseek", Kind: harness.Kind("deepseek"), DisplayName: "DeepSeek",
	DefaultBaseURL: "https://api.deepseek.com", DefaultModel: "deepseek-v4-pro",
	Models:        []string{"deepseek-v4-pro", "deepseek-flash", "deepseek-chat", "deepseek-reasoner"},
	APIKeyAccount: "deepseek.apiKey", APIKeyEnv: "DEEPSEEK_API_KEY", SignupURL: "https://platform.deepseek.com/api_keys",
}

// Zai 预设。Z.ai 只使用 GLM Coding Plan 端点，因此默认地址与 Coding Plan 地址相同。
var Zai = Preset{
	ID: "zai", Kind: harness.Kind("zai"), DisplayName: "Z.ai",
	DefaultBaseURL: "https://api.z.ai/api/coding/paas/v4", CodingPlanBaseURL: "https://api.z.ai/api/coding/paas/v4",
	DefaultModel: "glm-5.3", Models: []string{"glm-5.3", "glm-5.3-flash"},
	APIKeyAccount: "zai.apiKey", APIKeyEnv: "ZAI_API_KEY", SignupURL: "https://z.ai/manage-apikey/apikey-list",
}

// Meta（Muse Spark）预设。模型列表取自 ProviderRegistry.metaSeed。
var Meta = Preset{
	ID: "meta", Kind: harness.Kind("meta"), DisplayName: "Meta",
	DefaultBaseURL: "https://api.meta.ai/v1", DefaultModel: "muse-spark-1.3",
	Models:        []string{"muse-spark-1.3", "muse-spark-1.3-contributor", "muse-spark-1.2", "muse-spark-1.2-contributor", "muse-spark-1.1"},
	APIKeyAccount: "meta.apiKey", APIKeyEnv: "MODEL_API_KEY", SignupURL: "https://dev.meta.ai/",
}

const magpieSlots = 4

// Magpie 返回第 slot 个（1..4）Magpie 网关预设，槽位越界时返回 false。
func Magpie(slot int) (Preset, bool) {
	if slot < 1 || slot > magpieSlots {
		return Preset{}, false
	}
	id := "magpie"
	displayName := "Magpie"
	if slot > 1 {
		id = fmt.Sprintf("magpie%d", slot)
		displayName = fmt.Sprintf("Magpie %d", slot)
	}
	return Preset{
		ID: id, Kind: harness.Kind(id), DisplayName: displayName,
		DefaultBaseURL: "http://127.0.0.1:3425/v1", APIKeyAccount: fmt.Sprintf("magpie%d.apiKey", slot),
		APIKeyEnv: magpieEnv(slot), SignupURL: "https://usemagpie.ai",
	}, true
}

func magpieEnv(slot int) string {
	if slot == 1 {
		return "MAGPIE_GATEWAY_KEY"
	}
	return ""
}

// PresetByID 按预设 ID 查找内置预设。
func PresetByID(id string) (Preset, bool) {
	switch id {
	case "deepseek":
		return DeepSeek, true
	case "zai":
		return Zai, true
	case "meta":
		return Meta, true
	}
	for slot := 1; slot <= magpieSlots; slot++ {
		if p, ok := Magpie(slot); ok && p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// ResolveBaseURL 优先使用覆盖地址，其次使用 Coding Plan 地址，最后使用默认地址。
func (p Preset) ResolveBaseURL(override string, codingPlan bool) string {
	if v := strings.TrimSpace(override); v != "" {
		return v
	}
	if codingPlan && p.CodingPlanBaseURL != "" {
		return p.CodingPlanBaseURL
	}
	return p.DefaultBaseURL
}
