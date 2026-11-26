package mcodeui

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestThemesSheetCodeColorGroup(t *testing.T) {
	_, tt := newThemesTester(t)
	if _, ok := tt.Find("设置卡片 theme-section-代码"); ok {
		t.Fatal("不应再有独立的 代码 分组")
	}
	if !tt.HasText("文字") {
		t.Fatal("缺少分组标题 文字")
	}
	for _, title := range []string{"标题", "次要标记", "链接", "粗体"} {
		if _, ok := tt.Access("编辑" + title); !ok {
			t.Errorf("缺少颜色行 %q", title)
		}
	}
	if _, ok := tt.Access("编辑输入框"); !ok {
		t.Error("缺少颜色行 \"输入框\"")
	}
}

// 输入卡片底色与卡片色一样走调色板：未设置时回退卡片色，主题设置 composer_background 后改用该色。
func TestComposerCardBackground(t *testing.T) {
	const key = "composer_background"
	if !slicesContains(config.ThemeColorKeys, key) {
		t.Fatalf("%s 未登记到 ThemeColorKeys", key)
	}
	setAppReduceMotion(t, true)

	// 阶段一：未设置 composer_background 的干净状态，底色回退内置浅色卡片色。
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	t.Cleanup(st.Close)
	st.cfg.Appearance.Themes = []config.ThemeDef{{ID: "custom", Name: "测试主题", Base: config.ThemeLight}}
	st.cfg.Appearance.Theme = "custom"
	st.cfgMu.Lock()
	base := st.cfg
	st.cfgMu.Unlock()
	cfg := base.Appearance
	cfg.Theme = config.ThemeLight
	palette := resolvePalette(cfg, config.ThemeLight)
	if palette.ComposerBackground != builtinPalette(config.ThemeLight).Card {
		t.Fatalf("未设置 %s 时调色板底色 = %v，期望卡片色 %v", key, palette.ComposerBackground, builtinPalette(config.ThemeLight).Card)
	}
	got := sampleComposerCardBg(t, cfg, config.ThemeLight)
	if expected := rgbOf(builtinPalette(config.ThemeLight).Card); !colorNear(got, expected, 2) {
		t.Fatalf("未设置 %s 时输入卡片底色 = %v，期望卡片色 %v", key, got, expected)
	}

	// 阶段二：写入 composer_background 后改用该色。
	if err := cfg.SetThemeColor(config.ThemeLight, key, "#123456"); err != nil {
		t.Fatalf("SetThemeColor(%s) 失败：%v", key, err)
	}
	snapshot := base
	snapshot.Appearance = cfg
	if err := config.Validate(snapshot); err != nil {
		t.Fatalf("登记后配置校验失败：%v", err)
	}
	palette = resolvePalette(cfg, config.ThemeLight)
	want := ui.Color{R: 0x12, G: 0x34, B: 0x56, A: 255}
	if palette.ComposerBackground != want {
		t.Fatalf("设置 %s=#123456 后调色板底色 = %v，期望 %v", key, palette.ComposerBackground, want)
	}
	if got := sampleComposerCardBg(t, cfg, config.ThemeLight); !colorNear(got, rgbOf(want), 2) {
		t.Fatalf("设置 %s=#123456 后输入卡片底色 = %v，期望 %v", key, got, want)
	}

	// 阶段三：只覆盖卡片色时，未设置的输入框底色跟随卡片色。
	cardCfg := config.AppearanceConfig{Theme: config.ThemeLight}
	if err := cardCfg.SetThemeColor(config.ThemeLight, "card", "#0A0B0C"); err != nil {
		t.Fatalf("SetThemeColor(card) 失败：%v", err)
	}
	if p := resolvePalette(cardCfg, config.ThemeLight); p.ComposerBackground != p.Card || p.ComposerBackground != (ui.Color{R: 0x0A, G: 0x0B, B: 0x0C, A: 255}) {
		t.Fatalf("只设置 card 后输入框底色 = %v，期望跟随卡片色 %v", p.ComposerBackground, p.Card)
	}
}

