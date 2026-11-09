package apisource

import (
	"reflect"
	"testing"

	"mcode/internal/config"
)

// stubEnv 替换包级 lookupEnv，把环境变量固定为给定的表。
func stubEnv(t *testing.T, env map[string]string) {
	t.Helper()
	prev := lookupEnv
	lookupEnv = func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}
	t.Cleanup(func() { lookupEnv = prev })
}

// findSource 按 Kind 查找来源。
func findSource(sources []Source, kind string) (Source, bool) {
	for _, src := range sources {
		if src.Kind == kind {
			return src, true
		}
	}
	return Source{}, false
}

// sourceKinds 返回来源列表的 Kind 顺序。
func sourceKinds(sources []Source) []string {
	kinds := make([]string, 0, len(sources))
	for _, src := range sources {
		kinds = append(kinds, src.Kind)
	}
	return kinds
}

func TestKindsIncludesDisabledAndKeylessSources(t *testing.T) {
	stubEnv(t, nil)
	cfg := config.Config{Providers: config.ProvidersConfig{Custom: []config.CustomEndpointConfig{
		{ID: "magpie", Magpie: true},
		{ID: "other"},
		{ID: "magpie2", Magpie: true},
	}}}
	want := []string{"magpie", "magpie2", "anthropic", "openai", "deepseek", "zai", "meta", "custom-other"}
	if got := Kinds(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("Kinds = %v, want %v", got, want)
	}
}

func TestResolvePresets(t *testing.T) {
	tests := []struct {
		name        string
		id          string
		pc          config.PresetProviderConfig
		wantBaseURL string
		wantModel   string
		wantModels  []string
	}{
		{
			name:        "deepseek 默认",
			id:          "deepseek",
			pc:          config.PresetProviderConfig{Enabled: true},
			wantBaseURL: "https://api.deepseek.com",
			wantModel:   "deepseek-v4-pro",
			wantModels:  []string{"deepseek-v4-pro", "deepseek-flash", "deepseek-chat", "deepseek-reasoner"},
		},
		{
			name:        "zai coding plan 地址",
			id:          "zai",
			pc:          config.PresetProviderConfig{Enabled: true, CodingPlan: true},
			wantBaseURL: "https://api.z.ai/api/coding/paas/v4",
			wantModel:   "glm-5.3",
			wantModels:  []string{"glm-5.3", "glm-5.3-flash"},
		},
		{
			name:        "zai 覆盖地址优先于 coding plan",
			id:          "zai",
			pc:          config.PresetProviderConfig{Enabled: true, CodingPlan: true, BaseURL: "https://example.com/v1"},
			wantBaseURL: "https://example.com/v1",
			wantModel:   "glm-5.3",
			wantModels:  []string{"glm-5.3", "glm-5.3-flash"},
		},
		{
			name:        "meta 覆盖模型",
			id:          "meta",
			pc:          config.PresetProviderConfig{Enabled: true, Model: "muse-spark-custom"},
			wantBaseURL: "https://api.meta.ai/v1",
			wantModel:   "muse-spark-custom",
			wantModels:  []string{"muse-spark-1.3", "muse-spark-1.3-contributor", "muse-spark-1.2", "muse-spark-1.2-contributor", "muse-spark-1.1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubEnv(t, nil)
			cfg := config.Config{Providers: config.ProvidersConfig{
				Presets: map[string]config.PresetProviderConfig{tt.id: tt.pc},
			}}
			src, ok := findSource(Resolve(cfg, nil), tt.id)
			if !ok {
				t.Fatalf("来源 %q 未启用", tt.id)
			}
			if src.Protocol != ProtocolOpenAI {
				t.Errorf("Protocol = %q, want %q", src.Protocol, ProtocolOpenAI)
			}
			if src.BaseURL != tt.wantBaseURL {
				t.Errorf("BaseURL = %q, want %q", src.BaseURL, tt.wantBaseURL)
			}
			if src.DefaultModel != tt.wantModel {
				t.Errorf("DefaultModel = %q, want %q", src.DefaultModel, tt.wantModel)
			}
			if !reflect.DeepEqual(src.Models, tt.wantModels) {
				t.Errorf("Models = %v, want %v", src.Models, tt.wantModels)
			}
			if len(src.APIKeyAccounts) != 1 || src.APIKeyAccounts[0] == "" {
				t.Errorf("APIKeyAccounts = %v, want 一个非空账号", src.APIKeyAccounts)
			}
			if src.APIKeyEnv == "" {
				t.Error("APIKeyEnv 为空，预设应带环境变量兜底")
			}
			if src.Magpie {
				t.Error("预设不应标记为 Magpie")
			}
		})
	}
}

