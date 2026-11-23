package mcodeui

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestModelPickerFlyoutRisesFromBelow(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		t.Run(fmt.Sprintf("reduced=%t", reduced), func(t *testing.T) {
			setAppReduceMotion(t, reduced)
			var transition ui.ElementTransition
			tester := ui.NewTesterWith(func(c *ui.Context) {
				transition = appFlyoutTransition(c, Space4)
			}, 330, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
			tester.Frame()
			if reduced {
				if transition.Enter != nil {
					t.Fatal("减少动效时不应有入场位移")
				}
			} else if transition.Enter == nil || transition.Enter.Y != Space4 || transition.Enter.Opacity != 0 {
				t.Fatalf("入场动效错误: %+v", transition.Enter)
			}
		})
	}
}

func TestAppFlyoutLifecycle(t *testing.T) {
	oldRun, oldNew, oldDestroy, oldAnchor := flyoutRunOnMain, flyoutNew, flyoutDestroy, flyoutSetAnchor
	t.Cleanup(func() {
		flyoutRunOnMain, flyoutNew, flyoutDestroy, flyoutSetAnchor = oldRun, oldNew, oldDestroy, oldAnchor
	})
	var queue []func()
	flyoutRunOnMain = func(fn func()) { queue = append(queue, fn) }
	var callbacks []func()
	var opts []mygo.FlyoutOptions
	flyoutNew = func(o mygo.FlyoutOptions, _ func(*mygo.CloseEvent), closed func()) *mygo.Window {
		opts = append(opts, o)
		callbacks = append(callbacks, closed)
		return &mygo.Window{}
	}
	var destroyed []*mygo.Window
	flyoutDestroy = func(w *mygo.Window) { destroyed = append(destroyed, w) }
	var anchors []mygo.Rectangle
	flyoutSetAnchor = func(_ *mygo.Window, r mygo.Rectangle) { anchors = append(anchors, r) }
	var mu sync.Mutex
	var f appFlyout
	var closed int
	create := func() {
		f.create(&mu, appFlyoutOptions(&mygo.Window{}, mygo.Rectangle{}, 100), nil, func() { closed++ })
	}
	update := func(r mygo.Rectangle) {
		mu.Lock()
		action := f.updateLocked(&mu, r, create)
		mu.Unlock()
		if action != nil {
			action()
		}
	}
	r1, r2 := mygo.Rectangle{X: 1, Width: 10}, mygo.Rectangle{X: 2, Width: 10}
	update(r1)
	update(r1)
	update(r2)
	if len(queue) != 1 {
		t.Fatalf("创建操作数量: %d", len(queue))
	}
	queue[0]()
	queue = nil
	if len(opts) != 1 || opts[0].Anchor != r2 {
		t.Fatalf("创建或待创建时的锚点错误: %+v", opts)
	}
	update(r2)
	if len(queue) != 0 || len(anchors) != 0 {
		t.Fatal("锚点未变化时不应定位")
	}
	update(r1)
	update(r2)
	for _, fn := range queue {
		fn()
	}
	queue = nil
	if len(anchors) != 1 || anchors[0] != r2 {
		t.Fatalf("过期锚点不应覆盖最新锚点: %v", anchors)
	}
	first := f.window
	mu.Lock()
	dispose := f.destroyLocked()
	again := f.destroyLocked()
	mu.Unlock()
	if dispose != nil {
		dispose()
	}
	if again != nil {
		again()
	}
	for _, fn := range queue {
		fn()
	}
	queue = nil
	if len(destroyed) != 1 || destroyed[0] != first {
		t.Fatalf("重复销毁: %v", destroyed)
	}
	update(r1)
	queue[0]()
	queue = nil
	second := f.window
	callbacks[0]()
	if f.window != second || closed != 0 {
		t.Fatal("旧关闭回调影响新窗口")
	}
	callbacks[1]()
	if f.window != nil || closed != 1 {
		t.Fatal("当前窗口关闭未通知")
	}
}

