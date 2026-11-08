package remote

import (
	"testing"

	"mcode/internal/remote/wire"
)

func TestMcodeModelCatalogHookAndFallback(t *testing.T) {
	SetMcodeModels(nil)
	t.Cleanup(func() { SetMcodeModels(nil) })
	fallback := ProviderModels("mcode")
	if len(fallback) != 1 || fallback[0].ID != "default" {
		t.Fatalf("fallback = %#v", fallback)
	}
	want := []wire.ProviderModel{{ID: "gateway/model", Name: "Model", IsDefault: true}}
	SetMcodeModels(func() []wire.ProviderModel { return want })
	got := ProviderModels("mcode")
	if len(got) != 1 || got[0].ID != want[0].ID {
		t.Fatalf("hook models = %#v", got)
	}
}
