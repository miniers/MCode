package mcodeui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSettingsArchive_EmptyState(t *testing.T) {
	resetArchivePageStateForTest()
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	st := NewSettingsState(dataDir, nil, newChatState(fake))
	tt := archivePageTester(st)
	waitFor(t, tt, func() bool { _, ok := tt.Find("已归档会话 0 个"); return ok })
	openConversationSheet(t, tt, "已归档会话 0 个")

	waitFor(t, tt, func() bool {
		return tt.HasText("暂无已归档会话")
	})
}

func TestSettingsArchive_RestoreAndDelete(t *testing.T) {
	resetArchivePageStateForTest()
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	p := Project{ID: "proj-1", Name: "DemoProject", Path: "/path/to/proj"}
	now := time.Now()
	th1 := Thread{ID: "th-1", ProjectID: "proj-1", Title: "归档会话1", UpdatedAt: now}
	th2 := Thread{ID: "th-2", ProjectID: "proj-1", Title: "归档会话2", UpdatedAt: now}
	fake.SetProjectsAndThreads([]Project{p}, map[string][]Thread{
		"proj-1": {th1, th2},
	})

	// 归档这两个会话
	if err := fake.SetThreadArchived("th-1", true); err != nil {
		t.Fatalf("SetThreadArchived th-1 failed: %v", err)
	}
	if err := fake.SetThreadArchived("th-2", true); err != nil {
		t.Fatalf("SetThreadArchived th-2 failed: %v", err)
	}

	ctx := context.Background()
	archived, err := fake.ArchivedThreads(ctx)
	if err != nil {
		t.Fatalf("ArchivedThreads failed: %v", err)
	}
	if len(archived) != 2 {
		t.Fatalf("expected 2 archived threads, got %d", len(archived))
	}

	st := NewSettingsState(dataDir, nil, newChatState(fake))
	tt := archivePageTester(st)
	waitFor(t, tt, func() bool { _, ok := tt.Find("已归档会话 2 个"); return ok })
	openConversationSheet(t, tt, "已归档会话 2 个")

	waitFor(t, tt, func() bool {
		return tt.HasText("归档会话1") && tt.HasText("归档会话2")
	})

	// 通过面板恢复第一条。
	if err := tt.Click("恢复会话 归档会话1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("归档会话1") })
	remaining, err := fake.ArchivedThreads(ctx)
	if err != nil {
		t.Fatalf("ArchivedThreads after restore failed: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "th-2" {
		t.Fatalf("expected 1 remaining thread (th-2), got %+v", remaining)
	}

	// 删除剩余会话并确认。
	if err := tt.Click("删除会话 归档会话2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认删除单个已归档会话"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("暂无已归档会话") })
	afterDelete, err := fake.ArchivedThreads(ctx)
	if err != nil {
		t.Fatalf("ArchivedThreads after delete failed: %v", err)
	}
	if len(afterDelete) != 0 {
		t.Fatalf("expected 0 archived threads, got %+v", afterDelete)
	}
}

// archiveFailClient 让指定会话的 DeleteThread 失败，用于验证批量删除的逐条结果（S-111）。
type archiveFailClient struct {
	*FakeClient
	fail map[string]bool
}

func (c archiveFailClient) DeleteThread(threadID string) error {
	if c.fail[threadID] {
		return fmt.Errorf("模拟删除失败")
	}
	return c.FakeClient.DeleteThread(threadID)
}

// 「全部删除」必须逐条汇总：成功的计数、失败的列出原因，并复位忙态（S-109 / S-111）。
func TestArchiveDeleteAll_ReportsPerThreadResult(t *testing.T) {
	resetArchivePageStateForTest()
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	fake := NewFakeClient()
	p := Project{ID: "p1", Name: "项目"}
	fake.SetProjectsAndThreads([]Project{p}, map[string][]Thread{
		"p1": {
			{ID: "th-1", ProjectID: "p1", Title: "会话一"},
			{ID: "th-2", ProjectID: "p1", Title: "会话二"},
			{ID: "th-3", ProjectID: "p1", Title: "会话三"},
		},
	})
	for _, id := range []string{"th-1", "th-2", "th-3"} {
		if err := fake.SetThreadArchived(id, true); err != nil {
			t.Fatalf("SetThreadArchived %s failed: %v", id, err)
		}
	}

	client := archiveFailClient{FakeClient: fake, fail: map[string]bool{"th-2": true}}
	archived, err := client.ArchivedThreads(context.Background())
	if err != nil {
		t.Fatalf("ArchivedThreads failed: %v", err)
	}

	deleteAllArchivedThreads(st, client, archived)

	globalArchiveState.mu.Lock()
	deleted := globalArchiveState.deleteAllDeleted
	result := globalArchiveState.deleteAllResult
	failed := append([]string(nil), globalArchiveState.deleteAllFailed...)
	deletingAll := globalArchiveState.deletingAll
	globalArchiveState.mu.Unlock()

	if deletingAll {
		t.Fatal("删除结束后应复位忙态")
	}
	if deleted != 2 {
		t.Fatalf("成功删除数 = %d, want 2", deleted)
	}
	if len(failed) != 1 || !strings.Contains(failed[0], "会话二") || !strings.Contains(failed[0], "模拟删除失败") {
		t.Fatalf("失败原因应逐条列出会话标题与错误，实际为 %+v", failed)
	}
	if !strings.Contains(result, "已删除 2") || !strings.Contains(result, "1 个失败") {
		t.Fatalf("结果汇总 = %q，应包含成功数与失败数", result)
	}

	remaining, err := fake.ArchivedThreads(context.Background())
	if err != nil {
		t.Fatalf("ArchivedThreads failed: %v", err)
	}
	if len(remaining) != 1 || remaining[0].ID != "th-2" {
		t.Fatalf("删除失败的会话应保留，实际剩余 %+v", remaining)
	}
	if got := archiveDeleteAllResultText(0, failed); !strings.HasPrefix(got, "删除失败") {
		t.Fatalf("全部失败时文案应以「删除失败」开头，实际为 %q", got)
	}
}

// 单条写操作失败时，失败原因要在「已归档会话」区块内显示（S-109）。
func TestArchivePage_ShowsOperationErrors(t *testing.T) {
	resetArchivePageStateForTest()
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	globalArchiveState.mu.Lock()
	globalArchiveState.loaded = true
	globalArchiveState.threads = []Thread{{ID: "th-1", Title: "会话一"}}
	globalArchiveState.opErr = "删除失败：网络中断"
	globalArchiveState.deleteAllDeleted = 1
	globalArchiveState.deleteAllFailed = []string{"会话二：网络中断"}
	globalArchiveState.deleteAllResult = archiveDeleteAllResultText(1, globalArchiveState.deleteAllFailed)
	globalArchiveState.mu.Unlock()

	tt := archivePageTester(st)
	openConversationSheet(t, tt, "已归档会话 1 个")

	if !tt.HasText("删除失败：网络中断") {
		t.Fatalf("单条删除失败原因应显示在区块内: %v", tt.Texts())
	}
	if !tt.HasText("会话二：网络中断") {
		t.Fatalf("批量删除的逐条失败原因应显示在区块内: %v", tt.Texts())
	}
}

// 加载窗口内到来的刷新请求不能丢：记 pending 后由工作线程补拉一次（S-112）。
func TestArchiveLoad_PendingReloadAfterInFlight(t *testing.T) {
	resetArchivePageStateForTest()

	if !beginArchiveLoad() {
		t.Fatal("首次开始加载应成功")
	}
	if beginArchiveLoad() {
		t.Fatal("加载中的第二个请求不应再起一个加载")
	}
	globalArchiveState.mu.Lock()
	pending := globalArchiveState.pendingReload
	globalArchiveState.mu.Unlock()
	if !pending {
		t.Fatal("加载中的刷新请求应记为待补拉")
	}

	fake := NewFakeClient()
	p := Project{ID: "p1", Name: "项目"}
	fake.SetProjectsAndThreads([]Project{p}, map[string][]Thread{
		"p1": {{ID: "th-1", ProjectID: "p1", Title: "会话一"}},
	})
	if err := fake.SetThreadArchived("th-1", true); err != nil {
		t.Fatalf("SetThreadArchived failed: %v", err)
	}

	fetchArchivedThreadsWorker(nil, fake)

	globalArchiveState.mu.Lock()
	defer globalArchiveState.mu.Unlock()
	if globalArchiveState.loading || globalArchiveState.pendingReload || !globalArchiveState.loaded {
		t.Fatalf("补拉结束后应复位状态: loading=%v pending=%v loaded=%v",
			globalArchiveState.loading, globalArchiveState.pendingReload, globalArchiveState.loaded)
	}
	if len(globalArchiveState.threads) != 1 || globalArchiveState.threads[0].ID != "th-1" {
		t.Fatalf("补拉后应拿到最新归档列表，实际为 %+v", globalArchiveState.threads)
	}
}

// 归档页数据只加载一次：成功快照多帧复用；失败后等用户点「重试」才再次加载。
func TestSettingsArchive_DataLoadOnce(t *testing.T) {
	resetArchivePageStateForTest()
	oldFetch := archiveThreadsFetch
	t.Cleanup(func() { archiveThreadsFetch = oldFetch })

	var calls atomic.Int32
	var fail atomic.Bool
	archiveThreadsFetch = func(ctx context.Context, client Client) ([]Thread, map[string]string, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, nil, errors.New("网络中断")
		}
		return oldFetch(ctx, client)
	}

	fake := NewFakeClient()
	p := Project{ID: "p1", Name: "项目甲"}
	fake.SetProjectsAndThreads([]Project{p}, map[string][]Thread{
		"p1": {{ID: "th-1", ProjectID: "p1", Title: "归档会话一", UpdatedAt: time.Now()}},
	})
	if err := fake.SetThreadArchived("th-1", true); err != nil {
		t.Fatalf("SetThreadArchived failed: %v", err)
	}
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))

	render := func() *ui.Tester {
		return ui.NewTester(func(c *ui.Context) { renderPageArchive(c, st) }, 1000, 4000)
	}
	tt := render()
	waitFor(t, tt, func() bool { return tt.HasText("归档会话一") })
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("成功加载应只发生一次，实际 %d 次", got)
	}

	// 失败一轮：不动用户操作就不重试。
	resetArchivePageStateForTest()
	fail.Store(true)
	tt = render()
	waitFor(t, tt, func() bool { return tt.HasText("加载失败：网络中断") })
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("加载失败后不应自动重试，实际 %d 次", got)
	}

	// 用户点重试：再次加载并展示列表。
	fail.Store(false)
	if err := tt.Click("重新加载已归档会话"); err != nil {
		t.Fatalf("点击重试失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("归档会话一") })
	if got := calls.Load(); got != 3 {
		t.Fatalf("重试应再加载一次，实际 %d 次", got)
	}
}
