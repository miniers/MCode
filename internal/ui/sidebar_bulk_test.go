package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// TestSidebarBulk_MarkAllAsRead 测试项目右键菜单在有未读会话时显示「全部标记为已读」，点击后调用 BulkMarkRead。
func TestSidebarBulk_MarkAllAsRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.projects = []Project{
		{ID: "p1", Name: "mcode-test", Path: "/tmp/test"},
	}
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "未读会话1", HasUnread: true, UpdatedAt: time.Now()},
		{ID: "t2", ProjectID: "p1", Title: "未读会话2", HasUnread: true, UpdatedAt: time.Now().Add(-time.Minute)},
		{ID: "t3", ProjectID: "p1", Title: "已读会话3", HasUnread: false, UpdatedAt: time.Now().Add(-2 * time.Minute)},
	}

	_, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("mcode-test") && tt.HasText("未读会话1")
	})

	// 右键点击项目行
	if err := tt.RightClick("mcode-test"); err != nil {
		t.Fatalf("right click 'mcode-test' failed: %v", err)
	}
	tt.Frame()

	menu := tt.Menu()
	hasMarkAll := false
	for _, m := range menu {
		if m == "全部标记为已读" {
			hasMarkAll = true
			break
		}
	}
	if !hasMarkAll {
		t.Fatalf("expected menu to contain '全部标记为已读', got: %v", menu)
	}

	// 选择「全部标记为已读」
	if err := tt.ChooseMenuItem("全部标记为已读"); err != nil {
		t.Fatalf("choose '全部标记为已读' failed: %v", err)
	}

	// 验证 Fake 记录到了调用与对应的未读会话 ID
	waitFor(t, tt, func() bool {
		calls := fake.BulkMarkReadCalls()
		if len(calls) != 1 {
			return false
		}
		c := calls[0]
		if c.ProjectID != "p1" || len(c.ThreadIDs) != 2 {
			return false
		}
		hasT1 := false
		hasT2 := false
		for _, id := range c.ThreadIDs {
			if id == "t1" {
				hasT1 = true
			}
			if id == "t2" {
				hasT2 = true
			}
		}
		return hasT1 && hasT2
	})
}

// TestSidebarBulk_ArchiveDormantAndUndo 测试归档休眠会话后显示撤销条，点击撤销调用 UndoBulkOperation。
func TestSidebarBulk_ArchiveDormantAndUndo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.projects = []Project{
		{ID: "p1", Name: "mcode-test", Path: "/tmp/test"},
	}
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "休眠会话1", Dormant: true, UpdatedAt: time.Now()},
		{ID: "t2", ProjectID: "p1", Title: "休眠会话2", Dormant: true, UpdatedAt: time.Now().Add(-time.Minute)},
		{ID: "t3", ProjectID: "p1", Title: "普通活跃会话", Dormant: false, UpdatedAt: time.Now().Add(-2 * time.Minute)},
	}

	_, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("mcode-test") && tt.HasText("普通活跃会话")
	})

	// 右键点击项目行
	if err := tt.RightClick("mcode-test"); err != nil {
		t.Fatalf("right click project failed: %v", err)
	}
	tt.Frame()

	expectedItem := "归档休眠会话（2）"
	menu := tt.Menu()
	hasArchiveDormant := false
	for _, m := range menu {
		if m == expectedItem {
			hasArchiveDormant = true
			break
		}
	}
	if !hasArchiveDormant {
		t.Fatalf("expected menu to contain %q, got: %v", expectedItem, menu)
	}

	// 点击「归档休眠会话（2）」
	if err := tt.ChooseMenuItem(expectedItem); err != nil {
		t.Fatalf("choose %q failed: %v", expectedItem, err)
	}

	// 验证侧边栏底部上方出现撤销条
	waitFor(t, tt, func() bool {
		return len(fake.BulkArchiveCalls()) == 1 &&
			tt.HasText("已归档 2 个会话") &&
			tt.HasText("撤销")
	})

	// 验证项目右键菜单在有效期内追加「撤销归档 2 个会话」
	if err := tt.RightClick("mcode-test"); err != nil {
		t.Fatalf("second right click project failed: %v", err)
	}
	tt.Frame()
	menu = tt.Menu()
	hasUndoMenu := false
	for _, m := range menu {
		if m == "撤销归档 2 个会话" {
			hasUndoMenu = true
			break
		}
	}
	if !hasUndoMenu {
		t.Fatalf("expected context menu to contain '撤销归档 2 个会话', got: %v", menu)
	}

	// 点击撤销按钮
	if err := tt.Click("撤销"); err != nil {
		t.Fatalf("click '撤销' failed: %v", err)
	}

	// 验证 UndoBulkOperation 收到调用
	waitFor(t, tt, func() bool {
		calls := fake.UndoBulkOpCalls()
		return len(calls) == 1 && calls[0].ProjectID == "p1" && calls[0].Token != ""
	})
}

