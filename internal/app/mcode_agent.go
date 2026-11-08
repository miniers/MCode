package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/mcp"
	"mcode/internal/agent/prompt"
	"mcode/internal/agent/tools"
	"mcode/internal/agentconf"
	"mcode/internal/apisource"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/textgen"
)

// mcodeAnthropicAPIKeyAccount 是旧版 Anthropic API Key 的凭据账号名，只读不写。
const mcodeAnthropicAPIKeyAccount = "mcode.anthropicApiKey"

// mcodeAgentRuntime 保存 mcode 运行时使用的数据目录与凭据存储。
var mcodeAgentRuntime = struct {
	sync.RWMutex
	dataDir string
	secrets config.SecretStore
}{}

// setMcodeAgentRuntime 更新 mcode 运行时的数据目录与凭据存储。
func setMcodeAgentRuntime(dataDir string, secrets config.SecretStore) {
	mcodeAgentRuntime.Lock()
	mcodeAgentRuntime.dataDir, mcodeAgentRuntime.secrets = dataDir, secrets
	mcodeAgentRuntime.Unlock()
}

// mcodeCurrentConfig 返回最新配置，读取失败时保留调用方配置。
func mcodeCurrentConfig(fallback config.Config, secrets config.SecretStore) config.Config {
	mcodeAgentRuntime.RLock()
	dir := mcodeAgentRuntime.dataDir
	if mcodeAgentRuntime.secrets != nil {
		secrets = mcodeAgentRuntime.secrets
	}
	mcodeAgentRuntime.RUnlock()
	if dir != "" {
		if current, err := config.Load(dir); err == nil {
			config.ApplyEnv(&current, os.LookupEnv)
			return current
		}
		return fallback
	}
	return fallback
}

// mcodeCurrentSecrets 返回运行时凭据存储，未设置时使用调用方提供的存储。
func mcodeCurrentSecrets(fallback config.SecretStore) config.SecretStore {
	mcodeAgentRuntime.RLock()
	defer mcodeAgentRuntime.RUnlock()
	if mcodeAgentRuntime.secrets != nil {
		return mcodeAgentRuntime.secrets
	}
	return fallback
}

// registerMcodeTextGenerator 把内置 mcode 注册为 textgen 的外部生成器，
// 让 swarm 审查等不依赖 app 包的调用方也能用 mcode 完成一次性文本生成。
// 每次调用都重新读取最新配置与凭据；mcode 停用时注销，避免误用。
func registerMcodeTextGenerator(cfg config.Config, secrets config.SecretStore) {
	if !cfg.Providers.Mcode.Enabled {
		textgen.RegisterGenerator(config.ProviderMcode, nil)
		return
	}
	textgen.RegisterGenerator(config.ProviderMcode, func(ctx context.Context, model, effort, prompt string) (string, error) {
		current := mcodeCurrentConfig(cfg, secrets)
		return mcodeGenerateText(ctx, current, mcodeCurrentSecrets(secrets), model, effort, prompt)
	})
}

// mcodeAnthropicKey 返回 SecretStore 或环境变量中的 Anthropic API Key。
// 解析规则与 apisource.Resolve 一致：先读新账号 anthropic.apiKey，再读旧账号，最后读环境变量。
func mcodeAnthropicKey(secrets config.SecretStore) string {
	return apisource.AnthropicAPIKey(secrets)
}

// mcodeTargets 返回 mcode 当前可用的 Magpie 端点。
func mcodeTargets(cfg config.Config, secrets config.SecretStore) []magpieinject.Target {
	return magpieinject.Targets(cfg, secrets, nil)
}

