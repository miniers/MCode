package kanban

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/store"
)

// TestWriteConcurrencyManager 校验写并发锁管理器的并发限流、排他与幂等释放特性。
func TestWriteConcurrencyManager(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	mgr := NewWriteConcurrencyManager()

	// 1. 同一 session 最多 4 个写操作，第 5 个返回 ErrRateLimitExceeded
	sess1 := "sess-1"
	r1, err := mgr.Acquire(sess1, "t1", "req1")
	if err != nil {
		t.Fatalf("Acquire 1 失败: %v", err)
	}
	r2, err := mgr.Acquire(sess1, "t2", "req2")
	if err != nil {
		t.Fatalf("Acquire 2 失败: %v", err)
	}
	r3, err := mgr.Acquire(sess1, "t3", "req3")
	if err != nil {
		t.Fatalf("Acquire 3 失败: %v", err)
	}
	r4, err := mgr.Acquire(sess1, "t4", "req4")
	if err != nil {
		t.Fatalf("Acquire 4 失败: %v", err)
	}

	_, err = mgr.Acquire(sess1, "t5", "req5")
	if !errors.Is(err, ErrRateLimitExceeded) {
		t.Fatalf("期望 ErrRateLimitExceeded，实际: %v", err)
	}

	// 2. 同一 taskID 重叠时直接拒绝 ErrTaskAlreadyInFlight
	sess2 := "sess-2"
	_, err = mgr.Acquire(sess2, "t1", "req-diff")
	if !errors.Is(err, ErrTaskAlreadyInFlight) {
		t.Fatalf("期望 ErrTaskAlreadyInFlight，实际: %v", err)
	}

	// 3. 释放 r4 之后测试同一 requestID 重叠时拒绝 ErrRequestAlreadyInFlight
	r4()
	_, err = mgr.Acquire(sess1, "t-other", "req1")
	if !errors.Is(err, ErrRequestAlreadyInFlight) {
		t.Fatalf("期望 ErrRequestAlreadyInFlight，实际: %v", err)
	}

	// 4. 释放 r1，重复调用安全
	r1()
	r1() // 幂等调用不 panic

	// release 之后对应 task 和 request 锁释放，并能重新获取
	rReacquire, err := mgr.Acquire(sess1, "t1", "req1")
	if err != nil {
		t.Fatalf("释放后重新获取失败: %v", err)
	}
	rReacquire()
	r2()
	r3()

	// 5. 多 goroutine 并发 Acquire/release
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			s := fmt.Sprintf("sess-concurrent-%d", idx%3)
			tid := fmt.Sprintf("task-concurrent-%d", idx)
			req := fmt.Sprintf("req-concurrent-%d", idx)
			rel, aerr := mgr.Acquire(s, tid, req)
			if aerr == nil {
				time.Sleep(2 * time.Millisecond)
				rel()
				rel() // 测试重复调用安全性
			}
		}(i)
	}
	wg.Wait()
}

// TestWriteConcurrencyAndMoveTaskRace 保证在 -race 下 Service.MoveTask 与 AcquireWrite 无竞态。
func TestWriteConcurrencyAndMoveTaskRace(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-race",
		Title:     "竞态测试任务",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 15; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rel, aerr := svc.AcquireWrite(fmt.Sprintf("sess-%d", idx), task.ID, fmt.Sprintf("req-%d", idx))
			if aerr == nil {
				defer rel()
			}
			// 并发调用 MoveTask（即便返回 revision 冲突或流转错误也不应引起 data race）
			_, _ = svc.MoveTask(ctx, task.ID, TaskStatusInProgress, task.Revision)
		}(i)
	}
	wg.Wait()
}

// TestTaskStatusTransitions 校验非法流转被拒与合法流转推进。
func TestTaskStatusTransitions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()
	svc, _, _ := setupTestService(t)

	// 1. 合法流转: todo -> in_progress -> needs_approval -> review -> done -> todo -> rejected -> todo
	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "流转测试"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// todo -> in_progress
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusInProgress, task.Revision)
	if err != nil {
		t.Fatalf("todo -> in_progress 失败: %v", err)
	}

	// in_progress -> needs_approval
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusNeedsApproval, task.Revision)
	if err != nil {
		t.Fatalf("in_progress -> needs_approval 失败: %v", err)
	}

	// needs_approval -> review
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusReview, task.Revision)
	if err != nil {
		t.Fatalf("needs_approval -> review 失败: %v", err)
	}

	// review -> done
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusDone, task.Revision)
	if err != nil {
		t.Fatalf("review -> done 失败: %v", err)
	}

	// 2. 非法流转: done -> in_progress 必须被拒绝
	_, err = svc.MoveTask(ctx, task.ID, TaskStatusInProgress, task.Revision)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("期望 done -> in_progress 返回 ErrInvalidTransition，实际: %v", err)
	}

	// done -> todo 合法
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusTodo, task.Revision)
	if err != nil {
		t.Fatalf("done -> todo 失败: %v", err)
	}

	// todo -> rejected 合法
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusRejected, task.Revision)
	if err != nil {
		t.Fatalf("todo -> rejected 失败: %v", err)
	}

	// 3. 非法流转: rejected -> review 必须被拒绝
	_, err = svc.MoveTask(ctx, task.ID, TaskStatusReview, task.Revision)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("期望 rejected -> review 返回 ErrInvalidTransition，实际: %v", err)
	}

	// rejected -> todo 合法
	task, err = svc.MoveTask(ctx, task.ID, TaskStatusTodo, task.Revision)
	if err != nil {
		t.Fatalf("rejected -> todo 失败: %v", err)
	}
}

