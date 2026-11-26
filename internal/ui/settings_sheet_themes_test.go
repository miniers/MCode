package mcodeui

import (
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func restoreActiveTheme(t *testing.T) {
	t.Helper()
	prev := activeTheme.Load()
	// t.Cleanup 按后进先出执行，需在 st.Close 之前注册，以在 Close 写入主题后再还原全局 activeTheme。
	t.Cleanup(func() { activeTheme.Store(prev) })
}

func newThemesTester(t *testing.T) (*SettingsState, *ui.Tester) {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Appearance.Theme = config.ThemeLight
	restoreActiveTheme(t)
	t.Cleanup(st.Close)
	return st, ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			renderThemesSheet(c, st)
			renderThemeActions(c, st, getThemesSheetState(st))
		})
	}, 1000, 4000)
}

func TestThemeControl_ColorfulBuiltinSelection(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Appearance.Theme = config.ThemeColorfulLight
	restoreActiveTheme(t)
	setAppReduceMotion(t, true)
	t.Cleanup(st.Close)
	tt := ui.NewTester(func(c *ui.Context) {
		themeControl{}.render(c, st, appearanceThemeSection.Rows[0])
	}, 1000, 600)

	if !tt.HasText("多彩浅色") || !tt.HasText("多彩深色") {
		t.Fatalf("外观页缺少内置彩色主题：%v", tt.Texts())
	}
	for _, name := range []string{"跟随系统", "浅色", "深色"} {
		info, ok := tt.AccessByRole(ui.RoleRadio, name)
		if !ok || info.Checked || info.Selected {
			t.Errorf("彩色主题生效时分段 %s 应无选中态：%+v, found=%v", name, info, ok)
		}
	}
	if st.cfg.Appearance.Theme != config.ThemeColorfulLight {
		t.Fatalf("渲染不应修改当前主题：%q", st.cfg.Appearance.Theme)
	}
}

