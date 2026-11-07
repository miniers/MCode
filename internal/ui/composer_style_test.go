package mcodeui

import (
	"os"
	"strings"
	"testing"
)

func TestComposerShellRadii(t *testing.T) {
	for _, tc := range []struct {
		hero         bool
		card, button float32
	}{{false, 12, 6}, {true, 14, 8}} {
		card, button := composerShellRadii(tc.hero)
		if card != tc.card || button != tc.button {
			t.Fatalf("hero=%v: 圆角 = %v/%v, want %v/%v", tc.hero, card, button, tc.card, tc.button)
		}
	}
}

func TestComposerCardMaxHeight(t *testing.T) {
	for _, tc := range []struct{ viewport, want float32 }{{600, 240}, {2000, 384}} {
		if got := composerCardMaxHeight(tc.viewport); got != tc.want {
			t.Fatalf("视口高度 %v: 上限 = %v, want %v", tc.viewport, got, tc.want)
		}
	}
}

// assertSourceHasNo 读取 internal/ui 下的源码文件，报告所有仍含 forbidden 子串的行。
// 断言直接看源码而不是看渲染结果：主题色漏用令牌时渲染依旧正常，只有源码能暴露。
func assertSourceHasNo(t *testing.T, file string, forbidden []string) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", file, err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		for _, bad := range forbidden {
			if strings.Contains(line, bad) {
				t.Errorf("%s:%d 仍含 %q: %s", file, i+1, bad, strings.TrimSpace(line))
			}
		}
	}
}

// TestComposerSourceUsesThemeTokens 守住输入框周边的取色：颜色只走 Theme* / 具名令牌，
// 不再出现写死的十六进制色与纯白。
func TestComposerSourceUsesThemeTokens(t *testing.T) {
	for _, file := range []string{
		"composer.go",
		"composer_attach.go",
		"composer_mcp_menu.go",
		"composer_downloads.go",
	} {
		assertSourceHasNo(t, file, []string{"ui.Hex(", "ui.RGB(255"})
	}
}

// TestEffortSliderUsesFixedDarkTokens 守住力度滑块卡片的深色面板约束：
// 它固定画在深色浮层上（fixedDarkSurfaceFiles），只能用固定色与白色基色，
// 不能用会随主题翻转的 Theme* 文字色。
func TestEffortSliderUsesFixedDarkTokens(t *testing.T) {
	assertSourceHasNo(t, "effort_slider.go", []string{
		"ui.RGB(255",
		"ui.RGBA(255",
		"ThemePrimaryText(",
		"ThemeSecondaryText(",
		"ThemeAccent(c)",
	})
}
