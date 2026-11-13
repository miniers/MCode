package mcodeui

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func inspectorSnapshotTestState(t *testing.T) (*chatState, *inspectorSessionState) {
	t.Helper()
	s := sidePaneTestState(t)
	t.Cleanup(s.close)
	inspectorSelect(s, sidePaneFileID, false)
	inspectorValidateWorkspace(s)
	st := inspectorSession(s)
	fileManagerMu.Lock()
	st.Files.active = "src/main.go"
	st.Files.tabs = []*fileManagerTab{{path: "src/main.go", hlFirst: 12, hlLast: 18}}
	fileManagerMu.Unlock()
	diffPanelMu.Lock()
	st.Review.scope, st.Review.turnID, st.Review.selectedPath = "session", "", "src/review.go"
	st.Review.snapshot = ReviewSnapshot{Scope: "session", BaseRevision: "base-1", HeadRevision: "head-1"}
	diffPanelMu.Unlock()
	s.mu.Lock()
	st.follow = &inspectorFollowState{paused: true, badges: map[string]int{sidePaneFileID: 2, sidePaneDiffID: 3}}
	s.mu.Unlock()
	return s, st
}

func TestInspectorSnapshotImmutableAndBounded(t *testing.T) {
	s, st := inspectorSnapshotTestState(t)
	before := time.Now()
	snap, ok := inspectorSnapshotFor(s)
	if !ok || snap.Timestamp.Before(before) || snap.Timestamp.After(time.Now()) || snap.Generation == 0 {
		t.Fatal("快照缺少时间或代次")
	}
	workspace := turnFilesWorkDir(s, "t1")
	if !snap.Open || snap.Active != sidePaneFileID || snap.Workspace != workspace+"\x00"+workspace || snap.FilePath != "src/main.go" || snap.FirstLine != 12 || snap.LastLine != 18 {
		t.Fatalf("容器或文件位置错误：%+v", snap)
	}
	if snap.ReviewScope != "session" || snap.ReviewTurnID != "" || snap.ReviewBaseRevision != "base-1" || snap.ReviewHeadRevision != "head-1" || snap.ReviewPath != "src/review.go" || snap.Following || !snap.Paused {
		t.Fatalf("审阅或跟随状态错误：%+v", snap)
	}
	snap.Badges[sidePaneFileID] = 99
	delete(snap.Badges, sidePaneDiffID)
	snap.Badges["unknown"] = 123
	s.mu.Lock()
	unchanged := st.follow.badges[sidePaneFileID] == 2 && st.follow.badges[sidePaneDiffID] == 3 && len(st.follow.badges) == 2
	s.mu.Unlock()
	if !unchanged {
		t.Fatal("修改快照徽标影响内部状态")
	}

	long := strings.Repeat("长", inspectorSnapshotStringMaxBytes)
	fileManagerMu.Lock()
	st.Files.active = long
	st.Files.tabs = []*fileManagerTab{{path: long, hlFirst: 1, hlLast: 2}}
	fileManagerMu.Unlock()
	diffPanelMu.Lock()
	st.Review.selectedPath, st.Review.turnID = long, long
	st.Review.snapshot.TurnID = long
	st.Review.snapshot.BaseRevision, st.Review.snapshot.HeadRevision = long, long
	diffPanelMu.Unlock()
	s.mu.Lock()
	st.workspace = filepath.Join(t.TempDir(), "workspace", long) + "\x00/main"
	st.Active = long
	s.mu.Unlock()
	snap, ok = inspectorSnapshotFor(s)
	if !ok || snap.FilePath == long || snap.FilePath == "" || snap.FirstLine != 1 || snap.LastLine != 2 {
		t.Fatal("超长路径未被截断或丢失行范围")
	}
	fields := reflect.ValueOf(snap)
	for i := 0; i < fields.NumField(); i++ {
		if field := fields.Field(i); field.Kind() == reflect.String && (len(field.String()) > inspectorSnapshotStringMaxBytes || !utf8.ValidString(field.String())) {
			t.Errorf("字符串 %s 未限制长度或被截断成无效 UTF-8", fields.Type().Field(i).Name)
		}
	}
	pathRoot := filepath.Join(t.TempDir(), "workspace")
	for _, tc := range []struct{ root, path, want string }{
		{pathRoot, filepath.Join(pathRoot, "src", "a.go"), "src/a.go"},
		{pathRoot, filepath.Join(t.TempDir(), "private", "secret"), ""},
		{"", filepath.Join(t.TempDir(), "private", "secret"), ""},
		{pathRoot, filepath.Join("..", "secret"), ""},
		{pathRoot, "https://example.test/token", ""},
	} {
		if got := inspectorSnapshotPath(tc.root, tc.path); got != tc.want {
			t.Errorf("路径 %q 的相对结果=%q，期望 %q", tc.path, got, tc.want)
		}
	}
}

