package mcodeui

import "testing"

func TestSettingsMagpieHub_ClineAgentRowAbsent(t *testing.T) {
	tt, _, _ := magpiePageTester(t, true)
	if tt.HasText("Cline") {
		t.Fatalf("Magpie 页面不应显示 Cline: %v", tt.Texts())
	}
}
