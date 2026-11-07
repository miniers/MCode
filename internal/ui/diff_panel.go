package mcodeui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"mcode/internal/git"
)

const (
	DiffPanelDefaultWidth float32 = 440
	DiffPanelMinWidth     float32 = 320
	DiffPanelMaxWidth     float32 = 720
)

// 改动面板空态卡片文案（标题 + 灰色副标题）。
const (
	diffEmptyTitle    = "暂无代码变更"
	diffEmptySubtitle = "每轮对话后，Agent 改动的文件会显示在这里。"
)

// 改动面板的固定尺寸：标题栏高度、状态卡片图标与副标题宽度、轮次菜单尺寸。
const (
	diffHeaderHeight           float32 = 40
	diffStatusIconSize         float32 = 32
	diffStatusSubtitleMaxWidth float32 = 320
	diffTurnChevronSize        float32 = 10
	diffTurnMenuMinWidth       float32 = 120
)

// iconFileSearch 是空态卡片使用的「文档 + 放大镜」图标（Lucide file-search），
// SF Symbol doc.text.magnifyingglass。
var iconFileSearch = parseLucideSVG(`<path d="M6 22a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h8a2.4 2.4 0 0 1 1.704.706l3.588 3.588A2.4 2.4 0 0 1 20 8v12a2 2 0 0 1-2 2z"/><path d="M14 2v5a1 1 0 0 0 1 1h5"/><circle cx="11.5" cy="14.5" r="2.5"/><path d="M13.3 16.3 15 18"/>`)

var (
	diffPanelMu     sync.Mutex
	diffPanelStates = make(map[*chatState]*diffPanelState)
)

// diffPanelState 维护改动面板的状态。
type diffPanelState struct {
	open      bool
	threadID  string
	turnID    string
	loading   bool
	err       string
	data      ThreadDiff
	files     []git.FileDiff
	collapsed map[string]bool
	// expandedDeferred 缓存大文件延迟补丁点开后的展开结果，键为文件路径（见 expandDeferredDiffFile）。
	expandedDeferred map[string]git.FileDiff
	// expandingDeferred 记录正在后台展开的大文件，重复点开只排队一次解析。
	expandingDeferred map[string]bool
	// expandedLarge 记录用户显式展开过的大文件（超过 diffLargeFileLineThreshold 行），跨轮次保留。
	expandedLarge map[string]bool
	// hunksOnly 记录用户选了「只显示改动块」的大文件：按 hunk 逐块展示，跨轮次保留。
	hunksOnly map[string]bool
	// expandedHunks 记录「只显示改动块」模式下已整块展开的 hunk，键见 diffHunkStateKey。
	expandedHunks map[string]bool
	// rows 是摊平后的行模型缓存（见 diff_rows.go），rowsSig 用于判断能否复用，
	// rowsRev 在数据或折叠状态变化时自增，让签名失效。
	rows    []diffDisplayRow
	rowsSig uint64
	rowsRev uint64
	// rowsList 是行模型虚拟列表的滚动状态。
	rowsList     ui.ListState
	width        float32
	turnMenuOpen bool
	// worktreePath 是加载这批改动时识别到的 AI 自建 worktree；
	// 非空表示改动取自那个 worktree（见 ai_worktree.go）。
	worktreePath string
	// worktreeResolved 表示当前这批改动已确认过自建 worktree 的归属：
	// 首次从「没有 worktree」变成「有 worktree」时只重取一次改动，
	// 之后只认非空路径之间的切换，识别短暂返回空串不触发重取。
	worktreeResolved bool
}

func getDiffPanelState(s *chatState) *diffPanelState {
	if s == nil {
		return nil
	}
	diffPanelMu.Lock()
	defer diffPanelMu.Unlock()
	st, ok := diffPanelStates[s]
	if !ok {
		st = &diffPanelState{
			collapsed: make(map[string]bool),
			width:     sidePaneInitialWidth(s, sidePaneDiffID, DiffPanelMinWidth, DiffPanelDefaultWidth, DiffPanelMaxWidth),
		}
		diffPanelStates[s] = st
	}
	return st
}

