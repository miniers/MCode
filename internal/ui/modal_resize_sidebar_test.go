package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSidebarSmallDialogs_Resize(t *testing.T) {
	for _, tc := range []struct {
		name, key, card string
		open            func(*chatState)
		render          func(*ui.Context, *chatState)
	}{
		{"rename", modalDragRenameThread, "modal-rename-card", func(s *chatState) { s.renameModalOpen = true }, renderRenameThreadModal},
		{"worktree", modalDragWorktreeMove, "modal-worktree-move-card", func(s *chatState) { getWorktreeUIState(s).moveOpen = true }, renderWorktreeMoveModal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newChatState(NewFakeClient())
			t.Cleanup(s.close)
			t.Cleanup(func() { modalDragReset(tc.key) })
			tc.open(s)
			tt := ui.NewTester(func(c *ui.Context) { tc.render(c, s) }, 1200, 1000)
			tt.Frame()
			initial, ok := tt.Find(tc.card)
			if !ok {
				t.Fatal("未找到弹窗")
			}
			if _, ok := tt.Find("调整弹窗大小"); !ok {
				t.Fatal("未找到缩放抓手")
			}
			x, y := initial.X+initial.W-4, initial.Y+initial.H-4
			tt.Press(x, y)
			tt.Move(x+30, y+30)
			tt.Release(x+30, y+30)
			tt.Frame()
			larger, ok := tt.Find(tc.card)
			if !ok || larger.W <= initial.W || larger.H <= initial.H {
				t.Fatalf("拖大前 %v，拖大后 %v", initial, larger)
			}
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			tc.open(s)
			tt.Frame()
			reopened, ok := tt.Find(tc.card)
			if !ok || reopened.W != initial.W || reopened.H != initial.H {
				t.Fatalf("重新打开尺寸 = %v，初始 = %v", reopened, initial)
			}
		})
	}
}
