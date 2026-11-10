package mcodeui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"mcode/internal/kanban"
)

// kanbanDragPayload 是看板卡片拖拽传递的载荷数据。
type kanbanDragPayload struct {
	Task KanbanTask
}

// kanbanColumnDef 是看板列的定义：状态键、列名与列头圆形状态图标的颜色。
type kanbanColumnDef struct {
	key   string
	label string
	color func(*ui.Context) ui.Color
}

// 列定义：五列依次为待办、进行中、待你处理、待审阅、已完成。
var kanbanColumnDefs = []kanbanColumnDef{
	{key: "todo", label: "待办", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "todo") }},
	{key: "in_progress", label: "进行中", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "in_progress") }},
	{key: "needs_you", label: "待你处理", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "needs_you") }},
	{key: "review", label: "待审阅", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "review") }},
	{key: "done", label: "已完成", color: func(c *ui.Context) ui.Color { return kanbanStatusColor(c, "done") }},
}

// kanbanListGroup 是列表视图里按状态聚合的一组任务。
type kanbanListGroup struct {
	status string
	label  string
	tasks  []KanbanTask
}

// kanbanListGroups 按固定状态顺序分组任务，未知状态追加在末尾；
// 组内按更新时间倒序，更新时间相同则按标题排序。
func kanbanListGroups(tasks []KanbanTask) []kanbanListGroup {
	order := []string{"todo", "in_progress", "needs_you", "review", "done"}
	groups := make([]kanbanListGroup, 0, len(order))
	index := make(map[string]int, len(order))
	for _, status := range order {
		index[status] = len(groups)
		groups = append(groups, kanbanListGroup{status: status, label: kanbanStatusLabel(status)})
	}

	for _, task := range tasks {
		status := kanbanDisplayColumn(task)
		if status == "" {
			status = "unknown"
		}
		idx, ok := index[status]
		if !ok {
			idx = len(groups)
			index[status] = idx
			groups = append(groups, kanbanListGroup{status: status, label: kanbanStatusLabel(status)})
		}
		groups[idx].tasks = append(groups[idx].tasks, task)
	}

	out := make([]kanbanListGroup, 0, len(groups))
	for _, group := range groups {
		if len(group.tasks) == 0 {
			continue
		}
		sort.SliceStable(group.tasks, func(i, j int) bool {
			if !group.tasks[i].UpdatedAt.Equal(group.tasks[j].UpdatedAt) {
				return group.tasks[i].UpdatedAt.After(group.tasks[j].UpdatedAt)
			}
			return group.tasks[i].Title < group.tasks[j].Title
		})
		out = append(out, group)
	}
	return out
}

// renderKanbanListView 渲染按状态分组的任务列表视图。
func renderKanbanListView(c *ui.Context, s *chatState, tasks []KanbanTask) {
	ui.Scroll(c.Key("kanban-list-scroll")).
		TrackScroll(&getKanbanState(s).listScroll).
		Grow(1).
		FillWidth().
		Children(func() {
			ui.Column(c.Key("kanban-list-root")).
				FillWidth().
				Gap(Space4).
				Children(func() {
					for _, group := range kanbanListGroups(tasks) {
						group := group
						ui.Column(c.Key("kanban-list-group-" + group.status)).
							Label("kanban-list-group-" + group.status).
							FillWidth().
							Gap(Space2).
							Children(func() {
								ui.Text(c, fmt.Sprintf("%s · %d", group.label, len(group.tasks))).
									FontSize(TypeFootnote).
									FontWeight(600).
									TextColor(ThemeSecondaryText(c))

								for _, task := range group.tasks {
									task := task
									renderKanbanListRow(c, s, task)
								}
							})
					}
				})
		})
}

