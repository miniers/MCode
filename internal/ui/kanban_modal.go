package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// kanbanViewAutomations 是分段切换里的「自动化」视图，与看板 / 列表共用本地视图状态。
const kanbanViewAutomations = "automations"

const modalDragKanbanWorkspace = "modal-drag-kanban-workspace"

const (
	// kanbanModalInset 是弹窗距离窗口边缘的留白。
	kanbanModalInset = 24
	// kanbanModalRadius 是弹窗与侧滑面板的圆角。
	kanbanModalRadius = 14
	// kanbanColumnSpacing 是看板列间距。
	kanbanColumnSpacing = 14
	// kanbanColumnMinWidth 是看板单列的最小宽度：窗口不够宽时横向滚动，而不是把列压窄。
	kanbanColumnMinWidth = float32(270)
	// kanbanTopBarHeight 是弹窗顶栏（标题 + 视图切换 + 操作按钮）的高度。
	kanbanTopBarHeight float32 = 52
	// kanbanEmptyIconSize 是空态插图的边长。
	kanbanEmptyIconSize float32 = 36
	// kanbanProjectPickerMaxHeight 是项目下拉浮层列表的最大高度，超出后在浮层内滚动。
	kanbanProjectPickerMaxHeight float32 = 352
	// kanbanOverviewCardMaxHeight 是概览卡片的最大高度，超出后卡片内滚动。
	kanbanOverviewCardMaxHeight float32 = 420
	// kanbanDropBorder 是拖拽悬停时列与卡片的描边宽度。
	kanbanDropBorder float32 = 2
)

// renderKanbanWorkspace 在根层挂载「任务与看板」大弹窗。
// 工作区视图为 tasks 表示弹窗打开；按 Esc 或点击遮罩时关闭工作区。
func renderKanbanWorkspace(c *ui.Context, s *chatState) {
	renderKanbanPanel(c, s, false)
}

// renderKanbanWindow 铺满独立任务窗口，复用弹窗内容与详情层。
func renderKanbanWindow(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	if !kanbanModalOpen(s) {
		s.openWorkspace("tasks")
	}
	renderKanbanPanel(c, s, true)
}

