package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// archiveRecordClient 在 FakeClient 之上记录批量操作调用，供多选批量测试断言「每条各调用一次」。
type archiveRecordClient struct {
	*FakeClient

	mu      sync.Mutex
	restore []string
	deleted []string
	failDel map[string]bool
}

// newArchiveRecordClient 包一层记录型客户端。
func newArchiveRecordClient(fake *FakeClient) *archiveRecordClient {
	return &archiveRecordClient{FakeClient: fake, failDel: make(map[string]bool)}
}

// SetThreadArchived 记录恢复（archived=false）的会话 ID 后转发给 FakeClient。
func (c *archiveRecordClient) SetThreadArchived(threadID string, archived bool) error {
	c.mu.Lock()
	if !archived {
		c.restore = append(c.restore, threadID)
	}
	c.mu.Unlock()
	return c.FakeClient.SetThreadArchived(threadID, archived)
}

// DeleteThread 记录删除的会话 ID；failDel 里的会话返回错误，用于验证失败项保持选中。
func (c *archiveRecordClient) DeleteThread(threadID string) error {
	c.mu.Lock()
	c.deleted = append(c.deleted, threadID)
	fail := c.failDel[threadID]
	c.mu.Unlock()
	if fail {
		return fmt.Errorf("模拟删除失败")
	}
	return c.FakeClient.DeleteThread(threadID)
}

// calls 返回到目前为止的恢复与删除调用（副本）。
func (c *archiveRecordClient) calls() (restore, deleted []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.restore...), append([]string(nil), c.deleted...)
}

// seedArchiveState 预置归档页状态：已加载 + 归档列表 + 项目名映射。
func seedArchiveState(t *testing.T, threads []Thread, projects map[string]string) {
	t.Helper()
	resetArchivePageStateForTest()
	globalArchiveState.mu.Lock()
	defer globalArchiveState.mu.Unlock()
	globalArchiveState.loaded = true
	globalArchiveState.threads = append([]Thread(nil), threads...)
	globalArchiveState.projects = projects
}

// archivePageTester 与 conversationSheetsTester 同构，窗口给到 1000×4000，避免归档行与弹窗被裁掉。
func archivePageTester(st *SettingsState) *ui.Tester {
	return ui.NewTester(func(c *ui.Context) {
		renderPageConversation(c, st)
		if id, _ := settingsTreeSheetState(st); id != "" {
			if sheet, ok := settingsTreeSheetOf(id); ok {
				renderSettingsTreeSheet(c, st, sheet)
			}
		}
	}, 1000, 4000)
}

// newArchivePageTester 直接渲染归档页（不经设置导航），窗口给足高度避免行被滚动裁掉。
func newArchivePageTester(t *testing.T, st *SettingsState) *ui.Tester {
	t.Helper()
	tt := archivePageTester(st)
	globalArchiveState.mu.Lock()
	count := len(globalArchiveState.threads)
	globalArchiveState.mu.Unlock()
	openConversationSheet(t, tt, fmt.Sprintf("已归档会话 %d 个", count))
	return tt
}

// openArchivePageInSettings 通过设置面板导航到归档页，返回记录型客户端。
func openArchivePageInSettings(t *testing.T, archivedIDs []string) (*ui.Tester, *archiveRecordClient) {
	t.Helper()
	resetArchivePageStateForTest()
	fake := NewFakeClient()
	p := Project{ID: "p1", Name: "项目甲"}
	now := time.Now()
	threads := make([]Thread, 0, len(archivedIDs))
	for _, id := range archivedIDs {
		threads = append(threads, Thread{ID: id, ProjectID: "p1", Title: "归档-" + id, UpdatedAt: now})
	}
	fake.SetProjectsAndThreads([]Project{p}, map[string][]Thread{"p1": threads})
	for _, id := range archivedIDs {
		if err := fake.SetThreadArchived(id, true); err != nil {
			t.Fatalf("SetThreadArchived %s failed: %v", id, err)
		}
	}
	rec := newArchiveRecordClient(fake)

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(rec))
	tt := archivePageTester(st)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find(fmt.Sprintf("已归档会话 %d 个", len(archivedIDs)))
		return ok
	})
	openConversationSheet(t, tt, fmt.Sprintf("已归档会话 %d 个", len(archivedIDs)))
	waitFor(t, tt, func() bool { return tt.HasText("归档-" + archivedIDs[0]) })
	return tt, rec
}

