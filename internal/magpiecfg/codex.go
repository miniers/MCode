package magpiecfg

import (
	"errors"
	"strings"
)

// CodexAgent 是 codex 在配置与状态文件里的 agent 名。
const CodexAgent = "codex"

// CodexProviderID 是写进 config.toml 的 provider 表名，也是 model_provider
// 的取值。
const CodexProviderID = "mcode-magpie"

// CodexOptions 是一次 codex 接管要写的内容。BaseURL 通常已经带 /v1，
// CodexPlan 会再按 codex 的规则规范化一次。
type CodexOptions struct {
	BaseURL string
	APIKey  string
	Model   string
}

// CodexPlan 是 codex 的 global 接管计划：顶层 model / model_provider 加一张
// [model_providers.mcode-magpie] 表。
//
// 整张 provider 表由 mcode 拥有：Connect 整体替换，Disconnect 按 stash 整表
// 还原，所以用户可以在表里改任何键（包括 mcode 不认识的键），drift 检测会
// 把整张表看成一个字段。bearer token 按计划明文写入
// experimental_bearer_token，不写 env_key。
func CodexPlan(opts CodexOptions) (Plan, error) {
	baseURL := strings.TrimSpace(opts.BaseURL)
	if baseURL == "" {
		return Plan{}, errors.New("magpiecfg: codex 的 base URL 为空")
	}
	apiKey := strings.TrimSpace(opts.APIKey)
	if apiKey == "" {
		return Plan{}, errors.New("magpiecfg: codex 的 API Key 为空")
	}
	model := strings.TrimSpace(opts.Model)
	if model == "" {
		return Plan{}, errors.New("magpiecfg: codex 的模型为空")
	}
	path, err := CodexConfigPath()
	if err != nil {
		return Plan{}, err
	}
	return Plan{
		Agent: CodexAgent,
		Files: []FilePlan{{
			Path:   path,
			Format: FormatTOML,
			Fields: []Field{
				{
					Path:      "model_providers." + CodexProviderID,
					TOMLTable: true,
					Value: map[string]any{
						"name":                      "Magpie",
						"base_url":                  codexBaseURL(baseURL),
						"wire_api":                  "responses",
						"requires_openai_auth":      true,
						"experimental_bearer_token": apiKey,
					},
				},
				{Path: "model", Value: model},
				{Path: "model_provider", Value: CodexProviderID},
			},
		}},
	}, nil
}

// codexBaseURL 把端点地址归一化成 codex 期望的 /v1 形式：去掉尾部斜杠和
// /chat/completions，未以 /v1 结尾时补上。
func codexBaseURL(baseURL string) string {
	u := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	u = strings.TrimSuffix(u, "/chat/completions")
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/v1") {
		u += "/v1"
	}
	return u
}
