package mcodeui

import (
	"sync"
	"testing"

	"github.com/egoist/mygo"
)

// foregroundReporterClient 是只实现前台会话上报的假客户端，其余方法沿用 FakeClient。
type foregroundReporterClient struct {
	*FakeClient

	mu  sync.Mutex
	got []string
}

// SetForegroundThread 记录一次上报，供测试断言上报序列。
func (c *foregroundReporterClient) SetForegroundThread(threadID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, threadID)
}

// reported 返回已收到的上报序列副本。
func (c *foregroundReporterClient) reported() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.got...)
}

// stubForegroundFocused 把窗口前台判定换成 focused 指向的开关，并让上报同步执行；
// 测试结束后还原包级钩子。返回上报被派发的次数（未实现接口时应当保持 0）。
func stubForegroundFocused(t *testing.T, focused *bool) *int {
	t.Helper()
	oldFocused := foregroundWindowFocused
	oldDispatch := foregroundReportDispatch
	dispatch := 0
	foregroundWindowFocused = func(win *mygo.Window) bool { return *focused }
	foregroundReportDispatch = func(fn func()) {
		dispatch++
		fn()
	}
	t.Cleanup(func() {
		foregroundWindowFocused = oldFocused
		foregroundReportDispatch = oldDispatch
	})
	return &dispatch
}

// selectThreadForTest 直接设置选中会话，绕开会触发数据加载的界面流程。
func selectThreadForTest(s *chatState, threadID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.selectedThreadID = threadID
}

// TestForegroundReportSkippedWithoutReporter 覆盖客户端未实现上报接口的情况：什么都不做。
func TestForegroundReportSkippedWithoutReporter(t *testing.T) {
	focused := true
	dispatch := stubForegroundFocused(t, &focused)
	s := newChatState(NewFakeClient())
	selectThreadForTest(s, "t1")

	s.reportForegroundThread()

	if *dispatch != 0 {
		t.Fatalf("客户端未实现 ForegroundThreadReporter 时不该上报，实际派发 %d 次", *dispatch)
	}
}

// TestForegroundReportSelectedThreadWhenFocused 覆盖选中会话且窗口在前台时上报该会话 ID。
func TestForegroundReportSelectedThreadWhenFocused(t *testing.T) {
	focused := true
	stubForegroundFocused(t, &focused)
	client := &foregroundReporterClient{FakeClient: NewFakeClient()}
	s := newChatState(client)
	selectThreadForTest(s, "t1")

	s.reportForegroundThread()

	got := client.reported()
	if len(got) != 1 || got[0] != "t1" {
		t.Fatalf("期望上报 [t1]，实际 %v", got)
	}
}

// TestForegroundReportEmptyWhenBlurred 覆盖窗口失焦后上报空串（表示用户没在看任何会话）。
func TestForegroundReportEmptyWhenBlurred(t *testing.T) {
	focused := true
	stubForegroundFocused(t, &focused)
	client := &foregroundReporterClient{FakeClient: NewFakeClient()}
	s := newChatState(client)
	selectThreadForTest(s, "t1")
	s.reportForegroundThread()

	focused = false
	s.reportForegroundThread()

	got := client.reported()
	if len(got) != 2 || got[0] != "t1" || got[1] != "" {
		t.Fatalf("期望上报 [t1 ]（先 t1 后空串），实际 %v", got)
	}
}

// TestForegroundReportEmptyWithoutSelectedThread 覆盖窗口在前台但没选中会话的情况：算出的空串
// 与初始记账值相同，所以初次不上报；选中会话上报过 ID 之后再取消选中，才把空串报出去。
func TestForegroundReportEmptyWithoutSelectedThread(t *testing.T) {
	focused := true
	stubForegroundFocused(t, &focused)
	client := &foregroundReporterClient{FakeClient: NewFakeClient()}
	s := newChatState(client)

	// 记账初始值就是空串，第一次算出的空串不该上报。
	s.reportForegroundThread()
	if got := client.reported(); len(got) != 0 {
		t.Fatalf("初次计算结果为空串时不该上报，实际 %v", got)
	}

	// 选中会话后正常上报 ID，取消选中再回到空串。
	selectThreadForTest(s, "t1")
	s.reportForegroundThread()
	selectThreadForTest(s, "")
	s.reportForegroundThread()

	got := client.reported()
	if len(got) != 2 || got[0] != "t1" || got[1] != "" {
		t.Fatalf("期望上报 [t1 ]（先 t1 后空串），实际 %v", got)
	}
}

// TestForegroundReportDeduplicatesUnchangedValue 覆盖值不变时不重复上报。
func TestForegroundReportDeduplicatesUnchangedValue(t *testing.T) {
	focused := true
	dispatch := stubForegroundFocused(t, &focused)
	client := &foregroundReporterClient{FakeClient: NewFakeClient()}
	s := newChatState(client)
	selectThreadForTest(s, "t1")

	s.reportForegroundThread()
	s.reportForegroundThread()
	s.reportForegroundThread()

	if got := client.reported(); len(got) != 1 || got[0] != "t1" {
		t.Fatalf("值未变化时只应上报一次，实际 %v", got)
	}
	if *dispatch != 1 {
		t.Fatalf("值未变化时只应派发一次，实际 %d 次", *dispatch)
	}

	// 换一个会话后必须重新上报。
	selectThreadForTest(s, "t2")
	s.reportForegroundThread()
	if got := client.reported(); len(got) != 2 || got[1] != "t2" {
		t.Fatalf("选中会话变化后应上报 [t1 t2]，实际 %v", got)
	}
}

// TestForegroundReportWithoutWindow 覆盖状态没有窗口句柄（测试、嵌入会话）时走空串分支，
// 这里用真实的前台判定，验证窗口为 nil 时不会去碰原生窗口接口。
func TestForegroundReportWithoutWindow(t *testing.T) {
	oldDispatch := foregroundReportDispatch
	foregroundReportDispatch = func(fn func()) { fn() }
	t.Cleanup(func() { foregroundReportDispatch = oldDispatch })
	client := &foregroundReporterClient{FakeClient: NewFakeClient()}
	s := newChatState(client)
	selectThreadForTest(s, "t1")

	// 没有窗口时永远算作不在前台，算出的空串与初始记账值相同，因此不上报。
	s.reportForegroundThread()
	if got := client.reported(); len(got) != 0 {
		t.Fatalf("没有窗口时初次计算结果为空串，不该上报，实际 %v", got)
	}

	// 之前上报过会话 ID（比如窗口还在时），失去窗口后要把空串报回去。
	s.mu.Lock()
	s.reportedForegroundThread = "t1"
	s.mu.Unlock()
	s.reportForegroundThread()

	got := client.reported()
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("没有窗口时不该上报会话 ID，期望上报 [ ]（空串），实际 %v", got)
	}
}
