package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	providermcode "mcode/internal/providers/mcode"
)

// openAIOfficialBaseURL 是官方 OpenAI 端点的默认地址，用于判断 reasoning_effort 的白名单。
const openAIOfficialBaseURL = "https://api.openai.com/v1"

// mcodeRuntimeDataDir 返回内置引擎使用的数据目录：优先运行时记录，其次 app 级数据目录。
func mcodeRuntimeDataDir() string {
	mcodeAgentRuntime.RLock()
	defer mcodeAgentRuntime.RUnlock()
	if mcodeAgentRuntime.dataDir != "" {
		return mcodeAgentRuntime.dataDir
	}
	return getAppDataDir()
}

// newSourceEngineHarness 让 API Key 类来源改由内置引擎运行。
// legacy 只提供元数据（Kind、名称、模型目录、可用性与刷新），会话执行交给内置引擎，
// 因此这些来源同样具备 bash、会话压缩与 steer 能力；provider kind 保持不变。
func newSourceEngineHarness(legacy harness.Harness, cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {
	kind := legacy.Kind()
	opts := mcodeEngineOptions(secrets, dataDir, mcpInjection)
	opts.Kind = kind
	if named, ok := legacy.(interface{ DisplayName() string }); ok {
		opts.DisplayName = named.DisplayName()
	}
	if models, ok := legacy.(interface{ ModelIDs() []string }); ok {
		opts.ModelIDs = models.ModelIDs
	}
	if refresh, ok := legacy.(interface {
		RefreshModels(context.Context) error
	}); ok {
		opts.RefreshModels = refresh.RefreshModels
	}
	if available, ok := legacy.(interface{ Available() (bool, string) }); ok {
		opts.Available = available.Available
	}
	opts.ResolveClient = func(model, affinityKey string) (llm.Client, string, error) {
		return resolveSourceClient(kind, legacy, cfg, secrets, model, affinityKey)
	}
	opts.ContextWindow = func(model string) int {
		return sourceEngineContextWindow(kind, cfg, secrets, model)
	}
	return providermcode.New(opts)
}

// resolveSourceClient 每次调用都重新解析最新配置与凭据，保存新 Key 后无需重建 harness。
func resolveSourceClient(kind harness.Kind, legacy harness.Harness, cfg config.Config, secrets config.SecretStore, model, affinityKey string) (llm.Client, string, error) {
	current := mcodeCurrentConfig(cfg, secrets)
	store := mcodeCurrentSecrets(secrets)
	source, ok := findAPISource(apisource.Resolve(current, store), kind)
	if !ok {
		return nil, "", fmt.Errorf("来源 %s 未启用或已删除", kind)
	}
	modelID := strings.TrimSpace(model)
	if modelID == "" || strings.EqualFold(modelID, "default") {
		modelID = sourceEngineDefaultModel(source, kind, legacy)
	}
	if modelID == "" {
		return nil, "", fmt.Errorf("来源 %s 未配置默认模型", kind)
	}
	key := source.APIKey(store)
	if source.Magpie {
		target := magpieinject.Target{BaseURL: source.BaseURL, APIKey: key, ProviderID: source.Kind}
		clientCfg := mcodeMagpieConfig(target, affinityKey, modelID)
		clientCfg.Effort = engineReasoningEffort(source.BaseURL)
		return llm.NewOpenAI(clientCfg), modelID, nil
	}
	return llm.NewOpenAI(llm.OpenAIConfig{
		BaseURL:       source.BaseURL,
		APIKey:        key,
		Headers:       source.Headers,
		EchoReasoning: isDeepSeekModel(modelID),
		Effort:        engineReasoningEffort(source.BaseURL),
	}), modelID, nil
}

// findAPISource 按 provider kind 找到当前启用的来源。
func findAPISource(sources []apisource.Source, kind harness.Kind) (apisource.Source, bool) {
	for _, source := range sources {
		if source.Kind == string(kind) {
			return source, true
		}
	}
	return apisource.Source{}, false
}

// sourceEngineDefaultModel 复刻旧循环选择默认模型的规则：先取来源配置的默认模型；
// 官方 OpenAI 端点回退到 gpt-4o；其余来源取模型目录的第一项，目录为空时
// 同步刷新一次（与旧循环 modelIDsForRun 一致），刷新失败就返回空。
func sourceEngineDefaultModel(source apisource.Source, kind harness.Kind, legacy harness.Harness) string {
	if model := strings.TrimSpace(source.DefaultModel); model != "" {
		return model
	}
	if isOfficialOpenAIEndpoint(kind, source.BaseURL) {
		return "gpt-4o"
	}
	models, ok := legacy.(interface{ ModelIDs() []string })
	if !ok {
		return ""
	}
	ids := models.ModelIDs()
	if len(ids) == 0 {
		if refresher, ok := legacy.(interface {
			RefreshModels(context.Context) error
		}); ok {
			_ = refresher.RefreshModels(context.Background())
			ids = models.ModelIDs()
		}
	}
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			return id
		}
	}
	return ""
}

// isOfficialOpenAIEndpoint 与旧循环 isOfficialOpenAI 一致：kind 为 openai，
// 或端点主机是 api.openai.com。
func isOfficialOpenAIEndpoint(kind harness.Kind, baseURL string) bool {
	if strings.EqualFold(string(kind), "openai") {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com")
}

// engineReasoningEffort 复刻旧循环 reasoningEffort 的语义：非官方端点原样透传
// （便于网关接收 xhigh、max），官方端点只对推理模型下发白名单内的取值，
// deepseek-reasoner 一律不发送。
func engineReasoningEffort(baseURL string) func(model, effort string) (string, bool) {
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

// sourceEngineContextWindow 返回来源模型的上下文窗口：只有 Magpie 网关的模型
// 能从缓存元数据里读到窗口，其余来源返回 0 表示未知。
func sourceEngineContextWindow(kind harness.Kind, cfg config.Config, secrets config.SecretStore, model string) int {
	current := mcodeCurrentConfig(cfg, secrets)
	store := mcodeCurrentSecrets(secrets)
	source, ok := findAPISource(apisource.Resolve(current, store), kind)
	if !ok || !source.Magpie {
		return 0
	}
	key := source.APIKey(store)
	candidates := []string{strings.TrimSpace(model)}
	if _, rest, found := strings.Cut(candidates[0], "/"); found && rest != "" {
		candidates = append(candidates, rest)
	}
	for _, id := range candidates {
		if cached, hit := magpie.CachedModel(source.BaseURL, key, id); hit && cached.ContextWindow > 0 {
			return cached.ContextWindow
		}
	}
	return 0
}
