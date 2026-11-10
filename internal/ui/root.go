package mcodeui

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// sidebarSlideDuration 是侧边栏显示/隐藏过渡的时长。
const sidebarSlideDuration = 220 * time.Millisecond

// sidebarAnimatedWidth 计算侧边栏过渡动画中当前帧的裁切宽度。
// target 为展开状态下的侧边栏宽度；progress 为 0..1 的过渡进度，
// 0 表示仍停留在动画起点，1 表示已到达目标状态；
// collapsing 表示目标状态是否为折叠隐藏：折叠时宽度从 target 收缩到 0，展开时从 0 增长到 target。
func sidebarAnimatedWidth(target float32, progress float32, collapsing bool) float32 {
	if target <= 0 {
		return 0
	}
	if progress < 0 {
		progress = 0
	} else if progress > 1 {
		progress = 1
	}
	if collapsing {
		progress = 1 - progress
	}
	return target * progress
}

// Option 配置 Root 的可选参数。
type Option func(*chatState)

// WithPickFolder 注入自定义文件夹选择函数（例如测试时注入假对话框）。
func WithPickFolder(fn func() (string, bool)) Option {
	return func(s *chatState) {
		s.pickFolder = fn
	}
}

// buildRoot 构造独立的 chatState 实例与界面构建闭包。
func buildRoot(client Client, opts ...Option) (*chatState, func(c *ui.Context)) {
	state := newChatState(client)
	for _, opt := range opts {
		if opt != nil {
			opt(state)
		}
	}
	if state.tasksWindowView != "" {
		return state, tasksRootView(state)
	}
	// 启动时恢复上次持久化的侧边栏宽度与显隐状态。
	// 默认不读写 ui-state.json，只有显式传入 WithUIStatePersistence 的主窗口才恢复，
	// 避免多窗口与测试通过共享数据目录互相污染。
	if uiStatePersistenceEnabled(state) {
		applyUIState(state, loadUIState())
		// 恢复各会话草稿（仅文本），首次选中会话时回填输入框。
		applyComposerDrafts(state, loadComposerDrafts())
	}
	type scriptResult struct {
		threadID string
		scripts  []Script
		err      error
	}
	scriptResults := make(chan scriptResult, 1)
	scriptLoading := false

	view := func(c *ui.Context) {
		if takeOnboardingAutoOpen(state) {
			openOnboarding(state)
		}
		applyAppTheme(c)
		state.init(c)
		state.applyWorkspaceUpdates()
		// 每帧把「用户正在看的会话」同步给应用层，值没变时内部直接跳过。
		state.reportForegroundThread()

		// 后台建好的总管会话每帧在这里跳到前台，侧栏折叠时也不会漏跳。
		consumeStewardPendingThread(state)

		// 侧边栏显隐过渡。root-shell 是根容器，动画值挂在它上面：
		// 0 为完全展开、1 为完全折叠，按 ease-out 约 220ms 插值；动画期间侧边栏与主区
		// 同层布局、侧边栏按插值宽度裁切（内容始终按目标宽度排版，不随每帧重新换行），
		// 动画结束后回到既有的 Split / 无 Split 布局。
		shell := ui.Column(c.Key("root-shell")).Fill()
		collapseTarget := float32(0)
		if state.sidebarCollapsed {
			collapseTarget = 1
		}
		collapse := shell.AnimateTo("sidebar-collapse", collapseTarget, ui.Animation{
			Duration: animateDuration(c, sidebarSlideDuration),
			Ease:     ui.EaseOut,
		})
		slide := 1 - collapse
		slideSettled := shell.Settled("sidebar-collapse")
		setSidebarVisibleProgress(state, slide)

		// renderShell 渲染外壳布局：动画期间侧边栏按插值宽度裁切并与主区同层，
		// 动画结束后折叠时只渲染主区，展开时回到可拖拽的 Split 分栏（拖拽结束持久化宽度）。
		renderShell := func(renderSidebarPane, renderMainPane func()) {
			if !slideSettled {
				progress := slide
				if state.sidebarCollapsed {
					progress = 1 - slide
				}
				width := sidebarAnimatedWidth(state.sidebarWidth, progress, state.sidebarCollapsed)
				ui.Row(c.Key("root-slide")).Fill().Children(func() {
					if width > 0.5 {
						// 侧边栏内容按目标宽度排版，只有外层裁切宽度随动画变化；
						// 右对齐让内容随宽度收缩向左滑出，而不是被逐帧重新换行。
						ui.Row(c.Key("root-slide-sidebar")).
							Width(width).
							MinWidth(0).
							Clip().
							Justify(ui.End).
							Children(func() {
								ui.Column(c.Key("root-slide-sidebar-inner")).
									Width(state.sidebarWidth).
									FillHeight().
									Children(renderSidebarPane)
							})
					}
					ui.Column(c.Key("root-slide-main")).Grow(1).MinWidth(0).Children(renderMainPane)
				})
				return
			}
			if state.sidebarCollapsed {
				renderMainPane()
				// 折叠态下把窗口左缘的展开热区叠加在主面板之上
				// （绝对定位，不参与布局，不影响展开态）。
				renderSidebarEdgeHandle(c, state)
				return
			}
			ui.Split(c.Key("root-split"), &state.sidebarWidth, renderSidebarPane, renderMainPane).Fill().OnChange(func() {
				persistUIState(state)
			})
		}

		shell.Children(func() {
			select {
			case result := <-scriptResults:
				scriptLoading = false
				state.mu.Lock()
				current := state.selectedThreadID == result.threadID
				state.mu.Unlock()
				if current {
					switch {
					case result.err != nil:
						state.setToast("读取脚本失败: " + result.err.Error())
					case len(result.scripts) == 0:
						state.setToast("当前项目没有可运行的脚本")
					default:
						state.runScriptInTerminal(result.scripts[0])
					}
				}
			default:
			}

			// 检查是否有来自后台 goroutine 的错误信息，并在 UI 主线程中弹出 Toast
			state.mu.Lock()
			if state.toastMessage != "" {
				c.Toast(state.toastMessage)
				state.toastMessage = ""
			}
			state.mu.Unlock()

			// 限制侧边栏宽度在 SidebarMinWidth 与 SidebarMaxWidth 之间，默认 SidebarDefaultWidth。
			state.sidebarWidth = clampSidebarWidth(state.sidebarWidth)

			pal := getPaletteState(state)

			// 命令面板与设置窗口互不遮挡。
			if mods, key, ok := shortcutBindingFor("command-palette"); ok && c.Shortcut(mods, key) {
				togglePalette(state)
			}
			if mods, key, ok := shortcutBindingFor("toggle-sidebar"); ok && c.Shortcut(mods, key) {
				toggleSidebarPersisted(state)
			}

			// 主界面快捷键处理（仅在设置面板未打开时生效）
			ws := getWorkspaceState(state)
			view := "chat"
			if ws != nil {
				workspaceMu.Lock()
				view = ws.view
				workspaceMu.Unlock()
			}
			if view == "tasks" && c.Shortcut(ui.Cmd, ui.KeyF) {
				kanbanKeyboardSearchFocus(state, true)
			}

			paletteOpen := pal != nil && pal.open
			if paletteOpen {
				if c.Shortcut(0, ui.KeyEscape) {
					closePalette(state)
				}
			} else if view != "chat" && c.Shortcut(0, ui.KeyEscape) {
				// 看板的快速添加与搜索框先消费 Esc（取消输入或清空搜索），工作区保持打开。
				if view != "tasks" || !consumeKanbanEscape(state) {
					state.closeWorkspace()
					view = "chat"
				}
			}

			scID, scIdx, ok := dispatchActiveShortcut(c)
			if !ok {
				scID, scIdx, ok = state.popPendingAction()
			}
			if ok {
				// 右侧分栏面板（文件、网页预览、改动）的开关由 side_pane.go 的注册表统一派发，
				// 下面的 switch 不再为这三项单独写 case（命中时 switch 自然什么也不做）。
				if p := sidePaneByAction(scID); p != nil {
					p.toggle(state)
				}
				switch scID {
				case "new-thread":
					state.closeWorkspace()
					view = "chat"
					state.createNewThread()
				case "new-worktree-thread":
					// 取当前选中会话所属项目（退化为第一个项目），没有项目时提示。
					if projectID := worktreeTargetProjectID(state); projectID == "" {
						state.setToast("请先添加项目")
					} else {
						state.closeWorkspace()
						view = "chat"
						state.createThreadInWorktree(projectID)
					}
				case "toggle-terminal":
					toggleTerminal(state)
				case "toggle-plan-mode":
					if view == "chat" {
						state.toggleInteractionMode()
					}
				case "open-editor":
					// ⇧⌘E 打开迷你源码编辑器（从文件选择器选文件）
					openMiniEditorFromPicker(state)
				case "toggle-activity":
					// A-WS：动态视图开关由 sidebar_activity.go 的 toggleActivityView 实现。
					state.toggleActivityView()
				case "run-script":
					state.mu.Lock()
					threadID := state.selectedThreadID
					state.mu.Unlock()
					if threadID != "" && !scriptLoading {
						scriptLoading = true
						go func() {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							defer cancel()
							scripts, err := client.Scripts(ctx, threadID)
							scriptResults <- scriptResult{threadID: threadID, scripts: scripts, err: err}
							state.invalidate()
						}()
					}
				case "select-thread-n":
					visible := state.visibleThreads()
					if scIdx >= 1 && scIdx <= len(visible) {
						state.closeWorkspace()
						view = "chat"
						state.selectThread(visible[scIdx-1].ID)
					}
				case "previous-thread":
					if state.selectAdjacentThread(-1) {
						view = "chat"
					}
				case "next-thread":
					if state.selectAdjacentThread(1) {
						view = "chat"
					}
				case "dormant-thread":
					if state.selectedThreadID != "" {
						state.mu.Lock()
						isDormant := false
						for _, th := range state.threads {
							if th.ID == state.selectedThreadID {
								isDormant = th.Dormant
								break
							}
						}
						state.mu.Unlock()
						state.setThreadDormant(state.selectedThreadID, !isDormant)
					}
				case "archive-thread":
					if state.selectedThreadID != "" {
						state.archiveThread(state.selectedThreadID)
					}
				case "forward-thread":
					// 没有选中会话或会话不在列表里时 forwardSelectedThread 返回 false，不做任何事。
					if state.forwardSelectedThread() {
						view = "chat"
					}
				case "open-settings":
					state.closeShortcutHelp()
					openSettingsWindow(state, "")
				case "show-shortcuts":
					state.toggleShortcutHelp()
				case "jump-to-latest":
					// 与时间线上「跳到最新」按钮共用 chatState 上的同一方法。
					state.jumpChatToLatest(state.selectedThreadID)
				case "open-attention-inbox":
					state.toggleAttentionInbox()
				case "steward-start-topic":
					state.openStewardThread()
				case "add-project":
					state.promptAddProject()
				case "close-file-tab":
					// ⌘W：文件面板正在预览时先关预览；没有预览时菜单项直接关闭窗口，不会派发到这里。
					fileManagerCloseActiveTab(state)
				case "toggle-context":
					state.toggleContextPanel()
				case "toggle-memory":
					state.toggleMemoryPanel()
				case "command-palette":
					togglePalette(state)
				case "find-in-thread":
					// 会话内查找条：由 chat_find.go 渲染，菜单点击与 ⌘F 打开同一个浮条。
					state.openChatFind()
				case "toggle-sidebar":
					toggleSidebarPersisted(state)
				case "stop-reply":
					state.interruptCurrent()
				}
			}
			// 项目文档弹窗（快捷键 ⇧⌘K 与标题栏按钮经 toggleContextPanel 打开/关闭）
			state.applyPendingContextToggle()
			state.syncContextProject(false)

			// 整体主工作区内容
			renderMainArea := func() {
				ui.Column(c.Key("chat-wrapper")).Fill().Background(ThemeSurfaceMain(c)).Children(func() {
					switch view {
					case "tasks":
						// 任务与看板以根层大弹窗呈现，主工作区保持聊天视图
						ui.Column(c.Key("chat-terminal-col")).Fill().Children(func() {
							ui.Column(c.Key("chat-pane")).Grow(1).MinWidth(0).Children(func() {
								renderChat(c, state)
							})
							renderTerminalDock(c, state)
						})
					case "automations":
						renderAutomationWorkspace(c, state)
					default:
						renderChatArea := func() {
							ui.Column(c.Key("chat-terminal-col")).Fill().Children(func() {
								ui.Column(c.Key("chat-pane")).Grow(1).MinWidth(0).Children(func() {
									renderChat(c, state)
								})
								renderTerminalDock(c, state)
							})
						}

						// 右侧三个面板（文件管理 / 网页预览 / 改动）互斥，共用同一分栏，
						// 挂载、宽度夹取与持久化都交给 sidePanes 注册表。
						renderSidePaneSplit(c, state, renderChatArea)
					}
				})
			}

			renderShell(func() {
				renderSidebar(c, state)
			}, renderMainArea)

			// 仅供没有主事件循环的测试在聊天窗口内渲染设置。
			if st := inlineSettingsWindowState(state); st != nil {
				ui.Box(c.Key("settings-inline-window")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
					Background(ThemeWindowBg(c)).Children(func() {
					renderSettingsWindow(c, st)
				})
			}

			// 任务看板与新建任务 / 新建目标弹窗挂在根层，覆盖侧边栏与主区
			if view == "tasks" {
				renderKanbanWorkspace(c, state)
				renderKanbanNewTaskSheet(c, state)
				renderKanbanNewGoalSheet(c, state)
			}

			renderSidebarModals(c, state)
			renderGitHubClonePanel(c, state)
			renderContextModal(c, state)
			// 记忆面板（Nowledge）与项目文档同层，盖住侧边栏与主区。
			renderMemoryModal(c, state)
			// 本地图片大图浮层与上列弹窗同层，盖住侧边栏与主区。
			renderImagePreviewOverlay(c, state)
			// 表格展开浮窗同层渲染，不受消息列表裁切。
			renderTablePopupOverlay(c, state)
			// 快捷键浮层最后渲染，盖在其他弹窗之上。
			renderShortcutHelp(c, state)
			renderOnboardingDialog(c, state)
			renderCommandPalette(c, state)
		})
	}

	return state, view
}

// Root 导出原生桌面界面视图构造函数，供 cmd/mcode/main.go 作为 ui.View 的参数调用。
func Root(client Client, opts ...Option) func(c *ui.Context) {
	_, view := buildRoot(client, opts...)
	return view
}

func (s *chatState) selectAdjacentThread(offset int) bool {
	visible := s.visibleThreads()
	if len(visible) == 0 {
		return false
	}
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	target := 0
	if offset < 0 {
		target = len(visible) - 1
	}
	for i, thread := range visible {
		if thread.ID == selected {
			target = min(max(i+offset, 0), len(visible)-1)
			break
		}
	}
	s.closeWorkspace()
	s.selectThread(visible[target].ID)
	return true
}

// consumeKanbanEscape 处理任务看板里的 Esc：快速添加展开时收起并清空输入，搜索框有内容时清空搜索词，
// 新建任务 / 新建目标弹窗打开时先关闭弹窗。两者都没有内容时返回 false，由调用方关闭看板弹窗。
// 返回 true 表示 Esc 已被看板消费，看板弹窗应保持打开。
// mygo 的窗口级 Esc 先于输入框自身的快捷键收到按键，因此需要在根视图里判断。
func consumeKanbanEscape(s *chatState) bool {
	ks := getKanbanState(s)
	if ks == nil {
		return false
	}
	if kanbanProjectMenuOpen(s) {
		setKanbanProjectMenuOpen(s, false)
		return true
	}
	kanbanMu.Lock()
	consumed := false
	if ks.goalMenuOpen || ks.newMenuOpen {
		ks.goalMenuOpen = false
		ks.newMenuOpen = false
		consumed = true
	} else if ks.destination != "automations" && kanbanKeyboardClearSelection(s) {
		consumed = true
	} else if ks.goalDetailOpen {
		ks.goalDetailOpen = false
		consumed = true
	} else if ks.detailTaskID != "" || ks.planReviewGoalID != "" || ks.outputsGoalID != "" {
		// 详情自身处理 Esc；此处只阻止外层 sheet 同时关闭。
		consumed = true
	} else if ks.showNewTask {
		ks.showNewTask = false
		ks.newTaskTitle = ""
		ks.newTaskNotes = ""
		consumed = true
	} else if ks.showNewGoal {
		ks.showNewGoal = false
		ks.newGoalPrompt = ""
		consumed = true
	} else if ks.quickAddOpen {
		ks.quickAddOpen = false
		ks.quickAddText = ""
		consumed = true
	} else if ks.destination == "automations" && ks.automationSearch != "" {
		ks.automationSearch = ""
		consumed = true
	} else if ks.destination != "automations" && ks.searchText != "" {
		ks.searchText = ""
		consumed = true
	}
	kanbanMu.Unlock()
	if consumed {
		s.invalidate()
	}
	return consumed
}

// worktreeTargetProjectID 返回「在 Worktree 新建会话」的目标项目 ID：
// 优先当前选中会话所属项目，其次第一个项目；没有任何项目时返回空串。
func worktreeTargetProjectID(s *chatState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selectedThreadID != "" {
		for _, th := range s.threads {
			if th.ID == s.selectedThreadID && th.ProjectID != "" {
				return th.ProjectID
			}
		}
	}
	if len(s.projects) > 0 {
		return s.projects[0].ID
	}
	return ""
}

// visibleThreads 返回侧边栏当前可见的会话列表（按项目遍历与过滤后的顺序）。
func (s *chatState) visibleThreads() []Thread {
	s.mu.Lock()
	defer s.mu.Unlock()

	searchQueryPtr := getSearchQuery(s)
	searchQuery := ""
	if searchQueryPtr != nil {
		searchQuery = strings.TrimSpace(*searchQueryPtr)
	}
	lowerQuery := strings.ToLower(searchQuery)

	var visible []Thread
	for _, p := range s.projects {
		if s.collapsedProj != nil && s.collapsedProj[p.ID] {
			continue
		}
		for _, th := range s.threadsByProj[p.ID] {
			if lowerQuery == "" || strings.Contains(strings.ToLower(th.Title), lowerQuery) {
				visible = append(visible, th)
			}
		}
	}
	return visible
}
