package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKanbanModal_Resize(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ks := getKanbanState(s)
	ks.showNewTask = true
	modalDragMu.Lock()
	modalDragOffsets[modalDragKanbanNewTask] = &modalDragOffset{rw: 600, rh: 500}
	modalDragMu.Unlock()
	t.Cleanup(func() { modalDragReset(modalDragKanbanNewTask) })
	tt := ui.NewTester(func(c *ui.Context) { renderKanbanNewTaskSheet(c, s) }, 1200, 1000)
	tt.Frame()
	bounds, ok := tt.Find("kanban-new-task-card")
	if !ok || bounds.W != 600 || bounds.H != 500 {
		t.Fatalf("任务弹窗尺寸 = %v，找到 = %v", bounds, ok)
	}
	if _, ok := tt.Find("调整弹窗大小"); !ok {
		t.Fatal("未找到缩放抓手")
	}
}

func TestContextAndMemoryDialogs_Resize(t *testing.T) {
	for _, tc := range []struct {
		name, key, label string
		render func(*ui.Context, *chatState)
		open func(*chatState)
	}{
		{"context", modalDragContextPanel, "context-modal-card", renderContextModal, func(s *chatState) { getContextPanelState(s).open = true }},
		{"memory", modalDragMemoryPanel, "memory-modal-card", renderMemoryModal, func(s *chatState) { getMemoryPanelState(s).open = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			t.Cleanup(s.close)
			tc.open(s)
			modalDragMu.Lock()
			modalDragOffsets[tc.key] = &modalDragOffset{rw: 700, rh: 650}
			modalDragMu.Unlock()
			t.Cleanup(func() { modalDragReset(tc.key) })
			tt := ui.NewTester(func(c *ui.Context) { tc.render(c, s) }, 1200, 1000)
			tt.Frame()
			bounds, ok := tt.Find(tc.label)
			if !ok || bounds.W != 700 || bounds.H != 650 {
				t.Fatalf("面板尺寸 = %v，找到 = %v", bounds, ok)
			}
			if _, ok := tt.Find("调整弹窗大小"); !ok {
				t.Fatal("未找到缩放抓手")
			}
		})
	}
}
