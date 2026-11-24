package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

type taskDetailDrawerSplitOpts struct {
	AvailableWidth float32
	PreferredWidth float32
	SplitKey       string
	BoardKey       string
	HandleKey      string
	PaneKey        string
	OnWidthChange  func(float32)
	OnWidthCommit  func(float32)
	Board          func(boardWidth float32)
	Detail         func()
}

// renderTaskDetailDrawerSplit 在宽屏下渲染主区、可拖拽分隔手柄与右侧详情分栏，空间不足时仅渲染主区。
func renderTaskDetailDrawerSplit(c *ui.Context, opts taskDetailDrawerSplitOpts) {
	pane, boardWidth, detailWidth := kanbanDetailLayout(opts.AvailableWidth, opts.PreferredWidth)
	if !pane {
		if opts.Board != nil {
			opts.Board(boardWidth)
		}
		return
	}
	splitKey := opts.SplitKey
	if splitKey == "" {
		splitKey = "task-detail-split"
	}
	boardKey := opts.BoardKey
	if boardKey == "" {
		boardKey = "task-detail-board"
	}
	handleKey := opts.HandleKey
	if handleKey == "" {
		handleKey = "task-detail-handle"
	}
	paneKey := opts.PaneKey
	if paneKey == "" {
		paneKey = "task-detail-pane"
	}

	ui.Row(c.Key(splitKey)).Fill().Children(func() {
		ui.Column(c.Key(boardKey)).Width(boardWidth).FillHeight().Shrink(0).Children(func() {
			if opts.Board != nil {
				opts.Board(boardWidth)
			}
		})
		handle := ui.Box(c.Key(handleKey)).Label("调整详情宽度").Width(sidePaneHandleWidth).FillHeight().Shrink(0).Focusable().Role(ui.RoleSplitter).Cursor(ui.CursorResizeEW)
		dx, _, held := handle.Dragged()
		_, _, released := handle.DragEnded()
		adjust := func(delta float32) float32 {
			_, _, width := kanbanDetailLayout(opts.AvailableWidth, max(KanbanDetailMinWidth, detailWidth+delta))
			if opts.OnWidthChange != nil {
				opts.OnWidthChange(width)
			}
			return width
		}
		if held && dx != 0 {
			detailWidth = adjust(-dx)
		}
		if released && opts.OnWidthCommit != nil {
			opts.OnWidthCommit(clampTasksDetailWidth(detailWidth))
		}
		handle.OnShortcut(0, ui.KeyLeft, func() {
			width := adjust(sidePaneHandleStep)
			if opts.OnWidthCommit != nil {
				opts.OnWidthCommit(clampTasksDetailWidth(width))
			}
		})
		handle.OnShortcut(0, ui.KeyRight, func() {
			width := adjust(-sidePaneHandleStep)
			if opts.OnWidthCommit != nil {
				opts.OnWidthCommit(clampTasksDetailWidth(width))
			}
		})
		color := ThemeDivider(c)
		if held || handle.Hovered() {
			color = ThemeAccent(c)
		}
		handle.Children(func() {
			ui.Box(c.Key(handleKey + "-line")).Absolute().Top(0).Bottom(0).Left((sidePaneHandleWidth - Hairline) / 2).Width(Hairline).Background(color)
		})
		ui.Column(c.Key(paneKey)).Width(detailWidth).FillHeight().Shrink(0).Background(ThemeSurfaceRaised(c)).Transition(motionSidePanel(c, false)).Children(func() {
			if opts.Detail != nil {
				opts.Detail()
			}
		})
	})
}

type taskDetailDrawerSheetOpts struct {
	SheetKey string
	OnClose  func()
	OnEscape func()
	Content  func()
}

// renderTaskDetailDrawerSheet 在窄屏下以 ui.DialogBase 弹层承载详情内容，支持遮罩点击与 Esc 关闭。
func renderTaskDetailDrawerSheet(c *ui.Context, opts taskDetailDrawerSheetOpts) {
	sheetKey := opts.SheetKey
	if sheetKey == "" {
		sheetKey = "task-detail-sheet"
	}
	winWidth, winHeight := c.Size()
	sheetWidth, sheetHeight := min(settingsSheetWidth, winWidth-Space8), winHeight-Space8
	open := true
	backdropClicked := false
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Background(ui.Color{}.Alpha(ScrimOpacity))
		panel.MaxWidth(winWidth - Space8).MaxHeight(winHeight - Space8).Padding(0)
		ui.Box(c.Key(sheetKey + "-backdrop")).Fill().OnClick(func() { backdropClicked = true })
		ui.Column(c.Key(sheetKey)).Label(sheetKey).Width(sheetWidth).Height(sheetHeight).
			Background(ThemeSurface(c)).Radius(RadiusLG).Transition(motionDialog(c)).Children(func() {
			if opts.Content != nil {
				opts.Content()
			}
		})
	})
	if !open {
		if !backdropClicked && opts.OnEscape != nil {
			opts.OnEscape()
		} else if opts.OnClose != nil {
			opts.OnClose()
		}
	}
}

