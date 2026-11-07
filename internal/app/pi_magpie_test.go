package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/providers/codex"
	"mcode/internal/providers/magpie"
	mcodeui "mcode/internal/ui"
)

// magpieTestConfig 构造一份只含 Magpie 自定义端点的配置。
func magpieTestConfig(endpoints ...config.CustomEndpointConfig) config.Config {
	cfg := config.Defaults()
	cfg.Providers.Custom = endpoints
	return cfg
}

// magpieTestSecrets 返回带指定端点密钥的内存凭据存储。
func magpieTestSecrets(t *testing.T, keys map[string]string) config.SecretStore {
	t.Helper()
	secrets := config.NewMemorySecretStore()
	for endpointID, key := range keys {
		if err := secrets.Set(config.CustomEndpointSecretAccount(endpointID), key); err != nil {
			t.Fatalf("写入 %s 的密钥失败: %v", endpointID, err)
		}
	}
	return secrets
}

// TestPiMagpieTargets 校验 providerID 的分配顺序与跳过规则：
// 只在启用的 Magpie 端点上分配，同名端点依次加后缀，没配 key 的也分配但不注入。
func TestPiMagpieTargets(t *testing.T) {
	cfg := magpieTestConfig(
		config.CustomEndpointConfig{ID: "off", Name: "Magpie Off", Enabled: false, Magpie: true},
		config.CustomEndpointConfig{ID: "plain", Name: "Plain", BaseURL: "http://plain.example/v1", Enabled: true},
		config.CustomEndpointConfig{ID: "nokey", Name: "Magpie", Enabled: true, Magpie: true},
		config.CustomEndpointConfig{
			ID: "jp", Name: "Magpie JP", BaseURL: "http://jp.example/v1/", Enabled: true, Magpie: true,
			PiModels: []string{"cline/gpt-x"},
		},
		config.CustomEndpointConfig{
			ID: "jp2", Name: "Magpie JP", BaseURL: "http://jp2.example/v1", Enabled: true, Magpie: true,
		},
	)
	secrets := magpieTestSecrets(t, map[string]string{"jp": "gw-key"})

	targets := piMagpieTargets(cfg, secrets)
	if len(targets) != 3 {
		t.Fatalf("端点数 = %d，想要 3（只取启用的 Magpie 端点）", len(targets))
	}

	want := []struct {
		endpointID string
		providerID string
		baseURL    string
		apiKey     string
		injectable bool
	}{
		{"nokey", "magpie-mcode", config.DefaultMagpieBaseURL, "", false},
		{"jp", "magpie-jp", "http://jp.example/v1", "gw-key", true},
		{"jp2", "magpie-jp-2", "http://jp2.example/v1", "", false},
	}
	for i, w := range want {
		got := targets[i]
		if got.endpoint.ID != w.endpointID {
			t.Errorf("第 %d 个端点 = %q，想要 %q", i, got.endpoint.ID, w.endpointID)
		}
		if got.providerID != w.providerID {
			t.Errorf("端点 %s 的 providerID = %q，想要 %q", w.endpointID, got.providerID, w.providerID)
		}
		if got.baseURL != w.baseURL {
			t.Errorf("端点 %s 的 baseURL = %q，想要 %q", w.endpointID, got.baseURL, w.baseURL)
		}
		if got.apiKey != w.apiKey {
			t.Errorf("端点 %s 的 apiKey = %q，想要 %q", w.endpointID, got.apiKey, w.apiKey)
		}
		if got.injectable() != w.injectable {
			t.Errorf("端点 %s 的 injectable = %v，想要 %v", w.endpointID, got.injectable(), w.injectable)
		}
	}
}

