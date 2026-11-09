package mcodeui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
)

// (a) TestKanbanWorkspace_Empty：SetKanbanBoard 设空看板，打开任务工作区，断言出现“还没有任务”。
func TestKanbanWorkspace_Empty(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("任务与看板")
	})

	if err := tt.Click("任务与看板"); err != nil {
		t.Fatalf("click '任务与看板' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("还没有任务")
	})

	// 空态直接给出新建入口，文案指向两个按钮
	waitFor(t, tt, func() bool {
		return tt.HasText("新建一个任务，或用目标让 Agent 拆解规划")
	})
	// 紧凑工具栏使用图标按钮，文字节点数量会变化；按空态按钮的无障碍名称检查并点击。
	for _, item := range []struct{ label, sheet string }{
		{"空态新建任务", "kanban-new-task-sheet"},
		{"空态新建目标", "kanban-new-goal-sheet"},
	} {
		if _, ok := tt.Find(item.label); !ok {
			t.Fatalf("找不到空态按钮 %s", item.label)
		}
		if err := tt.Click(item.label); err != nil {
			t.Fatalf("点击空态按钮 %s 失败: %v", item.label, err)
		}
		waitFor(t, tt, func() bool {
			_, ok := tt.Find(item.sheet)
			return ok
		})
		tt.Key(0, ui.KeyEscape)
		waitFor(t, tt, func() bool {
			_, ok := tt.Find(item.sheet)
			return !ok
		})
	}
	// 空态说明文字只有空态框里有。
	if n := exactTextCount(tt, "新建一个任务，或用目标让 Agent 拆解规划"); n != 1 {
		t.Fatalf("empty state description should appear once, got %d: %v", n, tt.Texts())
	}
}

// exactTextCount 统计渲染树中文本精确等于 text 的节点数量（Find 同样按精确匹配文本或 Label）。
func exactTextCount(tt *ui.Tester, text string) int {
	n := 0
	for _, s := range tt.Texts() {
		if s == text {
			n++
		}
	}
	return n
}

// (b) TestKanbanWorkspace_MoveTask：看板里放一个 Column 为 todo、CanMove 为 true、Revision 为 3 的任务，
// 经菜单移动到 review，断言 MoveTaskCalls 记录了 {TaskID, "review", 3}，重新加载后该任务出现在审查列（KanbanBoardCalls 至少为 2）。
func TestKanbanWorkspace_MoveTask(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-1",
		ProjectID: "p1",
		Title:     "待办任务1",
		Status:    "todo",
		Column:    "todo",
		CanMove:   true,
		Revision:  3,
	}
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Tasks:    []KanbanTask{task},
	})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("任务与看板")
	})

	if err := tt.Click("任务与看板"); err != nil {
		t.Fatalf("click '任务与看板' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("待办任务1")
	})

	// 经菜单移动：点击移动菜单触发按钮
	if err := tt.Click("kanban-task-task-1-move"); err != nil {
		if clickErr := tt.Click("移动"); clickErr != nil {
			t.Fatalf("click move trigger failed: %v", err)
		}
	}

	// 目标移动到 review (审查)
	waitFor(t, tt, func() bool {
		return tt.HasText("审查")
	})

	if err := tt.Click("kanban-move-review"); err != nil {
		if clickErr := tt.Click("审查"); clickErr != nil {
			t.Fatalf("click 'kanban-move-review' failed: %v", err)
		}
	}

	// 断言 MoveTaskCalls 记录了 {TaskID, "review", 3}
	waitFor(t, tt, func() bool {
		calls := fake.MoveTaskCalls()
		if len(calls) == 0 {
			return false
		}
		c := calls[0]
		return c.TaskID == "task-1" && c.Status == "review" && c.Revision == 3
	})

	// 重新加载后看板刷新，KanbanBoardCalls 至少为 2
	waitFor(t, tt, func() bool {
		return fake.KanbanBoardCalls() >= 2
	})
}

// (c) TestKanbanWorkspace_RevisionConflict：SetMoveTaskError(fmt.Errorf("%w", ErrRevisionConflict)) 后执行移动，
// 断言出现“数据已在别处被修改，请刷新后重试”；点击 kanban-conflict-refresh 后横幅消失，KanbanBoard 再次被调用。
func TestKanbanWorkspace_RevisionConflict(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-conflict-1",
		ProjectID: "p1",
		Title:     "冲突任务",
		Status:    "todo",
		Column:    "todo",
		CanMove:   true,
		Revision:  1,
	}
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Tasks:    []KanbanTask{task},
	})
	fake.SetMoveTaskError(fmt.Errorf("%w", ErrRevisionConflict))

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("任务与看板")
	})

	if err := tt.Click("任务与看板"); err != nil {
		t.Fatalf("click '任务与看板' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("冲突任务")
	})

	// 执行移动
	if err := tt.Click("kanban-task-task-conflict-1-move"); err != nil {
		_ = tt.Click("移动")
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("审查")
	})

	if err := tt.Click("kanban-move-review"); err != nil {
		_ = tt.Click("审查")
	}

	// 断言出现冲突提示横幅
	waitFor(t, tt, func() bool {
		return tt.HasText("数据已在别处被修改，请刷新后重试")
	})

	// 记录当前调用次数并恢复错误
	callsBefore := fake.KanbanBoardCalls()
	fake.SetMoveTaskError(nil)

	// 点击刷新按钮
	if err := tt.Click("kanban-conflict-refresh"); err != nil {
		if clickErr := tt.Click("刷新"); clickErr != nil {
			t.Fatalf("click 'kanban-conflict-refresh' failed: %v", err)
		}
	}

	// 断言横幅消失且看板重新加载
	waitFor(t, tt, func() bool {
		return !tt.HasText("数据已在别处被修改，请刷新后重试") && fake.KanbanBoardCalls() > callsBefore
	})
}

