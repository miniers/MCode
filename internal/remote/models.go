package remote

import (
	"strings"

	"mcode/internal/modelsdev"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/cline"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/commandcode"
	"mcode/internal/providers/openai"
	"mcode/internal/remote/wire"
)

func stringPtr(s string) *string {
	return &s
}

// claudeAliasSeedIDs 是旧静态目录里的模型 ID：目录改为只认运行时探测后，
// 已有会话仍按这些 ID 记住模型，IsValidModel 必须继续放行它们。
// default/opus/sonnet/haiku 是 CLI 别名，claude-fable-5-1[1m] 是旧目录里的完整 ID。
var claudeAliasSeedIDs = map[string]bool{
	"default":              true,
	"opus":                 true,
	"sonnet":               true,
	"haiku":                true,
	"claude-fable-5-1[1m]": true,
}

// normalizeClaudeModelID 把 claude 模型 ID 归一化成可比较的形式：统一小写，
// 去掉末尾的 1m 上下文标记（claude-fable-5-1[1m] 与 claude-fable-5-1 是同一模型）。
func normalizeClaudeModelID(id string) string {
	return strings.TrimSuffix(strings.ToLower(id), "[1m]")
}

// mergedClaudeModels 把 claude CLI 实时列出的模型映射成协议行：
// 按归一化 ID 去重（claude-fable-5-1[1m] 与 claude-fable-5-1 是同一模型），保留 CLI 给出的顺序。
// 别名行（default/opus/sonnet/haiku）保留不动，只把 CLI 报告的 resolvedModel 写进 AliasOf，
// 由界面据此折叠重复行；实体行自身就是目标模型，AliasOf 留空。
// 目录只认运行时探测结果，不再叠加静态种子行。
func mergedClaudeModels(live []claude.Model) []wire.ProviderModel {
	merged := make([]wire.ProviderModel, 0, len(live))
	seen := make(map[string]bool, len(live))
	for _, option := range live {
		key := normalizeClaudeModelID(option.ID)
		if seen[key] {
			continue
		}
		seen[key] = true
		aliasOf := ""
		if option.ResolvedModel != "" && normalizeClaudeModelID(option.ResolvedModel) != key {
			aliasOf = option.ResolvedModel
		}
		merged = append(merged, wire.ProviderModel{
			ID:            option.ID,
			Name:          option.Name,
			Detail:        optionalString(option.Description),
			Efforts:       append([]string(nil), option.Efforts...),
			DefaultEffort: reconcileDefaultEffort(stringPtr("medium"), option.Efforts),
			IsDefault:     option.ID == "default",
			SupportsFast:  option.SupportsFast,
			AliasOf:       aliasOf,
		})
	}
	return merged
}

// claudeGatewayPathSegments 是网关自己的路径段，不是注入时加的 provider 前缀：
// 端点本身可能就叫 auto/group，此时钩子行 ID 的首段属于模型 ID 的一部分。
var claudeGatewayPathSegments = map[string]bool{"auto": true, "group": true}

// claudeHookIDWithoutProvider 去掉 Magpie 钩子行 ID 的首段 provider 前缀。
// 钩子行 ID 是"<ProviderID>/<网关模型 ID>"（见 internal/app/agent_magpie.go 的
// target.ProviderID + "/" + id），而网关模型 ID 自己常带 auto/... 路由段，于是同一个
// 模型在目录里出现两种写法：claude CLI 实时行 "auto/claude-sonnet-5-5[1m]" 与钩子行
// "magpie_jp/auto/claude-sonnet-5-5"。剥掉 provider 首段后两者归一化相同，可判为同一模型。
// 首段是网关路径段或首段之后没有剩余段时返回 false。
func claudeHookIDWithoutProvider(id string) (string, bool) {
	prefix, rest, ok := strings.Cut(id, "/")
	if !ok || rest == "" || claudeGatewayPathSegments[strings.ToLower(prefix)] {
		return "", false
	}
	return rest, true
}

