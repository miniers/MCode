package apisource

import (
	"testing"

	"mcode/internal/config"
)

func TestNormalizeBeeModel(t *testing.T) {
	stubEnv(t, nil)
	cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{ID: "jp", Name: "JP", Enabled: true, Magpie: true},
		{ID: "magpie", Name: "Old", Enabled: true, Magpie: true},
	}}}
	secrets := config.NewMemorySecretStore()
	_ = secrets.Set("anthropic.apiKey", "key")
	targets := MagpieTargets(cfg, secrets)
	cases := []struct {
		provider, model, wantProvider, wantModel string
		changed                                  bool
	}{
		{"mcode", targets[0].ProviderID + "/auto/model", "custom-jp", "auto/model", true},
		{"mcode", targets[1].ProviderID + "/model", "magpie", "model", true},
		{"mcode", "anthropic/opus", "anthropic", "opus", true},
		{"mcode", "unknown/model", "mcode", "unknown/model", false},
		{"pi", targets[0].ProviderID + "/model", "pi", targets[0].ProviderID + "/model", false},
		{"", "plain", "", "plain", false},
	}
	for _, tc := range cases {
		p, m, changed := NormalizeBeeModel(cfg, secrets, tc.provider, tc.model)
		if p != tc.wantProvider || m != tc.wantModel || changed != tc.changed {
			t.Errorf("NormalizeBeeModel(%q, %q) = %q, %q, %v", tc.provider, tc.model, p, m, changed)
		}
	}
}
