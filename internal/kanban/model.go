package kanban

// TaskStatus 定义看板任务状态枚举。
type TaskStatus string

const (
	TaskStatusTodo          TaskStatus = "todo"
	TaskStatusInProgress    TaskStatus = "in_progress"
	TaskStatusNeedsApproval TaskStatus = "needs_approval"
	TaskStatusReview        TaskStatus = "review"
	TaskStatusDone          TaskStatus = "done"
	TaskStatusRejected      TaskStatus = "rejected"
	TaskStatusWaiting       TaskStatus = "waiting"
	TaskStatusFailed        TaskStatus = "failed"
	TaskStatusCancelled     TaskStatus = "cancelled"
)

// Valid 校验 TaskStatus 是否有效。
func (s TaskStatus) Valid() bool {
	switch s {
	case TaskStatusTodo, TaskStatusInProgress, TaskStatusNeedsApproval,
		TaskStatusReview, TaskStatusDone, TaskStatusRejected, TaskStatusWaiting, TaskStatusFailed, TaskStatusCancelled:
		return true
	default:
		return false
	}
}

// Priority 定义任务优先级枚举。
type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityMedium Priority = "medium"
	PriorityLow    Priority = "low"
	PriorityNone   Priority = "none"
)

// Valid 校验 Priority 是否有效。
func (p Priority) Valid() bool {
	switch p {
	case PriorityUrgent, PriorityHigh, PriorityMedium, PriorityLow, PriorityNone:
		return true
	default:
		return false
	}
}

// DisplayColumn 定义看板卡片视觉展示列。
type DisplayColumn string

const (
	DisplayColumnTodo       DisplayColumn = "todo"
	DisplayColumnInProgress DisplayColumn = "in_progress"
	DisplayColumnNeedsYou   DisplayColumn = "needs_you"
	DisplayColumnReview     DisplayColumn = "review"
	DisplayColumnDone       DisplayColumn = "done"
)

// Valid 校验 DisplayColumn 是否有效。
func (c DisplayColumn) Valid() bool {
	switch c {
	case DisplayColumnTodo, DisplayColumnInProgress, DisplayColumnNeedsYou,
		DisplayColumnReview, DisplayColumnDone:
		return true
	default:
		return false
	}
}

// GoalStatus 定义目标生命周期状态枚举。
type GoalStatus string

const (
	GoalStatusPlanning   GoalStatus = "planning"
	GoalStatusPlanFailed GoalStatus = "plan_failed"
	GoalStatusPlanReview GoalStatus = "plan_review"
	GoalStatusRunning    GoalStatus = "running"
	GoalStatusPaused     GoalStatus = "paused"
	GoalStatusCompleted  GoalStatus = "completed"
	GoalStatusCancelled  GoalStatus = "cancelled"
)

// Valid 校验 GoalStatus 是否有效。
func (g GoalStatus) Valid() bool {
	switch g {
	case GoalStatusPlanning, GoalStatusPlanFailed, GoalStatusPlanReview,
		GoalStatusRunning, GoalStatusPaused, GoalStatusCompleted, GoalStatusCancelled:
		return true
	default:
		return false
	}
}

// 关注标志常量，对应前端与线协议 attentionFlags。
const (
	AttentionFlagFailed           = "failed"
	AttentionFlagStuck            = "stuck"
	AttentionFlagAwaitingApproval = "awaiting_approval"
	AttentionFlagAwaitingInput    = "awaiting_input"
	AttentionFlagNeedsReview      = "needs_review"
)

// CanTransition 校验两个任务状态间的流转是否合法。
// 同状态视为合法（幂等）。根据既有测试与调用方依赖，todo 允许流转至 review 供送审。
func CanTransition(from, to TaskStatus) bool {
	if !from.Valid() || !to.Valid() {
		return false
	}
	if from == to {
		return true
	}

	switch from {
	case TaskStatusTodo:
		switch to {
		case TaskStatusInProgress, TaskStatusDone, TaskStatusRejected, TaskStatusReview, TaskStatusCancelled:
			return true
		default:
			return false
		}
	case TaskStatusInProgress:
		switch to {
		case TaskStatusTodo, TaskStatusNeedsApproval, TaskStatusReview, TaskStatusDone, TaskStatusRejected, TaskStatusWaiting, TaskStatusFailed, TaskStatusCancelled:
			return true
		default:
			return false
		}
	case TaskStatusNeedsApproval:
		switch to {
		case TaskStatusTodo, TaskStatusInProgress, TaskStatusReview, TaskStatusDone, TaskStatusRejected, TaskStatusCancelled, TaskStatusWaiting, TaskStatusFailed:
			return true
		default:
			return false
		}
	case TaskStatusReview:
		switch to {
		case TaskStatusTodo, TaskStatusInProgress, TaskStatusDone, TaskStatusRejected:
			return true
		default:
			return false
		}
	case TaskStatusWaiting:
		return to == TaskStatusTodo || to == TaskStatusCancelled || to == TaskStatusNeedsApproval
	case TaskStatusFailed, TaskStatusCancelled:
		return to == TaskStatusTodo
	case TaskStatusDone:
		return to == TaskStatusTodo
	case TaskStatusRejected:
		return to == TaskStatusTodo
	default:
		return false
	}
}
