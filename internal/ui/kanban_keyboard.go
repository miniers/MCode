package mcodeui

import (
	"sync"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

var kanbanKeyboardMu sync.Mutex
var kanbanKeyboardStates = make(map[*chatState]*kanbanKeyboardState)

type kanbanKeyboardState struct {
	tasks       []KanbanTask
	focusID     string
	pendingID   string
	selected    map[string]bool
	anchorID    string
	projectID   string
	searchFocus bool
}

func kanbanKeyboardSearchFocus(s *chatState, set bool) bool {
	kanbanKeyboardMu.Lock()
	defer kanbanKeyboardMu.Unlock()
	st := kanbanKeyboardStates[s]
	if st == nil {
		st = &kanbanKeyboardState{}
		kanbanKeyboardStates[s] = st
	}
	if set {
		st.searchFocus = true
		return true
	}
	pending := st.searchFocus
	st.searchFocus = false
	return pending
}

func kanbanKeyboardUpdate(s *chatState, tasks []KanbanTask, view string) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	doneExpanded := ks.doneExpanded
	kanbanMu.Unlock()
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
			if col.key == "done" && !doneExpanded {
				continue
			}
			for _, t := range tasks {
				if kanbanDisplayColumn(t) == col.key {
					ordered = append(ordered, t)
				}
			}
		}
	}
	st.tasks = ordered
	visible := make(map[string]bool, len(ordered))
	for _, task := range ordered {
		visible[task.ID] = true
	}
	for id := range st.selected {
		if !visible[id] {
			delete(st.selected, id)
		}
	}
	if !visible[st.anchorID] {
		st.anchorID = ""
	}
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

func kanbanKeyboardProject(s *chatState, projectID string) {
	kanbanKeyboardMu.Lock()
	st := kanbanKeyboardStates[s]
	if st == nil {
		st = &kanbanKeyboardState{projectID: projectID}
		kanbanKeyboardStates[s] = st
	} else if st.projectID != projectID {
		st.projectID = projectID
		st.selected = nil
		st.anchorID = ""
	}
	kanbanKeyboardMu.Unlock()
}

func kanbanKeyboardSelect(s *chatState, id string, mode string) {
	kanbanKeyboardMu.Lock()
	st := kanbanKeyboardStates[s]
	if st != nil {
		if st.selected == nil {
			st.selected = make(map[string]bool)
		}
		switch mode {
		case "all":
			for _, task := range st.tasks {
				st.selected[task.ID] = true
			}
			st.anchorID = id
		case "range":
			if st.anchorID == "" {
				st.anchorID = st.focusID
			}
			start, end := -1, -1
			for i, task := range st.tasks {
				if task.ID == st.anchorID {
					start = i
				}
				if task.ID == id {
					end = i
				}
			}
			if start < 0 {
				start = end
				st.anchorID = id
			}
			if start >= 0 && end >= 0 {
				clear(st.selected)
				for i := min(start, end); i <= max(start, end); i++ {
					st.selected[st.tasks[i].ID] = true
				}
			}
		default:
			if st.selected[id] {
				delete(st.selected, id)
			} else {
				st.selected[id] = true
			}
			st.anchorID = id
		}
	}
	kanbanKeyboardMu.Unlock()
	s.invalidate()
}

func kanbanKeyboardSelected(s *chatState, id string) bool {
	kanbanKeyboardMu.Lock()
	defer kanbanKeyboardMu.Unlock()
	st := kanbanKeyboardStates[s]
	return st != nil && st.selected[id]
}

func kanbanKeyboardClearSelection(s *chatState) bool {
	kanbanKeyboardMu.Lock()
	defer kanbanKeyboardMu.Unlock()
	st := kanbanKeyboardStates[s]
	if st == nil || len(st.selected) == 0 {
		return false
	}
	clear(st.selected)
	st.anchorID = ""
	return true
}

func kanbanKeyboardExtend(s *chatState, id string, delta int) {
	kanbanKeyboardMu.Lock()
	st := kanbanKeyboardStates[s]
	target := ""
	if st != nil {
		for i, task := range st.tasks {
			if task.ID == id && i+delta >= 0 && i+delta < len(st.tasks) {
				if st.anchorID == "" {
					st.anchorID = id
				}
				target = st.tasks[i+delta].ID
				st.focusID, st.pendingID = target, target
				break
			}
		}
	}
	kanbanKeyboardMu.Unlock()
	if target != "" {
		kanbanKeyboardSelect(s, target, "range")
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
	case e.Shortcut(0, ui.KeySpace):
		kanbanKeyboardSelect(s, task.ID, "toggle")
	case e.Shortcut(ui.Cmd, ui.KeyA):
		kanbanKeyboardSelect(s, task.ID, "all")
	case e.Shortcut(ui.Shift, ui.KeyUp):
		kanbanKeyboardExtend(s, task.ID, -1)
	case e.Shortcut(ui.Shift, ui.KeyDown):
		kanbanKeyboardExtend(s, task.ID, 1)
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