func setDiffPanelState(s *chatState, st *diffPanelState) {
	if s == nil {
		return
	}
	diffPanelMu.Lock()
	defer diffPanelMu.Unlock()
	diffPanelStates[s] = st
}

// IsOpen 返回改动面板当前是否展开。
func (st *diffPanelState) IsOpen() bool {
	if st == nil {
		return false
	}
	diffPanelMu.Lock()
	defer diffPanelMu.Unlock()
	return st.open
}

// toggleDiffPanel 切换改动面板的展开与折叠状态。
func (s *chatState) toggleDiffPanel() {
	if s == nil {
		return
	}
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}

	diffPanelMu.Lock()
	dp.open = !dp.open
	isOpen := dp.open
	diffPanelMu.Unlock()

	if isOpen {
		// 右侧只留一个分栏：关掉文件面板与预览面板（改动与预览也互斥）。
		closeOtherSidePanes(s, sidePaneDiffID)
		s.mu.Lock()
		tid := s.selectedThreadID
		s.mu.Unlock()
		s.loadDiff(tid, "")
	}
	s.invalidate()
}

// loadDiff 在后台异步加载指定会话或轮次的代码差异。
func (s *chatState) loadDiff(threadID, turnID string) {
	if s == nil {
		return
	}
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}

	diffPanelMu.Lock()
	dp.threadID = threadID
	dp.turnID = turnID
	if threadID == "" {
		dp.loading = false
		dp.err = ""
		dp.data = ThreadDiff{}
		dp.files = nil
		dp.worktreePath = ""
		dp.worktreeResolved = false
		dp.rowsRev++
		dp.rebuildDiffRowsLocked()
		diffPanelMu.Unlock()
		s.invalidate()
		return
	}
	dp.loading = true
	dp.err = ""
	diffPanelMu.Unlock()
	s.invalidate()

	go func(tid, turn string) {
		ctx := context.Background()
		data, err := s.client.Diff(ctx, tid, turn)
		// 会话实际在 AI 自建的 worktree 里干活时，整个会话的改动改从那个 worktree 的根目录取。
		worktreePath := s.aiWorktreeFor(ctx, tid).Path
		if worktreePath != "" && turn == "" {
			if text, worktreeErr := aiWorktreeWorkingDiff(ctx, worktreePath); worktreeErr == nil {
				data.Diff = text
				data.FailureReason = ""
				data.Truncated = false
				err = nil
			}
		}

		var files []git.FileDiff
		var parseErr error
		if err == nil && data.Diff != "" {
			files, parseErr = git.ParseUnifiedDiff([]byte(data.Diff))
		}

		diffPanelMu.Lock()
		defer diffPanelMu.Unlock()
		if dp.threadID != tid || dp.turnID != turn {
			return
		}
		dp.loading = false
		dp.worktreePath = worktreePath
		// 识别出非空路径才意味着改动归属已确认；空串可能只是识别暂时给不出结论，
		// 留 false 让下一帧再看一次，但最多只看一次（见 diffWorktreeReloadNeeded）。
		dp.worktreeResolved = worktreePath != ""
		// 新一批改动到货，之前点开的大文件展开结果可能已过期，一并作废
		dp.expandedDeferred = nil
		dp.expandingDeferred = nil
		if err != nil {
			dp.err = err.Error()
			dp.data = ThreadDiff{}
			dp.files = nil
		} else if parseErr != nil {
			dp.err = "解析 diff 失败: " + parseErr.Error()
			dp.data = data
			dp.files = nil
		} else {
			dp.err = ""
			dp.data = data
			// 加载阶段就补齐增删统计并摊平出行模型，渲染期不再逐帧扫描 hunk 与行
			prefillDiffStats(files)
			dp.files = files
		}
		dp.rowsRev++
		dp.rebuildDiffRowsLocked()
		s.invalidate()
	}(threadID, turnID)
}

