package mcodeui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type inspectorPreviewCall struct {
	threadID string
	path     string
}

type inspectorPreviewTestClient struct {
	*FakeClient

	mu          sync.Mutex
	calls       []inspectorPreviewCall
	target      string
	err         error
	revoked     []string
	revokedURLs []string
}

func (c *inspectorPreviewTestClient) RevokeInspectorPreviewURL(target string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revokedURLs = append(c.revokedURLs, target)
}

func TestLocalPreviewCloseRevokesSingleToken(t *testing.T) {
	client := &inspectorPreviewTestClient{FakeClient: NewFakeClient()}
	s := newInspectorPreviewState(t, client)
	first := getPreviewState(s)
	openPreviewLocalURL(s, "t1", "http://127.0.0.1:4321/p/one/index.html")
	second := browserNewTab(s)
	openPreviewLocalURL(s, "t1", "http://127.0.0.1:4321/p/two/index.html")
	previewMu.Lock()
	first.url = "https://example.test/redirect"
	previewMu.Unlock()
	browserCloseTab(s, first.owner, first.tabID)
	client.mu.Lock()
	if len(client.revokedURLs) != 1 || client.revokedURLs[0] != "http://127.0.0.1:4321/p/one/index.html" || len(client.revoked) != 0 {
		t.Error("关闭单个标签未撤销自己的 Token，或撤销了整个会话")
	}
	client.mu.Unlock()
	if second.localPreviewURL != "http://127.0.0.1:4321/p/two/index.html" {
		t.Fatal("兄弟标签被改变")
	}
}

func (c *inspectorPreviewTestClient) InspectorPreviewURL(_ context.Context, threadID, relPath string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, inspectorPreviewCall{threadID: threadID, path: relPath})
	return c.target, c.err
}

func (c *inspectorPreviewTestClient) RevokeInspectorPreviews(threadID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked = append(c.revoked, threadID)
}

func (c *inspectorPreviewTestClient) callsSnapshot() []inspectorPreviewCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]inspectorPreviewCall(nil), c.calls...)
}

func (c *inspectorPreviewTestClient) revokedSnapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.revoked...)
}

func newInspectorPreviewState(t *testing.T, client Client) *chatState {
	t.Helper()
	s := newChatState(client)
	s.mu.Lock()
	s.projects = []Project{{ID: "p1", Name: "preview-test", Path: "/tmp/mcode-preview-test"}}
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "测试会话"}}
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	s.prepareFileManagerTree("t1")
	t.Cleanup(s.close)
	return s
}

