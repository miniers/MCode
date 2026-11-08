package kanban

import (
	"sort"
	"time"

	"mcode/internal/store"
)

// Stuck 告警与卡死阈值。
const (
	StuckWarnInterval = 20 * time.Minute
	StuckHardInterval = 40 * time.Minute
)

// RuntimeState 描述线程运行时信号。
type RuntimeState struct {
	Running          bool      // 线程是否正在执行
	ThreadFailed     bool      // 线程是否失败
	AwaitingApproval bool      // 是否等待用户审批
	AwaitingInput    bool      // 是否等待用户输入
	SessionIsDead    bool      // 会话是否已终止
	Heartbeat        time.Time // 最后有效活动/心跳时间戳
	Now              time.Time // 当前参考时间，为零值时自动取当前系统时间
	IsHardStuck      bool      // 显式指定严重卡死（>= 40分钟）
	IsWarnStuck      bool      // 显式指定告警卡死（20 ~ 40分钟）
	IsOptimistic     bool      // 乐观派发标记（调度中仍为 todo 时推导为 in_progress）
}

// Derived 包含看板卡片派生得到的视觉列、阻塞状态、操作权限和关注标志。
type Derived struct {
	DisplayColumn  DisplayColumn `json:"display_column"`
	Blocked        bool          `json:"blocked"`
	CanStart       bool          `json:"can_start"`
	CanMove        bool          `json:"can_move"`
	CanReview      bool          `json:"can_review"`
	CanApprove     bool          `json:"can_approve"`
	AttentionFlags []string      `json:"attention_flags"`
}

// Derive 根据持久化任务、上下文任务字典和运行时状态派生看板属性。
func Derive(task store.Task, all map[string]store.Task, rt RuntimeState) Derived {
	flags, isHardStuck := deriveAttentionFlags(task, rt)
	col := deriveDisplayColumn(task, flags, isHardStuck, rt.IsOptimistic)
	blocked := isBlocked(task, all)

	canStart := task.Status == string(TaskStatusTodo) && !blocked && !rt.Running && !rt.IsOptimistic
	canMove := !rt.Running && !rt.IsOptimistic && task.Status != string(TaskStatusInProgress) && task.Status != string(TaskStatusNeedsApproval)
	canReview := task.Status == string(TaskStatusReview) && !rt.Running
	canApprove := task.Status == string(TaskStatusReview) && !rt.Running

	return Derived{
		DisplayColumn:  col,
		Blocked:        blocked,
		CanStart:       canStart,
		CanMove:        canMove,
		CanReview:      canReview,
		CanApprove:     canApprove,
		AttentionFlags: flags,
	}
}

// deriveAttentionFlags 派生任务的紧急关注标志及是否处于严重卡死状态。
func deriveAttentionFlags(task store.Task, rt RuntimeState) ([]string, bool) {
	flags := make([]string, 0)

	// 1. 失败状态（线程失败或任务被驳回）
	if rt.ThreadFailed || task.Status == string(TaskStatusRejected) || task.Status == string(TaskStatusFailed) {
		flags = append(flags, AttentionFlagFailed)
	}

	// 2. 会话死亡且有未决请求时转化为 Failed
	if rt.SessionIsDead && (rt.AwaitingApproval || rt.AwaitingInput) {
		if !contains(flags, AttentionFlagFailed) {
			flags = append(flags, AttentionFlagFailed)
		}
	} else if !rt.SessionIsDead {
		// 3. 正常等待用户审批或输入
		if rt.AwaitingApproval || task.Status == string(TaskStatusNeedsApproval) {
			flags = append(flags, AttentionFlagAwaitingApproval)
		}
		if rt.AwaitingInput {
			flags = append(flags, AttentionFlagAwaitingInput)
		}
	}

	// 4. 卡死检测（仅针对 in_progress 或 needs_approval）
	isStuckCandidate := task.Status == string(TaskStatusInProgress) || task.Status == string(TaskStatusNeedsApproval)
	isHard := false
	isWarn := false

	if isStuckCandidate {
		if rt.IsHardStuck {
			isHard = true
		}
		if rt.IsWarnStuck {
			isWarn = true
		}
		if !isHard && !isWarn && !rt.Heartbeat.IsZero() {
			now := rt.Now
			if now.IsZero() {
				now = time.Now()
			}
			if now.After(rt.Heartbeat) {
				age := now.Sub(rt.Heartbeat)
				if age >= StuckHardInterval {
					isHard = true
				} else if age >= StuckWarnInterval {
					isWarn = true
				}
			}
		}
		if isHard || isWarn {
			flags = append(flags, AttentionFlagStuck)
		}
	}

	// 5. 待评审
	if task.Status == string(TaskStatusReview) {
		flags = append(flags, AttentionFlagNeedsReview)
	}

	return flags, isHard
}

