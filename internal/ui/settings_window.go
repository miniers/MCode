package mcodeui

import (
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type settingsWindowEntry struct {
	win      *mygo.Window
	owner    *chatState
	state    *SettingsState
	services ui.Services
	visitors map[*chatState]bool
}

var settingsWindowMu sync.Mutex
var settingsWindow *settingsWindowEntry

func openSettingsWindow(owner *chatState, page string) *mygo.Window {
	if owner == nil || getChatSettings(owner) == nil {
		return nil
	}
	var win *mygo.Window
	mygo.RunOnMain(func() {
		settingsWindowMu.Lock()
		entry := settingsWindow
		settingsWindowMu.Unlock()
		if entry != nil && !entry.win.IsDestroyed() {
			settingsWindowMu.Lock()
			entry.visitors[owner] = true
			settingsWindowMu.Unlock()
			win = entry.win
			if page != "" {
				entry.state.selectSettingsPage(page)
				entry.state.openLegacySettingsSheet(page)
			}
			if win.IsMinimized() {
				win.Restore()
			}
			win.Show()
			win.Focus()
			return
		}
		st := getChatSettings(owner)
		if st == nil {
			return
		}
		st.Open()
		if page != "" {
			st.selectSettingsPage(page)
			st.openLegacySettingsSheet(page)
		}
		entry = &settingsWindowEntry{owner: owner, state: st, visitors: map[*chatState]bool{owner: true}}
		win = mygo.NewWindow(mygo.WindowOptions{
			Title: "设置", Width: 960, Height: 680, MinWidth: 640, MinHeight: 420,
			TitleBarStyle:        mygo.TitleBarHiddenInset,
			TrafficLightPosition: &mygo.Point{X: 18, Y: 16},
			TitleBarHeight:       hiddenTitleBarHeight(titleBarGOOS, int(HeaderHeight)),
			StateKey:             "settings-window",
			Content: ui.View(func(c *ui.Context) {
				applyAppTheme(c)
				settingsWindowMu.Lock()
				entry.services = c.Services()
				settingsWindowMu.Unlock()
				renderSettingsWindow(c, st)
			}),
		})
		entry.win = win
		settingsWindowMu.Lock()
		settingsWindow = entry
		settingsWindowMu.Unlock()
		win.OnClose(func(e *mygo.CloseEvent) {
			if !st.prepareWindowClose() {
				e.PreventDefault()
			}
		})
		win.OnClosed(func() {
			settingsWindowMu.Lock()
			if settingsWindow == entry {
				settingsWindow = nil
			}
			entry.services = ui.Services{}
			settingsWindowMu.Unlock()
			st.Close()
			for visitor := range entry.visitors {
				visitor.invalidate()
			}
		})
	})
	return win
}

func invalidateSettingsWindow(st *SettingsState) {
	settingsWindowMu.Lock()
	var services ui.Services
	if settingsWindow != nil && settingsWindow.state == st {
		services = settingsWindow.services
	}
	settingsWindowMu.Unlock()
	services.Invalidate()
}

func closeSettingsWindowForOwner(owner *chatState) bool {
	settingsWindowMu.Lock()
	entry := settingsWindow
	settingsWindowMu.Unlock()
	if entry == nil || entry.owner != owner {
		return true
	}
	if !entry.state.prepareWindowClose() {
		return false
	}
	entry.win.Close()
	return entry.win.IsDestroyed()
}

func currentSettingsWindowState() *SettingsState {
	settingsWindowMu.Lock()
	defer settingsWindowMu.Unlock()
	if settingsWindow == nil {
		return nil
	}
	return settingsWindow.state
}
