package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func floatPtr(v float64) *float64 { return &v }

func TestParseMagpieCodexCardsKeepsAccountsPlansAndReadTimes(t *testing.T) {
	cards := []magpieQuotaCard{
		{
			Provider:     "codex",
			Plan:         "plus",
			User:         "a@b.com",
			Kind:         "subscription",
			Last:         true,
			LastServedAt: "2026-10-28T03:10:00Z",
			ReadAt:       "2026-10-28T02:58:16.34Z",
			Windows: []magpieQuotaWindow{
				{Name: "5 hours", Used: floatPtr(85), ResetsAt: "2026-10-28T06:00:00Z"},
				{Name: "7 days", Used: floatPtr(40)},
			},
		},
		{
			Provider: "codex",
			Plan:     "free",
			User:     "{{EMAIL_d949a3a8}}",
			Windows: []magpieQuotaWindow{
				{Name: "30 days", Used: floatPtr(0)},
			},
		},
		{
			// 非 Codex 卡片必须过滤掉。
			Provider: "claude",
			Plan:     "pro",
			User:     "{{EMAIL_02bdb2f1}}",
			Windows:  []magpieQuotaWindow{{Name: "5 hours", Used: floatPtr(10)}},
		},
	}

	accounts := parseMagpieCodexCards(cards)
	if len(accounts) != 2 {
		t.Fatalf("expected 2 codex accounts, got %d: %+v", len(accounts), accounts)
	}
	first := accounts[0]
	if first.Account != "a@b.com" || first.PlanType != "Plus" {
		t.Errorf("unexpected first account: %+v", first.Account)
	}
	if !first.ReadAt.Equal(time.Date(2026, 10, 28, 2, 58, 16, 340_000_000, time.UTC)) {
		t.Errorf("expected card read time, got %v", first.ReadAt)
	}
	if first.Provider != "codex" || first.User != "a@b.com" {
		t.Errorf("expected card identity, got provider %q user %q", first.Provider, first.User)
	}
	if first.Kind != "subscription" || !first.Last {
		t.Errorf("expected kind and last flag, got kind %q last %v", first.Kind, first.Last)
	}
	if !first.LastServedAt.Equal(time.Date(2026, 10, 28, 3, 10, 0, 0, time.UTC)) {
		t.Errorf("expected last served time, got %v", first.LastServedAt)
	}
	if len(first.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(first.Windows))
	}
	if first.Windows[0].Period != "5h" || first.Windows[0].WindowDurationMins != 300 || first.Windows[0].UsedPercent != 85 {
		t.Errorf("unexpected 5h window: %+v", first.Windows[0])
	}
	if first.Windows[0].ResetsAt == nil || !first.Windows[0].ResetsAt.Equal(time.Date(2026, 10, 28, 6, 0, 0, 0, time.UTC)) {
		t.Errorf("expected 5h reset time, got %v", first.Windows[0].ResetsAt)
	}
	if first.Windows[1].Period != "7d" || first.Windows[1].WindowDurationMins != 10080 {
		t.Errorf("unexpected 7d window: %+v", first.Windows[1])
	}

	second := accounts[1]
	if second.Account != "{{EMAIL_d949a3a8}}" || second.PlanType != "Free" {
		t.Errorf("unexpected second account: %+v", second)
	}
	// 0% 是已知用量，不能因为数值为零就丢掉窗口。
	if len(second.Windows) != 1 || second.Windows[0].Period != "30d" || second.Windows[0].UsedPercent != 0 {
		t.Errorf("unexpected second windows: %+v", second.Windows)
	}
	// 卡片没给读取时间时保持零值，不能伪装成当前时间。
	if !second.ReadAt.IsZero() {
		t.Errorf("unknown read time must stay zero, got %v", second.ReadAt)
	}
}

