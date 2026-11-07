package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
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
	if string(got) != want {
		t.Fatal("web/src/tokens.css 与 Go 令牌不一致，请运行：UPDATE_WEB_TOKENS=1 go test ./internal/ui -run TestWebTokensInSync")
	}
}

// TestWebTokenPaletteMatchesTheme 保证 webThemeHex 镜像的仍是 theme.go 里的值。
func TestWebTokenPaletteMatchesTheme(t *testing.T) {
	declared := themeHexDecls(t)
	for name, hex := range webThemeHex {
		want, ok := declared[name]
		if !ok {
			t.Errorf("theme.go 里找不到 %s，请同步 web_tokens.go", name)
			continue
		}
		if !strings.EqualFold(want, hex) {
			t.Errorf("%s 不一致：web_tokens.go 是 %s，theme.go 是 %s", name, hex, want)
		}
	}
}

// themeHexDecls 读出 theme.go 里所有 ui.Hex("…") 形式的颜色常量。
func themeHexDecls(t *testing.T) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "theme.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 theme.go 失败：%v", err)
	}
	declared := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok {
			return true
		}
		for i, name := range spec.Names {
			if i >= len(spec.Values) {
				continue
			}
			call, ok := spec.Values[i].(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Hex" {
				continue
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			hex, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			declared[name.Name] = hex
		}
		return true
	})
	return declared
}
