package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// TestParseSwarmReport_NewFields 验证解析器能够正确提取档位、模型、用户指定模型、越权写入与可信度等新增字段。
func TestParseSwarmReport_NewFields(t *testing.T) {
	text := `[编排] 子代理汇报：Ives, Dune 已结束工作。

## Ives：修复时间线解析器 (已完成)
- 档位：deep · 模型：anthropic/opus · 用户指定模型
- 越权写入：只读档子代理改动了文件，改动已丢弃，未合并
- 可信度：高（已核实）
- 合并结果：已合并
- 汇报总结：修复了时间线中的空行解析逻辑。

## Dune：代码只读调研 (已完成)
- 档位：read · 模型：anthropic/claude-3-5-sonnet
- 可信度：不可核实
- 合并结果：无需合并
- 汇报总结：完成了系统代码调用链调研。`

	title, entries := parseSwarmReport(text)
	if title != "子代理汇报：Ives, Dune 已结束工作。" {
		t.Fatalf("title = %q, want %q", title, "子代理汇报：Ives, Dune 已结束工作。")
	}
	if len(entries) != 2 {
		t.Fatalf("entries count = %d, want 2", len(entries))
	}

	e1 := entries[0]
	if e1.Name != "Ives" {
		t.Errorf("e1.Name = %q, want Ives", e1.Name)
	}
	if e1.Task != "修复时间线解析器" {
		t.Errorf("e1.Task = %q, want 修复时间线解析器", e1.Task)
	}
	if e1.Status != "已完成" {
		t.Errorf("e1.Status = %q, want 已完成", e1.Status)
	}
	if e1.Tier != "deep" {
		t.Errorf("e1.Tier = %q, want deep", e1.Tier)
	}
	if e1.Model != "anthropic/opus" {
		t.Errorf("e1.Model = %q, want anthropic/opus", e1.Model)
	}
	if !e1.DirectModel {
		t.Errorf("e1.DirectModel = false, want true")
	}
	if e1.Violation != "只读档子代理改动了文件，改动已丢弃，未合并" {
		t.Errorf("e1.Violation = %q, want 只读档子代理改动了文件，改动已丢弃，未合并", e1.Violation)
	}
	if e1.Credibility != "高（已核实）" {
		t.Errorf("e1.Credibility = %q, want 高（已核实）", e1.Credibility)
	}
	if e1.MergeResult != "已合并" {
		t.Errorf("e1.MergeResult = %q, want 已合并", e1.MergeResult)
	}

	e2 := entries[1]
	if e2.Name != "Dune" {
		t.Errorf("e2.Name = %q, want Dune", e2.Name)
	}
	if e2.Tier != "read" {
		t.Errorf("e2.Tier = %q, want read", e2.Tier)
	}
	if e2.Model != "anthropic/claude-3-5-sonnet" {
		t.Errorf("e2.Model = %q, want anthropic/claude-3-5-sonnet", e2.Model)
	}
	if e2.DirectModel {
		t.Errorf("e2.DirectModel = true, want false")
	}
	if e2.Violation != "" {
		t.Errorf("e2.Violation = %q, want empty", e2.Violation)
	}
	if e2.Credibility != "不可核实" {
		t.Errorf("e2.Credibility = %q, want 不可核实", e2.Credibility)
	}
}

// TestSwarmTierName_Fallback 验证 swarmTierName 回退逻辑：未配置或找不到时返回 tierID 本身，匹配时返回配置的 Name。
func TestSwarmTierName_Fallback(t *testing.T) {
	// 1. chatState 为 nil 时回退为 ID 本身
	if got := swarmTierName(nil, "deep"); got != "deep" {
		t.Errorf("swarmTierName(nil, deep) = %q, want deep", got)
	}
	if got := swarmTierName(nil, "custom-tier"); got != "custom-tier" {
		t.Errorf("swarmTierName(nil, custom-tier) = %q, want custom-tier", got)
	}

	// 2. 未配置 settings 的 chatState 回退为 ID 本身
	s := newChatState(NewFakeClient())
	if got := swarmTierName(s, "read"); got != "read" {
		t.Errorf("swarmTierName(s, read) without settings = %q, want read", got)
	}

	// 3. 注入配置后能够正确映射 ID -> Name；未配置或 Name 为空时回退为 ID
	st := &SettingsState{
		cfg: config.Config{
			Orchestration: config.OrchestrationConfig{
				Tiers: []config.SwarmTier{
					{ID: "read", Name: "只读模式"},
					{ID: "deep", Name: "深度推演"},
					{ID: "no-name", Name: ""},
				},
			},
		},
	}
	setChatSettings(s, st)

	if got := swarmTierName(s, "read"); got != "只读模式" {
		t.Errorf("swarmTierName(s, read) = %q, want 只读模式", got)
	}
	if got := swarmTierName(s, "deep"); got != "深度推演" {
		t.Errorf("swarmTierName(s, deep) = %q, want 深度推演", got)
	}
	if got := swarmTierName(s, "no-name"); got != "no-name" {
		t.Errorf("swarmTierName(s, no-name) = %q, want no-name", got)
	}
	if got := swarmTierName(s, "unknown-tier"); got != "unknown-tier" {
		t.Errorf("swarmTierName(s, unknown-tier) = %q, want unknown-tier", got)
	}
}

