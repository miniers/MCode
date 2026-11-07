package app

// 本文件维护「会话 → 实际应答账号」的映射。pi / claude / codex 经 Magpie 网关发请求时，
// 网关的 trace 记录每条路由最终由哪个账号应答；额度窗口据此只给本会话真正用到的账号
// 标「在用」。查不到映射时谁都不标，不按模型名猜。

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"mcode/internal/providers/magpie"
)

const (
	// sessionRouteLimit 限制 bySession 的条目数：超出时按 At 淘汰最旧的。
	sessionRouteLimit = 2048
	// sessionRouteStaleAfter 之内只接受不比已存路由更旧的路由 ID：magpie 重启后
	// 路由 ID 会归零，超过这段时间后允许用更小的 ID 覆盖。
	sessionRouteStaleAfter = 10 * time.Minute
	// traceForbiddenBackoff 是 trace 返回 403 后的等待：远程或受限 key 读不到 trace，
	// 期间不反复重试，只保留首次同步读取的能力。
	traceForbiddenBackoff = 10 * time.Minute
	// traceFailureLogInterval 是 trace 连续失败时打日志的最小间隔，避免刷屏。
	traceFailureLogInterval = 10 * time.Minute
	// traceRouteReadTimeout 是首次查不到映射时同步读一次 trace 的超时：额度面板在
	// 交互路径上，读不到就按「未知」处理，不长时间阻塞。
	traceRouteReadTimeout = 2 * time.Second
	// usageRouteReadTimeout 是补读用量记录的超时：远程网关比本机 trace 慢，但仍在
	// 交互路径上，读不到就按「未知」处理。
	usageRouteReadTimeout = 5 * time.Second
	// usageRouteReadLimit 是补读用量记录时取的条数：只是找最近的几条路由，不需要整页。
	usageRouteReadLimit = 100
	// usageReadDedupeWindow 是同一「端点 + agent」允许再次读用量记录的间隔：远程网关
	// 不该因为每打开一次额度面板就打一次。
	usageReadDedupeWindow = 10 * time.Second
	// quotaRefreshDedupeWindow 是同一「端点 + provider + user」允许再次向网关发刷新
	// 请求的间隔，与网关服务端的去重窗口一致，避免每轮对话都白跑一次请求。
	quotaRefreshDedupeWindow = 30 * time.Second
	// quotaRefreshLimit 限制刷新时间表的条目数，超出时先清掉已经过期的条目。
	quotaRefreshLimit = 256
)

// traceBackoffSteps 是 trace 其它错误连续失败时的退避序列，走完后保持最后一步。
var traceBackoffSteps = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 60 * time.Second}

// sessionRoute 是一个会话最近一次被哪个账号应答。
type sessionRoute struct {
	Provider string
	User     string
	Kind     string
	Model    string
	// RouteID 是 magpie 的 TraceRoute.ID，用来丢弃乱序到达的旧路由。
	RouteID int64
	// At 是这条映射写入（或刷新）的时间，用于过期覆盖与 LRU 淘汰。
	At time.Time
}

// sessionAccountTracker 是进程内的会话账号跟踪器：trace 长期在后台拉取，映射按会话
// 保留最后一次结果，不随 trace 窗口滚动而消失。
type sessionAccountTracker struct {
	mu        sync.Mutex
	bySession map[string]sessionRoute
	// refreshAt 按「端点 + provider + user」记录上次向网关发额度刷新的时间。
	refreshAt map[string]time.Time
	// usageAt 按「端点 + agent」记录上次读用量记录的时间。
	usageAt map[string]time.Time
	// started 按源记录已启动的 trace 监听，保证同一端点只启动一次。
	started map[string]bool
	// ctx 控制所有监听 goroutine 的退出。
	ctx context.Context

	// readTrace、readUsage、sleep、now 是可变函数字段，测试里替换以绕开真实网络与时钟。
	readTrace func(ctx context.Context, baseURL, apiKey string, after int64, wait bool) (magpie.TraceSnapshot, error)
	readUsage func(ctx context.Context, baseURL, apiKey, agent string, limit int) ([]magpie.UsageRow, error)
	sleep     func(ctx context.Context, d time.Duration) bool
	now       func() time.Time
	// usageLogAt 是上次给用量读取失败打日志的时间，避免面板被反复打开时刷屏。
	usageLogAt time.Time
}