func TestParseMagpieCodexCardsDistinguishesAccounts(t *testing.T) {
	cards := []magpieQuotaCard{
		// 别名优先于账号标识。
		{Provider: "codex", Alias: "work", User: "a@b.com", Windows: []magpieQuotaWindow{{Name: "5 hours", Used: floatPtr(1)}}},
		// 远程 Magpie 的卡片带来源前缀，避免与本机卡片重名。
		{Provider: "codex", User: "a@b.com", From: "magpie-jp", Windows: []magpieQuotaWindow{{Name: "5 hours", Used: floatPtr(2)}}},
		// 标识完全缺失时按序号区分。
		{Provider: "codex", Windows: []magpieQuotaWindow{{Name: "5 hours", Used: floatPtr(3)}}},
		// 标识重复时补序号，不能合并成同一账号。
		{Provider: "codex", User: "work", Windows: []magpieQuotaWindow{{Name: "5 hours", Used: floatPtr(4)}}},
	}
	accounts := parseMagpieCodexCards(cards)
	if len(accounts) != 4 {
		t.Fatalf("expected 4 accounts, got %d: %+v", len(accounts), accounts)
	}
	got := []string{accounts[0].Account, accounts[1].Account, accounts[2].Account, accounts[3].Account}
	want := []string{"work", "magpie-jp · a@b.com", "账号 3", "work #2"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("account %d: expected %q, got %q", i, want[i], got[i])
		}
	}
}

func TestParseMagpieCodexCardsSkipsInvalidWindows(t *testing.T) {
	cards := []magpieQuotaCard{
		{Provider: "codex", User: "a", Windows: []magpieQuotaWindow{
			{Name: "", Used: floatPtr(10)},
			{Name: "5 hours"}, // 缺用量不能当成 0%
			{Name: "7 days", Used: floatPtr(150)},
		}},
		{Provider: "codex", User: "b"}, // 没有窗口的卡片不展示
	}
	accounts := parseMagpieCodexCards(cards)
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d: %+v", len(accounts), accounts)
	}
	windows := accounts[0].Windows
	if len(windows) != 1 || windows[0].Title != "7 days" || windows[0].UsedPercent != 100 {
		t.Fatalf("unexpected windows: %+v", windows)
	}
}

func TestGatewayRootAndOpenAIHost(t *testing.T) {
	roots := map[string]string{
		"https://magpie-api.jp.lk.mk/v1":  "https://magpie-api.jp.lk.mk",
		"https://magpie-api.jp.lk.mk/v1/": "https://magpie-api.jp.lk.mk",
		"http://127.0.0.1:3425":           "http://127.0.0.1:3425",
		"https://api.openai.com/v1":       "https://api.openai.com",
	}
	for base, want := range roots {
		if got := gatewayRoot(base); got != want {
			t.Errorf("gatewayRoot(%q) = %q, want %q", base, got, want)
		}
	}
	openAI := []string{"https://api.openai.com/v1", "https://chatgpt.com/backend-api", "https://api.openai.com"}
	for _, base := range openAI {
		if !isOpenAIHost(base) {
			t.Errorf("expected %q to be an OpenAI host", base)
		}
	}
	gateways := []string{"https://magpie-api.jp.lk.mk/v1", "http://127.0.0.1:3425/v1", ""}
	for _, base := range gateways {
		if isOpenAIHost(base) {
			t.Errorf("expected %q not to be an OpenAI host", base)
		}
	}
}

func TestQuotaCacheKeyIsolatesSources(t *testing.T) {
	base := quotaCacheKey(QuotaOptions{CodexHome: "/codex/a", WorkDir: "/proj/a"}, "/bin/codex")
	if base == "" {
		t.Fatal("empty cache key")
	}
	if base == quotaCacheKey(QuotaOptions{CodexHome: "/codex/b", WorkDir: "/proj/a"}, "/bin/codex") {
		t.Error("different codex homes must not share cache")
	}
	if base == quotaCacheKey(QuotaOptions{CodexHome: "/codex/a", WorkDir: "/proj/b"}, "/bin/codex") {
		t.Error("different work dirs must not share cache")
	}
	if base == quotaCacheKey(QuotaOptions{CodexHome: "/codex/a", WorkDir: "/proj/a"}, "/bin/other") {
		t.Error("different binaries must not share cache")
	}
}

