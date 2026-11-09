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
	previewMu     sync.Mutex
	previewStates = make(map[*chatState]*previewState)
)

// previewState 维护单个聊天窗口下的 Web 预览状态。
type previewState struct {
	visible bool
	url     string
	html    string
	web     *mygo.WebView
	width   float32
	addr    string
	err     string
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
	previewMu.Lock()
	defer previewMu.Unlock()
	st, ok := previewStates[s]
	if !ok {
		st = &previewState{
			width:    sidePaneInitialWidth(s, sidePanePreviewID, PreviewPanelMinWidth, PreviewPanelDefaultWidth, PreviewPanelMaxWidth),
			viewport: previewViewportDesktop,
			addr:     startURL,
		}
		previewStates[s] = st
	}
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
	if st.consolePolling || st.web == nil || st.web.IsDestroyed() {
		previewMu.Unlock()
		return
	}
	if !force && time.Since(st.consoleLastPoll) < previewConsolePollInterval {
		previewMu.Unlock()
		return
	}
	st.consoleLastPoll = time.Now()
	st.consolePolling = true
	web := st.web
	previewMu.Unlock()

	go func() {
		var errors []previewConsoleError
		if !web.IsDestroyed() && web.Page() != nil {
			if raw, err := web.Page().Eval(previewConsoleScript); err == nil {
				if text, ok := raw.(string); ok && text != "" {
					_ = json.Unmarshal([]byte(text), &errors)
				}
			}
		}

		previewMu.Lock()
		st.consolePolling = false
		changed := !previewConsoleErrorsEqual(st.consoleErrors, errors)
		if changed {
			st.consoleErrors = errors
			if len(errors) == 0 {
				st.consoleOpen = false
			}
		}
		previewMu.Unlock()

		if changed && s != nil {
			s.invalidate()
		}
	}()
}

