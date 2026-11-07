package mcodeui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

var (
	sidebarMu     sync.Mutex
	sidebarSearch = make(map[*chatState]*string)
)

func getSearchQuery(s *chatState) *string {
	sidebarMu.Lock()
	defer sidebarMu.Unlock()
	q, ok := sidebarSearch[s]
	if !ok {
		val := ""
		q = &val
		sidebarSearch[s] = q
	}
	return q
}

// formatRelativeTime 将时间格式化为中文短格式。
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	diff := time.Since(t)
	if diff < time.Minute {
		return "刚刚"
	}
	if diff < time.Hour {
		return fmt.Sprintf("%d分钟前", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		return fmt.Sprintf("%d小时前", int(diff.Hours()))
	}
	days := int(diff.Hours() / 24)
	if days < 7 {
		return fmt.Sprintf("%d天前", days)
	}
	return t.Format("01-02")
}

// isThreadDormantOrArchived 判断会话是否处于休眠状态。
func isThreadDormantOrArchived(th Thread) bool {
	return th.Dormant
}

// 侧边栏活跃会话分页参数：默认展示 sidebarThreadPageInit 条，每次「加载更多」追加 sidebarThreadPageStep 条。
const (
	sidebarThreadPageInit = 8  // 每个项目默认展示的活跃会话条数
	sidebarThreadPageStep = 20 // 「加载更多」一次追加的条数
)

// visibleThreadWindow 按 limit 截取要展示的活跃会话，返回可见列表与被隐藏的条数。
// limit<=0 视为默认条数；unlimited（搜索中）或总数不超过 limit 时返回全部。
// selectedID 命中的会话若落在窗口之外，会追加到可见列表末尾，避免当前选中项从侧边栏消失。
func visibleThreadWindow(threads []Thread, limit int, selectedID string, unlimited bool) (visible []Thread, hidden int) {
	if limit <= 0 {
		limit = sidebarThreadPageInit
	}
	if unlimited || len(threads) <= limit {
		return threads, 0
	}
	window := threads[:limit]
	visible = make([]Thread, 0, limit+1)
	visible = append(visible, window...)

	selectedInWindow := false
	for _, th := range window {
		if th.ID == selectedID {
			selectedInWindow = true
			break
		}
	}
	if selectedID != "" && !selectedInWindow {
		for _, th := range threads[limit:] {
			if th.ID == selectedID {
				visible = append(visible, th)
				break
			}
		}
	}
	return visible, len(threads) - len(visible)
}

// threadOriginIcon 返回会话来源类型的指示图标：
// 普通会话返回 nil；分支会话返回 IconGitBranch()；转发会话返回 IconReply()。
func threadOriginIcon(th Thread) *ui.SVG {
	if th.ForkedFromThreadID == "" {
		return nil
	}
	if th.ForkedFromMessageID != "" {
		return IconGitBranch()
	}
	return IconReply()
}

// threadStillBusy 判断会话这一轮是否还没真正结束：主会话自己在跑、后端标记名下子线程
// 还在排队/运行（SwarmBusy），或仍有子代理（Swarm bee）在跑，三者任一为真都不算结束。
// 调用方不得持有 s.mu（内部会取锁读会话列表）。
func (s *chatState) threadStillBusy(th Thread) bool {
	if th.IsRunning || th.SwarmBusy {
		return true
	}
	return s.threadHasRunningBees(th.ID)
}

// hasThreadUnread 判断会话是否存在未读消息。
// 还在忙的会话一律不算未读：子任务/子代理跑完之前不给提示，避免一轮里反复亮灯；
// 全部结束后 HasUnread 仍为真，未读点会照常显示出来。
func (s *chatState) hasThreadUnread(th Thread) bool {
	if !th.HasUnread {
		return false
	}
	return !s.threadStillBusy(th)
}

// renderSidebar 渲染左侧边栏，实现 Liquid Glass 视觉。
var projectPathMissing = func(path string) bool {
	info, err := os.Stat(path)
	return err != nil || !info.IsDir()
}

func refreshProjectPathStatus(s *chatState) {
	s.mu.Lock()
	paths := make(map[string]string, len(s.projects))
	for _, p := range s.projects {
		paths[p.ID] = p.Path
	}
	active := s.windowActive.Load()
	changed := len(paths) != len(s.projectPathsChecked) || s.projectPathsChecked == nil
	if !changed {
		for id, path := range paths {
			if old, ok := s.projectPathsChecked[id]; !ok || old != path {
				changed = true
				break
			}
		}
	}
	refresh := changed || (active && !s.projectPathsWasActive)
	s.projectPathsWasActive = active
	if !refresh {
		s.mu.Unlock()
		return
	}
	s.projectPathsChecked = paths
	s.projectPathCheckSeq++
	seq := s.projectPathCheckSeq
	s.mu.Unlock()
	go func() {
		missing := make(map[string]bool, len(paths))
		for id, path := range paths {
			missing[id] = projectPathMissing(path)
		}
		s.mu.Lock()
		if s.projectPathCheckSeq != seq || s.closed {
			s.mu.Unlock()
			return
		}
		s.projectPathsMissing = missing
		s.mu.Unlock()
		s.invalidate()
	}()
}

