package mcodeui

import (
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type swarmTierFlyoutCapture struct {
	options   []mygo.FlyoutOptions
	closes    []func(*mygo.CloseEvent)
	closed    []func()
	anchors   []mygo.Rectangle
	viewport  *ui.Rect
	destroyed int
	focused   int
}

func setupSwarmTierFlyoutTest(t *testing.T) (*SettingsState, *ui.Tester, *swarmTierFlyoutCapture, *mygo.Window) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.currentPage = "swarm"
	parent := &mygo.Window{}
	capture := &swarmTierFlyoutCapture{}
	oldRun, oldNew, oldDestroy, oldFocus, oldAnchor := swarmTierRunOnMain, swarmTierNewFlyout, swarmTierDestroyFlyout, swarmTierFocusFlyout, swarmTierSetAnchor
	settingsWindowMu.Lock()
	oldEntry, oldInline := settingsWindow, settingsWindowInline
	settingsWindowInline = false
	entry := &settingsWindowEntry{state: st, win: parent}
	settingsWindow = entry
	settingsWindowMu.Unlock()
	swarmTierRunOnMain = func(fn func()) { fn() }
	swarmTierNewFlyout = func(o mygo.FlyoutOptions, close func(*mygo.CloseEvent), closed func()) *mygo.Window {
		capture.options = append(capture.options, o)
		capture.closes = append(capture.closes, close)
		capture.closed = append(capture.closed, closed)
		return &mygo.Window{}
	}
	swarmTierDestroyFlyout = func(*mygo.Window) { capture.destroyed++ }
	swarmTierFocusFlyout = func(*mygo.Window) { capture.focused++ }
	swarmTierSetAnchor = func(_ *mygo.Window, r mygo.Rectangle) { capture.anchors = append(capture.anchors, r) }
	t.Cleanup(func() {
		swarmTierRunOnMain, swarmTierNewFlyout, swarmTierDestroyFlyout, swarmTierFocusFlyout, swarmTierSetAnchor = oldRun, oldNew, oldDestroy, oldFocus, oldAnchor
		settingsWindowMu.Lock()
		settingsWindow, settingsWindowInline = oldEntry, oldInline
		settingsWindowMu.Unlock()
	})
	host := ui.NewTester(func(c *ui.Context) {
		settingsWindowMu.Lock()
		entry.services = c.Services()
		settingsWindowMu.Unlock()
		if capture.viewport != nil {
			setSwarmTierEditorViewport(c, st, *capture.viewport)
		}
		ui.Column(c).Width(1000).Children(func() { renderSwarmTiersBody(c, st) })
	}, 1100, 3000)
	host.Frame()
	return st, host, capture, parent
}

func TestSwarmTierFlyoutAnchorLeavesViewport(t *testing.T) {
	st, host, capture, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("deep"); err != nil {
		t.Fatal(err)
	}
	capture.viewport = &ui.Rect{X: 0, Y: 0, W: 10, H: 10}
	host.Frame()
	if getSwarmTiersUIState(st).editor != nil || capture.destroyed != 1 {
		t.Fatal("锚点离开可见区域后没有清理窗口")
	}
}