func TestAppFlyoutAnchorAndOptions(t *testing.T) {
	r, ok := flyoutAnchor(ui.Rect{X: -10.2, Y: 20.2, W: 30.1, H: 40.1}, 1.3)
	if !ok || r != (mygo.Rectangle{X: -14, Y: 26, Width: 40, Height: 53}) {
		t.Fatalf("锚点: %+v %v", r, ok)
	}
	if _, ok := flyoutAnchor(ui.Rect{W: 1, H: 1}, 0); ok {
		t.Fatal("零缩放不可用")
	}
	parent := &mygo.Window{}
	o := appFlyoutOptions(parent, r, 123)
	if o.Parent != parent || o.Anchor != r || o.Placement != mygo.PlacementBottomStart || o.Gap != int(Space1) || o.Width != 123 || o.MaxWidth != 123 || !o.FitContent || !o.Focusable || !o.Shadow || o.Popover || o.CornerRadius != int(RadiusMD) {
		t.Fatalf("参数: %+v", o)
	}
}

func TestAppFlyoutCreationCancellationAndMovingAnchor(t *testing.T) {
	oldRun, oldNew, oldDestroy, oldAnchor := flyoutRunOnMain, flyoutNew, flyoutDestroy, flyoutSetAnchor
	t.Cleanup(func() {
		flyoutRunOnMain, flyoutNew, flyoutDestroy, flyoutSetAnchor = oldRun, oldNew, oldDestroy, oldAnchor
	})
	var queue []func()
	flyoutRunOnMain = func(fn func()) { queue = append(queue, fn) }
	var mu sync.Mutex
	var f appFlyout
	var creations int
	var anchors []mygo.Rectangle
	flyoutSetAnchor = func(_ *mygo.Window, r mygo.Rectangle) { anchors = append(anchors, r) }
	flyoutDestroy = func(*mygo.Window) {}
	newAnchor := mygo.Rectangle{X: 5, Width: 10}
	flyoutNew = func(_ mygo.FlyoutOptions, _ func(*mygo.CloseEvent), _ func()) *mygo.Window {
		creations++
		mu.Lock()
		f.updateLocked(&mu, newAnchor, nil)
		mu.Unlock()
		return &mygo.Window{}
	}
	create := func() { f.create(&mu, appFlyoutOptions(&mygo.Window{}, mygo.Rectangle{}, 100), nil, nil) }
	mu.Lock()
	first := f.updateLocked(&mu, mygo.Rectangle{Width: 10}, create)
	f.destroyLocked()
	second := f.updateLocked(&mu, mygo.Rectangle{Width: 10}, create)
	mu.Unlock()
	first()
	second()
	for _, fn := range queue {
		fn()
	}
	if creations != 1 || len(anchors) != 1 || anchors[0] != newAnchor {
		t.Fatalf("取消或创建期间移动锚点失败: 创建 %d 次，锚点 %v", creations, anchors)
	}
}

func TestFlyoutHostWindow(t *testing.T) {
	var services, other ui.Services
	ui.NewTester(func(c *ui.Context) { services = c.Services() }, 10, 10).Frame()
	ui.NewTester(func(c *ui.Context) { other = c.Services() }, 10, 10).Frame()
	settingsWindowMu.Lock()
	old := settingsWindow
	settings := &mygo.Window{}
	settingsWindow = &settingsWindowEntry{win: settings, services: services}
	settingsWindowMu.Unlock()
	t.Cleanup(func() { settingsWindowMu.Lock(); settingsWindow = old; settingsWindowMu.Unlock() })
	if flyoutHostWindow(services) != settings {
		t.Fatal("独立设置窗口未找到")
	}
	if flyoutHostWindow(other) != nil || flyoutHostWindow(ui.Services{}) != nil {
		t.Fatal("不应匹配其他或空服务")
	}
	settingsWindowMu.Lock()
	settingsWindow = nil
	settingsWindowMu.Unlock()
	chatWindow := &mygo.Window{}
	chat := &chatState{}
	chat.setUIServices(services)
	chatWindowsMu.Lock()
	chatWindows[chatWindow] = chat
	chatWindowsMu.Unlock()
	if flyoutHostWindow(services) != chatWindow {
		t.Fatal("聊天窗口未找到")
	}
	chatWindowsMu.Lock()
	delete(chatWindows, chatWindow)
	chatWindowsMu.Unlock()
	beeWindow := &mygo.Window{}
	beeWindowsMu.Lock()
	beeWindows["flyout-test"] = &beeWindowEntry{win: beeWindow, state: chat}
	beeWindowsMu.Unlock()
	t.Cleanup(func() { beeWindowsMu.Lock(); delete(beeWindows, "flyout-test"); beeWindowsMu.Unlock() })
	if flyoutHostWindow(services) != beeWindow {
		t.Fatal("子代理窗口未找到")
	}
	beeWindowsMu.Lock()
	delete(beeWindows, "flyout-test")
	beeWindowsMu.Unlock()
	tasksWindowMu.Lock()
	oldTaskWindow, oldTaskState := tasksWindow, tasksWindowState
	taskWindow := &mygo.Window{}
	tasksWindow, tasksWindowState = taskWindow, chat
	tasksWindowMu.Unlock()
	t.Cleanup(func() {
		tasksWindowMu.Lock()
		tasksWindow, tasksWindowState = oldTaskWindow, oldTaskState
		tasksWindowMu.Unlock()
	})
	if flyoutHostWindow(services) != taskWindow || flyoutHostWindow(other) != nil {
		t.Fatal("任务窗口服务匹配错误")
	}
}

