package mcodeui

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// native 方法先取得状态锁，确保调用方没有持锁进入原生层。
type browserTestPage struct {
	inspectorTestPage
	navigate, oldNavigate                                               func(string)
	finish                                                              func()
	title                                                               func(*mygo.TitleEvent)
	entries                                                             []string
	index                                                               int
	backCalls, forwardCalls, reloadCalls, stopCalls, urlReads, offCount int
	loading                                                             bool
}

func (p *browserTestPage) URL() string {
	previewMu.Lock()
	previewMu.Unlock()
	p.urlReads++
	if len(p.entries) == 0 {
		return ""
	}
	return p.entries[p.index]
}
func (p *browserTestPage) CanGoBack() bool              { return p.index > 0 }
func (p *browserTestPage) CanGoForward() bool           { return p.index+1 < len(p.entries) }
func (p *browserTestPage) GoBack()                      { p.backCalls++; p.index--; p.navigate(p.entries[p.index]) }
func (p *browserTestPage) GoForward()                   { p.forwardCalls++; p.index++; p.navigate(p.entries[p.index]) }
func (p *browserTestPage) Reload()                      { p.reloadCalls++ }
func (p *browserTestPage) Stop()                        { p.stopCalls++ }
func (p *browserTestPage) IsLoading() bool              { return p.loading }
func (p *browserTestPage) CapturePage() ([]byte, error) { return []byte("invalid"), nil }
func (p *browserTestPage) LoadURL(target string) error {
	p.loaded.Add(1)
	p.entries = append(p.entries[:p.index+1], target)
	p.index = len(p.entries) - 1
	p.navigate(target)
	return nil
}
func (p *browserTestPage) OnDidNavigate(fn func(string)) func() {
	p.oldNavigate, p.navigate = p.navigate, fn
	return func() { p.offCount++ }
}
func (p *browserTestPage) OnDidFinishLoad(fn func()) func() {
	p.finish = fn
	return func() { p.offCount++ }
}
func (p *browserTestPage) OnPageTitleUpdated(fn func(*mygo.TitleEvent)) func() {
	p.title = fn
	return func() { p.offCount++ }
}

func browserAttachTestPage(s *chatState, target string) (*browserTab, *browserTestPage) {
	tab := getPreviewState(s)
	page := &browserTestPage{entries: []string{target}}
	previewMu.Lock()
	browserPromoteTabLocked(tab)
	previewNextPageID++
	tab.page, tab.pageID = page, previewNextPageID
	tab.url, tab.addr = target, target
	token := previewPageTokenLocked(tab)
	previewMu.Unlock()
	previewBindPageCallbacks(s, token, page)
	return tab, page
}

func TestBrowserTabsStableIdentityAndClose(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	first, pageA := browserAttachTestPage(s, "https://example.test/a")
	browserNewTab(s)
	second, pageB := browserAttachTestPage(s, "https://example.test/a")
	if first.tabID == 0 || second.tabID <= first.tabID {
		t.Fatal("标签 ID 不稳定或未递增")
	}
	pageA.title(&mygo.TitleEvent{Title: "重复标题"})
	pageB.title(&mygo.TitleEvent{Title: "重复标题"})
	id := second.tabID
	pageB.navigate("https://example.test/redirect")
	if second.tabID != id || second.title != "重复标题" || first.tabID == id {
		t.Fatal("重定向或重复标题改变标签身份")
	}
	stale, title := pageB.navigate, pageB.title
	browserCloseTab(s, second.owner, id)
	stale("https://example.test/stale")
	title(&mygo.TitleEvent{Title: "过期"})
	if pageB.destroyed.Load() != 1 || pageA.destroyed.Load() != 0 || second.title == "过期" {
		t.Fatal("关闭释放错误页面或旧回调写入状态")
	}
	if pageB.offCount != 6 {
		t.Fatalf("未解绑所有回调：%d", pageB.offCount)
	}
	third := browserNewTab(s)
	if third.tabID <= id {
		t.Fatal("关闭后复用了 ID")
	}
}

