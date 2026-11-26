package mcodeui

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
)

func builtinPalette(id string) themePalette {
	colors, _ := config.BuiltinThemeColors(id)
	p := paletteFromColors(colors)
	derivePalette(&p)
	return p
}

func TestSurfaceAndAccentTokens(t *testing.T) {
	setAppReduceMotion(t, true)
	for _, dark := range []bool{false, true} {
		var colors [5]ui.Color
		var accent, hover, pressed, subtle, onAccent, hairline ui.Color
		tester := ui.NewTester(func(c *ui.Context) {
			colors = [5]ui.Color{ThemeSurfaceCanvas(c), ThemeSurfaceMain(c), ThemeSurfaceCard(c), ThemeSurfaceRaised(c), ThemeSurfacePopover(c)}
			accent, hover, pressed = ThemeAccent(c), ThemeAccentHover(c), ThemeAccentPressed(c)
			subtle, onAccent, hairline = ThemeAccentSubtle(c), ThemeOnAccent(c), ThemeHairline(c)
		}, 100, 100)
		tester.SetPreferences(ui.Preferences{ReduceMotion: true})
		tester.SetDark(dark)
		tester.Frame()
		want := [5]ui.Color{ui.Hex("#F5F5F3"), ui.Hex("#FAFAF9"), ui.Hex("#FFFFFF"), ui.Hex("#FFFFFF"), ui.Hex("#FFFFFF")}
		text := ColorTextPrimaryLight
		if dark {
			want = [5]ui.Color{ui.Hex("#141414"), ui.Hex("#1C1C1C"), ui.Hex("#1F1F1F"), ui.Hex("#242424"), ui.Hex("#2B2B2B")}
			text = ColorTextPrimaryDark
		}
		if colors != want {
			t.Errorf("dark=%v: surfaces=%v, want %v", dark, colors, want)
		}
		if colors[0] == colors[1] || colors[0] == colors[2] || colors[1] == colors[2] {
			t.Errorf("dark=%v: 窗口、主区和卡片颜色应两两不同", dark)
		}
		if hover != accent.Mix(text, 0.10) || pressed != accent.Mix(text, 0.18) || subtle != accent.Alpha(0.12) || onAccent != ui.Hex("#FFFFFF") {
			t.Errorf("dark=%v: accent 派生色不匹配", dark)
		}
		if hairline.A != ui.RGBA(0, 0, 0, 0.10).A {
			t.Errorf("dark=%v: hairline=%v", dark, hairline)
		}
	}
}

func TestIdentityAndLegacyColors(t *testing.T) {
	if ThemeIdentity("aria") != ThemeIdentity("aria") || ThemeIdentity("").A == 0 {
		t.Fatal("身份色应稳定且空 key 应返回有效颜色")
	}
	for _, dark := range []bool{false, true} {
		tester := ui.NewTester(func(c *ui.Context) {
			if ThemeWindowBg(c) != ThemeSurfaceCanvas(c) || ThemeSidebarBg(c) != ThemeSurfaceCanvas(c) || ThemeSurface(c) != ThemeSurfaceCard(c) {
				t.Errorf("dark=%v: 旧表面颜色别名不匹配", dark)
			}
		}, 100, 100)
		tester.SetPreferences(ui.Preferences{ReduceMotion: true})
		tester.SetDark(dark)
		tester.Frame()
	}
}

func TestNativeTheme(t *testing.T) {
	for _, base := range []*ui.Theme{ui.LightTheme(), ui.DarkTheme()} {
		base.FontSize = 15.6
		var colors ui.Theme
		tester := ui.NewTester(func(c *ui.Context) {
			colors = nativeThemeColors(c)
		}, 100, 100)
		tester.SetPreferences(ui.Preferences{ReduceMotion: true})
		tester.SetDark(base.Dark)
		tester.Frame()
		got := nativeTheme(*base, colors, false)
		if got.Accent != colors.Accent || got.AccentHover == base.AccentHover {
			t.Errorf("dark=%v: 强调色或悬停色不正确", base.Dark)
		}
		if got.Background != colors.Background || got.Surface != colors.Surface || got.Selection != colors.Selection ||
			got.Radius != RadiusSM || got.Spacing != Space1 || got.FontSize != base.FontSize {
			t.Errorf("dark=%v: 主题映射或文字缩放不正确", base.Dark)
		}
		if nativeTheme(*base, colors, true).Focus.A != 255 {
			t.Errorf("dark=%v: 高对比度焦点应不透明", base.Dark)
		}
	}
}