func renderKanbanPanel(c *ui.Context, s *chatState, windowMode bool) {
	if s == nil {
		return
	}
	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	if !kanbanModalOpen(s) {
		modalDragReset(modalDragKanbanWorkspace)
		return
	}

	defaultView := kanbanConfiguredDefaultView(s)
	curPID := s.currentWorkspaceProjectID()
	if windowMode {
		kanbanMu.Lock()
		initialized, scope := ks.initialized, ks.scopeID
		kanbanMu.Unlock()
		if !initialized {
			s.setKanbanScope(scope, scope == "")
		}
		curPID = scope
	} else {
		s.syncKanbanProject(curPID)
	}

	kanbanMu.Lock()
	if ks.viewMode == "" {
		ks.viewMode = defaultView
	}
	p := kanbanModalBodyProps{
		projectID:        curPID,
		scopeID:          ks.scopeID,
		viewMode:         ks.viewMode,
		destination:      ks.destination,
		automationSearch: ks.automationSearch,
		goalID:           ks.goalID,
		overview:         ks.overview,
		searching:        strings.TrimSpace(ks.searchText) != "",
		loading:          ks.loading,
		errMsg:           ks.err,
		conflict:         ks.conflict,
		board:            ks.board,
		replanGoalID:     ks.replanGoalID,
		replanFeedback:   ks.replanFeedback,
		moveMenuTaskID:   ks.moveMenuTaskID,
		reviewTaskID:     ks.reviewTaskID,
		reviewFeedback:   ks.reviewFeedback,
		rescueMenuTaskID: ks.rescueMenuTaskID,
		deleteTaskID:     ks.deleteTaskID,
		viewFilter:       ks.viewFilter,
	}
	// 搜索只改变展示，不改看板数据；看板与列表视图共用同一份过滤结果。
	p.visibleBoard = p.board
	p.visibleBoard.Tasks = filterKanbanTasks(p.board.Tasks, ks.searchText)
	if ks.goalID != "" {
		filtered := make([]KanbanTask, 0, len(p.visibleBoard.Tasks))
		for _, task := range p.visibleBoard.Tasks {
			if task.GoalID == ks.goalID {
				filtered = append(filtered, task)
			}
		}
		p.visibleBoard.Tasks = filtered
	}
	if ks.viewFilter == "attention" {
		filtered := make([]KanbanTask, 0, len(p.visibleBoard.Tasks))
		for _, task := range p.visibleBoard.Tasks {
			if tasksNeedsAttention(task) {
				filtered = append(filtered, task)
			}
		}
		p.visibleBoard.Tasks = filtered
	}
	detailTaskID := ks.detailTaskID
	goalDetailOpen := ks.goalDetailOpen
	kanbanMu.Unlock()
	if p.destination != "automations" {
		kanbanKeyboardMu.Lock()
		oldProject := ""
		if keyboard := kanbanKeyboardStates[s]; keyboard != nil {
			oldProject = keyboard.projectID
		}
		kanbanKeyboardMu.Unlock()
		if oldProject != "" && oldProject != p.projectID {
			kanbanBatchCancel(s)
		}
		kanbanKeyboardProject(s, p.projectID)
		kanbanKeyboardUpdate(s, p.visibleBoard.Tasks, p.viewMode)
	}
	winWidth, _ := c.Size()
	contentWidth := winWidth
	if windowMode {
		contentWidth -= TasksSidebarWidth
	} else {
		contentWidth -= kanbanModalInset * 2
	}
	pane, _, _ := kanbanDetailLayout(contentWidth, ks.detailWidth)
	wideDetail := pane && p.destination != "automations" && (detailTaskID != "" || goalDetailOpen)
	renderBody := func() {
		if !wideDetail {
			p.boardWidth = contentWidth
			renderKanbanModalBody(c, s, ks, p)
			return
		}
		renderKanbanDetailSplit(c, s, ks, contentWidth, func(width float32) {
			p.boardWidth = width
			renderKanbanModalBody(c, s, ks, p)
		}, func() {
			if detailTaskID != "" {
				renderTaskDetailContent(c, s, ks)
			} else {
				renderGoalDetailContent(c, s, ks)
			}
		})
	}

	if windowMode {
		ui.Row(c.Key("kanban-window-root")).Fill().Background(ThemeWindowBg(c)).Clip().Gap(0).Children(func() {
			renderTasksWindowSidebar(c, s, ks, p)
			ui.Column(c.Key("tasks-window-content")).Grow(1).MinWidth(0).FillHeight().Gap(0).Children(func() {
				renderTasksWindowHeader(c, s, ks, p)
				renderBody()
			})
		})
	} else {
		winWidth, winHeight := c.Size()
		modalWidth := winWidth - kanbanModalInset*2
		modalHeight := winHeight - kanbanModalInset*2
		if modalWidth <= 0 {
			modalWidth = winWidth
		}
		if modalHeight <= 0 {
			modalHeight = winHeight
		}

		modalWidth, modalHeight = modalResizeSize(modalDragKanbanWorkspace, modalWidth, modalHeight, min(360, max(0, winWidth-modalDragPanelInset)), min(240, max(0, winHeight-modalDragPanelInset)), winWidth, winHeight)
		open := true
		ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
			// 遮罩透明（不套黑色容器），点击空白处关闭。
			backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center()
			_ = ui.Box(c.Key("kanban-modal-backdrop")).Fill().Background(ui.Transparent).OnClick(func() {
				open = false
			})
			if !open {
				return
			}

			// panel 本身不加任何外观，尺寸交给内层唯一的圆角卡片。
			modalDragPanel(c, modalDragKanbanWorkspace, panel)
			panel.Key("kanban-modal-panel").
				Label("kanban-modal-panel").
				Width(modalWidth).
				Height(modalHeight).
				Padding(0).
				Gap(0)

			card := ui.Column(c.Key("kanban-modal-card")).
				Label("kanban-modal-card").
				Fill().
				Radius(kanbanModalRadius).
				Background(ThemeWindowBg(c)).
				BorderWidth(Hairline).
				BorderColor(ThemeBorder(c)).
				Clip().
				Gap(0)
			applyElevation(c, card, ElevDialog).Children(func() {
				renderKanbanModalHeader(c, s, ks, p, modalWidth)
				renderBody()
				modalResizeGrips(c, modalDragKanbanWorkspace, modalWidth, modalHeight)
			})
		})

		if !open {
			modalDragReset(modalDragKanbanWorkspace)
			kanbanBatchCancel(s)
			s.closeWorkspace()
			return
		}
	}

	if !wideDetail && p.destination != "automations" && (detailTaskID != "" || goalDetailOpen) {
		open := true
		backdropClicked := false
		if detailTaskID != "" {
			contentWidth = winWidth
		}
		ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
			backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Background(ui.Color{}.Alpha(ScrimOpacity))
			ui.Box(c.Key("tasks-detail-backdrop")).Fill().OnClick(func() { backdropClicked = true })
			_, height := c.Size()
			panel.MaxWidth(contentWidth - Space8).MaxHeight(height - Space8).Padding(0)
			sheetWidth, sheetHeight := min(settingsSheetWidth, contentWidth-Space8), height-Space8
			key := "kanban-goal-sheet"
			if detailTaskID != "" {
				key = "kanban-detail-sheet"
			}
			ui.Column(c.Key(key)).Width(sheetWidth).Height(sheetHeight).Background(ThemeSurface(c)).Radius(RadiusLG).Transition(motionDialog(c)).Children(func() {
				if detailTaskID != "" {
					renderTaskDetailContent(c, s, ks)
				} else {
					renderGoalDetailContent(c, s, ks)
				}
			})
		})
		if !open {
			if !backdropClicked {
				consumeKanbanEscape(s)
			} else if detailTaskID != "" {
				s.closeTaskDetail()
			} else {
				kanbanMu.Lock()
				ks.goalDetailOpen = false
				kanbanMu.Unlock()
				s.invalidate()
			}
		}
	}
}

