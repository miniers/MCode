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
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/magpieinject"
	"mcode/internal/providers/magpie"
	providermcode "mcode/internal/providers/mcode"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

const mcodeAnthropicAPIKeyAccount = "mcode.anthropicApiKey"

var mcodeAgentRuntime = struct {
	sync.RWMutex
	dataDir string
	secrets config.SecretStore
}{}

func setMcodeAgentRuntime(dataDir string, secrets config.SecretStore) {
	mcodeAgentRuntime.Lock()
	mcodeAgentRuntime.dataDir, mcodeAgentRuntime.secrets = dataDir, secrets
	mcodeAgentRuntime.Unlock()
}

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
		return config.Defaults()
	}
	return fallback
}

func mcodeAnthropicKey(secrets config.SecretStore) string {
	if secrets != nil {
		if key, err := secrets.Get(mcodeAnthropicAPIKeyAccount); err == nil && strings.TrimSpace(key) != "" {
			return strings.TrimSpace(key)
		}
	}
	return strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
}

func mcodeTargets(cfg config.Config, secrets config.SecretStore) []magpieinject.Target {
	return magpieinject.Targets(cfg, secrets, nil)
}

func resolveMcodeClient(cfg config.Config, secrets config.SecretStore, model string) (llm.Client, string, error) {
	targets := mcodeTargets(cfg, secrets)
	if model == "" {
		for _, target := range targets {
			if target.APIKey != "" {
				return llm.NewOpenAI(llm.OpenAIConfig{BaseURL: target.BaseURL, APIKey: target.APIKey}), magpieinject.DefaultModel().ID, nil
			}
		}
		return nil, "", fmt.Errorf("没有配置可用的 Magpie 端点，请在设置中连接 Magpie")
	}
	if target, modelID, ok := magpieinject.ProviderOf(model, targets); ok {
		if target.APIKey == "" {
			return nil, "", fmt.Errorf("Magpie 端点 %s 没有 API Key", target.ProviderID)
		}
		return llm.NewOpenAI(llm.OpenAIConfig{BaseURL: target.BaseURL, APIKey: target.APIKey}), modelID, nil
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
		return llm.NewAnthropic(llm.AnthropicConfig{BaseURL: baseURL, APIKey: key}), strings.TrimPrefix(model, "anthropic/"), nil
	}
	return nil, "", fmt.Errorf("没有找到模型 %s 对应的 Magpie 端点，请在设置中连接 Magpie", model)
}

func buildMcodeHarness(cfg config.Config, secrets config.SecretStore, dataDir string, mcpInjection providerMCP) harness.Harness {
	storeDir := ""
	if dataDir != "" {
		storeDir = filepath.Join(dataDir, "mcode", "sessions")
	}
	setMcodeAgentRuntime(dataDir, secrets)
	remote.SetMcodeModels(mcodeModelCatalog)
	return providermcode.New(providermcode.Options{
		StoreDir:     storeDir,
		DefaultModel: "",
		MaxRounds:    20,
		ResolveClient: func(model string) (llm.Client, string, error) {
			current := mcodeCurrentConfig(cfg, secrets)
			currentSecrets := secrets
			mcodeAgentRuntime.RLock()
			if mcodeAgentRuntime.secrets != nil {
				currentSecrets = mcodeAgentRuntime.secrets
			}
			mcodeAgentRuntime.RUnlock()
			return resolveMcodeClient(current, currentSecrets, model)
		},
		SystemPrompt: func(turn harness.Turn, toolNames []string) string {
			home, _ := os.UserHomeDir()
			absWorkDir, _ := filepath.Abs(turn.WorkDir)
			skills, _ := agentconf.ScanSkills(agentconf.Options{Home: home, ProjectDir: absWorkDir, DataDir: dataDir})
			var promptSkills []prompt.Skill
			globalRoot := filepath.Join(home, ".agents", "skills")
			projectRoot := filepath.Join(absWorkDir, ".agents", "skills")
			for _, skill := range skills {
				path := filepath.Clean(skill.Path)
				if (strings.HasPrefix(path, globalRoot+string(os.PathSeparator)) || strings.HasPrefix(path, projectRoot+string(os.PathSeparator))) && skill.Enabled {
					promptSkills = append(promptSkills, prompt.Skill{Name: skill.Name, Description: skill.Description, Path: path})
				}
			}
			return prompt.Build(prompt.Options{WorkDir: turn.WorkDir, AdditionalDirs: turn.AdditionalDirs, HomeDir: home, GlobalDir: filepath.Join(home, ".agents"), ToolNames: toolNames, Skills: promptSkills, Now: time.Now(), OS: ""})
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

func mcodeModelCatalog() []wire.ProviderModel {
	mcodeAgentRuntime.RLock()
	dir, secrets := mcodeAgentRuntime.dataDir, mcodeAgentRuntime.secrets
	mcodeAgentRuntime.RUnlock()
	cfg := config.Config{}
	if dir != "" {
		cfg, _ = config.Load(dir)
	}
	config.ApplyEnv(&cfg, os.LookupEnv)
	return mcodeModelsForConfig(cfg, secrets)
}

func mcodeModelsForConfig(cfg config.Config, secrets config.SecretStore) []wire.ProviderModel {
	setting := config.MagpieAgentSetting(cfg, config.ProviderMcode)
	targets := magpieinject.Targets(cfg, secrets, setting.Selections)
	rows := make([]wire.ProviderModel, 0)
	for _, target := range targets {
		if target.APIKey == "" {
			continue
		}
		for _, selection := range setting.Selections {
			if selection.EndpointID != target.Endpoint {
				continue
			}
			for _, id := range selection.Models {
				rows = append(rows, wire.ProviderModel{ID: target.ProviderID + "/" + id, Name: magpie.ShortName(magpie.Model{ID: id}), Efforts: []string{"low", "medium", "high", "xhigh", "max"}, IsDefault: len(rows) == 0})
			}
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