func setupPopoverFlyoutTest(t *testing.T) (*settingsWindowEntry, *swarmTierFlyoutCapture) {
	t.Helper()
	entry := &settingsWindowEntry{win: &mygo.Window{}}
	capture := &swarmTierFlyoutCapture{}
	settingsWindowMu.Lock()
	old := settingsWindow
	settingsWindow = entry
	settingsWindowMu.Unlock()
	oldRun, oldNew, oldDestroy, oldFocus, oldAnchor := flyoutRunOnMain, flyoutNew, flyoutDestroy, flyoutFocus, flyoutSetAnchor
	flyoutRunOnMain = func(fn func()) { fn() }
	flyoutNew = func(o mygo.FlyoutOptions, close func(*mygo.CloseEvent), closed func()) *mygo.Window {
		capture.options = append(capture.options, o)
		capture.closes = append(capture.closes, close)
		capture.closed = append(capture.closed, closed)
		return &mygo.Window{}
	}
	flyoutDestroy = func(w *mygo.Window) { capture.destroyedWindows = append(capture.destroyedWindows, w) }
	flyoutFocus = func(*mygo.Window) { capture.focused++ }
	flyoutSetAnchor = func(_ *mygo.Window, r mygo.Rectangle) { capture.anchors = append(capture.anchors, r) }
	t.Cleanup(func() {
		flyoutRunOnMain, flyoutNew, flyoutDestroy, flyoutFocus, flyoutSetAnchor = oldRun, oldNew, oldDestroy, oldFocus, oldAnchor
		settingsWindowMu.Lock()
		settingsWindow = old
		settingsWindowMu.Unlock()
		appFlyoutPopoverMu.Lock()
		for k := range appFlyoutPopovers {
			if k.services == entry.services {
				delete(appFlyoutPopovers, k)
			}
		}
		clear(appFlyoutHeights)
		appFlyoutPopoverMu.Unlock()
	})
	return entry, capture
}

func popoverTestView(t *testing.T, services ui.Services, key string) func(*ui.Context) {
	t.Helper()
	appFlyoutPopoverMu.Lock()
	defer appFlyoutPopoverMu.Unlock()
	entry := appFlyoutPopovers[appFlyoutPopoverKey{services, key}]
	if entry == nil || entry.render == nil {
		t.Fatal("浮层视图不存在")
	}
	return entry.render
}

func TestFlyoutComposerKeysAreIndependent(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	mainKey := composerKeyPrefix(&chatState{}, "lead") + "model-popover"
	beeKey := composerKeyPrefix(&chatState{embeddedBee: true}, "bee") + "model-popover"
	mainOpen, beeOpen := true, false
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		mainAnchor := ui.ButtonBase(c.Key("main-anchor")).Size(100, 30)
		appFlyoutPopover(c, mainKey, mainAnchor, &mainOpen, nil, appFlyoutProps{Width: 120}, func(*ui.Context) {})
		beeAnchor := ui.ButtonBase(c.Key("bee-anchor")).Size(100, 30)
		appFlyoutPopover(c, beeKey, beeAnchor, &beeOpen, nil, appFlyoutProps{Width: 120}, func(*ui.Context) {})
	}, 400, 300)
	host.Frame()
	host.Frame()
	appFlyoutPopoverMu.Lock()
	openEntry := appFlyoutPopovers[appFlyoutPopoverKey{entry.services, mainKey}]
	appFlyoutPopoverMu.Unlock()
	if openEntry == nil || len(capture.options) != 1 || len(capture.destroyedWindows) != 0 {
		t.Fatal("关闭子代理浮层不应销毁主输入框浮层")
	}
}

