package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEntryAttentionCount(t *testing.T) {
	board := KanbanBoard{
		Tasks: []KanbanTask{
			{ID: "approval", AttentionFlags: []string{"awaiting_approval", "needs_review"}},
			{ID: "input", AttentionFlags: []string{"awaiting_input"}},
			{ID: "failed", AttentionFlags: []string{"failed"}},
			{ID: "review", Column: "review", CanReview: true},
			{ID: "review", AttentionFlags: []string{"needs_review"}},
			{ID: "normal", Status: "todo"},
		},
		Goals: []KanbanGoal{{ID: "review", Status: "plan_review"}, {ID: "review", Status: "plan_review"}, {ID: "other", Status: "running"}},
	}
	if got := sidebarAttentionCount(board); got != 5 {
		t.Fatalf("待处理数 = %d，期望 5", got)
	}
	board.Tasks = board.Tasks[1:]
	if got := sidebarAttentionCount(board); got != 4 {
		t.Fatalf("处理后待处理数 = %d，期望 4", got)
	}
}

func TestEntryIconAliases(t *testing.T) {
	for _, name := range []string{"automation", "automations"} {
		if IconByName(name) != IconCalendarClock() {
			t.Errorf("%s 图标不匹配", name)
		}
	}
	if IconByName("zap") != iconZap {
		t.Error("zap 图标已改变")
	}
}

func TestEntryUnloadedHidesCount(t *testing.T) {
	s := newChatState(nil)
	t.Cleanup(s.close)
	tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, s) }, 500, 100)
	if tt.HasText("0") {
		t.Fatal("未加载时不应显示零角标")
	}
}
