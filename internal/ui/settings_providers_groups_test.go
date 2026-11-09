package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsProvidersGroups(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.providerRows.loaded = true
	st.providerRows.cliIDs = []string{"pi", "mcode"}
	st.providerRows.settings = map[string]ProviderSetting{
		"pi":    {ID: "pi", Name: "Pi"},
		"mcode": {ID: "mcode", Name: "mcode"},
	}
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "deepseek", Kind: "api", Enabled: true, HasAPIKey: true}}
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "local", Name: "本地端点", Enabled: true, HasAPIKey: true}}

	tt := ui.NewTester(func(c *ui.Context) { renderProvidersCard(c, st, nil) }, 1000, 4000)
	tt.Frame()
	texts := tt.Texts()
	sheetSectionOrder(t, texts, []string{"智能体", "Pi", "API Key", "DeepSeek", "本地端点"})
	for _, text := range texts {
		if text == "mcode" {
			t.Fatalf("供应商列表不应包含 mcode：%v", texts)
		}
	}
}
