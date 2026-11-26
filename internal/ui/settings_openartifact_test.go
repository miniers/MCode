package mcodeui

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/openartifact/noderuntime"
)

type fakeOpenArtifactClient struct {
	*FakeClient

	mu            sync.Mutex
	nodeStatus    noderuntime.Status
	nodeCalls     atomic.Int32
	downloadFn    func(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error)
	downloadCalls atomic.Int32
	enabledCalls  []bool
	setEnabledErr error
	onSetEnabled  func(enabled bool)
	ready         bool
	stateErr      error
	homeURL       string
	homeErr       error
	homeURLCalls  atomic.Int32
}

func (f *fakeOpenArtifactClient) OpenArtifactNodeStatus(context.Context) noderuntime.Status {
	f.nodeCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodeStatus
}

func (f *fakeOpenArtifactClient) DownloadOpenArtifactNode(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error) {
	f.downloadCalls.Add(1)
	f.mu.Lock()
	fn := f.downloadFn
	st := f.nodeStatus
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, progress)
	}
	return st, nil
}

func (f *fakeOpenArtifactClient) SetOpenArtifactEnabled(enabled bool) error {
	f.mu.Lock()
	f.enabledCalls = append(f.enabledCalls, enabled)
	err := f.setEnabledErr
	cb := f.onSetEnabled
	f.mu.Unlock()
	if cb != nil {
		cb(enabled)
	}
	return err
}

func (f *fakeOpenArtifactClient) OpenArtifactState() (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ready, f.stateErr
}

func (f *fakeOpenArtifactClient) OpenArtifactHomeURL(context.Context) (string, error) {
	f.homeURLCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.homeURL, f.homeErr
}

func (f *fakeOpenArtifactClient) recordedEnabledCalls() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]bool(nil), f.enabledCalls...)
}

func newOpenArtifactTestState(t *testing.T, client Client) *SettingsState {
	t.Helper()
	resetOpenArtifactUIForTest()
	t.Cleanup(resetOpenArtifactUIForTest)
	return NewSettingsState(settingsTestDir(t), nil, newChatState(client))
}

func TestSettingsOpenArtifact_HiddenWhenClientUnsupported(t *testing.T) {
	st := newOpenArtifactTestState(t, NewFakeClient())
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	tt.Frame()
	if tt.HasText("Artifact") || tt.HasText("打开主页") || tt.HasText("下载 Node") {
		t.Fatalf("未实现 OpenArtifactClient 时不应渲染 Artifact 分组: %v", tt.Texts())
	}

	items := settingsItems()
	for _, id := range []string{"row-artifact-enabled", "row-artifact-node", "row-artifact-home"} {
		var found bool
		for _, item := range items {
			if item.ID == id {
				found = true
				if settingsItemVisible(st, item) {
					t.Errorf("未实现接口时 %s 应隐藏", id)
				}
			}
		}
		if !found {
			t.Fatalf("搜索索引中缺少 %s", id)
		}
	}

	supported := newOpenArtifactTestState(t, &fakeOpenArtifactClient{FakeClient: NewFakeClient()})
	for _, query := range []string{"artifact", "open-artifact", "node", "白板", "幻灯片", "交付物"} {
		if hits := matchVisibleSettingsItemsIn(supported, query, items); len(hits) == 0 {
			t.Errorf("实现接口后搜索 %q 应命中 Artifact 分组行", query)
		}
	}
}

func TestSettingsOpenArtifact_NodeOKShowsVersionWithoutDownload(t *testing.T) {
	client := &fakeOpenArtifactClient{
		FakeClient: NewFakeClient(),
		nodeStatus: noderuntime.Status{
			Path:    "/usr/local/bin/node",
			Version: "22.18.0",
			OK:      true,
		},
	}
	st := newOpenArtifactTestState(t, client)
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool {
		return tt.HasText("v22.18.0") && tt.HasText("/usr/local/bin/node")
	})
	if tt.HasText("下载 Node") {
		t.Fatalf("Node 可用时不应显示下载按钮: %v", tt.Texts())
	}

	tt.Frame()
	tt.Frame()
	if got := client.nodeCalls.Load(); got != 1 {
		t.Fatalf("多次渲染应缓存 Node 状态，实际探测次数 = %d", got)
	}
}