func renderSidebar(c *ui.Context, s *chatState) {
	if win := s.currentWindow(); win != nil {
		s.mu.Lock()
		install := !s.projectPathFocusHook
		s.projectPathFocusHook = true
		s.mu.Unlock()
		if install {
			win.OnFocus(func() {
				s.mu.Lock()
				s.projectPathsChecked = nil
				s.mu.Unlock()
				refreshProjectPathStatus(s)
			})
		}
	}
	refreshProjectPathStatus(s)
	// 状态分组闩锁按帧工作：先清空本帧的悬停标记，侧栏渲染结束后再固化
	// 「指针是否还在列表内」，并在指针刚离开、还有会话待归位时再请求一帧完成归位。
	// defer 也覆盖搜索无结果等提前返回的分支。
	sidebarStateGroupLatchState.beginFrame()
	defer func() {
		if sidebarStateGroupLatchState.endFrame() {
			s.invalidate()
		}
	}()

	// 侧栏每帧要对每个会话多次判定忙碌，逐次全表扫描在几千个会话时是 O(N²)，会卡死主线程，
	// 所以每帧只算一次，本帧内的所有判定复用同一份结果。
	s.beeBusyFrame = s.computeRunningBeeParents()
	defer func() { s.beeBusyFrame = nil }()

	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)
	hoverBg := ThemeHover(c)

	s.mu.Lock()
	projects := make([]Project, len(s.projects))
	copy(projects, s.projects)
	missingPaths := make(map[string]bool, len(s.projectPathsMissing))
	for id, missing := range s.projectPathsMissing {
		missingPaths[id] = missing
	}

	providers := make([]ProviderInfo, len(s.providers))
	copy(providers, s.providers)

	threads := make([]Thread, len(s.threads))
	copy(threads, s.threads)

	threadsByProj := make(map[string][]Thread, len(s.threadsByProj))
	for pid, list := range s.threadsByProj {
		cp := make([]Thread, len(list))
		copy(cp, list)
		threadsByProj[pid] = cp
	}

	collapsed := make(map[string]bool, len(s.collapsedProj))
	for pid, col := range s.collapsedProj {
		collapsed[pid] = col
	}
	selectedID := s.selectedThreadID

	if s.newThreadProvider == "" {
		for _, p := range s.providers {
			if p.Available {
				s.newThreadProvider = p.ID
				break
			}
		}
		if s.newThreadProvider == "" && len(s.providers) > 0 {
			s.newThreadProvider = s.providers[0].ID
		}
	}
	s.mu.Unlock()

	// 总管已经在侧栏顶部单独成段：它的会话与所在项目不再在项目树、动态视图里重复出现。
	// 话题会话也只在总管段里列出，项目树与动态视图同样不再重复显示它们（项目本身保留）。
	// 这里只过滤本地副本，不改 s.projects / s.threads 本身。
	s.refreshStewardTopics()
	s.ensureStewardTopicsWatch()
	stewardThread := s.stewardThreadID()
	topicThreads := s.stewardTopicThreadIDs()
	if stewardThread != "" || len(topicThreads) > 0 {
		projects, threads, threadsByProj = withoutStewardThread(stewardThread, topicThreads, projects, threads, threadsByProj)
	}

	// 按保存的项目顺序渲染，未在顺序中的项目按原顺序追加到末尾。
	projects = applyProjectOrder(projects, sidebarProjectOrder(s))
	// 项目按“内部会话的最后用户消息时间”倒序：手拖顺序只作为同活跃时间与空项目的稳定兜底，
	// 不再主导项目的前后位置。
	projects = SortProjectsByActivity(projects, threadsByProj)
	projectNames := make(map[string]string, len(projects))
	for _, p := range projects {
		projectNames[p.ID] = p.Name
	}

	searchQueryPtr := getSearchQuery(s)
	searchQuery := ""
	if searchQueryPtr != nil {
		searchQuery = strings.TrimSpace(*searchQueryPtr)
	}
	lowerQuery := strings.ToLower(searchQuery)

	activityView := s.activityViewEnabled()
	matchesByProject := make(map[string][]Thread)
	var matchingThreads []Thread
	if lowerQuery != "" {
		for _, p := range projects {
			var active, dormant []Thread
			projectMatches := strings.Contains(strings.ToLower(p.Name), lowerQuery)
			for _, th := range threadsByProj[p.ID] {
				// 动态视图不显示子代理，这里同样跳过它们：否则方向键会落在看不见的行上。
				// 休眠会话也不在动态视图中显示。
				if activityView && (isActivityHiddenHelper(th) || th.Dormant) {
					continue
				}
				if projectMatches || strings.Contains(strings.ToLower(th.Title), lowerQuery) {
					if th.Dormant {
						dormant = append(dormant, th)
					} else {
						active = append(active, th)
					}
				}
			}
			sort.SliceStable(dormant, func(i, j int) bool {
				return threadActivityTime(dormant[i]).After(threadActivityTime(dormant[j]))
			})
			if projectMatches || len(active)+len(dormant) > 0 {
				matchesByProject[p.ID] = append(active, dormant...)
				matchingThreads = append(matchingThreads, matchesByProject[p.ID]...)
			}
		}
		if activityView {
			groups := groupThreadsByActivity(matchingThreads, time.Now())
			matchingThreads = nil
			for _, group := range groups {
				matchingThreads = append(matchingThreads, group.Threads...)
			}
		}
	}

	// 状态分组：闩锁的 split 每次都会整体重建归属表，逐项目调用会互相覆盖，
	// 所以整帧先对全部活跃会话切分一次，项目循环里再按 ID 取用。
	var stateGroups map[string]sidebarStateGroup
	if lowerQuery == "" {
		var frameActive []Thread
		for _, p := range projects {
			for _, th := range threadsByProj[p.ID] {
				if !th.Dormant {
					frameActive = append(frameActive, th)
				}
			}
		}
		needsYouAll, runningAll, _ := sidebarStateGroupLatchState.split(frameActive, func(th Thread) bool {
			return s.threadStillBusy(th)
		})
		stateGroups = make(map[string]sidebarStateGroup, len(frameActive))
		for _, th := range needsYouAll {
			stateGroups[th.ID] = sidebarStateGroupNeedsYou
		}
		for _, th := range runningAll {
			stateGroups[th.ID] = sidebarStateGroupRunning
		}
	} else {
		// 搜索态平铺、不分组：清空归属表，退出搜索后不沿用搜索前的旧分组。
		sidebarStateGroupLatchState.split(nil, nil)
	}

	ui.Column(c.Key("sidebar-column")).Fill().Background(ThemeSidebarBg(c)).Children(func() {
		// 1. 顶部避让系统窗口按钮：macOS 是交通灯（30pt 占位），Windows 的按钮在右侧，占位为 0。
		ui.Box(c.Key("traffic-light-pad")).Height(titleBarTopOffset(c)).DragWindow()

		// 2. 顶部工具栏：搜索输入框占满剩余宽度，右侧是动态视图开关。
		// 占位为 0 时（Windows）这一行就是窗口顶部，行内空白处（非输入框、按钮）仍能拖动窗口。
		ui.Row(c.Key("sidebar-top-bar")).
			DragWindow().
			Padding(Space1, ListInset, Space1, ListInset).
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				searchFocused := false
				s.mu.Lock()
				if s.searchFocusInput {
					searchFocused = true
					s.searchFocusInput = false
				}
				if len(matchingThreads) == 0 {
					s.searchHighlightIndex = 0
				} else if s.searchHighlightIndex >= len(matchingThreads) {
					s.searchHighlightIndex = len(matchingThreads) - 1
				} else if s.searchHighlightIndex < 0 {
					s.searchHighlightIndex = 0
				}
				s.mu.Unlock()

				// 搜索输入框（根据标题即时过滤，上下键移动高亮，回车打开会话）
				SearchField(c.Key("sidebar-search-field"), TextFieldProps{
					Value:       searchQueryPtr,
					Placeholder: "搜索对话...",
					Label:       "搜索对话...",
					Clearable:   true,
					Focused:     searchFocused,
					OnChange: func(val string) {
						s.mu.Lock()
						s.searchHighlightIndex = 0
						s.mu.Unlock()
						s.invalidate()
					},
					// 上下键移动高亮，回车打开高亮会话。
					OnKeyDown: func(k ui.TextKey) bool {
						return s.handleSearchKey(k, matchingThreads)
					},
				}).Grow(1)

				// 动态视图开关：铃铛图标按钮（开启时用强调色高亮底），与 ⌥⌘U 共用入口
				activityOn := s.activityViewEnabled()
				activityTip := "开启动态视图 (⌥⌘U)"
				if activityOn {
					activityTip = "关闭动态视图 (⌥⌘U)"
				}
				IconButton(c.Key("sidebar-activity-btn"), IconButtonProps{
					Icon:    IconBell(),
					Size:    ControlMD,
					Variant: ButtonGhost,
					Label:   activityTip,
					Active:  activityOn,
					OnClick: func() {
						s.toggleActivityView()
					},
				})
			})

		// 总管段：头像、「昵称 · 总管」与待处理数徽标合成一行；
		// 客户端不支持总管或会话还没建好时退回只渲染待处理行。
		renderSidebarStewardSection(c, s)

		// 3. 项目与会话列表
		ui.Scroll(c.Key("sidebar-projects-list")).Grow(1).Children(func() {
			isCompact := false
			if st := getChatSettings(s); st != nil {
				isCompact = st.cfg.Appearance.SidebarCompact
			}

			// 行高与左右内边距跟「界面密度」走；行高再按「文字大小」缩放：
			// 行内标题按比例变大，行高不跟上就会把文字截断；内边距与间距是间距，不缩放。
			dens := currentDensity(c, s)
			rowScale := textScaleFactor()
			var threadRowHeight float32 = dens.RowH * rowScale
			var threadPadY float32 = 4
			var threadPadX float32 = dens.PadX
			var folderIndent float32 = FolderIndent
			var projectGap float32 = GroupGap

			// 「紧凑侧边栏」是侧栏自己的开关，再压到紧凑档：更矮的行、更小的缩进与间距。
			if isCompact {
				threadRowHeight = DensityCompact.RowH * rowScale
				threadPadY = Space1 / 2
				threadPadX = DensityCompact.PadX
				folderIndent = 10
				projectGap = Space2
			}

			// 搜索词非空且项目与会话全部未命中时，列表区居中显示「无结果」卡片。
			if lowerQuery != "" && len(matchesByProject) == 0 {
				renderSidebarNoResultsCard(c)
				return
			}

			// 动态视图：不渲染项目树，按最后活跃时间分组平铺全部会话；搜索词非空时按标题过滤。
			if activityView {
				if lowerQuery != "" {
					for gi, group := range groupThreadsByActivity(matchingThreads, time.Now()) {
						renderActivityGroupHeader(c, group.Title, gi == 0, threadPadX)
						for _, th := range group.Threads {
							renderSidebarThreadRow(c, s, sidebarThreadRowOptions{
								projectID: th.ProjectID, thread: th, selected: th.ID == selectedID,
								highlighted: s.searchHighlightIndex >= 0 && s.searchHighlightIndex < len(matchingThreads) && matchingThreads[s.searchHighlightIndex].ID == th.ID,
								rowHeight:   threadRowHeight, padY: threadPadY, padX: threadPadX,
								compact: isCompact, providers: providers, projectName: projectNames[th.ProjectID],
							})
						}
					}
					return
				}
				renderActivityThreadList(c, s, sidebarActivityListOptions{
					threads:         threads,
					projectNames:    projectNames,
					providers:       providers,
					selectedID:      selectedID,
					matchingThreads: matchingThreads,
					highlightIndex:  s.searchHighlightIndex,
					query:           lowerQuery,
					rowHeight:       threadRowHeight,
					padY:            threadPadY,
					padX:            threadPadX,
					compact:         isCompact,
				})
				return
			}

			if len(projects) == 0 {
				ui.Text(c, "暂无项目").
					TextColor(secondaryText).
					FontSize(scaledFont(TypeFootnote)).
					Padding(Space5, Space5)
				return
			}

			for idx, p := range projects {
				pVal := p
				isCollapsed := collapsed[pVal.ID] && lowerQuery == ""
				allThs := threadsByProj[pVal.ID]
				ths := allThs
				if lowerQuery != "" {
					var ok bool
					ths, ok = matchesByProject[pVal.ID]
					if !ok {
						continue
					}
				}

				// 项目标题行：文件夹图标 + 名称（13 号 medium），右侧是会话数与“+”图标按钮
				headerRow := ui.Row(c.Key("proj-header-"+pVal.ID)).
					Height(threadRowHeight).
					Padding(0, threadPadX).
					Margin(1, ListInset, 1, ListInset).
					Radius(RowCornerRadius).
					AlignItems(ui.Center)

				if headerRow.Hovered() {
					headerRow.Background(ThemeOverlayHover(c))
				}

				headerRow.ContextMenu(func(m *ui.Menu) {
					if m.Item("新建会话").Chosen() {
						s.closeWorkspace()
						s.createThread(pVal.ID)
					}
					if m.Item("在 Worktree 中新建会话").Chosen() {
						s.closeWorkspace()
						s.createThreadInWorktree(pVal.ID)
					}
					if m.Item("Worktree 管理…").Chosen() {
						s.openWorktreeManage(pVal.ID, pVal.Path)
					}
					// 交给总管：把这件事直接开成总管的一个话题，项目已经带好。
					if _, ok := s.stewardTopicsClient(); ok {
						nickname, _ := s.stewardIdentity()
						if m.Item("交给" + stewardName(nickname)).Chosen() {
							s.openStewardStartTopicDialog(pVal.ID, pVal.Name)
						}
					}
					// 外部应用唤起：默认编辑器取打开方式列表首位可用应用，终端读 General.TerminalShell。
					if m.Item("在默认编辑器中打开").Chosen() {
						openInDefaultEditor(s, pVal.Path)
					}
					if m.Item("在终端中打开").Chosen() {
						if err := openDirInExternalTerminal(pVal.Path, getTerminalConfiguredShell(s)); err != nil {
							s.setToast("打开终端失败: " + err.Error())
						}
					}
					if m.Item("在访达中显示").Chosen() {
						if pVal.Path != "" {
							mygo.Shell.ShowItemInFolder(pVal.Path)
						}
					}
					if m.Item("复制路径").Chosen() {
						c.WriteClipboard(pVal.Path)
						s.setToast("已复制路径")
					}

					// 步骤 1：整理会话…
					if m.Item("整理会话…").Chosen() {
						s.mu.Lock()
						s.bulkPopoverProjectID = pVal.ID
						s.bulkRangeDays = 7
						s.bulkIncludePinned = false
						s.bulkIncludeDormant = false
						s.mu.Unlock()
						s.invalidate()
					}

					// 收集该项目内未读会话与已休眠未归档会话。
					// 这里按原始 HasUnread 收集：仍在跑的会话界面上虽不显示未读点
					// （见 hasThreadUnread），用户主动点「全部标记为已读」时也应一并清掉，
					// 否则忙完后又冒出一个没人点过的未读点。
					var unreadIDs []string
					var dormantIDs []string
					for _, th := range allThs {
						if th.HasUnread {
							unreadIDs = append(unreadIDs, th.ID)
						}
						if th.Dormant && !th.IsRunning && !th.SwarmBusy {
							dormantIDs = append(dormantIDs, th.ID)
						}
					}

					if len(unreadIDs) > 0 {
						if m.Item("全部标记为已读").Chosen() {
							s.bulkMarkRead(pVal.ID, unreadIDs)
						}
					}

					if len(dormantIDs) > 0 {
						archiveLabel := fmt.Sprintf("归档休眠会话（%d）", len(dormantIDs))
						if m.Item(archiveLabel).Chosen() {
							s.bulkArchive(pVal.ID, dormantIDs)
						}
					}

					// 步骤 3：项目右键菜单在 lastBulk 有效期内追加撤销菜单项
					s.mu.Lock()
					lb := s.lastBulk
					s.mu.Unlock()
					if lb != nil && lb.projectID == pVal.ID && time.Since(lb.at) <= 10*time.Second {
						var undoTitle string
						switch lb.kind {
						case "archive":
							undoTitle = fmt.Sprintf("撤销归档 %d 个会话", lb.count)
						case "dormant":
							undoTitle = fmt.Sprintf("撤销休眠 %d 个会话", lb.count)
						default:
							undoTitle = fmt.Sprintf("撤销标为已读 %d 个会话", lb.count)
						}
						if m.Item(undoTitle).Chosen() {
							s.undoLastBulk()
						}
					}

					m.Separator()
					if m.Item("从侧边栏移除…").Chosen() {
						// E-01：删除前确认关闭时跳过确认框，直接移除。
						if confirmBeforeDeleting(s) {
							s.openRemoveProjectModal(pVal.ID, pVal.Name)
						} else {
							s.removeProjectNow(pVal.ID)
						}
					}

					// 项目激活状态切换；有覆盖时提供恢复全局默认的入口。
					m.Separator()
					if isProjectActive(s, pVal.ID) {
						if m.Item("停用项目").Chosen() {
							setProjectActive(s, pVal.ID, false)
						}
					} else {
						if m.Item("激活项目").Chosen() {
							setProjectActive(s, pVal.ID, true)
						}
					}
					if hasProjectActivationOverride(s, pVal.ID) {
						if m.Item("遵循全局设置").Chosen() {
							clearProjectActivationOverride(s, pVal.ID)
						}
					}
				})

				// 挂载整理浮窗 Popover（锚定在 headerRow）；弹窗被点外部/按 Esc 关闭时一并清掉项目标记。
				bulkPopoverOpen := s.bulkPopoverProjectID == pVal.ID
				appPopover(c.Key("bulk-popover-"+pVal.ID), headerRow, &bulkPopoverOpen, func() {
					s.mu.Lock()
					if s.bulkPopoverProjectID == pVal.ID {
						s.bulkPopoverProjectID = ""
					}
					s.mu.Unlock()
				}, func() {
					renderBulkPopover(c, s, pVal.ID)
				})
				if bulkPopoverOpen != (s.bulkPopoverProjectID == pVal.ID) {
					s.mu.Lock()
					if s.bulkPopoverProjectID == pVal.ID {
						s.bulkPopoverProjectID = ""
					}
					s.mu.Unlock()
				}

				// 按住项目行拖动可改变顺序，松手落到目标项目行时重排并持久化。
				// 框架的 Drag/Drop 会在指针移动后接管按压，拖动不产生点击。
				if lowerQuery == "" {
					headerRow.Drag(pVal.ID)
					if droppedID, dropped := ui.Drop[string](headerRow); dropped {
						if reorderProjectsByDrop(s, projects, droppedID, idx) {
							s.invalidate()
						}
					}
				}

				headerRow.Children(func() {
					projToggleLabel := "展开项目"
					if !isCollapsed {
						projToggleLabel = "收起项目"
					}
					if projName := pVal.Name; projName != "" {
						projToggleLabel += " " + projName
					}
					titleArea := ui.Row(c.Key("proj-toggle-" + pVal.ID)).
						AlignItems(ui.Center).
						Grow(1).
						Gap(Space3).
						Radius(RadiusXS).
						Label(projToggleLabel)

					// 标题区悬停显示项目绝对路径
					if missingPaths[pVal.ID] {
						titleArea.Tooltip("路径失效 · " + pVal.Path)
					} else if pVal.Path != "" {
						titleArea.Tooltip(pVal.Path)
					}

					if titleArea.Clicked() && lowerQuery == "" {
						s.mu.Lock()
						if s.collapsedProj == nil {
							s.collapsedProj = make(map[string]bool)
						}
						s.collapsedProj[pVal.ID] = !isCollapsed
						s.mu.Unlock()
						s.invalidate()
					}

					titleArea.Children(func() {
						// 停用的项目用半透明次要色弱化文件夹图标与名称；会话仍正常列出。
						projectActive := isProjectActive(s, pVal.ID)
						iconColor := secondaryText
						nameColor := primaryText
						if missingPaths[pVal.ID] {
							nameColor = secondaryText
						}
						if !projectActive {
							iconColor = secondaryText.Alpha(0.5)
							nameColor = secondaryText.Alpha(0.5)
						}

						// 文件夹图标
						folderIcon := IconFolderOpen()
						if isCollapsed {
							folderIcon = IconFolder()
						}
						ui.Icon(c, folderIcon).
							Size(12, 12).
							TextColor(iconColor)

						name := pVal.Name
						if name == "" {
							name = "未命名项目"
						}
						ui.Text(c, name).
							FontSize(scaledFont(FontSizeThreadTitle)).
							FontWeight(500).
							TextColor(nameColor).
							Grow(1).
							SingleLine()
					})

					// 右侧：会话计数（次要色、小号） + 新建会话 "+" 图标按钮（始终可点）
					if len(ths) > 0 {
						ui.Text(c, "("+strconv.Itoa(len(ths))+")").
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemeTertiaryText(c)).
							Margin(0, Space1, 0, 0)
					}

					IconButton(c.Key("proj-add-thread-"+pVal.ID), IconButtonProps{
						Icon:    IconPlus(),
						Size:    ControlMD,
						Variant: ButtonGhost,
						Label:   "在此项目中新建会话",
						OnClick: func() {
							s.mu.Lock()
							if s.newThreadProvider == "" {
								for _, p := range s.providers {
									if p.Available {
										s.newThreadProvider = p.ID
										break
									}
								}
								if s.newThreadProvider == "" && len(s.providers) > 0 {
									s.newThreadProvider = s.providers[0].ID
								}
							}
							s.mu.Unlock()
							s.closeWorkspace()
							s.createThread(pVal.ID)
						},
					})
				})

				// 展开状态下展示该项目的会话列表与已休眠折叠组
				if !isCollapsed {
					// 步骤 2: 在 renderSidebar 构建会话列表时，把 IsHelper 为真且 ParentThreadID 指向当前列表中某个会话的线程从顶层移除，按 parent 分组；找不到父会话时仍按普通会话显示。
					topThs, beesByParent := GroupThreadsByParent(ths)
					if lowerQuery != "" {
						topThs, beesByParent = ths, nil
					}

					// 对当前选中的 Lead 会话，如果其 SwarmState.Bees 中有 bee 的 ThreadID 还不在会话列表中，
					// 就直接用 SwarmState 里的 Name、ThreadID、Status 在该父行下渲染头像，这样派出瞬间就能显示。
					if s != nil && lowerQuery == "" {
						hp := getSwarmPanelState(s)
						if hp != nil {
							swarmPanelMu.Lock()
							st := hp.state
							swarmPanelMu.Unlock()
							leadID := st.LeadThreadID
							if leadID == "" {
								leadID = selectedID
							}
							if leadID != "" && len(st.Bees) > 0 {
								beesByParent[leadID] = MergeSwarmStateBees(beesByParent[leadID], st.Bees, leadID, pVal.ID)
								beeIDSet := make(map[string]bool, len(st.Bees))
								for _, h := range st.Bees {
									beeIDSet[h.ThreadID] = true
								}
								cleanTop := make([]Thread, 0, len(topThs))
								for _, th := range topThs {
									if !beeIDSet[th.ID] {
										cleanTop = append(cleanTop, th)
									}
								}
								topThs = cleanTop
							}
						}
					}

					// 设置页「侧边栏自动隐藏空闲子代理」打开时，统一过滤所有 lead 下已结束的子代理，
					// 只保留运行中、等待回答的；当前选中的子代理始终保留，免得正在看的头像消失。
					if cs := getChatSettings(s); cs != nil && cs.cfg.Orchestration.AutoHidesIdleBees {
						for pID, list := range beesByParent {
							beesByParent[pID] = FilterIdleSwarmBees(list, selectedID)
						}
					}

					var activeThs []Thread
					var dormantThs []Thread
					for _, th := range topThs {
						if th.Dormant {
							dormantThs = append(dormantThs, th)
						} else {
							activeThs = append(activeThs, th)
						}
					}

					// 已休眠会话按活跃时间（LastUserMessageAt，零值回退 UpdatedAt）倒序排列，与 SortThreads 一致
					sort.SliceStable(dormantThs, func(i, j int) bool {
						return threadActivityTime(dormantThs[i]).After(threadActivityTime(dormantThs[j]))
					})

					renderThreadItem := func(th Thread, isDormant bool) {
						// 键盘高亮匹配判定
						isHighlighted := (searchQuery != "" && len(matchingThreads) > 0 &&
							s.searchHighlightIndex >= 0 && s.searchHighlightIndex < len(matchingThreads) &&
							matchingThreads[s.searchHighlightIndex].ID == th.ID)
						renderSidebarThreadRow(c, s, sidebarThreadRowOptions{
							projectID:   pVal.ID,
							thread:      th,
							bees:        beesByParent[th.ID],
							dormant:     isDormant,
							selected:    th.ID == selectedID,
							highlighted: isHighlighted,
							rowHeight:   threadRowHeight,
							padY:        threadPadY,
							padX:        threadPadX,
							indent:      folderIndent,
							compact:     isCompact,
							providers:   providers,
							latchHover:  true,
						})
					}

					if len(activeThs) == 0 && len(dormantThs) == 0 {
						emptyTip := "暂无会话"
						if lowerQuery != "" && !strings.Contains(strings.ToLower(pVal.Name), lowerQuery) {
							emptyTip = "无匹配会话"
						}
						ui.Text(c, emptyTip).
							TextColor(secondaryText).
							FontSize(scaledFont(TypeFootnote)).
							Padding(Space1, Space8)
					} else {
						// 状态分组：等你处理、运行中全量置顶；其余走下面的分页窗口。
						// 搜索态保持平铺，不分组也不锁旧归属。
						restThs := activeThs
						var needsYouThs, runningThs []Thread
						if lowerQuery == "" {
							needsYouThs = make([]Thread, 0, len(activeThs))
							runningThs = make([]Thread, 0, len(activeThs))
							restThs = make([]Thread, 0, len(activeThs))
							for _, th := range activeThs {
								switch stateGroups[th.ID] {
								case sidebarStateGroupNeedsYou:
									needsYouThs = append(needsYouThs, th)
								case sidebarStateGroupRunning:
									runningThs = append(runningThs, th)
								default:
									restThs = append(restThs, th)
								}
							}
						}
						// 组头文字与会话标题左对齐：会话行是「行内边距 + 文件夹缩进 + 状态图标 14 + Gap 8」。
						groupHeaderIndent := threadPadX + folderIndent + sidebarThreadTitleOffset
						if len(needsYouThs) > 0 {
							renderSidebarStateGroupHeader(c, pVal.ID, pVal.Name, "等你处理", len(needsYouThs), groupHeaderIndent)
							for _, th := range needsYouThs {
								renderThreadItem(th, false)
							}
						}
						if len(runningThs) > 0 {
							renderSidebarStateGroupHeader(c, pVal.ID, pVal.Name, "运行中", len(runningThs), groupHeaderIndent)
							for _, th := range runningThs {
								renderThreadItem(th, false)
							}
						}

						// 1. 其余活跃会话：按项目自己的展示窗口截取，超过默认条数的部分折叠
						s.mu.Lock()
						threadLimit := s.visibleThreadsProj[pVal.ID]
						s.mu.Unlock()
						if threadLimit <= 0 {
							threadLimit = sidebarThreadPageInit
						}
						visibleThs, hiddenThs := visibleThreadWindow(restThs, threadLimit, selectedID, lowerQuery != "")
						for _, th := range visibleThs {
							renderThreadItem(th, false)
						}

						// 搜索时不分页（结果本身就是筛选后的全集），只有默认状态下超过一页才给操作栏；
						// 分页只覆盖其余会话，两个状态组始终全量显示。
						if lowerQuery == "" && len(restThs) > sidebarThreadPageInit {
							// 分页栏文字左边缘要与会话标题文字对齐：会话行是「状态图标 14 + Gap 8」之后再放标题
							// （见 renderSidebarThreadRow），这里把同样的偏移追加到左内边距上。
							limitBar := ui.Row(c.Key("thread-limit-"+pVal.ID)).
								Height(threadRowHeight).
								Padding(threadPadY, threadPadX+sidebarThreadTitleOffset).
								Margin(1, ListInset, 1, ListInset+folderIndent).
								Gap(Space4).
								AlignItems(ui.Center).
								Radius(RowCornerRadius)

							limitBar.Children(func() {
								// 次级纯文字链（synara / droppy 风格）：无底色，悬停只把文字提亮到主文字色
								barButton := func(key, label string, onClick func()) {
									btn := ui.ButtonBase(c.Key(key)).
										Padding(Space0, 0).
										Label(label)
									if btn.Clicked() {
										onClick()
									}
									textColor := secondaryText
									if btn.Hovered() {
										textColor = primaryText
									}
									btn.Children(func() {
										ui.Text(c, label).
											FontSize(scaledFont(FontSizeDormantTitle)).
											TextColor(textColor).
											SingleLine()
									})
								}

								// 还有没展示的会话：只给一个「加载更多」
								if hiddenThs > 0 {
									barButton("thread-more-"+pVal.ID, "加载更多", func() {
										s.mu.Lock()
										if s.visibleThreadsProj == nil {
											s.visibleThreadsProj = make(map[string]int)
										}
										s.visibleThreadsProj[pVal.ID] = threadLimit + sidebarThreadPageStep
										s.mu.Unlock()
										s.invalidate()
									})
								}

								// 已展开过（展示条数超过默认值）时在「加载更多」右侧提供「收起」，全部显示完只剩它
								if threadLimit > sidebarThreadPageInit {
									barButton("thread-fold-"+pVal.ID, "收起", func() {
										s.mu.Lock()
										delete(s.visibleThreadsProj, pVal.ID)
										s.mu.Unlock()
										s.invalidate()
									})
								}
							})
						}

						// 2. 已休眠会话折叠分组「已休眠会话（N）」
						if len(dormantThs) > 0 {
							s.mu.Lock()
							isDormantOpen := s.expandedDormantProj[pVal.ID] || lowerQuery != ""
							s.mu.Unlock()

							dormantGroupLabel := "展开已休眠会话"
							if isDormantOpen {
								dormantGroupLabel = "收起已休眠会话"
							}
							dormantGroupRow := ui.Row(c.Key("dormant-group-"+pVal.ID)).
								Height(threadRowHeight).
								Padding(threadPadY, threadPadX).
								Margin(1, ListInset, 1, ListInset+folderIndent).
								Gap(Space2).
								AlignItems(ui.Center).
								Radius(RowCornerRadius).
								Label(dormantGroupLabel)

							if dormantGroupRow.Hovered() {
								dormantGroupRow.Background(hoverBg)
							}

							if dormantGroupRow.Clicked() && lowerQuery == "" {
								s.mu.Lock()
								if s.expandedDormantProj == nil {
									s.expandedDormantProj = make(map[string]bool)
								}
								s.expandedDormantProj[pVal.ID] = !isDormantOpen
								s.mu.Unlock()
								s.invalidate()
							}

							dormantGroupRow.Children(func() {
								chevronIcon := IconChevronRight()
								if isDormantOpen {
									chevronIcon = IconChevronDown()
								}
								ui.Icon(c, chevronIcon).
									Size(10, 10).
									TextColor(secondaryText)

								ui.Text(c, fmt.Sprintf("已休眠会话（%d）", len(dormantThs))).
									FontSize(scaledFont(FontSizeDormantTitle)).
									TextColor(secondaryText).
									Grow(1).
									SingleLine()
							})

							if isDormantOpen {
								for _, th := range dormantThs {
									renderThreadItem(th, true)
								}
							}
						}
					}
				}

				// 项目行之间的间距（紧凑模式 6，常规模式 10）
				ui.Box(c.Key("proj-gap-" + pVal.ID)).
					Height(projectGap)
			}
		})

		// 撤销提示条（在列表下方、底部常驻工具栏上方）
		renderBulkUndoNotice(c, s)

		// 4. 底部常驻工具栏：一排无边框图标按钮（添加项目、看板、设置齿轮、更多）
		renderSidebarBottomBar(c, s)
	})
}