// newSessionAccountTracker 创建跟踪器；readTrace 为空时使用 magpie.ReadTrace。
func newSessionAccountTracker(ctx context.Context, readTrace func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error)) *sessionAccountTracker {
	if ctx == nil {
		ctx = context.Background()
	}
	if readTrace == nil {
		readTrace = magpie.ReadTrace
	}
	return &sessionAccountTracker{
		bySession: map[string]sessionRoute{},
		refreshAt: map[string]time.Time{},
		usageAt:   map[string]time.Time{},
		started:   map[string]bool{},
		ctx:       ctx,
		readTrace: readTrace,
		readUsage: magpie.ReadUsageRequests,
		sleep:     sleepContext,
		now:       time.Now,
	}
}

// sleepContext 等待 d 或 ctx 结束；返回 false 表示 ctx 已经结束。
func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ingest 把一次 trace 快照写进映射：只收会话非空且能判断出应答账号的路由。
// 同一会话的路由 ID 比已存的更旧时忽略，除非已存条目已经超过 10 分钟（magpie 重启
// 后 ID 归零）。
func (t *sessionAccountTracker) ingest(snap magpie.TraceSnapshot) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bySession == nil {
		t.bySession = map[string]sessionRoute{}
	}
	for _, r := range snap.Routes {
		session := strings.TrimSpace(r.Session)
		if session == "" {
			continue
		}
		provider, who, kind, ok := magpie.ServedAccount(r)
		if !ok {
			continue
		}
		if prev, exists := t.bySession[session]; exists &&
			r.ID < prev.RouteID && now.Sub(prev.At) < sessionRouteStaleAfter {
			continue
		}
		t.bySession[session] = sessionRoute{
			Provider: provider,
			User:     who,
			Kind:     kind,
			Model:    r.Model,
			RouteID:  r.ID,
			At:       now,
		}
	}
	for len(t.bySession) > sessionRouteLimit {
		t.evictOldestLocked()
	}
}

// evictOldestLocked 删掉 At 最早的一条；调用方必须持有 t.mu。
func (t *sessionAccountTracker) evictOldestLocked() {
	var oldestKey string
	var oldest time.Time
	for key, route := range t.bySession {
		if oldestKey == "" || route.At.Before(oldest) {
			oldestKey, oldest = key, route.At
		}
	}
	delete(t.bySession, oldestKey)
}

// ingestUsage 把一批用量记录写进映射：只收能判断出应答账号的行。行按时间从新到旧
// 排列，同一会话的新行先到，旧行不得覆盖它（行时间为零值时按 now）。
func (t *sessionAccountTracker) ingestUsage(rows []magpie.UsageRow) {
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bySession == nil {
		t.bySession = map[string]sessionRoute{}
	}
	for _, row := range rows {
		provider := strings.TrimSpace(row.Provider)
		if provider == "" || provider == "session-unknown" {
			continue
		}
		who := strings.TrimSpace(row.ProviderAccount)
		if who == "" {
			continue
		}
		session := strings.TrimSpace(row.Session)
		native := strings.TrimSpace(row.NativeSession)
		if session == "" && native == "" {
			continue
		}
		at := row.T
		if at.IsZero() {
			at = now
		}
		route := sessionRoute{Provider: provider, User: who, Model: row.Model, At: at}
		for _, id := range []string{session, native} {
			if id == "" {
				continue
			}
			if prev, exists := t.bySession[id]; exists && at.Before(prev.At) {
				continue
			}
			t.bySession[id] = route
		}
	}
	for len(t.bySession) > sessionRouteLimit {
		t.evictOldestLocked()
	}
}

