package steward

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"mcode/internal/kanban"
)

// seedTopicScopeTopics 直接把多条话题写进设置，供归属判定使用。
func seedTopicScopeTopics(t *testing.T, rig *stewardRig, topics ...Topic) {
	t.Helper()
	payload, err := json.Marshal(topics)
	if err != nil {
		t.Fatalf("序列化话题失败: %v", err)
	}
	rig.settings.set(settingTopics, string(payload))
}

// TestTopicOwnsSubject 覆盖任务、执行会话、评审会话的命中与各种不命中。
func TestTopicOwnsSubject(t *testing.T) {
	rig, board := newTopicRig(t)
	seedTopicScopeTopics(t, rig,
		ptTopic("topic-1", "goal-1", false),
		ptTopic("topic-closed", "goal-2", true),
		ptTopic("topic-draft", "", false),
	)
	board.tasks = []kanban.TaskView{
		ptTask("task-1", "goal-1", "thread-1", "review-1"),
		ptTask("task-2", "goal-2", "thread-2", "review-2"),
	}
	ctx := context.Background()
	cases := []struct {
		name                   string
		topic                  string
		subjectType, subjectID string
		want                   bool
	}{
		{"任务 ID 命中", "topic-1", "kanban_task", "task-1", true},
		{"执行会话命中", "topic-1", "thread", "thread-1", true},
		{"评审会话命中", "topic-1", "thread", "review-1", true},
		{"别的目标的任务不命中", "topic-1", "kanban_task", "task-2", false},
		{"别的目标的会话不命中", "topic-1", "thread", "thread-2", false},
		{"已结项话题不命中", "topic-closed", "kanban_task", "task-2", false},
		{"未开工话题不命中", "topic-draft", "kanban_task", "task-1", false},
		{"未知 subjectType 不命中", "topic-1", "card", "task-1", false},
		{"不存在的对象不命中", "topic-1", "kanban_task", "missing", false},
		{"不存在的话题不命中", "topic-missing", "kanban_task", "task-1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := rig.rt.TopicOwnsSubject(ctx, c.topic, c.subjectType, c.subjectID)
			if err != nil {
				t.Fatalf("归属判定失败: %v", err)
			}
			if got != c.want {
				t.Fatalf("TopicOwnsSubject(%q, %q, %q) = %v，期望 %v", c.topic, c.subjectType, c.subjectID, got, c.want)
			}
		})
	}
}

// TestTopicOwnsSubjectWithoutKanban 校验没接看板时一律不命中。
func TestTopicOwnsSubjectWithoutKanban(t *testing.T) {
	rig, _ := newTopicRig(t)
	seedTopicScopeTopics(t, rig, ptTopic("topic-1", "goal-1", false))
	rig.rt.kanban = nil
	got, err := rig.rt.TopicOwnsSubject(context.Background(), "topic-1", "kanban_task", "task-1")
	if err != nil {
		t.Fatalf("没接看板时不应报错: %v", err)
	}
	if got {
		t.Fatal("没接看板时不应命中任何对象")
	}
}

// TestTopicOwnsSubjectBoardError 校验看板读取失败时把错误交给调用方。
func TestTopicOwnsSubjectBoardError(t *testing.T) {
	rig, board := newTopicRig(t)
	seedTopicScopeTopics(t, rig, ptTopic("topic-1", "goal-1", false))
	wantErr := errors.New("看板读取失败")
	board.boardErr = wantErr
	if _, err := rig.rt.TopicOwnsSubject(context.Background(), "topic-1", "kanban_task", "task-1"); !errors.Is(err, wantErr) {
		t.Fatalf("看板错误应原样返回，实际 %v", err)
	}
}
