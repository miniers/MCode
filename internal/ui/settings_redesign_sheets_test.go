package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/testenv"
)

func sheetRectContains(outer, inner ui.Rect) bool {
	return inner.X >= outer.X-1 &&
		inner.Y >= outer.Y-1 &&
		inner.X+inner.W <= outer.X+outer.W+1 &&
		inner.Y+inner.H <= outer.Y+outer.H+1
}

func TestSettingsRedesignSheets_ShortcutsResetInFooter(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	t.Cleanup(st.Close)
	t.Cleanup(func() { modalDragReset("settings-sheet-shortcuts") })

	openSettingsSheet(st, "shortcuts", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetShortcuts) }, 1200, 1000)
	tt.Frame()

	footer, ok := tt.Find("面板操作栏 快捷键")
	if !ok {
		t.Fatalf("shortcuts 缺少 Footer：%v", tt.Texts())
	}
	resetBtn, ok := tt.Find("全部恢复默认")
	if !ok {
		t.Fatalf("shortcuts 缺少「全部恢复默认」按钮：%v", tt.Texts())
	}
	if !sheetRectContains(footer, resetBtn) {
		t.Fatalf("「全部恢复默认」应在 Footer 内：footer=%+v btn=%+v", footer, resetBtn)
	}
}

func TestSettingsRedesignSheets_ThemesHeaderAndAccentFirst(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	restoreActiveTheme(t)
	t.Cleanup(st.Close)
	t.Cleanup(func() { modalDragReset("settings-sheet-themes") })

	openSettingsSheet(st, "themes", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetThemes) }, 1200, 1400)
	tt.Frame()

	title, ok := tt.Find("主题")
	if !ok {
		t.Fatalf("themes 头部缺少标题「主题」：%v", tt.Texts())
	}
	closeBtn, ok := tt.Find("关闭主题")
	if !ok {
		t.Fatalf("themes 头部缺少关闭按钮：%v", tt.Texts())
	}
	tile, ok := tt.Find("浅色")
	if !ok {
		t.Fatalf("themes 正文缺少主题缩略块「浅色」：%v", tt.Texts())
	}
	if title.Y > tile.Y || closeBtn.Y > tile.Y {
		t.Fatalf("主题缩略块应位于正文顶部（标题之后）：title=%+v close=%+v tile=%+v", title, closeBtn, tile)
	}

	prevY := tile.Y
	for _, heading := range []string{"强调", "背景", "文字", "状态与线条"} {
		r, ok := tt.Find(heading)
		if !ok {
			t.Fatalf("themes 缺少颜色分节 %q：%v", heading, tt.Texts())
		}
		if r.Y <= prevY {
			t.Fatalf("themes 分节 %q 顺序错误：Y=%v <= prevY=%v", heading, r.Y, prevY)
		}
		prevY = r.Y
	}
	for _, txt := range tt.Texts() {
		if txt == "状态" || txt == "线条" {
			t.Fatalf("状态与线条应合并为「状态与线条」：%v", tt.Texts())
		}
	}
}

func TestSettingsRedesignSheets_ProviderAddNoBackAndAddInFooter(t *testing.T) {
	testenv.TempHome(t)
	sh, ok := settingsTreeSheetOf("provider-add")
	if !ok {
		t.Fatal("缺少 provider-add 面板")
	}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api"}}
	st.presets.addType = 2
	t.Cleanup(st.Close)
	t.Cleanup(func() { modalDragReset("settings-sheet-provider-add") })

	openSettingsSheet(st, "provider-add", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sh) }, 1200, 1000)
	tt.Frame()

	if tt.HasText("← API Key") {
		t.Fatalf("provider-add 不应有返回按钮「← API Key」：%v", tt.Texts())
	}
	if !tt.HasText("预设") || !tt.HasText("DeepSeek") || !tt.HasText("自定义端点") {
		t.Fatalf("provider-add 应在同张卡片内提供「预设」切换：%v", tt.Texts())
	}
	footer, ok := tt.Find("面板操作栏 添加供应商")
	if !ok {
		t.Fatalf("provider-add 缺少 Footer：%v", tt.Texts())
	}
	addBtn, ok := tt.Find("添加")
	if !ok {
		t.Fatalf("provider-add 缺少「添加」按钮：%v", tt.Texts())
	}
	if !sheetRectContains(footer, addBtn) {
		t.Fatalf("「添加」应在 Footer 内：footer=%+v btn=%+v", footer, addBtn)
	}
}

