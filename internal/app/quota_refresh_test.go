package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcode/internal/providers/claude"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// setClaudeGatewayEnv 让 Claude Code 指向给定网关，并隔离真实的 HOME 与 Claude 配置目录，
// 避免读到开发机上的 settings.json 或登录凭据。
func setClaudeGatewayEnv(t *testing.T, base, token string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_BASE_URL", base)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", token)
	t.Setenv("ANTHROPIC_API_KEY", "")
}

// TestMarkRouteActive 校验只在 provider 与账号都对得上时标在用：账号大小写不敏感，
// 查不到路由、路由缺账号或窗口缺账号都不标。
func TestMarkRouteActive(t *testing.T) {
	windows := []mcodeui.ProviderQuotaWindow{
		{Provider: "claude", User: "a@example.com"},
		{Provider: "claude", User: "b@example.com"},
		{Provider: "codex", User: "b@example.com"},
		{Provider: "claude"},
	}
	markRouteActive(windows, sessionRoute{Provider: "Claude", User: "B@Example.com"}, true)
	for i, w := range windows {
		if want := i == 1; w.Active != want {
			t.Errorf("第 %d 个窗口 Active = %v，想要 %v: %+v", i, w.Active, want, w)
		}
	}

	markRouteActive(windows, sessionRoute{Provider: "claude", User: "a@example.com"}, false)
	if windows[0].Active {
		t.Errorf("查不到路由时不应标 Active: %+v", windows[0])
	}
	for i := range windows {
		windows[i].Active = false
	}
	markRouteActive(windows, sessionRoute{Provider: "claude", User: " "}, true)
	for _, w := range windows {
		if w.Active {
			t.Errorf("路由缺账号时不应标 Active: %+v", w)
		}
	}
}

// TestClaudeQuotaWindowsDirect 校验直连时映射行为不变：没有 Source、没有路由字段，
// 即使传了路由也不标 Active。
func TestClaudeQuotaWindowsDirect(t *testing.T) {
	limits := claude.PlanLimits{Windows: []claude.PlanLimitWindow{
		{ID: "session", Label: "5-hour limit", UsedPercent: 12},
	}}
	windows := claudeQuotaWindows(limits, sessionRoute{Provider: "claude", User: "a@example.com"}, true)
	if len(windows) != 1 {
		t.Fatalf("窗口数 = %d，想要 1", len(windows))
	}
	w := windows[0]
	if w.Source != "" || w.Active || w.Provider != "" || w.User != "" || w.Kind != "" {
		t.Errorf("直连窗口不应带 Magpie 路由字段: %+v", w)
	}
	if w.ID != "session" || w.Period != "5h" || w.UsedPercent != 12 {
		t.Errorf("直连窗口映射不对: %+v", w)
	}
}

// TestClaudeQuotaWindowsMagpieActive 校验网关来源时只给路由命中的账号标 Active，
// 并带上卡片的 Provider/User/Kind/Last 等元数据。
func TestClaudeQuotaWindowsMagpieActive(t *testing.T) {
	readAt := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	lastServedAt := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	limits := claude.PlanLimits{
		Source: "magpie",
		Windows: []claude.PlanLimitWindow{
			{ID: "session", Account: "a@example.com", Label: "a@example.com · 5-hour limit", UsedPercent: 10, Provider: "claude", User: "a@example.com", Kind: "subscription"},
			{ID: "session", Account: "b@example.com", Label: "b@example.com · 5-hour limit", UsedPercent: 20, Provider: "claude", User: "b@example.com", Kind: "subscription", Last: true, LastServedAt: lastServedAt, ReadAt: readAt},
		},
	}
	windows := claudeQuotaWindows(limits, sessionRoute{Provider: "claude", User: "b@example.com"}, true)
	if len(windows) != 2 {
		t.Fatalf("窗口数 = %d，想要 2", len(windows))
	}
	if windows[0].Active {
		t.Errorf("a@example.com 不是本会话应答的账号，不应 Active: %+v", windows[0])
	}
	second := windows[1]
	if !second.Active {
		t.Errorf("b@example.com 是本会话应答的账号，应 Active: %+v", second)
	}
	if second.Source != codex.QuotaSourceMagpie || second.Provider != "claude" || second.User != "b@example.com" || second.Kind != "subscription" {
		t.Errorf("卡片元数据映射不完整: %+v", second)
	}
	if !second.Last || !second.ReadAt.Equal(readAt) || !second.LastServedAt.Equal(lastServedAt) {
		t.Errorf("卡片时间与 Last 映射不完整: %+v", second)
	}
}

