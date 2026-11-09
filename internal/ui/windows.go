package mcodeui

import (
	"path/filepath"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/startuptrace"
)

var (
	chatWindowsMu sync.RWMutex
	chatWindows   = map[*mygo.Window]*chatState{}
	// pendingRevealThread 是通知点击先于窗口就绪时记下的待定位会话，由下一个建窗口的首次选中消费。
	pendingRevealThread string

	// chatWindowDefaultsMu 保护主窗口创建参数快照。
	chatWindowDefaultsMu sync.RWMutex
	// chatWindowDefaults 记录主窗口的创建参数：全部窗口关闭后，新建会话等操作据此重新开窗。
	chatWindowDefaults *chatWindowSpec

	// 首帧回调状态：首个聊天窗口绘制完首帧后，在主线程依次执行启动期登记的回调，
	// 用来把 Dock 图标这类不急着生效的系统设置挪到首帧之后。
	afterFirstFrameMu  sync.Mutex
	firstFrameShown    bool
	afterFirstFrameFns []func()
)

// AfterFirstFrame 登记首个聊天窗口绘制完首帧后执行的回调，每个回调只执行一次。
// 首帧已经发生过时立即在主线程执行；fn 为 nil 时忽略。
func AfterFirstFrame(fn func()) {
	if fn == nil {
		return
	}
	afterFirstFrameMu.Lock()
	if !firstFrameShown {
		afterFirstFrameFns = append(afterFirstFrameFns, fn)
		afterFirstFrameMu.Unlock()
		return
	}
	afterFirstFrameMu.Unlock()
	mygo.RunOnMain(fn)
}