// diffWorktreeReloadNeeded 判断识别出的 AI 自建 worktree 是否值得重取一次改动。
// 识别可能短暂给出空串（后台识别还没结论），此时沿用上次的加载结果；
// 只有从「已知非空」变成另一个非空路径才重取；首次从空变成非空时重取一次并记已确认，
// 避免空串与同一路径交替时反复取数。
func diffWorktreeReloadNeeded(loadedWorktree string, resolved bool, current string) (reload, nextResolved bool) {
	if current == "" {
		return false, resolved
	}
	if loadedWorktree == "" {
		if resolved {
			// 已经为这次识别重取过，不再重复取数
			return false, true
		}
		return true, true
	}
	if current != loadedWorktree {
		return true, true
	}
	return false, true
}

// renderDiffPanel 渲染右侧代码改动面板。
func renderDiffPanel(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	dp := getDiffPanelState(s)
	if dp == nil {
		return
	}

	s.mu.Lock()
	selectedTid := s.selectedThreadID
	s.mu.Unlock()

	diffPanelMu.Lock()
	panelTid := dp.threadID
	loadedWorktree := dp.worktreePath
	worktreeResolved := dp.worktreeResolved
	panelTurn := dp.turnID
	needReload := dp.open && selectedTid != "" && selectedTid != panelTid && !dp.loading
	if dp.open && selectedTid == "" && panelTid != "" {
		dp.threadID = ""
		dp.turnID = ""
		dp.worktreePath = ""
		dp.worktreeResolved = false
		dp.data = ThreadDiff{}
		dp.files = nil
		dp.rowsRev++
		dp.rebuildDiffRowsLocked()
	}
	panelOpen := dp.open
	panelLoading := dp.loading
	diffPanelMu.Unlock()

	if needReload {
		s.loadDiff(selectedTid, "")
	} else if panelOpen && !panelLoading && panelTid != "" {
		// AI 自建 worktree 的识别可能晚于这次改动加载：识别结果变了就重取一次，让改动归属跟上。
		// 识别偶尔会短暂返回空串，不能当作「worktree 变了」，否则反复取数并触发整片内容重帧。
		reloadWorktree, resolvedWorktree := diffWorktreeReloadNeeded(loadedWorktree, worktreeResolved, s.aiWorktreePath(panelTid))
		if reloadWorktree {
			diffPanelMu.Lock()
			stillCurrent := dp.open && !dp.loading && dp.threadID == panelTid && dp.turnID == panelTurn
			if stillCurrent {
				dp.worktreeResolved = resolvedWorktree
			}
			diffPanelMu.Unlock()
			if stillCurrent {
				s.loadDiff(panelTid, panelTurn)
			}
		}
	}

	diffPanelMu.Lock()
	threadID := dp.threadID
	turnID := dp.turnID
	loading := dp.loading
	errMsg := dp.err
	data := dp.data
	worktreePath := dp.worktreePath
	// 行模型不可变，直接引用已发布的数据，不再每帧复制整个文件切片
	files := dp.files
	diffPanelMu.Unlock()

	viewState := diffPanelViewState(loading, errMsg, len(files))
	// 未选会话、加载、错误、非 Git、失败原因与空态仍是普通滚动容器；文件行才走虚拟列表
	showStatus := threadID == "" || viewState != "files" || data.FailureReason != ""

	ui.Column(c.Key("diff-panel-root")).
		Fill().
		Background(ThemeWindowBg(c)).
		BorderWidth(0, 0, 0, Hairline).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			// 顶部标题栏与控制项
			renderDiffHeader(c, s, dp, threadID, turnID, data)

			// AI 在自建 worktree 里干活时，说明改动取自哪个目录
			renderDiffWorktreeNotice(c, worktreePath, turnID)

			// 状态类视图（未选会话 / 加载 / 错误 / 非 Git / 空态）仍是普通滚动容器
			if showStatus {
				ui.Scroll(c.Key("diff-scroll")).
					Grow(1).
					FillWidth().
					Padding(Space4, Space4).
					Children(func() {
						if threadID == "" {
							ui.Text(c, "未选择会话").
								FontSize(TypeBody).
								TextColor(ThemeSecondaryText(c)).
								Margin(ui.Auto)
							return
						}
						switch viewState {
						case "loading":
							ui.Row(c.Key("diff-loading-row")).
								Gap(Space3).
								AlignItems(ui.Center).
								Margin(ui.Auto).
								Children(func() {
									ui.Spinner(c).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
									ui.Text(c, "正在读取改动…").
										FontSize(TypeBody).
										TextColor(ThemeSecondaryText(c))
								})
							return
						case "error":
							renderDiffStatusState(c, "diff-error-state", IconOctagonAlert(),
								"无法读取改动", errMsg, func() {
									s.loadDiff(threadID, turnID)
								})
							return
						case "not-git":
							renderDiffStatusState(c, "diff-not-git-state", IconGitBranch(),
								"当前工作区不是 Git 仓库", "初始化 Git 仓库后即可查看本会话的改动", nil)
							return
						}
						if data.FailureReason != "" {
							ui.Row(c.Key("diff-failure-row")).
								Padding(Space3, Space4).
								Radius(RadiusMD).
								Background(ThemeHover(c)).
								Children(func() {
									ui.Text(c, data.FailureReason).
										FontSize(TypeBody).
										TextColor(ThemeSecondaryText(c))
								})
							return
						}
						renderDiffEmptyState(c)
					})
				return
			}

			// 文件行：虚拟列表，只构建视口附近的若干行（见 diff_rows.go）
			rows := diffPanelRowsFor(dp, files)
			dp.rowsList.Key = func(i int) any {
				if i >= 0 && i < len(rows) {
					return rows[i].Key
				}
				return i
			}
			ui.List(c.Key("diff-rows"), &dp.rowsList, len(rows), func(i int) {
				renderDiffRow(c, s, dp, rows[i])
			}).
				Grow(1).
				MinHeight(0).
				FillWidth().
				Padding(Space4, Space4)

			if data.Truncated {
				ui.Row(c.Key("diff-truncated-banner")).
					Padding(Space3, Space4).
					Radius(RadiusSM).
					Background(ThemeWarning(c).Alpha(0.12)).
					Margin(0, Space4, Space3, Space4).
					Children(func() {
						ui.Text(c, "改动过多，已截断").
							FontSize(TypeFootnote).
							TextColor(ThemeWarning(c))
					})
			}
		})
}

