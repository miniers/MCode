package mcodeui

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSettingsDetail_SyncAppearanceTheme(t *testing.T) {
	previousApply, previousTheme := applyThemeSource, appearanceThemeApplied
	t.Cleanup(func() {
		applyThemeSource = previousApply
		appearanceThemeApplied = previousTheme
		setActiveTheme(config.AppearanceConfig{})
	})
	var applied []mygo.ThemeSource
	applyThemeSource = func(source mygo.ThemeSource) { applied = append(applied, source) }
	appearanceThemeApplied = ""

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.currentPage = "appearance"
	st.cfg.Appearance.Theme = config.ThemeLight
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 2400)
	tt.Frame()
	if len(applied) != 1 || applied[0] != mygo.ThemeSource(config.ThemeLight) {
		t.Fatalf("浅色主题调用 = %v，想要 [light]", applied)
	}

	st.cfg.Appearance.Theme = config.ThemeDark
	tt.Frame()
	if len(applied) != 2 || applied[1] != mygo.ThemeSource(config.ThemeDark) {
		t.Fatalf("深色主题调用 = %v，想要 [light dark]", applied)
	}
	tt.Frame()
	if len(applied) != 2 {
		t.Fatalf("主题未变化时不应重复调用：%v", applied)
	}

	st.cfg.Appearance.Theme = "custom"
	st.cfg.Appearance.Themes = []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{"accent": "#123456"}}}
	tt.Frame()
	if len(applied) != 3 || applied[2] != mygo.ThemeSource(config.ThemeLight) || activeTheme.Load().fixed.Accent != ui.Hex("#123456") {
		t.Fatalf("用户主题未使用明暗基底: %v", applied)
	}
	st.cfg.Appearance.Themes[0].Colors["accent"] = "#654321"
	tt.Frame()
	if len(applied) != 4 || activeTheme.Load().fixed.Accent != ui.Hex("#654321") {
		t.Fatalf("同一主题颜色修改未刷新: %v", applied)
	}
	tt.Frame()
	if len(applied) != 4 {
		t.Fatalf("主题内容未变却重复同步: %v", applied)
	}
}

// TestAppearanceSettings_TextScale 验证「文字大小」档位切换会写回配置并落盘，
// 旧字段仍可保存；运行时字号改由主题提供。
func TestAppearanceSettings_TextScale(t *testing.T) {
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })

	dataDir := settingsTestDir(t)
	s := newChatState(NewFakeClient())
	st := NewSettingsState(dataDir, nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderPageAppearance(c, st) }, 1000, 2400)

	// 行标题与当前档位都要可见；说明句已经删除，布局自己解释。
	for _, text := range []string{"文字大小", "100%"} {
		if !tt.HasText(text) {
			t.Fatalf("外观设置页缺少文案 %q: %v", text, tt.Texts())
		}
	}
	if tt.HasText("只缩放侧栏和对话内容，窗口其他部分保持原样") {
		t.Fatalf("外观设置页不该再有说明句: %v", tt.Texts())
	}

	// Tester.Click 按元素显示的文本匹配（不是 c.Key），130 档按钮上显示的是 "130%"。
	if err := tt.Click("130%"); err != nil {
		t.Fatalf("click text scale option failed: %v", err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Appearance.TextScale == 130 })

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if cfg.Appearance.TextScale != 130 {
		t.Fatalf("落盘的文字大小 = %d，想要 130", cfg.Appearance.TextScale)
	}
	if got := textScaleFactor(); got != 1 {
		t.Fatalf("旧字段不应影响运行时缩放，实际 %v", got)
	}
	if got := scaledFont(10); got != 10 {
		t.Fatalf("scaledFont(10) = %v，想要 10", got)
	}

	// 越界值（含缺省的 0）一律按 100 处理。
	for _, percent := range []int{0, 120, -5, 200} {
		got := (config.AppearanceConfig{TextScale: percent}).EffectiveTextScale()
		if got != config.DefaultTextScale {
			t.Fatalf("EffectiveTextScale(%d) = %d，想要 %d", percent, got, config.DefaultTextScale)
		}
	}
	if got := (config.AppearanceConfig{TextScale: 150}).EffectiveTextScale(); got != 150 {
		t.Fatalf("EffectiveTextScale(150) = %d，想要 150", got)
	}
}

// TestSettingsPageAppearance_Tree 验证「外观」页已经走声明式页面树：
// 两节标题都在、原高级区三行直接可见、锁定档位显示徽标、切换主题写回配置。
func TestSettingsPageAppearance_Tree(t *testing.T) {
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })

	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, map[string]bool{"code-font-size": true}, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderPageAppearance(c, st) }, 1000, 3000)

	texts := make(map[string]bool)
	for _, text := range tt.Texts() {
		texts[text] = true
	}
	for _, want := range []string{
		"主题与排版", "字体与字号",
		"主题", "文字大小", "界面密度", "内容宽度", "紧凑侧边栏",
		"代码字体", "代码字号", "锁定",
	} {
		if !texts[want] {
			t.Fatalf("外观页缺少 %q: %v", want, tt.Texts())
		}
	}

	// 原高级区的行已经并入普通分节，页面底部不该再有折叠入口。
	if tt.HasText("高级（3 项）") {
		t.Fatalf("外观页不该再有高级折叠区: %v", tt.Texts())
	}

	// 选「深色」后配置立即变成 dark，并落盘。
	if err := tt.Click("深色"); err != nil {
		t.Fatalf("click theme option failed: %v", err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Appearance.Theme == config.ThemeDark })

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if cfg.Appearance.Theme != config.ThemeDark {
		t.Fatalf("落盘的主题 = %q，想要 %q", cfg.Appearance.Theme, config.ThemeDark)
	}
}

func TestSettingsAppearance_FontSettingsMoved(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderPageAppearance(c, st) }, 1000, 3000)
	for _, title := range []string{"终端字号", "编辑器字号"} {
		if n := countText(tt, title); n != 1 {
			t.Fatalf("%q 出现 %d 次", title, n)
		}
	}
	for _, id := range []string{"row-terminal-font-size", "row-editor-font-size"} {
		var found []numberControl
		for _, page := range []settingsPage{pageAppearance, pageWorkbench} {
			for _, section := range page.Sections {
				for _, row := range section.Rows {
					if row.ID == id {
						if page.ID != "appearance" || section.ID != "appearance.fonts" {
							t.Fatalf("%s 位于错误页面或分节", id)
						}
						found = append(found, row.Control.(numberControl))
					}
				}
			}
		}
		if len(found) != 1 {
			t.Fatalf("%s 出现 %d 次", id, len(found))
		}
		ctl := found[0]
		commitSettingsControl(st, func(cfg *config.Config) { ctl.b.write(cfg, ctl.b.def+1) })
		cfg, err := config.Load(dataDir)
		if err != nil || ctl.b.value(&cfg) != ctl.b.def+1 {
			t.Fatalf("%s 保存失败: %v", id, err)
		}
	}
	resetSettingsTreePage(st, pageWorkbench)
	if st.cfg.Workbench.TerminalFontSize != config.DefaultTerminalFontSize+1 || st.cfg.Workbench.EditorFontSize != config.DefaultEditorFontSize+1 {
		t.Fatal("工作台重置不应影响字号")
	}
	resetSettingsTreePage(st, pageAppearance)
	if st.cfg.Workbench.TerminalFontSize != config.DefaultTerminalFontSize || st.cfg.Workbench.EditorFontSize != config.DefaultEditorFontSize {
		t.Fatal("外观重置应恢复字号")
	}
}
