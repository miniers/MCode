package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 子代理（bee）的系统级独立窗口：内容与聊天区右下角的 bee 浮窗面板一致（顶栏 + 主体 + 输入框），
// 但不含侧边栏，也不占父窗口的布局。窗口关闭时释放它自己的嵌入状态；
// 同一个 bee 同时存在于浮窗与独立窗口是预期行为，两者各自持有一份 chatState。

const (
	beeWindowWidth     = 560
	beeWindowHeight    = 720
	beeWindowMinWidth  = 420
	beeWindowMinHeight = 480
	// beeWindowStripHeight 是自绘顶栏的高度，取交通灯中心（16 + 14/2 = 23pt）的两倍，
	// 顶栏里的图标与文字才能与左侧红绿灯垂直居中对齐；Windows 的系统按钮也填满这个高度。
	beeWindowStripHeight float32 = 46
	// beeWindowStripBaseInset 是顶栏在系统窗口按钮之外保留的常规水平内边距。
	beeWindowStripBaseInset float32 = 14
	// macOS 交通灯的标准几何：关闭按钮左上角离窗口左上角 (18, 16)，按钮直径 14、相邻间距 20。
	beeWindowTrafficX    float32 = 18
	beeWindowTrafficY    float32 = 16
	beeWindowTrafficSize float32 = 14
	beeWindowTrafficGap  float32 = 20
	// beeWindowTrafficRight 是绿灯（缩放按钮）右边缘的 x。
	beeWindowTrafficRight = beeWindowTrafficX + 2*beeWindowTrafficGap + beeWindowTrafficSize
	// beeWindowTrafficInset 是 macOS 顶栏左侧要给红绿灯让出的宽度：绿灯右边缘再留一个常规内边距。
	beeWindowTrafficInset = beeWindowTrafficRight + beeWindowStripBaseInset
	// beeWindowStateKey 让各子代理窗口共享同一份窗口尺寸与位置记忆。
	beeWindowStateKey = "bee-window"
)

// beeWindowEntry 记录某个 bee 的独立窗口与它自己的会话状态。
type beeWindowEntry struct {
	win   *mygo.Window
	state *chatState
}

var (
	beeWindowsMu sync.Mutex
	// beeWindows 以 bee 的会话 ID 为键，重复调用时聚焦已有窗口而不是再开一个。
	beeWindows = map[string]*beeWindowEntry{}
)

// beeWindowOpener 是 openBeeWindow 的出口，测试里替换成桩以避免真的创建窗口。
// 这里不写初始值（直接赋 openBeeWindowImpl 会构成包级初始化循环），
// nil 表示使用真实实现。
var beeWindowOpener func(parent *chatState, beeID string)

// openBeeWindow 打开（或聚焦）指定 bee 的独立窗口。
func openBeeWindow(parent *chatState, beeID string) {
	if beeWindowOpener != nil {
		beeWindowOpener(parent, beeID)
		return
	}
	openBeeWindowImpl(parent, beeID)
}

// openBeeWindowImpl 打开（或聚焦）指定 bee 的独立窗口。
// parent 提供 Swarm 状态与设置（每个窗口自己的 chatState 由它派生），beeID 是 bee 的会话 ID。
func openBeeWindowImpl(parent *chatState, beeID string) {
	if parent == nil || beeID == "" {
		return
	}
	mygo.RunOnMain(func() {
		beeWindowsMu.Lock()
		if entry := beeWindows[beeID]; entry != nil {
			beeWindowsMu.Unlock()
			entry.win.Show()
			entry.win.Focus()
			return
		}
		beeWindowsMu.Unlock()

		parent.mu.Lock()
		client := parent.client
		parent.mu.Unlock()
		if client == nil {
			return
		}

		// 建窗时的快照只用来起标题与同步会话；之后每帧都从父会话的 Swarm 状态重读。
		bee, found := findBeeForWindow(parent, beeID)
		if !found {
			bee = SwarmBee{ThreadID: beeID}
		}
		title := beeWindowTitle(parent, bee)

		child := newEmbeddedBeeState(parent)
		syncEmbeddedBeeState(parent, child, bee)

		entry := &beeWindowEntry{state: child}
		entry.win = mygo.NewWindow(mygo.WindowOptions{
			Title:                title,
			Width:                beeWindowWidth,
			Height:               beeWindowHeight,
			MinWidth:             beeWindowMinWidth,
			MinHeight:            beeWindowMinHeight,
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TrafficLightPosition: &mygo.Point{X: int(beeWindowTrafficX), Y: int(beeWindowTrafficY)},
			// Windows 上系统按钮填满 TitleBarHeight，设成自绘顶栏的高度，按钮与顶栏同一行居中。
			TitleBarHeight: hiddenTitleBarHeight(titleBarGOOS, int(beeWindowStripHeight)),
			StateKey:       beeWindowStateKey,
			Content:        ui.View(func(c *ui.Context) { renderBeeWindow(c, parent, beeID, child) }),
		})

		beeWindowsMu.Lock()
		beeWindows[beeID] = entry
		beeWindowsMu.Unlock()

		entry.win.OnClosed(func() {
			beeWindowsMu.Lock()
			if beeWindows[beeID] == entry {
				delete(beeWindows, beeID)
			}
			beeWindowsMu.Unlock()
			// 窗口自己的状态不在嵌入表里，必须在关闭时显式回收订阅与草稿。
			releaseEmbeddedBeeState(entry.state)
		})
	})
}

