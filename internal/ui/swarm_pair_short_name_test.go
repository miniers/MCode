package mcodeui

import (
	"strings"
	"testing"

	"mcode/internal/config"
)

// swarmPairShortNameProviders 构造带完整显示名的提供方目录：短名由 modelShortDisplayName 得出。
func swarmPairShortNameProviders() []ProviderInfo {
	return []ProviderInfo{
		{
			ID:   config.ProviderClaude,
			Name: "Claude",
			Models: []ModelInfo{
				{ID: "claude-opus-5-5", Name: "Claude Opus 5.5"},
			},
		},
		{
			ID:   config.ProviderCodex,
			Name: "Codex",
			Models: []ModelInfo{
				{ID: "gpt-5", Name: "GPT-5"},
			},
		},
	}
}

func TestSwarmPairTitleShortNames(t *testing.T) {
	providers := swarmPairShortNameProviders()

	// 未命名配对：两侧都用短名，连接符保持 " + "。
	p := config.SwarmPair{
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-opus-5-5",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-5",
	}
	if got := swarmPairTitle(p, providers); got != "Opus 5.5 + GPT-5" {
		t.Errorf("expected 'Opus 5.5 + GPT-5', got %q", got)
	}

	// 占位词保持不变。
	empty := config.SwarmPair{LeadProvider: config.ProviderClaude}
	if got := swarmPairTitle(empty, providers); got != "任意模型 + 跟随主代理" {
		t.Errorf("expected '任意模型 + 跟随主代理', got %q", got)
	}
}

func TestSwarmPairTitleLegacyAutoName(t *testing.T) {
	providers := swarmPairShortNameProviders()
	p := config.SwarmPair{
		Name:           "Claude Opus 5.5 + GPT-5",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-opus-5-5",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-5",
	}
	if got := swarmPairFullTitle(p, providers); got != p.Name {
		t.Fatalf("expected full title %q, got %q", p.Name, got)
	}
	if got := swarmPairTitle(p, providers); got != "Opus 5.5 + GPT-5" {
		t.Errorf("expected legacy auto name shortened to 'Opus 5.5 + GPT-5', got %q", got)
	}
}

func TestSwarmPairTitleLegacyQuickSaveName(t *testing.T) {
	providers := []ProviderInfo{
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
	p := config.SwarmPair{
		Name:           "Claude Opus 5.5 + DeepSeek V4.1 Flash (auto)",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-opus-5-5",
		WorkerProvider: "deepseek",
		WorkerModel:    "deepseek-v4.1-flash",
	}
	if !swarmPairIsAutoName(p, providers) {
		t.Fatalf("旧快速保存的全名应判为自动名：%q", p.Name)
	}
	if got, want := swarmPairTitle(p, providers), "Opus 5.5 + V4.1 Flash"; got != want {
		t.Errorf("expected %q, got %q", want, got)
	}

	// 自定义名既不算自动名，也原样显示。
	custom := p
	custom.Name = "我的配对"
	if swarmPairIsAutoName(custom, providers) {
		t.Errorf("自定义名不应判为自动名：%q", custom.Name)
	}
	if got := swarmPairTitle(custom, providers); got != "我的配对" {
		t.Errorf("expected custom name kept, got %q", got)
	}
}

func TestSwarmPairListDetailLegacyQuickSaveName(t *testing.T) {
	providers := []ProviderInfo{
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
	p := config.SwarmPair{
		Name:           "Claude Opus 5.5 + DeepSeek V4.1 Flash (auto)",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-opus-5-5",
		WorkerProvider: "deepseek",
		WorkerModel:    "deepseek-v4.1-flash",
	}
	detail := swarmPairListDetail(p, providers)
	if strings.Contains(detail, p.Name) {
		t.Errorf("expected detail to omit legacy quick-save name, got %q", detail)
	}
	if strings.Contains(detail, "Opus 5.5 + V4.1 Flash") {
		t.Errorf("expected detail not to repeat model pair, got %q", detail)
	}
}

func TestSwarmPairTitleCustomName(t *testing.T) {
	providers := swarmPairShortNameProviders()
	p := config.SwarmPair{
		Name:           "我的配对",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-opus-5-5",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-5",
	}
	if got := swarmPairTitle(p, providers); got != "我的配对" {
		t.Errorf("expected '我的配对', got %q", got)
	}
	if detail := swarmPairListDetail(p, providers); !strings.Contains(detail, "Opus 5.5 + GPT-5") {
		t.Errorf("expected detail to use short names, got %q", detail)
	} else if strings.Contains(detail, "Claude Opus 5.5") {
		t.Errorf("expected detail to avoid full model name, got %q", detail)
	}
}

func TestSwarmPairTooltipKeepsFullNames(t *testing.T) {
	providers := swarmPairShortNameProviders()
	p := config.SwarmPair{
		Name:           "Claude Opus 5.5 + GPT-5",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-opus-5-5",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-5",
	}
	tip := swarmPairTooltip(p, "", providers)
	if !strings.Contains(tip, "Claude Opus 5.5") || !strings.Contains(tip, "GPT-5") {
		t.Errorf("expected tooltip to keep full model names, got %q", tip)
	}
}

func TestSwarmPairModelShortLabel(t *testing.T) {
	providers := swarmPairShortNameProviders()
	if got := swarmPairModelShortLabel(providers, config.ProviderClaude, "claude-opus-5-5", "任意模型"); got != "Opus 5.5" {
		t.Errorf("expected 'Opus 5.5', got %q", got)
	}
	if got := swarmPairModelShortLabel(providers, config.ProviderClaude, "", "跟随主代理"); got != "跟随主代理" {
		t.Errorf("expected '跟随主代理', got %q", got)
	}
}
