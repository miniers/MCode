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

func TestDefaultBeeSourceKeylessAndLeadFallback(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "DEEPSEEK_API_KEY", "ZAI_API_KEY", "META_API_KEY"} {
		t.Setenv(name, "")
	}
	local := config.CustomEndpointConfig{ID: "local", Enabled: true, Model: "local-model"}
	magpie := config.CustomEndpointConfig{ID: "jp", Enabled: true, Magpie: true, Model: "auto/model"}
	tests := []struct {
		name      string
		endpoints []config.CustomEndpointConfig
		lead      swarm.ThreadContext
		provider  string
		model     string
		wantError bool
	}{
		{name: "无 Key 本地端点", endpoints: []config.CustomEndpointConfig{local}, provider: "custom-local", model: "local-model"},
		{name: "无 Key Magpie", endpoints: []config.CustomEndpointConfig{magpie}, wantError: true},
		{name: "继承可用来源和模型", endpoints: []config.CustomEndpointConfig{local}, lead: swarm.ThreadContext{Provider: "custom-local", Model: "lead-model"}, provider: "custom-local", model: "lead-model"},
		{name: "不可用来源回退", endpoints: []config.CustomEndpointConfig{magpie, local}, lead: swarm.ThreadContext{Provider: "custom-jp", Model: "lead-model"}, provider: "custom-local", model: "local-model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Providers: config.ProvidersConfig{Custom: tt.endpoints}}
			provider, model, err := defaultBeeSource(cfg, config.NewMemorySecretStore(), tt.lead)
			if (err != nil) != tt.wantError || provider != tt.provider || model != tt.model {
				t.Fatalf("defaultBeeSource = %q, %q, %v", provider, model, err)
			}
		})
	}
}