// TestCodeFontFamilyFallback 验证代码字体在未配置或为默认值时回退至系统等宽字体 monospace，配置后保留指定字体。
func TestCodeFontFamilyFallback(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "empty string fallback", input: "", expected: "monospace"},
		{name: "spaces only fallback", input: "   ", expected: "monospace"},
		{name: "system default fallback", input: "系统默认", expected: "monospace"},
		{name: "sf mono preset", input: "SF Mono", expected: "SF Mono"},
		{name: "menlo preset", input: "Menlo", expected: "Menlo"},
		{name: "jetbrains mono preset", input: "JetBrains Mono", expected: "JetBrains Mono"},
		{name: "custom font", input: "Fira Code", expected: "Fira Code"},
		{name: "custom font with whitespace", input: "  Cascadia Code  ", expected: "Cascadia Code"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.AppearanceConfig{CodeFont: tc.input}
			got := codeFontFamily(cfg)
			if got != tc.expected {
				t.Errorf("codeFontFamily(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// TestCodeFontSizeClamp 验证代码字号在非法值或边界外的正确钳位逻辑。
func TestCodeFontSizeClamp(t *testing.T) {
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })
	tests := []struct {
		name     string
		input    int
		expected float32
	}{
		{name: "zero default", input: 0, expected: float32(config.DefaultCodeFontSize)},
		{name: "negative default", input: -10, expected: float32(config.DefaultCodeFontSize)},
		{name: "below min clamped to 10", input: 5, expected: float32(config.MinCodeFontSize)},
		{name: "nine clamped to 10", input: 9, expected: float32(config.MinCodeFontSize)},
		{name: "exact min 10", input: 10, expected: 10},
		{name: "normal size 14", input: 14, expected: 14},
		{name: "exact max 24", input: 24, expected: 24},
		{name: "above max clamped to 24", input: 25, expected: float32(config.MaxCodeFontSize)},
		{name: "large number clamped to 24", input: 100, expected: float32(config.MaxCodeFontSize)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Fonts: map[string]int{"code": tc.input}}}}
			setActiveTheme(cfg)
			got := codeFontSize()
			if got != tc.expected {
				t.Errorf("codeFontSize(%d) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}

func resetRuntimeTheme(t *testing.T) {
	t.Helper()
	setActiveTheme(config.AppearanceConfig{})
	themeDark.Store(false)
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}); themeDark.Store(false) })
}

func TestThemeRuntimeDefaultPalette(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	getters := []func(*ui.Context) ui.Color{
		ThemeSurfaceCanvas, ThemeSurfaceMain, ThemeSurfaceCard, ThemeSurfaceRaised, ThemeSurfacePopover,
		ThemePrimaryText, ThemeSecondaryText, ThemeTertiaryText, ThemeAccent, ThemeDanger, ThemeWarning,
		ThemeSuccess, ThemeCaution, ThemePlan, ThemeBorder, ThemeDivider,
	}
	for _, dark := range []bool{false, true} {
		want := []ui.Color{
			ui.Hex("#F5F5F3"), ui.Hex("#FAFAF9"), ui.Hex("#FFFFFF"), ui.Hex("#FFFFFF"), ui.Hex("#FFFFFF"),
			ui.RGBA(0, 0, 0, 0.85), ui.RGBA(0, 0, 0, 0.60), ui.RGBA(0, 0, 0, 0.58), ui.Hex("#0062CC"), ui.Hex("#C82333"), ui.Hex("#A85500"),
			ui.Hex("#167936"), ui.Hex("#8A6700"), ui.Hex("#8A2BB8"), ui.RGBA(0, 0, 0, 0.14), ui.RGBA(0, 0, 0, 0.22),
		}
		if dark {
			want = []ui.Color{
				ui.Hex("#141414"), ui.Hex("#1C1C1C"), ui.Hex("#1F1F1F"), ui.Hex("#242424"), ui.Hex("#2B2B2B"),
				ui.RGBA(255, 255, 255, 0.85), ui.RGBA(255, 255, 255, 0.60), ui.RGBA(255, 255, 255, 0.38), ui.Hex("#0A84FF"), ui.Hex("#FF453A"), ui.Hex("#FF9F0A"),
				ui.Hex("#30D158"), ui.Hex("#E8C15A"), ui.Hex("#BF5AF2"), ui.RGBA(255, 255, 255, 0.14), ui.RGBA(255, 255, 255, 0.22),
			}
		}
		var got []ui.Color
		tt := ui.NewTester(func(c *ui.Context) {
			got = got[:0]
			for _, get := range getters {
				got = append(got, get(c))
			}
		}, 100, 100)
		appReduceMotion.Store(true)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.SetDark(dark)
		tt.Frame()
		for i, color := range got {
			if color != want[i] {
				t.Errorf("dark=%v key=%s: got %v, want %v", dark, config.ThemeColorKeys[i], color, want[i])
			}
		}
	}
}