func TestFlyoutPopoverLifecycle(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	open, dismissed, dismissCount, frames, version := false, 0, 0, 0, 1
	var bounds ui.Rect
	host := ui.NewTester(func(c *ui.Context) {
		frames++
		entry.services = c.Services()
		trigger := ui.ButtonBase(c.Key("trigger")).Label("打开").Size(100, 30).OnClick(func() { open = true })
		bounds = trigger.Bounds()
		currentVersion := version
		appFlyoutPopover(c, "test-popover", trigger, &open, func() { dismissed = currentVersion; dismissCount++ }, appFlyoutProps{Width: 120}, func(c *ui.Context) {
			if currentVersion == 1 {
				ui.Text(c, "旧内容")
			} else {
				ui.Text(c, "新内容")
			}
		})
	}, 400, 300)
	host.SetZoom(1.3)
	host.Frame()
	if err := host.Click("打开"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 {
		t.Fatalf("窗口数: %d", len(capture.options))
	}
	o := capture.options[0]
	r, _ := flyoutAnchor(bounds, 1.3)
	if o.Parent != entry.win || o.Anchor != r || !o.FitContent || o.Popover || !o.Focusable || capture.closes[0] != nil {
		t.Fatalf("窗口参数: %+v, 锚点: %+v", o, r)
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "test-popover"), 120, 200)
	menu.Frame()
	before := frames
	host.Frame()
	host.Frame()
	if frames != before+2 || len(capture.anchors) != 0 {
		t.Fatalf("宿主重绘或锚点异常: %d, %v", frames-before, capture.anchors)
	}
	if !menu.FramePending() {
		t.Fatal("宿主重绘未通知浮层")
	}
	menu.Frame()
	if frames != before+2 || host.FramePending() {
		t.Fatal("浮层重绘不应触发宿主重绘")
	}
	open = false
	host.Frame()
	if len(capture.destroyedWindows) != 1 {
		t.Fatal("关闭时未销毁")
	}
	open = true
	host.Frame()
	if len(capture.options) != 2 {
		t.Fatal("重新打开失败")
	}
	capture.closed[0]()
	host.Frame()
	if dismissed != 0 || !open || len(capture.options) != 2 {
		t.Fatal("旧窗口关闭影响新窗口")
	}
	// 更新闭包后，关闭事件必须调用最新版本。
	version = 2
	host.Frame()
	menu = ui.NewTester(popoverTestView(t, entry.services, "test-popover"), 120, 200)
	if !menu.HasText("新内容") {
		t.Fatal("浮层未使用最新内容")
	}
	capture.closed[1]()
	if dismissed != 0 || !open || capture.focused != 1 || !host.FramePending() {
		t.Fatal("关闭回调不应在宿主帧外执行")
	}
	host.Frame()
	host.Frame()
	if dismissed != 2 || dismissCount != 1 || open || len(capture.options) != 2 {
		t.Fatal("关闭事件未通知宿主或恢复焦点")
	}
}

