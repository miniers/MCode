package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestStewardModelSelectionUnavailable(t *testing.T) {
	var cfg config.Config
	cfg.Orchestration.Steward.Provider = "openai"
	cfg.Orchestration.Steward.Model = "removed"
	cat := modelPickerCatalog{Providers: []ProviderInfo{{ID: "openai", Models: []ModelInfo{{ID: "available"}}}}}
	if got := stewardModelSelection(cfg, cat); got != (ModelSelection{Provider: "openai"}) {
		t.Fatalf("下架模型应回到自动：%+v", got)
	}
}

func TestStewardComposerModel(t *testing.T) {
	fake := newStewardFakeClient("")
	fake.threadID = "steward"
	s := newChatState(fake)
	t.Cleanup(s.close)
	st := NewSettingsState(t.TempDir(), nil, s)
	setChatSettings(s, st)
	t.Cleanup(func() { setChatSettings(s, nil) })
	st.cfg.Providers.OpenAI.Enabled = true
	s.providers = []ProviderInfo{
		{ID: "openai", Name: "OpenAI", Available: true, Models: []ModelInfo{{ID: "gpt-test", Name: "GPT Test"}}},
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "cli-test", Name: "CLI Test"}}},
	}
	s.selectedThreadID = "steward"
	s.threads = []Thread{{ID: "steward"}, {ID: "ordinary", Provider: "claude", Model: "cli-test"}}
	tt := ui.NewTester(func(c *ui.Context) { renderComposer(c, s) }, 1000, 700)
	if _, ok := tt.Find("选择前台模型"); !ok {
		t.Fatal("总管输入框缺少前台模型胶囊")
	}
	for _, label := range []string{"构建", "计划", "上下文用量"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("总管输入框不应显示 %s", label)
		}
	}
	if err := tt.Click("选择前台模型"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("CLI Test") || !tt.HasText("GPT Test") {
		t.Fatalf("前台模型候选不正确: %v", tt.Texts())
	}
	if err := tt.Click("GPT Test"); err != nil {
		t.Fatal(err)
	}
	if st.cfg.Orchestration.Steward.Provider != "openai" || st.cfg.Orchestration.Steward.Model != "gpt-test" {
		t.Fatalf("前台模型未保存: %+v", st.cfg.Orchestration.Steward)
	}
	s.selectedThreadID = "ordinary"
	tt.Frame()
	if _, ok := tt.Find("构建"); !ok {
		t.Fatal("普通会话缺少模式切换")
	}
	if _, ok := tt.Find("选择模型"); !ok {
		t.Fatal("普通会话缺少模型选择器")
	}
	s.selectedThreadID = "steward"
	s.threads = nil
	tt.Frame()
	if _, ok := tt.Find("选择前台模型"); !ok {
		t.Fatal("总管会话尚未载入时缺少前台模型胶囊")
	}
}

func TestStewardComposerModelLegacySelection(t *testing.T) {
	s := newChatState(newStewardFakeClient(""))
	t.Cleanup(s.close)
	st := NewSettingsState(t.TempDir(), nil, s)
	setChatSettings(s, st)
	t.Cleanup(func() { setChatSettings(s, nil) })
	s.providers = []ProviderInfo{{ID: "openai", Name: "OpenAI", Models: []ModelInfo{{ID: "gpt-test", Name: "GPT Test"}}}}
	st.cfg.Orchestration.Steward.Provider = "pi"
	st.cfg.Orchestration.Steward.Model = "magpie-jp/auto/deepseek-v4.1-flash"
	tt := ui.NewTester(func(c *ui.Context) { renderStewardComposerModel(c, s) }, 1000, 700)
	if !tt.HasText("自动") || tt.HasText("pi") || tt.HasText(st.cfg.Orchestration.Steward.Model) {
		t.Fatalf("旧来源应显示自动: %v", tt.Texts())
	}
	if st.cfg.Orchestration.Steward.Provider != "pi" || st.cfg.Orchestration.Steward.Model != "magpie-jp/auto/deepseek-v4.1-flash" {
		t.Fatal("显示旧来源时不应修改配置")
	}
	st.cfg.Orchestration.Steward.Provider = "openai"
	st.cfg.Orchestration.Steward.Model = "gpt-test"
	tt.Frame()
	if !tt.HasText("GPT Test") {
		t.Fatalf("API 模型应显示原值: %v", tt.Texts())
	}
}

func TestStewardComposerModelShowsMagpieSource(t *testing.T) {
	s := newChatState(newStewardFakeClient(""))
	t.Cleanup(s.close)
	st := NewSettingsState(t.TempDir(), nil, s)
	setChatSettings(s, st)
	t.Cleanup(func() { setChatSettings(s, nil) })
	ep := config.CustomEndpointConfig{ID: "432db46a", Name: "Magpie", Magpie: true, Enabled: true}
	st.cfg.Providers.Custom = []config.CustomEndpointConfig{ep}
	id := config.CustomEndpointProviderKind(ep)
	s.providers = []ProviderInfo{
		{ID: id, Name: "Magpie", Available: true, Models: []ModelInfo{{ID: "auto/deepseek-v4.1-flash", Name: "DeepSeek Flash"}}},
		{ID: config.ProviderClaude, Name: "Claude", Available: true, Models: []ModelInfo{{ID: "cli-test", Name: "CLI Test"}}},
	}
	cat := frontdeskModelPickerCatalog(modelPickerCatalogFor(s), st.cfg)
	if len(cat.Providers) != 1 || cat.Providers[0].ID != id {
		t.Fatalf("前台来源不正确: %+v", cat.Providers)
	}
	filtered := filterModelPickerMagpie(cat.Providers, cat.MagpieFilter)
	if len(filtered) != 1 || filtered[0].ID != id {
		t.Fatalf("前台 Magpie 来源被隐藏: %+v", filtered)
	}
	tt := ui.NewTester(func(c *ui.Context) { renderStewardComposerModel(c, s) }, 1000, 700)
	if err := tt.Click("选择前台模型"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("DeepSeek Flash") || tt.HasText("CLI Test") {
		t.Fatalf("前台模型候选不正确: %v", tt.Texts())
	}
	if err := tt.Click("DeepSeek Flash"); err != nil {
		t.Fatal(err)
	}
	if st.cfg.Orchestration.Steward.Provider != id || st.cfg.Orchestration.Steward.Model != "auto/deepseek-v4.1-flash" {
		t.Fatalf("前台模型未保存: %+v", st.cfg.Orchestration.Steward)
	}
}