func TestSwarmTierFlyoutEntrancesAndLifecycle(t *testing.T) {
	for _, label := range []string{"添加档位", "deep"} {
		t.Run(label, func(t *testing.T) {
			st, host, capture, parent := setupSwarmTierFlyoutTest(t)
			if err := host.Click(label); err != nil {
				t.Fatal(err)
			}
			if len(capture.options) != 1 {
				t.Fatalf("创建次数: %d", len(capture.options))
			}
			o := capture.options[0]
			if o.Parent != parent || o.Placement != mygo.PlacementBottomStart || o.Anchor.Width <= 0 || o.Anchor.Height <= 0 || !o.FitContent || o.MaxHeight != 0 || !o.Focusable || o.Popover || !o.Shadow || o.Width != 360 || o.Content == nil {
				t.Fatalf("FlyoutOptions: %+v", o)
			}
			state := getSwarmTiersUIState(st)
			editor := ui.NewTester(state.editor.view, 360, 1200)
			editor.Frame()
			if !editor.HasText("保存") {
				t.Fatalf("表单缺少保存按钮: %v", editor.Texts())
			}
			state.draft.Name = "保留草稿"
			e := &mygo.CloseEvent{}
			capture.closes[0](e)
			if !e.DefaultPrevented() || state.draft.Name != "保留草稿" {
				t.Fatal("外部关闭没有保留草稿")
			}
			if err := editor.Click("取消"); err != nil {
				t.Fatal(err)
			}
			disposeSwarmTierEditor(st)
			if capture.destroyed != 1 || state.editor != nil || state.openPopoverKey != "" {
				t.Fatalf("清理失败: %d", capture.destroyed)
			}
		})
	}
}

func TestSwarmTierFlyoutAnchor(t *testing.T) {
	r, ok := swarmTierAnchor(ui.Rect{X: -10.2, Y: 20.2, W: 30.1, H: 40.1}, 1.3)
	if !ok || r != (mygo.Rectangle{X: -14, Y: 26, Width: 40, Height: 53}) {
		t.Fatalf("锚点 = %+v, %v", r, ok)
	}
	if _, ok := swarmTierAnchor(ui.Rect{}, 1); ok {
		t.Fatal("未布局的锚点不可用")
	}
}

func TestSwarmTierFlyoutAnchorTracksZoom(t *testing.T) {
	_, host, capture, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	host.SetZoom(1.3)
	host.Frame()
	if len(capture.anchors) == 0 || capture.anchors[len(capture.anchors)-1] == capture.options[0].Anchor {
		t.Fatal("缩放后没有更新锚点")
	}
	count := len(capture.anchors)
	host.Frame()
	if len(capture.anchors) != count {
		t.Fatal("锚点未变化时不应重复定位")
	}
}

func TestSwarmTierFlyoutStaleClosedAndPageSwitch(t *testing.T) {
	st, host, capture, _ := setupSwarmTierFlyoutTest(t)
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	disposeSwarmTierEditor(st)
	if err := host.Click("添加档位"); err != nil {
		t.Fatal(err)
	}
	if len(capture.closed) != 2 {
		t.Fatalf("创建次数: %d", len(capture.closed))
	}
	current := getSwarmTiersUIState(st).editor
	capture.closed[0]()
	if getSwarmTiersUIState(st).editor != current {
		t.Fatal("旧回调影响新会话")
	}
	st.selectSettingsPage("general")
	st.Close()
	if capture.destroyed != 2 || getSwarmTiersUIState(st).editor != nil {
		t.Fatalf("切页清理失败: %d", capture.destroyed)
	}
}

func TestSwarmTierFlyoutInlineHost(t *testing.T) {
	st, host, _, parent := setupSwarmTierFlyoutTest(t)
	var services ui.Services
	tester := ui.NewTester(func(c *ui.Context) { services = c.Services(); ui.Box(c).Size(10, 10) }, 20, 20)
	tester.Frame()
	chat := &chatState{}
	chat.setUIServices(services)
	settingsWindowMu.Lock()
	settingsWindowInline = true
	settingsWindow.services = ui.Services{}
	settingsWindowMu.Unlock()
	chatWindowsMu.Lock()
	chatWindows[parent] = chat
	chatWindowsMu.Unlock()
	t.Cleanup(func() { chatWindowsMu.Lock(); delete(chatWindows, parent); chatWindowsMu.Unlock() })
	if got := swarmTierHostWindow(st, services); got != parent {
		t.Fatal("内联宿主解析失败")
	}
	if swarmTierHostWindow(st, ui.Services{}) != nil {
		t.Fatal("空 services 不能匹配")
	}
	_ = host
}
