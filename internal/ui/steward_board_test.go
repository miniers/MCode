package mcodeui

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestStewardBoardGroups(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	statuses := []struct {
		status, wait string
		group        int
	}{
		{"failed", "", 0}, {"needs_approval", "", 0}, {"review", "", 0},
		{"rejected", "", 0}, {"waiting", "human", 0}, {"todo", "", 1},
		{"in_progress", "", 1}, {"waiting", "machine", 1}, {"waiting", "", 1},
		{"done", "", 2}, {"done", "", 2}, {"cancelled", "", -1},
	}
	var tasks []KanbanTask
	var want [3][]string
	for i, tc := range statuses {
		id := tc.status + tc.wait + base.Add(time.Duration(i)*time.Second).Format("150405")
		tasks = append(tasks, KanbanTask{ID: id, Origin: "owner", Status: tc.status, WaitKind: tc.wait, UpdatedAt: base.Add(time.Duration(i) * time.Second)})
		if tc.group >= 0 {
			want[tc.group] = append([]string{id}, want[tc.group]...)
		}
	}
	tasks = append(tasks, KanbanTask{ID: "other-origin", Origin: "agent", Status: "todo"}, KanbanTask{ID: "child", Origin: "owner", ParentID: "parent", Status: "failed"})
	groups := stewardBoardGroups(tasks, base)
	for i, group := range groups {
		var got []string
		for _, task := range group {
			got = append(got, task.ID)
		}
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("group %d = %v, want %v", i, got, want[i])
		}
	}
}

func TestStewardBoardDoneWindow(t *testing.T) {
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	tasks := []KanbanTask{
		{ID: "recent", Origin: "owner", Status: "done", UpdatedAt: now.Add(-23 * time.Hour)},
		{ID: "boundary", Origin: "owner", Status: "done", UpdatedAt: now.Add(-24 * time.Hour)},
		{ID: "expired", Origin: "owner", Status: "done", UpdatedAt: now.Add(-25 * time.Hour)},
		{ID: "missing", Origin: "owner", Status: "done"},
		{ID: "active", Origin: "owner", Status: "in_progress", UpdatedAt: now.Add(-30 * time.Hour)},
	}
	groups := stewardBoardGroups(tasks, now)
	var got [3][]string
	for i, group := range groups {
		for _, task := range group {
			got[i] = append(got[i], task.ID)
		}
	}
	want := [3][]string{nil, {"active"}, {"recent", "boundary", "missing"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("groups = %v, want %v", got, want)
	}
}

func TestStewardBoardSignal(t *testing.T) {
	cases := []struct {
		name  string
		task  KanbanTask
		group int
		want  string
	}{
		{"done", KanbanTask{LastActivity: "progress"}, 2, ""},
		{"activity", KanbanTask{LastActivity: "  progress  "}, 1, "progress"},
		{"empty activity", KanbanTask{LastActivity: "  "}, 1, ""},
		{"failure reason", KanbanTask{Status: "failed", LastFailureReason: "  error  \nmore"}, 0, "error"},
		{"failure fallback", KanbanTask{Status: "failed", LastFailureReason: " \nmore"}, 0, "失败"},
		{"review", KanbanTask{Status: "review"}, 0, "待审阅"},
		{"rejected", KanbanTask{Status: "rejected"}, 0, "已驳回"},
		{"approval", KanbanTask{Status: "needs_approval"}, 0, "待审批"},
		{"human wait", KanbanTask{Status: "waiting", WaitKind: "human"}, 0, "待你处理"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stewardBoardSignal(tc.task, tc.group); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func newStewardBoardTester(t *testing.T, width int, selected string, tasks []KanbanTask) (*ui.Tester, *chatState, *stewardFakeClient) {
	t.Helper()
	testenv.TempHome(t)
	fake := newStewardFakeClient("steward")
	fake.SetKanbanBoard(KanbanBoard{Tasks: tasks})
	s := newChatState(fake)
	s.mu.Lock()
	s.sidebarCollapsed = true
	s.selectedThreadID = selected
	s.threads = []Thread{{ID: "steward", Title: "总管"}, {ID: "worker", Title: "执行会话"}, {ID: "other", Title: "普通会话"}}
	if selected != "steward" {
		s.stewardBoard.tasks = tasks
	}
	s.mu.Unlock()
	t.Cleanup(s.close)
	tt := ui.NewTesterWith(func(c *ui.Context) { renderChat(c, s) }, width, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	return tt, s, fake
}

func TestStewardBoardRender(t *testing.T) {
	task := KanbanTask{ID: "task", ThreadID: "worker", Title: "测试任务", Origin: "owner", Status: "todo"}
	cases := []struct {
		name         string
		width        int
		selected     string
		tasks        []KanbanTask
		board, badge bool
	}{
		{"wide", 1100, "steward", []KanbanTask{task}, true, false},
		{"empty", 1100, "steward", nil, false, false},
		{"filtered", 1100, "steward", []KanbanTask{{ID: "ignored", Title: "测试任务", Origin: "agent", Status: "todo"}}, false, false},
		{"other session", 1100, "other", []KanbanTask{task}, false, false},
		{"narrow", 700, "steward", []KanbanTask{task}, false, true},
		{"narrow empty", 700, "steward", nil, false, false},
		{"narrow other session", 700, "other", []KanbanTask{task}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tt, s, _ := newStewardBoardTester(t, tc.width, tc.selected, tc.tasks)
			if tc.selected == "steward" {
				waitFor(t, tt, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.stewardBoard.active && !s.stewardBoard.loading })
			} else {
				tt.Frame()
			}
			tt.Frame()
			_, board := tt.Find("测试任务")
			_, badge := tt.Find("总管任务 1")
			if board != tc.board || badge != tc.badge {
				t.Errorf("board=%v badge=%v, want %v %v", board, badge, tc.board, tc.badge)
			}
		})
	}
}

func TestStewardBoardExpiredDoneDoesNotRender(t *testing.T) {
	task := KanbanTask{ID: "expired", Title: "过期任务", Origin: "owner", Status: "done", UpdatedAt: time.Now().Add(-25 * time.Hour)}
	for _, width := range []int{700, 1100} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			tt, s, _ := newStewardBoardTester(t, width, "steward", []KanbanTask{task})
			waitFor(t, tt, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.stewardBoard.active && !s.stewardBoard.loading })
			tt.Frame()
			if _, ok := tt.Find("已完成"); ok {
				t.Error("expired done group is visible")
			}
			if _, ok := tt.Find("总管任务 0"); ok {
				t.Error("badge is visible")
			}
		})
	}
}