func TestColorfulBuiltinThemePalette(t *testing.T) {
	light := resolvePalette(config.AppearanceConfig{}, config.ThemeColorfulLight)
	if light.Text != ui.Hex("#4D5666D8") {
		t.Fatalf("多彩浅色主文字 = %v，期望 #4D5666D8", light.Text)
	}
	if light.Accent != ui.Hex("#0062CC") {
		t.Fatalf("多彩浅色强调色 = %v，期望 #0062CC", light.Accent)
	}
	dark := resolvePalette(config.AppearanceConfig{}, config.ThemeColorfulDark)
	if dark.Accent != ui.Hex("#e9c1ff") {
		t.Fatalf("多彩深色强调色 = %v，期望 #e9c1ff", dark.Accent)
	}
	cfg := config.AppearanceConfig{Themes: []config.ThemeDef{{ID: config.ThemeColorfulLight, Base: config.ThemeLight, Colors: map[string]string{"accent": "#123456"}}}}
	overridden := resolvePalette(cfg, config.ThemeColorfulLight)
	if overridden.Accent != ui.Hex("#123456") {
		t.Fatalf("用户覆盖多彩浅色强调色未生效: %v", overridden.Accent)
	}
}

func TestBuiltinThemePalettes(t *testing.T) {
	ids := []string{config.ThemeLight, config.ThemeDark, config.ThemeColorfulLight, config.ThemeColorfulDark}
	palettes := make([]themePalette, len(ids))
	for i, id := range ids {
		palettes[i] = resolvePalette(config.AppearanceConfig{}, id)
	}
	// 多彩主题继承同族 base 的表面色，因此只比较各自独特的主体色；浅色族与深色族之间才比表面色。
	for i := range palettes {
		if palettes[i].Canvas.A == 0 || palettes[i].Main.A == 0 || palettes[i].Card.A == 0 || palettes[i].Popover.A == 0 {
			t.Fatalf("内置主题 %s 表面色不透明: canvas=%v main=%v card=%v popover=%v", ids[i], palettes[i].Canvas, palettes[i].Main, palettes[i].Card, palettes[i].Popover)
		}
	}
	if palettes[2].Canvas != palettes[0].Canvas {
		t.Fatalf("多彩浅色 Canvas 未继承浅色: colorful=%v light=%v", palettes[2].Canvas, palettes[0].Canvas)
	}
	if palettes[3].Text != palettes[1].Text {
		t.Fatalf("多彩深色 Text 未继承深色: colorful=%v dark=%v", palettes[3].Text, palettes[1].Text)
	}
	luminance := func(c ui.Color) float64 {
		return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
	}
	if luminance(palettes[0].Canvas) <= luminance(palettes[1].Canvas) {
		t.Fatalf("浅色 Canvas 应比深色更亮: light=%v dark=%v", palettes[0].Canvas, palettes[1].Canvas)
	}
}

func TestBuiltinThemeSwitchingIsFixed(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	setActiveTheme(config.AppearanceConfig{Theme: config.ThemeColorfulDark})
	want := resolvePalette(config.AppearanceConfig{}, config.ThemeColorfulDark)
	var got themePalette
	tt := ui.NewTester(func(c *ui.Context) { got = paletteOf(c) }, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Frame()
		if got != want {
			t.Fatalf("多彩深色固定主题受系统外观影响，dark=%v", dark)
		}
	}
}

func TestThemeRuntimeOverridesAndFixed(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	cfg := config.AppearanceConfig{Theme: config.ThemeSystem, Themes: []config.ThemeDef{
		{ID: config.ThemeLight, Base: config.ThemeLight, Colors: map[string]string{"accent": "#123456"}},
		{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{"text": "#345678AA"}, Fonts: map[string]int{"conversation": 150}},
	}}
	setActiveTheme(cfg)
	appReduceMotion.Store(true)
	var accent, text, canvas ui.Color
	tt := ui.NewTester(func(c *ui.Context) { accent, text, canvas = ThemeAccent(c), ThemePrimaryText(c), ThemeSurfaceCanvas(c) }, 100, 100)
	appReduceMotion.Store(true)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetDark(false)
	tt.Frame()
	if accent != ui.Hex("#123456") {
		t.Fatalf("浅色覆盖未生效: %v", accent)
	}
	tt.SetDark(true)
	tt.Frame()
	if accent != builtinPalette(config.ThemeDark).Accent {
		t.Fatalf("浅色覆盖影响了深色: %v", accent)
	}
	cfg.Theme = "custom"
	setActiveTheme(cfg)
	appReduceMotion.Store(true)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Frame()
		if text != ui.Hex("#345678AA") || canvas != builtinPalette(config.ThemeLight).Canvas || accent != builtinPalette(config.ThemeLight).Accent {
			t.Fatalf("固定主题应只叠加基底默认值，dark=%v text=%v canvas=%v accent=%v", dark, text, canvas, accent)
		}
		themeDark.Store(dark)
		if chatFont(10) != 15 {
			t.Fatalf("固定主题字号受系统明暗影响")
		}
	}
	// 发布后不保留配置里的可变 map。
	cfg.Themes[1].Colors["text"] = "#FFFFFF"
	cfg.Themes[1].Fonts["conversation"] = 100
	tt.Frame()
	if text != ui.Hex("#345678AA") || chatFont(10) != 15 {
		t.Fatal("主题快照被配置后续修改影响")
	}
}

