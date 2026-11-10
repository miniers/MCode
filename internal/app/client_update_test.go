package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClientNativeUpdateInstall(t *testing.T) {
	oldEnabled, oldCheck, oldRelaunch := nativeUpdatesEnabled, nativeUpdateCheck, relaunchAfterUpdate
	t.Cleanup(func() {
		nativeUpdatesEnabled, nativeUpdateCheck, relaunchAfterUpdate = oldEnabled, oldCheck, oldRelaunch
	})
	nativeUpdatesEnabled = func() bool { return true }
	nativeUpdateCheck = func(context.Context) (string, installableUpdate, error) {
		return "2.0.0", fakeInstallableUpdate{}, nil
	}
	relaunched := false
	relaunchAfterUpdate = func() { relaunched = true }
	client := &Client{updateChecker: &UpdateChecker{}}
	status, err := client.CheckForUpdate(context.Background())
	if err != nil || !status.Available || !status.Installable || !client.LatestUpdate().Installable {
		t.Fatalf("原生更新状态错误: %+v, %v", status, err)
	}
	if err := client.InstallUpdate(context.Background(), nil); err != nil || !relaunched {
		t.Fatalf("安装失败: %v, 重启=%v", err, relaunched)
	}
	if err := (&Client{}).InstallUpdate(context.Background(), nil); err == nil {
		t.Fatal("缺少更新检查器时应报错")
	}
}

func TestClientUpdateNotes(t *testing.T) {
	var client *Client
	if got := client.UpdateNotes(); got != "" {
		t.Fatalf("空客户端不应返回发布说明: %q", got)
	}
	client = &Client{}
	if got := client.UpdateNotes(); got != "" {
		t.Fatalf("缺少检查器时不应返回发布说明: %q", got)
	}
	client.updateChecker = &UpdateChecker{latest: UpdateInfo{Notes: "# 更新"}}
	if got := client.UpdateNotes(); got != "# 更新" {
		t.Fatalf("发布说明错误: %q", got)
	}
}

func TestClientLatestUpdate(t *testing.T) {
	client := &Client{}
	if got := client.LatestUpdate(); got.Latest != "" || got.Available {
		t.Fatalf("尚未检查时应返回零值: %+v", got)
	}
	client.updateChecker = &UpdateChecker{latest: UpdateInfo{Latest: "9.0.0", URL: "https://example.com", Available: true}}
	got := client.LatestUpdate()
	if !got.Available || got.Latest != "9.0.0" || got.URL != "https://example.com" {
		t.Fatalf("后台检查结果不匹配: %+v", got)
	}
}

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
