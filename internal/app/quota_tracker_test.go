package app

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"

	"mcode/internal/providers/magpie"
)

// newTestSessionAccountTracker 返回一个不会真的做网络请求的跟踪器：ctx 已取消，
// 监听到 noteSource 时会在下一轮直接退出；同步读 trace / 读用量由用例按需替换。
func newTestSessionAccountTracker(t *testing.T) *sessionAccountTracker {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tracker := newSessionAccountTracker(ctx, func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error) {
		return magpie.TraceSnapshot{}, nil
	})
	tracker.readUsage = func(context.Context, string, string, string, int) ([]magpie.UsageRow, error) {
		return nil, nil
	}
	return tracker
}

// useQuotaTracker 把跟踪器装进 Client，并标记懒创建已完成。
func useQuotaTracker(c *Client, tracker *sessionAccountTracker) {
	c.quotaTracker = tracker
	c.quotaTrackerOnce.Do(func() {})
}

// sessionTraceRoute 构造一条能被 ServedAccount 判断出账号的 trace 路由。
func sessionTraceRoute(id int64, session, provider, who, kind string) magpie.TraceRoute {
	weighed := "w-" + strconv.FormatInt(id, 10)
	return magpie.TraceRoute{
		ID:      id,
		Session: session,
		Model:   "test-model",
		Order:   []magpie.TraceWeighed{{ID: weighed, Provider: provider, Who: who, Kind: kind}},
		Tries:   []magpie.TraceTry{{ID: weighed, Done: true, Status: 200}},
	}
}

// TestSessionAccountTrackerIngest 校验只记录能判断出应答账号的路由。
func TestSessionAccountTrackerIngest(t *testing.T) {
	tracker := newTestSessionAccountTracker(t)
	tracker.ingest(magpie.TraceSnapshot{Seq: 3, Routes: []magpie.TraceRoute{
		sessionTraceRoute(5, "sess-a", "claude", "me@example.com", "account"),
		sessionTraceRoute(6, "sess-b", "codex", "key-1", "key"),
		{ID: 7, Session: "sess-c", Model: "m"},
		{ID: 8, Session: "  ", Order: []magpie.TraceWeighed{{ID: "w-8", Provider: "claude"}}},
	}})

	route, ok := tracker.lookup([]string{"sess-a"})
	if !ok || route.Provider != "claude" || route.User != "me@example.com" || route.Kind != "account" || route.RouteID != 5 || route.Model != "test-model" {
		t.Errorf("sess-a 的路由 = %+v, %v；想要 claude/me@example.com/account/5", route, ok)
	}
	if route, ok := tracker.lookup([]string{"sess-b"}); !ok || route.Provider != "codex" || route.Kind != "key" {
		t.Errorf("sess-b 的路由 = %+v, %v；想要 codex/key", route, ok)
	}
	if _, ok := tracker.lookup([]string{"sess-c"}); ok {
		t.Error("判断不出应答账号的路由不应记录")
	}
}

// TestSessionAccountTrackerRouteIDOrdering 校验乱序到达的旧路由不覆盖新路由，
// 但条目超过 10 分钟后允许更小的路由 ID 覆盖（magpie 重启后 ID 归零）。
func TestSessionAccountTrackerRouteIDOrdering(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	tracker := newTestSessionAccountTracker(t)
	tracker.now = func() time.Time { return now }

	tracker.ingest(magpie.TraceSnapshot{Routes: []magpie.TraceRoute{
		sessionTraceRoute(42, "sess-a", "claude", "new@example.com", "account"),
	}})
	tracker.ingest(magpie.TraceSnapshot{Routes: []magpie.TraceRoute{
		sessionTraceRoute(41, "sess-a", "claude", "old@example.com", "account"),
	}})
	if route, _ := tracker.lookup([]string{"sess-a"}); route.User != "new@example.com" {
		t.Errorf("旧路由 ID 覆盖了新路由：%+v，想要 new@example.com", route)
	}

	now = now.Add(sessionRouteStaleAfter + time.Second)
	tracker.ingest(magpie.TraceSnapshot{Routes: []magpie.TraceRoute{
		sessionTraceRoute(7, "sess-a", "claude", "restarted@example.com", "account"),
	}})
	if route, _ := tracker.lookup([]string{"sess-a"}); route.User != "restarted@example.com" || route.RouteID != 7 {
		t.Errorf("过期条目应允许小 ID 覆盖：%+v，想要 restarted@example.com/7", route)
	}
}