// markFirstFrameShown 执行已登记的首帧回调，只生效一次（每个聊天窗口都注册了 OnReadyToShow）。
// 调用方在主线程上，所以回调直接调用，不再经 mygo.RunOnMain 转发。
func markFirstFrameShown() {
	afterFirstFrameMu.Lock()
	if firstFrameShown {
		afterFirstFrameMu.Unlock()
		return
	}
	firstFrameShown = true
	fns := afterFirstFrameFns
	afterFirstFrameFns = nil
	afterFirstFrameMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// 新窗口的项目列表由后台 goroutine 拉取，等它就绪再派发依赖项目的动作。
const (
	actionProjectWaitTimeout  = 5 * time.Second
	actionProjectPollInterval = 50 * time.Millisecond
)

// chatWindowSpec 保存主窗口的创建参数（client、标题与调用方选项）。
type chatWindowSpec struct {
	client Client
	title  string
	opts   []Option
}

// rememberChatWindowDefaults 记录主窗口的创建参数，只记第一次，重开窗口时沿用启动参数。
func rememberChatWindowDefaults(client Client, title string, opts []Option) {
	if client == nil {
		return
	}
	chatWindowDefaultsMu.Lock()
	if chatWindowDefaults == nil {
		chatWindowDefaults = &chatWindowSpec{
			client: client,
			title:  title,
			opts:   append([]Option(nil), opts...),
		}
	}
	chatWindowDefaultsMu.Unlock()
}

// chatWindowSpecSnapshot 返回主窗口创建参数的快照；尚未建过主窗口时 ok=false。
func chatWindowSpecSnapshot() (chatWindowSpec, bool) {
	chatWindowDefaultsMu.RLock()
	defer chatWindowDefaultsMu.RUnlock()
	if chatWindowDefaults == nil {
		return chatWindowSpec{}, false
	}
	return *chatWindowDefaults, true
}

// registerChatWindow 记录窗口与界面状态的对应关系，供菜单派发时定位目标状态。
func registerChatWindow(win *mygo.Window, state *chatState) {
	if win == nil || state == nil {
		return
	}
	chatWindowsMu.Lock()
	chatWindows[win] = state
	chatWindowsMu.Unlock()
}

// unregisterChatWindow 在窗口关闭后清除登记，避免留下已关闭的状态。
func unregisterChatWindow(win *mygo.Window) {
	if win == nil {
		return
	}
	chatWindowsMu.Lock()
	delete(chatWindows, win)
	chatWindowsMu.Unlock()
}

// activeChatState 返回聚焦窗口对应的状态；没有聚焦窗口时退化为任意存活窗口，都没有则返回 nil。
func activeChatState() *chatState {
	_, state := activeChatWindow()
	return state
}

// HasChatWindow 报告当前是否存在存活的聊天窗口。
// 全部窗口关闭后 Dock 点击、菜单栏等入口据此判断需要先开窗口。
func HasChatWindow() bool {
	return activeChatState() != nil
}

// activeChatWindow 返回聚焦窗口及其状态；没有聚焦窗口时退化为任意登记窗口，都没有则返回 nil。
func activeChatWindow() (*mygo.Window, *chatState) {
	chatWindowsMu.RLock()
	defer chatWindowsMu.RUnlock()
	if win := mygo.FocusedWindow(); win != nil {
		if state := chatWindows[win]; state != nil {
			return win, state
		}
	}
	for win, state := range chatWindows {
		if state != nil {
			return win, state
		}
	}
	return nil, nil
}

// IsViewingThread 报告用户此刻是否正看着指定会话：存在聚焦的窗口，且该窗口当前选中的
// 就是这条会话。桌面端据此不给用户眼前正在看的会话再弹一条通知。
// 没有聚焦窗口时返回 false：用户在看别的地方，通知仍然有意义。
func IsViewingThread(threadID string) bool {
	if threadID == "" {
		return false
	}
	win, state := activeChatWindow()
	if win == nil || state == nil || win.IsDestroyed() || !win.IsVisible() || !win.IsFocused() {
		return false
	}
	return state.chatSelectedThreadID() == threadID
}

// initialThreadID 返回窗口首次选中的会话：显式传入优先，否则消费通知点击留下的待定位会话。
func initialThreadID(threadID string) string {
	if threadID != "" {
		return threadID
	}
	chatWindowsMu.Lock()
	defer chatWindowsMu.Unlock()
	id := pendingRevealThread
	pendingRevealThread = ""
	return id
}

// ActivateThread 把界面切到指定会话：系统通知点击后据此定位到出错或完成的那个对话。
// 会话列表还没加载完时先记下目标，交给首次加载选中；窗口还没建好时留给新窗口的首次选中。
func ActivateThread(threadID string) {
	if threadID == "" {
		return
	}
	mygo.RunOnMain(func() {
		win, state := activeChatWindow()
		if win == nil || state == nil {
			chatWindowsMu.Lock()
			pendingRevealThread = threadID
			chatWindowsMu.Unlock()
			return
		}
		state.openWorkspace("chat")
		state.mu.Lock()
		loaded := len(state.threads) > 0
		if !loaded {
			state.initialThreadID = threadID
		}
		state.mu.Unlock()
		if loaded {
			state.selectThread(threadID)
		}
		if win.IsMinimized() {
			win.Restore()
		}
		win.Show()
		win.Focus()
	})
}

// WithWindow 注入获取当前宿主窗口的函数。
func WithWindow(get func() *mygo.Window) Option {
	return func(s *chatState) {
		s.window = get
	}
}

// WithOpenThreadWindow 注入在独立窗口打开指定会话的函数。
func WithOpenThreadWindow(fn func(threadID string)) Option {
	return func(s *chatState) {
		s.openThreadWindow = fn
	}
}

// WithInitialThread 注入首次加载时优先选中的会话 ID。
func WithInitialThread(threadID string) Option {
	return func(s *chatState) {
		s.initialThreadID = threadID
	}
}

// ensureChatWindow 返回可用的聊天窗口及其状态：没有存活窗口时用主窗口的创建参数重新开一个。
// 菜单栏与全局快捷键在全部窗口关闭后仍然可用，靠它先把窗口开出来。
func ensureChatWindow() (*mygo.Window, *chatState) {
	if win, state := activeChatWindow(); state != nil {
		return win, state
	}
	spec, ok := chatWindowSpecSnapshot()
	if !ok {
		// 主窗口还没建过（启动前），调用方无从开窗。
		return nil, nil
	}
	return openThreadWindow(spec.client, "", spec.title, spec.opts...)
}

// hasLoadedProjects 报告窗口是否已经拉到项目列表。
func (s *chatState) hasLoadedProjects() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.projects) > 0
}

// enqueueActionWhenReady 等窗口的项目列表就绪后再把菜单动作入队。
// 新窗口的项目在后台异步加载，动作立刻入队会跑在数据到位之前（新建会话此时只会提示
// “尚未加载到有效项目”）；等待超时后仍照常入队，让窗口给出原本的提示。
func (s *chatState) enqueueActionWhenReady(id string, index int) {
	if s == nil {
		return
	}
	go func() {
		deadline := time.Now().Add(actionProjectWaitTimeout)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			ready := len(s.projects) > 0
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			if ready {
				break
			}
			time.Sleep(actionProjectPollInterval)
		}
		s.enqueueAction(id, index)
	}()
}

