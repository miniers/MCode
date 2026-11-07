package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// agentMagpieCodexTestSetting 构造 codex 的 Magpie 设置：一个端点一个模型，支持写全局配置。
func agentMagpieCodexTestSetting() AgentMagpieSetting {
	return AgentMagpieSetting{
		Agent: "codex",
		Mode:  "inject",
		Endpoints: []PiMagpieEndpoint{
			{
				ID:         "jp",
				Name:       "JP 网关",
				ProviderID: "magpie-jp",
				Models:     []PiMagpieModel{{ID: "auto/gpt-5", Name: "JP GPT-5"}},
				Selected:   []string{"auto/gpt-5"},
			},
		},
		Global: AgentMagpieGlobal{
			Supported: true,
			State:     "none",
			Path:      "/home/me/.codex/config.toml",
		},
	}
}

func agentMagpieCodexTester(t *testing.T, fake *FakeClient, setting AgentMagpieSetting) (*ui.Tester, *SettingsState) {
	t.Helper()
	return magpieModelPanelTester(t, fake, setting, ProviderInfo{ID: "codex", Name: "Codex", Models: []ModelInfo{{ID: "gpt-5", Name: "目录 GPT-5"}}})
}

func TestSettings_AgentMagpieCodex_InjectDescription(t *testing.T) {
	setting := agentMagpieCodexTestSetting()
	tt, st, _ := agentMagpiePageTester(t, setting)
	if value, known := magpieAgentRouteValue(st, "codex"); !known || value != "仅 mcode" {
		t.Fatalf("Codex inject 应显示仅 mcode: %q %v", value, known)
	}
	choice := magpieAgentRouteChoice("codex")
	if len(choice.options) != 3 || choice.options[1] != "仅 mcode" || choice.options[2] != "全局" {
		t.Fatalf("Codex 应有三段路由: %v", choice.options)
	}
	if idx, busy := choice.get(st); busy || idx != 1 {
		t.Fatalf("Codex inject 应选中仅 mcode: %d %v", idx, busy)
	}
	if tt.HasText("只注入 mcode 内启动的 codex") {
		t.Fatalf("不应显示旧的注入说明: %v", tt.Texts())
	}
}

func TestSettings_AgentMagpieCodex_GlobalModeConfirm(t *testing.T) {
	setting := agentMagpieCodexTestSetting()
	setting.Mode = "off"
	tt, st, fake := agentMagpiePageTester(t, setting)
	clickMagpiePageRoute(t, tt, "Codex", "全局")
	waitFor(t, tt, func() bool { return tt.HasText("写入全局配置") })
	if len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("确认前不得写入 Codex 全局配置")
	}
	if !tt.HasText("Codex 会改用该 key 与 mcode-magpie provider，不再使用 ChatGPT 登录") {
		t.Fatalf("缺少 Codex 全局确认说明: %v", tt.Texts())
	}
	if err := tt.Click("写入全局配置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		value, known := magpieAgentRouteValue(st, "codex")
		return len(calls) == 1 && calls[0].Agent == "codex" && calls[0].Mode == "global" && known && value == "全局"
	})
	tt.Frame()
	waitRouteIdle(t, tt, st, "codex")
	clickMagpiePageRoute(t, tt, "Codex", "直连")
	waitFor(t, tt, func() bool {
		calls := fake.SetAgentMagpieModeCalls()
		return len(calls) == 2 && calls[1].Agent == "codex" && calls[1].Mode == "off"
	})
}

func TestSettings_AgentMagpieCodex_GlobalModeCancel(t *testing.T) {
	setting := agentMagpieCodexTestSetting()
	setting.Mode = "off"
	tt, st, fake := agentMagpiePageTester(t, setting)
	clickMagpiePageRoute(t, tt, "Codex", "全局")
	waitFor(t, tt, func() bool { return tt.HasText("写入全局配置") })
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("写入全局配置") })
	value, known := magpieAgentRouteValue(st, "codex")
	if !known || value != "直连" || len(fake.SetAgentMagpieModeCalls()) != 0 {
		t.Fatal("取消应保持原路由且不保存")
	}
}

// TestSettings_AgentMagpieCodex_说明位于默认模型下方 覆盖：说明紧跟默认模型行。
func TestSettings_AgentMagpieCodex_说明位于默认模型下方(t *testing.T) {
	tt, _ := agentMagpieCodexTester(t, NewFakeClient(), agentMagpieCodexTestSetting())
	waitFor(t, tt, func() bool {
		return tt.HasText("默认模型") && tt.HasText("写全局配置后，codex 使用这里选定的默认模型")
	})
	row, ok := tt.Find("选择默认模型")
	if !ok {
		t.Fatal("找不到默认模型行")
	}
	footer, ok := tt.Find("写全局配置后，codex 使用这里选定的默认模型")
	if !ok || footer.Y < row.Y+row.H {
		t.Fatalf("说明应位于默认模型行下方: row=%+v, footer=%+v", row, footer)
	}
}

func TestSettings_AgentMagpieCodex_NoTierSection(t *testing.T) {
	setting := agentMagpieCodexTestSetting()
	setting.Mode = "global"
	setting.Global.State = "applied"
	setting.Tiers = []AgentMagpieTier{{Tier: "opus", EndpointID: "jp", ModelID: "auto/gpt-5"}}
	tt, _ := agentMagpieCodexTester(t, NewFakeClient(), setting)
	waitFor(t, tt, func() bool { return tt.HasText("已启用（1）") && tt.HasText("JP GPT-5") })
	if tt.HasText("档位映射") {
		t.Fatalf("codex 不应显示档位映射: %v", tt.Texts())
	}
	if !tt.HasText("写全局配置后，codex 使用这里选定的默认模型") {
		t.Fatalf("codex 应显示默认模型说明: %v", tt.Texts())
	}
}