func TestFlyoutPopoverClosesWhenHostUnmounts(t *testing.T) {
	t.Run("宿主卸载", func(t *testing.T) {
		entry, capture := setupPopoverFlyoutTest(t)
		show, open := true, false
		host := ui.NewTester(func(c *ui.Context) {
			entry.services = c.Services()
			if show {
				trigger := ui.ButtonBase(c.Key("trigger")).Label("打开").Size(100, 30).OnClick(func() { open = true })
				appFlyoutPopover(c, "test-popover", trigger, &open, nil, appFlyoutProps{Width: 120}, func(*ui.Context) {})
			}
		}, 400, 300)
		host.Frame()
		if err := host.Click("打开"); err != nil {
			t.Fatal(err)
		}
		if len(capture.options) != 1 {
			t.Fatalf("窗口数: %d", len(capture.options))
		}
		show = false
		host.Frame()
		host.Frame()
		appFlyoutPopoverMu.Lock()
		_, exists := appFlyoutPopovers[appFlyoutPopoverKey{entry.services, "test-popover"}]
		appFlyoutPopoverMu.Unlock()
		if len(capture.destroyedWindows) != 1 || exists {
			t.Fatalf("卸载后销毁窗口数: %d，条目存在: %t", len(capture.destroyedWindows), exists)
		}
	})
	t.Run("切换浮层键", func(t *testing.T) {
		entry, capture := setupPopoverFlyoutTest(t)
		key, open := "session-one", false
		host := ui.NewTester(func(c *ui.Context) {
			entry.services = c.Services()
			trigger := ui.ButtonBase(c.Key("trigger")).Label("打开").Size(100, 30).OnClick(func() { open = true })
			appFlyoutPopover(c, key, trigger, &open, nil, appFlyoutProps{Width: 120}, func(*ui.Context) {})
		}, 400, 300)
		host.Frame()
		if err := host.Click("打开"); err != nil {
			t.Fatal(err)
		}
		if len(capture.options) != 1 {
			t.Fatalf("窗口数: %d", len(capture.options))
		}
		key = "session-two"
		host.Frame()
		host.Frame()
		appFlyoutPopoverMu.Lock()
		_, oldExists := appFlyoutPopovers[appFlyoutPopoverKey{entry.services, "session-one"}]
		newEntry := appFlyoutPopovers[appFlyoutPopoverKey{entry.services, "session-two"}]
		appFlyoutPopoverMu.Unlock()
		if len(capture.options) != 2 || len(capture.destroyedWindows) != 1 || oldExists || newEntry == nil || newEntry.window == nil {
			t.Fatalf("切换后创建窗口数: %d，销毁窗口数: %d，旧条目存在: %t，新条目: %v", len(capture.options), len(capture.destroyedWindows), oldExists, newEntry)
		}
	})
}

func TestFlyoutPopoverDismissWithoutCallback(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	open := true
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		trigger := ui.ButtonBase(c.Key("trigger")).Label("锚点").Size(100, 30)
		appFlyoutPopover(c, "nil-dismiss", trigger, &open, nil, appFlyoutProps{Width: 120}, func(c *ui.Context) { ui.Text(c, "内容") })
	}, 400, 300)
	host.Frame()
	if len(capture.options) != 1 {
		t.Fatal("未创建浮层")
	}
	capture.closed[0]()
	if !open || !host.FramePending() {
		t.Fatal("关闭应等待宿主帧写回")
	}
	host.Frame()
	host.Frame()
	if open || len(capture.options) != 1 {
		t.Fatal("关闭未写回或浮层重新创建")
	}
}

func TestFlyoutPopoverNestedLifecycle(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	parentOpen, childOpen := true, true
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		anchor := ui.ButtonBase(c.Key("parent-anchor")).Size(100, 30)
		appFlyoutPopover(c, "parent", anchor, &parentOpen, nil, appFlyoutProps{Width: 150}, func(c *ui.Context) {
			anchor := ui.ButtonBase(c.Key("child-anchor")).Size(80, 30)
			appFlyoutPopover(c, "child", anchor, &childOpen, nil, appFlyoutProps{Width: 100}, func(c *ui.Context) { ui.Text(c, "子浮层") })
		})
	}, 300, 300)
	host.Frame()
	parent := ui.NewTester(popoverTestView(t, entry.services, "parent"), 150, 200)
	parent.Frame()
	parent.Frame()
	if len(capture.options) != 2 {
		t.Fatalf("浮层数量: %d", len(capture.options))
	}
	appFlyoutPopoverMu.Lock()
	parentWindow := appFlyoutPopovers[appFlyoutPopoverKey{entry.services, "parent"}].window
	var childWindow *mygo.Window
	for _, e := range appFlyoutPopovers {
		if e.parent == parentWindow {
			childWindow = e.window
		}
	}
	appFlyoutPopoverMu.Unlock()
	if capture.options[1].Parent != parentWindow || childWindow == nil {
		t.Fatal("子浮层父窗口错误")
	}
	capture.closed[1]()
	parent.Frame()
	if childOpen || !parentOpen || capture.focused != 1 {
		t.Fatal("子浮层关闭后应恢复父浮层焦点")
	}
	childOpen = true
	parent.Frame()
	appFlyoutPopoverMu.Lock()
	for _, e := range appFlyoutPopovers {
		if e.parent == parentWindow {
			childWindow = e.window
		}
	}
	appFlyoutPopoverMu.Unlock()
	parentOpen = false
	host.Frame()
	if len(capture.destroyedWindows) != 2 || capture.destroyedWindows[0] != childWindow || capture.destroyedWindows[1] != parentWindow {
		t.Fatalf("销毁顺序错误: %v", capture.destroyedWindows)
	}
}

