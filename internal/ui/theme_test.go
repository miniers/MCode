package mcodeui

import (
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

func TestSurfaceAndAccentTokens(t *testing.T) {
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
	if ThemeIdentity("aspen") != ThemeIdentity("aspen") || ThemeIdentity("").A == 0 {
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
	getters := []func(*ui.Context) ui.Color{
		ThemeSurfaceCanvas, ThemeSurfaceMain, ThemeSurfaceCard, ThemeSurfaceRaised, ThemeSurfacePopover,
		ThemePrimaryText, ThemeSecondaryText, ThemeTertiaryText, ThemeAccent, ThemeDanger, ThemeWarning,
		ThemeSuccess, ThemeCaution, ThemePlan, ThemeBorder, ThemeDivider,
	}
	for _, dark := range []bool{false, true} {
		want := []ui.Color{
			ui.Hex("#F5F5F3"), ui.Hex("#FAFAF9"), ui.Hex("#FFFFFF"), ui.Hex("#FFFFFF"), ui.Hex("#FFFFFF"),
			ui.RGBA(0, 0, 0, 0.85), ui.RGBA(0, 0, 0, 0.60), ui.RGBA(0, 0, 0, 0.38), ui.Hex("#007AFF"), ui.Hex("#FF3B30"), ui.Hex("#FF9500"),
			ui.Hex("#28CD41"), ui.Hex("#C9A227"), ui.Hex("#AF52DE"), ui.RGBA(0, 0, 0, 0.14), ui.RGBA(0, 0, 0, 0.22),
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

func TestThemeRuntimeOverridesAndFixed(t *testing.T) {
	resetRuntimeTheme(t)
	cfg := config.AppearanceConfig{Theme: config.ThemeSystem, Themes: []config.ThemeDef{
		{ID: config.ThemeLight, Base: config.ThemeLight, Colors: map[string]string{"accent": "#123456"}},
		{ID: "custom", Base: config.ThemeLight, Colors: map[string]string{"text": "#345678AA"}, Fonts: map[string]int{"conversation": 150}},
	}}
	setActiveTheme(cfg)
	var accent, text, canvas ui.Color
	tt := ui.NewTester(func(c *ui.Context) { accent, text, canvas = ThemeAccent(c), ThemePrimaryText(c), ThemeSurfaceCanvas(c) }, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.SetDark(false)
	tt.Frame()
	if accent != ui.Hex("#123456") {
		t.Fatalf("浅色覆盖未生效: %v", accent)
	}
	tt.SetDark(true)
	tt.Frame()
	if accent != builtinPalettes[config.ThemeDark].Accent {
		t.Fatalf("浅色覆盖影响了深色: %v", accent)
	}
	cfg.Theme = "custom"
	setActiveTheme(cfg)
	for _, dark := range []bool{false, true} {
		tt.SetDark(dark)
		tt.Frame()
		if text != ui.Hex("#345678AA") || canvas != builtinPalettes[config.ThemeLight].Canvas || accent != builtinPalettes[config.ThemeLight].Accent {
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
	for _, base := range []string{config.ThemeLight, config.ThemeDark} {
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: base, Colors: map[string]string{
			"accent": config.ThemeAccentSystem, "canvas": "#oops", "text": "#12345", "border": "#GGGGGG",
		}}}})
		var accent, canvas, text, border ui.Color
		tt := ui.NewTester(func(c *ui.Context) {
			accent, canvas, text, border = ThemeAccent(c), ThemeSurfaceCanvas(c), ThemePrimaryText(c), ThemeBorder(c)
		}, 100, 100)
		system := ui.Hex("#ABCDEF80")
		tt.SetPreferences(ui.Preferences{ReduceMotion: true, Accent: system})
		tt.Frame()
		if accent != system {
			t.Fatalf("系统强调色未使用: %v", accent)
		}
		tt.SetPreferences(ui.Preferences{ReduceMotion: true})
		tt.Frame()
		p := builtinPalettes[base]
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
	chat := newChatState(nil)
	mini := &miniEditorState{path: "test.md", text: "# Test", baseline: "# Test", preview: true}
	tt := ui.NewTester(func(c *ui.Context) { ensureChatMarkdown(c, chat); mini.view(c) }, 600, 400)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	for _, key := range []string{"accent", "card", "border", "text_secondary"} {
		oldChat, oldMini := chat.mdOpts.Theme, mini.mdOpts.Theme
		setActiveTheme(config.AppearanceConfig{Theme: "custom", Themes: []config.ThemeDef{{ID: "custom", Base: config.ThemeDark, Colors: map[string]string{key: "#123456"}}}})
		tt.Frame()
		if chat.mdOpts.Theme == oldChat || mini.mdOpts.Theme == oldMini || chat.mdStaticOpts.Theme != chat.mdOpts.Theme {
			t.Fatalf("%s 变化后 Markdown 缓存未重建", key)
		}
		tt.Frame()
		if markdown := chat.mdOpts.Theme; markdown == nil {
			t.Fatal("Markdown 主题为空")
		}
	}
	oldChat, oldMini := chat.mdOpts.Theme, mini.mdOpts.Theme
	tt.Frame()
	if chat.mdOpts.Theme != oldChat || mini.mdOpts.Theme != oldMini {
		t.Fatal("颜色不变时不应重建 Markdown 主题")
	}
}

func TestThemeRuntimeTransitionReduceMotion(t *testing.T) {
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
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	if got != target {
		t.Fatalf("减少动效时应立即到终点: %v", got)
	}
	cfg.Themes[0].Colors["accent"] = "#30E040"
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
