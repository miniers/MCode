package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/testenv"
)

func TestSwarmPresetAddedShownOnce(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	addSwarmPreset(st, swarmPresets[0])

	providers := []ProviderInfo{
		{
			ID:        config.ProviderClaude,
			Available: true,
			Models: []ModelInfo{
				{ID: "Fable", Name: "Fable"},
				{ID: "Opus", Name: "Opus"},
			},
		},
	}

	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmPresetsPanel(c, st, providers)
	}, 900, 4000)
	tt.Frame()
	if err := tt.Click("预设组合"); err != nil {
		t.Fatal(err)
	}

	var addedNodes []ui.AccessInfo
	for _, a := range tt.AccessAll() {
		if a.Name == "已添加" {
			addedNodes = append(addedNodes, a)
		}
	}
	if len(addedNodes) != 1 || addedNodes[0].Role != ui.RoleButton || !addedNodes[0].Disabled {
		t.Fatalf("已添加 presets must render only the disabled button once, got access=%+v texts=%v", addedNodes, tt.Texts())
	}
}

func TestMagpieNewGatewayHidesQuotaRow(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) {
		renderMagpieGatewaySheetBody(c, st, "")
	}, 1200, 4000)
	tt.Frame()

	if !tt.HasText("名称") {
		t.Fatalf("新建网关表单未渲染：%v", tt.Texts())
	}
	for _, forbidden := range []string{"菜单栏额度", "刷新供应商", "保存后可选"} {
		if tt.HasText(forbidden) {
			t.Fatalf("新建网关不应出现 %q：%v", forbidden, tt.Texts())
		}
	}
}

func TestModelsCatalogNoReadonlyLabel(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	lib := &modelsLibraryView{
		providers: []ProviderInfo{
			{
				ID:     "claude",
				Name:   "Claude",
				Models: []ModelInfo{{ID: "claude-test", Name: "Claude 测试模型"}},
			},
		},
	}
	panel := ui.NewTester(func(c *ui.Context) {
		for _, def := range cliProviderSheetSections(st, "claude", lib) {
			def.Render(c, st)
		}
	}, 1000, 4000)
	panel.Frame()

	if !panel.HasText("Claude 测试模型") {
		t.Fatalf("模型目录未渲染：%v", panel.Texts())
	}
	for _, txt := range panel.Texts() {
		if txt == "只读" {
			t.Fatalf("模型目录不应再出现独立「只读」标签：%v", panel.Texts())
		}
	}
}
