package mcodeui

import (
	"strings"

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
			out = append(out, timelineItem{kind: timelineItemStewardTask, id: "steward-task-" + messageID + "-" + task.ID, turnIndex: turn, stewardTask: task})
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
	openStewardTask(s, stewardResult{Task: task.ID, Thread: task.ThreadID, Status: task.Status, Summary: task.ResultSummary})
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
