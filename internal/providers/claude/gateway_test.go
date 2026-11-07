package claude

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// setGatewayEnv 配置网关所需的环境变量，并隔离真实的 HOME 与 Claude 配置目录。
func setGatewayEnv(t *testing.T, base, token string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("ANTHROPIC_BASE_URL", base)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", token)
	t.Setenv("ANTHROPIC_API_KEY", "")
}

func useGatewayTestServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	orig := gatewayHTTPClient
	gatewayHTTPClient = srv.Client()
	t.Cleanup(func() { gatewayHTTPClient = orig })
}

func TestReadGatewayPlanLimits_Magpie(t *testing.T) {
	var infoAuth, cardsAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			infoAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
		case "/v1/magpie/quotas/cards":
			cardsAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"data":[
				{"provider":"codex","plan":"ignored","user":"nobody","windows":[{"name":"5 hours","used":99}]},
				{"provider":"claude","plan":"Max 20x","user":"alice","windows":[
					{"name":"5 hours","used":12.5,"resetsAt":"2026-10-18T12:00:00.123456Z"},
					{"name":"7 days","used":40},
					{"name":"1 hour","used":7}
				]}
			]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	limits, ok, err := readGatewayPlanLimits(context.Background())
	if err != nil {
		t.Fatalf("读取网关额度失败: %v", err)
	}
	if !ok {
		t.Fatal("应识别为 Magpie 网关")
	}
	if infoAuth != "Bearer gw-token" || cardsAuth != "Bearer gw-token" {
		t.Errorf("Authorization 头不符: info=%q cards=%q", infoAuth, cardsAuth)
	}
	if limits.PlanType != "Max 20x" {
		t.Errorf("方案名不符: %q", limits.PlanType)
	}
	if len(limits.Windows) != 3 {
		t.Fatalf("期望 3 个 Claude 窗口，实际 %+v", limits.Windows)
	}

	session := limits.Windows[0]
	wantReset := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if session.ID != "session" || session.Label != "5-hour limit" || session.UsedPercent != 12.5 {
		t.Errorf("5 小时窗口不符: %+v", session)
	}
	if session.ResetsAt == nil || !session.ResetsAt.Equal(wantReset) {
		t.Errorf("5 小时窗口重置时间不符: %v", session.ResetsAt)
	}

	weekly := limits.Windows[1]
	if weekly.ID != "weekly_all" || weekly.Label != "Weekly · all models" || weekly.UsedPercent != 40 || weekly.ResetsAt != nil {
		t.Errorf("每周窗口不符: %+v", weekly)
	}

	extra := limits.Windows[2]
	if extra.ID != "gateway" || extra.Label != "1 hour" || extra.UsedPercent != 7 {
		t.Errorf("额外窗口不符: %+v", extra)
	}
}

func TestReadGatewayPlanLimits_MultipleCardsPrefixUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"provider":"claude","plan":"Max 20x","user":"alice","windows":[{"name":"5 hours","used":10}]},
			{"provider":"claude","plan":"Pro","user":"bob","windows":[{"name":"7 days","used":20}]}
		]}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	limits, ok, err := readGatewayPlanLimits(context.Background())
	if err != nil || !ok {
		t.Fatalf("读取网关额度失败: ok=%v err=%v", ok, err)
	}
	if limits.PlanType != "Max 20x · Pro" {
		t.Errorf("方案名不符: %q", limits.PlanType)
	}
	if len(limits.Windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %+v", limits.Windows)
	}
	if limits.Windows[0].Label != "alice · 5-hour limit" {
		t.Errorf("首个窗口应带 user 前缀: %+v", limits.Windows[0])
	}
	if limits.Windows[1].Label != "bob · Weekly · all models" {
		t.Errorf("第二个窗口应带 user 前缀: %+v", limits.Windows[1])
	}
}

