package mcodeui

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// motionSpringAnimations 是允许直接传给 AnimateTo 的弹簧动画：弹簧没有时长可包，
// mygo 在减少动效时会把值直接送到目标，调用点只需在减少动效时绕过它（见 chat_anchor.go）。
var motionSpringAnimations = map[string]bool{
	"ui.Smooth": true,
	"ui.Snappy": true,
	"ui.Bouncy": true,
}

// TestMotionGuardAnimateDurations 用 go/parser 扫 internal/ui 下所有非测试 .go 文件，
// 要求每个 AnimateTo/AnimateWith/Animate 调用的时长都来自 animateDuration(c, d)，
// 这样减少动效时全部降为 0；弹簧动画只在 motionSpringAnimations 白名单里放行。
func TestMotionGuardAnimateDurations(t *testing.T) {
	const dir = "internal/ui"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", dir, err)
	}
	var failures []string
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("解析 %s/%s 失败：%v", dir, name, err)
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
			// AnimateTo 的时长在 Animation 结构里，AnimateWith/Animate 直接收时长。
			if sel.Sel.Name != "AnimateTo" && sel.Sel.Name != "AnimateWith" && sel.Sel.Name != "Animate" {
				return true
			}
			if len(call.Args) < 3 {
				return true
			}
			checked++
			pos := fset.Position(call.Pos())
			dur := call.Args[2]
			if sel.Sel.Name != "AnimateTo" {
				if !isAnimateDurationCall(dur) {
					failures = append(failures, fmt.Sprintf("%s/%s:%d %s 的时长不是 animateDuration(...)",
						dir, name, pos.Line, sel.Sel.Name))
				}
				return true
			}
			if isMotionSpringAnimation(dur) {
				return true
			}
			value, ok := animationDurationField(dur)
			if !ok {
				failures = append(failures, fmt.Sprintf("%s/%s:%d AnimateTo 的动画不是带 Duration 的 ui.Animation 字面量或白名单弹簧",
					dir, name, pos.Line))
				return true
			}
			if !isAnimateDurationCall(value) {
				failures = append(failures, fmt.Sprintf("%s/%s:%d AnimateTo 的 Duration 不是 animateDuration(...)",
					dir, name, pos.Line))
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("没有扫到任何动画调用，守卫失效")
	}
	if len(failures) > 0 {
		t.Fatalf("动画时长必须经 animateDuration 取：\n%s", strings.Join(failures, "\n"))
	}
}

// isAnimateDurationCall 判断表达式是否是对 animateDuration 的直接调用。
// animateDuration 是包内函数，调用点写成 *ast.Ident；带包名的 *ast.SelectorExpr 也一并接受。
func isAnimateDurationCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "animateDuration"
	case *ast.SelectorExpr:
		return fun.Sel.Name == "animateDuration"
	}
	return false
}

// TestAnimateDurationCallShape 锁定守卫对包内函数调用的识别，避免再次误报。
func TestAnimateDurationCallShape(t *testing.T) {
	expr, err := parser.ParseExpr("animateDuration(c, DurSnap)")
	if err != nil {
		t.Fatalf("解析 animateDuration 调用失败：%v", err)
	}
	if !isAnimateDurationCall(expr) {
		t.Fatal("animateDuration(c, DurSnap) 应被识别为 animateDuration 调用")
	}
	expr, err = parser.ParseExpr("DurSnap")
	if err != nil {
		t.Fatalf("解析 DurSnap 失败：%v", err)
	}
	if isAnimateDurationCall(expr) {
		t.Fatal("DurSnap 不应被识别为 animateDuration 调用")
	}
}

// isMotionSpringAnimation 判断表达式是否在白名单里的弹簧动画（可带取址前缀）。
// 白名单值是 ui.Smooth 这类包名限定写法，因此这里按 *ast.SelectorExpr 判断。
func isMotionSpringAnimation(expr ast.Expr) bool {
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = unary.X
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "ui" {
		return false
	}
	return motionSpringAnimations["ui."+sel.Sel.Name]
}

// animationDurationField 从 ui.Animation 字面量里取出 Duration 字段的表达式。
func animationDurationField(expr ast.Expr) (ast.Expr, bool) {
	if unary, ok := expr.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		expr = unary.X
	}
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if ok && key.Name == "Duration" {
			return kv.Value, true
		}
	}
	return nil, false
}