// renderKanbanListRow 渲染列表视图中的单条任务行，点击打开与看板卡片相同的详情。
func renderKanbanListRow(c *ui.Context, s *chatState, task KanbanTask) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	mixed := ks.scopeID == ""
	kanbanMu.Unlock()
	signal, tone := kanbanTaskSignal(task)

	var row ui.Element
	row = ui.Row(c.Key("kanban-list-row-"+task.ID)).
		Label("kanban-list-row-"+task.ID).
		Focusable().
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		AlignItems(ui.Center).
		Gap(Space3).
		OnClick(func() {
			if row.ClickModifiers()&ui.Shift != 0 {
				kanbanKeyboardSelect(s, task.ID, "range")
				return
			}
			s.openTaskDetail(task)
		})
	selected := kanbanKeyboardSelected(s, task.ID)
	if selected {
		row.BorderWidth(kanbanDropBorder).BorderColor(ThemeAccent(c))
	}
	kanbanKeyboardItem(c, s, row, task)
	row.Children(func() {
		if selected {
			ui.Icon(c, IconCheck()).Size(kanbanBadgeIconSize, kanbanBadgeIconSize).TextColor(ThemeAccent(c))
		}
		renderKanbanPriority(c, task)
		ui.Text(c, task.Title).
			FontSize(TypeBody).
			FontWeight(500).
			TextColor(ThemePrimaryText(c)).
			Grow(1).
			SingleLine()

		if signal != "" {
			ui.Text(c, signal).FontSize(TypeCaption).TextColor(kanbanSignalColor(c, tone)).SingleLine()
		}
		if mixed {
			if name := projectNameFor(s, task.ProjectID); name != "" {
				ui.Text(c, name).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c)).SingleLine()
			}
		}
	})
}

// kanbanTaskSignal 只返回一条与所在列不重复的信号。
func kanbanTaskSignal(task KanbanTask) (string, string) {
	if task.Status == "cancelled" {
		return "已取消", "neutral"
	}
	if task.Status == "failed" {
		return "失败", "danger"
	}
	if task.Status == "rejected" {
		return "已驳回", "danger"
	}
	if task.Blocked {
		if len(task.DependsOn) > 0 {
			return fmt.Sprintf("依赖 %d", len(task.DependsOn)), "warning"
		}
		return "受阻", "warning"
	}
	for _, flag := range task.AttentionFlags {
		switch flag {
		case kanban.AttentionFlagFailed:
			return "失败", "danger"
		case kanban.AttentionFlagStuck:
			return "执行停滞", "warning"
		case kanban.AttentionFlagAwaitingApproval:
			return "需要审批", "warning"
		case kanban.AttentionFlagAwaitingInput:
			return "需要回复", "warning"
		case kanban.AttentionFlagNeedsReview:
			if kanbanDisplayColumn(task) != "review" {
				return "需要审阅", "warning"
			}
		}
	}
	if task.Status == "needs_approval" {
		return "需要审批", "warning"
	}
	if task.Status == "waiting" {
		text := strings.TrimSpace(task.ResultSummary)
		if text == "" {
			switch task.WaitKind {
			case "human":
				text = "等待你的决定"
			case "machine":
				text = "等待条件满足"
			case "children":
				text = "等待子任务"
			default:
				text = "等待中"
			}
		}
		if at := stewardWakeLocalTime(task.WakeAt); at != "" {
			text += " · " + at
		}
		return text, "neutral"
	}
	if task.Status == "done" && strings.TrimSpace(task.ResultSummary) != "" {
		return strings.TrimSpace(task.ResultSummary), "neutral"
	}
	return "", "neutral"
}

func kanbanSignalColor(c *ui.Context, tone string) ui.Color {
	switch tone {
	case "danger":
		return ThemeDanger(c)
	case "warning":
		return ThemeWarning(c)
	default:
		return ThemeSecondaryText(c)
	}
}

func renderKanbanPriority(c *ui.Context, task KanbanTask) {
	if label, icon, color, ok := kanbanPriorityStyle(c, task.Priority); ok {
		switch strings.ToLower(strings.TrimSpace(task.Priority)) {
		case "urgent", "high", "medium", "low":
			ui.Icon(c, icon).Label("优先级："+label).Tooltip("优先级："+label).
				Size(cardGlyphSize, cardGlyphSize).TextColor(color)
		}
	}
}

func kanbanQuickAddAction(s *chatState) {
	ks := getKanbanState(s)
	kanbanMu.Lock()
	projectID := ks.scopeID
	kanbanMu.Unlock()
	if projectID == "" {
		openKanbanNewTaskSheet(s)
		kanbanMu.Lock()
		ks.newTaskProjectID = ""
		ks.newTaskVersion++
		kanbanMu.Unlock()
		return
	}
	kanbanMu.Lock()
	ks.quickAddOpen = true
	kanbanMu.Unlock()
	s.invalidate()
}