func TestReadGatewayPlanLimits_AcceptsClaudeTUIProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"provider":"claude","plan":"Max 20x","user":"alice@example.com","windows":[{"name":"5 hours","used":10}]},
			{"provider":"claude-tui","plan":null,"user":"API key …UifQ","alias":"lkliuss","windows":[{"name":"5 hours","used":55}]},
			{"provider":"codex","plan":"ignored","user":"nobody","windows":[{"name":"5 hours","used":99}]}
		]}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	limits, ok, err := readGatewayPlanLimits(context.Background())
	if err != nil || !ok {
		t.Fatalf("读取网关额度失败: ok=%v err=%v", ok, err)
	}
	if len(limits.Windows) != 2 {
		t.Fatalf("期望保留 claude 与 claude-tui 的 2 个窗口，实际 %+v", limits.Windows)
	}
	if limits.Windows[0].Label != "alice@example.com · 5-hour limit" || limits.Windows[0].UsedPercent != 10 {
		t.Errorf("claude 卡片窗口不符: %+v", limits.Windows[0])
	}
	if limits.Windows[0].Account != "alice@example.com" {
		t.Errorf("claude 卡片账号应为邮箱: %+v", limits.Windows[0])
	}
	// claude-tui 卡片带 alias 时，Label 前缀与账号都用 alias，而不是 API key 描述。
	if limits.Windows[1].Label != "lkliuss · 5-hour limit" || limits.Windows[1].UsedPercent != 55 {
		t.Errorf("claude-tui 卡片窗口不符: %+v", limits.Windows[1])
	}
	if limits.Windows[1].Account != "lkliuss" {
		t.Errorf("claude-tui 卡片账号应为 alias: %+v", limits.Windows[1])
	}
	// plan 为 null 的卡片不贡献方案名，只保留 claude 卡片的方案名。
	if limits.PlanType != "Max 20x" {
		t.Errorf("方案名不符: %q", limits.PlanType)
	}
}

