package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestUIReview14_AutomationDeleteConfirm(t *testing.T) {
	fake := NewFakeClient()
	auto := testAutomation("auto-del-1", "p1")
	fake.SetAutomations([]Automation{auto})
	tt, _ := newWorkspaceTester(t, fake, "automations")
	waitFor(t, tt, func() bool { return tt.HasText(auto.Name) })

	if err := tt.Click(auto.Name); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("自动化更多操作"); return ok })
	if err := tt.Click("自动化更多操作"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("删除"); return ok })
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool { return tt.HasText("删除自动化？") })
	if got := len(fake.DeleteAutomationCalls()); got != 0 {
		t.Fatalf("点删除后不应立即调用 DeleteAutomation，实际调用 %d 次", got)
	}

	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("删除自动化？") })
	if got := len(fake.DeleteAutomationCalls()); got != 0 {
		t.Fatalf("取消后不应调用 DeleteAutomation，实际调用 %d 次", got)
	}

	if err := tt.Click("自动化更多操作"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("删除"); return ok })
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("删除自动化？") })
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}

	waitFor(t, tt, func() bool { return len(fake.DeleteAutomationCalls()) == 1 })
	if got := fake.DeleteAutomationCalls()[0]; got != auto.ID {
		t.Fatalf("DeleteAutomation ID = %q, want %q", got, auto.ID)
	}
}

func TestUIReview14_CachedLoadLocation(t *testing.T) {
	loc1, err := cachedLoadLocation("Asia/Shanghai")
	if err != nil || loc1 == nil {
		t.Fatalf("cachedLoadLocation(Asia/Shanghai) failed: loc=%v err=%v", loc1, err)
	}
	loc2, err := cachedLoadLocation("Asia/Shanghai")
	if err != nil || loc2 == nil {
		t.Fatalf("cachedLoadLocation(Asia/Shanghai) second call failed: loc=%v err=%v", loc2, err)
	}
	if loc1 != loc2 {
		t.Fatalf("cachedLoadLocation did not return cached pointer: %p != %p", loc1, loc2)
	}

	badName := "Invalid/Timezone_Review14"
	if loc, err := cachedLoadLocation(badName); err == nil || loc != nil {
		t.Fatalf("expected error for invalid timezone, got loc=%v err=%v", loc, err)
	}
	if loc, err := cachedLoadLocation(badName); err == nil || loc != nil {
		t.Fatalf("expected error on repeated invalid timezone call, got loc=%v err=%v", loc, err)
	}
}

func TestUIReview14_KanbanDetailEmptyNotesHidesLabel(t *testing.T) {
	for _, notes := range []string{"", "   \n\t "} {
		s := newChatState(NewFakeClient())
		ks := getKanbanState(s)
		task := KanbanTask{
			ID:        "notes-empty",
			ProjectID: "p1",
			Title:     "无备注任务",
			Notes:     notes,
			Status:    "todo",
			Column:    "todo",
		}
		ks.board.Tasks = []KanbanTask{task}
		ks.detailTaskID = task.ID
		ks.detailTab = "result"

		tt := ui.NewTester(func(c *ui.Context) { renderTaskDetailContent(c, s, ks) }, 640, 480)
		tt.Frame()
		if tt.HasText("需求") {
			t.Fatalf("Notes=%q 时不应显示「需求」: %v", notes, tt.Texts())
		}
		s.close()
	}

	s := newChatState(NewFakeClient())
	defer s.close()
	ks := getKanbanState(s)
	task := KanbanTask{
		ID:        "notes-present",
		ProjectID: "p1",
		Title:     "有备注任务",
		Notes:     "实现缓存逻辑",
		Status:    "todo",
		Column:    "todo",
	}
	ks.board.Tasks = []KanbanTask{task}
	ks.detailTaskID = task.ID
	ks.detailTab = "result"

	tt := ui.NewTester(func(c *ui.Context) { renderTaskDetailContent(c, s, ks) }, 640, 480)
	tt.Frame()
	if !tt.HasText("实现缓存逻辑") {
		t.Fatalf("应显示备注正文，实际文本：%v", tt.Texts())
	}
	if tt.HasText("需求:") || tt.HasText("需求：") {
		t.Fatalf("不应再显示「需求:」前缀，实际文本：%v", tt.Texts())
	}
}