// renderTodoQuickAdd 渲染待办列底部的「+ 添加任务」：点击展开单行输入，回车提交、Esc 取消。
func renderTodoQuickAdd(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	open := ks.quickAddOpen
	kanbanMu.Unlock()

	if !open {
		Button(c.Key("kanban-quick-add-toggle"), ButtonOpts{
			Icon:    IconPlus(),
			Text:    "添加任务",
			Label:   "kanban-quick-add-toggle",
			Variant: ButtonSecondary,
			OnClick: func() { kanbanQuickAddAction(s) },
		})
		return
	}

	ui.Row(c.Key("kanban-quick-add-row")).
		Label("kanban-quick-add-row").
		FillWidth().
		Padding(Space2, Space3).
		Radius(RadiusMD).
		Background(ThemeSurface(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeAccent(c).Alpha(0.60)).
		AlignItems(ui.Center).
		Gap(Space2).
		Children(func() {
			ui.Icon(c, IconPlus()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeAccent(c))

			in := ui.TextInput(c.Key("kanban-quick-add-input"), &ks.quickAddText).
				Placeholder("添加任务...").
				Label("kanban-quick-add-input").
				FontSize(TypeFootnote).
				BorderWidth(0).
				Background(ui.Transparent).
				Padding(0).
				Grow(1).
				AutoFocus()

			in.OnTextKey(func(k ui.TextKey) bool {
				switch k.Key {
				case ui.KeyEnter:
					s.submitQuickAdd()
					return true
				}
				return false
			})

			// Esc 登记为输入框自己的快捷键：根窗口的 Esc（关闭工作区）会先于 OnTextKey 收到按键。
			if in.Shortcut(0, ui.KeyEscape) {
				kanbanMu.Lock()
				ks.quickAddOpen = false
				ks.quickAddText = ""
				kanbanMu.Unlock()
				s.invalidate()
			}
		})
}

// kanbanTaskCardState 汇总看板列与任务卡片共用的渲染状态。
type kanbanTaskCardState struct {
	moveMenuTaskID, reviewTaskID, reviewFeedback string
	goals                                        []KanbanGoal
	rescueMenuTaskID, deleteTaskID               string
}

// projectNameFor 返回项目显示名，找不到时返回空字符串。
func projectNameFor(s *chatState, projectID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, project := range s.projects {
		if project.ID == projectID {
			return project.Name
		}
	}
	return ""
}

// kanbanToneBadge 渲染使用指定颜色的小胶囊。
func kanbanToneBadge(c *ui.Context, key, label string, color ui.Color) {
	ui.Row(c.Key(key)).
		Label(key).
		Padding(Space0, Space2).
		Radius(RadiusMD).
		Background(color.Alpha(0.15)).
		Children(func() {
			ui.Text(c, label).FontSize(TypeCaption).FontWeight(600).TextColor(color)
		})
}

// renderTaskCard 渲染标题、一条信号和按需显示的操作。
func renderTaskCard(c *ui.Context, s *chatState, ks *kanbanState, task KanbanTask, st kanbanTaskCardState) {
	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)
	actions := kanbanActions(task, kanbanTaskBusy(ks, task.ID))

	var card ui.Element
	card = ui.Column(c.Key("kanban-task-card-"+task.ID)).
		Label("kanban-task-card-"+task.ID).
		Focusable().
		FillWidth().
		Padding(Space4, Space4).
		Radius(RadiusMD).
		Background(ThemeSurfaceCard(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		Transition(motionFeedback(c)).
		Gap(Space2).
		OnClick(func() {
			if card.ClickModifiers()&ui.Shift != 0 {
				kanbanKeyboardSelect(s, task.ID, "range")
				return
			}
			s.openTaskDetail(task)
		})
	kanbanMu.Lock()
	returnFocus := ks.detailFocusReturnID == task.ID
	opened := ks.detailTaskID == task.ID
	if returnFocus {
		ks.detailFocusReturnID = ""
	}
	kanbanMu.Unlock()
	selected := kanbanKeyboardSelected(s, task.ID)
	if returnFocus {
		card.Focus()
	}
	kanbanKeyboardItem(c, s, card, task)
	// 悬停时提亮底色与描边，让指针下的卡片更清晰
	if card.Hovered() {
		card.Background(ThemeHover(c)).BorderColor(ThemeAccent(c))
	}
	if opened || selected {
		card.Background(ThemeSelected(c))
	}
	if selected {
		card.BorderColor(ThemeAccent(c))
	}

	// 右键菜单：与卡片内的按钮复用同一批调用
	card.ContextMenu(func(m *ui.Menu) {
		if m.Item("查看详情").Chosen() {
			s.openTaskDetail(task)
		}
		if actions.primary == "开始" {
			m.Separator()
			startLabel := "开始"
			if kanbanDisplayColumn(task) == "in_progress" {
				startLabel = "继续"
			}
			if m.Item(startLabel).Chosen() {
				if kanbanTaskBusy(ks, task.ID) {
					return
				}
				if task.Blocked {
					s.setToast("暂不可启动：" + kanbanTaskBlockReason(task))
					return
				}
				s.startTask(task.ID, task.Revision)
			}
		}
		if len(actions.menu) > 0 {
			m.Separator()
			for _, opt := range actions.menu {
				optVal := opt
				if m.Item("移动到 " + optVal.label).Chosen() {
					s.performKanbanTarget(task, optVal.status)
				}
			}
		}
	})

	// 若任务允许移动，挂载原生拖拽源
	if len(actions.targets) > 0 {
		card.Drag(kanbanDragPayload{Task: task})
		if card.Dragging() {
			card.Opacity(KanbanDragSourceOpacity)
		}
	}

	kanbanMu.Lock()
	mixed := ks.scopeID == ""
	kanbanMu.Unlock()
	signal, tone := kanbanTaskSignal(task)
	project := ""
	if mixed {
		project = projectNameFor(s, task.ProjectID)
	}
	menuOpen := st.moveMenuTaskID == task.ID || st.rescueMenuTaskID == task.ID
	var more ui.Element
	card.Children(func() {
		if opened {
			ui.Box(c.Key("kanban-task-open-" + task.ID)).Label("kanban-task-open-" + task.ID).
				Absolute().Left(0).Right(0).Top(0).Bottom(0)
		}
		if card.Focused() {
			ui.Box(c.Key("kanban-task-focus-"+task.ID)).Label("kanban-task-focus-"+task.ID).
				Absolute().Left(0).Right(0).Top(0).Bottom(0).Radius(RadiusMD).
				Border(KanbanFocusRingWidth, ThemeAccent(c))
		}
		ui.Row(c.Key("kanban-task-title-row-" + task.ID)).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
			if selected {
				icon, color := IconSquare(), secondaryText
				if selected {
					icon, color = IconCircleCheck(), ThemeAccent(c)
				}
				ui.Icon(c, icon).Label("kanban-task-selection-"+task.ID).Size(IconSM, IconSM).TextColor(color)
			}
			renderKanbanPriority(c, task)
			ui.Text(c, task.Title).Label(task.Title).Tooltip(task.Title).FontSize(TypeBody).FontWeight(500).
				MaxLines(2).TextColor(primaryText).Grow(1).MinWidth(0)
			ui.Row(c.Key("kanban-task-actions-" + task.ID)).Width(KanbanCardActionSlotWidth).Height(ControlXS).Shrink(0).AlignItems(ui.Center).Children(func() {
				if card.Hovered() || card.FocusWithin() || menuOpen {
					if actions.primary == "开始" {
						label := "开始"
						if kanbanDisplayColumn(task) == "in_progress" {
							label = "继续"
						}
						btn := Button(c.Key("kanban-task-"+task.ID+"-start"), ButtonOpts{
							Text: label, Label: "kanban-task-" + task.ID + "-start", Size: ControlXS, Variant: ButtonPrimary,
							OnClick: func() {
								if kanbanTaskBusy(ks, task.ID) {
									return
								}
								if task.Blocked {
									s.setToast("暂不可启动：" + kanbanTaskBlockReason(task))
									return
								}
								s.startTask(task.ID, task.Revision)
							},
						})
						if task.Blocked {
							btn.Tooltip("暂不可启动：" + kanbanTaskBlockReason(task))
						}
					} else if actions.primary != "" {
						label := actions.primary
						Button(c.Key("kanban-task-"+task.ID+"-primary"), ButtonOpts{
							Text: label, Label: label + " " + task.Title, Size: ControlXS, Variant: ButtonPrimary,
							OnClick: func() {
								if !kanbanTaskBusy(ks, task.ID) {
									s.openTaskDetail(task)
								}
							},
						})
					}
					more = Button(c.Key("kanban-task-"+task.ID+"-more"), ButtonOpts{
						Icon: IconMoreHorizontal(), Label: "kanban-task-" + task.ID + "-more", Size: ControlXS, Variant: ButtonSecondary,
						OnClick: func() {
							kanbanMu.Lock()
							if ks.rescueMenuTaskID == task.ID {
								ks.rescueMenuTaskID = ""
							} else {
								ks.rescueMenuTaskID = task.ID
								ks.moveMenuTaskID = ""
							}
							ks.deleteTaskID = ""
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				}
			})
		})
		if signal != "" || project != "" {
			if signal != "" && project != "" {
				signal += " · "
			}
			ui.Row(c.Key("kanban-task-signal-" + task.ID)).Label("kanban-task-signal-" + task.ID).Children(func() {
				ui.Text(c, signal+project).FontSize(TypeCaption).TextColor(kanbanSignalColor(c, tone)).SingleLine()
			})
		}
		if menuOpen {
			appPopover(c.Key("kanban-task-more-popover-"+task.ID), more, &menuOpen, func() {
				kanbanMu.Lock()
				ks.moveMenuTaskID = ""
				ks.rescueMenuTaskID = ""
				kanbanMu.Unlock()
				s.invalidate()
			}, func() {
				popoverCard(c, ui.Column(c.Key("kanban-task-more-menu-"+task.ID))).Padding(Space2).Children(func() {
					// 移动选项菜单展开
					if st.moveMenuTaskID == task.ID {
						ui.Row(c.Key("kanban-task-move-options-"+task.ID)).
							FillWidth().
							Wrap().
							Padding(Space1, Space2).
							Radius(RadiusSM).
							Background(ThemeHover(c)).
							AlignItems(ui.Center).
							Gap(Space1).
							Children(func() {
								ui.Text(c, "移动到:").
									FontSize(TypeCaption).
									TextColor(secondaryText)

								for _, opt := range actions.menu {
									optVal := opt
									Button(c.Key(optVal.key), ButtonOpts{
										Text:    optVal.label,
										Label:   optVal.key,
										Size:    ControlXS,
										Variant: ButtonSecondary,
										OnClick: func() {
											kanbanMu.Lock()
											ks.moveMenuTaskID = ""
											kanbanMu.Unlock()
											s.performKanbanTarget(task, optVal.status)
										},
									})
								}
							})
					}

					// 救援菜单展开：打开会话、提醒 Agent、中断并重置、删除
					if st.rescueMenuTaskID == task.ID && !kanbanTaskBusy(ks, task.ID) {
						rescuable := task.Status == "in_progress" || len(task.AttentionFlags) > 0
						ui.Row(c.Key("kanban-task-rescue-options-"+task.ID)).
							Label("kanban-task-rescue-options-"+task.ID).
							FillWidth().
							Wrap().
							Padding(Space1, Space2).
							Radius(RadiusSM).
							Background(ThemeHover(c)).
							AlignItems(ui.Center).
							Gap(Space1).
							Children(func() {
								if rescuable {
									ui.Text(c, "救援:").
										FontSize(TypeCaption).
										TextColor(secondaryText)
								}

								if (task.Status == "done" || task.Column == "done") && actions.targets["todo"] != "" {
									Button(c.Key("kanban-task-"+task.ID+"-reopen"), ButtonOpts{
										Text: "重新打开", Label: "kanban-task-" + task.ID + "-reopen", Size: ControlXS, Variant: ButtonSecondary,
										OnClick: func() { s.performKanbanTarget(task, "todo") },
									})
								}
								if len(actions.menu) > 0 && task.Status != "done" && !(task.Status == "cancelled" && task.Column == "done") {
									Button(c.Key("kanban-task-"+task.ID+"-move"), ButtonOpts{
										Text: "移动", Label: "kanban-task-" + task.ID + "-move", Size: ControlXS, Variant: ButtonSecondary,
										OnClick: func() {
											kanbanMu.Lock()
											ks.rescueMenuTaskID = ""
											ks.moveMenuTaskID = task.ID
											kanbanMu.Unlock()
											s.invalidate()
										},
									})
								}

								if task.ThreadID != "" {
									Button(c.Key("kanban-task-rescue-open-"+task.ID), ButtonOpts{
										Text:    "打开会话",
										Label:   "kanban-task-rescue-open-" + task.ID,
										Size:    ControlXS,
										Variant: ButtonSecondary,
										OnClick: func() {
											kanbanMu.Lock()
											ks.rescueMenuTaskID = ""
											kanbanMu.Unlock()
											s.closeWorkspace()
											s.loadThreads(task.ProjectID, false)
											s.revealThread(task.ThreadID)
										},
									})
								}

								if rescuable {
									Button(c.Key("kanban-task-rescue-nudge-"+task.ID), ButtonOpts{
										Text:    "提醒 Agent",
										Label:   "kanban-task-rescue-nudge-" + task.ID,
										Size:    ControlXS,
										Variant: ButtonSecondary,
										OnClick: func() {
											s.nudgeTaskAgent(task)
										},
									})

									Button(c.Key("kanban-task-rescue-interrupt-"+task.ID), ButtonOpts{
										Text:    "中断并重置为待办",
										Label:   "kanban-task-rescue-interrupt-" + task.ID,
										Size:    ControlXS,
										Variant: ButtonSecondary,
										OnClick: func() {
											kanbanMu.Lock()
											ks.rescueMenuTaskID = ""
											kanbanMu.Unlock()
											s.interruptTaskToTodo(task)
										},
									})
								}

								Button(c.Key("kanban-task-rescue-delete-"+task.ID), ButtonOpts{
									Text:    "删除",
									Label:   "kanban-task-rescue-delete-" + task.ID,
									Size:    ControlXS,
									Variant: ButtonDanger,
									OnClick: func() {
										kanbanMu.Lock()
										ks.rescueMenuTaskID = ""
										ks.moveMenuTaskID = ""
										ks.deleteTaskID = task.ID
										kanbanMu.Unlock()
										s.invalidate()
									},
								})
							})
					}
				})
			})
		}

		// 删除二次确认：确认后调用客户端删除任务，成功时刷新看板。
		if st.deleteTaskID == task.ID {
			open := true
			ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
				backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
				w, h := c.Size()
				panel.MaxWidth(w - Space8).MaxHeight(h - Space8)
				ui.Row(c.Key("kanban-task-delete-confirm-"+task.ID)).
					Wrap().
					AlignItems(ui.Center).
					Padding(Space1, Space2).
					Radius(RadiusSM).
					Background(ThemeSurfaceCard(c)).
					Gap(Space2).
					Children(func() {
						ui.Text(c, "确认删除该任务？此操作不可撤销").
							FontSize(TypeCaption).
							TextColor(ThemeDanger(c)).
							Grow(1)
						Button(c.Key("kanban-task-delete-cancel-"+task.ID), ButtonOpts{
							Text:    "取消",
							Label:   "kanban-task-delete-cancel-" + task.ID,
							Size:    ControlXS,
							Variant: ButtonSecondary,
							OnClick: func() {
								kanbanMu.Lock()
								ks.deleteTaskID = ""
								kanbanMu.Unlock()
								s.invalidate()
							},
						})
						Button(c.Key("kanban-task-delete-confirm-yes-"+task.ID), ButtonOpts{
							Text:    "确认删除",
							Label:   "kanban-task-delete-confirm-yes-" + task.ID,
							Size:    ControlXS,
							Variant: ButtonDanger,
							OnClick: func() {
								s.deleteKanbanTask(task)
							},
						})
					})
			})
			if !open {
				kanbanMu.Lock()
				ks.deleteTaskID = ""
				kanbanMu.Unlock()
				s.invalidate()
			}
		}

		// 验收驳回反馈输入框
		if st.reviewTaskID == task.ID {
			ui.Row(c.Key("kanban-task-reject-form-" + task.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space2).
				Children(func() {
					ui.TextInput(c.Key("kanban-task-reject-feedback-"+task.ID), &st.reviewFeedback).
						Placeholder("驳回反馈原因...").
						Label("kanban-task-reject-feedback").
						Grow(1)

					Button(c.Key("kanban-task-reject-submit-"+task.ID), ButtonOpts{
						Text:    "确认驳回",
						Label:   "确认驳回",
						Variant: ButtonDanger,
						OnClick: func() {
							s.reviewTask(task.ID, "reject", st.reviewFeedback, task.Revision)
						},
					})
				})
		}
	})
}

// kanbanColumnIcon 按列 id 映射列头状态图标；图标颜色仍由 kanbanColumnDefs 提供。
func kanbanColumnIcon(colKey string) *ui.SVG {
	switch colKey {
	case "in_progress":
		return IconLoader()
	case "needs_you":
		return IconHandRaised()
	case "review":
		return IconEye()
	case "done":
		return IconCircleCheck()
	default:
		return IconListTodo()
	}
}

// kanbanColumnCountBadge 渲染列头计数徽标；搜索时显示「过滤后/总数」。
func kanbanColumnCountBadge(c *ui.Context, key string, filtered, total int, searching bool) {
	text := fmt.Sprintf("%d", filtered)
	if searching {
		text = fmt.Sprintf("%d/%d", filtered, total)
	}
	ui.Row(c.Key(key)).
		Label(key + "-" + text).
		Center().
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeCaption).
				FontWeight(700).
				TextColor(ThemeSecondaryText(c))
		})
}