// sidebarThreadRowOptions 是会话行渲染的参数，项目树与动态视图两种模式共用。
type sidebarThreadRowOptions struct {
	projectID   string
	thread      Thread
	bees        []Thread // 所属 Swarm bee 会话列表（若无则为空切片）
	dormant     bool     // 已休眠会话：标题使用次要灰度色与更小字号
	selected    bool
	highlighted bool // 搜索键盘高亮命中
	rowHeight   float32
	padY        float32
	padX        float32
	indent      float32 // 行左缩进：项目树用文件夹缩进，动态视图为 0
	compact     bool
	providers   []ProviderInfo
	latchHover  bool   // 项目树：把行悬停写进状态分组闩锁（动态视图不传，保持按帧实时分组）
	projectName string // 动态视图：行内展示的所属项目名（弱化色小字），空则不展示
}

// renderSidebarThreadRow 渲染一条会话行：状态前缀、标题、模型徽章与状态点，右侧是相对时间
// （悬停时改为「更多操作」按钮），并挂载右键菜单与点击选中行为。
// 项目树与动态视图共用同一份渲染，避免两处样式漂移。
func renderSidebarThreadRow(c *ui.Context, s *chatState, o sidebarThreadRowOptions) {
	primaryText := ThemePrimaryText(c)
	secondaryText := ThemeSecondaryText(c)
	accent := ThemeAccent(c)
	hoverBg := ThemeHover(c)

	thVal := o.thread
	isSelected := o.selected
	// 会话的 AI 自己在外面建了 worktree 时带一个标识；识别在后台进行，结果按会话缓存。
	aiWorktree := s.aiWorktreePath(thVal.ID)

	// 会话行：根据紧凑模式自适应高度、缩进与内边距；选中时浅底，悬停或搜索高亮时 6% 遮罩
	rowLabel := "打开会话"
	if thVal.Title != "" {
		rowLabel = "打开会话 " + thVal.Title
	}
	row := ui.Row(c.Key("th-"+o.projectID+"-"+thVal.ID)).
		MinHeight(o.rowHeight).
		Padding(o.padY, o.padX).
		Margin(1, ListInset, 1, ListInset+o.indent).
		Gap(Space3).
		AlignItems(ui.Center).
		Radius(RowCornerRadius).
		Label(rowLabel)

	// 行悬停状态：驱动浅底遮罩与右侧操作按钮的显隐
	rowHovered := row.Hovered()
	if rowHovered && o.latchHover {
		// 项目树：记录「指针还在列表内」，让悬停期间的状态变化延迟到指针离开后再归组。
		sidebarStateGroupLatchState.markRowHovered()
	}
	// 悬停一段时间后在行尾浮出只读信息卡（标题、项目、分支、状态等）。
	renderSidebarHoverCard(c, s, row, rowHovered, o, thVal)
	// 省略号菜单打开时会拿到焦点，而 mygo 只有在下一帧仍构建菜单按钮时才会报告选中项；
	// 因此指针离开后只要焦点还在行内，仍需保留右侧操作按钮。
	showRowActions := rowHovered || row.FocusWithin()

	if isSelected {
		row.Background(ThemeSelected(c))
	} else if o.highlighted || rowHovered {
		row.Background(hoverBg)
	}

	row.ContextMenu(threadContextMenu(c, s, o.projectID, thVal))

	if row.Clicked() {
		s.closeWorkspace()
		s.selectThread(thVal.ID)
	}

	row.Children(func() {
		// 状态前缀：本会话或它派出的子代理仍在运行时显示点阵动态图标；
		// 这一轮跑完但用户还没打开看时，在供应商图标右上角叠一个小圆点。
		// 仍在忙（主会话运行中、子线程/子代理未结束）时既不换标题色，也不亮完成角标与未读点。
		busy := s.threadStillBusy(thVal)
		turnDone := ""
		if busy {
			renderRunningGlyph(c, "th-run-"+o.projectID+"-"+thVal.ID, 14, accent)
		} else {
			turnDone = s.threadTurnFinished(thVal.ID)
			if turnDone != "" {
				renderProviderIconDone(c, "th-done-"+o.projectID+"-"+thVal.ID, thVal.Provider, 14, turnDone)
			} else {
				renderProviderIcon(c, thVal.Provider, 14)
			}
		}

		// 来源标识：转发或分支会话在标题左侧展示来源类型小图标
		if originIcon := threadOriginIcon(thVal); originIcon != nil {
			ui.Icon(c, originIcon).
				Size(12, 12).
				Shrink(0).
				TextColor(secondaryText.Alpha(0.65)).
				Margin(0, -3, 0, 0)
		}

		// 会话标题：紧凑模式单行展示，常规模式最多两行，已休眠/已归档使用次要灰度色（12pt），活跃选中时 500 Medium（13pt）
		title := thVal.Title
		if title == "" {
			title = "新会话"
		}
		// 紧凑模式单行：超长标题在悬停时跑马灯横向滚动。常规模式仍是两行截断，
		// 换成单行滚动会让行高在悬停瞬间跳变。
		var tTitle ui.Element
		if o.compact {
			tTitle = renderSidebarScrollingTitle(c, "th-title-"+o.projectID+"-"+thVal.ID, title, rowHovered, 0)
		} else {
			tTitle = ui.Text(c, title).Grow(1).Shrink(1).MinWidth(0).MaxLines(2).Ellipsis("…")
		}
		if o.dormant {
			tTitle.FontSize(scaledFont(FontSizeDormantTitle))
			if isSelected {
				tTitle.TextColor(primaryText.Alpha(0.75))
			} else {
				tTitle.TextColor(secondaryText)
			}
		} else {
			tTitle.FontSize(scaledFont(FontSizeThreadTitle))
			if isSelected {
				tTitle.TextColor(primaryText).FontWeight(500)
			} else if turnDone != "" {
				// 跑完未查看：标题与角标同色，长列表里一眼能挑出来；报错才用红色。
				tTitle.TextColor(turnDoneColor(c, turnDone))
			} else {
				tTitle.TextColor(primaryText.Alpha(0.92))
			}
		}

		// 动态视图：标题后附带所属项目名（弱化色小字）
		if o.projectName != "" {
			ui.Text(c, o.projectName).
				FontSize(scaledFont(FontSizeProjectName)).
				TextColor(secondaryText).
				SingleLine()
		}

		// 当前会话开启 Swarm 时，标题旁展示一个小 Swarm 标识。
		if isSelected && swarmPanelEnabled(s) {
			ui.Icon(c, IconSwarm()).
				Size(11, 11).
				TextColor(accent)
		}

		// AI 自建 worktree 标识：会话实际写在一个自己建的 worktree 里，悬停可看路径
		if aiWorktree != "" {
			ui.Icon(c, IconGitBranch()).
				Size(11, 11).
				TextColor(accent).
				Tooltip("AI Worktree\n" + aiWorktree)
		}

		// 总管会话标识：长列表里一眼认出总管；stewardThreadID 只做类型断言加一次接口调用，很轻。
		if stewardID := s.stewardThreadID(); stewardID != "" && stewardID == thVal.ID {
			nickname, _ := s.stewardIdentity()
			ui.Icon(c, IconSparkles()).
				Size(11, 11).
				TextColor(accent).
				Tooltip(stewardName(nickname))
		}

		// 状态内容始终占位，悬停操作覆盖在右侧，避免标题宽度跳变。
		ui.Row(c.Key("th-slot-" + thVal.ID)).MinWidth(ControlMD).Shrink(0).AlignItems(ui.Center).Justify(ui.End).Children(func() {
			status := ui.Row(c.Key("th-status-" + thVal.ID)).AlignItems(ui.Center)
			if showRowActions {
				status.Opacity(0).Disabled(true)
			}
			status.Children(func() {
				renderSidebarStatusSlot(c, s, thVal, secondaryText)
			})
			if showRowActions {
				ui.Row(c.Key("th-actions-" + thVal.ID)).Absolute().Right(0).Top(0).Bottom(0).AlignItems(ui.Center).Children(func() {
					renderThreadRowHoverActions(c, s, o, thVal)
				})
			}
		})
	})

	// 步骤 3: 渲染完父会话行之后调用 bee 子行
	if len(o.bees) > 0 {
		renderSidebarBeeSubRow(c, s, o)
	}
}

