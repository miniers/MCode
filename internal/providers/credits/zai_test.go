package credits

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseZaiWindows(t *testing.T) {
	windows, err := parseZaiWindows(readFixture(t, "zai_quota_limit.json"))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("期望 3 个窗口，实际 %d: %+v", len(windows), windows)
	}
	if windows[0].Label != "5-hour limit" || windows[0].UsedPercent != 25 {
		t.Errorf("5 小时窗口不符: %+v", windows[0])
	}
	if want := time.UnixMilli(1792339200000).UTC(); !windows[0].ResetsAt.Equal(want) {
		t.Errorf("5 小时重置时间 = %v，期望 %v", windows[0].ResetsAt, want)
	}
	if windows[1].Label != "Weekly" || windows[1].UsedPercent != 25 {
		t.Errorf("每周窗口不符: %+v", windows[1])
	}
	if windows[2].Label != "Monthly · MCP tools" || windows[2].UsedPercent != 25 {
		t.Errorf("MCP 工具窗口不符: %+v", windows[2])
	}
}

func TestParseZaiWindows_ClampsPercentAndRejectsErrorResponse(t *testing.T) {
	raw := []byte(`{"code":200,"data":{"limits":[{"type":"CREDIT_LIMIT","usage":10,"currentValue":40,"unit":3,"number":5}]}}`)
	windows, err := parseZaiWindows(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(windows) != 1 || windows[0].UsedPercent != 100 {
		t.Errorf("百分比应夹到 100: %+v", windows)
	}

	if _, err := parseZaiWindows([]byte(`{"code":500,"msg":"boom"}`)); err == nil {
		t.Error("非 200 响应应报错")
	}
}

func TestWindowTitle(t *testing.T) {
	for minutes, want := range map[int]string{
		300:   "5-hour limit",
		1440:  "Daily",
		10080: "Weekly",
		43200: "Monthly",
		2880:  "2-day limit",
		180:   "3-hour limit",
	} {
		if got := windowTitle(minutes); got != want {
			t.Errorf("windowTitle(%d) = %q，期望 %q", minutes, got, want)
		}
	}
}

func TestReadPlanWindowsZai(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer zai-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(readFixture(t, "zai_quota_limit.json"))
	}))
	defer server.Close()

	windows, err := ReadPlanWindows(context.Background(), "zai", Options{
		APIKey:     "zai-key",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
		Env:        map[string]string{},
		HomeDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("ReadPlanWindows 失败: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("期望 3 个窗口，实际 %+v", windows)
	}
}

func TestReadPlanWindowsZai_RejectedKeyAndMissingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	if _, err := ReadPlanWindows(context.Background(), "zai", Options{APIKey: "bad", BaseURL: server.URL, HTTPClient: server.Client()}); err == nil {
		t.Error("401 应映射为错误")
	}
	if _, err := ReadPlanWindows(context.Background(), "zai", Options{Env: map[string]string{}}); !errors.Is(err, ErrMissingKey) {
		t.Errorf("期望 ErrMissingKey，实际 %v", err)
	}
}
