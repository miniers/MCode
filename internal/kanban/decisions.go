package kanban

import (
	"context"
	"fmt"
	"strings"

	"mcode/internal/store"
)

// AnswerDecision 关闭决策卡，并将答案追加到关联任务。
func (s *Service) AnswerDecision(ctx context.Context, id, answer, by string) (store.StewardDecision, error) {
	d, err := s.opts.Store.AnswerStewardDecision(ctx, id, answer, by, s.nowTime())
	if err != nil {
		return d, err
	}
	defer func() {
		s.revision.Add(1)
		s.notifyChange()
	}()
	if d.TaskID == "" {
		return d, nil
	}

	task, err := s.opts.Store.GetTask(ctx, d.TaskID)
	if err != nil {
		return d, err
	}
	meta, err := ParseTaskMeta(task.Meta)
	if err != nil {
		return d, err
	}
	task.Notes = strings.TrimSpace(task.Notes + "\n\n" + fmt.Sprintf("决策 %s：%s → %s", d.ID, d.Question, d.Answer))
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
	return d, nil
}
