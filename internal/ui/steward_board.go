package mcodeui

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	stewardBoardWidth      float32 = 260
	stewardChatMinWidth    float32 = 560
	stewardBoardDoneWindow         = 24 * time.Hour
)

type stewardBoardState struct {
	active           bool
	generation       uint64
	rev              uint64
	indexRev         uint64
	index            stewardTaskIndex
	stop             func()
	loading, pending bool
	tasks            []KanbanTask
	open, doneOpen   bool
}

type stewardGroups [3][]KanbanTask

func stewardBoardGroups(tasks []KanbanTask, now time.Time) stewardGroups {
	var groups stewardGroups
	for _, task := range tasks {
		if task.Origin != "owner" || task.ParentID != "" {
			continue
		}
		group := -1
		switch task.Status {
		case "failed", "needs_approval", "review", "rejected":
			group = 0
		case "waiting":
			group = 1
			if task.WaitKind == "human" {
				group = 0
			}
		case "todo", "in_progress":
			group = 1
		case "done":
			if !task.UpdatedAt.IsZero() && now.Sub(task.UpdatedAt) > stewardBoardDoneWindow {
				continue
			}
			group = 2
		}
		if group >= 0 {
			groups[group] = append(groups[group], task)
		}
	}
	for i := range groups {
		sort.SliceStable(groups[i], func(a, b int) bool { return groups[i][a].UpdatedAt.After(groups[i][b].UpdatedAt) })
	}
	return groups
}

func stewardBoardCount(groups stewardGroups) int {
	return len(groups[0]) + len(groups[1]) + len(groups[2])
}
func stewardBoardWide(width float32) bool { return width >= stewardChatMinWidth+stewardBoardWidth }

func (s *chatState) stewardBoardTasks() []KanbanTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]KanbanTask(nil), s.stewardBoard.tasks...)
}

func (s *chatState) stewardBoardRev() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stewardBoard.rev
}

