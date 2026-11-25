package kanban

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/store"
)

// setThreadState 设置指定会话的状态，便于构造「会话已停止」等场景。
func (d *pokeDriver) setThreadState(threadID string, st SessionState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.states[threadID] = st
}

// setupReceiptTestService 创建带巡视器与调度器的看板服务，Now 可注入。
func setupReceiptTestService(t *testing.T, driver SessionDriver, autoReview, autoStart bool, nowFn func() time.Time) (*Service, *store.Store, *Watchdog, *GoalScheduler) {
	t.Helper()
	s := store.OpenForTest(t)

	svc := NewService(Options{
		Store:             s,
		Driver:            driver,
		Projects:          &mockProjects{dir: t.TempDir()},
		AutoReview:        func() bool { return autoReview },
		AutoStartOnCreate: func() bool { return autoStart },
		Now:               nowFn,
	})
	t.Cleanup(func() { svc.Close() })

	return svc, s, NewWatchdog(svc, 0), NewGoalScheduler(svc, time.Minute)
}

// TestReceiptDoneMovesTaskToReview 验证 done 回执让会话停止的任务进入 review，活动文本使用回执摘要。
func TestReceiptDoneMovesTaskToReview(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, s, wd, _ := setupReceiptTestService(t, driver, true, true, nil)

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "回执完成任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.ThreadID == nil {
		t.Fatal("自动启动后应写入 ThreadID")
	}
	if err := svc.SubmitTaskResult(ctx, task.ID, *task.ThreadID, ReceiptOutcomeDone, "  修复了登录重定向，验收通过  "); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}
	driver.setThreadState(*task.ThreadID, SessionState{Running: false, LastAssistantText: "旧文本不应覆盖回执"})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}

	latest, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.Status != string(TaskStatusReview) {
		t.Fatalf("期望状态为 review，实际: %s", latest.Status)
	}
	if latest.LastActivityText == nil || *latest.LastActivityText != "修复了登录重定向，验收通过" {
		t.Fatalf("期望活动文本为回执摘要，实际: %v", latest.LastActivityText)
	}
	if len(driver.sentFollowUps()) != 0 {
		t.Fatalf("有回执时不应追问，实际追问了 %d 次", len(driver.sentFollowUps()))
	}
}

func TestWaitingTaskCanSubmitReceiptOnlyFromItsThread(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, wd, _ := setupReceiptTestService(t, driver, true, false, nil)
	thread := "waiting-executor"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-waiting-receipt", ProjectID: "p", Title: "waiting receipt"},
		store.Task{ID: "t-waiting-receipt", Title: "任务", Status: string(TaskStatusWaiting), ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{WaitKind: "machine", WakeAt: time.Now().Add(time.Hour).Format(time.RFC3339), WaitCheck: "true", WaitBase: "base", Wakes: 2})})

	if err := svc.SubmitTaskResult(ctx, "t-waiting-receipt", "other-thread", ReceiptOutcomeDone, "done"); err == nil {
		t.Fatal("其他会话不应提交 waiting 任务结果")
	}
	if err := svc.SubmitTaskResult(ctx, "t-waiting-receipt", thread, ReceiptOutcomeDone, "完成"); err != nil {
		t.Fatalf("任务执行会话应能提交结果: %v", err)
	}
	latest, err := db.GetTask(ctx, "t-waiting-receipt")
	if err != nil {
		t.Fatal(err)
	}
	meta, err := ParseTaskMeta(latest.Meta)
	if err != nil || latest.Status != string(TaskStatusInProgress) || meta.WaitKind != "" || meta.WakeAt != "" || meta.WaitCheck != "" || meta.WaitBase != "" || meta.Wakes != 0 {
		t.Fatalf("提交时应恢复执行并清理等待元数据: task=%+v meta=%+v err=%v", latest, meta, err)
	}
	driver.setThreadState(thread, SessionState{})
	if err := wd.Check(ctx); err != nil {
		t.Fatal(err)
	}
	latest, err = db.GetTask(ctx, "t-waiting-receipt")
	if err != nil || latest.Status != string(TaskStatusReview) {
		t.Fatalf("waiting 任务应由回执结算至 review: task=%+v err=%v", latest, err)
	}
	meta, err = ParseTaskMeta(latest.Meta)
	if err != nil || meta.ResultStatus != string(ResultDone) {
		t.Fatalf("waiting 回执应记录为 done: meta=%+v err=%v", meta, err)
	}
}

func TestDelegatingBeesSuppressReceiptFollowUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)
	thread := "delegating-lead"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-delegating", ProjectID: "p", Title: "目标"},
		store.Task{ID: "t-delegating", Title: "任务", Status: string(TaskStatusInProgress), ThreadID: &thread})
	driver.setThreadState(thread, SessionState{})
	var checked string
	svc.opts.DelegatingBees = func(id string) bool { checked = id; return true }
	scheduler.Tick(ctx)
	if checked != thread || len(driver.sentFollowUps()) != 0 {
		t.Fatalf("lead 有运行中的 bee 时应跳过催交: checked=%q followups=%v", checked, driver.sentFollowUps())
	}
	task, err := db.GetTask(ctx, "t-delegating")
	if err != nil || task.Status != string(TaskStatusInProgress) {
		t.Fatalf("等待 bee 结果时任务应保持执行中: task=%+v err=%v", task, err)
	}
}

func TestOpenDecisionKeepsOrdinaryGoalTaskInApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	_, db, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)
	thread := "ordinary-goal-executor"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-open-decision", ProjectID: "p", Title: "普通目标"},
		store.Task{ID: "t-open-decision", Title: "任务", Status: string(TaskStatusNeedsApproval), ThreadID: &thread})
	decision, err := db.CreateStewardDecision(ctx, store.StewardDecision{TaskID: "t-open-decision", Question: "继续？"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := db.GetTask(ctx, "t-open-decision")
	if err != nil {
		t.Fatal(err)
	}
	task.Meta = EncodeTaskMeta(TaskMeta{DecisionID: decision.ID})
	if _, err := db.UpdateTask(ctx, task, task.Revision); err != nil {
		t.Fatal(err)
	}
	driver.setThreadState(thread, SessionState{Running: true})

	scheduler.Tick(ctx)

	task, err = db.GetTask(ctx, "t-open-decision")
	if err != nil || task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("open 决策期间任务应保持 needs_approval: task=%+v err=%v", task, err)
	}
	decision, err = db.GetStewardDecision(ctx, decision.ID)
	if err != nil || decision.Status != store.DecisionStatusOpen {
		t.Fatalf("会话运行中不得关闭 open 决策: decision=%+v err=%v", decision, err)
	}
}