func TestThemeRuntimeSystemAccentAndInvalidColors(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	for _, base := range []string{config.ThemeLight, config.ThemeDark} {
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: base, Colors: map[string]string{
			"accent": config.ThemeAccentSystem, "canvas": "#oops", "text": "#12345", "divider": "#GGGGGG",
		}}}})
		var accent, canvas, text, border ui.Color
		tt := ui.NewTester(func(c *ui.Context) {
			accent, canvas, text, border = ThemeAccent(c), ThemeSurfaceCanvas(c), ThemePrimaryText(c), ThemeBorder(c)
		}, 100, 100)
		system := ui.Hex("#ABCDEF80")
		appReduceMotion.Store(true)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true, Accent: system})
		tt.Frame()
		if accent != system {
			t.Fatalf("系统强调色未使用: %v", accent)
		}
		appReduceMotion.Store(true)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.Frame()
		p := builtinPalette(base)
		if accent != p.Accent || canvas != p.Canvas || text != p.Text || border != p.Border {
			t.Fatalf("非法颜色或空系统强调色未回退 %s", base)
		}
	}
	for _, value := range []string{"", "system", "123456", "#123", "#1234567", "#GGGGGG", "#+12345", "#12_345", "#123456789", " #123456"} {
		if _, ok := parseThemeColor(value); ok {
			t.Errorf("非法颜色被接受: %q", value)
		}
	}
	for _, value := range []string{"#123456", "#abcdef", "#12345678", "#00000000"} {
		color, ok := parseThemeColor(value)
		if !ok || color != ui.Hex(value) {
			t.Errorf("颜色解析失败: %q %v %v", value, color, ok)
		}
	}
}

func TestThemeRuntimeRegionalFonts(t *testing.T) {
	resetRuntimeTheme(t)
	setActiveTheme(config.AppearanceConfig{Themes: []config.ThemeDef{
		{ID: config.ThemeLight, Base: config.ThemeLight, Fonts: map[string]int{"sidebar": 130}},
		{ID: config.ThemeDark, Base: config.ThemeDark, Fonts: map[string]int{"conversation": 150, "interface": 115, "code": 16, "terminal": 17, "editor": 18}},
	}})
	themeDark.Store(false)
	if sidebarFont(10) != 13 || sidebarScaleFactor() != 1.3 || chatFont(10) != 10 || uiFont(10) != 10 || scaledFont(10) != 10 {
		t.Fatal("侧栏字号影响了其他区域")
	}
	tt := ui.NewTester(func(c *ui.Context) { applyAppTheme(c) }, 100, 100)
	tt.SetDark(true)
	tt.Frame()
	if !themeDark.Load() || sidebarFont(10) != 10 || chatFont(10) != 15 || uiFont(10) != 11.5 || codeFontSize() != 16 || terminalFontSize() != 17 || editorFontSize() != 18 {
		t.Fatal("明暗主题区域字号未切换")
	}
	tt.SetDark(false)
	tt.Frame()
	if themeDark.Load() || sidebarFont(10) != 13 || chatFont(10) != 10 {
		t.Fatal("浅色主题区域字号未恢复")
	}
}

func TestThemeRuntimeMarkdownRefresh(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	chat := newChatState(nil)
	tt := ui.NewTester(func(c *ui.Context) { ensureChatMarkdown(c, chat) }, 600, 400)
	appReduceMotion.Store(true)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	for _, key := range []string{"accent", "card", "divider", "text_secondary"} {
		oldChat := chat.mdOpts.Theme
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeDark, Colors: map[string]string{key: "#123456"}}}})
		appReduceMotion.Store(true)
		tt.Frame()
		if chat.mdOpts.Theme == oldChat || chat.mdStaticOpts.Theme != chat.mdOpts.Theme {
			t.Fatalf("%s 变化后 Markdown 缓存未重建", key)
		}
		tt.Frame()
		if markdown := chat.mdOpts.Theme; markdown == nil {
			t.Fatal("Markdown 主题为空")
		}
	}
	oldChat := chat.mdOpts.Theme
	tt.Frame()
	if chat.mdOpts.Theme != oldChat {
		t.Fatal("颜色不变时不应重建 Markdown 主题")
	}
}

func TestThemeRuntimeTransitionReduceMotion(t *testing.T) {
	setAppReduceMotion(t, false)
	resetRuntimeTheme(t)
	var got ui.Color
	tt := ui.NewTester(func(c *ui.Context) { applyAppTheme(c); got = ThemeAccent(c) }, 100, 100)
	start := got
	cfg := config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeDark, Colors: map[string]string{"accent": "#E03040"}}}}
	target := ui.Hex("#E03040")
	setActiveTheme(cfg)
	tt.Frame()
	tt.Advance(motionFast / 2)
	if got == start || got == target {
		t.Fatalf("主题颜色缺少中间态: %v", got)
	}
	appReduceMotion.Store(true)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if got != target {
		t.Fatalf("减少动效时应立即到终点: %v", got)
	}
	cfg.Themes[0].Colors["accent"] = "#30E040"
	cfg.ReduceMotion = true
	setActiveTheme(cfg)
	tt.Frame()
	if got != ui.Hex("#30E040") {
		t.Fatalf("减少动效时切换仍有过渡: %v", got)
	}
}

