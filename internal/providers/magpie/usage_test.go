package magpie

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// useUsageServer 把用量记录的客户端指向 srv，测试结束后还原。
func useUsageServer(t *testing.T, srv *httptest.Server) {
	t.Helper()
	restore := usageHTTPClient
	usageHTTPClient = srv.Client()
	t.Cleanup(func() { usageHTTPClient = restore })
}

const usageRespJSON = `{
	"rows":[
		{"t":"2026-11-04T10:30:00.123456789+08:00","agent":"pi","provider":"claude",
		 "providerAccount":"me@example.com","model":"claude-sonnet-5-5",
		 "session":"01a10696-22fc-7145-8c50-53284638ca27","native_session":""},
		{"t":"2026-11-04T02:00:00Z","agent":"claude","provider":"codex",
		 "providerAccount":"","model":"gpt-x","session":"sess-2","native_session":"native-2"},
		{"t":"坏时间","agent":"pi","provider":"session-unknown","providerAccount":"x@example.com"}
	],
	"total":3,
	"offset":0
}`

// TestReadUsageRequests 校验请求参数、鉴权头与响应解析（含纳秒和 +08:00 时区）。
func TestReadUsageRequests(t *testing.T) {
	var gotPath, gotLimit, gotAgent, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotLimit = r.URL.Query().Get("limit")
		gotAgent = r.URL.Query().Get("agent")
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, usageRespJSON)
	}))
	defer srv.Close()
	useUsageServer(t, srv)

	rows, err := ReadUsageRequests(context.Background(), srv.URL+"/v1", "gw-key", "pi", 100)
	if err != nil {
		t.Fatalf("ReadUsageRequests: %v", err)
	}
	if gotPath != "/v1/magpie/usage/requests" {
		t.Errorf("path = %q，想要 /v1/magpie/usage/requests", gotPath)
	}
	if gotLimit != "100" {
		t.Errorf("limit = %q，想要 100", gotLimit)
	}
	if gotAgent != "pi" {
		t.Errorf("agent = %q，想要 pi", gotAgent)
	}
	if gotAuth != "Bearer gw-key" {
		t.Errorf("Authorization = %q，想要 Bearer gw-key", gotAuth)
	}
	if len(rows) != 3 {
		t.Fatalf("行数 = %d，想要 3", len(rows))
	}
	want := time.Date(2026, 11, 4, 10, 30, 0, 123456789, time.FixedZone("", 8*3600))
	if !rows[0].T.Equal(want) || rows[0].T.Nanosecond() != 123456789 {
		t.Errorf("第一行时间 = %v，想要 %v", rows[0].T, want)
	}
	if rows[0].Provider != "claude" || rows[0].ProviderAccount != "me@example.com" ||
		rows[0].Model != "claude-sonnet-5-5" || rows[0].Session != "01a10696-22fc-7145-8c50-53284638ca27" {
		t.Errorf("第一行 = %+v", rows[0])
	}
	if rows[1].T.IsZero() || rows[1].NativeSession != "native-2" {
		t.Errorf("第二行 = %+v", rows[1])
	}
	if !rows[2].T.IsZero() {
		t.Errorf("解析不了的时间应为零值，实际 %v", rows[2].T)
	}

	// agent 为空时不下发过滤参数。
	rows, err = ReadUsageRequests(context.Background(), srv.URL, "gw-key", "  ", 0)
	if err != nil {
		t.Fatalf("ReadUsageRequests（无 agent）: %v", err)
	}
	if gotAgent != "" || gotLimit != "" {
		t.Errorf("空 agent / 非正 limit 不应下发参数：agent=%q limit=%q", gotAgent, gotLimit)
	}
	if len(rows) != 3 {
		t.Errorf("行数 = %d，想要 3", len(rows))
	}
}

// TestReadUsageRequestsForbidden 校验 403 返回可被 errors.Is 识别的哨兵错误。
func TestReadUsageRequestsForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"error":"forbidden"}`)
	}))
	defer srv.Close()
	useUsageServer(t, srv)

	_, err := ReadUsageRequests(context.Background(), srv.URL+"/v1", "gw-key", "pi", 100)
	if !errors.Is(err, ErrUsageForbidden) {
		t.Fatalf("403 的报错 = %v，想要 ErrUsageForbidden", err)
	}
}

// TestReadUsageRequestsServerError 校验非 2xx 的报错带上响应片段。
func TestReadUsageRequestsServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		io.WriteString(w, "upstream down")
	}))
	defer srv.Close()
	useUsageServer(t, srv)

	_, err := ReadUsageRequests(context.Background(), srv.URL+"/v1", "gw-key", "pi", 100)
	if err == nil || errors.Is(err, ErrUsageForbidden) {
		t.Fatalf("502 的报错 = %v，想要普通错误", err)
	}
	if want := "upstream down"; !strings.Contains(err.Error(), want) {
		t.Errorf("报错 = %v，想要包含 %q", err, want)
	}
}
