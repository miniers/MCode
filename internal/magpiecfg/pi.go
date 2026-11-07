package magpiecfg

// 本文件生成 pi 的全局配置计划：models.json 的 providers.<ProviderID> 与
// settings.json 的 defaultProvider / defaultModel。字段结构对齐 pi 的
// models.json，也与 internal/providers/pi 里桥接注册的 provider 一致，
// 保证切到 global 后原有模型引用不需要改。

import (
	"errors"
	"fmt"
	"strings"

	"mcode/internal/providers/magpie"
)

// PiAgent 是 pi 在 mcode 配置里的 agent 名，与 config.ProviderPi 相同。
const PiAgent = "pi"

// piProviderAPI 是 pi 里 OpenAI 兼容 provider 的 api 标识。
const piProviderAPI = "openai-completions"

// pi 的桥在模型缺元数据时用的默认值；计划里补同样的值，避免切换模式后
// 上下文窗口或输出上限忽然变成 0。
const (
	piDefaultContextWindow = 200000
	piDefaultMaxTokens     = 32000
)

// PiOptions 是生成 pi 计划所需的输入。
type PiOptions struct {
	// Providers 按勾选顺序列出要接管的端点，Provider 是 pi provider id。
	Providers []magpie.PiProvider
	// DefaultProvider / DefaultModel 是用户手动指定的主模型；为空或不在
	// Providers 里时退回首个 provider 的首个模型。
	DefaultProvider string
	DefaultModel    string
}

// piBuiltProvider 是已校验的 provider 及其模型 ID，用来挑默认模型。
type piBuiltProvider struct {
	provider string
	object   map[string]any
	models   []string
}

// PiPlan 生成 pi 两份 JSON 的写入计划。模型元数据直接取自 PiProvider
// （通常由 magpieinject.Selected 补齐），缺省值按桥的规则补。
func PiPlan(opts PiOptions) (Plan, error) {
	providers := make([]piBuiltProvider, 0, len(opts.Providers))
	seen := make(map[string]bool, len(opts.Providers))
	for _, p := range opts.Providers {
		id := strings.TrimSpace(p.Provider)
		baseURL := strings.TrimRight(strings.TrimSpace(p.BaseURL), "/")
		apiKey := strings.TrimSpace(p.APIKey)
		switch {
		case id == "":
			return Plan{}, errors.New("magpiecfg: pi 的 provider ID 为空")
		case baseURL == "":
			return Plan{}, fmt.Errorf("magpiecfg: pi provider %q 的 BaseURL 为空", id)
		case apiKey == "":
			return Plan{}, fmt.Errorf("magpiecfg: pi provider %q 的 API Key 为空", id)
		case seen[id]:
			return Plan{}, fmt.Errorf("magpiecfg: pi provider ID %q 重复", id)
		}
		models, ids := piModelRows(p.Models)
		if len(models) == 0 {
			return Plan{}, fmt.Errorf("magpiecfg: pi provider %q 没有可写的模型", id)
		}
		seen[id] = true
		providers = append(providers, piBuiltProvider{
			provider: id,
			models:   ids,
			object: map[string]any{
				"baseUrl": baseURL,
				"apiKey":  piEscapeAPIKey(apiKey),
				"api":     piProviderAPI,
				"models":  models,
			},
		})
	}
	if len(providers) == 0 {
		return Plan{}, errors.New("magpiecfg: pi 没有可写的 provider")
	}

	defaultProvider, defaultModel := piDefault(providers, opts.DefaultProvider, opts.DefaultModel)
	modelsPath, settingsPath, err := PiPaths()
	if err != nil {
		return Plan{}, err
	}
	providerFields := make([]Field, 0, len(providers))
	for _, p := range providers {
		providerFields = append(providerFields, Field{Path: "providers." + p.provider, Value: p.object})
	}
	return Plan{
		Agent: PiAgent,
		Files: []FilePlan{
			{Path: modelsPath, Format: FormatJSON, Fields: providerFields},
			{Path: settingsPath, Format: FormatJSON, Fields: []Field{
				{Path: "defaultProvider", Value: defaultProvider},
				{Path: "defaultModel", Value: defaultModel},
			}},
		},
	}, nil
}

