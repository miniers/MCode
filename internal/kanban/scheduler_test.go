package kanban

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/store"
)

type mockPlanner struct {
	result PlanResult
	err    error
}

func TestStewardOwnerCompletionScheduler(t *testing.T) {
	testStewardOwnerCompletion(t, false)
}

func TestPlanningGoalOwnerCompletionGoesToReview(t *testing.T) {
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)
	thread, planThread := "executor", "plan"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward, PlanThreadID: &planThread}, store.Task{ID: "t", Title: "任务", Status: "in_progress", ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})})
	driver.setThreadState(thread, SessionState{})
	if err := svc.SubmitExecutorResult(ctx, "t", thread, ExecutorResult{Status: ResultDone, Summary: "结果"}); err != nil {
		t.Fatal(err)
	}
	scheduler.Tick(ctx)
	task, err := db.GetTask(ctx, "t")
	if err != nil || task.Status != "review" {
		t.Fatalf("任务=%+v 错误=%v", task, err)
	}
}

func testStewardOwnerCompletion(t *testing.T, watchdogFirst bool) {
	t.Helper()
	for _, autoReview := range []bool{false, true} {
		for _, autoAccept := range []bool{false, true} {
			t.Run(fmt.Sprintf("review=%t/accept=%t", autoReview, autoAccept), func(t *testing.T) {
				ctx := context.Background()
				driver := newPokeDriver()
				svc, db, wd, scheduler := setupReceiptTestService(t, driver, autoReview, false, nil)
				thread := "executor"
				seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward, Policy: map[string]any{"autoAccept": autoAccept}}, store.Task{ID: "t", Title: "任务", Status: "in_progress", ThreadID: &thread, AttemptCount: 1, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})})
				driver.setThreadState(thread, SessionState{})
				calls := 0
				svc.SetOnResult(func(context.Context, store.Task, ExecutorResult) { calls++ })
				if err := svc.SubmitExecutorResult(ctx, "t", thread, ExecutorResult{Status: ResultDone, Summary: "结果"}); err != nil {
					t.Fatal(err)
				}
				if watchdogFirst {
					if err := wd.Check(ctx); err != nil { t.Fatal(err) }
					scheduler.Tick(ctx)
				} else {
					scheduler.Tick(ctx)
					if err := wd.Check(ctx); err != nil { t.Fatal(err) }
				}
				scheduler.Tick(ctx)
				if err := wd.Check(ctx); err != nil { t.Fatal(err) }
				task, err := db.GetTask(ctx, "t")
				if err != nil || task.Status != "done" || calls != 1 { t.Fatalf("任务=%+v 回调=%d 错误=%v", task, calls, err) }
				goal, err := db.GetGoal(ctx, "g")
				if err != nil || goal.Status != "running" { t.Fatalf("目标=%+v 错误=%v", goal, err) }
			})
		}
	}
	for _, tc := range []struct{ owner, title, origin string }{{"user", "普通", "owner"}, {goalOwnerSteward, "总管", "delegated"}} {
		t.Run(fmt.Sprintf("%s-%s", tc.title, tc.origin), func(t *testing.T) {
			ctx := context.Background()
			driver := newPokeDriver()
			svc, db, wd, scheduler := setupReceiptTestService(t, driver, true, false, nil)
			thread := "executor"
			seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: tc.title, Owner: tc.owner}, store.Task{ID: "t", Title: "任务", Status: "in_progress", ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{Origin: tc.origin})})
			driver.setThreadState(thread, SessionState{})
			if err := svc.SubmitExecutorResult(ctx, "t", thread, ExecutorResult{Status: ResultDone, Summary: "结果"}); err != nil { t.Fatal(err) }
			if watchdogFirst { if err := wd.Check(ctx); err != nil { t.Fatal(err) } } else { scheduler.Tick(ctx) }
			task, err := db.GetTask(ctx, "t")
			if err != nil || task.Status != "review" { t.Fatalf("任务=%+v 错误=%v", task, err) }
		})
	}
}

