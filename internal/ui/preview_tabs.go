package mcodeui

import (
	"fmt"
	"sort"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 非当前会话合计只保留四个原生页面；标签元数据不落盘。
const browserInactivePageLimit = 4

var browserNextTabID uint64

// browserTab 的页面、输入和采集状态统一由 previewMu 保护。
type browserTab struct {
	tabID                          uint64
	owner                          *previewState
	title, localPath               string
	addressEditing                 bool
	canGoBack, canGoForward        bool
	loading                        bool
	navigationLastPoll             time.Time
	visible                        bool
	url, html                      string
	web                            *mygo.WebView
	page                           previewPage
	pageID, generation             uint64
	consoleRevision, annotRevision uint64
	webCreating                    bool
	pageOff                        func()
	// 本地内容一律 Untrusted；信任级别变化时重建页面。
	untrusted, pageUntrusted bool
	localPreview             bool
	previewThreadID          string
	nextAnnotationID         uint64
	addr, err                string
	// 创建失败后只由显式打开或刷新重试，避免按帧重建。
	webFailed                   bool
	viewport                    previewViewportMode
	consoleErrors               []previewConsoleError
	consoleOpen, consolePolling bool
	consoleLastPoll             time.Time
	annotating                  bool
	annotations                 []previewAnnotation
	annotOpen, annotPolling     bool
	annotLastPoll               time.Time
}

type previewState struct {
	tabs        []*browserTab
	activeTabID uint64
	empty       *browserTab
	visible     bool
	lastUsed    time.Time
}

type browserHistoryPage interface {
	previewPage
	URL() string
	CanGoBack() bool
	CanGoForward() bool
	GoBack()
	GoForward()
	Reload()
	Stop()
	IsLoading() bool
}

type browserTitlePage interface {
	OnPageTitleUpdated(func(*mygo.TitleEvent)) func()
}

func browserActiveTabLocked(st *previewState) *browserTab {
	for _, tab := range st.tabs {
		if tab.tabID == st.activeTabID {
			return tab
		}
	}
	if st.empty == nil {
		st.empty = &browserTab{owner: st, viewport: previewViewportDesktop, visible: st.visible}
	}
	return st.empty
}

func browserPromoteTabLocked(tab *browserTab) {
	if tab.tabID != 0 || tab.owner == nil {
		return
	}
	browserNextTabID++
	tab.tabID = browserNextTabID
	st := tab.owner
	st.tabs = append(st.tabs, tab)
	st.empty = nil
	st.activeTabID = tab.tabID
	st.lastUsed = time.Now()
}

func browserNewTab(s *chatState) *browserTab {
	current := getPreviewState(s)
	if current == nil {
		return nil
	}
	st := current.owner
	hideBrowserTab(s, current)
	previewMu.Lock()
	tab := &browserTab{owner: st, viewport: previewViewportDesktop, visible: st.visible}
	browserPromoteTabLocked(tab)
	previewMu.Unlock()
	s.invalidate()
	return tab
}

func openPreviewURLInNewTab(s *chatState, raw string) {
	if normalizePreviewURL(raw) == "" {
		openPreviewURL(s, raw)
		return
	}
	browserNewTab(s)
	openPreviewURL(s, raw)
}

func browserActivateTab(s *chatState, st *previewState, id uint64) {
	previewMu.Lock()
	old := browserActiveTabLocked(st)
	var target *browserTab
	for _, tab := range st.tabs {
		if tab.tabID == id {
			target = tab
			break
		}
	}
	previewMu.Unlock()
	if target == nil || old == target {
		return
	}
	hideBrowserTab(s, old)
	previewMu.Lock()
	st.activeTabID = id
	st.lastUsed = time.Now()
	target.visible = st.visible
	previewMu.Unlock()
	s.invalidate()
}

func browserCloseTab(s *chatState, st *previewState, id uint64) {
	previewMu.Lock()
	var removed *browserTab
	for i, tab := range st.tabs {
		if tab.tabID != id {
			continue
		}
		removed = tab
		st.tabs = append(st.tabs[:i:i], st.tabs[i+1:]...)
		if st.activeTabID == id {
			st.activeTabID = 0
			if len(st.tabs) > 0 {
				st.activeTabID = st.tabs[min(i, len(st.tabs)-1)].tabID
			}
			browserActiveTabLocked(st).visible = st.visible
		}
		break
	}
	previewMu.Unlock()
	if removed != nil {
		disposeBrowserTab(s, removed)
	}
	s.invalidate()
}

// 原生菜单在主线程调用，直接使用同一关闭入口，无需新增根视图动作。
func browserCloseFromMenu(s *chatState) bool {
	if !inspectorPaneOpen(s, sidePanePreviewID) {
		return false
	}
	tab := getPreviewState(s)
	if tab != nil {
		browserCloseTab(s, tab.owner, tab.tabID)
	}
	return true
}

func browserPageHistory(page previewPage) (bool, bool) {
	if p, ok := page.(browserHistoryPage); ok {
		return p.CanGoBack(), p.CanGoForward()
	}
	return false, false
}

func browserCommitNavigationLocked(tab *browserTab, target string, back, forward bool) {
	tab.url = target
	tab.canGoBack, tab.canGoForward = back, forward
	if !tab.addressEditing {
		tab.addr = target
	}
}

func browserBindTitle(s *chatState, token previewPageToken, page previewPage) func() {
	p, ok := page.(browserTitlePage)
	if !ok {
		return func() {}
	}
	return p.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
		e.PreventDefault()
		previewMu.Lock()
		if tab := previewStateForTokenLocked(s, token); tab != nil {
			tab.title = e.Title
		}
		previewMu.Unlock()
		s.invalidate()
	})
}