// TestSessionCandidates 校验 pi 的 .jsonl 会话路径解析出 UUID 候选，其它写法原样返回。
func TestSessionCandidates(t *testing.T) {
	file := "/Users/me/.pi/agent/sessions/--Users-me-code--/2026-10-04T11-04-25-085Z_01a10696-22fc-7145-8c50-53284638ca27.jsonl"
	got := sessionCandidates(file)
	if len(got) != 2 || got[0] != "01a10696-22fc-7145-8c50-53284638ca27" || got[1] != file {
		t.Errorf("候选 = %v，想要 UUID 在前、原始路径在后", got)
	}

	if got := sessionCandidates(" 01a10696-22fc-7145-8c50-53284638ca27 "); len(got) != 1 || got[0] != "01a10696-22fc-7145-8c50-53284638ca27" {
		t.Errorf("纯会话 ID 的候选 = %v", got)
	}
	if got := sessionCandidates("/tmp/pi/session-no-uuid.jsonl"); len(got) != 1 || got[0] != "/tmp/pi/session-no-uuid.jsonl" {
		t.Errorf("解析不出 UUID 时应原样返回：%v", got)
	}
	if got := sessionCandidates("   "); got != nil {
		t.Errorf("空会话 ID 的候选 = %v，想要 nil", got)
	}
}

// TestSessionAccountTrackerEvictsOldest 校验超过上限时按 At 淘汰最旧的条目。
func TestSessionAccountTrackerEvictsOldest(t *testing.T) {
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	tracker := newTestSessionAccountTracker(t)
	tracker.now = func() time.Time { return now }

	for i := 0; i <= sessionRouteLimit+1; i++ {
		now = now.Add(time.Second)
		session := fmt.Sprintf("sess-%04d", i)
		tracker.ingest(magpie.TraceSnapshot{Routes: []magpie.TraceRoute{
			sessionTraceRoute(int64(i+1), session, "claude", "me@example.com", "account"),
		}})
	}

	tracker.mu.Lock()
	size := len(tracker.bySession)
	tracker.mu.Unlock()
	if size != sessionRouteLimit {
		t.Errorf("映射条目数 = %d，想要 %d", size, sessionRouteLimit)
	}
	if _, ok := tracker.lookup([]string{"sess-0000"}); ok {
		t.Error("最旧的条目应被淘汰")
	}
	if _, ok := tracker.lookup([]string{"sess-0001"}); ok {
		t.Error("第二旧的条目也应被淘汰")
	}
	if _, ok := tracker.lookup([]string{fmt.Sprintf("sess-%04d", sessionRouteLimit+1)}); !ok {
		t.Error("最新的条目应保留")
	}
}

// TestSessionAccountTrackerRouteForReadsTraceOnMiss 校验未命中时同步读一次 trace 再查。
func TestSessionAccountTrackerRouteForReadsTraceOnMiss(t *testing.T) {
	var calls int
	var gotAfter int64
	var gotWait bool
	tracker := newSessionAccountTracker(context.Background(), func(_ context.Context, baseURL, apiKey string, after int64, wait bool) (magpie.TraceSnapshot, error) {
		calls++
		gotAfter, gotWait = after, wait
		if baseURL != "http://127.0.0.1:8787/v1" || apiKey != "gw-key" {
			t.Errorf("读取参数 = %q/%q", baseURL, apiKey)
		}
		return magpie.TraceSnapshot{Seq: 9, Routes: []magpie.TraceRoute{
			sessionTraceRoute(3, "01a10696-22fc-7145-8c50-53284638ca27", "cline", "x@example.com", "account"),
		}}, nil
	})

	route, ok := tracker.routeFor(context.Background(), "http://127.0.0.1:8787/v1", "gw-key", "",
		"/Users/me/.pi/agent/sessions/--x--/2026-10-04T11-04-25-085Z_01a10696-22fc-7145-8c50-53284638ca27.jsonl", false)
	if !ok || route.User != "x@example.com" || route.Provider != "cline" {
		t.Fatalf("routeFor = %+v, %v；想要 cline/x@example.com", route, ok)
	}
	if calls != 1 || gotAfter != 0 || gotWait {
		t.Errorf("同步读次数 = %d, after = %d, wait = %v；想要 1 次、after=0、wait=false", calls, gotAfter, gotWait)
	}

	// 第二次直接命中映射，不再读 trace。
	if _, ok := tracker.routeFor(context.Background(), "http://127.0.0.1:8787/v1", "gw-key", "", "01a10696-22fc-7145-8c50-53284638ca27", false); !ok {
		t.Error("第二次应命中已有映射")
	}
	if calls != 1 {
		t.Errorf("命中映射后不应再读 trace，实际读了 %d 次", calls)
	}
}