func TestThemeControl_CustomSelectionAndReset(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Appearance.Themes = []config.ThemeDef{{ID: "custom", Name: "测试主题", Base: config.ThemeLight,
		Colors: map[string]string{"accent": "#123456"}, Fonts: map[string]int{"code": 16}}}
	before := cloneConfig(st.cfg).Appearance.Themes
	restoreActiveTheme(t)
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
	if _, ok := tt.Find("还原强调色"); ok {
		t.Fatal("未编辑时不应显示还原按钮")
	}
	if err := tt.Click("编辑强调色"); err != nil {
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
	if err := tt.Click("还原强调色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	theme, _ = st.cfg.Appearance.ThemeByID(config.ThemeLight)
	if _, ok := theme.Colors["accent"]; ok {
		t.Fatal("还原后仍保留颜色覆盖项")
	}
	if _, ok := tt.Find("还原强调色"); ok {
		t.Fatal("还原后仍显示还原按钮")
	}
}

func TestThemesSheet_BuiltinAndAdd(t *testing.T) {
	st, tt := newThemesTester(t)
	for _, name := range []string{"浅色", "深色", "多彩浅色", "多彩深色"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if tt.HasText("删除主题") {
			t.Fatalf("内置主题 %s 不应提供删除按钮", name)
		}
	}
	if err := tt.Click("添加主题"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
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
	origDebounce, origGrace := settingsTextSaveDebounce, settingsTextSaveGrace
	settingsTextSaveDebounce = 30 * time.Millisecond
	settingsTextSaveGrace = 15 * time.Millisecond
	t.Cleanup(func() {
		settingsTextSaveDebounce = origDebounce
		settingsTextSaveGrace = origGrace
	})
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	var saves atomic.Int64
	settingsSaveHook = func() { saves.Add(1) }
	t.Cleanup(func() { settingsSaveHook = nil })
	restoreActiveTheme(t)
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

func TestThemesSheet_LayoutAndValues(t *testing.T) {
	st, tt := newThemesTester(t)
	for _, name := range []string{"浅色", "深色", "多彩浅色", "多彩深色"} {
		info, ok := tt.AccessByRole(ui.RoleButton, name)
		if !ok || info.Checked != (name == "浅色") {
			t.Fatalf("主题选择状态 %s: %+v, %v", name, info, ok)
		}
	}
	if err := tt.Click("深色"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	info, ok := tt.AccessByRole(ui.RoleButton, "深色")
	if !ok || !info.Checked || getThemesSheetState(st).editingID != config.ThemeDark {
		t.Fatalf("未选中深色: %+v", info)
	}
	mutateTheme(st, func(a *config.AppearanceConfig) {
		_ = a.SetThemeColor(config.ThemeDark, "accent", "#123456")
		_ = a.SetThemeFont(config.ThemeDark, "sidebar", 107)
	})
	tt.Frame()
	if !tt.HasText("#123456") || !tt.HasText("107%") {
		t.Fatal("未显示颜色或字号")
	}
	if err := tt.Click("强调色跟随系统"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("系统") {
		t.Fatal("未显示系统强调色")
	}
	if st.cfg.Appearance.ThemeFonts(config.ThemeDark)["sidebar"] != 107 {
		t.Fatal("非档位字号未保留")
	}
	if err := tt.Click("侧栏字号"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if got := st.cfg.Appearance.ThemeFonts(config.ThemeDark)["sidebar"]; got == 107 || got < config.MinTextScale || got > config.MaxTextScale {
		t.Fatalf("滑块未更新字号: %d", got)
	}

	if err := tt.Click("侧栏字号"); err != nil {
		t.Fatal(err)
	}
	mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeFont(config.ThemeDark, "sidebar", 189) })
	tt.Frame()
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if got := st.cfg.Appearance.ThemeFonts(config.ThemeDark)["sidebar"]; got != 190 || !tt.HasText("190%") {
		t.Fatalf("滑块字号或显示不正确：%d", got)
	}
	st.flushTextSave(true)
	loaded, err := config.Load(st.dataDir)
	if err != nil || loaded.Appearance.ThemeFonts(config.ThemeDark)["sidebar"] != 190 {
		t.Fatalf("字号未保存：%v, %v", loaded.Appearance.ThemeFonts(config.ThemeDark), err)
	}

	mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeFont(config.ThemeDark, "sidebar", 250) })
	tt.Frame()
	if !tt.HasText("200%") {
		t.Fatal("字号未限制到上限")
	}
	reference := ui.NewTester(func(c *ui.Context) {
		SettingRow(c, "reference", "普通行", func() { ui.Box(c).Size(Space6, Space6) }).Label("reference")
	}, 1000, 200)
	normal, ok := reference.Find("reference")
	if !ok {
		t.Fatal("缺少普通行")
	}
	// 字号卡片全是普通行，平摊后的行高与普通行一致。
	fontsRect, ok := tt.Find("设置卡片 theme-fonts")
	if !ok {
		t.Fatal("缺少卡片 设置卡片 theme-fonts")
	}
	fontRows := len(config.ThemeFontKeys)
	fontHeight := (fontsRect.H - 2*Hairline - float32(fontRows-1)*Hairline) / float32(fontRows)
	if math.Abs(float64(fontHeight-normal.H)) > 1 {
		t.Errorf("卡片 设置卡片 theme-fonts 的行高=%v，普通行=%v", fontHeight, normal.H)
	}
	// 色块回到 Space6 后颜色行与普通行同高：行高由 SettingRowKeyedTall 的右侧控件决定。
	backgroundRows := float32(0)
	for _, section := range colorSections {
		if section.title == "背景" {
			backgroundRows = float32(len(section.keys))
		}
	}
	colorRect, ok := tt.Find("设置卡片 theme-section-背景")
	if !ok {
		t.Fatal("缺少卡片 设置卡片 theme-section-背景")
	}
	colorHeight := (colorRect.H - 2*Hairline - (backgroundRows-1)*Hairline) / backgroundRows
	if math.Abs(float64(colorHeight-normal.H)) > 1 {
		t.Errorf("颜色行高=%v，普通行=%v，色块不应撑高颜色行", colorHeight, normal.H)
	}
}

func TestUIReview14_ThemeSheetA11yAndResetPlaceholder(t *testing.T) {
	st, tt := newThemesTester(t)

	// 顺序须与设置页颜色行一致（colorSections 拼接顺序），逐键校验标签。
	colorTitles := map[string]string{"canvas": "窗口", "main": "主区域", "card": "卡片", "raised": "浮起", "popover": "浮层", "code_background": "代码底色", "composer_background": "输入框", "table_header": "表头", "text": "主文字", "text_secondary": "次要文字", "text_tertiary": "辅助文字", "code_text": "代码文字", "accent": "强调色", "danger": "危险", "warning": "警告", "success": "成功", "caution": "提醒", "plan": "Plan", "code_heading": "标题", "code_muted": "次要标记", "code_link": "链接", "code_strong": "粗体", "quote_text": "引用文字", "quote_bar": "引用竖线", "user_message_background": "用户消息", "border": "边框", "divider": "分隔线"}
	fontTitles := []string{"侧栏", "对话", "界面", "代码", "终端", "编辑器"}

	beforeColorRect, ok := tt.Find("编辑强调色")
	if !ok {
		t.Fatal("缺少色块按钮 编辑强调色")
	}
	beforeSliderRect, ok := tt.Find("侧栏字号")
	if !ok {
		t.Fatal("缺少字号滑块 侧栏字号")
	}

	mutateTheme(st, func(a *config.AppearanceConfig) {
		for _, key := range config.ThemeColorKeys {
			_ = a.SetThemeColor(config.ThemeLight, key, "#123456")
		}
		for _, key := range config.ThemeFontKeys {
			lower, _, _, _ := config.ThemeFontRange(key)
			_ = a.SetThemeFont(config.ThemeLight, key, lower)
		}
	})
	tt.Frame()

	for _, key := range config.ThemeColorKeys {
		colorTitles, ok := colorTitles[key]
		if !ok {
			t.Fatalf("缺少 %s 的显示名，请在 colorTitles 里补齐", key)
		}
		editLabel := "编辑" + colorTitles
		if _, ok := tt.Access(editLabel); !ok {
			t.Errorf("缺少颜色按钮无障碍标签 %q", editLabel)
		}
		resetLabel := "还原" + colorTitles
		if _, ok := tt.Access(resetLabel); !ok {
			t.Errorf("缺少颜色还原按钮无障碍标签 %q", resetLabel)
		}
		if _, ok := tt.Access("编辑" + key); ok {
			t.Errorf("颜色按钮不应暴露内部 key: 编辑%s", key)
		}
		if _, ok := tt.Access("theme-reset-color-" + key); ok {
			t.Errorf("颜色还原按钮不应暴露内部 key: theme-reset-color-%s", key)
		}
	}

	for i, key := range config.ThemeFontKeys {
		sliderLabel := fontTitles[i] + "字号"
		if _, ok := tt.Access(sliderLabel); !ok {
			t.Errorf("缺少字号滑块无障碍标签 %q", sliderLabel)
		}
		resetLabel := "还原" + fontTitles[i]
		if _, ok := tt.Access(resetLabel); !ok {
			t.Errorf("缺少字号还原按钮无障碍标签 %q", resetLabel)
		}
		if _, ok := tt.Access(key + "字号"); ok {
			t.Errorf("字号滑块不应暴露内部 key: %s字号", key)
		}
		if _, ok := tt.Access("theme-reset-font-" + key); ok {
			t.Errorf("字号还原按钮不应暴露内部 key: theme-reset-font-%s", key)
		}
	}

	isASCIIOnly := func(s string) bool {
		if s == "" {
			return false
		}
		for _, r := range s {
			if r > 127 {
				return false
			}
		}
		return true
	}
	for _, node := range tt.AccessAll() {
		if strings.HasPrefix(node.Name, "还原") && isASCIIOnly(strings.TrimPrefix(node.Name, "还原")) && node.Name != "还原Plan" {
			t.Errorf("还原按钮标签包含纯 ASCII 内部 key: %q", node.Name)
		}
		if strings.HasPrefix(node.Name, "编辑") && isASCIIOnly(strings.TrimPrefix(node.Name, "编辑")) && node.Name != "编辑Plan" {
			t.Errorf("颜色按钮标签包含纯 ASCII 内部 key: %q", node.Name)
		}
		if isASCIIOnly(node.Name) {
			for _, key := range append(append([]string(nil), config.ThemeColorKeys...), config.ThemeFontKeys...) {
				if node.Name == key || strings.Contains(node.Name, "theme-reset-") {
					t.Errorf("无障碍节点包含内部 key: %q", node.Name)
				}
			}
		}
	}

	afterColorRect, ok := tt.Find("编辑强调色")
	if !ok {
		t.Fatal("修改后缺少色块按钮 编辑强调色")
	}
	if afterColorRect.X != beforeColorRect.X {
		t.Errorf("出现还原按钮后色块按钮横向跳动: before=%v after=%v", beforeColorRect.X, afterColorRect.X)
	}
	afterSliderRect, ok := tt.Find("侧栏字号")
	if !ok {
		t.Fatal("修改后缺少字号滑块 侧栏字号")
	}
	if afterSliderRect.X != beforeSliderRect.X {
		t.Errorf("出现还原按钮后字号滑块横向跳动: before=%v after=%v", beforeSliderRect.X, afterSliderRect.X)
	}
	if afterColorRect.W != Space6 || afterColorRect.H != Space6 {
		t.Errorf("色块按钮应回到 Space6（%v），实际 %vx%v", Space6, afterColorRect.W, afterColorRect.H)
	}
	quotingRow, ok := tt.Find("row-theme-color-quote_text")
	if !ok {
		t.Fatal("缺少颜色行 row-theme-color-quote_text")
	}
	accentRow, ok := tt.Find("row-theme-color-accent")
	if !ok {
		t.Fatal("缺少颜色行 row-theme-color-accent")
	}
	if quotingRow.Y <= accentRow.Y {
		t.Errorf("引用文字应排到强调分栏之后（文字组），实际 Y=%v，强调行 Y=%v", quotingRow.Y, accentRow.Y)
	}
}

// 分组键表是设置页的布局事实：四组合起来恰好覆盖 ThemeColorKeys 且互不重复，
// 每个键都能被 config.ValidThemeColor 接受。改分组时这里必须同步。
func TestThemesSheet_ColorKeysAreValid(t *testing.T) {
	seen := make(map[string]bool, len(config.ThemeColorKeys))
	for _, section := range colorSections {
		for _, key := range section.keys {
			if seen[key] {
				t.Fatalf("颜色分组重复包含 %s", key)
			}
			seen[key] = true
		}
	}
	if len(seen) != len(config.ThemeColorKeys) {
		t.Fatalf("颜色分组应有 %d 项，实际 %d", len(config.ThemeColorKeys), len(seen))
	}
	for _, key := range config.ThemeColorKeys {
		if !seen[key] {
			t.Fatalf("颜色分组漏掉 %s", key)
		}
		if !config.ValidThemeColor(key, "#123456") {
			t.Errorf("ThemeColorKeys 的 %s 未被 config 接受", key)
		}
	}
}