// piDefault 选出 settings.json 里的主模型。只有 provider 与模型都在写入
// 列表里才采用用户的选择，否则退回首个 provider 的首个模型，保证 pi 启动
// 时一定指向一个已定义的模型。
func piDefault(providers []piBuiltProvider, defaultProvider, defaultModel string) (string, string) {
	wantProvider := strings.TrimSpace(defaultProvider)
	wantModel := strings.TrimSpace(defaultModel)
	if wantProvider != "" && wantModel != "" {
		for _, p := range providers {
			if p.provider != wantProvider {
				continue
			}
			for _, id := range p.models {
				if id == wantModel {
					return wantProvider, wantModel
				}
			}
		}
	}
	return providers[0].provider, providers[0].models[0]
}

// piModelRows 按勾选顺序生成模型行，跳过空 ID 与重复 ID，并返回同样的
// 顺序供默认模型匹配。模型 ID 是 pi 里唯一的引用，不去重会让 pi 显示
// 重复条目。
func piModelRows(models []magpie.Model) ([]map[string]any, []string) {
	rows := make([]map[string]any, 0, len(models))
	ids := make([]string, 0, len(models))
	seen := make(map[string]bool, len(models))
	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
		rows = append(rows, piModelRow(id, m))
	}
	return rows, ids
}

// piModelRow 把 magpie 的模型元数据转成 pi 的模型条目，缺省值按桥的规则补。
func piModelRow(id string, m magpie.Model) map[string]any {
	name := strings.TrimSpace(m.Name)
	if name == "" {
		name = id
	}
	input := make([]string, 0, len(m.Input))
	for _, kind := range m.Input {
		if kind = strings.TrimSpace(kind); kind != "" {
			input = append(input, kind)
		}
	}
	if len(input) == 0 {
		input = []string{"text"}
	}
	cost := map[string]any{
		"input":      0.0,
		"output":     0.0,
		"cacheRead":  0.0,
		"cacheWrite": 0.0,
	}
	if m.Cost != nil {
		cost["input"] = m.Cost.Input
		cost["output"] = m.Cost.Output
		cost["cacheRead"] = m.Cost.CacheRead
		cost["cacheWrite"] = m.Cost.CacheWrite
	}
	contextWindow := m.ContextWindow
	if contextWindow <= 0 {
		contextWindow = piDefaultContextWindow
	}
	maxTokens := m.MaxTokens
	if maxTokens <= 0 {
		maxTokens = piDefaultMaxTokens
	}
	compat := map[string]any{"sendSessionAffinityHeaders": true}
	if strings.Contains(strings.ToLower(id), "deepseek") {
		compat["thinkingFormat"] = "deepseek"
		compat["requiresReasoningContentOnAssistantMessages"] = true
	}
	row := map[string]any{
		"id":            id,
		"name":          name,
		"reasoning":     m.Reasoning,
		"input":         input,
		"cost":          cost,
		"contextWindow": contextWindow,
		"maxTokens":     maxTokens,
		"compat":        compat,
	}
	if len(m.ThinkingLevelMap) > 0 {
		levels := make(map[string]*string, len(m.ThinkingLevelMap))
		for level, model := range m.ThinkingLevelMap {
			levels[level] = model
		}
		row["thinkingLevelMap"] = levels
	}
	return row
}

// piEscapeAPIKey 按 pi 的 models.json 规则转义会被当成环境变量或命令的
// 字面量 key：$$ 与 $! 分别表示字面量 $ 与 !。
func piEscapeAPIKey(key string) string {
	if strings.HasPrefix(key, "$") || strings.HasPrefix(key, "!") {
		return "$" + key
	}
	return key
}
