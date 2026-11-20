package app

import (
	"testing"

	"mcode/internal/config"
	"mcode/internal/harness"
)

func TestApplyResumeTemplatesUsesConfig(t *testing.T) {
	var cfg config.Config
	cfg.Providers.ACP = map[string]config.ProviderConfig{
		"acme": {Resume: "--resume sessionid"},
	}
	applyResumeTemplates(cfg)
	t.Cleanup(func() {
		harness.RegisterResumeTemplate("acme", "")
	})
	if got := harness.FormatResumeCommand("acme", "acme", "abc"); got != "acme --resume abc" {
		t.Fatalf("acme = %q", got)
	}
	if got := harness.FormatResumeCommand("cursor", "cursor-agent", "chat-1"); got != "cursor-agent --resume chat-1" {
		t.Fatalf("cursor = %q", got)
	}
	if got := harness.FormatResumeCommand("copilot", "copilot", "sess"); got != "" {
		t.Fatalf("copilot = %q", got)
	}
}
