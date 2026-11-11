package kanban

import (
	"context"
	"testing"

	"mcode/internal/store"
)

// TestStartTaskAppliesStewardTaskDefaults 验证总管下发的任务按设置的编排配对下发：
// 任务未指定模型时用配对主导侧模型，指定了则保留；配对的子代理模型与配对 ID 总会带上并开启编排；非总管任务不受影响。
func TestStartTaskAppliesStewardTaskDefaults(t *testing.T) {
	ctx := context.Background()
	svc, db, mock := setupTestService(t)
	svc.opts.StewardTasks = func() StewardTaskDefaults {
		return StewardTaskDefaults{Provider: "codex", Model: "gpt-x", BeesProvider: "claude", BeesModel: "haiku", PairID: "pair-1"}
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
		{Provider: "codex", Model: "gpt-x", Swarm: true, BeesProvider: "claude", BeesModel: "haiku", PairID: "pair-1"},
		{Provider: "claude", Model: "opus", Swarm: true, BeesProvider: "claude", BeesModel: "haiku", PairID: "pair-1"},
		{},
	}
	if len(mock.starts) != len(want) {
		t.Fatalf("启动次数 = %d, want %d", len(mock.starts), len(want))
	}
	for i, w := range want {
		got := mock.starts[i]
		if got.Provider != w.Provider || got.Model != w.Model || got.Swarm != w.Swarm ||
			got.BeesProvider != w.BeesProvider || got.BeesModel != w.BeesModel || got.PairID != w.PairID {
			t.Errorf("第 %d 个任务 = %+v, want %+v", i, got, w)
		}
	}
}

// TestStartTaskStewardSingleModelNoSwarm 验证任务模型选的是单个模型（没有配对）时不开编排。
func TestStartTaskStewardSingleModelNoSwarm(t *testing.T) {
	ctx := context.Background()
	svc, db, mock := setupTestService(t)
	svc.opts.StewardTasks = func() StewardTaskDefaults {
		return StewardTaskDefaults{Provider: "codex", Model: "gpt-x"}
	}
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward},
		store.Task{ID: "auto", Title: "自动", Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})})
	task, err := db.GetTask(ctx, "auto")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartTask(ctx, "auto", task.Revision); err != nil {
		t.Fatal(err)
	}
	if len(mock.starts) != 1 {
		t.Fatalf("启动次数 = %d", len(mock.starts))
	}
	if got := mock.starts[0]; got.Provider != "codex" || got.Model != "gpt-x" || got.Swarm || got.PairID != "" {
		t.Fatalf("请求 = %+v", got)
	}
}