// TestProviderQuotaClaudeMagpieActiveFollowsSession 校验 claude 经 Magpie 网关时，
// 额度窗口带 magpie 来源，且只给本会话 trace 路由命中的账号标「在用」。
func TestProviderQuotaClaudeMagpieActiveFollowsSession(t *testing.T) {
	claude.ResetPlanLimitsCache()
	t.Cleanup(claude.ResetPlanLimitsCache)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			io.WriteString(w, `{"name":"magpie"}`)
		case "/v1/magpie/quotas/cards":
			io.WriteString(w, `{"data":[
				{"provider":"claude","plan":"Pro","user":"a@example.com","kind":"subscription","windows":[{"name":"5 hours","used":10}]},
				{"provider":"claude","plan":"Max","user":"b@example.com","kind":"subscription","last":true,
				 "lastServedAt":"2026-11-04T09:00:00Z","readAt":"2026-11-04T09:30:00Z",
				 "windows":[{"name":"5 hours","used":20}]}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setClaudeGatewayEnv(t, srv.URL, "gw-token")

	client := NewClient(nil)
	client.SetAgySessionLookup(func(_ context.Context, threadID string) string {
		if threadID == "th-1" {
			return "claude-session-1"
		}
		return ""
	})
	tracker := newTestSessionAccountTracker(t)
	useQuotaTracker(client, tracker)
	tracker.ingest(magpie.TraceSnapshot{Seq: 1, Routes: []magpie.TraceRoute{
		sessionTraceRoute(7, "claude-session-1", "claude", "b@example.com", "subscription"),
	}})

	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "th-1"), "claude")
	if err != nil {
		t.Fatalf("ProviderQuota 失败: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("窗口数 = %d，想要 2: %+v", len(windows), windows)
	}
	if windows[0].Active {
		t.Errorf("a@example.com 不是本会话应答的账号，不应 Active: %+v", windows[0])
	}
	if !windows[1].Active {
		t.Errorf("b@example.com 是本会话应答的账号，应 Active: %+v", windows[1])
	}
	for _, w := range windows {
		if w.Source != codex.QuotaSourceMagpie {
			t.Errorf("Source = %q，想要 %q", w.Source, codex.QuotaSourceMagpie)
		}
		if w.Provider != "claude" || w.User == "" {
			t.Errorf("卡片缺少 Provider/User: %+v", w)
		}
	}

	// 会话解析不出 provider 会话 ID 或 trace 里没有该会话时，哪张卡都不标。
	none, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "th-2"), "claude")
	if err != nil {
		t.Fatalf("ProviderQuota（无路由）失败: %v", err)
	}
	for _, w := range none {
		if w.Active {
			t.Errorf("没有路由时不应标 Active: %+v", w)
		}
	}
}

// TestRefreshThreadQuotaClaude 校验 claude 经 Magpie 时按会话路由刷新：只发一次带
// provider 与 user 的 POST，30 秒内不重复；网关不是 Magpie 时不发请求。
func TestRefreshThreadQuotaClaude(t *testing.T) {
	queries := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			io.WriteString(w, `{"name":"magpie"}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/magpie/quotas/refresh" {
			t.Errorf("请求 = %s %s，想要 POST /v1/magpie/quotas/refresh", r.Method, r.URL.Path)
		}
		queries <- r.URL.Query().Get("provider") + "|" + r.URL.Query().Get("user")
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()
	setClaudeGatewayEnv(t, srv.URL, "gw-token")
	claude.ResetPlanLimitsCache()
	t.Cleanup(claude.ResetPlanLimitsCache)

	client := NewClient(nil)
	client.SetAgySessionLookup(func(_ context.Context, threadID string) string {
		if threadID == "th-1" {
			return "claude-session-1"
		}
		return ""
	})
	tracker := newTestSessionAccountTracker(t)
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	useQuotaTracker(client, tracker)
	tracker.ingest(magpie.TraceSnapshot{Seq: 1, Routes: []magpie.TraceRoute{
		sessionTraceRoute(7, "claude-session-1", "claude", "b@example.com", "subscription"),
	}})

	ctx := context.Background()
	if err := client.RefreshThreadQuota(ctx, "th-1", "claude"); err != nil {
		t.Fatalf("RefreshThreadQuota 失败: %v", err)
	}
	select {
	case got := <-queries:
		if got != "claude|b@example.com" {
			t.Errorf("刷新参数 = %q，想要 claude|b@example.com", got)
		}
	default:
		t.Fatal("有路由时应向网关发一次刷新请求")
	}

	// 30 秒内同一「端点 + provider + user」不重复请求。
	if err := client.RefreshThreadQuota(ctx, "th-1", "claude"); err != nil {
		t.Fatalf("第二次 RefreshThreadQuota 失败: %v", err)
	}
	if len(queries) != 0 {
		t.Errorf("30 秒内不应重复刷新，实际收到 %d 个请求", len(queries))
	}

	// 过了去重窗口后允许再次刷新。
	now = now.Add(quotaRefreshDedupeWindow)
	if err := client.RefreshThreadQuota(ctx, "th-1", "claude"); err != nil {
		t.Fatalf("窗口后 RefreshThreadQuota 失败: %v", err)
	}
	// 读掉这条请求，避免影响后面两段的 len(queries) == 0 断言。
	select {
	case got := <-queries:
		if got != "claude|b@example.com" {
			t.Errorf("窗口后刷新参数 = %q，想要 claude|b@example.com", got)
		}
	default:
		t.Fatal("过了窗口应再次刷新，没有收到请求")
	}

	// 网关不是 Magpie 时不刷新。
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			queries <- "unexpected|" + r.URL.Path
		}
		io.WriteString(w, `{"name":"other"}`)
	}))
	defer other.Close()
	setClaudeGatewayEnv(t, other.URL, "gw-token")
	now = now.Add(quotaRefreshDedupeWindow)
	if err := client.RefreshThreadQuota(ctx, "th-1", "claude"); err != nil {
		t.Fatalf("非 Magpie 网关的 RefreshThreadQuota 返回错误: %v", err)
	}
	if len(queries) != 0 {
		t.Errorf("非 Magpie 网关不应刷新，实际收到 %d 个请求", len(queries))
	}

	// 其它提供方按空操作处理。
	if err := client.RefreshThreadQuota(ctx, "th-1", "gemini"); err != nil {
		t.Fatalf("未支持的提供方返回错误: %v", err)
	}
	if len(queries) != 0 {
		t.Errorf("未支持的提供方不应刷新，实际收到 %d 个请求", len(queries))
	}
}