func renderTasksWindowHeader(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps) {
	ui.Row(c.Key("tasks-window-toolbar")).Label("tasks-window-toolbar").FillWidth().Height(TasksToolbarHeight).Shrink(0).Padding(0, Space5).AlignItems(ui.Center).Gap(Space4).BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c)).Children(func() {
		title := "全部任务"
		if p.destination == "automations" {
			title = "自动化"
		} else if ks.viewFilter == "attention" {
			title = "待你处理"
		} else if p.goalID != "" {
			for _, goal := range p.board.Goals {
				if goal.ID == p.goalID {
					title = goal.Title
					if title == "" {
						title = goal.Prompt
					}
					break
				}
			}
		}
		ui.Text(c, title).SingleLine().FontSize(TypeProse).FontWeight(600).TextColor(ThemePrimaryText(c)).Grow(1).MinWidth(0)
		if p.destination != "automations" {
			idx := 0
			if p.viewMode == config.KanbanViewList {
				idx = 1
			}
			if kanbanViewSwitch(c, &idx).Changed() {
				kanbanMu.Lock()
				ks.viewMode = config.KanbanViewKanban
				if idx == 1 {
					ks.viewMode = config.KanbanViewList
				}
				kanbanMu.Unlock()
				s.invalidate()
			}
		}
		query := p.automationSearch
		if p.destination != "automations" {
			query = ks.searchText
		}
		kanbanSearchRow(c, s, ks, &query)
		if p.destination == "automations" {
			renderTasksAutomationCreate(c, s, p.scopeID)
		} else {
			kanbanCreateMenu(c, s, ks)
		}
	})
}

// kanbanModalBodyProps 汇总看板弹窗内容区需要的只读状态快照。
type kanbanModalBodyProps struct {
	boardWidth       float32
	viewFilter       string
	projectID        string
	scopeID          string
	viewMode         string
	destination      string
	automationSearch string
	goalID           string
	overview         bool
	searching        bool
	loading          bool
	errMsg           string
	conflict         bool
	board            KanbanBoard
	visibleBoard     KanbanBoard
	replanGoalID     string
	replanFeedback   string
	moveMenuTaskID   string
	reviewTaskID     string
	reviewFeedback   string
	rescueMenuTaskID string
	deleteTaskID     string
}

func renderKanbanModalHeader(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps, width float32) {
	ui.Column(c.Key("kanban-modal-header")).Label("kanban-modal-header").FillWidth().Shrink(0).Gap(0).
		BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c)).Children(func() {
		ui.Row(c.Key("kanban-header-main")).FillWidth().Height(kanbanTopBarHeight).Padding(0, Space5).
			AlignItems(ui.Center).Gap(Space4).Children(func() {
			title := ui.Row(c.Key("kanban-modal-title")).Shrink(0)
			if s.tasksWindowView == "" {
				modalDragHandle(c, modalDragKanbanWorkspace, title)
			}
			title.Children(func() { ui.Text(c, "任务").FontSize(TypeProse).FontWeight(600).TextColor(ThemePrimaryText(c)) })
			ui.Box(c.Key("kanban-header-spacer")).Grow(1)
			idx := 0
			if p.destination == "automations" {
				idx = 1
			}
			parts := ui.SegmentedBase(c.Key("kanban-destination"), &idx, 2)
			parts.Track.Label("任务目的").Gap(Space0).Radius(RadiusSM).Background(ThemeSurface(c)).Children(func() {
				for i, label := range []string{"任务", "自动化"} {
					segment := parts.Segment(i).Label("kanban-destination-"+[]string{"tasks", "automations"}[i]).Padding(Space1, Space4).Radius(RadiusSM).Center().Transition(motionFeedback(c))
					if i == idx {
						segment.Background(ThemeWindowBg(c))
					}
					segment.Children(func() { ui.Text(c, label).SingleLine().TextColor(ThemePrimaryText(c)) })
				}
			})
			if parts.Track.Changed() {
				if idx == 1 {
					s.setTasksWindowView("automations")
				} else {
					s.setTasksWindowView("tasks")
				}
				s.invalidate()
			}
			if s.tasksWindowView != "" {
				IconButton(c.Key("kanban-window-close"), IconButtonProps{Icon: IconX(), Label: "关闭任务", OnClick: s.closeTasksWindow, Variant: ButtonGhost, Size: ControlMD})
			}
		})
		ui.Row(c.Key("kanban-header-filters")).FillWidth().Height(kanbanTopBarHeight).Padding(0, Space5).
			AlignItems(ui.Center).Gap(Space4).Children(func() {
			if p.destination != "automations" {
				kanbanProjectMenu(c, s, p.scopeID, func(id string) { s.setKanbanScope(id, id == "") })
				idx := 0
				if p.viewMode == config.KanbanViewList {
					idx = 1
				}
				seg := kanbanViewSwitch(c, &idx)
				if seg.Changed() {
					kanbanMu.Lock()
					if idx == 1 {
						ks.viewMode = config.KanbanViewList
					} else {
						ks.viewMode = config.KanbanViewKanban
					}
					kanbanMu.Unlock()
					s.invalidate()
				}
				if len(p.board.Goals) > 0 {
					kanbanGoalMenu(c, s, ks, p)
				}
			}
			ui.Box(c.Key("kanban-header-filter-spacer")).Grow(1)
			query := p.automationSearch
			if p.destination != "automations" {
				kanbanMu.Lock()
				query = ks.searchText
				kanbanMu.Unlock()
			}
			kanbanSearchRow(c, s, ks, &query)
			if p.destination != "automations" {
				kanbanCreateMenu(c, s, ks)
			} else {
				renderTasksAutomationCreate(c, s, p.scopeID)
			}
		})
	})
}

