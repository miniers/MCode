package pi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// planTestNow 是方案额度测试里的固定“现在”。
func planTestNow() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }

func TestReadPlanLimitsAnthropic(t *testing.T) {
	usage := loadPiTestdata(t, "usage_anthropic.json")
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		_, _ = w.Write(usage)
	}))
	defer server.Close()

	dir := writeConfigDir(t, map[string][]byte{
		"auth.json": []byte(`{"anthropic":{"type":"oauth","access":"anthropic-test-token","expires":4102444800000}}`),
	})
	windows, err := ReadPlanLimits(context.Background(), planOptions(dir, server))
	if err != nil {
		t.Fatalf("ReadPlanLimits 失败: %v", err)
	}
	requestHeader := waitHeaders(t, headers)
	if got := requestHeader.Get("Authorization"); got != "Bearer anthropic-test-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := requestHeader.Get("anthropic-beta"); got != "oauth-2025-04-20" {
		t.Errorf("anthropic-beta = %q", got)
	}
	if got := requestHeader.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q", got)
	}

	if len(windows) != 4 {
		t.Fatalf("期望 4 个窗口，实际 %+v", windows)
	}
	want := []struct {
		label   string
		percent float64
		resets  time.Time
	}{
		{"Claude · 5-hour limit", 12.5, time.Date(2026, 10, 18, 20, 0, 0, 0, time.UTC)},
		{"Claude · Weekly · all models", 40.0, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)},
		{"Claude · Opus · Opus 4.6", 7.5, time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)},
		{"Claude · Extra usage", 3.25, time.Time{}},
	}
	for i, expected := range want {
		window := windows[i]
		if window.Label != expected.label || window.UsedPercent != expected.percent {
			t.Errorf("第 %d 个窗口 = %+v，期望 %s / %v", i, window, expected.label, expected.percent)
		}
		if !window.ResetsAt.Equal(expected.resets) {
			t.Errorf("第 %d 个窗口的重置时间 = %v，期望 %v", i, window.ResetsAt, expected.resets)
		}
	}
}

func TestReadPlanLimitsAnthropicFallback(t *testing.T) {
	usage := loadPiTestdata(t, "usage_anthropic_fallback.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(usage)
	}))
	defer server.Close()

	dir := writeConfigDir(t, map[string][]byte{
		"auth.json": []byte(`{"anthropic":{"type":"oauth","access":"anthropic-test-token","expires":4102444800000}}`),
	})
	windows, err := ReadPlanLimits(context.Background(), planOptions(dir, server))
	if err != nil {
		t.Fatalf("ReadPlanLimits 失败: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("limits 为空时应退回 five_hour / seven_day，实际 %+v", windows)
	}
	if windows[0].Label != "Claude · 5-hour limit" || windows[0].UsedPercent != 5.5 {
		t.Errorf("five_hour 窗口不符: %+v", windows[0])
	}
	if windows[1].Label != "Claude · Weekly · all models" || windows[1].UsedPercent != 22.25 {
		t.Errorf("seven_day 窗口不符: %+v", windows[1])
	}
}

func TestReadPlanLimitsCodex(t *testing.T) {
	usage := loadPiTestdata(t, "usage_codex.json")
	var authRoot map[string]json.RawMessage
	if err := json.Unmarshal(loadPiTestdata(t, "auth_full.json"), &authRoot); err != nil {
		t.Fatalf("解析 auth fixture 失败: %v", err)
	}
	var codexEntry struct {
		Access string `json:"access"`
	}
	if err := json.Unmarshal(authRoot["openai-codex"], &codexEntry); err != nil {
		t.Fatalf("解析 codex 登录失败: %v", err)
	}

	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers <- r.Header.Clone()
		_, _ = w.Write(usage)
	}))
	defer server.Close()

	dir := writeConfigDir(t, map[string][]byte{
		"auth.json": []byte(`{"openai-codex":` + string(authRoot["openai-codex"]) + `}`),
	})
	windows, err := ReadPlanLimits(context.Background(), planOptions(dir, server))
	if err != nil {
		t.Fatalf("ReadPlanLimits 失败: %v", err)
	}
	requestHeader := waitHeaders(t, headers)
	gotAuthorization := requestHeader.Get("Authorization")
	gotAccountID := requestHeader.Get("ChatGPT-Account-Id")
	gotAccept := requestHeader.Get("Accept")
	if gotAuthorization != "Bearer "+codexEntry.Access || gotAccountID != "acct_test_123" || gotAccept != "application/json" {
		t.Errorf("请求头不符: Authorization=%q ChatGPT-Account-Id=%q Accept=%q", gotAuthorization, gotAccountID, gotAccept)
	}
	if len(windows) != 2 {
		t.Fatalf("期望 2 个窗口，实际 %+v", windows)
	}
	if windows[0].Label != "Codex · 5-hour limit" || windows[0].UsedPercent != 18.5 {
		t.Errorf("主窗口不符: %+v", windows[0])
	}
	if want := time.Unix(1792348800, 0); !windows[0].ResetsAt.Equal(want) {
		t.Errorf("主窗口重置时间 = %v，期望 %v", windows[0].ResetsAt, want)
	}
	if windows[1].Label != "Codex · Weekly" || windows[1].UsedPercent != 63 {
		t.Errorf("次窗口不符: %+v", windows[1])
	}
	if want := planTestNow().Add(3600 * time.Second); !windows[1].ResetsAt.Equal(want) {
		t.Errorf("次窗口重置时间 = %v，期望 %v", windows[1].ResetsAt, want)
	}
}

