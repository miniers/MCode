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
	kanbanMu.Lock()
	destination, viewFilter := ks.destination, ks.viewFilter
	kanbanMu.Unlock()
	unread, known := automationScopeUnread(s)
	if !known {
		unread = 0
	}
	ui.Column(c.Key("tasks-sidebar")).Label("tasks-sidebar").Width(TasksSidebarWidth).FillHeight().Background(ThemeSidebarBg(c)).Padding(Space3).Gap(Space5).Children(func() {
		ui.Box(c.Key("tasks-sidebar-titlebar")).Height(HeaderHeight).FillWidth().DragWindow()
		ui.Column(c.Key("tasks-sidebar-destinations")).FillWidth().Children(func() {
			tasksDestinationRow(c, s, destination, "tasks", "任务", IconColumns3(), 0)
			tasksDestinationRow(c, s, destination, "automations", "自动化", IconByName("automation"), unread)
		})
		_, height := c.Size()
		ui.Column(c.Key("tasks-sidebar-projects")).MaxHeight(max(0, height/3)).FillWidth().Children(func() {
			ui.Scroll(c.Key("tasks-sidebar-project-scroll")).Children(func() {
				tasksSidebarRow(c, "tasks-sidebar-project-all", "全部项目", 0, p.scopeID == "", nil, func() { s.setKanbanScope("", true) })
				s.mu.Lock()
				projects := append([]Project(nil), s.projects...)
				s.mu.Unlock()
				for _, project := range projects {
					project := project
					tasksSidebarRow(c, "tasks-sidebar-project-"+project.ID, project.Name, 0, p.scopeID == project.ID, nil, func() { s.setKanbanScope(project.ID, false) })
				}
			})
		})
		if p.destination == "automations" {
			renderAutomationSidebarSection(c, s)
		} else {
			ui.Column(c.Key("tasks-sidebar-views")).FillWidth().Children(func() {
				tasksSidebarRow(c, "tasks-sidebar-all-tasks", "全部任务", 0, p.goalID == "" && viewFilter == "", nil, func() {
					kanbanMu.Lock()
					ks.goalID, ks.viewFilter = "", ""
					kanbanMu.Unlock()
					s.invalidate()
				})
				tasksSidebarRow(c, "tasks-sidebar-attention", "待你处理", tasksAttentionCount(p.board), viewFilter == "attention", nil, func() {
					kanbanMu.Lock()
					ks.goalID, ks.viewFilter = "", "attention"
					kanbanMu.Unlock()
					s.invalidate()
				})
				if len(p.board.Goals) > 0 {
					ui.Column(c.Key("tasks-sidebar-goals")).FillWidth().Padding(Space5, 0, 0, 0).Children(func() {
						ui.Text(c, "目标").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
						for _, goal := range p.board.Goals {
							goal := goal
							title := goal.Title
							if title == "" {
								title = goal.Prompt
							}
							var confirm func()
							if goal.Status == "plan_review" {
								confirm = func() {
									kanbanMu.Lock()
									ks.goalID, ks.viewFilter, ks.goalDetailOpen = goal.ID, "", true
									kanbanMu.Unlock()
									s.invalidate()
								}
							}
							tasksSidebarRow(c, "tasks-sidebar-goal-"+goal.ID, title, 0, p.goalID == goal.ID, confirm, func() {
								kanbanMu.Lock()
								ks.goalID, ks.viewFilter, ks.goalDetailOpen = goal.ID, "", false
								kanbanMu.Unlock()
								s.invalidate()
							})
						}
					})
				}
			})
		}
	})
}

func tasksDestinationRow(c *ui.Context, s *chatState, current, destination, title string, icon *ui.SVG, count int) {
	selected := current == destination
	row := ui.ButtonBase(c.Key("kanban-destination-"+destination)).Label("kanban-destination-"+destination).FillWidth().Height(RowHeight).Padding(0, Space3).Radius(RowCornerRadius).Gap(Space3).AlignItems(ui.Center).OnClick(func() { s.setTasksWindowView(destination) })
	if selected {
		row = row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row = row.Background(ThemeHover(c))
	}
	row.Children(func() {
		ui.Icon(c, icon).Size(IconMD, IconMD).TextColor(tasksSidebarIconColor(c, selected))
		tasksSidebarText(c, title, selected)
		if count > 0 {
			ui.Text(c.Key("kanban-destination-"+destination+"-count"), fmt.Sprint(count)).FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c)).Transition(motionListItem(c))
		}
	})
}

func tasksSidebarRow(c *ui.Context, key, title string, count int, selected bool, confirm, click func()) {
	row := ui.ButtonBase(c.Key(key)).Label(title).FillWidth().Height(RowHeight).Padding(0, Space3).Radius(RowCornerRadius).Gap(Space3).AlignItems(ui.Center).OnClick(click)
	if selected {
		row = row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row = row.Background(ThemeHover(c))
	}
	row.Children(func() {
		tasksSidebarText(c, title, selected)
		if count > 0 {
			ui.Text(c, fmt.Sprint(count)).FontSize(uiFont(TypeBody)).TextColor(ThemeSecondaryText(c))
		}
		if confirm != nil {
			ui.ButtonBase(c.Key(key+"-confirm")).Label("确认目标计划 "+title).Size(IconMD, IconMD).OnClick(confirm).Children(func() {
				ui.Icon(c, IconOctagonAlert()).Size(IconMD, IconMD).TextColor(ThemeWarning(c))
			})
		}
	})
}

func tasksSidebarText(c *ui.Context, title string, selected bool) {
	text := ui.Text(c, title).SingleLine().FontSize(uiFont(TypeBody)).Grow(1).MinWidth(0).TextColor(tasksSidebarTextColor(c, selected))
	if selected {
		text.FontWeight(500)
	}
}

func tasksSidebarTextColor(c *ui.Context, selected bool) ui.Color {
	if selected {
		return ThemePrimaryText(c)
	}
	return ThemeSecondaryText(c)
}

func tasksSidebarIconColor(c *ui.Context, selected bool) ui.Color {
	if selected {
		return ThemeAccent(c)
	}
	return ThemeSecondaryText(c)
}
