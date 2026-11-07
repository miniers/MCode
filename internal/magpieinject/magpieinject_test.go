package magpieinject

import (
	"reflect"
	"testing"

	"mcode/internal/config"
	"mcode/internal/providers/magpie"
)

// testConfig 构造一份只含指定自定义端点的配置。
func testConfig(endpoints ...config.CustomEndpointConfig) config.Config {
	cfg := config.Defaults()
	cfg.Providers.Custom = endpoints
	return cfg
}

// testSecrets 返回带指定端点密钥的内存凭据存储。
func testSecrets(t *testing.T, keys map[string]string) config.SecretStore {
	t.Helper()
	secrets := config.NewMemorySecretStore()
	for endpointID, key := range keys {
		if err := secrets.Set(config.CustomEndpointSecretAccount(endpointID), key); err != nil {
			t.Fatalf("写入 %s 的密钥失败: %v", endpointID, err)
		}
	}
	return secrets
}

// targetsFixture 与 pi 的 piMagpieTargets 测试用同一份输入：未启用、非 Magpie、
// 同名端点、末尾斜杠与默认地址都在里面。
func targetsFixture() config.Config {
	return testConfig(
		config.CustomEndpointConfig{ID: "off", Name: "Magpie Off", Enabled: false, Magpie: true},
		config.CustomEndpointConfig{ID: "plain", Name: "Plain", BaseURL: "http://plain.example/v1", Enabled: true},
		config.CustomEndpointConfig{ID: "nokey", Name: "Magpie", Enabled: true, Magpie: true},
		config.CustomEndpointConfig{
			ID: "jp", Name: "Magpie JP", BaseURL: "http://jp.example/v1/", Enabled: true, Magpie: true,
			PiModels: []string{"cline/gpt-x"},
		},
		config.CustomEndpointConfig{
			ID: "jp2", Name: "Magpie JP", BaseURL: "http://jp2.example/v1", Enabled: true, Magpie: true,
		},
	)
}

// TestTargetsMatchPiExpectations 用写死的期望值锁定 piMagpieTargets 的行为：
// 只取启用的 Magpie 端点、同名端点依次加后缀、默认地址与末尾斜杠归一化、
// key 从 secret store 读且去空白。
func TestTargetsMatchPiExpectations(t *testing.T) {
	secrets := testSecrets(t, map[string]string{"jp": "  gw-key  "})

	got := Targets(targetsFixture(), secrets, nil)
	want := []Target{
		{Endpoint: "nokey", BaseURL: config.DefaultMagpieBaseURL, APIKey: "", ProviderID: "magpie-mcode"},
		{Endpoint: "jp", BaseURL: "http://jp.example/v1", APIKey: "gw-key", ProviderID: "magpie-jp"},
		{Endpoint: "jp2", BaseURL: "http://jp2.example/v1", APIKey: "", ProviderID: "magpie-jp-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Targets = %#v，想要 %#v", got, want)
	}
}

// TestTargetsSkipsDisabledAndNonMagpie 校验未启用或非 Magpie 的端点不出现，
// 且它们不占用 providerID 后缀。
func TestTargetsSkipsDisabledAndNonMagpie(t *testing.T) {
	cfg := testConfig(
		config.CustomEndpointConfig{ID: "off", Name: "Magpie", Enabled: false, Magpie: true},
		config.CustomEndpointConfig{ID: "plain", Name: "Magpie", BaseURL: "http://plain.example/v1", Enabled: true},
		config.CustomEndpointConfig{ID: "on", Name: "Magpie", BaseURL: "http://on.example/v1", Enabled: true, Magpie: true},
	)
	got := Targets(cfg, nil, nil)
	if len(got) != 1 {
		t.Fatalf("端点数 = %d，想要 1", len(got))
	}
	if got[0].Endpoint != "on" {
		t.Fatalf("端点 = %q，想要 on", got[0].Endpoint)
	}
	if got[0].ProviderID != "magpie-mcode" {
		t.Fatalf("ProviderID = %q，想要 magpie-mcode", got[0].ProviderID)
	}
	if got[0].HasKey() {
		t.Fatalf("没有 key 的端点 HasKey 应为 false")
	}
}