func TestThemeRuntimeConcurrentSnapshot(t *testing.T) {
	resetRuntimeTheme(t)
	cfg := config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{"accent": "#123456"}, Fonts: map[string]int{"sidebar": 130}}}}
	setActiveTheme(cfg)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				setActiveTheme(cfg)
				snapshot := activeTheme.Load()
				if snapshot.fixed == nil || snapshot.fixedFonts == nil || snapshot.fixed.Accent != ui.Hex("#123456") || snapshot.fixedFonts.sidebar != 130 {
					t.Error("读到了不完整主题快照")
					return
				}
				_ = paletteOf(nil)
				_ = currentThemeFonts()
			}
		}()
	}
	wg.Wait()
}

func TestThemeRuntimeExternalConfigRefresh(t *testing.T) {
	resetRuntimeTheme(t)
	dir := settingsTestDir(t)
	st := NewSettingsState(dir, nil, nil)
	cfg := config.Defaults()
	cfg.Appearance.Theme = "custom"
	cfg.Appearance.Themes = []config.ThemeDef{{ID: "custom", Name: "Test", Base: config.ThemeDark, Colors: map[string]string{"accent": "#123456"}, Fonts: map[string]int{"sidebar": 130}}}
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	st.handleConfigChanged()
	snapshot := activeTheme.Load()
	if snapshot.fixed == nil || snapshot.fixed.Accent != ui.Hex("#123456") || sidebarFont(10) != 13 {
		t.Fatal("外部配置修改未发布到主题运行时")
	}
	cfg.Appearance.Theme = config.ThemeSystem
	cfg.Appearance.Themes = nil
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	st.handleConfigChanged()
	if activeTheme.Load().fixed != nil || sidebarFont(10) != 10 {
		t.Fatal("外部配置切回系统主题未恢复默认")
	}
}

func TestThemeRuntimeDerivedColors(t *testing.T) {
	resetRuntimeTheme(t)
	setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeDark, Colors: map[string]string{"text": "#12345640"}}}})
	tt := ui.NewTester(func(c *ui.Context) {
		if got := ThemeOverlay(c, 0.25); got != ui.RGBA(0x12, 0x34, 0x56, 0.25) {
			t.Errorf("遮罩未采用主文字 RGB: %v", got)
		}
		if got := ThemeHover(c); got != ui.RGBA(0x12, 0x34, 0x56, 0.08) {
			t.Errorf("悬停色未采用主文字 RGB: %v", got)
		}
		if got := ThemeSelected(c); got != ui.RGBA(0x12, 0x34, 0x56, 0.12) {
			t.Errorf("选中色未采用主文字 RGB: %v", got)
		}
	}, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
}

func TestThemeRuntimeCodeColors(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	for _, dark := range []bool{false, true} {
		setActiveTheme(config.AppearanceConfig{})
		base := builtinPalette(config.ThemeLight)
		if dark {
			base = builtinPalette(config.ThemeDark)
		}
		mdTheme := (*markdown.Theme)(nil)
		tt := ui.NewTester(func(c *ui.Context) { mdTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
		appReduceMotion.Store(true)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.SetDark(dark)
		tt.Frame()
		if mdTheme.Text != base.Text || mdTheme.Code != base.Text || mdTheme.CodeBackground != base.Text.Alpha(0.07) {
			t.Fatalf("dark=%v 默认代码色应跟随主文字: text=%v code=%v bg=%v", dark, mdTheme.Text, mdTheme.Code, mdTheme.CodeBackground)
		}
	}
	setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{
		"code_text": "#123456", "code_background": "#ABCDEF80",
	}}}})
	appReduceMotion.Store(true)
	mdTheme := (*markdown.Theme)(nil)
	tt := ui.NewTester(func(c *ui.Context) { mdTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
	appReduceMotion.Store(true)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if mdTheme.Code != ui.Hex("#123456") || mdTheme.CodeBackground != ui.Hex("#ABCDEF80") {
		t.Fatalf("代码覆盖色未生效: code=%v bg=%v", mdTheme.Code, mdTheme.CodeBackground)
	}
	if mdTheme.Text != builtinPalette(config.ThemeLight).Text {
		t.Fatalf("代码覆盖不应改动主文字: %v", mdTheme.Text)
	}

	// 路由联动：值变的是调色板终值，而不是经过主题过渡的当前色。只设 text 时
	// 代码色必须仍跟随它，否则换主题时行内代码会与正文不同步。
	setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{
		"text": "#654321",
	}}}})
	appReduceMotion.Store(true)
	textOnlyTheme := (*markdown.Theme)(nil)
	ui.NewTester(func(c *ui.Context) { textOnlyTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100).Frame()
	if textOnlyTheme.Text != ui.Hex("#654321") || textOnlyTheme.Code != ui.Hex("#654321") || textOnlyTheme.CodeBackground != ui.Hex("#654321").Alpha(0.07) {
		t.Fatalf("只覆盖 text 时代码色应跟随: text=%v code=%v bg=%v", textOnlyTheme.Text, textOnlyTheme.Code, textOnlyTheme.CodeBackground)
	}

	chat := newChatState(nil)
	setActiveTheme(config.AppearanceConfig{})
	appReduceMotion.Store(true)
	chatTT := ui.NewTester(func(c *ui.Context) { ensureChatMarkdown(c, chat) }, 600, 400)
	chatTT.SetPreferences(ui.Preferences{ReduceMotion: true})
	chatTT.Frame()
	for _, key := range []string{"code_text", "code_background"} {
		oldChat := chat.mdOpts.Theme
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeDark, Colors: map[string]string{key: "#123456"}}}})
		appReduceMotion.Store(true)
		chatTT.Frame()
		if chat.mdOpts.Theme == oldChat {
			t.Fatalf("%s 变化后 Markdown 缓存未重建", key)
		}
		// 只覆盖其中一个键时，另一项仍取默认（跟随主文字），不应跟着变成覆盖值。
		defaultText := builtinPalette(config.ThemeDark).Text
		wantCode, wantBG := defaultText, defaultText.Alpha(0.07)
		if key == "code_text" {
			wantCode = ui.Hex("#123456")
		} else {
			wantBG = ui.Hex("#123456")
		}
		if chat.mdOpts.Theme.Code != wantCode || chat.mdOpts.Theme.CodeBackground != wantBG {
			t.Fatalf("%s 覆盖未进入 Markdown 主题: %v/%v", key, chat.mdOpts.Theme.Code, chat.mdOpts.Theme.CodeBackground)
		}
	}
}