func TestExecutorResultFailedAndDecision(t *testing.T) {
	t.Parallel()
	for _, status := range []ResultStatus{ResultFailed, ResultNeedsDecision} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			driver := newPokeDriver()
			svc, db, wd, _ := setupReceiptTestService(t, driver, true, true, nil)
			task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "p", Title: "任务"})
			if err != nil || task.ThreadID == nil {
				t.Fatalf("创建任务 %v", err)
			}
			res := ExecutorResult{Status: status, Summary: "结果", Question: "继续吗", Options: []DecisionOption{{Key: "A", Text: "继续"}, {Key: "B", Text: "停下"}}}
			for i := 0; i < 2; i++ {
				if i > 0 && status == ResultFailed {
					break
				}
				if err := svc.SubmitExecutorResult(ctx, task.ID, *task.ThreadID, res); err != nil {
					t.Fatal(err)
				}
				driver.setThreadState(*task.ThreadID, SessionState{})
				if err := wd.Check(ctx); err != nil {
					t.Fatal(err)
				}
			}
			latest, err := db.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			meta, err := ParseTaskMeta(latest.Meta)
			if err != nil {
				t.Fatal(err)
			}
			if status == ResultFailed && (latest.Status != "failed" || meta.Summary != "结果") {
				t.Fatalf("失败状态 %#v", latest)
			}
			if status == ResultNeedsDecision {
				decisions, err := db.ListStewardDecisions(ctx, "", 0)
				if err != nil || latest.Status != "needs_approval" || len(decisions) != 1 || meta.DecisionID != "D-0001" {
					t.Fatalf("决策状态 %#v %#v %v", latest, decisions, err)
				}
			}
		})
	}
}

func TestStewardOwnerFailureBudgetAndDecision(t *testing.T) {
	t.Parallel()
	for _, watchdogFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "scheduler", true: "watchdog"}[watchdogFirst], func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			driver := newPokeDriver()
			svc, db, wd, scheduler := setupReceiptTestService(t, driver, true, false, nil)
			thread := "executor"
			seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward}, store.Task{ID: "t", Title: "任务", Status: "in_progress", ThreadID: &thread, AttemptCount: 1, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})})
			driver.setThreadState(thread, SessionState{})
			calls := 0
			svc.SetOnResult(func(context.Context, store.Task, ExecutorResult) { calls++ })
			finish := func(res ExecutorResult) {
				t.Helper()
				if err := svc.SubmitExecutorResult(ctx, "t", thread, res); err != nil {
					t.Fatal(err)
				}
				if watchdogFirst {
					if err := wd.Check(ctx); err != nil {
						t.Fatal(err)
					}
				} else {
					scheduler.Tick(ctx)
				}
			}
			finish(ExecutorResult{Status: ResultFailed, Summary: "失败"})
			task, _ := db.GetTask(ctx, "t")
			if task.Status != "todo" || calls != 0 {
				t.Fatalf("首次失败: %+v 回调=%d", task, calls)
			}
			task.Status, task.AttemptCount = "in_progress", 2
			if _, err := db.UpdateTask(ctx, task, task.Revision); err != nil {
				t.Fatal(err)
			}
			finish(ExecutorResult{Status: ResultFailed, Summary: "再次失败"})
			scheduler.Tick(ctx)
			if err := wd.Check(ctx); err != nil {
				t.Fatal(err)
			}
			task, _ = db.GetTask(ctx, "t")
			goal, _ := db.GetGoal(ctx, "g")
			if task.Status != "failed" || calls != 1 || goal.Status != "running" {
				t.Fatalf("最终失败: %+v 回调=%d 目标=%s", task, calls, goal.Status)
			}
		})
	}
}

func TestStewardOwnerDecisionRemainsBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, wd, scheduler := setupReceiptTestService(t, driver, true, false, nil)
	thread := "executor"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward}, store.Task{ID: "t", Title: "任务", Status: "in_progress", ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})})
	driver.setThreadState(thread, SessionState{})
	res := ExecutorResult{Status: ResultNeedsDecision, Summary: "请选择", Question: "继续吗", Options: []DecisionOption{{Key: "A", Text: "继续"}, {Key: "B", Text: "停下"}}}
	if err := svc.SubmitExecutorResult(ctx, "t", thread, res); err != nil {
		t.Fatal(err)
	}
	if err := wd.Check(ctx); err != nil {
		t.Fatal(err)
	}
	scheduler.Tick(ctx)
	task, _ := db.GetTask(ctx, "t")
	if task.Status != "needs_approval" {
		t.Fatalf("决策必须等待: %+v", task)
	}
}

