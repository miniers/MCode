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

func TestFactoryToken(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".factory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), readFixture(t, "factory_auth.json"), 0o600); err != nil {
		t.Fatalf("写 auth.json 失败: %v", err)
	}

	cases := []struct {
		name string
		opts Options
		want string
	}{
		{"APIKey 优先", Options{APIKey: "opt-key", Env: map[string]string{"FACTORY_API_KEY": "env-key"}, HomeDir: home}, "opt-key"},
		{"环境变量次之", Options{Env: map[string]string{"FACTORY_API_KEY": "env-key"}, HomeDir: home}, "env-key"},
		{"HomeDir 文件兜底", Options{Env: map[string]string{}, HomeDir: home}, "factory-file-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewFactoryClient(tc.opts).Token(context.Background())
			if err != nil {
				t.Fatalf("Token 失败: %v", err)
			}
			if got != tc.want {
				t.Errorf("Token = %q，期望 %q", got, tc.want)
			}
		})
	}

	if _, err := NewFactoryClient(Options{Env: map[string]string{}, HomeDir: t.TempDir()}).Token(context.Background()); !errors.Is(err, ErrMissingKey) {
		t.Errorf("无令牌时期望 ErrMissingKey，实际 %v", err)
	}
}

func TestParseFactoryWindows(t *testing.T) {
	windows, err := parseFactoryWindows(readFixture(t, "factory_limits.json"), fixedNow())
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(windows) != 5 {
		t.Fatalf("期望 5 个窗口，实际 %d: %+v", len(windows), windows)
	}
	standard := windows[0]
	if standard.Label != "Standard · 5-hour limit" || standard.UsedPercent != 12.5 {
		t.Errorf("Standard 5 小时窗口不符: %+v", standard)
	}
	if want := time.Date(2026, 10, 18, 17, 0, 0, 0, time.UTC); !standard.ResetsAt.Equal(want) {
		t.Errorf("重置时间 = %v，期望 %v", standard.ResetsAt, want)
	}
	// 窗口已结束的读数按 0 处理，且没有重置时间。
	monthly := windows[2]
	if monthly.Label != "Standard · Monthly" || monthly.UsedPercent != 0 || !monthly.ResetsAt.IsZero() {
		t.Errorf("过期窗口应按 0 处理: %+v", monthly)
	}
	// core 池有活跃且用量大于 0 的窗口时才显示。
	if windows[3].Label != "Droid Core · 5-hour limit" || windows[4].Label != "Droid Core · Weekly" || windows[4].UsedPercent != 60 {
		t.Errorf("Droid Core 窗口不符: %+v", windows[3:])
	}
}

func TestParseFactoryWindows_RequiresStandard(t *testing.T) {
	if _, err := parseFactoryWindows([]byte(`{"limits":{"core":{}}}`), fixedNow()); err == nil {
		t.Error("缺少 limits.standard 应报错")
	}
}

func TestReadPlanWindowsFactory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer factory-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(readFixture(t, "factory_limits.json"))
	}))
	defer server.Close()

	opts := Options{
		APIKey:     "factory-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Env:        map[string]string{},
		HomeDir:    t.TempDir(),
		Now:        fixedNow,
	}
	windows, err := NewFactoryClient(opts).PlanWindows(context.Background())
	if err != nil {
		t.Fatalf("PlanWindows 失败: %v", err)
	}
	if len(windows) != 5 {
		t.Fatalf("期望 5 个窗口，实际 %+v", windows)
	}

	// 同一入口也服务于 droid。
	if _, err := ReadPlanWindows(context.Background(), "droid", opts); err != nil {
		t.Fatalf("ReadPlanWindows(droid) 失败: %v", err)
	}
}

func TestReadPlanWindowsFactory_RejectedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := ReadPlanWindows(context.Background(), "droid", Options{APIKey: "bad", BaseURL: server.URL, HTTPClient: server.Client()})
	if err == nil {
		t.Fatal("401 应映射为错误")
	}

	_, err = ReadPlanWindows(context.Background(), "droid", Options{Env: map[string]string{}, HomeDir: t.TempDir()})
	if !errors.Is(err, ErrMissingKey) {
		t.Errorf("期望 ErrMissingKey，实际 %v", err)
	}
}