func TestChatMarkdownThemeStableWhileThemeAnimates(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, false)
	chat := newChatState(nil)
	chatTT := ui.NewTester(func(c *ui.Context) { ensureChatMarkdown(c, chat) }, 600, 400)
	chatTT.SetPreferences(ui.Preferences{})
	chatTT.Frame()
	if chat.mdOpts.Theme == nil {
		t.Fatal("Markdown 主题未初始化")
	}

	setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{
		"code_text": "#123456", "code_background": "#ABCDEF",
	}}}})
	appReduceMotion.Store(false)
	chatTT.Frame() // 切主题后允许重建一次
	stable := chat.mdOpts.Theme
	for frame := 1; frame <= 30; frame++ {
		chatTT.Advance(10 * time.Millisecond)
		chatTT.Frame()
		if chat.mdOpts.Theme != stable {
			t.Fatalf("第 %d 帧 Markdown 主题被重建（主题动画中缓存不得逐帧变化）", frame)
		}
	}

	if chat.mdOpts.Theme.Code != ui.Hex("#123456") || chat.mdOpts.Theme.CodeBackground != ui.Hex("#ABCDEF") {
		t.Fatalf("代码色应为覆盖终值: code=%v bg=%v", chat.mdOpts.Theme.Code, chat.mdOpts.Theme.CodeBackground)
	}
}

func TestThemeScrollbarStyleHoverActive(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	for _, base := range []*ui.Theme{ui.LightTheme(), ui.DarkTheme()} {
		var colors ui.Theme
		tester := ui.NewTester(func(c *ui.Context) {
			colors = nativeThemeColors(c)
		}, 100, 100)
		tester.SetPreferences(ui.Preferences{ReduceMotion: true})
		tester.SetDark(base.Dark)
		tester.Frame()
		s := nativeTheme(*base, colors, false).ScrollbarStyle
		if s.Thumb != colors.Scrollbar {
			t.Errorf("dark=%v: Thumb=%v, want %v", base.Dark, s.Thumb, colors.Scrollbar)
		}
		if s.Hover.A <= s.Thumb.A || s.Active.A <= s.Thumb.A {
			t.Errorf("dark=%v: Hover(%d) 和 Active(%d) 的 alpha 应大于 Thumb(%d)", base.Dark, s.Hover.A, s.Active.A, s.Thumb.A)
		}
		if s.Active.A < s.Hover.A {
			t.Errorf("dark=%v: Active(%d) 的 alpha 不应小于 Hover(%d)", base.Dark, s.Active.A, s.Hover.A)
		}
		if s.Hover.R != s.Thumb.R || s.Hover.G != s.Thumb.G || s.Hover.B != s.Thumb.B ||
			s.Active.R != s.Thumb.R || s.Active.G != s.Thumb.G || s.Active.B != s.Thumb.B {
			t.Errorf("dark=%v: Hover(%v) 或 Active(%v) 的 RGB 与 Thumb(%v) 不一致", base.Dark, s.Hover, s.Active, s.Thumb)
		}
		if s.TrackWidth != 0 || s.ThumbWidth != 0 || s.Radius != 0 || s.MinLength != 0 || s.Square || s.Flush || s.Track != ui.Transparent {
			t.Errorf("dark=%v: ScrollbarStyle 其余字段应保持零值: %+v", base.Dark, s)
		}
	}
}