func TestInspectorSnapshotRedactsSensitiveFields(t *testing.T) {
	s, st := inspectorSnapshotTestState(t)
	secret := "private-draft-console-comment-DOM-cookie-screenshot"
	s.mu.Lock()
	s.draft = secret
	st.follow.badges[secret] = 100
	s.mu.Unlock()
	fileManagerMu.Lock()
	st.Files.tabs[0].content = secret
	fileManagerMu.Unlock()
	diffPanelMu.Lock()
	st.Review.commentDraft = &reviewCommentDraft{body: secret, original: secret}
	st.Review.comments = []*reviewComment{{body: secret, original: secret}}
	diffPanelMu.Unlock()
	previewMu.Lock()
	tab := &browserTab{tabID: 1, html: secret, addr: secret, title: secret,
		consoleErrors: []previewConsoleError{{Message: secret}}, annotations: []previewAnnotation{{Selector: secret}}}
	st.Browser.tabs, st.Browser.activeTabID = []*browserTab{tab}, 1
	previewMu.Unlock()
	for _, tc := range []struct {
		name, raw, want string
		local           bool
	}{
		{"普通地址", "https://user:password@example.test:8443/private/path?token=secret-query#secret-hash", "https://example.test:8443", false},
		{"本地令牌", "http://127.0.0.1:4321/p/secret-token/index.html?token=secret-query#secret-hash", "local", true},
		{"粘贴本地令牌", "http://127.0.0.1:4321/p/secret-token/index.html?token=secret-query#secret-hash", "local", false},
		{"本地 IPv6 令牌", "http://[::1]:4321/p/secret-token/index.html", "local", false},
		{"本地跳转", "https://example.test/redirect?token=secret-query", "local", true},
		{"IPv6", "http://user:password@[::1]:3000/private?token=secret-query#secret-hash", "http://[::1]:3000", false},
		{"文件地址", "file:///private/secret-token", "", false},
		{"无效地址", "https://example.test:bad/private", "", false},
		{"空地址", "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			previewMu.Lock()
			tab.url, tab.localPreview = tc.raw, tc.local
			previewMu.Unlock()
			snap, ok := inspectorSnapshotFor(s)
			if !ok || snap.BrowserOrigin != tc.want {
				t.Fatalf("浏览器 origin=%q，期望 %q", snap.BrowserOrigin, tc.want)
			}
			encoded, err := json.Marshal(snap)
			if err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{secret, "secret-token", "secret-query", "secret-hash", "password", "/private", "user:"} {
				if strings.Contains(string(encoded), private) {
					t.Errorf("快照泄露敏感内容 %q", private)
				}
			}
		})
	}
}

// 嵌入空接口使任何未预期的 Client 调用直接失败。
type inspectorSnapshotNoCallClient struct {
	Client
	calls atomic.Int32
}

func (c *inspectorSnapshotNoCallClient) ThreadWorkDir(string) (string, bool) {
	c.calls.Add(1)
	return "", false
}

