package mcodeui

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestBrowserServerDiscoveryFromToolOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	loopback := "http://127.0.0.1" + strings.TrimPrefix(srv.URL, "http://127.0.0.1")
	candidate := browserServerURLs("ready at " + loopback)
	if len(candidate) != 1 {
		t.Fatalf("工具输出中的 loopback 地址未发现: %v", candidate)
	}
	if browserServerOrigin(loopback) == "" {
		t.Fatal("loopback 地址解析失败")
	}
	if !browserServerProbe(testContext(t), loopback) {
		t.Fatal("httptest loopback 未通过探测")
	}
}
func TestBrowserServerDiscoveryLoopbackOnly(t *testing.T) {
	for _, host := range []string{"localhost:5173", "127.0.0.1:5173", "[::1]:5173", "0.0.0.0:5173"} {
		if got := browserServerOrigin("http://" + host); got == "" {
			t.Errorf("拒绝 loopback %s", host)
		}
	}
	for _, host := range []string{"example.com:5173", "192.168.1.2:5173", "localhost.evil:5173"} {
		if got := browserServerOrigin("http://" + host); got != "" {
			t.Errorf("接受非 loopback %s: %s", host, got)
		}
	}
}
func TestBrowserServerDiscoveryDedupAndExpiry(t *testing.T) {
	d := &browserServerDiscovery{threadID: "thread", workspace: "ws", visible: true, candidates: map[string]*browserServerCandidate{}}
	d.addLocked("thread", "ws", "http://localhost:5173", "tool")
	d.addLocked("thread", "ws", "http://localhost:5173", "terminal")
	if len(d.candidates) != 1 {
		t.Fatalf("候选未按 origin 去重: %d", len(d.candidates))
	}
	candidate := d.candidates["http://localhost:5173"]
	candidate.lastSeen = time.Now().Add(-browserServerExpiry - time.Second)
	if time.Since(candidate.lastSeen) <= browserServerExpiry {
		t.Fatal("过期测试时间无效")
	}
}
func TestBrowserServerDiscoveryDoesNotAutoNavigate(t *testing.T) {
	d := &browserServerDiscovery{threadID: "t", workspace: "w", candidates: map[string]*browserServerCandidate{}}
	d.addLocked("t", "w", "http://localhost:3000", "tool")
	if d.candidates["http://localhost:3000"].alive {
		t.Fatal("候选不应未经探测即为 alive")
	}
}
func TestBrowserServerDiscoveryStopsOnThreadChange(t *testing.T) {
	d := &browserServerDiscovery{threadID: "old", workspace: "w", visible: true, candidates: map[string]*browserServerCandidate{}}
	d.addLocked("old", "w", "http://localhost:3000", "tool")
	d.mu.Lock()
	d.stopLocked()
	d.threadID = "new"
	d.workspace = "w"
	d.candidates = make(map[string]*browserServerCandidate)
	d.mu.Unlock()
	if d.visible || len(d.candidates) != 0 {
		t.Fatal("切会话后旧候选仍处于活动状态")
	}
}

func TestBrowserServerCandidatesRenderedInToolbar(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	target := "http://localhost:" + port
	s := sidePaneTestState(t)
	defer s.close()
	tab, page := browserAttachTestPage(s, "https://example.test/start")
	inspectorSelect(s, sidePanePreviewID, false)
	tester := ui.NewTester(func(c *ui.Context) { renderPreviewPanel(c, s) }, 1800, 600)
	tester.Frame()
	tester.Advance(DurSheet + 50*time.Millisecond)
	if _, ok := tester.Find("3000"); !ok {
		t.Fatal("工具条缺少快捷端口")
	}
	browserServerObserveToolOutput(s, ThreadUpdate{ThreadID: "t1", Messages: []Message{{Role: "tool", Text: "ready at " + target}}})
	tester.Frame()
	d := browserServerDiscoveryFor(s)
	deadline := time.Now().Add(2 * time.Second)
	for {
		d.mu.Lock()
		candidate := d.candidates[target]
		alive := candidate != nil && candidate.alive
		d.mu.Unlock()
		if alive {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("服务器探测未成功")
		}
		time.Sleep(time.Millisecond)
	}
	tester.Frame()
	tester.Advance(DurSheet + 50*time.Millisecond)
	if _, ok := tester.Find(target); !ok {
		t.Fatal("工具条缺少已发现的服务器")
	}
	if tab.url != "https://example.test/start" || page.loaded.Load() != 0 {
		t.Fatal("发现服务器时不应导航")
	}
	if err := tester.Click(target); err != nil {
		t.Fatal(err)
	}
	tester.Frame()
	if tab.url != target || page.loaded.Load() != 1 {
		t.Fatalf("点击服务器未导航：%s", tab.url)
	}
}

func TestBrowserServerDiscoveryIdleWhenHidden(t *testing.T) {
	started := make(chan struct{}, 1)
	canceled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer srv.Close()
	s := sidePaneTestState(t)
	defer s.close()
	inspectorSelect(s, sidePanePreviewID, false)
	tester := ui.NewTester(func(c *ui.Context) { renderPreviewPanel(c, s) }, 1800, 600)
	tester.Frame()
	tester.Advance(DurSheet + 50*time.Millisecond)
	browserServerObserveToolOutput(s, ThreadUpdate{ThreadID: "t1", Messages: []Message{{Role: "tool", Text: srv.URL}}})
	tester.Frame()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("未开始探测")
	}
	inspectorClose(s)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("隐藏后探测未取消")
	}
	d := browserServerDiscoveryFor(s)
	d.mu.Lock()
	visible, cancel, lastProbe := d.visible, d.cancel, d.lastProbe
	d.mu.Unlock()
	if visible || cancel != nil {
		t.Fatal("隐藏后发现状态仍在运行")
	}
	frames := 0
	idle := ui.NewTester(func(c *ui.Context) {
		frames++
		browserServerDiscoveryTick(c, s, "t1")
	}, 100, 100)
	idle.Frame()
	initialFrames := frames
	idle.Advance(3 * browserServerProbeInterval)
	if frames != initialFrames {
		t.Fatalf("隐藏后仍调度了 %d 帧", frames-initialFrames)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastProbe != lastProbe || d.cancel != nil {
		t.Fatal("隐藏后仍发起探测")
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
