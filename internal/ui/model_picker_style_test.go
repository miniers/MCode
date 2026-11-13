package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"mcode/internal/testenv"
)

// parseUIFile 解析 internal/ui 下的源文件，供样式断言逐个节点检查。
func parseUIFile(t *testing.T, name string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", name, err)
	}
	return fset, f
}

// uiSelector 判断节点是否是 pkg.name 形式的包级调用目标。
func uiSelector(node ast.Node, pkg, name string) bool {
	sel, ok := node.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	return ok && ident.Name == pkg && sel.Sel.Name == name
}

// 模型面板是固定深色面，颜色一律走 Theme* 或固定的深色令牌；自造品牌蓝必须清干净。
func TestModelPickerHasNoHexColors(t *testing.T) {
	fset, f := parseUIFile(t, "model_picker.go")
	ast.Inspect(f, func(n ast.Node) bool {
		if !uiSelector(n, "ui", "Hex") {
			return true
		}
		t.Errorf("model_picker.go:%d 仍在用 ui.Hex 写死颜色，请改用 Theme*", fset.Position(n.Pos()).Line)
		return true
	})
}

// 用量面板的阴影由 ElevPopover 统一提供，不能再写死黑色投影。
func TestUsagePanelHasNoHardcodedBlackShadow(t *testing.T) {
	fset, f := parseUIFile(t, "composer_usage_panel.go")
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 3 || !uiSelector(call.Fun, "ui", "RGBA") {
			return true
		}
		for _, arg := range call.Args[:3] {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Value != "0" {
				return true
			}
		}
		t.Errorf("composer_usage_panel.go:%d 仍在写死 ui.RGBA(0, 0, 0, …)，请改用 ElevPopover", fset.Position(call.Pos()).Line)
		return true
	})
}

// 额度浮层已迁到 PopoverCard：打开后标题与列表可见，点面板外部即关闭并清空打开状态。
func TestComposerQuotaPopoverOpensAndClosesOnOutsideClick(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	th := &Thread{ID: "t1", Provider: "pi:magpie-jp"}
	SetChatQuotaWindowsForProvider(s, th.Provider, quotaPopoverWindows(2))
	t.Cleanup(func() { SetChatQuotaWindowsForProvider(s, th.Provider, nil) })

	const width, height = 480, 360
	tt := quotaPopoverTester(s, th, width, height)
	if err := tt.Click("额度 " + th.Provider); err != nil {
		t.Fatalf("打开额度弹层失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("额度详情")
		return ok
	})

	// 选项可见：两个账号的窗口都在列表里。
	if !tt.HasText("账号0") || !tt.HasText("账号1") {
		t.Fatalf("弹层选项不可见，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("额度列表"); !ok {
		t.Fatal("额度列表应可见")
	}

	// 额度条贴底，面板从锚点往上长；左上角与右上角都在面板之外。
	for _, pt := range [][2]float32{{width - 2, 2}, {2, 2}} {
		tt.ClickAt(pt[0], pt[1])
		tt.Frame()
		if _, ok := tt.Find("额度详情"); !ok {
			break
		}
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("额度详情")
		return !ok
	})
	if s.composerView.quotaOpen {
		t.Fatal("外部点击后应清空额度弹层的全局打开状态")
	}
}