// TestBuildPiMagpieEnv 校验注入值：只挑勾选的模型、网关没给的 ID 补默认值、
// 名字换成 ShortName；没有可注入端点时返回空串。
func TestBuildPiMagpieEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("请求路径 = %q，想要 /v1/models", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
			t.Errorf("Authorization = %q，想要 Bearer gw-key", got)
		}
		io.WriteString(w, `{"data":[
			{"id":"cline/gpt-x","display_name":"GPT X · cline"},
			{"id":"cline/gpt-y","display_name":"GPT Y · cline"}]}`)
	}))
	defer srv.Close()

	cfg := magpieTestConfig(config.CustomEndpointConfig{
		ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
		PiModels: []string{"cline/gpt-x", "acme/missing-model"},
	})
	secrets := magpieTestSecrets(t, map[string]string{"jp": "gw-key"})

	raw := buildPiMagpieEnv(context.Background(), config.Normalize(cfg), secrets)
	if raw == "" {
		t.Fatal("注入值为空，想要一个 Magpie provider")
	}
	var providers []magpie.PiProvider
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		t.Fatalf("解析注入值失败: %v，原文 %q", err, raw)
	}
	if len(providers) != 1 {
		t.Fatalf("provider 数 = %d，想要 1", len(providers))
	}
	provider := providers[0]
	if provider.Provider != "magpie-jp" || provider.Endpoint != "jp" || provider.APIKey != "gw-key" {
		t.Errorf("provider = %+v，想要 magpie-jp / jp / gw-key", provider)
	}
	if provider.BaseURL != srv.URL+"/v1" {
		t.Errorf("baseUrl = %q，想要 %q", provider.BaseURL, srv.URL+"/v1")
	}
	if len(provider.Models) != 2 {
		t.Fatalf("模型数 = %d，想要 2（只取勾选的）", len(provider.Models))
	}
	first := provider.Models[0]
	if first.ID != "cline/gpt-x" || first.Name != "GPT X" {
		t.Errorf("第一个模型 = %q/%q，想要 cline/gpt-x/GPT X", first.ID, first.Name)
	}
	second := provider.Models[1]
	if second.ID != "acme/missing-model" {
		t.Errorf("第二个模型 ID = %q，想要 acme/missing-model", second.ID)
	}
	if second.Name != "missing-model" {
		t.Errorf("第二个模型 Name = %q，想要 missing-model", second.Name)
	}
	if !second.Reasoning || strings.Join(second.Input, ",") != "text,image" ||
		second.ContextWindow != 200000 || second.MaxTokens != 32000 {
		t.Errorf("补默认值的模型元数据不对: %+v", second)
	}

	// 网关拉取失败时同样按勾选补默认值，不至于一个模型都没有。
	failed := magpieTestConfig(config.CustomEndpointConfig{
		ID: "down", Name: "Magpie Down", BaseURL: "http://127.0.0.1:1/v1", Enabled: true, Magpie: true,
		PiModels: []string{"cline/gpt-x"},
	})
	raw = buildPiMagpieEnv(context.Background(), config.Normalize(failed), magpieTestSecrets(t, map[string]string{"down": "gw-key"}))
	if raw == "" {
		t.Fatal("拉取失败时注入值为空，想要按勾选补默认值")
	}
	providers = nil
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		t.Fatalf("解析注入值失败: %v", err)
	}
	if len(providers) != 1 || len(providers[0].Models) != 1 || providers[0].Models[0].Name != "gpt-x" {
		t.Fatalf("拉取失败时的注入值不对: %+v", providers)
	}

	// 没有 key 或没勾模型：不注入。
	cases := []struct {
		name     string
		endpoint config.CustomEndpointConfig
		keys     map[string]string
	}{
		{name: "没有 key", endpoint: config.CustomEndpointConfig{
			ID: "nokey", Name: "Magpie", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
			PiModels: []string{"cline/gpt-x"},
		}},
		{name: "没勾模型", endpoint: config.CustomEndpointConfig{
			ID: "empty", Name: "Magpie", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
		}, keys: map[string]string{"empty": "gw-key"}},
	}
	for _, tc := range cases {
		if got := buildPiMagpieEnv(context.Background(), magpieTestConfig(tc.endpoint), magpieTestSecrets(t, tc.keys)); got != "" {
			t.Errorf("%s 时注入值 = %q，想要空串", tc.name, got)
		}
	}
}

