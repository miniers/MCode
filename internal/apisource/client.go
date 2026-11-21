package apisource

import (
	"strings"

	"mcode/internal/agent/llm"
	"mcode/internal/magpieinject"
)

// IsClaudeModel 判断模型名称是否包含 claude。
func IsClaudeModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "claude")
}

// ClientOptions 配置额外请求头和推理档位转换。
type ClientOptions struct {
	Headers map[string]string
	Effort  func(model, effort string) (string, bool)
}

// openAIOfficialBaseURL 是官方 OpenAI 端点的默认地址，用于判断 reasoning_effort 的白名单。
const openAIOfficialBaseURL = "https://api.openai.com/v1"

// ReasoningEffort 复刻旧循环 reasoningEffort 的语义：非官方端点原样透传
// （便于网关接收 xhigh、max），官方端点只对推理模型下发白名单内的取值，
// deepseek-reasoner 一律不发送。
func ReasoningEffort(baseURL string) func(model, effort string) (string, bool) {
	return func(model, effort string) (string, bool) {
		if effort == "" {
			return "", false
		}
		if strings.EqualFold(model, "deepseek-reasoner") {
			return "", false
		}
		if strings.TrimRight(strings.TrimSpace(baseURL), "/") != openAIOfficialBaseURL {
			return effort, true
		}
		lower := strings.ToLower(model)
		isReasoning := strings.HasPrefix(lower, "o1") || strings.HasPrefix(lower, "o3") ||
			strings.Contains(lower, "reasoner") || strings.Contains(lower, "r1") ||
			strings.Contains(lower, "thinking") || strings.Contains(lower, "qwq")
		if !isReasoning {
			return "", false
		}
		switch effort {
		case "low", "medium", "high":
			return effort, true
		case "max", "xhigh", "extra-high":
			return "high", true
		default:
			return "", false
		}
	}
}

// NewClient 根据来源和模型选择请求协议。
func NewClient(src Source, key, model string, opts ClientOptions) llm.Client {
	headers := opts.Headers
	if headers == nil {
		headers = src.Headers
	}
	if src.Protocol == ProtocolAnthropic {
		return llm.NewAnthropic(llm.AnthropicConfig{BaseURL: src.BaseURL, APIKey: key, Headers: headers})
	}
	if src.Magpie && IsClaudeModel(model) {
		return llm.NewAnthropic(llm.AnthropicConfig{BaseURL: magpieinject.AnthropicBaseURL(src.BaseURL), APIKey: key, Headers: headers, BearerAuth: true})
	}
	return llm.NewOpenAI(llm.OpenAIConfig{BaseURL: src.BaseURL, APIKey: key, Headers: headers, EchoReasoning: strings.Contains(strings.ToLower(model), "deepseek"), Effort: opts.Effort})
}