// codex 模型目录只来自 codex app-server 的运行时探测，不再回退静态表。
// legacyCodexModelIDs 是旧静态目录里的 ID：已有会话仍按这些 ID 记住模型，
// IsValidModel 在动态目录未命中后继续放行它们。
var legacyCodexModelIDs = map[string]bool{
	"gpt-5.5":       true,
	"gpt-5.4":       true,
	"gpt-5.3-codex": true,
	"gpt-5.4-mini":  true,
}

// memory Provider 专用于测试与回显模拟
var memoryModels = []wire.ProviderModel{
	{
		ID:        "echo",
		Name:      "Echo",
		IsDefault: true,
	},
}

// agy（Antigravity）模型目录，基座模型与 `agy models` 的输出保持一致。
// gemini-3.1-pro 的 medium 档在上游不可用，provider 层会把 medium 解析为 high，因此这里只列出 low/high。
var agyModels = []wire.ProviderModel{
	{
		ID:            "gemini-3.8-flash",
		Name:          "Gemini 3.8 Flash",
		Efforts:       []string{"low", "medium", "high"},
		DefaultEffort: stringPtr("high"),
		IsDefault:     true,
	},
	{
		ID:            "gemini-3.7-flash",
		Name:          "Gemini 3.7 Flash",
		Efforts:       []string{"low", "medium", "high"},
		DefaultEffort: stringPtr("high"),
	},
	{
		ID:            "gemini-3.6-flash",
		Name:          "Gemini 3.6 Flash",
		Efforts:       []string{"low", "medium", "high"},
		DefaultEffort: stringPtr("high"),
	},
	{
		ID:            "gemini-3.1-pro",
		Name:          "Gemini 3.1 Pro",
		Efforts:       []string{"low", "high"},
		DefaultEffort: stringPtr("high"),
	},
	{
		ID:   "claude-sonnet-4-6",
		Name: "Claude Sonnet 4.6 (Thinking)",
	},
	{
		ID:   "claude-opus-4-6-thinking",
		Name: "Claude Opus 4.6 (Thinking)",
	},
	{
		ID:            "gpt-oss-120b",
		Name:          "GPT-OSS 120B",
		Efforts:       []string{"medium"},
		DefaultEffort: stringPtr("medium"),
	},
}

// openaiModels 是未在配置中指定具体模型时的 OpenAI 起始目录；
// 配置里指定了模型时，ListProviders 会把它追加为首选行，而不是替换整张目录。
var openaiModels = []wire.ProviderModel{
	{ID: "gpt-4o", Name: "GPT-4o", Efforts: []string{"low", "medium", "high"}, DefaultEffort: stringPtr("medium"), IsDefault: true},
	{ID: "gpt-4o-mini", Name: "GPT-4o Mini"},
	{ID: "gpt-4.1", Name: "GPT-4.1"},
	{ID: "gpt-4.1-mini", Name: "GPT-4.1 Mini"},
	{ID: "o3", Name: "o3", Efforts: []string{"low", "medium", "high"}, DefaultEffort: stringPtr("medium")},
	{ID: "o4-mini", Name: "o4-mini", Efforts: []string{"low", "medium", "high"}, DefaultEffort: stringPtr("medium")},
}

// modelsDevProviders 把 mcode provider 映射到 models.dev 的 provider 键。
// pi / omp / mu / droid 这类聚合型 CLI 的模型 id 自带上游前缀（如 deepseek/deepseek-v4-flash），
// 目录里查不到精确条目，由 fallbackContextLimit 兜底。
var modelsDevProviders = map[string]string{
	"claude":      "anthropic",
	"codex":       "openai",
	"openai":      "openai",
	"copilot":     "github-copilot",
	"agy":         "google",
	"antigravity": "google",
	"cline":       "cline-pass",
	"deepseek":    "deepseek",
	"grok":        "xai",
	"zai":         "zai",
}