func TestBrowserNavigationSyncPreservesEditingAddress(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	tab, page := browserAttachTestPage(s, "https://example.test/start")
	inspectorSelect(s, sidePanePreviewID, false)
	tab.addr, tab.addressEditing = "localhost:4000/正在输入", true
	page.navigate("https://example.test/redirect")
	if tab.url != "https://example.test/redirect" || tab.addr != "localhost:4000/正在输入" {
		t.Fatal("导航覆盖正在编辑的地址")
	}
	tab.addressEditing = false
	page.navigate("https://example.test/next")
	if tab.addr != tab.url {
		t.Fatal("已提交地址没有同步")
	}
	page.entries[0] = "https://example.test/#spa"
	tab.annotations = []previewAnnotation{{Selector: "#old"}}
	token := previewPageTokenLocked(tab)
	browserMaybeSyncNavigation(s, tab)
	if tab.addr != page.entries[0] || tab.generation != token.generation+1 || len(tab.annotations) != 0 {
		t.Fatal("URL 兜底未同步或没有清理旧页标注")
	}
	browserMaybeSyncNavigation(s, tab)
	if page.urlReads != 1 {
		t.Fatal("URL 兜底没有限频")
	}
	hidePreviewState(s, tab.owner)
	tab.navigationLastPoll = time.Time{}
	browserMaybeSyncNavigation(s, tab)
	if page.urlReads != 1 {
		t.Fatal("隐藏页仍读取 URL")
	}
	inspectorSelect(s, sidePanePreviewID, false)
	page.loading = true
	page.entries[0] = ""
	tab.navigationLastPoll = time.Time{}
	browserMaybeSyncNavigation(s, tab)
	if tab.url == "" {
		t.Fatal("尚未提交的空 URL 覆盖了当前地址")
	}
}

func TestBrowserBackForwardUsesPageHistory(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	tab, page := browserAttachTestPage(s, "https://example.test/a")
	page.entries = []string{"https://example.test/a", "https://example.test/b"}
	page.index = 1
	page.navigate(page.entries[1])
	browserNavigateHistory(s, tab, false)
	if tab.url != page.entries[0] || page.backCalls != 1 || !tab.canGoForward {
		t.Fatal("后退未使用页面历史或未同步地址")
	}
	browserNavigateHistory(s, tab, false)
	if page.backCalls != 1 {
		t.Fatal("不可后退时仍调用原生方法")
	}
	browserNavigateHistory(s, tab, true)
	if tab.addr != page.entries[1] || page.forwardCalls != 1 || !tab.canGoBack {
		t.Fatal("前进未使用页面历史")
	}
	tab.loading = false
	browserReloadOrStop(s, tab)
	tab.loading = true
	browserReloadOrStop(s, tab)
	if page.reloadCalls != 1 || page.stopCalls != 1 || page.loaded.Load() != 0 {
		t.Fatal("刷新/停止退化成重新 LoadURL")
	}
}

func TestBrowserHiddenTabRetainsPage(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	first, pageA := browserAttachTestPage(s, "https://example.test/a")
	inspectorSelect(s, sidePanePreviewID, false)
	first.annotating = true
	first.annotations = []previewAnnotation{{Selector: "#keep"}}
	browserNewTab(s)
	second, pageB := browserAttachTestPage(s, "https://example.test/b")
	for range 3 {
		browserActivateTab(s, first.owner, first.tabID)
		ensureWebView(s, getPreviewState(s))
		browserActivateTab(s, first.owner, second.tabID)
		ensureWebView(s, getPreviewState(s))
	}
	if first.visible || !second.visible || first.annotating || len(first.annotations) != 1 {
		t.Fatal("隐藏页仍活跃或丢失标注")
	}
	if pageA.destroyed.Load()+pageB.destroyed.Load() != 0 || pageA.loaded.Load()+pageB.loaded.Load() != 0 {
		t.Fatal("切换标签销毁或重载页面")
	}
	if pageA.evaluated.Load() != 1 {
		t.Fatal("隐藏时没有关闭拾取")
	}
	previewMaybePollAnnotations(s, first, true)
	if pageA.evaluated.Load() != 1 {
		t.Fatal("隐藏标签继续轮询")
	}
}