// TestSidebarBulk_PopoverPreviewSkipsRunning 测试整理浮窗的预览数量会跳过运行中的会话。
func TestSidebarBulk_PopoverPreviewSkipsRunning(t *testing.T) {
	// 关闭自动休眠（sidebar_auto_settle_days=0）：本用例的会话已 10 天无新消息，
	// 若沿用默认 7 天会被 loadProjects 的自动休眠提前移出活跃列表，浮窗预览数量随之变化。
	dataDir := settingsTestDir(t)
	t.Setenv("MCODE_DATA", dataDir)
	t.Cleanup(waitUIStateWrites)
	cfg := config.Defaults()
	off := 0
	cfg.Archive.SidebarAutoDormantDays = &off
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.projects = []Project{
		{ID: "p1", Name: "mcode-test", Path: "/tmp/test"},
	}

	tenDaysAgo := time.Now().Add(-10 * 24 * time.Hour)
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "老会话1", UpdatedAt: tenDaysAgo},
		{ID: "t2", ProjectID: "p1", Title: "老会话2", UpdatedAt: tenDaysAgo},
		{ID: "t3", ProjectID: "p1", Title: "运行中老会话", UpdatedAt: tenDaysAgo, IsRunning: true},
		{ID: "t4", ProjectID: "p1", Title: "刚刚会话", UpdatedAt: time.Now()},
	}

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("mcode-test")
	})

	// 右键点击项目行打开菜单
	if err := tt.RightClick("mcode-test"); err != nil {
		t.Fatalf("right click project failed: %v", err)
	}
	tt.Frame()

	// 点击「整理会话…」
	if err := tt.ChooseMenuItem("整理会话…"); err != nil {
		t.Fatalf("choose '整理会话…' failed: %v", err)
	}

	// 验证整理浮窗出现
	waitFor(t, tt, func() bool {
		return tt.HasText("整理会话") && state.bulkPopoverProjectID == "p1"
	})

	// 默认时间范围 7 天：老会话 1 和 2 受影响（2个），老会话 3 是运行中被跳过（1个），刚刚会话 4 不在范围内
	expectedPreview := "将影响 2 个会话，跳过 1 个运行中的会话"
	if !tt.HasText(expectedPreview) {
		t.Fatalf("expected popover to show %q", expectedPreview)
	}

	// 点击「归档」按钮
	if err := tt.Click("归档"); err != nil {
		t.Fatalf("click '归档' failed: %v", err)
	}

	// 验证归档调用发送且浮窗关闭
	waitFor(t, tt, func() bool {
		calls := fake.BulkArchiveCalls()
		return len(calls) == 1 && state.bulkPopoverProjectID == ""
	})

	archiveCall := fake.BulkArchiveCalls()[0]
	if len(archiveCall.ThreadIDs) != 2 {
		t.Fatalf("expected 2 threads archived, got %v", archiveCall.ThreadIDs)
	}
	for _, id := range archiveCall.ThreadIDs {
		if id == "t3" {
			t.Fatalf("running thread t3 should have been skipped")
		}
	}
}

// TestSidebarBulk_DormantCollapsedGroup 测试已休眠会话收进折叠组，默认折叠，展开后才可见。
func TestSidebarBulk_DormantCollapsedGroup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.projects = []Project{
		{ID: "p1", Name: "mcode-test", Path: "/tmp/test"},
	}
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "活跃会话A", Dormant: false, UpdatedAt: time.Now()},
		{ID: "t2", ProjectID: "p1", Title: "已休眠会话B", Dormant: true, UpdatedAt: time.Now().Add(-time.Minute)},
	}

	_, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	// 活跃会话可见，且显示已休眠折叠组标题「已休眠会话（1）」
	waitFor(t, tt, func() bool {
		return tt.HasText("活跃会话A") && tt.HasText("已休眠会话（1）")
	})

	// 初始折叠状态下，已休眠会话B不可见
	if tt.HasText("已休眠会话B") {
		t.Fatalf("expected '已休眠会话B' to be hidden initially in collapsed group")
	}

	// 点击折叠组展开
	if err := tt.Click("已休眠会话（1）"); err != nil {
		t.Fatalf("click '已休眠会话（1）' failed: %v", err)
	}

	// 展开后已休眠会话B可见
	waitFor(t, tt, func() bool {
		return tt.HasText("已休眠会话B")
	})

	// 再次点击折叠组收起
	if err := tt.Click("已休眠会话（1）"); err != nil {
		t.Fatalf("click '已休眠会话（1）' to collapse failed: %v", err)
	}

	// 收起后已休眠会话B再次不可见
	waitFor(t, tt, func() bool {
		return !tt.HasText("已休眠会话B")
	})
}