// mcodeDefaultModel 返回 mcode 未指定模型时使用的完整模型 ID。
// 优先用 mcode 设置里手动指定的默认模型；没有时取 Magpie 模型目录缓存里的第一个模型。
func mcodeDefaultModel(cfg config.Config, secrets config.SecretStore) (string, error) {
	targets := mcodeTargets(cfg, secrets)
	setting := config.MagpieAgentSetting(cfg, config.ProviderMcode)
	if modelID := strings.TrimSpace(setting.DefaultModel); modelID != "" {
		endpointID := strings.TrimSpace(setting.DefaultEndpointID)
		for _, target := range targets {
			if target.APIKey == "" || (endpointID != "" && target.Endpoint != endpointID) {
				continue
			}
			return target.ProviderID + "/" + modelID, nil
		}
		return "", fmt.Errorf("默认模型 %s 的 Magpie 端点不可用", modelID)
	}
	hasKey := false
	for _, target := range targets {
		if target.APIKey == "" {
			continue
		}
		hasKey = true
		models, hit := magpie.CachedModels(target.BaseURL, target.APIKey)
		if !hit {
			continue
		}
		for _, model := range models {
			if id := strings.TrimSpace(model.ID); id != "" {
				return target.ProviderID + "/" + id, nil
			}
		}
	}
	if hasKey {
		return "", fmt.Errorf("没有可用的默认模型，请先在 Magpie 模型目录里选择默认模型")
	}
	return "", fmt.Errorf("没有配置可用的 Magpie 端点，请在设置中连接 Magpie")
}

// resolveMcodeClient 根据模型 ID 解析对应的上游客户端。
// 模型目录为空时界面会显示占位模型 default，这里统一当成空串（默认 Magpie 模型）。
func resolveMcodeClient(cfg config.Config, secrets config.SecretStore, model, affinityKey string) (llm.Client, string, error) {
	model = strings.TrimSpace(model)
	if strings.EqualFold(model, "default") {
		model = ""
	}
	targets := mcodeTargets(cfg, secrets)
	if model == "" {
		resolved, err := mcodeDefaultModel(cfg, secrets)
		if err != nil {
			return nil, "", err
		}
		model = resolved
	}
	if target, modelID, ok := magpieinject.ProviderOf(model, targets); ok {
		if target.APIKey == "" {
			return nil, "", fmt.Errorf("Magpie 端点 %s 没有 API Key", target.ProviderID)
		}
		client, modelID := magpieClient(target, affinityKey, modelID, nil)
		return client, modelID, nil
	}
	if strings.HasPrefix(model, "anthropic/") {
		if !cfg.Providers.Anthropic.IsEnabled() {
			return nil, "", fmt.Errorf("Anthropic 已在设置中关闭")
		}
		key := mcodeAnthropicKey(secrets)
		if key == "" {
			return nil, "", fmt.Errorf("未配置 Anthropic API Key")
		}
		baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")), "/")
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		client := llm.NewAnthropic(llm.AnthropicConfig{BaseURL: baseURL, APIKey: key})
		return client, strings.TrimPrefix(model, "anthropic/"), nil
	}
	return nil, "", fmt.Errorf("没有找到模型 %s 对应的 Magpie 端点，请在设置中连接 Magpie", model)
}

// mcodeMagpieConfig 构造 Magpie 网关客户端配置。
// 会话亲和头让网关把同一会话路由到同一上游账号，提高缓存命中率；
// DeepSeek 系模型按 id 声明 reasoning_content 回传方言。
func mcodeMagpieConfig(target magpieinject.Target, affinityKey, modelID string) llm.OpenAIConfig {
	return llm.OpenAIConfig{
		BaseURL:       target.BaseURL,
		APIKey:        target.APIKey,
		Headers:       mcodeAffinityHeaders(affinityKey),
		EchoReasoning: isDeepSeekModel(modelID),
	}
}

// mcodeAffinityHeaders 返回 Magpie 的会话亲和头：与 pi 的 openai 会话亲和格式一致，
// 使用 session_id、x-client-request-id、x-session-affinity；affinityKey 为空时不带。
func mcodeAffinityHeaders(affinityKey string) map[string]string {
	if affinityKey == "" {
		return nil
	}
	return map[string]string{
		"session_id":          affinityKey,
		"x-client-request-id": affinityKey,
		"x-session-affinity":  affinityKey,
	}
}

// isClaudeModel 判断模型是否属于 Claude 家族：模型 ID 小写后包含 claude 即为真。
func isClaudeModel(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "claude")
}

