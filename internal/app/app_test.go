package app

import (
	"testing"

	"mcode/internal/config"
)

func TestCloseCancelsUpdateChecker(t *testing.T) {
	app, err := Open(Options{
		DataDir:  t.TempDir(),
		Provider: "memory",
		WorkDir:  t.TempDir(),
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	cancel := app.updateCancel
	if cancel == nil {
		defer app.Close()
		t.Fatal("update checker cancel function is nil")
	}
	called := false
	app.updateCancel = func() {
		called = true
		cancel()
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if !called {
		t.Fatal("Close did not cancel the update checker")
	}
}

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
