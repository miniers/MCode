package mcodeui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 测试 Bee 档位区块默认展示三种档位（只读、执行、攻坚）及“跟随会话子代理模型”胶囊文案。
func TestSwarmTiers_DefaultRender(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() {
			renderPageSwarm(c, st)
		})
	}, 1200, 6000)
	tt.Frame()

	if !tt.HasText("子代理档位") {
		t.Fatalf("expected 'Bee 档位' section header to be rendered")
	}
	if !tt.HasText("只读") || !tt.HasText("执行") || !tt.HasText("攻坚") {
		t.Fatalf("expected default tiers '只读', '执行', '攻坚' to be rendered: %v", tt.Texts())
	}
	if !tt.HasText("跟随会话子代理模型") {
		t.Fatalf("expected '跟随会话子代理模型' capsule text to be rendered: %v", tt.Texts())
	}
	if !tt.HasText("默认档") {
		t.Fatalf("expected '默认档' label to be rendered")
	}
}

func TestSwarmTierRowCustomizedIndicator(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	def := config.DefaultSwarmTiers()[2]
	description := def
	description.Description = "自定义说明"
	prompt := def
	prompt.SystemPrompt = ""
	model := def
	model.Model = "custom-model"
	for _, tc := range []struct {
		name string
		tier config.SwarmTier
		want bool
	}{
		{"默认", def, false},
		{"说明", description, true},
		{"提示词", prompt, true},
		{"模型", model, false},
		{"自定义档位", config.SwarmTier{ID: "custom", Name: "自定义"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &swarmTiersUIState{}
			tester := ui.NewTester(func(c *ui.Context) {
				renderSwarmTierRow(c, st, state, tc.tier, nil)
			}, 1000, 200)
			tester.Frame()
			if _, got := tester.Find("已修改"); got != tc.want {
				t.Errorf("修改标记 = %v，期望 %v", got, tc.want)
			}
		})
	}
}

// 测试添加档位持久化保存至磁盘。
func TestSwarmTiers_AddTierPersists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	newTier := config.SwarmTier{
		ID:          "review",
		Name:        "审查",
		Description: "代码审查与规范核对",
		ReadOnly:    true,
	}

	var errStr string
	ok := saveTier(st, newTier, true, &errStr)
	if !ok {
		t.Fatalf("saveTier failed: %s", errStr)
	}

	if len(st.cfg.Orchestration.Tiers) != 5 {
		t.Fatalf("expected 5 tiers in state, got %d", len(st.cfg.Orchestration.Tiers))
	}

	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(loaded.Orchestration.Tiers) != 5 {
		t.Fatalf("expected 5 tiers persisted to disk, got %d", len(loaded.Orchestration.Tiers))
	}
	last := loaded.Orchestration.Tiers[4]
	if last.ID != "review" || last.Name != "审查" || !last.ReadOnly {
		t.Fatalf("persisted tier mismatch: %+v", last)
	}

	// 验证界面渲染新增的档位
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() {
			renderPageSwarm(c, st)
		})
	}, 1200, 6000)
	tt.Frame()

	if !tt.HasText("审查") || !tt.HasText("review") {
		t.Fatalf("expected newly added tier to render in UI: %v", tt.Texts())
	}
}

// 测试默认档位删除被禁用/拦截。
func TestSwarmTiers_DeleteDefaultTierBlocked(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	// 打开默认档位的编辑 Popover
	defaultID := st.cfg.Orchestration.DefaultTier
	if defaultID == "" {
		defaultID = config.DefaultSwarmTierID
	}
	uiSt := getSwarmTiersUIState(st)
	uiSt.openPopoverKey = "edit-" + defaultID
	uiSt.isNew = false
	for _, tier := range st.cfg.Orchestration.Tiers {
		if tier.ID == defaultID {
			uiSt.draft = tier
			break
		}
	}

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() {
			renderPageSwarm(c, st)
		})
	}, 1200, 6000)
	tt.Frame()

	// 尝试直接通过 Validate 模拟删除默认档位后的校验失败
	cfgCopy := st.cfg
	var filtered []config.SwarmTier
	for _, tItem := range cfgCopy.Orchestration.Tiers {
		if tItem.ID != defaultID {
			filtered = append(filtered, tItem)
		}
	}
	cfgCopy.Orchestration.Tiers = filtered
	if err := config.Validate(cfgCopy); err == nil {
		t.Fatalf("expected config.Validate to fail when default tier is missing")
	}

	// 验证默认档位在配置中依然存在
	found := false
	for _, tier := range st.cfg.Orchestration.Tiers {
		if tier.ID == defaultID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("default tier should remain in state")
	}
}

