package kanban

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"mcode/internal/store"
)

// GoalScheduler 负责定期调度并推进看板中正在运行的目标任务。
type GoalScheduler struct {
	svc      *Service
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
	stateMu  sync.Mutex
	// lastStartErrors 记录每个任务上次启动失败的原因：同一任务同一错误只写一次库、打一条日志，
	// 避免调度轮询把同一行失败原因反复刷写；启动成功时清除。
	lastStartErrors map[string]string
}

// NewGoalScheduler 创建调度器实例，interval <= 0 时默认设为 10s。
func NewGoalScheduler(svc *Service, interval time.Duration) *GoalScheduler {
	if interval <= 0 {
		interval = 10 * time.Second
	}
	return &GoalScheduler{
		svc:             svc,
		interval:        interval,
		lastStartErrors: make(map[string]string),
	}
}

// 会话等待用户处理与人工评审时写入的说明文案。
const (
	awaitingUserText = "会话等待用户审批或输入，暂不推进"
	manualReviewText = "会话已结束，等待人工处理"
)

// sameProgress 比较两份进度快照是否一致。
// 进度经 JSON 落库后再读回时数字会变成 float64，直接深比较永远判不等，
// 因此统一比较两者的 JSON 序列化结果。
func sameProgress(a, b map[string]any) bool {
	ab, errA := json.Marshal(a)
	if errA != nil {
		return false
	}
	bb, errB := json.Marshal(b)
	if errB != nil {
		return false
	}
	return string(ab) == string(bb)
}

// Start 启动后台定时调度协程，多次调用无副作用。
func (g *GoalScheduler) Start() {
	g.stateMu.Lock()
	if g.running {
		g.stateMu.Unlock()
		return
	}
	g.running = true
	g.stopCh = make(chan struct{})
	g.stateMu.Unlock()

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		// 启动时先恢复执行会话已丢失的孤儿任务，使其能被正常调度
		_, _ = g.svc.RecoverOrphanTasks(context.Background())
		ticker := time.NewTicker(g.interval)
		defer ticker.Stop()
		for {
			select {
			case <-g.stopCh:
				return
			case <-ticker.C:
				g.Tick(context.Background())
			}
		}
	}()
}

// Stop 停止调度循环并等待其平稳退出，幂等。
func (g *GoalScheduler) Stop() {
	g.stateMu.Lock()
	if !g.running {
		g.stateMu.Unlock()
		return
	}
	g.running = false
	close(g.stopCh)
	g.stateMu.Unlock()
	g.wg.Wait()
}

// Tick 执行单次调度逻辑，内部使用互斥锁保证串行执行。
func (g *GoalScheduler) Tick(ctx context.Context) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.svc.CheckWaits(ctx, g.svc.nowTime()); err != nil {
		log.Printf("kanban: 检查等待失败: %v", err)
	}
	if err := g.svc.WakeParents(ctx); err != nil {
		log.Printf("kanban: 唤醒父任务失败: %v", err)
	}

	goals, err := g.svc.opts.Store.ListGoals(ctx, "")
	if err != nil {
		return
	}

	for _, goal := range goals {
		if goal.Status != string(GoalStatusRunning) {
			continue
		}
		if err := g.tickGoal(ctx, goal); err != nil {
			reason := err.Error()
			latest, gerr := g.svc.opts.Store.GetGoal(ctx, goal.ID)
			if gerr == nil {
				latest.StatusReason = &reason
				_, _ = g.svc.opts.Store.UpdateGoal(ctx, latest, latest.Revision)
			}
		}
	}
}

func getPolicyInt(policy map[string]any, key string, def int) int {
	if v, ok := policy[key]; ok {
		switch val := v.(type) {
		case int:
			return val
		case float64:
			return int(val)
		case int64:
			return int(val)
		}
	}
	return def
}

