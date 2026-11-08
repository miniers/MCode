package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/mcp"
	"mcode/internal/agent/prompt"
	"mcode/internal/agent/tools"
	"mcode/internal/agentconf"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

// mcodeAnthropicAPIKeyAccount 是 Anthropic API Key 的凭据账号名。
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

// mcodeAnthropicKey 返回 SecretStore 或环境变量中的 Anthropic API Key。
func mcodeAnthropicKey(secrets config.SecretStore) string {
	if secrets != nil {
		if key, err := secrets.Get(mcodeAnthropicAPIKeyAccount); err == nil && strings.TrimSpace(key) != "" {
			return strings.TrimSpace(key)
		}
	}
	return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
}

// mcodeTargets 返回 mcode 当前可用的 Magpie 端点。
func mcodeTargets(cfg config.Config, secrets config.SecretStore) []magpieinject.Target {
	return magpieinject.Targets(cfg, secrets, nil)
}

// resolveMcodeClient 根据模型 ID 解析对应的上游客户端。
func resolveMcodeClient(cfg config.Config, secrets config.SecretStore, model, affinityKey string) (llm.Client, string, error) {
	targets := mcodeTargets(cfg, secrets)
	if model == "" {
		for _, target := range targets {
			if target.APIKey != "" {
				modelID := magpieinject.DefaultModel().ID
				return llm.NewOpenAI(mcodeMagpieConfig(target, affinityKey, modelID)), modelID, nil
			}
		}
		return nil, "", fmt.Errorf("没有配置可用的 Magpie 端点，请在设置中连接 Magpie")
	}
	if target, modelID, ok := magpieinject.ProviderOf(model, targets); ok {
		if target.APIKey == "" {
			return nil, "", fmt.Errorf("Magpie 端点 %s 没有 API Key", target.ProviderID)
		}
		return llm.NewOpenAI(mcodeMagpieConfig(target, affinityKey, modelID)), modelID, nil
	}
	if strings.HasPrefix(model, "anthropic/") {
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
	cfg := llm.OpenAIConfig{BaseURL: target.BaseURL, APIKey: target.APIKey, EchoReasoning: isDeepSeekModel(modelID)}
	if affinityKey != "" {
		// 与 pi 的 openai 会话亲和格式一致：session_id、x-client-request-id、x-session-affinity。
		cfg.Headers = map[string]string{
			"session_id":          affinityKey,
			"x-client-request-id": affinityKey,
			"x-session-affinity":  affinityKey,
		}
	}
	return cfg
}

// isDeepSeekModel 判断模型 ID 是否属于 DeepSeek 系，规则与 Magpie 桥的 isDeepSeekModel 一致。
func isDeepSeekModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "deepseek")
}

// mcodeAnthropicContextWindow 是 anthropic/ 前缀模型的上下文窗口（tokens）。
const mcodeAnthropicContextWindow = 200000

// mcodeContextWindow 返回模型的上下文窗口：anthropic/ 前缀固定 200000；Magpie 模型按
// pi 勾选查找，网关缓存里有元数据就用它，否则用 pi 注入的默认窗口；查不到返回 0。
func mcodeContextWindow(cfg config.Config, secrets config.SecretStore, model string) int {
	if strings.HasPrefix(model, "anthropic/") {
		return mcodeAnthropicContextWindow
	}
	target, modelID, ok := magpieinject.ProviderOf(model, mcodeTargets(cfg, secrets))
	if !ok {
		return 0
	}
	for _, piTarget := range piMagpieAllTargets(cfg, secrets) {
		if piTarget.providerID != target.ProviderID || !slices.Contains(piTarget.models, modelID) {
			continue
		}
		if cached, hit := magpie.CachedModel(piTarget.baseURL, piTarget.apiKey, modelID); hit && cached.ContextWindow > 0 {
			return cached.ContextWindow
		}
		return magpieinject.DefaultModel().ContextWindow
	}
	return 0
}

// buildMcodeHarness 构造内置 mcode agent harness。
func buildMcodeHarness(cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {
	storeDir := ""
	if dataDir != "" {
		storeDir = filepath.Join(dataDir, "mcode", "sessions")
	}
	setMcodeAgentRuntime(dataDir, secrets)
	remote.SetMcodeModels(mcodeModelCatalog)
	return providermcode.New(providermcode.Options{
		StoreDir:  storeDir,
		MaxRounds: 0,
		ResolveClient: func(model, affinityKey string) (llm.Client, string, error) {
			current := mcodeCurrentConfig(cfg, secrets)
			return resolveMcodeClient(current, mcodeCurrentSecrets(secrets), model, affinityKey)
		},
		ContextWindow: func(model string) int {
			current := mcodeCurrentConfig(cfg, secrets)
			return mcodeContextWindow(current, mcodeCurrentSecrets(secrets), model)
		},
		SystemPrompt: func(turn harness.Turn, toolNames []string) string {
			home, _ := os.UserHomeDir()
			return prompt.Build(prompt.Options{
				WorkDir: turn.WorkDir, AdditionalDirs: turn.AdditionalDirs,
				HomeDir: home, GlobalDir: filepath.Join(home, ".agents"),
				ToolNames: toolNames, Skills: mcodeSkills(home, turn.WorkDir, dataDir), Now: time.Now(),
			})
		},
		ExtraTools: func(ctx context.Context, turn harness.Turn) []tools.Tool {
			var external []harness.MCPEndpoint
			if mcpInjection.External != nil {
				external = mcpInjection.External(turn.WorkDir, turn.ThreadID)
			}
			return mcp.Tools(ctx, mcp.Servers(turn, mcpInjection.Builtin, external))
		},
	})
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

// mcodeModelsForConfig 生成与 pi 共用模型 ID 和名称规则的模型目录。
func mcodeModelsForConfig(cfg config.Config, secrets config.SecretStore) []wire.ProviderModel {
	targets := piMagpieAllTargets(cfg, secrets)
	selected := false
	for _, target := range targets {
		if len(target.models) > 0 {
			selected = true
			break
		}
	}
	rows := make([]wire.ProviderModel, 0)
	for _, target := range targets {
		if target.apiKey == "" {
			continue
		}
		ids := target.models
		if !selected {
			ids = []string{magpieinject.DefaultModel().ID}
		}
		for _, id := range ids {
			rows = append(rows, wire.ProviderModel{
				ID:        target.providerID + "/" + id,
				Name:      magpie.ShortName(magpie.Model{ID: id}),
				Efforts:   []string{"low", "medium", "high", "xhigh", "max"},
				IsDefault: len(rows) == 0,
			})
		}
	}
	if mcodeAnthropicKey(secrets) != "" {
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