func stopStewardBoardWatch(s *chatState) {
	s.mu.Lock()
	b := &s.stewardBoard
	b.active = false
	b.generation++
	b.loading, b.pending = false, false
	b.tasks = nil
	b.rev++
	stop := b.stop
	b.stop = nil
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

func (s *chatState) syncStewardBoard(active bool) {
	if !active || s.client == nil {
		s.mu.Lock()
		wasActive := s.stewardBoard.active
		s.mu.Unlock()
		if wasActive {
			stopStewardBoardWatch(s)
		}
		return
	}
	s.mu.Lock()
	b := &s.stewardBoard
	if b.active || s.closed {
		s.mu.Unlock()
		return
	}
	b.active = true
	b.generation++
	generation := b.generation
	s.mu.Unlock()
	stop := s.client.WatchKanban(func() { s.loadStewardBoard(generation) })
	s.mu.Lock()
	if b.active && b.generation == generation && !s.closed {
		b.stop = stop
		stop = nil
	}
	s.mu.Unlock()
	if stop != nil {
		stop()
		return
	}
	s.loadStewardBoard(generation)
}

func (s *chatState) loadStewardBoard(generation uint64) {
	s.mu.Lock()
	b := &s.stewardBoard
	if !b.active || b.generation != generation || s.closed {
		s.mu.Unlock()
		return
	}
	if b.loading {
		b.pending = true
		s.mu.Unlock()
		return
	}
	b.loading = true
	s.mu.Unlock()
	go func() {
		board, err := s.client.KanbanBoard(context.Background(), "")
		s.mu.Lock()
		if !b.active || b.generation != generation || s.closed {
			s.mu.Unlock()
			return
		}
		b.loading = false
		pending := b.pending
		b.pending = false
		if err == nil {
			b.tasks = board.Tasks
			b.rev++
		}
		s.mu.Unlock()
		s.invalidate()
		if pending {
			s.loadStewardBoard(generation)
		}
	}()
}

func stewardBoardSignal(task KanbanTask, group int) string {
	if group == 2 {
		return ""
	}
	if group == 1 {
		return strings.TrimSpace(task.LastActivity)
	}
	switch task.Status {
	case "failed":
		if reason := strings.TrimSpace(strings.SplitN(task.LastFailureReason, "\n", 2)[0]); reason != "" {
			return reason
		}
		return "失败"
	case "review":
		return "待审阅"
	case "rejected":
		return "已驳回"
	default:
		return "等你决定"
	}
}

func renderStewardBoard(c *ui.Context, s *chatState, groups stewardGroups) {
	ui.Column(c.Key("steward-board")).Width(stewardBoardWidth).FillHeight().Shrink(0).
		BorderWidth(0, 0, 0, 1).BorderColor(ThemeBorder(c)).Transition(motionSidePanel(c, false)).Children(func() {
		renderStewardBoardList(c, s, groups)
	})
}

func renderStewardBoardList(c *ui.Context, s *chatState, groups stewardGroups) {
	ui.Scroll(c.Key("steward-board-scroll")).FillWidth().Grow(1).Children(func() {
		ui.Column(c.Key("steward-board-groups")).FillWidth().Padding(Space3, Space2).Gap(Space3).Children(func() {
			for group, tasks := range groups {
				if len(tasks) == 0 {
					continue
				}
				name := [...]string{"待你处理", "进行中", "已完成"}[group]
				ui.Column(c.Key("steward-board-group-" + strconv.Itoa(group))).FillWidth().Children(func() {
					header := ui.Row(c.Key("steward-board-title-"+strconv.Itoa(group))).FillWidth().AlignItems(ui.Center).Gap(Space2).Padding(Space2, Space2)
					if group == 2 {
						header.Focusable().Label(name).Cursor(ui.CursorPointer)
						if header.Clicked() {
							s.stewardBoard.doneOpen = !s.stewardBoard.doneOpen
						}
					}
					header.Children(func() {
						ui.Text(c, name).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
						rollingNumber(c, "steward-board-count-"+strconv.Itoa(group), len(tasks))
					})
					if group == 2 && !s.stewardBoard.doneOpen {
						return
					}
					for _, task := range tasks {
						task := task
						row := ui.Column(c.Key("steward-board-task-"+task.ID)).FillWidth().MinWidth(0).Padding(Space2, Space2).Gap(Space1).Radius(RadiusSM).Focusable().Label(task.Title).Cursor(ui.CursorPointer).Transition(motionListItem(c))
						if row.Hovered() {
							row.Background(ThemeHover(c))
						}
						if row.Clicked() {
							stewardTaskOpen(s, task)
						}
						row.Children(func() {
							ui.Row(c.Key("steward-board-task-title-" + task.ID)).FillWidth().MinWidth(0).AlignItems(ui.Center).Gap(Space2).Children(func() {
								if stewardTaskUnread(s, task) {
									ui.Box(c.Key("steward-board-unread-"+task.ID)).Size(Space1, Space1).Radius(RadiusPill).Background(ThemeAccent(c)).Shrink(0)
								}
								ui.Box(c.Key("steward-board-dot-"+task.ID)).Size(Space2, Space2).Radius(RadiusPill).Background(kanbanStatusColor(c, kanbanDisplayColumn(task))).Shrink(0)
								ui.Text(c, task.Title).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).Grow(1).Shrink(1).MinWidth(0).SingleLine().Ellipsis("…")
							})
							if signal := stewardBoardSignal(task, group); signal != "" {
								ui.Text(c, signal).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).FillWidth().MinWidth(0).SingleLine().Ellipsis("…")
							}
						})
					}
				})
			}
		})
	})
}

func renderStewardBoardBadge(c *ui.Context, s *chatState) {
	groups := stewardBoardGroups(s.stewardBoardTasks(), time.Now())
	if stewardBoardCount(groups) == 0 {
		return
	}
	count := len(groups[0])
	if count == 0 {
		count = len(groups[1])
	}
	btn := ui.ButtonBase(c.Key("steward-board-badge")).Label("总管任务 "+strconv.Itoa(count)).Padding(Space1, Space2).Radius(RadiusSM).OnClick(func() { s.stewardBoard.open = !s.stewardBoard.open })
	scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
	btn.Scale(scale, scale)
	btn.Children(func() { rollingNumber(c, "steward-board-badge-count", count) })
	appPopover(c.Key("steward-board-popover"), btn, &s.stewardBoard.open, nil, func() {
		popoverCard(c, ui.Column(c.Key("steward-board-popover-card"))).Width(stewardBoardWidth).MaxHeight(480).Children(func() {
			renderStewardBoardList(c, s, groups)
		})
	})
}
