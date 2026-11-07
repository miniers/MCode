package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestSettingsPagesNoRawColor 设置其余页不再直接构造颜色：颜色一律走 Theme*，
// 白色文字走 ThemeOnAccent，品牌色走 ThemeIdentity。
func TestSettingsPagesNoRawColor(t *testing.T) {
	files := []string{
		"settings_mcp_controls.go",
		"settings_agy.go",
		"settings_magpie_hub.go",
		"settings_status.go",
		"settings_page_models.go",
		"settings_agent_magpie.go",
		"settings_providers.go",
		"settings_remote.go",
		"settings_swarm_tiers.go",
	}
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
			switch sel.Sel.Name {
			case "Hex", "RGB", "RGBA":
				pos := fset.Position(call.Pos())
				t.Errorf("%s 不应直接调用 ui.%s，请改用 Theme* 令牌", pos, sel.Sel.Name)
			}
			return true
		})
	}
}
