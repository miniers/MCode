package mcodeui

import (
	"testing"

	"mcode/internal/config"
)

// pairFavFixture 构造一组主/子模型选择（含推理强度），返回解析后的两侧信息供收藏测试复用。
func pairFavFixture() (ModelPickerProps, modelPickerSide, modelPickerSide) {
	providers := []ProviderInfo{
		{
			ID:   "claude",
			Name: "Claude",
			Models: []ModelInfo{
				{ID: "opus-5.5", Name: "Opus 5.5", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
				{ID: "sonnet-5.5", Name: "Sonnet 5.5", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"},
			},
		},
	}
	p := ModelPickerProps{
		Mode:      ModelPickerSwarm,
		SwarmOn:   true,
		Providers: providers,
		Selection: ModelSelection{Provider: "claude", Model: "opus-5.5", Effort: "high"},
		Bees:      ModelSelection{Provider: "claude", Model: "sonnet-5.5", Effort: "medium"},
	}
	return p, modelPickerSideInfo(p, ModelPickerSideLead), modelPickerSideInfo(p, ModelPickerSideBees)
}

// pairFavDrop 返回去掉指定 ID 的配对列表，模拟设置页的 RemovePair。
func pairFavDrop(pairs []SwarmPair, id string) []SwarmPair {
	next := make([]SwarmPair, 0, len(pairs))
	for _, pair := range pairs {
		if pair.ID != id {
			next = append(next, pair)
		}
	}
	return next
}

// pairFavShortNameProviders 构造带全名与 (auto) 后缀的模型目录，用于验证默认名取短名。
func pairFavShortNameProviders() []ProviderInfo {
	return []ProviderInfo{
		{
			ID:   config.ProviderClaude,
			Name: "Claude",
			Models: []ModelInfo{
				{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
			},
		},
		{
			ID:   "deepseek",
			Name: "DeepSeek",
			Models: []ModelInfo{
				{ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash (auto)"},
			},
		},
	}
}

// TestSwarmPairQuickSaveDefaultName_ShortNames 验证默认配对名用短名，不带全名与 (auto) 后缀。
func TestSwarmPairQuickSaveDefaultName_ShortNames(t *testing.T) {
	providers := pairFavShortNameProviders()
	lead := modelPickerSide{Selection: ModelSelection{Provider: config.ProviderClaude, Model: "claude-opus-5-5"}}
	bees := modelPickerSide{Selection: ModelSelection{Provider: "deepseek", Model: "deepseek-v4.1-flash"}}
	if got, want := swarmPairQuickSaveDefaultName(providers, lead, bees), "Opus 5.5 + V4.1 Flash"; got != want {
		t.Fatalf("默认配对名 = %q, want %q", got, want)
	}
}

// TestSwarmPairFavPlan_SaveThenMatch 验证未匹配的组合拼出默认名草稿，保存后再次匹配并可取回该配对。
func TestSwarmPairFavPlan_SaveThenMatch(t *testing.T) {
	p, lead, bees := pairFavFixture()

	plan := swarmPairFavPlanFor(nil, p, lead, bees)
	if plan.Matched {
		t.Fatalf("未保存时不应匹配：%+v", plan)
	}
	if plan.HasDup {
		t.Fatalf("空列表不应判为重复：%+v", plan)
	}
	if got, want := plan.Draft.Name, "Opus 5.5 + Sonnet 5.5"; got != want {
		t.Fatalf("默认配对名 = %q, want %q", got, want)
	}
	if plan.Draft.LeadProvider != "claude" || plan.Draft.LeadModel != "opus-5.5" ||
		plan.Draft.WorkerProvider != "claude" || plan.Draft.WorkerModel != "sonnet-5.5" {
		t.Fatalf("草稿主/子模型不正确：%+v", plan.Draft)
	}

	// 模拟 swarmPairSave 落盘后的结果：AddPair 会补上 ID。
	saved := plan.Draft
	saved.ID = "pair-1"
	pairs := []SwarmPair{saved}

	current, ok := swarmPairQuickSaveFind(pairs, lead, bees)
	if !ok || current.ID != "pair-1" {
		t.Fatalf("保存后应匹配到 pair-1，实际 %+v, ok=%v", current, ok)
	}
	if again := swarmPairFavPlanFor(pairs, p, lead, bees); !again.Matched || again.Pair.ID != "pair-1" {
		t.Fatalf("再次点击应命中已存配对：%+v", again)
	}
}

// TestSwarmPairFavPlan_DeleteMatched 验证已匹配时点击会指向该配对，删除后回到未匹配。
func TestSwarmPairFavPlan_DeleteMatched(t *testing.T) {
	p, lead, bees := pairFavFixture()
	draft := swarmPairQuickSaveDraft(p, lead, bees, "我的配对")
	draft.ID = "pair-7"
	pairs := []SwarmPair{draft}

	plan := swarmPairFavPlanFor(pairs, p, lead, bees)
	if !plan.Matched || plan.Pair.ID != "pair-7" {
		t.Fatalf("应命中待删除的 pair-7：%+v", plan)
	}
	if plan.HasDup || plan.Draft.ID != "" {
		t.Fatalf("已匹配时不应再生成新增草稿：%+v", plan)
	}

	if _, ok := swarmPairQuickSaveFind(pairFavDrop(pairs, plan.Pair.ID), lead, bees); ok {
		t.Fatalf("删除后不应再匹配")
	}
}

// TestSwarmPairFavPlan_DuplicateCombo 验证同组合但强度不同时更新原配对。
func TestSwarmPairFavPlan_DuplicateCombo(t *testing.T) {
	p, lead, bees := pairFavFixture()
	existing := swarmPairQuickSaveDraft(p, lead, bees, "另一档强度")
	// 把已存配对的强度和快速模式改掉，使其与当前选择不同。
	low := "low"
	existing.LeadEffort = &low
	existing.WorkerEffort = &low
	existing.LeadFastMode = true
	existing.WorkerFastMode = true
	existing.ID = "pair-dup"
	existing.BeeTiers = map[string]config.SwarmTierOverride{"tier": {}}

	pairs := []SwarmPair{existing}
	plan := swarmPairFavPlanFor(pairs, p, lead, bees)
	if plan.Matched {
		t.Fatalf("强度不同不应匹配：%+v", plan)
	}
	if !plan.HasDup || plan.Duplicate.ID != "pair-dup" {
		t.Fatalf("应判为重复并指向 pair-dup：%+v", plan)
	}
	updated := plan.Duplicate
	if updated.Name != existing.Name || updated.LeadProvider != existing.LeadProvider || updated.LeadModel != existing.LeadModel ||
		updated.WorkerProvider != existing.WorkerProvider || updated.WorkerModel != existing.WorkerModel {
		t.Fatalf("更新时应保留原配对名称和模型：%+v", updated)
	}
	if _, ok := updated.BeeTiers["tier"]; !ok {
		t.Fatalf("更新时应保留原配对档位：%+v", updated)
	}
	if updated.LeadEffort == nil || *updated.LeadEffort != lead.Selection.Effort || *updated.LeadEffort == *existing.LeadEffort ||
		updated.WorkerEffort == nil || *updated.WorkerEffort != bees.Selection.Effort || *updated.WorkerEffort == *existing.WorkerEffort ||
		updated.LeadFastMode != lead.Selection.Fast || updated.LeadFastMode == existing.LeadFastMode ||
		updated.WorkerFastMode != bees.Selection.Fast || updated.WorkerFastMode == existing.WorkerFastMode {
		t.Fatalf("更新时应采用当前强度和快速模式：%+v", updated)
	}
	pairs[0] = updated
	if again := swarmPairFavPlanFor(pairs, p, lead, bees); !again.Matched || again.Pair.ID != "pair-dup" {
		t.Fatalf("更新后应匹配原配对：%+v", again)
	}
}
