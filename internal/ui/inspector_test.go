package mcodeui

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 计数页与 nativePreviewPage 使用相同的页面适配接口。
type inspectorTestPage struct {
	destroyed atomic.Int32
	loaded    atomic.Int32
	evaluated atomic.Int32
}

func (p *inspectorTestPage) Eval(string) (any, error) { p.evaluated.Add(1); return "[]", nil }
func (p *inspectorTestPage) Destroy() {
	// 释放期间必须能取得状态锁。
	previewMu.Lock()
	previewMu.Unlock()
	p.destroyed.Add(1)
}
func (p *inspectorTestPage) LoadURL(string) error    { p.loaded.Add(1); return nil }
func (p *inspectorTestPage) LoadHTML(string, string) { p.loaded.Add(1) }

func inspectorAttachPage(s *chatState) *inspectorTestPage {
	st := getPreviewState(s)
	p := &inspectorTestPage{}
	previewMu.Lock()
	previewNextPageID++
	st.pageID = previewNextPageID
	st.page = p
	previewMu.Unlock()
	return p
}

func TestInspectorTabSwitchPreservesState(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	slot := inspectorSession(s)
	files, review, browser := slot.Files, slot.Review, slot.Browser
	files.dir, files.active = "src", "src/main.go"
	files.tabs = []*fileManagerTab{{path: "src/main.go", content: "package main"}}
	review.turnID = "turn-2"
	review.collapsed["src/main.go"] = true
	page := inspectorAttachPage(s)
	openPreviewURL(s, "http://localhost:3000")
	for _, id := range []string{sidePaneFileID, sidePanePreviewID, sidePaneDiffID, sidePanePreviewID} {
		inspectorSelect(s, id, false)
		if activeSidePane(s).id != id {
			t.Fatalf("未选择目的 %s", id)
		}
	}
	if slot.Files != files || slot.Review != review || slot.Browser != browser || browser.page != page {
		t.Fatal("目的切换替换了子状态或页面")
	}
	if files.dir != "src" || len(files.tabs) != 1 || review.turnID != "turn-2" || !review.collapsed["src/main.go"] {
		t.Fatal("目的切换丢失目录、文件标签或 diff 选择")
	}
	if page.loaded.Load() != 1 || page.destroyed.Load() != 0 {
		t.Fatal("切换目的重载或销毁了页面")
	}
}

func TestInspectorThreadIsolation(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	inspectorSelect(s, sidePanePreviewID, false)
	a := inspectorSession(s)
	a.Width = 600
	a.Files.dir = "src"
	a.Files.tabs = []*fileManagerTab{{path: "src/a.go"}}
	a.Review.turnID = "turn-a"
	pageA := inspectorAttachPage(s)
	s.selectThread("t2")
	b := inspectorSession(s)
	if a == b || a.Files == b.Files || a.Review == b.Review || a.Browser == b.Browser || b.Open {
		t.Fatal("不同会话共享了状态")
	}
	pageB := inspectorAttachPage(s)
	inspectorSelect(s, sidePaneFileID, false)
	b.Width = 400
	s.selectThread("t1")
	if inspectorSession(s) != a || getPreviewState(s).page != pageA || !a.Open || a.Active != sidePanePreviewID || a.Width != 600 || a.Files.dir != "src" || a.Review.turnID != "turn-a" {
		t.Fatal("返回会话未恢复原来的状态")
	}
	s.selectThread("")
	temporary := inspectorSession(s)
	if temporary == a || temporary == b || s.inspector.Sessions[""] != nil {
		t.Fatal("空会话未使用独立临时槽")
	}
	other := sidePaneTestState(t)
	defer other.close()
	if inspectorSession(other) == a {
		t.Fatal("不同窗口共享了会话槽")
	}
	if pageA.destroyed.Load() != 0 || pageB.destroyed.Load() != 0 {
		t.Fatal("切换会话销毁了页面")
	}
}