// 测试配对中配置档位映射持久化保存，且重置按钮能清除该映射。
func TestSwarmTiers_PairOverridePersistenceAndReset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	pairDraft := config.SwarmPair{
		Name:           "Tier Override Pair",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-3-7-sonnet",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-4o",
		BeeTiers: map[string]config.SwarmTierOverride{
			"deep": {
				Provider: config.ProviderCodex,
				Model:    "o1",
			},
		},
	}

	saved := swarmPairSave(st, pairDraft, true)
	if saved.ID == "" {
		t.Fatalf("expected pair to be saved with ID")
	}

	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(loaded.Orchestration.Pairs) == 0 {
		t.Fatalf("expected pair to be persisted")
	}
	persistedPair := loaded.Orchestration.Pairs[0]
	if persistedPair.BeeTiers == nil || persistedPair.BeeTiers["deep"].Model != "o1" {
		t.Fatalf("expected BeeTiers[deep] override persisted as 'o1', got: %+v", persistedPair.BeeTiers)
	}

	// 在配对编辑器弹层内打开并触发重置按钮
	pairUISt := getSwarmUIState(st)
	pairUISt.openPopoverKey = "edit-" + saved.ID
	pairUISt.isNew = false
	pairUISt.draft = saved

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() {
			renderPageSwarm(c, st)
		})
	}, 1200, 6000)
	tt.Frame()

	if err := tt.Click("恢复继承"); err != nil {
		t.Fatalf("click '恢复继承' failed: %v", err)
	}

	// 验证 draft 中的 BeeTiers 已被清除
	if len(pairUISt.draft.BeeTiers) != 0 {
		t.Fatalf("expected BeeTiers to be cleared after reset in draft, got: %+v", pairUISt.draft.BeeTiers)
	}

	// 保存并持久化验证
	swarmPairSave(st, pairUISt.draft, false)
	loaded2, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(loaded2.Orchestration.Pairs[0].BeeTiers) != 0 {
		t.Fatalf("expected BeeTiers to be empty on disk after save, got: %+v", loaded2.Orchestration.Pairs[0].BeeTiers)
	}
}

// 测试 swarmTierColor 对相同 ID 始终返回稳定一致的颜色。
func TestSwarmTierColor_Stable(t *testing.T) {
	cRead1 := swarmTierColor(nil, "read")
	cRead2 := swarmTierColor(nil, "read")
	if cRead1 != cRead2 {
		t.Fatalf("swarmTierColor for 'read' must be stable: %v vs %v", cRead1, cRead2)
	}
	if cRead1.R != 48 || cRead1.G != 176 || cRead1.B != 199 {
		t.Fatalf("expected read color (48, 176, 199), got: %v", cRead1)
	}

	cDeep1 := swarmTierColor(nil, "deep")
	cDeep2 := swarmTierColor(nil, "deep")
	if cDeep1 != cDeep2 {
		t.Fatalf("swarmTierColor for 'deep' must be stable: %v vs %v", cDeep1, cDeep2)
	}
	if cDeep1.R != 175 || cDeep1.G != 82 || cDeep1.B != 222 {
		t.Fatalf("expected deep color (175, 82, 222), got: %v", cDeep1)
	}

	for _, id := range []string{"custom-tier-1", "custom-tier-2", "arbitrary-id"} {
		colA := swarmTierColor(nil, id)
		colB := swarmTierColor(nil, id)
		if colA != colB {
			t.Fatalf("swarmTierColor for %q must be stable: %v vs %v", id, colA, colB)
		}
	}
}

// 删除守卫必须看运行态配置里的只读位：草稿里刚勾上只读不能绕过「至少保留一个可写档位」（S-095）。
func TestSwarmTierDeleteGuard_UsesRuntimeReadOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.Tiers = []config.SwarmTier{
		{ID: "read", Name: "只读", ReadOnly: true},
		{ID: "edit", Name: "执行"},
		{ID: "solo", Name: "单独"},
	}
	st.cfg.Orchestration.DefaultTier = "read"

	if disabled, _ := swarmTierDeleteGuard(st, "edit", true); disabled {
		t.Fatal("还有两个可写档位时不应禁用删除")
	}
	if disabled, tip := swarmTierDeleteGuard(st, "read", false); !disabled || tip != "默认档位不可删除" {
		t.Fatalf("默认档位应不可删除，得到 disabled=%v tip=%q", disabled, tip)
	}

	if ok := deleteTier(st, "solo"); !ok {
		t.Fatal("应允许删除非最后一个可写档位")
	}
	// 此时只剩 read（只读）与 edit（唯一可写）：草稿勾上只读也不能绕过守卫。
	if disabled, tip := swarmTierDeleteGuard(st, "edit", true); !disabled || tip != "至少保留一个可写档位" {
		t.Fatalf("唯一可写档位应不可删除，得到 disabled=%v tip=%q", disabled, tip)
	}
}

