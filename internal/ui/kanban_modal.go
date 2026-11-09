package mcodeui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
	"mcode/internal/kanban"
)

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
	// 单行宽度估算：标题与计数 128、范围胶囊 80、项目选择 154、搜索 140、视图分段 170，
	// 两个文字按钮各 94、刷新和关闭各 28、间距 9×12、左右内边距各 16。
	kanbanHeaderSingleRowMinWidth float32 = 128 + 80 + 154 + 140 + 170 + 2*94 + 2*ControlMD + 9*Space4 + 2*Space5
	// kanbanEmptyIconSize 是空态插图的边长。
	kanbanEmptyIconSize float32 = 36
	// kanbanProjectPickerMaxHeight 是项目下拉浮层列表的最大高度，超出后在浮层内滚动。
	kanbanProjectPickerMaxHeight float32 = 352
	// kanbanOverviewCardMaxHeight 是概览卡片的最大高度，超出后卡片内滚动。
	kanbanOverviewCardMaxHeight float32 = 420
	// kanbanDropBorder 是拖拽悬停时列与卡片的描边宽度。
	kanbanDropBorder float32 = 2
)

// kanbanModalOpen 报告指定会话的看板弹窗是否打开（由工作区视图 tasks 驱动）。
func kanbanModalOpen(s *chatState) bool {
	if s == nil {
		return false
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return false
	}
	workspaceMu.Lock()
	view := ws.view
	workspaceMu.Unlock()
	return view == "tasks"
}

// kanbanDisplayColumn 返回任务所在的展示列：优先服务端派生的 Column，
// 为空时才回退到原始 Status（Status 含 needs_approval、rejected 等非展示列值）。
func kanbanDisplayColumn(task KanbanTask) string {
	if task.Column != "" {
		return task.Column
	}
	return task.Status
}

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
	if curPID != "" && (ks.projectID != curPID || !ks.initialized) {
		kanbanMu.Lock()
		if ks.projectID != curPID {
			// 重建时保留已弹出的审查目标：可能刚指定了一个新项目里的目标，
			// 这里一丢就再也不会自动打开（planReviewSeen 已经把它标记成看过）。
			*ks = kanbanState{watchStop: ks.watchStop, loadID: ks.loadID, detailLoadID: ks.detailLoadID, planReviewSeen: ks.planReviewSeen, planReviewGoalID: ks.planReviewGoalID, viewMode: ks.viewMode, searchText: ks.searchText, overview: ks.overview, selecting: ks.selecting}
		}
		ks.projectID = curPID
		ks.initialized = true
		kanbanMu.Unlock()
		s.startKanbanWatch(curPID)
		s.loadKanban(curPID)
	}

	kanbanMu.Lock()
	if ks.viewMode == "" {
		ks.viewMode = defaultView
	}
	p := kanbanModalBodyProps{
		projectID:        curPID,
		viewMode:         ks.viewMode,
		overview:         ks.overview,
		searching:        strings.TrimSpace(ks.searchText) != "",
		selecting:        ks.selecting,
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
	}
	// plan_review 目标首次出现时自动打开一次规划审查，记录已弹出过的目标避免重复弹出
	if ks.planReviewGoalID == "" && ks.detailTaskID == "" {
		for _, g := range ks.board.Goals {
			if g.Status != "plan_review" || ks.planReviewSeen[g.ID] {
				continue
			}
			if ks.planReviewSeen == nil {
				ks.planReviewSeen = make(map[string]bool)
			}
			ks.planReviewSeen[g.ID] = true
			ks.planReviewGoalID = g.ID
			ks.planReviewReplanOpen = false
			ks.planReviewFeedback = ""
			break
		}
	}
	// 搜索只改变展示，不改看板数据；看板与列表视图共用同一份过滤结果。
	p.visibleBoard = p.board
	p.visibleBoard.Tasks = filterKanbanTasks(p.board.Tasks, ks.searchText)
	detailTaskID := ks.detailTaskID
	kanbanMu.Unlock()

	if windowMode {
		winWidth, _ := c.Size()
		ui.Column(c.Key("kanban-window-root")).
			Fill().
			Background(ThemeWindowBg(c)).
			Clip().
			Gap(0).
			Children(func() {
				ui.Box(c.Key("kanban-window-titlebar")).FillWidth().Height(ControlLG).DragWindow()
				renderKanbanModalHeader(c, s, ks, p, winWidth)
				renderKanbanModalBody(c, s, ks, p)
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
				renderKanbanModalBody(c, s, ks, p)
				modalResizeGrips(c, modalDragKanbanWorkspace, modalWidth, modalHeight)
			})
		})

		if !open {
			modalDragReset(modalDragKanbanWorkspace)
			s.closeWorkspace()
			return
		}
	}

	// 规划审查弹窗先于任务详情渲染，任务详情浮在它上层
	renderGoalPlanReviewModal(c, s)

	// 目标产物弹窗与规划审查同级，任务详情仍浮在最上层
	renderGoalOutputsModal(c, s)

	if detailTaskID != "" {
		renderTaskDetail(c, s, ks)
	}
}