// OpenNewThreadWindow 开一个主聊天窗口并在其中进入新建会话流程。
// 供全部窗口关闭后的 Dock 点击、应用重新激活等入口使用。
func OpenNewThreadWindow(client Client, title string, opts ...Option) *mygo.Window {
	win, state := openThreadWindow(client, "", title, opts...)
	state.enqueueActionWhenReady("new-thread", 0)
	return win
}

// OpenThreadWindow 创建并在独立窗口中展示指定会话，threadID 为空时创建主窗口。
// 该窗口使用独立的 chatState 与 Watch 订阅，共享同一个底层 client，关闭时不退出进程。
func OpenThreadWindow(client Client, threadID, title string, opts ...Option) *mygo.Window {
	win, _ := openThreadWindow(client, threadID, title, opts...)
	return win
}

// openThreadWindow 是 OpenThreadWindow 的内部实现，额外返回窗口对应的界面状态。
func openThreadWindow(client Client, threadID, title string, opts ...Option) (*mygo.Window, *chatState) {
	var (
		win   *mygo.Window
		state *chatState
	)

	allOpts := make([]Option, 0, len(opts)+4)
	allOpts = append(allOpts, opts...)
	allOpts = append(allOpts,
		WithInitialThread(initialThreadID(threadID)),
		WithWindow(func() *mygo.Window { return win }),
		WithOpenThreadWindow(func(id string) {
			OpenThreadWindow(client, id, title, opts...)
		}),
		WithOpenTasksWindow(func(view string) {
			if threadID != "" {
				OpenTasksWindow(client, view, opts...)
				return
			}
			OpenTasksWindow(client, view, append(opts, WithRevealThreadInMain(func(id string) {
				mygo.RunOnMain(func() {
					if state != nil {
						state.openWorkspace("chat")
						state.selectThread(id)
					}
					if win != nil {
						win.Show()
						win.Focus()
					}
				})
			}))...)
		}),
	)

	state, view := buildRoot(client, allOpts...)

	// 记录主窗口的创建参数，供全部窗口关闭后重新开窗复用。
	if threadID == "" {
		rememberChatWindowDefaults(client, title, opts)
	}

	if title == "" {
		title = "MCode"
	}
	stateKey := "thread-window"
	if threadID == "" {
		stateKey = "main-window"
	}

	// 主窗口保持不透明：透明窗口会让 WindowServer 每帧整窗 alpha 混合，
	// 实测运行中 GPU 占用约为不透明窗口的 3 倍。

	mygo.RunOnMain(func() {
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:                title,
			Width:                WindowDefaultWidth,
			Height:               WindowDefaultHeight,
			MinWidth:             WindowMinWidth, // 固定窗口最小宽度，不随侧边栏默认宽度变化
			MinHeight:            WindowMinHeight,
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 16},
			// Windows 上系统按钮画在右上角并填满 TitleBarHeight，设成聊天标题栏的实际高度
			// 才能与标题栏里的工具栏图标在同一行垂直居中；
			// macOS 用交通灯位置，这里返回 0 即不设置。
			TitleBarHeight: hiddenTitleBarHeight(titleBarGOOS, int(HeaderHeight)),
			StateKey:       stateKey,
			Content:        ui.View(view),
		})
		win.OnReadyToShow(func() {
			startuptrace.Mark("first frame ready")
			markFirstFrameShown()
		})
		registerChatWindow(win, state)
		// mygo 按需出帧：失焦或隐藏后这个窗口通常不再重绘，光靠每帧上报无法把「没在看」
		// 告诉应用层，所以在这几个焦点/可见性回调里各补一次上报（值没变时内部跳过）。
		win.OnFocus(func() {
			state.setWindowActive(true)
			state.reportForegroundThread()
		})
		win.OnBlur(func() {
			state.setWindowActive(false)
			state.reportForegroundThread()
		})
		win.OnHide(func() { state.reportForegroundThread() })

		win.OnClose(func(e *mygo.CloseEvent) {
			if threadID == "" {
				st := getChatSettings(state)
				if st != nil && st.cfg.Startup.QuitAction == config.QuitActionMinimizeToTray && st.cfg.Startup.ShowTrayIcon {
					e.PreventDefault()
					win.Hide()
					return
				}
			}
			if !closeSettingsWindowForOwner(state) {
				e.PreventDefault()
			}
		})

		win.OnClosed(func() {
			unregisterChatWindow(win)
			state.close()
		})
	})

	return win, state
}

