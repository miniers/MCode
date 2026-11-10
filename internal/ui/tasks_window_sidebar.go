package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"mcode/internal/kanban"
)

func tasksNeedsAttention(task KanbanTask) bool {
	for _, flag := range task.AttentionFlags {
		switch flag {
		case kanban.AttentionFlagFailed, kanban.AttentionFlagAwaitingApproval, kanban.AttentionFlagAwaitingInput, kanban.AttentionFlagNeedsReview:
			return true
		}
	}
	return task.Status == "needs_approval" || task.Status == "failed" || task.Status == "review" || task.Column == "review" || (task.Status == "waiting" && task.WaitKind == "human")
}

func tasksAttentionCount(board KanbanBoard) int {
	seen := make(map[string]bool)
	for _, task := range board.Tasks {
		if tasksNeedsAttention(task) {
			seen[task.ID] = true
		}
	}
	count := len(seen)
	for _, goal := range board.Goals {
		if goal.Status == "plan_review" {
			count++
		}
	}
	return count
}

func renderTasksWindowSidebar(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps) {
	ui.Column(c.Key("tasks-sidebar")).Label("tasks-sidebar").Width(TasksSidebarWidth).FillHeight().Background(ThemeSidebarBg(c)).Padding(Space3).Gap(Space5).Children(func() {
		ui.Box(c.Key("tasks-sidebar-titlebar")).Height(HeaderHeight).FillWidth().DragWindow()
		tasksDestinationRow(c, s, ks, "tasks", "任务")
		tasksDestinationRow(c, s, ks, "automations", "自动化")
		_, height := c.Size()
		ui.Column(c.Key("tasks-sidebar-projects")).MaxHeight(max(0, height/3)).FillWidth().Children(func() {
			ui.Scroll(c.Key("tasks-sidebar-project-scroll")).Children(func() {
				tasksSidebarRow(c, "全部项目", p.scopeID == "", func() { s.setKanbanScope("", true) })
				s.mu.Lock()
				projects := append([]Project(nil), s.projects...)
				s.mu.Unlock()
				for _, project := range projects {
					project := project
					tasksSidebarRow(c, project.Name, p.scopeID == project.ID, func() { s.setKanbanScope(project.ID, false) })
				}
			})
		})
		if p.destination == "automations" {
			renderAutomationSidebarSection(c, s)
		} else {
			tasksSidebarRow(c, "全部任务", p.goalID == "" && ks.viewFilter == "", func() {
				kanbanMu.Lock()
				ks.goalID, ks.viewFilter = "", ""
				kanbanMu.Unlock()
				s.invalidate()
			})
			tasksSidebarCountRow(c, "待你处理", tasksAttentionCount(p.board), ks.viewFilter == "attention", func() {
				kanbanMu.Lock()
				ks.goalID, ks.viewFilter = "", "attention"
				kanbanMu.Unlock()
				s.invalidate()
			})
			if len(p.board.Goals) > 0 {
				ui.Text(c, "目标").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
				for _, goal := range p.board.Goals {
					goal := goal
					title := goal.Title
					if title == "" {
						title = goal.Prompt
					}
					tasksSidebarRow(c, title, p.goalID == goal.ID, func() {
						kanbanMu.Lock()
						ks.goalID, ks.viewFilter, ks.goalDetailOpen = goal.ID, "", false
						kanbanMu.Unlock()
						s.invalidate()
					})
				}
			}
		}
	})
}

func tasksDestinationRow(c *ui.Context, s *chatState, ks *kanbanState, destination, title string) {
	row := ui.ButtonBase(c.Key("kanban-destination-"+destination)).Label("kanban-destination-"+destination).FillWidth().Height(RowHeight).Padding(0, Space3).Radius(RowCornerRadius).AlignItems(ui.Center).OnClick(func() { s.setTasksWindowView(destination) })
	if ks.destination == destination {
		row = row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row = row.Background(ThemeHover(c))
	}
	row.Children(func() { ui.Text(c, title).FontSize(TypeBody).TextColor(ThemePrimaryText(c)) })
}

func tasksSidebarRow(c *ui.Context, title string, selected bool, click func()) {
	tasksSidebarCountRow(c, title, 0, selected, click)
}

func tasksSidebarCountRow(c *ui.Context, title string, count int, selected bool, click func()) {
	row := ui.ButtonBase(c.Key("tasks-sidebar-row-"+title)).Label(title).FillWidth().Height(RowHeight).Padding(0, Space3).Radius(RowCornerRadius).AlignItems(ui.Center).OnClick(click)
	if selected {
		row = row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row = row.Background(ThemeHover(c))
	}
	row.Children(func() {
		ui.Text(c, title).SingleLine().FontSize(TypeBody).TextColor(ThemePrimaryText(c)).Grow(1).MinWidth(0)
		if count > 0 {
			ui.Text(c, fmt.Sprint(count)).FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
		}
	})
}
