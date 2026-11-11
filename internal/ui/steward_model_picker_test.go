package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

type stewardCountingSecrets struct {
	config.SecretStore
	gets int
}

func (s *stewardCountingSecrets) Get(account string) (string, error) {
	s.gets++
	return "test-key", nil
}

func TestStewardComposerModelCachesAutoLabel(t *testing.T) {
	s := newChatState(newStewardFakeClient(""))
	t.Cleanup(s.close)
	st := NewSettingsState(t.TempDir(), nil, s)
	setChatSettings(s, st)
	t.Cleanup(func() { setChatSettings(s, nil) })
	secrets := &stewardCountingSecrets{SecretStore: config.NewMemorySecretStore()}
	st.stewardSecrets = secrets
	st.cfg.Providers.Anthropic.Enabled = nil
	st.cfg.Providers.OpenAI.Enabled = true
	st.cfg.Orchestration.Steward.Provider = "openai"
	s.providers = []ProviderInfo{
		{ID: "openai", Name: "OpenAI", Models: []ModelInfo{{ID: "gpt-test", Name: "GPT Test"}}},
		{ID: "anthropic", Name: "Anthropic", Models: []ModelInfo{{ID: "claude-opus-5-5", Name: "Opus Test"}}},
	}
	tt := ui.NewTester(func(c *ui.Context) { renderStewardComposerModel(c, s) }, 1000, 700)
	for i := 0; i < 20; i++ {
		tt.Frame()
	}
	if !tt.HasText("GPT Test") || secrets.gets != 1 {
		t.Fatalf("缓存标签或解析次数不正确: %v, %d", tt.Texts(), secrets.gets)
	}
	st.cfg.Orchestration.Steward.Provider = "anthropic"
	st.save()
	tt.Frame()
	if !tt.HasText("Opus Test") || secrets.gets != 2 {
		t.Fatalf("保存后标签或解析次数不正确: %v, %d", tt.Texts(), secrets.gets)
	}
	st.save()
	tt.Frame()
	if secrets.gets != 3 {
		t.Fatalf("保存后未清除缓存: %d", secrets.gets)
	}
}

func TestStewardModelSelectionUnavailable(t *testing.T) {
	var cfg config.Config
	cfg.Orchestration.Steward.Provider = "openai"
	cfg.Orchestration.Steward.Model = "removed"
	cat := modelPickerCatalog{Providers: []ProviderInfo{{ID: "openai", Models: []ModelInfo{{ID: "available"}}}}}
	if got := stewardModelSelection(cfg, cat); got != (ModelSelection{Provider: "openai"}) {
		t.Fatalf("下架模型应回到自动：%+v", got)
	}
}

func TestStewardModelEmptyLabel(t *testing.T) {
	var cfg config.Config
	cfg.Providers.OpenAI.Enabled = true
	cat := modelPickerCatalog{Providers: []ProviderInfo{{ID: "openai", Models: []ModelInfo{
		{ID: "first", Name: "First"}, {ID: "default", Name: "Default"},
	}}}}
	secrets := config.NewMemorySecretStore()
	for _, tc := range []struct {
		name, configured, provider, want string
		catalog                          modelPickerCatalog
	}{
		{"默认模型", "default", "openai", "Default", cat},
		{"首个模型", "", "openai", "First", cat},
		{"自动来源", "default", "", "Default", cat},
		{"空目录", "default", "", "自动", modelPickerCatalog{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.Providers.OpenAI.Model = tc.configured
			if got := stewardModelEmptyLabel(cfg, tc.catalog, ModelSelection{Provider: tc.provider}, secrets); got != tc.want {
				t.Fatalf("标签 = %q，期望 %q", got, tc.want)
			}
		})
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
		{ID: "openai", Name: "OpenAI", Available: true, Models: []ModelInfo{{ID: "gpt-test", Name: "GPT Test"}, {ID: "gpt-other", Name: "GPT Other"}}},
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "cli-test", Name: "CLI Test"}}},
	}
	s.selectedThreadID = "steward"
	s.threads = []Thread{{ID: "steward"}, {ID: "ordinary", Provider: "claude", Model: "cli-test"}}
	tt := ui.NewTester(func(c *ui.Context) { renderComposer(c, s) }, 1000, 700)
	if _, ok := tt.Find("选择前台模型"); !ok {
		t.Fatal("总管输入框缺少前台模型胶囊")
	}
	if !tt.HasText("GPT Test") {
		t.Fatalf("自动模型显示不正确: %v", tt.Texts())
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
	if err := tt.Click("GPT Other"); err != nil {
		t.Fatal(err)
	}
	if st.cfg.Orchestration.Steward.Provider != "openai" || st.cfg.Orchestration.Steward.Model != "gpt-other" {
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
	if !tt.HasText("GPT Test") || tt.HasText("pi") || tt.HasText(st.cfg.Orchestration.Steward.Model) {
		t.Fatalf("旧来源应显示回退模型: %v", tt.Texts())
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

// TestStewardTaskModelSelection 验证任务模型读的是子任务字段，与前台模型互不影响。
func TestStewardTaskModelSelection(t *testing.T) {
	var cfg config.Config
	cfg.Orchestration.Steward.Provider = "openai"
	cfg.Orchestration.Steward.Model = "front"
	cfg.Orchestration.Steward.TaskProvider = "claude"
	cfg.Orchestration.Steward.TaskModel = "opus"
	cat := modelPickerCatalog{Providers: []ProviderInfo{
		{ID: "openai", Models: []ModelInfo{{ID: "front"}}},
		{ID: "claude", Models: []ModelInfo{{ID: "opus"}}},
	}}
	if got := stewardTaskModelSelection(cfg, cat); got != (ModelSelection{Provider: "claude", Model: "opus"}) {
		t.Fatalf("任务模型 = %+v", got)
	}
	cfg.Orchestration.Steward.TaskProvider, cfg.Orchestration.Steward.TaskModel = "", ""
	if got := stewardTaskModelSelection(cfg, cat); got != (ModelSelection{}) {
		t.Fatalf("未设置时应为自动：%+v", got)
	}
}
