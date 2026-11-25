package app

import (
	"strings"
	"testing"

	"mcode/internal/config"
)

func TestFirstTurnExtraSectionPlanChecklist(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = false
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	got := firstTurnExtraSection(dir, "ordinary", false)
	if got != planChecklistRule {
		t.Fatalf("未开启 steward 时应只返回计划清单规则，实际: %q", got)
	}
	if !strings.Contains(got, "TaskCreate") || !strings.Contains(got, "update_plan") {
		t.Fatalf("计划清单规则应包含 TaskCreate 和 update_plan，实际: %q", got)
	}
}

func TestFirstTurnExtraSkipsPlansOnlyLead(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = false
	cfg.Orchestration.Steward.EnabledChoice = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if got := firstTurnExtraSection(dir, "lead", true); got != "" {
		t.Fatalf("plansOnlyLead=true 时应返回空字符串，实际: %q", got)
	}
	if got := firstTurnExtraSection(dir, "lead", false); got != planChecklistRule {
		t.Fatalf("plansOnlyLead=false 时应返回 planChecklistRule，实际: %q", got)
	}
}