// waitForArchiveBulk 等批量操作结束（bulkRunning 复位），最多 2 秒。
func waitForArchiveBulk(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		globalArchiveState.mu.Lock()
		running := globalArchiveState.bulkRunning
		globalArchiveState.mu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("批量操作未在 2 秒内结束")
}

// 搜索按标题过滤（不区分大小写），无匹配时给出提示而不是空白。
func TestArchiveSearchFiltersByTitle(t *testing.T) {
	now := time.Now()
	seedArchiveState(t, []Thread{
		{ID: "th-1", ProjectID: "p1", Title: "Fix Login Bug", UpdatedAt: now},
		{ID: "th-2", ProjectID: "p1", Title: "支付重写", UpdatedAt: now},
	}, map[string]string{"p1": "项目甲"})
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	globalArchiveState.mu.Lock()
	globalArchiveState.query = "支付"
	globalArchiveState.mu.Unlock()

	tt := newArchivePageTester(t, st)
	// tester 找不到按 Key 命名的元素，行是否保留改按行标题文字判断。
	if !tt.HasText("支付重写") {
		t.Fatalf("命中搜索词的行应保留，texts: %v", tt.Texts())
	}
	if tt.HasText("Fix Login Bug") {
		t.Fatal("不匹配搜索词的行应被过滤")
	}

	// 大小写不敏感：小写关键字仍命中英文标题。
	globalArchiveState.mu.Lock()
	globalArchiveState.query = "fix login"
	globalArchiveState.mu.Unlock()
	tt.Frame()
	if !tt.HasText("Fix Login Bug") {
		t.Fatalf("搜索应不区分大小写，texts: %v", tt.Texts())
	}
	if tt.HasText("支付重写") {
		t.Fatal("不匹配搜索词的行应被过滤")
	}

	// 搜索词也能命中项目名。
	globalArchiveState.mu.Lock()
	globalArchiveState.query = "项目甲"
	globalArchiveState.mu.Unlock()
	tt.Frame()
	if !tt.HasText("Fix Login Bug") {
		t.Fatal("搜索词命中项目名时该项目的会话应保留")
	}

	// 无匹配时给出提示。
	globalArchiveState.mu.Lock()
	globalArchiveState.query = "不存在的关键字"
	globalArchiveState.mu.Unlock()
	tt.Frame()
	if _, ok := tt.Find("row-archive-no-match"); !ok {
		t.Fatalf("搜索无匹配时应显示提示行，texts: %v", tt.Texts())
	}
}

// 归档列表按项目分组：每个项目一个分组标题，未知项目归入「未关联项目」。
func TestArchiveGroupedByProject(t *testing.T) {
	now := time.Now()
	seedArchiveState(t, []Thread{
		{ID: "th-a1", ProjectID: "p1", Title: "甲一", UpdatedAt: now},
		{ID: "th-a2", ProjectID: "p1", Title: "甲二", UpdatedAt: now.Add(-2 * time.Minute)},
		{ID: "th-b1", ProjectID: "p2", Title: "乙一", UpdatedAt: now.Add(-time.Minute)},
		{ID: "th-x", ProjectID: "unknown", Title: "孤儿", UpdatedAt: now},
	}, map[string]string{"p1": "项目甲", "p2": "项目乙"})
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	tt := newArchivePageTester(t, st)
	for _, key := range []string{"archive-group-p1", "archive-group-p2", "archive-group-unknown"} {
		if _, ok := tt.Find(key); !ok {
			t.Fatalf("缺少项目分组标题 %s，texts: %v", key, tt.Texts())
		}
	}
	if !tt.HasText("项目甲") || !tt.HasText("项目乙") {
		t.Fatalf("分组标题应显示项目名，texts: %v", tt.Texts())
	}
	if !tt.HasText(archiveUnknownProjectName) {
		t.Fatalf("未知项目应归入「%s」分组，texts: %v", archiveUnknownProjectName, tt.Texts())
	}
	if !tt.HasText("2 个会话") {
		t.Fatalf("分组标题应显示会话数量，texts: %v", tt.Texts())
	}
}

