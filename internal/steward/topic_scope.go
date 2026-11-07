package steward

import "context"

// TopicOwnsSubject 报告 subjectType/subjectID 指向的对象是否属于该话题的目标。
// subjectType 取 thread（任务的执行会话或评审会话）或 kanban_task（看板任务）。
func (r *Runtime) TopicOwnsSubject(ctx context.Context, topicThreadID, subjectType, subjectID string) (bool, error) {
	topic, ok, err := r.Topic(ctx, topicThreadID)
	if err != nil {
		return false, err
	}
	if !ok || topic.Closed || topic.GoalID == "" {
		return false, nil
	}
	if r.kanban == nil {
		return false, nil
	}
	board, err := r.kanban.Board(ctx)
	if err != nil {
		return false, err
	}
	// 只认本目标下的任务；其他目标的任务即使绑了同一个会话也不算。
	for _, view := range board.Tasks {
		task := view.Task
		if task.GoalID == nil || *task.GoalID != topic.GoalID {
			continue
		}
		switch subjectType {
		case "kanban_task":
			if task.ID == subjectID {
				return true, nil
			}
		case "thread":
			if task.ThreadID != nil && *task.ThreadID == subjectID {
				return true, nil
			}
			if task.ReviewThreadID != nil && *task.ReviewThreadID == subjectID {
				return true, nil
			}
		}
	}
	return false, nil
}
