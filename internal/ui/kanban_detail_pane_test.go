package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestKanbanDetailLayout(t *testing.T) {
	for _, tc := range []struct {
		available     float32
		pane          bool
		board, detail float32
	}{
		{619, false, 619, 0}, {766, false, 766, 0}, {767, true, 400, 360}, {919, true, 432, 480}, {1219, true, 732, 480},
	} {
		pane, board, detail := kanbanDetailLayout(tc.available, 0)
		if pane != tc.pane || board != tc.board || detail != tc.detail {
			t.Errorf("width %v: got %v %v %v", tc.available, pane, board, detail)
		}
	}
	_, _, narrow := kanbanDetailLayout(919, 640)
	_, _, restored := kanbanDetailLayout(1219, 640)
	if narrow != 512 || restored != 640 {
		t.Fatalf("clamp/restoration: %v %v", narrow, restored)
	}
	_, _, minimum := kanbanDetailLayout(1219, 1)
	_, _, maximum := kanbanDetailLayout(1219, 900)
	if minimum != 360 || maximum != 640 {
		t.Fatalf("limits: %v %v", minimum, maximum)
	}
}

func TestKanbanDetailHandleDragAndKeyboard(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	ks := &kanbanState{detailWidth: 480, detailTaskID: "task"}
	tt := ui.NewTester(func(c *ui.Context) {
		renderKanbanDetailSplit(c, s, ks, 1219, func(float32) {}, func() { ui.Text(c, "detail") })
	}, 1219, 700)
	tt.Frame()
	r, ok := tt.Find("调整详情宽度")
	if !ok {
		t.Fatal("找不到手柄")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x-40, y)
	tt.Release(x-40, y)
	if ks.detailWidth != 520 || ks.detailTaskID != "task" {
		t.Fatalf("拖动后状态: %v %q", ks.detailWidth, ks.detailTaskID)
	}
	tt.Key(0, ui.KeyLeft)
	if ks.detailWidth != 528 {
		t.Fatalf("键盘调整后宽度: %v", ks.detailWidth)
	}
}