func renderTasksAutomationCreate(c *ui.Context, s *chatState, scopeID string) {
	as := getAutomationState(s)
	button := Button(c.Key("tasks-new-automation"), ButtonOpts{Text: "新建", Label: "新建自动化", Disabled: !automationCanCreate(s), Variant: ButtonPrimary, OnClick: func() {
		requestNewAutomation(s, scopeID, func(onChosen func(string)) {
			as.pendingProjectChoice = onChosen
			as.chooseProjectOpen = true
		})
	}})
	appPopover(c, button, &as.chooseProjectOpen, nil, func() {
		popoverCard(c, ui.Column(c.Key("tasks-automation-project-menu"))).Padding(Space2, Space2).Children(func() {
			for _, project := range s.projects {
				project := project
				Button(c.Key("tasks-automation-project-"+project.ID), ButtonOpts{Text: project.Name, Label: "选择项目 " + project.Name, Variant: ButtonGhost, OnClick: func() {
					choose := as.pendingProjectChoice
					as.pendingProjectChoice = nil
					as.chooseProjectOpen = false
					if choose != nil {
						choose(project.ID)
					}
				}})
			}
		})
	})
}

func kanbanCreateMenu(c *ui.Context, s *chatState, ks *kanbanState) {
	ui.Row(c.Key("kanban-create-menu")).AlignItems(ui.Center).Gap(Space0).Children(func() {
		IconButton(c.Key("kanban-new-task-toggle"), IconButtonProps{Icon: IconPlus(), Label: "新建任务", OnClick: func() { kanbanNewTaskAction(s) }, Variant: ButtonSecondary})
		btn := ui.ButtonBase(c.Key("kanban-create-toggle")).Label("新建菜单").Size(ControlSM, ControlSM).Center().OnClick(func() { kanbanMu.Lock(); ks.newMenuOpen = !ks.newMenuOpen; kanbanMu.Unlock(); s.invalidate() })
		btn.Children(func() { ui.Icon(c, IconChevronDown()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c)) })
		kanbanMu.Lock()
		open := ks.newMenuOpen
		kanbanMu.Unlock()
		if open {
			opened := true
			anchoredMenu(c, "kanban-create-picker", btn, &opened, func() { kanbanMu.Lock(); ks.newMenuOpen = false; kanbanMu.Unlock(); s.invalidate() }, 180, 220, 200, func() {
				popoverCard(c, ui.Column(c.Key("kanban-create-card")).Padding(Space1).Children(func() {
					Button(c.Key("kanban-new-goal-toggle"), ButtonOpts{Text: "新建目标", Label: "新建目标", Variant: ButtonGhost, OnClick: func() { kanbanMu.Lock(); ks.newMenuOpen = false; kanbanMu.Unlock(); kanbanNewGoalAction(s) }})
				}))
			})
		}
	})
}

