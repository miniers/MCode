package mcodeui

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

// openKanbanNewTaskSheet 打开新建任务弹窗。
func openKanbanNewTaskSheet(s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	if ks.newTaskProjectID == "" {
		ks.newTaskProjectID = ks.projectID
	}
	ks.showNewTask = true
	ks.showNewGoal = false
	kanbanMu.Unlock()
	// 每次打开都从收起态开始，避免沿用上一轮遗留的下拉状态。
	setKanbanPickerMenuOpen(s, false)
	s.invalidate()
}

// closeKanbanNewTaskSheet 关闭新建任务弹窗，并清理弹窗内项目下拉的展开状态。
func closeKanbanNewTaskSheet(s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	ks.showNewTask = false
	kanbanMu.Unlock()
	setKanbanPickerMenuOpen(s, false)
	modalDragReset(modalDragKanbanNewTask)
	s.invalidate()
}

// openKanbanNewGoalSheet 打开新建目标弹窗。
func openKanbanNewGoalSheet(s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	if ks.newGoalProjectID == "" {
		ks.newGoalProjectID = ks.projectID
	}
	ks.showNewGoal = true
	ks.showNewTask = false
	kanbanMu.Unlock()
	s.invalidate()
}

// closeKanbanNewGoalSheet 关闭新建目标弹窗。
func closeKanbanNewGoalSheet(s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	ks.showNewGoal = false
	kanbanMu.Unlock()
	modalDragReset(modalDragKanbanNewGoal)
	s.invalidate()
}

// kanbanSheetLabel 渲染弹窗里的字段名。
func kanbanSheetLabel(c *ui.Context, key, text string) {
	ui.Text(c, text).
		Label(key).
		FontSize(uiFont(TypeBody)).
		FontWeight(500).
		TextColor(ThemePrimaryText(c))
}

// kanbanModalPickOption 是弹窗内项目下拉的一行的展示信息。
type kanbanModalPickOption struct {
	ID   string
	Name string
	// Hint 是与其他项目重名时附加的区分信息（父目录名或路径尾段），为空时不显示。
	Hint string
}

// kanbanProjectDisplayName 返回项目的展示名，空名回退为「未命名项目」。
func kanbanProjectDisplayName(p Project) string {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name
	}
	return "未命名项目"
}

// kanbanProjectListOptions 把项目列表转换成下拉可选行：过滤掉路径为空的项目
// （会显示成「/」的无效项），并在展示名重复时追加父目录名或路径尾段作为区分。
func kanbanProjectListOptions(projects []Project) []kanbanModalPickOption {
	valid := make([]Project, 0, len(projects))
	counts := make(map[string]int, len(projects))
	for _, p := range projects {
		if p.ID == "" || strings.TrimSpace(p.Path) == "" {
			continue
		}
		valid = append(valid, p)
		counts[kanbanProjectDisplayName(p)]++
	}
	opts := make([]kanbanModalPickOption, 0, len(valid))
	for _, p := range valid {
		name := kanbanProjectDisplayName(p)
		opt := kanbanModalPickOption{ID: p.ID, Name: name}
		if counts[name] > 1 {
			opt.Hint = kanbanProjectHint(p.Path)
		}
		opts = append(opts, opt)
	}
	return opts
}

// kanbanProjectHint 取项目路径的父目录名（新建项目默认以仓库目录名为名称，
// 重名时父目录最能区分），取不到时回退到路径尾段。
func kanbanProjectHint(path string) string {
	parent := filepath.Dir(strings.TrimRight(path, "/"))
	if base := filepath.Base(parent); base != "" && base != "." && base != "/" {
		return base
	}
	return filepath.Base(path)
}

// kanbanProjectOptionLabel 渲染下拉一行：名称占满剩余宽度，重名时右侧追加次要色小号的父目录名。
func kanbanProjectOptionLabel(c *ui.Context, opt kanbanModalPickOption) {
	ui.Text(c, opt.Name).
		FontSize(uiFont(TypeFootnote)).
		TextColor(ThemePrimaryText(c)).
		Grow(1).
		SingleLine()
	if opt.Hint != "" {
		ui.Text(c, opt.Hint).
			Label("项目所在目录 "+opt.Hint).
			FontSize(uiFont(TypeCaption)).
			TextColor(ThemeSecondaryText(c)).
			Margin(0, Space2, 0, 0).
			Shrink(0).
			SingleLine()
	}
}