// 勾选多条后点「恢复」：每个选中会话各调用一次 SetThreadArchived(id, false)，未选中的不动。
func TestArchiveBulkRestoreCallsClient(t *testing.T) {
	tt, rec := openArchivePageInSettings(t, []string{"th-1", "th-2", "th-3"})

	if err := tt.Click("archive-check-th-1"); err != nil {
		t.Fatalf("click archive-check-th-1 failed: %v", err)
	}
	tt.Frame()
	if err := tt.Click("archive-check-th-2"); err != nil {
		t.Fatalf("click archive-check-th-2 failed: %v", err)
	}
	tt.Frame()
	if !tt.HasText("已选 2 项") {
		t.Fatalf("批量条应显示已选数量，texts: %v", tt.Texts())
	}
	if err := tt.Click("批量恢复"); err != nil {
		t.Fatalf("click 批量恢复 failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		restore, _ := rec.calls()
		return len(restore) == 2
	})
	restore, deleted := rec.calls()
	if len(restore) != 2 || restore[0] != "th-1" || restore[1] != "th-2" {
		t.Fatalf("恢复调用 = %v, want [th-1 th-2]", restore)
	}
	if len(deleted) != 0 {
		t.Fatalf("恢复不应调用删除，实际 %v", deleted)
	}
}