// sidebarStatusSlot 是会话行尾状态位的展示类型。
type sidebarStatusSlot int

const (
	sidebarStatusNone sidebarStatusSlot = iota
	sidebarStatusPending
	sidebarStatusRunning
	sidebarStatusWaiting
	sidebarStatusUnread
	sidebarStatusPinned
	sidebarStatusTime
)

// sidebarStatusSlotKind 决定会话行尾展示哪一种状态位，按优先级只选一个：
// 待审批 > 运行中 > 等待后台任务 > 未读 > 置顶 > 最近活动相对时间。运行中的会话本来就不会亮未读点，
// 运行中排在待审批之后，是为了把「为什么还没好」提前到置顶与相对时间之前。
// 等待后台任务排在运行中之后：这一轮回复已经结束，但仍有后台任务在跑，用时钟提醒它还没彻底完；
// 它不影响未读判定（未读只认 threadStillBusy，见 hasThreadUnread）。
// 优先级集中在这里，便于直接断言，渲染见 renderSidebarStatusSlot。
func (s *chatState) sidebarStatusSlotKind(th Thread) sidebarStatusSlot {
	switch {
	case th.HasPendingRequest:
		return sidebarStatusPending
	case s.threadStillBusy(th):
		return sidebarStatusRunning
	case th.BackgroundTasks > 0:
		return sidebarStatusWaiting
	case s.hasThreadUnread(th):
		return sidebarStatusUnread
	case th.Pinned:
		return sidebarStatusPinned
	case !threadActivityTime(th).IsZero():
		return sidebarStatusTime
	default:
		return sidebarStatusNone
	}
}

