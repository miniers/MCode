package mcodeui

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
)

type previewTestPage struct {
	eval func(string) (any, error)
}

func (p *previewTestPage) Eval(script string) (any, error) { return p.eval(script) }
func (p *previewTestPage) Destroy()                        {}

type previewTestEventPage struct {
	previewTestPage
	navigate func(string)
	finish   func()
	offCount int
}

func (p *previewTestEventPage) OnDidNavigate(fn func(string)) func() {
	previewMu.Lock()
	previewMu.Unlock()
	p.navigate = fn
	return func() {
		previewMu.Lock()
		previewMu.Unlock()
		p.offCount++
	}
}

func (p *previewTestEventPage) OnDidFinishLoad(fn func()) func() {
	previewMu.Lock()
	previewMu.Unlock()
	p.finish = fn
	return func() {
		previewMu.Lock()
		previewMu.Unlock()
		p.offCount++
	}
}

func TestPreviewPageCallbacksDropOldGeneration(t *testing.T) {
	s := &chatState{}
	st := getPreviewState(s)
	page := &previewTestEventPage{previewTestPage: previewTestPage{eval: func(string) (any, error) { return `[]`, nil }}}
	previewMu.Lock()
	st.page, st.pageID = page, 42
	st.consoleErrors = []previewConsoleError{{Message: "旧错误"}}
	token := previewPageTokenLocked(st)
	previewMu.Unlock()
	previewBindPageCallbacks(s, token, page)
	oldNavigate, oldFinish := page.navigate, page.finish
	oldNavigate("http://localhost/next")
	oldFinish()
	oldNavigate("http://localhost/stale")
	previewMu.Lock()
	if st.generation != token.generation+1 || len(st.consoleErrors) != 0 || st.consolePolling {
		t.Errorf("旧事件被应用或导航未换代: %+v", st)
	}
	previewMu.Unlock()
	if page.offCount != 2 {
		t.Fatalf("导航后未解绑旧回调: %d", page.offCount)
	}
	currentNavigate, currentFinish := page.navigate, page.finish
	closePreview(s)
	currentNavigate("http://localhost/released")
	currentFinish()
	previewMu.Lock()
	defer previewMu.Unlock()
	if st.generation != token.generation+2 || st.page != nil || st.consolePolling || page.offCount != 4 {
		t.Fatal("释放后旧回调仍生效，或回调未全部解绑")
	}
}

func TestPreviewSnapshotConcurrentUpdates(t *testing.T) {
	st := &browserTab{
		annotations:   []previewAnnotation{{Selector: "#app"}},
		consoleErrors: []previewConsoleError{{Message: "初始"}},
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			previewMu.Lock()
			st.annotations[0].Comment = "更新"
			st.consoleErrors[0].Message = "更新"
			st.viewport = previewViewportMobile
			st.annotOpen = !st.annotOpen
			st.consoleOpen = !st.consoleOpen
			previewMu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		previewMu.Lock()
		snapshot := previewSnapshotLocked(st)
		previewMu.Unlock()
		_ = previewViewportWidth(snapshot.viewport)
		snapshot.annotations[0].Comment = "快照"
		snapshot.consoleErrors[0].Message = "快照"
	}
	wg.Wait()
}

func TestPreviewPollDropsStalePage(t *testing.T) {
	for _, kind := range []string{"console", "annotations"} {
		for _, change := range []string{"navigation", "replacement", "release", "error"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				s := &chatState{}
				st := getPreviewState(s)
				started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				page := &previewTestPage{eval: func(string) (any, error) {
					close(started)
					<-release
					if change == "error" {
						return nil, errors.New("读取失败")
					}
					if kind == "console" {
						return `[{"message":"旧页"}]`, nil
					}
					return `[{"selector":"#old"}]`, nil
				}}
				previewMu.Lock()
				st.page = page
				st.pageID = 1
				previewAdvanceGenerationLocked(st)
				token := previewPageTokenLocked(st)
				st.consolePolling, st.annotPolling = true, true
				st.annotating = true
				previewMu.Unlock()
				go func() {
					defer close(done)
					if kind == "console" {
						previewPollConsoleErrors(s, token, page)
					} else {
						previewPollAnnotations(s, token, page)
					}
				}()
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("轮询未启动")
				}
				previewMu.Lock()
				if change != "error" {
					previewAdvanceGenerationLocked(st)
					if change == "replacement" {
						st.pageID++
					}
					if change == "release" {
						st.page = nil
					}
					// 旧请求结束不能清掉新请求的进行中标记。
					st.consolePolling, st.annotPolling = true, true
				}
				previewMu.Unlock()
				close(release)
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("轮询未结束")
				}
				previewMu.Lock()
				defer previewMu.Unlock()
				if len(st.consoleErrors) != 0 || len(st.annotations) != 0 {
					t.Fatal("旧页结果被写回")
				}
				polling := st.consolePolling
				if kind == "annotations" {
					polling = st.annotPolling
				}
				if polling != (change != "error") {
					t.Fatalf("轮询标记错误: %v", polling)
				}
			})
		}
	}
}

