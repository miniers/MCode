package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// KanbanService 定义看板与目标业务服务接口。
type KanbanService interface {
	Board(ctx context.Context) (kanban.Board, error)
	CreateTask(ctx context.Context, input kanban.CreateTaskInput) (store.Task, error)
	MoveTask(ctx context.Context, taskID string, status kanban.TaskStatus, expectedRevision int) (store.Task, error)
	StartTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error)
	ReviewTask(ctx context.Context, taskID string, decision kanban.ReviewDecision, feedback string, expectedRevision int) (store.Task, error)
	TaskDiff(ctx context.Context, taskID string) (string, bool, error)
	CreateGoal(ctx context.Context, projectID, prompt string) (store.Goal, error)
	StartGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error)
	PauseGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error)
	CancelGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error)
	ReplanGoal(ctx context.Context, goalID, feedback string, expectedRevision int) (store.Goal, error)
	RetryGoalTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error)
	SkipGoalTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error)
}

// SetKanban 设置 Dispatcher 使用的看板服务实例。
func (d *Dispatcher) SetKanban(k KanbanService) {
	d.kanban = k
}

// NotifyKanbanChanged 当看板产生变更时，拉取最新看板数据并向所有已认证连接广播 kanban.taskUpdated 通知。
func (d *Dispatcher) NotifyKanbanChanged() {
	if d.kanban == nil {
		return
	}
	ctx := context.Background()
	board, err := d.kanban.Board(ctx)
	if err != nil {
		return
	}
	wireBoard, err := d.convertToWireBoard(ctx, board)
	if err != nil {
		return
	}

	d.authedMu.RLock()
	conns := make([]Conn, 0, len(d.authedConns))
	for c := range d.authedConns {
		conns = append(conns, c)
	}
	d.authedMu.RUnlock()

	for _, c := range conns {
		_ = c.Notify("kanban.taskUpdated", wireBoard)
	}
}

func taskViewToWire(tv kanban.TaskView) wire.KanbanTask {
	t := tv.Task
	derived := tv.Derived
	meta, err := kanban.ParseTaskMeta(t.Meta)
	if err != nil {
		meta = kanban.TaskMeta{}
	}

	dependsOn := t.DependsOn
	if dependsOn == nil {
		dependsOn = []string{}
	}
	attentionFlags := derived.AttentionFlags
	if attentionFlags == nil {
		attentionFlags = []string{}
	}

	return wire.KanbanTask{
		ID:                 t.ID,
		Origin:             meta.Origin,
		ParentID:           meta.ParentID,
		SourceMessageID:    meta.SourceMessageID,
		ProjectID:          t.ProjectID,
		ThreadID:           t.ThreadID,
		ReviewThreadID:     t.ReviewThreadID,
		GoalID:             t.GoalID,
		DependsOn:          dependsOn,
		Title:              t.Title,
		Notes:              t.Notes,
		AcceptanceCriteria: t.AcceptanceCriteria,
		Status:             t.Status,
		Priority:           t.Priority,
		UpdatedAt:          t.UpdatedAt.UTC().Format(time.RFC3339),
		Blocked:            derived.Blocked,
		ReviewSummary:      t.ReviewSummary,
		ReviewFeedback:     t.ReviewFeedback,
		ReviewStatus:       t.ReviewStatus,
		CanStart:           derived.CanStart,
		CanMove:            derived.CanMove,
		CanReview:          derived.CanReview,
		CanApprove:         derived.CanApprove,
		AttentionFlags:     attentionFlags,
		DisplayColumn:      string(derived.DisplayColumn),
		LastActivityText:   t.LastActivityText,
		AttemptCount:       t.AttemptCount,
		RejectCount:        t.RejectCount,
		LastFailureReason:  t.LastFailureReason,
		ResultSummary:      meta.Summary,
		WaitKind:           meta.WaitKind,
		WakeAt:             meta.WakeAt,
		Revision:           t.Revision,
	}
}

