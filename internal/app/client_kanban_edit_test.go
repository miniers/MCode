package app

import (
	"context"
	"strings"
	"testing"
)

// TestClientDeleteTaskWithoutKanbanService：未装配看板服务时，看板编辑接口应返回错误，
// 而不是静默成功让界面以为任务已删除。
func TestClientDeleteTaskWithoutKanbanService(t *testing.T) {
	client := &Client{}

	if err := client.DeleteTask(context.Background(), "task-1", 1); err == nil ||
		!strings.Contains(err.Error(), "看板服务不可用") {
		t.Fatalf("DeleteTask err = %v, want 看板服务不可用", err)
	}

	if _, err := client.NudgeTask(context.Background(), "task-1", 1); err == nil ||
		!strings.Contains(err.Error(), "看板服务不可用") {
		t.Fatalf("NudgeTask err = %v, want 看板服务不可用", err)
	}
}
