package mcodeui

import (
	"context"
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
	threadID                       string
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
	untrusted, pageUntrusted              bool
	localPreview                          bool
	previewThreadID                       string
	localPreviewURL                       string
	consoleConfigured, pageConsoleCapture bool
	consoleDocument                       string
	consoleWatermark, locationSequence    uint64
	consoleAvailable                      bool
	consoleClearPending                   bool
	consoleFilter                         string
	consoleSelected                       map[uint64]bool
	nextAnnotationID                      uint64
	addr, err                             string
	// 创建失败后只由显式打开或刷新重试，避免按帧重建。
	webFailed                   bool
	viewport                    previewViewportMode
	consoleErrors               []previewConsoleError
	consoleOpen, consolePolling bool
	consoleLastPoll             time.Time
	consoleResetSeq             uint64
	consoleList                 ui.ListState
	consoleListReady            bool
	consoleListResetSeq         uint64
	annotating                  bool
	annotations                 []previewAnnotation
	annotPolling, annotSending  bool
	annotOps                    previewAnnotationOps
	annotHighlight, annotFocus  uint64
	annotLastPoll               time.Time
	feedbackText                string
	feedbackSending             bool
	pollDone                    chan struct{}
	pollCancel                  context.CancelFunc
}

type previewState struct {
	tabs             []*browserTab
	activeTabID      uint64
	empty            *browserTab
	threadID         string
	viewportMenuOpen bool
	captureMenuOpen  bool
	visible          bool
	lastUsed         time.Time
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
		st.empty = &browserTab{owner: st, threadID: st.threadID, viewport: previewViewportDesktop, visible: st.visible, consoleList: ui.ListState{FollowEnd: true}, consoleListReady: true}
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
	tab := &browserTab{owner: st, threadID: st.threadID, viewport: previewViewportDesktop, visible: st.visible, consoleList: ui.ListState{FollowEnd: true}, consoleListReady: true}
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
	if polling, ok := page.(browserPollingPage); ok {
		page = polling.previewPage
	}
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
		changed := false
		if tab := previewStateForTokenLocked(s, token); tab != nil && tab.title != e.Title {
			tab.title = e.Title
			changed = true
		}
		previewMu.Unlock()
		if changed {
			s.invalidate()
		}
	})
}

func browserMaybeSyncNavigation(s *chatState, tab *browserTab) {
	previewMu.Lock()
	page, ok := tab.page.(browserHistoryPage)
	if !ok || !tab.visible || tab.consoleAvailable || time.Since(tab.navigationLastPoll) < time.Second {
		previewMu.Unlock()
		return
	}
	tab.navigationLastPoll = time.Now()
	token := previewPageTokenLocked(tab)
	previewMu.Unlock()
	// 仅在 drain 尚不可用时兜底；空闲且关闭采集时不为此安排定时重绘。
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
	IconButton(c, IconButtonProps{Key: "preview-back", Icon: IconArrowLeft(), Label: "后退", Tooltip: "后退", Size: ControlXS, Variant: ButtonGhost, Disabled: !snap.canGoBack, OnClick: func() { browserNavigateHistory(s, tab, false) }}).Shrink(0)
	IconButton(c, IconButtonProps{Key: "preview-forward", Icon: IconArrowRight(), Label: "前进", Tooltip: "前进", Size: ControlXS, Variant: ButtonGhost, Disabled: !snap.canGoForward, OnClick: func() { browserNavigateHistory(s, tab, true) }}).Shrink(0)
	label, icon := "刷新", IconRefreshCw()
	if snap.loading {
		label, icon = "停止", IconX()
	}
	IconButton(c, IconButtonProps{Key: "preview-reload-btn", Icon: icon, Label: label, Tooltip: label, Size: ControlXS, Variant: ButtonGhost, Disabled: snap.url == "" && snap.html == "", OnClick: func() { browserReloadOrStop(s, tab) }}).Shrink(0)
}

func browserNeedsPollingLocked(tab *browserTab, capture bool) bool {
	return tab.visible && tab.page != nil && (tab.loading || tab.annotating || capture)
}

type browserPollingPage struct {
	previewPage
	ctx context.Context
}

func (p browserPollingPage) Eval(script string) (any, error) {
	if page, ok := p.previewPage.(interface {
		EvalContext(context.Context, string) (any, error)
	}); ok {
		// 隐藏保留原生页；页面不响应时也必须让周期任务有机会检查生命周期。
		ctx, cancel := context.WithTimeout(p.ctx, previewConsolePollInterval)
		defer cancel()
		return page.EvalContext(ctx, script)
	}
	return p.previewPage.Eval(script)
}