// kanbanSheetProjectPicker 渲染新建弹窗里的项目选择：显示当前项目名，点击在下方展开项目列表。
// 弹窗内的下拉使用独立的开关状态与浮层，不与看板头部的项目菜单共用；
// 选中、点外部、按 Esc 关闭弹窗时都会清理该状态。
func kanbanSheetProjectPicker(c *ui.Context, s *chatState, projectID string, onSelect func(string)) {
	s.mu.Lock()
	projects := append([]Project(nil), s.projects...)
	s.mu.Unlock()
	projects = withoutStewardProject(s.stewardProjectID(), projects)
	opts := kanbanProjectListOptions(projects)

	kst := getKanbanModalState(s)
	if kst == nil {
		return
	}
	kanbanModalMu.Lock()
	open := kst.pickerMenuOpen
	kanbanModalMu.Unlock()
	nextOpen := !open

	name := "选择项目"
	for _, opt := range opts {
		if opt.ID == projectID {
			name = opt.Name
			break
		}
	}

	ui.Column(c.Key("kanban-sheet-project")).Gap(Space1).Children(func() {
		btn := ui.ButtonBase(c.Key("kanban-sheet-project-toggle")).
			Label("项目").
			FillWidth().
			Height(ControlMD).
			Padding(0, Space3).
			Radius(RadiusSM).
			AlignItems(ui.Center).
			Gap(Space2).
			Cursor(ui.CursorPointer).
			Background(ThemeOverlay(c, 0.06)).
			// 点按钮只负责开合；关闭统一走下面的 appPopover（点外部 / Esc 也会写回该状态）。
			OnClick(func() {
				setKanbanPickerMenuOpen(s, nextOpen)
			})
		scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
		btn.Scale(scale, scale)
		btn.Children(func() {
			ui.Icon(c, IconFolder()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
			ui.Text(c, name).
				FontSize(uiFont(TypeBody)).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()
			ui.Icon(c, IconChevronDown()).Size(10, 10).TextColor(ThemeSecondaryText(c))
		})

		if !open || len(opts) == 0 {
			return
		}
		opened := true
		anchoredMenu(c, "kanban-sheet-project-menu", btn, &opened, func() {
			setKanbanPickerMenuOpen(s, false)
		}, 220, 320, 280, func() {
			popoverCard(c, ui.Column(c.Key("kanban-sheet-project-menu-card")).
				FillWidth().
				Padding(Space1).
				Gap(2).
				MaxHeight(272).
				Clip().
				Children(func() {
					ui.Scroll(c.Key("kanban-sheet-project-menu-scroll")).Grow(1).Children(func() {
						ui.Column(c.Key("kanban-sheet-project-menu-list")).Gap(2).Children(func() {
							for _, opt := range opts {
								option := opt
								item := ui.ButtonBase(c.Key("kanban-sheet-project-menu-item-"+option.ID)).
									Label(option.Name).
									FillWidth().
									Height(ControlMD).
									Padding(0, Space3).
									Radius(RadiusSM).
									AlignItems(ui.Center)
								if item.Hovered() {
									item.Background(ThemeHover(c))
								}
								item.OnClick(func() {
									setKanbanPickerMenuOpen(s, false)
									onSelect(option.ID)
								})
								item.Children(func() {
									kanbanProjectOptionLabel(c, option)
									if option.ID == projectID {
										ui.Icon(c, IconCheck()).Size(10, 10).TextColor(ThemeAccent(c))
									}
								})
							}
						})
					})
				}))
		})
	})
}

// renderKanbanNewTaskSheet 渲染新建任务弹窗（宽 540、内边距 20，
// 标题 / 项目 / 需求与备注三项，右上角取消，右下角「保存任务」，标题与备注都为空时不可提交）。
// 落盘字段只保留 Client.CreateTask 支持的 projectID、title、notes。
func renderKanbanNewTaskSheet(c *ui.Context, s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	show := ks.showNewTask
	title := ks.newTaskTitle
	notes := ks.newTaskNotes
	projectID := ks.newTaskProjectID
	busy, errMsg := ks.newTaskBusy, ks.newTaskErr
	kanbanMu.Unlock()
	if !show {
		modalDragReset(modalDragKanbanNewTask)
		return
	}

	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragKanbanNewTask, 540, 480, min(360, max(0, winW-modalDragPanelInset)), min(240, max(0, winH-modalDragPanelInset)), winW, winH)
	open := true
	Sheet(c, SheetProps{Key: modalDragKanbanNewTask, PanelKey: "kanban-new-task-sheet", PanelLabel: "kanban-new-task-sheet", CardKey: "kanban-new-task-card", Label: "kanban-new-task-card", Open: &open, Width: width, Height: height}, func() {
		ui.Column(c).FillWidth().Grow(1).Padding(Space6).Gap(Space5).Children(func() {
			// 标题行：标题 + 右上角取消，整行兼作弹窗的拖拽把手
			modalDragHandle(c, modalDragKanbanNewTask, ui.Row(c.Key("kanban-new-task-header")).
				FillWidth().
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "新建任务").
						FontSize(uiFont(TypeTitle)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c)).
						Grow(1)
					Button(c.Key("kanban-new-task-cancel"), ButtonOpts{
						Text:    "取消",
						Label:   "kanban-new-task-cancel",
						Variant: ButtonSecondary,
						OnClick: func() {
							closeKanbanNewTaskSheet(s)
						},
					})
				}))

			ui.Scroll(c.Key("kanban-new-task-scroll")).FillWidth().Grow(1).Children(func() {
				ui.Column(c.Key("kanban-new-task-fields")).FillWidth().Gap(Space5).Children(func() {
					// 任务标题（可留空，由备注自动生成标题）
					ui.Column(c.Key("kanban-new-task-title-field")).FillWidth().Gap(Space2).Children(func() {
						kanbanSheetLabel(c, "任务标题", "任务标题")
						input := ui.TextInput(c.Key("kanban-new-task-title"), &ks.newTaskTitle).
							Label("kanban-new-task-title").
							Placeholder("可选：留空则用备注自动生成标题").
							FillWidth().
							AutoFocus()
						if input.Changed() {
							ks.newTaskVersion++
						}
					})

					// 项目
					ui.Column(c.Key("kanban-new-task-project-field")).FillWidth().Gap(Space2).Children(func() {
						kanbanSheetLabel(c, "项目", "项目")
						kanbanSheetProjectPicker(c, s, projectID, func(id string) {
							kanbanMu.Lock()
							ks.newTaskProjectID = id
							ks.newTaskVersion++
							kanbanMu.Unlock()
							s.invalidate()
						})
					})

					// 需求与备注（多行）
					ui.Column(c.Key("kanban-new-task-notes-field")).FillWidth().Gap(Space2).Children(func() {
						kanbanSheetLabel(c, "需求与备注", "需求与备注")
						input := ui.TextArea(c.Key("kanban-new-task-notes"), &ks.newTaskNotes).
							Label("kanban-new-task-notes").
							Lines(4, 6).
							FillWidth()
						if input.Changed() {
							ks.newTaskVersion++
						}
					})

				})
			})
			// 底部：保存任务
			ui.Row(c.Key("kanban-new-task-footer")).
				FillWidth().
				AlignItems(ui.Center).
				Children(func() {
					if errMsg != "" {
						ui.Text(c, errMsg).TextColor(ThemeDanger(c))
					}
					ui.Box(c.Key("kanban-new-task-footer-spacer")).Grow(1)
					Button(c.Key("kanban-new-task-save"), ButtonOpts{
						Text:     "保存任务",
						Label:    "保存任务",
						Variant:  ButtonPrimary,
						Disabled: busy || (strings.TrimSpace(title) == "" && strings.TrimSpace(notes) == ""),
						OnClick: func() {
							submitKanbanNewTask(s)
						},
					})
				})
		})
		modalResizeGrips(c, modalDragKanbanNewTask, width, height)
	})

	if !open {
		closeKanbanNewTaskSheet(s)
	}
}

