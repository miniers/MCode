package kanban

import (
	"context"
	"testing"

	"mcode/internal/store"
)

// TestStartTaskAppliesStewardTaskDefaults 验证总管下发的任务按设置选模型并决定是否走编排：
// 任务未指定模型时用设置的模型，指定了则保留；非总管任务不受影响。
func TestStartTaskAppliesStewardTaskDefaults(t *testing.T) {
	ctx := context.Background()
	svc, db, mock := setupTestService(t)
	svc.opts.StewardTasks = func() StewardTaskDefaults {
		return StewardTaskDefaults{Provider: "codex", Model: "gpt-x", Swarm: true}
	}
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward},
		store.Task{ID: "auto", Title: "自动", Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})},
		store.Task{ID: "picked", Title: "指定", Meta: EncodeTaskMeta(TaskMeta{Origin: "owner", Provider: "claude", Model: "opus"})})
	plain, err := svc.CreateTask(ctx, CreateTaskInput{Title: "普通"})
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"auto", "picked", plain.ID} {
		task, err := db.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StartTask(ctx, id, task.Revision); err != nil {
			t.Fatalf("StartTask(%s): %v", id, err)
		}
	}

	want := []SessionRequest{
		{Provider: "codex", Model: "gpt-x", Swarm: true},
		{Provider: "claude", Model: "opus", Swarm: true},
		{},
	}
	if len(mock.starts) != len(want) {
		t.Fatalf("启动次数 = %d, want %d", len(mock.starts), len(want))
	}
	for i, w := range want {
		got := mock.starts[i]
		if got.Provider != w.Provider || got.Model != w.Model || got.Swarm != w.Swarm {
			t.Errorf("第 %d 个任务 = {%q %q %v}, want {%q %q %v}", i, got.Provider, got.Model, got.Swarm, w.Provider, w.Model, w.Swarm)
		}
	}
}