func TestReadMagpieQuota(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case magpieCardsPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data": []map[string]any{
					{
						"provider":     "codex",
						"plan":         "plus",
						"user":         "a@b.com",
						"kind":         "subscription",
						"last":         true,
						"lastServedAt": "2026-10-28T03:10:00Z",
						"readAt":       "2026-10-28T02:58:16Z",
						"windows": []map[string]any{
							{"name": "5 hours", "used": 85},
							{"name": "7 days", "used": 40},
						},
					},
					{"provider": "cline", "name": "Cline"},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	limits, err := readMagpieQuota(context.Background(), quotaRoute{BaseURL: srv.URL, Token: "secret-key"})
	if err != nil {
		t.Fatalf("readMagpieQuota: %v", err)
	}
	if limits.Source != QuotaSourceMagpie || len(limits.Accounts) != 1 {
		t.Fatalf("unexpected limits: %+v", limits)
	}
	if limits.Accounts[0].PlanType != "Plus" || len(limits.Accounts[0].Windows) != 2 {
		t.Errorf("unexpected account card: %+v", limits.Accounts[0])
	}
	acct := limits.Accounts[0]
	if acct.Provider != "codex" || acct.User != "a@b.com" {
		t.Errorf("expected card identity, got provider %q user %q", acct.Provider, acct.User)
	}
	if acct.Kind != "subscription" || !acct.Last {
		t.Errorf("expected kind and last flag, got kind %q last %v", acct.Kind, acct.Last)
	}
	if !acct.LastServedAt.Equal(time.Date(2026, 10, 28, 3, 10, 0, 0, time.UTC)) {
		t.Errorf("expected last served time, got %v", acct.LastServedAt)
	}
	if limits.GatewayBaseURL != srv.URL || limits.GatewayToken != "secret-key" {
		t.Errorf("expected gateway route to be exposed, got %q / %q", limits.GatewayBaseURL, limits.GatewayToken)
	}
}

func TestCloneRateLimitsKeepsGateway(t *testing.T) {
	src := RateLimits{
		Source:         QuotaSourceMagpie,
		GatewayBaseURL: "https://magpie.example",
		GatewayToken:   "secret-key",
		Accounts: []AccountQuota{{
			Account:  "a@b.com",
			Provider: "codex",
			User:     "a@b.com",
			Kind:     "subscription",
			Last:     true,
		}},
	}
	got := cloneRateLimits(src)
	if got.GatewayBaseURL != src.GatewayBaseURL || got.GatewayToken != src.GatewayToken {
		t.Errorf("clone lost gateway route: %+v", got)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].Provider != "codex" || got.Accounts[0].User != "a@b.com" || got.Accounts[0].Kind != "subscription" || !got.Accounts[0].Last {
		t.Errorf("clone lost account identity: %+v", got.Accounts)
	}
}

func TestReadMagpieQuotaErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":"no quotas scope"}`, wantErr: "权限"},
		{name: "server error", status: http.StatusInternalServerError, body: "boom", wantErr: "500"},
		{name: "no codex cards", status: http.StatusOK, body: `{"object":"list","data":[{"provider":"claude","windows":[{"name":"5 hours","used":1}]}]}`, wantErr: "Codex"},
		{name: "invalid json", status: http.StatusOK, body: "not json", wantErr: "解析"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, err := readMagpieQuota(context.Background(), quotaRoute{BaseURL: srv.URL, Token: "k"})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("expected error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}

// 网关重定向时不能把 Bearer key 发给重定向后的主机。
func TestQuotaGetDoesNotFollowRedirect(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			leaked = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	_, status, err := quotaGet(context.Background(), redirector.URL, "secret-key")
	if err != nil {
		t.Fatalf("quotaGet: %v", err)
	}
	if status != http.StatusTemporaryRedirect {
		t.Errorf("expected redirect status to be returned as-is, got %d", status)
	}
	if leaked {
		t.Error("bearer key leaked to the redirect target")
	}
}

func TestIsMagpieGatewayChecksName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"name":"magpie","version":"v0.1.1795"}`)
	}))
	defer srv.Close()

	if !isMagpieGateway(context.Background(), srv.URL, "k") {
		t.Error("expected the gateway to be recognised as Magpie")
	}
	if isMagpieGateway(context.Background(), srv.URL, "other") {
		t.Error("a gateway rejecting the key must not be recognised as Magpie")
	}
}
