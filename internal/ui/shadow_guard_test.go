package mcodeui

import (
	"go/ast"
	"go/token"
	"testing"
)

// 阴影颜色守卫：Shadow(..., ui.RGBA(0, 0, 0, 字面量)) 把黑色透明度写死在调用处，
// 深浅主题无法各自调档，也绕过了 ElevPopover / ElevDialog 的统一层叠。
// 新代码请走 applyElevation，或至少用 ui.Color{}.Alpha(...)。elevation.go 是令牌实现，豁免。

// shadowColorBaseline 是现存违例的文件与调用数，只允许减少；新增写死黑色阴影会直接失败。
var shadowColorBaseline = map[string]int{
	"plan_reader.go":            1,
	"scripts_sheet.go":          1,
	"settings_page_models.go":   1,
	"settings_page_swarm.go":    1,
	"settings_swarm_presets.go": 1,
	"settings_swarm_tiers.go":   1,
	"settings_tree.go":          1,
	"sidebar_bulk.go":           1,
	"swarm.go":                  1,
	"table_popup.go":            1,
	"terminal.go":               1,
	"timeline_mention.go":       1,
}

func TestShadowColorUsesElevation(t *testing.T) {
	t.Run("当前代码", func(t *testing.T) {
		for _, v := range shadowColorViolations(t, ".") {
			t.Error(v)
		}
	})
	t.Run("注入违规可检出", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "shadow_color_probe.go", `package probe

type box struct{}

func (b box) Shadow(x, y, z, w float32, c color) box { return b }

func f(card box) {
	card.Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.2))
}
`)
		requireViolation(t, shadowColorViolations(t, dir), "shadow_color_probe.go", 8, "阴影颜色")
	})
	t.Run("白名单文件未超限通过", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "plan_reader.go", `package probe

type box struct{}

func (b box) Shadow(x, y, z, w float32, c color) box { return b }

func f(card box) {
	card.Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.2))
}
`)
		if got := shadowColorViolations(t, dir); len(got) != 0 {
			t.Fatalf("白名单文件未超限不应报错: %v", got)
		}
	})
}

// shadowColorViolations 返回 dir 下把阴影颜色写成 ui.RGBA(0, 0, 0, 字面量) 的调用，
// 未登记的文件与超过白名单数量的文件都算违规。
func shadowColorViolations(t *testing.T, dir string) []styleViolation {
	t.Helper()
	var out []styleViolation
	eachProductionFile(t, dir, func(name string, file *ast.File, fset *token.FileSet) {
		if name == "elevation.go" {
			return
		}
		limit := shadowColorBaseline[name]
		count := 0
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isShadowCall(call) || len(call.Args) < 5 {
				return true
			}
			if !isBlackRGBALiteral(call.Args[4]) {
				return true
			}
			count++
			if count > limit {
				out = append(out, styleViolation{name, fset.Position(call.Pos()).Line,
					"阴影颜色写死黑色透明度：改用 applyElevation / ElevPopover / ElevDialog，或 ui.Color{}.Alpha(...)"})
			}
			return true
		})
	})
	return out
}

// isBlackRGBALiteral 判断表达式是否为 ui.RGBA(0, 0, 0, 字面量)。
func isBlackRGBALiteral(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 4 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "RGBA" {
		return false
	}
	for i := 0; i < 3; i++ {
		lit, ok := call.Args[i].(*ast.BasicLit)
		if !ok || lit.Value != "0" {
			return false
		}
	}
	_, ok = call.Args[3].(*ast.BasicLit)
	return ok
}
