package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func modelPickerProviderGroupTester(t *testing.T, items []modelPickerItem, query string) *ui.Tester {
	t.Helper()
	st := &SettingsState{}
	ps := &modelsPageState{}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(420).Children(func() {
			renderModelPickerSections(c, st, ps, "provider-groups", items, query, "暂无可添加的模型", nil)
		})
	}, 440, 800)
	tt.Frame()
	return tt
}

func modelPickerProviderGroupItems() []modelPickerItem {
	return []modelPickerItem{
		{ProviderID: "claude", Section: "Claude Code", Key: "claude-sonnet", ID: "claude-sonnet", Name: "Sonnet"},
		{ProviderID: "agy", Section: "agy", Key: "agy-gpt", ID: "gpt-x", Name: "模型甲"},
		{ProviderID: "agy", Section: "agy", Key: "agy-claude", ID: "claude-y", Name: "模型乙"},
		{ProviderID: "agy", Section: "agy", Key: "agy-other", ID: "unknown", Name: "模型丙"},
	}
}

func Test模型选择浮层_供应商分组不显示系列标题(t *testing.T) {
	items := modelPickerProviderGroupItems()
	tt := modelPickerProviderGroupTester(t, items, "")
	for _, text := range []string{"Claude Code", "agy", "Sonnet", "模型甲", "模型乙", "模型丙"} {
		if !tt.HasText(text) {
			t.Fatalf("缺少供应商或模型 %q: %v", text, tt.Texts())
		}
	}
	for _, item := range items {
		if title := magpieModelFamily(item.ID); magpieTestHasExactText(tt, title) {
			t.Fatalf("供应商分组不应显示系列标题 %q: %v", title, tt.Texts())
		}
	}
}

func Test模型选择浮层_搜索隐藏无匹配供应商(t *testing.T) {
	tt := modelPickerProviderGroupTester(t, modelPickerProviderGroupItems(), "sonnet")
	if !tt.HasText("Claude Code") || !tt.HasText("Sonnet") {
		t.Fatalf("单个匹配供应商仍应显示组头和模型: %v", tt.Texts())
	}
	if tt.HasText("agy") || tt.HasText("模型甲") || tt.HasText("模型乙") || tt.HasText("模型丙") {
		t.Fatalf("无匹配供应商应整组隐藏: %v", tt.Texts())
	}
}

func Test模型选择浮层_未指定供应商保留分段和系列标题(t *testing.T) {
	items := modelPickerProviderGroupItems()
	for i := range items {
		items[i].ProviderID = ""
	}
	tt := modelPickerProviderGroupTester(t, items, "")
	for _, text := range []string{"Claude Code", "agy", "Sonnet", "模型甲", "模型乙", "模型丙", "Claude", "GPT", "其他"} {
		if !tt.HasText(text) {
			t.Fatalf("旧模式应保留分段、系列和模型 %q: %v", text, tt.Texts())
		}
	}
}

func Test模型系列图标映射(t *testing.T) {
	for _, tc := range []struct {
		title string
		key   string
	}{
		{"Claude", "claude"},
		{"GPT", "codex"},
		{"GLM", "zai"},
		{"DeepSeek", "deepseek"},
		{"Grok", "grok"},
		{"Gemini", ""},
		{"Qwen", ""},
		{"MiMo", ""},
		{"Kimi", ""},
		{"其他", ""},
		{"未知系列", ""},
	} {
		t.Run(tc.title, func(t *testing.T) {
			if got := modelFamilyIconKey(tc.title); got != tc.key {
				t.Fatalf("系列图标 = %q，期望 %q", got, tc.key)
			}
			if tc.key != "" && colorProviderIcons[tc.key] == nil {
				t.Fatalf("系列图标 %q 没有彩色资源", tc.key)
			}
		})
	}
}

func Test模型选择浮层_系列图标保留组头文字(t *testing.T) {
	items := []modelPickerItem{
		{Key: "claude", ID: "auto/claude-sonnet-5-5", Name: "Claude Sonnet 5.5"},
		{Key: "deepseek", ID: "deepseek-v4", Name: "DeepSeek V4"},
		{Key: "unknown", ID: "my-unknown-model", Name: "未知模型"},
	}
	tt := modelPickerProviderGroupTester(t, items, "")
	for _, title := range []string{"Claude", "DeepSeek", "其他"} {
		if !magpieTestHasExactText(tt, title) {
			t.Fatalf("缺少系列组头 %q: %v", title, tt.Texts())
		}
	}
}

func Test模型选择浮层_显示模型ID且不重复(t *testing.T) {
	for _, tc := range []struct {
		title string
		name  string
		id    string
		count int
	}{
		{"名称与ID不同", "Claude Sonnet 5.5", "auto/claude-sonnet-5-5", 1},
		{"名称为空", "", "auto/claude-sonnet-5-5", 1},
		{"名称等于ID", "auto/claude-sonnet-5-5", "auto/claude-sonnet-5-5", 1},
		{"ID为空", "Claude Sonnet 5.5", "", 0},
		{"ID仅含空白", "Claude Sonnet 5.5", "   ", 0},
	} {
		t.Run(tc.title, func(t *testing.T) {
			tt := modelPickerProviderGroupTester(t, []modelPickerItem{
				{Key: "model", ID: tc.id, Name: tc.name},
			}, "")
			if got := magpieTestCountExactText(tt, tc.id); got != tc.count {
				t.Fatalf("模型ID %q 出现 %d 次，期望 %d 次: %v", tc.id, got, tc.count, tt.Texts())
			}
			if tc.name != "" && !magpieTestHasExactText(tt, tc.name) {
				t.Fatalf("缺少模型名称 %q: %v", tc.name, tt.Texts())
			}
		})
	}
}

func Test添加收藏浮层_隐藏关闭和空模型供应商(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	providers := []ProviderInfo{
		{ID: "claude", Name: "Claude Code", Models: []ModelInfo{{ID: "claude-sonnet", Name: "Sonnet"}}},
		{ID: "codex", Name: "Codex", Models: []ModelInfo{{ID: "gpt-x", Name: "关闭的模型"}}},
		{ID: "empty", Name: "空模型供应商"},
	}
	fake.SetProviders(providers)
	chat := newChatState(fake)
	chat.providers = providers
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	st.mu.Lock()
	st.cfg.Providers.Claude.Enabled = true
	st.cfg.Providers.Codex.Enabled = false
	st.mu.Unlock()
	state := observeModelsPageState(t)
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(600).Children(func() {
			renderAddModelPin(c, st)
		})
	}, 640, 800)
	waitModelsPageLoaded(t, tt, state)
	clickWhenSettled(t, tt, "添加收藏")
	waitFor(t, tt, func() bool { return tt.HasText("Claude Code") && tt.HasText("Sonnet") })
	for _, text := range []string{"Codex", "关闭的模型", "空模型供应商"} {
		if tt.HasText(text) {
			t.Fatalf("关闭或没有模型的供应商应整组隐藏 %q: %v", text, tt.Texts())
		}
	}
}
