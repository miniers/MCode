package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// openAIOfficialBaseURL 是官方 OpenAI 端点的默认地址，用于判断 reasoning_effort 的白名单。
const openAIOfficialBaseURL = "https://api.openai.com/v1"

var magpieDefaultModelTimeout = 5 * time.Second

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
func newSourceEngineHarness(kind harness.Kind, cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {
	opts := mcodeEngineOptions(secrets, dataDir, mcpInjection)
	registerShellSecrets(mcodeCurrentConfig(cfg, secrets), mcodeCurrentSecrets(secrets))
	opts.Kind = kind
	opts.DisplayName = string(kind)
	if source, ok := findAPISource(apisource.Resolve(cfg, secrets), kind); ok {
		opts.DisplayName = source.Label
	}
	catalog := func() []wire.ProviderModel {
		current := mcodeCurrentConfig(cfg, secrets)
		store := mcodeCurrentSecrets(secrets)
		var ids []string
		if source, ok := findAPISource(apisource.Resolve(current, store), kind); ok && !source.Magpie && source.Protocol != apisource.ProtocolAnthropic {
			ids = sourceModelCache.models(source, source.APIKey(store))
		}
		return apiSourceCatalog(current, store, string(kind), ids)
	}
	remote.SetAPICatalog(string(kind), catalog)
	opts.ModelIDs = func() []string {
		rows := catalog()
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.ID)
		}
		return ids
	}
	opts.RefreshModels = func(ctx context.Context) error {
		current := mcodeCurrentConfig(cfg, secrets)
		store := mcodeCurrentSecrets(secrets)
		currentSource, ok := findAPISource(apisource.Resolve(current, store), kind)
		if !ok {
			return fmt.Errorf("来源 %s 未启用或已删除", kind)
		}
		if !currentSource.Magpie {
			if currentSource.Protocol == apisource.ProtocolAnthropic {
				return nil
			}
			return sourceModelCache.refresh(ctx, currentSource, currentSource.APIKey(store))
		}
		ctx, cancel := context.WithTimeout(ctx, apiModelRequestTimeout)
		defer cancel()
		_, err := magpie.FetchModels(ctx, currentSource.BaseURL, currentSource.APIKey(store))
		return err
	}
	availability := &sourceAvailability{}
	availability.start(kind, cfg, secrets)
	opts.Available = func() (bool, string) { return availability.get(kind, cfg, secrets) }
	opts.ResolveClient = func(ctx context.Context, model, affinityKey string) (llm.Client, string, error) {
		return resolveSourceClient(ctx, kind, cfg, secrets, model, affinityKey)
	}
	opts.ContextWindow = func(model string) int {
		return sourceEngineContextWindow(kind, cfg, secrets, model)
	}
	opts.MaxTokens = func(model string) int {
		cached, ok := magpieCachedModelFor(kind, cfg, secrets, model)
		if !ok || cached.MaxTokens <= 0 {
			return 0
		}
		return cached.MaxTokens
	}
	opts.AcceptsImages = func(model string) bool {
		cached, ok := magpieCachedModelFor(kind, cfg, secrets, model)
		if !ok || len(cached.Input) == 0 {
			return true
		}
		for _, input := range cached.Input {
			if input == "image" {
				return true
			}
		}
		return false
	}
	opts.CostUSD = func(model string, usage harness.Usage) (float64, bool) {
		return sourceUsageCost(kind, cfg, secrets, model, usage)
	}
	return providermcode.New(opts)
}

// magpieCachedModelFor 按最新来源配置读取网关模型缓存。
func magpieCachedModelFor(kind harness.Kind, cfg config.Config, secrets config.SecretStore, model string) (magpie.Model, bool) {
	model = strings.TrimSpace(model)
	if model == "" || strings.EqualFold(model, "default") {
		return magpie.Model{}, false
	}
	current := mcodeCurrentConfig(cfg, secrets)
	store := mcodeCurrentSecrets(secrets)
	source, ok := findAPISource(apisource.Resolve(current, store), kind)
	if !ok || !source.Magpie {
		return magpie.Model{}, false
	}
	return magpie.CachedModel(source.BaseURL, source.APIKey(store), model)
}

