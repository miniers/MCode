package app

import (
	"testing"

	"mcode/internal/config"
	"mcode/internal/kanban"
)

// TestStewardTaskDefaults 验证任务模型选配对时按配对两侧下发，选单个模型或配对已删除时不带配对。
func TestStewardTaskDefaults(t *testing.T) {
	high := "high"
	var c config.Config
	c.Orchestration.Pairs = []config.SwarmPair{{
		ID: "p1", LeadProvider: "claude", LeadModel: "opus", LeadEffort: &high,
		WorkerProvider: "codex", WorkerModel: "gpt-x",
	}}
	c.Orchestration.Steward.TaskProvider, c.Orchestration.Steward.TaskModel = "codex", "mini"

	if got := stewardTaskDefaults(c); got != (kanban.StewardTaskDefaults{Provider: "codex", Model: "mini"}) {
		t.Fatalf("单个模型 = %+v", got)
	}
	c.Orchestration.Steward.TaskPairID = "p1"
	want := kanban.StewardTaskDefaults{Provider: "claude", Model: "opus", Effort: "high", BeesProvider: "codex", BeesModel: "gpt-x", PairID: "p1"}
	if got := stewardTaskDefaults(c); got != want {
		t.Fatalf("配对 = %+v, want %+v", got, want)
	}
	c.Orchestration.Steward.TaskPairID = "gone"
	if got := stewardTaskDefaults(c); got.PairID != "" {
		t.Fatalf("已删除的配对不应生效: %+v", got)
	}
}

// TestStewardTaskDefaultsEffort 验证非配对时 TaskEffort 透传到 StewardTaskDefaults.Effort，
// 配对时仍使用配对的 LeadEffort 而不受 TaskEffort 影响。
func TestStewardTaskDefaultsEffort(t *testing.T) {
	leadEffort := "medium"
	var c config.Config
	c.Orchestration.Pairs = []config.SwarmPair{{
		ID: "p1", LeadProvider: "claude", LeadModel: "opus", LeadEffort: &leadEffort,
		WorkerProvider: "codex", WorkerModel: "gpt-x",
	}}
	c.Orchestration.Steward.TaskProvider = "codex"
	c.Orchestration.Steward.TaskModel = "mini"
	c.Orchestration.Steward.TaskEffort = "high"

	wantSingle := kanban.StewardTaskDefaults{Provider: "codex", Model: "mini", Effort: "high"}
	if got := stewardTaskDefaults(c); got != wantSingle {
		t.Fatalf("非配对默认 = %+v, want %+v", got, wantSingle)
	}

	c.Orchestration.Steward.TaskPairID = "p1"
	wantPair := kanban.StewardTaskDefaults{
		Provider: "claude", Model: "opus", Effort: "medium",
		BeesProvider: "codex", BeesModel: "gpt-x", PairID: "p1",
	}
	if got := stewardTaskDefaults(c); got != wantPair {
		t.Fatalf("配对默认 = %+v, want %+v", got, wantPair)
	}

	c.Orchestration.Steward.TaskPairID = "gone"
	if got := stewardTaskDefaults(c); got != wantSingle {
		t.Fatalf("已删除配对退回单模型 = %+v, want %+v", got, wantSingle)
	}
}
