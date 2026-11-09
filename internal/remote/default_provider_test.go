package remote

import (
	"testing"

	"mcode/internal/config"
	"mcode/internal/harness"
)

func TestRegisterHarnessDefaultProviderSkipsMcode(t *testing.T) {
	tests := []struct {
		name  string
		kinds []string
		want  string
	}{
		{"仅 mcode", []string{config.ProviderMcode}, ""},
		{"先注册 deepseek", []string{"deepseek", config.ProviderMcode}, "deepseek"},
		{"先注册 mcode", []string{config.ProviderMcode, "custom-x"}, "custom-x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &MemoryBackend{}
			for _, kind := range tt.kinds {
				b.RegisterHarness(unavailableCustomHarness{kind: harness.Kind(kind)})
			}
			if b.defaultProvider != tt.want {
				t.Fatalf("默认来源 = %q，期望 %q", b.defaultProvider, tt.want)
			}
		})
	}
}

func TestUnregisterHarnessDefaultProviderSkipsMcode(t *testing.T) {
	b := &MemoryBackend{}
	b.RegisterHarness(unavailableCustomHarness{kind: harness.Kind(config.ProviderMcode)})
	b.RegisterHarness(unavailableCustomHarness{kind: harness.Kind("custom-x")})
	b.UnregisterHarness("custom-x")
	if b.defaultProvider != "" {
		t.Fatalf("默认来源 = %q，期望为空", b.defaultProvider)
	}
}
