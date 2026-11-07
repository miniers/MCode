package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestApprovalAutomationInboxNoRawColor 审批、自动化、收件箱三个模块不再直接构造颜色：
// 颜色一律走 Theme*，白色文字走 ThemeOnAccent。
func TestApprovalAutomationInboxNoRawColor(t *testing.T) {
	files := []string{"approval.go", "automation.go", "attention_inbox.go"}
	fset := token.NewFileSet()
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s: %v", name, err)
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
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "ui" {
				return true
			}
			if sel.Sel.Name != "Hex" && sel.Sel.Name != "RGBA" {
				return true
			}
			pos := fset.Position(call.Pos())
			t.Errorf("%s 不应直接调用 ui.%s，请改用 Theme* 令牌", pos, sel.Sel.Name)
			return true
		})
	}
}
