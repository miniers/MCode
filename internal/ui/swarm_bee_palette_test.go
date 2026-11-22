package mcodeui

import (
	"fmt"
	"go/ast"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestSwarmRosterUsesIdentityPalette 断言 25 位名册都能从 identityPalette 取到互不相同的颜色，
// 不会落到 ThemeIdentity 的兜底色，且全部 25 枚图标资源可正常解析。
func TestSwarmRosterUsesIdentityPalette(t *testing.T) {
	if len(swarmRosterPersonas) != 25 {
		t.Fatalf("名册长度为 %d，期望 25", len(swarmRosterPersonas))
	}
	if swarmRosterPersonas[0].Name != "Aria" || swarmRosterPersonas[len(swarmRosterPersonas)-1].Name != "Zoe" {
		t.Fatalf("名册首末位 = %q / %q，期望 Aria / Zoe", swarmRosterPersonas[0].Name, swarmRosterPersonas[len(swarmRosterPersonas)-1].Name)
	}
	seen := make(map[ui.Color]string, len(swarmRosterPersonas))
	for i, p := range swarmRosterPersonas {
		if p.Name == "" {
			t.Fatalf("名册第 %d 条缺少名字", i)
		}
		color := ThemeIdentity(p.Name)
		if color == ColorTextSecondaryLight {
			t.Errorf("名册 %q 取到兜底色，说明 identityPalette 缺少小写键 %q", p.Name, p.Name)
		}
		if prev, ok := seen[color]; ok {
			t.Errorf("名册颜色重复：%q 与 %q 都是同一个颜色", prev, p.Name)
		} else {
			seen[color] = p.Name
		}
		wantAsset := fmt.Sprintf("swarm-bee-%02d.svg", i+1)
		if p.Asset != wantAsset {
			t.Errorf("名册 %q 资源名 = %q，期望 %q", p.Name, p.Asset, wantAsset)
		}
		if safePersonaIcon(p.Asset) == nil {
			t.Errorf("图标资源 %q 解析为 nil", p.Asset)
		}
	}
}

// TestSwarmBeeFloatsHasNoInlineHex 断言浮动卡源码不再内联十六进制颜色，人设色统一走 identityPalette。
func TestSwarmBeeFloatsHasNoInlineHex(t *testing.T) {
	pf := uiProdFileByName(t, "swarm_bee_floats.go")
	fset, file := pf.Fset, pf.File
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
