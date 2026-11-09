package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsNav_MagpieEntry(t *testing.T) {
	for _, tc := range []struct {
		name      string
		connected bool
		status    string
	}{
		{"未连接", false, "连接 Magpie"},
		{"已连接", true, "Magpie"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fake := NewFakeClient()
			if tc.connected {
				fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "测试网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true, HasAPIKey: true}}
				fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
			}
			state := newChatState(&routeTestClient{FakeClient: fake})
			st := NewSettingsState(settingsTestDir(t), nil, state)
			st.open = true
			st.currentPage = "general"
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 1000, 4000)
			waitMagpie(t, tt, func() bool { return tt.HasText(tc.status) && tt.HasText("通用") })
			general, ok := tt.Find("通用")
			if !ok {
				t.Fatal("未找到通用导航项")
			}
			magpie, ok := tt.Find("Magpie")
			if !ok || magpie.Y >= general.Y || magpie.X >= 500 {
				t.Fatalf("Magpie 导航项应位于通用之前：general=%+v magpie=%+v", general, magpie)
			}
			tt.ClickAt(magpie.X+magpie.W/2, magpie.Y+magpie.H/2)
			waitMagpie(t, tt, func() bool {
				gateway, ok := tt.Find("网关")
				return ok && gateway.X > magpie.X+magpie.W
			})
		})
	}
}

func TestSettingsSearch_ProviderItemOpensSheet(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.open = true
	st.currentPage = "general"
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 1000, 4000)
	tt.Frame()
	if err := tt.Click("搜索设置"); err != nil {
		t.Fatal(err)
	}
	tt.Type("可执行文件路径")
	waitMagpie(t, tt, func() bool { return tt.HasText("Claude 可执行文件路径") })
	if err := tt.Click("Claude 可执行文件路径"); err != nil {
		t.Fatal(err)
	}
	waitMagpie(t, tt, func() bool {
		open, _ := settingsTreeSheetState(st)
		return open == "provider" && tt.HasText("可执行文件路径 Claude")
	})
}