func TestFlyoutSessionStats(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	s := &chatState{}
	t.Cleanup(func() { setSessionStatsOpen(s, false) })
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		renderSessionStatsButton(c, s, "thread", "test")
	}, 400, 300)
	if err := host.Click("会话统计"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 || capture.options[0].Content == nil {
		t.Fatal("未创建会话统计窗口")
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "test-stats-popover"), 330, 300)
	if !menu.HasText("暂无统计数据") {
		t.Fatalf("缺少统计内容: %v", menu.Texts())
	}
}

// TestFlyoutSessionStatsTicksAndStopsNative 验证原生独立窗口里，宿主不出新帧、
// 仅内容窗口自身时钟推进时总用时逐秒增长；主轮完成后冻结且不再续帧；
// 关闭后内容不再渲染也不再调度（用渲染计数与 FramePending 观察，不单靠文本不变）。
func TestFlyoutSessionStatsTicksAndStopsNative(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	var s *chatState
	renders := 0
	t.Cleanup(func() { setSessionStatsOpen(s, false) })
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		if s == nil {
			s, _ = newSessionStatsLiveFixture(c)
		}
		renderSessionStatsButton(c, s, "t1", "test")
	}, 400, 300)
	if err := host.Click("会话统计"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 {
		t.Fatal("未创建会话统计窗口")
	}
	view := popoverTestView(t, entry.services, "test-stats-popover")
	anchored := false
	menu := ui.NewTester(func(c *ui.Context) {
		renders++
		if !anchored {
			anchored = true
			// 内容窗口与宿主是两套时钟：把运行轮起点重新对齐到内容窗口的整秒，避免跨秒边界漂移。
			start := c.Now().Truncate(time.Second).Add(-5 * time.Second).Format(time.RFC3339Nano)
			s.mu.Lock()
			for i := range s.messages {
				if s.messages[i].TurnIndex != 3 {
					continue
				}
				if s.messages[i].CreatedAt != "" {
					s.messages[i].CreatedAt = start
				}
				if s.messages[i].TurnStartedAt != "" {
					s.messages[i].TurnStartedAt = start
				}
			}
			s.mu.Unlock()
		}
		view(c)
	}, 330, 400)
	if !hasExactText(menu, "1m 05s") {
		t.Fatalf("总用时应含历史累计与当前轮: %v", menu.Texts())
	}
	for secs := 6; secs <= 7; secs++ {
		menu.Advance(time.Second)
		if !hasExactText(menu, fmt.Sprintf("1m %02ds", secs)) {
			t.Fatalf("宿主无新帧时内容窗口应自行读秒到 1m %02ds: %v", secs, menu.Texts())
		}
	}
	// 主轮完成：统计冻结，不再续帧（用渲染计数观察，FramePending 不含 After 定时器）。
	s.mu.Lock()
	done := menu.Now().Add(-time.Second)
	s.messages[6].TurnStatus = "completed"
	s.messages[6].TurnCompletedAt = done.Format(time.RFC3339Nano)
	s.mu.Unlock()
	menu.Frame()
	if !hasExactText(menu, "1m 06s") {
		t.Fatalf("主轮完成后应冻结在 1m 06s: %v", menu.Texts())
	}
	before := renders
	menu.Advance(2 * time.Second)
	if renders != before || menu.FramePending() {
		t.Fatalf("终态不应再续帧: %d -> %d", before, renders)
	}
	if !hasExactText(menu, "1m 06s") {
		t.Fatalf("终态不应再读秒: %v", menu.Texts())
	}
	// 关闭浮层：宿主帧销毁窗口；强制再渲染一帧，内容被 active 守卫拦住，不再调度。
	if err := host.Click("会话统计"); err != nil {
		t.Fatal(err)
	}
	if len(capture.destroyedWindows) != 1 {
		t.Fatalf("关闭后应销毁窗口: %v", capture.destroyedWindows)
	}
	menu.Frame()
	if menu.HasText("总用时") {
		t.Fatalf("关闭后内容不应再渲染: %v", menu.Texts())
	}
	if menu.FramePending() {
		t.Fatal("关闭后不应继续调度")
	}
	before = renders
	menu.Advance(3 * time.Second)
	if renders != before || menu.FramePending() {
		t.Fatalf("关闭后仍在渲染或调度: %d -> %d", before, renders)
	}
}