// TestStewardOwnerReviewResolution 验证旧总管 owner 任务停在 review 时的结算边界：
// ResultStatus 为 done 转 done、failed 转 todo，有未处理决策或会话仍在运行时保持 review。
func TestStewardOwnerReviewResolution(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		result     ResultStatus
		decisionID string
		running    bool
		want       string
	}{
		{name: "done转done", result: ResultDone, want: string(TaskStatusDone)},
		{name: "failed转todo", result: ResultFailed, want: string(TaskStatusTodo)},
		{name: "未处理决策保持review", result: ResultDone, decisionID: "D-0001", want: string(TaskStatusReview)},
		{name: "会话运行中保持review", result: ResultDone, running: true, want: string(TaskStatusReview)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := newPokeDriver()
			_, db, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)
			thread := "steward-review"
			seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-steward-review", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward},
				store.Task{ID: "t-steward-review", Title: "任务", Status: string(TaskStatusReview), ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner", ResultStatus: string(tc.result), DecisionID: tc.decisionID})})
			driver.setThreadState(thread, SessionState{Running: tc.running})

			scheduler.Tick(ctx)

			task, err := db.GetTask(ctx, "t-steward-review")
			if err != nil {
				t.Fatalf("获取任务失败: %v", err)
			}
			if task.Status != tc.want {
				t.Fatalf("期望状态 %s，实际: %s", tc.want, task.Status)
			}
		})
	}
}

// TestStewardQueueGoalCompletionBoundary 验证常驻总管目标不会因所有任务 done 而被置为 completed，
// 普通目标同样情况下仍会 completed。
func TestStewardQueueGoalCompletionBoundary(t *testing.T) {
	ctx := context.Background()

	t.Run("常驻总管目标保持running", func(t *testing.T) {
		_, db, _, scheduler := setupSchedulerTestService(t, true)
		seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-steward-queue", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward},
			store.Task{ID: "t-steward-queue", Title: "任务", Status: string(TaskStatusDone)})

		scheduler.Tick(ctx)

		goal, err := db.GetGoal(ctx, "g-steward-queue")
		if err != nil {
			t.Fatalf("获取目标失败: %v", err)
		}
		if goal.Status != string(GoalStatusRunning) {
			t.Fatalf("常驻总管目标应保持 running，实际: %s", goal.Status)
		}
	})

	t.Run("普通目标完成", func(t *testing.T) {
		_, db, _, scheduler := setupSchedulerTestService(t, true)
		seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-normal-queue", ProjectID: "p", Title: "普通目标", Owner: "user"},
			store.Task{ID: "t-normal-queue", Title: "任务", Status: string(TaskStatusDone)})

		scheduler.Tick(ctx)

		goal, err := db.GetGoal(ctx, "g-normal-queue")
		if err != nil {
			t.Fatalf("获取目标失败: %v", err)
		}
		if goal.Status != string(GoalStatusCompleted) {
			t.Fatalf("普通目标应完成，实际: %s", goal.Status)
		}
		if goal.ResultSummary == nil || *goal.ResultSummary != "完成 1 个任务" {
			t.Fatalf("期望完成摘要，实际: %v", goal.ResultSummary)
		}
	})
}

func TestStewardQueuePrunesOldTerminalTasks(t *testing.T) {
	ctx := context.Background()
	svc, db, _, scheduler := setupSchedulerTestService(t, true)
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	svc.opts.Now = func() time.Time { return now }
	old := now.Add(-8 * 24 * time.Hour)
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "steward", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward},
		store.Task{ID: "old-done", Status: string(TaskStatusDone), UpdatedAt: old},
		store.Task{ID: "old-cancelled", Status: string(TaskStatusCancelled), UpdatedAt: old},
		store.Task{ID: "old-failed", Status: string(TaskStatusFailed), UpdatedAt: old},
		store.Task{ID: "old-rejected", Status: string(TaskStatusRejected), UpdatedAt: old},
		store.Task{ID: "recent-done", Status: string(TaskStatusDone), UpdatedAt: now.Add(-24 * time.Hour)},
		store.Task{ID: "boundary-done", Status: string(TaskStatusDone), UpdatedAt: now.Add(-stewardTaskRetention)},
		store.Task{ID: "old-active", Status: string(TaskStatusInProgress), UpdatedAt: old})
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "normal", ProjectID: "p", Title: "普通目标", Owner: "user"},
		store.Task{ID: "normal-done", Status: string(TaskStatusDone), UpdatedAt: old})

	scheduler.Tick(ctx)

	ids, err := db.ListGoalTaskIDs(ctx, "steward")
	if err != nil {
		t.Fatalf("获取目标任务失败: %v", err)
	}
	for _, id := range []string{"old-done", "old-cancelled", "old-failed", "old-rejected"} {
		if _, err := db.GetTask(ctx, id); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("任务 %s 应已删除，错误: %v", id, err)
		}
		for _, got := range ids {
			if got == id {
				t.Errorf("目标仍关联已删除任务 %s", id)
			}
		}
	}
	for id, status := range map[string]TaskStatus{
		"recent-done": TaskStatusDone, "boundary-done": TaskStatusDone,
		"old-active": TaskStatusInProgress, "normal-done": TaskStatusDone,
	} {
		task, err := db.GetTask(ctx, id)
		if err != nil || task.Status != string(status) {
			t.Errorf("任务 %s 状态异常: %+v，错误: %v", id, task, err)
		}
	}
	goal, err := db.GetGoal(ctx, "steward")
	if err != nil || goal.Status != string(GoalStatusRunning) {
		t.Fatalf("总管目标状态异常: %+v，错误: %v", goal, err)
	}
}

