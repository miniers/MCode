package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestClientCheckForUpdate 覆盖「有新版本 / 已是最新 / 未配置更新源」三种情况。
// 使用 httptest 本地服务，不访问真实网络。
func TestClientCheckForUpdate(t *testing.T) {
	origFeed := UpdateFeedURL
	origVersion := AppVersion
	defer func() {
		UpdateFeedURL = origFeed
		AppVersion = origVersion
	}()

	var body atomic.Value
	body.Store(`{"tag_name":"v1.2.0","html_url":"https://example.com/release","prerelease":false}`)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(body.Load().(string)))
	}))
	defer srv.Close()

	AppVersion = "1.1.0"
	UpdateFeedURL = srv.URL
	client := &Client{httpClient: srv.Client()}

	if got := client.AppVersion(); got != "1.1.0" {
		t.Fatalf("AppVersion() = %q, want 1.1.0", got)
	}

	// 有新版本
	status, err := client.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdate error: %v", err)
	}
	if !status.Available || status.Current != "1.1.0" || status.Latest != "1.2.0" || status.URL != "https://example.com/release" {
		t.Fatalf("unexpected status with newer release: %+v", status)
	}

	// 已是最新（同版本不再提示）
	AppVersion = "1.2.0"
	status, err = client.CheckForUpdate(context.Background())
	if err != nil {
		t.Fatalf("CheckForUpdate error: %v", err)
	}
	if status.Available || status.Latest != "1.2.0" || status.Current != "1.2.0" {
		t.Fatalf("unexpected up-to-date status: %+v", status)
	}

	// feed 为空：返回错误且不发请求
	UpdateFeedURL = ""
	before := requests.Load()
	status, err = client.CheckForUpdate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "未配置更新源") {
		t.Fatalf("expected 未配置更新源 error, got %v", err)
	}
	if status.Current != "1.2.0" {
		t.Fatalf("unexpected status on empty feed: %+v", status)
	}
	if got := requests.Load(); got != before {
		t.Fatalf("empty feed must not send requests, got %d extra", got-before)
	}
}