// kanbanBoardColumnWidth 计算看板单列宽度：至少 270，窗口够宽时五列均分内容区。
// 窄窗口下回落到 270，由外层横向滚动容纳，不再压窄列。
func kanbanBoardColumnWidth(c *ui.Context, s *chatState) float32 {
	winWidth, _ := c.Size()
	avail := winWidth - 2*16 // 内容区左右各 16pt padding
	if s == nil || s.tasksWindowView == "" {
		avail -= float32(2 * kanbanModalInset)
	}
	return kanbanColumnWidthFor(avail-1, len(kanbanColumnDefs), 0)
}

func kanbanColumnWidthFor(boardWidth float32, expandedCount, collapsedCount int) float32 {
	if expandedCount <= 0 {
		return KanbanColumnMinWidth
	}
	columns := expandedCount + max(collapsedCount, 0)
	available := boardWidth - float32(max(collapsedCount, 0))*KanbanDoneRailWidth - float32(columns-1)*kanbanColumnSpacing
	return max(KanbanColumnMinWidth, available/float32(expandedCount))
}

// kanbanStatusBadge 返回任务状态徽标的文案，未知状态返回空串。
func kanbanStatusBadge(task KanbanTask) string {
	switch task.Status {
	case "waiting":
		return "等待中"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	}
	switch kanbanDisplayColumn(task) {
	case "todo":
		return "待办"
	case "in_progress":
		return "进行中"
	case "needs_you":
		return "待你处理"
	case "review":
		return "待审阅"
	case "done":
		return "已通过"
	case "rejected":
		return "需修改"
	default:
		return ""
	}
}

