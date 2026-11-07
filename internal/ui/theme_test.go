package mcodeui

import (
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
			cfg := config.AppearanceConfig{CodeFontSize: tc.input}
			got := codeFontSize(cfg)
			if got != tc.expected {
				t.Errorf("codeFontSize(%d) = %v, want %v", tc.input, got, tc.expected)
			}
		})
	}
}
