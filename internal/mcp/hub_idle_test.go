package mcp

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeUpstreamClient 是最小化的 upstreamClient 实现，用于验证回收与能力查询逻辑。
type fakeUpstreamClient struct {
	closed int
	caps   json.RawMessage
}

func (f *fakeUpstreamClient) initialize(ctx context.Context) error { return nil }

func (f *fakeUpstreamClient) serverInfo() (string, string) { return "fake", "1.0" }

func (f *fakeUpstreamClient) listTools(ctx context.Context) ([]Tool, error) { return nil, nil }

func (f *fakeUpstreamClient) callTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func (f *fakeUpstreamClient) forward(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, *jsonRPCError, error) {
	return nil, nil, nil
}

func (f *fakeUpstreamClient) close() error {
	f.closed++
	return nil
}

// capabilityUpstreamClient 额外实现 capabilityReporter。
type capabilityUpstreamClient struct{ fakeUpstreamClient }

func (f *capabilityUpstreamClient) capabilities() json.RawMessage { return f.caps }

// hubHasClient 在持锁状态下检查某上游是否有客户端，避免与后台回收 goroutine 竞争。
func hubHasClient(h *Hub, id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.clients[id]
	return ok
}

// hubClient 在持锁状态下取出某上游客户端。
func hubClient(h *Hub, id string) (upstreamClient, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	c, ok := h.clients[id]
	return c, ok
}

// hubMapHas 在持锁状态下检查 lastUsed / inflight 中是否存在某键。
func hubMapHas[V any](h *Hub, m map[string]V, id string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := m[id]
	return ok
}

// stdioSpecForFakeServer 构造指向本测试进程内假 MCP 服务的 stdio 上游配置。
func stdioSpecForFakeServer(id string) UpstreamSpec {
	return UpstreamSpec{
		ID:        id,
		Transport: "stdio",
		Command:   os.Args[0],
		Env:       map[string]string{"MCODE_FAKE_MCP_STDIO": "1"},
	}
}

// useIdleTimeout 临时缩短空闲阈值，避免测试等默认的 15 分钟。
func useIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := upstreamIdleTimeout
	upstreamIdleTimeout = d
	t.Cleanup(func() { upstreamIdleTimeout = old })
}

// 空闲超过阈值且无在途请求的 stdio 客户端应被移除并关闭。
func TestHubReapIdle_回收空闲的stdio客户端(t *testing.T) {
	useIdleTimeout(t, 50*time.Millisecond)
	hub := NewHub(nil)
	defer hub.Close()

	hub.SetUpstreams([]UpstreamSpec{stdioSpecForFakeServer("stdio-idle")})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := hub.ListTools(ctx, "stdio-idle"); err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	first, ok := hubClient(hub, "stdio-idle")
	if !ok {
		t.Fatal("客户端未创建")
	}

	hub.mu.Lock()
	hub.lastUsed["stdio-idle"] = time.Now().Add(-time.Hour)
	hub.mu.Unlock()

	hub.reapIdle(time.Now())

	if hubHasClient(hub, "stdio-idle") {
		t.Fatal("空闲的 stdio 客户端应被移除")
	}
	if hubMapHas(hub, hub.lastUsed, "stdio-idle") {
		t.Fatal("lastUsed 条目应被清理")
	}
	if hubMapHas(hub, hub.inflight, "stdio-idle") {
		t.Fatal("inflight 条目应被清理")
	}

	// 回收后再次请求会懒创建新的客户端。
	second, release, err := hub.acquireClient("stdio-idle")
	if err != nil {
		t.Fatalf("acquireClient after reap failed: %v", err)
	}
	defer release()
	if second == first {
		t.Fatal("回收后应创建新的客户端")
	}
}

// 有在途请求时不得回收。
func TestHubReapIdle_有在途请求时不回收(t *testing.T) {
	useIdleTimeout(t, 50*time.Millisecond)
	hub := NewHub(nil)
	defer hub.Close()

	hub.SetUpstreams([]UpstreamSpec{stdioSpecForFakeServer("stdio-busy")})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := hub.ListTools(ctx, "stdio-busy"); err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	hub.mu.Lock()
	hub.lastUsed["stdio-busy"] = time.Now().Add(-time.Hour)
	hub.inflight["stdio-busy"] = 1
	hub.mu.Unlock()

	hub.reapIdle(time.Now())

	if !hubHasClient(hub, "stdio-busy") {
		t.Fatal("有在途请求的客户端不应被回收")
	}
}

func TestHubAcquireClient_使用期间不回收(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	hub.SetUpstreams([]UpstreamSpec{stdioSpecForFakeServer("stdio-acquired")})
	first, releaseFirst, err := hub.acquireClient("stdio-acquired")
	if err != nil {
		t.Fatalf("acquireClient failed: %v", err)
	}
	releaseFirst()
	client, release, err := hub.acquireClient("stdio-acquired")
	if err != nil {
		t.Fatalf("acquireClient existing client failed: %v", err)
	}
	if client != first {
		t.Fatal("应复用已有客户端")
	}

	hub.reapIdle(time.Now().Add(upstreamIdleTimeout + time.Hour))
	if !hubHasClient(hub, "stdio-acquired") {
		t.Fatal("使用中的客户端不应被回收")
	}

	release()
	hub.reapIdle(time.Now().Add(upstreamIdleTimeout + time.Hour))
	if hubHasClient(hub, "stdio-acquired") {
		t.Fatal("释放后空闲超时的客户端应被回收")
	}
}

