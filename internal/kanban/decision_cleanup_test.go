package kanban

import (
	"context"
	"errors"
	"testing"
	"time"

	"mcode/internal/store"
)

func TestCancelClosesDecision(t *testing.T) {
	ctx := context.Background()
	svc, db, _ := setupTestService(t)
	if err := db.CreateTask(ctx, store.Task{ID: "task", Status: "needs_approval", Meta: EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"})}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task", Question: "继续？"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, " \n ", "user"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("空答案应被拒绝: %v", err)
	}
	task, _ := db.GetTask(ctx, "task")
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusCancelled, task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil || meta.DecisionID != "" {
		t.Fatalf("决策引用未清除: %+v, %v", meta, err)
	}
	if _, err := svc.AnswerDecision(ctx, d.ID, "继续", "user"); !errors.Is(err, store.ErrDecisionClosed) {
		t.Fatalf("关闭的决策卡应拒绝回答: %v", err)
	}
	task, err = db.GetTask(ctx, task.ID)
	if err != nil || task.Status != "cancelled" {
		t.Fatalf("任务应保持取消: %+v, %v", task, err)
	}
	d, err = db.GetStewardDecision(ctx, d.ID)
	if err != nil || d.Status != store.DecisionStatusSuperseded {
		t.Fatalf("决策卡应作废: %+v, %v", d, err)
	}
}

func TestAnswerDecisionRejectsTerminalTasks(t *testing.T) {
	for _, status := range []string{"done", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			svc, db, _ := setupTestService(t)
			if err := db.CreateTask(ctx, store.Task{ID: "task", Status: status}); err != nil {
				t.Fatal(err)
			}
			d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task", Question: "继续？"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := svc.AnswerDecision(ctx, d.ID, "继续", "user"); !errors.Is(err, store.ErrDecisionClosed) {
				t.Fatalf("终态任务应拒绝回答: %v", err)
			}
			task, err := db.GetTask(ctx, "task")
			if err != nil || task.Status != status {
				t.Fatalf("终态任务不应改变: %+v, %v", task, err)
			}
		})
	}
}

func TestReceiptClosesDecision(t *testing.T) {
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, watchdog, _ := setupReceiptTestService(t, driver, true, false, nil)
	thread := "executor"
	if err := db.CreateTask(ctx, store.Task{ID: "task", Status: "needs_approval", ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{DecisionID: "D-0001"})}); err != nil {
		t.Fatal(err)
	}
	d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task", Question: "继续？"})
	if err != nil {
		t.Fatal(err)
	}
	driver.setThreadState(thread, SessionState{})
	if err := svc.SubmitExecutorResult(ctx, "task", thread, ExecutorResult{Status: ResultDone, Summary: "完成"}); err != nil {
		t.Fatal(err)
	}
	if err := watchdog.Check(ctx); err != nil {
		t.Fatal(err)
	}
	d, err = db.GetStewardDecision(ctx, d.ID)
	if err != nil || d.Status != store.DecisionStatusSuperseded {
		t.Fatalf("回执后决策卡应作废: %+v, %v", d, err)
	}
	task, err := db.GetTask(ctx, "task")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil || meta.DecisionID != "" || task.Status != "review" {
		t.Fatalf("回执后任务: %+v, 元数据: %+v, 错误: %v", task, meta, err)
	}
}

func TestCancelStopsRunningSession(t *testing.T) {
	ctx := context.Background()
	svc, db, _ := setupTestService(t)
	thread := "executor"
	calls := []string{}
	svc.opts.StopSession = func(_ context.Context, id string) error {
		calls = append(calls, id)
		return nil
	}
	if err := db.CreateTask(ctx, store.Task{ID: "task", Status: "in_progress", ThreadID: &thread}); err != nil {
		t.Fatal(err)
	}
	task, _ := db.GetTask(ctx, "task")
	if _, err := svc.MoveTask(ctx, task.ID, TaskStatusCancelled, task.Revision); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != thread {
		t.Fatalf("中断会话调用: %v", calls)
	}
}

func TestDeleteTaskRemovesReferences(t *testing.T) {
	ctx := context.Background()
	svc, db, _ := setupTestService(t)
	for _, task := range []store.Task{
		{ID: "dependency", Status: "todo"},
		{ID: "dependent", Status: "todo", DependsOn: []string{"dependency"}},
		{ID: "child", Status: "todo", Meta: EncodeTaskMeta(TaskMeta{ParentID: "parent"})},
		{ID: "finished", Status: "done"},
		{ID: "parent", Status: "waiting", Meta: EncodeTaskMeta(TaskMeta{WaitKind: "children", Children: []string{"child", "finished"}})},
		{ID: "only-child", Status: "todo"},
		{ID: "only-parent", Status: "waiting", Meta: EncodeTaskMeta(TaskMeta{WaitKind: "children", Children: []string{"only-child"}})},
	} {
		if err := db.CreateTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"dependency", "child", "only-child"} {
		task, err := db.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.DeleteTask(ctx, id, task.Revision); err != nil {
			t.Fatal(err)
		}
	}
	dependent, err := db.GetTask(ctx, "dependent")
	if err != nil || len(dependent.DependsOn) != 0 {
		t.Fatalf("依赖未清理: %+v, %v", dependent, err)
	}
	if _, err := svc.StartTask(ctx, dependent.ID, dependent.Revision); err != nil {
		t.Fatalf("删除依赖后应能启动: %v", err)
	}
	if err := svc.WakeParents(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"parent", "only-parent"} {
		parent, err := db.GetTask(ctx, id)
		if err != nil || parent.Status != "todo" {
			t.Fatalf("父任务应就绪: %+v, %v", parent, err)
		}
	}
}

func TestStewardReviewClosedDecisionAndMissingSession(t *testing.T) {
	for _, missingSession := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed decision", true: "missing sessions"}[missingSession], func(t *testing.T) {
			ctx := context.Background()
			driver := newPokeDriver()
			_, db, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)
			thread, review := "executor", "reviewer"
			seedSchedulerGoal(t, ctx, db, store.Goal{ID: "goal", ProjectID: "p", Owner: goalOwnerSteward},
				store.Task{ID: "task", Status: "review", ThreadID: &thread, ReviewThreadID: &review, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner", ResultStatus: string(ResultDone), DecisionID: "D-0001"})})
			d, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "task", Question: "继续？"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.AnswerStewardDecision(ctx, d.ID, "继续", "user", time.Now()); err != nil {
				t.Fatal(err)
			}
			if !missingSession {
				driver.setThreadState(thread, SessionState{})
				driver.setThreadState(review, SessionState{})
			}
			scheduler.Tick(ctx)
			task, err := db.GetTask(ctx, "task")
			if err != nil || task.Status != "done" {
				t.Fatalf("应按回执结果收尾: %+v, %v", task, err)
			}
			meta, err := ParseTaskMeta(task.Meta)
			if err != nil || meta.DecisionID != "" {
				t.Fatalf("关闭的决策引用未清除: %+v, %v", meta, err)
			}
		})
	}
}