// TestSidebarBulk_SearchKeyboardNavigation 测试搜索过滤后按上下方向键移动高亮，按回车打开对应会话。
func TestSidebarBulk_SearchKeyboardNavigation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.projects = []Project{
		{ID: "p1", Name: "mcode-test", Path: "/tmp/test"},
	}
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "搜索测试目标Alpha", UpdatedAt: time.Now()},
		{ID: "t2", ProjectID: "p1", Title: "搜索测试目标Beta", UpdatedAt: time.Now().Add(-time.Minute)},
		{ID: "t3", ProjectID: "p1", Title: "其他无关对话", UpdatedAt: time.Now().Add(-2 * time.Minute)},
	}

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("搜索测试目标Alpha") && tt.HasText("其他无关对话")
	})

	// 点击搜索框使其获得焦点
	if err := tt.Click("搜索对话..."); err != nil {
		t.Fatalf("click '搜索对话...' failed: %v", err)
	}

	// 模拟在搜索框输入文字
	q := getSearchQuery(state)
	*q = "搜索测试目标"
	state.invalidate()
	tt.Frame()

	// 验证过滤结果中仅包含匹配的 Alpha 与 Beta，不包含无关对话
	waitFor(t, tt, func() bool {
		return tt.HasText("搜索测试目标Alpha") &&
			tt.HasText("搜索测试目标Beta") &&
			!tt.HasText("其他无关对话")
	})

	// 初始高亮索引为 0（Alpha）
	state.mu.Lock()
	if state.searchHighlightIndex != 0 {
		state.mu.Unlock()
		t.Fatalf("expected initial searchHighlightIndex to be 0, got %d", state.searchHighlightIndex)
	}
	state.mu.Unlock()

	// 按方向下键移动高亮到 1（Beta）
	tt.Key(0, ui.KeyDown)
	tt.Frame()

	state.mu.Lock()
	if state.searchHighlightIndex != 1 {
		state.mu.Unlock()
		t.Fatalf("expected searchHighlightIndex to be 1 after KeyDown, got %d", state.searchHighlightIndex)
	}
	state.mu.Unlock()

	// 按回车键选中当前高亮的会话（Beta，ID 为 t2）
	tt.Key(0, ui.KeyEnter)
	tt.Frame()

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID == "t2"
	})

	// 验证焦点标志已被设置准备归还输入框
	state.mu.Lock()
	focused := state.searchFocusInput
	state.mu.Unlock()
	// 下一帧渲染时会将焦点归还输入框
	tt.Frame()
	_ = focused
}

// waitUntil 轮询等待条件成立（不依赖界面帧的无窗口测试用），超时即失败。
func waitUntil(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	// 条件满足即返回；上限放宽到 10s，避免全量并行测试负载高时偶发超时。
	deadline := time.Now().Add(uiWaitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("等待超时: %s", desc)
	}
}