// TestTargetsTwoSameNamesGetDifferentProviderIDs 校验同名端点得到不同的 ProviderID。
func TestTargetsTwoSameNamesGetDifferentProviderIDs(t *testing.T) {
	got := Targets(targetsFixture(), nil, nil)
	if len(got) != 3 {
		t.Fatalf("端点数 = %d，想要 3", len(got))
	}
	if got[1].Endpoint != "jp" || got[2].Endpoint != "jp2" {
		t.Fatalf("端点顺序 = %q、%q，想要 jp、jp2", got[1].Endpoint, got[2].Endpoint)
	}
	if got[1].ProviderID != "magpie-jp" || got[2].ProviderID != "magpie-jp-2" {
		t.Fatalf("同名端点的 ProviderID = %q、%q，想要 magpie-jp、magpie-jp-2", got[1].ProviderID, got[2].ProviderID)
	}
}

// TestTargetsSelectionsKeepProviderID 校验只选第二个端点时，它的 ProviderID
// 与不过滤时相同——providerID 必须先对全部启用端点分配再过滤。
func TestTargetsSelectionsKeepProviderID(t *testing.T) {
	full := Targets(targetsFixture(), nil, nil)
	byEndpoint := make(map[string]Target, len(full))
	for _, target := range full {
		byEndpoint[target.Endpoint] = target
	}

	got := Targets(targetsFixture(), nil, []config.MagpieSelection{{EndpointID: "jp"}})
	if len(got) != 1 {
		t.Fatalf("端点数 = %d，想要 1", len(got))
	}
	if got[0].Endpoint != "jp" {
		t.Fatalf("端点 = %q，想要 jp", got[0].Endpoint)
	}
	if got[0].ProviderID != byEndpoint["jp"].ProviderID {
		t.Fatalf("过滤后 ProviderID = %q，不过滤时 = %q，应当相同", got[0].ProviderID, byEndpoint["jp"].ProviderID)
	}

	// 只选第三个端点（同名的第二个）时仍应拿到 -2 后缀。
	got = Targets(targetsFixture(), nil, []config.MagpieSelection{{EndpointID: "jp2"}})
	if len(got) != 1 || got[0].ProviderID != "magpie-jp-2" {
		t.Fatalf("只选 jp2 时 = %#v，想要 ProviderID magpie-jp-2", got)
	}
}

// TestTargetsEmptySelectionsReturnsAll 校验 nil 与空切片都返回全部端点。
func TestTargetsEmptySelectionsReturnsAll(t *testing.T) {
	for _, selections := range [][]config.MagpieSelection{nil, {}} {
		if got := Targets(targetsFixture(), nil, selections); len(got) != 3 {
			t.Fatalf("selections = %#v 时端点数 = %d，想要 3", selections, len(got))
		}
	}
}

// TestTargetsMissingSelectionEndpoint 校验选中的端点不存在时返回空列表。
func TestTargetsMissingSelectionEndpoint(t *testing.T) {
	got := Targets(targetsFixture(), nil, []config.MagpieSelection{{EndpointID: "gone", Models: []string{"m"}}})
	if len(got) != 0 {
		t.Fatalf("端点数 = %d，想要 0", len(got))
	}
}

func TestSelectedKeepsUnlistedAndOrder(t *testing.T) {
	fetched := []magpie.Model{
		{ID: "model-2", Name: "Model Two · 描述", ContextWindow: 100, MaxTokens: 10},
		{ID: "model-1", Name: "Model One", ContextWindow: 200, MaxTokens: 20},
	}
	got := Selected([]string{"model-2", "gone", "model-1"}, fetched)
	if len(got) != 3 {
		t.Fatalf("模型数 = %d，想要 3", len(got))
	}
	if got[0].ID != "model-2" || got[1].ID != "gone" || got[2].ID != "model-1" {
		t.Fatalf("模型顺序 = %q、%q、%q，想要 model-2、gone、model-1", got[0].ID, got[1].ID, got[2].ID)
	}
	if got[0].Name != "Model Two" {
		t.Errorf("model-2 的 Name = %q，想要 Model Two", got[0].Name)
	}
	if got[2].Name != "Model One" {
		t.Errorf("model-1 的 Name = %q，想要 Model One", got[2].Name)
	}

	// 网关未列出的模型用默认元数据补齐，ID 与 Name 仍然可用。
	fallback := got[1]
	if fallback.Reasoning != true || fallback.ContextWindow != 200000 || fallback.MaxTokens != 32000 {
		t.Errorf("兜底元数据 = %#v，想要与 DefaultModel 相同", fallback)
	}
	if !reflect.DeepEqual(fallback.Input, []string{"text", "image"}) {
		t.Errorf("兜底 Input = %#v，想要 [text image]", fallback.Input)
	}
	if fallback.Name != "gone" {
		t.Errorf("兜底 Name = %q，想要 gone", fallback.Name)
	}
}