func waitInspectorPreviewTab(t *testing.T, s *chatState, path string) fileManagerTabSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		st := getFileManagerState(s)
		if st != nil {
			fileManagerMu.Lock()
			snap := fileManagerSnapshotLocked(st)
			fileManagerMu.Unlock()
			if tab, ok := snap.tabByPath(path); ok && !tab.fileLoading && (tab.kind != "" || tab.fileErr != "") {
				return tab
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("文件标签 %s 未读完", path)
	return fileManagerTabSnapshot{}
}

func TestFilePreviewRoutesPDFAndHTML(t *testing.T) {
	client := &inspectorPreviewTestClient{
		FakeClient: NewFakeClient(),
		target:     "http://127.0.0.1:4321/p/token/doc.pdf",
	}
	client.SetFileContents(map[string]FileContent{
		"index.html": {Path: "index.html", Kind: "text", Text: "<html><body>hi</body></html>", TotalSize: 30},
	})
	s := newInspectorPreviewState(t, client)

	// PDF 点击直接进浏览器目的，不创建内容标签（客户端不支持时才退回二进制提示）。
	s.openFileManagerFile("t1", "doc.pdf", false)
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	tabs := len(st.tabs)
	fileManagerMu.Unlock()
	if tabs != 0 {
		t.Fatalf("PDF 创建了 %d 个内容标签", tabs)
	}
	if active := activeSidePane(s); active == nil || active.id != sidePanePreviewID {
		t.Fatal("PDF 未切到浏览器目的")
	}
	calls := client.callsSnapshot()
	if len(calls) != 1 || calls[0].threadID != "t1" || calls[0].path != "doc.pdf" {
		t.Fatalf("PDF 预览调用=%+v", calls)
	}
	previewState := getPreviewState(s)
	previewMu.Lock()
	target := previewState.url
	previewMu.Unlock()
	if target != client.target {
		t.Fatalf("PDF 导航地址=%q", target)
	}

	// HTML 默认仍显示源码，头部按钮再显式送到浏览器。
	inspectorSelect(s, sidePaneFileID, false)
	s.openFileManagerFile("t1", "index.html", false)
	tab := waitInspectorPreviewTab(t, s, "index.html")
	if tab.kind != "text" || tab.content == "" {
		t.Fatalf("HTML 默认未显示源码: %+v", tab)
	}
	if active := activeSidePane(s); active == nil || active.id != sidePaneFileID {
		t.Fatal("打开 HTML 后离开了文件目的")
	}

	tester := ui.NewTester(func(c *ui.Context) { renderFileManagerPanel(c, s) }, 900, 600)
	tester.Frame()
	tester.Advance(DurSheet + 50*time.Millisecond)
	if err := tester.Click("在浏览器打开"); err != nil {
		t.Fatal(err)
	}
	calls = client.callsSnapshot()
	if len(calls) != 2 || calls[1].path != "index.html" {
		t.Fatalf("HTML 预览调用=%+v", calls)
	}
	if active := activeSidePane(s); active == nil || active.id != sidePanePreviewID {
		t.Fatal("HTML 未切到浏览器目的")
	}
	previewMu.Lock()
	target = browserActiveTabLocked(previewState.owner).url
	previewMu.Unlock()
	if target != client.target {
		t.Fatalf("HTML 导航地址=%q", target)
	}

	// 客户端不支持可选接口时退回现有行为：PDF 显示二进制提示，HTML 仍是源码且没有浏览器按钮。
	plain := NewFakeClient()
	plain.SetFileContents(map[string]FileContent{
		"plain.pdf":  {Path: "plain.pdf", Kind: "binary", TotalSize: 10},
		"plain.html": {Path: "plain.html", Kind: "text", Text: "<html>plain</html>", TotalSize: 18},
	})
	other := newInspectorPreviewState(t, plain)
	other.openFileManagerFile("t1", "plain.pdf", false)
	pdfTab := waitInspectorPreviewTab(t, other, "plain.pdf")
	if pdfTab.kind != "binary" || pdfTab.fileErr != "" {
		t.Fatalf("不支持的客户端 PDF 回退=%+v", pdfTab)
	}
	other.openFileManagerFile("t1", "plain.html", false)
	waitInspectorPreviewTab(t, other, "plain.html")
	tester = ui.NewTester(func(c *ui.Context) { renderFileManagerPanel(c, other) }, 900, 600)
	tester.Frame()
	if _, ok := tester.Find("在浏览器打开"); ok {
		t.Fatal("不支持的客户端仍显示浏览器按钮")
	}
}

func TestFilePreviewSVGFallback(t *testing.T) {
	valid := `<svg xmlns="http://www.w3.org/2000/svg" width="12" height="8"><rect width="12" height="8" fill="red"/></svg>`
	external := []string{
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="https://example.com/a.png"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="http://example.com/a.png"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><use xlink:href="file:///etc/passwd"/></svg>`,
		`<svg xmlns="http://www.w3.org/2000/svg"><image href="//example.com/a.png"/></svg>`,
	}
	if svg := fileManagerSVGForContent("icon.svg", valid, int64(len(valid)), false); svg == nil {
		t.Fatal("合法 SVG 未生成预览")
	}
	for _, content := range external {
		if svg := fileManagerSVGForContent("icon.svg", content, int64(len(content)), false); svg != nil {
			t.Fatalf("外链 SVG 仍生成预览: %s", content)
		}
	}
	broken := `<svg xmlns="http://www.w3.org/2000/svg"><rect`
	if svg := fileManagerSVGForContent("broken.svg", broken, int64(len(broken)), false); svg != nil {
		t.Fatal("解析失败的 SVG 仍生成预览")
	}
	if svg := fileManagerSVGForContent("big.svg", valid, fileManagerSVGMaxBytes+1, false); svg != nil {
		t.Fatal("超限 SVG 仍生成预览")
	}
	if svg := fileManagerSVGForContent("partial.svg", valid, int64(len(valid)), true); svg != nil {
		t.Fatal("未读完的 SVG 仍生成预览")
	}

	// 回退时只显示源码：不渲染 SVG 切换，也不出现图片预览。
	client := &inspectorPreviewTestClient{FakeClient: NewFakeClient()}
	client.SetFileContents(map[string]FileContent{
		"broken.svg": {Path: "broken.svg", Kind: "text", Text: broken, TotalSize: int64(len(broken))},
	})
	s := newInspectorPreviewState(t, client)
	s.openFileManagerFile("t1", "broken.svg", false)
	tab := waitInspectorPreviewTab(t, s, "broken.svg")
	if tab.svg != nil || tab.svgPreview {
		t.Fatalf("失败 SVG 状态: svg=%v preview=%v", tab.svg, tab.svgPreview)
	}
	tester := ui.NewTester(func(c *ui.Context) { renderFileManagerPanel(c, s) }, 900, 600)
	tester.Frame()
	if _, ok := tester.Find("源码"); ok {
		t.Fatal("失败 SVG 仍显示预览/源码切换")
	}

	// 合法 SVG 默认显示预览，可切回源码。
	client.SetFileContents(map[string]FileContent{
		"icon.svg": {Path: "icon.svg", Kind: "text", Text: valid, TotalSize: int64(len(valid))},
	})
	s.openFileManagerFile("t1", "icon.svg", false)
	tab = waitInspectorPreviewTab(t, s, "icon.svg")
	if tab.svg == nil || !tab.svgPreview {
		t.Fatal("合法 SVG 未默认预览")
	}
	tester = ui.NewTester(func(c *ui.Context) { renderFileManagerPanel(c, s) }, 900, 600)
	tester.Frame()
	if err := tester.Click("源码"); err != nil {
		t.Fatal(err)
	}
	st := getFileManagerState(s)
	fileManagerMu.Lock()
	snap := fileManagerSnapshotLocked(st)
	fileManagerMu.Unlock()
	if tab, ok := snap.tabByPath("icon.svg"); !ok || tab.svgPreview {
		t.Fatal("切换到源码后仍处于 SVG 预览")
	}
}

func TestLocalPreviewUntrustedNoBindings(t *testing.T) {
	oldFactory := newPreviewWebView
	t.Cleanup(func() { newPreviewWebView = oldFactory })
	var created []mygo.WebViewOptions
	newPreviewWebView = func(_ *mygo.Window, opts mygo.WebViewOptions) (*mygo.WebView, error) {
		created = append(created, opts)
		return nil, errors.New("测试不创建原生 WebView")
	}

	client := &inspectorPreviewTestClient{FakeClient: NewFakeClient(), target: "http://127.0.0.1:4321/p/token/index.html"}
	s := newInspectorPreviewState(t, client)
	s.window = func() *mygo.Window { return &mygo.Window{} }
	trusted := inspectorAttachPage(s)

	openPreviewLocalURL(s, "t1", client.target)
	if trusted.destroyed.Load() != 1 {
		t.Fatal("切换本地内容前未释放旧的普通页面")
	}
	if len(created) != 1 {
		t.Fatalf("创建 WebView 次数=%d", len(created))
	}
	opts := created[0]
	if !opts.Page.Untrusted {
		t.Fatal("本地内容未以 Untrusted 创建")
	}
	if len(opts.Page.TrustedOrigins) != 0 || opts.Page.PreloadScript == "" || opts.Page.PreloadAllFrames {
		t.Fatal("本地内容扩大了权限，或缺少预装脚本")
	}
	// 创建入口不注册任何绑定：此路径只传 PageOptions，Untrusted 页面也无法调用 Go 方法。

	// 普通网页仍按原来的可信选项创建，不继承本地内容的限制。
	openPreviewURL(s, "http://localhost:3000")
	if len(created) != 2 {
		t.Fatalf("创建 WebView 次数=%d", len(created))
	}
	if created[1].Page.Untrusted {
		t.Fatal("普通网页被标记为 Untrusted")
	}

	// 会话释放统一撤销该会话的本地内容 Token。
	inspectorDisposeSession(s, inspectorSession(s))
	if revoked := client.revokedSnapshot(); len(revoked) != 1 || revoked[0] != "t1" {
		t.Fatalf("释放会话后的撤销记录=%v", revoked)
	}
}
