package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// providerSheetForTest 打开「供应商」面板并在测试器里只渲染这个面板。
func providerSheetForTest(t *testing.T, st *SettingsState, kind, id string) *ui.Tester {
	t.Helper()
	sheet, ok := settingsTreeSheetOf("provider")
	if !ok {
		t.Fatal("供应商面板未注册")
	}
	openProviderSheet(st, kind, id)
	tt := ui.NewTester(func(c *ui.Context) {
		if openID, _ := settingsTreeSheetState(st); openID == sheet.ID {
			renderSettingsTreeSheet(c, st, sheet)
			return
		}
		ui.Text(c, "settings-sheet-closed").FontSize(13)
	}, 1000, 4000)
	tt.Frame()
	return tt
}

// sheetSectionOrder 返回 texts 里这些节标题出现的先后位置，缺一个就失败。
func sheetSectionOrder(t *testing.T, texts []string, wants []string) map[string]int {
	t.Helper()
	positions := map[string]int{}
	pos := -1
	for _, want := range wants {
		found := false
		for i := pos + 1; i < len(texts); i++ {
			if texts[i] == want {
				pos = i
				positions[want] = i
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("缺少或乱序的节 %q: %v", want, texts)
		}
	}
	return positions
}

func TestSettingsModels_ProviderListOrder(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.providerRows.loaded = true
	// pi 已登记但探测到未安装：它必须照样出现在列表里，不折叠。
	st.providerRows.cliIDs = []string{"pi"}
	st.providerRows.settings = map[string]ProviderSetting{"pi": {ID: "pi", Name: "Pi"}}
	st.providerRows.versions = map[string]ProviderVersionInfo{}
	st.providerRows.available = map[string]bool{"pi": false}
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "preset-p4", Name: "A P4 preset", Kind: "api", Enabled: true}}
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{
		{ID: "custom-p4", Name: "Z P4 custom", Enabled: false},
		{ID: "gateway-p4", Name: "P4 gateway", Magpie: true},
	}
	st.cfg.Providers.Claude.Enabled = false
	tt := ui.NewTester(func(c *ui.Context) { renderProvidersCard(c, st, nil) }, 1000, 4000)
	tt.Frame()
	texts := tt.Texts()
	positions := map[string]int{}
	for i, text := range texts {
		if _, ok := positions[text]; !ok {
			positions[text] = i
		}
	}
	for _, text := range []string{"A P4 preset", "Pi", "Z P4 custom", providerDisplayName("claude")} {
		if _, ok := positions[text]; !ok {
			t.Fatalf("缺少供应商 %q: %v", text, texts)
		}
	}
	if !tt.HasText("未安装") {
		t.Fatalf("未安装的 CLI 应出现在列表里并标注未安装: %v", texts)
	}
	// 智能体和 API Key 分组各自按启用状态、名称排序。
	order := []string{"智能体", providerDisplayName("codex"), providerDisplayName("agy"), providerDisplayName("claude"), "Pi", "API Key", "A P4 preset", "Z P4 custom"}
	for i := 1; i < len(order); i++ {
		prev, ok := positions[order[i-1]]
		if !ok {
			t.Fatalf("缺少供应商 %q: %v", order[i-1], texts)
		}
		next, ok := positions[order[i]]
		if !ok {
			t.Fatalf("缺少供应商 %q: %v", order[i], texts)
		}
		if prev >= next {
			t.Fatalf("供应商排序错误，%q 应在 %q 之前: %v", order[i-1], order[i], texts)
		}
	}
	if _, ok := positions["P4 gateway"]; ok {
		t.Fatalf("Magpie 网关不应出现在供应商列表: %v", texts)
	}
}

func TestSettingsModels_ProviderSheetSections(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.endpoints.loaded = true
	st.mu.Lock()
	agentMagpie := st.agentMagpieFor("claude")
	agentMagpie.loaded = true
	agentMagpie.setting.Mode = "inject"
	st.mu.Unlock()
	lib := &modelsLibraryView{providers: []ProviderInfo{{ID: "claude", Models: []ModelInfo{{ID: "p4-model", Name: "P4 model"}}}}}
	defs := cliProviderSheetSections(st, "claude", lib)
	if len(defs) != 3 {
		t.Fatalf("Claude 应有账号、模型、命令行三节，实际 %d", len(defs))
	}
	for i, title := range []string{"账号", "模型", "命令行"} {
		if defs[i].Title != title || defs[i].Advanced != (i == 2) {
			t.Fatalf("第 %d 节标题或高级归属错误：%+v", i, defs[i])
		}
	}

	// agy 调试堆叠仍保留账号与调度，模型和命令行不属于 agy。
	agyState := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	agyState.agy.loaded = true
	agyTT := ui.NewTester(func(c *ui.Context) { renderAgyProviderSheetSections(c, agyState) }, 1000, 4000)
	agyTT.Frame()
	agyTexts := agyTT.Texts()
	agyPositions := sheetSectionOrder(t, agyTexts, []string{"账号", "调度"})
	if _, ok := agyPositions["模型"]; ok {
		t.Fatalf("agy 面板不应有「模型」节: %v", agyTexts)
	}
	if _, ok := agyPositions["命令行"]; ok {
		t.Fatalf("agy 面板不应有「命令行」节: %v", agyTexts)
	}

	// Esc 关闭面板后 openSheet 必须回到空串。
	escState := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	escState.agy.loaded = true
	escTT := providerSheetForTest(t, escState, "cli", "agy")
	if !escTT.HasText("调度策略") {
		t.Fatalf("agy 面板未打开: %v", escTT.Texts())
	}
	escTT.Key(0, ui.KeyEscape)
	escTT.Frame()
	if openID, _ := settingsTreeSheetState(escState); openID != "" {
		t.Fatalf("Esc 后面板仍打开: %q", openID)
	}
}

