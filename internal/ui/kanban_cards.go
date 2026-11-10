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
	TaskID   string
	Status   string
	Revision int
	CanStart bool
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
	updated := "—"
	if !task.UpdatedAt.IsZero() {
		updated = task.UpdatedAt.Local().Format("2006-01-02 15:04")
	}

	ui.Row(c.Key("kanban-list-row-"+task.ID)).
		Label("kanban-list-row-"+task.ID).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeHover(c).Alpha(0.20)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space3).
		OnClick(func() {
			s.openTaskDetail(task)
		}).
		Children(func() {
			ui.Text(c, task.Title).
				FontSize(TypeBody).
				FontWeight(500).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()

			ui.Row(c.Key("kanban-list-status-"+task.ID)).
				Padding(Space0, Space1).
				Radius(RadiusXS).
				Background(ThemeHover(c)).
				Children(func() {
					ui.Text(c, kanbanStatusLabel(task.Status)).
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c))
				})

			ui.Text(c, "更新于 "+updated).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c))
		})
}

// renderTodoQuickAdd 渲染待办列底部的「+ 添加任务」：点击展开单行输入，回车提交、Esc 取消。
func renderTodoQuickAdd(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	open := ks.quickAddOpen
	projectID := ks.projectID
	kanbanMu.Unlock()

	if projectID == "" {
		return
	}

	if !open {
		Button(c.Key("kanban-quick-add-toggle"), ButtonOpts{
			Icon:    IconPlus(),
			Text:    "添加任务",
			Label:   "kanban-quick-add-toggle",
			Variant: ButtonSecondary,
			OnClick: func() {
				kanbanMu.Lock()
				ks.quickAddOpen = true
				ks.quickAddText = ""
				kanbanMu.Unlock()
				s.invalidate()
			},
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
					s.submitQuickAdd(projectID)
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
	selecting                                    bool
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

// renderTaskCard 渲染单张任务卡片：项目胶囊与右上角状态 / 审查徽标、标题、备注预览、
// 打开审查会话按钮，以及底部的 agent 信息与「开始」按钮。
func renderTaskCard(c *ui.Context, s *chatState, ks *kanbanState, task KanbanTask, st kanbanTaskCardState) {
	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)

	card := ui.Column(c.Key("kanban-task-card-"+task.ID)).
		Label("kanban-task-card-"+task.ID).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeSurface(c).Alpha(0.55)).
		BorderWidth(Hairline).
		BorderColor(ThemeOverlay(c, 0.08)).
		Gap(Space3).
		OnClick(func() {
			// 多选模式下点击待办卡片只切换选中态，不打开详情。
			if st.selecting && kanbanDisplayColumn(task) == "todo" {
				kanbanMu.Lock()
				if ks.selectedTaskIDs == nil {
					ks.selectedTaskIDs = make(map[string]bool)
				}
				ks.selectedTaskIDs[task.ID] = !ks.selectedTaskIDs[task.ID]
				kanbanMu.Unlock()
				s.invalidate()
				return
			}
			s.openTaskDetail(task)
		})
	// 悬停时提亮底色与描边，让指针下的卡片更清晰
	if card.Hovered() {
		card.Background(ThemeSurface(c).Alpha(0.60)).BorderColor(ThemeAccent(c).Alpha(0.35))
	}
	if st.selecting && kanbanDisplayColumn(task) == "todo" {
		kanbanMu.Lock()
		selected := ks.selectedTaskIDs[task.ID]
		kanbanMu.Unlock()
		if selected {
			card.BorderWidth(kanbanCardHoverBorder).
				BorderColor(ThemeAccent(c)).
				Background(ThemeSurface(c).Alpha(0.75))
		}
	}

	// 右键菜单：与卡片内的按钮复用同一批调用
	card.ContextMenu(func(m *ui.Menu) {
		if m.Item("查看详情").Chosen() {
			s.openTaskDetail(task)
		}
		if task.CanStart {
			m.Separator()
			startLabel := "开始"
			if kanbanDisplayColumn(task) == "in_progress" {
				startLabel = "继续"
			}
			if m.Item(startLabel).Chosen() {
				if task.Blocked || len(task.AttentionFlags) > 0 {
					s.setToast("暂不可启动：" + kanbanTaskBlockReason(task))
					return
				}
				s.startTask(task.ID, task.Revision)
			}
		}
		if task.CanMove {
			m.Separator()
			for _, opt := range moveTargetsFor(task.Status) {
				optVal := opt
				if m.Item("移动到 " + optVal.label).Chosen() {
					s.moveTask(task.ID, optVal.status, task.Revision)
				}
			}
		}
	})

	// 若任务允许移动，挂载原生拖拽源
	if task.CanMove {
		card.Drag(kanbanDragPayload{
			TaskID:   task.ID,
			Status:   task.Status,
			Revision: task.Revision,
			CanStart: task.CanStart,
		})
	}

	card.Children(func() {
		// 头部：选中框、项目胶囊，右侧优先显示状态 / 审查徽标
		ui.Row(c.Key("kanban-task-title-row-" + task.ID)).
			FillWidth().
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				if st.selecting && kanbanDisplayColumn(task) == "todo" {
					kanbanMu.Lock()
					selected := ks.selectedTaskIDs[task.ID]
					kanbanMu.Unlock()
					icon := IconSquare()
					color := secondaryText
					if selected {
						icon = IconCircleCheck()
						color = ThemeAccent(c)
					}
					ui.Icon(c, icon).Size(IconSM, IconSM).TextColor(color)
				}

				if task.Blocked {
					ui.Icon(c, IconFlame()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeWarning(c))
				}

				if name := projectNameFor(s, task.ProjectID); name != "" {
					ui.Row(c.Key("kanban-task-project-"+task.ID)).
						Padding(Space0, Space1).
						Radius(RadiusXS).
						Background(ThemeOverlay(c, 0.06)).
						Children(func() {
							ui.Text(c, name).
								FontSize(TypeCaption).
								FontWeight(500).
								TextColor(secondaryText).
								SingleLine()
						})
				}

				if pLabel, pIcon, pColor, ok := kanbanPriorityStyle(c, task.Priority); ok {
					ui.Row(c.Key("kanban-task-prio-"+task.ID)).
						Padding(Space0, Space2).
						Radius(RadiusMD).
						Gap(Space1).
						AlignItems(ui.Center).
						Background(pColor.Alpha(0.15)).
						Children(func() {
							ui.Icon(c, pIcon).Size(cardGlyphSize, cardGlyphSize).TextColor(pColor)
							ui.Text(c, pLabel).
								FontSize(TypeCaption).
								FontWeight(600).
								TextColor(pColor)
						})
				}

				ui.Box(c.Key("kanban-task-head-spacer-" + task.ID)).Grow(1)

				// 状态与审查徽标：颜色与列头圆点保持一致
				if label := kanbanStatusBadge(task); label != "" {
					kanbanToneBadge(c, "kanban-task-status-"+task.ID, label, kanbanStatusColor(c, kanbanDisplayColumn(task)))
				}
			})

		// 标题
		ui.Text(c, task.Title).
			FontSize(TypeBody).
			FontWeight(600).
			MaxLines(2).
			TextColor(primaryText)

		// 阻塞与注意标记
		if task.Blocked || len(task.AttentionFlags) > 0 {
			ui.Row(c.Key("kanban-task-flags-" + task.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Wrap().
				Gap(Space2).
				Children(func() {
					if task.Blocked {
						ui.Row(c.Key("kanban-task-blocked-"+task.ID)).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeDanger(c).Alpha(0.12)).
							Gap(Space1).
							Children(func() {
								ui.Icon(c, IconFlame()).
									Size(kanbanBadgeIconSize, kanbanBadgeIconSize).
									TextColor(ThemeDanger(c))
								ui.Text(c, "阻塞").
									FontSize(TypeCaption).
									TextColor(ThemeDanger(c))
							})
					}
					for idx, flag := range task.AttentionFlags {
						fVal := flag
						ui.Row(c.Key(fmt.Sprintf("kanban-task-flag-%s-%d", task.ID, idx))).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeWarning(c).Alpha(0.12)).
							Children(func() {
								ui.Text(c, fVal).
									FontSize(TypeCaption).
									TextColor(ThemeWarning(c))
							})
					}
				})
		}

		// 备注预览：最多两行
		if task.Notes != "" {
			ui.Text(c, task.Notes).
				FontSize(TypeCaption).
				TextColor(secondaryText).
				MaxLines(2)
		}

		// 目标提示：任务来自目标规划时，用一行次要文字显示所属目标
		if task.GoalID != "" {
			goalHint := "目标任务 " + task.GoalID
			for _, g := range st.goals {
				if g.ID == task.GoalID {
					if title := strings.TrimSpace(g.Title); title != "" {
						goalHint = title
					} else if prompt := strings.TrimSpace(g.Prompt); prompt != "" {
						goalHint = prompt
					}
					break
				}
			}
			ui.Row(c.Key("kanban-task-goal-" + task.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space1).
				Children(func() {
					ui.Icon(c, IconWandSparkles()).Size(kanbanBadgeIconSize, kanbanBadgeIconSize).TextColor(ThemePlan(c))
					ui.Text(c, goalHint).
						FontSize(TypeCaption).
						FontWeight(500).
						TextColor(ThemePlan(c)).
						SingleLine()
				})
		}

		// 最近活动或失败原因
		activity := task.LastFailureReason
		if activity == "" {
			activity = task.LastActivity
		}
		if activity != "" {
			ui.Text(c, activity).
				FontSize(TypeCaption).
				TextColor(secondaryText.Alpha(0.80)).
				SingleLine()
		}

		// 审查会话入口：有审查会话时显示「打开审查会话」
		if task.ThreadID != "" && kanbanDisplayColumn(task) != "todo" {
			Button(c.Key("kanban-task-review-open-"+task.ID), ButtonOpts{
				Text:    "打开审查会话",
				Label:   "kanban-task-review-open-" + task.ID,
				Size:    ControlXS,
				Variant: ButtonSecondary,
				OnClick: func() {
					s.closeWorkspace()
					s.loadThreads(task.ProjectID, false)
					s.revealThread(task.ThreadID)
				},
			})
		}

		// 操作按钮区：CanStart, CanMove, CanReview
		ui.Row(c.Key("kanban-task-actions-" + task.ID)).
			FillWidth().
			Wrap().
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				if task.ThreadID != "" {
					Button(c.Key("kanban-task-"+task.ID+"-open"), ButtonOpts{
						Text: "会话", Label: "kanban-task-" + task.ID + "-open",
						OnClick: func() {
							s.closeWorkspace()
							s.loadThreads(task.ProjectID, false)
							s.revealThread(task.ThreadID)
						},
						Variant: ButtonSecondary,
					})
				}
				// 已完成任务可以重新打开，移回待办
				if task.Status == "done" || task.Column == "done" {
					Button(c.Key("kanban-task-"+task.ID+"-reopen"), ButtonOpts{
						Text:    "重新打开",
						Label:   "kanban-task-" + task.ID + "-reopen",
						Variant: ButtonSecondary,
						OnClick: func() {
							s.moveTask(task.ID, "todo", task.Revision)
						},
					})
				}

				// 移动菜单按钮
				if task.CanMove {
					Button(c.Key("kanban-task-"+task.ID+"-move"), ButtonOpts{
						Text:    "移动",
						Label:   "kanban-task-" + task.ID + "-move",
						Variant: ButtonSecondary,
						OnClick: func() {
							kanbanMu.Lock()
							if ks.moveMenuTaskID == task.ID {
								ks.moveMenuTaskID = ""
							} else {
								ks.moveMenuTaskID = task.ID
							}
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				}
				ui.Box(c.Key("kanban-task-actions-spacer-" + task.ID)).Grow(1)

				// 开始：被阻塞时点击或悬停说明原因，不启动任务。
				if task.CanStart {
					label := "开始"
					if kanbanDisplayColumn(task) == "in_progress" {
						label = "继续"
					}
					startBtn := Button(c.Key("kanban-task-"+task.ID+"-start"), ButtonOpts{
						Text:    label,
						Label:   "kanban-task-" + task.ID + "-start",
						Size:    ControlXS,
						Variant: ButtonPrimary,
						OnClick: func() {
							if task.Blocked || len(task.AttentionFlags) > 0 {
								s.setToast("暂不可启动：" + kanbanTaskBlockReason(task))
								return
							}
							s.startTask(task.ID, task.Revision)
						},
					})
					if task.Blocked || len(task.AttentionFlags) > 0 {
						startBtn.Tooltip("暂不可启动：" + kanbanTaskBlockReason(task))
					}
				}

				// 卡片操作菜单
				Button(c.Key("kanban-task-"+task.ID+"-more"), ButtonOpts{
					Icon:    IconChevronRight(),
					Label:   "kanban-task-" + task.ID + "-more",
					Size:    ControlXS,
					Variant: ButtonSecondary,
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
			})

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

					for _, opt := range moveTargetsFor(task.Status) {
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
								s.moveTask(task.ID, optVal.status, task.Revision)
							},
						})
					}
				})
		}

		// 救援菜单展开：打开会话、提醒 Agent、中断并重置、删除
		if st.rescueMenuTaskID == task.ID {
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
							ks.deleteTaskID = task.ID
							kanbanMu.Unlock()
							s.invalidate()
						},
					})
				})
		}

		// 删除二次确认：确认后调用客户端删除任务，成功时刷新看板。
		if st.deleteTaskID == task.ID {
			ui.Row(c.Key("kanban-task-delete-confirm-"+task.ID)).
				FillWidth().
				Wrap().
				AlignItems(ui.Center).
				Padding(Space1, Space2).
				Radius(RadiusSM).
				Background(ThemeDanger(c).Alpha(0.10)).
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
		Label(key+"-"+text).
		Padding(Space0, Space2).
		Radius(RadiusMD).
		Background(ThemeOverlay(c, 0.08)).
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
	width := (avail - 1 - float32(len(kanbanColumnDefs)-1)*kanbanColumnSpacing) / float32(len(kanbanColumnDefs))
	if width < kanbanColumnMinWidth {
		width = kanbanColumnMinWidth
	}
	return width
}