// 默认配置下 Markdown 各角色跟随基色：标题与粗体取主文字，次要标记取次要文字，链接取 accent。
func TestMarkdownRoleColors(t *testing.T) {
	resetRuntimeTheme(t)
	setActiveTheme(config.AppearanceConfig{})
	for _, dark := range []bool{false, true} {
		base := builtinPalette(config.ThemeLight)
		if dark {
			base = builtinPalette(config.ThemeDark)
		}
		appReduceMotion.Store(true)
		mdTheme := (*markdown.Theme)(nil)
		tt := ui.NewTester(func(c *ui.Context) { mdTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.SetDark(dark)
		tt.Frame()
		if mdTheme.Text != base.Text || mdTheme.Strong != base.Text || mdTheme.Muted != base.TextSecondary || mdTheme.Link != base.Accent {
			t.Fatalf("dark=%v 角色默认色不对: text=%v strong=%v muted=%v link=%v", dark, mdTheme.Text, mdTheme.Strong, mdTheme.Muted, mdTheme.Link)
		}
		for i, h := range mdTheme.Headings {
			if h.Color != base.Text {
				t.Fatalf("dark=%v H%d 标题色应为 %v，实际 %v", dark, i+1, base.Text, h.Color)
			}
		}
	}
}

// 只覆盖 text 时标题与粗体跟随，次要标记与链接保持各自的默认来源。
func TestMarkdownRoleColorOverridesFollowBase(t *testing.T) {
	resetRuntimeTheme(t)
	setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{
		"text": "#654321",
	}}}})
	appReduceMotion.Store(true)
	mdTheme := (*markdown.Theme)(nil)
	tt := ui.NewTester(func(c *ui.Context) { mdTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	light := builtinPalette(config.ThemeLight)
	if mdTheme.Text != ui.Hex("#654321") || mdTheme.Strong != ui.Hex("#654321") {
		t.Fatalf("只覆盖 text 时标题与粗体应跟随: text=%v strong=%v", mdTheme.Text, mdTheme.Strong)
	}
	for i, h := range mdTheme.Headings {
		if h.Color != ui.Hex("#654321") {
			t.Fatalf("H%d 标题色应跟随 text: %v", i+1, h.Color)
		}
	}
	if mdTheme.Muted != light.TextSecondary || mdTheme.Link != light.Accent {
		t.Fatalf("次要标记与链接不应跟随 text: muted=%v link=%v", mdTheme.Muted, mdTheme.Link)
	}

	// 角色覆盖各自生效且互不影响。
	chat := newChatState(nil)
	setActiveTheme(config.AppearanceConfig{})
	appReduceMotion.Store(true)
	chatTT := ui.NewTester(func(c *ui.Context) { ensureChatMarkdown(c, chat) }, 600, 400)
	chatTT.SetPreferences(ui.Preferences{ReduceMotion: true})
	chatTT.Frame()
	for _, key := range []string{"code_heading", "code_muted", "code_link", "code_strong"} {
		prev := chat.mdOpts.Theme
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeDark, Colors: map[string]string{key: "#123456"}}}})
		appReduceMotion.Store(true)
		chatTT.Frame()
		cur := chat.mdOpts.Theme
		if cur == prev {
			t.Fatalf("%s 变化后 Markdown 缓存未重建", key)
		}
		base := builtinPalette(config.ThemeDark)
		wantHeading, wantMuted, wantLink, wantStrong := base.Text, base.TextSecondary, base.Accent, base.Text
		switch key {
		case "code_heading":
			wantHeading = ui.Hex("#123456")
		case "code_muted":
			wantMuted = ui.Hex("#123456")
		case "code_link":
			wantLink = ui.Hex("#123456")
		case "code_strong":
			wantStrong = ui.Hex("#123456")
		}
		for i, h := range cur.Headings {
			if h.Color != wantHeading {
				t.Fatalf("%s: H%d 应为 %v，实际 %v", key, i+1, wantHeading, h.Color)
			}
		}
		if cur.Muted != wantMuted || cur.Link != wantLink || cur.Strong != wantStrong {
			t.Fatalf("%s 角色覆盖互串: muted=%v link=%v strong=%v", key, cur.Muted, cur.Link, cur.Strong)
		}
		if cur.Text != base.Text {
			t.Fatalf("%s 不应改动主文字: %v", key, cur.Text)
		}
	}
}

func TestThemeBorderDividerRestored(t *testing.T) {
	got := resolvePalette(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{
		"border": "#FF0000", "divider": "#00FF00",
	}}}}, "custom")
	if got.Border != ui.Hex("#FF0000") || got.Divider != ui.Hex("#00FF00") {
		t.Fatalf("border/divider 覆盖未生效：border=%v divider=%v", got.Border, got.Divider)
	}
}

