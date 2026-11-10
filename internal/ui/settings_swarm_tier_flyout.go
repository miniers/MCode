package mcodeui

import (
	"math"
	"strings"

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
	model        *swarmTierModelSession
}

type swarmTierModelSession struct {
	window   *mygo.Window
	services ui.Services
	anchor   mygo.Rectangle
	creating bool
	view     func(*ui.Context)
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
	if onClose != nil {
		w.OnClose(onClose)
	}
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

func swarmTierModelProps(st *SettingsState, state *swarmTiersUIState, key string, catalog modelPickerCatalog) ModelPickerProps {
	sel := ModelSelection{Provider: state.draft.Provider, Model: state.draft.Model, Effort: state.draft.Effort, Fast: state.draft.FastMode}
	return swarmModelCapsuleProps(st, "swarm-tier-model-capsule", "tier-model-"+key, sel, "跟随会话子代理模型", "选择档位模型", func(next ModelSelection) {
		state.draft.Provider, state.draft.Model = next.Provider, next.Model
		state.draft.Effort, state.draft.FastMode = next.Effort, next.Fast
		st.invalidate()
	}, catalog, false)
}

func resetSwarmTierModelPicker(key string) {
	scope := "tier-model-" + key
	modelPickerStateFor("swarm-tier-model-capsule-" + scope).Reset()
	clearModelPanelMoreOpen(scope, "lead")
	clearModelPanelMoreOpen(scope, "bees")
	modelPanelProviderMoreMu.Lock()
	for key := range modelPanelProviderMore {
		if strings.HasPrefix(key, scope+"/") {
			delete(modelPanelProviderMore, key)
		}
	}
	modelPanelProviderMoreMu.Unlock()
	clearModelCapsuleSnapshot(scope)
}

func closeSwarmTierModel(st *SettingsState, state *swarmTiersUIState, session *swarmTierEditorSession, model *swarmTierModelSession, destroy bool) {
	st.mu.Lock()
	if st.swarmTiersPage != state || state.editor != session || session.model != model {
		st.mu.Unlock()
		return
	}
	session.model = nil
	w := model.window
	model.services = ui.Services{}
	resetSwarmTierModelPicker(session.key)
	st.mu.Unlock()
	if destroy && w != nil {
		swarmTierRunOnMain(func() { swarmTierDestroyFlyout(w) })
	}
	st.invalidate()
}

func updateSwarmTierModelFlyout(st *SettingsState, state *swarmTiersUIState, session *swarmTierEditorSession, bounds ui.Rect, zoom float32, open bool) {
	r, valid := swarmTierAnchor(bounds, zoom)
	st.mu.Lock()
	if st.swarmTiersPage != state || state.editor != session {
		st.mu.Unlock()
		return
	}
	model := session.model
	if !open {
		st.mu.Unlock()
		if model != nil {
			closeSwarmTierModel(st, state, session, model, true)
		}
		return
	}
	if !valid || session.window == nil {
		st.mu.Unlock()
		return
	}
	if model == nil {
		model = &swarmTierModelSession{anchor: r, creating: true}
		session.model = model
		st.mu.Unlock()
		swarmTierRunOnMain(func() { createSwarmTierModelFlyout(st, state, session, model) })
		return
	}
	if model.anchor == r {
		st.mu.Unlock()
		return
	}
	model.anchor = r
	w := model.window
	st.mu.Unlock()
	if w != nil {
		swarmTierRunOnMain(func() { swarmTierSetAnchor(w, r) })
	}
}

func createSwarmTierModelFlyout(st *SettingsState, state *swarmTiersUIState, session *swarmTierEditorSession, model *swarmTierModelSession) {
	st.mu.Lock()
	active := st.swarmTiersPage == state && state.editor == session && session.model == model
	anchor, parent := model.anchor, session.window
	st.mu.Unlock()
	if !active || parent == nil {
		return
	}
	view := func(c *ui.Context) {
		applyAppTheme(c)
		st.mu.Lock()
		active := st.swarmTiersPage == state && state.editor == session && session.model == model
		if active {
			model.services = c.Services()
		}
		st.mu.Unlock()
		if !active {
			return
		}
		catalog := settingsModelPickerCatalog(st)
		props := catalog.apply(swarmTierModelProps(st, state, session.key, catalog))
		props.NativeMenu = func(ui.Rect, float32, bool) {}
		pick := props.OnPickModel
		props.OnPickModel = func(side ModelPickerSide, provider ProviderInfo, item ModelInfo) {
			pick(side, provider, item)
			closeSwarmTierModel(st, state, session, model, true)
		}
		transition := motionTransition(c, ui.ElementTransition{Duration: DurBase, Ease: EaseSnap, Enter: &ui.Motion{Opacity: 0}})
		ui.Column(c.Key("swarm-tier-model-flyout-root")).MaxWidth(330).FillWidth().FillHeight().Background(modelPickerPanelBg).Radius(RadiusMD).Transition(transition).Children(func() {
			ui.Scroll(c.Key("swarm-tier-model-flyout-scroll")).Grow(1).FillWidth().Children(func() {
				renderModelPickerMenuBody(c, props, props.State)
			})
		})
	}
	model.view = view
	w := swarmTierNewFlyout(mygo.FlyoutOptions{
		Parent: parent, Anchor: anchor, Placement: mygo.PlacementBottomStart,
		Width: 330, MaxWidth: 330, FitContent: true, Focusable: true, Shadow: true,
		CornerRadius: int(RadiusMD), Content: ui.View(view),
	}, nil, func() { closeSwarmTierModel(st, state, session, model, false) })
	st.mu.Lock()
	active = st.swarmTiersPage == state && state.editor == session && session.model == model
	if active {
		model.window, model.creating = w, false
	}
	st.mu.Unlock()
	if !active && w != nil {
		swarmTierDestroyFlyout(w)
	}
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
		var child *mygo.Window
		if st.swarmTiersPage == state && state.editor == session {
			if session.model != nil {
				child = session.model.window
				session.model = nil
				resetSwarmTierModelPicker(session.key)
			}
			state.editor = nil
			if session.parent.IsDestroyed() {
				state.openPopoverKey = ""
				state.saveError = ""
			}
		}
		st.mu.Unlock()
		if child != nil {
			swarmTierRunOnMain(func() { swarmTierDestroyFlyout(child) })
		}
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
	if session == nil {
		return nil
	}
	var child *mygo.Window
	if session.model != nil {
		child = session.model.window
		session.model = nil
		resetSwarmTierModelPicker(session.key)
	}
	window := session.window
	return func() {
		swarmTierRunOnMain(func() {
			if child != nil {
				swarmTierDestroyFlyout(child)
			}
			if window != nil {
				swarmTierDestroyFlyout(window)
			}
		})
	}
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