func TestReadGatewayPlanLimits_AccountFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		// 没有 alias 的 claude-tui 卡片回退用 user，且空白 alias 视为未设置。
		_, _ = w.Write([]byte(`{"data":[
			{"provider":"claude-tui","user":"API key …UifQ","windows":[{"name":"5 hours","used":55}]},
			{"provider":"claude-tui","user":"API key …Zzz1","alias":"  ","windows":[{"name":"5 hours","used":66}]}
		]}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	limits, ok, err := readGatewayPlanLimits(context.Background())
	if err != nil || !ok {
		t.Fatalf("读取网关额度失败: ok=%v err=%v", ok, err)
	}
	if len(limits.Windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %+v", limits.Windows)
	}
	if limits.Windows[0].Account != "API key …UifQ" || limits.Windows[0].Label != "API key …UifQ · 5-hour limit" {
		t.Errorf("缺少 alias 的卡片应回退到 user: %+v", limits.Windows[0])
	}
	if limits.Windows[1].Account != "API key …Zzz1" || limits.Windows[1].Label != "API key …Zzz1 · 5-hour limit" {
		t.Errorf("空白 alias 的卡片应回退到 user: %+v", limits.Windows[1])
	}
}

func TestReadGatewayPlanLimits_NotMagpie(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"name 不匹配", http.StatusOK, `{"name":"other"}`},
		{"根地址 404", http.StatusNotFound, `not found`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cardsRequested atomic.Bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/" {
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
					return
				}
				cardsRequested.Store(true)
			}))
			defer srv.Close()
			setGatewayEnv(t, srv.URL, "gw-token")
			useGatewayTestServer(t, srv)

			limits, ok, err := readGatewayPlanLimits(context.Background())
			if err != nil || ok {
				t.Fatalf("非 Magpie 应返回 (false, nil): ok=%v err=%v", ok, err)
			}
			if len(limits.Windows) != 0 {
				t.Errorf("非 Magpie 不应返回窗口: %+v", limits)
			}
			if cardsRequested.Load() {
				t.Error("非 Magpie 不应请求额度接口")
			}
		})
	}
}

func TestReadGatewayPlanLimits_CardsForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	_, ok, err := readGatewayPlanLimits(context.Background())
	if !ok {
		t.Fatal("应识别为 Magpie 网关")
	}
	if err == nil || !strings.Contains(err.Error(), "无权读取额度") {
		t.Fatalf("403 应返回无权读取额度的错误: %v", err)
	}
}

func TestReadGatewayPlanLimits_SettingsEnvironment(t *testing.T) {
	const token = "settings-token"
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
		case "/v1/magpie/quotas/cards":
			_, _ = w.Write([]byte(`{"data":[{"provider":"claude","plan":"Pro","windows":[{"name":"5 hours","used":5}]}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	useGatewayTestServer(t, srv)

	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	// 带末尾 /v1/ 的地址用于覆盖 base 规范化；凭据只存在于 settings.json。
	settings := fmt.Sprintf(`{"env":{"ANTHROPIC_BASE_URL":%q,"ANTHROPIC_AUTH_TOKEN":%q}}`, srv.URL+"/v1/", token)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0600); err != nil {
		t.Fatal(err)
	}

	limits, ok, err := readGatewayPlanLimits(context.Background())
	if err != nil || !ok {
		t.Fatalf("从 settings.json 读取网关失败: ok=%v err=%v", ok, err)
	}
	if gotAuth != "Bearer "+token {
		t.Errorf("Authorization 头不符: %q", gotAuth)
	}
	if limits.PlanType != "Pro" || len(limits.Windows) != 1 || limits.Windows[0].UsedPercent != 5 {
		t.Errorf("结果不符: %+v", limits)
	}
}

func TestReadPlanLimits_GatewayFirst(t *testing.T) {
	var deny atomic.Bool
	deny.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		if deny.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"provider":"claude","plan":"Max 20x","windows":[{"name":"5 hours","used":15}]}]}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	// 本地 OAuth 凭据齐备时也应以网关为准，不再请求 OAuth 用量接口。
	var usageRequested atomic.Bool
	usageSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		usageRequested.Store(true)
		_, _ = w.Write([]byte(`{"five_hour":{"utilization":99}}`))
	}))
	defer usageSrv.Close()
	useTestServer(t, usageSrv)

	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	raw := `{"claudeAiOauth":{"accessToken":"local-token","subscriptionType":"max"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}

	ResetPlanLimitsCache()
	origKeychain := keychainLogin
	keychainLogin = func(string) (claudeLogin, bool) { return claudeLogin{}, false }
	t.Cleanup(func() {
		ResetPlanLimitsCache()
		keychainLogin = origKeychain
	})

	// 网关返回 403 时直接报错，不用本地凭据兜底，也不缓存空结果。
	if _, err := ReadPlanLimits(context.Background()); err == nil || !strings.Contains(err.Error(), "无权读取额度") {
		t.Fatalf("网关拒绝时应返回网关错误: %v", err)
	}

	deny.Store(false)
	limits, err := ReadPlanLimits(context.Background())
	if err != nil {
		t.Fatalf("重试应成功: %v", err)
	}
	if limits.PlanType != "Max 20x" || len(limits.Windows) != 1 || limits.Windows[0].UsedPercent != 15 {
		t.Errorf("网关额度不符: %+v", limits)
	}
	if usageRequested.Load() {
		t.Error("配置了 Magpie 网关时不应请求 OAuth 用量接口")
	}

	// 成功结果命中 60 秒缓存，服务端关闭后仍可读取。
	srv.Close()
	cached, err := ReadPlanLimits(context.Background())
	if err != nil || len(cached.Windows) != 1 || cached.PlanType != "Max 20x" {
		t.Errorf("网关额度未正确缓存: %+v, %v", cached, err)
	}
}

func TestRefreshGatewayQuotas_Magpie(t *testing.T) {
	var method, query, auth, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
		case "/v1/magpie/quotas/refresh":
			method = r.Method
			query = r.URL.RawQuery
			auth = r.Header.Get("Authorization")
			accept = r.Header.Get("Accept")
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	// 带 /v1 的地址用于覆盖网关根地址规范化。
	setGatewayEnv(t, srv.URL+"/v1", "gw-token")
	useGatewayTestServer(t, srv)

	ok, err := RefreshGatewayQuotas(context.Background())
	if err != nil || !ok {
		t.Fatalf("刷新网关额度失败: ok=%v err=%v", ok, err)
	}
	if method != http.MethodPost {
		t.Errorf("应以 POST 刷新: %q", method)
	}
	if query != "provider=claude" {
		t.Errorf("provider 参数不符: %q", query)
	}
	if auth != "Bearer gw-token" {
		t.Errorf("Authorization 头不符: %q", auth)
	}
	if accept != "application/json" {
		t.Errorf("Accept 头不符: %q", accept)
	}
}

func TestRefreshGatewayQuotas_NotMagpie(t *testing.T) {
	var refreshRequested atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"other"}`))
			return
		}
		refreshRequested.Store(true)
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	ok, err := RefreshGatewayQuotas(context.Background())
	if err != nil || ok {
		t.Fatalf("非 Magpie 应返回 (false, nil): ok=%v err=%v", ok, err)
	}
	if refreshRequested.Load() {
		t.Error("非 Magpie 不应请求刷新接口")
	}

	// 没有网关配置时同样按空操作处理。
	setGatewayEnv(t, "", "")
	ok, err = RefreshGatewayQuotas(context.Background())
	if err != nil || ok {
		t.Fatalf("无网关配置应返回 (false, nil): ok=%v err=%v", ok, err)
	}
}