// beeWindowStripInsets 返回自绘顶栏左右两侧的内边距：macOS 让出交通灯区域（绿灯右边缘 + 常规内边距），
// 其他平台按窗口报告的按钮区域让位——Windows 的按钮在右侧，左侧只保留常规内边距，右侧再加上按钮宽度。
func beeWindowStripInsets(goos string, bar ui.TitleBar) (leading, trailing float32) {
	if goos == "darwin" {
		return beeWindowTrafficInset, beeWindowStripBaseInset
	}
	return max(beeWindowStripBaseInset, bar.Left), beeWindowStripBaseInset + bar.Right
}

// renderBeeWindow 渲染子代理独立窗口：顶栏（红绿灯留白 + 人设图标 + 名称 + 状态 + 时长）、
// 主体对话时间线与底部输入框。
func renderBeeWindow(c *ui.Context, parent *chatState, beeID string, child *chatState) {
	applyAppTheme(c)
	// 窗口自己的渲染服务：invalidate 要重绘本窗口，而不是父窗口。
	child.mu.Lock()
	child.setUIServices(c.Services())
	child.initialized = true
	child.mu.Unlock()

	// 状态和时长不是本窗口的事件驱动，但只有还在变化时才需要按秒取下一帧：
	// 未找到 bee 时保留每秒一次的重试（等它出现在 Swarm 状态里跟上），bee 未结束（运行中、在等用户
	// 回答/审批、待人工合并）或它的子会话仍在运行时继续跟随；进入终态且子会话空闲后不再排定时器，
	// 窗口不再无谓地每秒整窗重建。
	bee, found := findBeeForWindow(parent, beeID)
	if found {
		syncEmbeddedBeeState(parent, child, bee)
	}
	childRunning := beeWindowChildRunning(child, bee.ThreadID)
	background := 0
	if found {
		background = beeBackgroundTaskCount(child, bee.ThreadID)
	}
	// bee 已经结束但子会话里的后台任务还在跑时也要继续跟随，否则窗口会停在旧状态上。
	if !found || !swarmBeeDormant(bee, swarmBeeWaiting(parent, bee)) || childRunning || background > 0 {
		c.After(time.Second)
	}

	ui.Column(c.Key("bee-window-root")).Fill().Background(ThemeWindowBg(c)).Clip().Gap(0).Children(func() {
		stripLeading, stripTrailing := beeWindowStripInsets(titleBarGOOS, c.TitleBar())

		ui.Row(c.Key("bee-window-strip")).
			DragWindow().
			FillWidth().
			Height(beeWindowStripHeight).
			AlignItems(ui.Center).
			Padding(0, stripTrailing, 0, stripLeading).
			Gap(Space3).
			BorderWidth(0, 0, 1, 0).
			BorderColor(ThemeBorder(c)).
			Children(func() {
				if !found {
					ui.Text(c, "子代理").FontSize(TypeFootnote).FontWeight(600).TextColor(ThemeSecondaryText(c)).SingleLine()
					return
				}
				persona := findSwarmPersona(bee.Name)
				statusText := beeStatusText(bee.Status, childRunning, background)
				statusColor := ThemeSecondaryText(c)
				// 等待后台时用强调色，和仍在工作的 bee 区分开。
				if beeWaitingBackground(bee.Status, childRunning, background) {
					statusColor = ThemeAccent(c)
				}
				renderSwarmGlyph(c, "bee-window-glyph-"+bee.ThreadID, bee, 18)
				ui.Text(c, bee.Name).FontSize(TypeFootnote).FontWeight(600).TextColor(persona.Color).SingleLine()
				ui.Text(c, statusText).FontSize(TypeCaption).FontWeight(500).TextColor(statusColor)
				if d := formatSwarmDuration(bee.StartedAt, bee.FinishedAt); d != "" {
					ui.Text(c, d).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c).Alpha(0.8))
				}
			})

		ui.Column(c.Key("bee-window-body")).Grow(1).MinHeight(0).FillWidth().Clip().Children(func() {
			switch {
			case !found:
				ui.Column(c.Key("bee-window-empty")).
					Grow(1).
					FillWidth().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, "子代理已结束或不存在").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
					})
			default:
				renderBeeTimeline(c, child, bee)
			}
		})

		renderComposer(c, child)
	})
}