// readUsageThrottled 判断同一「端点 + agent」是否已过 10 秒窗口：允许时记下时间并
// 返回 true，否则返回 false。时间用跟踪器的 now，测试里可替换。
func (t *sessionAccountTracker) readUsageThrottled(source, agent string) bool {
	key := traceSourceKey(source) + "|" + strings.TrimSpace(agent)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.usageAt == nil {
		t.usageAt = map[string]time.Time{}
	}
	if len(t.usageAt) >= quotaRefreshLimit {
		for k, at := range t.usageAt {
			if now.Sub(at) >= usageReadDedupeWindow {
				delete(t.usageAt, k)
			}
		}
	}
	if at, ok := t.usageAt[key]; ok && now.Sub(at) < usageReadDedupeWindow {
		return false
	}
	t.usageAt[key] = now
	return true
}

// logUsageFailure 按 watch 循环的方式节流日志：同一源最多每 10 分钟一条。
func (t *sessionAccountTracker) logUsageFailure(source string, err error) {
	now := t.now()
	t.mu.Lock()
	if !t.usageLogAt.IsZero() && now.Sub(t.usageLogAt) < traceFailureLogInterval {
		t.mu.Unlock()
		return
	}
	t.usageLogAt = now
	t.mu.Unlock()
	log.Printf("quota usage: %s 读取失败: %v", source, err)
}

// noteSource 登记一个 trace 源：只有本机 Magpie（回环地址）能读 trace，同一地址只启动
// 一次监听。远程端点不启动监听，也不做任何探测。
func (t *sessionAccountTracker) noteSource(baseURL, apiKey string) {
	if !magpie.IsLocalGateway(baseURL) {
		return
	}
	key := traceSourceKey(baseURL)
	if key == "" {
		return
	}
	t.mu.Lock()
	if t.started == nil {
		t.started = map[string]bool{}
	}
	if t.started[key] {
		t.mu.Unlock()
		return
	}
	t.started[key] = true
	t.mu.Unlock()
	go t.watch(baseURL, apiKey, key)
}

// traceSourceKey 把端点地址归一成「源」的键：去掉末尾斜杠与 /v1 后缀，避免同一网关
// 因写法不同启动多个监听。
func traceSourceKey(baseURL string) string {
	raw := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	raw = strings.TrimSuffix(raw, "/v1")
	return strings.ToLower(raw)
}

// allowRefresh 判断同一「端点 + provider + user」是否已过 30 秒去重窗口：允许时
// 记下当前时间并返回 true，否则返回 false。时间用跟踪器的 now，测试里可替换。
func (t *sessionAccountTracker) allowRefresh(source, provider, user string) bool {
	key := traceSourceKey(source) + "|" + strings.TrimSpace(provider) + "|" + strings.TrimSpace(user)
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.refreshAt == nil {
		t.refreshAt = map[string]time.Time{}
	}
	if len(t.refreshAt) >= quotaRefreshLimit {
		for k, at := range t.refreshAt {
			if now.Sub(at) >= quotaRefreshDedupeWindow {
				delete(t.refreshAt, k)
			}
		}
	}
	if at, ok := t.refreshAt[key]; ok && now.Sub(at) < quotaRefreshDedupeWindow {
		return false
	}
	t.refreshAt[key] = now
	return true
}

