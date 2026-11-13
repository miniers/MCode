package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadPlanLimitsThirdPartyAPI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	setPlanLimitsEnv(t, []string{"ANTHROPIC_API_KEY=example"})
	ResetPlanLimitsCache()
	t.Cleanup(ResetPlanLimitsCache)
	_, err := ReadPlanLimits(context.Background())
	if err == nil || err.Error() != "claude: 第三方 API 不支持额度查询" {
		t.Fatalf("第三方 API 额度错误 = %v", err)
	}
	if !cachedPlanLimitsAt.IsZero() {
		t.Fatal("第三方 API 的错误不应缓存")
	}
}

func TestParsePlanLimits_LimitsArray(t *testing.T) {
	raw := []byte(`{
		"limits": [
			{"kind": "session", "percent": 12.5, "resets_at": "2026-10-18T12:00:00.123456Z"},
			{"kind": "weekly_all", "percent": 40, "resets_at": "2026-10-24T12:00:00.000000Z"},
			{"kind": "weekly_model", "percent": 5, "group": "sonnet", "scope": {"model": {"display_name": "Sonnet 4.5"}}, "resets_at": "2026-10-24T12:00:00Z"}
		],
		"extra_usage": {"is_enabled": true, "utilization": 3.5}
	}`)

	limits, err := ParsePlanLimits(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if len(limits.Windows) != 4 {
		t.Fatalf("期望 4 个窗口，实际 %d: %+v", len(limits.Windows), limits.Windows)
	}

	session := limits.Windows[0]
	if session.ID != "session" || session.Label != "5-hour limit" || session.UsedPercent != 12.5 {
		t.Errorf("session 窗口不符: %+v", session)
	}
	wantSessionReset := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if session.ResetsAt == nil || !session.ResetsAt.Equal(wantSessionReset) {
		t.Errorf("session 重置时间不符: %v", session.ResetsAt)
	}

	weekly := limits.Windows[1]
	if weekly.ID != "weekly_all" || weekly.Label != "Weekly · all models" || weekly.UsedPercent != 40 {
		t.Errorf("weekly_all 窗口不符: %+v", weekly)
	}
	wantWeeklyReset := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if weekly.ResetsAt == nil || !weekly.ResetsAt.Equal(wantWeeklyReset) {
		t.Errorf("weekly_all 重置时间不符: %v", weekly.ResetsAt)
	}

	model := limits.Windows[2]
	if model.Label != "Sonnet · Sonnet 4.5" || model.UsedPercent != 5 {
		t.Errorf("按模型窗口不符: %+v", model)
	}

	extra := limits.Windows[3]
	if extra.ID != "extra-usage" || extra.Label != "Extra usage" || extra.UsedPercent != 3.5 {
		t.Errorf("额外用量窗口不符: %+v", extra)
	}
	if extra.ResetsAt != nil {
		t.Errorf("额外用量不应有重置时间: %v", extra.ResetsAt)
	}
}

func TestParsePlanLimits_TopLevelFallback(t *testing.T) {
	raw := []byte(`{
		"five_hour": {"utilization": 10, "resets_at": "2026-10-18T12:00:00Z"},
		"seven_day": {"utilization": 20, "resets_at": "2026-10-24T12:00:00Z"},
		"extra_usage": {"is_enabled": false, "utilization": 90}
	}`)

	limits, err := ParsePlanLimits(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(limits.Windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %d: %+v", len(limits.Windows), limits.Windows)
	}
	if limits.Windows[0].ID != "five_hour" || limits.Windows[0].UsedPercent != 10 || limits.Windows[0].ResetsAt == nil {
		t.Errorf("five_hour 窗口不符: %+v", limits.Windows[0])
	}
	if limits.Windows[1].ID != "seven_day" || limits.Windows[1].UsedPercent != 20 {
		t.Errorf("seven_day 窗口不符: %+v", limits.Windows[1])
	}
}

func TestParsePlanLimits_MissingFieldsAndMalformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"空对象", `{}`},
		{"limits 为空", `{"limits": []}`},
		{"缺 percent", `{"limits": [{"kind": "session", "resets_at": "2026-10-18T12:00:00Z"}]}`},
		{"顶层缺 utilization", `{"five_hour": {"resets_at": "2026-10-18T12:00:00Z"}}`},
	}
	for _, tc := range cases {
		limits, err := ParsePlanLimits([]byte(tc.raw))
		if err != nil {
			t.Errorf("%s: 期望无错误，实际 %v", tc.name, err)
		}
		if len(limits.Windows) != 0 {
			t.Errorf("%s: 期望 0 个窗口，实际 %+v", tc.name, limits.Windows)
		}
	}

	// 顶层 five_hour 有 utilization 时正常返回，resets_at 缺失则重置时间为 nil
	limits, err := ParsePlanLimits([]byte(`{"five_hour": {"utilization": 8}}`))
	if err != nil || len(limits.Windows) != 1 || limits.Windows[0].ResetsAt != nil {
		t.Fatalf("five_hour 缺 resets_at 时应返回窗口且重置时间为 nil: %+v, %v", limits, err)
	}

	for _, raw := range []string{``, `{`, `not json`, `{"limits": "wrong"}`} {
		if _, err := ParsePlanLimits([]byte(raw)); err == nil {
			t.Errorf("非法 JSON %q 应返回错误", raw)
		}
	}
}

