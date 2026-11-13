package mcodeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/datadir"
)

// fakeUpdateClient 同时实现 mcodeui.Client（内嵌 FakeClient）与 appUpdateClient，
// 用于在不访问网络的前提下驱动关于页的更新检查状态。
type fakeUpdateClient struct {
	*FakeClient
	version string
	status  AppUpdateStatus
	err     error
	calls   int
	latest  AppUpdateStatus
}

func TestAboutDataDirTargetAppendsMCode(t *testing.T) {
	base := t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "existing"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := aboutDataDirTarget("  " + base + "  "); got != filepath.Join(base, "MCode") {
		t.Fatalf("目标目录 = %q", got)
	}
}

func TestAboutDataDirTargetKeepsEmptyDir(t *testing.T) {
	base := t.TempDir()
	if got := aboutDataDirTarget("  " + base + "  "); got != base {
		t.Fatalf("空目录目标 = %q", got)
	}
}

func TestAboutDataDirTargetKeepsMissingDir(t *testing.T) {
	base := filepath.Join(t.TempDir(), "missing")
	if got := aboutDataDirTarget("  " + base + "  "); got != base {
		t.Fatalf("不存在目录目标 = %q", got)
	}
}

func TestAboutDataDirPinnedHidesChange(t *testing.T) {
	resetAboutPageStateForTest()
	dir := t.TempDir()
	if err := datadir.Export(dir, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = datadir.Export("", false) })
	st := NewSettingsState(dir, nil, nil)
	tt := ui.NewTester(func(c *ui.Context) { renderAboutDataDirValue(c, st) }, 800, 120)
	tt.Frame()
	if _, ok := tt.Find("btn-about-change-data-dir"); ok {
		t.Fatal("固定目录不应显示更改按钮")
	}
}

func TestAboutDataDirMoveFailureKeepsConfirm(t *testing.T) {
	resetAboutPageStateForTest()
	old := t.TempDir()
	st := NewSettingsState(old, nil, nil)
	globalAboutState.mu.Lock()
	globalAboutState.dataDirConfirm = true
	globalAboutState.mu.Unlock()
	confirmAboutDataDirMove(st, filepath.Join(old, "child", "MCode"))
	globalAboutState.mu.Lock()
	open, errMsg := globalAboutState.dataDirConfirm, globalAboutState.dataDirErr
	globalAboutState.mu.Unlock()
	if !open || errMsg == "" {
		t.Fatalf("失败后确认框状态错误: open=%v err=%q", open, errMsg)
	}
}

func TestAboutDataDirMoveSuccessPreparesRelaunch(t *testing.T) {
	resetAboutPageStateForTest()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("AppData", filepath.Join(home, "config"))
	old := t.TempDir()
	parent := t.TempDir()
	t.Setenv("MCODE_DATA", old)
	if err := datadir.Export(old, false); err != nil {
		t.Fatal(err)
	}
	var relaunched bool
	oldRelaunch := appRelaunch
	appRelaunch = func() { relaunched = true }
	t.Cleanup(func() { appRelaunch = oldRelaunch })
	st := NewSettingsState(old, nil, nil)
	confirmAboutDataDirMove(st, filepath.Join(parent, "MCode"))
	if !relaunched {
		t.Fatal("成功迁移后应重启")
	}
	if got := os.Getenv("MCODE_DATA"); got != "" {
		t.Fatalf("非固定目录重启前应清理环境变量: %q", got)
	}
}

func (f *fakeUpdateClient) LatestUpdate() AppUpdateStatus { return f.latest }

func (f *fakeUpdateClient) CheckForUpdate(ctx context.Context) (AppUpdateStatus, error) {
	f.calls++
	return f.status, f.err
}

func (f *fakeUpdateClient) AppVersion() string { return f.version }

type fakeUpdateInstallClient struct {
	*fakeUpdateClient
	err error
}

func (f *fakeUpdateInstallClient) InstallUpdate(_ context.Context, progress func(float64)) error {
	progress(0.5)
	return f.err
}

