package mcodeui

import (
	"go/ast"
	"go/token"
	"strings"
	"testing"
)

var chatScaleFiles = map[string]bool{
	"attention_inbox.go": true, "chat.go": true, "command_activity.go": true, "command_activity_view.go": true, "composer_changes_tab.go": true,
	"composer_plan_pill.go": true, "kbd.go": true, "session_stats_popover.go": true,
	"steward_decision_card.go": true, "steward_reply.go": true, "steward_task_page.go": true,
	"steward_split.go": true, "steward_timeline.go": true, "steward_turn_time.go": true,
	"swarm_bee_window.go":           true,
	"swarm_dispatch_placeholder.go": true, "swarm_mentions.go": true,
	"swarm_report_markdown.go": true, "swarm_timeline_capsule.go": true, "text_scale.go": true,
	"timeline_answered_question.go": true, "timeline_background_tasks.go": true, "timeline_banner.go": true,
	"timeline_error_row.go": true, "timeline_mention.go": true, "timeline_minimap.go": true,
	"timeline_rows.go": true, "timeline_rows_assembly.go": true, "timeline_rows_assistant.go": true,
	"timeline_rows_context.go": true, "timeline_rows_notice.go": true, "timeline_rows_todo.go": true,
	"timeline_rows_tool.go": true, "timeline_rows_tool_group.go": true, "timeline_rows_turn_work.go": true,
	"timeline_rows_user.go": true, "timeline_diff.go": true, "tool_output_dialog.go": true,
	"mermaid_inline.go": true, "pr_review_card.go": true, "turn_files_card.go": true, "controls_message.go": true,
}
var sidebarScaleFiles = map[string]bool{
	"sidebar.go": true, "sidebar_activity.go": true, "sidebar_bees.go": true,
	"sidebar_chats.go": true, "sidebar_footer.go": true, "sidebar_hover_card.go": true,
	"sidebar_state_groups.go": true, "sidebar_steward.go": true, "sidebar_bulk.go": true,
}

// TestTextScaleOnlyChatAndSidebar 限制文字缩放的适用范围。
func TestTextScaleOnlyChatAndSidebar(t *testing.T) {
	for _, pf := range uiProdFiles(t) {
		name, fset, file := pf.Name, pf.Fset, pf.File
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "FontSize" && sidebarScaleFiles[name] && len(call.Args) > 0 {
				font, ok := call.Args[0].(*ast.CallExpr)
				if !ok {
					t.Errorf("%s: 侧栏 FontSize 必须经过 sidebarFont", fset.Position(call.Pos()))
				} else if fn, ok := font.Fun.(*ast.Ident); !ok || fn.Name != "sidebarFont" {
					t.Errorf("%s: 侧栏 FontSize 必须经过 sidebarFont", fset.Position(call.Pos()))
				}
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && name != "text_scale.go" {
				switch ident.Name {
				case "sidebarFont":
					if !sidebarScaleFiles[name] {
						t.Errorf("%s: sidebarFont 只能用于侧栏", fset.Position(call.Pos()))
					}
				case "scaledFont", "chatFont":
					if !chatScaleFiles[name] {
						t.Errorf("%s: 对话字号只能用于对话区域，侧栏请用 sidebarFont", fset.Position(call.Pos()))
					}
				}
			}
			return true
		})
	}
}

// TestChatBodyFontSizeUsesScale 守卫聊天区正文的字号来源：时间线与 bee 提及渲染里的
// 每一处 FontSize 都必须来自 scaledFont（或按字号算出的变量），不能写死数字常量，
// 否则调大「文字大小」后同一条消息里会一半大一半小。
func TestChatBodyFontSizeUsesScale(t *testing.T) {
	for _, pf := range uiProdFiles(t) {
		if !strings.HasPrefix(pf.Name, "timeline_rows") && pf.Name != "swarm_mentions.go" {
			continue
		}
		fset, file := pf.Fset, pf.File
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