// sidebarRunningLabel 返回「运行 N 分钟」式的行尾文案：不足 1 分钟或拿不到起点时间时只说「运行中」。
// Thread 没有单独的本轮开始时间，取用户最后一条消息的时间当本轮起点，缺失时退回最近活动时间。
func sidebarRunningLabel(th Thread, now time.Time) string {
	start := th.LastUserMessageAt
	if start.IsZero() {
		start = th.UpdatedAt
	}
	if start.IsZero() {
		return "运行中"
	}
	diff := now.Sub(start)
	switch {
	case diff < time.Minute:
		return "运行中"
	case diff < time.Hour:
		return fmt.Sprintf("运行 %d 分钟", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("运行 %d 小时", int(diff.Hours()))
	default:
		return fmt.Sprintf("运行 %d 天", int(diff.Hours()/24))
	}
}

// renderSidebarStatusSlot 渲染会话行尾的单一状态位。
func renderSidebarStatusSlot(c *ui.Context, s *chatState, thVal Thread, secondaryText ui.Color) {
	switch s.sidebarStatusSlotKind(thVal) {
	case sidebarStatusPending:
		// 待审批用警示三角加一行同样的短字说明原因，比单个图标更明确；
		// 宽度紧张时只截断文字，行高保持不变。
		ui.Row(c.Key("pending-mark-"+thVal.ID)).
			Margin(0, 2, 0, 2).
			Gap(Space1).
			AlignItems(ui.Center).
			Label("等你审批").
			Children(func() {
				ui.Icon(c, IconAlertTriangle()).
					Size(12, 12).
					TextColor(ThemeWarning(c))
				ui.Text(c, "等你审批").
					FontSize(scaledFont(FontSizeRelativeTime)).
					TextColor(ThemeWarning(c)).
					SingleLine()
			})
	case sidebarStatusRunning:
		// 运行中用相对时长说明这一轮已经跑了多久；同一时间只占行尾，不再显示相对时间。
		ui.Text(c, sidebarRunningLabel(thVal, time.Now())).
			FontSize(scaledFont(FontSizeRelativeTime)).
			TextColor(secondaryText).
			SingleLine()
	case sidebarStatusWaiting:
		// 回合已结束但仍有后台任务在跑：强调色时钟加「后台 N」，与运行中的时长文案区分开。
		label := "后台"
		if thVal.BackgroundTasks > 1 {
			label = fmt.Sprintf("后台 %d", thVal.BackgroundTasks)
		}
		ui.Row(c.Key("waiting-bg-"+thVal.ID)).
			Margin(0, 2, 0, 2).
			Gap(Space1).
			AlignItems(ui.Center).
			Label("等待后台任务").
			Tooltip(fmt.Sprintf("等待 %d 个后台任务，完成后自动继续", thVal.BackgroundTasks)).
			Children(func() {
				ui.Icon(c, IconClock()).
					Size(11, 11).
					TextColor(ThemeAccent(c))
				ui.Text(c, label).
					FontSize(scaledFont(FontSizeRelativeTime)).
					TextColor(ThemeAccent(c)).
					SingleLine()
			})
	case sidebarStatusUnread:
		// 仍在忙的会话不算未读，见 hasThreadUnread。
		ui.Box(c.Key("unread-dot-"+thVal.ID)).
			Size(6, 6).
			Radius(RadiusPill).
			Background(ThemeAccent(c)).
			Margin(0, 2, 0, 2)
	case sidebarStatusPinned:
		ui.Icon(c, IconPin()).
			Size(11, 11).
			TextColor(secondaryText).
			Margin(0, 2, 0, 0)
	case sidebarStatusTime:
		ui.Text(c, formatRelativeTime(threadActivityTime(thVal))).
			FontSize(scaledFont(FontSizeRelativeTime)).
			TextColor(secondaryText)
	}
}

// projectPathByID 返回项目的工作目录；项目列表里没有该 ID 时返回空串。
func (s *chatState) projectPathByID(projectID string) string {
	if s == nil || projectID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.projects {
		if p.ID == projectID {
			return p.Path
		}
	}
	return ""
}

// stewardTopicProjectTarget 返回「交给{昵称}」入口要用的当前项目：项目 ID 与显示名。
// 项目取自工作区当前聚焦的项目（与看板、自动化取法一致，最终回落到当前会话所属项目或第一个项目），
// 一个项目都没有时两个都返回空串。项目右键菜单那条入口直接用被点的那一行，不经过这里。
func (s *chatState) stewardTopicProjectTarget() (string, string) {
	if s == nil {
		return "", ""
	}
	projectID := s.currentWorkspaceProjectID()
	if projectID == "" {
		return "", ""
	}
	return projectID, projectNameFor(s, projectID)
}

// threadContextMenu 返回会话行的上下文菜单构建函数：右键菜单与悬停时的「更多操作」按钮共用，
// 保证两条入口的菜单项完全一致。
func threadContextMenu(c *ui.Context, s *chatState, projectID string, thVal Thread) func(*ui.Menu) {
	return func(m *ui.Menu) {
		// 第一组：主操作，最常用的转发与新窗口打开。
		if m.Item("转发到新会话").Chosen() {
			s.forwardThread(projectID, thVal.ID)
		}
		if s.openThreadWindow != nil {
			if m.Item("在新窗口打开").Chosen() {
				s.openThreadWindow(thVal.ID)
			}
		}
		m.Separator()
		// 第二组：会话自身的属性变更（重命名、置顶、休眠、迁入 Worktree）。
		if m.Item("重命名…").Chosen() {
			s.openRenameModal(thVal.ID, thVal.Title)
		}
		pinLabel := "置顶"
		if thVal.Pinned {
			pinLabel = "取消置顶"
		}
		if m.Item(pinLabel).Chosen() {
			s.setThreadPinned(thVal.ID, !thVal.Pinned)
		}
		dormantLabel := "标记休眠"
		if thVal.Dormant {
			dormantLabel = "重新激活"
		}
		if m.Item(dormantLabel).Chosen() {
			if thVal.Dormant {
				// 取消休眠不产生撤销令牌，沿用原有路径。
				s.setThreadDormant(thVal.ID, false)
			} else {
				// 休眠后 10 秒内可通过撤销条或项目菜单撤销。
				s.markDormantWithUndo(projectID, thVal.ID)
			}
		}
		if m.Item("迁入 Worktree…").Chosen() {
			s.openWorktreeMove(thVal.ID)
		}
		m.Separator()
		// 第三组：导出与复制类（不改动会话本身）。
		if m.Item("导出会话…").Chosen() {
			s.exportThread(thVal.ID)
		}
		if m.Item("复制为 Markdown").Chosen() {
			s.copyThreadMarkdown(c, thVal.ID)
		}
		// 会话目录：存在 AI worktree 时用 worktree 路径，否则用项目路径。
		threadDir := s.aiWorktreePath(thVal.ID)
		if threadDir == "" {
			threadDir = s.projectPathByID(projectID)
		}
		if m.Item("复制会话 ID").Chosen() {
			c.WriteClipboard(thVal.ID)
			s.setToast("已复制会话 ID")
		}
		if threadDir != "" {
			if m.Item("复制路径").Chosen() {
				c.WriteClipboard(threadDir)
				s.setToast("已复制路径")
			}
			if m.Item("在访达中显示").Chosen() {
				mygo.Shell.ShowItemInFolder(threadDir)
			}
		}
		m.Separator()
		// 第四组：危险操作，放在最后。
		if m.Item("归档").Chosen() {
			// 单条归档也返回撤销令牌，可从撤销条撤销。
			s.archiveThreadWithUndo(projectID, thVal.ID)
		}
		if m.Item("删除…").Chosen() {
			// E-01：删除前确认关闭时跳过确认框，直接删除。
			if confirmBeforeDeleting(s) {
				s.openDeleteModal(thVal.ID, thVal.Title)
			} else {
				s.deleteThreadNow(thVal.ID, thVal.Title)
			}
		}
	}
}

// renderThreadRowHoverActions 渲染会话行悬停时右侧的「更多操作」按钮（ellipsis 图标，
// 点击打开该会话的上下文菜单）。休眠入口只保留在右键菜单与项目菜单里，
// 行悬停不再提供按钮，避免误点把会话休眠掉。
func renderThreadRowHoverActions(c *ui.Context, s *chatState, o sidebarThreadRowOptions, thVal Thread) {
	secondaryText := ThemeSecondaryText(c)
	hoverBg := ThemeHover(c)

	moreBtn := ui.ButtonBase(c.Key("th-more-"+thVal.ID)).
		Label("更多操作").
		Tooltip("更多操作").
		Radius(RadiusSM).
		Padding(Space0, Space1)
	if moreBtn.Hovered() {
		moreBtn.Background(hoverBg)
	}
	moreBtn.Menu(threadContextMenu(c, s, o.projectID, thVal))
	moreBtn.Children(func() {
		ui.Icon(c, IconMoreHorizontal()).
			Size(14, 14).
			TextColor(secondaryText)
	})
}

// renderSidebarModals 统一渲染侧边栏相关的模态弹窗（重命名、删除会话、移除项目）。
func renderSidebarModals(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	renderRenameThreadModal(c, s)
	renderDeleteThreadModal(c, s)
	renderRemoveProjectModal(c, s)
	renderWorktreeManageModal(c, s)
	renderWorktreeMoveModal(c, s)
	// 总管话题的两个弹窗：交给总管 / 打回任务。挂在根层，不被侧栏与主区裁切。
	renderStewardTopicOverlays(c, s)
}

func renderRenameThreadModal(c *ui.Context, s *chatState) {
	s.mu.Lock()
	open := s.renameModalOpen
	title := s.renameThreadTitle
	s.mu.Unlock()
	if !open {
		modalDragReset(modalDragRenameThread)
		return
	}

	const renameModalHeight = 200
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragRenameThread, 380, renameModalHeight, 360, 160, winW, winH)
	modalDragModal(c, modalDragRenameThread, &s.renameModalOpen, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			s.closeRenameModal()
			return
		}

		ui.Column(c.Key("modal-rename-card")).
			Label("modal-rename-card").
			Width(width).
			Height(height).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(1).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, modalDragRenameThread, ui.Row(c.Key("modal-rename-title-row")).FillWidth().Children(func() {
					ui.Text(c, "重命名会话").
						FontSize(scaledFont(TypeTitle)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				}))

				in := ui.TextInput(c.Key("modal-rename-input"), &s.renameThreadTitle).
					Placeholder("输入会话标题...").
					FontSize(scaledFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					Background(ThemeHover(c).Alpha(0.25)).
					BorderColor(ThemeBorder(c)).
					BorderWidth(1).
					Radius(RadiusSM).
					Padding(Space2, Space4).
					FillWidth().
					AutoFocus()

				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEnter {
						if strings.TrimSpace(s.renameThreadTitle) != "" {
							s.submitRename()
						}
						return true
					}
					if k.Key == ui.KeyEscape {
						s.closeRenameModal()
						return true
					}
					return false
				})

				ui.Row(c.Key("modal-rename-actions")).
					FillWidth().
					Justify(ui.End).
					Gap(Space3).
					Children(func() {
						Button(c.Key("modal-rename-cancel-btn"), ButtonOpts{
							Text:    "取消",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.closeRenameModal()
							},
						})

						canSave := strings.TrimSpace(title) != ""
						Button(c.Key("modal-rename-confirm-btn"), ButtonOpts{
							Text:     "确定",
							Variant:  ButtonPrimary,
							Disabled: !canSave,
							OnClick: func() {
								if canSave {
									s.submitRename()
								}
							},
						})
					})
				modalResizeGripsMin(c, modalDragRenameThread, width, height, 360, 160)
			})
	})
}