// TestSessionAccountTrackerIgnoresRemoteSource 校验非回环地址既不启动监听也不读 trace。
func TestSessionAccountTrackerIgnoresRemoteSource(t *testing.T) {
	calls := 0
	tracker := newSessionAccountTracker(context.Background(), func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error) {
		calls++
		return magpie.TraceSnapshot{}, nil
	})

	tracker.noteSource("https://example.com/v1", "gw-key")
	tracker.mu.Lock()
	started := len(tracker.started)
	tracker.mu.Unlock()
	if started != 0 {
		t.Errorf("远程地址启动了 %d 个监听，想要 0", started)
	}

	if _, ok := tracker.routeFor(context.Background(), "https://example.com/v1", "gw-key", "", "sess-a", false); ok {
		t.Error("远程地址不应命中映射")
	}
	if calls != 0 {
		t.Errorf("远程地址读了 %d 次 trace，想要 0", calls)
	}
}

// TestSessionAccountTrackerForbiddenBackoff 校验 trace 返回 403 时等待 10 分钟再重试。
func TestSessionAccountTrackerForbiddenBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tracker := newSessionAccountTracker(ctx, func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error) {
		return magpie.TraceSnapshot{}, fmt.Errorf("%w: 403", magpie.ErrTraceForbidden)
	})
	var delays []time.Duration
	tracker.sleep = func(_ context.Context, d time.Duration) bool {
		delays = append(delays, d)
		cancel()
		return true
	}

	tracker.watch("http://127.0.0.1:8787/v1", "gw-key", "http://127.0.0.1:8787")
	if len(delays) != 1 || delays[0] != traceForbiddenBackoff {
		t.Errorf("403 的等待 = %v，想要 [%v]", delays, traceForbiddenBackoff)
	}
}

// TestSessionAccountTrackerErrorBackoff 校验其它错误按 2s、5s、15s、60s 退避，
// 到 60s 后保持不变。
func TestSessionAccountTrackerErrorBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tracker := newSessionAccountTracker(ctx, func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error) {
		return magpie.TraceSnapshot{}, fmt.Errorf("连接失败")
	})
	var delays []time.Duration
	tracker.sleep = func(_ context.Context, d time.Duration) bool {
		delays = append(delays, d)
		if len(delays) == len(traceBackoffSteps)+1 {
			cancel()
		}
		return true
	}

	tracker.watch("http://127.0.0.1:8787/v1", "gw-key", "http://127.0.0.1:8787")
	want := []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 60 * time.Second, 60 * time.Second}
	if len(delays) != len(want) {
		t.Fatalf("退避序列 = %v，想要 %v", delays, want)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Errorf("第 %d 次退避 = %v，想要 %v", i+1, delays[i], want[i])
		}
	}
}

// TestSessionAccountTrackerAllowRefresh 校验按「端点 + provider + user」的 30 秒去重：
// 同一键窗口内只放行一次，不同 provider / 端点各自独立，过了窗口再次放行。
func TestSessionAccountTrackerAllowRefresh(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	tracker := newTestSessionAccountTracker(t)
	tracker.now = func() time.Time { return now }

	const source = "http://127.0.0.1:8787"
	if !tracker.allowRefresh(source+"/v1", "claude", "a@example.com") {
		t.Fatal("第一次刷新应放行")
	}
	if tracker.allowRefresh(source+"/v1/", "claude", "a@example.com") {
		t.Error("同一端点的不同写法应在 30 秒内去重")
	}
	if !tracker.allowRefresh(source, "codex", "a@example.com") {
		t.Error("不同 provider 应各自独立计时")
	}
	if !tracker.allowRefresh("http://127.0.0.1:9999", "claude", "a@example.com") {
		t.Error("不同端点应各自独立计时")
	}
	if !tracker.allowRefresh(source, "claude", "b@example.com") {
		t.Error("不同账号应各自独立计时")
	}

	now = now.Add(quotaRefreshDedupeWindow)
	if !tracker.allowRefresh(source, "claude", "a@example.com") {
		t.Error("过了去重窗口应再次放行")
	}
}