// (d) TestKanbanWorkspace_StartTask：启动按钮调用 StartTask 的测试。
func TestKanbanWorkspace_StartTask(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-start-1",
		ProjectID: "p1",
		Title:     "待启动任务",
		Status:    "todo",
		Column:    "todo",
		CanStart:  true,
		Revision:  2,
	}
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Tasks:    []KanbanTask{task},
	})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("任务与看板")
	})

	if err := tt.Click("任务与看板"); err != nil {
		t.Fatalf("click '任务与看板' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("待启动任务")
	})

	// 点击启动按钮
	if err := tt.Click("kanban-task-task-start-1-start"); err != nil {
		if clickErr := tt.Click("启动"); clickErr != nil {
			t.Fatalf("click start button failed: %v", err)
		}
	}

	// 断言 StartTaskCalls 记录了该任务 ID
	waitFor(t, tt, func() bool {
		for _, id := range fake.StartTaskCalls() {
			if id == "task-start-1" {
				return true
			}
		}
		return false
	})
}

func TestKanbanWorkspace_ReopenRestoresWatch(t *testing.T) {
	fake := NewFakeClient()
	tt, s := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("还没有任务") })
	s.closeWorkspace()
	s.openWorkspace("tasks")
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.kanbanWatchers) == 1
	})
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "new-task", ProjectID: "p1", Title: "订阅更新任务", Column: "todo",
	}}})
	fake.TriggerKanbanChange()
	waitFor(t, tt, func() bool { return tt.HasText("订阅更新任务") })
	s.openWorkspace("automations")
	waitFor(t, tt, func() bool { return tt.HasText("还没有自动化") })
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.kanbanWatchers) != 0 || len(fake.automationWatchers) != 1 {
		t.Fatal("hidden workspace kept its subscription")
	}
}

// 目标状态徽标文案：planning 显示「规划中」，未知状态兜底「处理中」，不露原始英文。
func TestGoalStatusBadgeText(t *testing.T) {
	for _, tc := range []struct{ status, want string }{
		{status: "planning", want: "规划中"},
		{status: "running", want: "运行中"},
		{status: "completed", want: "已完成"},
		{status: "plan_failed", want: "规划失败"},
		{status: "plan_review", want: "规划就绪"},
		{status: "paused", want: "已暂停"},
		{status: "cancelled", want: "已取消"},
		{status: "future_status", want: "处理中"},
		{status: "", want: "处理中"},
	} {
		if got := goalStatusBadgeText(tc.status); got != tc.want {
			t.Fatalf("goalStatusBadgeText(%q) = %q，期望 %q", tc.status, got, tc.want)
		}
	}
}

func TestKanbanWorkspace_ReplanStates(t *testing.T) {
	for _, status := range []string{"plan_review", "paused", "plan_failed", "running"} {
		t.Run(status, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetKanbanBoard(KanbanBoard{Goals: []KanbanGoal{{
				ID: "g1", ProjectID: "p1", Title: "测试目标", Status: status, Revision: 1,
			}}})
			tt, _ := newWorkspaceTester(t, fake, "tasks")
			waitFor(t, tt, func() bool { return tt.HasText("测试目标") })
			if tt.HasText("重新规划") != (status != "running") {
				t.Fatalf("incorrect replan action for %s", status)
			}
		})
	}
}

func TestKanbanWorkspace_DragTransitions(t *testing.T) {
	for _, tc := range []struct {
		name, column, toast string
		canStart            bool
	}{
		{name: "start", column: "in_progress", canStart: true},
		{name: "blocked", column: "in_progress", toast: "此任务暂不可启动，请先处理前置依赖或会话状态"},
		{name: "derived", column: "needs_you", toast: "此列由会话状态决定，请打开任务会话处理"},
		{name: "move", column: "done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := NewFakeClient()
			fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
				ID: "drag-task", ProjectID: "p1", Title: "拖动任务", Status: "todo", Column: "todo",
				CanMove: true, CanStart: tc.canStart, Revision: 3,
			}}})
			tt, _ := newWorkspaceTester(t, fake, "tasks")
			waitFor(t, tt, func() bool { return tt.HasText("拖动任务") })
			from, ok := tt.Find("kanban-task-card-drag-task")
			if !ok {
				t.Fatal("task card missing")
			}
			to, ok := tt.Find("kanban-column-" + tc.column)
			if !ok {
				t.Fatal("target column missing")
			}
			tt.Press(from.X+5, from.Y+5)
			for step := 1; step <= 4; step++ {
				fraction := float32(step) / 4
				tt.Move(from.X+5+(to.X+20-from.X-5)*fraction, from.Y+5+(to.Y+20-from.Y-5)*fraction)
			}
			tt.Release(to.X+20, to.Y+20)
			if tc.toast != "" {
				waitFor(t, tt, func() bool { return tt.HasText(tc.toast) })
				if len(fake.StartTaskCalls()) != 0 || len(fake.MoveTaskCalls()) != 0 {
					t.Fatal("invalid drop changed the task")
				}
			} else if tc.canStart {
				waitFor(t, tt, func() bool { return len(fake.StartTaskCalls()) == 1 })
				if len(fake.MoveTaskCalls()) != 0 {
					t.Fatal("drop changed status without starting a session")
				}
			} else {
				waitFor(t, tt, func() bool { return len(fake.MoveTaskCalls()) == 1 })
				call := fake.MoveTaskCalls()[0]
				if call.Status != tc.column || call.Revision != 3 {
					t.Fatalf("unexpected move: %+v", call)
				}
			}
		})
	}
}

