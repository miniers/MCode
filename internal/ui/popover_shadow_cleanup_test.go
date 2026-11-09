package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPopoverCardHasNoExtraShadow(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			outer, ok := node.(*ast.CallExpr)
			if !ok || len(outer.Args) != 1 {
				return true
			}
			outerName, ok := outer.Fun.(*ast.Ident)
			if !ok || outerName.Name != "popoverCardShadow" {
				return true
			}
			inner, ok := ast.Unparen(outer.Args[0]).(*ast.CallExpr)
			if !ok {
				return true
			}
			innerName, ok := inner.Fun.(*ast.Ident)
			if ok && innerName.Name == "popoverCard" {
				t.Errorf("%s: popoverCard 已有阴影", fset.Position(outer.Pos()))
			}
			return true
		})
	}
}

func TestSwarmGlyphShowsInitialWithoutIcon(t *testing.T) {
	bee := SwarmBee{Name: "测试代理"}
	asset := findSwarmPersona(bee.Name).Asset
	beeIconCacheMu.Lock()
	previous, existed := beeIconCache[asset]
	beeIconCache[asset] = nil
	beeIconCacheMu.Unlock()
	t.Cleanup(func() {
		beeIconCacheMu.Lock()
		defer beeIconCacheMu.Unlock()
		if existed {
			beeIconCache[asset] = previous
		} else {
			delete(beeIconCache, asset)
		}
	})

	tester := ui.NewTester(func(c *ui.Context) {
		renderSwarmGlyph(c, "test-bee", bee, 32)
	}, 100, 100)
	if !tester.HasText("测") {
		t.Fatal("子代理头像未显示首字")
	}
}