func TestResolveSkipsDisabledPresets(t *testing.T) {
	stubEnv(t, nil)
	cfg := config.Config{Providers: config.ProvidersConfig{
		Presets: map[string]config.PresetProviderConfig{
			"deepseek": {Enabled: false},
			"zai":      {Enabled: true},
		},
	}}
	got := sourceKinds(Resolve(cfg, nil))
	if want := []string{"zai"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestResolveOpenAI(t *testing.T) {
	tests := []struct {
		name        string
		cfg         config.OpenAIConfig
		wantBaseURL string
		wantModel   string
	}{
		{
			name:        "默认地址",
			cfg:         config.OpenAIConfig{Enabled: true, Model: "gpt-5"},
			wantBaseURL: "https://api.openai.com/v1",
			wantModel:   "gpt-5",
		},
		{
			name:        "配置地址去掉末尾斜杠",
			cfg:         config.OpenAIConfig{Enabled: true, BaseURL: "https://proxy.example/v1/", Model: "gpt-custom"},
			wantBaseURL: "https://proxy.example/v1",
			wantModel:   "gpt-custom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stubEnv(t, map[string]string{"OPENAI_API_KEY": "env-key"})
			cfg := config.Config{Providers: config.ProvidersConfig{OpenAI: tt.cfg}}
			src, ok := findSource(Resolve(cfg, config.NewMemorySecretStore()), "openai")
			if !ok {
				t.Fatal("openai 未启用")
			}
			if src.Label != "OpenAI" || src.Protocol != ProtocolOpenAI {
				t.Errorf("Label/Protocol = %q/%q", src.Label, src.Protocol)
			}
			if src.BaseURL != tt.wantBaseURL {
				t.Errorf("BaseURL = %q, want %q", src.BaseURL, tt.wantBaseURL)
			}
			if src.DefaultModel != tt.wantModel {
				t.Errorf("DefaultModel = %q, want %q", src.DefaultModel, tt.wantModel)
			}
			if got := src.APIKey(config.NewMemorySecretStore()); got != "env-key" {
				t.Errorf("环境变量兜底 APIKey = %q, want env-key", got)
			}
		})
	}
}

func TestResolveOpenAIKeyringWinsOverEnv(t *testing.T) {
	stubEnv(t, map[string]string{"OPENAI_API_KEY": "env-key"})
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set("openai.apiKey", "ring-key"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Providers: config.ProvidersConfig{OpenAI: config.OpenAIConfig{Enabled: true}}}
	src, ok := findSource(Resolve(cfg, secrets), "openai")
	if !ok {
		t.Fatal("openai 未启用")
	}
	if got := src.APIKey(secrets); got != "ring-key" {
		t.Errorf("APIKey = %q, want ring-key", got)
	}
}

func TestResolveSkipsDisabledOpenAI(t *testing.T) {
	stubEnv(t, map[string]string{"OPENAI_API_KEY": "env-key"})
	sources := Resolve(config.Config{}, config.NewMemorySecretStore())
	if _, ok := findSource(sources, "openai"); ok {
		t.Fatal("未启用的 openai 不应出现在来源列表里")
	}
}

func TestResolveAnthropic(t *testing.T) {
	stubEnv(t, nil)
	if sources := Resolve(config.Config{}, config.NewMemorySecretStore()); len(sources) != 0 {
		t.Fatalf("没有凭据时不应有来源，got %v", sourceKinds(sources))
	}

	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(anthropicLegacyAPIKeyAccount, " legacy-key "); err != nil {
		t.Fatal(err)
	}
	src, ok := findSource(Resolve(config.Config{}, secrets), "anthropic")
	if !ok {
		t.Fatal("只有旧账号时 anthropic 仍应启用")
	}
	if src.Protocol != ProtocolAnthropic || src.Label != "Anthropic" {
		t.Errorf("Protocol/Label = %q/%q", src.Protocol, src.Label)
	}
	if src.BaseURL != "https://api.anthropic.com" {
		t.Errorf("BaseURL = %q, want https://api.anthropic.com", src.BaseURL)
	}
	if src.DefaultModel != "claude-opus-5-5" {
		t.Errorf("DefaultModel = %q, want claude-opus-5-5", src.DefaultModel)
	}
	wantModels := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"}
	if !reflect.DeepEqual(src.Models, wantModels) {
		t.Errorf("Models = %v, want %v", src.Models, wantModels)
	}
	if got := src.APIKey(secrets); got != "legacy-key" {
		t.Errorf("APIKey = %q, want legacy-key", got)
	}

	if err := secrets.Set(anthropicAPIKeyAccount, "new-key"); err != nil {
		t.Fatal(err)
	}
	if got := src.APIKey(secrets); got != "new-key" {
		t.Errorf("新旧账号都有时 APIKey = %q, want new-key", got)
	}
}

func TestResolveAnthropicEnv(t *testing.T) {
	stubEnv(t, map[string]string{
		"ANTHROPIC_API_KEY":  "env-key",
		"ANTHROPIC_BASE_URL": " https://anthropic.example/ ",
	})
	src, ok := findSource(Resolve(config.Config{}, config.NewMemorySecretStore()), "anthropic")
	if !ok {
		t.Fatal("环境变量有凭据时 anthropic 应启用")
	}
	if got := src.APIKey(nil); got != "env-key" {
		t.Errorf("APIKey = %q, want env-key", got)
	}
	if src.BaseURL != "https://anthropic.example" {
		t.Errorf("BaseURL = %q, want https://anthropic.example", src.BaseURL)
	}
}

func TestResolveAnthropicDisabled(t *testing.T) {
	stubEnv(t, nil)
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(anthropicAPIKeyAccount, "key"); err != nil {
		t.Fatal(err)
	}
	off := false
	cfg := config.Config{Providers: config.ProvidersConfig{Anthropic: config.AnthropicConfig{Enabled: &off}}}
	if _, ok := findSource(Resolve(cfg, secrets), "anthropic"); ok {
		t.Fatal("配置关闭后不应返回 Anthropic 来源")
	}
	cfg.Providers.Anthropic.Enabled = nil
	if _, ok := findSource(Resolve(cfg, secrets), "anthropic"); !ok {
		t.Fatal("Enabled 为 nil 时应启用 Anthropic 来源")
	}
}

func TestResolveCustomEndpoints(t *testing.T) {
	stubEnv(t, map[string]string{"OPENAI_API_KEY": "env-key"})
	secrets := config.NewMemorySecretStore()
	cfg := config.Config{Providers: config.ProvidersConfig{
		Custom: []config.CustomEndpointConfig{
			{ID: "magpie", Name: "Magpie", BaseURL: "http://127.0.0.1:3425/v1/", Enabled: true, Magpie: true},
			{ID: "alpha", Name: "Alpha", BaseURL: "https://alpha.example/v1/", Model: "alpha-model", Enabled: true},
			{ID: "magpie2", Name: "Magpie 2", BaseURL: "http://127.0.0.1:3426/v1", Enabled: true, Magpie: true},
			{ID: "off", Name: "Off", BaseURL: "https://off.example/v1"},
		},
	}}
	sources := Resolve(cfg, secrets)

	if want := []string{"magpie", "magpie2", "custom-alpha"}; !reflect.DeepEqual(sourceKinds(sources), want) {
		t.Fatalf("kinds = %v, want %v", sourceKinds(sources), want)
	}

	alpha, _ := findSource(sources, "custom-alpha")
	if alpha.Label != "Alpha" || alpha.BaseURL != "https://alpha.example/v1" || alpha.DefaultModel != "alpha-model" {
		t.Errorf("自定义端点字段 = %q/%q/%q", alpha.Label, alpha.BaseURL, alpha.DefaultModel)
	}
	if alpha.Magpie {
		t.Error("普通自定义端点不应标记为 Magpie")
	}
	if len(alpha.APIKeyAccounts) != 1 || alpha.APIKeyAccounts[0] != config.CustomEndpointSecretAccount("alpha") {
		t.Errorf("APIKeyAccounts = %v", alpha.APIKeyAccounts)
	}
	if alpha.APIKeyEnv != "" {
		t.Errorf("APIKeyEnv = %q, want 空", alpha.APIKeyEnv)
	}
	// 即使环境里有 OPENAI_API_KEY，自定义端点也不能拿它当凭据。
	if got := alpha.APIKey(secrets); got != "" {
		t.Errorf("自定义端点 APIKey = %q, want 空", got)
	}

	gateway, _ := findSource(sources, "magpie")
	if !gateway.Magpie || gateway.BaseURL != "http://127.0.0.1:3425/v1" {
		t.Errorf("Magpie 端点 Magpie/BaseURL = %v/%q", gateway.Magpie, gateway.BaseURL)
	}
	if gateway.APIKeyEnv != "" {
		t.Errorf("Magpie APIKeyEnv = %q, want 空", gateway.APIKeyEnv)
	}
	if got := gateway.APIKey(secrets); got != "" {
		t.Errorf("Magpie 端点 APIKey = %q, want 空", got)
	}

	if _, ok := findSource(sources, "custom-off"); ok {
		t.Error("未启用的自定义端点不应出现")
	}
}

func TestResolveOrder(t *testing.T) {
	stubEnv(t, nil)
	secrets := config.NewMemorySecretStore()
	if err := secrets.Set(anthropicAPIKeyAccount, "key"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Providers: config.ProvidersConfig{
		OpenAI: config.OpenAIConfig{Enabled: true, Model: "gpt-5"},
		Presets: map[string]config.PresetProviderConfig{
			"deepseek": {Enabled: true},
			"zai":      {Enabled: true},
			"meta":     {Enabled: true},
		},
		Custom: []config.CustomEndpointConfig{
			{ID: "magpie", Name: "Magpie", BaseURL: "http://127.0.0.1:3425/v1", Enabled: true, Magpie: true},
			{ID: "a", Name: "A", BaseURL: "https://a.example/v1", Enabled: true},
			{ID: "magpie2", Name: "Magpie 2", BaseURL: "http://127.0.0.1:3426/v1", Enabled: true, Magpie: true},
			{ID: "b", Name: "B", BaseURL: "https://b.example/v1", Enabled: true},
		},
	}}
	want := []string{"magpie", "magpie2", "anthropic", "openai", "deepseek", "zai", "meta", "custom-a", "custom-b"}
	if got := sourceKinds(Resolve(cfg, secrets)); !reflect.DeepEqual(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestSourceAPIKeyReadsLatest(t *testing.T) {
	stubEnv(t, nil)
	secrets := config.NewMemorySecretStore()
	src := Source{APIKeyAccounts: []string{"a", "b"}}

	if got := src.APIKey(secrets); got != "" {
		t.Fatalf("空存储 APIKey = %q, want 空", got)
	}
	if err := secrets.Set("b", "  fallback  "); err != nil {
		t.Fatal(err)
	}
	if got := src.APIKey(secrets); got != "fallback" {
		t.Errorf("APIKey = %q, want fallback", got)
	}
	if err := secrets.Set("a", "primary"); err != nil {
		t.Fatal(err)
	}
	if got := src.APIKey(secrets); got != "primary" {
		t.Errorf("APIKey = %q, want primary", got)
	}
	if err := secrets.Set("a", "   "); err != nil {
		t.Fatal(err)
	}
	if got := src.APIKey(secrets); got != "fallback" {
		t.Errorf("空白账号后 APIKey = %q, want fallback", got)
	}
}
