package mcodeui

import (
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestStewardFocusTasks(t *testing.T) {
	groups := stewardGroups{
		{{ID: "needs-1", Status: "failed"}, {ID: "needs-2", Status: "needs_approval"}},
		{{ID: "todo", Status: "todo"}, {ID: "doing-1", Status: "in_progress"}, {ID: "doing-2", Status: "in_progress"}},
	}
	got := stewardFocusTasks(groups)
	want := []string{"needs-1", "needs-2", "doing-1"}
	if len(got) != len(want) {
		t.Fatalf("任务数=%d, want %d", len(got), len(want))
	}
	for i, task := range got {
		if task.ID != want[i] {
			t.Errorf("任务 %d=%q, want %q", i, task.ID, want[i])
		}
	}
}

func TestStewardTaskUnreadBaselineAndSeen(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	task := KanbanTask{ID: "task", Status: "done", UpdatedAt: base}
	if stewardTaskUnread(s, task) {
		t.Fatal("首次出现的任务不应未读")
	}
	task.UpdatedAt = base.Add(time.Minute)
	if !stewardTaskUnread(s, task) {
		t.Fatal("完成任务更新时间变化后应未读")
	}
	markStewardTaskSeen(s, task)
	if stewardTaskUnread(s, task) {
		t.Fatal("打开任务后应清除未读")
	}
}

func TestStewardTaskShouldCollapse(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status string
		age    time.Duration
		want   bool
	}{
		{status: "done", age: 25 * time.Hour, want: true},
		{status: "failed", age: 25 * time.Hour, want: true},
		{status: "cancelled", age: 25 * time.Hour, want: true},
		{status: "rejected", age: 25 * time.Hour, want: true},
		{status: "done", age: 24 * time.Hour, want: false},
		{status: "in_progress", age: 25 * time.Hour, want: false},
	} {
		task := KanbanTask{Status: tc.status, UpdatedAt: now.Add(-tc.age)}
		if got := stewardTaskShouldCollapse(task, now); got != tc.want {
			t.Errorf("%s age %s = %v, want %v", tc.status, tc.age, got, tc.want)
		}
	}
}

func TestStewardTaskRowsByMessage(t *testing.T) {
	task := KanbanTask{ID: "t", Origin: "owner", SourceMessageID: "u1", AmendMessageIDs: []string{"u2", "u2"}, Status: "cancelled"}
	items := []timelineItem{
		{kind: timelineItemSingle, id: "u1", msg: Message{ID: "u1", Role: "user"}},
		{kind: timelineItemSingle, id: "a1", msg: Message{ID: "a1", Role: "assistant"}},
		{kind: timelineItemSingle, id: "u2", msg: Message{ID: "u2", Role: "user"}, turnIndex: 1},
		{kind: timelineItemSingle, id: "a2", msg: Message{ID: "a2", Role: "assistant"}, turnIndex: 1},
	}
	idx := indexStewardTasks([]KanbanTask{task, {ID: "child", Origin: "owner", ParentID: "t", SourceMessageID: "u1"}, {ID: "agent", Origin: "agent", SourceMessageID: "u1"}})
	out := insertStewardTaskRows(items, idx)
	var ids []string
	for _, item := range out {
		ids = append(ids, item.id)
	}
	want := []string{"u1", "a1", "steward-task-u1-t", "u2", "a2", "steward-amend-u2-t"}
	if !reflect.DeepEqual(ids, want) || out[2].kind != timelineItemStewardTask || out[5].kind != timelineItemStewardAmend {
		t.Fatalf("回合任务位置错误: %v", ids)
	}
}

