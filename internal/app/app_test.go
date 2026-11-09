package app

import (
	"testing"

	"mcode/internal/config"
)

func TestRemoteDraftExcludesMcode(t *testing.T) {
	cfg := config.Defaults()
	cfg.Models.Provider = config.ProviderMcode
	cfg.Models.ProviderSelected = true
	if got := remoteDraftDefaults(cfg).Provider; got == config.ProviderMcode {
		t.Fatalf("远程草稿使用了 %q", got)
	}
	if draftProviderEnabled(map[string]bool{config.ProviderMcode: true}, config.ProviderMcode) {
		t.Fatal("远程草稿不应启用 mcode")
	}
}

func TestExtraHarnessBuilderDoesNotBuildMcode(t *testing.T) {
	if _, ok := buildProviderHarness(config.Defaults(), config.NewMemorySecretStore(), config.ProviderMcode, providerMCP{}); ok {
		t.Fatal("额外来源构造器不应构造 mcode")
	}
}
