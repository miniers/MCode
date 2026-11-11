package mcodeui

import (
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
		{"approval", KanbanTask{Status: "needs_approval"}, 0, "等你决定"},
		{"human wait", KanbanTask{Status: "waiting", WaitKind: "human"}, 0, "等你决定"},
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
	t.Setenv("HOME", t.TempDir())
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
	s.mu.Lock()
	selected, snapshot := s.selectedThreadID, s.stewardTask
	s.mu.Unlock()
	if selected != task.ThreadID || snapshot.ID != task.ID {
		t.Fatalf("selected=%q task=%q", selected, snapshot.ID)
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