// 用户路径回归：设置面板的「编辑主题」打开主题页后，输入框与卡片、强调色、Markdown
// 角色各自都能打开拾色器并持续写入该主题的颜色项；标题与主文字同属「文字」组。
func TestSettingsThemesPickerDragInPanel(t *testing.T) {
	setAppReduceMotion(t, true)
	var st *SettingsState
	tt := ui.NewTester(Root(NewFakeClient(), WithSettings(settingsTestDir(t), nil), func(s *chatState) {
		st = getChatSettings(s)
	}), 1400, 1400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	waitFor(t, tt, func() bool { return st != nil })
	st.mu.Lock()
	st.cfg.Appearance.Themes = []config.ThemeDef{{ID: "custom", Name: "测试主题", Base: config.ThemeLight}}
	st.cfg.Appearance.Theme = "custom"
	st.mu.Unlock()
	st.invalidate()
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return st.open })
	tt.Frame()
	st.selectSettingsPage("appearance")
	waitFor(t, tt, func() bool { return tt.HasText("编辑主题") })
	openSettingsSheet(st, "themes", "")
	waitFor(t, tt, func() bool { return st.openSheet == "themes" })
	tt.Frame()
	for _, key := range []string{"accent", "card", "composer_background", "code_heading"} {
		dragGridColor(t, tt, st, key)
	}
}

// colorPanelKey 是该颜色行的拾色面板在 page.colorOpen 里的开关 key，与 renderThemeColor 一致。
func colorPanelKey(id, key string) string { return "theme-color-picker-" + id + "/" + key }

// closeColorPickers 收起上一行还开着的拾色面板。
// 滚轮只是滚动，不算按在面板外（PressedOutside），面板会一直开着并浮在颜色行按钮上，挡住下一次点击。
func closeColorPickers(t *testing.T, tt *ui.Tester, st *SettingsState) {
	t.Helper()
	page := getThemesSheetState(st)
	open := false
	for _, flag := range page.colorOpen {
		if *flag {
			*flag = false
			open = true
		}
	}
	if !open {
		return
	}
	st.invalidate()
	advancePanel(tt)
}

// dragGridColor 打开该颜色行的拾色器并拖动饱和度方块，返回每次松手后主题里的颜色。
func dragGridColor(t *testing.T, tt *ui.Tester, st *SettingsState, key string) []string {
	t.Helper()
	label := colorRowTitles[key]
	// 「文字」组里也有「强调色」，与「强调」组同名；按颜色行顺序把这一行滚进面板可视区。
	closeColorPickers(t, tt, st)
	r := scrollColorRowToButton(t, tt, st, key, label)
	id := getThemesSheetState(st).editingID
	if id == "" {
		t.Fatalf("%s: 主题页没有编辑中的主题", key)
	}
	// 同一文本（如「强调色」）可能出现在两组里，只有滚动到视口的那一行按钮可点，打开的就是这一行。
	if err := tt.Click("编辑" + label); err != nil {
		t.Fatalf("%s: 点击颜色行按钮 %v 失败：%v", key, r, err)
	}
	advancePanel(tt)
	square, ok := colorPickerBounds(tt)
	if !ok {
		t.Fatalf("%s: %q 打开后找不到饱和度方块（按钮在 %v）", key, label, r)
	}
	if square.H != 160 || square.Y < r.Y+r.H {
		// 拾色面板挂在按钮下方：方块完整显示且落在该行按钮之下，才证明打开的是这一行。
		t.Fatalf("%s: 饱和度方块 %v 未显示在按钮 %v 下方", key, square, r)
	}
	var colors []string
	for i := range 4 {
		moveTo := square.X + square.W*(0.3+0.1*float32(i))
		y := square.Y + square.H*0.5
		tt.Press(square.X+square.W*0.2, y)
		tt.Frame()
		tt.Move(moveTo, y)
		tt.Frame()
		tt.Release(moveTo, y)
		tt.Frame()
		theme, _ := st.cfg.Appearance.ThemeByID(id)
		colors = append(colors, theme.Colors[key])
	}
	// 拾色面板打开时不跟着滚动；滚回顶部让它收回，并等退场动画走完，
	// 否则下一行的同名面板会与前一个同时存在于树上。
	scrollToBeacon(t, tt, -2000, "设置卡片 theme-fonts")
	advancePanel(tt)
	for _, color := range colors {
		if color == "" {
			t.Fatalf("%s: 拖动后未写入颜色", key)
		}
	}
	if colors[0] == colors[len(colors)-1] {
		t.Fatalf("%s: 拖动多次颜色不变：%v", key, colors)
	}
	return colors
}

// colorPickerSquare 是拾色面板里饱和度方块的胶囊树名字，按位置拖动它即改颜色。
const colorPickerSquare = "Saturation and brightness"

// colorPickerBounds 返回已升起的拾色面板里饱和度方块的矩形。
func colorPickerBounds(tt *ui.Tester) (ui.Rect, bool) {
	var r ui.Rect
	for _, info := range tt.AccessAll() {
		if info.Name == colorPickerSquare {
			r = info.Bounds
		}
	}
	return r, r.W > 0
}

// colorRowTitles 是拖动测试覆盖的颜色键的显示名。
var colorRowTitles = map[string]string{"accent": "强调色", "card": "卡片", "composer_background": "输入框", "code_heading": "标题"}