// beeBackgroundTaskCount 统计子会话消息里仍在后台跑的工具项：provider 托管的后台任务与
// 回合结束后仍在跑的命令。这里不走 backgroundTasksFor，因为那条路径会为子会话懒建一个空的 Swarm 面板。
func beeBackgroundTaskCount(child *chatState, threadID string) int {
	if child == nil {
		return 0
	}
	threadRunning := child.isThreadRunning(threadID)
	runningTurn := 0
	if threadRunning {
		runningTurn = child.maxMessagesTurn()
	}
	child.mu.Lock()
	messages := child.messages
	child.mu.Unlock()
	return len(backgroundProviderTasks(messages)) + len(backgroundRunningCommands(messages, threadRunning, runningTurn))
}

// beeWaitingBackground 报告 bee 是否已经结束、子会话也空闲，但子会话里还有后台任务在跑。
func beeWaitingBackground(status string, childRunning bool, background int) bool {
	return status != "running" && !childRunning && background > 0
}

// beeStatusText 拼出子代理顶栏的状态文字：运行中补上后台项数，已结束但后台还在跑时改成等待后台。
func beeStatusText(status string, childRunning bool, background int) string {
	if status == "running" {
		if background > 0 {
			return fmt.Sprintf("正在工作 · 后台 %d 项", background)
		}
		return "正在工作"
	}
	if beeWaitingBackground(status, childRunning, background) {
		return fmt.Sprintf("等待后台 · %d 项", background)
	}
	return swarmStatusLabel(status)
}

// beeWindowChildRunning 报告窗口自己的子会话里这个 bee 的会话是否仍在运行。
// 子代理结束后用户还可以在输入框里追问，那种运行不再由 bee 状态反映，只能看子会话自己，
// 并且要靠每秒重建跟随时长与状态。
func beeWindowChildRunning(child *chatState, beeID string) bool {
	if child == nil {
		return false
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	for _, th := range child.threads {
		if th.ID == beeID {
			return th.IsRunning || th.SwarmBusy
		}
	}
	return false
}

// findBeeForWindow 从父会话的 Swarm 状态里取指定 bee 的最新快照，读时持 swarmPanelMu。
func findBeeForWindow(parent *chatState, beeID string) (SwarmBee, bool) {
	hp := getSwarmPanelState(parent)
	if hp == nil {
		return SwarmBee{}, false
	}
	swarmPanelMu.Lock()
	defer swarmPanelMu.Unlock()
	for _, h := range hp.state.Bees {
		if h.ThreadID == beeID {
			return h, true
		}
	}
	return SwarmBee{}, false
}

// beeWindowTitle 取窗口标题：优先 bee 名称，其次父会话里该会话的标题。
func beeWindowTitle(parent *chatState, bee SwarmBee) string {
	if name := strings.TrimSpace(bee.Name); name != "" {
		return name
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	for _, th := range parent.threads {
		if th.ID == bee.ThreadID && strings.TrimSpace(th.Title) != "" {
			return th.Title
		}
	}
	return "子代理"
}
