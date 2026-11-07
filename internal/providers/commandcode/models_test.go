package commandcode

import "testing"

func TestParseModelsConfiguredFirst(t *testing.T) {
	text := "Available models:\n\nAnthropic\n  claude-sonnet-5  best combo (recommended)\n  claude-opus-5-5  newest Opus\n\nOpen Source\n  moonshotai/kimi-k3  long-horizon coding\n  plain  ignored\n"
	got := ParseModels(text, "kimi-k3", map[string]string{"claude-opus-5-5": "high"})
	if len(got) != 3 {
		t.Fatalf("want 3 models, got %d: %+v", len(got), got)
	}
	if got[0].ID != "moonshotai/kimi-k3" || !got[0].Default || got[0].Detail != "Open Source · long-horizon coding" {
		t.Fatalf("configured model should lead as the only default: %+v", got[0])
	}
	if got[1].Default || got[1].Detail != "Anthropic · best combo" {
		t.Fatalf("recommended tag should be stripped and not default: %+v", got[1])
	}
	if got[2].DefaultEffort != "high" || len(got[2].Efforts) == 0 {
		t.Fatalf("configured effort and effort table should apply: %+v", got[2])
	}
}

func TestParseModelsRecommendedDefault(t *testing.T) {
	got := ParseModels("Anthropic\n  claude-opus-5  a\n  claude-sonnet-5  b (recommended)\n", "", nil)
	if len(got) != 2 || got[0].Default || !got[1].Default {
		t.Fatalf("recommended row should be the default: %+v", got)
	}
}

func TestDisplayName(t *testing.T) {
	for id, want := range map[string]string{
		"claude-fable-5-1":          "Claude Fable 5.1",
		"moonshotai/kimi-k2.7-code": "Kimi K2.7 Code",
		"gpt-5.6-terra":             "GPT-5.6 Terra",
		"deepseek/deepseek-v4-pro":  "Deepseek V4 Pro",
		"minimax/minimax-m3:free":   "Minimax M3 Free",
	} {
		if got := DisplayName(id); got != want {
			t.Errorf("DisplayName(%q) = %q, want %q", id, got, want)
		}
	}
}
