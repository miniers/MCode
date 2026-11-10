package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestApprovalAutomationInboxNoRawColor 审批、自动化、收件箱三个模块不再直接构造颜色：
// 颜色一律走 Theme*，白色文字走 ThemeOnAccent。
func TestApprovalAutomationInboxNoRawColor(t *testing.T) {
	files := []string{"approval.go", "automation.go", "automation_editor.go", "automation_schedule_ui.go", "automation_run_history.go", "attention_inbox.go"}
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

func TestAutomationRenderBoundariesAndKeys(t *testing.T) {
	tests := []struct {
		file, function string
		want           []string
	}{
		{"automation.go", "renderAutomationWorkspaceWithSearch", []string{"automation-detail-col", "renderAutomationRunHistory", "modalDragModal", "renderAutomationEditor"}},
		{"automation_editor.go", "renderAutomationEditor", []string{"automation-save", "automation-name-input", "automation-prompt-input", "renderAutomationScheduleFields", "automation-stop-when-input"}},
		{"automation_schedule_ui.go", "renderAutomationScheduleFields", []string{"form-sched-type-group", "form-sched-params", "automation-interval-input"}},
		{"automation_run_history.go", "renderAutomationRunHistory", []string{"automation-runs-divider", "automation-runs-section", "automation-runs-header"}},
	}
	for _, tt := range tests {
		t.Run(tt.function, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), tt.file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			var fn *ast.FuncDecl
			for _, decl := range file.Decls {
				if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == tt.function {
					fn = d
					break
				}
			}
			if fn == nil {
				t.Fatalf("未找到函数 %s", tt.function)
			}
			next := 0
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || next == len(tt.want) {
					return true
				}
				name := ""
				if id, ok := call.Fun.(*ast.Ident); ok {
					name = id.Name
				} else if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Key" && len(call.Args) == 1 {
					if arg, ok := call.Args[0].(*ast.BasicLit); ok && arg.Kind == token.STRING {
						name, _ = strconv.Unquote(arg.Value)
					}
				}
				if name == tt.want[next] {
					next++
				}
				return true
			})
			if next != len(tt.want) {
				t.Errorf("未按顺序找到 %q", tt.want[next])
			}
		})
	}
}
