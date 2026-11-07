package mcp

import (
	"time"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// kanbanCardResult 保持源工具的卡片字段，不把存储层 JSON 当作 MCP 契约。
func kanbanCardResult(tc ToolContext, task store.Task, key string, payload map[string]any, write bool) ToolResult {
	board, err := tc.Server.opts.Kanban.Board(tc.Context)
	if err != nil {
		if write {
			return toolError(CodePartialFailure, "Task was saved but its card could not be read: "+err.Error(), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	all := make(map[string]store.Task, len(board.Tasks))
	derived := kanban.Derived{}
	for _, view := range board.Tasks {
		all[view.Task.ID] = view.Task
		if view.Task.ID == task.ID {
			derived = view.Derived
		}
	}
	if derived.DisplayColumn == "" {
		derived = kanban.Derive(task, all, kanban.RuntimeState{})
	}
	blocking := make([]string, 0)
	deps := append([]string{}, task.DependsOn...)
	for _, id := range deps {
		dep, ok := all[id]
		if !ok {
			blocking = append(blocking, id)
		} else if dep.Status != string(kanban.TaskStatusDone) {
			blocking = append(blocking, dep.Title)
		}
	}
	card := map[string]any{
		"id": task.ID, "title": task.Title, "notes": task.Notes,
		"column": derived.DisplayColumn, "status": task.Status, "revision": task.Revision,
		"attention_flags": derived.AttentionFlags, "is_blocked": derived.Blocked,
		"blocking_tasks": blocking, "depends_on_task_ids": deps,
		"created_at": task.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	for name, value := range map[string]*string{
		"project_id": task.ProjectID, "thread_id": task.ThreadID,
		"goal_id": task.GoalID, "goal_prompt": task.GoalPrompt,
		"review_summary": task.ReviewSummary, "review_status": task.ReviewStatus,
	} {
		if value != nil {
			card[name] = *value
		}
	}
	if task.GoalID != nil {
		ids, err := tc.Server.opts.Store.ListGoalTaskIDs(tc.Context, *task.GoalID)
		if err != nil {
			if write {
				return toolError(CodePartialFailure, "Task was saved but its goal order could not be read.", false)
			}
			return toolError(CodeInternal, "Could not read goal task order.", true)
		}
		for position, id := range ids {
			if id == task.ID {
				card["goal_sort_order"] = position
				break
			}
		}
	}
	payload[key] = card
	return toolSuccess(payload)
}
