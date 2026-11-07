package mcodeui

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// stewardFakeClient 在内存假 Client 上补齐总管接口：记录创建次数，可配置返回的会话 ID 与错误，
// 并可用 gate 模拟首次创建耗时，用来检查防重入；昵称与头像也可配置，用来验证身份回落。
type stewardFakeClient struct {
	*FakeClient

	stewardMu sync.Mutex
	threadID  string        // 当前总管会话 ID，空串表示还没有
	openID    string        // OpenStewardThread 成功后返回并写入 threadID 的 ID
	openErr   error         // 非 nil 时创建返回该错误
	opens     int           // OpenStewardThread 被调用的次数
	gate      chan struct{} // 非 nil 时创建阻塞到它被关闭
	name      string        // StewardIdentity 返回的昵称原文，空串表示让界面回落「总管」
	avatar    string        // StewardIdentity 返回的头像字符，空串表示取 stewardName(nickname) 首字
}

var _ StewardClient = (*stewardFakeClient)(nil)

func newStewardFakeClient(threadID string) *stewardFakeClient {
	return &stewardFakeClient{FakeClient: NewFakeClient(), threadID: threadID, openID: "t1"}
}

// stewardTestThreadID 是总管用例里用的会话 ID：故意和默认选中的 t1 区分开，
// 否则「跳转到总管」的断言在默认状态下就已经成立，测不出任何东西。
const stewardTestThreadID = "t2"

// addStewardTestThread 往假客户端里加一个比 t1 旧的会话，并把创建后的总管 ID 指向它。
// t1 更近更新，所以启动时默认选中的仍是 t1。
func addStewardTestThread(fake *stewardFakeClient) {
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{"p1": {
			{ID: "t1", ProjectID: "p1", Title: "测试会话1", UpdatedAt: now},
			{ID: stewardTestThreadID, ProjectID: "p1", Title: "测试会话2", UpdatedAt: now.Add(-time.Hour)},
		}},
	)
	fake.openID = stewardTestThreadID
}

func (f *stewardFakeClient) StewardThreadID() string {
	f.stewardMu.Lock()
	defer f.stewardMu.Unlock()
	return f.threadID
}

func (f *stewardFakeClient) OpenStewardThread(_ context.Context) (string, error) {
	f.stewardMu.Lock()
	f.opens++
	gate := f.gate
	f.stewardMu.Unlock()

	if gate != nil {
		<-gate
	}

	f.stewardMu.Lock()
	defer f.stewardMu.Unlock()
	if f.openErr != nil {
		return "", f.openErr
	}
	f.threadID = f.openID
	return f.openID, nil
}

func (f *stewardFakeClient) StewardIdentity() (string, string) {
	f.stewardMu.Lock()
	defer f.stewardMu.Unlock()
	return f.name, f.avatar
}

// openCalls 在锁内读取创建次数。
func (f *stewardFakeClient) openCalls() int {
	f.stewardMu.Lock()
	defer f.stewardMu.Unlock()
	return f.opens
}

// 场景 1：客户端没实现总管接口时不渲染总管段，侧栏其余内容照常。
func TestSidebarStewardEntryHiddenWithoutStewardClient(t *testing.T) {
	state, view := buildRoot(NewFakeClient())
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if tt.HasText("总管") {
		t.Fatalf("未实现 StewardClient 时不应渲染总管段，当前文本: %q", tt.Texts())
	}
}

// 场景 2：总管会话还不存在时不渲染总管段，免得没启用的人多出一行。
func TestSidebarStewardEntryHiddenWithoutThread(t *testing.T) {
	state, view := buildRoot(newStewardFakeClient(""))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })
	if tt.HasText("总管") {
		t.Fatalf("总管会话 ID 为空时不应渲染总管段，当前文本: %q", tt.Texts())
	}
}

// 场景 3：会话已存在时渲染合并行，点击后跳到该会话。
func TestSidebarStewardEntryClickRevealsThread(t *testing.T) {
	fake := newStewardFakeClient(stewardTestThreadID)
	addStewardTestThread(fake)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		// 等总管段和默认选中都就绪，免得启动期的默认选中把后面的跳转盖掉。
		return tt.HasText("总管") && selectedThreadForTest(state) == "t1"
	})
	if err := tt.Click("总管"); err != nil {
		t.Fatalf("点击总管段失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == stewardTestThreadID })
	if calls := fake.openCalls(); calls != 0 {
		t.Fatalf("会话已存在时不应创建，实际创建 %d 次", calls)
	}
}

// 场景 4：还没有会话时点击会在后台创建，创建完成后跳到新会话。
func TestStewardOpenCreatesThreadAndReveals(t *testing.T) {
	fake := newStewardFakeClient("")
	addStewardTestThread(fake)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	// 等启动期默认选中完成，免得默认选中 t1 盖掉后面的跳转。
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	state.openStewardThread()

	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == stewardTestThreadID })
	if calls := fake.openCalls(); calls != 1 {
		t.Fatalf("应只创建一次，实际创建 %d 次", calls)
	}
	// 创建完成后总管段出现。
	waitFor(t, tt, func() bool { return tt.HasText("总管") })
}

