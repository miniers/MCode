package mcodeui

import (
	"context"
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

// fakeUpdateClient 同时实现 mcodeui.Client（内嵌 FakeClient）与 appUpdateClient，
// 用于在不访问网络的前提下驱动关于页的更新检查状态。
type fakeUpdateClient struct {
	*FakeClient
	version string
	status  AppUpdateStatus
	err     error
	calls   int
}

func (f *fakeUpdateClient) CheckForUpdate(ctx context.Context) (AppUpdateStatus, error) {
	f.calls++
	return f.status, f.err
}

func (f *fakeUpdateClient) AppVersion() string { return f.version }

// readAboutState 读取关于页状态快照，避免测试中复制互斥锁。
func readAboutState() (checking, done bool, status AppUpdateStatus, errMsg string) {
	globalAboutState.mu.Lock()
	defer globalAboutState.mu.Unlock()
	return globalAboutState.checking, globalAboutState.done, globalAboutState.status, globalAboutState.errMsg
}

// TestAboutUpdateCheckStateTransitions 测试更新检查状态转换：有新版本、已是最新、出错。
func TestAboutUpdateCheckStateTransitions(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	// 有新版本
	resetAboutPageStateForTest()
	fc := &fakeUpdateClient{
		FakeClient: NewFakeClient(),
		version:    "1.2.3",
		status:     AppUpdateStatus{Current: "1.2.3", Latest: "1.2.4", URL: "https://example.com/dl", Available: true},
	}
	runAppUpdateCheck(st, fc)

	checking, done, status, errMsg := readAboutState()
	if checking || !done || errMsg != "" || !status.Available || status.Latest != "1.2.4" || status.URL != "https://example.com/dl" {
		t.Fatalf("unexpected available state: checking=%v done=%v status=%+v err=%q", checking, done, status, errMsg)
	}
	if fc.calls != 1 {
		t.Fatalf("expected 1 check call, got %d", fc.calls)
	}

	// 已是最新
	resetAboutPageStateForTest()
	fc = &fakeUpdateClient{
		FakeClient: NewFakeClient(),
		version:    "1.2.4",
		status:     AppUpdateStatus{Current: "1.2.4", Latest: "1.2.4"},
	}
	runAppUpdateCheck(st, fc)

	checking, done, status, errMsg = readAboutState()
	if checking || !done || errMsg != "" || status.Available {
		t.Fatalf("unexpected up-to-date state: checking=%v done=%v status=%+v err=%q", checking, done, status, errMsg)
	}

	// 检查出错
	resetAboutPageStateForTest()
	fc = &fakeUpdateClient{FakeClient: NewFakeClient(), err: errors.New("更新源不可用")}
	runAppUpdateCheck(st, fc)

	checking, done, status, errMsg = readAboutState()
	if checking || !done || errMsg != "更新源不可用" || status.Available {
		t.Fatalf("unexpected error state: checking=%v done=%v status=%+v err=%q", checking, done, status, errMsg)
	}
}

// TestAboutVersionAndClientDetection 测试版本号读取与更新检查入口的接口探测。
// 版本号在数据入口只取一次快照，所以每种客户端都先重置状态。
func TestAboutVersionAndClientDetection(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	// 未接入客户端：回退 dev，且无检查更新入口
	resetAboutPageStateForTest()
	if v := aboutVersion(st); v != "dev" {
		t.Fatalf("aboutVersion = %q, want dev", v)
	}
	if aboutUpdateClient(st) != nil {
		t.Fatal("expected no update client when chat state is nil")
	}

	// 客户端未实现 appUpdateClient：同样回退 dev
	resetAboutPageStateForTest()
	st.chat = &chatState{client: NewFakeClient()}
	if v := aboutVersion(st); v != "dev" {
		t.Fatalf("aboutVersion = %q, want dev for plain client", v)
	}
	if aboutUpdateClient(st) != nil {
		t.Fatal("expected no update client for plain client")
	}

	// 客户端实现 appUpdateClient：展示其版本号
	resetAboutPageStateForTest()
	st.chat = &chatState{client: &fakeUpdateClient{FakeClient: NewFakeClient(), version: "9.9.9"}}
	if v := aboutVersion(st); v != "9.9.9" {
		t.Fatalf("aboutVersion = %q, want 9.9.9", v)
	}
	if aboutUpdateClient(st) == nil {
		t.Fatal("expected update client to be detected")
	}

	// AppVersion() 返回空时回退 dev
	resetAboutPageStateForTest()
	st.chat = &chatState{client: &fakeUpdateClient{FakeClient: NewFakeClient()}}
	if v := aboutVersion(st); v != "dev" {
		t.Fatalf("aboutVersion = %q, want dev for empty version", v)
	}
}

// TestSettingsPageAbout_VersionRowLoadsOnce 测试版本号行可见，且多帧渲染只取一次版本快照。
func TestSettingsPageAbout_VersionRowLoadsOnce(t *testing.T) {
	resetAboutPageStateForTest()
	oldLoader := aboutVersionLoader
	var calls int
	aboutVersionLoader = func(client appUpdateClient) string {
		calls++
		return client.AppVersion()
	}
	t.Cleanup(func() { aboutVersionLoader = oldLoader })

	fc := &fakeUpdateClient{FakeClient: NewFakeClient(), version: "3.2.1"}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fc))
	tt := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 4000)

	waitFor(t, tt, func() bool { return tt.HasText("版本号") && tt.HasText("3.2.1") })
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if !tt.HasText("版本号") || !tt.HasText("3.2.1") {
		t.Fatalf("版本号行应持续可见: %v", tt.Texts())
	}
	if calls != 1 {
		t.Fatalf("版本号只应在数据入口取一次，实际 %d 次", calls)
	}
}

// TestSettingsAbout_CheckUpdateRow 测试关于页检查更新行渲染并驱动状态切换。
func TestSettingsAbout_CheckUpdateRow(t *testing.T) {
	resetAboutPageStateForTest()
	fc := &fakeUpdateClient{
		FakeClient: NewFakeClient(),
		version:    "1.2.3",
		status:     AppUpdateStatus{Current: "1.2.3", Latest: "1.2.4", URL: "https://example.com/dl", Available: true},
	}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fc))
	tt := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 4000)

	waitFor(t, tt, func() bool {
		return tt.HasText("1.2.3") && tt.HasText("检查更新")
	})

	if err := tt.Click("btn-about-check-update"); err != nil {
		t.Fatalf("click '检查更新' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("发现新版本 v1.2.4") && tt.HasText("前往下载页")
	})
}