// TestKanbanWorkspace_DragRejectsInvalidTransition 验证拖拽投放按状态迁移规则过滤：
// done 卡拖到「待审阅」属于后端不接受的迁移，只给提示、不发 MoveTask。
func TestKanbanWorkspace_DragRejectsInvalidTransition(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "done-task", ProjectID: "p1", Title: "已完成任务", Status: "done", Column: "done",
		CanMove: true, Revision: 2,
	}}})
	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("已完成任务") })
	from, ok := tt.Find("kanban-task-card-done-task")
	if !ok {
		t.Fatal("task card missing")
	}
	to, ok := tt.Find("kanban-column-review")
	if !ok {
		t.Fatal("target column missing")
	}
	tt.Press(from.X+5, from.Y+5)
	for step := 1; step <= 4; step++ {
		fraction := float32(step) / 4
		tt.Move(from.X+5+(to.X+20-from.X-5)*fraction, from.Y+5+(to.Y+20-from.Y-5)*fraction)
	}
	tt.Release(to.X+20, to.Y+20)
	waitFor(t, tt, func() bool { return tt.HasText("此任务不能从当前状态移动到此列，请先回到待办") })
	if calls := fake.MoveTaskCalls(); len(calls) != 0 {
		t.Fatalf("非法迁移不应调用 MoveTask: %+v", calls)
	}
}

// (a) TestKanbanWorkspace_OpenTaskDetail：带 ThreadID 的任务点击 kanban-task-card-<id> 后出现 kanban-task-detail，
// fake 的 History 和 Diff 收到该 threadID，日志文本和 diff 文件名能渲染出来；关闭按钮能关掉浮窗。
func TestKanbanWorkspace_OpenTaskDetail(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-detail-1",
		ProjectID: "p1",
		Title:     "详情测试任务",
		Notes:     "详情备注",
		Status:    "in_progress",
		Column:    "in_progress",
		ThreadID:  "thread-task-1",
		Revision:  1,
	}
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Tasks:    []KanbanTask{task},
	})
	logText := strings.Repeat("运行日志", 180) + "日志末尾"
	fake.SetMessages("thread-task-1", []Message{
		{ID: "m1", Role: "assistant", Text: logText},
	})
	diffContent := `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
-import "fmt"
+import "log"
 func main() {}
`
	fake.SetThreadDiff("thread-task-1", "", ThreadDiff{
		Diff: diffContent,
	})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("任务与看板")
	})
	if err := tt.Click("任务与看板"); err != nil {
		t.Fatalf("click '任务与看板' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("详情测试任务")
	})

	if err := tt.Click("kanban-task-card-task-detail-1"); err != nil {
		t.Fatalf("click task card failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-task-detail")
		return ok
	})

	waitFor(t, tt, func() bool {
		var histFound, diffFound bool
		for _, id := range fake.HistoryCalls() {
			if id == "thread-task-1" {
				histFound = true
				break
			}
		}
		for _, id := range fake.DiffCalls() {
			if id == "thread-task-1" {
				diffFound = true
				break
			}
		}
		return histFound && diffFound
	})

	waitFor(t, tt, func() bool {
		return tt.HasText(logText) && tt.HasText("main.go") && tt.HasText("备注: 详情备注")
	})

	if err := tt.Click("kanban-task-detail-close"); err != nil {
		t.Fatalf("click 'kanban-task-detail-close' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-task-detail")
		return !ok
	})
}

// (b) TestKanbanWorkspace_DetailAccept：review 状态任务点批准，fake 收到 decision=accept 和正确的 revision。
func TestKanbanWorkspace_DetailAccept(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-review-1",
		ProjectID: "p1",
		Title:     "待审查任务1",
		Status:    "review",
		Column:    "review",
		CanReview: true,
		Revision:  4,
	}
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Tasks:    []KanbanTask{task},
	})

	// 看板每列最小 270pt，窗口窄于五列总宽时看板横向滚动；这里开到能容下
	// 「待审阅」列（第 4 列），否则卡片右上角的「详情」按钮落在滚动视口外，点不到。
	tt := ui.NewTester(Root(fake), 1500, 900)

	waitFor(t, tt, func() bool {
		return tt.HasText("任务与看板")
	})
	if err := tt.Click("任务与看板"); err != nil {
		t.Fatalf("click '任务与看板' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("待审查任务1")
	})

	if err := tt.Click("kanban-task-card-task-review-1"); err != nil {
		t.Fatalf("click 'kanban-task-card-task-review-1' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-task-detail")
		return ok
	})

	if err := tt.Click("kanban-task-detail-accept"); err != nil {
		t.Fatalf("click 'kanban-task-detail-accept' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.ReviewTaskCalls()
		if len(calls) == 0 {
			return false
		}
		c := calls[0]
		return c.TaskID == "task-review-1" && c.Decision == "accept" && c.Revision == 4
	})
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-task-detail")
		return !open
	})
}

// (c) TestKanbanWorkspace_DetailRejectWithFeedback：填反馈后驳回，fake 收到 reject 和对应 feedback。
func TestKanbanWorkspace_DetailRejectWithFeedback(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-review-2",
		ProjectID: "p1",
		Title:     "待驳回任务2",
		Status:    "review",
		Column:    "review",
		CanReview: true,
		Revision:  2,
	}
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Tasks:    []KanbanTask{task},
	})

	// 同 TestKanbanWorkspace_DetailAccept：窗口需容下「待审阅」列，才能点到卡片的「详情」。
	tt := ui.NewTester(Root(fake), 1500, 900)

	waitFor(t, tt, func() bool {
		return tt.HasText("任务与看板")
	})
	if err := tt.Click("任务与看板"); err != nil {
		t.Fatalf("click '任务与看板' nav failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("待驳回任务2")
	})

	if err := tt.Click("kanban-task-card-task-review-2"); err != nil {
		t.Fatalf("click 'kanban-task-card-task-review-2' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-task-detail")
		return ok
	})

	_ = tt.Click("kanban-task-detail-feedback")
	tt.Type("请补充单测后再提交审查")
	tt.Frame()

	if err := tt.Click("kanban-task-detail-reject"); err != nil {
		t.Fatalf("click 'kanban-task-detail-reject' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.ReviewTaskCalls()
		if len(calls) == 0 {
			return false
		}
		c := calls[0]
		return c.TaskID == "task-review-2" && c.Decision == "reject" && c.Feedback == "请补充单测后再提交审查" && c.Revision == 2
	})
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-task-detail")
		return !open
	})
}

