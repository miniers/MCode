package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 视觉规则守卫（B11）：阴影只给浮层，状态色只取主题 token。
// 沿用 modal_style_guard_test.go 的写法：解析 dir 下的 *.go（跳过 _test.go），按 AST 判定。
// 每条规则都带「注入违规」子测试，避免规则写坏后静默通过。

func TestShadowOnlyOnOverlays(t *testing.T) {
	t.Run("当前代码", func(t *testing.T) {
		for _, v := range shadowViolations(t, ".") {
			t.Error(v)
		}
	})
	t.Run("注入违规可检出", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "shadow_probe.go", `package probe

import "example.com/ui"

func f(box *ui.Box) {
	box.Shadow(0, 0, 4, 0, ui.RGBA(0, 0, 0, 0.2))
}
`)
		requireViolation(t, shadowViolations(t, dir), "shadow_probe.go", 6, "未登记的阴影")
	})
	t.Run("白名单文件未超限通过", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "popover.go", `package probe

type box struct{}

func (b box) Shadow(a, c, d float32, e, f any) box { return b }

func f(card box) {
	card.Shadow(0, 6, 20, 0, 0)
}
`)
		if got := shadowViolations(t, dir); len(got) != 0 {
			t.Fatalf("白名单文件未超限不应报错: %v", got)
		}
	})
}

func TestStatusColorsUseThemeTokens(t *testing.T) {
	t.Run("当前代码", func(t *testing.T) {
		for _, v := range statusColorViolations(t, ".") {
			t.Error(v)
		}
	})
	t.Run("注入违规可检出", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "color_probe.go", `package probe

import "example.com/ui"

func f(c *ui.Context) ui.Color {
	a := ui.Hex("#FF9500")
	_ = ColorDangerLight
	return ThemeDanger(c).Mix(a, 0.5)
}
`)
		got := statusColorViolations(t, dir)
		requireViolation(t, got, "color_probe.go", 6, "禁止复用状态色")
		requireViolation(t, got, "color_probe.go", 7, "禁止裸用 ColorDangerLight")
	})
	t.Run("主题 token 通过", func(t *testing.T) {
		dir := t.TempDir()
		writeProbeFile(t, dir, "color_token.go", `package probe

import "example.com/ui"

func f(c *ui.Context) ui.Color {
	return ui.Hex("#34C759")
}

func g(c *ui.Context) ui.Color { return ThemeDanger(c) }
`)
		if got := statusColorViolations(t, dir); len(got) != 0 {
			t.Fatalf("主题 token 不应报错: %v", got)
		}
	})
}

// shadowBaseline 是允许出现阴影的文件及其调用上限（按当前 AST 口径统计，只允许减少）。
// 未列出的文件出现 Shadow 即失败：阴影只属于浮层/弹窗/浮窗卡片，普通控件加阴影会让卡片外多一圈脏边。
// 注意：popoverCardShadow(...) 是统一的外阴影辅助函数，不是 Shadow 方法调用，不计入本表。
var shadowBaseline = map[string]int{
	"context_panel_views.go":    1,
	"controls_button.go":        2, // 焦点环：零模糊 spread 描边，不是投影
	"elevation.go":              1, // 阴影令牌定义文件，applyElevation 逐层调用
	"memory_panel_views.go":     1,
	"model_picker.go":           2,
	"popover.go":                2,
	"scripts_sheet.go":          1,
	"settings_page_models.go":   1,
	"settings_page_swarm.go":    1,
	"settings_swarm_presets.go": 1,
	"settings_swarm_tiers.go":   1,
	"settings_tree.go":          1, // 设置面板外层（DialogBase 浮层）
	"sidebar_bulk.go":           1,
	"swarm.go":                  1,
	"swarm_bee_floats.go":       3,
	"swarm_timeline_capsule.go": 1,
	"table_popup.go":            1,
	"terminal.go":               1,
	"timeline_mention.go":       1,
}

// shadowViolations 返回 dir 下所有违规的 Shadow 调用：文件未登记，或调用数超过该文件上限。
func shadowViolations(t *testing.T, dir string) []styleViolation {
	t.Helper()
	var out []styleViolation
	eachProductionFile(t, dir, func(name string, file *ast.File, fset *token.FileSet) {
		limit, listed := shadowBaseline[name]
		count := 0
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isShadowCall(call) {
				return true
			}
			count++
			line := fset.Position(call.Pos()).Line
			if !listed {
				out = append(out, styleViolation{name, line,
					"未登记的阴影：非浮层文件禁止 Shadow，改用 Border 或背景分层；确属浮层请登记 shadowBaseline"})
				return true
			}
			if count > limit {
				out = append(out, styleViolation{name, line,
					"Shadow 调用超过基线上限 " + strconv.Itoa(limit) + "，只允许减少不允许新增"})
			}
			return true
		})
	})
	return out
}

func isShadowCall(call *ast.CallExpr) bool {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name == "Shadow"
	case *ast.Ident:
		return fn.Name == "Shadow"
	}
	return false
}

