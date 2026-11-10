package mcodeui

import (
	"math"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// appFlyout 的字段由调用方的锁保护；返回的操作必须在解锁后执行。

// 测试替换此边界，避免创建原生窗口和依赖事件循环。
var flyoutRunOnMain = mygo.RunOnMain
var flyoutNew = func(opts mygo.FlyoutOptions, onClose func(*mygo.CloseEvent), onClosed func()) *mygo.Window {
	w := mygo.NewFlyout(opts)
	if onClose != nil {
		w.OnClose(onClose)
	}
	w.OnClosed(onClosed)
	return w
}
var flyoutDestroy = func(w *mygo.Window) { w.Destroy() }
var flyoutFocus = func(w *mygo.Window) { w.Show(); w.Focus() }
var flyoutSetAnchor = func(w *mygo.Window, r mygo.Rectangle) { w.SetAnchor(r) }

func flyoutHostWindow(services ui.Services) *mygo.Window {
	if services == (ui.Services{}) {
		return nil
	}
	settingsWindowMu.Lock()
	var w *mygo.Window
	if settingsWindow != nil && settingsWindow.services == services {
		w = settingsWindow.win
	}
	settingsWindowMu.Unlock()
	if w != nil && !w.IsDestroyed() {
		return w
	}
	chatWindowsMu.RLock()
	chats := make(map[*mygo.Window]*chatState, len(chatWindows))
	for win, chat := range chatWindows {
		chats[win] = chat
	}
	chatWindowsMu.RUnlock()
	for win, chat := range chats {
		if win != nil && chat != nil && chat.uiServices() == services && !win.IsDestroyed() {
			return win
		}
	}
	beeWindowsMu.Lock()
	bees := make([]beeWindowEntry, 0, len(beeWindows))
	for _, entry := range beeWindows {
		if entry != nil {
			bees = append(bees, *entry)
		}
	}
	beeWindowsMu.Unlock()
	for _, entry := range bees {
		if entry.win != nil && entry.state != nil && entry.state.uiServices() == services && !entry.win.IsDestroyed() {
			return entry.win
		}
	}
	return nil
}

func flyoutAnchor(bounds ui.Rect, zoom float32) (mygo.Rectangle, bool) {
	if bounds.W <= 0 || bounds.H <= 0 || zoom <= 0 {
		return mygo.Rectangle{}, false
	}
	x := int(math.Floor(float64(bounds.X * zoom)))
	y := int(math.Floor(float64(bounds.Y * zoom)))
	right := int(math.Ceil(float64((bounds.X + bounds.W) * zoom)))
	bottom := int(math.Ceil(float64((bounds.Y + bounds.H) * zoom)))
	return mygo.Rectangle{X: x, Y: y, Width: right - x, Height: bottom - y}, true
}

type appFlyout struct {
	window     *mygo.Window
	anchor     mygo.Rectangle
	creating   bool
	services   ui.Services
	view       func(*ui.Context)
	generation uint64
}

// updateLocked 更新锚点，并返回需要在解锁后执行的操作。
func (f *appFlyout) updateLocked(mu *sync.Mutex, anchor mygo.Rectangle, create func()) func() {
	if f.window == nil && !f.creating {
		f.anchor, f.creating = anchor, true
		gen := f.generation
		return func() {
			flyoutRunOnMain(func() {
				mu.Lock()
				valid := f.generation == gen && f.creating
				mu.Unlock()
				if valid {
					create()
				}
			})
		}
	}
	if f.anchor == anchor {
		return nil
	}
	f.anchor = anchor
	w := f.window
	if w == nil {
		return nil
	}
	return func() {
		flyoutRunOnMain(func() {
			mu.Lock()
			valid := f.window == w && f.anchor == anchor
			mu.Unlock()
			if valid {
				flyoutSetAnchor(w, anchor)
			}
		})
	}
}

// create 在主线程执行；调用方先检查会话有效性。
func (f *appFlyout) create(mu *sync.Mutex, opts mygo.FlyoutOptions, onClose func(*mygo.CloseEvent), onClosed func()) {
	mu.Lock()
	if !f.creating {
		mu.Unlock()
		return
	}
	gen, anchor := f.generation, f.anchor
	opts.Anchor = anchor
	mu.Unlock()
	var identity struct {
		window *mygo.Window
		closed bool
	}
	w := flyoutNew(opts, onClose, func() {
		mu.Lock()
		if identity.window == nil {
			identity.closed = true
			mu.Unlock()
			return
		}
		valid := f.generation == gen && f.window == identity.window
		if valid {
			f.window = nil
			f.services = ui.Services{}
			f.generation++
		}
		mu.Unlock()
		if valid && onClosed != nil {
			onClosed()
		}
	})
	mu.Lock()
	identity.window = w
	valid := f.generation == gen && f.creating
	closed := identity.closed
	if valid && closed {
		f.creating = false
		f.services = ui.Services{}
		f.generation++
	} else if valid {
		f.window, f.creating = w, false
	}
	latest := f.anchor
	mu.Unlock()
	if valid && closed && onClosed != nil {
		onClosed()
	} else if !valid && !closed && w != nil {
		flyoutDestroy(w)
	} else if valid && !closed && w != nil && latest != anchor {
		flyoutSetAnchor(w, latest)
	}
}

func (f *appFlyout) destroyLocked() func() {
	w := f.window
	f.window, f.creating, f.services = nil, false, ui.Services{}
	f.generation++
	if w == nil {
		return nil
	}
	return func() { flyoutRunOnMain(func() { flyoutDestroy(w) }) }
}

func appFlyoutOptions(parent *mygo.Window, anchor mygo.Rectangle, width int) mygo.FlyoutOptions {
	return mygo.FlyoutOptions{Parent: parent, Anchor: anchor, Placement: mygo.PlacementBottomStart, Gap: int(Space1), Width: width, MaxWidth: width, FitContent: true, Focusable: true, Shadow: true, CornerRadius: int(RadiusMD)}
}

func renderAppFlyout(c *ui.Context, rootKey, scrollKey string, width float32, background *ui.Color, body func()) {
	applyAppTheme(c)
	transition := motionTransition(c, ui.ElementTransition{Duration: DurBase, Ease: EaseSnap, Enter: &ui.Motion{Opacity: 0}})
	root := ui.Column(c.Key(rootKey)).MaxWidth(width).FillWidth().FillHeight().Transition(transition)
	if background != nil {
		root = root.Background(*background).Radius(RadiusMD)
	}
	root.Children(func() { ui.Scroll(c.Key(scrollKey)).Grow(1).FillWidth().Children(body) })
}