func setPlanLimitsEnv(t *testing.T, env []string) {
	t.Helper()
	original := planLimitsEnv
	planLimitsEnv = func() []string { return env }
	t.Cleanup(func() { planLimitsEnv = original })
}

func setTestHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// isolateGatewayEnv 清空网关相关环境变量，避免宿主机的 ANTHROPIC 配置
// 让无凭据路径意外请求真实网关。
func isolateGatewayEnv(t *testing.T) {
	t.Helper()
	setPlanLimitsEnv(t, nil)
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
}

// stubLogin 写入假凭据并让读取走该临时目录，不触碰真实 HOME 与钥匙串。
func stubLogin(t *testing.T, login claudeLogin) {
	t.Helper()
	setTestHome(t, t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	t.Cleanup(func() {
		ResetPlanLimitsCache()
		keychainLogin = readLoginFromKeychain
		planLimitsHTTPClient = &http.Client{Timeout: 15 * time.Second}
	})
	raw := `{"claudeAiOauth":{"accessToken":"` + login.accessToken +
		`","subscriptionType":"` + login.subscriptionType +
		`","rateLimitTier":"` + login.rateLimitTier + `"}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	keychainLogin = func(string) (claudeLogin, bool) { return claudeLogin{}, false }
}

// useTestServer 将用量接口指向测试服务器，并恢复原地址与客户端。
func useTestServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	origURL := PlanLimitsURL
	origClient := planLimitsHTTPClient
	PlanLimitsURL = srv.URL
	planLimitsHTTPClient = srv.Client()
	t.Cleanup(func() {
		PlanLimitsURL = origURL
		planLimitsHTTPClient = origClient
	})
}

func TestReadPlanLimits_FetchAndCache(t *testing.T) {
	const token = "test-access-token"
	// 配置了网关就只读网关，这里隔离宿主机 ANTHROPIC 环境以保证走 OAuth 分支。
	isolateGatewayEnv(t)
	stubLogin(t, claudeLogin{accessToken: token, subscriptionType: "max", rateLimitTier: "default_claude_max_20x"})

	var gotAuth, gotBeta string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotBeta = r.Header.Get("anthropic-beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"limits": [
				{"kind": "session", "percent": 12.5, "resets_at": "2026-10-18T12:00:00.123456Z"},
				{"kind": "weekly_all", "percent": 40, "resets_at": "2026-10-24T12:00:00.000000Z"}
			]
		}`))
	}))
	defer srv.Close()
	useTestServer(t, srv)

	ResetPlanLimitsCache()
	limits, err := ReadPlanLimits(context.Background())
	if err != nil {
		t.Fatalf("读取限额失败: %v", err)
	}
	if gotAuth != "Bearer "+token {
		t.Errorf("Authorization 头不符: %q", gotAuth)
	}
	if gotBeta != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta 头不符: %q", gotBeta)
	}
	if limits.PlanType != "Max (20x)" {
		t.Errorf("方案名不符: %q", limits.PlanType)
	}
	if len(limits.Windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %+v", limits.Windows)
	}
	if limits.Windows[0].Label != "5-hour limit" || limits.Windows[0].UsedPercent != 12.5 {
		t.Errorf("5 小时窗口不符: %+v", limits.Windows[0])
	}
	wantReset := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if limits.Windows[0].ResetsAt == nil || !limits.Windows[0].ResetsAt.Equal(wantReset) {
		t.Errorf("5 小时窗口重置时间不符: %v", limits.Windows[0].ResetsAt)
	}
	wantWeeklyReset := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if limits.Windows[1].Label != "Weekly · all models" || limits.Windows[1].ResetsAt == nil || !limits.Windows[1].ResetsAt.Equal(wantWeeklyReset) {
		t.Errorf("每周窗口不符: %+v", limits.Windows[1])
	}

	// 命中缓存：服务端关掉后仍可读取
	srv.Close()
	cached, err := ReadPlanLimits(context.Background())
	if err != nil {
		t.Fatalf("读取缓存失败: %v", err)
	}
	if len(cached.Windows) != 2 || cached.PlanType != "Max (20x)" {
		t.Errorf("缓存结果不符: %+v", cached)
	}
}

