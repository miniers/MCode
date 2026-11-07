package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestProviderSheetContentInsets(t *testing.T) {
	cases := []struct {
		name, kind, id, card, text string
	}{
		{"claude", "cli", "claude", "sec-provider-account", "测试额度"},
		{"codex", "cli", "codex", "sec-provider-account", "测试额度"},
		{"agy", "cli", "agy", "sec-agy-accounts", "还没有 agy 账号"},
		{"pi", "cli", "pi", "sec-provider-account", usageUnsupportedHint},
		{"cline", "cli", "cline", "sec-provider-cli", "修改后需重启 MCode 生效"},
		{"preset", "preset", "deepseek", "sec-provider-account", "测试额度"},
		{"custom", "custom", "endpoint", "sec-provider-account", "接口地址（BaseURL）"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			st.endpoints.loaded = true
			st.agy.loaded = true
			st.presets.items = []ProviderSetting{{ID: "deepseek", Kind: "api"}}
			st.endpoints.endpoints = []CustomEndpoint{{ID: "endpoint", Name: "端点"}}
			st.providerRows.quota = map[string][]ProviderQuotaWindow{
				"claude": {{Label: "测试额度", UsedPercent: 50}},
				"codex": {{Label: "测试额度", UsedPercent: 50}},
				"deepseek": {{Label: "测试额度", UsedPercent: 50}},
			}
			st.providerRows.quotaErr = map[string]string{"pi": "unsupported"}
			lib := &modelsLibraryView{providers: []ProviderInfo{{ID: tc.id, Models: []ModelInfo{{ID: "test-model", Name: "测试模型"}}}}}
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c.Key("test-provider-sheet")).FillWidth().Children(func() {
					if tc.kind == "cli" {
						renderCLIProviderSheetSections(c, st, tc.id, lib)
					} else {
						renderAPIProviderSheetSections(c, st, tc.kind, tc.id)
					}
				})
			}, 1000, 4000)
			tt.Frame()
			card, ok := tt.Find("设置卡片 " + tc.card)
			if !ok {
				t.Fatalf("缺少卡片 %s: %v", tc.card, tt.Texts())
			}
			text, ok := tt.Find(tc.text)
			if !ok {
				t.Fatalf("缺少文字 %s: %v", tc.text, tt.Texts())
			}
			if text.X < card.X+settingsRowPadX-1 || text.X+text.W > card.X+card.W-settingsRowPadX+1 {
				t.Errorf("%s 超出卡片内容区: text=%+v card=%+v", tc.text, text, card)
			}
		})
	}
}
