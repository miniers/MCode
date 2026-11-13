package mcodeui

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

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
	selected := s.selectedThreadID
	s.mu.Unlock()
	if selected != "worker2" {
		t.Fatalf("会话=%q", selected)
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
	waitFor(t, tt, func() bool { return tt.HasText("已通过") })
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
			selected := s.selectedThreadID
			s.mu.Unlock()
			if selected != "worker" {
				t.Fatalf("会话=%q", selected)
			}
		})
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
