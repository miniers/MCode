package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 本文件把桌面端看板卡片的「删除」与「救援」开放给远程客户端：
//   - kanban.deleteTask：删除一条任务（运行中的任务由服务侧拒绝，与桌面端一致）；
//   - kanban.rescueTask：执行救援菜单里的动作（中断并重置为待办、提醒 Agent）。
//
// 桌面端同源逻辑见 internal/ui/kanban.go 的救援菜单与 interruptTaskToTodo，
// 服务侧见 internal/kanban/service_edit.go 的 DeleteTask 与 service_rescue.go 的 RescueTask。
// 幂等（actionID 重放）与修订号冲突的处理方式与内置看板方法一致，见 kanban_methods.go。

// kanbanExtService 是看板删除与救援需要的服务能力，由 *kanban.Service 实现。
// 单独成接口而不是扩进 KanbanService：远程扩展不要求所有看板实现都支持救援。
type kanbanExtService interface {
	// DeleteTask 按修订号删除任务；任务仍有执行中会话时返回 kanban.ErrInvalidTransition。
	DeleteTask(ctx context.Context, taskID string, expectedRevision int) error
	// RescueTask 按动作救援任务，返回更新后的任务。
	RescueTask(ctx context.Context, taskID string, action kanban.RescueAction, expectedRevision int) (store.Task, error)
}

func init() {
	RegisterMethod("kanban.deleteTask", handleKanbanDeleteTask)
	RegisterMethod("kanban.rescueTask", handleKanbanRescueTask)
}

// handleKanbanDeleteTask 删除一条任务并广播看板变化。
func handleKanbanDeleteTask(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.KanbanDeleteTaskParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.TaskID) {
		return nil, ExtInvalidParams("Invalid params: 'taskID' must be a valid UUID")
	}
	if p.ExpectedRevision < 0 {
		return nil, ExtInvalidParams("Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return nil, ExtInvalidParams("Invalid params: 'actionID' must be a valid UUID")
	}

	// 幂等重放：同一个 actionID 直接返回首次结果，不再重复删除。
	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			}, nil
		}
	}

	svc, svcErr := kanbanExtServiceFrom(d)
	if svcErr != nil {
		return nil, svcErr
	}
	if err := svc.DeleteTask(ctx, p.TaskID, p.ExpectedRevision); err != nil {
		return nil, kanbanExtError(err)
	}

	taskID := p.TaskID
	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
		})
	}
	d.NotifyKanbanChanged()

	// 删除后任务已不存在，结果里只回 actionID 与 taskID。
	return wire.KanbanTaskMutationResult{ActionID: p.ActionID, TaskID: &taskID}, nil
}

// handleKanbanRescueTask 执行一次救援动作并广播看板变化。
func handleKanbanRescueTask(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.KanbanRescueTaskParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.TaskID) {
		return nil, ExtInvalidParams("Invalid params: 'taskID' must be a valid UUID")
	}
	action := kanban.RescueAction(strings.TrimSpace(p.Action))
	if !action.Valid() {
		return nil, ExtInvalidParams("Invalid params: 'action' must be interrupt or nudge")
	}
	if p.ExpectedRevision < 0 {
		return nil, ExtInvalidParams("Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return nil, ExtInvalidParams("Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			}, nil
		}
	}

	svc, svcErr := kanbanExtServiceFrom(d)
	if svcErr != nil {
		return nil, svcErr
	}
	task, err := svc.RescueTask(ctx, p.TaskID, action, p.ExpectedRevision)
	if err != nil {
		return nil, kanbanExtError(err)
	}

	taskID := task.ID
	rev := task.Revision
	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
			GoalID:   task.GoalID,
			Revision: &rev,
		})
	}
	d.NotifyKanbanChanged()

	return wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   &taskID,
		GoalID:   task.GoalID,
		Revision: &rev,
	}, nil
}

// kanbanExtServiceFrom 取出支持删除与救援的看板服务。
// 服务未注入时与内置看板方法一致返回 OperationRejected；注入了但不支持救援则按未实现处理。
func kanbanExtServiceFrom(d *Dispatcher) (kanbanExtService, error) {
	if d.kanban == nil {
		return nil, &ExtError{Code: wire.ErrCodeOperationRejected, Message: "kanban service unavailable"}
	}
	svc, ok := d.kanban.(kanbanExtService)
	if !ok {
		return nil, ExtNotImplemented()
	}
	return svc, nil
}

// kanbanExtError 把看板服务的错误翻译成协议错误码，与内置看板方法的 mapKanbanError 口径一致。
func kanbanExtError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, store.ErrRevisionConflict):
		return &ExtError{Code: wire.ErrCodeVersionConflict, Message: "Version conflict: expected revision does not match current revision"}
	case errors.Is(err, store.ErrNotFound):
		return &ExtError{Code: wire.ErrCodeResourceNotFound, Message: "Resource not found"}
	case errors.Is(err, kanban.ErrInvalidTransition),
		errors.Is(err, kanban.ErrInterruptUnsupported),
		errors.Is(err, kanban.ErrNudgeUnsupported):
		return &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
	}
	return err
}