// diffPanelViewState 把加载状态、错误文本与文件数量归约为面板的主视图状态。
// 错误文本里带 “not a git repository” 或 “不是 Git 仓库 / 非 Git” 时视为非 Git 工作区，
// 与真实的读取失败区分开，避免把“没有改动”和“无法读取改动”混为一谈。
func diffPanelViewState(loading bool, err string, fileCount int) string {
	if err != "" {
		lower := strings.ToLower(err)
		if strings.Contains(lower, "not a git repository") ||
			strings.Contains(err, "不是 Git 仓库") ||
			strings.Contains(err, "非 Git") {
			return "not-git"
		}
		return "error"
	}
	if fileCount == 0 {
		if loading {
			return "loading"
		}
		return "empty"
	}
	return "files"
}

// renderDiffEmptyState 渲染无改动文件时的图文空态卡片：文档搜索图标 + 标题 + 灰色副标题，
// 颜色与字号复用主题 token。
func renderDiffEmptyState(c *ui.Context) {
	renderDiffStatusState(c, "diff-empty-box", iconFileSearch, diffEmptyTitle, diffEmptySubtitle, nil)
}

// renderDiffStatusState 渲染居中的状态卡片：图标 + 标题 + 次要色副标题，
// onRetry 非 nil 时在下方追加“重试”按钮，复用标题栏刷新所用的 loadDiff。
// 加载、错误、非 Git 与空态共用同一套布局。
func renderDiffStatusState(c *ui.Context, key string, icon *ui.SVG, title, subtitle string, onRetry func()) {
	ui.Column(c.Key(key)).
		Center().
		Gap(Space3).
		Margin(ui.Auto).
		Children(func() {
			ui.Icon(c, icon).
				Size(diffStatusIconSize, diffStatusIconSize).
				TextColor(ThemeSecondaryText(c).Alpha(0.40))
			ui.Text(c, title).
				FontSize(TypeTitle).
				FontWeight(500).
				TextColor(ThemePrimaryText(c))
			if subtitle != "" {
				ui.Text(c, subtitle).
					FontSize(TypeFootnote).
					MaxWidth(diffStatusSubtitleMaxWidth).
					TextAlign(ui.Center).
					Wrap().
					TextColor(ThemeSecondaryText(c))
			}
			if onRetry != nil {
				Button(c, ButtonOpts{
					Key:     key + "-retry",
					Text:    "重试",
					Variant: ButtonSecondary,
					OnClick: onRetry,
				})
			}
		})
}