func TestRefreshGatewayQuotas_Forbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	ok, err := RefreshGatewayQuotas(context.Background())
	if !ok {
		t.Fatal("应识别为 Magpie 网关")
	}
	if err == nil || !strings.Contains(err.Error(), "无权刷新额度") {
		t.Fatalf("403 应返回无权刷新额度的错误: %v", err)
	}
}

func TestReadGatewayPlanLimits_CardIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[
			{"provider":"claude","kind":"subscription","plan":"Max 20x","user":"alice@example.com",
			 "last":true,"lastServedAt":"2026-11-03T10:00:00.5Z","readAt":"2026-11-03T09:58:00Z",
			 "windows":[{"name":"5 hours","used":12.5}]},
			{"provider":"claude-tui","user":"API key …UifQ","alias":"lkliuss",
			 "windows":[{"name":"7 days","used":30}]}
		]}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)

	limits, ok, err := readGatewayPlanLimits(context.Background())
	if err != nil || !ok {
		t.Fatalf("读取网关额度失败: ok=%v err=%v", ok, err)
	}
	if limits.Source != "magpie" {
		t.Errorf("额度来源应为 magpie: %q", limits.Source)
	}
	if len(limits.Windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %+v", limits.Windows)
	}

	session := limits.Windows[0]
	if session.Provider != "claude" || session.User != "alice@example.com" || session.Kind != "subscription" {
		t.Errorf("卡片身份字段不符: %+v", session)
	}
	if !session.Last {
		t.Errorf("claude 卡片应标记为最近使用: %+v", session)
	}
	wantServed := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	if !session.LastServedAt.Equal(wantServed) {
		t.Errorf("最近应答时间不符: %v", session.LastServedAt)
	}
	wantRead := time.Date(2026, 11, 3, 9, 58, 0, 0, time.UTC)
	if !session.ReadAt.Equal(wantRead) {
		t.Errorf("读取时间不符: %v", session.ReadAt)
	}

	// 卡片缺失的字段留空，时间留零值表示未知。
	weekly := limits.Windows[1]
	if weekly.Provider != "claude-tui" || weekly.User != "API key …UifQ" || weekly.Kind != "" || weekly.Last {
		t.Errorf("claude-tui 卡片身份字段不符: %+v", weekly)
	}
	if !weekly.LastServedAt.IsZero() || !weekly.ReadAt.IsZero() {
		t.Errorf("缺失的时间字段应为零值: %+v", weekly)
	}
}

