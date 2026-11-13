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

// TestSettingsPageAppearance_Tree 验证「外观」页已经走声明式页面树：
// 两节标题都在、原高级区三行直接可见、锁定档位显示徽标、切换主题写回配置。
func TestSettingsPageAppearance_Tree(t *testing.T) {
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })

	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, map[string]bool{"code-font": true}, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderPageAppearance(c, st) }, 1000, 3000)

	texts := make(map[string]bool)
	for _, text := range tt.Texts() {
		texts[text] = true
	}
	for _, want := range []string{
		"主题与排版", "字体",
		"主题", "编辑主题", "界面密度", "内容宽度", "紧凑侧边栏",
		"代码字体", "锁定",
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
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) { renderPageAppearance(c, st) }, 1000, 3000)
	for _, title := range []string{"文字大小", "代码字号", "终端字号", "编辑器字号"} {
		if tt.HasText(title) {
			t.Fatalf("外观页仍有旧字号行 %s", title)
		}
	}
	if !tt.HasText("编辑主题") {
		t.Fatal("缺少主题入口")
	}
}

func TestSettingsAppearanceMotion(t *testing.T) {
	previous := appReduceMotion.Load()
	t.Cleanup(func() { appReduceMotion.Store(previous) })
	appReduceMotion.Store(false)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	var reduced bool
	tt := ui.NewTester(func(c *ui.Context) { renderPageAppearance(c, st); reduced = reduceMotion(c) }, 1000, 3000)
	tt.SetPreferences(ui.Preferences{ReduceMotion: false})
	tt.Frame()
	info, ok := tt.Access("切换动效")
	if !ok || !info.Checked {
		t.Fatal("动效默认应开启")
	}
	if err := tt.Click("切换动效"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !st.cfg.Appearance.ReduceMotion || !reduced {
		t.Fatal("关闭动效后应立即生效")
	}
	applySettingsPageReset(st, "appearance")
	tt.Frame()
	info, ok = tt.Access("切换动效")
	if !ok || !info.Checked || st.cfg.Appearance.ReduceMotion || reduced {
		t.Fatal("重置后动效应恢复开启")
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if reduced {
		t.Fatal("系统减少动效偏好不应覆盖应用开关")
	}
}