func TestReadPlanLimits_KeychainFallback(t *testing.T) {
	isolateGatewayEnv(t)
	setTestHome(t, t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ResetPlanLimitsCache()
	t.Cleanup(func() {
		ResetPlanLimitsCache()
		keychainLogin = readLoginFromKeychain
		planLimitsHTTPClient = &http.Client{Timeout: 15 * time.Second}
	})
	keychainLogin = func(string) (claudeLogin, bool) {
		return claudeLogin{accessToken: "keychain-token", subscriptionType: "pro"}, true
	}

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"five_hour": {"utilization": 7, "resets_at": "2026-10-18T12:00:00Z"}}`))
	}))
	defer srv.Close()
	useTestServer(t, srv)

	limits, err := ReadPlanLimits(context.Background())
	if err != nil {
		t.Fatalf("读取限额失败: %v", err)
	}
	if gotAuth != "Bearer keychain-token" {
		t.Errorf("Authorization 头不符: %q", gotAuth)
	}
	if limits.PlanType != "Pro" || len(limits.Windows) != 1 || limits.Windows[0].UsedPercent != 7 {
		t.Errorf("结果不符: %+v", limits)
	}
}

func TestReadPlanLimits_NoCredentialsAndHTTPError(t *testing.T) {
	isolateGatewayEnv(t)
	// 凭据文件缺失且钥匙串没有条目时报错
	setTestHome(t, t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	ResetPlanLimitsCache()
	origKeychain := keychainLogin
	t.Cleanup(func() {
		ResetPlanLimitsCache()
		keychainLogin = origKeychain
		planLimitsHTTPClient = &http.Client{Timeout: 15 * time.Second}
	})
	keychainLogin = func(string) (claudeLogin, bool) { return claudeLogin{}, false }
	if _, err := ReadPlanLimits(context.Background()); err == nil {
		t.Fatal("缺少凭据时应返回错误")
	}

	// 用量接口非 200 时报错
	stubLogin(t, claudeLogin{accessToken: "token"})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer srv.Close()
	useTestServer(t, srv)
	ResetPlanLimitsCache()

	_, err := ReadPlanLimits(context.Background())
	if err == nil {
		t.Fatal("用量接口非 200 时应返回错误")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("错误应包含状态码: %v", err)
	}
}

func TestPlanTypeAndTierSuffix(t *testing.T) {
	cases := []struct {
		subscription string
		tier         string
		want         string
	}{
		{"max", "default_claude_max_20x", "Max (20x)"},
		{"max", "default_claude_max_5x", "Max (5x)"},
		{"pro", "", "Pro"},
		{"", "default_claude_max_20x", ""},
	}
	for _, tc := range cases {
		if got := planType(tc.subscription, tc.tier); got != tc.want {
			t.Errorf("planType(%q, %q) = %q, 期望 %q", tc.subscription, tc.tier, got, tc.want)
		}
	}
}

func TestParseResetTime_StripFraction(t *testing.T) {
	text := "2026-10-18T12:00:00.123456Z"
	got := parseResetTime(&text)
	want := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if got == nil || !got.Equal(want) {
		t.Fatalf("parseResetTime = %v, 期望 %v", got, want)
	}
	if got := parseResetTime(nil); got != nil {
		t.Errorf("空输入应返回 nil: %v", got)
	}
	empty := ""
	if got := parseResetTime(&empty); got != nil {
		t.Errorf("空字符串应返回 nil: %v", got)
	}
}
