package remote

import (
	"strings"
	"testing"

	"mcode/internal/config"
	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/remote/wire"
)

func TestProviderModelsReturnsIndependentCapabilities(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := ProviderModels("openai")
	if len(first) == 0 || len(first[0].Efforts) == 0 || first[0].DefaultEffort == nil {
		t.Fatal("OpenAI default model must expose reasoning capabilities")
	}
	first[0].Efforts[0] = "changed"
	*first[0].DefaultEffort = "changed"
	second := ProviderModels("openai")
	if second[0].Efforts[0] != "low" || *second[0].DefaultEffort != "medium" {
		t.Fatalf("caller mutated the shared catalog: %+v", second[0])
	}
}

// 非 claude/codex 的 Provider 也要给出可选模型目录，否则模型面板整列为空。
func TestProviderModelsCoverDeclaredProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, provider := range []string{"agy", "antigravity", "openai"} {
		models := ProviderModels(provider)
		if len(models) == 0 {
			t.Fatalf("%s must expose a seed model catalog", provider)
		}
		hasDefault := false
		for _, m := range models {
			if m.ID == "" {
				t.Fatalf("%s catalog contains an empty model id", provider)
			}
			if m.IsDefault {
				hasDefault = true
			}
		}
		if !hasDefault {
			t.Fatalf("%s catalog needs a default row", provider)
		}
		if DefaultModel(provider) == "" {
			t.Fatalf("%s default model must be non-empty", provider)
		}
	}
}

// 没有静态目录的 Provider 要给出一行占位模型（ID 是 "default"），
// 否则设置页与模型选择器整列为空、没有任何可点的行。
func TestProviderModelsPlaceholderForCataloglessProviders(t *testing.T) {
	for _, provider := range []string{"pi", "omp", "copilot", "droid"} {
		models := ProviderModels(provider)
		if len(models) != 1 || models[0].ID != "default" || models[0].Name != "默认" || !models[0].IsDefault {
			t.Fatalf("%s placeholder row wrong: %+v", provider, models)
		}
	}
}

// commandcode 与 cline 的目录由 CLI 按账号订阅列出，不再带“默认”占位行；
// 目录里恰有一个默认模型，已有会话存着的旧模型名（含 default）仍被放行。
func TestProviderModelsCLICatalogHasNoPlaceholder(t *testing.T) {
	for _, provider := range []string{"commandcode", "cline"} {
		models := ProviderModels(provider)
		if len(models) < 2 {
			t.Fatalf("%s catalog should list concrete models: %+v", provider, models)
		}
		defaults := 0
		for _, m := range models {
			if m.ID == "default" {
				t.Fatalf("%s catalog must not carry the placeholder row: %+v", provider, m)
			}
			if m.IsDefault {
				defaults++
			}
		}
		if defaults != 1 {
			t.Fatalf("%s catalog needs exactly one default row, got %d", provider, defaults)
		}
		if !IsValidModel(provider, "default") || !IsValidModel(provider, "some/unlisted-model") {
			t.Fatalf("%s should accept the legacy placeholder and any model the CLI may list", provider)
		}
	}
}

// pi / omp / copilot / droid 在 CLI 目录读到之前是占位行，读到之后任何非空模型名都被放行。
func TestIsValidModelCLICatalogProviders(t *testing.T) {
	for _, provider := range []string{"pi", "omp", "copilot", "droid"} {
		if !IsValidModel(provider, "default") || !IsValidModel(provider, "anthropic/some-model") || IsValidModel(provider, "") {
			t.Fatalf("%s model validation wrong", provider)
		}
	}
}

// 无运行时目录的 custom- 与 Magpie 端点保持空目录，让 ListProviders 用配置里的模型补行；
// 空切片而非 nil，保证 JSON 里 models 仍是数组（schema 必填）。
func TestProviderModelsOpenAICompatibleStayEmpty(t *testing.T) {
	for _, provider := range []string{"custom-test", "magpie", "magpie2"} {
		models := ProviderModels(provider)
		if len(models) != 0 || models == nil {
			t.Fatalf("%s must keep an empty array catalog, got %+v", provider, models)
		}
	}
}

