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
	cancelled := false
	if d.TaskID != "" {
		task, err := s.opts.Store.GetTask(ctx, d.TaskID)
		if err != nil {
			return d, err
		}
		switch TaskStatus(task.Status) {
		case TaskStatusDone, TaskStatusFailed, TaskStatusCancelled:
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
		status := TaskStatus(task.Status)
		if stop && (status == TaskStatusNeedsApproval || status == TaskStatusWaiting || status == TaskStatusTodo || status == TaskStatusInProgress) {
			if !CanTransition(status, TaskStatusCancelled) {
				return d, ErrInvalidTransition
			}
			if status == TaskStatusInProgress && task.ThreadID != nil && *task.ThreadID != "" && s.opts.StopSession != nil {
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
				if meta.DecisionID == d.ID {
					task.Status = string(TaskStatusTodo)
					meta.GateOverride, meta.DecisionID = d.ID, ""
				}
			case TaskStatusWaiting:
				meta.DecisionID = ""
				if continueWaiting {
					meta.WakeAt = now.Add(time.Duration(BackoffMinutes(0)) * time.Minute).Format(time.RFC3339)
					meta.Wakes = 0
				} else {
					task.Status = string(TaskStatusTodo)
					meta.WaitKind, meta.WakeAt, meta.WaitCheck, meta.WaitBase, meta.Wakes = "", "", "", "", 0
				}
			}
		}
		task.Meta = EncodeTaskMeta(meta)
		if _, err := s.opts.Store.UpdateTask(ctx, task, task.Revision); err != nil {
			return d, err
		}
		changed = true
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