func TestInspectorHideKeepsWebView(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	page := inspectorAttachPage(s)
	openPreviewURL(s, "http://localhost:3000")
	st := getPreviewState(s)
	previewMu.Lock()
	st.annotating = true
	st.annotations = []previewAnnotation{{Selector: "#app"}}
	st.consoleErrors = []previewConsoleError{{Message: "保留"}}
	previewMu.Unlock()
	inspectorClose(s)
	if page.evaluated.Load() != 1 {
		t.Fatal("隐藏时未停用页面拾取")
	}
	previewMaybePollConsoleErrors(s, st, true)
	previewMaybePollAnnotations(s, st, true)
	if page.evaluated.Load() != 1 {
		t.Fatal("隐藏后仍启动轮询")
	}
	togglePreview(s)
	if ensureWebView(s, st) {
		t.Fatal("重新打开时重建了页面")
	}
	previewMu.Lock()
	same := st.page == page && len(st.annotations) == 1 && len(st.consoleErrors) == 1 && !st.annotating
	previewMu.Unlock()
	if !same || page.destroyed.Load() != 0 || page.loaded.Load() != 1 {
		t.Fatal("收起后页面、标注或控制台记录丢失，或页面被重载")
	}
}

func TestInspectorWindowDisposeAllSessions(t *testing.T) {
	s := sidePaneTestState(t)
	pageA := inspectorAttachPage(s)
	s.selectThread("t2")
	pageB := inspectorAttachPage(s)
	s.selectThread("")
	pageTemporary := inspectorAttachPage(s)
	s.close()
	s.close()
	for i, page := range []*inspectorTestPage{pageA, pageB, pageTemporary} {
		if got := page.destroyed.Load(); got != 1 {
			t.Fatalf("页面 %d 释放次数=%d", i, got)
		}
	}
	if len(s.inspector.Sessions) != 0 || s.inspector.Temporary != nil {
		t.Fatal("关窗后槽未清空")
	}
}

func TestInspectorNarrowLayout(t *testing.T) {
	for _, tc := range []struct {
		available, preferred, width float32
		overlay                     bool
	}{
		{600, 480, 480, true}, {300, 480, 300, true}, {687, 800, 320, false}, {1200, 800, 800, false},
	} {
		overlay, width := inspectorLayout(tc.available, tc.preferred)
		if overlay != tc.overlay || width != tc.width {
			t.Fatalf("布局 %+v 得到 overlay=%v width=%v", tc, overlay, width)
		}
	}
	s := sidePaneTestState(t)
	defer s.close()
	stubSidePanePanelRender(t, sidePaneFileID)
	inspectorSelect(s, sidePaneFileID, false)
	st := inspectorSession(s)
	st.Width = 620
	var chatWidth float32
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() {
			ui.Box(c).Width(160).FillHeight().Label("项目侧栏")
			ui.Box(c).Grow(1).Children(func() {
				renderSidePaneSplit(c, s, func() { chatWidth = ui.Box(c).Fill().Label("聊天").Bounds().W })
			})
		})
	}, 760, 600)
	tester.Frame()
	tester.Advance(time.Second)
	if _, ok := tester.Find("关闭右栏"); !ok {
		t.Fatal("窄窗口无法打开右栏")
	}
	if _, ok := tester.Find(sidePaneHandleLabel); ok {
		t.Fatal("窄窗口仍挤占聊天宽度")
	}
	if chatWidth < 599 || st.Width != 620 {
		t.Fatalf("覆盖布局改写了聊天或偏好宽度: chat=%v pref=%v", chatWidth, st.Width)
	}
	panel, ok := tester.Find("关闭右栏")
	if !ok || panel.X < 160 {
		t.Fatal("覆盖卡片侵入项目侧栏")
	}
}

func TestInspectorWorkspaceInvalidatesCaches(t *testing.T) {
	s := sidePaneTestState(t)
	defer s.close()
	inspectorValidateWorkspace(s)
	st := inspectorSession(s)
	files, review := st.Files, st.Review
	files.tabs = []*fileManagerTab{{path: "old.go"}}
	page := inspectorAttachPage(s)
	s.client.(*FakeClient).SetThreadWorkDir("/tmp/new-worktree")
	inspectorValidateWorkspace(s)
	if st.Files == files || st.Review == review || len(st.Files.tabs) != 0 || files.gen != 1 || files.tabs[0].gen != 1 || review.requestGen != 1 {
		t.Fatal("工作区变化未作废缓存和旧请求")
	}
	if st.Browser.page != page || page.destroyed.Load() != 0 {
		t.Fatal("作废工作区缓存时错误销毁了网页")
	}
}
