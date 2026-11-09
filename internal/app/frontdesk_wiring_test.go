package app

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	if err := db.CreateGoal(ctx, store.Goal{ID: "ordinary", ProjectID: "project", Title: "总管", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	task, err := svc.CreateTask(ctx, frontdesk.CreateTaskInput{Title: "任务", Body: "原始要求"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ProjectID == nil || *task.ProjectID != "project" || task.GoalID == nil {
		t.Fatalf("任务归属错误: %+v", task)
	}
	goal, err := db.GetGoal(ctx, *task.GoalID)
	if err != nil || goal.ID == "ordinary" || goal.Title != "总管" || goal.Owner != "steward" || goal.Status != "running" || goal.PlanThreadID != nil {
		t.Fatalf("目标错误: %+v %v", goal, err)
	}
	meta, err := kanban.ParseTaskMeta(task.Meta)
	if err != nil || meta.Origin != "owner" {
		t.Fatalf("来源错误: %+v %v", meta, err)
	}
	goal.Status = "completed"
	if _, err := db.UpdateGoal(ctx, goal, goal.Revision); err != nil { t.Fatal(err) }
	second, err := svc.CreateTask(ctx, frontdesk.CreateTaskInput{Title: "第二项", ProjectID: "project"})
	if err != nil || second.GoalID == nil || *second.GoalID != goal.ID { t.Fatalf("目标复用失败: %+v %v", second, err) }
	goal, err = db.GetGoal(ctx, goal.ID)
	if err != nil || goal.Status != "running" { t.Fatalf("目标未恢复: %+v %v", goal, err) }
	failure := "失败"
	task.Status, task.AttemptCount, task.LastFailureReason = "done", 2, &failure
	meta.ResultStatus, meta.Summary, meta.NoChange = "done", "旧结果", true
	task.Meta = kanban.EncodeTaskMeta(meta)
	task, err = db.UpdateTask(ctx, task, task.Revision)
	if err != nil { t.Fatal(err) }
	if err := svc.AmendTask(ctx, task.ID, "继续修改"); err != nil { t.Fatal(err) }
	reopened, err := db.GetTask(ctx, task.ID)
	meta, _ = kanban.ParseTaskMeta(reopened.Meta)
	if err != nil || reopened.ID != task.ID || reopened.Status != "todo" || reopened.Notes != "原始要求\n继续修改" || reopened.AttemptCount != 0 || reopened.LastFailureReason != nil || meta.ResultStatus != "" || meta.NoChange {
		t.Fatalf("重开失败: %+v %+v %v", reopened, meta, err)
	}
	svc.kanban = kanban.NewService(kanban.Options{Store: db})
	feedID, err := backend.EnsureStewardFeedThread(ctx, "home")
	if err != nil { t.Fatal(err) }
	decision, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: task.ID, Question: "选择？", OptionsJSON: `["甲","乙"]`})
	if err != nil { t.Fatal(err) }
	reopened.Status = "needs_approval"
	meta.DecisionID = decision.ID
	reopened.Meta = kanban.EncodeTaskMeta(meta)
	if _, err := db.UpdateTask(ctx, reopened, reopened.Revision); err != nil { t.Fatal(err) }
	if err := svc.AmendTask(ctx, task.ID, "甲"); err != nil { t.Fatal(err) }
	answered, err := db.GetStewardDecision(ctx, decision.ID)
	if err != nil || answered.Status != store.DecisionStatusAnswered || answered.Answer != "甲" { t.Fatalf("决策未优先回答: %+v %v", answered, err) }
	if err := backend.AppendStewardMessage(ctx, feedID, "assistant", "steward_result", `{"task":"`+second.ID+`","title":"第二项","status":"done"}`); err != nil { t.Fatal(err) }
	recent, mentionedAt, err := svc.RecentTask(ctx, feedID)
	if err != nil || recent.ID != second.ID || mentionedAt.IsZero() { t.Fatalf("最近提及错误: %+v %v %v", recent, mentionedAt, err) }
	if _, _, err := svc.RecentTask(ctx, "old"); !errors.Is(err, store.ErrNotFound) { t.Fatalf("不同会话不应共享提及: %v", err) }
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

func TestWireFrontdeskDisabledDoesNotCreateProject(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	disableStewardForTest(t, dir)
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
	backend.AddProject(wire.ProjectSummary{ID: "project", Name: "项目", Path: filepath.Join(dir, "project")})
	before, err := backend.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wireFrontdesk(&App{store: db, backend: backend, kanban: kanban.NewService(kanban.Options{})}, dir)
	after, err := backend.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Projects) != len(before.Projects) {
		t.Fatalf("未启用总管时项目数变化：%d → %d", len(before.Projects), len(after.Projects))
	}
	feedID, _, err := db.GetSetting(ctx, "steward.feed_thread_id")
	if err != nil || feedID != "" {
		t.Fatalf("未启用总管时不应创建会话：%q，%v", feedID, err)
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
	cfg.Orchestration.Steward.QuietStart = ""
	cfg.Orchestration.Steward.QuietEnd = ""
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
	var pushBodies []string
	a := &App{store: db, backend: backend, ownerNotify: func(_, body, _ string) error { pushes++; pushBodies = append(pushBodies, body); return nil }}
	handle := frontdeskResultHandler(a, dir)
	owner := store.Task{ID: "owner", Title: "任务", Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner"})}
	unchanged := owner
	unchanged.Meta = kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", NoChange: true})
	handle(ctx, unchanged, kanban.ExecutorResult{Status: kanban.ResultDone, Summary: "无变化", NoChange: true})
	messages, _, err := db.ListMessages(ctx, feedID, 0, 10)
	if err != nil || len(messages) != 1 || pushes != 1 || pushBodies[0] != "无变化" {
		t.Fatalf("主任务无变化仍应回答：消息 %d，推送 %d，错误 %v", len(messages), pushes, err)
	}
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultDone, Summary: "完成"})
	if pushes != 2 {
		t.Fatalf("主任务推送次数 = %d", pushes)
	}
	if len(pushBodies) != 2 || pushBodies[1] != "完成" {
		t.Fatalf("结果推送正文错误：%q", pushBodies)
	}
	child := store.Task{ID: "child", Title: "子任务", Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", ParentID: "owner"})}
	handle(ctx, child, kanban.ExecutorResult{Status: kanban.ResultDone, Summary: "完成"})
	if pushes != 2 {
		t.Fatalf("子任务不应推送，次数 = %d", pushes)
	}
	decision, err := db.CreateStewardDecision(ctx, store.StewardDecision{Question: "怎么做？", OptionsJSON: `["甲","乙"]`})
	if err != nil {
		t.Fatal(err)
	}
	owner.Meta = kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner", DecisionID: decision.ID})
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultNeedsDecision, Question: decision.Question})
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultWaiting})
	messages, _, err = db.ListMessages(ctx, feedID, 0, 10)
	if err != nil || len(messages) != 4 {
		t.Fatalf("消息数量错误: %+v %v", messages, err)
	}
	if messages[0].Kind != "steward_result" || messages[1].Kind != "steward_result" || messages[2].Kind != "steward_result" || messages[2].Role != "system" || messages[2].Text != "子任务\n完成" || messages[3].Kind != "steward_decision" || pushes != 3 {
		t.Fatalf("分流错误: %+v，推送 %d 次", messages, pushes)
	}
	var resultPayload struct {
		Task    string `json:"task"`
		Thread  string `json:"thread"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Status  string `json:"status"`
		Owner   bool   `json:"owner"`
	}
	if err := json.Unmarshal([]byte(messages[1].Text), &resultPayload); err != nil || resultPayload.Task != owner.ID || resultPayload.Title != owner.Title || resultPayload.Summary != "完成" || resultPayload.Status != "done" || !resultPayload.Owner {
		t.Fatalf("结果内容错误: %+v %v", resultPayload, err)
	}
	var payload struct {
		ID       string   `json:"id"`
		TaskID   string   `json:"task_id"`
		Thread   string   `json:"thread"`
		Question string   `json:"question"`
		Options  []string `json:"options"`
	}
	if err := json.Unmarshal([]byte(messages[3].Text), &payload); err != nil || payload.ID != decision.ID || payload.TaskID != decision.TaskID || payload.Thread != decision.ThreadID || payload.Question != decision.Question || len(payload.Options) != 2 {
		t.Fatalf("决策内容错误: %+v %v", payload, err)
	}
	owner.Status = "todo"
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultFailed, Summary: "原因"})
	owner.Status = "failed"
	handle(ctx, owner, kanban.ExecutorResult{Status: kanban.ResultFailed, Summary: "原因"})
	messages, _, err = db.ListMessages(ctx, feedID, 0, 10)
	if err != nil || len(messages) != 5 || messages[4].Text != "没能完成「任务」：原因" || pushes != 4 || pushBodies[3] != messages[4].Text {
		t.Fatalf("失败回报错误: %+v %v", messages, err)
	}
}

func TestFrontdeskQuietWindowKeepsResult(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Orchestration.Steward.Enabled = true
	now := time.Now()
	cfg.Orchestration.Steward.QuietStart = now.Add(-time.Hour).Format("15:04")
	cfg.Orchestration.Steward.QuietEnd = now.Add(time.Hour).Format("15:04")
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
	task := store.Task{ID: "owner", Title: "任务", Meta: kanban.EncodeTaskMeta(kanban.TaskMeta{Origin: "owner"})}
	frontdeskResultHandler(a, dir)(ctx, task, kanban.ExecutorResult{Status: kanban.ResultDone, Summary: "完成"})
	messages, _, err := db.ListMessages(ctx, feedID, 0, 10)
	if err != nil || len(messages) != 1 || pushes != 0 {
		t.Fatalf("安静时段应保存结果且不推送：消息 %d，推送 %d，错误 %v", len(messages), pushes, err)
	}
}
