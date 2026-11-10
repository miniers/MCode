package mcodeui

import (
	"sync"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

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
}
