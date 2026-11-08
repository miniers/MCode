package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/config"
	"mcode/internal/frontdesk"
	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestFrontdeskTaskAndMigration(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(filepath.Join(t.TempDir(), "mcode.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	if err = backend.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "home", Name: "总管", Path: filepath.Join(t.TempDir(), "home")})
	backend.AddProject(wire.ProjectSummary{ID: "project", Name: "项目", Path: filepath.Join(t.TempDir(), "project")})
	backend.AddThread(wire.ThreadSummary{ID: "old", ProjectID: "home", Title: "旧总管", Provider: "memory"}, nil)
	if err = db.SetSetting(ctx, "steward.thread_id", "old"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = migrateFrontdeskArchive(ctx, db, backend); err != nil {
			t.Fatal(err)
		}
	}
	thread, err := db.GetThread(ctx, "old")
	if err != nil || thread.Title != "总管 · 归档" {
		t.Fatalf("归档失败: %+v %v", thread, err)
	}
	id, _, err := db.GetSetting(ctx, "steward.archived_thread_id")
	if err != nil || id != "old" {
		t.Fatalf("归档设置失败: %q %v", id, err)
	}
	svc := &frontdeskServices{store: db, backend: backend, homeID: "home"}
	task, err := svc.CreateTask(ctx, frontdesk.CreateTaskInput{Title: "任务"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ProjectID == nil || *task.ProjectID != "project" || task.GoalID == nil {
		t.Fatalf("任务归属错误: %+v", task)
	}
	goal, err := db.GetGoal(ctx, *task.GoalID)
	if err != nil || goal.Title != "总管" || goal.Status != "running" {
		t.Fatalf("目标错误: %+v %v", goal, err)
	}
	meta, err := kanban.ParseTaskMeta(task.Meta)
	if err != nil || meta.Origin != "owner" {
		t.Fatalf("来源错误: %+v %v", meta, err)
	}
}

func TestFrontdeskReceiverEnabledAtRuntime(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.SetSetting(ctx, "steward.feed_thread_id", "feed"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Models.Provider = "claude"
	cfg.Orchestration.Steward.Enabled = false
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	receiver := frontdeskReceiver{store: db, dataDir: dir}
	if receiver.IsFeed("feed") {
		t.Fatal("未启用时不应接收")
	}
	cfg.Orchestration.Steward.Enabled = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if !receiver.IsFeed("feed") || receiver.IsFeed("other") {
		t.Fatal("启用后的会话识别错误")
	}
}

func TestFrontdeskClientNoModel(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Models.Provider = "claude"
	disabled := false
	cfg.Providers.Anthropic.Enabled = &disabled
	cfg.Providers.OpenAI.Enabled = false
	cfg.Providers.Presets = nil
	cfg.Providers.Custom = nil
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	_, err := (frontdeskClient{dataDir: dir}).Stream(context.Background(), llm.Request{}, nil)
	if !errors.Is(err, frontdesk.ErrNoModel) {
		t.Fatalf("应返回 ErrNoModel，实际 %v", err)
	}
}

func TestFrontdeskResultRouting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Models.Provider = "claude"
	cfg.Orchestration.Steward.Enabled = true
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, db); err != nil {
		t.Fatal(err)
	}
	backend.AddProject(wire.ProjectSummary{ID: "home", Name: "总管", Path: filepath.Join(dir, "home")})
	feedID, err := backend.EnsureStewardFeedThread(ctx, "home")
	if err != nil {
		t.Fatal(err)
	}
	pushes := 0
	a := &App{store: db, backend: backend, ownerNotify: func(_, _, _ string) error { pushes++; return nil }}
	handle := frontdeskResultHandler(a, dir)
	owner := store.Task{ID: "owner", Title: "任务", Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner"})}
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultDone, Summary: "完成"})
	if pushes != 1 {
		t.Fatalf("主任务推送次数 = %d", pushes)
	}
	child := store.Task{ID: "child", Title: "子任务", Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", ParentID: "owner"})}
	handle(ctx, child, kanban.ExecutorResult{Status: kanban.ResultDone, Summary: "完成"})
	if pushes != 1 {
		t.Fatalf("子任务不应推送，次数 = %d", pushes)
	}
	decision, err := db.CreateStewardDecision(ctx, store.StewardDecision{Question: "怎么做？", OptionsJSON: `["甲","乙"]`})
	if err != nil {
		t.Fatal(err)
	}
	owner.Meta = kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", DecisionID: decision.ID})
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultNeedsDecision, Question: decision.Question})
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultWaiting})
	messages, _, err := db.ListMessages(ctx, feedID, 0, 10)
	if err != nil || len(messages) != 3 {
		t.Fatalf("消息数量错误: %+v %v", messages, err)
	}
	if messages[0].Kind != "steward_result" || messages[1].Kind != "steward_result" || messages[2].Kind != "steward_decision" || pushes != 2 {
		t.Fatalf("分流错误: %+v，推送 %d 次", messages, pushes)
	}
	var payload struct {
		ID       string   `json:"id"`
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(messages[2].Text), &payload); err != nil || payload.ID != decision.ID || payload.Question != decision.Question || len(payload.Options) != 2 {
		t.Fatalf("决策内容错误: %+v %v", payload, err)
	}
}
