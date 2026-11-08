package textgen

import (
	"context"
	"reflect"
	"testing"
)

func TestRegisteredGeneratorDrivesCapabilities(t *testing.T) {
	var gotModel, gotEffort, gotPrompt string
	RegisterGenerator(" Mcode ", func(ctx context.Context, model, effort, prompt string) (string, error) {
		gotModel, gotEffort, gotPrompt = model, effort, prompt
		return "ok", nil
	})
	t.Cleanup(func() { RegisterGenerator("mcode", nil) })

	if !SupportsProvider("mcode") {
		t.Fatalf("registered provider should be supported")
	}
	if !AvailableProvider("MCODE") {
		t.Fatalf("registered provider should be available without PATH lookup")
	}
	if got := ReviewProviderOrder("", ""); !reflect.DeepEqual(got, []string{"claude", "codex", "mcode"}) {
		t.Fatalf("ReviewProviderOrder = %v, want [claude codex mcode]", got)
	}
	if got := ReviewProviderOrder("mcode", ""); !reflect.DeepEqual(got, []string{"mcode", "claude", "codex"}) {
		t.Fatalf("explicit registered provider = %v", got)
	}

	out, err := GenerateWithOptions(context.Background(), Options{Provider: "mcode", Model: "magpie-mcode/claude-sonnet", Effort: "high"}, "提示")
	if err != nil || out != "ok" {
		t.Fatalf("GenerateWithOptions = %q, %v", out, err)
	}
	if gotModel != "magpie-mcode/claude-sonnet" || gotEffort != "high" || gotPrompt != "提示" {
		t.Fatalf("注册函数参数 = %q %q %q", gotModel, gotEffort, gotPrompt)
	}
}

func TestRegisteredProvidersSortedAfterBuiltins(t *testing.T) {
	RegisterGenerator("zeta", func(context.Context, string, string, string) (string, error) { return "z", nil })
	RegisterGenerator("alpha", func(context.Context, string, string, string) (string, error) { return "a", nil })
	t.Cleanup(func() {
		RegisterGenerator("zeta", nil)
		RegisterGenerator("alpha", nil)
	})

	want := []string{"claude", "codex", "alpha", "zeta"}
	if got := ReviewProviderOrder("", ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReviewProviderOrder = %v, want %v", got, want)
	}
}

func TestRegisteredPiIgnoresModelRequirement(t *testing.T) {
	RegisterGenerator("pi", func(context.Context, string, string, string) (string, error) { return "p", nil })
	t.Cleanup(func() { RegisterGenerator("pi", nil) })

	want := []string{"claude", "codex", "pi"}
	if got := ReviewProviderOrder("", ""); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReviewProviderOrder = %v, want %v", got, want)
	}
}

func TestRegisterGeneratorNilRestoresDefaults(t *testing.T) {
	RegisterGenerator("mcode", func(context.Context, string, string, string) (string, error) { return "ok", nil })
	RegisterGenerator("mcode", nil)

	if SupportsProvider("mcode") {
		t.Fatalf("unregistered provider should no longer be supported")
	}
	if AvailableProvider("mcode") {
		t.Fatalf("unregistered provider has no CLI and should be unavailable")
	}
	if got := ReviewProviderOrder("", ""); !reflect.DeepEqual(got, []string{"claude", "codex"}) {
		t.Fatalf("ReviewProviderOrder = %v, want [claude codex]", got)
	}
	if _, err := GenerateWithOptions(context.Background(), Options{Provider: "mcode"}, "提示"); err == nil {
		t.Fatalf("unregistered provider should fail with unsupported provider")
	}
}