// (d) TestKanbanWorkspace_DetailRevisionConflict：fake 的 ReviewTask 返回 ErrRevisionConflict，之后 detailConflict 为 true、浮窗仍打开、反馈仍保留，并且触发了重新加载看板。
func TestKanbanWorkspace_DetailRevisionConflict(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-conflict-detail",
		ProjectID: "p1",
		Title:     "冲突详情任务",
		Status:    "review",
		Column:    "review",
		CanReview: true,
		// 看板卡片上已提供「会话」按钮，因此带会话的任务必然在卡片操作区占位；
		// 这里保留 ThreadID 以覆盖详情加载，同时在卡片操作区多放几个按钮，确保操作区仍溢出到卡片下方。
		ThreadID:       "thread-before-conflict",
		AttentionFlags: []string{"failed", "stuck"},
		Revision:       1,
	}
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Tasks:    []KanbanTask{task},
	})
	fake.SetReviewTaskError(fmt.Errorf("%w", ErrRevisionConflict))

	tt, s := newWorkspaceTester(t, fake, "tasks")

	waitFor(t, tt, func() bool {
		return tt.HasText("冲突详情任务")
	})

	if err := tt.Click("kanban-task-card-task-conflict-detail"); err != nil {
		t.Fatalf("click open detail failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-task-detail")
		ks := getKanbanState(s)
		kanbanMu.Lock()
		loaded := !ks.detailLoading
		kanbanMu.Unlock()
		return ok && loaded
	})

	_ = tt.Click("kanban-task-detail-feedback")
	tt.Type("先改一下")
	tt.Frame()

	callsBefore := fake.KanbanBoardCalls()
	task.Revision = 2
	task.ThreadID = "thread-after-conflict"
	fake.SetKanbanBoard(KanbanBoard{Revision: 2, Tasks: []KanbanTask{task}})
	fake.SetMessages(task.ThreadID, []Message{{Role: "assistant", Text: "更新后的运行日志"}})

	if err := tt.Click("kanban-task-detail-accept"); err != nil {
		t.Fatalf("click accept failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		ks := getKanbanState(s)
		kanbanMu.Lock()
		detailConflict := ks.detailConflict
		detailFeedback := ks.detailFeedback
		kanbanMu.Unlock()

		_, stillOpen := tt.Find("kanban-task-detail")
		boardReloaded := fake.KanbanBoardCalls() > callsBefore

		return detailConflict && stillOpen && detailFeedback == "先改一下" && boardReloaded
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("任务已被其他地方修改，请核对刷新后的任务与改动后重试") &&
			tt.HasText("更新后的运行日志")
	})
	fake.SetReviewTaskError(nil)
	if err := tt.Click("kanban-task-detail-reject"); err != nil {
		t.Fatalf("retry reject failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.ReviewTaskCalls()
		_, open := tt.Find("kanban-task-detail")
		return len(calls) == 2 && calls[1].Revision == 2 &&
			calls[1].Decision == "reject" && calls[1].Feedback == "先改一下" && !open
	})
}

func TestKanbanWorkspace_DetailDiffFailureReason(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{ID: "diff-failure", ProjectID: "p1", Title: "差异失败任务", ThreadID: "thread-diff-failure", Column: "review"}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	fake.SetThreadDiff(task.ThreadID, "", ThreadDiff{FailureReason: "git is not available"})
	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	// 点击标题而非卡片中心，避免命中卡片内的「打开审查会话」按钮
	if err := tt.Click(task.Title); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("改动加载失败: git is not available") })
	if tt.HasText("无改动") {
		t.Fatal("diff 加载失败不应显示为无改动")
	}
}

func TestKanbanWorkspace_DetailConflictRefreshFailure(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID: "refresh-failure", ProjectID: "p1", Title: "刷新失败任务",
		Status: "review", Column: "review", CanReview: true, Revision: 1,
	}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	fake.SetReviewTaskError(ErrRevisionConflict)
	tt, s := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	if err := tt.Click("kanban-task-card-" + task.ID); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("kanban-task-detail-feedback"); err != nil {
		t.Fatal(err)
	}
	tt.Type("保留反馈")
	tt.Frame()
	fake.SetKanbanError(fmt.Errorf("看板暂时不可用"))
	if err := tt.Click("kanban-task-detail-reject"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("刷新失败: 看板暂时不可用") })
	fake.SetKanbanError(nil)
	task.Revision = 2
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	if err := tt.Click("kanban-task-detail-refresh"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		ks := getKanbanState(s)
		kanbanMu.Lock()
		defer kanbanMu.Unlock()
		return !ks.loading && ks.err == "" && ks.detailRevision == 2 && ks.detailFeedback == "保留反馈"
	})
	fake.SetReviewTaskError(nil)
	if err := tt.Click("kanban-task-detail-reject"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.ReviewTaskCalls()
		return len(calls) == 2 && calls[1].Revision == 2 && calls[1].Feedback == "保留反馈"
	})
}

// TestKanbanListGroups：列表视图按状态分组，未知状态追加在末尾，组内按更新时间倒序。
func TestKanbanListGroups(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	board := KanbanBoard{Tasks: []KanbanTask{
		{ID: "todo-old", Title: "旧待办", Status: "todo", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "done", Title: "已完成", Status: "done", UpdatedAt: now},
		{ID: "todo-new", Title: "新待办", Status: "todo", UpdatedAt: now.Add(-time.Hour)},
		{ID: "blocked", Title: "阻塞任务", Status: "blocked", UpdatedAt: now},
		{ID: "status-empty", Title: "按列回退", Column: "review", UpdatedAt: now},
	}}

	groups := kanbanListGroups(board.Tasks)
	wantOrder := []string{"todo", "review", "done", "blocked"}
	if len(groups) != len(wantOrder) {
		t.Fatalf("分组数量 = %d, want %d: %+v", len(groups), len(wantOrder), groups)
	}
	for i, status := range wantOrder {
		if groups[i].status != status {
			t.Fatalf("第 %d 组 = %q, want %q", i, groups[i].status, status)
		}
	}
	if groups[0].label != "待办" || len(groups[0].tasks) != 2 {
		t.Fatalf("待办分组不正确: %+v", groups[0])
	}
	if groups[0].tasks[0].ID != "todo-new" || groups[0].tasks[1].ID != "todo-old" {
		t.Fatalf("组内应按更新时间倒序: %+v", groups[0].tasks)
	}
	if groups[1].tasks[0].ID != "status-empty" {
		t.Fatalf("状态为空的任务应回退到列状态: %+v", groups[1].tasks)
	}
}