// renderKanbanOverview 渲染「全部项目」概览：每个项目一张圆角卡片，
// 列出该项目最近的任务；点击任务或右上角按钮进入该项目的看板。
func renderKanbanOverview(c *ui.Context, s *chatState, tasks []KanbanTask) {
	s.mu.Lock()
	projects := append([]Project(nil), s.projects...)
	s.mu.Unlock()

	// 没有任何项目时的空态
	if len(projects) == 0 {
		ui.Column(c.Key("kanban-overview-empty")).
			Grow(1).
			FillWidth().
			Center().
			Padding(Space7).
			Gap(Space3).
			Children(func() {
				ui.Icon(c, IconFolderOpen()).
					Size(kanbanEmptyIconSize, kanbanEmptyIconSize).
					TextColor(ThemeSecondaryText(c).Alpha(0.5))
				ui.Text(c, "还没有添加项目").
					FontSize(TypeTitle).
					FontWeight(600).
					TextColor(ThemePrimaryText(c))
				ui.Text(c, "在侧边栏添加项目后，这里会汇总各项目的任务").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					MaxLines(2)
			})
		return
	}

	ui.Scroll(c.Key("kanban-overview-scroll")).
		Grow(1).
		FillWidth().
		MinWidth(0).
		Children(func() {
			ui.Column(c.Key("kanban-overview-root")).
				FillWidth().
				Gap(Space3).
				Children(func() {
					ui.Text(c, "全部项目").
						FontSize(TypeFootnote).
						FontWeight(600).
						TextColor(ThemeSecondaryText(c))

					ui.Row(c.Key("kanban-overview-row")).
						FillWidth().
						Wrap().
						Gap(kanbanColumnSpacing).
						Children(func() {
							for _, p := range projects {
								proj := p
								var projTasks []KanbanTask
								for _, task := range tasks {
									if task.ProjectID == proj.ID {
										projTasks = append(projTasks, task)
									}
								}
								renderKanbanOverviewCard(c, s, proj, projTasks)
							}
						})
				})
		})
}