// TestStewardOwnerMissingReceiptUsesAssistantText 验证总管 owner 任务缺回执、会话调不到回执工具时，
// 用最后一条助手文本作为结果摘要并直接结算为 done。
func TestStewardOwnerMissingReceiptUsesAssistantText(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)

	thread := "steward-owner-text"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-steward-text", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward},
		store.Task{ID: "t-steward-text", Title: "任务", Status: string(TaskStatusInProgress), ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})})
	driver.setThreadState(thread, SessionState{Running: false, LastAssistantText: "  已补齐总管任务  ", HostToolsUnavailable: true})
	calls := 0
	svc.SetOnResult(func(context.Context, store.Task, ExecutorResult) { calls++ })

	scheduler.Tick(ctx)

	task, err := db.GetTask(ctx, "t-steward-text")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if task.Status != string(TaskStatusDone) {
		t.Fatalf("缺回执但有助手文本时应结算为 done，实际: %s", task.Status)
	}
	if task.LastActivityText == nil || *task.LastActivityText != "已补齐总管任务" {
		t.Fatalf("期望活动文本为最后一条助手文本，实际: %v", task.LastActivityText)
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil {
		t.Fatalf("解析任务元信息失败: %v", err)
	}
	if meta.ResultStatus != string(ResultDone) || meta.Summary != "已补齐总管任务" {
		t.Fatalf("期望结果摘要为助手文本，实际 status=%s summary=%q", meta.ResultStatus, meta.Summary)
	}
	if calls != 1 {
		t.Fatalf("完成时应通知一次结果，实际: %d", calls)
	}
	goal, err := db.GetGoal(ctx, "g-steward-text")
	if err != nil || goal.Status != string(GoalStatusRunning) {
		t.Fatalf("常驻总管目标应保持 running，实际: %+v 错误=%v", goal, err)
	}
}

// TestStewardOwnerMissingReceiptRetriesThenFailsOnce 验证总管 owner 任务缺回执且没有助手文本时，
// 失败预算内先回到 todo 且不通知，超过 maxAttempts 后 failed 且只通知一次。
func TestStewardOwnerMissingReceiptRetriesThenFailsOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, db, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)

	thread := "steward-owner-empty"
	seedSchedulerGoal(t, ctx, db, store.Goal{ID: "g-steward-empty", ProjectID: "p", Title: "总管", Owner: goalOwnerSteward},
		store.Task{ID: "t-steward-empty", Title: "任务", Status: string(TaskStatusInProgress), ThreadID: &thread, Meta: EncodeTaskMeta(TaskMeta{Origin: "owner"})})
	driver.setThreadState(thread, SessionState{Running: false, HostToolsUnavailable: true})
	calls := 0
	svc.SetOnResult(func(context.Context, store.Task, ExecutorResult) { calls++ })

	scheduler.Tick(ctx)
	task, err := db.GetTask(ctx, "t-steward-empty")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if task.Status != string(TaskStatusTodo) {
		t.Fatalf("失败预算内应先回到 todo，实际: %s", task.Status)
	}
	if calls != 0 {
		t.Fatalf("回到 todo 时不应通知结果，实际: %d", calls)
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil {
		t.Fatalf("解析任务元信息失败: %v", err)
	}
	if meta.ResultStatus != string(ResultFailed) || meta.Summary != "执行会话未提交结果" {
		t.Fatalf("期望记录缺回执失败，实际 status=%s summary=%q", meta.ResultStatus, meta.Summary)
	}

	task.Status, task.AttemptCount = string(TaskStatusInProgress), 2
	if _, err := db.UpdateTask(ctx, task, task.Revision); err != nil {
		t.Fatalf("更新任务失败: %v", err)
	}
	scheduler.Tick(ctx)
	task, err = db.GetTask(ctx, "t-steward-empty")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if task.Status != string(TaskStatusFailed) {
		t.Fatalf("超过失败预算后应 failed，实际: %s", task.Status)
	}
	if calls != 1 {
		t.Fatalf("最终失败应通知一次结果，实际: %d", calls)
	}

	scheduler.Tick(ctx)
	if calls != 1 {
		t.Fatalf("已失败的任务不应重复通知，实际: %d", calls)
	}
}

// TestReceiptMissingAsksThenEscalates 验证缺回执时先追问两次，宽限过后升级到 needs_approval 并停住。
func TestReceiptMissingAsksThenEscalates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	currTime := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	_, s, _, scheduler := setupReceiptTestService(t, driver, true, false, func() time.Time { return currTime })

	threadID := "thread-receipt-ask"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-receipt-ask", ProjectID: "proj-1", Title: "缺回执追问"},
		store.Task{ID: "task-receipt-ask", Title: "缺回执任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false, LastAssistantText: "做了一半"})

	scheduler.Tick(ctx)
	asks := driver.sentFollowUps()
	if len(asks) != 1 {
		t.Fatalf("第一轮应追问一次，实际: %d", len(asks))
	}
	if asks[0].text != taskReceiptPrompt("task-receipt-ask") || !strings.Contains(asks[0].text, "任务 ID：`task-receipt-ask`。") {
		t.Fatalf("追问文案应包含任务 ID，实际: %s", asks[0].text)
	}
	task, _ := s.GetTask(ctx, "task-receipt-ask")
	if task.Status != string(TaskStatusInProgress) {
		t.Fatalf("追问期间应保持 in_progress，实际: %s", task.Status)
	}

	currTime = currTime.Add(30 * time.Second)
	scheduler.Tick(ctx)
	if got := len(driver.sentFollowUps()); got != 1 {
		t.Fatalf("宽限期内不应重复追问，实际: %d", got)
	}

	currTime = currTime.Add(31 * time.Second)
	scheduler.Tick(ctx)
	if got := len(driver.sentFollowUps()); got != 2 {
		t.Fatalf("宽限过后应再追问一次，实际: %d", got)
	}

	currTime = currTime.Add(10 * time.Second)
	scheduler.Tick(ctx)
	task, _ = s.GetTask(ctx, "task-receipt-ask")
	if task.Status != string(TaskStatusInProgress) {
		t.Fatalf("第二次追问的宽限期内应保持 in_progress，实际: %s", task.Status)
	}

	currTime = currTime.Add(51 * time.Second)
	scheduler.Tick(ctx)
	task, _ = s.GetTask(ctx, "task-receipt-ask")
	if task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("追问满额后应升级为 needs_approval，实际: %s", task.Status)
	}
	if task.LastActivityText == nil || *task.LastActivityText != "执行会话两次追问后仍未提交结果" {
		t.Fatalf("期望升级文案，实际: %v", task.LastActivityText)
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil || meta.DecisionID == "" {
		t.Fatalf("追问满额升级后应创建决策卡并写入 DecisionID，实际 meta=%+v err=%v", meta, err)
	}

	// 升级后继续 Tick 不能把任务推进 review，也不能再追问
	currTime = currTime.Add(10 * time.Minute)
	scheduler.Tick(ctx)
	task, _ = s.GetTask(ctx, "task-receipt-ask")
	if task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("升级后应停在 needs_approval，实际: %s", task.Status)
	}
	if got := len(driver.sentFollowUps()); got != 2 {
		t.Fatalf("升级后不应再追问，实际: %d", got)
	}
}

