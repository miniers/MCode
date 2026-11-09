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

// 没有主事件循环的测试把设置内容内联渲染到所属聊天窗口根部，生产环境始终为 false。
var settingsWindowInline bool

// settingsWindowOpener 为空时走原生窗口；测试替换为无窗口实现。声明处不赋值，避免初始化循环。
var settingsWindowOpener func(owner *chatState, page string) *mygo.Window

func openSettingsWindow(owner *chatState, page string) *mygo.Window {
	if owner == nil || getChatSettings(owner) == nil {
		return nil
	}
	opener := settingsWindowOpener
	if opener == nil {
		opener = openSettingsWindowNative
	}
	return opener(owner, page)
}

// openSettingsAt 按设置项 ID 打开并定位高亮，找不到条目时按页面 ID 打开。
func openSettingsAt(owner *chatState, itemID string) {
	for _, item := range settingsItems() {
		if item.ID != itemID {
			continue
		}
		openSettingsWindow(owner, item.Page)
		st := currentSettingsWindowState()
		if st == nil {
			st = getChatSettings(owner)
		}
		if st != nil {
			st.focusSettingsItem(settingsItemHit{Item: item})
		}
		return
	}
	openSettingsWindow(owner, itemID)
}

// openProviderSettings 打开模型与供应商页面，并在指定供应商时打开其面板。
func openProviderSettings(owner *chatState, providerID string) {
	if owner == nil || getChatSettings(owner) == nil {
		return
	}
	openSettingsWindow(owner, "models")
	st := currentSettingsWindowState()
	if st == nil {
		st = getChatSettings(owner)
	}
	if st == nil {
		return
	}
	if providerID == "" {
		closeSettingsSheet(st)
		return
	}
	kind := "cli"
	st.mu.Lock()
	for _, preset := range st.presets.items {
		if preset.ID == providerID && preset.Kind == "api" {
			kind = "preset"
			break
		}
	}
	for _, endpoint := range st.endpoints.endpoints {
		if endpoint.ID == providerID && !endpoint.Magpie {
			kind = "custom"
			break
		}
	}
	st.mu.Unlock()
	openProviderSheet(st, kind, providerID)
}

// openSettingsWindowHeadless 供没有主事件循环的测试使用。
func openSettingsWindowHeadless(owner *chatState, page string) *mygo.Window {
	st := getChatSettings(owner)
	if st == nil {
		return nil
	}
	settingsWindowMu.Lock()
	entry := settingsWindow
	if entry != nil && entry.state == st {
		entry.visitors[owner] = true
		settingsWindowMu.Unlock()
	} else {
		settingsWindowMu.Unlock()
		st.Open()
		entry = &settingsWindowEntry{owner: owner, state: st, visitors: map[*chatState]bool{owner: true}}
		settingsWindowMu.Lock()
		settingsWindow = entry
		settingsWindowMu.Unlock()
	}
	if page != "" {
		st.selectSettingsPage(page)
		st.openLegacySettingsSheet(page)
	}
	return nil
}

func inlineSettingsWindowState(owner *chatState) *SettingsState {
	settingsWindowMu.Lock()
	defer settingsWindowMu.Unlock()
	entry := settingsWindow
	if !settingsWindowInline || entry == nil || entry.win != nil ||
		(!entry.visitors[owner] && entry.owner != owner) || entry.state == nil || !entry.state.IsOpen() {
		return nil
	}
	return entry.state
}

func resetSettingsWindowForTest() {
	settingsWindowMu.Lock()
	settingsWindow = nil
	settingsWindowMu.Unlock()
}

func openSettingsWindowNative(owner *chatState, page string) *mygo.Window {
	var win *mygo.Window
	mygo.RunOnMain(func() {
		settingsWindowMu.Lock()
		entry := settingsWindow
		settingsWindowMu.Unlock()
		if entry != nil && entry.win != nil && !entry.win.IsDestroyed() {
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
	if entry.win == nil {
		settingsWindowMu.Lock()
		if settingsWindow == entry {
			settingsWindow = nil
		}
		settingsWindowMu.Unlock()
		entry.state.Close()
		return true
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
