package app

import (
	"fmt"
	"strings"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// anthropicEngineKind 是 Anthropic API 来源的 Provider ID。
const anthropicEngineKind harness.Kind = "anthropic"

// anthropicEngineContextWindow 是 Anthropic 模型的上下文窗口（tokens）。
const anthropicEngineContextWindow = 200000

// newAnthropicEngineHarness 让 Anthropic API Key 来源由内置引擎运行。
// 来源解析与旧 mcode.anthropicApiKey 共用同一套规则，Key 放在新账号或旧账号都能用。
func newAnthropicEngineHarness(cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {
	opts := mcodeEngineOptions(secrets, dataDir, mcpInjection)
	opts.Kind = anthropicEngineKind
	opts.DisplayName = "Anthropic"
	remote.SetAPICatalog(string(anthropicEngineKind), func() []wire.ProviderModel {
		return apiSourceCatalog(mcodeCurrentConfig(cfg, secrets), mcodeCurrentSecrets(secrets), string(anthropicEngineKind), nil)
	})
	opts.ModelIDs = func() []string {
		source, ok := currentAnthropicSource(cfg, secrets)
		if !ok {
			return nil
		}
		return source.Models
	}
	opts.Available = func() (bool, string) {
		if _, ok := currentAnthropicSource(cfg, secrets); !ok {
			return false, "未配置 API Key"
		}
		return true, ""
	}
	opts.ResolveClient = func(model, affinityKey string) (llm.Client, string, error) {
		return resolveAnthropicClient(cfg, secrets, model)
	}
	opts.ContextWindow = func(string) int {
		return anthropicEngineContextWindow
	}
	return providermcode.New(opts)
}

// resolveAnthropicClient 每次调用都重新解析配置与凭据，找到 anthropic 来源后构造客户端。
// 模型 ID 不带 anthropic/ 前缀，收到带前缀的模型时去掉（与 resolveMcodeClient 一致）。
func resolveAnthropicClient(cfg config.Config, secrets config.SecretStore, model string) (llm.Client, string, error) {
	store := mcodeCurrentSecrets(secrets)
	source, ok := findAPISource(apisource.Resolve(mcodeCurrentConfig(cfg, secrets), store), anthropicEngineKind)
	if !ok {
		return nil, "", fmt.Errorf("未配置 Anthropic API Key")
	}
	modelID := strings.TrimSpace(model)
	if modelID == "" || strings.EqualFold(modelID, "default") {
		modelID = source.DefaultModel
	}
	modelID = strings.TrimPrefix(modelID, "anthropic/")
	if modelID == "" {
		return nil, "", fmt.Errorf("Anthropic 来源未配置默认模型")
	}
	client := llm.NewAnthropic(llm.AnthropicConfig{BaseURL: source.BaseURL, APIKey: source.APIKey(store)})
	return client, modelID, nil
}

// currentAnthropicSource 每次重新读取运行时配置与凭据后再解析 anthropic 来源。
func currentAnthropicSource(cfg config.Config, secrets config.SecretStore) (apisource.Source, bool) {
	return findAPISource(
		apisource.Resolve(mcodeCurrentConfig(cfg, secrets), mcodeCurrentSecrets(secrets)),
		anthropicEngineKind,
	)
}