// resolveSourceClient 每次调用都重新解析最新配置与凭据，保存新 Key 后无需重建 harness。
func resolveSourceClient(ctx context.Context, kind harness.Kind, cfg config.Config, secrets config.SecretStore, model, affinityKey string) (llm.Client, string, error) {
	current := mcodeCurrentConfig(cfg, secrets)
	store := mcodeCurrentSecrets(secrets)
	source, ok := findAPISource(apisource.Resolve(current, store), kind)
	if !ok {
		return nil, "", fmt.Errorf("来源 %s 未启用或已删除", kind)
	}
	modelID := strings.TrimSpace(model)
	if modelID == "" || strings.EqualFold(modelID, "default") {
		if source.Magpie {
			key := source.APIKey(store)
			models, _ := magpie.CachedModels(source.BaseURL, key)
			if len(models) == 0 {
				ctx, cancel := context.WithTimeout(ctx, magpieDefaultModelTimeout)
				var err error
				models, err = magpie.FetchModels(ctx, source.BaseURL, key)
				cancel()
				if err != nil {
					return nil, "", fmt.Errorf("来源 %s 获取模型目录失败：%w", kind, err)
				}
			}
			for _, candidate := range models {
				if candidate.ID == strings.TrimSpace(source.DefaultModel) {
					modelID = candidate.ID
					break
				}
			}
			if modelID == "" || strings.EqualFold(modelID, "default") {
				modelID = ""
				if len(models) > 0 {
					modelID = models[0].ID
				}
			}
		} else {
			modelID = sourceEngineDefaultModel(ctx, source, kind, store)
		}
	}
	if modelID == "" {
		return nil, "", fmt.Errorf("来源 %s 未配置默认模型", kind)
	}
	key := source.APIKey(store)
	if source.Magpie {
		target := magpieinject.Target{BaseURL: source.BaseURL, APIKey: key, ProviderID: source.Kind}
		client, modelID := magpieClient(target, affinityKey, modelID, engineReasoningEffort(source.BaseURL))
		return client, modelID, nil
	}
	return apisource.NewClient(source, key, modelID, apisource.ClientOptions{
		Headers: source.Headers,
		Effort:  engineReasoningEffort(source.BaseURL),
	}), modelID, nil
}

// sourceUsageCost 算 Magpie 来源本轮的费用：来源本身是网关，模型串不带端点前缀；
// 非 Magpie 来源不填费用。
func sourceUsageCost(kind harness.Kind, cfg config.Config, secrets config.SecretStore, model string, usage harness.Usage) (float64, bool) {
	current := mcodeCurrentConfig(cfg, secrets)
	store := mcodeCurrentSecrets(secrets)
	source, ok := findAPISource(apisource.Resolve(current, store), kind)
	if !ok || !source.Magpie {
		return 0, false
	}
	target := magpieinject.Target{BaseURL: source.BaseURL, APIKey: source.APIKey(store), ProviderID: source.Kind}
	return magpieUsageCost(target, model, usage)
}

// magpieUsageCost 用网关模型目录缓存的单价算费用：目录里没有这个模型或没有价格时返回 false。
// harness.Usage 的 InputTokens 已包含缓存的 token，CachedTokens 是读与写的合计；
// 两者无法拆分，统一按缓存读取价计，与界面按参考价估算会话费用的回退一致。
func magpieUsageCost(target magpieinject.Target, modelID string, usage harness.Usage) (float64, bool) {
	cached, ok := magpie.CachedModel(target.BaseURL, target.APIKey, modelID)
	if !ok || cached.Cost == nil {
		return 0, false
	}
	input := usage.InputTokens - usage.CachedTokens
	if input < 0 {
		input = 0
	}
	cacheRead := usage.CachedTokens
	if cached.Cost.CacheRead <= 0 {
		input, cacheRead = input+cacheRead, 0
	}
	return cached.Cost.USD(input, usage.OutputTokens, cacheRead, 0), true
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

// sourceEngineDefaultModel 优先使用配置的默认模型；官方 OpenAI 端点回退到 gpt-4o。
// 其余来源取模型目录的第一项，目录为空时同步刷新一次。
func sourceEngineDefaultModel(ctx context.Context, source apisource.Source, kind harness.Kind, secrets config.SecretStore) string {
	if model := strings.TrimSpace(source.DefaultModel); model != "" && !strings.EqualFold(model, "default") {
		return model
	}
	if isOfficialOpenAIEndpoint(kind, source.BaseURL) {
		return "gpt-4o"
	}
	key := source.APIKey(secrets)
	ids := append([]string(nil), source.Models...)
	ids = append(ids, sourceModelCache.models(source, key)...)
	if len(ids) == 0 {
		ctx, cancel := context.WithTimeout(ctx, apiModelRequestTimeout)
		_ = sourceModelCache.refresh(ctx, source, key)
		cancel()
		ids = sourceModelCache.models(source, key)
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
	return magpieinject.DefaultModel().ContextWindow
}