// TestReceiptWatchdogBlocked 验证 blocked 回执让 watchdog 把任务迁到 needs_approval 并写入求助文案。
func TestReceiptWatchdogBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, s, wd, _ := setupReceiptTestService(t, driver, true, true, nil)

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "求助任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if err := svc.SubmitTaskResult(ctx, task.ID, *task.ThreadID, ReceiptOutcomeBlocked, "依赖的服务没有测试账号，需要决定是否改用 mock"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}
	driver.setThreadState(*task.ThreadID, SessionState{Running: false})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}

	latest, _ := s.GetTask(ctx, task.ID)
	if latest.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("blocked 回执应迁到 needs_approval，实际: %s", latest.Status)
	}
	want := receiptBlockedPrefix + "依赖的服务没有测试账号，需要决定是否改用 mock"
	if latest.LastActivityText == nil || *latest.LastActivityText != want {
		t.Fatalf("期望求助文案 %q，实际: %v", want, latest.LastActivityText)
	}
	if !svc.isReceiptBlocked(task.ID) {
		t.Fatal("blocked 回执应记录求助标记")
	}
}

// TestReceiptSchedulerBlockedAndResume 验证调度器遇到求助任务保持不动，会话恢复后清除标记并回到 in_progress。
func TestReceiptSchedulerBlockedAndResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, s, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)

	threadID := "thread-blocked"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-blocked", ProjectID: "proj-1", Title: "求助"},
		store.Task{ID: "task-blocked", Title: "求助任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := svc.SubmitTaskResult(ctx, "task-blocked", threadID, ReceiptOutcomeBlocked, "缺少接口文档，需要负责人提供"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}

	scheduler.Tick(ctx)
	task, _ := s.GetTask(ctx, "task-blocked")
	if task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("blocked 回执应迁到 needs_approval，实际: %s", task.Status)
	}
	if task.LastActivityText == nil || !strings.HasPrefix(*task.LastActivityText, receiptBlockedPrefix) {
		t.Fatalf("期望求助文案，实际: %v", task.LastActivityText)
	}

	// 会话恢复运行但决策尚未回答时，仍停在 needs_approval。
	driver.setThreadState(threadID, SessionState{Running: true})
	scheduler.Tick(ctx)
	task, _ = s.GetTask(ctx, "task-blocked")
	if task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("未回答的决策应保持 needs_approval，实际: %s", task.Status)
	}
	if !svc.isReceiptBlocked(task.ID) {
		t.Fatal("未回答的决策仍应保留求助标记")
	}
}

// TestReceiptDoneClearsBlocked 验证求助后会话在两次 Tick 之间跑完并提交 done 时，
// 回执会解除求助标记，调度器直接按回执送审，而不是继续停在 needs_approval。
func TestReceiptDoneClearsBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, s, _, scheduler := setupReceiptTestService(t, driver, true, false, nil)

	threadID := "thread-blocked-done"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-blocked-done", ProjectID: "proj-1", Title: "求助后完成"},
		store.Task{ID: "task-blocked-done", Title: "求助后完成任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := svc.SubmitTaskResult(ctx, "task-blocked-done", threadID, ReceiptOutcomeBlocked, "缺少字段定义"); err != nil {
		t.Fatalf("提交求助回执失败: %v", err)
	}
	scheduler.Tick(ctx)
	task, _ := s.GetTask(ctx, "task-blocked-done")
	if task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("求助回执应迁到 needs_approval，实际: %s", task.Status)
	}

	// 用户回复后会话恢复并跑完，提交 done 后在下一次 Tick 之前停下
	driver.setThreadState(threadID, SessionState{Running: true})
	if err := svc.SubmitTaskResult(ctx, "task-blocked-done", threadID, ReceiptOutcomeDone, "补齐了字段定义并自查通过"); err != nil {
		t.Fatalf("提交完成回执失败: %v", err)
	}
	driver.setThreadState(threadID, SessionState{Running: false})

	scheduler.Tick(ctx)
	task, _ = s.GetTask(ctx, "task-blocked-done")
	if task.Status != string(TaskStatusReview) {
		t.Fatalf("完成后应送审，实际: %s", task.Status)
	}
	if task.LastActivityText == nil || *task.LastActivityText != "补齐了字段定义并自查通过" {
		t.Fatalf("期望活动文本为新回执摘要，实际: %v", task.LastActivityText)
	}
	if svc.isReceiptBlocked("task-blocked-done") {
		t.Fatal("提交新回执后应解除求助标记")
	}
}

// TestReceiptStandaloneNeedsApprovalDone 验证没有目标调度的独立任务求助后补交 done 回执也能自动送审。
func TestReceiptStandaloneNeedsApprovalDone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, s, wd, _ := setupReceiptTestService(t, driver, true, true, nil)

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "独立任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	threadID := *task.ThreadID
	if err := svc.SubmitTaskResult(ctx, task.ID, threadID, ReceiptOutcomeBlocked, "缺少上游字段"); err != nil {
		t.Fatalf("提交求助回执失败: %v", err)
	}
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}
	if latest, _ := s.GetTask(ctx, task.ID); latest.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("求助回执应迁到 needs_approval，实际: %s", latest.Status)
	}

	// 用户回复后会话恢复并跑完，在卡片仍停在 needs_approval 时补交 done 回执
	driver.setThreadState(threadID, SessionState{Running: true})
	if err := svc.SubmitTaskResult(ctx, task.ID, threadID, ReceiptOutcomeDone, "补齐上游字段并自查通过"); err != nil {
		t.Fatalf("提交完成回执失败: %v", err)
	}
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}

	latest, _ := s.GetTask(ctx, task.ID)
	if latest.Status != string(TaskStatusReview) {
		t.Fatalf("补交 done 回执后应送审，实际: %s", latest.Status)
	}
	if latest.LastActivityText == nil || *latest.LastActivityText != "补齐上游字段并自查通过" {
		t.Fatalf("期望活动文本为填写后的回执摘要，实际: %v", latest.LastActivityText)
	}
}

