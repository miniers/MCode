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
	renderTaskDetailDrawerSplit(c, taskDetailDrawerSplitOpts{
		AvailableWidth: availableWidth,
		PreferredWidth: preferredWidth,
		SplitKey:       "kanban-detail-split",
		BoardKey:       "kanban-detail-board",
		HandleKey:      "kanban-detail-handle",
		PaneKey:        "kanban-detail-pane",
		OnWidthChange: func(width float32) {
			kanbanMu.Lock()
			ks.detailWidth = width
			kanbanMu.Unlock()
			s.invalidate()
		},
		OnWidthCommit: func(width float32) {
			rememberTasksDetailWidth(ks, width)
		},
		Board:  board,
		Detail: detail,
	})
}