// renderDiffHeader 渲染顶部标题栏、轮次选择、刷新按钮和关闭按钮。
func renderDiffHeader(c *ui.Context, s *chatState, dp *diffPanelState, threadID, turnID string, data ThreadDiff) {
	ui.Row(c.Key("diff-panel-header")).
		FillWidth().
		Height(diffHeaderHeight).
		Padding(Space3, Space4).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			ui.Text(c, "改动").
				FontSize(TypeProse).
				FontWeight(600).
				TextColor(ThemePrimaryText(c))

			ui.Spacer(c)

			// 轮次选择
			renderDiffTurnSelector(c, s, dp, data, turnID)

			// 无边框刷新按钮
			refreshBtn := ui.ButtonBase(c.Key("diff-refresh-btn")).
				Label("刷新").
				Tooltip("刷新改动").
				Radius(RadiusSM).
				Padding(Space1, Space3)
			if refreshBtn.Hovered() {
				refreshBtn.Background(ThemeHover(c))
			}
			if refreshBtn.Clicked() {
				s.loadDiff(threadID, turnID)
			}
			refreshBtn.Children(func() {
				ui.Text(c, "刷新").
					FontSize(TypeFootnote).
					TextColor(ThemeSecondaryText(c))
			})

			// 关闭按钮
			closeBtn := ui.ButtonBase(c.Key("diff-close-btn")).
				Label("关闭").
				Tooltip("关闭改动 (⌘D)").
				Radius(RadiusSM).
				Padding(Space1, Space2)
			if closeBtn.Hovered() {
				closeBtn.Background(ThemeHover(c))
			}
			if closeBtn.Clicked() {
				s.toggleDiffPanel()
			}
			closeBtn.Children(func() {
				ui.Icon(c, IconX()).
					Size(IconSM, IconSM).
					TextColor(ThemeSecondaryText(c))
			})
		})
}

// renderDiffWorktreeNotice 在会话的 AI 在自建 worktree 里干活时，指明改动取自哪个目录。
// 整个会话的改动直接取自那个 worktree（见 loadDiff）；单轮改动仍来自会话记录的改动，不含该处。
func renderDiffWorktreeNotice(c *ui.Context, worktreePath, turnID string) {
	if worktreePath == "" {
		return
	}
	text := "AI 当前在 " + filepath.Base(worktreePath) + " 工作，以下改动取自该 worktree"
	if turnID != "" {
		text = "AI 当前在 " + filepath.Base(worktreePath) + " 工作，此轮改动不含该处"
	}
	ui.Row(c.Key("diff-worktree-notice")).
		FillWidth().
		Padding(0, Space4, Space3, Space4).
		Children(func() {
			ui.Text(c, text).
				FontSize(TypeCaption).
				TextColor(ThemeSecondaryText(c)).
				SingleLine().
				Tooltip(worktreePath)
		})
}