// TestReceiptDoneWithAutoAcceptHandoff 验证自动接受目标下 watchdog 先消费回执、调度器再按同一摘要结算，
// 调度器不会把已完成的任务误判成缺回执而追问。
func TestReceiptDoneWithAutoAcceptHandoff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, s, wd, scheduler := setupReceiptTestService(t, driver, false, false, nil)

	threadID := "thread-auto-accept"
	seedSchedulerGoal(t, ctx, s, store.Goal{
		ID:        "goal-auto-accept",
		ProjectID: "proj-1",
		Title:     "自动接受",
		Policy:    map[string]any{"autoAccept": true},
	}, store.Task{ID: "task-auto-accept", Title: "自动接受任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := svc.SubmitTaskResult(ctx, "task-auto-accept", threadID, ReceiptOutcomeDone, "改完并自查通过"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}
	task, _ := s.GetTask(ctx, "task-auto-accept")
	if task.Status != string(TaskStatusInProgress) {
		t.Fatalf("自动接受目标下 watchdog 应保持 in_progress，实际: %s", task.Status)
	}

	scheduler.Tick(ctx)
	task, _ = s.GetTask(ctx, "task-auto-accept")
	if task.Status != string(TaskStatusDone) {
		t.Fatalf("调度器应按自动接受策略结算为 done，实际: %s", task.Status)
	}
	if task.ReviewStatus == nil || *task.ReviewStatus != "passed" {
		t.Fatalf("自动接受应写 ReviewStatus=passed，实际: %v", task.ReviewStatus)
	}
	if task.LastActivityText == nil || *task.LastActivityText != "改完并自查通过" {
		t.Fatalf("期望活动文本为回执摘要，实际: %v", task.LastActivityText)
	}
	if got := len(driver.sentFollowUps()); got != 0 {
		t.Fatalf("有回执时不应追问，实际: %d", got)
	}
}

// TestReceiptFallbackWithoutFollowUp 验证驱动不支持追问时保持旧行为：直接用最后一条助手文本完成任务。
func TestReceiptFallbackWithoutFollowUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newMockDriver()
	svc, s, wd, _ := setupReceiptTestService(t, driver, true, true, nil)

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "旧行为任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	driver.setThreadState(*task.ThreadID, SessionState{Running: false, LastAssistantText: "旧行为完成"})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}

	latest, _ := s.GetTask(ctx, task.ID)
	if latest.Status != string(TaskStatusReview) {
		t.Fatalf("不支持追问时应按旧行为进入 review，实际: %s", latest.Status)
	}
	if latest.LastActivityText == nil || *latest.LastActivityText != "旧行为完成" {
		t.Fatalf("期望活动文本为最后一条助手文本，实际: %v", latest.LastActivityText)
	}
}

// TestReceiptFallbackWhenHostToolsUnavailable 会话调不到回执工具（HostToolsUnavailable）时，
// 即使驱动支持追问也按旧行为收尾：不追问，直接用最后一条助手文本完成任务并进入 review。
func TestReceiptFallbackWhenHostToolsUnavailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newPokeDriver()
	svc, s, wd, _ := setupReceiptTestService(t, driver, true, true, nil)

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "无宿主工具任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	driver.setThreadState(*task.ThreadID, SessionState{
		Running:              false,
		LastAssistantText:    "没有回执也算完成",
		HostToolsUnavailable: true,
	})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}

	latest, _ := s.GetTask(ctx, task.ID)
	if latest.Status != string(TaskStatusReview) {
		t.Fatalf("宿主工具不可用时应按旧行为进入 review，实际: %s", latest.Status)
	}
	if latest.LastActivityText == nil || *latest.LastActivityText != "没有回执也算完成" {
		t.Fatalf("期望活动文本为最后一条助手文本，实际: %v", latest.LastActivityText)
	}
	if got := len(driver.sentFollowUps()); got != 0 {
		t.Fatalf("宿主工具不可用时不应追问，实际追问了 %d 次", got)
	}
}

// TestSubmitTaskResultValidation 验证回执校验：线程不匹配、非法 outcome、空摘要与非法状态都被拒绝。
func TestSubmitTaskResultValidation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	driver := newMockDriver()
	svc, s, _, _ := setupReceiptTestService(t, driver, true, true, nil)

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "校验任务"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	if task.ThreadID == nil {
		t.Fatal("自动启动后应写入 ThreadID")
	}
	validThread := *task.ThreadID

	cases := []struct {
		name    string
		taskID  string
		thread  string
		outcome string
		summary string
	}{
		{name: "线程不匹配", taskID: task.ID, thread: "thread-other", outcome: ReceiptOutcomeDone, summary: "完成"},
		{name: "线程为空", taskID: task.ID, thread: "", outcome: ReceiptOutcomeDone, summary: "完成"},
		{name: "非法 outcome", taskID: task.ID, thread: validThread, outcome: "maybe", summary: "完成"},
		{name: "空摘要", taskID: task.ID, thread: validThread, outcome: ReceiptOutcomeDone, summary: "   "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.SubmitTaskResult(ctx, tc.taskID, tc.thread, tc.outcome, tc.summary); err == nil {
				t.Fatal("期望返回错误，实际成功")
			}
		})
	}

	// 不在执行中的任务不能提交回执
	todo, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "待办任务"})
	if err != nil {
		t.Fatalf("创建待办任务失败: %v", err)
	}
	if _, err := svc.MoveTask(ctx, todo.ID, TaskStatusTodo, todo.Revision); err != nil {
		t.Fatalf("移动任务失败: %v", err)
	}
	if err := svc.SubmitTaskResult(ctx, todo.ID, validThread, ReceiptOutcomeDone, "完成"); err == nil {
		t.Fatal("未执行的任务不应接受回执")
	}

	// 合法回执会去掉首尾空白并截断到 500 个 rune
	longSummary := strings.Repeat("好", 600)
	if err := svc.SubmitTaskResult(ctx, task.ID, validThread, ReceiptOutcomeDone, "  "+longSummary+"  "); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}
	stored := &svc.receipts
	stored.mu.Lock()
	receipt := stored.receipts[task.ID]
	stored.mu.Unlock()
	if len([]rune(receipt.Summary)) != receiptSummaryLimit {
		t.Fatalf("期望摘要截断到 %d 个 rune，实际: %d", receiptSummaryLimit, len([]rune(receipt.Summary)))
	}
	if receipt.Outcome != ReceiptOutcomeDone || receipt.ThreadID != validThread {
		t.Fatalf("回执内容不正确: %+v", receipt)
	}
	if latest, _ := s.GetTask(ctx, task.ID); latest.Status != string(TaskStatusInProgress) {
		t.Fatalf("提交回执本身不应改变任务状态，实际: %s", latest.Status)
	}
}