// TestProviderQuotaPiMagpie 校验 pi:<provider id> 的额度读取：能映射到有 key 的
// Magpie 端点时按窗口转换并写下来源与卡片账号；映射不到按未实现处理。
func TestProviderQuotaPiMagpie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/magpie/quotas" {
			t.Errorf("请求路径 = %q，想要 /v1/magpie/quotas", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer gw-key" {
			t.Errorf("Authorization = %q，想要 Bearer gw-key", got)
		}
		io.WriteString(w, `{"data":[
			{"provider":"cline","windows":[
				{"name":"5 hours","used":42.5,"resetsAt":"2026-05-01T10:00:00Z"},
				{"name":"week","used":10,"resetsAt":""}]},
			{"provider":"other","windows":[{"name":"5 hours","used":1}]}]}`)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfg := magpieTestConfig(config.CustomEndpointConfig{
		ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
		PiModels: []string{"cline/gpt-x"},
	})
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	secrets := magpieTestSecrets(t, map[string]string{"jp": "gw-key"})

	client := NewClient(nil)
	client.SetProviderStore(dataDir, secrets)
	useQuotaTracker(client, newTestSessionAccountTracker(t))

	windows, err := client.ProviderQuota(context.Background(), "pi:magpie-jp")
	if err != nil {
		t.Fatalf("ProviderQuota 失败: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("窗口数 = %d，想要 3: %+v", len(windows), windows)
	}
	// web 额度条用到的端点展示名也在同一次读取里拿（否则 web 只能显示 provider 图标）。
	reading, err := client.piMagpieQuotaReadingFor(context.Background(), "magpie-jp")
	if err != nil || !reading.Found || reading.Name != "Magpie JP" || len(reading.Windows) != 3 {
		t.Fatalf("piMagpieQuotaReadingFor = %+v, %v；想要 Found、Name=Magpie JP、3 个窗口", reading, err)
	}
	first := windows[0]
	if first.ID != "cline/5 hours" || first.Period != "5h" || first.Label != "5 hours" ||
		first.UsedPercent != 42.5 || first.Account != "cline" {
		t.Errorf("第一个窗口 = %+v，想要 cline/5 hours", first)
	}
	if first.Source != codex.QuotaSourceMagpie || first.Provider != "cline" {
		t.Errorf("第一个窗口的来源 = %q/%q，想要 magpie/cline", first.Source, first.Provider)
	}
	if first.ResetsAt.IsZero() {
		t.Error("第一个窗口的 ResetsAt 为零值，想要解析出的时间")
	}
	// 没有会话路由时不能按 provider 给任何卡片标「在用」。
	for _, w := range windows {
		if w.Active {
			t.Errorf("没有会话路由时不应标 Active: %+v", w)
		}
	}
	if windows[1].Period != "7d" {
		t.Errorf("第二个窗口 = %+v，想要 7d", windows[1])
	}

	// provider 对不上（pi 自带的 provider）或没有 key 的端点都按未实现处理。
	for _, provider := range []string{"pi:magpie-nope", "pi:magpie-mcode", "pi:"} {
		if _, err := client.ProviderQuota(context.Background(), provider); !errors.Is(err, mcodeui.ErrNotImplemented) {
			t.Errorf("provider %q 的报错 = %v，想要 ErrNotImplemented", provider, err)
		}
	}
}

// TestProviderQuotaPiMagpieActiveFollowsSession 校验「在用」只标在本会话实际应答的
// 账号上：两张同 provider、不同账号的卡片里只有匹配 trace 路由的那张 Active；
// 会话没有路由时都不标，不按 provider 猜。
func TestProviderQuotaPiMagpieActiveFollowsSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/magpie/quotas" {
			t.Errorf("请求路径 = %q，想要 /v1/magpie/quotas", r.URL.Path)
		}
		io.WriteString(w, `{"data":[
			{"provider":"claude","user":"a@example.com","kind":"account","plan":"Max",
			 "readAt":"2026-11-04T09:00:00Z","lastServedAt":"2026-11-04T08:59:00Z","last":true,
			 "windows":[{"name":"5 hours","used":10}]},
			{"provider":"claude","user":"b@example.com","kind":"account","plan":"Pro",
			 "readAt":"2026-11-04T09:01:00Z",
			 "windows":[{"name":"5 hours","used":20}]}]}`)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfg := magpieTestConfig(config.CustomEndpointConfig{
		ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
	})
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}

	client := NewClient(nil)
	client.SetProviderStore(dataDir, magpieTestSecrets(t, map[string]string{"jp": "gw-key"}))
	client.SetAgySessionLookup(func(_ context.Context, threadID string) string {
		if threadID == "th-1" {
			return "pi-session-uuid"
		}
		return ""
	})
	tracker := newTestSessionAccountTracker(t)
	useQuotaTracker(client, tracker)
	tracker.ingest(magpie.TraceSnapshot{Seq: 1, Routes: []magpie.TraceRoute{
		sessionTraceRoute(7, "pi-session-uuid", "claude", "b@example.com", "account"),
	}})

	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "th-1"), "pi:magpie-jp")
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
	if windows[0].Plan != "Max" || !windows[0].Last || windows[0].ReadAt.IsZero() || windows[0].LastServedAt.IsZero() {
		t.Errorf("卡片字段映射不完整: %+v", windows[0])
	}

	// 会话解析不出 provider 会话 ID 或 trace 里没有该会话时，哪张卡都不标。
	none, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "th-2"), "pi:magpie-jp")
	if err != nil {
		t.Fatalf("ProviderQuota（无路由）失败: %v", err)
	}
	for _, w := range none {
		if w.Active {
			t.Errorf("没有路由时不应标 Active: %+v", w)
		}
	}
}