// usageRow 构造一条用量记录。
func usageRow(at time.Time, provider, who, session, native string) magpie.UsageRow {
	return magpie.UsageRow{T: at, Agent: "pi", Provider: provider, ProviderAccount: who, Model: "test-model", Session: session, NativeSession: native}
}

// TestSessionAccountTrackerIngestUsage 校验用量记录的跳过规则、Session 与 NativeSession
// 都能命中，以及同一批里新行不被旧行覆盖。
func TestSessionAccountTrackerIngestUsage(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	tracker := newTestSessionAccountTracker(t)
	tracker.now = func() time.Time { return now }

	tracker.ingestUsage([]magpie.UsageRow{
		usageRow(now, "claude", "new@example.com", "sess-a", "native-a"),
		usageRow(now.Add(-time.Hour), "claude", "old@example.com", "sess-a", ""),
		usageRow(now, "session-unknown", "x@example.com", "sess-b", ""),
		usageRow(now, "", "x@example.com", "sess-c", ""),
		usageRow(now, "codex", "", "sess-d", ""),
		usageRow(now, "codex", "k@example.com", "", ""),
		usageRow(time.Time{}, "codex", "zero@example.com", "sess-e", ""),
	})

	for _, id := range []string{"sess-a", "native-a"} {
		route, ok := tracker.lookup([]string{id})
		if !ok || route.User != "new@example.com" || route.Provider != "claude" {
			t.Errorf("%s 的路由 = %+v, %v；想要 claude/new@example.com", id, route, ok)
		}
	}
	for _, id := range []string{"sess-b", "sess-c", "sess-d"} {
		if _, ok := tracker.lookup([]string{id}); ok {
			t.Errorf("%s 应被跳过", id)
		}
	}
	route, ok := tracker.lookup([]string{"sess-e"})
	if !ok || !route.At.Equal(now) {
		t.Errorf("时间零值的行应记为 now：%+v, %v", route, ok)
	}
}

// TestSessionAccountTrackerRouteForReadsUsageOnRemote 校验非本机端点未命中时补读用量
// 记录：agent 为 pi、不读 trace；同一端点 10 秒内不再重复读。
func TestSessionAccountTrackerRouteForReadsUsageOnRemote(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	traceCalls := 0
	var usageCalls int
	var gotAgent string
	var gotLimit int
	tracker := newTestSessionAccountTracker(t)
	tracker.now = func() time.Time { return now }
	tracker.readTrace = func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error) {
		traceCalls++
		return magpie.TraceSnapshot{}, nil
	}
	tracker.readUsage = func(_ context.Context, baseURL, apiKey, agent string, limit int) ([]magpie.UsageRow, error) {
		usageCalls++
		gotAgent, gotLimit = agent, limit
		if baseURL != "https://magpie-api.jp.lk.mk/v1" || apiKey != "gw-key" {
			t.Errorf("读取参数 = %q/%q", baseURL, apiKey)
		}
		return []magpie.UsageRow{
			usageRow(now, "claude", "remote@example.com", "01a1a471-b43c-77b7-813d-0131d486c1bf", ""),
		}, nil
	}

	const base = "https://magpie-api.jp.lk.mk/v1"
	file := "/Users/me/.pi/agent/sessions/--x--/2026-11-04T02-44-37-567Z_01a1a471-b43c-77b7-813d-0131d486c1bf.jsonl"
	route, ok := tracker.routeFor(context.Background(), base, "gw-key", "pi", file, false)
	if !ok || route.User != "remote@example.com" || route.Provider != "claude" {
		t.Fatalf("routeFor = %+v, %v；想要 claude/remote@example.com", route, ok)
	}
	if traceCalls != 0 {
		t.Errorf("非本机端点读了 %d 次 trace，想要 0", traceCalls)
	}
	if usageCalls != 1 || gotAgent != "pi" || gotLimit != usageRouteReadLimit {
		t.Errorf("读用量 %d 次、agent=%q、limit=%d；想要 1 次、pi、%d", usageCalls, gotAgent, gotLimit, usageRouteReadLimit)
	}

	// 另一个会话未命中时仍在 10 秒窗口内，不再读用量。
	if _, ok := tracker.routeFor(context.Background(), base, "gw-key", "pi", "sess-other", false); ok {
		t.Error("未命中时不应凭空返回路由")
	}
	if usageCalls != 1 {
		t.Errorf("10 秒内又读了 %d 次用量，想要 1 次", usageCalls)
	}

	// 过了窗口后允许再次读。
	now = now.Add(usageReadDedupeWindow)
	if _, ok := tracker.routeFor(context.Background(), base, "gw-key", "pi", "sess-other", false); ok {
		t.Error("未命中时不应凭空返回路由")
	}
	if usageCalls != 2 {
		t.Errorf("过了窗口后应再读一次用量，实际 %d 次", usageCalls)
	}
}