func kanbanGoalMenu(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps) {
	btn := ui.ButtonBase(c.Key("kanban-goal-toggle")).Label("目标筛选").Height(ControlSM).Padding(0, Space3).Radius(RadiusSM).AlignItems(ui.Center).Gap(Space2).OnClick(func() { kanbanMu.Lock(); ks.goalMenuOpen = !ks.goalMenuOpen; kanbanMu.Unlock(); s.invalidate() })
	btn.Children(func() {
		ui.Text(c, "目标").FontSize(TypeFootnote).TextColor(ThemePrimaryText(c))
		ui.Icon(c, IconChevronDown()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
	})
	kanbanMu.Lock()
	open := ks.goalMenuOpen
	kanbanMu.Unlock()
	if !open {
		return
	}
	opened := true
	anchoredMenu(c, "kanban-goal-picker", btn, &opened, func() { kanbanMu.Lock(); ks.goalMenuOpen = false; kanbanMu.Unlock(); s.invalidate() }, 220, 320, 360, func() {
		popoverCard(c, ui.Column(c.Key("kanban-goal-picker-card")).Padding(Space1).Gap(Space0).MaxHeight(kanbanProjectPickerMaxHeight).Children(func() {
			ui.Scroll(c.Key("kanban-goal-picker-scroll")).Children(func() {
				goals := append([]KanbanGoal{{Title: "全部目标"}}, p.board.Goals...)
				for _, goal := range goals {
					goal := goal
					label := goal.Title
					if label == "" {
						label = goal.Prompt
					}
					ui.ButtonBase(c.Key("kanban-goal-item-"+goal.ID)).Label(label).FillWidth().Height(ControlMD).Padding(0, Space3).AlignItems(ui.Center).OnClick(func() {
						kanbanMu.Lock()
						ks.goalID = goal.ID
						ks.goalMenuOpen = false
						ks.goalDetailOpen = false
						kanbanMu.Unlock()
						s.invalidate()
					}).Children(func() { ui.Text(c, label).SingleLine().TextColor(ThemePrimaryText(c)) })
				}
			})
		}))
	})
}

// kanbanNewTaskAction 打开新建任务弹窗；工具栏按钮与空态按钮共用。
func kanbanNewTaskAction(s *chatState) {
	openKanbanNewTaskSheet(s)
}

// kanbanNewGoalAction 打开新建目标弹窗；工具栏按钮与空态按钮共用。
func kanbanNewGoalAction(s *chatState) {
	openKanbanNewGoalSheet(s)
}

// kanbanViewSwitchCount 是看板视图分段控件的段数：看板、列表、自动化。
const kanbanViewSwitchCount = 2

// kanbanViewSwitchSegment 返回第 i 段的文案与无障碍名（kanban-view-switch-<段>）。
func kanbanViewSwitchSegment(i int) (label, name string) {
	switch i {
	case 1:
		return "列表", "kanban-view-switch-list"
	case 2:
		return "自动化", "kanban-view-switch-automations"
	default:
		return "看板", "kanban-view-switch-kanban"
	}
}

// kanbanViewSwitch 渲染「看板 / 列表 / 自动化」三段切换。
// 分段按文案宽度排布，每段都带自己的 Label：
// 切换只需按 Label 点具体分段，不必按控件宽度比例猜坐标。
func kanbanViewSwitch(c *ui.Context, viewIdx *int) ui.Element {
	parts := ui.SegmentedBase(c.Key("kanban-view-switch"), viewIdx, kanbanViewSwitchCount)
	track := parts.Track.
		Label("看板视图").
		AlignItems(ui.Stretch).
		Padding(Space0).
		Gap(Space0).
		Radius(RadiusSM).
		Background(ThemeSurface(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeBorder(c))
	track.Children(func() {
		for i := 0; i < kanbanViewSwitchCount; i++ {
			label, name := kanbanViewSwitchSegment(i)
			seg := parts.Segment(i).
				Label(name).
				Padding(Space1, Space4).
				Radius(RadiusSM).
				Center()
			if i == *viewIdx {
				seg.Background(ThemeWindowBg(c))
				applyElevation(c, seg, ElevPopover)
			} else if seg.Hovered() {
				seg.Background(ThemeHover(c))
			}
			seg.Children(func() {
				ui.Text(c, label).
					SingleLine().
					TextColor(ThemePrimaryText(c))
			})
		}
	})
	return track
}

// kanbanSearchRow 渲染看板自有的小号搜索行：高 24、宽 140、1pt 细边框。
// 不再用统一搜索框的胶囊外观，避免在头部占宽过多。
func kanbanSearchRow(c *ui.Context, s *chatState, ks *kanbanState, query *string) {
	row := ui.Row(c.Key("kanban-search-row")).
		Width(TasksSearchWidth).
		Height(ControlSM).
		Radius(RadiusSM).
		BorderWidth(Hairline).
		BorderColor(ThemeOverlay(c, 0.10)).
		Background(ThemeHover(c)).
		Padding(0, Space3).
		Gap(Space2).
		AlignItems(ui.Center).
		Shrink(1).
		MinWidth(TasksSearchMinWidth)

	row.Children(func() {
		ui.Icon(c, IconSearch()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c)).Shrink(0)

		in := ui.TextInput(c.Key("kanban-search-input"), query).
			Label("筛选任务").
			Placeholder("筛选任务…").
			FontSize(TypeFootnote).
			TextColor(ThemePrimaryText(c)).
			BorderWidth(0).
			Background(ui.Transparent).
			Padding(0).
			Grow(1).
			MinWidth(0)

		if kanbanKeyboardSearchFocus(s, false) || row.Clicked() || c.Shortcut(ui.Cmd, ui.KeyF) {
			in.Focus()
		}
		if in.Changed() && query != nil {
			kanbanMu.Lock()
			if ks.destination == "automations" {
				ks.automationSearch = *query
			} else {
				ks.searchText = *query
			}
			kanbanMu.Unlock()
			s.invalidate()
		}

		if query != nil && *query != "" {
			clear := ui.ButtonBase(c.Key("kanban-search-clear")).
				Size(IconSM, IconSM).
				Radius(RadiusSM).
				Center().
				Label("清除搜索").
				Tooltip("清除搜索").
				Cursor(ui.CursorPointer)
			if clear.Hovered() {
				clear.Background(ThemeOverlay(c, 0.20))
			} else {
				clear.Background(ThemeOverlay(c, 0.10))
			}
			clear.OnClick(func() {
				kanbanMu.Lock()
				if ks.destination == "automations" {
					ks.automationSearch = ""
				} else {
					ks.searchText = ""
				}
				kanbanMu.Unlock()
				s.invalidate()
			})
			clear.Children(func() {
				ui.Icon(c, IconX()).Size(microIconSize, microIconSize).TextColor(ThemeSecondaryText(c))
			})
		}
	})
}

// kanbanProjectMenu 渲染看板头部（任务与看板窗口标题栏）的项目选择器：收起时显示当前项目名，点击在按钮下方展开项目列表。
// 菜单走 Overlay + AttachTo 锚定在按钮左下方，宽度与高度受限、超出内部滚动，不覆盖标题栏。
// onSelect 在选中某个项目后调用。
func kanbanProjectMenu(c *ui.Context, s *chatState, projectID string, onSelect func(string)) {
	s.mu.Lock()
	projects := append([]Project(nil), s.projects...)
	s.mu.Unlock()
	opts := kanbanProjectListOptions(projects)
	opts = append([]kanbanModalPickOption{{Name: "全部项目"}}, opts...)

	name := "全部项目"
	for _, opt := range opts {
		if opt.ID == projectID {
			name = opt.Name
			break
		}
	}

	open := kanbanProjectMenuOpen(s)
	// 点按钮只负责开合；关闭统一走 appPopover，避免与 PopoverBase 写入的关闭相互抵消。
	nextOpen := !open

	ui.Column(c.Key("kanban-project-menu")).MaxWidth(180).Shrink(1).Gap(Space1).Children(func() {
		btn := ui.ButtonBase(c.Key("kanban-project-menu-toggle")).
			Label("项目").
			Height(ControlSM).
			Padding(0, Space3).
			Radius(RadiusSM).
			AlignItems(ui.Center).
			Gap(Space2).
			Cursor(ui.CursorPointer).
			OnClick(func() {
				setKanbanProjectMenuOpen(s, nextOpen)
			})
		if btn.Hovered() {
			btn.Background(ThemeHover(c))
		} else {
			btn.Background(ThemeOverlay(c, 0.06))
		}
		btn.Children(func() {
			ui.Icon(c, IconFolder()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeSecondaryText(c))
			ui.Text(c, name).
				MaxWidth(110).
				FontSize(TypeFootnote).
				TextColor(ThemePrimaryText(c)).
				MaxLines(1)
			ui.Icon(c, IconChevronDown()).Size(cardGlyphSize, cardGlyphSize).TextColor(ThemeSecondaryText(c))
		})

		if !open || len(opts) == 0 {
			return
		}
		opened := true
		anchoredMenu(c, "kanban-project-picker", btn, &opened, func() {
			setKanbanProjectMenuOpen(s, false)
		}, 220, 320, 360, func() {
			popoverCard(c, ui.Column(c.Key("kanban-project-picker-card")).
				FillWidth().
				Padding(Space1).
				Gap(Space0).
				MaxHeight(kanbanProjectPickerMaxHeight).
				Clip().
				Children(func() {
					ui.Scroll(c.Key("kanban-project-picker-scroll")).Grow(1).Children(func() {
						ui.Column(c.Key("kanban-project-picker-list")).Gap(Space0).Children(func() {
							for _, opt := range opts {
								option := opt
								item := ui.ButtonBase(c.Key("kanban-project-item-"+option.ID)).
									Label(option.Name).
									FillWidth().
									Height(ControlMD).
									Padding(0, Space3).
									Radius(RadiusSM).
									AlignItems(ui.Center).
									OnClick(func() {
										setKanbanProjectMenuOpen(s, false)
										onSelect(option.ID)
									})
								if item.Hovered() {
									item.Background(ThemeHover(c))
								}
								item.Children(func() {
									kanbanProjectOptionLabel(c, option)
									if option.ID == projectID {
										ui.Icon(c, IconCheck()).Size(kanbanBadgeIconSize, kanbanBadgeIconSize).TextColor(ThemeAccent(c))
									}
								})
							}
						})
					})
				}))
		})
	})
}

// renderKanbanModalBody 渲染弹窗内容区：按视图模式在看板、列表、自动化与全部项目概览之间切换。
func renderKanbanModalBody(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps) {
	ui.Column(c.Key("kanban-modal-body")).
		Grow(1).
		FillWidth().
		MinWidth(0).
		Padding(Space5).
		Gap(Space3).
		Children(func() {
			renderKanbanSelectionBar(c, s, ks)

			// 1. 版本冲突横幅
			if p.conflict {
				ui.Row(c.Key("kanban-conflict-banner")).
					FillWidth().
					Padding(Space3, Space4).
					Radius(RadiusMD).
					Background(ThemeWarning(c).Alpha(0.16)).
					AlignItems(ui.Center).
					Gap(Space4).
					Children(func() {
						ui.Icon(c, IconOctagonAlert()).
							Size(IconMD, IconMD).
							TextColor(ThemeWarning(c))
						ui.Text(c, "数据已在别处被修改，请刷新后重试").
							FontSize(TypeBody).
							FontWeight(500).
							TextColor(ThemeWarning(c)).
							Grow(1)
						Button(c.Key("kanban-conflict-refresh"), ButtonOpts{
							Text:  "刷新",
							Label: "kanban-conflict-refresh",
							OnClick: func() {
								kanbanMu.Lock()
								ks.conflict = false
								kanbanMu.Unlock()
								s.loadKanban(p.scopeID)
							},
							Variant: ButtonPrimary,
						})
					})
			}

			// 2. 自动化视图：直接嵌入现有的自动化工作区
			if p.destination == "automations" {
				ui.Column(c.Key("kanban-automations-embed")).Grow(1).FillWidth().MinWidth(0).Transition(motionDialog(c)).Children(func() {
					renderAutomationWorkspaceInScope(c, s, automationWorkspaceProps{ScopeID: p.scopeID, SearchText: p.automationSearch, ClearSearch: func() {
						kanbanMu.Lock()
						ks.automationSearch = ""
						kanbanMu.Unlock()
						s.invalidate()
					}})
				})
				return
			}

			if p.errMsg != "" && (len(p.board.Tasks) > 0 || len(p.board.Goals) > 0) {
				ui.Row(c.Key("kanban-refresh-error")).AlignItems(ui.Center).Gap(Space3).Children(func() {
					ui.Text(c, "刷新失败").FontSize(TypeFootnote).TextColor(ThemeDanger(c))
					Button(c.Key("kanban-refresh-retry"), ButtonOpts{Text: "重试", Label: "重试", Variant: ButtonGhost, OnClick: func() { s.loadKanban(p.scopeID) }})
				})
			}

			// 3. 目标区域 (Goals)
			attentionGoals := 0
			if p.viewFilter == "attention" {
				query := strings.ToLower(strings.TrimSpace(ks.searchText))
				for _, goal := range p.board.Goals {
					if goal.Status != "plan_review" || (query != "" && !strings.Contains(strings.ToLower(goal.Title+" "+goal.Prompt), query)) {
						continue
					}
					goal := goal
					attentionGoals++
					title := goal.Title
					if title == "" {
						title = goal.Prompt
					}
					ui.ButtonBase(c.Key("tasks-attention-goal-" + goal.ID)).Label("确认目标计划 " + goal.ID).FillWidth().Height(RowHeight).AlignItems(ui.Center).Gap(Space3).OnClick(func() {
						kanbanMu.Lock()
						ks.goalID = goal.ID
						ks.viewFilter = ""
						ks.goalDetailOpen = true
						kanbanMu.Unlock()
						s.invalidate()
					}).Children(func() {
						ui.Icon(c, IconOctagonAlert()).Size(IconMD, IconMD).TextColor(ThemeWarning(c))
						ui.Text(c, title).SingleLine().TextColor(ThemePrimaryText(c))
					})
				}
			}
			for _, goal := range p.board.Goals {
				if goal.ID != p.goalID {
					continue
				}
				goal := goal
				title := goal.Title
				if title == "" {
					title = goal.Prompt
				}
				ui.ButtonBase(c.Key("kanban-selected-goal-" + goal.ID)).Label("目标详情 " + title).FillWidth().Height(ControlMD).Shrink(0).AlignItems(ui.Center).Transition(motionCollapse(c)).OnClick(func() { kanbanMu.Lock(); ks.goalDetailOpen = true; kanbanMu.Unlock(); s.invalidate() }).Children(func() {
					ui.Text(c, fmt.Sprintf("%s · %d/%d", title, goal.Done, goal.Total)).FontSize(TypeBody).TextColor(ThemePrimaryText(c))
				})
				break
			}

			// 4. 全部项目概览

			// 5. 三态处理与看板五列主体
			if p.loading && len(p.board.Tasks) == 0 && len(p.board.Goals) == 0 {
				ui.Column(c.Key("kanban-loading-box")).
					Fill().
					Center().
					Gap(Space3).
					Children(func() {
						LoadingIndicator(c, nil, "正在加载看板…", IconLG)
					})
				return
			}

			if p.errMsg != "" && len(p.board.Tasks) == 0 && len(p.board.Goals) == 0 {
				ui.Column(c.Key("kanban-error-box")).
					Fill().
					Center().
					Gap(Space3).
					Children(func() {
						ui.Icon(c, IconOctagonAlert()).
							Size(ControlSM, ControlSM).
							TextColor(ThemeDanger(c))
						ui.Text(c, p.errMsg).
							FontSize(TypeBody).
							TextColor(ThemeDanger(c))
						Button(c.Key("kanban-retry-btn"), ButtonOpts{
							Text:  "重试",
							Label: "重试",
							OnClick: func() {
								s.loadKanban(p.scopeID)
							},
							Variant: ButtonSecondary,
						})
					})
				return
			}

			s.mu.Lock()
			hasProjects := len(s.projects) > 0
			s.mu.Unlock()
			if !hasProjects {
				ui.Column(c.Key("kanban-no-projects")).Fill().Center().Children(func() {
					Button(c.Key("kanban-add-project"), ButtonOpts{Text: "添加项目", Label: "添加项目", Variant: ButtonPrimary, OnClick: s.promptAddProject})
				})
				return
			}
			if !p.loading && len(p.board.Tasks) == 0 && len(p.board.Goals) == 0 && !p.searching && p.goalID == "" {
				ui.Column(c.Key("kanban-empty-box")).
					Fill().
					Center().
					Gap(Space3).
					Children(func() {
						ui.Icon(c, IconColumns3()).
							Size(ControlLG, ControlLG).
							TextColor(ThemeSecondaryText(c).Alpha(0.40))
						ui.Text(c, "还没有任务").
							FontSize(TypeTitle).
							FontWeight(500).
							TextColor(ThemePrimaryText(c))
						ui.Row(c.Key("kanban-empty-actions")).AlignItems(ui.Center).Gap(Space3).Children(func() {
							Button(c.Key("kanban-empty-new-task"), ButtonOpts{
								Icon:     IconPlus(),
								Text:     "新建任务",
								Label:    "空态新建任务",
								Disabled: p.projectID == "",
								OnClick: func() {
									kanbanNewTaskAction(s)
								},
								Variant: ButtonPrimary,
							})
						})
					})
				return
			}

			if (p.searching || p.goalID != "" || p.viewFilter == "attention") && len(p.visibleBoard.Tasks) == 0 && attentionGoals == 0 {
				ui.Column(c.Key("kanban-search-empty")).Fill().Center().Gap(Space2).Children(func() {
					ui.Icon(c, IconSearch()).Size(ControlSM, ControlSM).TextColor(ThemeSecondaryText(c).Alpha(0.50))
					ui.Text(c, "未找到任务").FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
					Button(c.Key("kanban-clear-filters"), ButtonOpts{Text: "清除筛选", Label: "清除筛选", Variant: ButtonSecondary, OnClick: func() {
						kanbanMu.Lock()
						ks.searchText = ""
						ks.goalID = ""
						ks.viewFilter = ""
						kanbanMu.Unlock()
						s.invalidate()
					}})
				})
				return
			}
			if p.viewMode == config.KanbanViewList {
				renderKanbanListView(c, s, p.visibleBoard.Tasks)
				return
			}

			// 6. 看板五列横向排列：每列至少 270，窗口拉窄后横向滚动，不再压缩列宽
			collapsed := 0
			if !ks.doneExpanded {
				collapsed = 1
			}
			boardWidth := p.boardWidth
			if boardWidth <= 0 {
				boardWidth, _ = c.Size()
			}
			colWidth := kanbanColumnWidthFor(max(0, boardWidth-2*Space5), len(kanbanColumnDefs)-collapsed, collapsed)
			ui.ScrollHorizontal(c.Key("kanban-columns-row")).
				TrackScroll(&ks.columnsScroll).
				Grow(1).
				FillWidth().
				MinWidth(0).
				Gap(kanbanColumnSpacing).
				Children(func() {
					for _, colDef := range kanbanColumnDefs {
						colDefVal := colDef
						renderKanbanColumn(c, s, ks, colDefVal, p.visibleBoard.Tasks, p.board.Tasks, p.searching, colWidth, kanbanTaskCardState{
							moveMenuTaskID:   p.moveMenuTaskID,
							reviewTaskID:     p.reviewTaskID,
							reviewFeedback:   p.reviewFeedback,
							goals:            p.board.Goals,
							rescueMenuTaskID: p.rescueMenuTaskID,
							deleteTaskID:     p.deleteTaskID,
						})
					}
				})
		})
}
