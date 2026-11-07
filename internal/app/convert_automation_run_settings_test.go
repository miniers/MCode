package app

import (
	"testing"

	"mcode/internal/automation"
	"mcode/internal/store"
	mcodeui "mcode/internal/ui"
)

// TestAutomationRunSettingsRoundtrip 验证模型与权限设置经 AutomationInputToSpec 与 AutomationToUI 往返不丢失。
func TestAutomationRunSettingsRoundtrip(t *testing.T) {
	in := mcodeui.AutomationInput{
		ProjectID:   "proj-1",
		Name:        "模型设置",
		Prompt:      "检查",
		Schedule:    mcodeui.AutomationSchedule{Type: "interval", Seconds: 3600},
		Enabled:     true,
		Provider:    "anthropic",
		Model:       "claude-sonnet-4-5",
		Effort:      "high",
		RuntimeMode: "autoAcceptEdits",
	}

	spec := AutomationInputToSpec(in, automation.Spec{})
	if spec.Provider != "anthropic" || spec.Model != "claude-sonnet-4-5" ||
		spec.Effort != "high" || spec.RuntimeMode != "autoAcceptEdits" {
		t.Fatalf("AutomationInputToSpec 丢失模型与权限: %+v", spec)
	}

	projID := in.ProjectID
	def := automation.Definition{
		Automation: store.Automation{
			ID:        "auto-1",
			ProjectID: &projID,
			Name:      in.Name,
			Prompt:    in.Prompt,
			Enabled:   true,
			Status:    automation.StatusActive,
			Revision:  1,
		},
		Spec: spec,
	}
	uiAuto := AutomationToUI(def)
	if uiAuto.Provider != "anthropic" || uiAuto.Model != "claude-sonnet-4-5" ||
		uiAuto.Effort != "high" || uiAuto.RuntimeMode != "autoAcceptEdits" {
		t.Fatalf("AutomationToUI 丢失模型与权限: %+v", uiAuto)
	}
	if uiAuto.RunMode != automation.RunModeStandalone {
		t.Fatalf("AutomationToUI 应带出运行方式: %q", uiAuto.RunMode)
	}

	// 清空后再往返一次：空串表示跟随默认，不应被旧值回填。
	in.Provider, in.Model, in.Effort, in.RuntimeMode = "", "", "", ""
	spec = AutomationInputToSpec(in, spec)
	if spec.Provider != "" || spec.Model != "" || spec.Effort != "" || spec.RuntimeMode != "" {
		t.Fatalf("空串应恢复默认: %+v", spec)
	}
	def.Spec = spec
	uiAuto = AutomationToUI(def)
	if uiAuto.Provider != "" || uiAuto.Model != "" || uiAuto.Effort != "" || uiAuto.RuntimeMode != "" {
		t.Fatalf("清空后 AutomationToUI 不应回填: %+v", uiAuto)
	}
}