// TestFlyoutSessionStatsFallbackTicksAndStops 验证无原生宿主时 fallback 浮层的
// 打开读秒与关闭停止：起点 0 秒也安排刷新，关闭后内容消失且宿主不再续帧。
// 用减少动效运行：既验证读秒不受 ReduceMotion 影响，也避免浮层升起动画移动 After 相位；
// 正常动效下的读秒由浮层内容测试覆盖。
func TestFlyoutSessionStatsFallbackTicksAndStops(t *testing.T) {
	setAppReduceMotion(t, true)
	var s *chatState
	tt := ui.NewTesterWith(func(c *ui.Context) {
		if s == nil {
			s = &chatState{selectedThreadID: "t1", threads: []Thread{{ID: "t1", IsRunning: true}}}
			start := c.Now().Truncate(time.Second).Format(time.RFC3339Nano)
			s.messages = []Message{{Role: "user", TurnIndex: 1, CreatedAt: start}}
		}
		ui.Box(c).Fill().Children(func() {
			renderSessionStatsButton(c, s, "t1", "fb")
		})
	}, 400, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	t.Cleanup(func() { setSessionStatsOpen(s, false) })
	if err := tt.Click("会话统计"); err != nil {
		t.Fatal(err)
	}
	if !hasExactText(tt, "0s") {
		t.Fatalf("fallback 打开后应从 0s 读秒: %v", tt.Texts())
	}
	tt.Advance(2 * time.Second)
	if !hasExactText(tt, "2s") {
		t.Fatalf("fallback 应从 0s 逐秒读秒: %v", tt.Texts())
	}
	if err := tt.Click("会话统计"); err != nil {
		t.Fatal(err)
	}
	if sessionStatsOpen(s) || tt.HasText("总用时") {
		t.Fatalf("再次点击应关闭浮层: open=%v %v", sessionStatsOpen(s), tt.Texts())
	}
	tt.Advance(3 * time.Second)
	if tt.FramePending() {
		t.Fatal("关闭后宿主不应继续调度")
	}
	if tt.HasText("总用时") {
		t.Fatalf("关闭后内容不应再出现: %v", tt.Texts())
	}
}

func TestFlyoutAutomationProject(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	entry, capture := setupPopoverFlyoutTest(t)
	s, view := buildTasksRoot(nil, WithTasksWindowMode("automations"))
	defer s.close()
	s.projects = []Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}}
	tasksWindowMu.Lock()
	oldWindow, oldState := tasksWindow, tasksWindowState
	tasksWindow, tasksWindowState = entry.win, s
	tasksWindowMu.Unlock()
	t.Cleanup(func() {
		tasksWindowMu.Lock()
		tasksWindow, tasksWindowState = oldWindow, oldState
		tasksWindowMu.Unlock()
	})
	host := ui.NewTester(view, 1140, 720)
	host.Frame()
	entry.services = s.uiServices()
	// 让任务窗口而非测试设置窗口匹配宿主。
	settingsWindowMu.Lock()
	settingsWindow = nil
	settingsWindowMu.Unlock()
	if err := host.Click("新建自动化"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 || capture.options[0].Parent != entry.win || capture.options[0].Content == nil {
		t.Fatal("未创建任务窗口项目浮层")
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "automation-choose-project"), 320, 300)
	if !menu.HasText("项目二") {
		t.Fatalf("缺少项目: %v", menu.Texts())
	}
	if err := menu.Click("项目二"); err != nil {
		t.Fatal(err)
	}
	as := getAutomationState(s)
	if as.chooseProjectOpen || !as.editorOpen || as.editorProjectID != "p2" || len(capture.destroyedWindows) != 1 {
		t.Fatal("选择项目后未关闭浮层或打开编辑器")
	}
}