func TestUIReview14_KanbanInterruptConfirm(t *testing.T) {
	fake := NewFakeClient()
	task := KanbanTask{
		ID:        "task-int-1",
		ProjectID: "p1",
		Title:     "中断确认任务",
		Status:    "in_progress",
		Column:    "in_progress",
		ThreadID:  "thread-int-1",
		Revision:  5,
	}
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})

	tt, _ := newWorkspaceTester(t, fake, "tasks")
	waitFor(t, tt, func() bool { return tt.HasText(task.Title) })
	hoverKanbanCard(t, tt, task.Title)

	if err := tt.Click("kanban-task-" + task.ID + "-more"); err != nil {
		t.Fatalf("click more failed: %v", err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("kanban-task-rescue-interrupt-" + task.ID); return ok })
	if err := tt.Click("kanban-task-rescue-interrupt-" + task.ID); err != nil {
		t.Fatalf("click interrupt failed: %v", err)
	}

	waitFor(t, tt, func() bool { return tt.HasText("中断并重置为待办？") })
	if got := len(fake.MoveTaskCalls()); got != 0 {
		t.Fatalf("点中断后不应立即调用 MoveTask，实际调用 %d 次", got)
	}
	if got := fake.InterruptCalls(); got != 0 {
		t.Fatalf("点中断后不应立即调用 Interrupt，实际调用 %d 次", got)
	}

	if err := tt.Click("kanban-task-interrupt-confirm-yes-" + task.ID); err != nil {
		t.Fatalf("click interrupt confirm failed: %v", err)
	}
	waitFor(t, tt, func() bool { return len(fake.MoveTaskCalls()) == 1 })
	call := fake.MoveTaskCalls()[0]
	if call.TaskID != task.ID || call.Status != "todo" || call.Revision != task.Revision {
		t.Fatalf("MoveTask 调用参数 = %+v", call)
	}
	if got := fake.InterruptCalls(); got != 1 {
		t.Fatalf("InterruptCalls = %d, want 1", got)
	}
}

func TestUIReview14_KanbanGoalCancelConfirm(t *testing.T) {
	fake := NewFakeClient()
	goal := KanbanGoal{ID: "goal-cancel-1", ProjectID: "p1", Title: "待取消目标", Status: "running", Revision: 3}
	fake.SetKanbanBoard(KanbanBoard{Goals: []KanbanGoal{goal}})

	s := newChatState(fake)
	defer s.close()
	ks := getKanbanState(s)
	ks.goalID = goal.ID
	ks.goalDetailOpen = true
	ks.board.Goals = []KanbanGoal{goal}

	tt := ui.NewTester(func(c *ui.Context) { renderGoalDetailContent(c, s, ks) }, 840, 520)
	tt.Frame()
	if err := tt.Click("取消目标"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("取消该目标？") })
	if len(fake.GoalCalls()) != 0 {
		t.Fatalf("点取消目标后不应立即调用 CancelGoal: %+v", fake.GoalCalls())
	}
	if err := tt.Click("kanban-goal-cancel-confirm-yes-" + goal.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return len(fake.GoalCalls()) == 1 })
	call := fake.GoalCalls()[0]
	if call.Action != "cancel" || call.GoalID != goal.ID || call.Revision != goal.Revision {
		t.Fatalf("GoalCall = %+v", call)
	}
}

func TestUIReview14_KanbanCardEmptyTitleFallbackAndBusy(t *testing.T) {
	s := newChatState(NewFakeClient())
	defer s.close()
	ks := getKanbanState(s)
	task := KanbanTask{ID: "empty-title", ProjectID: "p1", Title: "", Status: "todo", Column: "todo", CanStart: true, CanMove: true}
	tt := ui.NewTester(func(c *ui.Context) { renderTaskCard(c, s, ks, task, kanbanTaskCardState{}) }, 400, 200)
	tt.Frame()
	if _, ok := tt.Find("任务"); !ok {
		t.Fatal("空标题卡片应回退到无障碍标签「任务」")
	}

	if !beginKanbanTask(ks, task.ID) {
		t.Fatal("无法标记任务忙碌")
	}
	defer endKanbanTask(ks, task.ID)
	tt.Frame()
	if err := tt.RightClick("任务"); err != nil {
		t.Fatal(err)
	}
	for _, item := range tt.Menu() {
		if item == "开始" || item == "移动到 待审阅" || item == "移动到 已完成" {
			t.Fatalf("忙碌卡片右键菜单不应出现可操作项: %v", tt.Menu())
		}
	}
}