func goalToWire(g store.Goal) wire.KanbanGoal {
	policy := g.Policy
	if policy == nil {
		policy = make(map[string]any)
	}
	progress := g.Progress
	if progress == nil {
		progress = make(map[string]any)
	}

	return wire.KanbanGoal{
		ID:            g.ID,
		ProjectID:     g.ProjectID,
		Title:         g.Title,
		Prompt:        g.Prompt,
		Status:        g.Status,
		Owner:         g.Owner,
		Policy:        policy,
		Progress:      progress,
		PlanThreadID:  g.PlanThreadID,
		PlanSummary:   g.PlanSummary,
		StatusReason:  g.StatusReason,
		ResultSummary: g.ResultSummary,
		Revision:      g.Revision,
		UpdatedAt:     g.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func (d *Dispatcher) convertToWireBoard(ctx context.Context, board kanban.Board) (wire.KanbanBoard, error) {
	projects := make([]wire.KanbanProject, 0)
	if d.backend != nil {
		projList, err := d.backend.ListProjects(ctx)
		if err == nil && len(projList.Projects) > 0 {
			projects = make([]wire.KanbanProject, 0, len(projList.Projects))
			for _, p := range projList.Projects {
				projects = append(projects, wire.KanbanProject{
					ID:   p.ID,
					Name: p.Name,
				})
			}
		}
	}

	tasks := make([]wire.KanbanTask, 0, len(board.Tasks))
	for _, tv := range board.Tasks {
		tasks = append(tasks, taskViewToWire(tv))
	}

	goals := make([]wire.KanbanGoal, 0, len(board.Goals))
	for _, g := range board.Goals {
		goals = append(goals, goalToWire(g))
	}

	return wire.KanbanBoard{
		SchemaVersion: 1,
		Revision:      board.Revision,
		Projects:      projects,
		Tasks:         tasks,
		Goals:         goals,
	}, nil
}

func mapKanbanError(err error, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	if errors.Is(err, store.ErrRevisionConflict) {
		return replyErr(wire.ErrCodeVersionConflict, "Version conflict: expected revision does not match current revision")
	}
	if errors.Is(err, store.ErrNotFound) {
		return replyErr(wire.ErrCodeResourceNotFound, "Resource not found")
	}
	if errors.Is(err, kanban.ErrInvalidTransition) {
		return replyErr(wire.ErrCodeOperationRejected, err.Error())
	}
	return replyWithBackendErr(err)
}

func (d *Dispatcher) handleKanbanGetBoard(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	if !checkEmptyParams(params) {
		return replyErr(wire.InvalidParams, "Invalid params: 'kanban.getBoard' takes no parameters")
	}
	if d.kanban == nil {
		res, err := d.backend.KanbanBoard(ctx)
		if err != nil {
			return replyWithBackendErr(err)
		}
		return replySuccess(res)
	}
	board, err := d.kanban.Board(ctx)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}
	wireBoard, err := d.convertToWireBoard(ctx, board)
	if err != nil {
		return replyWithBackendErr(err)
	}
	return replySuccess(wireBoard)
}

func (d *Dispatcher) handleKanbanCreateTask(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.KanbanCreateTaskParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}
	if strings.TrimSpace(p.Title) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'title' is required")
	}
	if p.ThreadID != nil && !isValidUUID(*p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	task, err := d.kanban.CreateTask(ctx, kanban.CreateTaskInput{
		ProjectID: p.ProjectID,
		Title:     p.Title,
		Notes:     p.Notes,
		ThreadID:  p.ThreadID,
	})
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	taskID := task.ID
	rev := task.Revision
	goalID := task.GoalID

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
			GoalID:   goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   &taskID,
		GoalID:   goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleKanbanMoveTask(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.KanbanMoveTaskParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.TaskID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'taskID' must be a valid UUID")
	}
	st := kanban.TaskStatus(p.Status)
	if !st.Valid() {
		return replyErr(wire.InvalidParams, "Invalid params: 'status' is invalid")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	task, err := d.kanban.MoveTask(ctx, p.TaskID, st, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	taskID := task.ID
	rev := task.Revision
	goalID := task.GoalID

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
			GoalID:   goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   &taskID,
		GoalID:   goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleKanbanStartTask(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.KanbanStartTaskParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.TaskID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'taskID' must be a valid UUID")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	task, err := d.kanban.StartTask(ctx, p.TaskID, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	taskID := task.ID
	rev := task.Revision
	goalID := task.GoalID

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
			GoalID:   goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   &taskID,
		GoalID:   goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleKanbanReviewTask(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.KanbanReviewTaskParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.TaskID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'taskID' must be a valid UUID")
	}
	if p.Decision != "accept" && p.Decision != "reject" && p.Decision != "requestReview" {
		return replyErr(wire.InvalidParams, "Invalid params: 'decision' must be one of 'accept', 'reject', or 'requestReview'")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	task, err := d.kanban.ReviewTask(ctx, p.TaskID, kanban.ReviewDecision(p.Decision), p.Feedback, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	taskID := task.ID
	rev := task.Revision
	goalID := task.GoalID

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
			GoalID:   goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   &taskID,
		GoalID:   goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleKanbanGetDiff(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.KanbanGetDiffParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.TaskID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'taskID' must be a valid UUID")
	}
	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	diff, truncated, err := d.kanban.TaskDiff(ctx, p.TaskID)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	return replySuccess(wire.KanbanGetDiffResult{
		TaskID:    p.TaskID,
		Diff:      diff,
		Truncated: truncated,
	})
}

func (d *Dispatcher) handleGoalCreate(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.GoalCreateParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ProjectID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'projectID' must be a valid UUID")
	}
	if strings.TrimSpace(p.Prompt) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'prompt' is required")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	goal, err := d.kanban.CreateGoal(ctx, p.ProjectID, p.Prompt)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	goalID := goal.ID
	rev := goal.Revision

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   nil,
			GoalID:   &goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   nil,
		GoalID:   &goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleGoalStart(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.GoalMutationParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.GoalID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'goalID' must be a valid UUID")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	goal, err := d.kanban.StartGoal(ctx, p.GoalID, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	goalID := goal.ID
	rev := goal.Revision

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   nil,
			GoalID:   &goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   nil,
		GoalID:   &goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleGoalPause(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.GoalMutationParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.GoalID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'goalID' must be a valid UUID")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	goal, err := d.kanban.PauseGoal(ctx, p.GoalID, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	goalID := goal.ID
	rev := goal.Revision

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   nil,
			GoalID:   &goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   nil,
		GoalID:   &goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleGoalCancel(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.GoalMutationParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.GoalID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'goalID' must be a valid UUID")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	goal, err := d.kanban.CancelGoal(ctx, p.GoalID, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	goalID := goal.ID
	rev := goal.Revision

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   nil,
			GoalID:   &goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   nil,
		GoalID:   &goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleGoalReplan(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.GoalReplanParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.GoalID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'goalID' must be a valid UUID")
	}
	if strings.TrimSpace(p.Feedback) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'feedback' is required")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	goal, err := d.kanban.ReplanGoal(ctx, p.GoalID, p.Feedback, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	goalID := goal.ID
	rev := goal.Revision

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   nil,
			GoalID:   &goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   nil,
		GoalID:   &goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleGoalRetryTask(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.GoalTaskMutationParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.TaskID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'taskID' must be a valid UUID")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	task, err := d.kanban.RetryGoalTask(ctx, p.TaskID, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	taskID := task.ID
	rev := task.Revision
	goalID := task.GoalID

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
			GoalID:   goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   &taskID,
		GoalID:   goalID,
		Revision: &rev,
	})
}

func (d *Dispatcher) handleGoalSkipTask(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.GoalTaskMutationParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.TaskID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'taskID' must be a valid UUID")
	}
	if p.ExpectedRevision < 0 {
		return replyErr(wire.InvalidParams, "Invalid params: 'expectedRevision' must be non-negative")
	}
	if !isValidUUID(p.ActionID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' must be a valid UUID")
	}

	if d.kanbanActions != nil {
		if prev, ok := d.kanbanActions.get(p.ActionID); ok {
			return replySuccess(wire.KanbanTaskMutationResult{
				ActionID: prev.ActionID,
				TaskID:   prev.TaskID,
				GoalID:   prev.GoalID,
				Revision: prev.Revision,
			})
		}
	}

	if d.kanban == nil {
		return replyErr(wire.ErrCodeOperationRejected, "kanban service unavailable")
	}

	task, err := d.kanban.SkipGoalTask(ctx, p.TaskID, p.ExpectedRevision)
	if err != nil {
		return mapKanbanError(err, replyErr, replyWithBackendErr)
	}

	taskID := task.ID
	rev := task.Revision
	goalID := task.GoalID

	if d.kanbanActions != nil {
		d.kanbanActions.record(wire.ActionStatusResult{
			ActionID: p.ActionID,
			State:    wire.ActionStateAccepted,
			TaskID:   &taskID,
			GoalID:   goalID,
			Revision: &rev,
		})
	}

	d.NotifyKanbanChanged()

	return replySuccess(wire.KanbanTaskMutationResult{
		ActionID: p.ActionID,
		TaskID:   &taskID,
		GoalID:   goalID,
		Revision: &rev,
	})
}