func TestStewardBoardClickTask(t *testing.T) {
	task := KanbanTask{ID: "task", ThreadID: "worker", Title: "测试任务", Origin: "owner", Status: "todo"}
	tt, s, _ := newStewardBoardTester(t, 1100, "steward", []KanbanTask{task})
	waitFor(t, tt, func() bool { _, ok := tt.Find("测试任务"); return ok })
	if err := tt.Click("测试任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	s.mu.Lock()
	selected, detailID := s.selectedThreadID, s.stewardBoard.detailTaskID
	s.mu.Unlock()
	if selected != "steward" {
		t.Fatalf("点击总管任务不应强行切离总管会话, got %q", selected)
	}
	if detailID != task.ID {
		t.Fatalf("detailTaskID=%q, want %q", detailID, task.ID)
	}
	if _, ok := tt.Find("steward-task-detail-sheet"); !ok {
		t.Fatal("应挂载 steward-task-detail-sheet")
	}
	if _, ok := tt.Find("kanban-task-detail"); !ok {
		t.Fatal("应渲染 kanban-task-detail")
	}
	for _, want := range []string{"结果", "改动", "会话", "打开会话"} {
		if !tt.HasText(want) {
			t.Fatalf("详情抽屉缺少 %q: %v", want, tt.Texts())
		}
	}
	if err := tt.Click("打开会话"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	s.mu.Lock()
	selected, snapshot, detailID := s.selectedThreadID, s.stewardTask, s.stewardBoard.detailTaskID
	s.mu.Unlock()
	if selected != task.ThreadID || snapshot.ID != task.ID {
		t.Fatalf("selected=%q task=%q", selected, snapshot.ID)
	}
	if detailID != "" {
		t.Fatalf("打开会话后应关闭详情弹层, detailTaskID=%q", detailID)
	}
	if _, ok := tt.Find("kanban-task-detail"); ok {
		t.Fatal("打开会话后详情弹层应已卸载")
	}
}

func TestStewardBoardStopsWatchOnLeaving(t *testing.T) {
	tt, s, fake := newStewardBoardTester(t, 1100, "steward", nil)
	waitFor(t, tt, func() bool { fake.mu.Lock(); defer fake.mu.Unlock(); return len(fake.kanbanWatchers) == 1 })
	s.mu.Lock()
	s.selectedThreadID = "other"
	s.mu.Unlock()
	tt.Frame()
	s.mu.Lock()
	active, stop, tasks := s.stewardBoard.active, s.stewardBoard.stop, s.stewardBoard.tasks
	s.mu.Unlock()
	fake.mu.Lock()
	watchers := len(fake.kanbanWatchers)
	fake.mu.Unlock()
	if active || stop != nil || tasks != nil || watchers != 0 {
		t.Fatalf("active=%v stop=%v tasks=%v watchers=%d", active, stop != nil, tasks, watchers)
	}
}

func TestUIReview12_BadgeHidesZeroWhenOnlyDone(t *testing.T) {
	doneTask := KanbanTask{ID: "done-1", ThreadID: "worker", Title: "已完成任务", Origin: "owner", Status: "done", UpdatedAt: time.Now()}
	tt, s, _ := newStewardBoardTester(t, 700, "steward", []KanbanTask{doneTask})
	waitFor(t, tt, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.stewardBoard.active && !s.stewardBoard.loading })
	tt.Frame()

	if _, ok := tt.Find("总管任务"); !ok {
		t.Fatal("只剩已完成任务时窄窗应保留总管任务徽标")
	}
	if _, ok := tt.Find("总管任务 0"); ok {
		t.Fatal("只剩已完成任务时徽标标签不应出现 0")
	}
	if tt.HasText("0") {
		t.Fatalf("只剩已完成任务时徽标不应显示数字 0: %v", tt.Texts())
	}
}

func TestUIReview12_BadgePopoverClickCloses(t *testing.T) {
	task := KanbanTask{ID: "t1", ThreadID: "worker", Title: "浮层任务", Origin: "owner", Status: "todo"}
	tt, s, _ := newStewardBoardTester(t, 700, "steward", []KanbanTask{task})
	waitFor(t, tt, func() bool { _, ok := tt.Find("总管任务 1"); return ok })

	if err := tt.Click("总管任务 1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { _, ok := tt.Find("浮层任务"); return ok })
	if !s.stewardBoard.open {
		t.Fatal("点击徽标后浮层应打开")
	}

	if err := tt.Click("浮层任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	s.mu.Lock()
	open, detailID, selected := s.stewardBoard.open, s.stewardBoard.detailTaskID, s.selectedThreadID
	s.mu.Unlock()
	if open {
		t.Fatal("从徽标浮层点击任务后 open 应为 false")
	}
	if detailID != task.ID {
		t.Fatalf("从徽标浮层点击任务后应打开详情, detailTaskID=%q", detailID)
	}
	if selected != "steward" {
		t.Fatalf("点击浮层任务不应强行切离总管会话, got %q", selected)
	}
	if _, ok := tt.Find("kanban-task-detail"); !ok {
		t.Fatal("点击浮层任务后应弹出统一任务详情")
	}
	if err := tt.Click("打开会话"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	s.mu.Lock()
	selected, detailID = s.selectedThreadID, s.stewardBoard.detailTaskID
	s.mu.Unlock()
	if selected != "worker" {
		t.Fatalf("点击打开会话后应跳转到工作线程, got %q", selected)
	}
	if detailID != "" {
		t.Fatalf("点击打开会话后应关闭详情弹层, detailTaskID=%q", detailID)
	}
}

func TestUIReview12_TaskByID(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.stewardBoard.tasks = []KanbanTask{
		{ID: "t1", Title: "任务一", Status: "in_progress", Column: "doing"},
		{ID: "t2", Title: "任务二", Status: "done", Column: "done"},
	}
	s.mu.Unlock()

	got, ok := s.stewardBoardTaskByID("t1")
	if !ok || got.ID != "t1" || got.Status != "in_progress" || got.Column != "doing" {
		t.Fatalf("命中查找失败: got=%+v, ok=%v", got, ok)
	}
	if _, ok := s.stewardBoardTaskByID("missing"); ok {
		t.Fatal("未命中 ID 不应返回 true")
	}
	if _, ok := s.stewardBoardTaskByID(""); ok {
		t.Fatal("空 ID 不应返回 true")
	}
	var nilState *chatState
	if _, ok := nilState.stewardBoardTaskByID("t1"); ok {
		t.Fatal("nil chatState 不应返回 true")
	}
}

func TestUIReview12_DoneUnfoldAnimationSettles(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.mu.Lock()
	s.stewardBoard.tasks = []KanbanTask{
		{ID: "done-1", ThreadID: "worker", Title: "已完成任务", Origin: "owner", Status: "done", UpdatedAt: time.Now()},
	}
	s.stewardBoard.rev = 1
	s.mu.Unlock()

	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderStewardBoard(c, s, s.stewardBoardGroupsCached())
	}, 300, 500, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: false}})
	for i := 0; i < 30; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Fatal("初始静止状态下不应有未结束动画")
	}
	if acc, ok := tt.Access("已完成"); !ok || acc.Expanded {
		t.Fatalf("初始折叠头应为未展开态: ok=%v, acc=%+v", ok, acc)
	}
	if tt.HasText("已完成任务") {
		t.Fatal("未展开时不应显示已完成任务")
	}

	if err := tt.Click("已完成"); err != nil {
		t.Fatal(err)
	}
	if !s.stewardBoard.doneOpen {
		t.Fatal("点击折叠头后 doneOpen 应为 true")
	}
	if acc, ok := tt.Access("已完成"); !ok || !acc.Expanded {
		t.Fatalf("展开后无障碍状态应标出 Expanded: ok=%v, acc=%+v", ok, acc)
	}
	if !tt.FramePending() {
		t.Fatal("非 ReduceMotion 下点击展开应启动动画")
	}
	for i := 0; i < 30; i++ {
		tt.Advance(16 * time.Millisecond)
		tt.Frame()
	}
	if tt.FramePending() {
		t.Fatal("「已完成」展开动画推进时钟到结束后不应再请求下一帧")
	}
	if !tt.HasText("已完成任务") {
		t.Fatalf("展开动画完成后应显示已完成任务: %v", tt.Texts())
	}
}
