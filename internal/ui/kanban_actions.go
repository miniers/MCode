package mcodeui

import (
	"fmt"
	"strings"

	"mcode/internal/kanban"
)

// kanbanTaskBlockReason 从任务已有字段拼出阻塞原因，供「启动」按钮的提示展示。
func kanbanTaskBlockReason(task KanbanTask) string {
	var parts []string
	if task.Blocked && len(task.DependsOn) > 0 {
		parts = append(parts, fmt.Sprintf("等待前置任务 %d 项完成", len(task.DependsOn)))
	}
	if task.LastFailureReason != "" {
		parts = append(parts, task.LastFailureReason)
	}
	for _, flag := range task.AttentionFlags {
		// 后端下发的关注标志是 snake_case 标识，这里转成中文
		switch flag {
		case "failed":
			parts = append(parts, "上次执行失败")
		case "stuck":
			parts = append(parts, "会话长时间无响应（卡死）")
		case "awaiting_approval":
			parts = append(parts, "等待审批")
		case "awaiting_input":
			parts = append(parts, "等待你的补充信息")
		case "needs_review":
			parts = append(parts, "等待审查")
		default:
			parts = append(parts, flag)
		}
	}
	return strings.Join(parts, "；")
}

// moveTargetOption 是移动菜单里的一个可选目标状态。
type moveTargetOption struct {
	status string
	label  string
	key    string
}

type kanbanTaskActions struct {
	primary string
	targets map[string]string
	menu    []moveTargetOption
}

// kanbanActions 为拖放和菜单计算相同的目标意图。
func kanbanActions(task KanbanTask, busy bool) kanbanTaskActions {
	actions := kanbanTaskActions{targets: make(map[string]string)}
	if busy {
		return actions
	}
	if task.CanStart {
		actions.primary = "开始"
	} else if task.CanReview || task.Status == "review" {
		actions.primary = "审阅"
	} else if kanbanDisplayColumn(task) == "needs_you" {
		actions.primary = "处理"
	}
	if task.CanStart && !task.Blocked && task.Status == "todo" && kanbanDisplayColumn(task) != "in_progress" {
		actions.targets["in_progress"] = "start"
	}
	if task.CanMove {
		for _, opt := range moveTargetOptions {
			if opt.status == "rejected" || opt.status == kanbanDisplayColumn(task) || !kanban.CanTransition(kanban.TaskStatus(task.Status), kanban.TaskStatus(opt.status)) {
				continue
			}
			intent := "move"
			if task.Status == "review" && opt.status == "done" {
				intent = "review"
			}
			actions.targets[opt.status] = intent
			actions.menu = append(actions.menu, opt)
		}
	}
	if actions.targets["in_progress"] != "" {
		actions.menu = append(actions.menu, moveTargetOption{status: "in_progress", label: "进行中", key: "kanban-move-in-progress"})
	}
	return actions
}

func (s *chatState) performKanbanTarget(task KanbanTask, target string) {
	ks := getKanbanState(s)
	switch kanbanActions(task, kanbanTaskBusy(ks, task.ID)).targets[target] {
	case "start":
		s.startTask(task.ID, task.Revision)
	case "move":
		s.moveTask(task.ID, target, task.Revision)
	case "review":
		s.openTaskDetail(task)
	}
}

// 可移动的目标状态选项
var moveTargetOptions = []moveTargetOption{
	{status: "todo", label: "待办", key: "kanban-move-todo"},
	{status: "review", label: "审查", key: "kanban-move-review"},
	{status: "done", label: "完成", key: "kanban-move-done"},
	{status: "rejected", label: "驳回", key: "kanban-move-rejected"},
}

// moveTargetsFor 返回 from 状态可选的移动目标：跳过当前状态所在列，并按后端
// kanban.CanTransition 过滤，避免给 done / rejected 这类只剩「待办」的卡片提供
// 必定被后端拒绝的目标（否则选中只会得到「移动任务失败」）。
func moveTargetsFor(from string) []moveTargetOption {
	out := make([]moveTargetOption, 0, len(moveTargetOptions))
	for _, opt := range moveTargetOptions {
		if opt.status == from {
			continue
		}
		if !kanban.CanTransition(kanban.TaskStatus(from), kanban.TaskStatus(opt.status)) {
			continue
		}
		out = append(out, opt)
	}
	return out
}
