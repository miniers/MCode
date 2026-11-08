package tools

import "testing"

func toolIndex(toolset []Tool, name string) int {
	for i, tool := range toolset {
		if tool.Name == name {
			return i
		}
	}
	return -1
}

func TestCodingToolsForModel(t *testing.T) {
	editIndex := toolIndex(CodingTools(), "edit")
	for _, tc := range []struct {
		model     string
		wantApply bool
	}{
		{"gpt-5.1", true},
		{"openai/gpt-5-codex", true},
		{"o4-mini", true},
		{"claude-sonnet-4-5", false},
		{"deepseek-v4", false},
	} {
		toolset := CodingToolsFor(tc.model)
		for _, name := range []string{"web_fetch", "bash_output", "kill_bash"} {
			if toolIndex(toolset, name) < 0 {
				t.Fatalf("%s: missing %s", tc.model, name)
			}
		}
		if got := toolIndex(toolset, "apply_patch"); (got >= 0) != tc.wantApply {
			t.Fatalf("%s: apply_patch index = %d", tc.model, got)
		}
		if got := toolIndex(toolset, "edit"); (got >= 0) == tc.wantApply {
			t.Fatalf("%s: edit index = %d", tc.model, got)
		}
		if tc.wantApply && toolIndex(toolset, "apply_patch") != editIndex {
			t.Fatalf("%s: apply_patch should replace edit in place", tc.model)
		}
	}
}
