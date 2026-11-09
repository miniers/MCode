// Package apisource 把配置与凭据解析成统一的 API 来源描述。
// 本包只做数据解析：不发起请求，也不依赖 app 或 remote 层。
package apisource

import (
	"os"
	"strings"

	"mcode/internal/config"
)

// Protocol 是 API 来源使用的请求协议。
type Protocol string

const (
	// ProtocolOpenAI 是 OpenAI 兼容的 Chat Completions 协议。
	ProtocolOpenAI Protocol = "openai"
	// ProtocolAnthropic 是 Anthropic Messages 协议。
	ProtocolAnthropic Protocol = "anthropic"
)

// Source 描述一个已启用的 API 来源。除凭据外的字段都在 Resolve 时解析完成，
// 凭据由 APIKey 实时读取，保存新 Key 后不需要重建来源。
type Source struct {
	// Kind 是 Provider ID，如 deepseek、openai、anthropic、custom-<id>、magpie。
	Kind string
	// Label 是界面上显示的来源名称。
	Label string
	// Protocol 决定引擎使用哪种请求协议。
	Protocol Protocol
	// BaseURL 是已解析的端点地址：用户覆盖值、Coding Plan、默认值依次决定。
	BaseURL string
	// DefaultModel 是新会话默认使用的模型。
	DefaultModel string
	// Models 是来源自带的模型表；网关类来源为空，由引擎动态拉取。
	Models []string
	// Magpie 表示这是 Magpie 网关端点。
	Magpie bool
	// Headers 是预设要求的额外请求头。
	Headers map[string]string
	// APIKeyAccounts 是按顺序尝试的 keyring 账号。
	APIKeyAccounts []string
	// APIKeyEnv 是 keyring 都没有时的环境变量兜底，空表示不兜底。
	APIKeyEnv string
}

// lookupEnv 读取环境变量。它默认是 os.LookupEnv，测试可以替换成固定表，
// 包内所有环境变量读取都经由它。
var lookupEnv = os.LookupEnv

const (
	openaiKind          = "openai"
	openaiLabel         = "OpenAI"
	openaiAPIKeyAccount = "openai.apiKey"
	openaiAPIKeyEnv     = "OPENAI_API_KEY"
	// openaiDefaultBaseURL 与 openai.New 的默认地址一致。
	openaiDefaultBaseURL = "https://api.openai.com/v1"

	anthropicKind                = "anthropic"
	anthropicLabel               = "Anthropic"
	anthropicDefaultBaseURL      = "https://api.anthropic.com"
	anthropicBaseURLEnv          = "ANTHROPIC_BASE_URL"
	anthropicAPIKeyEnv           = "ANTHROPIC_API_KEY"
	anthropicAPIKeyAccount       = "anthropic.apiKey"
	anthropicLegacyAPIKeyAccount = "mcode.anthropicApiKey"
	// anthropicDefaultModel 与 mcode 的模型目录一致（取第一个模型）。
	anthropicDefaultModel = "claude-opus-5-5"
)

// anthropicModels 与 mcode 模型目录中的 anthropic 模型一致，不带 anthropic/ 前缀。
var anthropicModels = []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}

// presetIDs 是走内置 OpenAI 兼容预设的来源，顺序固定。
var presetIDs = []string{"deepseek", "zai", "meta"}

// Kinds 返回所有可能的 API 来源类型，不依赖启用状态或密钥。
func Kinds(cfg config.Config) []string {
	var kinds []string
	for _, ep := range cfg.Providers.Custom {
		if ep.Magpie {
			kinds = append(kinds, customSource(ep).Kind)
		}
	}
	kinds = append(kinds, anthropicKind, openaiKind)
	for _, id := range presetIDs {
		kinds = append(kinds, id)
	}
	for _, ep := range cfg.Providers.Custom {
		if !ep.Magpie {
			kinds = append(kinds, customSource(ep).Kind)
		}
	}
	return kinds
}

// APIKey 返回当前可用的 API Key：先按 APIKeyAccounts 顺序取 keyring 中第一个
// 非空值，都没有时读 APIKeyEnv。每次调用都实时读取。
func (s Source) APIKey(secrets config.SecretStore) string {
	if secrets != nil {
		for _, account := range s.APIKeyAccounts {
			if strings.TrimSpace(account) == "" {
				continue
			}
			key, err := secrets.Get(account)
			if err != nil {
				continue
			}
			if key = strings.TrimSpace(key); key != "" {
				return key
			}
		}
	}
	return envValue(s.APIKeyEnv)
}

// envValue 读取环境变量并去掉首尾空白，名称为空时返回空串。
func envValue(name string) string {
	if name == "" {
		return ""
	}
	value, _ := lookupEnv(name)
	return strings.TrimSpace(value)
}

