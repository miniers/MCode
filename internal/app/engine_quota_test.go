package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mcode/internal/config"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// engineQuotaTestGateway 返回一个假的 Magpie 网关：额度读取给出两张 claude 卡片
// （两个账号），刷新请求记下 query，便于断言引擎会话也能刷新在用账号。
func engineQuotaTestGateway(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var refreshes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/magpie/quotas":
			if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
				t.Errorf("Authorization = %q，想要 Bearer gw-key", got)
			}
			io.WriteString(w, `{"object":"list","data":[
				{"provider":"claude","user":"a@example.com","windows":[{"name":"5 hours","used":10}]},
				{"provider":"claude","user":"b@example.com","windows":[
					{"name":"5 hours","used":20},{"name":"7 days","used":30}]}]}`)
		case "/v1/magpie/quotas/refresh":
			if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
				t.Errorf("刷新 Authorization = %q，想要 Bearer gw-key", got)
			}
			mu.Lock()
			refreshes = append(refreshes, r.URL.Query().Encode())
			mu.Unlock()
			io.WriteString(w, `{"object":"list","data":[]}`)
		default:
			t.Errorf("请求路径 = %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), refreshes...)
	}
}

// engineQuotaTestClient 构造一个带配置与密钥的 Client，并装入会话跟踪器，
// trace 里记下线程 th-1 实际由 claude 的 b@example.com 应答。
func engineQuotaTestClient(t *testing.T, endpoints []config.CustomEndpointConfig, keys map[string]string) (*Client, *sessionAccountTracker) {
	t.Helper()
	dataDir := t.TempDir()
	if err := config.Save(dataDir, magpieTestConfig(endpoints...)); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	client := NewClient(nil)
	client.SetProviderStore(dataDir, magpieTestSecrets(t, keys))
	tracker := newTestSessionAccountTracker(t)
	useQuotaTracker(client, tracker)
	tracker.ingest(magpie.TraceSnapshot{Seq: 1, Routes: []magpie.TraceRoute{
		sessionTraceRoute(7, "th-1", "claude", "b@example.com", "account"),
	}})
	return client, tracker
}

// TestEngineQuotaKey 校验引擎会话的额度 key 形状：mcode 带端点前缀，magpie 与
// magpie2…magpie4 直接用 provider 自身命名，其余都不算。
func TestEngineQuotaKey(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want bool
	}{
		{"mcode:magpie-jp", true},
		{"mcode:magpie", true},
		{"mcode:", false},
		{"mcode", false},
		{"magpie", true},
		{"magpie2", true},
		{"magpie4", true},
		{"magpie1", false},
		{"magpie5", false},
		{"pi:magpie-jp", false},
		{"agy", false},
	} {
		if got := engineQuotaKey(tc.key); got != tc.want {
			t.Errorf("engineQuotaKey(%q) = %v，想要 %v", tc.key, got, tc.want)
		}
	}
}

// TestProviderQuotaEngineMcode 校验 mcode 线程（模型串第一段是端点 provider id）能读到
// Magpie 额度，并按 trace 只给在用账号标 Active。
func TestProviderQuotaEngineMcode(t *testing.T) {
	srv, _ := engineQuotaTestGateway(t)
	client, _ := engineQuotaTestClient(t, []config.CustomEndpointConfig{
		{ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true},
	}, map[string]string{"jp": "gw-key"})

	ctx := mcodeui.WithQuotaThread(context.Background(), "th-1")
	windows, err := client.ProviderQuota(ctx, "mcode:magpie-jp")
	if err != nil {
		t.Fatalf("ProviderQuota 失败: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("窗口数 = %d，想要 3: %+v", len(windows), windows)
	}
	if windows[0].Active {
		t.Errorf("a@example.com 不是本会话应答的账号，不应 Active: %+v", windows[0])
	}
	for _, w := range windows[1:] {
		if !w.Active {
			t.Errorf("b@example.com 是本会话应答的账号，应 Active: %+v", w)
		}
	}
	for _, w := range windows {
		if w.Source != codex.QuotaSourceMagpie || w.Provider != "claude" || w.User == "" {
			t.Errorf("卡片缺少 Magpie 来源或 provider/user: %+v", w)
		}
	}

	// 找不到端点、端点没有 key 或不是 Magpie 会话时保持未实现。
	for _, key := range []string{"mcode:nope", "mcode"} {
		if _, err := client.ProviderQuota(ctx, key); !errors.Is(err, mcodeui.ErrNotImplemented) {
			t.Errorf("ProviderQuota(%q) 错误 = %v，想要 ErrNotImplemented", key, err)
		}
	}
	if _, err := client.ProviderQuota(ctx, "agy"); !errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Errorf("非 Magpie provider 的错误 = %v，想要 ErrNotImplemented", err)
	}
}

// TestProviderQuotaEngineMagpieKind 校验 magpie2 这类以端点自身 provider kind 命名的
// 引擎会话能读到额度，密钥按要求从凭据存储读取。
func TestProviderQuotaEngineMagpieKind(t *testing.T) {
	srv, _ := engineQuotaTestGateway(t)
	endpoint := config.CustomEndpointConfig{
		ID: "magpie2", Name: "Magpie 2", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
	}
	client, _ := engineQuotaTestClient(t, []config.CustomEndpointConfig{endpoint}, map[string]string{"magpie2": "gw-key"})

	ctx := mcodeui.WithQuotaThread(context.Background(), "th-1")
	windows, err := client.ProviderQuota(ctx, "magpie2")
	if err != nil {
		t.Fatalf("ProviderQuota(magpie2) 失败: %v", err)
	}
	if len(windows) != 3 || !windows[1].Active || !windows[2].Active {
		t.Fatalf("magpie2 的额度窗口不对: %+v", windows)
	}

	// 没配 key 的端点读不到额度。
	noKey, _ := engineQuotaTestClient(t, []config.CustomEndpointConfig{endpoint}, nil)
	if _, err := noKey.ProviderQuota(ctx, "magpie2"); !errors.Is(err, mcodeui.ErrNotImplemented) {
		t.Errorf("没有 key 时的错误 = %v，想要 ErrNotImplemented", err)
	}
}

// TestRefreshEngineQuota 校验引擎会话的两条刷新路径：手动刷新按端点整份重读，
// 回合结束的刷新只重读本会话实际应答的账号，都不会用到 pi 的会话 ID。
func TestRefreshEngineQuota(t *testing.T) {
	srv, refreshes := engineQuotaTestGateway(t)
	client, _ := engineQuotaTestClient(t, []config.CustomEndpointConfig{
		{ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true},
	}, map[string]string{"jp": "gw-key"})

	ctx := context.Background()
	if err := client.RefreshProviderQuota(ctx, "mcode:magpie-jp"); err != nil {
		t.Fatalf("ProviderQuota 刷新失败: %v", err)
	}
	got := refreshes()
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("手动刷新 = %v，想要一次不带账号的重读", got)
	}

	if err := client.RefreshThreadQuota(ctx, "th-1", "mcode:magpie-jp"); err != nil {
		t.Fatalf("RefreshThreadQuota 失败: %v", err)
	}
	got = refreshes()
	if len(got) != 2 {
		t.Fatalf("刷新请求数 = %d，想要 2: %v", len(got), got)
	}
	params := got[1]
	if !strings.Contains(params, "provider=claude") || !strings.Contains(params, "user=b%40example.com") {
		t.Errorf("回合刷新 query = %q，想要只重读 claude 的 b@example.com", params)
	}
}
