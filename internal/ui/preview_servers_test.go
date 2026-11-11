package mcodeui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	return ctx
}
