package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
)

var uiFontToken = regexp.MustCompile(`^(Type|FontSize)[A-Z]`)

func unscaledUIFont(expr ast.Expr) bool {
	if call, ok := expr.(*ast.CallExpr); ok {
		if name, ok := call.Fun.(*ast.Ident); ok {
			switch name.Name {
			case "uiFont", "scaledFont", "chatFont", "sidebarFont":
				return false
			}
		}
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && uiFontToken.MatchString(id.Name) {
			found = true
		}
		return true
	})
	return found
}

func TestUIFontGuard(t *testing.T) {
	skip := map[string]bool{"text_scale.go": true, "theme.go": true, "tokens.go": true, "web_tokens.go": true, "elevation.go": true}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || skip[name] || chatScaleFiles[name] || sidebarScaleFiles[name] {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "FontSize" && unscaledUIFont(call.Args[0]) {
				pos := fset.Position(call.Pos())
				t.Errorf("%s:%d FontSize 写死了界面字号，请改用 FontSize(uiFont(...))，让字号跟随主题的界面字号", pos.Filename, pos.Line)
			}
			return true
		})
	}
}

func TestUnscaledUIFont(t *testing.T) {
	for _, tc := range []struct {
		expr string
		want bool
	}{
		{"TypeBody", true}, {"TypeBody + FontSizeCallout", true}, {"uiFont(TypeBody)", false},
		{"scaledFont(TypeBody)", false}, {"chatFont(TypeBody)", false}, {"sidebarFont(TypeBody)", false},
		{"codeFontSize()", false}, {"12", false}, {"other(TypeBody)", true},
	} {
		t.Run(tc.expr, func(t *testing.T) {
			expr, err := parser.ParseExpr(tc.expr)
			if err != nil {
				t.Fatal(err)
			}
			if got := unscaledUIFont(expr); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