// renderDiffTurnSelector 渲染轮次选择 Popover。
func renderDiffTurnSelector(c *ui.Context, s *chatState, dp *diffPanelState, data ThreadDiff, currentTurnID string) {
	var changedTurns []DiffTurn
	for _, t := range data.Turns {
		if t.HasChanges {
			changedTurns = append(changedTurns, t)
		}
	}

	currentTitle := "整个会话"
	if currentTurnID != "" {
		for _, t := range changedTurns {
			if t.TurnID == currentTurnID {
				currentTitle = fmt.Sprintf("第 %d 轮", t.Index+1)
				break
			}
		}
	}

	trigger := ui.ButtonBase(c.Key("diff-turn-trigger")).
		Label(currentTitle).
		Tooltip("选择改动轮次").
		Padding(Space0, Space3).
		Radius(RadiusSM).
		Background(ThemeHover(c)).
		OnClick(func() {
			diffPanelMu.Lock()
			dp.turnMenuOpen = !dp.turnMenuOpen
			diffPanelMu.Unlock()
			s.invalidate()
		})
	trigger.Children(func() {
		ui.Row(c.Key("diff-turn-trigger-content")).
			Gap(Space1).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, currentTitle).
					FontSize(TypeFootnote).
					TextColor(ThemePrimaryText(c))
				ui.Icon(c, IconChevronDown()).
					Size(diffTurnChevronSize, diffTurnChevronSize).
					TextColor(ThemeSecondaryText(c))
			})
	})

	diffPanelMu.Lock()
	menuOpen := dp.turnMenuOpen
	diffPanelMu.Unlock()

	appPopover(c.Key("diff-turn-popover"), trigger, &menuOpen, func() {
		diffPanelMu.Lock()
		dp.turnMenuOpen = false
		diffPanelMu.Unlock()
	}, func() {
		popoverCard(c, ui.Column(c.Key("diff-turn-menu"))).
			Gap(Space0).
			Padding(Space1).
			MinWidth(diffTurnMenuMinWidth).
			Children(func() {
				// “整个会话”选项
				isAllSelected := (currentTurnID == "")
				allBtn := ui.ButtonBase(c.Key("diff-turn-opt-all")).
					Label("整个会话").
					Padding(Space1, Space3).
					Radius(RadiusXS).
					FillWidth()
				if isAllSelected {
					allBtn.Background(ThemeSelected(c))
				} else if allBtn.Hovered() {
					allBtn.Background(ThemeHover(c))
				}
				if allBtn.Clicked() {
					diffPanelMu.Lock()
					dp.turnMenuOpen = false
					diffPanelMu.Unlock()
					s.loadDiff(dp.threadID, "")
				}
				allBtn.Children(func() {
					ui.Text(c, "整个会话").
						FontSize(TypeFootnote).
						TextColor(ThemePrimaryText(c))
				})

				// 各轮次选项
				for _, t := range changedTurns {
					turnItem := t
					turnTitle := fmt.Sprintf("第 %d 轮", turnItem.Index+1)
					isSelected := (currentTurnID == turnItem.TurnID)

					optBtn := ui.ButtonBase(c.Key("diff-turn-opt-"+turnItem.TurnID)).
						Label(turnTitle).
						Padding(Space1, Space3).
						Radius(RadiusXS).
						FillWidth()
					if isSelected {
						optBtn.Background(ThemeSelected(c))
					} else if optBtn.Hovered() {
						optBtn.Background(ThemeHover(c))
					}
					if optBtn.Clicked() {
						diffPanelMu.Lock()
						dp.turnMenuOpen = false
						diffPanelMu.Unlock()
						s.loadDiff(dp.threadID, turnItem.TurnID)
					}
					optBtn.Children(func() {
						ui.Text(c, turnTitle).
							FontSize(TypeFootnote).
							TextColor(ThemePrimaryText(c))
					})
				}
			})
	})
}

