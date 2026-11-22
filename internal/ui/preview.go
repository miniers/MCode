package mcodeui

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

const (
	PreviewPanelDefaultWidth float32 = 480
	PreviewPanelMinWidth     float32 = 280
	PreviewPanelMaxWidth     float32 = 800

	// PreviewTabletWidth / PreviewMobileWidth 是平板与手机视口的宽度。
	PreviewTabletWidth float32 = 820
	PreviewMobileWidth float32 = 390
)

// previewConsolePollInterval 是 drain 页面控制台与拾取队列的最小间隔。
var previewConsolePollInterval = 700 * time.Millisecond

// previewViewportMode 是 Web 预览的视口模式。
type previewViewportMode string

// 预览面板的固定尺寸：顶栏高度与控制台抽屉高度。
const (
	previewToolbarHeight       float32 = 38
	previewConsoleDrawerHeight float32 = 160
)

const (
	previewViewportDesktop previewViewportMode = "desktop"
	previewViewportTablet  previewViewportMode = "tablet"
	previewViewportMobile  previewViewportMode = "mobile"
)

// previewConsoleError 描述页面里捕获到的一条控制台或未处理异常记录。
type previewConsoleError struct {
	Sequence uint64 `json:"sequence"`
	Time     string `json:"time"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Source   string `json:"source"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
}

// previewViewportModes 返回视口分段选择器的顺序。
func previewViewportModes() []previewViewportMode {
	return []previewViewportMode{previewViewportDesktop, previewViewportTablet, previewViewportMobile}
}

// previewViewportWidth 返回视口模式的宽度约束；桌面模式返回 0，表示铺满面板。
func previewViewportWidth(mode previewViewportMode) float32 {
	switch mode {
	case previewViewportTablet:
		return PreviewTabletWidth
	case previewViewportMobile:
		return PreviewMobileWidth
	default:
		return 0
	}
}

// previewViewportLabel 返回视口模式的中文名称。
func previewViewportLabel(mode previewViewportMode) string {
	switch mode {
	case previewViewportTablet:
		return "平板"
	case previewViewportMobile:
		return "手机"
	default:
		return "桌面"
	}
}

// previewConsoleEntryLine 把一条控制台错误格式化为单行文本。
func previewConsoleEntryLine(entry previewConsoleError) string {
	kind := entry.Kind
	if kind == "" {
		kind = "error"
	}
	location := ""
	if entry.Source != "" {
		location = fmt.Sprintf(" (%s:%d)", entry.Source, entry.Line)
	}
	return kind + ": " + entry.Message + location
}

// previewConsoleErrorsEqual 比较两次轮询到的错误列表是否一致。
func previewConsoleErrorsEqual(a, b []previewConsoleError) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var (
	previewMu         sync.Mutex
	previewStates     = make(map[*chatState]*previewState)
	previewNextPageID uint64
)

// newPreviewWebView 是创建原生 WebView 的唯一入口；测试替换它来检查创建选项。
var newPreviewWebView = func(win *mygo.Window, opts mygo.WebViewOptions) (*mygo.WebView, error) {
	return win.NewWebView(opts)
}

// previewWorkbenchSettings 返回当前会话使用的工作台设置；设置尚未初始化时返回零值。
func previewWorkbenchSettings(s *chatState) config.WorkbenchConfig {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		wb := st.cfg.Workbench
		st.mu.Unlock()
		return wb
	}
	return config.WorkbenchConfig{}
}

// previewConsoleCaptureEnabled 返回预览面板是否启用控制台采集。
func previewConsoleCaptureEnabled(s *chatState) bool {
	return previewWorkbenchSettings(s).BrowserConsoleCaptureEnabled()
}

