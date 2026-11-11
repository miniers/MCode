package mcodeui

import (
	"path/filepath"
	"reflect"
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
	browserPromoteTabLocked(st)
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
	if slot.Files != files || slot.Review != review || slot.Browser != browser || getPreviewState(s).page != page {
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
	if getPreviewState(s).page != page || page.destroyed.Load() != 0 {
		t.Fatal("作废工作区缓存时错误销毁了网页")
	}
}

func TestInspectorWorktreeResolveKeepsReviewState(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	s := sidePaneTestState(t)
	defer s.close()
	inspectorValidateWorkspace(s)
	st := inspectorSession(s)
	files, review := st.Files, st.Review
	for _, path := range []string{filepath.Join(t.TempDir(), "ai-one"), "", filepath.Join(t.TempDir(), "ai-two")} {
		setDiffTestWorktree(t, s, "t1", path)
		inspectorValidateWorkspace(s)
		if st.Review != review || st.Files != files {
			t.Fatal("AI worktree 解析变化重置了会话缓存")
		}
	}
	s.client.(*FakeClient).SetThreadWorkDir(t.TempDir())
	inspectorValidateWorkspace(s)
	if st.Files == files || st.Review == review {
		t.Fatal("root 变化未重置文件与审阅缓存")
	}
}

func TestInspectorThreadPreferenceRestore(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	s := sidePaneTestState(t)
	WithUIStatePersistence()(s)
	applyUIState(s, UIState{})
	inspectorSelect(s, sidePanePreviewID, false)
	inspectorSession(s).Width = 640
	s.selectThread("t2")
	inspectorSelect(s, sidePaneDiffID, false)
	inspectorSession(s).Width = 370
	inspectorClose(s)
	s.selectThread("t1")
	waitUIStateWrites()
	s.close()

	other := sidePaneTestState(t)
	defer other.close()
	WithUIStatePersistence()(other)
	applyUIState(other, loadUIState())
	a := inspectorSession(other)
	if !a.Open || a.Active != sidePanePreviewID || a.Width != 640 || getPreviewState(other).page != nil {
		t.Fatalf("重启后会话 A 偏好未恢复或创建了网页: %+v", a)
	}
	_, actual := inspectorLayout(300, a.Width)
	if actual != 300 || a.Width != 640 {
		t.Fatal("窄窗口改写了期望宽度")
	}
	other.selectThread("t2")
	b := inspectorSession(other)
	if b.Open || b.Active != sidePaneDiffID || b.Width != 370 {
		t.Fatalf("重启后会话 B 偏好未恢复: %+v", b)
	}
	other.forgetDeletedThread("t1")
	waitUIStateWrites()
	if _, ok := loadUIState().InspectorSessions["t1"]; ok {
		t.Fatal("删除会话后偏好仍存在")
	}
}

func TestInspectorTransientThreadNotPersisted(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	s := sidePaneTestState(t)
	defer s.close()
	WithUIStatePersistence()(s)
	applyUIState(s, UIState{})
	s.selectThread("")
	inspectorSelect(s, sidePanePreviewID, false)
	inspectorSession(s).Width = 600
	inspectorClose(s)
	s.selectThread("t1")
	waitUIStateWrites()
	if _, ok := loadUIState().InspectorSessions[""]; ok {
		t.Fatal("临时会话以空键落盘")
	}
	if len(loadUIState().InspectorSessions) != 0 {
		t.Fatal("临时会话写入了真实会话的偏好")
	}
}

func TestInspectorPrunePreferences(t *testing.T) {
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	s := sidePaneTestState(t)
	defer s.close()
	WithUIStatePersistence()(s)
	prefs := map[string]InspectorPreference{
		"t1":      {Open: true, Active: sidePaneFileID, Width: 500},
		"deleted": {Open: true, Active: sidePaneDiffID, Width: 600},
	}
	if err := saveUIState(UIState{InspectorSessions: prefs}); err != nil {
		t.Fatal(err)
	}
	applyUIState(s, loadUIState())
	s.mu.Lock()
	old := inspectorSessionLocked(s, "deleted")
	s.mu.Unlock()
	inspectorPrunePreferences(s)
	inspectorPersistSession(s, "deleted", old)
	waitUIStateWrites()
	got := loadUIState().InspectorSessions
	if len(got) != 1 || !reflect.DeepEqual(got["t1"], prefs["t1"]) {
		t.Fatalf("清理未保留有效偏好或旧槽复活已删偏好: %+v", got)
	}
	if s.inspector.Sessions["deleted"] != nil {
		t.Fatal("过期会话槽未清理")
	}
}