func TestAboutInstallUpdate(t *testing.T) {
	resetAboutPageStateForTest()
	fc := &fakeUpdateInstallClient{fakeUpdateClient: &fakeUpdateClient{
		FakeClient: NewFakeClient(), version: "1.0.0",
		latest: AppUpdateStatus{Latest: "2.0.0", URL: "https://example.com", Available: true, Installable: true},
	}}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fc))
	tt := ui.NewTester(func(c *ui.Context) { renderPageAbout(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("更新并重启") })
	if _, ok := tt.Find("btn-about-install-update"); !ok || tt.HasText("前往下载页") {
		t.Fatal("可安装更新应显示更新按钮而不是下载页")
	}
	globalAboutState.mu.Lock()
	globalAboutState.installing = true
	globalAboutState.mu.Unlock()
	runAppUpdateInstall(st, fc)
	globalAboutState.mu.Lock()
	installing, progress, errMsg := globalAboutState.installing, globalAboutState.progress, globalAboutState.errMsg
	globalAboutState.mu.Unlock()
	if installing || progress != 0.5 || errMsg != "" {
		t.Fatalf("安装成功状态错误: %v %v %q", installing, progress, errMsg)
	}
	fc.err = errors.New("安装失败")
	globalAboutState.mu.Lock()
	globalAboutState.installing = true
	globalAboutState.mu.Unlock()
	runAppUpdateInstall(st, fc)
	globalAboutState.mu.Lock()
	installing, errMsg = globalAboutState.installing, globalAboutState.errMsg
	globalAboutState.mu.Unlock()
	if installing || errMsg != "安装失败" {
		t.Fatalf("安装失败状态错误: %v %q", installing, errMsg)
	}
}

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
	tt := ui.NewTester(func(c *ui.Context) { renderPageAbout(c, st) }, 1000, 4000)

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

func TestSettingsAbout_BackgroundUpdate(t *testing.T) {
	resetAboutPageStateForTest()
	fc := &fakeUpdateClient{FakeClient: NewFakeClient(), version: "1.0.0", latest: AppUpdateStatus{Latest: "1.1.0", URL: "https://example.com", Available: true}}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fc))
	tt := ui.NewTester(func(c *ui.Context) { renderPageAbout(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool {
		return tt.HasText("发现新版本 v1.1.0") && tt.HasText("前往下载页") && tt.HasText("自动检查更新")
	})
	if fc.calls != 0 {
		t.Fatal("后台结果不应触发手动检查")
	}
	if !updateAvailable(fc) || updateAvailable(NewFakeClient()) {
		t.Fatal("更新提示状态不正确")
	}
	fc.latest.Available = false
	if updateAvailable(fc) {
		t.Fatal("没有新版本时不应显示提示")
	}
}

func TestSettingsNavUpdateDot(t *testing.T) {
	fc := &fakeUpdateClient{FakeClient: NewFakeClient()}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fc))
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsNavItem(c, st, settingsNavDef{id: "about", title: "关于", icon: IconInfo()}, nil)
	}, 400, 100)
	tt.Frame()
	if _, ok := tt.Find("settings-nav-update-dot"); ok {
		t.Fatal("没有新版本时不应显示圆点")
	}
	fc.latest.Available = true
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	if _, ok := tt.Find("有新版本"); !ok {
		t.Fatal("发现新版本时应显示圆点")
	}
}

func TestSidebarUpdateCapsule(t *testing.T) {
	fc := &fakeUpdateClient{FakeClient: NewFakeClient()}
	chat := newChatState(fc)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	setChatSettings(chat, st)
	tt := ui.NewTester(func(c *ui.Context) { renderSidebarBottomBar(c, chat) }, 400, 100)

	tt.Frame()
	if _, ok := tt.Find("有新版本 9.9.9"); ok {
		t.Fatal("没有新版本时侧栏设置按钮不应显示版本胶囊")
	}
	fc.latest.Available = true
	fc.latest.Latest = "9.9.9"
	tt.Frame()
	tt.Advance(200 * time.Millisecond)
	if _, ok := tt.Find("有新版本 9.9.9"); !ok {
		t.Fatal("发现新版本时侧栏设置按钮应显示版本胶囊")
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
	tt := ui.NewTester(func(c *ui.Context) { renderPageAbout(c, st) }, 1000, 4000)

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