// TestSwarmBeeRow_RenderTags 验证在渲染 bee 卡片时展示“越权写入”与“指定模型”标签及简短模型名。
func TestSwarmBeeRow_RenderTags(t *testing.T) {
	s := newChatState(NewFakeClient())
	bee := SwarmBee{
		ThreadID:          "h-test-1",
		Index:             1,
		Name:              "Larch",
		Status:            "completed",
		Tier:              "read",
		DirectModel:       true,
		ReadOnlyViolation: true,
		ReadOnly:          true,
		Provider:          "anthropic",
		Model:             "anthropic/claude-3-5-sonnet",
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeRow(c, s, bee, 0)
	}, 800, 600)

	if !tt.HasText("指定模型") {
		t.Errorf("expected rendered bee row to include '指定模型'")
	}
	if !tt.HasText("越权写入") {
		t.Errorf("expected rendered bee row to include '越权写入'")
	}
	if !tt.HasText("claude-3-5-sonnet") {
		t.Errorf("expected rendered bee row to include short model 'claude-3-5-sonnet'")
	}
}

// TestSwarmReportEntryCard_RenderTags 验证时间线卡片中渲染档位 chip、模型、“指定模型”、“越权写入”与可信度文本。
func TestSwarmReportEntryCard_RenderTags(t *testing.T) {
	s := newChatState(NewFakeClient())
	entry := swarmReportEntry{
		Name:        "Ives",
		Task:        "修复时间线解析器",
		Status:      "已完成",
		Tier:        "deep",
		Model:       "anthropic/opus",
		DirectModel: true,
		Violation:   "只读档子代理改动了文件，改动已丢弃，未合并",
		Credibility: "高（已核实）",
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmReportEntryCard(c, s, nil, "msg-1", 0, entry, 520)
	}, 800, 600)

	if !tt.HasText("指定模型") {
		t.Errorf("expected report entry card to include '指定模型'")
	}
	if !tt.HasText("越权写入") {
		t.Errorf("expected report entry card to include '越权写入'")
	}
	if !tt.HasText("高（已核实）") {
		t.Errorf("expected report entry card to include credibility '高（已核实）'")
	}
}

// TestSwarmBeeRow_CredibilityBadge 验证在渲染 bee 卡片时展示可信度标记。
func TestSwarmBeeRow_CredibilityBadge(t *testing.T) {
	s := newChatState(NewFakeClient())

	// 1. 全部核实：Cited 3, Verified 3（文本 3/3 可见）
	beeFull := SwarmBee{
		ThreadID: "h-test-cred-full",
		Index:    1,
		Name:     "Larch",
		Status:   "completed",
		Credibility: &SwarmCredibility{
			Cited:    3,
			Verified: 3,
			Summary:  "引用 3/3 核实",
		},
	}
	ttFull := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeRow(c, s, beeFull, 0)
	}, 800, 600)

	if !ttFull.HasText("3/3") {
		t.Errorf("expected rendered bee row to include '3/3'")
	}

	// 2. 部分核实：Cited 3, Verified 2（文本 2/3 可见）
	beePartial := SwarmBee{
		ThreadID: "h-test-cred-partial",
		Index:    2,
		Name:     "Dune",
		Status:   "completed",
		Credibility: &SwarmCredibility{
			Cited:      3,
			Verified:   2,
			Unverified: []string{"parser.go:15"},
			Summary:    "引用 2/3 核实，不可核实：parser.go:15",
		},
	}
	ttPartial := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeRow(c, s, beePartial, 0)
	}, 800, 600)

	if !ttPartial.HasText("2/3") {
		t.Errorf("expected rendered bee row to include '2/3'")
	}

	// 3. ReadOnlyViolation 置位时不展示可信度标记
	beeViolation := SwarmBee{
		ThreadID:          "h-test-cred-violation",
		Index:             3,
		Name:              "Ives",
		Status:            "completed",
		ReadOnlyViolation: true,
		Credibility: &SwarmCredibility{
			Cited:    3,
			Verified: 3,
			Summary:  "引用 3/3 核实",
		},
	}
	ttViolation := ui.NewTester(func(c *ui.Context) {
		renderSwarmBeeRow(c, s, beeViolation, 0)
	}, 800, 600)

	if ttViolation.HasText("3/3") {
		t.Errorf("expected rendered bee row with ReadOnlyViolation NOT to include '3/3'")
	}
}