func (m *mockPlanner) Plan(ctx context.Context, goal store.Goal, feedback string) (PlanResult, error) {
	return m.result, m.err
}

func TestGoalSchedulerEndToEnd(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer s.Close()

	driver := newMockDriver()
	projects := &mockProjects{dir: t.TempDir()}

	planner := &mockPlanner{
		result: PlanResult{
			Summary: "端到端双任务规划",
			Tasks: []PlannedTask{
				{Title: "任务一", Notes: "前置基础", DependsOn: []int{}},
				{Title: "任务二", Notes: "后置依赖", DependsOn: []int{0}},
			},
		},
	}

	svc := NewService(Options{
		Store:    s,
		Git:      nil,
		Driver:   driver,
		Projects: projects,
		Planner:  planner,
	})
	defer svc.Close()

	goal, err := svc.CreateGoal(ctx, "proj-1", "完成端到端目标测试")
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}

	// 等待后台规划 goroutine 结束
	svc.wg.Wait()

	goalAfterPlan, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}
	if goalAfterPlan.Status != string(GoalStatusPlanReview) {
		t.Fatalf("期望状态为 plan_review，实际: %s", goalAfterPlan.Status)
	}

	startedGoal, err := svc.StartGoal(ctx, goalAfterPlan.ID, goalAfterPlan.Revision)
	if err != nil {
		t.Fatalf("启动目标失败: %v", err)
	}
	if startedGoal.Status != string(GoalStatusRunning) {
		t.Fatalf("期望状态为 running，实际: %s", startedGoal.Status)
	}

	taskIDs, err := s.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil || len(taskIDs) != 2 {
		t.Fatalf("期望 2 个关联任务，实际: %v", len(taskIDs))
	}
	task1ID, task2ID := taskIDs[0], taskIDs[1]

	scheduler := NewGoalScheduler(svc, 100*time.Millisecond)

	// Tick 1: 任务 1 被调度启动，进入 in_progress
	scheduler.Tick(ctx)
	t1, err := s.GetTask(ctx, task1ID)
	if err != nil {
		t.Fatalf("获取任务 1 失败: %v", err)
	}
	if t1.Status != string(TaskStatusInProgress) {
		t.Fatalf("期望任务 1 为 in_progress，实际: %s", t1.Status)
	}

	// Tick 2: 任务 1 完成会话，进入 review
	scheduler.Tick(ctx)
	t1, _ = s.GetTask(ctx, task1ID)
	if t1.Status != string(TaskStatusReview) {
		t.Fatalf("期望任务 1 为 review，实际: %s", t1.Status)
	}

	// ReviewTask accept 任务 1
	t1, err = svc.ReviewTask(ctx, t1.ID, ReviewAccept, "通过任务一", t1.Revision)
	if err != nil {
		t.Fatalf("评审任务 1 失败: %v", err)
	}
	if t1.Status != string(TaskStatusDone) {
		t.Fatalf("期望任务 1 为 done，实际: %s", t1.Status)
	}

	// Tick 3: 任务 2 依赖已满足，被调度启动，进入 in_progress
	scheduler.Tick(ctx)
	t2, err := s.GetTask(ctx, task2ID)
	if err != nil {
		t.Fatalf("获取任务 2 失败: %v", err)
	}
	if t2.Status != string(TaskStatusInProgress) {
		t.Fatalf("期望任务 2 为 in_progress，实际: %s", t2.Status)
	}

	// Tick 4: 任务 2 完成会话，进入 review
	scheduler.Tick(ctx)
	t2, _ = s.GetTask(ctx, task2ID)
	if t2.Status != string(TaskStatusReview) {
		t.Fatalf("期望任务 2 为 review，实际: %s", t2.Status)
	}

	// ReviewTask accept 任务 2
	t2, err = svc.ReviewTask(ctx, t2.ID, ReviewAccept, "通过任务二", t2.Revision)
	if err != nil {
		t.Fatalf("评审任务 2 失败: %v", err)
	}
	if t2.Status != string(TaskStatusDone) {
		t.Fatalf("期望任务 2 为 done，实际: %s", t2.Status)
	}

	// Tick 5: 所有任务已完成，goal 置为 completed
	scheduler.Tick(ctx)
	finalGoal, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取最终目标失败: %v", err)
	}
	if finalGoal.Status != string(GoalStatusCompleted) {
		t.Fatalf("期望目标状态为 completed，实际: %s", finalGoal.Status)
	}
	if finalGoal.ResultSummary == nil || *finalGoal.ResultSummary != "完成 2 个任务" {
		t.Fatalf("期望 ResultSummary 为 '完成 2 个任务'，实际: %v", finalGoal.ResultSummary)
	}

	// 断言 driver 被调用了两次（分别启动两个任务的执行会话）
	driver.mu.Lock()
	callCount := len(driver.starts)
	driver.mu.Unlock()
	if callCount != 2 {
		t.Errorf("期望 driver 启动会话 2 次，实际: %d", callCount)
	}

	// 断言 Progress 中 done = 2
	doneVal, ok := finalGoal.Progress["done"]
	if !ok {
		t.Fatalf("期望 progress 中包含 done 统计")
	}
	switch v := doneVal.(type) {
	case int:
		if v != 2 {
			t.Errorf("期望 progress done 为 2，实际: %d", v)
		}
	case float64:
		if int(v) != 2 {
			t.Errorf("期望 progress done 为 2，实际: %f", v)
		}
	default:
		t.Errorf("未知的 done 类型: %T", doneVal)
	}
}

