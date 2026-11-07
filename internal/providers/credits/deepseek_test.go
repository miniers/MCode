package credits

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("读取 fixture %s: %v", name, err)
	}
	return data
}

func fixedNow() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }

func TestParseDeepSeekBalance(t *testing.T) {
	raw := readFixture(t, "deepseek_balance.json")
	got, err := parseDeepSeekBalance(raw, fixedNow())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Provider != ProviderDeepSeek || got.Total != 12.34 || got.Currency != "USD" || !got.Usable {
		t.Errorf("余额不符: %+v", got)
	}
	if got.ToppedUp == nil || *got.ToppedUp != 10 || got.Granted == nil || *got.Granted != 2.34 {
		t.Errorf("拆分不符: %+v", got)
	}
	if got.AmountText() != "$12.34" {
		t.Errorf("AmountText = %q", got.AmountText())
	}
	if got.Depleted() {
		t.Error("有余额时不应为已耗尽")
	}
	if !got.ReadAt.Equal(fixedNow()) {
		t.Errorf("ReadAt = %v", got.ReadAt)
	}
}

func TestParseDeepSeekBalance_PrefersUSDOverCNY(t *testing.T) {
	raw := readFixture(t, "deepseek_balance_multi.json")
	got, err := parseDeepSeekBalance(raw, fixedNow())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Currency != "USD" || got.Total != 12.34 {
		t.Errorf("应优先取 USD，实际 %+v", got)
	}
}

func TestParseDeepSeekBalance_ZeroIsDepleted(t *testing.T) {
	raw := []byte(`{"is_available": true, "balance_infos": [{"currency":"USD","total_balance":"0","topped_up_balance":"0","granted_balance":"0"}]}`)
	got, err := parseDeepSeekBalance(raw, fixedNow())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !got.Depleted() {
		t.Errorf("余额为 0 应为已耗尽: %+v", got)
	}
	// is_available 缺失时按可用处理。
	got, err = parseDeepSeekBalance([]byte(`{"balance_infos":[{"currency":"USD","total_balance":"3.00"}]}`), fixedNow())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !got.Usable || got.Depleted() {
		t.Errorf("is_available 缺失时应按可用处理: %+v", got)
	}
}

func TestReadDeepSeek(t *testing.T) {
	fixture := readFixture(t, "deepseek_balance.json")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer server.Close()

	opts := Options{
		APIKey:     "test-key",
		BaseURL:    server.URL,
		Env:        map[string]string{},
		HomeDir:    t.TempDir(),
		Now:        fixedNow,
		HTTPClient: server.Client(),
	}
	got, err := Read(context.Background(), "deepseek", opts)
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if got.Total != 12.34 || got.AmountText() != "$12.34" {
		t.Errorf("余额不符: %+v", got)
	}
}

func TestReadDeepSeek_RejectedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := Read(context.Background(), "deepseek", Options{APIKey: "bad", BaseURL: server.URL, HTTPClient: server.Client()})
	if err == nil {
		t.Fatal("401 应映射为错误")
	}
	if errors.Is(err, ErrMissingKey) {
		t.Errorf("401 不该报成缺凭据: %v", err)
	}
}

func TestReadDeepSeek_MissingKey(t *testing.T) {
	_, err := Read(context.Background(), "deepseek", Options{Env: map[string]string{}, HomeDir: t.TempDir()})
	if !errors.Is(err, ErrMissingKey) {
		t.Errorf("期望 ErrMissingKey，实际 %v", err)
	}
}