// TestRefreshRouteAccountCodex 校验 codex 分支共用的刷新逻辑：用 codex 作为 agent
// 查路由，并把网关只重读命令发给命中的账号；查不到会话时什么都不发。
func TestRefreshRouteAccountCodex(t *testing.T) {
	queries := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/magpie/quotas/refresh" {
			t.Errorf("请求 = %s %s，想要 POST /v1/magpie/quotas/refresh", r.Method, r.URL.Path)
		}
		queries <- r.URL.Query().Get("provider") + "|" + r.URL.Query().Get("user")
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()

	client := NewClient(nil)
	client.SetAgySessionLookup(func(_ context.Context, threadID string) string {
		if threadID == "th-1" {
			return "codex-session-1"
		}
		return ""
	})
	tracker := newTestSessionAccountTracker(t)
	useQuotaTracker(client, tracker)
	tracker.ingest(magpie.TraceSnapshot{Seq: 1, Routes: []magpie.TraceRoute{
		sessionTraceRoute(7, "codex-session-1", "codex", "codex@example.com", "subscription"),
	}})

	ctx := context.Background()
	if err := client.refreshRouteAccount(ctx, srv.URL, "gw-key", "codex", "th-1"); err != nil {
		t.Fatalf("refreshRouteAccount 失败: %v", err)
	}
	select {
	case got := <-queries:
		if got != "codex|codex@example.com" {
			t.Errorf("刷新参数 = %q，想要 codex|codex@example.com", got)
		}
	default:
		t.Fatal("有路由时应向网关发一次刷新请求")
	}

	if err := client.refreshRouteAccount(ctx, srv.URL, "gw-key", "codex", "th-2"); err != nil {
		t.Fatalf("无会话时 refreshRouteAccount 返回错误: %v", err)
	}
	if len(queries) != 0 {
		t.Errorf("查不到会话时不应刷新，实际收到 %d 个请求", len(queries))
	}
}