// TestReceiptConsumedOnce 验证同一条回执只会被消费一次：消费后既不重复追问，也不会被再次结算。
func TestReceiptConsumedOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	currTime := time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, _, scheduler := setupReceiptTestService(t, driver, true, false, func() time.Time { return currTime })

	threadID := "thread-once"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-once", ProjectID: "proj-1", Title: "只消费一次"},
		store.Task{ID: "task-once", Title: "只消费一次任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := svc.SubmitTaskResult(ctx, "task-once", threadID, ReceiptOutcomeBlocked, "等数据库迁移脚本"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}

	scheduler.Tick(ctx)
	first, _ := s.GetTask(ctx, "task-once")
	if first.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("期望迁到 needs_approval，实际: %s", first.Status)
	}
	// 回执已被取走
	svc.receipts.mu.Lock()
	_, stillThere := svc.receipts.receipts["task-once"]
	svc.receipts.mu.Unlock()
	if stillThere {
		t.Fatal("回执消费后不应残留")
	}

	// 超过追问宽限后再 Tick，不应升级、不应追问，活动文本也不变
	currTime = currTime.Add(10 * time.Minute)
	scheduler.Tick(ctx)
	second, _ := s.GetTask(ctx, "task-once")
	if second.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("二次 Tick 不应改变状态，实际: %s", second.Status)
	}
	if second.LastActivityText == nil || first.LastActivityText == nil || *second.LastActivityText != *first.LastActivityText {
		t.Fatalf("二次 Tick 不应改写活动文本，实际: %v", second.LastActivityText)
	}
	if got := len(driver.sentFollowUps()); got != 0 {
		t.Fatalf("已有回执时不应追问，实际: %d", got)
	}
}

// TestStartPromptMentionsSubmitResult 验证执行会话的启动提示词要求调用 kanban_submit_result。
func TestStartPromptMentionsSubmitResult(t *testing.T) {
	t.Parallel()
	prompt := buildStartPrompt(store.Task{ID: "task-prompt", Title: "提示词任务"}, nil, "kanban/task-prompt", "", false, false)
	if !strings.Contains(prompt, "kanban_submit_result") {
		t.Fatalf("启动提示词应包含 kanban_submit_result，实际: %s", prompt)
	}
	if !strings.Contains(prompt, "用 kanban_update_card 的 progress 参数写一句不超过 80 字的进展") {
		t.Fatalf("启动提示词应包含进展报告要求，实际: %s", prompt)
	}
}

// TestDecideFinishedInterruptedSendsResumePrompt 验证重启中断会话无回执时发送续跑文案，不把任务标为 done 或 review。
func TestDecideFinishedInterruptedSendsResumePrompt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	_, s, wd, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

	threadID := "thread-interrupted"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-interrupted", ProjectID: "proj-1", Title: "中断续跑"},
		store.Task{ID: "task-interrupted", Title: "中断任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{
		Running:            false,
		Interrupted:        true,
		RestartInterrupted: true,
		LastAssistantText:  "做了一半，还没收尾",
	})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}

	asks := driver.sentFollowUps()
	if len(asks) != 1 {
		t.Fatalf("期望发送 1 次续跑追问，实际: %d", len(asks))
	}
	if asks[0].text != taskResumePrompt("task-interrupted") {
		t.Fatalf("期望发送续跑文案 %q，实际: %q", taskResumePrompt("task-interrupted"), asks[0].text)
	}
	latest, err := s.GetTask(ctx, "task-interrupted")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if latest.Status != string(TaskStatusInProgress) {
		t.Fatalf("中断续跑追问期间任务不得标为 done/review，实际: %s", latest.Status)
	}
}

// TestDecideFinishedInterruptedHostToolsUnavailableNotDone 验证 HostToolsUnavailable 且 RestartInterrupted 时，
// 总管 owner 任务返回 ResultFailed（不把半截助手文本当成完成），非 owner 任务返回 ResultNeedsDecision。
func TestDecideFinishedInterruptedHostToolsUnavailableNotDone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, _, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

	threadOwner := "thread-owner-int"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-owner-int", ProjectID: "proj-1", Title: "总管", Owner: goalOwnerSteward},
		store.Task{
			ID:       "task-owner-int",
			Title:    "总管任务",
			Status:   string(TaskStatusInProgress),
			ThreadID: &threadOwner,
			Meta:     EncodeTaskMeta(TaskMeta{Origin: "owner"}),
		})
	ownerTask, err := s.GetTask(ctx, "task-owner-int")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	st := SessionState{
		Running:              false,
		Interrupted:          true,
		RestartInterrupted:   true,
		HostToolsUnavailable: true,
		LastAssistantText:    "半截回复",
	}
	decision := svc.decideFinished(ctx, ownerTask, now, st)
	if decision.kind != finishDone || decision.result.Status != ResultFailed || decision.result.Summary != "执行会话被中断" {
		t.Fatalf("总管 owner 任务中断且无法追问时应返回 ResultFailed，实际: %+v", decision)
	}

	threadNormal := "thread-normal-int"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-normal-int", ProjectID: "proj-1", Title: "普通目标"},
		store.Task{ID: "task-normal-int", Title: "普通任务", Status: string(TaskStatusInProgress), ThreadID: &threadNormal})
	normalTask, err := s.GetTask(ctx, "task-normal-int")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	normalDecision := svc.decideFinished(ctx, normalTask, now, st)
	if normalDecision.kind != finishDone || normalDecision.result.Status != ResultNeedsDecision {
		t.Fatalf("非总管任务中断且无法追问时应建决策卡，实际: %+v", normalDecision)
	}
}