func TestReadPlanLimitsWithoutToken(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	cases := []struct {
		name  string
		files map[string][]byte
	}{
		{
			name: "没有 oauth 条目",
			files: map[string][]byte{
				"auth.json": []byte(`{"anthropic":{"type":"api_key","key":"sk-x"},"some-api-key":"sk-y"}`),
			},
		},
		{
			name: "access 为空",
			files: map[string][]byte{
				"auth.json": []byte(`{"anthropic":{"type":"oauth","access":"","expires":4102444800000}}`),
			},
		},
		{
			name: "令牌已过期",
			files: map[string][]byte{
				"auth.json": []byte(`{"anthropic":{"type":"oauth","access":"stale","expires":1000}}`),
			},
		},
		{
			name:  "没有 auth.json",
			files: map[string][]byte{},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := writeConfigDir(t, testCase.files)
			windows, err := ReadPlanLimits(context.Background(), planOptions(dir, server))
			if err != nil {
				t.Fatalf("ReadPlanLimits 失败: %v", err)
			}
			if windows != nil {
				t.Errorf("没有可用令牌时应返回 nil，实际 %+v", windows)
			}
		})
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("没有可用令牌时不该请求用量端点，实际请求 %d 次", got)
	}
}

func TestReadPlanLimitsNon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cases := []struct {
		name string
		auth string
	}{
		{name: "anthropic", auth: `{"anthropic":{"type":"oauth","access":"token","expires":4102444800000}}`},
		{
			name: "codex",
			auth: `{"openai-codex":{"type":"oauth","access":"header.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnt9fQ.sig","expires":4102444800000}}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := writeConfigDir(t, map[string][]byte{"auth.json": []byte(testCase.auth)})
			windows, err := ReadPlanLimits(context.Background(), planOptions(dir, server))
			if err == nil {
				t.Fatal("非 200 应返回错误")
			}
			if !strings.Contains(err.Error(), "500") {
				t.Errorf("错误里应带状态码: %v", err)
			}
			if windows != nil {
				t.Errorf("出错时不应返回窗口: %+v", windows)
			}
		})
	}
}

func TestPiWindowTitle(t *testing.T) {
	cases := []struct {
		minutes *int
		want    string
	}{
		{nil, "Usage limit"},
		{intPtr(300), "5-hour limit"},
		{intPtr(1440), "Daily"},
		{intPtr(10080), "Weekly"},
		{intPtr(43200), "Monthly"},
		{intPtr(44640), "Monthly"},
		{intPtr(2880), "2-day limit"},
		{intPtr(90), "1-hour limit"},
		{intPtr(120), "2-hour limit"},
	}
	for _, testCase := range cases {
		if got := piWindowTitle(testCase.minutes); got != testCase.want {
			t.Errorf("piWindowTitle(%v) = %q，期望 %q", testCase.minutes, got, testCase.want)
		}
	}
}

func TestPiCodexAccountID(t *testing.T) {
	var authRoot map[string]json.RawMessage
	if err := json.Unmarshal(loadPiTestdata(t, "auth_full.json"), &authRoot); err != nil {
		t.Fatalf("解析 auth fixture 失败: %v", err)
	}
	var entry struct {
		Access string `json:"access"`
	}
	if err := json.Unmarshal(authRoot["openai-codex"], &entry); err != nil {
		t.Fatalf("解析 codex 登录失败: %v", err)
	}
	if got := piCodexAccountID(entry.Access); got != "acct_test_123" {
		t.Errorf("账号 id = %q，期望 acct_test_123", got)
	}
	if got := piCodexAccountID("no-dots"); got != "" {
		t.Errorf("不是 JWT 时 = %q，期望空", got)
	}
	if got := piCodexAccountID("header.!!!not-base64!!!.sig"); got != "" {
		t.Errorf("payload 非法时 = %q，期望空", got)
	}
}

// planOptions 组装指向测试服务器的读取配置。
func planOptions(dir string, server *httptest.Server) PlanLimitsOptions {
	return PlanLimitsOptions{
		Env:          map[string]string{"PI_CODING_AGENT_DIR": dir},
		HomeDir:      dir,
		HTTPClient:   server.Client(),
		AnthropicURL: server.URL + "/api/oauth/usage",
		CodexURL:     server.URL + "/backend-api/wham/usage",
		Now:          planTestNow,
	}
}

// waitHeaders 取出用量端点处理器记录的请求头；通道带同步，读不到时不会挂死整个测试。
func waitHeaders(t *testing.T, headers <-chan http.Header) http.Header {
	t.Helper()
	select {
	case got := <-headers:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("用量端点没有被请求")
		return nil
	}
}

func intPtr(value int) *int { return &value }
