package credits

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeClineProviders 把 providers.json fixture 的 {{BASE}} 换成测试服务器地址，
// 写进临时 home 的 .cline/data/settings/providers.json。
func writeClineProviders(t *testing.T, home, base string) {
	t.Helper()
	content := strings.ReplaceAll(string(readFixture(t, "cline_providers.json")), "{{BASE}}", base)
	dir := filepath.Join(home, ".cline", "data", "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("写 providers.json 失败: %v", err)
	}
}

func TestReadClineCredits(t *testing.T) {
	fixture := readFixture(t, "cline_users_me.json")
	balance := readFixture(t, "cline_balance.json")
	var balancePath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer workos:cline-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/users/me":
			_, _ = w.Write(fixture)
		case "/api/v1/organizations/org-9/balance":
			balancePath = r.URL.Path
			_, _ = w.Write(balance)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	writeClineProviders(t, home, server.URL)

	got, err := Read(context.Background(), "cline", Options{
		Env:        map[string]string{},
		HomeDir:    home,
		HTTPClient: server.Client(),
		Now:        fixedNow,
	})
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if balancePath != "/api/v1/organizations/org-9/balance" {
		t.Errorf("应读启用组织的余额，实际路径 %q", balancePath)
	}
	if got.Total != 12.5 || got.Currency != "USD" || !got.Usable || got.Provider != ProviderCline {
		t.Errorf("余额不符: %+v", got)
	}
	if got.AmountText() != "$12.50" {
		t.Errorf("AmountText = %q", got.AmountText())
	}
}

func TestReadClineCredits_UserBalanceWhenNoOrganization(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/users/me":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"user-7","organizations":[]}}`))
		case "/api/v1/users/user-7/balance":
			_, _ = w.Write(readFixture(t, "cline_balance.json"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	home := t.TempDir()
	writeClineProviders(t, home, server.URL)
	// 只留 cline 的条目，且没有启用的组织。
	got, err := Read(context.Background(), "cline", Options{
		Env:        map[string]string{},
		HomeDir:    home,
		HTTPClient: server.Client(),
		Now:        fixedNow,
	})
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if got.Total != 12.5 {
		t.Errorf("余额不符: %+v", got)
	}
}

func TestReadClineCredits_ZeroBalanceIsDepleted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/users/me":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"user-7"}}`))
		default:
			_, _ = w.Write([]byte(`{"success":true,"data":{"balance":0}}`))
		}
	}))
	defer server.Close()

	home := t.TempDir()
	writeClineProviders(t, home, server.URL)
	got, err := Read(context.Background(), "cline", Options{Env: map[string]string{}, HomeDir: home, HTTPClient: server.Client(), Now: fixedNow})
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if !got.Depleted() {
		t.Errorf("余额为 0 应为已耗尽: %+v", got)
	}
}

func TestReadClineCredits_EnvKeyAndRejectedLogin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer env-cline-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/users/me":
			_, _ = w.Write([]byte(`{"success":true,"data":{"id":"user-7"}}`))
		default:
			_, _ = w.Write(readFixture(t, "cline_balance.json"))
		}
	}))
	defer server.Close()

	home := t.TempDir()
	writeClineProviders(t, home, server.URL)
	env := map[string]string{"CLINE_API_KEY": "env-cline-key"}
	if _, err := Read(context.Background(), "cline", Options{Env: env, HomeDir: home, HTTPClient: server.Client(), Now: fixedNow}); err != nil {
		t.Fatalf("环境变量密钥应可用: %v", err)
	}

	// 401 映射为错误。
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	writeClineProviders(t, home, bad.URL)
	if _, err := Read(context.Background(), "cline", Options{Env: map[string]string{}, HomeDir: home, HTTPClient: bad.Client()}); err == nil {
		t.Error("401 应映射为错误")
	}
}

func TestReadClineCredits_PassProviderHasNoBalance(t *testing.T) {
	home := t.TempDir()
	writeClineProviders(t, home, "https://api.cline.bot")
	content := strings.Replace(string(readFixture(t, "cline_providers.json")), `"lastUsedProvider": "cline"`, `"lastUsedProvider": "cline-pass"`, 1)
	dir := filepath.Join(home, ".cline", "data", "settings")
	if err := os.WriteFile(filepath.Join(dir, "providers.json"), []byte(content), 0o600); err != nil {
		t.Fatalf("写 providers.json 失败: %v", err)
	}
	_, err := Read(context.Background(), "cline", Options{Env: map[string]string{}, HomeDir: home})
	if err == nil {
		t.Fatal("cline-pass 不读预付费余额")
	}
}

func TestReadClineCredits_MissingLogin(t *testing.T) {
	_, err := Read(context.Background(), "cline", Options{Env: map[string]string{}, HomeDir: t.TempDir()})
	if !errors.Is(err, ErrMissingKey) {
		t.Errorf("期望 ErrMissingKey，实际 %v", err)
	}
}

func TestReadPlanWindowsCline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/me/plan/usage-limits" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer workos:cline-pass-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(readFixture(t, "cline_usage_limits.json"))
	}))
	defer server.Close()

	home := t.TempDir()
	writeClineProviders(t, home, server.URL)
	windows, err := ReadPlanWindows(context.Background(), "cline", Options{Env: map[string]string{}, HomeDir: home, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("ReadPlanWindows 失败: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("期望 3 个窗口，实际 %+v", windows)
	}
	if windows[0].Label != "5-hour limit" || windows[0].UsedPercent != 12.5 {
		t.Errorf("5 小时窗口不符: %+v", windows[0])
	}
	if want := time.Date(2026, 10, 18, 12, 0, 0, 123456000, time.UTC); !windows[0].ResetsAt.Equal(want) {
		t.Errorf("重置时间 = %v，期望 %v", windows[0].ResetsAt, want)
	}
	if windows[1].Label != "Weekly" || windows[1].UsedPercent != 40 {
		t.Errorf("每周窗口不符: %+v", windows[1])
	}
	if windows[2].Label != "Monthly" || windows[2].UsedPercent != 5 {
		t.Errorf("每月窗口不符: %+v", windows[2])
	}
}
