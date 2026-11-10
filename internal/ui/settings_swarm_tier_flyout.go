package mcodeui

import (
	"math"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

type swarmTierEditorSession struct {
	parent       *mygo.Window
	window       *mygo.Window
	hostServices ui.Services
	services     ui.Services
	anchor       mygo.Rectangle
	viewport     ui.Rect
	key          string
	creating     bool
	view         func(*ui.Context)
}

func setSwarmTierEditorViewport(c *ui.Context, st *SettingsState, viewport ui.Rect) {
	if viewport.W <= 0 || viewport.H <= 0 {
		return
	}
	st.mu.Lock()
	if state := st.swarmTiersPage; state != nil && state.editor != nil && state.editor.hostServices == c.Services() {
		state.editor.viewport = viewport
	}
	st.mu.Unlock()
}

// 测试替换此边界，避免创建原生窗口和依赖事件循环。
var swarmTierRunOnMain = mygo.RunOnMain
var swarmTierNewFlyout = func(opts mygo.FlyoutOptions, onClose func(*mygo.CloseEvent), onClosed func()) *mygo.Window {
	w := mygo.NewFlyout(opts)
	w.OnClose(onClose)
	w.OnClosed(onClosed)
	return w
}
var swarmTierDestroyFlyout = func(w *mygo.Window) { w.Destroy() }
var swarmTierFocusFlyout = func(w *mygo.Window) { w.Show(); w.Focus() }
var swarmTierSetAnchor = func(w *mygo.Window, r mygo.Rectangle) { w.SetAnchor(r) }

func swarmTierHostWindow(st *SettingsState, services ui.Services) *mygo.Window {
	if services == (ui.Services{}) {
		return nil
	}
	settingsWindowMu.Lock()
	entry, inline := settingsWindow, settingsWindowInline
	inline = inline && entry != nil && entry.state == st
	var win *mygo.Window
	if entry != nil && entry.state == st && entry.services == services {
		win = entry.win
	}
	settingsWindowMu.Unlock()
	if win != nil && !win.IsDestroyed() {
		return win
	}
	if !inline {
		return nil
	}
	chatWindowsMu.RLock()
	windows := make(map[*mygo.Window]*chatState, len(chatWindows))
	for w, chat := range chatWindows {
		windows[w] = chat
	}
	chatWindowsMu.RUnlock()
	for w, chat := range windows {
		if w != nil && chat != nil && chat.uiServices() == services && !w.IsDestroyed() {
			return w
		}
	}
	return nil
}

func swarmTierAnchor(bounds ui.Rect, zoom float32) (mygo.Rectangle, bool) {
	if bounds.W <= 0 || bounds.H <= 0 || zoom <= 0 {
		return mygo.Rectangle{}, false
	}
	x := int(math.Floor(float64(bounds.X * zoom)))
	y := int(math.Floor(float64(bounds.Y * zoom)))
	right := int(math.Ceil(float64((bounds.X + bounds.W) * zoom)))
	bottom := int(math.Ceil(float64((bounds.Y + bounds.H) * zoom)))
	return mygo.Rectangle{X: x, Y: y, Width: right - x, Height: bottom - y}, true
}

func openSwarmTierEditor(c *ui.Context, st *SettingsState, state *swarmTiersUIState, anchor ui.Element, key string, tier config.SwarmTier) {
	services := c.Services()
	parent := swarmTierHostWindow(st, services)
	st.mu.Lock()
	if state.openPopoverKey != "" {
		if session := state.editor; session != nil {
			w := session.window
			st.mu.Unlock()
			if w != nil {
				swarmTierRunOnMain(func() { swarmTierFocusFlyout(w) })
			}
			return
		}
		if key != state.openPopoverKey {
			st.mu.Unlock()
			return
		}
	} else {
		if parent == nil {
			st.mu.Unlock()
			return
		}
		state.openPopoverKey = key
		state.isNew = key == "add"
		state.draft = tier
		state.draft.WritePaths = append([]string(nil), tier.WritePaths...)
		state.writePathsText = formatWritePaths(tier.WritePaths)
		state.saveError = ""
	}
	if parent == nil {
		st.mu.Unlock()
		return
	}
	session := &swarmTierEditorSession{parent: parent, hostServices: services, key: key}
	state.editor = session
	st.mu.Unlock()
	updateSwarmTierEditorAnchor(c, st, anchor, key)
	st.invalidate()
}

func updateSwarmTierEditorAnchor(c *ui.Context, st *SettingsState, element ui.Element, key string) {
	bounds := element.Bounds()
	r, valid := swarmTierAnchor(bounds, c.Zoom())
	if !valid {
		return
	}
	st.mu.Lock()
	if st.swarmTiersPage == nil {
		st.mu.Unlock()
		return
	}
	state := st.swarmTiersPage
	session := state.editor
	if session == nil || session.key != key || session.hostServices != c.Services() {
		st.mu.Unlock()
		return
	}
	width, height := c.Size()
	viewport := session.viewport
	if viewport.W <= 0 || viewport.H <= 0 {
		viewport = ui.Rect{W: width, H: height}
	}
	if session.window != nil && (bounds.X+bounds.W <= viewport.X || bounds.Y+bounds.H <= viewport.Y || bounds.X >= viewport.X+viewport.W || bounds.Y >= viewport.Y+viewport.H) {
		dispose := detachSwarmTierEditorLocked(st)
		st.mu.Unlock()
		if dispose != nil {
			dispose()
		}
		return
	}
	if session.window == nil && !session.creating {
		session.anchor, session.creating = r, true
		st.mu.Unlock()
		swarmTierRunOnMain(func() { createSwarmTierEditor(st, state, session) })
		return
	}
	if session.anchor == r {
		st.mu.Unlock()
		return
	}
	session.anchor = r
	w := session.window
	st.mu.Unlock()
	if w != nil {
		swarmTierRunOnMain(func() { swarmTierSetAnchor(w, r) })
	}
}

func createSwarmTierEditor(st *SettingsState, state *swarmTiersUIState, session *swarmTierEditorSession) {
	st.mu.Lock()
	active := st.swarmTiersPage == state && state.editor == session
	anchor := session.anchor
	st.mu.Unlock()
	if !active || session.parent.IsDestroyed() || swarmTierHostWindow(st, session.hostServices) != session.parent {
		if active {
			disposeSwarmTierEditor(st)
		}
		return
	}
	session.view = func(c *ui.Context) {
		applyAppTheme(c)
		st.mu.Lock()
		active := st.swarmTiersPage == state && state.editor == session
		if active {
			session.services = c.Services()
		}
		st.mu.Unlock()
		if !active {
			return
		}
		transition := motionTransition(c, ui.ElementTransition{Duration: DurBase, Ease: EaseSnap, Enter: &ui.Motion{Opacity: 0}})
		ui.Column(c.Key("swarm-tier-flyout-root")).MaxWidth(360).FillWidth().FillHeight().Transition(transition).Children(func() {
			ui.Scroll(c.Key("swarm-tier-editor-scroll-" + session.key)).Grow(1).FillWidth().Children(func() {
				renderSwarmTierEditorForm(c, st, state, session.key)
			})
		})
	}
	opts := mygo.FlyoutOptions{
		Parent: session.parent, Anchor: anchor, Placement: mygo.PlacementBottomStart,
		Gap: int(Space1), Width: 360, MaxWidth: 360, FitContent: true, Focusable: true,
		Shadow: true, CornerRadius: int(RadiusMD),
		Content: ui.View(session.view),
	}
	w := swarmTierNewFlyout(opts, func(e *mygo.CloseEvent) {
		st.mu.Lock()
		active := st.swarmTiersPage == state && state.editor == session
		st.mu.Unlock()
		if active {
			e.PreventDefault()
		}
	}, func() {
		st.mu.Lock()
		if st.swarmTiersPage == state && state.editor == session {
			state.editor = nil
			if session.parent.IsDestroyed() {
				state.openPopoverKey = ""
				state.saveError = ""
			}
		}
		st.mu.Unlock()
		st.invalidate()
	})
	st.mu.Lock()
	active = st.swarmTiersPage == state && state.editor == session
	if active {
		session.window = w
		session.creating = false
	}
	st.mu.Unlock()
	if !active && w != nil {
		swarmTierDestroyFlyout(w)
	}
}

// detachSwarmTierEditorLocked 只摘除会话；调用方解锁后销毁窗口。
func detachSwarmTierEditorLocked(st *SettingsState) func() {
	state := st.swarmTiersPage
	if state == nil {
		return nil
	}
	session := state.editor
	state.editor = nil
	state.openPopoverKey = ""
	state.saveError = ""
	if session == nil || session.window == nil {
		return nil
	}
	return func() { swarmTierRunOnMain(func() { swarmTierDestroyFlyout(session.window) }) }
}

func disposeSwarmTierEditor(st *SettingsState) {
	st.mu.Lock()
	dispose := detachSwarmTierEditorLocked(st)
	st.mu.Unlock()
	if dispose != nil {
		dispose()
	}
}

func disposeSwarmTierEditorIfEditing(st *SettingsState, id string) {
	st.mu.Lock()
	var dispose func()
	if st.swarmTiersPage != nil && !st.swarmTiersPage.isNew && st.swarmTiersPage.openPopoverKey == "edit-"+id {
		dispose = detachSwarmTierEditorLocked(st)
	}
	st.mu.Unlock()
	if dispose != nil {
		dispose()
	}
}
