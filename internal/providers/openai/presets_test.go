package openai

import (
	"testing"

	"mcode/internal/config"
	"mcode/internal/harness"
)

func TestPresetByID(t *testing.T) {
	cases := []struct {
		id          string
		wantKind    harness.Kind
		wantAccount string
	}{
		{"deepseek", harness.Kind("deepseek"), "deepseek.apiKey"},
		{"zai", harness.Kind("zai"), "zai.apiKey"},
		{"meta", harness.Kind("meta"), "meta.apiKey"},
		{"magpie", harness.Kind("magpie"), "magpie1.apiKey"},
		{"magpie2", harness.Kind("magpie2"), "magpie2.apiKey"},
		{"magpie4", harness.Kind("magpie4"), "magpie4.apiKey"},
	}
	for _, tc := range cases {
		p, ok := PresetByID(tc.id)
		if !ok {
			t.Errorf("PresetByID(%q): expected found", tc.id)
			continue
		}
		if p.Kind != tc.wantKind {
			t.Errorf("PresetByID(%q): kind = %q, want %q", tc.id, p.Kind, tc.wantKind)
		}
		if p.APIKeyAccount != tc.wantAccount {
			t.Errorf("PresetByID(%q): account = %q, want %q", tc.id, p.APIKeyAccount, tc.wantAccount)
		}
	}

	if _, ok := PresetByID("unknown"); ok {
		t.Error("PresetByID(unknown): expected not found")
	}
}

func TestMagpieSlotBounds(t *testing.T) {
	for slot := 1; slot <= 4; slot++ {
		p, ok := Magpie(slot)
		if !ok {
			t.Fatalf("Magpie(%d): expected found", slot)
		}
		if p.DefaultBaseURL != "http://127.0.0.1:3425/v1" {
			t.Errorf("Magpie(%d): base URL = %q", slot, p.DefaultBaseURL)
		}
		if p.Kind != harness.Kind(p.ID) {
			t.Errorf("Magpie(%d): kind = %q, id = %q", slot, p.Kind, p.ID)
		}
	}
	for _, slot := range []int{0, 5, -1} {
		if _, ok := Magpie(slot); ok {
			t.Errorf("Magpie(%d): expected out of range", slot)
		}
	}
}

func TestPresetResolveBaseURL(t *testing.T) {
	// override 优先
	if got := DeepSeek.ResolveBaseURL("  https://proxy.example/v1  ", false); got != "https://proxy.example/v1" {
		t.Errorf("override: got %q", got)
	}
	// 空白 override 退回默认地址
	if got := DeepSeek.ResolveBaseURL("   ", false); got != DeepSeek.DefaultBaseURL {
		t.Errorf("blank override: got %q, want %q", got, DeepSeek.DefaultBaseURL)
	}
	// coding plan 地址
	zai, _ := PresetByID("zai")
	if got := zai.ResolveBaseURL("", true); got != zai.CodingPlanBaseURL {
		t.Errorf("coding plan: got %q, want %q", got, zai.CodingPlanBaseURL)
	}
	// 没有 coding plan 地址时退回默认
	if got := Meta.ResolveBaseURL("", true); got != Meta.DefaultBaseURL {
		t.Errorf("no coding plan: got %q, want %q", got, Meta.DefaultBaseURL)
	}
}

func TestPresetOptions(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-env-deepseek")
	secrets := config.NewMemorySecretStore()

	opts := DeepSeek.Options(secrets, "", false)
	if opts.Kind != harness.Kind("deepseek") || opts.DisplayName != "DeepSeek" {
		t.Errorf("kind/display mismatch: %+v", opts)
	}
	if opts.BaseURL != DeepSeek.DefaultBaseURL {
		t.Errorf("base URL = %q, want %q", opts.BaseURL, DeepSeek.DefaultBaseURL)
	}
	if opts.Model != "deepseek-v4-pro" {
		t.Errorf("model = %q", opts.Model)
	}
	if opts.APIKeyAccount != "deepseek.apiKey" {
		t.Errorf("account = %q", opts.APIKeyAccount)
	}
	if opts.Secrets == nil {
		t.Error("secrets not passed through")
	}
	if opts.FallbackAPIKey != "sk-env-deepseek" {
		t.Errorf("fallback = %q", opts.FallbackAPIKey)
	}
	if len(opts.ExtraHeaders) != 0 {
		t.Errorf("unexpected extra headers: %+v", opts.ExtraHeaders)
	}

	// 显式 override 与 coding plan 生效
	zaiOpts := Zai.Options(secrets, "https://proxy.example/v1", true)
	if zaiOpts.BaseURL != "https://proxy.example/v1" {
		t.Errorf("zai override base URL = %q", zaiOpts.BaseURL)
	}
	if zaiOpts.Model != "glm-5.3" || zaiOpts.APIKeyAccount != "zai.apiKey" {
		t.Errorf("zai options mismatch: %+v", zaiOpts)
	}

	// Magpie 无默认模型与 env 回退（第一个槽位除外）
	mp, _ := Magpie(1)
	if got := mp.Options(secrets, "", false); got.Model != "" || got.FallbackAPIKey != "" {
		t.Errorf("magpie1 options = %+v", got)
	}
	mp2, _ := Magpie(2)
	if got := mp2.Options(secrets, "", false); got.APIKeyAccount != "magpie2.apiKey" {
		t.Errorf("magpie2 account = %q", got.APIKeyAccount)
	}
}

func TestDeepSeekModels(t *testing.T) {
	has := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"deepseek-chat", "deepseek-reasoner", "deepseek-v4-pro", "deepseek-flash"} {
		if !has(DeepSeek.Models, want) {
			t.Errorf("DeepSeek.Models missing %q", want)
		}
	}
}
