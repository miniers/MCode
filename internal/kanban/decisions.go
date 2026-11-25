package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mcode/internal/store"
)

const decisionReanswerWindow = 5 * time.Minute

var ErrDecisionLocked = errors.New("任务已开始执行，不能改答")

func decisionAnswer(d store.StewardDecision, answer string) (string, string) {
	var options []struct {
		Key  string `json:"key"`
		Text string `json:"text"`
	}
	if json.Unmarshal([]byte(d.OptionsJSON), &options) == nil {
		trimmed := strings.TrimSpace(answer)
		for _, option := range options {
			if strings.EqualFold(trimmed, option.Key) || trimmed == option.Text {
				return option.Text, option.Key + " · " + option.Text
			}
		}
	}
	return answer, answer
}

// AnswerDecision 回答决策卡，并将答案追加到关联任务。
// 已答卡在五分钟内允许改答，其他已关闭状态不能再答。
func (s *Service) AnswerDecision(ctx context.Context, id, answer, by string) (store.StewardDecision, error) {
	if strings.TrimSpace(answer) == "" {
		return store.StewardDecision{}, ErrInvalidTransition
	}
	changed := false
	defer func() {
		if changed {
			s.revision.Add(1)
			s.notifyChange()
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	d, err := s.opts.Store.GetStewardDecision(ctx, id)
	if err != nil {
		return d, err
	}
	now := s.nowTime()
	reanswer := false
	switch d.Status {
	case store.DecisionStatusOpen:
	case store.DecisionStatusAnswered:
		if d.AnsweredAt == nil || now.Sub(*d.AnsweredAt) > decisionReanswerWindow {
			return d, store.ErrDecisionClosed
		}
		reanswer = true
	default:
		return d, store.ErrDecisionClosed
	}
	answerText, answerDisplay := decisionAnswer(d, answer)
	trimmedAnswer := strings.TrimSpace(answerText)
	stop := strings.HasPrefix(trimmedAnswer, "停下") || strings.HasPrefix(trimmedAnswer, "停止") || strings.HasPrefix(trimmedAnswer, "取消")
	continueWaiting := trimmedAnswer == "继续等"
	ignore := trimmedAnswer == "忽略"
	cancelled := false
	var followUpMessenger SessionMessenger
	followUpThread := ""
	followUpPrompt := ""
	if d.TaskID != "" {
		task, err := s.opts.Store.GetTask(ctx, d.TaskID)
		if err != nil {
			return d, err
		}
		switch TaskStatus(task.Status) {
		case TaskStatusDone, TaskStatusFailed, TaskStatusCancelled, TaskStatusRejected:
			return d, store.ErrDecisionClosed
		case TaskStatusInProgress, TaskStatusReview:
			if reanswer {
				return d, ErrDecisionLocked
			}
		}
		meta, err := ParseTaskMeta(task.Meta)
		if err != nil {
			return d, err
		}
		line := fmt.Sprintf("决策 %s：%s → %s", d.ID, d.Question, answerDisplay)
		if reanswer {
			line = fmt.Sprintf("决策 %s：改答为 %s", d.ID, answerDisplay)
		}
		task.Notes = strings.TrimSpace(task.Notes + "\n\n" + line)
		stale := !reanswer && meta.DecisionID != d.ID
		status := TaskStatus(task.Status)
		tryResume := false
		if !stale {
			if stop && (status == TaskStatusNeedsApproval || status == TaskStatusWaiting || status == TaskStatusTodo || status == TaskStatusInProgress) {
				if !CanTransition(status, TaskStatusCancelled) {
					return d, ErrInvalidTransition
				}
				if task.ThreadID != nil && *task.ThreadID != "" && s.TaskExecuting(ctx, task) && s.opts.StopSession != nil {
					if err := s.opts.StopSession(ctx, *task.ThreadID); err != nil {
						return d, err
					}
				}
				task.Status = string(TaskStatusCancelled)
				meta.WaitKind, meta.WakeAt, meta.WaitCheck, meta.WaitBase, meta.Wakes = "", "", "", "", 0
				meta.DecisionID = ""
				cancelled = true
			} else {
				switch status {
				case TaskStatusNeedsApproval:
					meta.DecisionID = ""
					if meta.WaitKind != "" {
						// 等待到期弹出的决策卡不写硬锁授权，按等待分支语义更新状态。
						if continueWaiting || ignore {
							applyWaitDecisionAnswer(&task, &meta, now, true)
						} else {
							applyWaitDecisionAnswer(&task, &meta, now, false)
							tryResume = true
						}
					} else {
						if !ignore {
							meta.GateOverride = d.ID
						}
						tryResume = true
					}
				case TaskStatusWaiting:
					meta.DecisionID = ""
					if continueWaiting || ignore {
						applyWaitDecisionAnswer(&task, &meta, now, true)
					} else {
						applyWaitDecisionAnswer(&task, &meta, now, false)
						tryResume = true
					}
				}
			}
			if tryResume {
				if messenger, thread, ok := s.canResumeDecisionSession(ctx, task); ok {
					task.Status = string(TaskStatusInProgress)
					followUpMessenger = messenger
					followUpThread = thread
					if ignore {
						followUpPrompt = decisionIgnorePrompt(task.ID, d.ID, d.Question)
					} else {
						followUpPrompt = decisionResumePrompt(task.ID, d.ID, d.Question, answerDisplay)
					}
				} else {
					task.Status = string(TaskStatusTodo)
				}
			}
			task.Meta = EncodeTaskMeta(meta)
		}
		updated, err := s.opts.Store.UpdateTask(ctx, task, task.Revision)
		if err != nil {
			return d, err
		}
		changed = true
		if followUpMessenger != nil {
			// 先落库再唤醒原会话，保证新轮次读取到最新的硬锁授权与任务状态，并在发送前清掉上一轮回执计数。
			s.clearReceiptRound(updated.ID)
			if err := followUpMessenger.SendFollowUp(ctx, followUpThread, followUpPrompt); err != nil {
				updated.Status = string(TaskStatusTodo)
				if _, rbErr := s.opts.Store.UpdateTask(ctx, updated, updated.Revision); rbErr != nil {
					return d, rbErr
				}
			}
		}
		if cancelled && task.GoalID != nil && *task.GoalID != "" {
			s.updateGoalProgressLocked(ctx, *task.GoalID)
		}
	}

	var answered store.StewardDecision
	if reanswer {
		answered, err = s.opts.Store.ReanswerStewardDecision(ctx, id, answer, by, now)
	} else {
		answered, err = s.opts.Store.AnswerStewardDecision(ctx, id, answer, by, now)
	}
	if err != nil {
		if errors.Is(err, store.ErrDecisionClosed) {
			// 并发重复回答：任务更新已经完成，不回滚，直接报告卡片已关闭。
			return d, store.ErrDecisionClosed
		}
		return d, err
	}
	changed = true
	if cancelled {
		if err := s.opts.Store.SupersedeOpenDecisionsForTask(ctx, d.TaskID); err != nil {
			return answered, err
		}
	}
	return answered, nil
}

func (s *Service) closeTaskDecisions(ctx context.Context, task *store.Task) error {
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil {
		return err
	}
	if err := s.opts.Store.SupersedeOpenDecisionsForTask(ctx, task.ID); err != nil {
		return err
	}
	meta.DecisionID = ""
	task.Meta = EncodeTaskMeta(meta)
	return nil
}

// applyWaitDecisionAnswer 根据回答更新等待任务的状态与等待元数据。
func applyWaitDecisionAnswer(task *store.Task, meta *TaskMeta, now time.Time, continueWaiting bool) {
	if continueWaiting {
		task.Status = string(TaskStatusWaiting)
		meta.WakeAt = now.Add(time.Duration(BackoffMinutes(0)) * time.Minute).Format(time.RFC3339)
		meta.Wakes = 0
		return
	}
	task.Status = string(TaskStatusTodo)
	meta.WaitKind, meta.WakeAt, meta.WaitCheck, meta.WaitBase, meta.Wakes = "", "", "", "", 0
}

func (s *Service) canResumeDecisionSession(ctx context.Context, task store.Task) (SessionMessenger, string, bool) {
	thread := strings.TrimSpace(threadIDOf(task))
	if thread == "" || s.opts.Driver == nil {
		return nil, "", false
	}
	messenger, supportsFollowUp := s.opts.Driver.(SessionMessenger)
	if !supportsFollowUp || messenger == nil {
		return nil, "", false
	}
	st, err := s.opts.Driver.SessionState(ctx, thread)
	if err != nil || st.HostToolsUnavailable {
		return nil, "", false
	}
	return messenger, thread, true
}

func decisionResumePrompt(taskID, decisionID, question, answerDisplay string) string {
	return fmt.Sprintf("任务 ID：`%s`。用户回答了决策 %s：%s → %s。请按这个决定继续完成任务，不要重做已完成的部分；完成后调用 kanban_submit_result 提交结果。",
		taskID, decisionID, question, answerDisplay)
}

func decisionIgnorePrompt(taskID, decisionID, question string) string {
	return fmt.Sprintf("任务 ID：`%s`。用户忽略了决策 %s（%s），没有授权该操作。请不做这件事，继续完成其余工作；做不下去就提交 needs_decision 或 failed。",
		taskID, decisionID, question)
}