// renderDiffStatusBadge 渲染文件状态小标签（新增/删除/修改/重命名）。
func renderDiffStatusBadge(c *ui.Context, status string) {
	label := "修改"
	bg := ThemeSecondaryText(c).Alpha(0.12)
	fg := ThemeSecondaryText(c)

	switch status {
	case "added":
		label = "新增"
		bg = ThemeSuccess(c).Alpha(0.12)
		fg = ThemeSuccess(c)
	case "deleted":
		label = "删除"
		bg = ThemeDanger(c).Alpha(0.12)
		fg = ThemeDanger(c)
	case "renamed":
		label = "重命名"
		bg = ThemeAccent(c).Alpha(0.12)
		fg = ThemeAccent(c)
	default: // "modified"
		label = "修改"
		bg = ThemeSecondaryText(c).Alpha(0.12)
		fg = ThemeSecondaryText(c)
	}

	ui.Row(c.Key("diff-badge-"+status)).
		Padding(Hairline, Space2).
		Radius(RadiusXS).
		Background(bg).
		Children(func() {
			ui.Text(c, label).
				FontSize(TypeCaption).
				FontWeight(500).
				TextColor(fg)
		})
}

// renderDiffFileList 渲染文件差异列表（供 Swarm 合并审查器与看板复用）。
// 与改动面板共用同一份行模型与行渲染，但调用方已经提供了滚动容器，
// 所以这里按普通列表逐个渲染行，不另起一层虚拟列表。
func renderDiffFileList(c *ui.Context, s *chatState, dp *diffPanelState, files []git.FileDiff) {
	if len(files) == 0 {
		return
	}
	rows := diffPanelRowsFor(dp, files)
	ui.Column(c.Key("diff-files-col")).
		FillWidth().
		Children(func() {
			for _, row := range rows {
				renderDiffRow(c, s, dp, row)
			}
		})
}

// expandDeferredDiffFile 在后台展开延迟解析的大文件补丁，把结果记到面板状态的缓存里，
// 下一帧就能渲染出真实的行内容；解析失败时保留占位行。
// 同一文件重复点开（或事件重放）只排队一次解析。
func expandDeferredDiffFile(s *chatState, dp *diffPanelState, filePath string, file git.FileDiff) {
	if s == nil || dp == nil || filePath == "" {
		return
	}

	diffPanelMu.Lock()
	if _, done := dp.expandedDeferred[filePath]; done {
		diffPanelMu.Unlock()
		return
	}
	if dp.expandingDeferred == nil {
		dp.expandingDeferred = make(map[string]bool)
	}
	if dp.expandingDeferred[filePath] {
		diffPanelMu.Unlock()
		return
	}
	dp.expandingDeferred[filePath] = true
	diffPanelMu.Unlock()

	go func() {
		expanded, err := git.ExpandDeferred(file)
		diffPanelMu.Lock()
		delete(dp.expandingDeferred, filePath)
		if err != nil {
			diffPanelMu.Unlock()
			return
		}
		if dp.expandedDeferred == nil {
			dp.expandedDeferred = make(map[string]git.FileDiff)
		}
		dp.expandedDeferred[filePath] = expanded
		// 用户是主动点开这个文件的：记进显式展开集合，
		// 免得展开后立刻又被「大文件默认折叠」收起来
		if dp.expandedLarge == nil {
			dp.expandedLarge = make(map[string]bool)
		}
		dp.expandedLarge[filePath] = true
		// 展开结果写回后重建行模型，下一帧就能看到真实的行内容
		dp.rowsRev++
		dp.rebuildDiffRowsLocked()
		diffPanelMu.Unlock()
		s.invalidate()
	}()
}