func TestGoalSchedulerRetryFailure(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer s.Close()

	driver := newMockDriver()
	projects := &mockProjects{dir: t.TempDir()}

	planner := &mockPlanner{
		result: PlanResult{
			Summary: "重试失败测试规划",
			Tasks: []PlannedTask{
				{Title: "易失败任务", Notes: "可能执行失败", DependsOn: []int{}},
			},
		},
	}

	svc := NewService(Options{
		Store:    s,
		Git:      nil,
		Driver:   driver,
		Projects: projects,
		Planner:  planner,
	})
	defer svc.Close()

	goal, err := svc.CreateGoal(ctx, "proj-1", "测试重试上限")
	if err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	svc.wg.Wait()

	goalReady, _ := s.GetGoal(ctx, goal.ID)
	startedGoal, err := svc.StartGoal(ctx, goalReady.ID, goalReady.Revision)
	if err != nil {
		t.Fatalf("启动目标失败: %v", err)
	}

	taskIDs, _ := s.ListGoalTaskIDs(ctx, startedGoal.ID)
	if len(taskIDs) != 1 {
		t.Fatalf("期望 1 个任务")
	}
	taskID := taskIDs[0]

	scheduler := NewGoalScheduler(svc, 100*time.Millisecond)

	// 配置 mockDriver：下次会话启动后返回失败状态
	driver.defaultState = SessionState{
		Running:       false,
		Failed:        true,
		FailureReason: "build error 101",
	}

	// 1. Tick: 任务首次启动，变为 in_progress，attemptCount = 1
	scheduler.Tick(ctx)
	t1, _ := s.GetTask(ctx, taskID)
	if t1.Status != string(TaskStatusInProgress) {
		t.Fatalf("期望状态为 in_progress，实际: %s", t1.Status)
	}
	if t1.AttemptCount != 1 {
		t.Fatalf("期望 AttemptCount 为 1，实际: %d", t1.AttemptCount)
	}

	// 2. Tick: 检查会话状态发现 Failed，因为 attemptCount(1) < maxAttempts(2)，改回 todo
	scheduler.Tick(ctx)
	t1, _ = s.GetTask(ctx, taskID)
	if t1.Status != string(TaskStatusTodo) {
		t.Fatalf("第一次失败期望改回 todo，实际: %s", t1.Status)
	}
	if t1.LastFailureReason == nil || *t1.LastFailureReason != "build error 101" {
		t.Fatalf("期望 LastFailureReason 记录失败原因，实际: %v", t1.LastFailureReason)
	}

	// 3. Tick: 任务从 todo 重新启动，变为 in_progress，attemptCount = 2
	scheduler.Tick(ctx)
	t1, _ = s.GetTask(ctx, taskID)
	if t1.Status != string(TaskStatusInProgress) {
		t.Fatalf("期望第二次启动为 in_progress，实际: %s", t1.Status)
	}
	if t1.AttemptCount != 2 {
		t.Fatalf("期望 AttemptCount 为 2，实际: %d", t1.AttemptCount)
	}

	// 4. Tick: 检查会话再次 Failed，此时 attemptCount(2) >= maxAttempts(2)，改为 rejected，goal 置为 paused
	scheduler.Tick(ctx)
	t1, _ = s.GetTask(ctx, taskID)
	if t1.Status != string(TaskStatusRejected) {
		t.Fatalf("第二次失败期望置为 rejected，实际: %s", t1.Status)
	}

	pausedGoal, _ := s.GetGoal(ctx, goal.ID)
	if pausedGoal.Status != string(GoalStatusPaused) {
		t.Fatalf("期望 goal 置为 paused，实际: %s", pausedGoal.Status)
	}
	if pausedGoal.StatusReason == nil || !strings.Contains(*pausedGoal.StatusReason, "易失败任务") {
		t.Fatalf("期望 StatusReason 说明哪个任务失败，实际: %v", pausedGoal.StatusReason)
	}
}