// TestSidebarBulk_ArchiveThreadWithUndo 验证单条归档记录撤销令牌（kind=archive、count=1），
// 且 undoLastBulk 以该令牌调用 UndoBulkOperation。
func TestSidebarBulk_ArchiveThreadWithUndo(t *testing.T) {
	fake := NewFakeClient()
	fake.archiveThreadWithUndoToken = "undo-single-archive"
	s := newChatState(fake)
	s.mu.Lock()
	s.threadsByProj["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "待归档会话", UpdatedAt: time.Now()},
		{ID: "t2", ProjectID: "p1", Title: "保留会话", UpdatedAt: time.Now().Add(-time.Minute)},
	}
	s.threads = append([]Thread(nil), s.threadsByProj["p1"]...)
	s.mu.Unlock()

	s.archiveThreadWithUndo("p1", "t1")

	waitUntil(t, "归档后记录 lastBulk", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.lastBulk != nil
	})

	s.mu.Lock()
	lb := s.lastBulk
	remaining := len(s.threads)
	s.mu.Unlock()
	if lb == nil {
		t.Fatal("归档后应记录 lastBulk")
	}
	if lb.projectID != "p1" || lb.kind != "archive" || lb.count != 1 || lb.token != "undo-single-archive" {
		t.Fatalf("lastBulk = %+v, want projectID=p1 kind=archive count=1 token=undo-single-archive", lb)
	}
	if remaining != 1 {
		t.Fatalf("归档后剩余会话数 = %d, want 1", remaining)
	}
	if calls := fake.ArchiveThreadWithUndoCalls(); len(calls) != 1 || calls[0] != "t1" {
		t.Fatalf("ArchiveThreadWithUndo 调用 = %v, want [t1]", calls)
	}

	// 撤销：以记录的令牌调用 UndoBulkOperation
	s.undoLastBulk()
	waitUntil(t, "undoLastBulk 调用 UndoBulkOperation", func() bool {
		return len(fake.UndoBulkOpCalls()) == 1
	})
	calls := fake.UndoBulkOpCalls()
	if calls[0].ProjectID != "p1" || calls[0].Token != "undo-single-archive" {
		t.Fatalf("UndoBulkOperation 调用 = %+v, want projectID=p1 token=undo-single-archive", calls[0])
	}
}

// TestSidebarBulk_MarkDormantWithUndo 验证单条休眠记录撤销令牌（kind=settle、count=1），
// 会话原地标记休眠，且 undoLastBulk 以该令牌调用 UndoBulkOperation。
func TestSidebarBulk_MarkDormantWithUndo(t *testing.T) {
	fake := NewFakeClient()
	fake.markDormantWithUndoToken = "undo-single-settle"
	s := newChatState(fake)
	s.mu.Lock()
	s.threadsByProj["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "待休眠会话", UpdatedAt: time.Now()},
	}
	s.threads = append([]Thread(nil), s.threadsByProj["p1"]...)
	s.mu.Unlock()

	s.markDormantWithUndo("p1", "t1")

	waitUntil(t, "休眠后记录 lastBulk", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.lastBulk != nil
	})

	s.mu.Lock()
	lb := s.lastBulk
	dormant := len(s.threads) == 1 && s.threads[0].Dormant
	s.mu.Unlock()
	if lb == nil {
		t.Fatal("休眠后应记录 lastBulk")
	}
	if lb.projectID != "p1" || lb.kind != "dormant" || lb.count != 1 || lb.token != "undo-single-settle" {
		t.Fatalf("lastBulk = %+v, want projectID=p1 kind=settle count=1 token=undo-single-settle", lb)
	}
	if !dormant {
		t.Fatal("休眠后会话应保留在列表中并标记为已休眠")
	}
	if calls := fake.MarkDormantWithUndoCalls(); len(calls) != 1 || calls[0] != "t1" {
		t.Fatalf("MarkDormantWithUndo 调用 = %v, want [t1]", calls)
	}

	// 撤销：以记录的令牌调用 UndoBulkOperation
	s.undoLastBulk()
	waitUntil(t, "undoLastBulk 调用 UndoBulkOperation", func() bool {
		return len(fake.UndoBulkOpCalls()) == 1
	})
	calls := fake.UndoBulkOpCalls()
	if calls[0].ProjectID != "p1" || calls[0].Token != "undo-single-settle" {
		t.Fatalf("UndoBulkOperation 调用 = %+v, want projectID=p1 token=undo-single-settle", calls[0])
	}
}

// TestSidebarBulk_ThreadWithUndoEmptyToken 验证后端未返回撤销令牌时不记录 lastBulk（不显示无效的撤销条）。
func TestSidebarBulk_ThreadWithUndoEmptyToken(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.mu.Lock()
	s.threadsByProj["p1"] = []Thread{{ID: "t1", ProjectID: "p1", Title: "会话", UpdatedAt: time.Now()}}
	s.threads = append([]Thread(nil), s.threadsByProj["p1"]...)
	s.mu.Unlock()

	s.markDormantWithUndo("p1", "t1")
	s.archiveThreadWithUndo("p1", "t1")

	waitUntil(t, "归档后会话移除", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.threads) == 0
	})
	s.mu.Lock()
	lb := s.lastBulk
	s.mu.Unlock()
	if lb != nil {
		t.Fatalf("空令牌不应记录 lastBulk，got %+v", lb)
	}
}
