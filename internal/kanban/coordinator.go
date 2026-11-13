package kanban

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"mcode/internal/git"
	"mcode/internal/store"
)

// InteractionModeSetter 定义可切换会话交互模式（plan/build）的可选驱动能力。
type InteractionModeSetter interface {
	SetInteractionMode(ctx context.Context, threadID, mode string) error
}

// CoordinatorPreface 为目标计划会话生成协调员上下文说明（D-KB-017）。
// 规划完成后（plan_review/running/paused/completed），计划会话转为目标的协调员，
// 每次提示词前注入目标状态、进度、任务清单与 MCP 协调规则。
// 目标不存在、仍在规划、规划失败或已取消时返回空字符串。
func (s *Service) CoordinatorPreface(ctx context.Context, threadID string) string {
	if s == nil || s.opts.Store == nil {
		return ""
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ""
	}

	goals, err := s.opts.Store.ListGoals(ctx, "")
	if err != nil {
		return ""
	}
	var goal *store.Goal
	for i := range goals {
		if goals[i].PlanThreadID != nil && *goals[i].PlanThreadID == threadID {
			g := goals[i]
			goal = &g
			break
		}
	}
	if goal == nil {
		return ""
	}
	switch goal.Status {
	case string(GoalStatusPlanning), string(GoalStatusPlanFailed), string(GoalStatusCancelled):
		return ""
	}

	taskIDs, err := s.opts.Store.ListGoalTaskIDs(ctx, goal.ID)
	if err != nil {
		return ""
	}
	tasks := make([]store.Task, 0, len(taskIDs))
	for _, id := range taskIDs {
		if t, err := s.opts.Store.GetTask(ctx, id); err == nil {
			tasks = append(tasks, t)
		}
	}

	progress := s.goalProgress(ctx, tasks)
	stalled, _ := progress["stalled"].(bool)

	var b strings.Builder
	b.WriteString("[目标协调员上下文]\n")
	b.WriteString("目标 ID: " + goal.ID + "\n")
	b.WriteString("标题: " + goal.Title + "\n")
	statusLine := "状态: " + goal.Status
	if goal.StatusReason != nil {
		if reason := strings.TrimSpace(*goal.StatusReason); reason != "" {
			statusLine += "（" + reason + "）"
		}
	}
	b.WriteString(statusLine + "\n")
	b.WriteString(fmt.Sprintf("进度: 总数 %d，完成 %d，运行中 %d，可开始 %d，阻塞 %d，待你处理 %d，失败 %d\n",
		coordinatorProgressInt(progress, "total"),
		coordinatorProgressInt(progress, "done"),
		coordinatorProgressInt(progress, "running"),
		coordinatorProgressInt(progress, "ready"),
		coordinatorProgressInt(progress, "blocked"),
		coordinatorProgressInt(progress, "waitingOnYou"),
		coordinatorProgressInt(progress, "failed"),
	))
	if stalled {
		b.WriteString("停滞: 是\n")
	}
	if len(tasks) > 0 {
		b.WriteString("任务:\n")
		limit := len(tasks)
		if limit > 30 {
			limit = 30
		}
		for _, t := range tasks[:limit] {
			b.WriteString(fmt.Sprintf("- [%s] %q 状态: %s\n", t.ID, t.Title, t.Status))
		}
	}
	b.WriteString("\n")
	b.WriteString("角色说明: 你是该目标的协调员（项目经理），负责跟进进度、协调任务并回答进度问题；不要自己写代码或修改项目文件，具体工作由目标下的任务卡片执行。\n")
	b.WriteString("协调规则: 用 kanban_read_board、kanban_goal_status、kanban_read_card 等工具读取看板与目标；用 kanban_create_draft（可带 goal_id 与 depends_on_task_ids）补充任务；用 kanban_update_card、kanban_move_card、kanban_start_task 调整既有卡片。")
	return b.String()
}

// IsTaskWorktreeDir 判断给定目录是否位于任务 worktree 根目录之下（D-KB-019）。
// worktree 根目录本身不算任务执行目录；路径两侧都先尝试解析符号链接。
func (s *Service) IsTaskWorktreeDir(dir string) bool {
	if s == nil || strings.TrimSpace(dir) == "" || strings.TrimSpace(s.opts.WorktreeDir) == "" {
		return false
	}
	root := s.opts.WorktreeDir
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	root = filepath.Clean(root)

	target := dir
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		target = resolved
	}
	target = filepath.Clean(target)

	if git.IsProjectWorktreePath(target) {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// coordinatorProgressInt 从进度 map 读取整数字段，缺失或类型不符时返回 0。
func coordinatorProgressInt(progress map[string]any, key string) int {
	switch v := progress[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}