// TestSessionAccountTrackerRouteForUsageAfterTraceForbidden 校验本机端点 trace 返回
// 403 时退到用量记录，仍能标出应答账号。
func TestSessionAccountTrackerRouteForUsageAfterTraceForbidden(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	tracker := newTestSessionAccountTracker(t)
	tracker.now = func() time.Time { return now }
	tracker.readTrace = func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error) {
		return magpie.TraceSnapshot{}, fmt.Errorf("%w: 403", magpie.ErrTraceForbidden)
	}
	tracker.readUsage = func(context.Context, string, string, string, int) ([]magpie.UsageRow, error) {
		return []magpie.UsageRow{usageRow(now, "claude", "fallback@example.com", "sess-f", "")}, nil
	}

	route, ok := tracker.routeFor(context.Background(), "http://127.0.0.1:8787/v1", "gw-key", "pi", "sess-f", false)
	if !ok || route.User != "fallback@example.com" {
		t.Fatalf("routeFor = %+v, %v；想要 claude/fallback@example.com", route, ok)
	}
}

// TestSessionAccountTrackerRouteForFreshReadsUsage 校验 fresh=true 时即使缓存命中也会
// 重读；节流窗口内则退回缓存。
func TestSessionAccountTrackerRouteForFreshReadsUsage(t *testing.T) {
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	var usageCalls int
	tracker := newTestSessionAccountTracker(t)
	tracker.now = func() time.Time { return now }
	tracker.readUsage = func(context.Context, string, string, string, int) ([]magpie.UsageRow, error) {
		usageCalls++
		return []magpie.UsageRow{
			usageRow(now.Add(time.Minute), "claude", "failover@example.com", "sess-g", ""),
		}, nil
	}
	tracker.ingestUsage([]magpie.UsageRow{usageRow(now, "claude", "before@example.com", "sess-g", "")})

	const base = "https://magpie-api.jp.lk.mk/v1"
	// 非 fresh 直接命中缓存，不读用量。
	if route, ok := tracker.routeFor(context.Background(), base, "gw-key", "pi", "sess-g", false); !ok || route.User != "before@example.com" {
		t.Fatalf("缓存命中 = %+v, %v；想要 before@example.com", route, ok)
	}
	if usageCalls != 0 {
		t.Errorf("缓存命中时读了 %d 次用量，想要 0", usageCalls)
	}

	// fresh 绕过缓存，以用量里的新账号为准。
	if route, ok := tracker.routeFor(context.Background(), base, "gw-key", "pi", "sess-g", true); !ok || route.User != "failover@example.com" {
		t.Fatalf("fresh 读取 = %+v, %v；想要 failover@example.com", route, ok)
	}
	if usageCalls != 1 {
		t.Errorf("fresh 读取次数 = %d，想要 1", usageCalls)
	}

	// 10 秒窗口内 fresh 也不得再读，退回现有缓存。
	if route, ok := tracker.routeFor(context.Background(), base, "gw-key", "pi", "sess-g", true); !ok || route.User != "failover@example.com" {
		t.Fatalf("节流内 fresh = %+v, %v；想要 failover@example.com", route, ok)
	}
	if usageCalls != 1 {
		t.Errorf("节流内又读了 %d 次用量，想要 1 次", usageCalls)
	}
}