func browserMaybeSyncNavigation(s *chatState, tab *browserTab) {
	previewMu.Lock()
	page, ok := tab.page.(browserHistoryPage)
	if !ok || !tab.visible || time.Since(tab.navigationLastPoll) < time.Second {
		previewMu.Unlock()
		return
	}
	tab.navigationLastPoll = time.Now()
	token := previewPageTokenLocked(tab)
	previewMu.Unlock()
	// shortcut: OnDidNavigate 未承诺覆盖 SPA；P3.2 改用预加载脚本的 location 序号。
	target, back, forward, loading := page.URL(), page.CanGoBack(), page.CanGoForward(), page.IsLoading()
	previewMu.Lock()
	current := previewStateForTokenLocked(s, token)
	var off func()
	var events previewPageEvents
	if current != nil && current.visible {
		if target != "" && !loading && current.url != target {
			off = current.pageOff
			current.pageOff = nil
			previewAdvanceGenerationLocked(current)
			token = previewPageTokenLocked(current)
			events, _ = page.(previewPageEvents)
		}
		if target != "" && !loading {
			browserCommitNavigationLocked(current, target, back, forward)
		} else {
			current.canGoBack, current.canGoForward = back, forward
		}
		current.loading = loading
	}
	previewMu.Unlock()
	if off != nil {
		off()
	}
	if events != nil {
		previewBindPageCallbacks(s, token, events)
	}
}

func browserTabVisible(tab *browserTab) bool {
	previewMu.Lock()
	defer previewMu.Unlock()
	return tab.visible
}

func browserNavigateHistory(s *chatState, tab *browserTab, forward bool) {
	previewMu.Lock()
	page, ok := tab.page.(browserHistoryPage)
	enabled := tab.canGoBack
	if forward {
		enabled = tab.canGoForward
	}
	previewMu.Unlock()
	if !ok || !enabled {
		return
	}
	if forward {
		page.GoForward()
	} else {
		page.GoBack()
	}
	s.invalidate()
}

func browserReloadOrStop(s *chatState, tab *browserTab) {
	previewMu.Lock()
	page, ok := tab.page.(browserHistoryPage)
	loading := tab.loading
	token := previewPageTokenLocked(tab)
	tab.webFailed = false
	previewMu.Unlock()
	if ok {
		if loading {
			page.Stop()
		} else {
			page.Reload()
		}
		previewMu.Lock()
		if current := previewStateForTokenLocked(s, token); current != nil {
			current.loading = !loading
		}
		previewMu.Unlock()
	} else if !ensureWebView(s, tab) {
		loadPreviewPage(s, tab)
	}
	s.invalidate()
}

func renderBrowserHistory(c *ui.Context, s *chatState, tab *browserTab, snap previewSnapshot) {
	IconButton(c, IconButtonProps{Key: "preview-back", Icon: IconArrowLeft(), Label: "后退", Size: ControlXS, Variant: ButtonGhost, Disabled: !snap.canGoBack, OnClick: func() { browserNavigateHistory(s, tab, false) }})
	IconButton(c, IconButtonProps{Key: "preview-forward", Icon: IconChevronRight(), Label: "前进", Size: ControlXS, Variant: ButtonGhost, Disabled: !snap.canGoForward, OnClick: func() { browserNavigateHistory(s, tab, true) }})
	label, icon := "刷新", IconRefreshCw()
	if snap.loading {
		label, icon = "停止", IconX()
	}
	IconButton(c, IconButtonProps{Key: "preview-reload-btn", Icon: icon, Label: label, Size: ControlXS, Variant: ButtonGhost, Disabled: snap.url == "" && snap.html == "", OnClick: func() { browserReloadOrStop(s, tab) }})
}

