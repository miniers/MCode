package apisource

import (
	"reflect"
	"testing"

	"mcode/internal/config"
	"mcode/internal/magpieinject"
)

func TestMagpieTargets(t *testing.T) {
	cfg := config.Defaults()
	cfg.Providers.Custom = []config.CustomEndpointConfig{
		{ID: "off", Name: "Magpie", Enabled: false, Magpie: true},
		{ID: "plain", Name: "Magpie", Enabled: true, BaseURL: "http://plain.example/v1"},
		{ID: "on", Name: "Magpie", Enabled: true, Magpie: true, BaseURL: "http://on.example/v1/"},
	}
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(config.CustomEndpointSecretAccount("on"), "  gateway-key  "); err != nil {
		t.Fatal(err)
	}
	want := []magpieinject.Target{{Endpoint: "on", BaseURL: "http://on.example/v1", APIKey: "gateway-key", ProviderID: "magpie-mcode"}}
	if got := MagpieTargets(cfg, secrets); !reflect.DeepEqual(got, want) {
		t.Fatalf("MagpieTargets = %#v，想要 %#v", got, want)
	}
}
