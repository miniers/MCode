package quota

import "testing"

func TestModelGroup(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"claude-3-5-sonnet-20241022", GroupClaudeGPT},
		{"Claude-Opus", GroupClaudeGPT},
		{"gpt-4o", GroupClaudeGPT},
		{"GPT-4-turbo", GroupClaudeGPT},
		{"gemini-1.5-pro", GroupGemini},
		{"gemini-3-flash", GroupGemini},
		{"custom-code-model", GroupGemini},
	}

	for _, tt := range tests {
		got := ModelGroup(tt.model)
		if got != tt.want {
			t.Errorf("ModelGroup(%q) = %q, want %q", tt.model, got, tt.want)
		}
	}
}
