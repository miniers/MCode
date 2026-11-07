package mcodeui

import "testing"

func TestPrettyModelID(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{"magpie-jp/auto/claude-opus-5-5", "Claude Opus 5.5"},
		{"claude-haiku-5-5", "Claude Haiku 5.5"},
		{"deepseek-v4.1-flash", "Deepseek V4.1 Flash"},
		{"sonnet", "Sonnet"},
		{"gpt-5-codex", "GPT 5 Codex"},
	}
	for _, tc := range cases {
		if got := prettyModelID(tc.id); got != tc.want {
			t.Errorf("prettyModelID(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestModelPinSummaryStartsWithAgentName(t *testing.T) {
	providers := []ProviderInfo{{ID: "claude", Name: "Claude"}}
	pin := ModelPin{Provider: "claude", ModelID: "claude-haiku-5-5"}

	if got, want := modelPinSummary(providers, pin, ModelPreference{}), "Claude"; got != want {
		t.Fatalf("无偏好时应只显示 agent 名，got %q want %q", got, want)
	}

	effort := "high"
	pref := ModelPreference{Effort: &effort}
	if got, want := modelPinSummary(providers, pin, pref), "Claude · 推理强度 高"; got != want {
		t.Fatalf("有推理强度时副标题不对，got %q want %q", got, want)
	}
}
