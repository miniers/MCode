package mcodeui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

type stewardTaskIndex struct {
	source, amend map[string][]KanbanTask
}

func indexStewardTasks(tasks []KanbanTask) stewardTaskIndex {
	idx := stewardTaskIndex{source: make(map[string][]KanbanTask), amend: make(map[string][]KanbanTask)}
	for _, task := range tasks {
		if task.Origin != "owner" || task.ParentID != "" {
			continue
		}
		if task.SourceMessageID != "" {
			idx.source[task.SourceMessageID] = append(idx.source[task.SourceMessageID], task)
		}
		seen := make(map[string]bool)
		for _, id := range task.AmendMessageIDs {
			if id != "" && id != task.SourceMessageID && !seen[id] {
				idx.amend[id] = append(idx.amend[id], task)
				seen[id] = true
			}
		}
	}
	return idx
}

func (s *chatState) stewardTaskIndex() (stewardTaskIndex, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := &s.stewardBoard
	if b.index.source == nil || b.indexRev != b.rev {
		b.index = indexStewardTasks(b.tasks)
		b.indexRev = b.rev
	}
	return b.index, b.rev
}

func insertStewardTaskRows(items []timelineItem, idx stewardTaskIndex) []timelineItem {
	if len(idx.source) == 0 && len(idx.amend) == 0 {
		return items
	}
	out := make([]timelineItem, 0, len(items))
	var messageID string
	var turn int
	flush := func() {
		tasks := idx.source[messageID]
		if len(tasks) == 1 {
			task := tasks[0]
			kind := timelineItemStewardTask
			if stewardTaskShouldCollapse(task, time.Now()) {
				kind = timelineItemStewardAmend
			}
			out = append(out, timelineItem{kind: kind, id: "steward-task-" + messageID + "-" + task.ID, turnIndex: turn, stewardTask: task})
		} else if len(tasks) > 1 {
			out = append(out, timelineItem{kind: timelineItemStewardTaskChips, id: "steward-tasks-" + messageID, turnIndex: turn, stewardTasks: tasks})
		}
		for _, task := range idx.amend[messageID] {
			out = append(out, timelineItem{kind: timelineItemStewardAmend, id: "steward-amend-" + messageID + "-" + task.ID, turnIndex: turn, stewardTask: task})
		}
	}
	for _, item := range items {
		if item.kind == timelineItemSingle && item.msg.Role == "user" {
			flush()
			messageID, turn = item.msg.ID, item.turnIndex
		}
		out = append(out, item)
	}
	flush()
	return out
}

func stewardTaskSignal(task KanbanTask) string {
	var text string
	switch task.Status {
	case "in_progress":
		return strings.TrimSpace(task.LastActivity)
	case "failed":
		text = task.LastFailureReason
	case "done":
		text = task.ResultSummary
	}
	return strings.TrimSpace(strings.SplitN(text, "\n", 2)[0])
}

func stewardTaskOpen(s *chatState, task KanbanTask) {
	markStewardTaskSeen(s, task)
	openStewardTask(s, stewardResult{Task: task.ID, Thread: task.ThreadID, Status: task.Status, Summary: task.ResultSummary})
}

func stewardTaskShouldCollapse(task KanbanTask, now time.Time) bool {
	if task.UpdatedAt.IsZero() || now.Sub(task.UpdatedAt) <= stewardBoardDoneWindow {
		return false
	}
	switch task.Status {
	case "done", "failed", "cancelled", "rejected":
		return true
	default:
		return false
	}
}

func markStewardTaskSeen(s *chatState, task KanbanTask) {
	if s == nil || task.ID == "" {
		return
	}
	s.mu.Lock()
	if s.stewardTaskSeen == nil {
		s.stewardTaskSeen = make(map[string]time.Time)
	}
	s.stewardTaskSeen[task.ID] = task.UpdatedAt
	s.mu.Unlock()
}

func markStewardTaskSeenByID(s *chatState, id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	for _, task := range s.stewardBoard.tasks {
		if task.ID == id {
			if s.stewardTaskSeen == nil {
				s.stewardTaskSeen = make(map[string]time.Time)
			}
			s.stewardTaskSeen[id] = task.UpdatedAt
			break
		}
	}
	s.mu.Unlock()
}

func stewardTaskUnread(s *chatState, task KanbanTask) bool {
	if s == nil || task.ID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stewardTaskSeen == nil {
		s.stewardTaskSeen = make(map[string]time.Time)
	}
	baseline, ok := s.stewardTaskSeen[task.ID]
	if !ok {
		s.stewardTaskSeen[task.ID] = task.UpdatedAt
		return false
	}
	switch task.Status {
	case "done", "failed", "needs_approval":
		return task.UpdatedAt.After(baseline)
	default:
		return false
	}
}