// getPreviewState 保留活动页面访问入口，容器状态由 Inspector 会话槽持有。
func getPreviewState(s *chatState) *browserTab {
	if s == nil {
		return nil
	}
	slot := inspectorSession(s)
	if slot == nil {
		return nil
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()
	previewMu.Lock()
	defer previewMu.Unlock()
	st := slot.Browser
	if st.threadID == "" {
		st.threadID = threadID
	}
	previewStates[s] = st
	return browserActiveTabLocked(st)
}

// setPreviewViewport 切换视口模式并请求重绘。
func setPreviewViewport(s *chatState, st *browserTab, mode previewViewportMode) {
	if st == nil {
		return
	}
	previewMu.Lock()
	st.viewport = mode
	previewMu.Unlock()
	if s != nil {
		s.invalidate()
	}
}

// previewMaybePollConsoleErrors 按最小间隔读取预装收集器的增量记录。
// force 为真时忽略间隔（用于页面加载完成后的首次读取）。
func previewMaybePollConsoleErrors(s *chatState, st *browserTab, force bool) {
	if st == nil || !previewConsoleCaptureEnabled(s) {
		return
	}
	previewMu.Lock()
	if !st.visible || st.loading || st.consolePolling || st.page == nil {
		previewMu.Unlock()
		return
	}
	if !force && time.Since(st.consoleLastPoll) < previewConsolePollInterval {
		previewMu.Unlock()
		return
	}
	st.consoleLastPoll = time.Now()
	st.consolePolling = true
	page, token := st.page, previewPageTokenLocked(st)
	previewMu.Unlock()

	go previewPollConsoleErrors(s, token, page)
}

// clearPreviewConsoleErrors 清空页面与本地收集的控制台错误。
func clearPreviewConsoleErrors(s *chatState, st *browserTab) {
	if st == nil || !previewConsoleCaptureEnabled(s) {
		return
	}
	previewMu.Lock()
	page := st.page
	st.consoleRevision++
	st.consoleClearPending = true
	token := previewPageTokenLocked(st)
	st.consolePolling = false
	// 清空脚本完成前暂停新轮询，避免再读到页面侧尚未清掉的数据。
	if page != nil {
		st.consolePolling = true
	}
	st.consoleErrors = nil
	st.consoleSelected = nil
	st.consoleResetSeq++
	previewMu.Unlock()

	if page != nil {
		go previewPollConsoleErrors(s, token, page)
	}
	if s != nil {
		s.invalidate()
	}
}

// normalizePreviewURL 规范化用户输入或传入的 URL 地址：
// 去除首尾空白；若没有 scheme，localhost、127.0.0.1、以数字和端口开头的地址补 http://，其他补 https://。
func normalizePreviewURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.Contains(raw, "://") {
		if strings.HasPrefix(raw, ":") {
			raw = "localhost" + raw
		}
		host := strings.SplitN(raw, "/", 2)[0]
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		scheme := "https://"
		if strings.EqualFold(host, "localhost") || net.ParseIP(strings.Trim(host, "[]")) != nil {
			scheme = "http://"
		}
		raw = scheme + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}

// ParseQuickPorts 从配置字符串中解析快捷端口列表，按逗号、空格或换行分隔，去重并过滤非空项。
// 若无有效端口，返回默认端口列表 ["3000", "5173", "8080", "4321"]。
func ParseQuickPorts(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	var result []string
	seen := make(map[string]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && !seen[p] {
			seen[p] = true
			result = append(result, p)
		}
	}
	if len(result) == 0 {
		return []string{"3000", "5173", "8080", "4321"}
	}
	return result
}

func getBrowserQuickPorts(s *chatState) []string {
	raw := ""
	if s != nil {
		if st := getChatSettings(s); st != nil {
			st.mu.Lock()
			raw = st.cfg.Workbench.BrowserQuickPorts
			st.mu.Unlock()
		}
	}
	return ParseQuickPorts(raw)
}

// ensureWebView 保证网页标签的 WebView 实例已初始化。
// 创建失败后置 webFailed 并停手：本函数在渲染路径里每帧调用，若失败还继续尝试就会以帧率重试。
func ensureWebView(s *chatState, st *browserTab) bool {
	capture := previewConsoleCaptureEnabled(s)
	previewMu.Lock()
	web, pageExists, generation := st.web, st.page != nil, st.generation
	untrustedMismatch := pageExists && st.pageUntrusted != st.untrusted
	captureMismatch := pageExists && st.consoleConfigured && st.pageConsoleCapture != capture
	blocked := st.webFailed || st.webCreating
	previewMu.Unlock()
	if blocked {
		return false
	}
	if pageExists && !untrustedMismatch && !captureMismatch && (web == nil || !web.IsDestroyed()) {
		return false
	}
	previewMu.Lock()
	if st.web != web || st.generation != generation || st.webCreating {
		previewMu.Unlock()
		return false
	}
	if st.page != nil && (st.pageUntrusted != st.untrusted || captureMismatch) {
		// 信任或采集配置变化必须重建，旧回包由 generation 丢弃。
		page, off, oldWeb := st.page, st.pageOff, st.web
		st.page, st.pageOff, st.web = nil, nil, nil
		previewAdvanceGenerationLocked(st)
		previewMu.Unlock()
		if off != nil {
			off()
		}
		if page != nil {
			page.Destroy()
		} else if oldWeb != nil {
			oldWeb.Destroy()
		}
		return ensureWebView(s, st)
	}
	untrusted := st.untrusted
	previewAdvanceGenerationLocked(st)
	previewNextPageID++
	st.pageID = previewNextPageID
	st.consoleDocument = ""
	st.consoleWatermark, st.locationSequence = 0, 0
	st.consoleAvailable, st.consoleClearPending = false, false
	st.consoleSelected = nil
	token := previewPageTokenLocked(st)
	off := st.pageOff
	st.pageOff = nil
	st.web = nil
	st.page = nil
	st.webCreating = true
	previewMu.Unlock()
	if off != nil {
		off()
	}

	var win *mygo.Window
	if s != nil && s.window != nil {
		win = s.window()
	}
	var wv *mygo.WebView
	var page *mygo.Page
	var message string
	if win != nil {
		var err error
		wv, err = newPreviewWebView(win, previewWebViewOptions(untrusted, capture))
		if err != nil {
			message = "创建 Web 视图失败: " + err.Error()
		} else {
			page = wv.Page()
		}
	} else {
		message = "未关联宿主窗口，无法创建 Web 视图"
	}
	previewMu.Lock()
	if previewStateForTokenLocked(s, token) == nil {
		previewMu.Unlock()
		if wv != nil {
			wv.Destroy()
		}
		return false
	}
	st.webCreating = false
	st.web = wv
	st.pageUntrusted = untrusted
	st.pageConsoleCapture, st.consoleConfigured = capture, true
	if page != nil {
		st.page = &nativePreviewPage{Page: page, web: wv}
	}
	st.err = message
	st.webFailed = message != ""
	previewMu.Unlock()
	if page != nil {
		loadPreviewPage(s, st)
	}
	return page != nil
}

func loadPreviewPage(s *chatState, st *browserTab) {
	previewMu.Lock()
	page, ok := st.page.(previewNavigablePage)
	if !ok {
		previewMu.Unlock()
		return
	}
	// 页面一换，旧的 selector 就指向不了任何元素：退出标注模式并清空已选元素。
	previewAdvanceGenerationLocked(st)
	token := previewPageTokenLocked(st)
	target, addr, html := st.url, st.addr, st.html
	off := st.pageOff
	st.pageOff = nil
	st.err = ""
	st.loading = true
	previewMu.Unlock()
	if off != nil {
		off()
	}
	if events, ok := page.(previewPageEvents); ok {
		previewBindPageCallbacks(s, token, events)
	}
	if target != "" {
		if err := page.LoadURL(target); err != nil {
			previewMu.Lock()
			if current := previewStateForTokenLocked(s, token); current != nil {
				current.err = "加载预览失败: " + err.Error()
				current.loading = false
			}
			previewMu.Unlock()
		}
	} else if addr == "html-preview" {
		page.LoadHTML(html, "")
	}
}

// openPreviewURL 打开指定 URL 并显示预览面板。
func openPreviewURL(s *chatState, raw string) {
	openPreviewURLForThread(s, "", raw)
}

func openPreviewURLForThread(s *chatState, previewThreadID, raw string) {
	if s == nil {
		return
	}
	target := normalizePreviewURL(raw)
	if target == "" {
		s.setToast("请输入有效的 HTTP 或 HTTPS 地址")
		return
	}
	st := getPreviewState(s)
	if st == nil {
		return
	}

	previewMu.Lock()
	var revokeURL, revokeThreadID string
	if st.localPreview {
		revokeURL, revokeThreadID = releaseLocalPreviewLocked(st)
	}
	browserPromoteTabLocked(st)
	previewAdvanceGenerationLocked(st)
	st.webCreating = false
	st.url = target
	st.addr = target
	st.addressEditing = false
	st.html = ""
	st.untrusted = false
	st.localPreview = false
	st.previewThreadID = previewThreadID
	// 用户重新打开/改地址时重置失败标记，允许再试一次创建 WebView。
	st.webFailed = false
	previewMu.Unlock()

	revokeLocalPreview(s, revokeURL, revokeThreadID)

	inspectorSelect(s, sidePanePreviewID, false)

	if !ensureWebView(s, st) {
		loadPreviewPage(s, st)
	}
	s.invalidate()
}

// openPreviewHTML 载入指定 HTML 字符串并显示预览面板。
func openPreviewHTML(s *chatState, html string) {
	openPreviewHTMLWith(s, html, false)
}

// openPreviewHTMLWith 载入指定 HTML 字符串并按指定信任级别显示预览面板。
func openPreviewHTMLWith(s *chatState, html string, untrusted bool) {
	if s == nil {
		return
	}
	st := getPreviewState(s)
	if st == nil {
		return
	}

	previewMu.Lock()
	var revokeURL, revokeThreadID string
	if st.localPreview {
		revokeURL, revokeThreadID = releaseLocalPreviewLocked(st)
	}
	browserPromoteTabLocked(st)
	previewAdvanceGenerationLocked(st)
	st.webCreating = false
	st.html = html
	st.url = ""
	st.addr = "html-preview"
	st.addressEditing = false
	st.untrusted = untrusted
	st.localPreview = false
	st.previewThreadID = ""
	// 用户重新打开时重置失败标记，允许再试一次创建 WebView。
	st.webFailed = false
	previewMu.Unlock()

	revokeLocalPreview(s, revokeURL, revokeThreadID)

	inspectorSelect(s, sidePanePreviewID, false)

	if !ensureWebView(s, st) {
		loadPreviewPage(s, st)
	}
	s.invalidate()
}

// openPreviewLocalURL 打开后端签发的本地内容 URL。
// 这个页面以 Untrusted 创建：不注册 Go 绑定，也不因监听在 loopback 上获得应用权限。
func openPreviewLocalURL(s *chatState, threadID, raw string) {
	if s == nil {
		return
	}
	target := normalizePreviewURL(raw)
	if target == "" {
		s.setToast("本地预览地址无效")
		return
	}
	st := getPreviewState(s)
	if st == nil {
		return
	}

	previewMu.Lock()
	browserPromoteTabLocked(st)
	previewAdvanceGenerationLocked(st)
	st.webCreating = false
	st.url = target
	st.addr = target
	st.addressEditing = false
	st.html = ""
	st.untrusted = true
	st.localPreview = true
	st.previewThreadID = threadID
	st.localPreviewURL = target
	// 用户重新打开时重置失败标记，允许再试一次创建 WebView。
	st.webFailed = false
	previewMu.Unlock()

	inspectorSelect(s, sidePanePreviewID, false)

	if !ensureWebView(s, st) {
		loadPreviewPage(s, st)
	}
	s.invalidate()
}

// closePreview 关闭预览面板并销毁 WebView。
func closePreview(s *chatState) {
	if s == nil {
		return
	}
	st := getPreviewState(s)
	if st == nil {
		return
	}

	if inspectorPaneOpen(s, sidePanePreviewID) {
		inspectorClose(s)
	}
	disposePreviewState(s, st.owner)
	s.invalidate()
}

// disposePreviewState 只用于关闭网页、删除会话和关闭窗口。
func disposePreviewState(s *chatState, st *previewState) {
	if st == nil {
		return
	}
	previewMu.Lock()
	tabs := append([]*browserTab(nil), st.tabs...)
	if st.empty != nil {
		tabs = append(tabs, st.empty)
	}
	st.visible = false
	previewMu.Unlock()
	for _, tab := range tabs {
		disposeBrowserTab(s, tab)
	}
	previewMu.Lock()
	st.tabs, st.empty, st.activeTabID = nil, nil, 0
	previewMu.Unlock()
}

// releaseLocalPreviewLocked 在 previewMu 内清理标签的本地预览状态，并返回需要撤销的 URL 与会话 ID。
func releaseLocalPreviewLocked(st *browserTab) (previewURL, threadID string) {
	if st == nil {
		return "", ""
	}
	// 本地标签释放后清掉地址，避免再次加载已撤销的 URL。
	threadID = st.previewThreadID
	previewURL = st.localPreviewURL
	wasLocal := st.localPreview
	st.localPreviewURL = ""
	st.previewThreadID = ""
	// 现有接口按会话撤销，等最后一个同会话本地标签释放，避免使兄弟页失效。
	if st.owner != nil && threadID != "" {
		for _, tab := range st.owner.tabs {
			if tab != st && tab.localPreview && tab.previewThreadID == threadID {
				threadID = ""
				break
			}
		}
	}
	if st.localPreview {
		st.localPreview = false
		st.url = ""
		st.html = ""
		st.addr = ""
	}
	if !wasLocal {
		threadID = ""
	}
	return previewURL, threadID
}

// revokeLocalPreview 在 previewMu 外通知后端撤销本地预览 URL 与会话 Token。
func revokeLocalPreview(s *chatState, previewURL, threadID string) {
	if s == nil {
		return
	}
	if previewURL != "" {
		if client, ok := s.client.(inspectorPreviewURLRevoker); ok {
			client.RevokeInspectorPreviewURL(previewURL)
		}
	}
	if threadID != "" {
		if client, ok := s.client.(inspectorPreviewClient); ok {
			client.RevokeInspectorPreviews(threadID)
		}
	}
}

func disposeBrowserTab(s *chatState, st *browserTab) {
	if st == nil {
		return
	}
	browserReleaseView(st)
	previewMu.Lock()
	previewURL, threadID := releaseLocalPreviewLocked(st)
	st.feedbackText = ""
	st.feedbackSending = false
	previewMu.Unlock()

	revokeLocalPreview(s, previewURL, threadID)
}

// togglePreview 切换预览面板的可见状态。
func togglePreview(s *chatState) {
	if s == nil {
		return
	}
	st := getPreviewState(s)
	if st == nil {
		return
	}
	visible := inspectorSelect(s, sidePanePreviewID, true)
	previewMu.Lock()
	if visible {
		// 重新打开面板时重置创建失败标记，给 WebView 一次新的创建机会。
		st.webFailed = false
	}
	previewMu.Unlock()

	s.invalidate()
}

var previewScreenshotIcon = parseLucideSVG(`<path d="M14.5 4h-5L7 7H4a2 2 0 0 0-2 2v10a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V9a2 2 0 0 0-2-2h-3z"/><circle cx="12" cy="13" r="4"/>`)

func previewExternalURL(snap previewSnapshot) string {
	if snap.url != "" {
		return snap.url
	}
	if snap.addr != "html-preview" {
		return normalizePreviewURL(snap.addr)
	}
	return ""
}

// renderPreviewPanel 渲染右侧 Web 预览面板。
func renderPreviewPanel(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()
	st := getPreviewState(s)
	if st == nil {
		return
	}
	captureConsole := previewConsoleCaptureEnabled(s)
	previewMu.Lock()
	hasContent := st.url != "" || st.html != ""
	previewMu.Unlock()
	if hasContent {
		ensureWebView(s, st)
	}
	browserStartPolling(s, st)
	previewMu.Lock()
	snap := previewSnapshotLocked(st)
	previewMu.Unlock()

	ui.Column(c.Key("preview-panel")).
		Grow(1).
		FillHeight().
		Background(ThemeWindowBg(c)).
		BorderWidth(0, 0, 0, Hairline).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			renderBrowserTabs(c, s, st.owner)
			// 顶栏 Toolbar
			ui.Row(c.Key("preview-toolbar")).
				Height(previewToolbarHeight).
				Padding(0, Space3).
				Gap(Space1).
				Shrink(0).
				AlignItems(ui.Center).
				BorderWidth(0, 0, Hairline, 0).
				BorderColor(ThemeBorder(c)).
				Children(func() {
					renderBrowserHistory(c, s, st, snap)
					// 地址输入框
					addr := snap.addr
					in := ui.TextInput(c.Key(fmt.Sprintf("preview-addr-input-%p-%d", st.owner, st.tabID)), &addr).
						Label("地址").
						Placeholder("输入地址").
						FontSize(uiFont(TypeFootnote)).
						MinWidth(0).
						Grow(1)
					if addr != snap.addr || in.Focused() != snap.addressEditing {
						previewMu.Lock()
						if current := previewStateForTokenLocked(s, snap.token); current != nil {
							current.addr = addr
							current.addressEditing = in.Focused()
						}
						previewMu.Unlock()
					}
					if in.Submitted() {
						openPreviewURL(s, addr)
					}
					errorCount := previewConsoleErrorCount(snap.consoleErrors)
					if captureConsole && (len(snap.consoleErrors) > 0 || snap.consoleOpen) {
						badgeBtn := ui.ButtonBase(c.Key("preview-console-badge")).
							Label("控制台").
							Tooltip("展开或收起控制台").
							Radius(RadiusXS).
							Padding(Space0, Space2)
						if errorCount > 0 {
							badgeBtn.Background(ThemeDanger(c).Alpha(0.18))
						}
						badgeBtnScale := motionPressScale(c, badgeBtn, "button-press-scale", badgeBtn.Pressed())
						badgeBtn.Scale(badgeBtnScale, badgeBtnScale)
						if badgeBtn.Clicked() {
							previewMu.Lock()
							if current := previewStateForTokenLocked(s, snap.token); current != nil {
								current.consoleOpen = !snap.consoleOpen
							}
							previewMu.Unlock()
							s.invalidate()
						}
						badgeBtn.Children(func() {
							ui.Text(c, "控制台").FontSize(uiFont(TypeCaption))
							previous := ui.Local(badgeBtn, "error-count", func() int { return errorCount })
							_, fade := motionNumberProgress(c, badgeBtn, *previous != errorCount)
							*previous = errorCount
							if errorCount > 0 {
								ui.Text(c, fmt.Sprint(errorCount)).FontSize(codeFontSize()).TextColor(ThemeDanger(c).Alpha(fade))
							}
						})
					}

					label := "选择元素"
					if snap.annotating {
						label = "退出选择"
					}
					IconButton(c, IconButtonProps{Key: "preview-annotate-btn", Icon: IconHighlighter(), Label: label, Tooltip: label, Size: ControlXS, Variant: ButtonGhost, Active: snap.annotating, OnClick: func() {
						togglePreviewAnnotate(s, st)
					}})
					IconButton(c, IconButtonProps{Key: "preview-screenshot-btn", Icon: previewScreenshotIcon, Label: "截图", Tooltip: "截图", Size: ControlXS, Variant: ButtonGhost, OnClick: func() { previewCaptureScreenshot(s, st) }})
					menuBtn := IconButton(c, IconButtonProps{Key: "preview-capture-menu-btn", Icon: IconMoreHorizontal(), Label: "更多", Tooltip: "更多", Size: ControlXS, Variant: ButtonGhost, OnClick: func() {
						st.owner.captureMenuOpen = !st.owner.captureMenuOpen
					}})
					background := ThemeSurfacePopover(c)
					native := flyoutHostWindow(c.Services()) != nil
					appFlyoutPopover(c, "preview-capture-menu", menuBtn, &st.owner.captureMenuOpen, func() { st.owner.captureMenuOpen = false }, appFlyoutProps{Width: 180, Background: &background}, func(fc *ui.Context) {
						card := ui.Column(fc.Key("preview-capture-menu-panel"))
						if !native {
							card = popoverCard(fc, card)
						}
						card.Padding(Space1).Transition(motionPopover(fc, motionRiseOffset)).Children(func() {
							for _, mode := range previewViewportModes() {
								var icon *ui.SVG
								if snap.viewport == mode {
									icon = IconCheck()
								}
								Button(fc.Key("preview-viewport-"+string(mode)), ButtonOpts{Text: previewViewportLabel(mode), Icon: icon, Variant: ButtonGhost, OnClick: func() {
									st.owner.captureMenuOpen = false
									setPreviewViewport(s, st, mode)
								}})
							}
							Button(fc.Key("browser-new-tab"), ButtonOpts{Text: "新建标签", Variant: ButtonGhost, OnClick: func() {
								st.owner.captureMenuOpen = false
								browserNewTab(s)
							}})
							if target := previewExternalURL(snap); target != "" {
								Button(fc.Key("preview-open-external-btn"), ButtonOpts{Text: "在系统浏览器打开", Variant: ButtonGhost, OnClick: func() {
									st.owner.captureMenuOpen = false
									fc.OpenURL(target)
								}})
							}
						})
					})

				})

			// 主体区域
			if snap.err != "" && snap.web != nil {
				ui.Row(c.Key("preview-err-bar")).
					Padding(Space3).
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						ui.Text(c, snap.err).
							TextColor(ThemeDanger(c)).
							FontSize(uiFont(TypeFootnote)).
							Grow(1).
							MinWidth(0)
						Button(c, ButtonOpts{
							Key:     "preview-retry-btn",
							Text:    "重试",
							Size:    ControlXS,
							Variant: ButtonSecondary,
							OnClick: func() { browserReloadOrStop(s, st) },
						})
					})
			}
			if snap.web == nil {
				ui.Column(c.Key("preview-empty-state")).
					Grow(1).
					FillHeight().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Children(func() {
						msg := snap.err
						if msg == "" {
							msg = "未打开网页"
						}
						if snap.url == "" && snap.html == "" {
							ui.Column(c.Key("preview-server-candidates")).Gap(Space2).AlignItems(ui.Center).Children(func() {
								renderBrowserServerCandidates(c, s, threadID, getBrowserQuickPorts(s))
							})
						} else {
							var retryAction func()
							if snap.err != "" {
								retryAction = func() {
									Button(c, ButtonOpts{
										Key:     "preview-empty-retry-btn",
										Text:    "重试",
										Size:    ControlXS,
										Variant: ButtonSecondary,
										OnClick: func() { browserReloadOrStop(s, st) },
									})
								}
							}
							EmptyState(c, EmptyStateProps{Icon: IconGlobe(), Title: msg, Action: retryAction})
						}
					})
			} else if browserTabVisible(st) {
				renderPreviewViewport(c, snap)
				if len(snap.annotations) > 0 {
					renderPreviewAnnotateDrawer(c, s, snap)
				}
				if captureConsole {
					renderPreviewConsoleDrawer(c, s, st, snap)
				}
			}
			if snap.feedbackThreadID != "" {
				renderPreviewFeedbackBar(c, s, snap)
			}
		})
}

// renderPreviewViewport 渲染 Web 视图：桌面铺满面板，平板与手机居中并限制宽度。
func renderPreviewViewport(c *ui.Context, st previewSnapshot) {
	width := previewViewportWidth(st.viewport)
	if width <= 0 {
		ui.WebView(c, st.web).Grow(1)
		return
	}

	ui.Row(c.Key("preview-viewport-row-" + string(st.viewport))).
		Grow(1).
		FillWidth().
		Justify(ui.Center).
		Background(ThemeSidebarBg(c)).
		Children(func() {
			ui.WebView(c, st.web).Width(width)
		})
}
