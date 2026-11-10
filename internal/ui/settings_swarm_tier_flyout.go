package mcodeui

import (
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

type swarmTierEditorSession struct {
	parent *mygo.Window
	appFlyout
	hostServices ui.Services
	viewport     ui.Rect
	key          string
	model        *swarmTierModelSession
}

type swarmTierModelSession struct {
	appFlyout
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
	var dispose func()
	if destroy {
		dispose = model.destroyLocked()
	}
	resetSwarmTierModelPicker(session.key)
	st.mu.Unlock()
	if dispose != nil {
		dispose()
	}
	st.invalidate()
}

func updateSwarmTierModelFlyout(st *SettingsState, state *swarmTiersUIState, session *swarmTierEditorSession, bounds ui.Rect, zoom float32, open bool) {
	r, valid := flyoutAnchor(bounds, zoom)
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
		model = &swarmTierModelSession{}
		session.model = model
	}
	action := model.updateLocked(&st.mu, r, func() { createSwarmTierModelFlyout(st, state, session, model) })
	st.mu.Unlock()
	if action != nil {
		action()
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
		renderAppFlyout(c, "swarm-tier-model-flyout-root", "swarm-tier-model-flyout-scroll", 330, &modelPickerPanelBg, func() {
			renderModelPickerMenuBody(c, props, props.State)
		})
	}
	st.mu.Lock()
	model.view = view
	st.mu.Unlock()
	opts := appFlyoutOptions(parent, anchor, 330)
	opts.Content = ui.View(view)
	model.create(&st.mu, opts, nil, func() { closeSwarmTierModel(st, state, session, model, false) })
}

func openSwarmTierEditor(c *ui.Context, st *SettingsState, state *swarmTiersUIState, anchor ui.Element, key string, tier config.SwarmTier) {
	services := c.Services()
	parent := flyoutHostWindow(services)
	st.mu.Lock()
	if state.openPopoverKey != "" {
		if session := state.editor; session != nil {
			w := session.window
			st.mu.Unlock()
			if w != nil {
				flyoutRunOnMain(func() { flyoutFocus(w) })
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
	r, valid := flyoutAnchor(bounds, c.Zoom())
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
	action := session.updateLocked(&st.mu, r, func() { createSwarmTierEditor(st, state, session) })
	st.mu.Unlock()
	if action != nil {
		action()
	}
}

func createSwarmTierEditor(st *SettingsState, state *swarmTiersUIState, session *swarmTierEditorSession) {
	st.mu.Lock()
	active := st.swarmTiersPage == state && state.editor == session
	anchor := session.anchor
	st.mu.Unlock()
	if !active || session.parent.IsDestroyed() || flyoutHostWindow(session.hostServices) != session.parent {
		if active {
			disposeSwarmTierEditor(st)
		}
		return
	}
	view := func(c *ui.Context) {
		st.mu.Lock()
		active := st.swarmTiersPage == state && state.editor == session
		if active {
			session.services = c.Services()
		}
		st.mu.Unlock()
		if !active {
			return
		}
		renderAppFlyout(c, "swarm-tier-flyout-root", "swarm-tier-editor-scroll-"+session.key, 360, nil, func() {
			renderSwarmTierEditorForm(c, st, state, session.key)
		})
	}
	st.mu.Lock()
	session.view = view
	st.mu.Unlock()
	opts := appFlyoutOptions(session.parent, anchor, 360)
	opts.Content = ui.View(view)
	session.create(&st.mu, opts, func(e *mygo.CloseEvent) {
		st.mu.Lock()
		active := st.swarmTiersPage == state && state.editor == session
		st.mu.Unlock()
		if active {
			e.PreventDefault()
		}
	}, func() {
		st.mu.Lock()
		var disposeChild func()
		if st.swarmTiersPage == state && state.editor == session {
			if session.model != nil {
				disposeChild = session.model.destroyLocked()
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
		if disposeChild != nil {
			disposeChild()
		}
		st.invalidate()
	})
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
	var disposeChild func()
	if session.model != nil {
		disposeChild = session.model.destroyLocked()
		session.model = nil
		resetSwarmTierModelPicker(session.key)
	}
	disposeWindow := session.destroyLocked()
	return func() {
		if disposeChild != nil {
			disposeChild()
		}
		if disposeWindow != nil {
			disposeWindow()
		}
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