func getPolicyBool(policy map[string]any, key string, def bool) bool {
	if v, ok := policy[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

func (g *GoalScheduler) tickGoal(ctx context.Context, goal store.Goal) error {
	taskIDs, err := g.svc.opts.Store.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		return err
	}

	tasks := make([]store.Task, 0, len(taskIDs))
	for _, tid := range taskIDs {
		t, err := g.svc.opts.Store.GetTask(ctx, tid)
		if err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	maxConcurrency := getPolicyInt(goal.Policy, "maxConcurrency", 1)
	maxAttempts := getPolicyInt(goal.Policy, "maxAttempts", 2)
	autoAccept := getPolicyBool(goal.Policy, "autoAccept", false)

	goalChanged := false
	// 本次 Tick 因失败刚退回 todo 的任务，留一个 tick 的间隔再重启，
	// 避免同一次 Tick 里立刻重新启动并清掉刚记录的失败原因。
	failedThisTick := make(map[string]struct{})

	// (a) 对 in_progress / needs_approval 且有 ThreadID 的任务查 SessionState
	now := time.Now()
	if g.svc.opts.Now != nil {
		now = g.svc.opts.Now()
	}
	for i, t := range tasks {
		if t.Status == string(TaskStatusReview) && g.svc.isStewardOwnerTask(ctx, t) {
			meta, err := ParseTaskMeta(t.Meta)
			if err != nil || meta.DecisionID != "" || g.svc.isReviewActive(ctx, t) || (g.svc.opts.IsReviewActive != nil && g.svc.opts.IsReviewActive(t.ID)) {
				continue
			}
			if t.ReviewThreadID != nil && *t.ReviewThreadID != "" {
				if g.svc.opts.Driver == nil {
					continue
				}
				st, err := g.svc.opts.Driver.SessionState(ctx, *t.ReviewThreadID)
				if err != nil || st.Running || st.AwaitingApproval || st.AwaitingInput {
					continue
				}
			}
			if t.ThreadID != nil && *t.ThreadID != "" {
				if g.svc.opts.Driver == nil {
					continue
				}
				st, err := g.svc.opts.Driver.SessionState(ctx, *t.ThreadID)
				if err != nil || st.Running || st.AwaitingApproval || st.AwaitingInput {
					continue
				}
			}
			if meta.ResultStatus == string(ResultDone) {
				t.Status = string(TaskStatusDone)
			} else {
				t.Status = string(TaskStatusTodo)
				failedThisTick[t.ID] = struct{}{}
			}
			if up, err := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
				tasks[i] = up
				goalChanged = true
			}
			continue
		}
		if (t.Status != string(TaskStatusInProgress) && t.Status != string(TaskStatusNeedsApproval)) || t.ThreadID == nil || *t.ThreadID == "" || g.svc.opts.Driver == nil {
			continue
		}
		if t.Status == string(TaskStatusNeedsApproval) && g.svc.isStewardOwnerTask(ctx, t) {
			meta, err := ParseTaskMeta(t.Meta)
			if err == nil && meta.DecisionID != "" {
				d, err := g.svc.opts.Store.GetStewardDecision(ctx, meta.DecisionID)
				if err != nil || d.Status == store.DecisionStatusOpen {
					continue
				}
			}
		}
		st, err := g.svc.opts.Driver.SessionState(ctx, *t.ThreadID)
		if err != nil {
			continue
		}
		// 会话仍在等审批或等输入：任务停在 needs_approval、保留 ThreadID 并继续占着并发槽位，
		// 既不能当作已完成，也不能重置，等用户处理后再推进。
		if st.AwaitingApproval || st.AwaitingInput {
			if t.Status != string(TaskStatusNeedsApproval) {
				t.Status = string(TaskStatusNeedsApproval)
				waiting := awaitingUserText
				t.LastActivityText = &waiting
				if up, err := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					tasks[i] = up
					goalChanged = true
				}
			}
			continue
		}
		// 会话已恢复运行：把任务从 needs_approval 改回 in_progress
		if st.Running {
			if t.Status == string(TaskStatusNeedsApproval) {
				t.Status = string(TaskStatusInProgress)
				// 用户已经回复，执行会话重新跑起来，解除求助标记
				g.svc.clearReceiptBlocked(t.ID)
				if up, err := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					tasks[i] = up
					goalChanged = true
				}
			}
			continue
		}
		if st.Failed {
			if g.svc.isStewardOwnerTask(ctx, t) {
				log.Printf("kanban: 任务 %s 执行失败: %s", t.ID, st.FailureReason)
				res := ExecutorResult{Status: ResultFailed, Summary: "执行会话运行失败"}
				if err := g.svc.applyExecutorResult(ctx, &t, res, now); err != nil {
					return err
				}
				if up, err := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					tasks[i], goalChanged = up, true
					if up.Status == string(TaskStatusTodo) {
						failedThisTick[up.ID] = struct{}{}
					} else {
						g.svc.notifyResult(ctx, up, res)
					}
				}
				continue
			}
			t.LastFailureReason = &st.FailureReason
			if t.AttemptCount < maxAttempts {
				t.Status = string(TaskStatusTodo)
				if up, err := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					tasks[i] = up
					failedThisTick[up.ID] = struct{}{}
					goalChanged = true
				}
			} else {
				t.Status = string(TaskStatusRejected)
				if up, err := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					tasks[i] = up
					goalChanged = true
				}
				goal.Status = string(GoalStatusPaused)
				reason := fmt.Sprintf("task %s failed: %s", t.Title, st.FailureReason)
				goal.StatusReason = &reason
				if upGoal, err := g.svc.opts.Store.UpdateGoal(ctx, goal, goal.Revision); err == nil {
					goal = upGoal
					goalChanged = true
				}
			}
		} else {
			// 执行会话求助后停在这里等人处理，不能当作完成推进
			if t.Status == string(TaskStatusNeedsApproval) && g.svc.isReceiptBlocked(t.ID) {
				continue
			}
			// 会话已停止：有回执按回执收尾；没有回执时按驱动能力决定是否追问
			decision := g.svc.decideFinished(ctx, t, now, st.HostToolsUnavailable)
			if decision.kind == finishWait {
				continue
			}
			if err := g.svc.applyExecutorResult(ctx, &t, decision.result, now); err != nil {
				return err
			}
			text := decision.summary
			autoAcceptNow := false
			switch decision.kind {
			case finishBlocked:
				if CanTransition(TaskStatus(t.Status), TaskStatusNeedsApproval) {
					t.Status = string(TaskStatusNeedsApproval)
				}
				text = receiptBlockedPrefix + decision.summary
				g.svc.markReceiptBlocked(t.ID)
			case finishEscalate:
				if CanTransition(TaskStatus(t.Status), TaskStatusNeedsApproval) {
					t.Status = string(TaskStatusNeedsApproval)
				}
				text = receiptEscalatedText
				g.svc.markReceiptBlocked(t.ID)
			default:
				if decision.result.Status != "" && decision.result.Status != ResultDone {
					break
				}
				if g.svc.isStewardOwnerTask(ctx, t) {
					t.Status = string(TaskStatusDone)
					break
				}
				// 完成：按目标策略自动接受、送审，两个开关都关闭时交给人工处理。
				// 含 [命令] 或 [你验] 的验收标准必须先走验收，自动接受不生效。
				autoAcceptNow = autoAccept && !hasGatedAcceptance(t)
				if autoAcceptNow {
					// 先落到 review 写库，再走与人工接受完全相同的评审接受路径触发合并
					t.Status = string(TaskStatusReview)
				} else if g.svc.AutoReviewEnabled() {
					t.Status = string(TaskStatusReview)
				} else {
					// 自动接受与自动评审都关闭：迁到 review 等人工处理，不能把任务留在 in_progress
					t.Status = string(TaskStatusReview)
					// 有回执时卡片直接显示执行会话的结论，旧行为仍是原有文案
					if !decision.fromReceipt {
						text = manualReviewText
					}
				}
			}
			t.LastActivityText = &text
			if up, err := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
				if up.Status == string(TaskStatusTodo) && decision.result.Status == ResultFailed {
					failedThisTick[up.ID] = struct{}{}
				} else {
					g.svc.notifyResult(ctx, up, decision.result)
				}
				tasks[i] = up
				goalChanged = true
				if autoAcceptNow {
					// ReviewTask 失败（典型是合并失败）时任务留在 review，原因已写入卡片。
					accepted, reviewErr := g.svc.ReviewTask(ctx, up.ID, ReviewAccept, "", up.Revision)
					if reviewErr != nil {
						log.Printf("kanban: 自动接受任务 %s 失败: %v", up.ID, reviewErr)
					} else {
						tasks[i] = accepted
					}
				}
			}
		}
	}

	// 如果 goal 在 (a) 步骤变为 paused，不再启动新任务
	if goal.Status == string(GoalStatusRunning) {
		tasksByID := make(map[string]store.Task, len(tasks))
		for _, t := range tasks {
			tasksByID[t.ID] = t
		}

		reviewActiveFn := g.svc.opts.IsReviewActive
		if reviewActiveFn == nil {
			reviewActiveFn = func(taskID string) bool {
				return g.svc.isReviewActive(ctx, tasksByID[taskID])
			}
		}

		// (b) 只统计执行中的任务，按顺序启动依赖已完成的待办任务。
		inProgressCount := occupiedSlots(tasks, reviewActiveFn)

		if inProgressCount < maxConcurrency {
			freeSlots := maxConcurrency - inProgressCount
			for i, t := range tasks {
				if freeSlots <= 0 {
					break
				}
				if t.Status == string(TaskStatusTodo) {
					if _, justFailed := failedThisTick[t.ID]; justFailed {
						continue
					}
					// 依赖校验与服务端手动启动同口径（支持跨目标依赖），未就绪的任务留待后续 Tick
					if _, err := g.svc.ensureDependenciesDone(ctx, t); err != nil {
						continue
					}
					g.svc.mu.Lock()
					started, err := g.svc.startTaskInternal(ctx, t.ID, t.Revision)
					g.svc.mu.Unlock()
					if err == nil {
						tasks[i] = started
						tasksByID[t.ID] = started
						freeSlots--
						goalChanged = true
						delete(g.lastStartErrors, t.ID)
						continue
					}
					reason := fmt.Sprintf("启动任务失败：%v", err)
					// 同一任务同一错误只记录一次，避免每轮调度重复写库与刷日志
					if g.lastStartErrors[t.ID] == reason {
						continue
					}
					g.lastStartErrors[t.ID] = reason
					log.Printf("kanban: 目标 %s 调度启动任务 %s 失败: %v", goal.ID, t.ID, err)
					t.LastFailureReason = &reason
					if up, uerr := g.svc.opts.Store.UpdateTask(ctx, t, t.Revision); uerr == nil {
						tasks[i] = up
						tasksByID[t.ID] = up
						goalChanged = true
					}
				}
			}
		}
	}

	// (c) 所有任务都是 done 或 skipped 时 goal 置 completed，ResultSummary 写“完成 N 个任务”
	if len(tasks) > 0 {
		allCompleted := true
		for _, t := range tasks {
			if !isDone(t) {
				allCompleted = false
				break
			}
		}
		if allCompleted && goal.Status == string(GoalStatusRunning) && !isStewardQueue(goal) {
			goal.Status = string(GoalStatusCompleted)
			summary := fmt.Sprintf("完成 %d 个任务", len(tasks))
			goal.ResultSummary = &summary
			if upGoal, err := g.svc.opts.Store.UpdateGoal(ctx, goal, goal.Revision); err == nil {
				goal = upGoal
				goalChanged = true
			}
		}
	}

	// (d) 更新 Progress。进度与已落库快照一致时跳过落库，避免每轮 Tick 都改写目标版本号；
	// 有变更就触发 onChange
	prevStalled, _ := goal.Progress["stalled"].(bool)
	nextProgress := g.svc.goalProgress(ctx, tasks)
	if !sameProgress(goal.Progress, nextProgress) {
		goal.Progress = nextProgress
		if upGoal, err := g.svc.opts.Store.UpdateGoal(ctx, goal, goal.Revision); err == nil {
			goal = upGoal
		}
	}
	stalled, _ := goal.Progress["stalled"].(bool)
	goalChanged = goalChanged || prevStalled != stalled

	if goalChanged {
		g.svc.mu.Lock()
		g.svc.revision.Add(1)
		g.svc.mu.Unlock()
		g.svc.notifyChange()
	}

	return nil
}

// occupiedSlots 统计当前占用并发槽位的任务数。
// 执行中、需要审批及活跃评审占用槽位；等待决策卡的任务不占用槽位。
func occupiedSlots(tasks []store.Task, isReviewActive func(string) bool) int {
	count := 0
	for _, t := range tasks {
		switch t.Status {
		case string(TaskStatusInProgress):
			count++
		case string(TaskStatusNeedsApproval):
			meta, err := ParseTaskMeta(t.Meta)
			if err != nil || meta.DecisionID == "" {
				count++
			}
		case string(TaskStatusReview):
			if isReviewActive != nil && isReviewActive(t.ID) {
				count++
			}
		}
	}
	return count
}
