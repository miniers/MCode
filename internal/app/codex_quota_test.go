package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcode/internal/providers/codex"
	"mcode/internal/providers/magpie"
	"mcode/internal/remote"
	mcodeui "mcode/internal/ui"
)

// 直连 Codex 只有一组窗口，套餐与读取时间来自本机登录的实时读取。
func TestCodexQuotaWindowsDirect(t *testing.T) {
	reset := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	readAt := time.Date(2026, 10, 18, 11, 58, 0, 0, time.UTC)
	windows := codexQuotaWindows(codex.RateLimits{
		Source:   codex.QuotaSourceOpenAI,
		PlanType: "Pro 20x",
		ReadAt:   readAt,
		Windows: []codex.RateLimitWindow{
			{ID: "codex-primary", Title: "5-hour limit", Period: "5h", UsedPercent: 25.4, ResetsAt: &reset},
			{ID: "codex-secondary", Title: "Weekly", Period: "7d", UsedPercent: 60},
		},
	})
	if len(windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(windows))
	}
	for _, w := range windows {
		if w.Account != "" {
			t.Errorf("direct codex must not carry an account, got %q", w.Account)
		}
		if w.Source != codex.QuotaSourceOpenAI || w.Plan != "Pro 20x" || !w.ReadAt.Equal(readAt) {
			t.Errorf("unexpected source metadata: %+v", w)
		}
	}
	if !windows[0].ResetsAt.Equal(reset) || windows[0].UsedPercent != 25.4 {
		t.Errorf("unexpected first window: %+v", windows[0])
	}
	if !windows[1].ResetsAt.IsZero() {
		t.Errorf("missing reset time must stay zero, got %v", windows[1].ResetsAt)
	}
}

func codexQuotaTestGateway(t *testing.T, provider, account string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
			t.Errorf("Authorization = %q，想要 Bearer gw-key", got)
		}
		switch r.URL.Path {
		case "/v1/magpie/quotas":
			io.WriteString(w, `{"object":"list","data":[
				{"provider":"codex","user":"free@example.com","plan":"Free","windows":[{"name":"30 days","used":10}]},
				{"provider":"codex","user":"plus@example.com","plan":"Plus","windows":[{"name":"5 hours","used":20}]},
				{"provider":"sub2api","user":"sub@example.com","plan":"Wallet","windows":[{"name":"Credits","used":30}]}]}`)
		case "/v1/magpie/usage/requests":
			io.WriteString(w, `{"rows":[{"t":"2026-11-11T22:19:17Z","agent":"codex","provider":"`+provider+`","providerAccount":"`+account+`","session":"codex-session"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func codexQuotaTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	previousReader := readCodexQuotaLimits
	readCodexQuotaLimits = func(*Client, context.Context, string) (codex.RateLimits, error) {
		return codex.RateLimits{Source: codex.QuotaSourceMagpie, GatewayBaseURL: srv.URL + "/v1", GatewayToken: "gw-key"}, nil
	}
	t.Cleanup(func() { readCodexQuotaLimits = previousReader })
	client := NewClient(nil)
	client.SetAgySessionLookup(func(context.Context, string) string { return "codex-session" })
	tracker := newTestSessionAccountTracker(t)
	tracker.readUsage = magpie.ReadUsageRequests
	useQuotaTracker(client, tracker)
	return client
}

func assertCodexQuotaActive(t *testing.T, windows []mcodeui.ProviderQuotaWindow, activeUser string) {
	t.Helper()
	if len(windows) != 3 {
		t.Fatalf("窗口数 = %d，想要 3: %+v", len(windows), windows)
	}
	for _, w := range windows {
		if w.Source != codex.QuotaSourceMagpie || w.User == "" {
			t.Errorf("Magpie 卡片元数据不完整: %+v", w)
		}
		want := w.User == activeUser
		if w.Active != want {
			t.Errorf("账号 %q Active = %v，想要 %v: %+v", w.User, w.Active, want, w)
		}
	}
}

func TestProviderQuotaCodexMagpieUsageAccount(t *testing.T) {
	srv := codexQuotaTestGateway(t, "sub2api", "sub@example.com")
	defer srv.Close()
	client := codexQuotaTestClient(t, srv)
	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "thread-1"), "codex")
	if err != nil {
		t.Fatalf("ProviderQuota 失败: %v", err)
	}
	assertCodexQuotaActive(t, windows, "sub@example.com")
}

func TestProviderQuotaCodexMagpieCodexPlus(t *testing.T) {
	srv := codexQuotaTestGateway(t, "codex", "plus@example.com")
	defer srv.Close()
	client := codexQuotaTestClient(t, srv)
	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "thread-1"), "codex")
	if err != nil {
		t.Fatalf("ProviderQuota 失败: %v", err)
	}
	assertCodexQuotaActive(t, windows, "plus@example.com")
}

func TestProviderQuotaCodexMagpieInjectedRoute(t *testing.T) {
	srv := codexQuotaTestGateway(t, "codex", "plus@example.com")
	defer srv.Close()
	previousRoutes := codexMagpieRoutesForQuota
	previousModel := readCodexThreadModel
	previousReader := readCodexQuotaLimits
	codexMagpieRoutesForQuota = func() []codex.MagpieRoute {
		return []codex.MagpieRoute{{ProviderID: "magpie-test", BaseURL: srv.URL + "/v1", APIKey: "gw-key"}}
	}
	readCodexThreadModel = func(context.Context, *remote.MemoryBackend, string) (string, error) {
		return "magpie-test/auto/gpt", nil
	}
	readCodexQuotaLimits = func(*Client, context.Context, string) (codex.RateLimits, error) {
		t.Fatal("注入路由不应读取 Codex app-server")
		return codex.RateLimits{}, nil
	}
	t.Cleanup(func() {
		codexMagpieRoutesForQuota = previousRoutes
		readCodexThreadModel = previousModel
		readCodexQuotaLimits = previousReader
	})
	client := NewClient(nil)
	client.SetAgySessionLookup(func(context.Context, string) string { return "codex-session" })
	tracker := newTestSessionAccountTracker(t)
	tracker.readUsage = magpie.ReadUsageRequests
	useQuotaTracker(client, tracker)
	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "thread-1"), "codex")
	if err != nil {
		t.Fatalf("ProviderQuota 失败: %v", err)
	}
	assertCodexQuotaActive(t, windows, "plus@example.com")
}