// contextLimitFallbacks 是 models.dev 查不到上下文窗口时的静态兜底表（tokens），
// 按模型 id 里的关键字从具体到宽泛匹配；没有它时 pi/omp/mu/copilot 这类只有模型名的目录
// 上下文上限恒为 0，用量圆环永远显示「暂无上下文用量数据」。
var contextLimitFallbacks = []struct {
	needles []string
	limit   int
}{
	// 百万级窗口：Gemini 全系与带 1m 上下文标记的模型。
	{[]string{"gemini", "1m"}, 1_000_000},
	// 200K 级窗口。
	{[]string{"claude", "grok", "glm", "kimi", "gpt-5", "gpt-4.1", "o3", "o4"}, 200_000},
	// 128K 级窗口。
	{[]string{"deepseek", "gpt-4o", "gpt-4", "qwen", "llama", "mistral", "mimo", "spark", "minimax"}, 128_000},
}

// providerContextLimits 是模型名完全匹配不上时的 provider 默认窗口：
// 聚合型 CLI（pi/omp/mu）与 Copilot 的模型名由上游决定，统一给保守的 200K；
// 内置 mcode agent 的默认窗口是 128K。
var providerContextLimits = map[string]int{
	"mcode":    128_000,
	"pi":       200_000,
	"omp":      200_000,
	"mu":       200_000,
	"copilot":  200_000,
	"droid":    200_000,
	"grok":     200_000,
	"cline":    200_000,
	"zai":      128_000,
	"deepseek": 128_000,
}

// fallbackContextLimit 返回模型 id 对应的静态上下文上限（tokens），匹配不上时返回 0。
func fallbackContextLimit(provider, modelID string) int {
	id := strings.ToLower(strings.TrimSpace(modelID))
	// 聚合型 CLI 的 id 带上游前缀（deepseek/deepseek-v4-flash），取末段再匹配。
	if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
		id = id[idx+1:]
	}
	for _, row := range contextLimitFallbacks {
		for _, needle := range row.needles {
			if strings.Contains(id, needle) {
				return row.limit
			}
		}
	}
	return providerContextLimits[provider]
}

// claudeAliasFamilies 把 claude CLI 的模型别名映射到 models.dev 的 family，
// 别名随 CLI 指向该 family 最新的模型，因此能力取该 family 最新条目。
// Default 行（ID "default"）无法确定账号实际使用的模型，取 sonnet 作为参照。
var claudeAliasFamilies = map[string]string{
	"default": "claude-sonnet",
	"opus":    "claude-opus",
	"sonnet":  "claude-sonnet",
	"haiku":   "claude-haiku",
}

// applyModelsDev 用 models.dev 目录补全能力字段，目录里查不到的模型保持原值。
// 只有 claude 的档位与 Fast 由目录决定；其余 provider 的档位来自各自 CLI 或已核对的静态表，
// 这里只补上下文上限、价格与缺失的说明。
func applyModelsDev(provider string, models []wire.ProviderModel) {
	key, ok := modelsDevProviders[provider]
	if !ok {
		return
	}
	for i := range models {
		m := &models[i]
		var info modelsdev.Info
		var found bool
		if family, alias := claudeAliasFamilies[m.ID]; provider == "claude" && alias {
			info, found = modelsdev.Default.Latest(key, family)
		} else {
			lookupID := m.ID
			if provider == "claude" {
				// 带 1m 上下文标记的 ID（如 claude-fable-5-1[1m]）在目录里是同一模型。
				lookupID = strings.TrimSuffix(lookupID, "[1m]")
			}
			info, found = modelsdev.Default.Lookup(key, lookupID)
		}
		applyModelsDevExtras(m, info, found)
		if provider == "cline" {
			applyClineEfforts(m, info, found)
			continue
		}
		if !found {
			continue
		}
		if info.ContextLimit > 0 {
			m.ContextLimit = info.ContextLimit
		}
		if provider != "claude" {
			continue
		}
		m.Efforts = append([]string(nil), info.Efforts...)
		m.DefaultEffort = reconcileDefaultEffort(m.DefaultEffort, m.Efforts)
		if m.ID != "default" {
			m.SupportsFast = info.Fast
		}
	}
}