// TestReceiptEscalateCreatesDecision 验证两次追问仍无回执时任务转为 needs_approval，生成 open 决策卡并释放并发槽位。
func TestReceiptEscalateCreatesDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	currTime := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	_, s, _, scheduler := setupReceiptTestService(t, driver, true, false, func() time.Time { return currTime })

	threadID := "thread-escalate-card"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-escalate-card", ProjectID: "proj-1", Title: "追问建卡"},
		store.Task{ID: "task-escalate-card", Title: "无回执任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false})

	scheduler.Tick(ctx)
	currTime = currTime.Add(receiptAskGrace + time.Second)
	scheduler.Tick(ctx)
	currTime = currTime.Add(receiptAskGrace + time.Second)
	scheduler.Tick(ctx)

	task, err := s.GetTask(ctx, "task-escalate-card")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if task.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("追问达上限后应为 needs_approval，实际: %s", task.Status)
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil || meta.DecisionID == "" {
		t.Fatalf("追问达上限后应设置 DecisionID，实际 meta=%+v err=%v", meta, err)
	}
	decision, err := s.GetStewardDecision(ctx, meta.DecisionID)
	if err != nil {
		t.Fatalf("获取决策卡失败: %v", err)
	}
	if decision.Status != store.DecisionStatusOpen || decision.TaskID != task.ID {
		t.Fatalf("期望存在 open 决策卡，实际: %+v", decision)
	}
	if got := occupiedSlots([]store.Task{task}, nil); got != 0 {
		t.Fatalf("已建决策卡的 needs_approval 不应占用槽位，实际: %d", got)
	}
}

// TestConcurrentWatchdogAndSchedulerFinishOnce 验证 Watchdog 与 GoalScheduler 并发收尾同一任务时串行执行，
// 只消费一次回执、不会重复建卡或误发追问。
func TestConcurrentWatchdogAndSchedulerFinishOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	currTime := time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, wd, scheduler := setupReceiptTestService(t, driver, true, false, func() time.Time { return currTime })

	threadID := "thread-concurrent-finish"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-concurrent-finish", ProjectID: "proj-1", Title: "并发收尾"},
		store.Task{ID: "task-concurrent-finish", Title: "并发收尾任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{Running: false})
	if err := svc.SubmitTaskResult(ctx, "task-concurrent-finish", threadID, ReceiptOutcomeDone, "并发收尾完成"); err != nil {
		t.Fatalf("提交回执失败: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		_ = wd.Check(ctx)
	})
	wg.Go(func() {
		<-start
		scheduler.Tick(ctx)
	})
	close(start)
	wg.Wait()

	task, err := s.GetTask(ctx, "task-concurrent-finish")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if task.Status != string(TaskStatusReview) {
		t.Fatalf("并发收尾后应进入 review，实际: %s", task.Status)
	}
	if asks := driver.sentFollowUps(); len(asks) != 0 {
		t.Fatalf("并发收尾有回执时不应误发追问，实际: %d", len(asks))
	}
}

// TestDecideFinishedUserInterruptedCreatesDecision 验证用户手动中断时不追问、不自动续跑、
// 不把任务当完成；普通任务与总管 owner 任务均转 needs_approval 并建决策卡，且重复巡视不重复建卡。
func TestDecideFinishedUserInterruptedCreatesDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, wd, scheduler := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

	normalThread := "thread-user-int-normal"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-user-int-normal", ProjectID: "proj-1", Title: "手动中断"},
		store.Task{ID: "task-user-int-normal", Title: "普通任务", Status: string(TaskStatusInProgress), ThreadID: &normalThread})
	driver.setThreadState(normalThread, SessionState{
		Running:            false,
		Interrupted:        true,
		RestartInterrupted: false,
	})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}
	if asks := driver.sentFollowUps(); len(asks) != 0 {
		t.Fatalf("手动中断不应发送 SendFollowUp，实际: %+v", asks)
	}

	normalTask, err := s.GetTask(ctx, "task-user-int-normal")
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if normalTask.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("期望手动中断后任务转为 needs_approval，实际: %s", normalTask.Status)
	}
	normalMeta, err := ParseTaskMeta(normalTask.Meta)
	if err != nil || normalMeta.DecisionID == "" {
		t.Fatalf("期望生成决策卡并写入 DecisionID，实际 meta=%+v err=%v", normalMeta, err)
	}
	d, err := s.GetStewardDecision(ctx, normalMeta.DecisionID)
	if err != nil {
		t.Fatalf("获取决策卡失败: %v", err)
	}
	if d.Status != store.DecisionStatusOpen || d.Question != "执行会话已被手动中断，怎么处理" {
		t.Fatalf("决策卡内容不符合预期: %+v", d)
	}
	var opts []DecisionOption
	if err := json.Unmarshal([]byte(d.OptionsJSON), &opts); err != nil {
		t.Fatalf("解析决策选项失败: %v", err)
	}
	if len(opts) != 2 || opts[0].Key != "A" || opts[0].Text != "继续执行" || opts[1].Key != "B" || opts[1].Text != "停下" {
		t.Fatalf("期望选项 A 为「继续执行」、B 为「停下」，实际: %+v", opts)
	}

	// 再次巡视与调度不应重复建卡
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("二次 watchdog check 失败: %v", err)
	}
	scheduler.Tick(ctx)
	normalAgain, _ := s.GetTask(ctx, "task-user-int-normal")
	metaAgain, _ := ParseTaskMeta(normalAgain.Meta)
	if metaAgain.DecisionID != normalMeta.DecisionID {
		t.Fatalf("不应重复建卡，期望 DecisionID=%q，实际=%q", normalMeta.DecisionID, metaAgain.DecisionID)
	}

	// 总管 owner 任务手动中断同样建卡而不是 ResultFailed
	ownerThread := "thread-user-int-owner"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-user-int-owner", ProjectID: "proj-1", Title: "总管", Owner: goalOwnerSteward},
		store.Task{
			ID:       "task-user-int-owner",
			Title:    "总管任务",
			Status:   string(TaskStatusInProgress),
			ThreadID: &ownerThread,
			Meta:     EncodeTaskMeta(TaskMeta{Origin: "owner"}),
		})
	driver.setThreadState(ownerThread, SessionState{
		Running:            false,
		Interrupted:        true,
		RestartInterrupted: false,
	})
	ownerTask, err := s.GetTask(ctx, "task-user-int-owner")
	if err != nil {
		t.Fatalf("获取总管任务失败: %v", err)
	}
	ownerDecision := svc.decideFinished(ctx, ownerTask, now, SessionState{Running: false, Interrupted: true})
	if ownerDecision.kind != finishDone || ownerDecision.result.Status != ResultNeedsDecision {
		t.Fatalf("总管 owner 任务手动中断应返回 ResultNeedsDecision，实际: %+v", ownerDecision)
	}
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("总管任务 watchdog check 失败: %v", err)
	}
	ownerLatest, _ := s.GetTask(ctx, "task-user-int-owner")
	if ownerLatest.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("总管 owner 任务手动中断应转为 needs_approval，实际: %s", ownerLatest.Status)
	}
	if asks := driver.sentFollowUps(); len(asks) != 0 {
		t.Fatalf("总管 owner 任务手动中断不应发送 SendFollowUp，实际: %+v", asks)
	}
}