// deriveDisplayColumn 映射任务至看板 5 列。
func deriveDisplayColumn(task store.Task, flags []string, isHardStuck bool, isOptimistic bool) DisplayColumn {
	if task.Status == string(TaskStatusFailed) {
		return DisplayColumnTodo
	}
	if task.Status == string(TaskStatusCancelled) {
		return DisplayColumnDone
	}
	// Needs You 具有最高优先级：失败、待用户审批/输入、严重卡死均提升至 Needs You
	if contains(flags, AttentionFlagFailed) ||
		contains(flags, AttentionFlagAwaitingApproval) ||
		contains(flags, AttentionFlagAwaitingInput) ||
		isHardStuck {
		return DisplayColumnNeedsYou
	}

	if task.Status == string(TaskStatusReview) {
		return DisplayColumnReview
	}

	if task.Status == string(TaskStatusDone) {
		return DisplayColumnDone
	}

	if task.Status == string(TaskStatusInProgress) || task.Status == string(TaskStatusWaiting) || (isOptimistic && task.Status == string(TaskStatusTodo)) {
		return DisplayColumnInProgress
	}

	return DisplayColumnTodo
}

// isBlocked 判断任务是否被前置依赖阻塞。
func isBlocked(task store.Task, all map[string]store.Task) bool {
	if invalidDependencies(task, all) {
		return true
	}
	for _, depID := range task.DependsOn {
		if all[depID].Status != string(TaskStatusDone) {
			return true
		}
	}
	return false
}

func sameProject(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// invalidDependencies 校验完整依赖链，使用传入任务而非字典中可能过期的副本。
func invalidDependencies(task store.Task, all map[string]store.Task) bool {
	visiting := make(map[string]bool)
	visited := make(map[string]bool)

	var dfs func(curr store.Task) bool
	dfs = func(curr store.Task) bool {
		visiting[curr.ID] = true
		for _, depID := range curr.DependsOn {
			if depID == curr.ID || visiting[depID] {
				return true
			}
			dep, ok := all[depID]
			if !ok || !sameProject(task.ProjectID, dep.ProjectID) {
				return true
			}
			if !visited[depID] && dfs(dep) {
				return true
			}
		}
		visiting[curr.ID] = false
		visited[curr.ID] = true
		return false
	}

	return dfs(task)
}

// DetectCycle 检测任务集合中的依赖成环情况。
// 若存在环，返回成环路径上的任务 ID 列表；若无环返回 nil。
func DetectCycle(tasks map[string]store.Task) []string {
	ids := make([]string, 0, len(tasks))
	for id := range tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	visited := make(map[string]bool)
	inStack := make(map[string]int)
	stack := make([]string, 0)

	var cyclePath []string

	var dfs func(id string) bool
	dfs = func(id string) bool {
		inStack[id] = len(stack)
		stack = append(stack, id)

		task, ok := tasks[id]
		if ok {
			// 为保证确定性，依赖项排序后遍历
			deps := make([]string, len(task.DependsOn))
			copy(deps, task.DependsOn)
			sort.Strings(deps)

			for _, depID := range deps {
				if depID == id {
					cyclePath = []string{id, id}
					return true
				}
				if pos, ok := inStack[depID]; ok {
					// 发现环：从 pos 到当前栈底，再闭环到 depID
					cyclePath = append([]string{}, stack[pos:]...)
					cyclePath = append(cyclePath, depID)
					return true
				}
				if !visited[depID] {
					if dfs(depID) {
						return true
					}
				}
			}
		}

		delete(inStack, id)
		stack = stack[:len(stack)-1]
		visited[id] = true
		return false
	}

	for _, id := range ids {
		if !visited[id] {
			if dfs(id) {
				return cyclePath
			}
		}
	}

	return nil
}

func contains(slice []string, val string) bool {
	for _, item := range slice {
		if item == val {
			return true
		}
	}
	return false
}
