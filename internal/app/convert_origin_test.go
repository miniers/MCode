package app

import (
	"testing"

	"mcode/internal/automation"
	"mcode/internal/store"
)

func TestAutomationToUIOrigin(t *testing.T) {
	def := automation.Definition{
		Automation: store.Automation{
			ID:      "auto-steward",
			Name:    "总管巡检",
			Enabled: true,
		},
		Spec: automation.Spec{
			Origin: "steward",
		},
	}
	got := AutomationToUI(def)
	if got.Origin != "steward" {
		t.Fatalf("AutomationToUI Origin = %q, want %q", got.Origin, "steward")
	}

	def.Spec.Origin = ""
	if gotEmpty := AutomationToUI(def); gotEmpty.Origin != "" {
		t.Fatalf("AutomationToUI Origin = %q, want empty", gotEmpty.Origin)
	}
}
