package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

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