// TestKanbanWorkspace_ListView：切换列表视图后按状态分组展示，点击行打开与看板卡片相同的详情。
func TestKanbanWorkspace_ListView(t *testing.T) {
	fake := NewFakeClient()
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{
		{ID: "list-1", ProjectID: "p1", Title: "列表任务一", Status: "todo", Column: "todo", UpdatedAt: now},
		{ID: "list-2", ProjectID: "p1", Title: "列表任务二", Status: "todo", Column: "todo", UpdatedAt: now.Add(-time.Hour)},
		{ID: "list-3", ProjectID: "p1", Title: "列表完成任务", Status: "done", Column: "done", UpdatedAt: now},
	}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("列表任务一") })

	if err := tt.Click("列表"); err != nil {
		t.Fatalf("click list segment failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, todoGroup := tt.Find("kanban-list-group-todo")
		_, doneGroup := tt.Find("kanban-list-group-done")
		return todoGroup && doneGroup && tt.HasText("待办 · 2") && tt.HasText("完成 · 1")
	})

	if err := tt.Click("kanban-list-row-list-1"); err != nil {
		t.Fatalf("click list row failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-task-detail")
		return open && tt.HasText("列表任务一")
	})
}

// TestKanbanWorkspace_DefaultListViewFromConfig：初始视图取 KanbanDefaultView，切换只影响当前会话。
func TestKanbanWorkspace_DefaultListViewFromConfig(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "default-list-task", ProjectID: "p1", Title: "默认列表任务",
		Status: "todo", Column: "todo", UpdatedAt: time.Now(),
	}}})

	dataDir := settingsTestDir(t)
	cfg := config.Defaults()
	cfg.Workbench.KanbanDefaultView = config.KanbanViewList
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("save config failed: %v", err)
	}

	var state *chatState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) { state = s }), 1320, 860)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.projects) > 0
	})
	state.openWorkspace("tasks")

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-list-group-todo")
		return ok && tt.HasText("默认列表任务")
	})

	// 侧边栏与页头也有名为“看板”的按钮，按文本点击会命中它们；
	// 分段控件每段都有独立 Label，直接点「看板」分段。
	if err := tt.Click("kanban-view-switch-kanban"); err != nil {
		t.Fatalf("click kanban view segment failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-column-todo")
		return ok
	})

	saved, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("reload config failed: %v", err)
	}
	if saved.Workbench.KanbanDefaultView != config.KanbanViewList {
		t.Fatalf("切换视图不应写回配置: %q", saved.Workbench.KanbanDefaultView)
	}
}

// TestKanbanWorkspace_SearchFilter：搜索框按标题与备注不区分大小写过滤，看板与列表视图都生效，清除后恢复。
func TestKanbanWorkspace_SearchFilter(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{
		{ID: "s-1", ProjectID: "p1", Title: "修复 Login 流程", Status: "todo", Column: "todo", UpdatedAt: time.Now()},
		{ID: "s-2", ProjectID: "p1", Title: "编写文档", Notes: "补充 LOGIN 摘要", Status: "review", Column: "review", UpdatedAt: time.Now()},
		{ID: "s-3", ProjectID: "p1", Title: "清理日志", Status: "done", Column: "done", UpdatedAt: time.Now()},
	}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("修复 Login 流程") && tt.HasText("编写文档") })

	if err := tt.Click("筛选任务"); err != nil {
		t.Fatalf("focus search failed: %v", err)
	}
	tt.Type("login")
	waitFor(t, tt, func() bool {
		_, kept := tt.Find("kanban-task-card-s-1")
		_, byNotes := tt.Find("kanban-task-card-s-2")
		_, filtered := tt.Find("kanban-task-card-s-3")
		return kept && byNotes && !filtered
	})

	// 列表视图共用同一份过滤结果
	if err := tt.Click("列表"); err != nil {
		t.Fatalf("click list segment failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, kept := tt.Find("kanban-list-row-s-1")
		_, filtered := tt.Find("kanban-list-row-s-3")
		return kept && !filtered
	})

	// 清除后恢复全部任务
	if err := tt.Click("清除搜索"); err != nil {
		t.Fatalf("click clear search failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, restored := tt.Find("kanban-list-row-s-3")
		return restored
	})
}

// TestKanbanWorkspace_QuickAdd：待办列底部的「+ 添加任务」展开输入，回车以输入内容为标题创建任务并收起；Esc 取消不创建。
func TestKanbanWorkspace_QuickAdd(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "qa-1", ProjectID: "p1", Title: "已有任务", Status: "todo", Column: "todo",
	}}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("已有任务") })

	if err := tt.Click("kanban-quick-add-toggle"); err != nil {
		t.Fatalf("click quick add toggle failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-quick-add-input")
		return ok
	})

	// Esc 取消：不创建任务且收起输入框
	tt.Type("不要创建的任务")
	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-quick-add-input")
		_, toggle := tt.Find("kanban-quick-add-toggle")
		return !open && toggle
	})
	if calls := fake.CreateTaskCalls(); len(calls) != 0 {
		t.Fatalf("Esc 不应创建任务: %+v", calls)
	}

	if err := tt.Click("kanban-quick-add-toggle"); err != nil {
		t.Fatalf("reopen quick add failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-quick-add-input")
		return ok
	})
	tt.Type("快速添加的任务")
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		calls := fake.CreateTaskCalls()
		if len(calls) != 1 {
			return false
		}
		return calls[0].Title == "快速添加的任务" && calls[0].ProjectID == "p1" && calls[0].Notes == ""
	})
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-quick-add-input")
		return !open
	})
}