// 目录只认运行时探测：没有探测结果时 claude/codex 返回空数组，
// 具体原因由 ModelCatalogNotice 给出。
func TestProviderModelsRequireRuntimeCatalog(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// 同包其他用例可能已经触发过真实探测、把进程级缓存写满；先重置成「从未探测」，
	// 并让后台探测走测试桩，保证断言不依赖本机 CLI 与测试执行顺序。
	t.Cleanup(claude.ResetCatalogCacheForTest())
	t.Cleanup(codex.ResetCatalogCacheForTest())
	for _, provider := range []string{"claude", "codex"} {
		models := ProviderModels(provider)
		if models == nil || len(models) != 0 {
			t.Fatalf("%s 在无探测结果时应为空数组，got %+v", provider, models)
		}
	}
	if notice := ModelCatalogNotice("claude"); notice == "" {
		t.Error("claude 无目录时应给出原因文案")
	}
	if notice := ModelCatalogNotice("codex"); notice == "" {
		t.Error("codex 无目录时应给出原因文案")
	}
	if notice := ModelCatalogNotice("openai"); notice != "" {
		t.Errorf("openai 不适用目录原因文案，got %q", notice)
	}
}

// 目录为空或只有占位行的 Provider 接受任意非空模型名，空模型名仍然无效。
func TestIsValidModelPlaceholderCatalog(t *testing.T) {
	for _, provider := range []string{"pi", "cline", "custom-test"} {
		if !IsValidModel(provider, "some-model") {
			t.Errorf("IsValidModel(%q, some-model) = false, want true", provider)
		}
		if IsValidModel(provider, "") {
			t.Errorf("IsValidModel(%q, empty) = true, want false", provider)
		}
	}
}

// 目录改为只认运行时探测后，旧会话存的别名/静态目录 ID 仍要通过校验。
func TestIsValidModelLegacyCatalogIDs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, model := range []string{"default", "opus", "sonnet", "haiku", "claude-fable-5-1[1m]", ""} {
		if !IsValidModel("claude", model) {
			t.Errorf("IsValidModel(claude, %q) = false, want true", model)
		}
	}
	for _, model := range []string{"gpt-5.5", "gpt-5.4", "gpt-5.3-codex", "gpt-5.4-mini"} {
		if !IsValidModel("codex", model) {
			t.Errorf("IsValidModel(codex, %q) = false, want true", model)
		}
	}
}

// agy 目录保留完整强度档位；gemini-3.1-pro 不含 medium（上游会解析为 high）。
func TestAgyModelEfforts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, m := range ProviderModels("agy") {
		if m.ID != "gemini-3.1-pro" {
			continue
		}
		for _, e := range m.Efforts {
			if e == "medium" {
				t.Fatalf("gemini-3.1-pro must not advertise medium effort")
			}
		}
		return
	}
	t.Fatal("gemini-3.1-pro missing from agy catalog")
}

// 配置里的 OpenAI 模型并入目录并置为首选，绝不替换整张目录。
func TestWithConfiguredModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := ProviderModels("openai")

	unchanged := withConfiguredModel(base, "  ")
	if len(unchanged) != len(base) || unchanged[0].ID != base[0].ID {
		t.Fatalf("empty configured model must keep the catalog: %+v", unchanged)
	}

	custom := withConfiguredModel(base, "my-model")
	if len(custom) != len(base)+1 || custom[0].ID != "my-model" || !custom[0].IsDefault {
		t.Fatalf("configured model should lead the catalog: %+v", custom[0])
	}
	for _, m := range custom[1:] {
		if m.IsDefault {
			t.Fatalf("only one default row allowed, got extra %s", m.ID)
		}
	}

	promoted := withConfiguredModel(base, base[1].ID)
	if promoted[0].ID != base[1].ID || !promoted[0].IsDefault || len(promoted) != len(base) {
		t.Fatalf("existing model should be promoted without duplication: %+v", promoted[:2])
	}
	if base[0].IsDefault == false {
		t.Fatalf("input catalog must not be mutated")
	}
}

