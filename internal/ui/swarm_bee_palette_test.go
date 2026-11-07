package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestSwarmRosterUsesIdentityPalette 断言名册里每个名字都能从 identityPalette 取到颜色，
// 不会落到 ThemeIdentity 的兜底色；前 8 个名字的颜色必须互不相同，避免名册整体撞色。
func TestSwarmRosterUsesIdentityPalette(t *testing.T) {
	if len(swarmRosterPersonas) < 8 {
		t.Fatalf("名册只有 %d 条，不足以校验前 8 个颜色", len(swarmRosterPersonas))
	}
	seen := make(map[interface{}]string, 8)
	for i, p := range swarmRosterPersonas {
		if p.Name == "" {
			t.Fatalf("名册第 %d 条缺少名字", i)
		}
		color := ThemeIdentity(p.Name)
		if color == ColorTextSecondaryLight {
			t.Errorf("名册 %q 取到兜底色，说明 identityPalette 缺少小写键 %q", p.Name, p.Name)
		}
		if i >= 8 {
			continue
		}
		if prev, ok := seen[color]; ok {
			t.Errorf("名册前 8 个颜色重复：%q 与 %q 都是同一个颜色", prev, p.Name)
			continue
		}
		seen[color] = p.Name
	}
}

// TestSwarmBeeFloatsHasNoInlineHex 断言浮动卡源码不再内联十六进制颜色，人设色统一走 identityPalette。
func TestSwarmBeeFloatsHasNoInlineHex(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "swarm_bee_floats.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 swarm_bee_floats.go 失败: %v", err)
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
		if sel.Sel.Name == "Hex" {
			t.Errorf("swarm_bee_floats.go:%d 仍内联 ui.Hex，请改为 ThemeIdentity", fset.Position(call.Pos()).Line)
		}
		return true
	})
}