// TestSelectedSkipsBlankIDs 校验空白 ID 被跳过。
func TestSelectedSkipsBlankIDs(t *testing.T) {
	got := Selected([]string{"", "  ", "a"}, nil)
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("Selected = %#v，想要只有 a", got)
	}
}

// TestSelectedFallbackDoesNotShareInput 校验默认元数据的 Input 是副本，
// 调用方修改一个模型的 Input 不会影响其它模型。
func TestSelectedFallbackDoesNotShareInput(t *testing.T) {
	got := Selected([]string{"a", "b"}, nil)
	got[0].Input[0] = "changed"
	if got[1].Input[0] != "text" {
		t.Fatalf("第二个模型的 Input = %#v，应保持 text", got[1].Input)
	}
}

func TestDefaultModel(t *testing.T) {
	want := magpie.Model{
		Reasoning:     true,
		Input:         []string{"text", "image"},
		ContextWindow: 200000,
		MaxTokens:     32000,
	}
	if got := DefaultModel(); !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultModel = %#v，想要 %#v", got, want)
	}
}

func TestProviderOf(t *testing.T) {
	targets := []Target{
		{Endpoint: "auto", BaseURL: "http://auto.example/v1", APIKey: "k", ProviderID: "auto"},
		{Endpoint: "jp", BaseURL: "http://jp.example/v1", ProviderID: "magpie-jp"},
	}
	got, rest, ok := ProviderOf("auto/a/b", targets)
	if !ok {
		t.Fatalf("auto/a/b 应当匹配")
	}
	if got.ProviderID != "auto" || got.Endpoint != "auto" {
		t.Fatalf("Target = %#v，想要 ProviderID auto", got)
	}
	if rest != "a/b" {
		t.Fatalf("剩余模型 = %q，想要 a/b", rest)
	}

	got, rest, ok = ProviderOf("magpie-jp/gpt-x", targets)
	if !ok || got.ProviderID != "magpie-jp" || rest != "gpt-x" {
		t.Fatalf("magpie-jp/gpt-x = %#v、%q、%v，想要 magpie-jp、gpt-x、true", got, rest, ok)
	}

	for _, model := range []string{
		"claude-sonnet-4-5", // 没有斜杠
		"unknown/x",         // 前缀不存在
		"auto/",             // 剩余部分为空
		"auto",              // 没有斜杠
		"/x",                // 空前缀
		"",                  // 空模型
		"Auto/x",            // 前缀大小写不匹配
	} {
		if _, _, ok := ProviderOf(model, targets); ok {
			t.Errorf("ProviderOf(%q) 应当返回 false", model)
		}
	}

	if _, _, ok := ProviderOf("auto/x", nil); ok {
		t.Errorf("没有 targets 时应当返回 false")
	}
}

func TestAnthropicBaseURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://h:3425/v1", "http://h:3425"},
		{"http://h:3425/v1/", "http://h:3425"},
		{"http://h:3425", "http://h:3425"},
		{"http://h:3425/", "http://h:3425"},
		{"https://h/proxy/v1", "https://h/proxy"},
		{"https://h/proxy/v1/", "https://h/proxy"},
		{"https://h/v1/v1", "https://h/v1"},
		{"", ""},
	}
	for _, c := range cases {
		if got := AnthropicBaseURL(c.in); got != c.want {
			t.Errorf("AnthropicBaseURL(%q) = %q，想要 %q", c.in, got, c.want)
		}
	}
}