// 设置和窗口状态先读完再取 previewMu；持锁期间不进入页面 API 或 UI 回调。
func browserStartPolling(s *chatState, tab *browserTab) {
	capture := previewConsoleCaptureEnabled(s)
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	previewMu.Lock()
	if closed || tab.pollDone != nil || !browserNeedsPollingLocked(tab, capture) {
		previewMu.Unlock()
		return
	}
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	tab.pollDone, tab.pollCancel = done, cancel
	token := previewPageTokenLocked(tab)
	previewMu.Unlock()
	go func() {
		defer cancel()
		defer func() {
			previewMu.Lock()
			tab.pollDone, tab.pollCancel = nil, nil
			close(done)
			previewMu.Unlock()
			// 状态变化可能已渲染过；退出时按最新条件接管，避免漏掉重启。
			browserStartPolling(s, tab)
		}()
		ticker := time.NewTicker(previewConsolePollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			capture := previewConsoleCaptureEnabled(s)
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			previewMu.Lock()
			if closed || previewStateForTokenLocked(s, token) == nil || !browserNeedsPollingLocked(tab, capture) {
				previewMu.Unlock()
				return
			}
			page := browserPollingPage{previewPage: tab.page, ctx: ctx}
			console := capture && !tab.loading && !tab.consolePolling
			current := previewPageTokenLocked(tab)
			if console {
				tab.consolePolling = true
				tab.consoleLastPoll = time.Now()
			}
			previewMu.Unlock()
			if console {
				previewPollConsoleErrors(s, current, page)
			}
			previewMu.Lock()
			// 同步读取两个队列，整个标签只保留一个周期任务。
			annotate := previewStateForTokenLocked(s, token) != nil && tab.visible && tab.annotating && !tab.annotPolling
			current = previewPageTokenLocked(tab)
			if annotate {
				tab.annotPolling = true
				tab.annotLastPoll = time.Now()
			}
			previewMu.Unlock()
			if annotate {
				previewPollAnnotations(s, current, page)
			}
		}
	}()
}

func renderBrowserTabs(c *ui.Context, s *chatState, st *previewState) {
	type item struct {
		id     uint64
		title  string
		hasErr bool
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
		items = append(items, item{tab.tabID, title, tab.err != ""})
		if tab.tabID == st.activeTabID {
			selected = i
		}
	}
	previewMu.Unlock()
	if len(items) < 2 {
		return
	}
	row := ui.Row(c.Key("browser-tabs-row")).
		FillWidth().
		Height(ControlLG).
		Padding(Space1, Space2, 0, Space2).
		Gap(Space1).
		AlignItems(ui.Center).
		Background(ThemeSurfaceCanvas(c)).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeHairline(c)).
		Shrink(0)
	selection := ui.Local(row, "selection", func() int { return selected })
	*selection = selected
	row.Children(func() {
		tabs := ui.TabsBase(c.Key("browser-tabs"), selection, len(items))
		tabs.List.Gap(Space0).FillHeight().Shrink(1).MinWidth(0).Children(func() {
			for i, item := range items {
				id := item.id
				active := *selection == i
				tabRow := ui.Row(c.Key(fmt.Sprintf("browser-tab-%d", id))).
					Transition(motionListItem(c)).
					FillHeight().
					AlignItems(ui.Center).
					MinWidth(0).
					MaxWidth(184).
					Shrink(1).
					Padding(0, Space1, 0, 0).
					Radius(RadiusSM, RadiusSM, 0, 0)
				if active {
					tabRow.Background(ThemeSurfaceMain(c)).
						BorderWidth(Hairline, Hairline, 0, Hairline).
						BorderColor(ThemeHairline(c))
				} else if tabRow.Hovered() {
					tabRow.Background(ThemeOverlayHover(c))
				}
				target := float32(0)
				if active {
					target = 1
				}
				if reduceMotion(c) {
					tabRow.Jump("indicator", target)
				}
				ink := tabRow.AnimateTo("indicator", target, springSnappy)
				tabRow.Children(func() {
					tab := tabs.Tab(i).
						Label(item.title).
						FillHeight().
						Padding(0, Space1, 0, Space2).
						Gap(Space1).
						AlignItems(ui.Center).
						MinWidth(0).
						Shrink(1)
					dotColor := ThemeTertiaryText(c)
					if item.hasErr {
						dotColor = ThemeDanger(c)
					} else if active {
						dotColor = ThemeSuccess(c)
					}
					textColor := ThemeSecondaryText(c)
					if active || tabRow.Hovered() {
						textColor = ThemePrimaryText(c)
					}
					tab.Children(func() {
						ui.Box(c).Size(Space1+Space0, Space1+Space0).Radius(RadiusPill).Background(dotColor).Shrink(0)
						text := ui.Text(c, item.title).
							FontSize(uiFont(TypeCaption)).
							TextColor(textColor).
							SingleLine().
							Ellipsis("…").
							MinWidth(0).
							Shrink(1)
						if active {
							text.FontWeight(weightFor(c, 500))
						}
					})
					IconButton(c, IconButtonProps{
						Key:     fmt.Sprintf("browser-close-%d", id),
						Icon:    IconX(),
						Label:   "关闭网页标签",
						Tooltip: "关闭网页标签",
						Size:    ControlXS,
						Variant: ButtonGhost,
						OnClick: func() { browserCloseTab(s, st, id) },
					}).Shrink(0)
					ui.Box(c).
						Absolute().
						Left(Space2).
						Right(Space2).
						Bottom(0).
						Height(1.5).
						Radius(RadiusPill).
						Background(ThemeAccent(c).Alpha(ink))
				})
			}
		})
		IconButton(c, IconButtonProps{
			Key:     "browser-tabs-new",
			Icon:    IconPlus(),
			Label:   "新建标签",
			Tooltip: "新建标签",
			Size:    ControlSM,
			Variant: ButtonGhost,
			OnClick: func() { browserNewTab(s) },
		}).Shrink(0)
	})
	if *selection >= 0 && *selection < len(items) && *selection != selected {
		browserActivateTab(s, st, items[*selection].id)
	}
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
	if tab.pollCancel != nil {
		tab.pollCancel()
	}
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