// 引用块文字与左侧竖线各自可覆盖；未设时分别派生为次要文字与默认边框色。
func TestMarkdownQuoteRoleColors(t *testing.T) {
	resetRuntimeTheme(t)
	setAppReduceMotion(t, true)
	for _, dark := range []bool{false, true} {
		base := builtinPalette(config.ThemeLight)
		if dark {
			base = builtinPalette(config.ThemeDark)
		}
		mdTheme := (*markdown.Theme)(nil)
		tt := ui.NewTester(func(c *ui.Context) { mdTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.SetDark(dark)
		tt.Frame()
		if mdTheme.QuoteText != base.TextSecondary || mdTheme.QuoteBar != base.Border {
			t.Fatalf("dark=%v 引用块默认色应派生为次要文字与默认边框: text=%v bar=%v", dark, mdTheme.QuoteText, mdTheme.QuoteBar)
		}
	}

	setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{
		"quote_text": "#123456", "quote_bar": "#654321",
	}}}})
	appReduceMotion.Store(true)
	mdTheme := (*markdown.Theme)(nil)
	tt := ui.NewTester(func(c *ui.Context) { mdTheme = buildMarkdownTheme(c, 13, "", 12) }, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if mdTheme.QuoteText != ui.Hex("#123456") || mdTheme.QuoteBar != ui.Hex("#654321") {
		t.Fatalf("引用块覆盖色未生效: text=%v bar=%v", mdTheme.QuoteText, mdTheme.QuoteBar)
	}
	if mdTheme.Text != builtinPalette(config.ThemeLight).Text {
		t.Fatalf("引用块覆盖不应改动主文字: %v", mdTheme.Text)
	}

	// 只设其一另一项仍取默认；两项变化都必须触发 Markdown 缓存重建。
	chat := newChatState(nil)
	setActiveTheme(config.AppearanceConfig{})
	appReduceMotion.Store(true)
	chatTT := ui.NewTester(func(c *ui.Context) { ensureChatMarkdown(c, chat) }, 600, 400)
	chatTT.SetPreferences(ui.Preferences{ReduceMotion: true})
	chatTT.Frame()
	base := builtinPalette(config.ThemeDark)
	for _, key := range []string{"quote_text", "quote_bar"} {
		prev := chat.mdOpts.Theme
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeDark, Colors: map[string]string{key: "#123456"}}}})
		appReduceMotion.Store(true)
		chatTT.Frame()
		cur := chat.mdOpts.Theme
		if cur == prev {
			t.Fatalf("%s 变化后 Markdown 缓存未重建", key)
		}
		wantText, wantBar := base.TextSecondary, base.Border
		if key == "quote_text" {
			wantText = ui.Hex("#123456")
		} else {
			wantBar = ui.Hex("#123456")
		}
		if cur.QuoteText != wantText || cur.QuoteBar != wantBar {
			t.Fatalf("%s 覆盖未进入 Markdown 主题: text=%v bar=%v", key, cur.QuoteText, cur.QuoteBar)
		}
	}
}

// 用户消息气泡底色：未设置时等于改动前的默认值（卡片色 ThemeSurfaceCard 的终值），
// 设置 user_message_background 后取该色，且不经主题过渡。
func TestUserMessageBackgroundColor(t *testing.T) {
	const key = "user_message_background"
	if !slices.Contains(config.ThemeColorKeys, key) {
		t.Fatalf("%s 未登记到 ThemeColorKeys", key)
	}
	defaultLight := ui.Hex("#FFFFFF")
	defaultDark := ui.Hex("#1F1F1F")
	for _, want := range []ui.Color{defaultLight, defaultDark} {
		id := config.ThemeLight
		if want == defaultDark {
			id = config.ThemeDark
		}
		if got := resolvePalette(config.AppearanceConfig{}, id).UserMessageBackground; got != want {
			t.Fatalf("%s 未设置时气泡底色 = %v，期望默认值 %v", id, got, want)
		}
	}

	resetRuntimeTheme(t)
	cfg := config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{
		{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{key: "#123456"}},
	}}
	if _, ok := cfg.ThemeByID("custom"); !ok {
		t.Fatal("主题缺失")
	}
	want := ui.Color{R: 0x12, G: 0x34, B: 0x56, A: 255}
	if got := resolvePalette(cfg, "custom").UserMessageBackground; got != want {
		t.Fatalf("设置 %s 后气泡底色 = %v，期望 %v", key, got, want)
	}

	// getter 走调色板终值：关掉减少动效时也立即返回目标色，不返回过渡中间色。
	setActiveTheme(cfg)
	setAppReduceMotion(t, false)
	tt := ui.NewTester(func(c *ui.Context) {
		if got := ThemeUserMessageBackground(c); got != want {
			t.Fatalf("关闭减少动效时气泡底色 = %v，期望 %v（不应走过渡）", got, want)
		}
	}, 100, 100)
	tt.Frame()
}