// renderKanbanOverviewCard 渲染概览里的单个项目卡片。
func renderKanbanOverviewCard(c *ui.Context, s *chatState, project Project, tasks []KanbanTask) {
	const overviewCardWidth = float32(300)
	const overviewTaskLimit = 6

	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].UpdatedAt.After(tasks[j].UpdatedAt)
	})

	setOverview := func() {
		// getKanbanState 内部会取 kanbanMu，这里先取状态再加锁，避免重复加锁自锁。
		ks := getKanbanState(s)
		if ks == nil {
			return
		}
		kanbanMu.Lock()
		ks.overview = false
		kanbanMu.Unlock()
	}

	ui.Column(c.Key("kanban-overview-card-" + project.ID)).
		Label("kanban-overview-card-" + project.ID).
		Width(overviewCardWidth).
		MaxHeight(kanbanOverviewCardMaxHeight).
		Padding(Space3).
		Radius(RadiusMD).
		Background(ThemeSurfaceCard(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c)).
		Gap(Space3).
		Children(func() {
			ui.Row(c.Key("kanban-overview-card-header-" + project.ID)).
				FillWidth().
				AlignItems(ui.Center).
				Gap(Space3).
				Children(func() {
					ui.Icon(c, IconFolder()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					ui.Text(c, project.Name).
						FontSize(TypeBody).
						FontWeight(600).
						TextColor(ThemePrimaryText(c)).
						Grow(1).
						SingleLine()
					kanbanCountBadge(c, "kanban-overview-count-"+project.ID, len(tasks), nil)
					IconButton(c.Key("kanban-overview-open-"+project.ID), IconButtonProps{
						Icon:  IconChevronRight(),
						Label: "打开看板",
						OnClick: func() {
							setOverview()
							s.setWorkspaceProject(project.ID)
						},
						Variant: ButtonGhost,
						Size:    ControlMD,
					})
				})

			if len(tasks) == 0 {
				ui.Text(c, "暂无任务").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c)).
					Center().
					Padding(Space4, 0)
				return
			}

			for i, task := range tasks {
				if i >= overviewTaskLimit {
					break
				}
				taskVal := task
				row := ui.Row(c.Key("kanban-overview-task-"+taskVal.ID)).
					Label("kanban-overview-task-"+taskVal.ID).
					FillWidth().
					Padding(Space1, Space2).
					Radius(RadiusSM).
					AlignItems(ui.Center).
					Gap(Space2)
				if row.Hovered() {
					row.Background(ThemeHover(c))
				}
				row.OnClick(func() {
					setOverview()
					s.setWorkspaceProject(project.ID)
				})
				row.Children(func() {
					ui.Text(c, taskVal.Title).
						FontSize(TypeFootnote).
						TextColor(ThemePrimaryText(c)).
						Grow(1).
						SingleLine()
					if label := kanbanStatusBadge(taskVal); label != "" {
						ui.Row(c.Key("kanban-overview-status-"+taskVal.ID)).
							Padding(Space0, Space1).
							Radius(RadiusXS).
							Background(ThemeOverlay(c, 0.08)).
							Children(func() {
								ui.Text(c, label).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
							})
					}
				})
			}

			// 超出概览上限的任务收成一行，点击进入该项目看完整看板
			if len(tasks) > overviewTaskLimit {
				more := len(tasks) - overviewTaskLimit
				moreText := fmt.Sprintf("还有 %d 个任务", more)
				moreRow := ui.ButtonBase(c.Key("kanban-overview-more-"+project.ID)).
					Label(moreText).
					FillWidth().
					Height(ControlXS).
					Padding(0, Space2).
					Radius(RadiusSM).
					AlignItems(ui.Center).
					Gap(Space1).
					Cursor(ui.CursorPointer).
					Tooltip("进入该项目查看全部任务").
					OnClick(func() {
						setOverview()
						s.setWorkspaceProject(project.ID)
					})
				if moreRow.Hovered() {
					moreRow.Background(ThemeHover(c))
				}
				moreRow.Children(func() {
					ui.Text(c, moreText).
						FontSize(TypeCaption).
						TextColor(ThemeSecondaryText(c)).
						Grow(1)
					ui.Icon(c, IconChevronRight()).
						Size(kanbanBadgeIconSize, kanbanBadgeIconSize).
						TextColor(ThemeSecondaryText(c))
				})
			}
		})
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

	colBox := ui.Column(c.Key("kanban-col-" + colKey)).
		Label("kanban-column-" + colKey).
		Width(colWidth).
		MinWidth(kanbanColumnMinWidth).
		FillHeight().
		Radius(RadiusLG).
		Background(ThemeSurfaceCard(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeOverlay(c, 0.08)).
		Padding(Space3).
		Gap(Space3)

	// 原生拖拽投放响应 (Drop)
	if payload, ok := ui.Drop[kanbanDragPayload](colBox); ok {
		switch colKey {
		case "in_progress":
			if payload.Status == "todo" && payload.CanStart {
				s.startTask(payload.TaskID, payload.Revision)
			} else {
				s.setToast("此任务暂不可启动，请先处理前置依赖或会话状态")
			}
		case "needs_you":
			s.setToast("此列由会话状态决定，请打开任务会话处理")
		default:
			if payload.Status != colKey {
				// 拖拽投放要按状态迁移规则过滤：后端只接受合法迁移（如 done、rejected 只能回到待办），
				// 非法投放直接忽略并提示，避免发一次必定被拒的 MoveTask。
				if !kanban.CanTransition(kanban.TaskStatus(payload.Status), kanban.TaskStatus(colKey)) {
					s.setToast("此任务不能从当前状态移动到此列，请先回到待办")
				} else {
					s.moveTask(payload.TaskID, colKey, payload.Revision)
				}
			}
		}
	}

	// 拖拽悬停高亮：目标列边框变 accent 2pt；needs_you 列不接受投放，不高亮
	if _, over := ui.DragOver[kanbanDragPayload](colBox); over && colKey != "needs_you" {
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
		ui.Scroll(c.Key("kanban-col-scroll-" + colKey)).
			Grow(1).
			FillWidth().
			MinWidth(0).
			Children(func() {
				ui.Column(c.Key("kanban-col-cards-" + colKey)).
					FillWidth().
					Gap(Space3).
					Children(func() {
						if len(colTasks) == 0 {
							emptyLabel := "暂无任务"
							if searching {
								emptyLabel = "未找到任务"
							}
							ui.Text(c, emptyLabel).
								FontSize(TypeCaption).
								TextColor(secondaryText).
								Center().
								Padding(Space4, 0)
						}
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