func TestReadGatewayPlanLimits_ServedFromKeptReport(t *testing.T) {
	// cards 端点只有卡片、没有 last 与 lastServedAt；kept 报告才带应答时间。
	const cardsBody = `{"data":[
		{"provider":"claude","kind":"subscription","plan":"Max 20x","user":"alice@example.com",
		 "windows":[{"name":"5 hours","used":12}]},
		{"provider":"claude","kind":"subscription","plan":"Pro","user":"bob@example.com",
		 "windows":[{"name":"5 hours","used":85}]}
	]}`
	const servedBody = `{"data":[
		{"provider":"claude","kind":"subscription","user":"alice@example.com","lastServedAt":"2026-11-03T09:00:00Z"},
		{"provider":"claude","kind":"subscription","user":"bob@example.com","lastServedAt":"2026-11-03T10:00:00Z"}
	]}`

	t.Run("kept 报告补齐最近应答线索", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/":
				_, _ = w.Write([]byte(`{"name":"magpie"}`))
			case "/v1/magpie/quotas/cards":
				_, _ = w.Write([]byte(cardsBody))
			case "/v1/magpie/quotas":
				if got := r.URL.Query().Get("kept"); got != "1" {
					t.Errorf("kept 报告请求应带 kept=1，实际 %q", got)
				}
				_, _ = w.Write([]byte(servedBody))
			default:
				http.NotFound(w, r)
			}
		}))
		defer srv.Close()
		setGatewayEnv(t, srv.URL, "gw-token")
		useGatewayTestServer(t, srv)

		limits, ok, err := readGatewayPlanLimits(context.Background())
		if err != nil || !ok {
			t.Fatalf("读取网关额度失败: ok=%v err=%v", ok, err)
		}
		if len(limits.Windows) != 2 {
			t.Fatalf("期望 2 个窗口，实际 %+v", limits.Windows)
		}

		wantAlice := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
		wantBob := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
		alice, bob := limits.Windows[0], limits.Windows[1]
		if !alice.LastServedAt.Equal(wantAlice) || alice.Last {
			t.Errorf("alice 应拿到自己的应答时间且不标最近使用: %+v", alice)
		}
		if !bob.LastServedAt.Equal(wantBob) || !bob.Last {
			t.Errorf("bob 应拿到更新的应答时间并标最近使用: %+v", bob)
		}
	})

	failures := []struct {
		name   string
		status int
		body   string
	}{
		{"served 返回 500", http.StatusInternalServerError, `{"error":"boom"}`},
		{"served 返回异常数据", http.StatusOK, `{"data":{}}`},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/":
					_, _ = w.Write([]byte(`{"name":"magpie"}`))
				case "/v1/magpie/quotas/cards":
					_, _ = w.Write([]byte(cardsBody))
				case "/v1/magpie/quotas":
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			setGatewayEnv(t, srv.URL, "gw-token")
			useGatewayTestServer(t, srv)

			limits, ok, err := readGatewayPlanLimits(context.Background())
			if err != nil || !ok {
				t.Fatalf("served 失败不应影响卡片读数: ok=%v err=%v", ok, err)
			}
			if len(limits.Windows) != 2 {
				t.Fatalf("期望 2 个窗口，实际 %+v", limits.Windows)
			}
			for _, w := range limits.Windows {
				if !w.LastServedAt.IsZero() || w.Last {
					t.Errorf("served 不可用时不应有最近应答线索: %+v", w)
				}
			}
		})
	}
}

func TestGatewayEndpoint_Magpie(t *testing.T) {
	var infoAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			infoAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"name":"magpie"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	// 带末尾 /v1/ 的地址用于覆盖网关根地址规范化。
	setGatewayEnv(t, srv.URL+"/v1/", "gw-token")
	useGatewayTestServer(t, srv)
	resetGatewayProbeCache()

	base, token, ok := GatewayEndpoint(context.Background())
	if !ok {
		t.Fatal("应识别为 Magpie 网关")
	}
	if base != srv.URL {
		t.Errorf("网关根地址不符: %q", base)
	}
	if token != "gw-token" {
		t.Errorf("网关 key 不符: %q", token)
	}
	if infoAuth != "Bearer gw-token" {
		t.Errorf("Authorization 头不符: %q", infoAuth)
	}
}