// clearPreviewConsoleErrors 清空页面与本地收集的控制台错误。
func clearPreviewConsoleErrors(s *chatState, st *previewState) {
	if st == nil || !previewConsoleCaptureEnabled(s) {
		return
	}
	previewMu.Lock()
	web := st.web
	st.consoleErrors = nil
	st.consoleOpen = false
	previewMu.Unlock()

	if web != nil && !web.IsDestroyed() && web.Page() != nil {
		go func() {
			_, _ = web.Page().Eval(previewConsoleClearScript)
			// 清空后立刻重读，避免上一次轮询的旧结果把列表又填回来。
			previewMu.Lock()
			st.consoleLastPoll = time.Time{}
			previewMu.Unlock()
			previewMaybePollConsoleErrors(s, st, true)
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
func ensureWebView(s *chatState, st *previewState) {
	if st.web != nil && !st.web.IsDestroyed() {
		return
	}
	st.web = nil
	if st.webFailed {
		return
	}

	var win *mygo.Window
	if s != nil && s.window != nil {
		win = s.window()
	}
	if win == nil {
		st.err = "未关联宿主窗口，无法创建 Web 视图"
		st.webFailed = true
		return
	}

	wv, err := win.NewWebView(mygo.WebViewOptions{})
	if err != nil {
		st.err = "创建 Web 视图失败: " + err.Error()
		st.webFailed = true
		return
	}

	st.web = wv
	st.err = ""
	st.webFailed = false

	// 页面每次加载完成后注入错误收集器并立刻读一次；注入脚本自身幂等。
	// 关闭控制台错误采集时既不注入脚本，也不轮询。
	if previewConsoleCaptureEnabled(s) {
		if page := wv.Page(); page != nil {
			page.OnDidFinishLoad(func() {
				go previewMaybePollConsoleErrors(s, st, true)
			})
		}
	}

	loadPreviewPage(st)
}

func loadPreviewPage(st *previewState) {
	if st.web == nil || st.web.IsDestroyed() || st.web.Page() == nil {
		return
	}
	// 页面一换，旧的 selector 就指向不了任何元素：退出标注模式并清空已选元素。
	previewMu.Lock()
	previewResetAnnotationsLocked(st)
	previewMu.Unlock()
	st.err = ""
	if st.url != "" {
		if err := st.web.Page().LoadURL(st.url); err != nil {
			st.err = "加载预览失败: " + err.Error()
		}
	} else if st.addr == "html-preview" {
		st.web.Page().LoadHTML(st.html, "")
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
	st.visible = true
	st.url = target
	st.addr = target
	st.html = ""
	// 用户重新打开/改地址时重置失败标记，允许再试一次创建 WebView。
	st.webFailed = false
	previewMu.Unlock()

	// 右侧只留一个分栏：预览打开时先关掉文件面板与改动面板（改动与预览也互斥）。
	closeOtherSidePanes(s, sidePanePreviewID)

	if st.web != nil && !st.web.IsDestroyed() {
		loadPreviewPage(st)
	} else {
		ensureWebView(s, st)
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
	st.visible = true
	st.html = html
	st.url = ""
	st.addr = "html-preview"
	// 用户重新打开时重置失败标记，允许再试一次创建 WebView。
	st.webFailed = false
	previewMu.Unlock()

	// 右侧只留一个分栏：预览打开时先关掉文件面板与改动面板（改动与预览也互斥）。
	closeOtherSidePanes(s, sidePanePreviewID)

	if st.web != nil && !st.web.IsDestroyed() {
		loadPreviewPage(st)
	} else {
		ensureWebView(s, st)
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

	previewMu.Lock()
	st.visible = false
	// 关闭预览时一并退出标注模式并清空标注，下次打开不残留旧页面的 selector。
	previewResetAnnotationsLocked(st)
	web := st.web
	st.web = nil
	previewMu.Unlock()

	if web != nil && !web.IsDestroyed() {
		web.Destroy()
	}
	s.invalidate()
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

	previewMu.Lock()
	st.visible = !st.visible
	if st.visible && strings.TrimSpace(st.addr) == "" {
		st.addr = startURL
	}
	if st.visible {
		// 重新打开面板时重置创建失败标记，给 WebView 一次新的创建机会。
		st.webFailed = false
	}
	visible := st.visible
	previewMu.Unlock()

	if visible {
		// 右侧只留一个分栏：预览打开时先关掉文件面板与改动面板（改动与预览也互斥）。
		closeOtherSidePanes(s, sidePanePreviewID)
	}

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
					in := ui.TextInput(c.Key("preview-addr-input"), &st.addr).
						Placeholder("输入 URL (例如 localhost:3000)...").
						FontSize(TypeFootnote).
						Grow(1)
					if in.Submitted() {
						openPreviewURL(s, st.addr)
					}

					// 视口分段选择：桌面 / 平板 / 手机
					for _, mode := range previewViewportModes() {
						mode := mode
						selected := st.viewport == mode
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
							st.addr = target
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
					if captureConsole && len(st.consoleErrors) > 0 {
						badgeBtn := ui.ButtonBase(c.Key("preview-console-badge")).
							Label(fmt.Sprintf("控制台错误 %d", len(st.consoleErrors))).
							Tooltip("展开或收起控制台错误").
							Radius(RadiusXS).
							Padding(Space0, Space2).
							Background(ThemeDanger(c).Alpha(0.18))
						if badgeBtn.Clicked() {
							previewMu.Lock()
							st.consoleOpen = !st.consoleOpen
							previewMu.Unlock()
							s.invalidate()
						}
						badgeBtn.Children(func() {
							ui.Text(c, fmt.Sprintf("! %d", len(st.consoleErrors))).
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
					if st.annotating {
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
						if st.annotating {
							color = ThemeAccent(c)
						}
						ui.Row(c.Key("preview-annotate-btn-row")).Gap(Space0).AlignItems(ui.Center).Children(func() {
							ui.Icon(c, IconHighlighter()).Size(IconSM, IconSM).TextColor(color)
							if len(st.annotations) > 0 {
								ui.Text(c, fmt.Sprintf("%d", len(st.annotations))).
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
						st.webFailed = false
						ensureWebView(s, st)
						loadPreviewPage(st)
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
						target := st.url
						if target == "" && st.addr != "" && st.addr != "html-preview" {
							target = normalizePreviewURL(st.addr)
						}
						if target != "" {
							c.OpenURL(target)
						}
					}
					openExternalBtn.Children(func() {
						ui.Icon(c, IconGlobe()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					})

					// 关闭按钮
					IconButton(c, IconButtonProps{
						Key: "preview-close-btn", Icon: IconX(), Label: "关闭预览", Tooltip: "关闭预览",
						Size: ControlXS, Variant: ButtonGhost, OnClick: func() { closePreview(s) },
					})
				})

			// 主体区域
			ensureWebView(s, st)
			if st.err != "" && st.web != nil {
				ui.Text(c, st.err).TextColor(ThemeDanger(c)).FontSize(TypeFootnote).Padding(Space3)
			}
			if st.web == nil {
				ui.Column(c.Key("preview-empty-state")).
					Grow(1).
					FillHeight().
					Justify(ui.Center).
					AlignItems(ui.Center).
					Children(func() {
						msg := st.err
						if msg == "" {
							msg = "暂无预览内容或 Web 视图不可用"
						}
						ui.Text(c, msg).TextColor(ThemeSecondaryText(c)).FontSize(TypeBody)
					})
			} else {
				// 轮询页面收集到的 JS 错误与元素标注；没有推送式消息通道时按最小间隔读取。
				previewMaybePollConsoleErrors(s, st, false)
				previewMaybePollAnnotations(s, st, false)
				renderPreviewViewport(c, st)
				if st.annotOpen && len(st.annotations) > 0 {
					renderPreviewAnnotateDrawer(c, s, st)
				}
				if captureConsole && st.consoleOpen && len(st.consoleErrors) > 0 {
					renderPreviewConsoleDrawer(c, s, st)
				}
			}
		})
}

// renderPreviewViewport 渲染 Web 视图：桌面铺满面板，平板与手机居中并限制宽度。
func renderPreviewViewport(c *ui.Context, st *previewState) {
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
func renderPreviewConsoleDrawer(c *ui.Context, s *chatState, st *previewState) {
	previewMu.Lock()
	entries := append([]previewConsoleError(nil), st.consoleErrors...)
	previewMu.Unlock()

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
