package mcodeui

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestTextScaleOnlyChatAndSidebar 限制文字缩放的适用范围。
func TestTextScaleOnlyChatAndSidebar(t *testing.T) {
	allowed := map[string]bool{
		"attention_inbox.go": true, "chat.go": true, "composer_changes_tab.go": true,
		"composer_plan_pill.go": true, "kbd.go": true, "session_stats_popover.go": true,
		"sidebar.go": true, "sidebar_activity.go": true, "sidebar_bees.go": true,
		"sidebar_footer.go": true, "sidebar_hover_card.go": true, "sidebar_state_groups.go": true,
		"sidebar_steward.go": true, "sidebar_bulk.go": true,
		"steward_decision_card.go": true, "steward_reply.go": true, "steward_task_page.go": true,
		"steward_split.go": true, "steward_timeline.go": true,
		"swarm_dispatch_placeholder.go": true, "swarm_mentions.go": true,
		"swarm_report_markdown.go": true, "swarm_timeline_capsule.go": true, "text_scale.go": true,
		"timeline_answered_question.go": true, "timeline_background_tasks.go": true, "timeline_banner.go": true,
		"timeline_error_row.go": true, "timeline_mention.go": true, "timeline_minimap.go": true,
		"timeline_rows.go": true, "timeline_diff.go": true, "tool_output_dialog.go": true,
		"turn_files_card.go": true, "controls_message.go": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || allowed[name] {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "scaledFont" {
				t.Errorf("%s: 「文字大小」只缩放聊天内容与侧栏，其余界面请直接用字号令牌", fset.Position(call.Pos()))
			}
			return true
		})
	}
}

// TestChatBodyFontSizeUsesScale 守卫聊天区正文的字号来源：时间线与 bee 提及渲染里的
// 每一处 FontSize 都必须来自 scaledFont（或按字号算出的变量），不能写死数字常量，
// 否则调大「文字大小」后同一条消息里会一半大一半小。
func TestChatBodyFontSizeUsesScale(t *testing.T) {
	for _, path := range []string{"swarm_mentions.go", "timeline_rows.go"} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "FontSize" || len(call.Args) == 0 {
				return true
			}
			if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind != token.STRING {
				t.Errorf("%s: FontSize(%s) 写死了字号，聊天区请改用 FontSize(scaledFont(%s))，让字号跟随「文字大小」",
					fset.Position(call.Pos()), lit.Value, lit.Value)
			}
			return true
		})
	}
}
