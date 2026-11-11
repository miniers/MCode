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
	s.mu.Lock()
	s.selectedThreadID = "other"
	s.mu.Unlock()
	for _, item := range s.assembledTimelineItems(nil) {
		if item.kind == timelineItemStewardTask || item.kind == timelineItemStewardAmend {
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