// TestProviderQuotaPiMagpieActiveFromUsage 校验 trace 读不到（如远程端点的 403）时，
// 用 usage/requests 兜底把本会话实际应答的账号标成 Active。
func TestProviderQuotaPiMagpieActiveFromUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/magpie/quotas" {
			t.Errorf("请求路径 = %q，想要 /v1/magpie/quotas", r.URL.Path)
		}
		io.WriteString(w, `{"data":[
			{"provider":"claude","user":"a@example.com","kind":"account","windows":[{"name":"5 hours","used":10}]},
			{"provider":"claude","user":"b@example.com","kind":"account","windows":[{"name":"5 hours","used":20}]}]}`)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfg := magpieTestConfig(config.CustomEndpointConfig{
		ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
	})
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	client := NewClient(nil)
	client.SetProviderStore(dataDir, magpieTestSecrets(t, map[string]string{"jp": "gw-key"}))
	client.SetAgySessionLookup(func(_ context.Context, threadID string) string {
		if threadID == "th-1" {
			return "pi-session-uuid"
		}
		return ""
	})

	tracker := newTestSessionAccountTracker(t)
	tracker.readTrace = func(context.Context, string, string, int64, bool) (magpie.TraceSnapshot, error) {
		return magpie.TraceSnapshot{}, fmt.Errorf("%w: 403", magpie.ErrTraceForbidden)
	}
	tracker.readUsage = func(_ context.Context, baseURL, apiKey, agent string, limit int) ([]magpie.UsageRow, error) {
		if agent != "pi" {
			t.Errorf("agent = %q，想要 pi", agent)
		}
		return []magpie.UsageRow{{
			T: time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC), Agent: "pi", Provider: "claude",
			ProviderAccount: "b@example.com", Model: "test-model", Session: "pi-session-uuid",
		}}, nil
	}
	useQuotaTracker(client, tracker)

	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "th-1"), "pi:magpie-jp")
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
		t.Errorf("b@example.com 由 usage 兜底命中，应 Active: %+v", windows[1])
	}
}

// TestRefreshThreadQuota 校验按会话刷新额度：只有 pi 提供方、能映射到有 key 的端点、
// 会话查到 trace 路由且路由带账号时才发一次带 provider 与 user 的 POST；30 秒内不重复；
// 未支持的提供方、无路由、无会话、端点无 key 都不发请求。
func TestRefreshThreadQuota(t *testing.T) {
	queries := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/magpie/quotas/refresh" {
			t.Errorf("请求 = %s %s，想要 POST /v1/magpie/quotas/refresh", r.Method, r.URL.Path)
		}
		queries <- r.URL.Query().Get("provider") + "|" + r.URL.Query().Get("user")
		io.WriteString(w, `{"object":"list","data":[]}`)
	}))
	defer srv.Close()

	dataDir := t.TempDir()
	cfg := magpieTestConfig(config.CustomEndpointConfig{
		ID: "jp", Name: "Magpie JP", BaseURL: srv.URL + "/v1", Enabled: true, Magpie: true,
	})
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	client := NewClient(nil)
	client.SetProviderStore(dataDir, magpieTestSecrets(t, map[string]string{"jp": "gw-key"}))
	client.SetAgySessionLookup(func(_ context.Context, threadID string) string {
		if threadID == "th-1" {
			return "pi-session-uuid"
		}
		return ""
	})
	tracker := newTestSessionAccountTracker(t)
	now := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	tracker.now = func() time.Time { return now }
	useQuotaTracker(client, tracker)
	tracker.ingest(magpie.TraceSnapshot{Seq: 1, Routes: []magpie.TraceRoute{
		sessionTraceRoute(7, "pi-session-uuid", "claude", "b@example.com", "account"),
	}})

	ctx := context.Background()
	if err := client.RefreshThreadQuota(ctx, "th-1", "pi:magpie-jp"); err != nil {
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
	if err := client.RefreshThreadQuota(ctx, "th-1", "pi:magpie-jp"); err != nil {
		t.Fatalf("第二次 RefreshThreadQuota 失败: %v", err)
	}
	if len(queries) != 0 {
		t.Errorf("30 秒内不应重复刷新，实际收到 %d 个请求", len(queries))
	}

	for _, tc := range []struct {
		name     string
		threadID string
		provider string
	}{
		{"未支持的提供方", "th-1", "gemini"},
		{"会话查不到路由", "th-2", "pi:magpie-jp"},
		{"会话为空", "", "pi:magpie-jp"},
		{"端点映射不到", "th-1", "pi:magpie-nope"},
		{"provider 只有前缀", "th-1", "pi:"},
	} {
		if err := client.RefreshThreadQuota(ctx, tc.threadID, tc.provider); err != nil {
			t.Errorf("%s: RefreshThreadQuota 返回错误: %v", tc.name, err)
		}
		if len(queries) != 0 {
			t.Errorf("%s: 不应刷新，实际收到 %d 个请求", tc.name, len(queries))
		}
	}

	// 过了去重窗口后允许再次刷新。
	now = now.Add(quotaRefreshDedupeWindow)
	if err := client.RefreshThreadQuota(ctx, "th-1", "pi:magpie-jp"); err != nil {
		t.Fatalf("窗口后 RefreshThreadQuota 失败: %v", err)
	}
	if len(queries) != 1 {
		t.Errorf("过了窗口应再次刷新，实际收到 %d 个请求", len(queries))
	}
}