// confirmBeforeDeleting 读取「删除前确认」开关（Archive.ConfirmBeforeDeleting）。
// 设置面板存在时读内存配置，否则回退读磁盘配置；读不到时按开启处理，保持原有二次确认行为。
func confirmBeforeDeleting(s *chatState) bool {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Archive.ConfirmBeforeDeleting
	}
	dataDir := resolveDataDirFallback()
	if dataDir == "" {
		return true
	}
	cfg, err := config.Load(dataDir)
	if err != nil {
		return true
	}
	return cfg.Archive.ConfirmBeforeDeleting
}

// deleteThreadNow 跳过确认弹窗直接删除会话：confirmDelete 依赖弹窗暂存的会话 ID，
// 这里先填好 ID 再走同一条删除路径。
func (s *chatState) deleteThreadNow(id, title string) {
	s.mu.Lock()
	s.deleteThreadID = id
	s.deleteThreadTitle = title
	s.mu.Unlock()
	s.confirmDelete()
}

// removeProjectNow 跳过确认弹窗直接从侧边栏移除项目。
func (s *chatState) removeProjectNow(id string) {
	s.mu.Lock()
	s.removeProjID = id
	s.mu.Unlock()
	s.confirmRemoveProject()
}

func renderDeleteThreadModal(c *ui.Context, s *chatState) {
	s.mu.Lock()
	open := s.deleteModalOpen
	title := s.deleteThreadTitle
	s.mu.Unlock()

	desc := "删除后无法恢复，确定要删除此会话吗？"
	if title != "" {
		desc = fmt.Sprintf("删除后无法恢复，确定要删除会话 %q 吗？", title)
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragDeleteThread,
		Open:         open,
		Tone:         ConfirmDanger,
		Title:        "删除会话",
		Body:         desc,
		ConfirmLabel: "删除",
		// 确认与取消都沿用原有语义：confirmDelete 内部会先关闭弹窗再异步删除。
		OnConfirm: func() { s.confirmDelete() },
		OnCancel:  func() { s.closeDeleteModal() },
	})
}

