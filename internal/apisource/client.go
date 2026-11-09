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