// Resolve 按固定顺序返回所有已启用的来源：Magpie 端点（按配置顺序）、
// anthropic、openai、deepseek、zai、meta、其余自定义端点（按配置顺序）。
//
// 旧版 Magpie 预设槽位（cfg.Providers.Presets 里的 magpie..magpie4）不在这里处理：
// internal/app 启动时先经 migrateLegacyMagpieEndpoints 把它们迁移成自定义端点，
// 并从 Presets 删除，因此到 Resolve 时它们已经以 Magpie 端点的形式出现。
func Resolve(cfg config.Config, secrets config.SecretStore) []Source {
	var res []Source

	for _, ep := range cfg.Providers.Custom {
		if ep.Enabled && ep.Magpie {
			res = append(res, customSource(ep))
		}
	}
	if src, ok := anthropicSource(cfg, secrets); ok {
		res = append(res, src)
	}
	if src, ok := openaiSource(cfg); ok {
		res = append(res, src)
	}
	for _, id := range presetIDs {
		if src, ok := presetSource(cfg, id); ok {
			res = append(res, src)
		}
	}
	for _, ep := range cfg.Providers.Custom {
		if ep.Enabled && !ep.Magpie {
			res = append(res, customSource(ep))
		}
	}
	return res
}

// presetSource 按预设元数据生成 deepseek、zai、meta 来源。
func presetSource(cfg config.Config, id string) (Source, bool) {
	pc, ok := cfg.Providers.Presets[id]
	if !ok || !pc.Enabled {
		return Source{}, false
	}
	preset, ok := PresetByID(id)
	if !ok {
		return Source{}, false
	}
	model := strings.TrimSpace(pc.Model)
	if model == "" {
		model = preset.DefaultModel
	}
	return Source{
		Kind:           id,
		Label:          preset.DisplayName,
		Protocol:       ProtocolOpenAI,
		BaseURL:        preset.ResolveBaseURL(pc.BaseURL, pc.CodingPlan),
		DefaultModel:   model,
		Models:         preset.Models,
		Headers:        preset.ExtraHeaders,
		APIKeyAccounts: []string{preset.APIKeyAccount},
		APIKeyEnv:      preset.APIKeyEnv,
	}, true
}

// openaiSource 生成内置 OpenAI 来源：地址为空时用官方默认端点，
// 环境变量 OPENAI_API_KEY 只作 keyring 的兜底。
func openaiSource(cfg config.Config) (Source, bool) {
	oc := cfg.Providers.OpenAI
	if !oc.Enabled {
		return Source{}, false
	}
	baseURL := strings.TrimRight(strings.TrimSpace(oc.BaseURL), "/")
	if baseURL == "" {
		baseURL = openaiDefaultBaseURL
	}
	return Source{
		Kind:           openaiKind,
		Label:          openaiLabel,
		Protocol:       ProtocolOpenAI,
		BaseURL:        baseURL,
		DefaultModel:   strings.TrimSpace(oc.Model),
		APIKeyAccounts: []string{openaiAPIKeyAccount},
		APIKeyEnv:      openaiAPIKeyEnv,
	}, true
}

// AnthropicAPIKey 返回 Anthropic API Key：账号按新名、旧名顺序尝试，
// 都没有时读 ANTHROPIC_API_KEY。旧名只读不写。
func AnthropicAPIKey(secrets config.SecretStore) string {
	return (Source{
		APIKeyAccounts: []string{anthropicAPIKeyAccount, anthropicLegacyAPIKeyAccount},
		APIKeyEnv:      anthropicAPIKeyEnv,
	}).APIKey(secrets)
}

// anthropicSource 生成 Anthropic 来源：配置没有关闭且凭据可用时启用。
// 账号按新名、旧名顺序尝试，旧名只读不写。
func anthropicSource(cfg config.Config, secrets config.SecretStore) (Source, bool) {
	if !cfg.Providers.Anthropic.IsEnabled() {
		return Source{}, false
	}
	src := Source{
		Kind:           anthropicKind,
		Label:          anthropicLabel,
		Protocol:       ProtocolAnthropic,
		BaseURL:        anthropicBaseURL(),
		DefaultModel:   anthropicDefaultModel,
		Models:         anthropicModels,
		APIKeyAccounts: []string{anthropicAPIKeyAccount, anthropicLegacyAPIKeyAccount},
		APIKeyEnv:      anthropicAPIKeyEnv,
	}
	if src.APIKey(secrets) == "" {
		return Source{}, false
	}
	return src, true
}

// anthropicBaseURL 返回 Anthropic 端点：环境变量 ANTHROPIC_BASE_URL 去掉末尾
// 斜杠后优先，未配置时用官方地址。
func anthropicBaseURL() string {
	if baseURL := strings.TrimRight(envValue(anthropicBaseURLEnv), "/"); baseURL != "" {
		return baseURL
	}
	return anthropicDefaultBaseURL
}

// customSource 由自定义端点配置生成来源。自定义端点不读 OPENAI_API_KEY，
// 否则内置 OpenAI 的官方密钥会被发给第三方端点。
func customSource(ep config.CustomEndpointConfig) Source {
	return Source{
		Kind:           config.CustomEndpointProviderKind(ep),
		Label:          ep.Name,
		Protocol:       ProtocolOpenAI,
		BaseURL:        strings.TrimRight(ep.BaseURL, "/"),
		DefaultModel:   ep.Model,
		Magpie:         ep.Magpie,
		APIKeyAccounts: []string{config.CustomEndpointSecretAccount(ep.ID)},
	}
}
