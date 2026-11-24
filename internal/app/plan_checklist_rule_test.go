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
	got := firstTurnExtraSection(dir, "ordinary")
	if got != planChecklistRule {
		t.Fatalf("未开启 steward 时应只返回计划清单规则，实际: %q", got)
	}
	if !strings.Contains(got, "TaskCreate") || !strings.Contains(got, "update_plan") {
		t.Fatalf("计划清单规则应包含 TaskCreate 和 update_plan，实际: %q", got)
	}
}
