package acp

import (
	"reflect"
	"testing"

	"mcode/internal/harness"
)

func TestAgentsMetadata(t *testing.T) {
	agents := Agents()
	if len(agents) != 6 {
		t.Fatalf("expected 6 agents, got %d", len(agents))
	}

	// 验证可执行文件名
	expectedBinaries := map[Agent]string{
		AgentCline:    "cline",
		AgentCursor:   "cursor-agent",
		AgentOpenCode: "opencode",
		AgentGrok:     "grok",
		AgentDevin:    "devin",
		AgentDroid:    "droid",
	}
	for a, expected := range expectedBinaries {
		if got := DefaultBinary(a); got != expected {
			t.Errorf("DefaultBinary(%s) = %s, expected %s", a, got, expected)
		}
	}

	// 验证显示名称
	expectedNames := map[Agent]string{
		AgentCline:    "Cline",
		AgentCursor:   "Cursor",
		AgentOpenCode: "OpenCode",
		AgentGrok:     "Grok",
		AgentDevin:    "Devin",
		AgentDroid:    "Factory Droid",
	}
	for a, expected := range expectedNames {
		if got := DisplayName(a); got != expected {
			t.Errorf("DisplayName(%s) = %s, expected %s", a, got, expected)
		}
	}
}

func TestLaunchArgsMatrix(t *testing.T) {
	tests := []struct {
		agent    Agent
		mode     harness.PermissionMode
		expected []string
	}{
		// Cline
		{AgentCline, harness.ModeReadOnly, []string{"--acp"}},
		{AgentCline, harness.ModeSupervised, []string{"--acp"}},
		{AgentCline, harness.ModeAutoAcceptEdits, []string{"--acp"}},
		{AgentCline, harness.ModeAuto, []string{"--acp"}},
		{AgentCline, harness.ModeFullAccess, []string{"--acp"}},

		// Droid
		{AgentDroid, harness.ModeReadOnly, []string{"exec", "--output-format", "acp"}},
		{AgentDroid, harness.ModeSupervised, []string{"exec", "--output-format", "acp"}},
		{AgentDroid, harness.ModeAutoAcceptEdits, []string{"exec", "--output-format", "acp"}},
		{AgentDroid, harness.ModeAuto, []string{"exec", "--output-format", "acp"}},
		{AgentDroid, harness.ModeFullAccess, []string{"exec", "--output-format", "acp"}},

		// Cursor
		{AgentCursor, harness.ModeReadOnly, []string{"acp"}},
		{AgentCursor, harness.ModeSupervised, []string{"acp"}},
		{AgentCursor, harness.ModeAutoAcceptEdits, []string{"acp"}},
		{AgentCursor, harness.ModeAuto, []string{"--auto-review", "acp"}},
		{AgentCursor, harness.ModeFullAccess, []string{"--force", "acp"}},

		// Grok
		{AgentGrok, harness.ModeReadOnly, []string{"--permission-mode", "default", "agent", "stdio"}},
		{AgentGrok, harness.ModeSupervised, []string{"--permission-mode", "default", "agent", "stdio"}},
		{AgentGrok, harness.ModeAutoAcceptEdits, []string{"--permission-mode", "acceptEdits", "agent", "stdio"}},
		{AgentGrok, harness.ModeAuto, []string{"--permission-mode", "auto", "agent", "stdio"}},
		{AgentGrok, harness.ModeFullAccess, []string{"agent", "--always-approve", "stdio"}},

		// Devin
		{AgentDevin, harness.ModeReadOnly, []string{"--permission-mode", "auto", "acp"}},
		{AgentDevin, harness.ModeSupervised, []string{"--permission-mode", "auto", "acp"}},
		{AgentDevin, harness.ModeAutoAcceptEdits, []string{"--permission-mode", "accept-edits", "acp"}},
		{AgentDevin, harness.ModeAuto, []string{"--permission-mode", "smart", "acp"}},
		{AgentDevin, harness.ModeFullAccess, []string{"--permission-mode", "dangerous", "acp"}},

		// OpenCode
		{AgentOpenCode, harness.ModeReadOnly, []string{"acp"}},
		{AgentOpenCode, harness.ModeSupervised, []string{"acp"}},
		{AgentOpenCode, harness.ModeAutoAcceptEdits, []string{"acp"}},
		{AgentOpenCode, harness.ModeAuto, []string{"acp"}},
		{AgentOpenCode, harness.ModeFullAccess, []string{"acp"}},
	}

	for _, tc := range tests {
		got := LaunchArgs(tc.agent, tc.mode)
		if !reflect.DeepEqual(got, tc.expected) {
			t.Errorf("LaunchArgs(%s, %s) = %v, expected %v", tc.agent, tc.mode, got, tc.expected)
		}
	}
}

func TestAgentCapabilities(t *testing.T) {
	tests := []struct {
		agent Agent
		want  Capabilities
	}{
		{AgentDroid, Capabilities{SupportsPlanMode: true, SupportsSubagents: true}},
		{AgentOpenCode, Capabilities{SupportsPlanMode: true, SupportsSubagents: true}},
		{AgentCursor, Capabilities{SupportsPlanMode: true, SupportsSubagents: false}},
		{AgentCline, Capabilities{SupportsPlanMode: true, SupportsSubagents: false}},
		{AgentGrok, Capabilities{SupportsPlanMode: true, SupportsSubagents: false}},
		{AgentDevin, Capabilities{SupportsPlanMode: true, SupportsSubagents: false}},
		{"unknown", Capabilities{}},
	}
	for _, tc := range tests {
		if got := AgentCapabilities(tc.agent); got != tc.want {
			t.Errorf("AgentCapabilities(%s) = %+v, expected %+v", tc.agent, got, tc.want)
		}
	}
}
