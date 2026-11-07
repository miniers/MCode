package commandcode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// billingServer 起一个只认 /alpha/billing/credits 的 httptest 服务器。
func billingServer(t *testing.T, fixture string, wantKey string) *httptest.Server {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("读取 fixture 失败: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != commandCodePlanLimitsPath {
			http.NotFound(w, r)
			return
		}
		if wantKey != "" && r.Header.Get("Authorization") != "Bearer "+wantKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPlanLimitsWindows(t *testing.T) {
	server := billingServer(t, "billing_credits.json", "test-key")
	windows, err := PlanLimits(context.Background(), PlanLimitsOptions{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("PlanLimits 返回错误: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("应有 fiveHour 与 weekly 两个窗口，得到 %+v", windows)
	}

	// cap 3、used 1.5：标题带上 cap，百分比 50，resetAt 是 epoch 毫秒。
	if windows[0].Label != "5-hour limit · $3.00" {
		t.Fatalf("窗口标题不符: %q", windows[0].Label)
	}
	if windows[0].UsedPercent != 50 {
		t.Fatalf("已用百分比应为 50，得到 %v", windows[0].UsedPercent)
	}
	if !windows[0].ResetsAt.Equal(time.UnixMilli(1760000000000)) {
		t.Fatalf("重置时间不符: %v", windows[0].ResetsAt)
	}

	// cap 16、used 20：百分比封顶 100；cap 为 0 的 monthly 窗口跳过。
	if windows[1].Label != "Weekly · $16.00" {
		t.Fatalf("窗口标题不符: %q", windows[1].Label)
	}
	if windows[1].UsedPercent != 100 {
		t.Fatalf("已用百分比应封顶 100，得到 %v", windows[1].UsedPercent)
	}
	if !windows[1].ResetsAt.Equal(time.UnixMilli(1760500000000)) {
		t.Fatalf("重置时间不符: %v", windows[1].ResetsAt)
	}
}

func TestPlanLimitsWithoutWindows(t *testing.T) {
	server := billingServer(t, "billing_credits_plain.json", "test-key")
	windows, err := PlanLimits(context.Background(), PlanLimitsOptions{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("缺少 cap 的窗口不应报错: %v", err)
	}
	if windows != nil {
		t.Fatalf("没有可展示窗口时应返回 nil，得到 %+v", windows)
	}
}

func TestPlanLimitsWithoutKey(t *testing.T) {
	// Env 非 nil 且为空：不读进程环境；HomeDir 指向空目录：读不到 auth.json。
	windows, err := PlanLimits(context.Background(), PlanLimitsOptions{
		Env:     map[string]string{},
		HomeDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("没有密钥不应报错: %v", err)
	}
	if windows != nil {
		t.Fatalf("没有密钥时应返回 nil，得到 %+v", windows)
	}
}

func TestPlanLimitsKeyFromEnv(t *testing.T) {
	server := billingServer(t, "billing_credits.json", "env-key")
	windows, err := PlanLimits(context.Background(), PlanLimitsOptions{
		Env:        map[string]string{"COMMAND_CODE_API_KEY": "env-key"},
		HomeDir:    t.TempDir(),
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("PlanLimits 返回错误: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("环境变量密钥应能读到窗口，得到 %+v", windows)
	}
}

func TestPlanLimitsKeyFromAuthFile(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".commandcode"), 0o700); err != nil {
		t.Fatal(err)
	}
	auth := `{"apiKey":"file-key","userName":"miniers"}`
	if err := os.WriteFile(filepath.Join(home, ".commandcode", "auth.json"), []byte(auth), 0o600); err != nil {
		t.Fatal(err)
	}
	server := billingServer(t, "billing_credits.json", "file-key")
	windows, err := PlanLimits(context.Background(), PlanLimitsOptions{
		Env:        map[string]string{},
		HomeDir:    home,
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("PlanLimits 返回错误: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("auth.json 里的密钥应能读到窗口，得到 %+v", windows)
	}
}

func TestPlanLimitsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	if _, err := PlanLimits(context.Background(), PlanLimitsOptions{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}); err == nil {
		t.Fatal("非 200 应返回错误")
	}
}