func TestBrowserSessionEvictionReleasesViews(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	var tabs []*browserTab
	var pages []*browserTestPage
	for i := 0; i < browserInactivePageLimit+3; i++ {
		s.mu.Lock()
		old := s.selectedThreadID
		s.selectedThreadID = fmt.Sprintf("thread-%d", i)
		s.mu.Unlock()
		inspectorSwitchThread(s, old)
		tab, page := browserAttachTestPage(s, fmt.Sprintf("https://example.test/%d", i))
		tab.title = fmt.Sprintf("页面 %d", i)
		// 淘汰按 lastUsed 排队：Windows 时钟粒度粗，相邻标签可能拿到同一时间戳，
		// 排序就会退化成 map 迭代顺序；这里给出严格递增的时间，锁定“最旧先淘汰”。
		previewMu.Lock()
		tab.owner.lastUsed = time.Unix(0, int64(i+1))
		previewMu.Unlock()
		tabs, pages = append(tabs, tab), append(pages, page)
	}
	browserEnforceSessionLimit(s)
	for i, page := range pages {
		expected := int32(0)
		if i < 2 {
			expected = 1
		}
		if page.destroyed.Load() != expected {
			t.Fatalf("页面 %d 释放次数=%d", i, page.destroyed.Load())
		}
		if tabs[i].url == "" || tabs[i].title == "" {
			t.Fatal("淘汰丢失标签元数据")
		}
		if expected == 1 && (tabs[i].page != nil || page.offCount != 6) {
			t.Fatal("淘汰未释放实例或解绑回调")
		}
	}
	current := tabs[len(tabs)-1]
	inspectorSelect(s, sidePaneFileID, false)
	browserEnforceSessionLimit(s)
	if current.page == nil || pages[len(pages)-1].destroyed.Load() != 0 {
		t.Fatal("当前会话切目的被淘汰")
	}
}

