package kanban

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestReceiptSurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := store.OpenForTest(t)
	driver := newPokeDriver()
	projectsDir := t.TempDir()

	svc1 := NewService(Options{
		Store:      db,
		Driver:     driver,
		Projects:   &mockProjects{dir: projectsDir},
		AutoReview: func() bool { return true },
	})
	t.Cleanup(func() { svc1.Close() })

	thread1 := "thread-restart-1"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "goal-restart-receipt", ProjectID: "proj-1", Title: "重启回执"},
		store.Task{ID: "task-restart-1", Title: "重启后取回执", Status: string(TaskStatusInProgress), ThreadID: &thread1})

	if err := svc1.SubmitTaskResult(ctx, "task-restart-1", thread1, ReceiptOutcomeDone, "已完成核心逻辑"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}

	// 用同一 store 构造第二个 Service，模拟进程重启（内存 receiptState 为空）
	svc2 := NewService(Options{
		Store:      db,
		Driver:     driver,
		Projects:   &mockProjects{dir: projectsDir},
		AutoReview: func() bool { return true },
	})
	t.Cleanup(func() { svc2.Close() })

	if svc2.hasReceipt("task-restart-1", "other-thread") {
		t.Fatal("threadID 不匹配时 hasReceipt 应返回 false")
	}
	if !svc2.hasReceipt("task-restart-1", thread1) {
		t.Fatal("重启后 hasReceipt 应从任务 Meta 回退读取并返回 true")
	}
	got, ok := svc2.takeReceipt("task-restart-1", thread1)
	if !ok {
		t.Fatal("重启后 takeReceipt 应拿到持久化的回执")
	}
	if got.ThreadID != thread1 || got.Outcome != ReceiptOutcomeDone || got.Summary != "已完成核心逻辑" {
		t.Fatalf("取出的回执内容不符: %+v", got)
	}
	if svc2.hasReceipt("task-restart-1", thread1) {
		t.Fatal("takeReceipt 消费后 hasReceipt 应返回 false")
	}
	afterTake, err := db.GetTask(ctx, "task-restart-1")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	meta, err := ParseTaskMeta(afterTake.Meta)
	if err != nil {
		t.Fatalf("解析任务 Meta 失败: %v", err)
	}
	if meta.PendingReceipt != nil {
		t.Fatalf("takeReceipt 消费后 Meta 不应保留 pending_receipt: %+v", meta.PendingReceipt)
	}

	// 验证重启后通过 Watchdog/decideFinished 收尾不发催办
	thread2 := "thread-restart-2"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "goal-restart-finish", ProjectID: "proj-1", Title: "重启收尾"},
		store.Task{ID: "task-restart-2", Title: "重启后收尾不追问", Status: string(TaskStatusInProgress), ThreadID: &thread2})
	if err := svc1.SubmitTaskResult(ctx, "task-restart-2", thread2, ReceiptOutcomeDone, "重启前提交完成"); err != nil {
		t.Fatalf("提交第二个回执失败: %v", err)
	}

	svc3 := NewService(Options{
		Store:      db,
		Driver:     driver,
		Projects:   &mockProjects{dir: projectsDir},
		AutoReview: func() bool { return true },
	})
	t.Cleanup(func() { svc3.Close() })
	wd3 := NewWatchdog(svc3, 0)
	driver.setThreadState(thread2, SessionState{Running: false})

	if err := wd3.Check(ctx); err != nil {
		t.Fatalf("重启后 watchdog check 失败: %v", err)
	}
	if got := len(driver.sentFollowUps()); got != 0 {
		t.Fatalf("重启后已有持久化回执不应追问，实际追问 %d 次", got)
	}
	finished, err := db.GetTask(ctx, "task-restart-2")
	if err != nil {
		t.Fatalf("读取收尾任务失败: %v", err)
	}
	if finished.Status != string(TaskStatusReview) {
		t.Fatalf("重启后收尾应迁至 review，实际: %s", finished.Status)
	}
	if finished.LastActivityText == nil || *finished.LastActivityText != "重启前提交完成" {
		t.Fatalf("期望活动文本为回执摘要，实际: %v", finished.LastActivityText)
	}
	finishedMeta, err := ParseTaskMeta(finished.Meta)
	if err != nil || finishedMeta.PendingReceipt != nil {
		t.Fatalf("收尾后 Meta 不应保留 pending_receipt: meta=%+v err=%v", finishedMeta, err)
	}
}

func TestPendingDoneSurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := store.OpenForTest(t)
	driver := newPokeDriver()
	projectsDir := t.TempDir()

	svc1 := NewService(Options{
		Store:      db,
		Driver:     driver,
		Projects:   &mockProjects{dir: projectsDir},
		AutoReview: func() bool { return false },
	})
	t.Cleanup(func() { svc1.Close() })
	wd1 := NewWatchdog(svc1, 0)

	threadID := "thread-pending-done"
	seedSchedulerGoal(t, ctx, db, store.Goal{
		ID:        "goal-pending-done",
		ProjectID: "proj-1",
		Title:     "自动接受重启",
		Policy:    map[string]any{"autoAccept": true},
	}, store.Task{
		ID:       "task-pending-done",
		Title:    "重启后自动接受",
		Status:   string(TaskStatusInProgress),
		ThreadID: &threadID,
	})

	if err := svc1.SubmitTaskResult(ctx, "task-pending-done", threadID, ReceiptOutcomeDone, "自动接受完成摘要"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := wd1.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}

	mid, err := db.GetTask(ctx, "task-pending-done")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if mid.Status != string(TaskStatusInProgress) {
		t.Fatalf("自动接受目标下 watchdog 应保持 in_progress，实际: %s", mid.Status)
	}
	midMeta, err := ParseTaskMeta(mid.Meta)
	if err != nil || midMeta.PendingDone == nil || *midMeta.PendingDone != "自动接受完成摘要" || midMeta.PendingReceipt != nil {
		t.Fatalf("watchdog 消费回执后应持久化 pending_done 并清除 pending_receipt: meta=%+v err=%v", midMeta, err)
	}

	// 模拟重启：新建 Service 与 GoalScheduler，消费持久化在 Meta 中的 pending_done
	svc2 := NewService(Options{
		Store:      db,
		Driver:     driver,
		Projects:   &mockProjects{dir: projectsDir},
		AutoReview: func() bool { return false },
	})
	t.Cleanup(func() { svc2.Close() })
	scheduler2 := NewGoalScheduler(svc2, time.Minute)
	scheduler2.Tick(ctx)

	doneTask, err := db.GetTask(ctx, "task-pending-done")
	if err != nil {
		t.Fatalf("读取完成任务失败: %v", err)
	}
	if doneTask.Status != string(TaskStatusDone) {
		t.Fatalf("重启后调度器应自动接受为 done，实际: %s", doneTask.Status)
	}
	if doneTask.ReviewStatus == nil || *doneTask.ReviewStatus != "passed" {
		t.Fatalf("自动接受应写入 ReviewStatus=passed，实际: %v", doneTask.ReviewStatus)
	}
	if doneTask.LastActivityText == nil || *doneTask.LastActivityText != "自动接受完成摘要" {
		t.Fatalf("期望活动文本为 pending_done 摘要，实际: %v", doneTask.LastActivityText)
	}
	if got := len(driver.sentFollowUps()); got != 0 {
		t.Fatalf("有 pending_done 时不应追问，实际追问 %d 次", got)
	}
	doneMeta, err := ParseTaskMeta(doneTask.Meta)
	if err != nil || doneMeta.PendingDone != nil {
		t.Fatalf("消费后 Meta 不应保留 pending_done: meta=%+v err=%v", doneMeta, err)
	}
}

func TestReceiptMetaKeepsOtherKeys(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := store.OpenForTest(t)
	driver := newPokeDriver()

	svc := NewService(Options{
		Store:    db,
		Driver:   driver,
		Projects: &mockProjects{dir: t.TempDir()},
	})
	t.Cleanup(func() { svc.Close() })

	threadID := "thread-keep-keys"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "goal-keep-keys", ProjectID: "proj-1", Title: "保留元信息键"},
		store.Task{
			ID:       "task-keep-keys",
			Title:    "保留 origin",
			Status:   string(TaskStatusInProgress),
			ThreadID: &threadID,
			Meta:     `{"origin":"owner"}`,
		})

	if err := svc.SubmitTaskResult(ctx, "task-keep-keys", threadID, ReceiptOutcomeDone, "完成并保留 origin"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}
	if _, ok := svc.takeReceipt("task-keep-keys", threadID); !ok {
		t.Fatal("takeReceipt 应取到刚提交的回执")
	}

	task, err := db.GetTask(ctx, "task-keep-keys")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	raw := map[string]any{}
	if err := json.Unmarshal([]byte(task.Meta), &raw); err != nil {
		t.Fatalf("解析任务 Meta 失败: %v", err)
	}
	if raw["origin"] != "owner" {
		t.Fatalf("清除回执后应保留 origin=\"owner\"，实际 Meta: %s", task.Meta)
	}
	if _, exists := raw[taskMetaPendingReceiptKey]; exists {
		t.Fatalf("清除回执后不应再包含 %s: %s", taskMetaPendingReceiptKey, task.Meta)
	}

	// 再验证 stashPendingDone 与 clearReceiptRound 同样保留其他键
	svc.stashPendingDone("task-keep-keys", "待自动接受")
	svc.clearReceiptRound("task-keep-keys")
	task, err = db.GetTask(ctx, "task-keep-keys")
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	raw = map[string]any{}
	if err := json.Unmarshal([]byte(task.Meta), &raw); err != nil {
		t.Fatalf("解析任务 Meta 失败: %v", err)
	}
	if raw["origin"] != "owner" {
		t.Fatalf("clearReceiptRound 后应保留 origin=\"owner\"，实际 Meta: %s", task.Meta)
	}
	if _, exists := raw[taskMetaPendingDoneKey]; exists {
		t.Fatalf("clearReceiptRound 后不应再包含 %s: %s", taskMetaPendingDoneKey, task.Meta)
	}
}
