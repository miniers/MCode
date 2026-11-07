package kanban

import (
	"context"
	"errors"
	"strings"

	"mcode/internal/store"
)

// ErrNudgeUnsupported 表示驱动不支持向运行中的会话追加消息，无法执行「提醒 Agent」。
var ErrNudgeUnsupported = errors.New("kanban: session driver does not support follow-up")

// RescueAction 是任务救援动作，取值与桌面端看板卡片的救援菜单一致：
//   - interrupt：中断执行中的会话并把任务重置为待办（桌面「中断并重置为待办」）；
//   - nudge：给执行中的会话追加一条催促消息（桌面「提醒 Agent」）。
type RescueAction string

const (
	RescueActionInterrupt RescueAction = "interrupt"
	RescueActionNudge     RescueAction = "nudge"
)

// Valid 判断救援动作是否受支持。
func (a RescueAction) Valid() bool {
	return a == RescueActionInterrupt || a == RescueActionNudge
}

// RescueTask 按动作救援一条任务，返回更新后的任务。
// expectedRevision 大于 0 时必须与当前修订号一致，否则返回 store.ErrRevisionConflict；
// 为 0 表示不校验（与 DeleteTask 一致）。
func (s *Service) RescueTask(ctx context.Context, taskID string, action RescueAction, expectedRevision int) (store.Task, error) {
	if strings.TrimSpace(taskID) == "" || !action.Valid() {
		return store.Task{}, ErrInvalidTransition
	}

	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if expectedRevision > 0 && task.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}

	switch action {
	case RescueActionInterrupt:
		if err := s.interruptSessionLocked(ctx, task); err != nil {
			return store.Task{}, err
		}
		// 与桌面端一致：中断之后把任务重置为待办；已经是待办就不写库。
		if TaskStatus(task.Status) == TaskStatusTodo {
			return task, nil
		}
		if !CanTransition(TaskStatus(task.Status), TaskStatusTodo) {
			return store.Task{}, ErrInvalidTransition
		}
		task.Status = string(TaskStatusTodo)
		note := "已按救援请求中断执行并重置为待办"
		task.LastActivityText = &note
		updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
		if err != nil {
			return store.Task{}, err
		}
		if updated.GoalID != nil && *updated.GoalID != "" {
			s.updateGoalProgressLocked(ctx, *updated.GoalID)
		}
		s.revision.Add(1)
		changed = true
		return updated, nil
	case RescueActionNudge:
		if task.ThreadID == nil || strings.TrimSpace(*task.ThreadID) == "" {
			return store.Task{}, ErrInvalidTransition
		}
		if s.opts.Driver == nil {
			return store.Task{}, ErrNudgeUnsupported
		}
		messenger, ok := s.opts.Driver.(SessionMessenger)
		if !ok {
			return store.Task{}, ErrNudgeUnsupported
		}
		if err := messenger.SendFollowUp(ctx, *task.ThreadID, StuckPokeMessage); err != nil {
			return store.Task{}, err
		}
		note := "已按救援请求发送催促消息"
		task.LastActivityText = &note
		updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
		if err != nil {
			return store.Task{}, err
		}
		s.revision.Add(1)
		changed = true
		return updated, nil
	}
	return store.Task{}, ErrInvalidTransition
}

// interruptSessionLocked 中断任务的执行会话，调用方必须已持有 s.mu。
func (s *Service) interruptSessionLocked(ctx context.Context, task store.Task) error {
	if task.ThreadID == nil || strings.TrimSpace(*task.ThreadID) == "" {
		return ErrInvalidTransition
	}
	if s.opts.Driver == nil {
		return ErrInterruptUnsupported
	}
	interrupter, ok := s.opts.Driver.(SessionInterrupter)
	if !ok {
		return ErrInterruptUnsupported
	}
	return interrupter.InterruptSession(ctx, *task.ThreadID)
}