// TestOccupiedSlotsAndTickGoal 校验 occupiedSlots 及调度器对活跃审查会话的槽位考量。
func TestOccupiedSlotsAndTickGoal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	// 1. occupiedSlots 纯函数计数测试
	reviewActiveTaskID := "task-rev-active"
	tasks := []store.Task{
		{ID: "t-prog", Status: string(TaskStatusInProgress)},
		{ID: "t-appr", Status: string(TaskStatusNeedsApproval)},
		{ID: reviewActiveTaskID, Status: string(TaskStatusReview)},
		{ID: "t-rev-inactive", Status: string(TaskStatusReview)},
		{ID: "t-todo", Status: string(TaskStatusTodo)},
		{ID: "t-done", Status: string(TaskStatusDone)},
	}

	// isReviewActive 为 nil 时不计入 review
	if count := occupiedSlots(tasks, nil); count != 2 {
		t.Fatalf("isReviewActive 为 nil 时期望槽位数 2，实际为 %d", count)
	}

	// isReviewActive 对指定任务返回 true 时计入该 review 任务
	countWithHook := occupiedSlots(tasks, func(id string) bool {
		return id == reviewActiveTaskID
	})
	if countWithHook != 3 {
		t.Fatalf("isReviewActive 活跃时期望槽位数 3，实际为 %d", countWithHook)
	}

	// 2. tickGoal 在 review 活跃时不会超出 maxConcurrency 继续启动任务
	dbPath := filepath.Join(t.TempDir(), "t.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	defer s.Close()

	driver := newMockDriver()
	projects := &mockProjects{dir: t.TempDir()}

	svc := NewService(Options{
		Store:    s,
		Git:      nil,
		Driver:   driver,
		Projects: projects,
	})
	defer svc.Close()

	projID := "proj-sched-slot"
	goal := store.Goal{
		ID:        "goal-slot-1",
		ProjectID: projID,
		Title:     "槽位目标",
		Prompt:    "槽位目标说明",
		Status:    string(GoalStatusRunning),
		Policy:    map[string]any{"maxConcurrency": 1},
		Progress:  map[string]any{},
	}
	if err := s.CreateGoal(ctx, goal); err != nil {
		t.Fatalf("创建目标失败: %v", err)
	}
	createdGoal, err := s.GetGoal(ctx, goal.ID)
	if err != nil {
		t.Fatalf("获取目标失败: %v", err)
	}

	revThreadID := "rev-thread-active"
	taskReview := store.Task{
		ID:             "t-review-running",
		ProjectID:      &projID,
		GoalID:         &goal.ID,
		Title:          "审查中的任务",
		Status:         string(TaskStatusReview),
		ReviewThreadID: &revThreadID,
		DependsOn:      []string{},
	}
	if err := s.CreateTask(ctx, taskReview); err != nil {
		t.Fatalf("创建审查任务失败: %v", err)
	}

	taskTodo := store.Task{
		ID:        "t-todo-waiting",
		ProjectID: &projID,
		GoalID:    &goal.ID,
		Title:     "等待中的任务",
		Status:    string(TaskStatusTodo),
		DependsOn: []string{},
	}
	if err := s.CreateTask(ctx, taskTodo); err != nil {
		t.Fatalf("创建待办任务失败: %v", err)
	}

	if err := s.SetGoalTasks(ctx, goal.ID, []string{taskReview.ID, taskTodo.ID}, createdGoal.Revision); err != nil {
		t.Fatalf("关联目标任务失败: %v", err)
	}

	// 设置审查会话正在运行
	driver.setThreadState(revThreadID, SessionState{Running: true})

	sched := NewGoalScheduler(svc, time.Minute)
	sched.Tick(ctx)

	// 校验 taskTodo 未被启动（由于并发槽位为 1 且 review 处于活跃中）
	afterTickTodo, err := s.GetTask(ctx, taskTodo.ID)
	if err != nil {
		t.Fatalf("获取待办任务失败: %v", err)
	}
	if afterTickTodo.Status != string(TaskStatusTodo) {
		t.Fatalf("审查活跃时待办任务不应启动，实际状态: %s", afterTickTodo.Status)
	}

	// 审查会话结束后，下次调度应空出槽位并启动待办任务
	driver.setThreadState(revThreadID, SessionState{Running: false})
	sched.Tick(ctx)

	afterTickDone, err := s.GetTask(ctx, taskTodo.ID)
	if err != nil {
		t.Fatalf("获取待办任务失败: %v", err)
	}
	if afterTickDone.Status != string(TaskStatusInProgress) {
		t.Fatalf("审查结束后槽位空出，待办任务应被启动为 in_progress，实际状态: %s", afterTickDone.Status)
	}
}

