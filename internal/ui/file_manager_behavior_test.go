package mcodeui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

type behaviorClient struct {
	*FakeClient
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
}

func (c *behaviorClient) InspectorPreviewURL(ctx context.Context, threadID, path string) (string, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	select {
	case c.started <- struct{}{}:
	default:
	}
	select {
	case <-c.release:
		return "", errors.New("预览失败")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
func (c *behaviorClient) RevokeInspectorPreviews(string) {}
func (c *behaviorClient) FilesDuplicate(context.Context, string, string) (string, error) {
	return "", errors.New("磁盘错误")
}
func (c *behaviorClient) GitChangedFiles(context.Context, string) ([]GitChangedFile, error) {
	return nil, errors.New("读取改动失败")
}
func (c *behaviorClient) SearchMessages(context.Context, string, int) ([]MessageSearchHit, error) {
	return nil, errors.New("搜索错误")
}
func (c *behaviorClient) ForwardThread(context.Context, string) (string, error) {
	c.started <- struct{}{}
	<-c.release
	return "", errors.New("结束")
}
func newBehaviorClient() *behaviorClient {
	return &behaviorClient{FakeClient: treeTestBase(), started: make(chan struct{}, 4), release: make(chan struct{})}
}
func behaviorToast(s *chatState) string { s.mu.Lock(); defer s.mu.Unlock(); return s.toastMessage }
func waitBehavior(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("等待状态超时")
}

func TestFileManagerPreviewInflightAndError(t *testing.T) {
	for _, path := range []string{"doc.html", "doc.pdf"} {
		t.Run(path, func(t *testing.T) {
			client := newBehaviorClient()
			s := newInspectorPreviewState(t, client)
			tt := ui.NewTester(func(c *ui.Context) {
				renderFileManagerBrowserOpenButton(c, s, fileManagerTabSnapshot{path: path}, "test-")
			}, 100, 100)
			tt.Frame()
			if path == "doc.pdf" {
				s.openFileManagerFile("t1", path, false)
			} else if err := tt.Click("在浏览器打开"); err != nil {
				t.Fatal(err)
			}
			select {
			case <-client.started:
			case <-time.After(3 * time.Second):
				t.Fatal("请求未开始")
			}
			tt.Frame()
			info, ok := tt.Access("在浏览器打开")
			if !ok || !info.Disabled {
				t.Fatalf("按钮未禁用: %+v", info)
			}
			s.openInspectorPreview("t1", path)
			client.mu.Lock()
			calls := client.calls
			client.mu.Unlock()
			if calls != 1 {
				t.Fatalf("请求次数 = %d", calls)
			}
			close(client.release)
			waitInspectorPreview(t, s, "t1", path)
			if got := behaviorToast(s); got != "预览失败" {
				t.Fatalf("toast = %q", got)
			}
			tt.Frame()
			info, ok = tt.Access("在浏览器打开")
			if !ok || info.Disabled {
				t.Fatalf("按钮未恢复: %+v", info)
			}
		})
	}
}

func TestFileManagerDuplicateFailureToast(t *testing.T) {
	client := newBehaviorClient()
	s := newInspectorPreviewState(t, client)
	s.duplicateFileManagerEntry("a.txt")
	waitBehavior(t, func() bool { return behaviorToast(s) == "复制失败: 磁盘错误" })
}

func TestFileManagerOnlyChangedFailure(t *testing.T) {
	client := newBehaviorClient()
	_, tt := openFileTreeWatchForTest(t, client)
	waitFor(t, tt, func() bool { return tt.HasText("a.txt") })
	if err := tt.Click("只看改动"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("读取改动失败") })
	if tt.HasText("没有改动") {
		t.Fatal("读取失败显示为空改动")
	}
}

func TestPreviewConsoleFeedback(t *testing.T) {
	s, _ := inspectorContextSetup(t)
	tab := getPreviewState(s)
	previewMu.Lock()
	tab.consoleOpen = true
	tab.consoleErrors = []previewConsoleError{{Sequence: 1, Kind: "error", Message: "error"}}
	tab.consoleSelected = map[uint64]bool{1: true}
	previewMu.Unlock()
	limit := s.attachmentLimit()
	s.mu.Lock()
	s.attachments = make([]composerAttachment, limit)
	s.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) {
		previewMu.Lock()
		snap := previewSnapshotLocked(tab)
		previewMu.Unlock()
		renderPreviewConsoleDrawer(c, s, tab, snap)
	}, 700, 250)
	tt.Advance(DurSheet + 50*time.Millisecond)
	if err := tt.Click("复制"); err != nil {
		t.Fatal(err)
	}
	if got := behaviorToast(s); got != "已复制" {
		t.Fatalf("复制 toast = %q", got)
	}
	if err := tt.Click("添加到对话"); err != nil {
		t.Fatal(err)
	}
	if got := behaviorToast(s); got != "添加失败" {
		t.Fatalf("添加 toast = %q", got)
	}
}

func TestPaletteMessageSearchFailure(t *testing.T) {
	client := newBehaviorClient()
	s := newChatState(client)
	t.Cleanup(s.close)
	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		s.applyWorkspaceUpdates()
		if c.Shortcut(ui.Cmd, ui.KeyK) {
			togglePalette(s)
		}
		renderCommandPalette(c, s)
	}, 1000, 700)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Type("不存在的搜索内容")
	waitFor(t, tt, func() bool { return tt.HasText("搜索失败") })
	if tt.HasText("没有匹配项") {
		t.Fatal("搜索失败显示为没有匹配项")
	}
}

func TestSidebarForwardInflightToast(t *testing.T) {
	client := newBehaviorClient()
	s := newChatState(client)
	t.Cleanup(s.close)
	s.forwardThread("p1", "t1")
	select {
	case <-client.started:
	case <-time.After(3 * time.Second):
		t.Fatal("转发未开始")
	}
	if got := behaviorToast(s); got != "正在转发…" {
		t.Fatalf("开始 toast = %q", got)
	}
	s.setToast("")
	s.forwardThread("p1", "t1")
	if got := behaviorToast(s); got != "正在转发…" {
		t.Fatalf("重复 toast = %q", got)
	}
	close(client.release)
	waitBehavior(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return !s.forwardInflight["t1"] })
}