func TestPreviewPollResults(t *testing.T) {
	for _, kind := range []string{"console", "annotations"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%v", kind, fail), func(t *testing.T) {
				s := &chatState{}
				st := getPreviewState(s)
				page := &previewTestPage{eval: func(string) (any, error) {
					// 原生调用期间可重新取得状态锁。
					previewMu.Lock()
					previewMu.Unlock()
					if fail {
						return nil, errors.New("读取失败")
					}
					if kind == "console" {
						return `[{"message":"新错误"}]`, nil
					}
					return `[{"selector":"#new"}]`, nil
				}}
				previewMu.Lock()
				st.page = page
				st.annotating = true
				st.consolePolling, st.annotPolling = true, true
				token := previewPageTokenLocked(st)
				previewMu.Unlock()
				if kind == "console" {
					previewPollConsoleErrors(s, token, page)
				} else {
					previewPollAnnotations(s, token, page)
				}
				previewMu.Lock()
				defer previewMu.Unlock()
				if kind == "console" {
					if st.consolePolling || (len(st.consoleErrors) > 0) == fail {
						t.Fatalf("错误采集结果或进行中标记错误: %+v", st)
					}
				} else if st.annotPolling || (len(st.annotations) > 0) == fail {
					t.Fatalf("标注采集结果或进行中标记错误: %+v", st)
				} else if !fail && (st.annotations[0].id == 0 || !st.annotOpen) {
					t.Fatal("新标注缺少稳定身份或未展开")
				}
			})
		}
	}
}

func TestPreviewConsoleClearDropsPendingPoll(t *testing.T) {
	s := &chatState{}
	st := getPreviewState(s)
	started, release, cleared, reread, done := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	page := &previewTestPage{eval: func(script string) (any, error) {
		if script == previewConsoleClearScript {
			close(cleared)
			return nil, errors.New("清空脚本失败")
		}
		select {
		case <-started:
			close(reread)
			return `[]`, nil
		default:
			close(started)
			<-release
			return `[{"message":"清空前的旧错误"}]`, nil
		}
	}}
	previewMu.Lock()
	st.page, st.consolePolling = page, true
	token := previewPageTokenLocked(st)
	previewMu.Unlock()
	go func() { previewPollConsoleErrors(s, token, page); close(done) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("旧轮询未启动")
	}
	clearPreviewConsoleErrors(s, st)
	for _, signal := range []chan struct{}{cleared, reread} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatal("清空或重读未结束")
		}
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("旧轮询未结束")
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		previewMu.Lock()
		polling, count := st.consolePolling, len(st.consoleErrors)
		previewMu.Unlock()
		if count != 0 {
			t.Fatal("清空后旧结果复活")
		}
		if !polling {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("失败路径未清理轮询标记")
		}
	}
}

func TestPreviewAdvanceGenerationClearsPageData(t *testing.T) {
	st := &browserTab{
		consoleErrors: []previewConsoleError{{Message: "旧错误"}}, consoleOpen: true, consolePolling: true,
		annotations: []previewAnnotation{{Selector: "#old"}}, annotating: true, annotOpen: true, annotPolling: true,
	}
	previewMu.Lock()
	previewAdvanceGenerationLocked(st)
	defer previewMu.Unlock()
	if st.generation != 1 || len(st.consoleErrors) != 0 || len(st.annotations) != 0 || st.consoleOpen || st.annotOpen || st.annotating || st.consolePolling || st.annotPolling {
		t.Fatalf("换代未清理旧页状态: %+v", st)
	}
}

func TestNormalizePreviewURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"   ", ""},
		{"http://example.com", "http://example.com"},
		{"https://example.com/foo", "https://example.com/foo"},
		{"localhost", "http://localhost"},
		{"localhost:3000", "http://localhost:3000"},
		{"localhost:8080/api", "http://localhost:8080/api"},
		{"127.0.0.1", "http://127.0.0.1"},
		{"127.0.0.1:5173", "http://127.0.0.1:5173"},
		{":3000", "http://localhost:3000"},
		{"0.0.0.0:8000", "http://0.0.0.0:8000"},
		{"192.168.1.1:8080", "http://192.168.1.1:8080"},
		{"example.com", "https://example.com"},
		{"example.com/path", "https://example.com/path"},
		{"  localhost:3000  ", "http://localhost:3000"},
		{"[::1]:3000", "http://[::1]:3000"},
		{"localhost.example.com", "https://localhost.example.com"},
		{"file:///etc/passwd", ""},
		{"javascript://alert(1)", ""},
		{"http://", ""},
		{"http://localhost:bad", ""},
		{"http://user:pass@localhost", ""},
	}

	for _, tc := range tests {
		actual := normalizePreviewURL(tc.input)
		if actual != tc.expected {
			t.Errorf("normalizePreviewURL(%q) = %q; want %q", tc.input, actual, tc.expected)
		}
	}
}