// submitKanbanNewTask 校验并提交新建任务：标题留空时用备注首行生成标题。
func submitKanbanNewTask(s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	title := strings.TrimSpace(ks.newTaskTitle)
	notes := strings.TrimSpace(ks.newTaskNotes)
	projectID := ks.newTaskProjectID
	kanbanMu.Unlock()

	if projectID == "" {
		s.setToast("请选择项目")
		return
	}
	if title == "" {
		title = notes
		if idx := strings.IndexAny(title, "\r\n"); idx >= 0 {
			title = title[:idx]
		}
		runes := []rune(strings.TrimSpace(title))
		if len(runes) > 48 {
			runes = runes[:48]
		}
		title = strings.TrimSpace(string(runes))
		if title == "" {
			title = "未命名任务"
		}
	}

	s.createTask(projectID, title, notes)
}

// renderKanbanNewGoalSheet 渲染新建目标弹窗（宽 560、内边距 24，
// 标题与说明、大字 Prompt 输入卡、选项行、右下角「规划目标 ⌘↩」）。
// 依赖 mcode 的 CreateGoal(projectID, prompt)，选项行只展示默认策略，不参与落盘。
func renderKanbanNewGoalSheet(c *ui.Context, s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	show := ks.showNewGoal
	prompt := ks.newGoalPrompt
	projectID := ks.newGoalProjectID
	busy, errMsg := ks.newGoalBusy, ks.newGoalErr
	kanbanMu.Unlock()
	if !show {
		modalDragReset(modalDragKanbanNewGoal)
		return
	}

	canCreate := !busy && projectID != "" && strings.TrimSpace(prompt) != ""
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragKanbanNewGoal, 560, 520, min(360, max(0, winW-modalDragPanelInset)), min(240, max(0, winH-modalDragPanelInset)), winW, winH)

	open := true
	Sheet(c, SheetProps{Key: modalDragKanbanNewGoal, PanelKey: "kanban-new-goal-sheet", PanelLabel: "kanban-new-goal-sheet", CardKey: "kanban-new-goal-card", Label: "kanban-new-goal-card", Open: &open, Width: width, Height: height}, func() {
		ui.Column(c).FillWidth().Grow(1).Padding(Space7).Gap(Space5).Children(func() {
			// 头部：标题、说明与关闭，整行兼作弹窗的拖拽把手
			modalDragHandle(c, modalDragKanbanNewGoal, ui.Row(c.Key("kanban-new-goal-header")).
				FillWidth().
				AlignItems(ui.Start).
				Gap(Space4).
				Children(func() {
					ui.Column(c.Key("kanban-new-goal-header-text")).Grow(1).Gap(Space1).Children(func() {
						ui.Text(c, "新建目标").
							FontSize(uiFont(TypePageTitle)).
							FontWeight(600).
							TextColor(ThemePrimaryText(c))
					})
					Button(c.Key("kanban-new-goal-cancel"), ButtonOpts{
						Text:    "取消",
						Label:   "kanban-new-goal-cancel",
						Variant: ButtonSecondary,
						OnClick: func() {
							closeKanbanNewGoalSheet(s)
						},
					})
				}))

			ui.Scroll(c.Key("kanban-new-goal-scroll")).FillWidth().Grow(1).Children(func() {
				ui.Column(c.Key("kanban-new-goal-fields")).FillWidth().Gap(Space5).Children(func() {
					// Prompt 输入卡：多行输入 + 底部工具栏（项目选择）
					ui.Column(c.Key("kanban-new-goal-prompt-card")).
						FillWidth().
						Radius(RadiusXL).
						Background(ThemeOverlay(c, 0.04)).
						BorderWidth(1).
						BorderColor(ThemeOverlay(c, 0.10)).
						Gap(0).
						Children(func() {
							input := ui.TextArea(c.Key("kanban-new-goal-prompt"), &ks.newGoalPrompt).
								Label("kanban-new-goal-prompt").
								Placeholder("例如：为设置页加上深色模式，并补上测试").
								FontSize(uiFont(TypeProse)).
								Lines(5, 9).
								FillWidth().
								AutoFocus()
							if input.Changed() {
								ks.newGoalVersion++
							}
							ui.Row(c.Key("kanban-new-goal-toolbar")).
								FillWidth().
								Padding(Space3, Space3).
								BorderWidth(1, 0, 0, 0).
								BorderColor(ThemeOverlay(c, 0.06)).
								AlignItems(ui.Center).
								Children(func() {
									ui.Column(c.Key("kanban-new-goal-project")).Width(220).Children(func() {
										kanbanSheetProjectPicker(c, s, projectID, func(id string) {
											kanbanMu.Lock()
											ks.newGoalProjectID = id
											ks.newGoalVersion++
											kanbanMu.Unlock()
											s.invalidate()
										})
									})
								})
						})

				})
			})
			// 底部：说明 + 规划目标
			ui.Row(c.Key("kanban-new-goal-footer")).
				FillWidth().
				AlignItems(ui.Center).
				Children(func() {
					if errMsg != "" {
						ui.Text(c, errMsg).TextColor(ThemeDanger(c))
					}
					ui.Box(c).Grow(1)
					Button(c.Key("kanban-new-goal-create"), ButtonOpts{
						Text:     "规划目标",
						Label:    "规划目标",
						Variant:  ButtonPrimary,
						Size:     ControlLG,
						Disabled: !canCreate,
						OnClick: func() {
							submitKanbanNewGoal(s)
						},
					})
				})
		})
		modalResizeGrips(c, modalDragKanbanNewGoal, width, height)
	})

	if !open {
		closeKanbanNewGoalSheet(s)
	}
}

// kanbanGoalOptionPill 渲染目标弹窗里的选项胶囊，只做展示。
func kanbanGoalOptionPill(c *ui.Context, key, label string, on bool) {
	tone := ToneNeutral
	if on {
		tone = ToneAccent
	}
	Capsule(c, CapsuleProps{
		Key:      key,
		Text:     label,
		Icon:     IconCheck(),
		Tone:     tone,
		Size:     ControlSM,
		Selected: on,
	})
}

// submitKanbanNewGoal 校验并提交新建目标。
func submitKanbanNewGoal(s *chatState) {
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	prompt := strings.TrimSpace(ks.newGoalPrompt)
	projectID := ks.newGoalProjectID
	kanbanMu.Unlock()

	if projectID == "" {
		s.setToast("请选择项目")
		return
	}
	if prompt == "" {
		s.setToast("目标 Prompt 不能为空")
		return
	}

	s.createGoal(projectID, prompt)
}
