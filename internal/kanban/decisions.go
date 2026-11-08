package kanban

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"mcode/internal/store"
)

// AnswerDecision 关闭决策卡，并将答案追加到关联任务。
// 先更新任务再关卡：任务更新失败时卡片仍是 open，可以再次回答。
func (s *Service) AnswerDecision(ctx context.Context, id, answer, by string) (store.StewardDecision, error) {
	d, err := s.opts.Store.GetStewardDecision(ctx, id)
	if err != nil {
		return d, err
	}
	if d.Status != store.DecisionStatusOpen {
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
		task.Notes = strings.TrimSpace(task.Notes + "\n\n" + fmt.Sprintf("决策 %s：%s → %s", d.ID, d.Question, answer))
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

	answered, err := s.opts.Store.AnswerStewardDecision(ctx, id, answer, by, s.nowTime())
	if err != nil {
		if errors.Is(err, store.ErrDecisionClosed) {
			// 并发重复回答：任务更新已经完成，不回滚，直接报告卡片已关闭。
			return d, store.ErrDecisionClosed
		}
		return d, err
	}
	return answered, nil
}