func TestPreviewState(t *testing.T) {
	s := &chatState{}
	st := getPreviewState(s)
	if st == nil {
		t.Fatal("expected non-nil previewState")
	}
	if width := inspectorSession(s).Width; width != inspectorDefaultWidth {
		t.Errorf("expected default width %v, got %v", inspectorDefaultWidth, width)
	}
	if st.viewport != previewViewportDesktop {
		t.Errorf("expected default viewport %q, got %q", previewViewportDesktop, st.viewport)
	}
	if st.visible {
		t.Errorf("expected initially not visible")
	}

	togglePreview(s)
	if !st.visible {
		t.Errorf("expected visible after togglePreview")
	}

	closePreview(s)
	if st.visible {
		t.Errorf("expected not visible after closePreview")
	}
}

func TestPreviewViewportWidth(t *testing.T) {
	cases := []struct {
		mode  previewViewportMode
		width float32
		label string
	}{
		{previewViewportDesktop, 0, "桌面"},
		{previewViewportTablet, PreviewTabletWidth, "平板"},
		{previewViewportMobile, PreviewMobileWidth, "手机"},
		{"", 0, "桌面"},
		{"unknown", 0, "桌面"},
	}
	for _, tc := range cases {
		if got := previewViewportWidth(tc.mode); got != tc.width {
			t.Errorf("previewViewportWidth(%q) = %v, want %v", tc.mode, got, tc.width)
		}
		if got := previewViewportLabel(tc.mode); got != tc.label {
			t.Errorf("previewViewportLabel(%q) = %q, want %q", tc.mode, got, tc.label)
		}
	}

	if PreviewTabletWidth != 820 || PreviewMobileWidth != 390 {
		t.Errorf("unexpected viewport widths: tablet=%v mobile=%v", PreviewTabletWidth, PreviewMobileWidth)
	}

	modes := previewViewportModes()
	if len(modes) != 3 || modes[0] != previewViewportDesktop || modes[1] != previewViewportTablet || modes[2] != previewViewportMobile {
		t.Errorf("unexpected viewport modes: %v", modes)
	}
}

func TestSetPreviewViewport(t *testing.T) {
	s := &chatState{}
	st := getPreviewState(s)

	setPreviewViewport(s, st, previewViewportMobile)
	if st.viewport != previewViewportMobile {
		t.Errorf("expected viewport %q, got %q", previewViewportMobile, st.viewport)
	}
	if previewViewportWidth(st.viewport) != PreviewMobileWidth {
		t.Errorf("mobile viewport should constrain the width to %v", PreviewMobileWidth)
	}

	setPreviewViewport(s, st, previewViewportDesktop)
	if st.viewport != previewViewportDesktop || previewViewportWidth(st.viewport) != 0 {
		t.Errorf("desktop viewport should remove the width constraint, got %q", st.viewport)
	}
}

func TestPreviewConsoleErrorHelpers(t *testing.T) {
	entry := previewConsoleError{Kind: "console", Message: "boom", Source: "http://localhost:3000/app.js", Line: 12}
	if line := previewConsoleEntryLine(entry); line != "console: boom (http://localhost:3000/app.js:12)" {
		t.Errorf("unexpected entry line: %q", line)
	}
	if line := previewConsoleEntryLine(previewConsoleError{Message: "no kind"}); line != "error: no kind" {
		t.Errorf("unexpected entry line without kind: %q", line)
	}

	base := []previewConsoleError{{Kind: "error", Message: "a"}}
	if !previewConsoleErrorsEqual(base, []previewConsoleError{{Kind: "error", Message: "a"}}) {
		t.Error("expected equal error lists")
	}
	if previewConsoleErrorsEqual(base, []previewConsoleError{{Kind: "error", Message: "b"}}) {
		t.Error("expected different error lists")
	}
	if previewConsoleErrorsEqual(base, nil) {
		t.Error("expected length mismatch to be different")
	}
}

// TestPreviewStartURLDefault 验证配置只作显式导航入口，不自动访问网站。
func TestPreviewStartURLDefault(t *testing.T) {
	s := &chatState{}
	st := getPreviewState(s)
	if st.addr != "" || previewStartURL(s) != config.DefaultBrowserStartURL {
		t.Fatal("空态自动恢复了地址，或默认配置丢失")
	}
}