// TestKanbanWorkspace_ReopenDone：已完成卡片的「重新打开」把任务移回待办。
func TestKanbanWorkspace_ReopenDone(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "done-1", ProjectID: "p1", Title: "已完成任务", Status: "done", Column: "done", CanMove: true, Revision: 5,
	}}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("已完成任务") })

	if err := tt.Click("kanban-task-done-1-reopen"); err != nil {
		t.Fatalf("click reopen failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		calls := fake.MoveTaskCalls()
		if len(calls) != 1 {
			return false
		}
		return calls[0].TaskID == "done-1" && calls[0].Status == "todo" && calls[0].Revision == 5
	})
}

// TestKanbanWorkspace_BlockedStartShowsReason：被阻塞任务的「启动」不调用 StartTask，并提示已有字段里的阻塞原因。
func TestKanbanWorkspace_BlockedStartShowsReason(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "blocked-1", ProjectID: "p1", Title: "被阻塞任务", Status: "todo", Column: "todo",
		CanStart: true, Blocked: true, DependsOn: []string{"dep-1"}, LastFailureReason: "依赖任务失败", Revision: 1,
	}}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("被阻塞任务") })

	if err := tt.Click("kanban-task-blocked-1-start"); err != nil {
		t.Fatalf("click blocked start failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("依赖任务失败") })
	if len(fake.StartTaskCalls()) != 0 {
		t.Fatal("被阻塞任务不应启动")
	}
}

// TestGoalOutputs_FiltersAndSorts：goalOutputs 只保留指定目标的任务，并按看板列顺序与标题稳定排序。
func TestGoalOutputs_FiltersAndSorts(t *testing.T) {
	board := KanbanBoard{
		Goals: []KanbanGoal{{ID: "g1"}, {ID: "g2"}},
		Tasks: []KanbanTask{
			{ID: "t-done", GoalID: "g1", Title: "完成任务", Status: "done", Column: "done", ReviewSummary: "已完成并合入"},
			{ID: "t-other", GoalID: "g2", Title: "其他目标任务", Status: "todo", Column: "todo"},
			{ID: "t-todo-b", GoalID: "g1", Title: "B 待办", Status: "todo", Column: "todo", Notes: "备注 B"},
			{ID: "t-review", GoalID: "g1", Title: "审查任务", Status: "review", Column: "review", ThreadID: "thread-1"},
			{ID: "t-todo-a", GoalID: "g1", Title: "A 待办", Status: "todo", Column: "todo"},
		},
	}

	outputs := goalOutputs(board, "g1")
	got := make([]string, 0, len(outputs))
	for _, out := range outputs {
		got = append(got, out.Task.ID)
	}
	want := []string{"t-todo-a", "t-todo-b", "t-review", "t-done"}
	if len(got) != len(want) {
		t.Fatalf("goalOutputs 过滤结果 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("goalOutputs 排序 = %v，期望 %v", got, want)
		}
	}

	if outputs[0].Summary != "" || outputs[0].HasDiff {
		t.Fatalf("无备注无会话的任务不应有摘要与 Diff 入口: %+v", outputs[0])
	}
	if outputs[1].Summary != "备注 B" {
		t.Fatalf("Notes 摘要 = %q", outputs[1].Summary)
	}
	if !outputs[2].HasDiff {
		t.Fatal("有会话的任务应提供 Diff 入口")
	}
	if outputs[3].Summary != "已完成并合入" {
		t.Fatalf("完成说明摘要 = %q", outputs[3].Summary)
	}

	if len(goalOutputs(board, "g3")) != 0 || len(goalOutputs(board, "")) != 0 {
		t.Fatal("未知目标或空目标 ID 应返回空结果")
	}
}

// TestKanbanWorkspace_GoalOutputsModal：点击目标卡片的「目标产物」打开弹窗，
// 展示该目标下任务的标题、状态与完成说明；无任务的目标显示空态。
func TestKanbanWorkspace_GoalOutputsModal(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{
		Revision: 1,
		Goals: []KanbanGoal{
			{ID: "goal-1", ProjectID: "p1", Title: "目标一", Status: "completed", Done: 1, Total: 1},
			{ID: "goal-2", ProjectID: "p1", Title: "目标二", Status: "running"},
		},
		Tasks: []KanbanTask{
			{
				ID: "gt-1", ProjectID: "p1", GoalID: "goal-1", Title: "产物任务",
				Status: "done", Column: "done", ReviewSummary: "已完成并合入", ThreadID: "thread-gt-1",
			},
		},
	})
	tt, _ := newWorkspaceTester(t, fake, "tasks")

	waitFor(t, tt, func() bool { return tt.HasText("目标一") })

	if err := tt.Click("goal-outputs-goal-1"); err != nil {
		t.Fatalf("click goal outputs failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-goal-outputs")
		return open && tt.HasText("产物任务") && tt.HasText("已完成并合入")
	})

	if err := tt.Click("kanban-goal-outputs-close"); err != nil {
		t.Fatalf("close goal outputs failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-goal-outputs")
		return !open
	})

	if err := tt.Click("goal-outputs-goal-2"); err != nil {
		t.Fatalf("click empty goal outputs failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-goal-outputs")
		return open && tt.HasText("该目标暂无任务产出")
	})
}