// mergedClaudeModels 只映射运行时探测到的行：保留 CLI 顺序、按归一化 ID 去重，
// 不再补任何静态种子行。
func TestMergedClaudeModels(t *testing.T) {
	live := []claude.Model{
		{ID: "opus", Name: "Opus 5.5", Description: "实时说明", Efforts: []string{"low", "high"}},
		{ID: "claude-new-9", Name: "New 9", Efforts: []string{"low", "medium", "high"}},
		{ID: "claude-opus-9", Name: "Opus 9"},
	}
	merged := mergedClaudeModels(live)
	if len(merged) != len(live) {
		t.Fatalf("目录应只含实时行，got %d rows: %+v", len(merged), merged)
	}
	if merged[0].ID != "opus" || merged[1].ID != "claude-new-9" || merged[2].ID != "claude-opus-9" {
		t.Fatalf("探测顺序被打乱: %+v", merged)
	}
	opus := merged[0]
	if opus.Name != "Opus 5.5" || opus.Detail == nil || *opus.Detail != "实时说明" {
		t.Errorf("实时名称与说明应直接映射: %+v", opus)
	}
	if len(opus.Efforts) != 2 || opus.DefaultEffort == nil || *opus.DefaultEffort != "high" {
		t.Errorf("档位映射错误: efforts=%v default=%v", opus.Efforts, opus.DefaultEffort)
	}
	if opus.IsDefault {
		t.Error("只有 default 行才是默认行")
	}
	added := merged[1]
	if added.Name != "New 9" || added.DefaultEffort == nil || *added.DefaultEffort != "medium" || added.IsDefault {
		t.Errorf("实时行映射错误: %+v", added)
	}
	if !mergedClaudeModels([]claude.Model{{ID: "default", Name: "Default"}})[0].IsDefault {
		t.Error("default 行应标记为默认")
	}
}

// 别名行带 AliasOf，实体行（resolvedModel 与自身 ID 只差 1m 标记或大小写）和没有
// resolvedModel 的行留空；行数与顺序不受影响。
func TestMergedClaudeModelsAliasOf(t *testing.T) {
	merged := mergedClaudeModels([]claude.Model{
		{ID: "opus", Name: "Opus 5.5", ResolvedModel: "auto/claude-opus-5-5[1m]"},
		{ID: "auto/claude-opus-5-5[1m]", Name: "Opus 5.5", ResolvedModel: "auto/claude-opus-5-5"},
		{ID: "claude-sonnet-5-5", Name: "Sonnet 5.5", ResolvedModel: "Claude-Sonnet-5-5"},
		{ID: "claude-haiku-4-5", Name: "Haiku 4.5"},
	})
	if len(merged) != 4 {
		t.Fatalf("行数与顺序应与探测结果一致，got %d rows: %+v", len(merged), merged)
	}
	if merged[0].AliasOf != "auto/claude-opus-5-5[1m]" {
		t.Errorf("别名行应带 AliasOf，got %q", merged[0].AliasOf)
	}
	if merged[1].AliasOf != "" || merged[2].AliasOf != "" || merged[3].AliasOf != "" {
		t.Errorf("实体行与无 resolvedModel 的行应留空: %q %q %q", merged[1].AliasOf, merged[2].AliasOf, merged[3].AliasOf)
	}
}

// 归一化后相同的 1m 标记写法只保留第一条，避免同一模型出现两行。
func TestMergedClaudeModelsDedupesContextMarker(t *testing.T) {
	merged := mergedClaudeModels([]claude.Model{
		{ID: "claude-fable-5-1[1m]", Name: "Fable 5.1"},
		{ID: "claude-fable-5-1", Name: "Fable 5.1"},
	})
	if len(merged) != 1 {
		t.Fatalf("1m 标记写法应去重，got %+v", merged)
	}
	if merged[0].ID != "claude-fable-5-1[1m]" {
		t.Errorf("应保留第一条实时行的 ID，got %q", merged[0].ID)
	}
	if merged[0].Name != "Fable 5.1" {
		t.Errorf("实时名称应保留，got %q", merged[0].Name)
	}
}

// 目录为空时 mergedClaudeModels 返回空切片而不是 nil。
func TestMergedClaudeModelsEmpty(t *testing.T) {
	if got := mergedClaudeModels(nil); got == nil || len(got) != 0 {
		t.Fatalf("空目录应返回空切片，got %#v", got)
	}
}

func TestReconcileDefaultEffort(t *testing.T) {
	if got := reconcileDefaultEffort(stringPtr("high"), []string{"low", "high"}); *got != "high" {
		t.Errorf("kept = %q", *got)
	}
	if got := reconcileDefaultEffort(stringPtr("medium"), []string{"low", "high"}); *got != "high" {
		t.Errorf("middle fallback = %q", *got)
	}
	if got := reconcileDefaultEffort(stringPtr("high"), nil); got != nil {
		t.Errorf("no efforts must clear default, got %q", *got)
	}
}