func TestSettingsRedesignSheets_QuickRepliesFooterDone(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	t.Cleanup(st.Close)
	t.Cleanup(func() { modalDragReset("settings-sheet-quick-replies") })

	openSettingsSheet(st, "quick-replies", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetQuickReplies) }, 1200, 1000)
	tt.Frame()

	if tt.HasText("快捷回复列表") {
		t.Fatalf("quick-replies 不应显示分节标题「快捷回复列表」：%v", tt.Texts())
	}
	footer, ok := tt.Find("面板操作栏 快捷回复")
	if !ok {
		t.Fatalf("quick-replies 缺少 Footer：%v", tt.Texts())
	}
	for _, label := range []string{"添加一条", "完成"} {
		btn, ok := tt.Find(label)
		if !ok {
			t.Fatalf("quick-replies 缺少按钮 %q：%v", label, tt.Texts())
		}
		if !sheetRectContains(footer, btn) {
			t.Fatalf("%q 应在 Footer 内：footer=%+v btn=%+v", label, footer, btn)
		}
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if openID, _ := settingsTreeSheetState(st); openID != "" {
		t.Fatalf("点击「完成」应关闭面板，实际 openSheet=%q", openID)
	}
}

func TestSettingsRedesignSheets_OpenInDefaultBadgeAndFooter(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Workbench.OpenInApps = []config.OpenInAppConfig{
		{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: false},
		{Name: "Zed", AppPath: "/Applications/Zed.app", Enabled: true},
		{Name: "Xcode", AppPath: "/Applications/Xcode.app", Enabled: true},
	}
	t.Cleanup(st.Close)
	t.Cleanup(func() { modalDragReset("settings-sheet-open-in") })

	openSettingsSheet(st, "open-in", "")
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetOpenIn) }, 1200, 1000)
	tt.Frame()

	if tt.HasText("应用列表") {
		t.Fatalf("open-in 不应显示分节标题「应用列表」：%v", tt.Texts())
	}
	defBadge, ok := tt.Find("默认")
	if !ok {
		t.Fatalf("open-in 首个已启用应用应显示「默认」徽标：%v", tt.Texts())
	}
	zedText, ok := tt.Find("Zed")
	if !ok {
		t.Fatalf("open-in 缺少 Zed 行：%v", tt.Texts())
	}
	if defBadge.Y < zedText.Y-4 || defBadge.Y > zedText.Y+zedText.H+4 || defBadge.X <= zedText.X {
		t.Fatalf("「默认」徽标应位于 Zed 标题旁：zed=%+v badge=%+v", zedText, defBadge)
	}

	footer, ok := tt.Find("面板操作栏 打开方式")
	if !ok {
		t.Fatalf("open-in 缺少 Footer：%v", tt.Texts())
	}
	doneBtn, ok := tt.Find("完成")
	if !ok {
		t.Fatalf("open-in 缺少「完成」按钮：%v", tt.Texts())
	}
	if !sheetRectContains(footer, doneBtn) {
		t.Fatalf("「完成」应在 Footer 内：footer=%+v btn=%+v", footer, doneBtn)
	}
	if err := tt.Click("完成"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if openID, _ := settingsTreeSheetState(st); openID != "" {
		t.Fatalf("点击「完成」应关闭面板，实际 openSheet=%q", openID)
	}
}

func TestSettingsRedesignSheets_MagpieFootersAndPersonaOrder(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	gw := CustomEndpoint{ID: "gw", Name: "本地 Magpie", BaseURL: magpieDefaultBaseURL, Magpie: true, Enabled: true, HasAPIKey: true}
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{gw}
	t.Cleanup(st.Close)
	t.Cleanup(func() {
		modalDragReset("settings-sheet-magpie-gateway")
		modalDragReset("settings-sheet-magpie-personas")
	})

	openMagpieGatewaySheet(st, "gw")
	ttGw := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetMagpieGateway) }, 1200, 1200)
	ttGw.Frame()

	gwFooter, ok := ttGw.Find("面板操作栏 Magpie 网关")
	if !ok {
		t.Fatalf("magpie-gateway 缺少 Footer：%v", ttGw.Texts())
	}
	for _, label := range []string{"测试连接", "保存", "取消"} {
		btn, ok := ttGw.Find(label)
		if !ok || !sheetRectContains(gwFooter, btn) {
			t.Fatalf("magpie-gateway Footer 缺少或未包含 %q：footer=%+v btn=%+v", label, gwFooter, btn)
		}
	}
	closeSettingsSheet(st)

	openSettingsSheet(st, "magpie-personas", "")
	ttPersona := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSheet(c, st, sheetMagpiePersonas) }, 1200, 1200)
	ttPersona.Frame()

	pFooter, ok := ttPersona.Find("面板操作栏 人设")
	if !ok {
		t.Fatalf("magpie-personas 缺少 Footer：%v", ttPersona.Texts())
	}
	for _, label := range []string{"测试人设", "保存", "取消"} {
		btn, ok := ttPersona.Find(label)
		if !ok || !sheetRectContains(pFooter, btn) {
			t.Fatalf("magpie-personas Footer 缺少或未包含 %q：footer=%+v btn=%+v", label, pFooter, btn)
		}
	}
	rules, ok1 := ttPersona.Find("人设规则")
	manual, ok2 := ttPersona.Find("手动人设列表")
	if !ok1 || !ok2 || rules.Y >= manual.Y {
		t.Fatalf("magpie-personas 应按「人设规则 → 手动人设列表」顺序排列：rules=%+v manual=%+v", rules, manual)
	}
}
