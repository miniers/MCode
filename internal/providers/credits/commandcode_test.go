package credits

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseCommandCodeCredits(t *testing.T) {
	raw := readFixture(t, "commandcode_credits.json")
	got, err := parseCommandCodeCredits(raw, fixedNow())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got.Total != 16.5 || got.Currency != "USD" || !got.Usable {
		t.Errorf("余额不符: %+v", got)
	}
	if got.Monthly == nil || *got.Monthly != 10 || got.ToppedUp == nil || *got.ToppedUp != 5 || got.Granted == nil || *got.Granted != 1.5 {
		t.Errorf("拆分不符: %+v", got)
	}
	if want := "Monthly $10.00 · Topped up $5.00 · Granted $1.50"; got.BreakdownText() != want {
		t.Errorf("BreakdownText = %q，期望 %q", got.BreakdownText(), want)
	}
	if got.Depleted() {
		t.Error("有余额时不应为已耗尽")
	}
}

func TestParseCommandCodeCredits_BelowThresholdIsDepleted(t *testing.T) {
	raw := []byte(`{"credits":{"monthlyCredits":0,"purchasedCredits":0,"freeCredits":7,"belowThreshold":true}}`)
	got, err := parseCommandCodeCredits(raw, fixedNow())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !got.Depleted() {
		t.Errorf("belowThreshold 为 true 应为已耗尽: %+v", got)
	}
}

func TestReadCommandCode_FromEnv(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpha/billing/credits" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer env-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(readFixture(t, "commandcode_credits.json"))
	}))
	defer server.Close()

	opts := Options{
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Env:        map[string]string{"COMMAND_CODE_API_KEY": "env-key"},
		HomeDir:    t.TempDir(),
		Now:        fixedNow,
	}
	got, err := Read(context.Background(), "commandcode", opts)
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if got.Total != 16.5 {
		t.Errorf("余额不符: %+v", got)
	}
}

func TestReadCommandCode_FromStoredLogin(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".commandcode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"apiKey":"file-key","userName":"kai"}`), 0o600); err != nil {
		t.Fatalf("写 auth.json 失败: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer file-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(readFixture(t, "commandcode_credits.json"))
	}))
	defer server.Close()

	opts := Options{BaseURL: server.URL, HTTPClient: server.Client(), Env: map[string]string{}, HomeDir: home, Now: fixedNow}
	if _, err := Read(context.Background(), "commandcode", opts); err != nil {
		t.Fatalf("从 ~/.commandcode/auth.json 读取失败: %v", err)
	}
}

func TestReadCommandCode_RejectedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	_, err := Read(context.Background(), "commandcode", Options{APIKey: "bad", BaseURL: server.URL, HTTPClient: server.Client()})
	if err == nil {
		t.Fatal("403 应映射为错误")
	}
}

func TestReadCommandCode_MissingKey(t *testing.T) {
	_, err := Read(context.Background(), "commandcode", Options{Env: map[string]string{}, HomeDir: t.TempDir()})
	if !errors.Is(err, ErrMissingKey) {
		t.Errorf("期望 ErrMissingKey，实际 %v", err)
	}
}