// applyModelsDevExtras 用 models.dev 目录补价格与说明：
// 价格一律取目录的 API 参考价；说明只在 CLI 或种子没给出时补全。
func applyModelsDevExtras(m *wire.ProviderModel, info modelsdev.Info, found bool) {
	if !found {
		return
	}
	if info.Cost != nil {
		m.Cost = &wire.ModelCost{
			Input:      info.Cost.Input,
			Output:     info.Cost.Output,
			CacheRead:  info.Cost.CacheRead,
			CacheWrite: info.Cost.CacheWrite,
		}
	}
	if (m.Detail == nil || *m.Detail == "") && info.Summary() != "" {
		m.Detail = stringPtr(info.Summary())
	}
}

// clineEfforts 是 models.dev 没给档位的 Cline 推理模型共用的刻度。
var clineEfforts = []string{"low", "medium", "high", "xhigh"}

// clineSupportsReasoning 判断 Cline 模型是否支持推理：
// ClinePass 与 free 渠道的模型一律支持，其余按名字里的推理模型特征判断。
func clineSupportsReasoning(id string) bool {
	lower := strings.ToLower(id)
	for _, prefix := range []string{"cline-pass/", "cline-free/", "stealth/"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	for _, marker := range []string{"deepseek", "kimi", "glm", "mimo", "spark", "muse", "qwen", "minimax", "reason", "think", "r1", "o1", "o3", "o4", "qwq"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// applyClineEfforts 用 models.dev 的 cline-pass 条目补全 Cline 模型的档位与上下文：
// ACP 不提供档位，目录里有刻度就用它，没有（toggle 型或没收录）而模型支持推理时用共用刻度。
func applyClineEfforts(m *wire.ProviderModel, info modelsdev.Info, found bool) {
	if found {
		if info.ContextLimit > 0 {
			m.ContextLimit = info.ContextLimit
		}
		if len(info.Efforts) > 0 {
			m.Efforts = append([]string(nil), info.Efforts...)
		}
	}
	if len(m.Efforts) == 0 && clineSupportsReasoning(m.ID) {
		m.Efforts = append([]string(nil), clineEfforts...)
	}
	m.DefaultEffort = reconcileDefaultEffort(m.DefaultEffort, m.Efforts)
}

// reconcileDefaultEffort 保证默认档位落在档位列表内：原默认仍可用则保留，否则取 medium，再否则取中间档。
func reconcileDefaultEffort(current *string, efforts []string) *string {
	if len(efforts) == 0 {
		return nil
	}
	if current != nil {
		for _, e := range efforts {
			if e == *current {
				return current
			}
		}
	}
	for _, e := range efforts {
		if e == "medium" {
			return stringPtr(e)
		}
	}
	return stringPtr(efforts[len(efforts)/2])
}

// ProviderModels 返回指定 Provider 的模型目录副本，能力字段按 models.dev 目录补全。
func ProviderModels(provider string) []wire.ProviderModel {
	models := providerModelsBase(provider)
	applyModelsDev(provider, models)
	applyContextLimitFallbacks(provider, models)
	// providerModelsBase 的每条分支都返回新切片，这里可以原地裁剪显示名。
	trimModelDisplayNames(models)
	return models
}

// trimModelDisplayNames 去掉显示名里「 · 」之后的附加说明。
// 上游（magpie 的 label、CLI 的 DisplayName）会在模型名后拼上路由组、端点等说明，
// 而界面只显示模型名，所以由目录统一裁剪，只改 Name，不动 ID 与 Detail。
// 截断后为空时保留原名，避免把显示名清成空串。
func trimModelDisplayNames(models []wire.ProviderModel) {
	for i := range models {
		head, _, ok := strings.Cut(models[i].Name, " · ")
		if !ok {
			continue
		}
		if name := strings.TrimSpace(head); name != "" {
			models[i].Name = name
		}
	}
}

// applyContextLimitFallbacks 给 models.dev 没给出上下文窗口的模型补一个静态上限。
func applyContextLimitFallbacks(provider string, models []wire.ProviderModel) {
	for i := range models {
		if models[i].ContextLimit > 0 {
			continue
		}
		models[i].ContextLimit = fallbackContextLimit(provider, models[i].ID)
	}
}

// placeholderModelID 是没有静态目录的 provider 唯一的可选模型 id：它只表示
// “使用该供应商自己的默认模型”（omp/pi 等 CLI 的约定），让设置页与模型选择器有行可点，
// 不代表真实模型名。目录里给出占位行后，这些 provider 的模型校验仍放行任意非空模型名。
const placeholderModelID = "default"

// hasPlaceholderCatalog 判断目录是否为空或只有占位行（没有静态目录的 provider）。
func hasPlaceholderCatalog(models []wire.ProviderModel) bool {
	if len(models) == 0 {
		return true
	}
	for _, m := range models {
		if m.ID != placeholderModelID {
			return false
		}
	}
	return true
}

// openAICompatibleProvider 判断 provider 是否由 OpenAI 兼容 harness 驱动（预设与自定义端点）。
// 它们的模型来自各自 harness 的 ModelIDs()（配置里的具体模型），保持空目录才能让
// ListProviders 用该模型补行；返回占位行反而会顶掉配置里的模型。
func openAICompatibleProvider(provider string) bool {
	if strings.HasPrefix(provider, "custom-") {
		return true
	}
	_, ok := openai.PresetByID(provider)
	return ok
}

// placeholderModel 是“使用该供应商自己的默认模型”的占位行。
func placeholderModel() wire.ProviderModel {
	return wire.ProviderModel{ID: placeholderModelID, Name: "默认", IsDefault: true}
}

// cliCatalogProvider 判断 provider 的目录来自 CLI 动态列出：
// 列表随账号与 CLI 版本变化，模型名交给 CLI 自己校验。
func cliCatalogProvider(provider string) bool {
	switch provider {
	case "commandcode", "cline", "pi", "omp", "copilot", "droid", "cursor", "opencode", "grok", "devin":
		return true
	}
	return false
}

func providerModelsBase(provider string) []wire.ProviderModel {
	base := nativeProviderModels(provider)
	if provider != "claude" && provider != "codex" && provider != "pi" {
		return base
	}
	rows, active := magpieCatalogRows(provider)
	if !active {
		if provider == "pi" {
			filtered := make([]wire.ProviderModel, 0, len(base))
			for _, model := range base {
				if !isPiStaleModel(model.ID) {
					filtered = append(filtered, model)
				}
			}
			return withDefaultRow(filtered)
		}
		return base
	}
	if provider == "pi" {
		piInjectedRowsMu.RLock()
		base = append(base, piInjectedRows...)
		piInjectedRowsMu.RUnlock()
	}
	return enrichMagpieRows(provider, rows, base)
}

// sameModelID 判断目录行与勾选行是否同一模型：claude 忽略大小写与末尾 1m 标记
// （勾选行 auto/claude-sonnet-5-5 与 CLI 目录行 auto/claude-sonnet-5-5[1m] 是同一模型），
// 其余 provider 保持全等比较。
func sameModelID(provider, a, b string) bool {
	if provider == "claude" {
		return normalizeClaudeModelID(a) == normalizeClaudeModelID(b)
	}
	return a == b
}

// mergeMagpieRow 用目录候选补全能力元数据，保留勾选行的 ID、名称、默认状态
// 以及勾选行自带的网关档位（勾选行没带档位时沿用候选的）。
func mergeMagpieRow(row *wire.ProviderModel, candidate wire.ProviderModel) {
	id, name, detail, def := row.ID, row.Name, row.Detail, row.IsDefault
	efforts, defaultEffort := row.Efforts, row.DefaultEffort
	*row = candidate
	row.ID, row.Name, row.Detail, row.IsDefault = id, name, detail, def
	if len(efforts) > 0 {
		row.Efforts = append([]string(nil), efforts...)
		row.DefaultEffort = defaultEffort
	}
}

// enrichMagpieRows 只补能力元数据，保留勾选行的 ID、名称和默认状态。
func enrichMagpieRows(provider string, rows, base []wire.ProviderModel) []wire.ProviderModel {
	result := append([]wire.ProviderModel(nil), rows...)
	for i := range result {
		row := &result[i]
		lookup := row.ID
		matched := false
		for _, candidate := range base {
			if sameModelID(provider, candidate.ID, lookup) {
				mergeMagpieRow(row, candidate)
				matched = true
				break
			}
		}
		if provider != "pi" {
			_, known := modelsdev.Default.Lookup(modelsDevProviders[provider], lookup)
			if bare, ok := claudeHookIDWithoutProvider(lookup); ok && !matched && !known {
				lookup = bare
				for _, candidate := range base {
					if sameModelID(provider, candidate.ID, lookup) {
						mergeMagpieRow(row, candidate)
						break
					}
				}
			}
			tmp := *row
			tmp.ID = lookup
			if provider == "claude" {
				// 网关勾选行带 auto/ 等路由段，models.dev 按裸名收录：查不到时用最后一段兜底。
				if _, found := modelsdev.Default.Lookup(modelsDevProviders[provider], normalizeClaudeModelID(lookup)); !found {
					if idx := strings.LastIndex(lookup, "/"); idx >= 0 && idx+1 < len(lookup) {
						tmp.ID = lookup[idx+1:]
					}
				}
			}
			metadata := []wire.ProviderModel{tmp}
			applyModelsDev(provider, metadata)
			metadata[0].ID = row.ID
			*row = metadata[0]
		}
		if len(row.Efforts) > 0 {
			row.DefaultEffort = reconcileDefaultEffort(row.DefaultEffort, row.Efforts)
		}
	}
	return result
}

func nativeProviderModels(provider string) []wire.ProviderModel {
	var src []wire.ProviderModel
	switch provider {
	case "claude":
		src = mergedClaudeModels(claude.CachedModels())
	case "codex":
		if cached := codex.CachedModels(); len(cached) > 0 {
			res := make([]wire.ProviderModel, len(cached))
			hasGpt55 := false
			for _, m := range cached {
				if m.ID == "gpt-5.5" {
					hasGpt55 = true
					break
				}
			}
			defaultID := "gpt-5.5"
			if !hasGpt55 {
				defaultID = cached[0].ID
			}

			for i, m := range cached {
				name := m.DisplayName
				if name == "" {
					name = m.ID
				}
				var defEffort *string
				if m.DefaultEffort != "" {
					de := m.DefaultEffort
					defEffort = &de
				}
				efforts := make([]string, len(m.Efforts))
				copy(efforts, m.Efforts)

				res[i] = wire.ProviderModel{
					ID:            m.ID,
					Name:          name,
					Efforts:       efforts,
					DefaultEffort: defEffort,
					IsDefault:     m.ID == defaultID,
					SupportsFast:  m.SupportsFast,
				}
			}
			return res
		}
		// 未探测到目录时保持空切片，不再回退到静态 codexModels。
		return []wire.ProviderModel{}
	case "memory":
		src = memoryModels
	case "agy", "antigravity":
		src = agyModels
	case "openai":
		src = openaiModels
	case "commandcode":
		// `cmd --list-models` 的账号目录（只含当前订阅可用的模型），拉到之前使用内置初始列表。
		// 已有会话存着的旧占位模型 default 仍被 IsValidModel 放行。
		cached := commandcode.CachedModels()
		res := make([]wire.ProviderModel, len(cached))
		for i, m := range cached {
			var configured *string
			if m.DefaultEffort != "" {
				configured = stringPtr(m.DefaultEffort)
			}
			res[i] = wire.ProviderModel{ID: m.ID, Name: m.Name, Detail: optionalString(m.Detail), Efforts: m.Efforts,
				DefaultEffort: reconcileDefaultEffort(configured, m.Efforts), IsDefault: m.Default}
		}
		return res
	case "cline":
		// Cline 经 ACP 探测到的目录（账号的全部渠道模型，不属于当前渠道的标为 Secondary，界面默认折叠）；
		// 探测到之前使用内置初始列表；档位由 applyModelsDev 按 models.dev 的 cline-pass 条目补全。
		cached := cline.CachedModels()
		res := make([]wire.ProviderModel, len(cached))
		for i, m := range cached {
			res[i] = wire.ProviderModel{ID: m.ID, Name: m.Name, Detail: optionalString(m.Detail), IsDefault: m.Default, Secondary: m.Secondary}
		}
		return res
	case "mcode":
		if models := mcodeModels(); len(models) > 0 {
			return models
		}
		return []wire.ProviderModel{placeholderModel()}
	case "pi", "omp", "copilot", "droid":
		// 账号可用的模型目录由 CLI 在后台读取；读到之前保持占位行。
		if cached := cliCatalogModels(provider); len(cached) > 0 {
			return cached
		}
		return []wire.ProviderModel{placeholderModel()}
	case "cursor", "opencode", "grok", "devin":
		if cached := otherACPCatalogs[provider].snapshot(); len(cached) > 0 {
			return cached
		}
		return []wire.ProviderModel{placeholderModel()}
	default:
		// 没有静态目录的 provider：给一行占位模型，
		// 避免设置页与模型选择器整列为空、没有任何可点的行。
		// OpenAI 兼容的 provider 例外：保持空目录，让 ListProviders 用配置里的模型补行；
		// 这里用空切片而不是 nil，models 在协议里是必填数组，nil 会被序列化成 null。
		if openAICompatibleProvider(provider) {
			return []wire.ProviderModel{}
		}
		return []wire.ProviderModel{placeholderModel()}
	}
	res := make([]wire.ProviderModel, len(src))
	copy(res, src)
	for i := range res {
		if res[i].DefaultEffort != nil {
			res[i].DefaultEffort = stringPtr(*res[i].DefaultEffort)
		}
		if len(res[i].Efforts) > 0 {
			effortsCopy := make([]string, len(res[i].Efforts))
			copy(effortsCopy, res[i].Efforts)
			res[i].Efforts = effortsCopy
		}
	}
	return res
}

// DefaultModel 返回指定 Provider 的默认模型 ID。
func DefaultModel(provider string) string {
	models := ProviderModels(provider)
	for _, m := range models {
		if m.IsDefault {
			return m.ID
		}
	}
	if len(models) > 0 {
		return models[0].ID
	}
	switch provider {
	case "claude":
		return "default"
	case "codex":
		return "gpt-5.5"
	case "memory":
		return "echo"
	default:
		return ""
	}
}

// IsValidModel 检查模型 ID 是否在指定 Provider 的目录中。
// 目录为空或只有占位行、或目录由 CLI 动态列出的 provider 接受任意非空模型名。
// 动态列表加载后，仍兼容静态目录中的模型，避免已有线程失效。
func IsValidModel(provider, model string) bool {
	models := ProviderModels(provider)
	if hasPlaceholderCatalog(models) || cliCatalogProvider(provider) {
		return model != ""
	}
	for _, m := range models {
		if m.ID == model {
			return true
		}
	}
	// 目录改为只认运行时探测后，白名单保证旧会话存的模型 ID 仍然有效。
	switch provider {
	case "claude":
		if claudeAliasSeedIDs[model] {
			return true
		}
	case "codex":
		if legacyCodexModelIDs[model] {
			return true
		}
	}
	// 旧会话把 claude 的默认模型存成空 ID，仍按默认行放行。
	if provider == "claude" && model == "" {
		return true
	}
	return false
}

// ModelCatalogNotice 返回 provider 模型目录为空时的原因文案；目录正常或不适用时返回空串。
func ModelCatalogNotice(provider string) string {
	switch provider {
	case "claude":
		return claude.CatalogNotice()
	case "codex":
		return codex.CatalogNotice()
	}
	return ""
}
