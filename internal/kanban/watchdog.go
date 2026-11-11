package kanban

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"mcode/internal/store"
)

// Watchdog 负责监控看板进行中的任务：
// 1. 监测 Agent 执行结束：根据 AutoReview 配置自动转入 review 状态，遇到审批转入 needs_approval；
// 2. 监测任务卡死无响应：超过 StuckNudgeTimeout 发送提醒日志，超过 StuckInterruptTimeout 且开启 AutoRescueStuck 时中断任务并标记原因。
type Watchdog struct {
	svc      *Service
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	running  bool
	stateMu  sync.Mutex

	lastProgress map[string]string    // taskID -> last activity fingerprint
	lastActivity map[string]time.Time // taskID -> last activity timestamp
	nudgedAt     map[string]time.Time // taskID -> timestamp when nudge was sent
}

// NewWatchdog 创建卡死救援与状态巡视监控实例。
func NewWatchdog(svc *Service, interval time.Duration) *Watchdog {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &Watchdog{
		svc:          svc,
		interval:     interval,
		lastProgress: make(map[string]string),
		lastActivity: make(map[string]time.Time),
		nudgedAt:     make(map[string]time.Time),
	}
}

// activityFingerprint 计算判断会话是否仍在推进的活跃指纹。
// 驱动实现的 Progress 组合了消息条数、流式文本长度、文件改动与待审批请求数，
// 任一变化都说明会话仍有新输出（长工具调用同样会让文件改动数变化）；
// 未实现 Progress 的驱动退化为比较最后一条 assistant 回复。
func activityFingerprint(st SessionState) string {
	if st.Progress != "" {
		return st.Progress
	}
	return "text:" + st.LastAssistantText
}

// interruption 中断指定会话；驱动未实现 SessionInterrupter 时返回 ErrInterruptUnsupported。
func (w *Watchdog) interruptSession(ctx context.Context, threadID string) error {
	interrupter, ok := w.svc.opts.Driver.(SessionInterrupter)
	if !ok {
		return ErrInterruptUnsupported
	}
	return interrupter.InterruptSession(ctx, threadID)
}

// autoAcceptEnabled 返回任务所属目标是否开启了自动接受。
// 看门狗逐任务巡视，只能从目标策略里读 autoAccept；读不到目标时按未开启处理。
func (w *Watchdog) autoAcceptEnabled(ctx context.Context, t store.Task) bool {
	if t.GoalID == nil || *t.GoalID == "" || w.svc.opts.Store == nil {
		return false
	}
	goal, err := w.svc.opts.Store.GetGoal(ctx, *t.GoalID)
	if err != nil {
		return false
	}
	return getPolicyBool(goal.Policy, "autoAccept", false)
}

// Start 启动后台巡视循环。
func (w *Watchdog) Start() {
	w.stateMu.Lock()
	if w.running {
		w.stateMu.Unlock()
		return
	}
	w.running = true
	w.stopCh = make(chan struct{})
	w.stateMu.Unlock()

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()
		for {
			select {
			case <-w.stopCh:
				return
			case <-ticker.C:
				_ = w.Check(context.Background())
			}
		}
	}()
}

// Stop 停止巡视循环并等待平稳退出。
func (w *Watchdog) Stop() {
	w.stateMu.Lock()
	if !w.running {
		w.stateMu.Unlock()
		return
	}
	w.running = false
	close(w.stopCh)
	w.stateMu.Unlock()
	w.wg.Wait()
}