// kanbanModalBodyProps 汇总看板弹窗内容区需要的只读状态快照。
type kanbanModalBodyProps struct {
	projectID        string
	viewMode         string
	overview         bool
	searching        bool
	selecting        bool
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

// renderKanbanModalHeader 渲染弹窗头部：标题与计数、项目范围、搜索、视图分段、选择与新建入口。
func renderKanbanModalHeader(c *ui.Context, s *chatState, ks *kanbanState, p kanbanModalBodyProps, width float32) {
	primaryText := ThemePrimaryText(c)

	header := ui.Column(c.Key("kanban-modal-header")).
		Label("kanban-modal-header").FillWidth().Gap(0).
		BorderWidth(0, 0, Hairline, 0).BorderColor(ThemeBorder(c))
	compact := width > 0 && width < kanbanHeaderSingleRowMinWidth
	height := kanbanTopBarHeight
	if compact {
		height *= 2
	}
	renderScope := func() {
		if p.overview {
			kanbanHeaderPill(c, "kanban-overview-current", "当前项目", true, func() {
				kanbanMu.Lock()
				ks.overview = false
				kanbanMu.Unlock()
				s.invalidate()
			})
		} else {
			kanbanHeaderPill(c, "kanban-back-overview", "全部项目", false, func() {
				kanbanMu.Lock()
				ks.overview = true
				kanbanMu.Unlock()
				s.invalidate()
			})
		}
		kanbanProjectMenu(c, s, p.projectID, func(id string) { s.setWorkspaceProject(id) })
	}
	renderSearch := func() {
		kanbanMu.Lock()
		searchQuery := ks.searchText
		kanbanMu.Unlock()
		kanbanSearchRow(c, s, ks, &searchQuery)
	}
	header.Height(height).Children(func() {
		ui.Row(c.Key("kanban-header-main")).FillWidth().Height(kanbanTopBarHeight).
			Padding(0, Space5).AlignItems(ui.Center).Gap(Space4).Children(func() {
			// 看板图标 + 「任务与看板」+ 任务总数徽标
			// 标题不可压缩，否则窄窗口下会被压成「任...」。
			titleRow := ui.Row(c.Key("kanban-modal-title")).AlignItems(ui.Center).Gap(Space3).Shrink(0)
			if s.tasksWindowView == "" {
				modalDragHandle(c, modalDragKanbanWorkspace, titleRow)
			}
			titleRow.Children(func() {
				ui.Icon(c, IconColumns3()).Size(IconSM, IconSM).TextColor(ThemeAccent(c)).Shrink(0)
				ui.Text(c, "任务与看板").
					FontSize(TypeProse).
					FontWeight(600).
					TextColor(primaryText).
					SingleLine().
					Shrink(0)
				kanbanCountBadge(c, "kanban-modal-task-count", len(p.board.Tasks), nil)
			})

			if !compact {
				renderScope()
			}

			ui.Box(c.Key("kanban-header-spacer")).Grow(1)

			if !compact {
				renderSearch()
			}

			// 看板 / 列表 / 自动化：只影响当前会话，不写回配置
			viewIdx := 0
			switch p.viewMode {
			case config.KanbanViewList:
				viewIdx = 1
			case kanbanViewAutomations:
				viewIdx = 2
			}
			seg := kanbanViewSwitch(c, &viewIdx)
			if seg.Changed() {
				kanbanMu.Lock()
				switch viewIdx {
				case 1:
					ks.viewMode = config.KanbanViewList
				case 2:
					ks.viewMode = kanbanViewAutomations
				default:
					ks.viewMode = config.KanbanViewKanban
				}
				kanbanMu.Unlock()
				s.invalidate()
			}

			// 批量动作接入前隐藏入口：多选目前没有任何批量操作，先不渲染「批量选择」按钮。

			if compact {
				IconButton(c.Key("kanban-new-task-toggle"), IconButtonProps{
					Icon: IconPlus(), Label: "新建任务", Tooltip: "新建任务", Disabled: p.projectID == "",
					OnClick: func() { kanbanNewTaskAction(s) }, Variant: ButtonSecondary,
				})
				IconButton(c.Key("kanban-new-goal-toggle"), IconButtonProps{
					Icon: IconWandSparkles(), Label: "新建目标", Tooltip: "新建目标", Disabled: p.projectID == "",
					OnClick: func() { kanbanNewGoalAction(s) }, Variant: ButtonPrimary,
				})
			} else {
				Button(c.Key("kanban-new-task-toggle"), ButtonOpts{
					Icon:     IconPlus(),
					Text:     "新建任务",
					Label:    "新建任务",
					Disabled: p.projectID == "",
					OnClick: func() {
						kanbanNewTaskAction(s)
					},
					Variant: ButtonSecondary,
				})

				Button(c.Key("kanban-new-goal-toggle"), ButtonOpts{
					Icon:     IconWandSparkles(),
					Text:     "新建目标",
					Label:    "新建目标",
					Disabled: p.projectID == "",
					OnClick: func() {
						kanbanNewGoalAction(s)
					},
					Variant: ButtonPrimary,
				})
			}
			ui.Row(c.Key("kanban-header-actions")).Shrink(0).AlignItems(ui.Center).Gap(Space4).Children(func() {
				IconButton(c.Key("kanban-refresh-btn"), IconButtonProps{
					Icon:  IconRefreshCw(),
					Label: "刷新",
					OnClick: func() {
						s.loadKanban(p.projectID)
					},
					Variant: ButtonGhost,
					Size:    ControlMD,
				})
				if s.tasksWindowView != "" {
					IconButton(c.Key("kanban-window-close"), IconButtonProps{
						Icon:    IconX(),
						Label:   "关闭任务与看板",
						OnClick: s.closeTasksWindow,
						Variant: ButtonGhost,
						Size:    ControlMD,
					})
				}
			})
		})
		if compact {
			ui.Row(c.Key("kanban-header-filters")).FillWidth().Height(kanbanTopBarHeight).
				Padding(0, Space5).AlignItems(ui.Center).Gap(Space4).Children(func() {
				renderScope()
				ui.Box(c.Key("kanban-header-filter-spacer")).Grow(1)
				renderSearch()
			})
		}
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

// kanbanHeaderPill 渲染头部的小号 accent 胶囊按钮；filled 为 true 表示当前选中的范围。
func kanbanHeaderPill(c *ui.Context, key, label string, filled bool, onClick func()) {
	Capsule(c, CapsuleProps{
		Key:         key,
		Text:        label,
		Icon:        IconLayoutDashboard(),
		Tone:        ToneAccent,
		Size:        ControlSM,
		Interactive: true,
		Selected:    filled,
		Tooltip:     label,
		OnClick:     onClick,
	})
}

// kanbanCountBadge 渲染数量徽标。
func kanbanCountBadge(c *ui.Context, key string, count int, onClick func()) {
	badge := ui.Row(c.Key(key)).
		Label(fmt.Sprintf("%s-%d", key, count)).
		Padding(Space0, Space2).
		Radius(RadiusMD).
		Background(ThemeOverlay(c, 0.08)).
		Center()
	if onClick != nil {
		badge.OnClick(onClick)
	}
	badge.Children(func() {
		ui.Text(c, fmt.Sprintf("%d", count)).
			FontSize(TypeCaption).
			FontWeight(600).
			TextColor(ThemeSecondaryText(c))
	})
}

// kanbanViewSwitchCount 是看板视图分段控件的段数：看板、列表、自动化。
const kanbanViewSwitchCount = 3

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
		Width(140).
		Height(ControlSM).
		Radius(RadiusSM).
		BorderWidth(Hairline).
		BorderColor(ThemeOverlay(c, 0.10)).
		Background(ThemeHover(c)).
		Padding(0, Space3).
		Gap(Space2).
		AlignItems(ui.Center).
		Shrink(1).
		MinWidth(120)

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

		if row.Clicked() {
			in.Focus()
		}
		if in.Changed() && query != nil {
			kanbanMu.Lock()
			ks.searchText = *query
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
				ks.searchText = ""
				kanbanMu.Unlock()
				s.invalidate()
			})
			clear.Children(func() {
				ui.Icon(c, IconX()).Size(microIconSize, microIconSize).TextColor(ThemeSecondaryText(c))
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

// kanbanProjectMenu 渲染看板头部（任务与看板窗口标题栏）的项目选择器：收起时显示当前项目名，点击在按钮下方展开项目列表。
// 菜单走 Overlay + AttachTo 锚定在按钮左下方，宽度与高度受限、超出内部滚动，不覆盖标题栏。
// onSelect 在选中某个项目后调用。
func kanbanProjectMenu(c *ui.Context, s *chatState, projectID string, onSelect func(string)) {
	s.mu.Lock()
	projects := append([]Project(nil), s.projects...)
	s.mu.Unlock()
	opts := kanbanProjectListOptions(projects)

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

// renderKanbanSelectionBar 渲染多选模式的选择工具条：
// 左侧是已选数量与「仅待办任务可多选」提示，右侧是清除与完成。
// 批量合并运行 / 排队运行暂无对应的 client 方法，这里不提供这两个按钮。
func renderKanbanSelectionBar(c *ui.Context, s *chatState, ks *kanbanState) {
	kanbanMu.Lock()
	selected := 0
	for _, on := range ks.selectedTaskIDs {
		if on {
			selected++
		}
	}
	kanbanMu.Unlock()

	ui.Row(c.Key("kanban-selection-bar")).
		FillWidth().
		Padding(Space3, Space4).
		Radius(RadiusMD).
		Background(ThemeHover(c)).
		BorderWidth(Hairline).
		BorderColor(ThemeAccent(c).Alpha(0.30)).
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			ui.Text(c, fmt.Sprintf("已选 %d 项", selected)).
				FontSize(TypeFootnote).
				FontWeight(600).
				TextColor(ThemePrimaryText(c)).
				SingleLine().
				Shrink(0)
			ui.Text(c, "仅待办任务可多选").
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c)).
				SingleLine().
				Shrink(0)

			ui.Box(c.Key("kanban-selection-spacer")).Grow(1)

			Button(c.Key("kanban-selection-clear"), ButtonOpts{
				Text:  "清除",
				Label: "清除选择",
				OnClick: func() {
					kanbanMu.Lock()
					ks.selectedTaskIDs = nil
					kanbanMu.Unlock()
					s.invalidate()
				},
				Variant: ButtonSecondary,
				Size:    ControlSM,
			})

			Button(c.Key("kanban-selection-done"), ButtonOpts{
				Text:  "完成",
				Label: "完成选择",
				OnClick: func() {
					kanbanMu.Lock()
					ks.selecting = false
					ks.selectedTaskIDs = nil
					kanbanMu.Unlock()
					s.invalidate()
				},
				Variant: ButtonSecondary,
				Size:    ControlSM,
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
			// 0. 多选模式的选择工具条：已选数量、限制说明、清除与退出。
			if p.selecting {
				renderKanbanSelectionBar(c, s, ks)
			}

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
								s.loadKanban(p.projectID)
							},
							Variant: ButtonPrimary,
						})
					})
			}

			// 2. 自动化视图：直接嵌入现有的自动化工作区
			if p.viewMode == kanbanViewAutomations {
				ui.Column(c.Key("kanban-automations-embed")).Grow(1).FillWidth().MinWidth(0).Children(func() {
					renderAutomationWorkspace(c, s)
				})
				return
			}

			if p.errMsg != "" && (len(p.board.Tasks) > 0 || len(p.board.Goals) > 0) {
				ui.Text(c, "刷新失败: "+p.errMsg).FontSize(TypeFootnote).TextColor(ThemeDanger(c))
			}

			// 3. 目标区域 (Goals)
			if len(p.board.Goals) > 0 {
				ui.Column(c.Key("kanban-goals-section")).
					FillWidth().
					Gap(Space3).
					Children(func() {
						ui.Text(c, "目标规划").
							FontSize(TypeFootnote).
							FontWeight(600).
							TextColor(ThemeSecondaryText(c))

						ui.Row(c.Key("kanban-goals-list")).
							FillWidth().
							Wrap().
							Gap(Space3).
							Children(func() {
								for _, g := range p.board.Goals {
									goalVal := g
									renderGoalCard(c, s, ks, goalVal, p.replanGoalID, p.replanFeedback)
								}
							})
					})
			}

			// 4. 全部项目概览
			if p.overview {
				renderKanbanOverview(c, s, p.visibleBoard.Tasks)
				return
			}

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
								s.loadKanban(p.projectID)
							},
							Variant: ButtonSecondary,
						})
					})
				return
			}

			if !p.loading && len(p.board.Tasks) == 0 && len(p.board.Goals) == 0 {
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
						ui.Text(c, "新建一个任务，或用目标让 Agent 拆解规划").
							FontSize(TypeFootnote).
							TextColor(ThemeSecondaryText(c))
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
							Button(c.Key("kanban-empty-new-goal"), ButtonOpts{
								Icon:     IconWandSparkles(),
								Text:     "新建目标",
								Label:    "空态新建目标",
								Disabled: p.projectID == "",
								OnClick: func() {
									kanbanNewGoalAction(s)
								},
								Variant: ButtonSecondary,
							})
						})
					})
				return
			}

			if p.viewMode == config.KanbanViewList {
				renderKanbanListView(c, s, p.visibleBoard.Tasks)
				return
			}

			if p.searching && len(p.visibleBoard.Tasks) == 0 {
				ui.Column(c.Key("kanban-search-empty")).Fill().Center().Gap(Space2).Children(func() {
					ui.Icon(c, IconSearch()).Size(ControlSM, ControlSM).TextColor(ThemeSecondaryText(c).Alpha(0.50))
					ui.Text(c, "未找到任务").FontSize(TypeBody).TextColor(ThemeSecondaryText(c))
				})
				return
			}

			// 6. 看板五列横向排列：每列至少 270，窗口拉窄后横向滚动，不再压缩列宽
			colWidth := kanbanBoardColumnWidth(c, s)
			ui.ScrollHorizontal(c.Key("kanban-columns-row")).
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
							selecting:        p.selecting,
						})
					}
				})
		})
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