type taskDetailDrawerHeaderOpts struct {
	Key           string
	Leading       func()
	OriginSteward bool
	StatusLabel   string
	StatusColor   ui.Color
	Title         string
	FallbackTitle string
	ProjectName   string
	ThreadID      string
	OnOpenThread  func()
	ExtraActions  func()
	CloseKey      string
	CloseLabel    string
	CloseTooltip  string
	OnClose       func()
}

// renderTaskDetailDrawerHeader 渲染统一的详情抽屉顶栏（返回入口、总管标记、状态徽标、项目名、标题、打开会话与关闭按钮）。
func renderTaskDetailDrawerHeader(c *ui.Context, opts taskDetailDrawerHeaderOpts) {
	key := opts.Key
	if key == "" {
		key = "task-detail-header"
	}
	displayTitle := strings.TrimSpace(opts.Title)
	if displayTitle == "" {
		displayTitle = strings.TrimSpace(opts.FallbackTitle)
	}
	projectName := strings.TrimSpace(opts.ProjectName)
	hasMeta := opts.OriginSteward || opts.StatusLabel != "" || projectName != ""

	ui.Column(c.Key(key)).FillWidth().Padding(Space4, Space5).Gap(Space2).Children(func() {
		ui.Row(c.Key(key + "-bar")).FillWidth().AlignItems(ui.Center).Gap(Space2).Children(func() {
			if opts.Leading != nil {
				opts.Leading()
			}
			ui.Row(c.Key(key + "-meta")).Grow(1).Shrink(1).MinWidth(0).AlignItems(ui.Center).Gap(Space2).Children(func() {
				if opts.OriginSteward {
					renderStewardOriginBadge(c, key+"-origin")
				}
				if opts.StatusLabel != "" {
					kanbanToneBadge(c, key+"-status", opts.StatusLabel, opts.StatusColor)
				}
				if projectName != "" {
					ui.Text(c.Key(key+"-project"), projectName).
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine().
						Ellipsis("…").
						Tooltip(projectName).
						Shrink(1).
						MinWidth(0)
				}
				if !hasMeta && displayTitle != "" {
					ui.Text(c.Key(key+"-title"), displayTitle).
						FontSize(uiFont(TypeHeading)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c)).
						SingleLine().
						Ellipsis("…").
						Tooltip(displayTitle).
						Grow(1).
						Shrink(1).
						MinWidth(0)
				}
			})
			ui.Row(c.Key(key + "-actions")).Shrink(0).AlignItems(ui.Center).Gap(Space1).Children(func() {
				if opts.ThreadID != "" && opts.OnOpenThread != nil {
					Button(c.Key(key+"-open-thread"), ButtonOpts{
						Text:    "打开会话",
						Label:   "打开会话",
						Variant: ButtonSecondary,
						Size:    ControlSM,
						OnClick: opts.OnOpenThread,
					})
				}
				if opts.ExtraActions != nil {
					opts.ExtraActions()
				}
				if opts.OnClose != nil {
					closeKey := opts.CloseKey
					if closeKey == "" {
						closeKey = key + "-close"
					}
					closeLabel := opts.CloseLabel
					if closeLabel == "" {
						closeLabel = "关闭"
					}
					closeTooltip := opts.CloseTooltip
					if closeTooltip == "" {
						closeTooltip = closeLabel
					}
					IconButton(c.Key(closeKey), IconButtonProps{
						Icon:    IconX(),
						Label:   closeLabel,
						Tooltip: closeTooltip,
						Variant: ButtonGhost,
						OnClick: opts.OnClose,
					})
				}
			})
		})
		if hasMeta && displayTitle != "" {
			ui.Text(c.Key(key+"-title"), displayTitle).
				FontSize(uiFont(TypeTitle)).
				FontWeight(600).
				TextColor(ThemePrimaryText(c)).
				SingleLine().
				Ellipsis("…").
				Tooltip(displayTitle).
				FillWidth().
				MinWidth(0)
		}
	})
}

// renderTaskDetailDrawerEmpty 渲染详情抽屉对象缺失时的统一空态。
func renderTaskDetailDrawerEmpty(c *ui.Context, key, title string) {
	EmptyState(c, EmptyStateProps{
		Key:     key,
		Title:   title,
		Compact: true,
	})
}
