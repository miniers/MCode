package mcodeui

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestKanbanPriorityStyleTokens 固定高/中/低优先级的文案与主题色，
// 确保「中」继续取 ThemeCaution，不会退回硬编码黄色。
func TestKanbanPriorityStyleTokens(t *testing.T) {
	cases := []struct {
		priority string
		label    string
		color    ui.Color
	}{
		{"high", "高", ThemeWarning(nil)},
		{"medium", "中", ThemeCaution(nil)},
		{"low", "低", ThemeAccent(nil)},
	}
	for _, tc := range cases {
		label, icon, color, ok := kanbanPriorityStyle(nil, tc.priority)
		if !ok {
			t.Fatalf("kanbanPriorityStyle(%q) ok=false，期望 true", tc.priority)
		}
		if label != tc.label {
			t.Fatalf("kanbanPriorityStyle(%q) label=%q，期望 %q", tc.priority, label, tc.label)
		}
		if icon == nil {
			t.Fatalf("kanbanPriorityStyle(%q) 未返回图标", tc.priority)
		}
		if color != tc.color {
			t.Fatalf("kanbanPriorityStyle(%q) 颜色不是主题令牌", tc.priority)
		}
	}
}

// TestKanbanFilesUseDesignTokens 断言看板三文件不再出现硬编码颜色：
// ui.Hex(...) 与纯黑阴影 ui.RGBA(0, 0, 0, ...)；阴影统一走 ElevDialog/ElevPopover 令牌。
func TestKanbanFilesUseDesignTokens(t *testing.T) {
	banned := []string{"ui.Hex(", "ui.RGBA(0, 0, 0"}
	for _, name := range []string{"kanban.go", "kanban_modal.go", "kanban_sheets.go"} {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		if _, err := parser.ParseFile(token.NewFileSet(), name, src, parser.ParseComments); err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, bannedText := range banned {
			if bytes.Contains(src, []byte(bannedText)) {
				t.Errorf("%s 仍含 %s，请改用 Theme*/Elev* 令牌", name, bannedText)
			}
		}
	}
}