// statusTokenDefinitionsFile 是主题 token 定义处，允许出现原始 hex 与裸 token。
const statusTokenDefinitionsFile = "theme.go"

// statusIdentityPaletteFile 是 swarm 身份色板：给每个 bee 分配可区分的同系系统色，刻意复用系统色 hex。
const statusIdentityPaletteFile = "swarm.go"

// forbiddenStatusHex 是主题状态/强调色的字面量，业务代码只应通过 Theme* 取当前主题色。
var forbiddenStatusHex = map[string]bool{
	"#007aff": true, // accent 浅色
	"#0a84ff": true, // accent 深色
	"#ff3b30": true, // danger 浅色
	"#ff9500": true, // warning 浅色
	"#28cd41": true, // success 浅色
	"#af52de": true, // plan 浅色
}

// bareStatusColorNames 是不带主题判断的固定状态色；浅色值在深色主题下对比度不足。
var bareStatusColorNames = map[string]bool{
	"ColorWarningLight": true,
	"ColorWarningDark":  true,
	"ColorDangerLight":  true,
	"ColorDangerDark":   true,
	"ColorSuccessLight": true,
	"ColorSuccessDark":  true,
	"ColorPlanLight":    true,
	"ColorPlanDark":     true,
	"ColorAccentLight":  true,
	"ColorAccentDark":   true,
}

// fixedDarkSurfaceFiles 是固定画在模型选择浮层深色面板（modelPickerPanelBg 取自 ColorSurfaceMainDark，即 28,28,28）上的文件：
// effort 卡片与分段控件都用深色 token 才是正确取值，换成 Theme* 会在浅色主题下取到浅色值、对比度不足。
var fixedDarkSurfaceFiles = map[string]bool{
	"effort_slider.go": true,
	"model_picker.go":  true,
}

// bareStatusColorBaseline 是裸用固定状态色的过渡基线，只允许减少：
// 目前没有文件欠账，保留空表与守卫逻辑，后续再有欠账时再登记。
var bareStatusColorBaseline = map[string]int{}

// statusColorViolations 返回 dir 下复用状态色 hex、或裸用固定状态色 token 的位置。
func statusColorViolations(t *testing.T, dir string) []styleViolation {
	t.Helper()
	var out []styleViolation
	eachProductionFile(t, dir, func(name string, file *ast.File, fset *token.FileSet) {
		if name == statusTokenDefinitionsFile {
			return
		}
		bareCount := 0
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.CallExpr:
				hex, ok := uiHexLiteral(node)
				if !ok || !forbiddenStatusHex[hex] || name == statusIdentityPaletteFile {
					return true
				}
				out = append(out, styleViolation{name, fset.Position(node.Pos()).Line,
					"禁止复用状态色 " + hex + " 作为字面量：改用 ThemeAccent/ThemeDanger/ThemeWarning/ThemeSuccess/ThemePlan"})
			case *ast.Ident:
				if !bareStatusColorNames[node.Name] {
					return true
				}
				if strings.HasSuffix(node.Name, "Dark") && fixedDarkSurfaceFiles[name] {
					return true
				}
				bareCount++
				line := fset.Position(node.Pos()).Line
				limit, listed := bareStatusColorBaseline[name]
				if !listed {
					out = append(out, styleViolation{name, line,
						"禁止裸用 " + node.Name + "：固定浅/深色在另一主题下对比度不足，改用 Theme* 取当前主题色"})
					return true
				}
				if bareCount > limit {
					out = append(out, styleViolation{name, line,
						"裸用 " + node.Name + " 超过过渡基线上限 " + strconv.Itoa(limit) + "，只允许减少"})
				}
			}
			return true
		})
	})
	return out
}

// uiHexLiteral 识别 ui.Hex("...") 调用并返回小写 hex 字符串。
func uiHexLiteral(call *ast.CallExpr) (string, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "ui" || sel.Sel.Name != "Hex" || len(call.Args) != 1 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(value)), true
}

// styleViolation 是一条视觉规则违规，打印时带 file:line。
type styleViolation struct {
	file string
	line int
	msg  string
}

func (v styleViolation) String() string {
	return v.file + ":" + strconv.Itoa(v.line) + ": " + v.msg
}

// eachProductionFile 依次解析 dir 下的生产代码文件（跳过 _test.go）。
func eachProductionFile(t *testing.T, dir string, fn func(name string, file *ast.File, fset *token.FileSet)) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
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
		fn(filepath.Base(path), file, fset)
	}
}

// writeProbeFile 在临时目录写一个用于注入违规的最小 Go 文件。
func writeProbeFile(t *testing.T, dir, name, src string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// requireViolation 断言扫描结果里存在 file:line 与消息片段都匹配的违规。
func requireViolation(t *testing.T, got []styleViolation, wantFile string, wantLine int, wantMsg string) {
	t.Helper()
	for _, v := range got {
		if v.file == wantFile && v.line == wantLine && strings.Contains(v.msg, wantMsg) {
			return
		}
	}
	t.Fatalf("未报出预期违规 %s:%d（%s），实际: %v", wantFile, wantLine, wantMsg, got)
}