// advancePanel 走几帧让界面读到新的面板开关，并让升起、退场动画走完。
func advancePanel(tt *ui.Tester) {
	for range 5 {
		tt.Frame()
		tt.Advance(100 * time.Millisecond)
	}
	tt.Frame()
}

// colorSectionTitles 是颜色行所属分组的标题：同一文本可能在两组里出现（如「强调色」），
// 按分组卡片的位置才能认出视口里的那一行。
var colorSectionTitles = map[string]string{
	"accent": "强调", "card": "背景", "composer_background": "背景", "code_heading": "文字",
}

// panelContentBottom 是主题弹窗正文的下沿，取面板操作栏的上边。
func panelContentBottom(tt *ui.Tester) float32 {
	if bar, ok := tt.Find("面板操作栏 主题"); ok && bar.H > 0 {
		return bar.Y
	}
	return 0
}

// colorButtonBySection 返回该分组里、上下边都落在弹窗可视区内的编辑按钮。
func colorButtonBySection(tt *ui.Tester, title, section string) (ui.Rect, bool) {
	card, cardOK := tt.Find("设置卡片 theme-section-" + section)
	if !cardOK || card.W <= 0 {
		return ui.Rect{}, false
	}
	// 滚动区裁剪之外，分组卡片自身之间也有间距：只取上下都落在卡片里的按钮。
	var r ui.Rect
	for _, info := range tt.AccessAll() {
		b := info.Bounds
		if info.Name != "编辑"+title || b.W <= 0 || b.H <= 0 {
			continue
		}
		if b.Y < card.Y || b.Y+b.H > card.Y+card.H || b.Y+b.H > panelContentBottom(tt) {
			continue
		}
		r = b
	}
	return r, r.W > 0
}

// scrollColorRowToButton 把该颜色行滚进可视区，返回它的编辑按钮。同名按钮可能在别的分组里，
// 所以按分组卡片的位置认行：卡片不在可视区就滚一小步，卡片在可视区里就让卡片连同滚到底部
// （卡片已在下沿时再滚只会让它整张卷出去），直到按钮上下都落在卡片与面板正文里。
func scrollColorRowToButton(t *testing.T, tt *ui.Tester, st *SettingsState, key, title string) ui.Rect {
	t.Helper()
	section := colorSectionTitles[key]
	cardKey := "设置卡片 theme-section-" + section
	if r, ok := colorButtonBySection(tt, title, section); ok {
		return r
	}
	scrollToBeacon(t, tt, 2000, "设置卡片 theme-fonts")
	for range 120 {
		if r, ok := colorButtonBySection(tt, title, section); ok {
			return r
		}
		card, ok := tt.Find(cardKey)
		if !ok || card.W <= 0 {
			tt.Wheel(500, 450, 0, -200)
		} else {
			step := min(60, card.H-1)
			tt.Wheel(500, 450, 0, -step)
		}
		for range 6 {
			tt.Advance(30 * time.Millisecond)
			tt.Frame()
		}
	}
	t.Fatalf("%s：滚动后找不到颜色行按钮 %q", key, title)
	return ui.Rect{}
}

// scrollToBeacon 用滚轮按 delta 滚到底或滚到顶，直到 anchor 的矩形不再变化。
func scrollToBeacon(t *testing.T, tt *ui.Tester, delta float32, anchor string) {
	t.Helper()
	before, _ := tt.Find(anchor)
	for range 60 {
		tt.Wheel(500, 450, 0, delta)
		for range 6 {
			tt.Advance(30 * time.Millisecond)
			tt.Frame()
		}
		after, still := tt.Find(anchor)
		if !still || after == before {
			return
		}
		before = after
	}
}

func slicesContains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

// sampleComposerCardBg 发布主题后用 100x100 画布铺满输入卡片底色并取左上角像素。
func sampleComposerCardBg(t *testing.T, cfg config.AppearanceConfig, id string) [3]int {
	t.Helper()
	prev := activeTheme.Load()
	t.Cleanup(func() { activeTheme.Store(prev) })
	setActiveTheme(cfg)
	if _, ok := cfg.ThemeByID(id); !ok {
		t.Fatalf("主题 %q 不存在", id)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c).Size(10, 10).Background(ThemeComposerBackground(c))
	}, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	return pixelRGB(tt.Image())
}

func pixelRGB(img *image.RGBA) [3]int {
	px := img.RGBAAt(5, 5)
	return [3]int{int(px.R), int(px.G), int(px.B)}
}

func rgbOf(c ui.Color) [3]int { return [3]int{int(c.R), int(c.G), int(c.B)} }