// watch 长轮询一个源的路由记录：第一次不等待，先拿到当前窗口；之后 wait=1 由网关挂起
// 到有变化或超时。正常轮询不打日志，连续失败时最多每 10 分钟记一条。
func (t *sessionAccountTracker) watch(baseURL, apiKey, source string) {
	after := int64(0)
	wait := false
	failures := 0
	var lastLog time.Time
	for {
		if t.ctx.Err() != nil {
			return
		}
		snap, err := t.readTrace(t.ctx, baseURL, apiKey, after, wait)
		if err != nil {
			delay := traceBackoffSteps[len(traceBackoffSteps)-1]
			switch {
			case errors.Is(err, magpie.ErrTraceForbidden):
				delay = traceForbiddenBackoff
			case failures < len(traceBackoffSteps):
				delay = traceBackoffSteps[failures]
			}
			failures++
			now := t.now()
			if lastLog.IsZero() || now.Sub(lastLog) >= traceFailureLogInterval {
				log.Printf("quota trace: %s 读取失败，%s 后重试: %v", source, delay, err)
				lastLog = now
			}
			if !t.sleep(t.ctx, delay) {
				return
			}
			continue
		}
		failures = 0
		wait = true
		t.ingest(snap)
		after = snap.Seq
	}
}

// routeFor 按候选会话 ID 查映射。fresh 为 false 时先看缓存；本机端点再同步读一次
// trace（2 秒超时）；远程端点、trace 读失败（包括 403）、或 trace 读完仍未命中时，
// 在 agent 非空且未被 10 秒节流挡住的情况下补读一次用量记录。最后再查一次缓存，
// 仍然查不到就返回 false，不猜。
func (t *sessionAccountTracker) routeFor(ctx context.Context, baseURL, apiKey, agent, sessionID string, fresh bool) (sessionRoute, bool) {
	candidates := sessionCandidates(sessionID)
	if len(candidates) == 0 {
		return sessionRoute{}, false
	}
	if !fresh {
		if route, ok := t.lookup(candidates); ok {
			return route, true
		}
	}
	if magpie.IsLocalGateway(baseURL) {
		readCtx, cancel := context.WithTimeout(ctx, traceRouteReadTimeout)
		snap, err := t.readTrace(readCtx, baseURL, apiKey, 0, false)
		cancel()
		if err == nil {
			t.ingest(snap)
			if route, ok := t.lookup(candidates); ok {
				return route, true
			}
		}
	}
	agent = strings.TrimSpace(agent)
	if agent == "" || !t.readUsageThrottled(baseURL, agent) {
		return t.lookup(candidates)
	}
	usageCtx, cancel := context.WithTimeout(ctx, usageRouteReadTimeout)
	defer cancel()
	rows, err := t.readUsage(usageCtx, baseURL, apiKey, agent, usageRouteReadLimit)
	if err != nil {
		t.logUsageFailure(traceSourceKey(baseURL), err)
		return t.lookup(candidates)
	}
	t.ingestUsage(rows)
	return t.lookup(candidates)
}

// lookup 按候选顺序查映射。
func (t *sessionAccountTracker) lookup(candidates []string) (sessionRoute, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, id := range candidates {
		if route, ok := t.bySession[id]; ok {
			return route, true
		}
	}
	return sessionRoute{}, false
}

// piSessionUUIDPattern 匹配 pi 的会话文件名：<时间戳>_<UUID>.jsonl。依据是 pi 落盘
// 的实际命名（如 2026-10-04T11-04-25-085Z_01a10696-22fc-7145-8c50-53284638ca27.jsonl），
// 而 pi 发给网关的 x-session-affinity 是其中的 UUID 段，不是完整路径。
var piSessionUUIDPattern = regexp.MustCompile(`_([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})\.jsonl$`)

// sessionCandidates 把线程落盘的会话 ID 展开成可能的 magpie 会话 ID。pi 存的是
// .jsonl 路径（见 internal/providers/pi/rpc.go 的 sessionIDFromState）：UUID 放第一位，
// 原始值放第二位，兼容某些 pi 版本直接发路径的情况。其它 provider 的会话 ID 原样返回。
func sessionCandidates(sessionID string) []string {
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return nil
	}
	if !strings.HasSuffix(strings.ToLower(id), ".jsonl") {
		return []string{id}
	}
	if match := piSessionUUIDPattern.FindStringSubmatch(filepath.Base(id)); len(match) == 2 {
		return []string{match[1], id}
	}
	return []string{id}
}