// OpenMiniEditor 创建迷你源码编辑器独立窗口：860×640，行号列 + 多行编辑区，
// 停止输入约 800ms 后自动保存（写前检测外部改动），Markdown 文件可在源码/预览之间切换。
// 关闭窗口前立即落盘未保存的编辑。
func OpenMiniEditor(path string) *mygo.Window {
	state := newMiniEditorState(path)
	var win *mygo.Window

	mygo.RunOnMain(func() {
		title := filepath.Base(path)
		if title == "" || title == "." || title == string(filepath.Separator) {
			title = "源码编辑器"
		}
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:                title,
			Width:                MiniEditorDefaultWidth,
			Height:               MiniEditorDefaultHeight,
			MinWidth:             MiniEditorMinWidth,
			MinHeight:            MiniEditorMinHeight,
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 16},
			StateKey:             "mini-editor",
			Content: ui.View(func(c *ui.Context) {
				applyAppTheme(c)
				state.view(c)
			}),
		})
		win.OnClosed(func() {
			state.saveNow()
		})
	})

	return win
}

func (s *chatState) close() {
	s.mu.Lock()
	s.closed = true
	if s.draftSaveTimer != nil {
		s.draftSaveTimer.Stop()
		s.draftSaveTimer = nil
	}
	stop := s.watchStop
	s.watchStop = nil
	stopQueue := s.stopQueueWatch
	s.stopQueueWatch = nil
	stopBees := s.stopBeesWatch
	s.stopBeesWatch = nil
	stopSummary := s.stopSummaryWatch
	s.stopSummaryWatch = nil
	s.mu.Unlock()
	// 关闭前同步落盘一次草稿（仅开启持久化的窗口）。
	s.flushComposerDrafts()
	if stop != nil {
		stop()
	}
	if stopQueue != nil {
		stopQueue()
	}
	if stopBees != nil {
		stopBees()
	}
	if stopSummary != nil {
		stopSummary()
	}
	// 尾随刷新定时器持有 *chatState，关窗时必须一并清掉，否则定时器会拿着旧指针拉会话列表。
	resetBeeThreadsRefresh(s)
	closeEmbeddedBees(s)
	closeContextPanel(s)
	stopKanbanWatch(s)
	stopStewardTaskWatch(s)
	stopAutomationWatch(s)
	// 收件箱订阅与补拉定时器同样持有 *chatState，关窗时一并注销。
	stopAttentionWatch(s)
	swarmPanelMu.Lock()
	var stopSwarm func()
	if hp := swarmPanelStates[s]; hp != nil {
		stopSwarm = hp.watchStop
		delete(swarmPanelStates, s)
	}
	swarmPanelMu.Unlock()
	if stopSwarm != nil {
		stopSwarm()
	}
	closeTerminals(s)
	closePreview(s)
	previewMu.Lock()
	delete(previewStates, s)
	previewMu.Unlock()
	kanbanMu.Lock()
	delete(kanbanStates, s)
	kanbanMu.Unlock()
	automationMu.Lock()
	delete(automationStates, s)
	automationMu.Unlock()
	workspaceMu.Lock()
	delete(workspaceStates, s)
	workspaceMu.Unlock()
	diffPanelMu.Lock()
	delete(diffPanelStates, s)
	diffPanelMu.Unlock()
	// 文件管理面板状态同样按 chatState 存包级 map，不删会随关窗累积。
	fileManagerMu.Lock()
	delete(fileManagerStates, s)
	fileManagerMu.Unlock()
	paletteMu.Lock()
	delete(paletteStates, s)
	paletteMu.Unlock()
	settingsMu.Lock()
	delete(settingsList, s)
	settingsMu.Unlock()
	sidebarMu.Lock()
	delete(sidebarSearch, s)
	sidebarMu.Unlock()
	// 自建 worktree 识别缓存按 chatState 存包级 map，只增不删会在关窗后泄一整份会话状态。
	aiWorktreeMu.Lock()
	delete(aiWorktreeStates, s)
	aiWorktreeMu.Unlock()
	// 会话头部状态同样按 chatState 存在包级 map 里，不删会随关窗累积。
	releaseChatHeaderState(s)
	releaseForwardedContextState(s)
	releaseChatUIState(s)
	releaseThreadChangeStats(s)
}