// 用户固定的自定义模型 ID 不在目录里，只有 validModel 放行；IsValidModel 本身不受影响。
func TestValidModelAllowsPinnedModel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	b := &MemoryBackend{}
	b.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Models: config.ModelsConfig{Pins: []config.ModelPin{
			{Provider: "openai", ModelID: "my-custom-model"},
		}}}
	})
	if IsValidModel("openai", "my-custom-model") {
		t.Fatal("IsValidModel 本身不应认自定义模型，pin 放行必须走 validModel")
	}
	if !b.validModel("openai", "my-custom-model") {
		t.Fatal("已固定的自定义模型应通过 validModel")
	}
	if b.validModel("openai", "not-pinned") {
		t.Fatal("未固定的未知模型仍应被拒绝")
	}
	if b.validModel("codex", "my-custom-model") {
		t.Fatal("pin 只对自己的 provider 生效")
	}
	if b.validModel("", "") {
		t.Fatal("空 provider/model 不应通过")
	}
}

// 配对与草稿默认值的过滤同样认 pin；空模型仍表示不指定。
func TestProviderAllowsModelAcceptsPinnedModel(t *testing.T) {
	p := wire.Provider{ID: "claude", Models: []wire.ProviderModel{{ID: "sonnet"}}}
	if providerAllowsModel(p, "claude-custom-x", nil) {
		t.Fatal("未固定的目录外模型应被拒绝")
	}
	pins := []config.ModelPin{{Provider: "claude", ModelID: "claude-custom-x"}}
	if !providerAllowsModel(p, "claude-custom-x", pins) {
		t.Fatal("已固定的模型应放行")
	}
	if providerAllowsModel(p, "claude-custom-x", []config.ModelPin{{Provider: "codex", ModelID: "claude-custom-x"}}) {
		t.Fatal("pin 只对自己的 provider 生效")
	}
	if !providerAllowsModel(p, "", nil) {
		t.Fatal("空模型表示不指定，应放行")
	}
}

// CLI 动态列目录的 provider 放行目录外的非空模型，普通 provider 仍按目录过滤。
func TestProviderAllowsModelCLI(t *testing.T) {
	cli := wire.Provider{ID: "pi", Models: []wire.ProviderModel{{ID: "sonnet"}}}
	if !providerAllowsModel(cli, "mystery-model", nil) {
		t.Fatal("CLI 目录的 provider 应放行目录外模型")
	}
	plain := wire.Provider{ID: "deepseek", Models: []wire.ProviderModel{{ID: "deepseek-chat"}}}
	if providerAllowsModel(plain, "mystery-model", nil) {
		t.Fatal("普通 provider 的目录外模型应被拒绝")
	}
}

// 目录统一去掉显示名里「 · 」之后的附加说明，只改 Name，ID 与 Detail 保持原样。
func TestProviderModelsTrimDisplayNameSuffix(t *testing.T) {
	seedClaudeModelsForTest(t, "default")
	rows := []wire.ProviderModel{
		{ID: "magpie-jp/auto/claude-sonnet-5-5", Name: "Claude Sonnet 5.5 · routing group · jp", Detail: stringPtr("jp · Magpie")},
	}
	SetMagpieCatalog(func(agent string) ([]wire.ProviderModel, bool) {
		if agent != "claude" {
			return nil, false
		}
		return rows, true
	})
	t.Cleanup(func() { SetMagpieCatalog(nil) })

	models := ProviderModels("claude")
	var got *wire.ProviderModel
	for i := range models {
		if strings.Contains(models[i].Name, " · ") {
			t.Errorf("显示名不应带「 · 」后的附加说明，got %q", models[i].Name)
		}
		if models[i].ID == "magpie-jp/auto/claude-sonnet-5-5" {
			got = &models[i]
		}
	}
	if got == nil {
		t.Fatalf("目录里缺少钩子行: %+v", models)
	}
	if got.Name != "Claude Sonnet 5.5" {
		t.Errorf("显示名 = %q，想要 %q", got.Name, "Claude Sonnet 5.5")
	}
	if got.Detail == nil || *got.Detail != "jp · Magpie" {
		t.Errorf("说明字段不应被裁剪，got %v", got.Detail)
	}
}

// TestFallbackContextLimitMcode 校验 mcode 的兜底窗口按内置 agent 的默认 128K 下发，
// 且模型名能匹配到具体关键字时仍以关键字为准。
func TestFallbackContextLimitMcode(t *testing.T) {
	if got := fallbackContextLimit("mcode", "unknown-model"); got != 128_000 {
		t.Fatalf("mcode 默认窗口 = %d，想要 128000", got)
	}
	if got := fallbackContextLimit("mcode", "provider/claude-sonnet-5-5"); got != 200_000 {
		t.Fatalf("具体模型关键字应优先，got %d", got)
	}
}