// renderKanbanColumn 渲染看板单列容器：列头（13pt 状态图标、列名、数量、待办列的「+」）、
// 可纵向滚动的卡片区与待办列的快速添加。tasks 是搜索过滤后的任务，
// allTasks 是未过滤的看板任务，搜索时用于显示「过滤后/总数」。
func renderKanbanColumn(
	c *ui.Context,
	s *chatState,
	ks *kanbanState,
	colDef kanbanColumnDef,
	tasks []KanbanTask,
	allTasks []KanbanTask,
	searching bool,
	colWidth float32,
	st kanbanTaskCardState,
) {
	colKey := colDef.key
	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)

	// 过滤归属于该列的任务（Column 优先，回退 Status）
	var colTasks []KanbanTask
	for _, t := range tasks {
		if kanbanDisplayColumn(t) == colKey {
			colTasks = append(colTasks, t)
		}
	}
	// 搜索时列头计数要拿未过滤的任务数当分母
	totalCount := len(colTasks)
	if searching {
		totalCount = 0
		for _, t := range allTasks {
			if kanbanDisplayColumn(t) == colKey {
				totalCount++
			}
		}
	}

	kanbanMu.Lock()
	doneExpanded := ks.doneExpanded
	kanbanMu.Unlock()
	if colKey == "done" && !doneExpanded {
		rail := ui.Column(c.Key("kanban-col-done-rail")).
			Label("kanban-column-" + colKey).
			Transition(motionCollapse(c)).
			Focusable().Width(KanbanDoneRailWidth).MinWidth(KanbanDoneRailWidth).
			FillHeight().MinHeight(KanbanEmptyDropMinHeight).
			Padding(Space2).Gap(Space2).
			OnClick(func() {
				kanbanMu.Lock()
				ks.doneExpanded = true
				kanbanMu.Unlock()
				s.invalidate()
			})
		if rail.Focused() && rail.Shortcut(0, ui.KeyEnter) {
			kanbanMu.Lock()
			ks.doneExpanded = true
			kanbanMu.Unlock()
			s.invalidate()
		}
		if payload, ok := ui.Drop[kanbanDragPayload](rail); ok {
			s.performKanbanTarget(payload.Task, colKey)
		}
		if payload, over := ui.DragOver[kanbanDragPayload](rail); over && kanbanActions(payload.Task, kanbanTaskBusy(ks, payload.Task.ID)).targets[colKey] != "" {
			rail.BorderWidth(KanbanFocusRingWidth).BorderColor(ThemeAccent(c)).Background(ThemeSelected(c))
		} else if rail.Focused() {
			rail.BorderWidth(KanbanFocusRingWidth).BorderColor(ThemeAccent(c))
		}
		rail.Children(func() {
			ui.Icon(c, IconCircleCheck()).Size(IconSM, IconSM).TextColor(secondaryText)
			ui.Text(c, colDef.label).FontSize(TypeCaption).TextColor(secondaryText)
			kanbanColumnCountBadge(c, "kanban-col-count-"+colKey, len(colTasks), totalCount, searching)
		})
		return
	}

	columnKey := "kanban-col-" + colKey
	if colKey == "done" {
		columnKey += "-expanded"
	}
	colBox := ui.Column(c.Key(columnKey)).
		Label("kanban-column-" + colKey).
		Width(colWidth).
		MinWidth(kanbanColumnMinWidth).
		FillHeight().
		MinHeight(KanbanEmptyDropMinHeight).
		Padding(Space3).
		Gap(Space3)
	if colKey == "done" {
		colBox.Transition(motionCollapse(c))
	}

	// 原生拖拽投放响应 (Drop)
	if payload, ok := ui.Drop[kanbanDragPayload](colBox); ok {
		s.performKanbanTarget(payload.Task, colKey)
	}

	// 拖拽悬停高亮：目标列边框变 accent 2pt；needs_you 列不接受投放，不高亮
	if payload, over := ui.DragOver[kanbanDragPayload](colBox); over && kanbanActions(payload.Task, kanbanTaskBusy(ks, payload.Task.ID)).targets[colKey] != "" {
		colBox.BorderWidth(kanbanDropBorder).BorderColor(ThemeAccent(c))
	}

	colBox.Children(func() {
		// 列头：状态图标、列名、数量徽标，待办列另加「+」
		ui.Row(c.Key("kanban-col-header-" + colKey)).
			FillWidth().
			AlignItems(ui.Center).
			Gap(Space3).
			Children(func() {
				ui.Icon(c, kanbanColumnIcon(colKey)).
					Size(IconSM, IconSM).
					TextColor(colDef.color(c)).
					Shrink(0)
				ui.Text(c, colDef.label).
					FontSize(TypeFootnote).
					FontWeight(600).
					TextColor(primaryText).
					Grow(1)

				kanbanColumnCountBadge(c, "kanban-col-count-"+colKey, len(colTasks), totalCount, searching)
				if colKey == "done" {
					IconButton(c.Key("kanban-col-collapse-done"), IconButtonProps{
						Icon: IconChevronRight(), Label: "收起已完成", Variant: ButtonGhost, Size: ControlMD,
						OnClick: func() {
							kanbanMu.Lock()
							ks.doneExpanded = false
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				}

				if colKey == "todo" {
					IconButton(c.Key("kanban-col-add-"+colKey), IconButtonProps{
						Icon:  IconPlus(),
						Label: "添加任务",
						OnClick: func() {
							openKanbanNewTaskSheet(s)
						},
						Variant: ButtonGhost,
						Size:    ControlMD,
					})
				}
			})

		// 列内卡片滚动区域
		if ks.columnScroll == nil {
			ks.columnScroll = make(map[string]*ui.ScrollState)
		}
		if ks.columnScroll[colKey] == nil {
			ks.columnScroll[colKey] = &ui.ScrollState{}
		}
		ui.Scroll(c.Key("kanban-col-scroll-" + colKey)).
			TrackScroll(ks.columnScroll[colKey]).
			Grow(1).
			FillWidth().
			MinWidth(0).
			Children(func() {
				ui.Column(c.Key("kanban-col-cards-" + colKey)).
					FillWidth().
					Gap(Space3).
					Children(func() {
						for _, task := range colTasks {
							taskVal := task
							renderTaskCard(c, s, ks, taskVal, st)
						}
						// 待办列底部的快速添加
						if colKey == "todo" {
							renderTodoQuickAdd(c, s, ks)
						}
					})
			})
	})
}
