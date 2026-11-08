package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestTaskByThreadID(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "task_lookup_test.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	threadID := "thread-1"
	otherThreadID := "thread-2"
	for _, task := range []Task{
		{ID: "task-1", ThreadID: &threadID, Title: "执行任务", Status: "in_progress", Meta: `{"gate_override":"D-1"}`},
		{ID: "task-2", ThreadID: &otherThreadID, Title: "其他任务", Status: "todo"},
	} {
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("创建任务 %s 失败: %v", task.ID, err)
		}
	}

	got, err := s.TaskByThreadID(ctx, threadID)
	if err != nil {
		t.Fatalf("TaskByThreadID 失败: %v", err)
	}
	if got.ID != "task-1" || got.ThreadID == nil || *got.ThreadID != threadID {
		t.Fatalf("返回任务不匹配: %+v", got)
	}
	if got.Meta != `{"gate_override":"D-1"}` {
		t.Fatalf("meta 不匹配: %q", got.Meta)
	}

	if _, err := s.TaskByThreadID(ctx, "thread-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("缺少任务时应返回 ErrNotFound，实际 %v", err)
	}
	if _, err := s.TaskByThreadID(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空线程应返回 ErrNotFound，实际 %v", err)
	}
}