func TestStewardTaskRowsMultiple(t *testing.T) {
	tasks := []KanbanTask{
		{ID: "t1", Origin: "owner", SourceMessageID: "u1", AmendMessageIDs: []string{"u2"}},
		{ID: "t2", Origin: "owner", SourceMessageID: "u1"},
		{ID: "t3", Origin: "owner", SourceMessageID: "u1"},
	}
	items := []timelineItem{
		{kind: timelineItemSingle, msg: Message{ID: "u1", Role: "user"}, turnIndex: 2},
		{kind: timelineItemSingle, msg: Message{ID: "u2", Role: "user"}, turnIndex: 3},
	}
	out := insertStewardTaskRows(items, indexStewardTasks(tasks))
	if len(out) != 4 || out[1].kind != timelineItemStewardTaskChips || out[1].id != "steward-tasks-u1" || out[1].turnIndex != 2 || !reflect.DeepEqual(out[1].stewardTasks, tasks) {
		t.Fatalf("多任务行错误: %+v", out)
	}
	if out[3].kind != timelineItemStewardAmend || out[3].stewardTask.ID != "t1" || out[3].turnIndex != 3 {
		t.Fatalf("追加任务行错误: %+v", out[3])
	}
}

func TestStewardTaskChipsOpen(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.selectedThreadID = "steward"
	s.mu.Unlock()
	tasks := []KanbanTask{
		{ID: "t1", ThreadID: "worker1", Title: "任务一"},
		{ID: "t2", ThreadID: "worker2", Title: "任务二"},
		{ID: "t3", ThreadID: "worker3", Title: "任务三"},
	}
	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderStewardTaskChips(c, s, "tasks", tasks)
	}, 500, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	for _, task := range tasks {
		if !tt.HasText(task.Title) {
			t.Fatalf("缺少任务标题 %q", task.Title)
		}
	}
	if err := tt.Click("任务二"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	selected, detailID := s.selectedThreadID, s.stewardBoard.detailTaskID
	s.mu.Unlock()
	if selected != "steward" {
		t.Fatalf("点击胶囊不应强行切离总管会话, got %q", selected)
	}
	ks := getKanbanState(s)
	kanbanMu.Lock()
	ksDetailID := ks.detailTaskID
	kanbanMu.Unlock()
	if detailID != "t2" || ksDetailID != "t2" {
		t.Fatalf("detailTaskID=%q, ks.detailTaskID=%q, want %q", detailID, ksDetailID, "t2")
	}
}

func TestStewardTaskBoardRefreshAndOtherThread(t *testing.T) {
	task := KanbanTask{ID: "t", Origin: "owner", SourceMessageID: "u1", Title: "同步任务", Status: "todo"}
	fake := newStewardFakeClient("steward")
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	s := newChatState(fake)
	t.Cleanup(s.close)
	tt := ui.NewTester(func(c *ui.Context) {
		for _, item := range s.assembledTimelineItems(nil) {
			if item.kind == timelineItemStewardTask {
				renderStewardTaskCard(c, s, item.id, item.stewardTask)
			}
		}
	}, 300, 200)
	s.mu.Lock()
	s.selectedThreadID = "steward"
	s.timelineCacheItems = []timelineItem{{kind: timelineItemSingle, id: "u1", msg: Message{ID: "u1", Role: "user"}}}
	s.timelineCacheRev++
	s.mu.Unlock()
	s.syncStewardBoard(true)
	waitFor(t, tt, func() bool { return len(s.stewardBoardTasks()) == 1 })
	first := s.assembledTimelineItems(nil)
	second := s.assembledTimelineItems(nil)
	if len(first) == 0 || len(second) == 0 || &first[0] != &second[0] {
		t.Fatal("看板版本未变时应复用组装结果")
	}
	oldRev := s.stewardBoardRev()
	find := func() KanbanTask {
		for _, item := range s.assembledTimelineItems(nil) {
			if item.kind == timelineItemStewardTask {
				return item.stewardTask
			}
		}
		return KanbanTask{}
	}
	if got := kanbanStatusBadge(find()); got != "待办" {
		t.Fatalf("初始状态: %q", got)
	}
	waitFor(t, tt, func() bool { return tt.HasText("待办") })
	task.Status = "done"
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	fake.TriggerKanbanChange()
	waitFor(t, tt, func() bool { return tt.HasText("已完成") })
	updated := s.assembledTimelineItems(nil)
	if s.stewardBoardRev() <= oldRev || len(updated) == 0 || &first[0] == &updated[0] || find().Status != "done" {
		t.Fatal("看板更新后应重新组装并更新任务状态")
	}
	s.mu.Lock()
	s.selectedThreadID = "other"
	s.mu.Unlock()
	for _, item := range s.assembledTimelineItems(nil) {
		if item.kind == timelineItemStewardTask || item.kind == timelineItemStewardAmend || item.kind == timelineItemStewardTaskChips {
			t.Fatal("普通会话出现任务")
		}
	}
}