// TestKanbanWorkspace_EscInQuickAddKeepsWorkspaceOpen：快速添加输入框里按 Esc 只收起输入，任务工作区保持打开且不创建任务。
func TestKanbanWorkspace_EscInQuickAddKeepsWorkspaceOpen(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "esc-1", ProjectID: "p1", Title: "看板任务", Status: "todo", Column: "todo",
	}}})

	tt, state := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText("看板任务") })

	if err := tt.Click("kanban-quick-add-toggle"); err != nil {
		t.Fatalf("click quick add toggle failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-quick-add-input")
		return ok
	})

	if err := tt.Click("kanban-quick-add-input"); err != nil {
		t.Fatalf("focus quick add input failed: %v", err)
	}
	tt.Type("误触的任务")
	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool {
		_, open := tt.Find("kanban-quick-add-input")
		_, toggle := tt.Find("kanban-quick-add-toggle")
		return !open && toggle
	})

	ws := getWorkspaceState(state)
	workspaceMu.Lock()
	view := ws.view
	workspaceMu.Unlock()
	if view != "tasks" {
		t.Fatalf("快速添加中按 Esc 不应关闭任务工作区，当前视图: %q", view)
	}
	if calls := fake.CreateTaskCalls(); len(calls) != 0 {
		t.Fatalf("Esc 不应创建任务: %+v", calls)
	}
}

// TestKanbanProjectListOptions 测试项目下拉的预处理：路径为空的项目被过滤，
// 重名项目追加父目录名作为区分信息，不重名的项目不带区分信息。
func TestKanbanProjectListOptions(t *testing.T) {
	opts := kanbanProjectListOptions([]Project{
		{ID: "p1", Name: "mcode", Path: "/Users/me/work/mcode"},
		{ID: "p2", Name: "mcode", Path: "/Users/me/oss/mcode"},
		{ID: "p3", Name: "other", Path: "/Users/me/other"},
		{ID: "bad", Name: "/", Path: ""},
	})
	if len(opts) != 3 {
		t.Fatalf("expected 3 options, got %+v", opts)
	}
	if opts[0].Hint != "work" || opts[1].Hint != "oss" {
		t.Fatalf("expected duplicate names hint by parent dir, got %+v", opts)
	}
	if opts[2].Hint != "" {
		t.Fatalf("expected unique name without hint, got %+v", opts[2])
	}
	for _, opt := range opts {
		if opt.Name == "/" {
			t.Fatalf("expected empty-path project filtered, got %+v", opts)
		}
	}
}

// TestKanbanWorkspace_NewTaskPickerIndependent 覆盖新建任务弹窗内项目下拉的独立状态：下拉挂在外层 overlay，
// 原先点「保存任务」的命中路径失效、按钮不在命中树里，交互验证改用状态层：
// 弹窗内下拉与看板头部项目菜单各用各的开关，关闭弹窗 / 提交时只清理弹窗内下拉，不动头部菜单。
func TestKanbanWorkspace_NewTaskPickerIndependent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{})
	// 项目在挂载前注入，避免在 UI 线程之外写 fake.projects 与渲染并发读写切片。
	fake.projects = []Project{
		{ID: "p1", Name: "mcode", Path: "/Users/me/work/mcode"},
	}

	tt, state := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("新建任务")
		return ok
	})

	if err := tt.Click("新建任务"); err != nil {
		t.Fatalf("click new task failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-new-task-title")
		return ok
	})

	pickerMenuOpen := func() bool {
		kanbanModalMu.Lock()
		defer kanbanModalMu.Unlock()
		kst := kanbanModalStates[state]
		return kst != nil && kst.pickerMenuOpen
	}
	headerMenuOpen := func() bool {
		kanbanModalMu.Lock()
		defer kanbanModalMu.Unlock()
		kst := kanbanModalStates[state]
		return kst != nil && kst.projectMenuOpen
	}

	setKanbanPickerMenuOpen(state, true)
	if !pickerMenuOpen() {
		t.Fatalf("弹窗内下拉状态应可独立写入")
	}
	if headerMenuOpen() {
		t.Fatalf("弹窗内下拉不应连带打开头部项目菜单")
	}
	// 头部菜单单独打开时，弹窗内下拉状态保持不变（两个开关互不影响）。
	setKanbanProjectMenuOpen(state, true)
	if !headerMenuOpen() {
		t.Fatalf("头部项目菜单状态应可独立写入")
	}
	if !pickerMenuOpen() {
		t.Fatalf("打开头部菜单不应重置弹窗内下拉状态")
	}
	setKanbanProjectMenuOpen(state, false)

	closeKanbanNewTaskSheet(state)
	if pickerMenuOpen() {
		t.Fatalf("关闭弹窗后下拉状态应被清理")
	}
	waitFor(t, tt, func() bool {
		_, sheet := tt.Find("kanban-new-task-sheet")
		return !sheet
	})

	// 重新打开弹窗时从收起态开始，上一轮的展开状态不会残留。
	setKanbanPickerMenuOpen(state, true)
	openKanbanNewTaskSheet(state)
	if pickerMenuOpen() {
		t.Fatalf("重新打开弹窗时下拉应处于收起态")
	}
}