func TestGatewayEndpoint_NotMagpie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"name":"other"}`))
	}))
	defer srv.Close()
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)
	resetGatewayProbeCache()

	if base, token, ok := GatewayEndpoint(context.Background()); ok || base != "" || token != "" {
		t.Errorf("非 Magpie 应返回空值: base=%q token=%q ok=%v", base, token, ok)
	}

	// 没有网关配置时同样返回 ok=false。
	setGatewayEnv(t, "", "")
	if base, token, ok := GatewayEndpoint(context.Background()); ok || base != "" || token != "" {
		t.Errorf("无网关配置应返回空值: base=%q token=%q ok=%v", base, token, ok)
	}
}

// countingGatewayServer 统计根地址探测次数的 httptest 服务，info 是根地址的回应体。
func countingGatewayServer(t *testing.T, info string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		_, _ = w.Write([]byte(info))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// fixGatewayNow 把网关探测用的时间固定为 now 并给出推进函数，测试结束恢复。
func fixGatewayNow(t *testing.T) func(time.Duration) {
	t.Helper()
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	orig := gatewayNow
	gatewayNow = func() time.Time { return now }
	t.Cleanup(func() {
		gatewayNow = orig
		resetGatewayProbeCache()
	})
	return func(d time.Duration) { now = now.Add(d) }
}

func TestGatewayEndpoint_ProbesOnceWithinTTL(t *testing.T) {
	srv, hits := countingGatewayServer(t, `{"name":"magpie"}`)
	setGatewayEnv(t, srv.URL, "gw-token")
	useGatewayTestServer(t, srv)
	resetGatewayProbeCache()
	advance := fixGatewayNow(t)

	for i := 0; i < 2; i++ {
		base, token, ok := GatewayEndpoint(context.Background())
		if !ok || base != srv.URL || token != "gw-token" {
			t.Fatalf("第 %d 次 GatewayEndpoint = (%q, %q, %v)，想要 (%q, %q, true)", i+1, base, token, ok, srv.URL, "gw-token")
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("60 秒内两次调用应只探测一次，实际探测 %d 次", got)
	}

	// 未超过 60 秒仍命中缓存。
	advance(gatewayProbeTTL - time.Second)
	if _, _, ok := GatewayEndpoint(context.Background()); !ok {
		t.Fatal("缓存期内应仍识别为 Magpie")
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("缓存期内不应重新探测，实际探测 %d 次", got)
	}

	// 超过 60 秒后重新探测。
	advance(2 * time.Second)
	if _, _, ok := GatewayEndpoint(context.Background()); !ok {
		t.Fatal("过期后重新探测应仍识别为 Magpie")
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("超过 60 秒应重新探测，实际探测 %d 次", got)
	}
}

func TestGatewayEndpoint_AddressChangeReprobes(t *testing.T) {
	first, firstHits := countingGatewayServer(t, `{"name":"magpie"}`)
	second, secondHits := countingGatewayServer(t, `{"name":"other"}`)
	useGatewayTestServer(t, first)
	resetGatewayProbeCache()
	advance := fixGatewayNow(t)

	setGatewayEnv(t, first.URL, "gw-token")
	if _, _, ok := GatewayEndpoint(context.Background()); !ok {
		t.Fatal("第一个地址应识别为 Magpie")
	}

	// 换网关地址后立即重新探测，不受 60 秒缓存影响。
	advance(time.Second)
	useGatewayTestServer(t, second)
	setGatewayEnv(t, second.URL, "gw-token")
	if base, _, ok := GatewayEndpoint(context.Background()); ok || base != "" {
		t.Errorf("换地址后应按新地址探测: base=%q ok=%v", base, ok)
	}
	if firstHits.Load() != 1 || secondHits.Load() != 1 {
		t.Errorf("换地址应立即重新探测，旧=%d 新=%d", firstHits.Load(), secondHits.Load())
	}

	// 换 key 同样重新探测。
	advance(time.Second)
	useGatewayTestServer(t, first)
	setGatewayEnv(t, first.URL, "gw-token-2")
	if _, _, ok := GatewayEndpoint(context.Background()); !ok {
		t.Fatal("换 key 后应重新探测并识别为 Magpie")
	}
	if firstHits.Load() != 2 {
		t.Errorf("换 key 应重新探测，实际探测 %d 次", firstHits.Load())
	}
}

func TestGatewayEndpoint_NetworkErrorNotCached(t *testing.T) {
	srv, hits := countingGatewayServer(t, `{"name":"magpie"}`)
	setGatewayEnv(t, srv.URL, "gw-token")
	resetGatewayProbeCache()
	fixGatewayNow(t)

	// 第一次探测网络失败：不缓存，随后立即重试成功。
	orig := gatewayHTTPClient
	var fail atomic.Bool
	fail.Store(true)
	gatewayHTTPClient = &http.Client{Transport: &gatewayFailTransport{fail: &fail, base: srv.Client().Transport}}
	t.Cleanup(func() { gatewayHTTPClient = orig })

	if base, _, ok := GatewayEndpoint(context.Background()); ok || base != "" {
		t.Errorf("探测失败时应返回空值: base=%q ok=%v", base, ok)
	}
	if hits.Load() != 0 {
		t.Errorf("首次探测网络失败不应到达服务端，实际 %d 次", hits.Load())
	}

	if _, _, ok := GatewayEndpoint(context.Background()); !ok {
		t.Fatal("失败结果不应缓存，第二次调用应重试成功")
	}
	if hits.Load() != 1 {
		t.Errorf("重试应探测一次，实际 %d 次", hits.Load())
	}
}

// gatewayFailTransport 在 fail 为 true 时直接返回网络错误（并清掉标志）。
type gatewayFailTransport struct {
	fail *atomic.Bool
	base http.RoundTripper
}

func (g *gatewayFailTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if g.fail.Swap(false) {
		return nil, fmt.Errorf("模拟网络错误")
	}
	base := g.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}