// 场景 5：创建失败时用 Toast 提示错误，不跳转。
func TestStewardOpenShowsError(t *testing.T) {
	fake := newStewardFakeClient("")
	fake.openErr = errors.New("总管未启用")
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	// 等启动期默认选中完成，再记下当前会话，失败时比对有没有被改掉。
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	before := selectedThreadForTest(state)
	state.openStewardThread()

	waitFor(t, tt, func() bool { return tt.HasText("总管未启用") })
	if selected := selectedThreadForTest(state); selected != before {
		t.Fatalf("创建失败时不应跳转，选中从 %q 变成 %q", before, selected)
	}
	if pending := takeStewardPendingThread(state); pending != "" {
		t.Fatalf("创建失败时不应登记待跳转，实际登记 %q", pending)
	}
}

// 场景 6：创建还没完成时再次点击不会发起第二次创建。
func TestStewardOpenIgnoresRepeatedClicks(t *testing.T) {
	fake := newStewardFakeClient("")
	addStewardTestThread(fake)
	gate := make(chan struct{})
	fake.gate = gate
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	// 断言提前失败时也要放行后台创建，避免 goroutine 一直挂住。
	t.Cleanup(release)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	// 等启动期默认选中完成，再点两次，避免默认选中与后续跳转互相覆盖。
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	state.openStewardThread()
	state.openStewardThread()
	tt.Frame()

	release()
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == stewardTestThreadID })
	for i := 0; i < 5; i++ {
		tt.Frame()
	}
	if calls := fake.openCalls(); calls != 1 {
		t.Fatalf("连续点击应只创建一次，实际创建 %d 次", calls)
	}
}

// 场景 7：侧栏折叠时后台创建完成也要跳转；跳转不能依赖总管段渲染。
func TestStewardOpenRevealsWhenSidebarCollapsed(t *testing.T) {
	fake := newStewardFakeClient("")
	addStewardTestThread(fake)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	state.mu.Lock()
	state.sidebarCollapsed = true
	state.mu.Unlock()
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == "t1" })
	state.openStewardThread()

	// 折叠态下总管段不渲染，只有每帧的 consumeStewardPendingThread 能完成跳转。
	waitFor(t, tt, func() bool { return selectedThreadForTest(state) == stewardTestThreadID })
	state.mu.Lock()
	collapsed := state.sidebarCollapsed
	state.mu.Unlock()
	if !collapsed {
		t.Fatalf("跳转不应顺带展开侧栏")
	}
	if calls := fake.openCalls(); calls != 1 {
		t.Fatalf("应只创建一次，实际创建 %d 次", calls)
	}
}

// 客户端不实现可选接口时：今日提醒次数按拿不到处理，启用状态按未知处理，
// 界面据此保持原来的兜底渲染，不会凭空画未启用入口。
func TestStewardOptionalClientsAbsent(t *testing.T) {
	s := newChatState(newStewardFakeClient(""))
	if _, _, ok := s.stewardWakesToday(); ok {
		t.Fatal("客户端不支持今日提醒次数时应为 false")
	}
	if _, known := s.stewardEnabledState(); known {
		t.Fatal("客户端不报告启用状态时应为未知")
	}
}

// stewardWakesFakeClient 给可选接口的读取路径一个会报数、也会报未启用的替身。
type stewardWakesFakeClient struct {
	*stewardFakeClient
	enabled     bool
	used, limit int
	ok          bool
}

func (f *stewardWakesFakeClient) StewardEnabled() bool { return f.enabled }

func (f *stewardWakesFakeClient) StewardWakesToday() (int, int, bool) {
	return f.used, f.limit, f.ok
}

// 客户端报告未启用时，今日提醒次数与启用状态都按未启用处理。
func TestStewardWakesTodayDisabled(t *testing.T) {
	fake := &stewardWakesFakeClient{stewardFakeClient: newStewardFakeClient(""), enabled: false, limit: 5}
	s := newChatState(fake)
	if _, _, ok := s.stewardWakesToday(); ok {
		t.Fatal("未启用时应为 false")
	}
	if enabled, known := s.stewardEnabledState(); !known || enabled {
		t.Fatalf("应报告未知/未启用，实际 enabled=%v known=%v", enabled, known)
	}
}