// TestOptimisticDispatch 校验乐观派发在 30s 内推导为 in_progress，超时后自愈恢复为持久化真实状态。
func TestOptimisticDispatch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	nowBase := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	currentTime := nowBase

	dbPath := filepath.Join(t.TempDir(), "t.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer s.Close()

	driver := newMockDriver()
	projects := &mockProjects{dir: t.TempDir()}

	svc := NewService(Options{
		Store:    s,
		Driver:   driver,
		Projects: projects,
		Now: func() time.Time {
			return currentTime
		},
	})
	defer svc.Close()

	task, err := svc.CreateTask(ctx, CreateTaskInput{
		ProjectID: "proj-opt",
		Title:     "乐观派发任务",
	})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 初始状态为 todo
	board, err := svc.Board(ctx)
	if err != nil {
		t.Fatalf("获取看板失败: %v", err)
	}
	if len(board.Tasks) != 1 || board.Tasks[0].Derived.DisplayColumn != DisplayColumnTodo {
		t.Fatalf("初始看板列期望 todo，实际: %v", board.Tasks[0].Derived.DisplayColumn)
	}

	// 模拟开始派发标记（置入内存 optimistic 映射）
	svc.optimisticMu.Lock()
	svc.optimistic[task.ID] = currentTime
	svc.optimisticMu.Unlock()

	// 派发中途查询看板，应被推导为 in_progress 列
	boardOpt, err := svc.Board(ctx)
	if err != nil {
		t.Fatalf("获取看板失败: %v", err)
	}
	if boardOpt.Tasks[0].Derived.DisplayColumn != DisplayColumnInProgress {
		t.Fatalf("乐观派发期间看板列期望 in_progress，实际: %v", boardOpt.Tasks[0].Derived.DisplayColumn)
	}

	// 超过 30s（前进 31 秒），自动自愈恢复为持久化的 todo 列
	currentTime = currentTime.Add(31 * time.Second)
	boardExpired, err := svc.Board(ctx)
	if err != nil {
		t.Fatalf("获取看板失败: %v", err)
	}
	if boardExpired.Tasks[0].Derived.DisplayColumn != DisplayColumnTodo {
		t.Fatalf("超过 30s 后看板列应恢复为 todo，实际: %v", boardExpired.Tasks[0].Derived.DisplayColumn)
	}
}

// TestGoalProgressStalled 校验目标停滞判定逻辑。
func TestGoalProgressStalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// 1. 全部失败或等待用户 -> running == 0 && ready == 0 && done < total => stalled == true
	stalledTasks := []store.Task{
		{ID: "t1", Status: string(TaskStatusRejected)},
		{ID: "t2", Status: string(TaskStatusNeedsApproval)},
	}
	progStalled := GoalProgress(stalledTasks)
	if isStalled, ok := progStalled["stalled"].(bool); !ok || !isStalled {
		t.Fatalf("无运行无就绪且未完成时期望 stalled 为 true，实际: %v", progStalled["stalled"])
	}

	// 2. 有就绪可启动任务 (ready > 0) => stalled == false
	readyTasks := []store.Task{
		{ID: "t1", Status: string(TaskStatusRejected)},
		{ID: "t2", Status: string(TaskStatusTodo)}, // 无前置依赖，ready = 1
	}
	progReady := GoalProgress(readyTasks)
	if isStalled, ok := progReady["stalled"].(bool); !ok || isStalled {
		t.Fatalf("有就绪任务时期望 stalled 为 false，实际: %v", progReady["stalled"])
	}

	// 3. 有运行中任务 (running > 0) => stalled == false
	runningTasks := []store.Task{
		{ID: "t1", Status: string(TaskStatusInProgress)},
		{ID: "t2", Status: string(TaskStatusRejected)},
	}
	progRunning := GoalProgress(runningTasks)
	if isStalled, ok := progRunning["stalled"].(bool); !ok || isStalled {
		t.Fatalf("有运行任务时期望 stalled 为 false，实际: %v", progRunning["stalled"])
	}

	// 4. 全部完成 (done == total) => stalled == false
	completedTasks := []store.Task{
		{ID: "t1", Status: string(TaskStatusDone)},
		{ID: "t2", Status: string(TaskStatusDone)},
	}
	progCompleted := GoalProgress(completedTasks)
	if isStalled, ok := progCompleted["stalled"].(bool); !ok || isStalled {
		t.Fatalf("全部完成时期望 stalled 为 false，实际: %v", progCompleted["stalled"])
	}
}