func TestInspectorSnapshotReadHasNoUIEffects(t *testing.T) {
	s, st := inspectorSnapshotTestState(t)
	client := &inspectorSnapshotNoCallClient{}
	s.client = client
	s.window = func() *mygo.Window { t.Fatal("快照调用了窗口回调"); return nil }
	t.Cleanup(func() { s.window = nil })
	// 不初始化根视图或加载面板，只给已有窗口挂接焦点与出帧探针。
	tt := ui.NewTester(func(c *ui.Context) {
		s.setUIServices(c.Services())
		ui.Box(c.Key("snapshot-focus")).Size(100, 60).Focusable().Label("快照焦点")
	}, 400, 200)
	tt.Advance(DurSheet + 50*time.Millisecond)
	if err := tt.Click("快照焦点"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.Focused("快照焦点") || tt.FramePending() {
		t.Fatal("焦点或静止帧探针未就绪")
	}
	previewMu.Lock()
	st.Browser.tabs, st.Browser.empty = nil, nil
	previewMu.Unlock()
	s.mu.Lock()
	count := len(s.inspector.Sessions)
	follow := st.follow
	s.mu.Unlock()
	for range 10 {
		if _, ok := inspectorSnapshotFor(s); !ok {
			t.Fatal("已有右栏无法读取快照")
		}
	}
	previewMu.Lock()
	empty := st.Browser.empty
	previewMu.Unlock()
	if empty != nil {
		t.Fatal("读取创建了浏览器空标签")
	}
	page := &inspectorTestPage{}
	previewMu.Lock()
	st.Browser.empty = &browserTab{page: page, url: "https://example.test/private?token=secret"}
	previewMu.Unlock()
	for range 10 {
		if _, ok := inspectorSnapshotFor(s); !ok {
			t.Fatal("已有页面无法读取快照")
		}
	}
	if page.loaded.Load() != 0 || page.evaluated.Load() != 0 || page.destroyed.Load() != 0 {
		t.Fatal("读取加载、求值或释放了页面")
	}
	s.mu.Lock()
	st.follow = nil
	s.selectedThreadID = "不存在的会话"
	s.mu.Unlock()
	if _, ok := inspectorSnapshotFor(s); ok {
		t.Fatal("读取创建了不存在的会话")
	}
	s.mu.Lock()
	s.selectedThreadID = ""
	s.mu.Unlock()
	if _, ok := inspectorSnapshotFor(s); ok {
		t.Fatal("读取创建了临时会话")
	}
	s.mu.Lock()
	s.selectedThreadID = "t1"
	s.mu.Unlock()
	snap, ok := inspectorSnapshotFor(s)
	if !ok || !snap.Following || snap.Paused {
		t.Fatal("尚未创建跟随状态时应默认跟随")
	}
	s.mu.Lock()
	unchanged := len(s.inspector.Sessions) == count && s.inspector.Temporary == nil && st.follow == nil
	st.follow = follow
	s.mu.Unlock()
	if !unchanged || client.calls.Load() != 0 || !tt.Focused("快照焦点") || tt.FramePending() {
		t.Fatal("读取改变了会话、跟随、客户端、焦点或出帧状态")
	}
	// 避免清理测试窗口时使用故意失败的回调。
	s.window = nil
}

func TestInspectorSnapshotWindowAndThreadIsolation(t *testing.T) {
	if _, ok := inspectorSnapshotFor(nil); ok {
		t.Fatal("nil 窗口应不可用")
	}
	a, slotA := inspectorSnapshotTestState(t)
	b, slotB := inspectorSnapshotTestState(t)
	fileManagerMu.Lock()
	slotB.Files.active = "window-b.go"
	fileManagerMu.Unlock()
	snapA, okA := inspectorSnapshotFor(a)
	snapB, okB := inspectorSnapshotFor(b)
	if !okA || !okB || snapA.WindowID == "" || snapA.WindowID == snapB.WindowID || snapB.FilePath != "window-b.go" || snapA.ThreadID != "t1" || snapB.ThreadID != "t1" {
		t.Fatal("同会话的两个窗口未隔离")
	}
	a.mu.Lock()
	a.selectedThreadID = "t2"
	slot2 := inspectorSessionLocked(a, "t2")
	a.mu.Unlock()
	inspectorSwitchThread(a, "t1")
	inspectorSelect(a, sidePaneDiffID, false)
	diffPanelMu.Lock()
	slot2.Review.scope, slot2.Review.turnID, slot2.Review.selectedPath = "turn", "turn-2", "thread-2.go"
	slot2.Review.snapshot = ReviewSnapshot{Scope: "turn", TurnID: "turn-2", BaseRevision: "base-2", HeadRevision: "head-2"}
	diffPanelMu.Unlock()
	snap2, ok := inspectorSnapshotFor(a)
	if !ok || snap2.WindowID != snapA.WindowID || snap2.ThreadID != "t2" || snap2.Active != sidePaneDiffID || snap2.ReviewTurnID != "turn-2" || snap2.ReviewBaseRevision != "base-2" || snap2.ReviewHeadRevision != "head-2" || snap2.ReviewPath != "thread-2.go" || len(snap2.Badges) != 0 {
		t.Fatalf("会话之间数据串用：%+v", snap2)
	}
	a.mu.Lock()
	a.selectedThreadID = "t1"
	a.mu.Unlock()
	inspectorSwitchThread(a, "t2")
	back, ok := inspectorSnapshotFor(a)
	if !ok || back.FilePath != snapA.FilePath || back.ReviewPath != snapA.ReviewPath || back.Generation <= snapA.Generation || slotA == slot2 {
		t.Fatal("切回会话未恢复自己的快照，或代次未前进")
	}
	inspectorClose(a)
	hidden, ok := inspectorSnapshotFor(a)
	if !ok || hidden.Open || hidden.Generation <= back.Generation {
		t.Fatal("收起右栏应仍可读取关闭状态与新代次")
	}
	a.close()
	if _, ok := inspectorSnapshotFor(a); ok {
		t.Fatal("关窗后快照仍可用")
	}
	if other, ok := inspectorSnapshotFor(b); !ok || other.WindowID != snapB.WindowID || other.FilePath != snapB.FilePath {
		t.Fatal("关窗影响了其他窗口")
	}
}

func TestInspectorSnapshotConcurrentLifecycle(t *testing.T) {
	s, _ := inspectorSnapshotTestState(t)
	var readers sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					if snap, ok := inspectorSnapshotFor(s); ok {
						snap.Badges[sidePaneFileID] = 99
					}
				}
			}
		}()
	}
	for i := range 100 {
		inspectorSelect(s, sidePaneFileID, i%2 == 0)
		if i%2 == 0 {
			s.client.(*FakeClient).SetThreadWorkDir("/tmp/snapshot-one")
		} else {
			s.client.(*FakeClient).SetThreadWorkDir("/tmp/snapshot-two")
		}
		inspectorValidateWorkspace(s)
		s.mu.Lock()
		st := inspectorExistingSessionLocked(s, s.selectedThreadID)
		f := inspectorFollowLocked(st)
		f.paused = i%2 == 0
		f.badges[sidePaneDiffID]++
		s.mu.Unlock()
		fileManagerMu.Lock()
		st.Files.active = "concurrent.go"
		st.Files.tabs = []*fileManagerTab{{path: "concurrent.go", hlFirst: i + 1, hlLast: i + 2}}
		fileManagerMu.Unlock()
		diffPanelMu.Lock()
		st.Review.snapshot = ReviewSnapshot{BaseRevision: "concurrent-base", HeadRevision: "concurrent-head"}
		diffPanelMu.Unlock()
		previewMu.Lock()
		st.Browser.empty = &browserTab{url: "https://example.test/private?token=secret"}
		previewMu.Unlock()
	}
	s.close()
	close(stop)
	readers.Wait()
	if _, ok := inspectorSnapshotFor(s); ok {
		t.Fatal("并发读取关窗后仍返回有效快照")
	}
}