// magpieClient 为 Magpie 网关上的模型选择请求协议：Claude 家族走 Anthropic Messages，
// 保留 thinking 签名、prompt caching 与 tool_use 的原样语义；其余模型走 chat completions。
// Anthropic 请求的 thinking 由 Request.Effort 控制，effort 只作用于 OpenAI 兼容请求。
func magpieClient(target magpieinject.Target, affinityKey, modelID string, effort func(model, effort string) (string, bool)) (llm.Client, string) {
	if isClaudeModel(modelID) {
		// NewAnthropic 会在根地址后补 /v1/messages，这里先换成与 claude CLI 相同的根地址。
		clientCfg := llm.AnthropicConfig{
			BaseURL: magpieinject.AnthropicBaseURL(target.BaseURL),
			APIKey:  target.APIKey,
			Headers: mcodeAffinityHeaders(affinityKey),
		}
		return llm.NewAnthropic(clientCfg), modelID
	}
	cfg := mcodeMagpieConfig(target, affinityKey, modelID)
	cfg.Effort = effort
	return llm.NewOpenAI(cfg), modelID
}

// isDeepSeekModel 判断模型 ID 是否属于 DeepSeek 系，规则与 Magpie 桥的 isDeepSeekModel 一致。
func isDeepSeekModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "deepseek")
}

// mcodeAnthropicContextWindow 是 anthropic/ 前缀模型的上下文窗口（tokens）。
const mcodeAnthropicContextWindow = 200000

// mcodeContextWindow 返回模型的上下文窗口：anthropic/ 前缀固定 200000；Magpie 模型
// 按网关缓存里的元数据取值，缓存里没有这个模型时用默认窗口；解析不到端点返回 0。
func mcodeContextWindow(cfg config.Config, secrets config.SecretStore, model string) int {
	if strings.HasPrefix(model, "anthropic/") {
		return mcodeAnthropicContextWindow
	}
	target, modelID, ok := magpieinject.ProviderOf(model, mcodeTargets(cfg, secrets))
	if !ok {
		return 0
	}
	if cached, hit := magpie.CachedModel(target.BaseURL, target.APIKey, modelID); hit && cached.ContextWindow > 0 {
		return cached.ContextWindow
	}
	return magpieinject.DefaultModel().ContextWindow
}

// mcodeMaxTokens 返回模型的输出上限：anthropic/ 前缀交给 llm 层默认值；Magpie 模型
// 按网关缓存里的元数据取值，取不到返回 0 也交给 llm 层默认值。
func mcodeMaxTokens(cfg config.Config, secrets config.SecretStore, model string) int {
	if strings.HasPrefix(model, "anthropic/") {
		return 0
	}
	target, modelID, ok := magpieinject.ProviderOf(model, mcodeTargets(cfg, secrets))
	if !ok {
		return 0
	}
	if cached, hit := magpie.CachedModel(target.BaseURL, target.APIKey, modelID); hit {
		return cached.MaxTokens
	}
	return 0
}

// mcodeEngineOptions 返回内置引擎与具体来源无关的公共选项。
// API Key 类来源与 mcode 本身共用同一个会话存储目录：会话 ID 由引擎随机生成，
// 不同来源之间不会互相覆盖。
func mcodeEngineOptions(secrets config.SecretStore, dataDir string, mcpInjection providerMCP) providermcode.Options {
	storeDir := ""
	if dataDir != "" {
		storeDir = filepath.Join(dataDir, "mcode", "sessions")
	}
	setMcodeAgentRuntime(dataDir, secrets)
	return providermcode.Options{
		StoreDir:  storeDir,
		MaxRounds: 0,
		SystemPrompt: func(turn harness.Turn, toolNames []string) string {
			home, _ := os.UserHomeDir()
			return prompt.Build(prompt.Options{
				WorkDir: turn.WorkDir, AdditionalDirs: turn.AdditionalDirs,
				HomeDir: home, GlobalDir: filepath.Join(home, ".agents"),
				ToolNames: toolNames, Skills: mcodeSkills(home, turn.WorkDir, dataDir), Now: time.Now(),
			})
		},
		ReadOnlyDirs: func(turn harness.Turn) []string {
			home, _ := os.UserHomeDir()
			var dirs []string
			seen := make(map[string]bool)
			for _, skill := range mcodeSkills(home, turn.WorkDir, dataDir) {
				dir := filepath.Dir(skill.Path)
				if !seen[dir] {
					seen[dir] = true
					dirs = append(dirs, dir)
				}
			}
			return dirs
		},
		ExtraTools: func(ctx context.Context, turn harness.Turn) []tools.Tool {
			var external []harness.MCPEndpoint
			if mcpInjection.External != nil {
				external = mcpInjection.External(turn.WorkDir, turn.ThreadID)
			}
			return mcp.Tools(ctx, mcp.Servers(turn, mcpInjection.Builtin, external))
		},
	}
}

