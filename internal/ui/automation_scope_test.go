package mcodeui

import "testing"

func TestAutomationScopeEditProjectBinding(t *testing.T) {
	as := &automationState{projectID: ""}
	as.fillAutomation(Automation{ID: "a", ProjectID: "p2"})
	if as.projectID != "" || as.editorProjectID != "p2" {
		t.Fatalf("scope = %q, editor project = %q; want empty scope and p2", as.projectID, as.editorProjectID)
	}
}
