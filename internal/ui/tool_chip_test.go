package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestToolMessageState(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   ToolState
	}{
		{"pending", ToolPending}, {"running", ToolRunning}, {"background", ToolRunning},
		{"completed", ToolDone}, {"failed", ToolError}, {"blocked", ToolError},
	} {
		if got := toolMessageState(Message{ToolStatus: tc.status}); got != tc.want {
			t.Errorf("状态 %q = %v，期望 %v", tc.status, got, tc.want)
		}
	}
}

func TestPendingToolInputClosesOnCompletion(t *testing.T) {
	s := newChatState(NewFakeClient())
	m := Message{ID: "pending-input", Role: "tool", ToolName: "read", ToolStatus: "pending", ToolInput: `{"path":"unique-pending-file"}`}
	tt := ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 800, 600)
	if !tt.HasText("unique-pending-file") {
		t.Fatal("等待时应显示入参")
	}
	m.ToolStatus = "completed"
	tt = ui.NewTester(func(c *ui.Context) { renderSingleToolItem(c, s, m) }, 800, 600)
	if tt.HasText(m.ToolInput) {
		t.Fatal("完成后应收起入参")
	}
	s.toggleToolChip(m, []Message{m})
	if !s.toolMessageExpanded(m) {
		t.Fatal("手动展开应保留")
	}
}