func TestBrowserCloseLastTabShowsEmptyAddress(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	tab, page := browserAttachTestPage(s, "https://example.test/a")
	inspectorSelect(s, sidePanePreviewID, false)
	browserCloseTab(s, tab.owner, tab.tabID)
	empty := getPreviewState(s)
	if !inspectorPaneOpen(s, sidePanePreviewID) || empty.url != "" || empty.addr != "" || empty.page != nil || len(empty.owner.tabs) != 0 {
		t.Fatal("最后标签关闭后未保留空地址右栏")
	}
	if page.destroyed.Load() != 1 || page.offCount != 3 {
		t.Fatal("最后标签未完整释放")
	}
	tester := ui.NewTester(func(c *ui.Context) { renderPreviewPanel(c, s) }, 1000, 600)
	tester.Frame()
	tester.Advance(DurSheet + 50*time.Millisecond)
	if empty.webFailed || empty.err != "" {
		t.Fatal("空态仍尝试创建原生页面")
	}
	if err := tester.Click("新建标签"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if len(empty.owner.tabs) != 1 {
		t.Fatal("新建按钮未创建标签")
	}
}

func TestBrowserCmdWClosesWebTab(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	tab, page := browserAttachTestPage(s, "https://example.test/a")
	inspectorSelect(s, sidePanePreviewID, false)
	win := &mygo.Window{}
	chatWindowsMu.Lock()
	chatWindows[win] = s
	chatWindowsMu.Unlock()
	defer func() { chatWindowsMu.Lock(); delete(chatWindows, win); chatWindowsMu.Unlock() }()
	closeMenuItem().Click(nil, win)
	if page.destroyed.Load() != 1 || len(tab.owner.tabs) != 0 || !tab.owner.visible {
		t.Fatal("⌘W 没有关闭网页标签")
	}
	if !browserCloseFromMenu(s) {
		t.Fatal("空浏览器的 ⌘W 不应关闭窗口")
	}
	inspectorSelect(s, sidePaneFileID, false)
	if browserCloseFromMenu(s) {
		t.Fatal("文件目的仍拦截 ⌘W")
	}
}

func TestBrowserTabsUISelectsWithoutReload(t *testing.T) {
	s := &chatState{}
	defer inspectorDisposeAllSessions(s)
	first, pageA := browserAttachTestPage(s, "https://example.test/a")
	inspectorSelect(s, sidePanePreviewID, false)
	browserNewTab(s)
	second, pageB := browserAttachTestPage(s, "https://example.test/b")
	tester := ui.NewTester(func(c *ui.Context) { renderPreviewPanel(c, s) }, 480, 600)
	tester.Frame()
	tester.Advance(DurSheet + 50*time.Millisecond)
	if rect, ok := tester.Find("地址"); !ok || rect.W < 150 {
		t.Fatal("导航栏没有留出可用的地址输入空间")
	}
	if err := tester.Click("https://example.test/a"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if getPreviewState(s) != first || second.visible {
		t.Fatal("网页标签点击没有选择目标页")
	}
	if err := tester.Click("https://example.test/b"); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if getPreviewState(s) != second || first.visible {
		t.Fatal("网页标签返回没有选择原来的页面")
	}
	if pageA.loaded.Load()+pageB.loaded.Load()+pageA.destroyed.Load()+pageB.destroyed.Load() != 0 {
		t.Fatal("标签 UI 切换重载或销毁页面")
	}
}

func TestBrowserWindowDisposesCallbacks(t *testing.T) {
	s := sidePaneTestState(t)
	_, pageA := browserAttachTestPage(s, "https://example.test/a")
	browserNewTab(s)
	_, pageB := browserAttachTestPage(s, "https://example.test/b")
	s.selectThread("t2")
	_, pageC := browserAttachTestPage(s, "https://example.test/c")
	s.close()
	s.close()
	for i, page := range []*browserTestPage{pageA, pageB, pageC} {
		expectedOff := 6
		if i == 2 {
			expectedOff = 3
		}
		if page.destroyed.Load() != 1 || page.offCount != expectedOff {
			t.Fatalf("页面 %d 释放或回调解绑次数错误：destroy=%d off=%d", i, page.destroyed.Load(), page.offCount)
		}
		page.navigate("https://example.test/stale")
		page.finish()
		page.title(&mygo.TitleEvent{Title: "过期"})
	}
}

func TestBrowserLocalFilesReuseTabsAndRevoke(t *testing.T) {
	client := &inspectorPreviewTestClient{FakeClient: NewFakeClient(), target: "http://127.0.0.1:4321/p/token/a.pdf"}
	s := newInspectorPreviewState(t, client)
	if !s.openInspectorPreview("t1", "a.pdf") {
		t.Fatal("本地预览失败")
	}
	waitInspectorPreview(t, s, "t1", "a.pdf")
	first := getPreviewState(s)
	if !first.untrusted {
		t.Fatal("本地页没有 Untrusted")
	}
	pageA := inspectorAttachPage(s)
	if !s.openInspectorPreview("t1", "b.html") {
		t.Fatal("第二本地预览失败")
	}
	waitInspectorPreview(t, s, "t1", "b.html")
	second := getPreviewState(s)
	pageB := inspectorAttachPage(s)
	if first == second || len(first.owner.tabs) != 2 {
		t.Fatal("本地文件没有独立标签")
	}
	if !s.openInspectorPreview("t1", "a.pdf") {
		t.Fatal("重复打开本地预览失败")
	}
	waitInspectorPreview(t, s, "t1", "a.pdf")
	if getPreviewState(s) != first || len(client.callsSnapshot()) != 2 {
		t.Fatal("重复打开同一文件重新签发或新建标签")
	}
	browserCloseTab(s, first.owner, first.tabID)
	if pageA.destroyed.Load() != 1 || pageB.destroyed.Load() != 0 || len(client.revokedSnapshot()) != 0 {
		t.Fatal("关闭一个本地标签破坏兄弟页")
	}
	browserCloseTab(s, second.owner, second.tabID)
	if pageB.destroyed.Load() != 1 || len(client.revokedSnapshot()) != 1 {
		t.Fatal("最后一个本地标签未撤销会话 token")
	}
}

// 该 fixture 供真实 WebView 验证 SPA 事件；headless 测试仅核对页面可访问，不能据此宣称通知覆盖。
func TestBrowserSPANavigationFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<title>SPA</title><button id="push" onclick="history.pushState({},'', '/push')">push</button><button id="replace" onclick="history.replaceState({},'', '/replace')">replace</button><a href="#hash">hash</a>`)
	}))
	defer server.Close()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
	t.Log("未驱动真实 WebView，pushState/replaceState/hashchange 的 OnDidNavigate 覆盖未核实")
}