func TestStewardTaskCardAndChipOpen(t *testing.T) {
	for _, chip := range []bool{false, true} {
		t.Run(map[bool]string{false: "card", true: "chip"}[chip], func(t *testing.T) {
			s := newChatState(NewFakeClient())
			t.Cleanup(s.close)
			s.mu.Lock()
			s.selectedThreadID = "steward"
			s.mu.Unlock()
			task := KanbanTask{ID: "t", ThreadID: "worker", Title: "点击任务", Status: "todo"}
			tt := ui.NewTesterWith(func(c *ui.Context) {
				if chip {
					renderStewardTaskChip(c, s, "task", task)
				} else {
					renderStewardTaskCard(c, s, "task", task)
				}
			}, 500, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
			if err := tt.Click("点击任务"); err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			selected, detailID := s.selectedThreadID, s.stewardBoard.detailTaskID
			s.mu.Unlock()
			if selected != "steward" {
				t.Fatalf("点击任务不应强行切离当前会话, got %q", selected)
			}
			ks := getKanbanState(s)
			kanbanMu.Lock()
			ksDetailID := ks.detailTaskID
			kanbanMu.Unlock()
			if detailID != task.ID || ksDetailID != task.ID {
				t.Fatalf("detailTaskID=%q, ks.detailTaskID=%q, want %q", detailID, ksDetailID, task.ID)
			}
		})
	}
}

func TestStewardTaskDetailSheetActions(t *testing.T) {
	var gotTarget taskCenterTarget
	var targetCalls int
	task := KanbanTask{
		ID:            "t-steward",
		ProjectID:     "p1",
		ThreadID:      "worker",
		Title:         "总管任务卡",
		Origin:        "owner",
		Status:        "done",
		ResultSummary: "汇总完成",
	}
	fake := NewFakeClient()
	fake.SetKanbanBoard(KanbanBoard{Tasks: []KanbanTask{task}})
	s := newChatState(fake)
	s.openTasksWindowTargetFn = func(target taskCenterTarget) {
		gotTarget = target
		targetCalls++
	}
	t.Cleanup(s.close)
	s.mu.Lock()
	s.selectedThreadID = "steward"
	s.mu.Unlock()

	tt := ui.NewTesterWith(func(c *ui.Context) {
		s.mu.Lock()
		cur := s.selectedThreadID
		s.mu.Unlock()
		renderStewardTaskBand(c, s, cur)
		renderStewardTaskCard(c, s, "card", task)
		renderStewardTaskDetailSheet(c, s)
	}, 900, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})

	if err := tt.Click("总管任务卡"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("kanban-task-detail"); !ok {
		t.Fatal("点击时间线任务卡应弹出统一任务详情")
	}
	if err := tt.Click("打开会话"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	s.mu.Lock()
	selected, bandThread, snapshot, detailID := s.selectedThreadID, s.stewardTaskThreadID, s.stewardTask, s.stewardBoard.detailTaskID
	s.mu.Unlock()
	if selected != "worker" || bandThread != "worker" || snapshot.ID != task.ID {
		t.Fatalf("点击打开会话应切到执行会话并挂载状态带: selected=%q bandThread=%q snapshot=%+v", selected, bandThread, snapshot)
	}
	if detailID != "" {
		t.Fatalf("点击打开会话后应关闭详情弹层, detailTaskID=%q", detailID)
	}
	if !tt.HasText("汇总完成") {
		t.Fatalf("切到执行会话后应显示总管任务状态带: %v", tt.Texts())
	}

	if err := tt.Click("总管任务卡"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("在任务窗打开"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if targetCalls != 1 || gotTarget != (taskCenterTarget{Kind: "task", ID: task.ID, ProjectID: task.ProjectID, Title: task.Title}) {
		t.Fatalf("点击在任务窗打开回调不匹配: calls=%d target=%+v", targetCalls, gotTarget)
	}
	s.mu.Lock()
	detailID = s.stewardBoard.detailTaskID
	s.mu.Unlock()
	if detailID != "" {
		t.Fatalf("点击在任务窗打开后应关闭详情弹层, detailTaskID=%q", detailID)
	}
}

func TestStewardTaskRowsSkipSourceAmend(t *testing.T) {
	task := KanbanTask{ID: "t", Origin: "owner", SourceMessageID: "u1", AmendMessageIDs: []string{"u1", "u1"}, Status: "done"}
	items := []timelineItem{
		{kind: timelineItemSingle, id: "u1", msg: Message{ID: "u1", Role: "user"}},
		{kind: timelineItemSingle, id: "a1", msg: Message{ID: "a1", Role: "assistant"}},
	}
	out := insertStewardTaskRows(items, indexStewardTasks([]KanbanTask{task}))
	var ids []string
	for _, item := range out {
		ids = append(ids, item.id)
	}
	want := []string{"u1", "a1", "steward-task-u1-t"}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("同一消息的任务胶囊重复: %v", ids)
	}
}

func TestUIReview12_TaskCardAndChipTooltips(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	countText := func(tt *ui.Tester, target string) int {
		n := 0
		for _, txt := range tt.Texts() {
			if txt == target {
				n++
			}
		}
		return n
	}

	t.Run("card-signal", func(t *testing.T) {
		cardTask := KanbanTask{ID: "t1", ThreadID: "w1", Title: "任务卡片长标题", Status: "in_progress", LastActivity: "正在执行检查步骤"}
		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderStewardTaskCard(c, s, "card-1", cardTask)
		}, 500, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		rect, ok := tt.Find("正在执行检查步骤")
		if !ok {
			t.Fatal("未找到进展文本")
		}
		before := countText(tt, "正在执行检查步骤")
		tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
		tt.Advance(time.Second)
		tt.Frame()
		if countText(tt, "正在执行检查步骤") <= before {
			t.Fatalf("悬停进展文本后未弹出 Tooltip: rect=%+v texts=%v", rect, tt.Texts())
		}
	})

	t.Run("card-title", func(t *testing.T) {
		cardTask := KanbanTask{ID: "t2", ThreadID: "w2", Title: "无进展长标题", Status: "todo"}
		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderStewardTaskCard(c, s, "card-2", cardTask)
		}, 500, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		rect, ok := tt.Find("无进展长标题")
		if !ok {
			t.Fatal("未找到卡片标题")
		}
		before := countText(tt, "无进展长标题")
		tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
		tt.Advance(time.Second)
		tt.Frame()
		if countText(tt, "无进展长标题") <= before {
			t.Fatalf("悬停卡片标题后未弹出 Tooltip: rect=%+v texts=%v", rect, tt.Texts())
		}
	})

	t.Run("chip-title", func(t *testing.T) {
		longTitle := "这是一个非常非常长一定会发生截断的任务胶囊标题"
		chipTask := KanbanTask{ID: "t3", ThreadID: "w3", Title: longTitle, Status: "todo"}
		tt := ui.NewTesterWith(func(c *ui.Context) {
			renderStewardTaskChip(c, s, "chip-1", chipTask)
		}, 500, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
		rect, ok := tt.Find(longTitle)
		if !ok {
			t.Fatal("未找到胶囊标题")
		}
		before := countText(tt, longTitle)
		tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
		tt.Advance(time.Second)
		tt.Frame()
		if countText(tt, longTitle) <= before {
			t.Fatalf("悬停胶囊标题后未弹出 Tooltip: rect=%+v texts=%v", rect, tt.Texts())
		}
	})
}