func TestFlyoutPopoverFallback(t *testing.T) {
	open := true
	host := ui.NewTester(func(c *ui.Context) {
		trigger := ui.ButtonBase(c.Key("trigger")).Label("锚点").Size(100, 30)
		appFlyoutPopover(c, "fallback", trigger, &open, nil, appFlyoutProps{Width: 120}, func(c *ui.Context) { ui.Text(c, "退回内容") })
	}, 400, 300)
	host.Frame()
	if !host.HasText("退回内容") {
		t.Fatalf("窗口内浮层不存在: %v", host.Texts())
	}
}

func TestFlyoutDiffTurnMenu(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	s := &chatState{}
	dp := getDiffPanelState(s)
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		renderDiffTurnSelector(c, s, dp, ThreadDiff{Turns: []DiffTurn{{TurnID: "one", Index: 0, HasChanges: true}}}, "")
	}, 400, 300)
	if err := host.Click("整个会话"); err != nil {
		t.Fatal(err)
	}
	if len(capture.options) != 1 {
		t.Fatalf("窗口数: %d", len(capture.options))
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "diff-turn-popover"), 120, 200)
	if !menu.HasText("第 1 轮") {
		t.Fatalf("缺少轮次: %v", menu.Texts())
	}
	if err := menu.Click("整个会话"); err != nil {
		t.Fatal(err)
	}
	if len(capture.destroyedWindows) != 1 {
		t.Fatal("选择后未关闭")
	}
}

func TestFlyoutNewThreadProjectMenu(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	entry, capture := setupPopoverFlyoutTest(t)
	s := &chatState{closed: true, projects: []Project{{ID: "p1", Name: "项目Alpha"}, {ID: "p2", Name: "项目Beta"}}, threads: []Thread{{ID: "t1", ProjectID: "p1"}}, threadsByProj: map[string][]Thread{"p2": {{ID: "t2"}}}}
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		renderNewThreadPrompt(c, s, "t1")
	}, 800, 400)
	if err := host.Click("切换项目"); err != nil {
		t.Fatalf("%v: %v", err, host.Texts())
	}
	if len(capture.options) != 1 {
		t.Fatalf("窗口数: %d", len(capture.options))
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, "chat-new-thread-project-popover"), 320, 300)
	if !menu.HasText("项目Beta") {
		t.Fatalf("缺少项目: %v", menu.Texts())
	}
	if err := menu.Click("项目Beta"); err != nil {
		t.Fatal(err)
	}
	if len(capture.destroyedWindows) != 1 {
		t.Fatal("选择后未关闭")
	}
}

func TestAppFlyoutReopensAtLastFitHeight(t *testing.T) {
	entry, capture := setupPopoverFlyoutTest(t)
	const key = "reopen-fit-height-popover"
	t.Cleanup(func() {
		appFlyoutPopoverMu.Lock()
		delete(appFlyoutHeights, key)
		appFlyoutPopoverMu.Unlock()
	})
	open := true
	host := ui.NewTester(func(c *ui.Context) {
		entry.services = c.Services()
		anchor := ui.ButtonBase(c.Key("anchor")).Size(100, 30)
		appFlyoutPopover(c, key, anchor, &open, nil, appFlyoutProps{Width: 120}, func(c *ui.Context) {
			ui.Text(c, "内容")
		})
	}, 400, 300)
	host.Frame()
	if len(capture.options) != 1 || capture.options[0].Content == nil {
		t.Fatalf("首次打开未创建浮层: %+v", capture.options)
	}
	if capture.options[0].Height != 0 {
		t.Fatalf("首次打开应使用默认高度 0，实际: %d", capture.options[0].Height)
	}
	menu := ui.NewTester(popoverTestView(t, entry.services, key), 120, 189)
	menu.Frame()
	open = false
	host.Frame()
	if len(capture.destroyedWindows) != 1 {
		t.Fatalf("关闭时应销毁窗口，实际: %d", len(capture.destroyedWindows))
	}
	open = true
	host.Frame()
	if len(capture.options) != 2 {
		t.Fatalf("再次打开未创建浮层，窗口数: %d", len(capture.options))
	}
	if capture.options[1].Height != 189 {
		t.Fatalf("再次打开应沿用上次实测高度 189，实际: %d", capture.options[1].Height)
	}
}
