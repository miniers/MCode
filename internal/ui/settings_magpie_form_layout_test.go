package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestMagpiePersonaFormLayout(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Name: "网关", Magpie: true, Enabled: true, PersonaWebURL: "https://example.com"}}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Gap(Space6).Children(func() { renderMagpiePersonasSheetBody(c, st) })
	}, 600, 2000)
	tt.Frame()

	find := func(label string) ui.Rect {
		t.Helper()
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("找不到 %q: %v", label, tt.Texts())
		}
		return r
	}
	card := find("设置卡片 sec-magpie-form-persona")
	address := find("编辑人设 web 管理地址")
	key := find("编辑人设 web key")
	for name, r := range map[string]ui.Rect{"地址": address, "密钥": key} {
		if r.X < card.X || r.X+r.W > card.X+card.W || r.Y < card.Y || r.Y+r.H > card.Y+card.H {
			t.Errorf("%s 输入框不在卡片内: %+v, 卡片 %+v", name, r, card)
		}
	}
	// 输入框居中于设置行，中心距减去外框高度和上下内边距得到行间隙。
	gap := (key.Y + key.H/2) - (address.Y + address.H/2) - ControlMD - 2*settingsRowPadY
	if gap > Hairline+1 || gap < 0 {
		t.Errorf("地址和密钥行间隙 = %v", gap)
	}
	area := find("手动人设列表 JSON")
	if area.W < card.W-2*settingsRowPadX-2 {
		t.Errorf("手动列表宽度 = %v，卡片宽度 = %v", area.W, card.W)
	}
	rules := find("已在 magpie 部署 persona-rules")
	if rules.X+rules.W > card.X+card.W {
		t.Errorf("人设规则超出卡片: %+v, 卡片 %+v", rules, card)
	}
}
