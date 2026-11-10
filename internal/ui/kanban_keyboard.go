package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

var kanbanKeyboardMu sync.Mutex
var kanbanKeyboardStates = make(map[*chatState]*kanbanKeyboardState)

type kanbanKeyboardState struct {
	tasks     []KanbanTask
	focusID   string
	pendingID string
}

func kanbanKeyboardUpdate(s *chatState, tasks []KanbanTask, view string) {
	kanbanKeyboardMu.Lock()
	defer kanbanKeyboardMu.Unlock()
	st := kanbanKeyboardStates[s]
	if st == nil {
		st = &kanbanKeyboardState{}
		kanbanKeyboardStates[s] = st
	}
	oldIndex := 0
	for i, t := range st.tasks {
		if t.ID == st.focusID {
			oldIndex = i
			break
		}
	}
	var ordered []KanbanTask
	if view == config.KanbanViewList {
		for _, group := range kanbanListGroups(tasks) {
			ordered = append(ordered, group.tasks...)
		}
	} else {
		for _, col := range kanbanColumnDefs {
			for _, t := range tasks {
				if kanbanDisplayColumn(t) == col.key {
					ordered = append(ordered, t)
				}
			}
		}
	}
	st.tasks = ordered
	if st.focusID == "" {
		return
	}
	for _, t := range ordered {
		if t.ID == st.focusID {
			return
		}
	}
	st.focusID = ""
	st.pendingID = ""
	if len(ordered) > 0 {
		if oldIndex >= len(ordered) {
			oldIndex = len(ordered) - 1
		}
		st.focusID = ordered[oldIndex].ID
		st.pendingID = st.focusID
	}
}

func kanbanKeyboardMove(s *chatState, id string, delta int) {
	kanbanKeyboardMu.Lock()
	st := kanbanKeyboardStates[s]
	if st != nil {
		for i, task := range st.tasks {
			if task.ID == id {
				next := i + delta
				if next >= 0 && next < len(st.tasks) {
					st.focusID = st.tasks[next].ID
					st.pendingID = st.focusID
				}
				break
			}
		}
	}
	kanbanKeyboardMu.Unlock()
	s.invalidate()
}

func kanbanKeyboardMoveColumn(s *chatState, id string, delta int) {
	kanbanKeyboardMu.Lock()
	st := kanbanKeyboardStates[s]
	if st != nil {
		column, row := -1, 0
		for _, task := range st.tasks {
			if task.ID == id {
				for i, col := range kanbanColumnDefs {
					if col.key == kanbanDisplayColumn(task) {
						column = i
						break
					}
				}
				break
			}
		}
		if column >= 0 {
			for _, task := range st.tasks {
				if task.ID == id {
					break
				}
				if kanbanDisplayColumn(task) == kanbanColumnDefs[column].key {
					row++
				}
			}
			for target := column + delta; target >= 0 && target < len(kanbanColumnDefs); target += delta {
				var candidates []KanbanTask
				for _, task := range st.tasks {
					if kanbanDisplayColumn(task) == kanbanColumnDefs[target].key {
						candidates = append(candidates, task)
					}
				}
				if len(candidates) > 0 {
					st.focusID = candidates[min(row, len(candidates)-1)].ID
					st.pendingID = st.focusID
					break
				}
			}
		}
	}
	kanbanKeyboardMu.Unlock()
	s.invalidate()
}

func kanbanKeyboardItem(c *ui.Context, s *chatState, e ui.Element, task KanbanTask) {
	kanbanKeyboardMu.Lock()
	st := kanbanKeyboardStates[s]
	if st != nil && st.pendingID == task.ID {
		e.Focus()
		e.ScrollIntoView()
		st.pendingID = ""
	}
	kanbanKeyboardMu.Unlock()
	if !e.Focused() {
		return
	}
	kanbanKeyboardMu.Lock()
	if st != nil {
		st.focusID = task.ID
	}
	kanbanKeyboardMu.Unlock()
	e.BorderWidth(kanbanDropBorder).BorderColor(ThemeAccent(c))
	switch {
	case e.Shortcut(0, ui.KeyUp), e.Shortcut(0, ui.KeyK):
		kanbanKeyboardMove(s, task.ID, -1)
	case e.Shortcut(0, ui.KeyDown), e.Shortcut(0, ui.KeyJ):
		kanbanKeyboardMove(s, task.ID, 1)
	case e.Shortcut(0, ui.KeyLeft):
		ks := getKanbanState(s)
		kanbanMu.Lock()
		board := ks.viewMode != config.KanbanViewList
		kanbanMu.Unlock()
		if board {
			kanbanKeyboardMoveColumn(s, task.ID, -1)
		}
	case e.Shortcut(0, ui.KeyRight):
		ks := getKanbanState(s)
		kanbanMu.Lock()
		board := ks.viewMode != config.KanbanViewList
		kanbanMu.Unlock()
		if board {
			kanbanKeyboardMoveColumn(s, task.ID, 1)
		}
	case e.Shortcut(0, ui.KeyEnter):
		s.openTaskDetail(task)
	case e.Shortcut(ui.Shift, ui.KeyN):
		kanbanNewGoalAction(s)
	case e.Shortcut(0, ui.KeyN):
		ks := getKanbanState(s)
		kanbanMu.Lock()
		list := ks.viewMode == config.KanbanViewList
		kanbanMu.Unlock()
		if list {
			kanbanNewTaskAction(s)
		} else {
			kanbanQuickAddAction(s)
		}
	}
}