// 非 stdio 客户端不参与回收。
func TestHubReapIdle_非stdio客户端不回收(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	fake := &fakeUpstreamClient{}
	hub.mu.Lock()
	hub.clients["http-like"] = fake
	hub.lastUsed["http-like"] = time.Now().Add(-time.Hour)
	hub.mu.Unlock()

	hub.reapIdle(time.Now())

	if !hubHasClient(hub, "http-like") {
		t.Fatal("非 stdio 客户端不应被回收")
	}
	if fake.closed != 0 {
		t.Fatalf("非 stdio 客户端不应被关闭，实际关闭 %d 次", fake.closed)
	}
}

// 未超过阈值时不得回收。
func TestHubReapIdle_未超阈值不回收(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	hub.SetUpstreams([]UpstreamSpec{stdioSpecForFakeServer("stdio-fresh")})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := hub.ListTools(ctx, "stdio-fresh"); err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	hub.reapIdle(time.Now())

	if !hubHasClient(hub, "stdio-fresh") {
		t.Fatal("未超过空闲阈值的客户端不应被回收")
	}
}

// inflight 计数在请求期间正确累加与归零。
func TestHubAcquireRelease_计数归零(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	hub.SetUpstreams([]UpstreamSpec{stdioSpecForFakeServer("x")})
	_, releaseFirst, err := hub.acquireClient("x")
	if err != nil {
		t.Fatalf("acquireClient failed: %v", err)
	}
	_, releaseSecond, err := hub.acquireClient("x")
	if err != nil {
		t.Fatalf("second acquireClient failed: %v", err)
	}
	hub.mu.RLock()
	inflight := hub.inflight["x"]
	hub.mu.RUnlock()
	if inflight != 2 {
		t.Fatalf("期望 inflight=2，实际 %d", inflight)
	}
	releaseFirst()
	releaseSecond()
	if hubMapHas(hub, hub.inflight, "x") {
		t.Fatal("计数归零后应删除 inflight 条目")
	}
	// 多余的 release 不能让计数变负。
	hub.release("x")
	hub.mu.RLock()
	leftover, leftoverOK := hub.inflight["x"]
	hub.mu.RUnlock()
	if leftoverOK && leftover < 0 {
		t.Fatalf("inflight 不应为负：%d", leftover)
	}
}

// SetUpstreams 移除客户端时同步清理使用统计。
func TestHubSetUpstreams_清理使用统计(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	hub.SetUpstreams([]UpstreamSpec{stdioSpecForFakeServer("stdio-gone")})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := hub.ListTools(ctx, "stdio-gone"); err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	hub.SetUpstreams(nil)

	if hubMapHas(hub, hub.lastUsed, "stdio-gone") {
		t.Fatal("lastUsed 应随客户端一起清理")
	}
	if hubMapHas(hub, hub.inflight, "stdio-gone") {
		t.Fatal("inflight 应随客户端一起清理")
	}
}

// 重复关闭 Hub 不应因重复关闭 done 通道而 panic。
func TestHubClose_幂等关闭done(t *testing.T) {
	hub := NewHub(nil)
	if err := hub.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}
	if err := hub.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}
}

// 实现了 capabilityReporter 的客户端返回其 capabilities 原始 JSON。
func TestHubUpstreamCapabilities_返回客户端能力(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	caps := json.RawMessage(`{"tools":{},"resources":{"subscribe":true}}`)
	hub.mu.Lock()
	hub.clients["caps"] = &capabilityUpstreamClient{fakeUpstreamClient{caps: caps}}
	hub.mu.Unlock()

	got, err := hub.UpstreamCapabilities(context.Background(), "caps")
	if err != nil {
		t.Fatalf("UpstreamCapabilities failed: %v", err)
	}
	if string(got) != string(caps) {
		t.Fatalf("能力不匹配：期望 %s，实际 %s", string(caps), string(got))
	}
}

// 未实现 capabilityReporter 的客户端返回 nil, nil。
func TestHubUpstreamCapabilities_不支持时返回nil(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	hub.mu.Lock()
	hub.clients["plain"] = &fakeUpstreamClient{}
	hub.mu.Unlock()

	got, err := hub.UpstreamCapabilities(context.Background(), "plain")
	if err != nil {
		t.Fatalf("UpstreamCapabilities failed: %v", err)
	}
	if got != nil {
		t.Fatalf("不支持的客户端应返回 nil，实际 %s", string(got))
	}
}

// 未知上游返回错误而不是 panic。
func TestHubUpstreamCapabilities_未知上游返回错误(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	if _, err := hub.UpstreamCapabilities(context.Background(), "missing"); err == nil {
		t.Fatal("未知上游应返回错误")
	}
}

// 真实 stdio 客户端应把 initialize 结果里的 capabilities 保存下来。
func TestHubUpstreamCapabilities_stdio真实客户端(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Close()

	hub.SetUpstreams([]UpstreamSpec{stdioSpecForFakeServer("stdio-caps")})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	got, err := hub.UpstreamCapabilities(ctx, "stdio-caps")
	if err != nil {
		t.Fatalf("UpstreamCapabilities failed: %v", err)
	}
	if !strings.Contains(string(got), `"tools"`) {
		t.Fatalf("期望 capabilities 含 tools，实际 %s", string(got))
	}
}