// TestDecideFinishedRestartInterruptedResumes 验证 RestartInterrupted=true 时发送 taskResumePrompt 续跑。
func TestDecideFinishedRestartInterruptedResumes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 10, 30, 0, 0, time.UTC)
	driver := newPokeDriver()
	_, s, wd, _ := setupReceiptTestService(t, driver, true, false, func() time.Time { return now })

	threadID := "thread-restart-resume"
	seedSchedulerGoal(t, ctx, s, store.Goal{ID: "goal-restart-resume", ProjectID: "proj-1", Title: "重启续跑"},
		store.Task{ID: "task-restart-resume", Title: "重启任务", Status: string(TaskStatusInProgress), ThreadID: &threadID})
	driver.setThreadState(threadID, SessionState{
		Running:            false,
		Interrupted:        true,
		RestartInterrupted: true,
	})

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("watchdog check 失败: %v", err)
	}
	asks := driver.sentFollowUps()
	if len(asks) != 1 || asks[0].text != taskResumePrompt("task-restart-resume") {
		t.Fatalf("期望发送 1 次重启续跑文案，实际: %+v", asks)
	}
}

// TestWatchdogRescueInterruptedResumes 验证卡死救援后 meta.WatchdogInterrupted 为真、
// 会话转中断后下一轮发送续跑文案并清除标记，且续跑中用户再手动中断能被识别为手动中断。
func TestWatchdogRescueInterruptedResumes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	currTime := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	driver := newPokeDriver()
	svc, s, wd := setupPokeWatchdog(t, driver, true, 600*time.Second, 1800*time.Second, func() time.Time { return currTime })

	task, err := svc.CreateTask(ctx, CreateTaskInput{ProjectID: "proj-1", Title: "卡死救援续跑"})
	if err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}
	threadID := *task.ThreadID

	if err := wd.Check(ctx); err != nil {
		t.Fatalf("首次 check 失败: %v", err)
	}
	currTime = currTime.Add(650 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("催促 check 失败: %v", err)
	}
	currTime = currTime.Add(1250 * time.Second)
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("救援 check 失败: %v", err)
	}

	rescued, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	rescuedMeta, err := ParseTaskMeta(rescued.Meta)
	if err != nil || !rescuedMeta.WatchdogInterrupted {
		t.Fatalf("期望卡死救援后 meta.WatchdogInterrupted 为真，实际 meta=%+v err=%v", rescuedMeta, err)
	}

	driver.setThreadState(threadID, SessionState{Running: false, Interrupted: true})
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("续跑 check 失败: %v", err)
	}
	asks := driver.sentFollowUps()
	if len(asks) != 2 || asks[1].text != taskResumePrompt(task.ID) {
		t.Fatalf("期望发送续跑文案，实际: %+v", asks)
	}

	resumed, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	resumedMeta, err := ParseTaskMeta(resumed.Meta)
	if err != nil || resumedMeta.WatchdogInterrupted {
		t.Fatalf("期望发出续跑后清除 WatchdogInterrupted，实际 meta=%+v err=%v", resumedMeta, err)
	}

	// 续跑中用户再手动中断，应识别为用户中断并建决策卡，不再自动续跑
	driver.setThreadState(threadID, SessionState{Running: true})
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("运行中 check 失败: %v", err)
	}
	driver.setThreadState(threadID, SessionState{Running: false, Interrupted: true})
	if err := wd.Check(ctx); err != nil {
		t.Fatalf("手动中断 check 失败: %v", err)
	}
	if len(driver.sentFollowUps()) != 2 {
		t.Fatalf("续跑中用户手动中断不得再次自动续跑，实际追问数: %d", len(driver.sentFollowUps()))
	}
	stopped, err := s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("获取任务失败: %v", err)
	}
	if stopped.Status != string(TaskStatusNeedsApproval) {
		t.Fatalf("期望用户再次手动中断后转为 needs_approval，实际: %s", stopped.Status)
	}
}