func renderRemoveProjectModal(c *ui.Context, s *chatState) {
	s.mu.Lock()
	open := s.removeProjModalOpen
	name := s.removeProjName
	s.mu.Unlock()

	desc := "该项目的会话记录会一并从 mcode 中删除，磁盘上的文件不会被删除。"
	if name != "" {
		desc = fmt.Sprintf("确定移除项目 %q 吗？该项目的会话记录会一并从 mcode 中删除，磁盘上的文件不会被删除。", name)
	}
	RenderConfirmDialog(c, ConfirmDialog{
		Key:          modalDragRemoveProject,
		Open:         open,
		Tone:         ConfirmDanger,
		Title:        "移除项目",
		Body:         desc,
		ConfirmLabel: "移除",
		OnConfirm:    func() { s.confirmRemoveProject() },
		OnCancel:     func() { s.closeRemoveProjectModal() },
	})
}

// forwardThread 将已有会话转发为新会话并选中它。
func (s *chatState) forwardThread(projectID, threadID string) {
	s.mu.Lock()
	if s.forwardInflight[threadID] {
		// 同一会话已在转发中，直接忽略本次触发。
		s.mu.Unlock()
		return
	}
	if s.forwardInflight == nil {
		s.forwardInflight = make(map[string]bool)
	}
	s.forwardInflight[threadID] = true
	s.mu.Unlock()
	go func() {
		// defer 在 goroutine 退出时执行，与下面的持锁段、selectThread 天然错开，不会重入。
		defer func() {
			s.mu.Lock()
			delete(s.forwardInflight, threadID)
			s.mu.Unlock()
		}()
		ctx := context.Background()
		newThreadID, err := s.client.ForwardThread(ctx, threadID)
		if err != nil {
			s.setToast("转发会话失败: " + err.Error())
			return
		}
		if projectID != "" {
			threads, err := s.client.Threads(ctx, projectID)
			if err == nil {
				s.mu.Lock()
				s.threadsByProj[projectID] = SortThreads(threads)
				var all []Thread
				for _, list := range s.threadsByProj {
					all = append(all, list...)
				}
				s.threads = SortThreads(all)
				// 整份列表重载可能带来新的待处理请求/运行标记。
				s.bumpThreadsRuntimeLocked()
				if s.collapsedProj == nil {
					s.collapsedProj = make(map[string]bool)
				}
				s.collapsedProj[projectID] = false
				s.mu.Unlock()
			}
		}
		s.closeWorkspace()
		s.selectThread(newThreadID)
	}()
}

// forwardSelectedThread 把当前选中的会话转发为新会话。
// 没有选中会话、或选中的会话还不在列表里（如待创建会话）时什么也不做，返回 false。
func (s *chatState) forwardSelectedThread() bool {
	s.mu.Lock()
	id := s.selectedThreadID
	projectID := ""
	found := false
	for _, th := range s.threads {
		if th.ID == id {
			projectID = th.ProjectID
			found = true
			break
		}
	}
	s.mu.Unlock()
	if id == "" || !found {
		return false
	}
	s.forwardThread(projectID, id)
	return true
}

// setThreadDormant 设置指定会话的休眠状态，成功后播放休眠提示音。
func (s *chatState) setThreadDormant(id string, dormant bool) {
	s.setThreadDormantWithSound(id, dormant, true)
}

// setThreadDormantWithSound 同 setThreadDormant；sound 为 false 用于批量与自动休眠，不播提示音。
func (s *chatState) setThreadDormantWithSound(id string, dormant bool, sound bool) {
	go func() {
		if err := s.client.SetThreadDormant(id, dormant); err != nil {
			action := "标记休眠"
			if !dormant {
				action = "重新激活"
			}
			s.setToast(action + "失败: " + err.Error())
			return
		}
		s.mu.Lock()
		for pid, list := range s.threadsByProj {
			for i := range list {
				if list[i].ID == id {
					list[i].Dormant = dormant
				}
			}
			s.threadsByProj[pid] = SortThreads(list)
		}
		for i := range s.threads {
			if s.threads[i].ID == id {
				s.threads[i].Dormant = dormant
			}
		}
		s.threads = SortThreads(s.threads)
		s.mu.Unlock()
		s.invalidate()
		if dormant && sound {
			s.playDormantChime()
		}
	}()
}

// worktreeUIState 维护侧边栏 Worktree 管理与迁入模态框的状态，按 chatState 附加（同 chatHeaderState）。
// manageOpen、moveOpen、moveBranch、moveTransfer 绑定在控件上，其余字段受 mu 保护。
type worktreeUIState struct {
	mu sync.Mutex

	manageOpen        bool
	manageProjectPath string
	scanning          bool
	scanErr           string
	items             []WorktreeInfo
	cleaning          bool
	scanSeq           int

	moveOpen     bool
	moveThreadID string
	moveBranch   string
	moveTransfer bool
	moving       bool
}

var (
	worktreeUIMu     sync.Mutex
	worktreeUIStates = make(map[*chatState]*worktreeUIState)
)

func getWorktreeUIState(s *chatState) *worktreeUIState {
	worktreeUIMu.Lock()
	defer worktreeUIMu.Unlock()
	ws, ok := worktreeUIStates[s]
	if !ok {
		ws = &worktreeUIState{}
		worktreeUIStates[s] = ws
	}
	return ws
}

// refreshProjectThreads 重新拉取指定项目的会话列表并展开该项目。
func (s *chatState) refreshProjectThreads(ctx context.Context, projectID string) {
	if projectID == "" {
		return
	}
	threads, err := s.client.Threads(ctx, projectID)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.threadsByProj[projectID] = SortThreads(threads)
	var all []Thread
	for _, list := range s.threadsByProj {
		all = append(all, list...)
	}
	s.threads = SortThreads(all)
	// 整份列表重载可能带来新的待处理请求/运行标记。
	s.bumpThreadsRuntimeLocked()
	if s.collapsedProj == nil {
		s.collapsedProj = make(map[string]bool)
	}
	s.collapsedProj[projectID] = false
	s.mu.Unlock()
	s.invalidate()
}

// createThreadInWorktree 进入待创建会话：首次输入或发送时才真正建会话，并立即迁入自动命名的 Worktree。
// 迁入失败时新建的会话保留，仅提示错误。
func (s *chatState) createThreadInWorktree(projectID string) {
	s.beginPendingThread(projectID, s.newThreadOptions(), true)
}

// openWorktreeManage 打开项目的 Worktree 管理模态框并开始扫描。
func (s *chatState) openWorktreeManage(projectID, projectPath string) {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	ws.manageOpen = true
	ws.manageProjectPath = projectPath
	ws.items = nil
	ws.scanErr = ""
	ws.mu.Unlock()
	s.scanWorktrees()
}

// scanWorktrees 在后台扫描 Worktree，扫描期间显示「扫描中…」。
func (s *chatState) scanWorktrees() {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	ws.scanning = true
	ws.scanErr = ""
	ws.scanSeq++
	seq := ws.scanSeq
	ws.mu.Unlock()
	s.invalidate()

	go func() {
		items, err := s.client.ScanWorktrees(context.Background())
		ws.mu.Lock()
		if ws.scanSeq == seq {
			ws.scanning = false
			if err != nil {
				ws.scanErr = err.Error()
				ws.items = nil
			} else {
				ws.items = items
			}
		}
		ws.mu.Unlock()
		s.invalidate()
	}()
}

// cleanSafeWorktrees 清理安全的 Worktree，成功后重新扫描。
func (s *chatState) cleanSafeWorktrees() {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	if ws.cleaning {
		ws.mu.Unlock()
		return
	}
	ws.cleaning = true
	ws.mu.Unlock()
	s.invalidate()

	go func() {
		n, err := s.client.CleanSafeWorktrees(context.Background())
		ws.mu.Lock()
		ws.cleaning = false
		ws.mu.Unlock()
		if err != nil {
			s.setToast(err.Error())
			s.invalidate()
			return
		}
		s.setToast(fmt.Sprintf("已清理 %d 个 Worktree", n))
		s.scanWorktrees()
	}()
}