// 批量删除必须先确认：确认前、取消后都不删除；确认后逐条删除每个选中会话。
func TestArchiveBulkDeleteRequiresConfirm(t *testing.T) {
	tt, rec := openArchivePageInSettings(t, []string{"th-1", "th-2"})

	if err := tt.Click("archive-check-th-1"); err != nil {
		t.Fatalf("click archive-check-th-1 failed: %v", err)
	}
	tt.Frame()
	if err := tt.Click("archive-check-th-2"); err != nil {
		t.Fatalf("click archive-check-th-2 failed: %v", err)
	}
	tt.Frame()

	// 打开批量删除：只弹确认框，不删除。
	if err := tt.Click("批量删除"); err != nil {
		t.Fatalf("click 批量删除 failed: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("确认删除已归档会话"); !ok {
		t.Fatalf("批量删除应先弹确认框，texts: %v", tt.Texts())
	}
	if _, deleted := rec.calls(); len(deleted) != 0 {
		t.Fatalf("确认前不应删除，实际删除了 %v", deleted)
	}

	// 取消：仍然不删除，选择保留。
	if err := tt.Click("取消删除已归档会话"); err != nil {
		t.Fatalf("click 取消 failed: %v", err)
	}
	tt.Frame()
	if _, deleted := rec.calls(); len(deleted) != 0 {
		t.Fatalf("取消后不应删除，实际删除了 %v", deleted)
	}
	if !tt.HasText("归档-th-1") {
		t.Fatalf("取消后列表应保持不变，texts: %v", tt.Texts())
	}

	// 再次打开并确认：逐条删除。
	if err := tt.Click("批量删除"); err != nil {
		t.Fatalf("click 批量删除 failed: %v", err)
	}
	tt.Frame()
	if err := tt.Click("确认删除已归档会话"); err != nil {
		t.Fatalf("click 确认删除 failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, deleted := rec.calls()
		return len(deleted) == 2
	})
	_, deleted := rec.calls()
	if len(deleted) != 2 {
		t.Fatalf("确认后应逐条删除，调用 = %v", deleted)
	}
}

// 批量删除失败项保持勾选，并显示汇总与失败原因。
func TestArchiveBulkDeleteKeepsFailedSelected(t *testing.T) {
	resetArchivePageStateForTest()
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	fake := NewFakeClient()
	p := Project{ID: "p1", Name: "项目甲"}
	now := time.Now()
	fake.SetProjectsAndThreads([]Project{p}, map[string][]Thread{"p1": {
		{ID: "th-1", ProjectID: "p1", Title: "归档一", UpdatedAt: now},
		{ID: "th-2", ProjectID: "p1", Title: "归档二", UpdatedAt: now},
	}})
	for _, id := range []string{"th-1", "th-2"} {
		if err := fake.SetThreadArchived(id, true); err != nil {
			t.Fatalf("SetThreadArchived %s failed: %v", id, err)
		}
	}
	rec := newArchiveRecordClient(fake)
	rec.failDel["th-2"] = true
	archived, err := rec.ArchivedThreads(context.Background())
	if err != nil {
		t.Fatalf("ArchivedThreads failed: %v", err)
	}

	globalArchiveState.mu.Lock()
	globalArchiveState.loaded = true
	globalArchiveState.threads = archived
	globalArchiveState.projects = map[string]string{"p1": "项目甲"}
	globalArchiveState.picked = map[string]bool{"th-1": true, "th-2": true}
	globalArchiveState.mu.Unlock()

	runArchiveBulk(st, rec, archiveBulkDelete)
	waitForArchiveBulk(t)

	globalArchiveState.mu.Lock()
	running := globalArchiveState.bulkRunning
	result := globalArchiveState.bulkResult
	failed := append([]string(nil), globalArchiveState.bulkFailed...)
	keptFailed := globalArchiveState.picked["th-2"]
	keptSucceeded := globalArchiveState.picked["th-1"]
	globalArchiveState.mu.Unlock()

	if running {
		t.Fatal("批量结束后应复位忙态")
	}
	if !keptFailed {
		t.Fatal("失败项应保持勾选")
	}
	if keptSucceeded {
		t.Fatal("成功项应取消勾选")
	}
	if len(failed) != 1 || !strings.Contains(failed[0], "归档二") || !strings.Contains(failed[0], "模拟删除失败") {
		t.Fatalf("失败原因应带标题与错误，实际为 %v", failed)
	}
	if !strings.Contains(result, "1 个失败") {
		t.Fatalf("结果汇总 = %q，应包含失败数", result)
	}

	tt := newArchivePageTester(t, st)
	if _, ok := tt.Find("row-archive-bulk-result"); !ok {
		t.Fatalf("页面上应显示批量结果汇总，texts: %v", tt.Texts())
	}
	if !tt.HasText(result) {
		t.Fatalf("页面应显示汇总文案 %q，texts: %v", result, tt.Texts())
	}
}

// 全选与取消选择：全选勾中当前列表全部会话，取消选择清空勾选。
func TestArchiveSelectAllAndClear(t *testing.T) {
	now := time.Now()
	seedArchiveState(t, []Thread{
		{ID: "th-1", ProjectID: "p1", Title: "会话一", UpdatedAt: now},
		{ID: "th-2", ProjectID: "p1", Title: "会话二", UpdatedAt: now.Add(-time.Minute)},
	}, map[string]string{"p1": "项目甲"})
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	tt := newArchivePageTester(t, st)
	if _, ok := tt.Find("archive-bulk-bar"); !ok {
		t.Fatalf("归档页应有批量条，texts: %v", tt.Texts())
	}

	if err := tt.Click("批量全选"); err != nil {
		t.Fatalf("click 全选 failed: %v", err)
	}
	tt.Frame()
	globalArchiveState.mu.Lock()
	allPicked := globalArchiveState.picked["th-1"] && globalArchiveState.picked["th-2"]
	count := len(globalArchiveState.picked)
	globalArchiveState.mu.Unlock()
	if !allPicked || count != 2 {
		t.Fatalf("全选后应勾中两条，picked=%v", allPicked)
	}
	if !tt.HasText("已选 2 项") {
		t.Fatalf("批量条应显示已选 2 项，texts: %v", tt.Texts())
	}

	if err := tt.Click("批量取消选择"); err != nil {
		t.Fatalf("click 取消选择 failed: %v", err)
	}
	tt.Frame()
	globalArchiveState.mu.Lock()
	left := len(globalArchiveState.picked)
	globalArchiveState.mu.Unlock()
	if left != 0 {
		t.Fatalf("取消选择后应清空勾选，仍有 %d 条", left)
	}
	if !tt.HasText("已选 0 项") {
		t.Fatalf("批量条应显示已选 0 项，texts: %v", tt.Texts())
	}
}
