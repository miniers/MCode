package magpieinject

// 本文件是各 agent 共用的 Magpie 注入层：端点枚举、provider 前缀解析与 Claude Code
// 需要的根地址换算。这里不绑定任何具体 agent，pi 与 claude 的适配器各自取用。
// providerID 的分配顺序与 pi 原有实现完全一致，保证同一个端点在所有 agent 里的
// ProviderID 相同。

import (
	"errors"
	"net/url"
	"strings"

	"mcode/internal/config"
	"mcode/internal/providers/magpie"
)

// Target 是一个可注入的 Magpie 端点。
type Target struct {
	// Endpoint 是端点 ID，也是桥回传路由时用来对回配置的键。
	Endpoint string
	// BaseURL 是归一化后的 /v1 地址，与 MCODE_PI_MAGPIE 的 baseUrl 一致。
	BaseURL string
	APIKey  string
	// ProviderID 是注入后模型 ID 的前缀。
	ProviderID string
}

// HasKey 报告该端点是否配置了 API Key。没配 key 的端点不注入，但仍有 ProviderID。
func (t Target) HasKey() bool {
	return t.APIKey != ""
}

// Targets 按配置顺序列出启用的 Magpie 端点。ProviderID 先对全部启用端点依次分配
// （与 pi 的 piMagpieTargets 相同的顺序与 taken 规则），之后才按 selections 过滤，
// 因此同一个端点在不同 agent 里、以及过滤与否时得到同一个 ProviderID。
// selections 为 nil 或长度为 0 时返回全部端点；否则只保留 EndpointID 被列出的端点。
func Targets(cfg config.Config, secrets config.SecretStore, selections []config.MagpieSelection) []Target {
	selected := make(map[string]bool, len(selections))
	for _, selection := range selections {
		if id := strings.TrimSpace(selection.EndpointID); id != "" {
			selected[id] = true
		}
	}

	taken := make(map[string]bool, len(cfg.Providers.Custom))
	res := make([]Target, 0, len(cfg.Providers.Custom))
	for _, ep := range cfg.Providers.Custom {
		if !ep.Enabled || !ep.Magpie {
			continue
		}
		providerID := magpie.ProviderID(ep.Name, taken)
		taken[providerID] = true

		if len(selections) > 0 && !selected[strings.TrimSpace(ep.ID)] {
			continue
		}

		baseURL := strings.TrimSpace(ep.BaseURL)
		if baseURL == "" {
			baseURL = config.DefaultMagpieBaseURL
		}
		if normalized, err := endpointBaseURL(baseURL); err == nil {
			baseURL = normalized
		} else {
			baseURL = strings.TrimRight(baseURL, "/")
		}

		apiKey := ""
		if secrets != nil {
			if value, err := secrets.Get(config.CustomEndpointSecretAccount(ep.ID)); err == nil {
				apiKey = strings.TrimSpace(value)
			}
		}

		res = append(res, Target{
			Endpoint:   ep.ID,
			BaseURL:    baseURL,
			APIKey:     apiKey,
			ProviderID: providerID,
		})
	}
	return res
}

// errInvalidBaseURL 与 app 包的 customEndpointBaseURL 报同样的错；调用方按非 nil
// 走原样去斜杠的兜底分支。
var errInvalidBaseURL = errors.New("BaseURL 必须为不含账号密码、查询参数或片段的 http 或 https 地址")

// endpointBaseURL 校验并归一化端点地址，规则与 app 包的 customEndpointBaseURL 相同：
// 必须是带主机的 http(s) 地址，且不带账号密码、查询参数或片段，末尾斜杠去掉。
func endpointBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") ||
		u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errInvalidBaseURL
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// ProviderOf 按第一个斜杠把注入模型 ID 切成 provider 前缀与网关模型 ID，
// 前缀与某个 Target 的 ProviderID 精确相等时返回该 Target、剩余部分与 true。
// 空模型、没有斜杠、空前缀、前缀不存在、剩余部分为空都返回 false。
func ProviderOf(model string, targets []Target) (Target, string, bool) {
	providerID, rest, ok := strings.Cut(model, "/")
	if !ok || providerID == "" || rest == "" {
		return Target{}, "", false
	}
	for _, target := range targets {
		if target.ProviderID == providerID {
			return target, rest, true
		}
	}
	return Target{}, "", false
}

// AnthropicBaseURL 把端点的 /v1 地址换成 Claude Code 需要的根地址：
// 去掉结尾所有斜杠，再去掉结尾的 /v1，再去掉因此露出的结尾斜杠。
func AnthropicBaseURL(baseURL string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	trimmed = strings.TrimSuffix(trimmed, "/v1")
	return strings.TrimRight(trimmed, "/")
}
