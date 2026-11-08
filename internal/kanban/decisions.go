package kanban

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"mcode/internal/store"
)

const decisionReanswerWindow = 5 * time.Minute

// AnswerDecision 回答决策卡，并将答案追加到关联任务。
// 已答卡在五分钟内允许改答，其他已关闭状态不能再答。
func (s *Service) AnswerDecision(ctx context.Context, id, answer, by string) (store.StewardDecision, error) {
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
	defer func() {
		s.revision.Add(1)
		s.notifyChange()
	}()
	if d.TaskID != "" {
		task, err := s.opts.Store.GetTask(ctx, d.TaskID)
		if err != nil {
			return d, err
		}
		meta, err := ParseTaskMeta(task.Meta)
		if err != nil {
			return d, err
		}
		line := fmt.Sprintf("决策 %s：%s → %s", d.ID, d.Question, answer)
		if reanswer {
			line = fmt.Sprintf("决策 %s：改答为 %s", d.ID, answer)
		}
		task.Notes = strings.TrimSpace(task.Notes + "\n\n" + line)
		switch TaskStatus(task.Status) {
		case TaskStatusNeedsApproval:
			if meta.DecisionID == d.ID {
				task.Status = string(TaskStatusTodo)
				meta.GateOverride, meta.DecisionID = d.ID, ""
			}
		case TaskStatusWaiting:
			task.Status = string(TaskStatusTodo)
			meta.WaitKind, meta.WakeAt, meta.WaitCheck, meta.WaitBase, meta.Wakes = "", "", "", "", 0
		case TaskStatusDone, TaskStatusFailed, TaskStatusCancelled:
			task.Status = string(TaskStatusTodo)
		}
		task.Meta = EncodeTaskMeta(meta)
		if _, err := s.opts.Store.UpdateTask(ctx, task, task.Revision); err != nil {
			return d, err
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
	return answered, nil
}