// TestPreviewStartURLFromSettings 验证配置的起始地址优先于内置默认值。
func TestPreviewStartURLFromSettings(t *testing.T) {
	s := &chatState{}
	settings := NewSettingsState(settingsTestDir(t), nil, s)
	settings.cfg.Workbench.BrowserStartURL = "http://localhost:4321"
	setChatSettings(s, settings)

	st := getPreviewState(s)
	if st.addr != "" || previewStartURL(s) != "http://localhost:4321" {
		t.Fatal("空态自动恢复了地址，或自定义配置丢失")
	}
}

// TestPreviewToggleFillsEmptyAddress 验证重新打开面板仍保留地址空态。
func TestPreviewToggleFillsEmptyAddress(t *testing.T) {
	s := &chatState{}
	st := getPreviewState(s)
	st.addr = ""

	togglePreview(s)
	if st.addr != "" {
		t.Fatalf("打开面板后地址 = %q，预期空态", st.addr)
	}
}

// TestPreviewConsoleCaptureDisabled 验证关闭采集后不轮询、不清空既有错误。
func TestPreviewConsoleCaptureDisabled(t *testing.T) {
	s := &chatState{}
	st := getPreviewState(s)
	st.consoleErrors = []previewConsoleError{{Kind: "error", Message: "kept"}}

	disabled := false
	settings := NewSettingsState(settingsTestDir(t), nil, s)
	settings.cfg.Workbench.BrowserConsoleCapture = &disabled
	setChatSettings(s, settings)

	if previewConsoleCaptureEnabled(s) {
		t.Fatal("关闭后不应启用控制台采集")
	}

	previewMaybePollConsoleErrors(s, st, true)
	if st.consolePolling {
		t.Fatal("关闭采集后不应开始轮询")
	}
	if len(st.consoleErrors) != 1 || st.consoleErrors[0].Message != "kept" {
		t.Fatalf("关闭采集不应改动已有错误: %+v", st.consoleErrors)
	}

	clearPreviewConsoleErrors(s, st)
	if len(st.consoleErrors) != 1 {
		t.Fatalf("关闭采集时清空操作应直接返回: %+v", st.consoleErrors)
	}
}

func TestParseQuickPorts(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "默认空字符串返回预设端口",
			input:    "",
			expected: []string{"3000", "5173", "8080", "4321"},
		},
		{
			name:     "仅空白字符返回预设端口",
			input:    "   \t\n  ",
			expected: []string{"3000", "5173", "8080", "4321"},
		},
		{
			name:     "正常逗号分隔端口",
			input:    "3000, 5173, 8080",
			expected: []string{"3000", "5173", "8080"},
		},
		{
			name:     "包含换行与空格混合分隔",
			input:    "3000\n5173, 8080 4321",
			expected: []string{"3000", "5173", "8080", "4321"},
		},
		{
			name:     "重复端口去重",
			input:    "3000, 3000, 8080, 5173, 8080",
			expected: []string{"3000", "8080", "5173"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := ParseQuickPorts(tc.input)
			if len(actual) != len(tc.expected) {
				t.Fatalf("ParseQuickPorts(%q) length = %d, want %d: %v", tc.input, len(actual), len(tc.expected), actual)
			}
			for i, v := range actual {
				if v != tc.expected[i] {
					t.Errorf("ParseQuickPorts(%q)[%d] = %q, want %q", tc.input, i, v, tc.expected[i])
				}
			}
		})
	}
}

// 预览 WebView 创建失败后不再每帧重试：ensureWebView 在渲染路径每帧调用，
// 失败后置 webFailed 并停手，只有用户重新打开预览、改地址或点刷新时才重置标记再试一次。
func TestEnsureWebView_StopsRetryingAfterFailure(t *testing.T) {
	// 没有关联宿主窗口时创建必然失败
	s := &chatState{}
	st := getPreviewState(s)
	ensureWebView(s, st)
	if st.web != nil || !st.webFailed {
		t.Fatalf("创建失败后应记录 webFailed 且不保留视图，web=%v failed=%v", st.web, st.webFailed)
	}
	firstErr := st.err
	if firstErr == "" {
		t.Fatal("创建失败应写入错误原因")
	}

	// 失败标记存在时不再尝试创建，因此不会重新写入错误信息
	st.err = ""
	ensureWebView(s, st)
	if st.web != nil || st.err != "" {
		t.Fatalf("失败后不应再尝试创建视图，err=%q", st.err)
	}

	// 重置标记（等价于用户重新打开预览/点刷新）后允许再试一次
	st.webFailed = false
	ensureWebView(s, st)
	if st.err != firstErr {
		t.Fatalf("重置标记后应重新尝试创建并再次失败，err=%q", st.err)
	}
}