func TestSettingsOpenArtifact_NodeMissingAndDownloadFlow(t *testing.T) {
	setAppReduceMotion(t, false)
	release := make(chan struct{})
	client := &fakeOpenArtifactClient{
		FakeClient: NewFakeClient(),
		nodeStatus: noderuntime.Status{},
		downloadFn: func(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error) {
			progress(50, 100)
			progress(50, 100)
			<-release
			return noderuntime.Status{
				Path:    "/data/runtimes/node/22.18.0/bin/node",
				Version: "22.18.0",
				Managed: true,
				OK:      true,
			}, nil
		},
	}
	st := newOpenArtifactTestState(t, client)
	st.cfgMu.Lock()
	st.cfg.OpenArtifact.Enabled = true
	st.cfgMu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool {
		return tt.HasText("未找到（需要 >= "+noderuntime.MinVersion+"）") && tt.HasText("下载 Node")
	})

	if err := tt.Click("下载 Node"); err != nil {
		t.Fatalf("点击下载 Node 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("下载中 50%") && tt.HasText("取消")
	})
	tt.Advance(DurBase + time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("进度条到达目标百分比并推进时钟后不应继续请求下一帧")
	}

	close(release)
	waitFor(t, tt, func() bool {
		return tt.HasText("v22.18.0") && tt.HasText("/data/runtimes/node/22.18.0/bin/node") && !tt.HasText("下载 Node")
	})
	if client.downloadCalls.Load() != 1 {
		t.Fatalf("期望调用 1 次 DownloadOpenArtifactNode，实际 %d", client.downloadCalls.Load())
	}
	calls := client.recordedEnabledCalls()
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("开关已开启且桥接未就绪时下载成功后应触发 SetOpenArtifactEnabled(true)，实际 = %v", calls)
	}
}

func TestSettingsOpenArtifact_CancelNodeDownload(t *testing.T) {
	started := make(chan struct{})
	client := &fakeOpenArtifactClient{
		FakeClient: NewFakeClient(),
		nodeStatus: noderuntime.Status{},
		downloadFn: func(ctx context.Context, progress func(done, total int64)) (noderuntime.Status, error) {
			progress(20, 100)
			close(started)
			<-ctx.Done()
			return noderuntime.Status{}, ctx.Err()
		},
	}
	st := newOpenArtifactTestState(t, client)
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("下载 Node") })

	if err := tt.Click("下载 Node"); err != nil {
		t.Fatalf("点击下载 Node 失败: %v", err)
	}
	<-started
	waitFor(t, tt, func() bool { return tt.HasText("取消") })
	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点击取消失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("下载 Node") && !tt.HasText("取消") })
	if tt.HasText("context canceled") {
		t.Fatal("用户取消下载不应显示 context canceled 错误")
	}
}

