package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// 禁止在 internal/ui 的生产代码中调用 ui.Modal / ui.Popover（它们自带背景、内边距和阴影，会让自绘卡片外多一圈黑边）。
func TestNoStyledModalOrPopover(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob *.go: %v", err)
	}
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "ui" {
				return true
			}
			if sel.Sel.Name != "Modal" && sel.Sel.Name != "Popover" {
				return true
			}
			t.Errorf("%s: 禁止使用 ui.%s：ui.Modal / ui.Popover 自带背景、内边距和阴影，会让自绘卡片外多一圈黑边；弹窗请用 ui.DialogBase 或 modalDragModal，浮层请用 ui.PopoverBase 或 appPopover", fset.Position(call.Pos()), sel.Sel.Name)
			return true
		})
	}
}
