package mcodeui

import (
	"os"
	"strings"
	"testing"

	"mcode/internal/config"
)

const webTokensCSSPath = "../../web/src/tokens.css"

// TestWebTokensInSync 保证 web/src/tokens.css 与 Go 令牌逐字节一致。
func TestWebTokensInSync(t *testing.T) {
	want := webTokensCSS()
	if os.Getenv("UPDATE_WEB_TOKENS") == "1" {
		if err := os.WriteFile(webTokensCSSPath, []byte(want), 0o644); err != nil {
			t.Fatalf("写入 %s 失败：%v", webTokensCSSPath, err)
		}
	}
	got, err := os.ReadFile(webTokensCSSPath)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", webTokensCSSPath, err)
	}
	if strings.ReplaceAll(string(got), "\r\n", "\n") != strings.ReplaceAll(want, "\r\n", "\n") {
		t.Fatal("web/src/tokens.css 与 Go 令牌不一致，请运行：UPDATE_WEB_TOKENS=1 go test ./internal/ui -run TestWebTokensInSync")
	}
}

// TestWebTokenPaletteMatchesTheme 保证导出使用内置值，不受当前自定义主题影响。
func TestWebTokenPaletteMatchesTheme(t *testing.T) {
	before := webTokensCSS()
	t.Cleanup(func() { setActiveTheme(config.AppearanceConfig{}) })
	setActiveTheme(config.AppearanceConfig{Themes: []config.ThemeDef{{ID: config.ThemeLight, Base: config.ThemeLight, Colors: map[string]string{"accent": "#123456"}}}})
	if got := webTokensCSS(); got != before {
		t.Fatal("自定义主题不应改变内置 Web 令牌")
	}
}
