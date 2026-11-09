package app

import (
	"testing"

	"mcode/internal/config"
	"mcode/internal/swarm"
)

func TestDefaultBeeSource(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("META_API_KEY", "")
	secrets := config.NewMemorySecretStore()
	cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{ID: "jp", Name: "JP", Enabled: true, Magpie: true, Model: "auto/model"},
	}}}
	_ = secrets.Set(config.CustomEndpointSecretAccount("jp"), "key")
	for _, lead := range []swarm.ThreadContext{{}, {Provider: "pi"}, {Provider: "custom-jp", Model: "auto/other"}} {
		p, m, err := defaultBeeSource(cfg, secrets, lead)
		want := "auto/model"
		if lead.Provider == "custom-jp" {
			want = "auto/other"
		}
		if err != nil || p != "custom-jp" || m != want {
			t.Errorf("defaultBeeSource(%+v) = %q, %q, %v", lead, p, m, err)
		}
	}
	if _, _, err := defaultBeeSource(config.Config{}, config.NewMemorySecretStore(), swarm.ThreadContext{}); err == nil {
		t.Fatal("没有可用来源时应返回错误")
	}
}