// deleteTier 遇到校验失败时不改内存，并且配对里的档位映射写在新副本上（S-095）。
func TestDeleteTier_RejectsLastWritableAndKeepsPairs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	st.cfg.Orchestration.Tiers = []config.SwarmTier{
		{ID: "read", Name: "只读", ReadOnly: true},
		{ID: "edit", Name: "执行"},
	}
	st.cfg.Orchestration.DefaultTier = "read"
	st.cfg.Orchestration.Pairs = []config.SwarmPair{{
		ID:           "pair-1",
		Name:         "配对一",
		LeadProvider: config.ProviderClaude,
		BeeTiers: map[string]config.SwarmTierOverride{
			"edit": {Model: "o1"},
		},
	}}
	if ok := deleteTier(st, "edit"); ok {
		t.Fatal("删除唯一可写档位应失败")
	}
	if len(st.cfg.Orchestration.Tiers) != 2 {
		t.Fatalf("删除失败后档位列表应保持不变，实际为 %+v", st.cfg.Orchestration.Tiers)
	}
	if _, ok := st.cfg.Orchestration.Pairs[0].BeeTiers["edit"]; !ok {
		t.Fatal("删除失败后配对的档位映射应保持不变")
	}

	// 允许删除时映射应级联清理，且写在新副本上而不是就地改原 map。
	if ok := saveTier(st, config.SwarmTier{ID: "deep", Name: "深度"}, true, nil); !ok {
		t.Fatal("saveTier 应允许新增档位")
	}
	st.cfg.Orchestration.Pairs[0].BeeTiers["deep"] = config.SwarmTierOverride{Model: "o3"}
	liveMap := st.cfg.Orchestration.Pairs[0].BeeTiers
	if ok := deleteTier(st, "deep"); !ok {
		t.Fatal("应允许删除新增档位")
	}
	if _, ok := liveMap["deep"]; !ok {
		t.Fatal("删除档位不应就地改动原 BeeTiers 映射")
	}
	if _, ok := st.cfg.Orchestration.Pairs[0].BeeTiers["deep"]; ok {
		t.Fatal("删除档位应级联移除配对的档位映射")
	}
}

// parseWritePaths 按行拆分写入白名单：去空白、丢空行、去重且保持顺序。
func TestParseWritePaths(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{name: "空串返回 nil", in: "", want: nil},
		{name: "只有空白行返回 nil", in: "  \n\t\n \r\n", want: nil},
		{name: "去掉首尾空白", in: "  docs/a.md  \n\tinternal/**/*.go\t", want: []string{"docs/a.md", "internal/**/*.go"}},
		{name: "丢弃空行", in: "a.md\n\n\nb.md\n", want: []string{"a.md", "b.md"}},
		{name: "丢弃重复项并保持顺序", in: "b.md\na.md\nb.md\n a.md ", want: []string{"b.md", "a.md"}},
		{name: "多行保序", in: "docs/superpowers/plans/**/*.md\ninternal/ui/*.go\ninternal/config/*.go", want: []string{"docs/superpowers/plans/**/*.md", "internal/ui/*.go", "internal/config/*.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseWritePaths(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("parseWritePaths(%q) = %#v, 期望 %#v", tc.in, got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("parseWritePaths(%q)[%d] = %q, 期望 %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}

// formatWritePaths 与 parseWritePaths 往返一致。
func TestWritePathsFormatParseRoundTrip(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"docs/superpowers/plans/**/*.md"},
		{"docs/a.md", "internal/ui/*.go", "internal/config/config.go"},
	}
	for _, in := range cases {
		text := formatWritePaths(in)
		got := parseWritePaths(text)
		if len(in) == 0 {
			if got != nil {
				t.Fatalf("空列表往返后应为 nil，得到 %#v", got)
			}
			continue
		}
		if len(got) != len(in) {
			t.Fatalf("%#v 往返后长度不一致：%#v", in, got)
		}
		for i := range in {
			if got[i] != in[i] {
				t.Fatalf("%#v 往返后第 %d 项为 %q", in, i, got[i])
			}
		}
		// 再格式化一次应完全稳定。
		if again := formatWritePaths(got); again != text {
			t.Fatalf("往返后格式化结果不稳定：%q vs %q", again, text)
		}
	}
}

// 带 WritePaths 的草稿保存后写入配置并落盘。
func TestSaveTier_PersistsWritePaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	draft := config.SwarmTier{
		ID:          "write-docs",
		Name:        "写文档",
		Description: "只允许改动计划文档",
		WritePaths:  parseWritePaths(" docs/superpowers/plans/**/*.md \ndocs/superpowers/plans/**/*.md\n"),
	}
	var errStr string
	if ok := saveTier(st, draft, true, &errStr); !ok {
		t.Fatalf("saveTier failed: %s", errStr)
	}

	var savedCfg *config.SwarmTier
	for i := range st.cfg.Orchestration.Tiers {
		if st.cfg.Orchestration.Tiers[i].ID == "write-docs" {
			savedCfg = &st.cfg.Orchestration.Tiers[i]
		}
	}
	if savedCfg == nil || len(savedCfg.WritePaths) != 1 || savedCfg.WritePaths[0] != "docs/superpowers/plans/**/*.md" {
		t.Fatalf("内存配置里的 WritePaths 不符：%+v", savedCfg)
	}

	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	for _, tier := range loaded.Orchestration.Tiers {
		if tier.ID != "write-docs" {
			continue
		}
		if len(tier.WritePaths) != 1 || tier.WritePaths[0] != "docs/superpowers/plans/**/*.md" {
			t.Fatalf("落盘的 WritePaths 不符：%+v", tier.WritePaths)
		}
		return
	}
	t.Fatal("落盘配置中未找到 write-docs 档位")
}