// TestKanbanHeaderProjectMenuClamped 测试头部项目菜单点击后展开，且锚定在按钮下方并与按钮左对齐、
// 宽度受限且完整落在窗口可视区域内（不溢出窗口顶部、不覆盖标题栏）。
// 按钮的 Tester 查找键是 Label“项目”；overlay 打开后命中树可能被压缩，
// 因此展开状态在状态层断言，几何信息仅在菜单面板可见时再核对。
func TestKanbanHeaderProjectMenuClamped(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{})
	projects := make([]Project, 0, 12)
	for i := 0; i < 12; i++ {
		projects = append(projects, Project{
			ID:   fmt.Sprintf("wide-project-%02d", i),
			Name: "一个名字很长很长很长很长的项目",
			Path: fmt.Sprintf("/Users/me/proj-%02d/mcode", i),
		})
	}
	fake.projects = projects
	tt, state := newWorkspaceTester(t, fake, "tasks")

	waitFor(t, tt, func() bool {
		_, ok := tt.Find("项目")
		return ok
	})
	btn, ok := tt.Find("项目")
	if !ok {
		t.Fatalf("header project menu button missing")
	}

	if err := tt.Click("项目"); err != nil {
		t.Fatalf("click header project menu failed: %v", err)
	}
	waitFor(t, tt, func() bool { return kanbanProjectMenuOpen(state) })

	// 菜单面板由 anchoredMenu 固定为 AnchorBottomLeft 对 AnchorTopLeft、
	// MinWidth 220 / MaxWidth 320 / MaxHeight 360；面板进入命中树时核对实际几何。
	menu, ok := tt.Find("kanban-project-picker")
	if !ok {
		return
	}
	// 浮层要等触发按钮布局后的下一帧才按锚点定尺寸。
	waitFor(t, tt, func() bool {
		menu, ok = tt.Find("kanban-project-picker")
		return ok && menu.W > 0
	})
	tt.Advance(DurBase + 50*time.Millisecond)
	menu, ok = tt.Find("kanban-project-picker")
	if !ok {
		t.Fatal("header project menu missing after animation")
	}
	if menu.Y < btn.Y+btn.H {
		t.Fatalf("菜单应锚定在按钮下方：btn=%+v menu=%+v", btn, menu)
	}
	if menu.X < btn.X-1 || menu.X > btn.X+1 {
		t.Fatalf("菜单应与按钮左对齐：btn=%+v menu=%+v", btn, menu)
	}
	if menu.W < 220 {
		t.Fatalf("菜单最小宽度应为 220，实际 %.1f", menu.W)
	}
	if menu.W > 320 || menu.H > 360 {
		t.Fatalf("菜单超出最大尺寸：%+v", menu)
	}
	if menu.X < 0 || menu.Y < 0 || menu.X+menu.W > 1320 || menu.Y+menu.H > 860 {
		t.Fatalf("菜单超出窗口可视区域：%+v", menu)
	}
}

// TestKanbanWorkspace_NewTaskSheet：头部「新建任务」打开弹窗，填写标题后「保存任务」
// 调用 Client.CreateTask 并关闭弹窗；弹窗内按 Esc 只关闭弹窗，看板保持打开。
func TestKanbanWorkspace_NewTaskSheet(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("新建任务")
		return ok
	})

	if err := tt.Click("新建任务"); err != nil {
		t.Fatalf("click new task failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-new-task-sheet")
		return ok && tt.HasText("新建任务")
	})

	// Esc 只关闭新建任务弹窗，看板弹窗保持打开
	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool {
		_, sheet := tt.Find("kanban-new-task-sheet")
		_, modal := tt.Find("kanban-modal-panel")
		return !sheet && modal
	})

	if err := tt.Click("新建任务"); err != nil {
		t.Fatalf("reopen new task failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-new-task-title")
		return ok
	})
	if err := tt.Click("kanban-new-task-title"); err != nil {
		t.Fatalf("focus title failed: %v", err)
	}
	tt.Type("弹窗新建的任务")

	if err := tt.Click("保存任务"); err != nil {
		t.Fatalf("click save task failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.CreateTaskCalls()
		if len(calls) == 0 {
			return false
		}
		return calls[0].Title == "弹窗新建的任务" && calls[0].ProjectID != ""
	})
	waitFor(t, tt, func() bool {
		_, sheet := tt.Find("kanban-new-task-sheet")
		return !sheet
	})
}

// TestKanbanWorkspace_NewGoalSheet：头部「新建目标」打开弹窗，输入 Prompt 后「规划目标」
// 调用 Client.CreateGoal 并关闭弹窗。
func TestKanbanWorkspace_NewGoalSheet(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("新建目标")
		return ok
	})

	if err := tt.Click("新建目标"); err != nil {
		t.Fatalf("click new goal failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-new-goal-sheet")
		return ok && tt.HasText("新建目标")
	})

	if err := tt.Click("kanban-new-goal-prompt"); err != nil {
		t.Fatalf("focus goal prompt failed: %v", err)
	}
	tt.Type("给设置页加上深色模式")

	if err := tt.Click("规划目标"); err != nil {
		t.Fatalf("click plan goal failed: %v", err)
	}

	// CreateGoal 不记录调用，成功后重新加载看板里会出现以 Prompt 为标题的目标。
	waitFor(t, tt, func() bool {
		return tt.HasText("给设置页加上深色模式")
	})
	waitFor(t, tt, func() bool {
		_, sheet := tt.Find("kanban-new-goal-sheet")
		return !sheet
	})
}

// TestKanbanWorkspace_EscClosesModal：没有快速添加与搜索内容时，Esc 关闭看板弹窗并回到聊天视图。
func TestKanbanWorkspace_EscClosesModal(t *testing.T) {
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{{
		ID: "esc-close-1", ProjectID: "p1", Title: "关闭前的任务", Status: "todo", Column: "todo",
	}}})

	tt, state := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("kanban-modal-panel")
		return ok && tt.HasText("关闭前的任务")
	})

	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool {
		_, modal := tt.Find("kanban-modal-panel")
		return !modal
	})

	ws := getWorkspaceState(state)
	workspaceMu.Lock()
	view := ws.view
	workspaceMu.Unlock()
	if view != "chat" {
		t.Fatalf("Esc 关闭看板后应回到聊天视图，当前视图: %q", view)
	}
}

// U-005：移动菜单的候选目标必须是后端会接受的目标。done / rejected 卡片只剩「待办」，
// 否则菜单会给出「审查」「完成」「驳回」，选中后必然收到「移动任务失败」。
func TestMoveTargetsForFiltersByCanTransition(t *testing.T) {
	cases := []struct {
		from string
		want []string
	}{
		{from: "todo", want: []string{"review", "done", "rejected"}},
		{from: "in_progress", want: []string{"todo", "review", "done", "rejected"}},
		{from: "needs_approval", want: []string{"todo", "review", "done", "rejected"}},
		{from: "review", want: []string{"todo", "done", "rejected"}},
		{from: "done", want: []string{"todo"}},
		{from: "rejected", want: []string{"todo"}},
	}
	for _, tc := range cases {
		var got []string
		for _, opt := range moveTargetsFor(tc.from) {
			got = append(got, opt.status)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("moveTargetsFor(%q) = %v, want %v", tc.from, got, tc.want)
		}
	}
}