func stewardFocusTasks(groups stewardGroups) []KanbanTask {
	const maxTasks = 3
	tasks := make([]KanbanTask, 0, maxTasks)
	for _, task := range groups[0] {
		if len(tasks) == maxTasks {
			return tasks
		}
		tasks = append(tasks, task)
	}
	for _, task := range groups[1] {
		if len(tasks) == maxTasks {
			break
		}
		if task.Status == "in_progress" {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

func renderStewardFocusStrip(c *ui.Context, s *chatState, groups stewardGroups) {
	if s == nil {
		return
	}
	s.mu.Lock()
	selectedID := s.selectedThreadID
	s.mu.Unlock()
	if !stewardSelected(s, selectedID) || (stewardBoardWide(chatAreaWidth(c, s)) && stewardBoardCount(groups) > 0) {
		return
	}
	tasks := stewardFocusTasks(groups)
	if len(tasks) == 0 {
		return
	}
	ui.Row(c.Key("steward-focus-strip")).FillWidth().MaxWidth(timelineContentMaxWidth(s)).AlignSelf(ui.Center).
		Gap(Space2).Wrap().Transition(motionListItem(c)).Children(func() {
		for _, task := range tasks {
			renderStewardTaskChip(c, s, "steward-focus-task-"+task.ID, task)
		}
	})
}

func renderStewardTaskDot(c *ui.Context, key string, task KanbanTask) {
	ui.Box(c.Key(key)).Size(Space2, Space2).Radius(RadiusPill).Background(kanbanStatusColor(c, kanbanDisplayColumn(task))).Shrink(0).Transition(motionFeedback(c))
}

func renderStewardTaskCard(c *ui.Context, s *chatState, key string, task KanbanTask) {
	card := ui.Column(c.Key(key)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).AlignSelf(ui.Center).
		Padding(Space3, Space3).Gap(Space1).Radius(RadiusSM).BorderWidth(1, 1, 1, 1).BorderColor(ThemeBorder(c)).
		Focusable().Label(task.Title).Cursor(ui.CursorPointer).Transition(motionListItem(c))
	if card.Hovered() {
		card.Background(ThemeHover(c))
	}
	if card.Clicked() {
		stewardTaskOpen(s, task)
	}
	card.Children(func() {
		ui.Row(c.Key(key + "-title")).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(Space2).Children(func() {
			if stewardTaskUnread(s, task) {
				ui.Box(c.Key(key+"-unread")).Size(Space1, Space1).Radius(RadiusPill).Background(ThemeAccent(c)).Shrink(0)
			}
			renderStewardTaskDot(c, key+"-dot", task)
			ui.Text(c, task.Title).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
			status := kanbanStatusBadge(task)
			if status == "" && task.Status == "needs_approval" {
				status = "待你处理"
			}
			ui.Text(c.Key(key+"-status-"+status), status).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).Shrink(0).Transition(motionListItem(c))
		})
		if signal := stewardTaskSignal(task); signal != "" {
			ui.Text(c, signal).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).FillWidth().MinWidth(0).SingleLine().Ellipsis("…")
		}
	})
}

func renderStewardTaskChip(c *ui.Context, s *chatState, key string, task KanbanTask) {
	chip := ui.Row(c.Key(key)).MaxWidth(240).MinWidth(0).AlignItems(ui.Center).Gap(Space2).
		Padding(Space1, Space3).Radius(RadiusPill).BorderWidth(1, 1, 1, 1).BorderColor(ThemeBorder(c)).
		Focusable().Label(task.Title).Cursor(ui.CursorPointer).Transition(motionListItem(c))
	if chip.Hovered() {
		chip.Background(ThemeHover(c))
	}
	if chip.Clicked() {
		stewardTaskOpen(s, task)
	}
	chip.Children(func() {
		if stewardTaskUnread(s, task) {
			ui.Box(c.Key(key+"-unread")).Size(Space1, Space1).Radius(RadiusPill).Background(ThemeAccent(c)).Shrink(0)
		}
		renderStewardTaskDot(c, key+"-dot", task)
		ui.Text(c, task.Title).FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c)).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
	})
}

func renderStewardTaskChips(c *ui.Context, s *chatState, key string, tasks []KanbanTask) {
	ui.Row(c.Key(key)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).AlignSelf(ui.Center).Wrap().Gap(Space2).Children(func() {
		for _, task := range tasks {
			renderStewardTaskChip(c, s, key+"-"+task.ID, task)
		}
	})
}