// buildMcodeHarness 构造内置 mcode agent harness。
func buildMcodeHarness(cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {
	opts := mcodeEngineOptions(secrets, dataDir, mcpInjection)
	remote.SetMcodeModels(mcodeModelCatalog)
	opts.ResolveClient = func(model, affinityKey string) (llm.Client, string, error) {
		current := mcodeCurrentConfig(cfg, secrets)
		return resolveMcodeClient(current, mcodeCurrentSecrets(secrets), model, affinityKey)
	}
	opts.ContextWindow = func(model string) int {
		current := mcodeCurrentConfig(cfg, secrets)
		return mcodeContextWindow(current, mcodeCurrentSecrets(secrets), model)
	}
	opts.MaxTokens = func(model string) int {
		current := mcodeCurrentConfig(cfg, secrets)
		return mcodeMaxTokens(current, mcodeCurrentSecrets(secrets), model)
	}
	opts.CostUSD = func(model string, usage harness.Usage) (float64, bool) {
		current := mcodeCurrentConfig(cfg, secrets)
		return mcodeUsageCost(current, mcodeCurrentSecrets(secrets), model, usage)
	}
	return providermcode.New(opts)
}

// mcodeModelCatalog 从最新配置生成远程模型目录。
func mcodeModelCatalog() []wire.ProviderModel {
	mcodeAgentRuntime.RLock()
	dir, secrets := mcodeAgentRuntime.dataDir, mcodeAgentRuntime.secrets
	mcodeAgentRuntime.RUnlock()
	if dir != "" {
		cfg, err := config.Load(dir)
		if err != nil {
			return nil
		}
		config.ApplyEnv(&cfg, os.LookupEnv)
		return mcodeModelsForConfig(cfg, secrets)
	}
	cfg := config.Defaults()
	config.ApplyEnv(&cfg, os.LookupEnv)
	return mcodeModelsForConfig(cfg, secrets)
}

// mcodeModelsForConfig 用内置引擎实际可用的 Magpie 端点生成模型目录：只保留 Magpie 页
// 为 mcode 勾选的模型（没有 mcode 勾选时沿用 pi 的勾选），行顺序与勾选顺序一致，
// 手动默认模型有效时排在最前。行 ID 是 target.ProviderID + 模型 ID，与
// resolveMcodeClient 的解析规则一致。
func mcodeModelsForConfig(cfg config.Config, secrets config.SecretStore) []wire.ProviderModel {
	setting := config.MagpieAgentSetting(cfg, config.ProviderMcode)
	defaultEndpointID := strings.TrimSpace(setting.DefaultEndpointID)
	defaultModelID := strings.TrimSpace(setting.DefaultModel)
	targets := make(map[string]magpieinject.Target, len(cfg.Providers.Custom))
	for _, target := range mcodeTargets(cfg, secrets) {
		targets[target.Endpoint] = target
	}
	rows := make([]wire.ProviderModel, 0)
	defaultRow := -1
	seen := make(map[string]bool, len(setting.Selections))
	for _, selection := range setting.Selections {
		endpointID := strings.TrimSpace(selection.EndpointID)
		if endpointID == "" || seen[endpointID] {
			continue
		}
		seen[endpointID] = true
		target, ok := targets[endpointID]
		if !ok || target.APIKey == "" {
			continue
		}
		ids := mcodeSelectionModels(setting, endpointID)
		if len(ids) == 0 {
			continue
		}
		fetched, hit := magpie.CachedModels(target.BaseURL, target.APIKey)
		if !hit {
			// 缓存没拉过时先在后台预热；本次仍按勾选的模型 ID 出行，元数据用默认值补。
			warmMagpieModels(target)
			fetched = nil
		}
		for _, model := range magpieinject.Selected(ids, fetched) {
			if defaultRow < 0 && endpointID == defaultEndpointID && model.ID == defaultModelID {
				defaultRow = len(rows)
			}
			rows = append(rows, wire.ProviderModel{
				ID:           target.ProviderID + "/" + model.ID,
				Name:         magpie.ShortName(model),
				Efforts:      mcodeEfforts(model),
				ContextLimit: model.ContextWindow,
				Cost:         magpieModelCost(model),
			})
		}
	}
	if len(rows) > 0 {
		if defaultRow > 0 {
			rows[0], rows[defaultRow] = rows[defaultRow], rows[0]
		}
		rows[0].IsDefault = true
	}
	if cfg.Providers.Anthropic.IsEnabled() && mcodeAnthropicKey(secrets) != "" {
		for _, row := range []wire.ProviderModel{
			{ID: "anthropic/claude-opus-5-5", Name: "Claude Opus 5.5"},
			{ID: "anthropic/claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
			{ID: "anthropic/claude-haiku-5-5", Name: "Claude Haiku 5.5"},
		} {
			row.Efforts = []string{"low", "medium", "high", "xhigh", "max"}
			if len(rows) == 0 {
				row.IsDefault = true
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// mcodeSelectionModels 返回某端点在内置引擎勾选里的模型 ID，按勾选顺序去空；
// 手动默认模型仍在该端点勾选里时移到最前，让模型列表第一项与默认一致。
func mcodeSelectionModels(setting config.MagpieAgentConfig, endpointID string) []string {
	ids := make([]string, 0)
	for _, selection := range setting.Selections {
		if strings.TrimSpace(selection.EndpointID) != endpointID {
			continue
		}
		for _, model := range selection.Models {
			if model = strings.TrimSpace(model); model != "" {
				ids = append(ids, model)
			}
		}
	}
	if len(ids) == 0 || strings.TrimSpace(setting.DefaultEndpointID) != endpointID {
		return ids
	}
	defaultModel := strings.TrimSpace(setting.DefaultModel)
	for i, id := range ids {
		if id != defaultModel {
			continue
		}
		if i == 0 {
			return ids
		}
		reordered := make([]string, 0, len(ids))
		reordered = append(reordered, id)
		reordered = append(reordered, ids[:i]...)
		reordered = append(reordered, ids[i+1:]...)
		return reordered
	}
	return ids
}

// magpieModelCost 把网关模型的价格转成目录行价格；网关没有报价格时返回 nil。
func magpieModelCost(model magpie.Model) *wire.ModelCost {
	if model.Cost == nil {
		return nil
	}
	return &wire.ModelCost{
		Input:      model.Cost.Input,
		Output:     model.Cost.Output,
		CacheRead:  model.Cost.CacheRead,
		CacheWrite: model.Cost.CacheWrite,
	}
}

// mcodeUsageCost 按网关模型目录的价格算本轮费用：模型串按端点前缀解析，
// 非 Magpie 模型、目录里没有这个模型或没有价格时返回 false。
func mcodeUsageCost(cfg config.Config, secrets config.SecretStore, model string, usage harness.Usage) (float64, bool) {
	target, modelID, ok := magpieinject.ProviderOf(strings.TrimSpace(model), mcodeTargets(cfg, secrets))
	if !ok {
		return 0, false
	}
	return magpieUsageCost(target, modelID, usage)
}

// mcodeEfforts 返回内置引擎可用的思考档位：网关给了档位就按 mcode 后端过滤，
// 没有档位且不是推理模型时不填，只有元数据缺失时沿用五档默认值。
func mcodeEfforts(model magpie.Model) []string {
	if len(model.Levels) > 0 {
		return magpieEffortsFor(config.ProviderMcode, model.Levels)
	}
	if !model.Reasoning {
		return nil
	}
	return []string{"low", "medium", "high", "xhigh", "max"}
}

// mcodeSkills 返回仅来自全局与项目目录的已启用技能。
func mcodeSkills(home, workDir, dataDir string) []prompt.Skill {
	absWorkDir, _ := filepath.Abs(workDir)
	skills, _ := agentconf.ScanSkills(agentconf.Options{Home: home, ProjectDir: absWorkDir, DataDir: dataDir})
	globalRoot := filepath.Join(home, ".agents", "skills")
	projectRoot := filepath.Join(absWorkDir, ".agents", "skills")
	var result []prompt.Skill
	for _, skill := range skills {
		path := filepath.Clean(skill.Path)
		if skill.Enabled && (strings.HasPrefix(path, globalRoot+string(os.PathSeparator)) ||
			strings.HasPrefix(path, projectRoot+string(os.PathSeparator))) {
			result = append(result, prompt.Skill{Name: skill.Name, Description: skill.Description, Path: path})
		}
	}
	return result
}
