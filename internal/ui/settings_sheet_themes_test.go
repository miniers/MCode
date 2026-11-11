package mcodeui

import (
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func newThemesTester(t *testing.T) (*SettingsState, *ui.Tester) {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Appearance.Theme = config.ThemeLight
	t.Cleanup(st.Close)
	return st, ui.NewTester(func(c *ui.Context) { renderThemesSheet(c, st) }, 1000, 4000)
}

func TestThemeControl_CustomSelectionAndReset(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Appearance.Themes = []config.ThemeDef{{ID: "custom", Name: "测试主题", Base: config.ThemeLight,
		Colors: map[string]string{"accent": "#123456"}, Fonts: map[string]int{"code": 16}}}
	before := cloneConfig(st.cfg).Appearance.Themes
	t.Cleanup(st.Close)
	tt := ui.NewTester(func(c *ui.Context) {
		themeControl{}.render(c, st, appearanceThemeSection.Rows[0])
	}, 1000, 600)
	if err := tt.Click("测试主题"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if st.cfg.Appearance.Theme != "custom" {
		t.Fatalf("选用户主题后 Theme = %q", st.cfg.Appearance.Theme)
	}
	for _, name := range []string{"跟随系统", "浅色", "深色"} {
		info, ok := tt.AccessByRole(ui.RoleRadio, name)
		if !ok || info.Checked || info.Selected {
			t.Errorf("用户主题生效时分段 %s 应无选中态：%+v, found=%v", name, info, ok)
		}
	}
	loaded, err := config.Load(st.dataDir)
	if err != nil || loaded.Appearance.Theme != "custom" {
		t.Fatalf("用户主题未落盘：theme=%q, err=%v", loaded.Appearance.Theme, err)
	}
	themeControl{}.reset(&st.cfg)
	if st.cfg.Appearance.Theme != config.ThemeSystem || !reflect.DeepEqual(st.cfg.Appearance.Themes, before) {
		t.Fatalf("还原选择不应修改主题定义：%+v", st.cfg.Appearance)
	}
}

func TestThemesSheet_AccentEditAndReset(t *testing.T) {
	st, tt := newThemesTester(t)
	if _, ok := tt.Find("theme-reset-color-accent"); ok {
		t.Fatal("未编辑时不应显示还原按钮")
	}
	if err := tt.Click("编辑accent"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Hex"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("#12345680")
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	theme, _ := st.cfg.Appearance.ThemeByID(config.ThemeLight)
	if theme.Colors["accent"] != "#12345680" {
		t.Fatalf("颜色编辑未写回配置：%v", theme.Colors)
	}
	if err := tt.Click("theme-reset-color-accent"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	theme, _ = st.cfg.Appearance.ThemeByID(config.ThemeLight)
	if _, ok := theme.Colors["accent"]; ok {
		t.Fatal("还原后仍保留颜色覆盖项")
	}
	if _, ok := tt.Find("theme-reset-color-accent"); ok {
		t.Fatal("还原后仍显示还原按钮")
	}
}

func TestThemesSheet_BuiltinAndAdd(t *testing.T) {
	st, tt := newThemesTester(t)
	for _, name := range []string{"浅色", "深色"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if tt.HasText("删除主题") {
			t.Fatalf("内置主题 %s 不应提供删除按钮", name)
		}
	}
	if err := tt.Click("新增"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	page := getThemesSheetState(st)
	theme, ok := st.cfg.Appearance.ThemeByID(page.editingID)
	if !ok || config.IsBuiltinTheme(theme.ID) || theme.Base != config.ThemeDark || !tt.HasText("删除主题") {
		t.Fatalf("新增后未进入副本编辑：id=%q, theme=%+v", page.editingID, theme)
	}
	if _, ok := tt.Find("主题名称"); !ok {
		t.Fatal("新增主题后缺少名称输入框")
	}
}

func TestThemesSheet_ExportImport(t *testing.T) {
	st, tt := newThemesTester(t)
	mutateTheme(st, func(a *config.AppearanceConfig) {
		for key, value := range map[string]string{"accent": "#12345680", "canvas": "#ABCDEF"} {
			if err := a.SetThemeColor(config.ThemeLight, key, value); err != nil {
				t.Error(err)
			}
		}
		for key, value := range map[string]int{"sidebar": 130, "code": 16, "terminal": 18} {
			if err := a.SetThemeFont(config.ThemeLight, key, value); err != nil {
				t.Error(err)
			}
		}
	})
	before, _ := st.cfg.Appearance.ThemeByID(config.ThemeLight)
	data, err := st.cfg.Appearance.ExportTheme(config.ThemeLight)
	if err != nil {
		t.Fatal(err)
	}
	page := getThemesSheetState(st)
	if !importSheetTheme(st, page, string(data)) {
		t.Fatal("导入导出的主题失败")
	}
	tt.Frame()
	after, ok := st.cfg.Appearance.ThemeByID(page.editingID)
	if !ok || after.ID == before.ID || after.Base != before.Base || !reflect.DeepEqual(after.Colors, before.Colors) || !reflect.DeepEqual(after.Fonts, before.Fonts) {
		t.Fatalf("导入导出内容不一致：before=%+v, after=%+v", before, after)
	}
	count, editingID := len(st.cfg.Appearance.Themes), page.editingID
	if importSheetTheme(st, page, `{"base":"invalid"}`) || len(st.cfg.Appearance.Themes) != count || page.editingID != editingID {
		t.Fatal("非法导入修改了主题或编辑状态")
	}
}

func TestFormatThemeColor(t *testing.T) {
	for _, tc := range []struct {
		color ui.Color
		want  string
	}{
		{ui.Color{R: 18, G: 52, B: 86, A: 255}, "#123456"},
		{ui.Color{R: 18, G: 52, B: 86, A: 128}, "#12345680"},
		{ui.Color{}, "#00000000"},
	} {
		if got := formatThemeColor(tc.color); got != tc.want {
			t.Errorf("formatThemeColor(%+v) = %s, want %s", tc.color, got, tc.want)
		}
	}
}

func TestThemesSheet_DebouncedSave(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	var saves atomic.Int64
	settingsSaveHook = func() { saves.Add(1) }
	t.Cleanup(func() { settingsSaveHook = nil })
	t.Cleanup(st.Close)
	for _, value := range []string{"#123456", "#654321", "#ABCDEF80"} {
		mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeColor(config.ThemeLight, "accent", value) })
		if !st.flushTextSave(false) {
			t.Fatal("防抖检查失败")
		}
	}
	if saves.Load() != 0 {
		t.Fatal("连续改色在防抖期内落盘")
	}
	time.Sleep(settingsTextSaveDebounce + settingsTextSaveGrace)
	if !st.flushTextSave(false) || !st.flushTextSave(false) || saves.Load() != 1 {
		t.Fatalf("连续改色应只落盘一次：%d", saves.Load())
	}
	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	theme, _ := loaded.Appearance.ThemeByID(config.ThemeLight)
	if theme.Colors["accent"] != "#ABCDEF80" {
		t.Fatalf("没有保存最后一次改色：%v", theme.Colors)
	}
}

func TestThemesSheet_StateLifecycle(t *testing.T) {
	st, tt := newThemesTester(t)
	page := getThemesSheetState(st)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if getThemesSheetState(st) != page {
				t.Error("同一设置面板没有共用编辑态")
			}
		})
	}
	wg.Wait()
	color, font := page.colors["light/accent"], page.fonts["light/code"]
	tt.Frame()
	if color == nil || font == nil || page.colors["light/accent"] != color || page.fonts["light/code"] != font {
		t.Fatal("重绘更换了控件持有的值地址")
	}
	other := NewSettingsState(settingsTestDir(t), nil, nil)
	if getThemesSheetState(other) == page {
		t.Fatal("不同设置面板共用编辑态")
	}
	other.Close()
	st.Close()
	if st.themesPage != nil || other.themesPage != nil {
		t.Fatal("关闭后没有清理编辑态")
	}
	if getThemesSheetState(st) == page {
		t.Fatal("关闭后仍复用旧编辑态")
	}
}
