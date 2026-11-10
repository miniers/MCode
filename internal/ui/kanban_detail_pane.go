package mcodeui

import "github.com/egoist/mygo/ui"

// kanbanDetailLayout 返回是否内联，以及看板和详情的实际宽度。
func kanbanDetailLayout(availableWidth, preferredWidth float32) (pane bool, boardWidth, detailWidth float32) {
	if availableWidth < KanbanBoardPaneMinWidth+sidePaneHandleWidth+KanbanDetailMinWidth {
		return false, availableWidth, 0
	}
	detailWidth = min(clampTasksDetailWidth(preferredWidth), availableWidth-KanbanBoardPaneMinWidth-sidePaneHandleWidth)
	return true, availableWidth - sidePaneHandleWidth - detailWidth, detailWidth
}

func renderKanbanDetailSplit(c *ui.Context, s *chatState, ks *kanbanState, availableWidth float32, board func(float32), detail func()) {
	kanbanMu.Lock()
	preferredWidth := ks.detailWidth
	kanbanMu.Unlock()
	pane, boardWidth, detailWidth := kanbanDetailLayout(availableWidth, preferredWidth)
	if !pane {
		board(boardWidth)
		return
	}
	ui.Row(c.Key("kanban-detail-split")).Fill().Children(func() {
		ui.Column(c.Key("kanban-detail-board")).Width(boardWidth).FillHeight().Shrink(0).Children(func() { board(boardWidth) })
		handle := ui.Box(c.Key("kanban-detail-handle")).Label("调整详情宽度").Width(sidePaneHandleWidth).FillHeight().Shrink(0).Focusable().Role(ui.RoleSplitter).Cursor(ui.CursorResizeEW)
		dx, _, held := handle.Dragged()
		_, _, released := handle.DragEnded()
		adjust := func(delta float32) float32 {
			_, _, width := kanbanDetailLayout(availableWidth, max(KanbanDetailMinWidth, detailWidth+delta))
			kanbanMu.Lock()
			ks.detailWidth = width
			kanbanMu.Unlock()
			s.invalidate()
			return width
		}
		if held && dx != 0 {
			adjust(-dx)
		}
		if released {
			kanbanMu.Lock()
			width := ks.detailWidth
			kanbanMu.Unlock()
			rememberTasksDetailWidth(ks, clampTasksDetailWidth(width))
		}
		handle.OnShortcut(0, ui.KeyLeft, func() { rememberTasksDetailWidth(ks, adjust(sidePaneHandleStep)) })
		handle.OnShortcut(0, ui.KeyRight, func() { rememberTasksDetailWidth(ks, adjust(-sidePaneHandleStep)) })
		color := ThemeDivider(c)
		if held || handle.Hovered() {
			color = ThemeAccent(c)
		}
		handle.Children(func() {
			ui.Box(c.Key("kanban-detail-handle-line")).Absolute().Top(0).Bottom(0).Left((sidePaneHandleWidth - Hairline) / 2).Width(Hairline).Background(color)
		})
		ui.Column(c.Key("kanban-detail-pane")).Width(detailWidth).FillHeight().Shrink(0).Background(ThemeSurfaceRaised(c)).Transition(motionSidePanel(c, false)).Children(detail)
	})
}