func TestSettingsModels_MagpieSourceBadge(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.piMagpie.loaded = true
	st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "jp", ProviderID: "magpie-jp", Selected: []string{"auto/x"}}}
	pin := ModelPin{Provider: "pi", ModelID: "magpie-jp/auto/x"}
	ps := newModelsPageState()
	tt := ui.NewTester(func(c *ui.Context) {
		renderPinnedModelRow(c, st, &ps, nil, []ProviderInfo{{ID: "pi", Models: []ModelInfo{{ID: pin.ModelID, Name: "P4 注入模型"}}}}, []ModelPin{pin}, 0, pin)
	}, 1000, 4000)
	tt.Frame()
	if !tt.HasText("P4 注入模型") || !tt.HasText("Magpie") {
		t.Fatalf("收藏行缺少模型名称或来源徽标: %v", tt.Texts())
	}
}

// trayQuotaShownForTest 读菜单栏额度开关的当前状态。
func trayQuotaShownForTest(st *SettingsState) (shown bool, hidden int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return trayQuotaShown(st.cfg, "claude"), len(st.cfg.Startup.TrayQuotaHidden)
}

func TestSettingsModels_LockedProviderReadOnlyInSheet(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())

	// 直接渲染「账号」节：避开弹窗滚动区，点击能稳定落在开关上。
	accountTester := func(st *SettingsState) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
			renderProviderAccountTab(c, st, "claude", providerDetailState{}, st.isLocked("claude-enabled"))
		}, 1000, 4000)
		tt.Frame()
		return tt
	}

	// 未锁定：点开关会写配置，证明点击落在开关上。
	unlocked := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := accountTester(unlocked)
	if err := tt.Click("在菜单栏显示额度"); err != nil {
		t.Fatalf("点击菜单栏额度开关: %v", err)
	}
	tt.Frame()
	if shown, _ := trayQuotaShownForTest(unlocked); shown {
		t.Fatalf("未锁定时点击开关应写入配置")
	}

	// 锁定 claude 后面板里的开关禁用，点击不产生写入。
	locked := NewSettingsState(settingsTestDir(t), map[string]bool{"claude-enabled": true}, newChatState(NewFakeClient()))
	if !locked.isLocked("claude-enabled") {
		t.Fatal("fixture 未锁定 claude")
	}
	tt = accountTester(locked)
	if err := tt.Click("在菜单栏显示额度"); err != nil {
		t.Fatalf("点击菜单栏额度开关: %v", err)
	}
	tt.Frame()
	if shown, hidden := trayQuotaShownForTest(locked); !shown || hidden != 0 {
		t.Fatalf("锁定后点击开关仍写入了配置: shown=%v hidden=%d", shown, hidden)
	}
}

func TestSettingsSheet_EscReturnsFocus(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	tt, st := openProvidersPageTest(t, NewFakeClient())
	clickProviderDetailTabForTest(t, tt, "claude", "")
	waitFor(t, tt, func() bool {
		openID, _ := settingsTreeSheetState(st)
		return openID == "provider"
	})
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	waitFor(t, tt, func() bool {
		openID, _ := settingsTreeSheetState(st)
		return openID == ""
	})
	st.mu.Lock()
	highlightID := st.highlightItemID
	startedAt := st.highlightStartedAt
	st.mu.Unlock()
	if highlightID != "provider-row-cli-claude" {
		t.Fatalf("Esc 后高亮未回到来源行: %q", highlightID)
	}
	if startedAt.IsZero() || time.Since(startedAt) > settingsItemHighlightDuration {
		t.Fatalf("来源行高亮时间不对: %v", startedAt)
	}
}

// TestSettingsSheets_MaxDepth 确认面板正文渲染期间不会打开另一个面板：
// 面板叠面板会让 Esc 一次关不掉，层级始终停在原面板。
func TestSettingsSheets_MaxDepth(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.endpoints.loaded = true
	st.presets.loaded = true
	st.agy.loaded = true
	st.magpieHub.sheetGatewayID = ""

	openProviderSheet(st, "cli", "claude")

	var opened []string
	settingsSheetOpenHook = func(sheet string) { opened = append(opened, sheet) }
	t.Cleanup(func() { settingsSheetOpenHook = nil })

	tt := ui.NewTester(func(c *ui.Context) {
		openID, _ := settingsTreeSheetState(st)
		if sheet, ok := settingsTreeSheetOf(openID); ok && openID != "" {
			renderSettingsTreeSheet(c, st, sheet)
			return
		}
		ui.Text(c, "settings-sheet-closed").FontSize(13)
	}, 1000, 4000)

	sheets := settingsTreeSheets()
	if len(sheets) == 0 {
		t.Fatal("没有注册的面板")
	}
	for _, sheet := range sheets {
		sheet := sheet
		opened = opened[:0]
		st.mu.Lock()
		st.openSheet = sheet.ID
		st.sheetJumpRow = ""
		st.mu.Unlock()
		for i := 0; i < 3; i++ {
			tt.Frame()
			openID, _ := settingsTreeSheetState(st)
			if openID == sheet.ID {
				continue
			}
			if openID == "" {
				// 正文渲染时自行关闭面板是允许的（例如 Magpie 数据面板），只要没换成别的。
				break
			}
			t.Fatalf("面板 %q 渲染时打开了另一个面板 %q", sheet.ID, openID)
		}
		if len(opened) > 0 {
			t.Fatalf("面板 %q 渲染时调用了打开面板: %v", sheet.ID, opened)
		}
	}
}