func TestDefaultSwarmTier(t *testing.T) {
	for _, id := range []string{"read", "chore", "edit", "deep"} {
		tier, ok := defaultSwarmTier(id)
		if !ok || tier.ID != id {
			t.Fatalf("未找到默认档位 %q：%+v", id, tier)
		}
	}
	if _, ok := defaultSwarmTier("custom"); ok {
		t.Fatal("自定义档位不应有默认配置")
	}
}

func TestSwarmTierDraftIsDefault(t *testing.T) {
	deep, ok := defaultSwarmTier("deep")
	if !ok {
		t.Fatal("未找到 deep 默认档位")
	}
	paths := formatWritePaths(deep.WritePaths)
	if !swarmTierDraftIsDefault(deep, paths) {
		t.Fatal("默认 deep 草稿应等于默认配置")
	}
	model := deep
	model.Model = "custom-model"
	description := deep
	description.Description = "修改后的说明"
	prompt := deep
	prompt.SystemPrompt = "修改后的提示词"
	for _, tc := range []struct {
		name  string
		draft config.SwarmTier
		paths string
	}{
		{name: "模型", draft: model, paths: paths},
		{name: "说明", draft: description, paths: paths},
		{name: "提示词", draft: prompt, paths: paths},
		{name: "白名单", draft: deep, paths: "other/**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if swarmTierDraftIsDefault(tc.draft, tc.paths) {
				t.Fatal("修改后的草稿不应等于默认配置")
			}
		})
	}
	read, ok := defaultSwarmTier("read")
	if !ok {
		t.Fatal("未找到 read 默认档位")
	}
	if !swarmTierDraftIsDefault(read, "ignored/**") {
		t.Fatal("只读档应忽略白名单文本")
	}
	edit, ok := defaultSwarmTier("edit")
	if !ok {
		t.Fatal("未找到 edit 默认档位")
	}
	edit.WritePaths = []string{}
	if !swarmTierDraftIsDefault(edit, "") {
		t.Fatal("空白名单应与 nil 等价")
	}
}

func TestSaveTier_ResetToDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	defaultTier, ok := defaultSwarmTier("edit")
	if !ok {
		t.Fatal("未找到 edit 默认档位")
	}
	draft := defaultTier
	draft.Model = "custom-model"
	draft.Effort = "high"
	draft.Description = "修改后的说明"
	for i := range st.cfg.Orchestration.Tiers {
		if st.cfg.Orchestration.Tiers[i].ID == draft.ID {
			st.cfg.Orchestration.Tiers[i] = draft
			break
		}
	}
	draft = defaultTier
	var errStr string
	if !saveTier(st, draft, false, &errStr) {
		t.Fatalf("保存重置后的档位失败：%s", errStr)
	}
	for _, tier := range st.cfg.Orchestration.Tiers {
		if tier.ID == defaultTier.ID {
			if !reflect.DeepEqual(tier, defaultTier) {
				t.Fatalf("重置后的档位不等于默认值：%+v", tier)
			}
			return
		}
	}
	t.Fatal("配置中未找到 edit 档位")
}
