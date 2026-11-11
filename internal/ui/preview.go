package mcodeui

import (
	"encoding/json"
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

	// previewConsolePollInterval 是读取页面 JS 错误的最小间隔。
	previewConsolePollInterval = 700 * time.Millisecond
)

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

// previewConsoleError 描述页面里捕获到的一条 JS 错误或 console.error 记录。
type previewConsoleError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Source  string `json:"source"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
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

// previewConsoleScript 在页面中安装一次性的错误收集器，并返回已收集的错误 JSON。
// mygo 的 WebView 没有推送式消息通道，因此脚本由 Page.Eval 注入并按间隔轮询读取。
const previewConsoleScript = `(function () {
  if (!window.__mcodePreviewConsoleErrors) { window.__mcodePreviewConsoleErrors = []; }
  if (!window.__mcodePreviewConsoleHook) {
    window.__mcodePreviewConsoleHook = true;
    var push = function (kind, message, source, line, column) {
      try {
        window.__mcodePreviewConsoleErrors.push({
          kind: kind,
          message: String(message == null ? "" : message),
          source: String(source == null ? "" : source),
          line: line || 0,
          column: column || 0
        });
        if (window.__mcodePreviewConsoleErrors.length > 100) { window.__mcodePreviewConsoleErrors.shift(); }
      } catch (e) {}
    };
    window.addEventListener("error", function (event) {
      push("error", event && event.message, event && event.filename, event && event.lineno, event && event.colno);
    });
    window.addEventListener("unhandledrejection", function (event) {
      var reason = event && event.reason;
      push("rejection", reason && reason.message ? reason.message : reason, "", 0, 0);
    });
    var originalError = console.error;
    console.error = function () {
      var parts = [];
      for (var i = 0; i < arguments.length; i++) {
        var value = arguments[i];
        try { parts.push(typeof value === "string" ? value : JSON.stringify(value)); }
        catch (e) { parts.push(String(value)); }
      }
      push("console", parts.join(" "), "", 0, 0);
      return originalError.apply(console, arguments);
    };
  }
  return JSON.stringify(window.__mcodePreviewConsoleErrors || []);
})()`

// previewConsoleClearScript 清空页面中已收集的错误。
const previewConsoleClearScript = `(function () {
  window.__mcodePreviewConsoleErrors = [];
  return "cleared";
})()`

var (
	previewMu         sync.Mutex
	previewStates     = make(map[*chatState]*previewState)
	previewNextPageID uint64
)

// newPreviewWebView 是创建原生 WebView 的唯一入口；测试替换它来检查创建选项。
var newPreviewWebView = func(win *mygo.Window, opts mygo.WebViewOptions) (*mygo.WebView, error) {
	return win.NewWebView(opts)
}

// previewState 维护单个聊天窗口下的 Web 预览状态。
type previewState struct {
	visible                        bool
	url                            string
	html                           string
	web                            *mygo.WebView
	page                           previewPage
	pageID, generation             uint64
	consoleRevision, annotRevision uint64
	webCreating                    bool
	pageOff                        func()
	// untrusted / pageUntrusted 记录当前内容想要的信任级别与已创建页面的级别；
	// 本地内容一律 Untrusted，切换内容时若级别不同就先释放旧页面再重建。
	untrusted bool
	// pageUntrusted 只在创建页面时写入，由 previewMu 保护。
	pageUntrusted bool
	// localPreview 与 previewThreadID 记录本地内容页：释放时撤销该会话的 Token。
	localPreview    bool
	previewThreadID string

	nextAnnotationID uint64
	addr             string
	err              string
	// webFailed 记录原生 WebView 创建失败：失败原因（无宿主窗口等）不变时按帧重试只会反复失败，
	// 因此失败后不再自动重试，由用户重新打开预览、改地址或点刷新时重置（见 ensureWebView）。
	webFailed bool
	// viewport 为桌面 / 平板 / 手机视口模式。
	viewport previewViewportMode
	// consoleErrors 是最近一次轮询到的页面 JS 错误，consoleOpen 控制底部抽屉。
	consoleErrors   []previewConsoleError
	consoleOpen     bool
	consolePolling  bool
	consoleLastPoll time.Time
	// annotating 为真时预览处于元素标注模式；annotations 是已圈选元素（含评论），
	// annotOpen 控制底部标注抽屉，annotPolling/annotLastPoll 复用错误轮询的间隔与节流。
	annotating    bool
	annotations   []previewAnnotation
	annotOpen     bool
	annotPolling  bool
	annotLastPoll time.Time
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

// previewStartURL 返回预览面板的默认起始地址，未配置时使用内置默认值。
func previewStartURL(s *chatState) string {
	if raw := strings.TrimSpace(previewWorkbenchSettings(s).BrowserStartURL); raw != "" {
		return raw
	}
	return config.DefaultBrowserStartURL
}

// previewConsoleCaptureEnabled 返回预览面板是否启用控制台错误采集。
func previewConsoleCaptureEnabled(s *chatState) bool {
	return previewWorkbenchSettings(s).BrowserConsoleCaptureEnabled()
}

// getPreviewState 获取或初始化当前 chatState 的预览状态。
func getPreviewState(s *chatState) *previewState {
	if s == nil {
		return nil
	}
	startURL := previewStartURL(s)
	slot := inspectorSession(s)
	if slot == nil {
		return nil
	}
	previewMu.Lock()
	defer previewMu.Unlock()
	st := slot.Browser
	if st.addr == "" {
		st.addr = startURL
	}
	previewStates[s] = st
	return st
}

// setPreviewViewport 切换视口模式并请求重绘。
func setPreviewViewport(s *chatState, st *previewState, mode previewViewportMode) {
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

// previewMaybePollConsoleErrors 按最小间隔读取页面收集到的 JS 错误。
// force 为真时忽略间隔（用于页面加载完成后的首次读取）。
func previewMaybePollConsoleErrors(s *chatState, st *previewState, force bool) {
	if st == nil || !previewConsoleCaptureEnabled(s) {
		return
	}
	previewMu.Lock()
	if !st.visible || st.consolePolling || st.page == nil {
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

func previewPollConsoleErrors(s *chatState, token previewPageToken, page previewPage) {
	previewMu.Lock()
	current := previewStateForTokenLocked(s, token)
	valid := current != nil && current.page != nil && current.consoleRevision == token.consoleRevision
	previewMu.Unlock()
	if !valid {
		return
	}
	raw, err := page.Eval(previewConsoleScript)
	var entries []previewConsoleError
	if text, ok := raw.(string); err == nil && ok && text != "" {
		err = json.Unmarshal([]byte(text), &entries)
	}
	previewMu.Lock()
	st := previewStateForTokenLocked(s, token)
	if st == nil || st.page == nil || st.consoleRevision != token.consoleRevision {
		previewMu.Unlock()
		return
	}
	st.consolePolling = false
	changed := err == nil && !previewConsoleErrorsEqual(st.consoleErrors, entries)
	if changed {
		st.consoleErrors = entries
		if len(entries) == 0 {
			st.consoleOpen = false
		}
	}
	previewMu.Unlock()
	if changed && s != nil {
		s.invalidate()
	}
}

// clearPreviewConsoleErrors 清空页面与本地收集的控制台错误。
func clearPreviewConsoleErrors(s *chatState, st *previewState) {
	if st == nil || !previewConsoleCaptureEnabled(s) {
		return
	}
	previewMu.Lock()
	page := st.page
	st.consoleRevision++
	token := previewPageTokenLocked(st)
	st.consolePolling = false
	// 清空脚本完成前暂停新轮询，避免再读到页面侧尚未清掉的数据。
	if page != nil {
		st.consolePolling = true
	}
	st.consoleErrors = nil
	st.consoleOpen = false
	previewMu.Unlock()

	if page != nil {
		go func() {
			previewMu.Lock()
			current := previewStateForTokenLocked(s, token)
			valid := current != nil && current.page != nil && current.consoleRevision == token.consoleRevision
			previewMu.Unlock()
			if !valid {
				return
			}
			_, _ = page.Eval(previewConsoleClearScript)
			// 清空后立刻重读，避免上一次轮询的旧结果把列表又填回来。
			previewMu.Lock()
			current = previewStateForTokenLocked(s, token)
			if current == nil || current.consoleRevision != token.consoleRevision {
				previewMu.Unlock()
				return
			}
			current.consoleLastPoll = time.Now()
			previewMu.Unlock()
			previewPollConsoleErrors(s, token, page)
		}()
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

// ensureWebView 保证 previewState 中的 webview 实例已初始化。
// 创建失败后置 webFailed 并停手：本函数在渲染路径里每帧调用，若失败还继续尝试就会以帧率重试。
func ensureWebView(s *chatState, st *previewState) bool {
	previewMu.Lock()
	web, pageExists, generation := st.web, st.page != nil, st.generation
	untrustedMismatch := pageExists && st.pageUntrusted != st.untrusted
	blocked := st.webFailed || st.webCreating
	previewMu.Unlock()
	if blocked {
		return false
	}
	if pageExists && !untrustedMismatch && (web == nil || !web.IsDestroyed()) {
		return false
	}
	previewMu.Lock()
	if st.web != web || st.generation != generation || st.webCreating {
		previewMu.Unlock()
		return false
	}
	if st.page != nil && st.pageUntrusted != st.untrusted {
		// 信任级别变了（普通网页 ↔ 本地内容）：旧页面连同 WebView 一起释放，
		// 新一代页面使用新的 Untrusted 选项，旧回包由 generation 丢弃。
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
		wv, err = newPreviewWebView(win, mygo.WebViewOptions{
			Page: mygo.PageOptions{Untrusted: untrusted},
		})
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

func loadPreviewPage(s *chatState, st *previewState) {
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
			}
			previewMu.Unlock()
		}
	} else if addr == "html-preview" {
		page.LoadHTML(html, "")
	}
}

// openPreviewURL 打开指定 URL 并显示预览面板。
func openPreviewURL(s *chatState, raw string) {
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
	previewAdvanceGenerationLocked(st)
	st.webCreating = false
	st.url = target
	st.addr = target
	st.html = ""
	st.untrusted = false
	st.localPreview = false
	// 用户重新打开/改地址时重置失败标记，允许再试一次创建 WebView。
	st.webFailed = false
	previewMu.Unlock()

	inspectorSelect(s, sidePanePreviewID, false)

	if !ensureWebView(s, st) {
		loadPreviewPage(s, st)
	}
	s.invalidate()
}

// openPreviewHTML 载入指定 HTML 字符串并显示预览面板。
func openPreviewHTML(s *chatState, html string) {
	if s == nil {
		return
	}
	st := getPreviewState(s)
	if st == nil {
		return
	}

	previewMu.Lock()
	previewAdvanceGenerationLocked(st)
	st.webCreating = false
	st.html = html
	st.url = ""
	st.addr = "html-preview"
	st.untrusted = false
	st.localPreview = false
	// 用户重新打开时重置失败标记，允许再试一次创建 WebView。
	st.webFailed = false
	previewMu.Unlock()

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
	previewAdvanceGenerationLocked(st)
	st.webCreating = false
	st.url = target
	st.addr = target
	st.html = ""
	st.untrusted = true
	st.localPreview = true
	st.previewThreadID = threadID
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
	disposePreviewState(s, st)
	s.invalidate()
}

// disposePreviewState 只用于关闭网页、删除会话和关闭窗口。
func disposePreviewState(s *chatState, st *previewState) {
	if st == nil {
		return
	}
	previewMu.Lock()
	st.visible = false
	// 关闭预览时一并退出标注模式并清空标注，下次打开不残留旧页面的 selector。
	previewAdvanceGenerationLocked(st)
	st.webCreating = false
	page := st.page
	off := st.pageOff
	st.pageOff = nil
	st.web = nil
	st.page = nil
	// 本地内容页关闭后 Token 立即失效；清掉地址，避免再次打开面板时加载已撤销的 URL。
	threadID := st.previewThreadID
	st.previewThreadID = ""
	if st.localPreview {
		st.localPreview = false
		st.url = ""
		st.html = ""
		st.addr = ""
	}
	previewMu.Unlock()

	if s != nil && threadID != "" {
		if client, ok := s.client.(inspectorPreviewClient); ok {
			client.RevokeInspectorPreviews(threadID)
		}
	}
	if off != nil {
		off()
	}
	if page != nil {
		page.Destroy()
	}
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
	startURL := previewStartURL(s)

	visible := inspectorSelect(s, sidePanePreviewID, true)
	previewMu.Lock()
	if visible && strings.TrimSpace(st.addr) == "" {
		st.addr = startURL
	}
	if visible {
		// 重新打开面板时重置创建失败标记，给 WebView 一次新的创建机会。
		st.webFailed = false
	}
	previewMu.Unlock()

	s.invalidate()
}

// renderPreviewPanel 渲染右侧 Web 预览面板。
func renderPreviewPanel(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	st := getPreviewState(s)
	if st == nil {
		return
	}
	captureConsole := previewConsoleCaptureEnabled(s)
	ensureWebView(s, st)
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
			// 顶栏 Toolbar
			ui.Row(c.Key("preview-toolbar")).
				Height(previewToolbarHeight).
				Padding(0, Space3).
				Gap(Space2).
				AlignItems(ui.Center).
				BorderWidth(0, 0, Hairline, 0).
				BorderColor(ThemeBorder(c)).
				Children(func() {
					// 地址输入框
					addr := snap.addr
					in := ui.TextInput(c.Key("preview-addr-input"), &addr).
						Placeholder("输入 URL (例如 localhost:3000)...").
						FontSize(TypeFootnote).
						Grow(1)
					if addr != snap.addr {
						previewMu.Lock()
						if current := previewStateForTokenLocked(s, snap.token); current != nil {
							current.addr = addr
						}
						previewMu.Unlock()
					}
					if in.Submitted() {
						openPreviewURL(s, addr)
					}

					// 视口分段选择：桌面 / 平板 / 手机
					for _, mode := range previewViewportModes() {
						mode := mode
						selected := snap.viewport == mode
						viewportBtn := ui.ButtonBase(c.Key("preview-viewport-"+string(mode))).
							Label(previewViewportLabel(mode)).
							Tooltip(previewViewportLabel(mode)+"视口").
							Radius(RadiusXS).
							Padding(Space0, Space2).
							Background(ThemeOverlay(c, 0.06))
						if selected {
							viewportBtn.Background(ThemeSelected(c))
						}
						if viewportBtn.Hovered() {
							viewportBtn.Background(ThemeHover(c))
						}
						if viewportBtn.Clicked() {
							setPreviewViewport(s, st, mode)
						}
						viewportBtn.Children(func() {
							color := ThemeSecondaryText(c)
							if selected {
								color = ThemeAccent(c)
							}
							ui.Text(c, previewViewportLabel(mode)).
								FontSize(TypeCaption).
								FontWeight(500).
								TextColor(color)
						})
					}

					// 快捷端口胶囊
					quickPorts := getBrowserQuickPorts(s)
					for _, port := range quickPorts {
						portStr := port
						btn := ui.ButtonBase(c.Key("preview-port-"+portStr)).
							Label(portStr).
							Tooltip("http://localhost:"+portStr).
							Radius(RadiusXS).
							Padding(Space0, Space2).
							Background(ThemeOverlay(c, 0.06))
						if btn.Hovered() {
							btn.Background(ThemeHover(c))
						}
						if btn.Clicked() {
							target := "http://localhost:" + portStr
							openPreviewURL(s, target)
						}
						btn.Children(func() {
							ui.Text(c, portStr).
								FontSize(TypeCode).
								Font("monospace").
								FontWeight(500).
								TextColor(ThemeSecondaryText(c))
						})
					}

					// 控制台错误胶囊：有错误时显示数量，点击展开底部错误抽屉
					if captureConsole && len(snap.consoleErrors) > 0 {
						badgeBtn := ui.ButtonBase(c.Key("preview-console-badge")).
							Label(fmt.Sprintf("控制台错误 %d", len(snap.consoleErrors))).
							Tooltip("展开或收起控制台错误").
							Radius(RadiusXS).
							Padding(Space0, Space2).
							Background(ThemeDanger(c).Alpha(0.18))
						if badgeBtn.Clicked() {
							previewMu.Lock()
							if current := previewStateForTokenLocked(s, snap.token); current != nil {
								current.consoleOpen = !snap.consoleOpen
							}
							previewMu.Unlock()
							s.invalidate()
						}
						badgeBtn.Children(func() {
							ui.Text(c, fmt.Sprintf("! %d", len(snap.consoleErrors))).
								FontSize(TypeCode).
								FontWeight(600).
								Font("monospace").
								TextColor(ThemeDanger(c))
						})
					}

					// 标注按钮：进入/退出元素标注模式，有标注时在图标后显示数量
					annotBtn := ui.ButtonBase(c.Key("preview-annotate-btn")).
						Label("标注").
						Tooltip("标注页面元素并向 Agent 留言").
						Radius(RadiusXS).
						Padding(Space0, Space2).
						Background(ThemeOverlay(c, 0.06))
					if snap.annotating {
						annotBtn.Background(ThemeSelected(c))
					}
					if annotBtn.Hovered() {
						annotBtn.Background(ThemeHover(c))
					}
					if annotBtn.Clicked() {
						togglePreviewAnnotate(s, st)
					}
					annotBtn.Children(func() {
						color := ThemeSecondaryText(c)
						if snap.annotating {
							color = ThemeAccent(c)
						}
						ui.Row(c.Key("preview-annotate-btn-row")).Gap(Space0).AlignItems(ui.Center).Children(func() {
							ui.Icon(c, IconHighlighter()).Size(IconSM, IconSM).TextColor(color)
							if len(snap.annotations) > 0 {
								ui.Text(c, fmt.Sprintf("%d", len(snap.annotations))).
									FontSize(TypeCaption).
									FontWeight(600).
									TextColor(color)
							}
						})
					})

					// 刷新按钮
					reloadBtn := ui.ButtonBase(c.Key("preview-reload-btn")).
						Tooltip("刷新").
						Label("刷新").
						Radius(RadiusXS).
						Padding(Space1, Space2)
					if reloadBtn.Hovered() {
						reloadBtn.Background(ThemeHover(c))
					}
					if reloadBtn.Clicked() {
						// 刷新按钮是创建失败后的人工重试入口：先清失败标记再建视图，最后重新加载。
						previewMu.Lock()
						previewAdvanceGenerationLocked(st)
						st.webCreating = false
						st.webFailed = false
						previewMu.Unlock()
						if !ensureWebView(s, st) {
							loadPreviewPage(s, st)
						}
					}
					reloadBtn.Children(func() {
						ui.Icon(c, IconRefreshCw()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					})

					// 在系统浏览器打开按钮
					openExternalBtn := ui.ButtonBase(c.Key("preview-open-external-btn")).
						Tooltip("在系统浏览器打开").
						Label("在系统浏览器打开").
						Radius(RadiusXS).
						Padding(Space1, Space2)
					if openExternalBtn.Hovered() {
						openExternalBtn.Background(ThemeHover(c))
					}
					if openExternalBtn.Clicked() {
						target := snap.url
						if target == "" && snap.addr != "" && snap.addr != "html-preview" {
							target = normalizePreviewURL(snap.addr)
						}
						if target != "" {
							c.OpenURL(target)
						}
					}
					openExternalBtn.Children(func() {
						ui.Icon(c, IconGlobe()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					})

				})

			// 主体区域
			if snap.err != "" && snap.web != nil {
				ui.Text(c, snap.err).TextColor(ThemeDanger(c)).FontSize(TypeFootnote).Padding(Space3)
			}
			if snap.web == nil {
				ui.Column(c.Key("preview-empty-state")).
					Grow(1).
					FillHeight().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Children(func() {
						msg := snap.err
						if msg != "" {
							EmptyState(c, EmptyStateProps{Icon: IconGlobe(), Title: msg})
						}
					})
			} else {
				// 轮询页面收集到的 JS 错误与元素标注；没有推送式消息通道时按最小间隔读取。
				previewMaybePollConsoleErrors(s, st, false)
				previewMaybePollAnnotations(s, st, false)
				renderPreviewViewport(c, snap)
				if snap.annotOpen && len(snap.annotations) > 0 {
					renderPreviewAnnotateDrawer(c, s, snap)
				}
				if captureConsole && snap.consoleOpen && len(snap.consoleErrors) > 0 {
					renderPreviewConsoleDrawer(c, s, st, snap)
				}
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

// renderPreviewConsoleDrawer 渲染底部控制台错误抽屉，使用不透明背景，可清空。
func renderPreviewConsoleDrawer(c *ui.Context, s *chatState, st *previewState, snap previewSnapshot) {
	entries := snap.consoleErrors

	ui.Column(c.Key("preview-console-drawer")).
		Height(previewConsoleDrawerHeight).
		FillWidth().
		Background(ThemeWindowBg(c)).
		BorderWidth(Hairline, 0, 0, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			ui.Row(c.Key("preview-console-header")).
				Height(ControlMD).
				Padding(0, Space3).
				AlignItems(ui.Center).
				BorderWidth(0, 0, Hairline, 0).
				BorderColor(ThemeBorder(c)).
				Children(func() {
					ui.Text(c, fmt.Sprintf("控制台错误 (%d)", len(entries))).
						FontSize(TypeFootnote).
						FontWeight(600).
						TextColor(ThemePrimaryText(c))

					ui.Spacer(c)

					clearBtn := ui.ButtonBase(c.Key("preview-console-clear")).
						Label("清空").
						Radius(RadiusXS).
						Padding(Space0, Space3)
					if clearBtn.Hovered() {
						clearBtn.Background(ThemeHover(c))
					}
					if clearBtn.Clicked() {
						clearPreviewConsoleErrors(s, st)
					}
					clearBtn.Children(func() {
						ui.Text(c, "清空").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
					})
				})

			ui.Scroll(c.Key("preview-console-scroll")).
				Grow(1).
				FillWidth().
				Padding(Space1, Space3).
				Children(func() {
					for _, entry := range entries {
						entry := entry
						ui.Text(c, previewConsoleEntryLine(entry)).
							FontSize(TypeCode).
							Font("monospace").
							TextColor(ThemeDanger(c)).
							Padding(Hairline, 0)
					}
				})
		})
}