func renderBrowserTabs(c *ui.Context, s *chatState, st *previewState) {
	type item struct {
		id    uint64
		title string
	}
	previewMu.Lock()
	items := make([]item, 0, len(st.tabs))
	selected := 0
	for i, tab := range st.tabs {
		title := tab.title
		if title == "" {
			title = tab.url
		}
		if title == "" {
			title = "新标签"
		}
		items = append(items, item{tab.tabID, title})
		if tab.tabID == st.activeTabID {
			selected = i
		}
	}
	previewMu.Unlock()
	row := ui.Row(c.Key("browser-tabs-row")).FillWidth().Height(ControlMD).Shrink(0)
	selection := ui.Local(row, "selection", func() int { return selected })
	*selection = selected
	var closeID uint64
	row.Children(func() {
		tabs := ui.TabsBase(c.Key("browser-tabs"), selection, len(items))
		tabs.List.Grow(1).Shrink(1).MinWidth(0).Children(func() {
			for i, item := range items {
				ui.Row(c.Key(fmt.Sprintf("browser-tab-%d", item.id))).Transition(motionListItem(c)).Shrink(1).Children(func() {
					tab := tabs.Tab(i).Label(item.title).Height(ControlMD).Padding(0, Space2).MinWidth(0).Shrink(1)
					target := float32(0)
					if *selection == i {
						target = 1
					}
					if reduceMotion(c) {
						tab.Jump("indicator", target)
					}
					ink := tab.AnimateTo("indicator", target, springSnappy)
					tab.Children(func() {
						ui.Text(c, item.title).FontSize(TypeCaption).SingleLine().MaxWidth(160)
						ui.Box(c).Absolute().Left(Space2).Right(Space2).Bottom(0).Height(Hairline).Background(ThemeAccent(c).Alpha(ink))
					})
					IconButton(c, IconButtonProps{Key: fmt.Sprintf("browser-close-%d", item.id), Icon: IconX(), Label: "关闭网页标签", Size: ControlXS, Variant: ButtonGhost, OnClick: func() { closeID = item.id }})
				})
			}
		})
		IconButton(c, IconButtonProps{Key: "browser-new-tab", Icon: IconPlus(), Label: "新建标签", Size: ControlXS, Variant: ButtonGhost, OnClick: func() { browserNewTab(s) }})
	})
	if closeID != 0 {
		browserCloseTab(s, st, closeID)
	} else if *selection >= 0 && *selection < len(items) && *selection != selected {
		browserActivateTab(s, st, items[*selection].id)
	}
	c.After(time.Second)
}

func browserEnforceSessionLimit(s *chatState) {
	s.mu.Lock()
	current := inspectorExistingSessionLocked(s, s.selectedThreadID)
	var inactive []*previewState
	for _, slot := range s.inspector.Sessions {
		if slot != current {
			inactive = append(inactive, slot.Browser)
		}
	}
	if slot := s.inspector.Temporary; slot != nil && slot != current {
		inactive = append(inactive, slot.Browser)
	}
	s.mu.Unlock()
	previewMu.Lock()
	sort.SliceStable(inactive, func(i, j int) bool { return inactive[i].lastUsed.Before(inactive[j].lastUsed) })
	count := 0
	for _, state := range inactive {
		for _, tab := range state.tabs {
			if tab.page != nil {
				count++
			}
		}
	}
	var evicted []*browserTab
	for _, state := range inactive {
		for _, tab := range state.tabs {
			if count <= browserInactivePageLimit {
				break
			}
			if tab.page != nil {
				evicted = append(evicted, tab)
				count--
			}
		}
	}
	previewMu.Unlock()
	for _, tab := range evicted {
		// 元数据保留；本地 URL 的 token 仍属于该标签，关标签时再撤销。
		browserReleaseView(tab)
	}
}

func browserReleaseView(tab *browserTab) {
	previewMu.Lock()
	page, web, off := tab.page, tab.web, tab.pageOff
	previewAdvanceGenerationLocked(tab)
	tab.page, tab.web, tab.pageOff = nil, nil, nil
	tab.webCreating, tab.webFailed = false, false
	tab.canGoBack, tab.canGoForward, tab.loading = false, false, false
	tab.navigationLastPoll = time.Time{}
	tab.visible = false
	previewMu.Unlock()
	if off != nil {
		off()
	}
	if page != nil {
		page.Destroy()
	} else if web != nil {
		web.Destroy()
	}
}