// Check 执行单轮任务巡视，对处于 in_progress 的任务检查完成状态与最后活动时间。
func (w *Watchdog) Check(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.svc == nil || w.svc.opts.Store == nil {
		return nil
	}
	if !w.svc.isLeader() {
		return nil
	}

	tasks, err := w.svc.opts.Store.ListTasks(ctx, "")
	if err != nil {
		return err
	}

	now := time.Now()
	if w.svc.opts.Now != nil {
		now = w.svc.opts.Now()
	}

	nudgeTimeout := w.svc.GetStuckNudgeTimeout()
	interruptTimeout := w.svc.GetStuckInterruptTimeout()
	autoRescue := w.svc.AutoRescueStuckEnabled()
	autoReview := w.svc.AutoReviewEnabled()

	activeIDs := make(map[string]bool)
	changed := false

	for _, t := range tasks {
		// 只巡视进行中的任务；停在 needs_approval 的任务只有在执行会话补交了回执时才收尾，
		// 这样没有目标调度的独立任务也能在提交结果后自动离开 Needs You。
		pendingReceipt := t.Status == string(TaskStatusNeedsApproval) && w.svc.hasReceipt(t.ID, threadIDOf(t))
		if t.Status != string(TaskStatusInProgress) && !pendingReceipt {
			continue
		}
		activeIDs[t.ID] = true

		if t.ThreadID == nil || *t.ThreadID == "" || w.svc.opts.Driver == nil {
			continue
		}

		st, err := w.svc.opts.Driver.SessionState(ctx, *t.ThreadID)
		if err != nil {
			// 会话已不存在：视为已死，重置任务为待办
			if !errors.Is(err, store.ErrNotFound) {
				continue
			}
			delete(w.nudgedAt, t.ID)
			delete(w.lastActivity, t.ID)
			delete(w.lastProgress, t.ID)
			w.svc.mu.Lock()
			if t.Status == string(TaskStatusNeedsApproval) {
				if err := w.svc.closeTaskDecisions(ctx, &t); err != nil {
					w.svc.mu.Unlock()
					continue
				}
			}
			if CanTransition(TaskStatus(t.Status), TaskStatusTodo) {
				t.Status = string(TaskStatusTodo)
			}
			t.ThreadID = nil
			resetMsg := "会话已不存在，任务已重置为待办"
			t.LastActivityText = &resetMsg
			if _, err := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
				w.svc.revision.Add(1)
				changed = true
			}
			w.svc.mu.Unlock()
			continue
		}

		// 1. 处于待审批或待输入状态：保留 ThreadID，任务停在 needs_approval 等用户处理
		if st.AwaitingApproval || st.AwaitingInput {
			if CanTransition(TaskStatus(t.Status), TaskStatusNeedsApproval) {
				t.Status = string(TaskStatusNeedsApproval)
			}
			waiting := awaitingUserText
			t.LastActivityText = &waiting
			w.svc.mu.Lock()
			if up, err := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
				w.svc.revision.Add(1)
				changed = true
				_ = up
			}
			w.svc.mu.Unlock()
			delete(w.nudgedAt, t.ID)
			continue
		}

		// 2. 已经结束运行
		if !st.Running {
			delete(w.nudgedAt, t.ID)
			delete(w.lastActivity, t.ID)
			delete(w.lastProgress, t.ID)

			if st.Failed {
				if w.svc.isStewardOwnerTask(ctx, t) {
					log.Printf("kanban: 任务 %s 执行失败: %s", t.ID, st.FailureReason)
					res := ExecutorResult{Status: ResultFailed, Summary: sessionFailureSummary(st.FailureReason)}
					if err := w.svc.applyExecutorResult(ctx, &t, res, now); err != nil {
						continue
					}
					w.svc.mu.Lock()
					up, err := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision)
					if err == nil {
						w.svc.revision.Add(1)
						changed = true
					}
					w.svc.mu.Unlock()
					if err == nil && up.Status == string(TaskStatusFailed) {
						w.svc.notifyResult(ctx, up, res)
					}
					continue
				}
				t.LastFailureReason = &st.FailureReason
				w.svc.mu.Lock()
				if _, err := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					w.svc.revision.Add(1)
					changed = true
				}
				w.svc.mu.Unlock()
			} else {
				// 会话已停止：有回执按回执收尾；没有回执时按驱动能力决定是否追问
				decision := w.svc.decideFinished(ctx, t, now, st.HostToolsUnavailable)
				if decision.kind == finishWait {
					continue
				}
				if err := w.svc.applyExecutorResult(ctx, &t, decision.result, now); err != nil {
					continue
				}
				text := decision.summary
				switch decision.kind {
				case finishBlocked:
					if CanTransition(TaskStatus(t.Status), TaskStatusNeedsApproval) {
						t.Status = string(TaskStatusNeedsApproval)
					}
					text = receiptBlockedPrefix + decision.summary
					w.svc.markReceiptBlocked(t.ID)
				case finishEscalate:
					if CanTransition(TaskStatus(t.Status), TaskStatusNeedsApproval) {
						t.Status = string(TaskStatusNeedsApproval)
					}
					text = receiptEscalatedText
					w.svc.markReceiptBlocked(t.ID)
				default:
					if decision.result.Status != "" && decision.result.Status != ResultDone {
						break
					}
					if w.svc.isStewardOwnerTask(ctx, t) {
						t.Status = string(TaskStatusDone)
						break
					}
					switch {
					case autoReview:
						if CanTransition(TaskStatus(t.Status), TaskStatusReview) {
							t.Status = string(TaskStatusReview)
						}
					case w.autoAcceptEnabled(ctx, t):
						// 自动接受由目标调度器按策略处理：把回执摘要留给它，避免它误判为缺回执
						w.svc.stashPendingDone(t.ID, decision.summary)
					default:
						// 自动接受与自动评审都关闭：迁到 review 等人工处理，不能把任务留在 in_progress
						if CanTransition(TaskStatus(t.Status), TaskStatusReview) {
							t.Status = string(TaskStatusReview)
						}
						// 有回执时卡片直接显示执行会话的结论，旧行为仍是原有文案
						if !decision.fromReceipt {
							text = manualReviewText
						}
					}
				}
				t.LastActivityText = &text
				w.svc.mu.Lock()
				up, updateErr := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision)
				if updateErr == nil {
					w.svc.revision.Add(1)
					changed = true
				}
				w.svc.mu.Unlock()
				if updateErr == nil && !(up.Status == string(TaskStatusTodo) && decision.result.Status == ResultFailed) {
					w.svc.notifyResult(ctx, up, decision.result)
				}
			}
			continue
		}

		// 3. 仍在运行中：比较活跃指纹，指纹变化即视为有新输出并刷新活动时间
		fingerprint := activityFingerprint(st)
		prevFingerprint, hasPrev := w.lastProgress[t.ID]
		if !hasPrev || fingerprint != prevFingerprint {
			w.lastProgress[t.ID] = fingerprint
			w.lastActivity[t.ID] = now
			// 有新进展就重置催促周期，避免长工具调用的健康回合被判卡死
			delete(w.nudgedAt, t.ID)
		} else if _, hasAct := w.lastActivity[t.ID]; !hasAct {
			if !t.UpdatedAt.IsZero() {
				w.lastActivity[t.ID] = t.UpdatedAt
			} else {
				w.lastActivity[t.ID] = now
			}
		}

		lastAct := w.lastActivity[t.ID]
		inactivity := now.Sub(lastAct)

		// 超过 StuckNudgeTimeout 发送一次催促（D-KB-029），无论成败都记录 nudgedAt，避免每轮重复发送
		if inactivity >= nudgeTimeout {
			if _, nudged := w.nudgedAt[t.ID]; !nudged {
				w.nudgedAt[t.ID] = now
				var nudgeMsg string
				if messenger, ok := w.svc.opts.Driver.(SessionMessenger); ok {
					if err := messenger.SendFollowUp(ctx, *t.ThreadID, StuckPokeMessage); err != nil {
						nudgeMsg = fmt.Sprintf("任务已超过 %d 秒无活动，催促发送失败：%v", int(nudgeTimeout.Seconds()), err)
					} else {
						nudgeMsg = fmt.Sprintf("任务已超过 %d 秒无活动，已发送催促", int(nudgeTimeout.Seconds()))
					}
				} else {
					nudgeMsg = fmt.Sprintf("任务已超过 %d 秒无活动，催促发送失败：驱动不支持发送催促", int(nudgeTimeout.Seconds()))
				}
				t.LastActivityText = &nudgeMsg
				w.svc.mu.Lock()
				if _, err := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					w.svc.revision.Add(1)
					changed = true
				}
				w.svc.mu.Unlock()
			}
		}

		// 超过 StuckInterruptTimeout 且开启 AutoRescueStuck 时中断任务并标记原因（D-KB-030）
		if _, nudged := w.nudgedAt[t.ID]; nudged {
			if inactivity >= interruptTimeout && autoRescue {
				if err := w.interruptSession(ctx, *t.ThreadID); err != nil {
					// 驱动不支持中断或中断失败：保留任务原状态、ThreadID 与催促记录，留待下轮重试；
					// 文案同一就不重复写库，避免每轮巡视都把同一行写一遍。
					failReason := fmt.Sprintf("卡死救援中断失败：%v，保留原状态待下轮重试", err)
					if t.LastFailureReason == nil || *t.LastFailureReason != failReason ||
						t.LastActivityText == nil || *t.LastActivityText != failReason {
						t.LastFailureReason = &failReason
						t.LastActivityText = &failReason
						w.svc.mu.Lock()
						if _, uerr := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision); uerr == nil {
							w.svc.revision.Add(1)
							changed = true
						}
						w.svc.mu.Unlock()
					}
					continue
				}
				reason := fmt.Sprintf("任务连续 %d 秒无活动，卡死救援已自动中断", int(interruptTimeout.Seconds()))
				t.LastFailureReason = &reason
				t.LastActivityText = &reason
				if CanTransition(TaskStatus(t.Status), TaskStatusTodo) {
					t.Status = string(TaskStatusTodo)
				}
				t.ThreadID = nil
				w.svc.mu.Lock()
				if _, err := w.svc.opts.Store.UpdateTask(ctx, t, t.Revision); err == nil {
					w.svc.revision.Add(1)
					changed = true
				}
				w.svc.mu.Unlock()
				delete(w.nudgedAt, t.ID)
				delete(w.lastActivity, t.ID)
				delete(w.lastProgress, t.ID)
			}
		}
	}

	// 清理非活动任务缓存
	for id := range w.lastActivity {
		if !activeIDs[id] {
			delete(w.lastActivity, id)
			delete(w.lastProgress, id)
			delete(w.nudgedAt, id)
		}
	}

	if changed {
		w.svc.notifyChange()
	}
	return nil
}