func TestSettingsOpenArtifact_ToggleSyncsDraftAndShowsBridgeError(t *testing.T) {
	dir := settingsTestDir(t)
	resetOpenArtifactUIForTest()
	t.Cleanup(resetOpenArtifactUIForTest)

	client := &fakeOpenArtifactClient{
		FakeClient: NewFakeClient(),
		nodeStatus: noderuntime.Status{Path: "/usr/local/bin/node", Version: "22.18.0", OK: true},
		onSetEnabled: func(enabled bool) {
			_ = config.Update(dir, func(cfg *config.Config) error {
				cfg.OpenArtifact.Enabled = enabled
				return nil
			})
		},
	}
	st := NewSettingsState(dir, nil, newChatState(client))

	// 模拟同页文本项尚在防抖草稿期，防止之后 flushTextSave 把 open_artifact.enabled 覆盖回去。
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.Workbench.BrowserQuickPorts = "3000"
	st.cfgMu.Unlock()
	st.textSavePending = true
	st.textSaveDue = time.Now().Add(time.Minute)
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("v22.18.0") })

	if err := tt.Click("切换启用"); err != nil {
		t.Fatalf("点击启用开关失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return len(client.recordedEnabledCalls()) == 1
	})
	if calls := client.recordedEnabledCalls(); !calls[0] {
		t.Fatalf("期望调用 SetOpenArtifactEnabled(true)，实际 %v", calls)
	}
	if !st.flushTextSave(true) {
		t.Fatalf("落盘文本草稿失败: %s", st.errorMessage)
	}
	diskCfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("读取磁盘配置失败: %v", err)
	}
	if !diskCfg.OpenArtifact.Enabled || diskCfg.Workbench.BrowserQuickPorts != "3000" {
		t.Fatalf("草稿落盘覆盖了 open_artifact.enabled 或丢失了文本修改: %+v", diskCfg)
	}

	// 验证开启后桥接启动失败时在本行显示 OpenArtifactState 返回的错误。
	client.mu.Lock()
	client.ready = false
	client.stateErr = errors.New("bridge start failed")
	client.mu.Unlock()
	if err := tt.Click("切换启用"); err != nil {
		t.Fatalf("关闭开关失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		info, ok := tt.Access("切换启用")
		return len(client.recordedEnabledCalls()) == 2 && ok && !info.Disabled && !info.Checked
	})
	if err := tt.Click("切换启用"); err != nil {
		t.Fatalf("重新打开开关失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("bridge start failed") })
}

func TestSettingsOpenArtifact_OpenHomeButtonStateAndRouting(t *testing.T) {
	oldRunOnMain := openArtifactRunOnMain
	oldActiveWin := openArtifactActiveChatWindow
	oldPreviewTab := openArtifactPreviewNewTab
	oldExternal := openArtifactOpenExternalURL
	t.Cleanup(func() {
		openArtifactRunOnMain = oldRunOnMain
		openArtifactActiveChatWindow = oldActiveWin
		openArtifactPreviewNewTab = oldPreviewTab
		openArtifactOpenExternalURL = oldExternal
	})

	client := &fakeOpenArtifactClient{
		FakeClient: NewFakeClient(),
		nodeStatus: noderuntime.Status{Path: "/usr/local/bin/node", Version: "22.18.0", OK: true},
		ready:      false,
		homeURL:    "http://127.0.0.1:4800/",
	}
	st := newOpenArtifactTestState(t, client)
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("v22.18.0") })

	info, ok := tt.AccessByRole(ui.RoleButton, "打开主页")
	if !ok || !info.Disabled {
		t.Fatalf("未就绪时「打开主页」应禁用: info=%+v ok=%v", info, ok)
	}

	// 切换为就绪后重新加载状态，确认按钮可点并走当前窗口右侧浏览器新标签。
	client.mu.Lock()
	client.ready = true
	client.mu.Unlock()
	resetOpenArtifactUIForTest()

	mainRan := make(chan struct{}, 1)
	var gotTabChat *chatState
	var gotTabURL string
	var gotExternalURL string
	win := &mygo.Window{}
	activeChat := newChatState(client)
	openArtifactActiveChatWindow = func() (*mygo.Window, *chatState) { return win, activeChat }
	openArtifactRunOnMain = func(fn func()) {
		fn()
		select {
		case mainRan <- struct{}{}:
		default:
		}
	}
	openArtifactPreviewNewTab = func(s *chatState, raw string) {
		gotTabChat = s
		gotTabURL = raw
	}
	openArtifactOpenExternalURL = func(raw string) {
		gotExternalURL = raw
	}

	tt.Frame()
	waitFor(t, tt, func() bool {
		btn, exists := tt.AccessByRole(ui.RoleButton, "打开主页")
		return exists && !btn.Disabled
	})
	if err := tt.Click("打开主页"); err != nil {
		t.Fatalf("点击打开主页失败: %v", err)
	}
	select {
	case <-mainRan:
	case <-time.After(2 * time.Second):
		t.Fatal("未在主线程打开右侧浏览器新标签")
	}
	if gotTabChat != activeChat || gotTabURL != "http://127.0.0.1:4800/" || gotExternalURL != "" {
		t.Fatalf("窗口打开参数错误: chat=%v url=%q external=%q", gotTabChat == activeChat, gotTabURL, gotExternalURL)
	}

	// 无聊天窗口时退回系统浏览器打开。
	extDone := make(chan string, 1)
	openArtifactActiveChatWindow = func() (*mygo.Window, *chatState) { return nil, nil }
	openArtifactOpenExternalURL = func(raw string) {
		extDone <- raw
	}
	waitFor(t, tt, func() bool {
		btn, exists := tt.AccessByRole(ui.RoleButton, "打开主页")
		return exists && !btn.Disabled
	})
	if err := tt.Click("打开主页"); err != nil {
		t.Fatalf("无窗口时点击打开主页失败: %v", err)
	}
	select {
	case got := <-extDone:
		if got != "http://127.0.0.1:4800/" {
			t.Fatalf("系统浏览器打开地址 = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("无聊天窗口时未调用系统浏览器打开函数")
	}
}