// setupSchedulerTestService 创建调度器测试用的服务，autoReview 控制自动评审开关。
func setupSchedulerTestService(t *testing.T, autoReview bool) (*Service, *store.Store, *mockDriver, *GoalScheduler) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "scheduler.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	driver := newMockDriver()
	svc := NewService(Options{
		Store:      s,
		Driver:     driver,
		Projects:   &mockProjects{dir: t.TempDir()},
		AutoReview: func() bool { return autoReview },
	})
	t.Cleanup(func() { svc.Close() })

	return svc, s, driver, NewGoalScheduler(svc, time.Minute)
}

// seedSchedulerGoal 直接落库一个目标及其任务，便于按需构造调度场景。
func seedSchedulerGoal(t *testing.T, ctx context.Context, s *store.Store, goal store.Goal, tasks ...store.Task) {
	t.Helper()
	if goal.Status == "" {
		goal.Status = string(GoalStatusRunning)
	}
	if err := s.CreateGoal(ctx, goal); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	goalID := goal.ID
	ids := make([]string, 0, len(tasks))
	for _, task := range tasks {
		task.GoalID = &goalID
		if task.Status == "" {
			task.Status = string(TaskStatusTodo)
		}
		if err := s.CreateTask(ctx, task); err != nil {
			t.Fatalf("创建任务失败: %v", err)
		}
		ids = append(ids, task.ID)
	}
	if err := s.SetGoalTasks(ctx, goal.ID, ids, 1); err != nil {
		t.Fatalf("关联目标任务失败: %v", err)
	}
}