// openWorktreeMove 打开会话迁入 Worktree 的模态框，默认勾选迁移未提交改动。
func (s *chatState) openWorktreeMove(threadID string) {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	ws.moveOpen = true
	ws.moveThreadID = threadID
	ws.moveBranch = ""
	ws.moveTransfer = true
	ws.moving = false
	ws.mu.Unlock()
	s.invalidate()
}

func (s *chatState) closeWorktreeMove() {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	ws.moveOpen = false
	ws.moveThreadID = ""
	ws.moveBranch = ""
	ws.mu.Unlock()
	s.invalidate()
}

// submitWorktreeMove 执行迁入，成功后关闭模态框并刷新会话列表。
func (s *chatState) submitWorktreeMove() {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	if ws.moving || ws.moveThreadID == "" {
		ws.mu.Unlock()
		return
	}
	ws.moving = true
	id := ws.moveThreadID
	branch := strings.TrimSpace(ws.moveBranch)
	transfer := ws.moveTransfer
	ws.mu.Unlock()
	s.invalidate()

	go func() {
		ctx := context.Background()
		err := s.client.MoveToWorktree(ctx, id, branch, transfer)
		ws.mu.Lock()
		ws.moving = false
		ws.mu.Unlock()
		if err != nil {
			s.setToast(err.Error())
			s.invalidate()
			return
		}
		s.closeWorktreeMove()
		s.setToast("已迁入 Worktree")
		s.mu.Lock()
		var projectID string
		for _, th := range s.threads {
			if th.ID == id {
				projectID = th.ProjectID
				break
			}
		}
		s.mu.Unlock()
		s.refreshProjectThreads(ctx, projectID)
	}()
}

func renderWorktreeManageModal(c *ui.Context, s *chatState) {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	open := ws.manageOpen
	projectPath := ws.manageProjectPath
	scanning := ws.scanning
	scanErr := ws.scanErr
	cleaning := ws.cleaning
	var items []WorktreeInfo
	for _, it := range ws.items {
		if it.ProjectPath == projectPath {
			items = append(items, it)
		}
	}
	ws.mu.Unlock()
	if !open {
		modalDragReset(modalDragWorktreeManage)
		return
	}

	closeManage := func() {
		ws.mu.Lock()
		ws.manageOpen = false
		ws.mu.Unlock()
		s.invalidate()
	}

	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragWorktreeManage, 480, 500, min(360, max(0, winW-modalDragPanelInset)), min(240, max(0, winH-modalDragPanelInset)), winW, winH)
	modalDragModal(c, modalDragWorktreeManage, &ws.manageOpen, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			closeManage()
			return
		}

		ui.Column(c.Key("modal-worktree-card")).
			Width(width).
			Height(height).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(1).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, modalDragWorktreeManage, ui.Row(c.Key("modal-worktree-title-row")).FillWidth().Children(func() {
					ui.Text(c, "Worktree 管理").
						FontSize(scaledFont(TypeTitle)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				}))

				ui.Text(c, "仅清理已合入、无未提交改动、且 10 分钟内无活动的 Worktree；使用中的不会被删除").
					FontSize(scaledFont(TypeFootnote)).
					TextColor(ThemeSecondaryText(c))

				switch {
				case scanning:
					ui.Row(c.Key("modal-worktree-scanning")).
						Gap(Space2).
						AlignItems(ui.Center).
						Children(func() {
							ui.Spinner(c).Size(12, 12).TextColor(ThemeAccent(c))
							ui.Text(c, "扫描中…").
								FontSize(scaledFont(TypeBody)).
								TextColor(ThemeSecondaryText(c))
						})
				case scanErr != "":
					ui.Row(c.Key("modal-worktree-error")).
						Gap(Space3).
						AlignItems(ui.Center).
						Children(func() {
							ui.Text(c, scanErr).
								FontSize(scaledFont(TypeBody)).
								TextColor(ThemeDanger(c))
							Button(c.Key("modal-worktree-retry-btn"), ButtonOpts{
								Text:    "重试",
								Variant: ButtonSecondary,
								OnClick: func() {
									s.scanWorktrees()
								},
							})
						})
				case len(items) == 0:
					ui.Text(c, "没有额外的 Worktree").
						FontSize(scaledFont(TypeBody)).
						TextColor(ThemeSecondaryText(c))
				default:
					ui.Scroll(c.Key("modal-worktree-scroll")).MaxHeight(max(0, height-180)).Grow(1).Children(func() {
						ui.Column(c.Key("modal-worktree-list")).
							FillWidth().
							Gap(Space0).
							Children(func() {
								for _, it := range items {
									renderWorktreeRow(c, it)
								}
							})
					})
				}

				ui.Row(c.Key("modal-worktree-actions")).
					FillWidth().
					Justify(ui.End).
					Gap(Space3).
					Children(func() {
						Button(c.Key("modal-worktree-close-btn"), ButtonOpts{
							Text:    "关闭",
							Variant: ButtonSecondary,
							OnClick: func() {
								closeManage()
							},
						})
						Button(c.Key("modal-worktree-clean-btn"), ButtonOpts{
							Text:     "清理安全的 Worktree",
							Variant:  ButtonPrimary,
							Disabled: cleaning || scanning,
							OnClick: func() {
								s.cleanSafeWorktrees()
							},
						})
					})
				modalResizeGrips(c, modalDragWorktreeManage, width, height)
			})
	})
}

func renderWorktreeRow(c *ui.Context, it WorktreeInfo) {
	key := "modal-worktree-row-" + it.Path
	ui.Row(c.Key(key)).
		FillWidth().
		Padding(Space2, Space3).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, filepath.Base(it.Path)).
				FontSize(scaledFont(TypeBody)).
				TextColor(ThemePrimaryText(c)).
				SingleLine()
			ui.Text(c, it.Branch).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine().
				Grow(1)
			ui.Text(c, formatFileSize(it.SizeBytes)).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
			ui.Text(c, strconv.Itoa(it.AgeDays)+" 天").
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
			if it.Merged {
				renderWorktreeTag(c, key+"-merged", "已合入", ThemeAccent(c))
			}
			if it.Active {
				renderWorktreeTag(c, key+"-active", "使用中", ThemeDanger(c))
			}
		})
}

func renderWorktreeTag(c *ui.Context, key, text string, color ui.Color) {
	ui.Row(c.Key(key)).
		Padding(Hairline, Space2).
		Radius(RadiusXS).
		Background(color.Alpha(0.15)).
		Children(func() {
			ui.Text(c, text).
				FontSize(scaledFont(TypeCaption)).
				TextColor(color)
		})
}

func renderWorktreeMoveModal(c *ui.Context, s *chatState) {
	ws := getWorktreeUIState(s)
	ws.mu.Lock()
	open := ws.moveOpen
	moving := ws.moving
	ws.mu.Unlock()
	if !open {
		modalDragReset(modalDragWorktreeMove)
		return
	}

	const worktreeMoveModalHeight = 240
	winW, winH := c.Size()
	width, height := modalResizeSize(modalDragWorktreeMove, 380, worktreeMoveModalHeight, 360, 160, winW, winH)
	modalDragModal(c, modalDragWorktreeMove, &ws.moveOpen, func() {
		if c.Shortcut(0, ui.KeyEscape) {
			s.closeWorktreeMove()
			return
		}

		ui.Column(c.Key("modal-worktree-move-card")).
			Label("modal-worktree-move-card").
			Width(width).
			Height(height).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(1).
			Radius(RadiusLG).
			Padding(Space5).
			Gap(Space4).
			Children(func() {
				// 标题行同时是弹窗的拖拽把手。
				modalDragHandle(c, modalDragWorktreeMove, ui.Row(c.Key("modal-worktree-move-title-row")).FillWidth().Children(func() {
					ui.Text(c, "迁入 Worktree").
						FontSize(scaledFont(TypeTitle)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))
				}))

				in := ui.TextInput(c.Key("modal-worktree-move-input"), &ws.moveBranch).
					Placeholder("留空自动生成").
					FontSize(scaledFont(TypeBody)).
					TextColor(ThemePrimaryText(c)).
					Background(ThemeHover(c).Alpha(0.25)).
					BorderColor(ThemeBorder(c)).
					BorderWidth(1).
					Radius(RadiusSM).
					Padding(Space2, Space4).
					FillWidth().
					AutoFocus()
				in.OnTextKey(func(k ui.TextKey) bool {
					if k.Key == ui.KeyEnter {
						s.submitWorktreeMove()
						return true
					}
					if k.Key == ui.KeyEscape {
						s.closeWorktreeMove()
						return true
					}
					return false
				})

				ui.Row(c.Key("modal-worktree-move-transfer")).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Toggle(c.Key("modal-worktree-move-transfer-check"), &ws.moveTransfer, "同时迁移未提交的改动")
						ui.Text(c, "同时迁移未提交的改动").
							FontSize(scaledFont(TypeBody)).
							TextColor(ThemePrimaryText(c))
					})

				ui.Row(c.Key("modal-worktree-move-actions")).
					FillWidth().
					Justify(ui.End).
					Gap(Space3).
					Children(func() {
						Button(c.Key("modal-worktree-move-cancel-btn"), ButtonOpts{
							Text:    "取消",
							Variant: ButtonSecondary,
							OnClick: func() {
								s.closeWorktreeMove()
							},
						})
						Button(c.Key("modal-worktree-move-confirm-btn"), ButtonOpts{
							Text:     "迁入",
							Variant:  ButtonPrimary,
							Disabled: moving,
							OnClick: func() {
								s.submitWorktreeMove()
							},
						})
					})
				modalResizeGripsMin(c, modalDragWorktreeMove, width, height, 360, 160)
			})
	})
}