// TestGoalSchedulerNeedsApprovalPaths 覆盖 needs_approval 的回归路径：
// 仍在等待审批或输入时任务与槽位都不动，会话恢复运行后改回 in_progress，会话结束时释放槽位。
func TestGoalSchedulerNeedsApprovalPaths(t *testing.T) {
	ctx := context.Background()

	t.Run("等待审批时保持needs_approval并保留ThreadID", func(t *testing.T) {
		_, s, driver, scheduler := setupSchedulerTestService(t, true)
		threadID := "thread-wait-approval"
		seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-wait-approval", ProjectID: "proj-1", Title: "等待审批"},
			store.Task{ID: "task-wait-approval", Title: "待审批任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
		driver.setThreadState(threadID, SessionState{Running: true, AwaitingApproval: true})

		scheduler.Tick(ctx)

		latest, err := s.GetTask(ctx, "task-wait-approval")
		if err != nil {
			t.Fatalf("获取任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusNeedsApproval) {
			t.Fatalf("期望状态为 needs_approval，实际: %s", latest.Status)
		}
		if latest.ThreadID == nil || *latest.ThreadID != threadID {
			t.Fatalf("等待审批时应保留 ThreadID，实际: %v", latest.ThreadID)
		}
		if latest.LastActivityText == nil || *latest.LastActivityText != awaitingUserText {
			t.Fatalf("期望写入等待文案，实际: %v", latest.LastActivityText)
		}
	})

	t.Run("等待输入时保持needs_approval", func(t *testing.T) {
		_, s, driver, scheduler := setupSchedulerTestService(t, true)
		threadID := "thread-wait-input"
		seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-wait-input", ProjectID: "proj-1", Title: "等待输入"},
			store.Task{ID: "task-wait-input", Title: "待输入任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
		driver.setThreadState(threadID, SessionState{Running: true, AwaitingInput: true})

		scheduler.Tick(ctx)

		latest, err := s.GetTask(ctx, "task-wait-input")
		if err != nil {
			t.Fatalf("获取任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusNeedsApproval) {
			t.Fatalf("等待输入时期望状态为 needs_approval，实际: %s", latest.Status)
		}
		if latest.ThreadID == nil || *latest.ThreadID != threadID {
			t.Fatalf("等待输入时应保留 ThreadID，实际: %v", latest.ThreadID)
		}
	})

	t.Run("会话恢复运行时改回in_progress", func(t *testing.T) {
		_, s, driver, scheduler := setupSchedulerTestService(t, true)
		threadID := "thread-resumed"
		seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-resumed", ProjectID: "proj-1", Title: "恢复运行"},
			store.Task{ID: "task-resumed", Title: "恢复任务", Status: string(TaskStatusNeedsApproval), ThreadID: &threadID})
		driver.setThreadState(threadID, SessionState{Running: true})

		scheduler.Tick(ctx)

		latest, err := s.GetTask(ctx, "task-resumed")
		if err != nil {
			t.Fatalf("获取任务失败: %v", err)
		}
		if latest.Status != string(TaskStatusInProgress) {
			t.Fatalf("会话恢复运行时期望改回 in_progress，实际: %s", latest.Status)
		}
	})

	t.Run("会话结束后释放槽位并转review", func(t *testing.T) {
		_, s, driver, scheduler := setupSchedulerTestService(t, true)
		threadID := "thread-ended"
		seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-ended", ProjectID: "proj-1", Title: "会话结束"},
			store.Task{ID: "task-ended", Title: "已结束任务", Status: string(TaskStatusNeedsApproval), ThreadID: &threadID},
			store.Task{ID: "task-next", Title: "下一个任务"})
		driver.setThreadState(threadID, SessionState{Running: false, LastAssistantText: "处理完毕"})

		scheduler.Tick(ctx)

		ended, err := s.GetTask(ctx, "task-ended")
		if err != nil {
			t.Fatalf("获取任务失败: %v", err)
		}
		if ended.Status != string(TaskStatusReview) {
			t.Fatalf("会话结束后期望转为 review，实际: %s", ended.Status)
		}
		next, err := s.GetTask(ctx, "task-next")
		if err != nil {
			t.Fatalf("获取任务失败: %v", err)
		}
		if next.Status != string(TaskStatusInProgress) {
			t.Fatalf("槽位释放后期望启动下一个任务，实际: %s", next.Status)
		}
	})
}

// TestGoalSchedulerSkipsUnchangedProgress 验证进度未变化时不重复改写目标版本号。
func TestGoalSchedulerSkipsUnchangedProgress(t *testing.T) {
	ctx := context.Background()
	_, s, driver, scheduler := setupSchedulerTestService(t, true)
	threadID := "thread-progress"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-progress", ProjectID: "proj-1", Title: "进度去重"},
		store.Task{ID: "task-progress", Title: "执行中任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: true})

	scheduler.Tick(ctx)
	before, err := s.GetGoal(ctx, "goal-progress")
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}
	scheduler.Tick(ctx)
	after, err := s.GetGoal(ctx, "goal-progress")
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("进度未变化时不应更新目标，版本号 %d -> %d", before.Revision, after.Revision)
	}
}

// TestGoalSchedulerManualReviewWhenSwitchesOff 验证自动接受与自动评审都关闭时任务离开 in_progress 转入 review。
func TestGoalSchedulerManualReviewWhenSwitchesOff(t *testing.T) {
	ctx := context.Background()
	_, s, driver, scheduler := setupSchedulerTestService(t, false)
	threadID := "thread-manual-review"
	seedSchedulerGoal(t, ctx, s, store.Goal{
		ID:        "goal-manual-review",
		ProjectID: "proj-1",
		Title:     "人工处理",
		Policy:    map[string]any{"maxConcurrency": 1, "autoAccept": false},
	}, store.Task{ID: "task-manual-review", Title: "人工任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false, LastAssistantText: "执行完成"})

	scheduler.Tick(ctx)

	latest, err := s.GetTask(ctx, "task-manual-review")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.Status != string(TaskStatusReview) {
		t.Fatalf("两个开关都关闭时期望转为 review，实际: %s", latest.Status)
	}
	if latest.LastActivityText == nil || *latest.LastActivityText != manualReviewText {
		t.Fatalf("期望写入人工处理文案，实际: %v", latest.LastActivityText)
	}
}

// TestGoalSchedulerCrossGoalDependency 验证依赖其他目标里已完成的任务时也能启动。
func TestGoalSchedulerCrossGoalDependency(t *testing.T) {
	ctx := context.Background()
	_, s, _, scheduler := setupSchedulerTestService(t, true)
	depTaskID := "task-other-goal-done"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-other", ProjectID: "proj-1", Title: "其他目标"},
		store.Task{ID: depTaskID, Title: "跨目标前置任务", Status: string(TaskStatusDone)})
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-dependent", ProjectID: "proj-1", Title: "跨目标依赖"},
		store.Task{ID: "task-dependent", Title: "跨目标后续任务", DependsOn: []string{depTaskID}})

	scheduler.Tick(ctx)

	latest, err := s.GetTask(ctx, "task-dependent")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.Status != string(TaskStatusInProgress) {
		t.Fatalf("跨目标依赖已完成时期望启动任务，实际: %s", latest.Status)
	}
}

// failingStartDriver 让会话启动固定失败，用于验证调度器记录启动失败原因。
type failingStartDriver struct {
	*mockDriver
	err error
}

func (d *failingStartDriver) StartSession(ctx context.Context, req SessionRequest) (string, error) {
	return "", d.err
}

// TestGoalSchedulerStartFailureRecordedOnce 验证启动失败会写入原因，且同一错误只记录一次。
func TestGoalSchedulerStartFailureRecordedOnce(t *testing.T) {
	ctx := context.Background()
	svc, s, mock, scheduler := setupSchedulerTestService(t, true)
	startErr := errors.New("session boom")
	svc.opts.Driver = &failingStartDriver{mockDriver: mock, err: startErr}

	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-start-fail", ProjectID: "proj-1", Title: "启动失败"},
		store.Task{ID: "task-start-fail", Title: "启动失败任务"})

	scheduler.Tick(ctx)
	first, err := s.GetTask(ctx, "task-start-fail")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if first.Status != string(TaskStatusTodo) {
		t.Fatalf("启动失败后任务应保持 todo，实际: %s", first.Status)
	}
	if first.LastFailureReason == nil || !strings.Contains(*first.LastFailureReason, "启动任务失败") ||
		!strings.Contains(*first.LastFailureReason, startErr.Error()) {
		t.Fatalf("期望写入启动失败原因，实际: %v", first.LastFailureReason)
	}

	scheduler.Tick(ctx)
	second, err := s.GetTask(ctx, "task-start-fail")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if second.Revision != first.Revision {
		t.Fatalf("同一错误不应重复写库，版本号 %d -> %d", first.Revision, second.Revision)
	}
}
