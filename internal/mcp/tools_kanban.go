package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"mcode/internal/kanban"
	"mcode/internal/store"
)

// kanbanScope 返回调用线程的看板角色与自身任务 ID；评审线程的写操作一律以只读拒绝。
func kanbanScope(tc ToolContext) (threadRole, string, *ToolResult) {
	role, ownTaskID := tc.Server.threadRole(tc.Context, tc.Project.ID)
	if role == threadRoleReview {
		res := toolError(CodeReadOnly, "Kanban is read-only in this conversation.", false,
			"A review thread can look at the board, but changes to it have to come from the thread that created this one.")
		return role, ownTaskID, &res
	}
	return role, ownTaskID, nil
}

// kanbanWriteGuard 返回仅需只读检查的写工具的守卫结果；评审线程以外返回 nil。
func kanbanWriteGuard(tc ToolContext) *ToolResult {
	_, _, guard := kanbanScope(tc)
	return guard
}

// kanbanSelfTargetError 拒绝线程对自身卡片的生命周期操作；元数据更新与记录进度不受限制。
func kanbanSelfTargetError(role threadRole, ownTaskID, taskID string) *ToolResult {
	if role == threadRoleOrdinary || ownTaskID == "" || strings.TrimSpace(taskID) != ownTaskID {
		return nil
	}
	res := toolError(CodeSelfTarget, "A thread cannot start, stop, review, delete or settle its own card.", false,
		"Those actions belong to the thread that created this card. Updating your own card's progress and notes is still allowed.")
	return &res
}

// kanbanDisplayColumn 返回任务当前派生的看板列；查询失败时返回空串。
func kanbanDisplayColumn(tc ToolContext, taskID string) kanban.DisplayColumn {
	if tc.Server.opts.Kanban == nil {
		return ""
	}
	board, err := tc.Server.opts.Kanban.Board(tc.Context)
	if err != nil {
		return ""
	}
	for _, view := range board.Tasks {
		if view.Task.ID == taskID {
			return view.Derived.DisplayColumn
		}
	}
	return ""
}

// kanbanServiceError 把看板服务/存储层的错误映射成能指导模型的业务错误码：
// 版本冲突提示重读卡片，状态非法属于不可重试的参数问题，存储故障才算内部错误。
func kanbanServiceError(prefix string, err error) ToolResult {
	switch {
	case errors.Is(err, store.ErrRevisionConflict):
		return toolError(CodeConflict, fmt.Sprintf("%s: %v", prefix, err), true,
			"The card changed in the meantime: fetch it again with kanban_read_card and retry using the new revision.")
	case errors.Is(err, kanban.ErrInvalidTransition):
		return toolError(CodeInvalidArguments, fmt.Sprintf("%s: %v", prefix, err), false,
			"The card's current status does not permit this; check it with kanban_read_card before trying again.")
	case errors.Is(err, store.ErrNotFound):
		return toolError(CodeNotFound, fmt.Sprintf("%s: %v", prefix, err), false,
			"That card is gone; look at the board again before doing anything with it.")
	}
	return toolError(CodeInternal, fmt.Sprintf("%s: %v", prefix, err), true)
}

func (r *Registry) registerKanbanTools(s *Server) {
	r.Register(ToolDefinition{
		Name:        "kanban_read_board",
		Description: "Return the project's Kanban board. Cards are grouped into the columns todo, in_progress, needs_you, review and done, and each one carries its id, title, notes, status, column, revision, review status, attention flags (awaiting_approval, awaiting_input, failed, stuck, needs_review), the tasks blocking it, timestamps and its goal. The column and the attention flags are computed from the card's live state, not stored.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project_id": map[string]any{
					"type":        "string",
					"description": "Optional project UUID. If given, it has to be the project of the current conversation.",
				},
			},
			"additionalProperties": false,
		},
		IsWrite:    false,
		Capability: CapKanban,
		Handler:    handleKanbanReadBoard,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_read_card",
		Description: "Return one card by its task ID, with its column, status, notes, revision, review status, attention flags, blocking tasks and timestamps. A small, fast lookup.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID to read.",
				},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		IsWrite:    false,
		Capability: CapKanban,
		Handler:    handleKanbanReadCard,
	})

	// kanban_task_diff 只读：把任务 worktree 相对 HEAD 的 diff 交给模型，便于评审与复核。
	r.Register(ToolDefinition{
		Name:        kanbanTaskDiffToolName,
		Description: "读取看板任务在其 worktree 中相对 HEAD 的代码变更（只读）。没有变更时说明没有变更；超过 200 KB 时截断并在末尾标注（diff 已截断）。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID to read the diff of.",
				},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		IsWrite:    false,
		Capability: CapKanban,
		Handler:    handleKanbanTaskDiff,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_list_tasks",
		Description: "List the tasks of this conversation's project, including each one's status and dependencies.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"project_id": map[string]any{
					"type":        "string",
					"description": "Optional project UUID.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"todo", "in_progress", "needs_approval", "review", "done", "rejected", "waiting", "failed", "cancelled"},
					"description": "Optional status filter.",
				},
			},
			"additionalProperties": false,
		},
		IsWrite:    false,
		Capability: CapKanban,
		Handler:    handleKanbanListTasks,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_create_task",
		Description: "Add a task to the board and start working on it right away. request_id is mandatory: repeating the call with the same request_id and arguments returns the first result rather than a second task.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Short title for the task.",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Mandatory idempotency key. A repeated call with the same key and arguments returns the original result instead of a duplicate task.",
				},
				"requestId": map[string]any{
					"type":        "string",
					"description": "Alias of request_id.",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "Optional instructions the task's first turn starts from.",
				},
				"project_id": map[string]any{
					"type":        "string",
					"description": "Optional project UUID.",
				},
				"depends_on_task_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional UUIDs of tasks that must finish first. If any of them is still open, the card is placed in To Do and does not start yet.",
				},
			},
			"required":             []string{"title", "request_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanCreateTask,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_move_card",
		Description: "Change where a card is in its lifecycle. 'inProgress' starts the card, or resumes it; 'done' asks its running turn to wrap up or stop. Cards in Needs You are waiting for the user and cannot be moved this way.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID to move.",
				},
				"target": map[string]any{
					"type":        "string",
					"enum":        []string{"inProgress", "done"},
					"description": "Where to move the card: 'inProgress' or 'done'.",
				},
			},
			"required":             []string{"task_id", "target"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanMoveCard,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_start_task",
		Description: "Launch the agent for a task that is still in To Do. Include request_id so that a retried call with the same arguments returns the first result.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "UUID of the task to launch.",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Optional idempotency key: repeating the call with the same key and arguments returns the first result.",
				},
				"requestId": map[string]any{
					"type":        "string",
					"description": "Alias of request_id.",
				},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanStartTask,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_review_task",
		Description: "Decide on a task that is waiting for review: accept, reject or requestReview. Include request_id so that a retried call with the same arguments returns the first result.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "UUID of the task being reviewed.",
				},
				"decision": map[string]any{
					"type":        "string",
					"enum":        []string{"accept", "reject", "requestReview"},
					"description": "The review decision.",
				},
				"feedback": map[string]any{
					"type":        "string",
					"description": "Optional feedback.",
				},
				"user_confirmed": map[string]any{
					"type":        "boolean",
					"description": "Set to true only when the user has explicitly agreed in the conversation. Needed to reject a task that was already rejected once, or to accept a task whose acceptance criteria contain [你验].",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Optional idempotency key: repeating the call with the same key and arguments returns the first result.",
				},
				"requestId": map[string]any{
					"type":        "string",
					"description": "Alias of request_id.",
				},
			},
			"required":             []string{"task_id", "decision"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanReviewTask,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_create_goal",
		Description: "Turn what was agreed in this conversation into a Goal for the project. A planner agent breaks the goal into Kanban tasks and runs them; unless told otherwise, the user approves the plan first. The prompt must stand on its own: state the goal, the constraints and the acceptance criteria. Available in ordinary chats only, not in task, review or goal-planning threads.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "The goal written so that it stands on its own: what was agreed here, the constraints, and how to tell it is done.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "Optional goal title; without it a title is taken from the prompt.",
				},
				"preview_plan": map[string]any{
					"type":        "boolean",
					"description": "Whether the user must approve the plan before its tasks start (default true).",
				},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanCreateGoal,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_goal_status",
		Description: "Show where a goal stands: its overall status, how many tasks are done, whether it has stalled, and its tasks in goalSortOrder.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id": map[string]any{
					"type":        "string",
					"description": "Goal UUID to inspect.",
				},
			},
			"required":             []string{"goal_id"},
			"additionalProperties": false,
		},
		IsWrite:    false,
		Capability: CapKanban,
		Handler:    handleKanbanGoalStatus,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_create_draft",
		Description: "Put a new task into To Do without starting it. goal_id attaches it to a goal and depends_on_task_ids lists tasks that must finish first. Include request_id so that a retried call with the same arguments returns the first result.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title": map[string]any{
					"type":        "string",
					"description": "Short title for the new task.",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "Optional notes or description.",
				},
				"project_id": map[string]any{
					"type":        "string",
					"description": "Optional project UUID.",
				},
				"goal_id": map[string]any{
					"type":        "string",
					"description": "Optional UUID of the goal to attach the task to; that goal has to be plan_review, running or paused.",
				},
				"depends_on_task_ids": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional UUIDs of tasks that must finish first.",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Optional idempotency key: repeating the call with the same key and arguments returns the first result.",
				},
				"requestId": map[string]any{
					"type":        "string",
					"description": "Alias of request_id.",
				},
			},
			"required":             []string{"title"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanCreateDraft,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_update_card",
		Description: "Change a card's title or notes, or add to its notes with notes_append. This only edits the card's details; it never starts or settles work, which is what kanban_move_card is for. Include request_id so that a retried call with the same arguments returns the first result.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID to update.",
				},
				"title": map[string]any{
					"type":        "string",
					"description": "New task title.",
				},
				"notes": map[string]any{
					"type":        "string",
					"description": "Replace full notes content.",
				},
				"notes_append": map[string]any{
					"type":        "string",
					"description": "Text to add to the end of the notes, such as a milestone reached.",
				},
				"review_summary": map[string]any{
					"type":        "string",
					"description": "New summary of the work, shown to the user when they review it.",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Optional idempotency key: repeating the call with the same key and arguments returns the first result.",
				},
				"requestId": map[string]any{
					"type":        "string",
					"description": "Alias of request_id.",
				},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanUpdateCard,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_submit_result",
		Description: "提交任务结果：status 为 done、failed、waiting、delegated 或 needs_decision。",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{"type": "string", "enum": []string{"done", "failed", "waiting", "delegated", "needs_decision"}},
				"waiting_on": map[string]any{"type": "string"},
				"wait_kind": map[string]any{"type": "string", "enum": []string{"human", "machine"}},
				"wait_minutes": map[string]any{"type": "integer"},
				"wait_check": map[string]any{"type": "string"},
				"question": map[string]any{"type": "string"},
				"recommendation": map[string]any{"type": "string"},
				"options": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type":"string"}, "text": map[string]any{"type":"string"}}, "required": []string{"key", "text"}}},
				"followups": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type":"string"}, "body": map[string]any{"type":"string"}, "project": map[string]any{"type":"string"}, "priority": map[string]any{"type":"string"}, "agent": map[string]any{"type":"string"}}, "required": []string{"title"}}},
				"lessons": map[string]any{"type":"array", "items": map[string]any{"type":"string"}},
				"attachments": map[string]any{"type":"array", "items": map[string]any{"type":"string"}},
				"task_id": map[string]any{
					"type":        "string",
					"description": "UUID of the task this conversation is executing.",
				},
				"outcome": map[string]any{
					"type":        "string",
					"enum":        []string{"done", "blocked"},
					"description": "done when the work is finished, blocked when it cannot continue without a decision.",
				},
				"summary": map[string]any{
					"type":        "string",
					"description": "One or two sentences: what changed and whether acceptance passed, or where you are stuck and who has to decide.",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Optional idempotency key: repeating the call with the same key and arguments returns the first result.",
				},
				"requestId": map[string]any{
					"type":        "string",
					"description": "Alias of request_id.",
				},
			},
			"required":             []string{"task_id", "summary"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanSubmitResult,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_update_task",
		Description: "Older way to update a task: add progress with notes_append or hand in a review_summary. Status changes are restricted. Include request_id so that a retried call with the same arguments returns the first result.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{
					"type":        "string",
					"description": "UUID of the task to update.",
				},
				"status": map[string]any{
					"type":        "string",
					"enum":        []string{"in_progress", "done"},
					"description": "Optional new status; only in_progress and done are accepted, matching kanban_move_card.",
				},
				"notes_append": map[string]any{
					"type":        "string",
					"description": "Optional text to add to the end of the task's notes.",
				},
				"review_summary": map[string]any{
					"type":        "string",
					"description": "Optional summary of the work for the user's review.",
				},
				"request_id": map[string]any{
					"type":        "string",
					"description": "Optional idempotency key: repeating the call with the same key and arguments returns the first result.",
				},
				"requestId": map[string]any{
					"type":        "string",
					"description": "Alias of request_id.",
				},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanUpdateTask,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_delete_card",
		Description: "Remove a card for good. Not possible while an agent is working on it. Deleting a card that is already gone succeeds and says so.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{"type": "string", "description": "Optional idempotency key."},
				"requestId":  map[string]any{"type": "string", "description": "Alias of request_id."},
				"task_id": map[string]any{
					"type":        "string",
					"description": "Task UUID to delete.",
				},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanDeleteCard,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_delete_task",
		Description: "Delete a task from this project for the user. Deleting a task that is already gone succeeds and says so.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{"type": "string", "description": "Optional idempotency key."},
				"requestId":  map[string]any{"type": "string", "description": "Alias of request_id."},
				"task_id": map[string]any{
					"type":        "string",
					"description": "UUID of the task to delete.",
				},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanDeleteTask,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_set_goal",
		Description: "Give a task in this project a lasting objective (its Goal), or change the existing one.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"request_id": map[string]any{"type": "string", "description": "Optional idempotency key."},
				"requestId":  map[string]any{"type": "string", "description": "Alias of request_id."},
				"task_id": map[string]any{
					"type":        "string",
					"description": "UUID of the task the objective belongs to.",
				},
				"goal": map[string]any{
					"type":        "string",
					"description": "The objective text; an empty string removes it.",
				},
			},
			"required":             []string{"task_id", "goal"},
			"additionalProperties": false,
		},
		IsWrite:    true,
		Capability: CapKanban,
		Handler:    handleKanbanSetGoal,
	})

	r.Register(ToolDefinition{
		Name:        "kanban_read_goal",
		Description: "Fetch the full, unchanged text of a long goal that was stored outside the board, identified by goal_id and content_hash.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"goal_id": map[string]any{
					"type":        "string",
					"description": "UUID of the goal.",
				},
				"content_hash": map[string]any{
					"type":        "string",
					"description": "SHA-256 of the goal text, as 64 lowercase hex characters.",
				},
			},
			"required":             []string{"goal_id", "content_hash"},
			"additionalProperties": false,
		},
		IsWrite:    false,
		Capability: CapKanban,
		Handler:    handleKanbanReadGoal,
	})

	// 入口工具放在最后注册：此时全部类型化看板工具已在列表中，enum 可直接从它们收集。
	kanbanFamily.register(r)
}

func handleKanbanReadBoard(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}
	board, err := tc.Server.opts.Kanban.Board(tc.Context)
	if err != nil {
		return toolError(CodeInternal, err.Error(), true)
	}

	tasks := make([]kanban.TaskView, 0)
	for _, tv := range board.Tasks {
		if tv.Task.ProjectID != nil && *tv.Task.ProjectID == tc.Project.ID {
			tasks = append(tasks, tv)
		}
	}
	goals := make([]store.Goal, 0)
	for _, g := range board.Goals {
		if g.ProjectID == tc.Project.ID {
			goals = append(goals, g)
		}
	}

	columns := map[string][]any{
		"todo":        {},
		"in_progress": {},
		"needs_you":   {},
		"review":      {},
		"done":        {},
	}
	for _, tv := range tasks {
		col := string(tv.Derived.DisplayColumn)
		// 过渡期兼容旧列名 awaiting_you，归一化为 needs_you 后归入对应列。
		if col == "awaiting_you" {
			col = "needs_you"
		}
		if _, ok := columns[col]; !ok {
			columns[col] = make([]any, 0)
		}
		columns[col] = append(columns[col], tv)
	}

	payload := map[string]any{
		"project_id":  tc.Project.ID,
		"revision":    board.Revision,
		"total_count": len(tasks),
		"columns":     columns,
		"goals":       goals,
		"as_of":       time.Now().UTC().Format(time.RFC3339),
	}
	return toolSuccess(payload)
}

// kanbanTaskDiffToolName 是只读的代码变更查询工具名。
const kanbanTaskDiffToolName = "kanban_task_diff"

func handleKanbanReadCard(tc ToolContext, args map[string]any) ToolResult {
	taskID, ok := args["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)
	if tc.Server.opts.Store == nil {
		return toolError(CodeInternal, "Store is unavailable", false)
	}
	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}
	if tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}
	return kanbanCardResult(tc, task, "card", map[string]any{}, false)
}

// kanbanTaskDiffMaxBytes 是工具层对 diff 的第二道上限：看板服务已按 512 KiB 截断，
// 这里再收到 200 KB，避免完整 diff 白占模型上下文。
const kanbanTaskDiffMaxBytes = 200 * 1024

// kanbanTaskDiffTruncatedMark 是 diff 被截断后追加在末尾的一行说明。
const kanbanTaskDiffTruncatedMark = "（diff 已截断）"

// handleKanbanTaskDiff 返回任务 worktree 相对 HEAD 的 diff：只读，不改动 worktree。
func handleKanbanTaskDiff(tc ToolContext, args map[string]any) ToolResult {
	taskID, ok := args["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)
	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}
	// 先确认任务属于当前项目：看板服务对不存在或无 worktree 的任务都返回空 diff，
	// 不加这道校验就没法把“任务不存在”与“没有变更”区分开，也会漏掉跨项目读取。
	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}

	diff, truncated, err := tc.Server.opts.Kanban.TaskDiff(tc.Context, taskID)
	if err != nil {
		return kanbanServiceError("Reading the task diff failed", err)
	}
	return toolSuccess(kanbanTaskDiffText(diff, truncated))
}

// kanbanTaskDiffText 把服务层拿到的 diff 变成工具输出：空 diff 给一句结论，
// 过长时按 rune 边界截到 200 KB；服务层或本地任一环节截断都在末尾追加标记。
func kanbanTaskDiffText(diff string, truncated bool) string {
	if strings.TrimSpace(diff) == "" {
		return "该任务没有代码变更"
	}
	if len(diff) > kanbanTaskDiffMaxBytes {
		diff = truncateAtRuneBoundary(diff, kanbanTaskDiffMaxBytes)
		truncated = true
	}
	if truncated {
		diff = strings.TrimRight(diff, "\n") + "\n" + kanbanTaskDiffTruncatedMark
	}
	return diff
}

// truncateAtRuneBoundary 在 limit 字节内保留最长的合法 UTF-8 前缀，不切断多字节字符。
func truncateAtRuneBoundary(s string, limit int) string {
	if limit >= len(s) {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

func handleKanbanListTasks(tc ToolContext, args map[string]any) ToolResult {
	if tc.Server.opts.Store == nil {
		return toolError(CodeInternal, "Store is unavailable", false)
	}
	tasks, err := tc.Server.opts.Store.ListTasks(tc.Context, tc.Project.ID)
	if err != nil {
		return toolError(CodeInternal, err.Error(), true)
	}
	statusFilter, _ := args["status"].(string)
	if statusFilter != "" {
		if !kanban.TaskStatus(statusFilter).Valid() {
			return toolError(CodeInvalidArguments, fmt.Sprintf("Invalid 'status' filter: %q.", statusFilter), false)
		}
		filtered := make([]store.Task, 0, len(tasks))
		for _, t := range tasks {
			if t.Status == statusFilter {
				filtered = append(filtered, t)
			}
		}
		tasks = filtered
	}
	return toolSuccess(map[string]any{
		"count": len(tasks),
		"tasks": tasks,
	})
}

// parseDependsOnTaskIDs 解析 depends_on_task_ids 参数，校验依赖任务存在且属于当前项目。
func parseDependsOnTaskIDs(tc ToolContext, args map[string]any) ([]string, *ToolResult) {
	raw, ok := args["depends_on_task_ids"]
	if !ok {
		return nil, nil
	}
	list, isArray := raw.([]any)
	if !isArray {
		res := toolError(CodeInvalidArguments, "'depends_on_task_ids' has to be a list of task UUIDs.", false)
		return nil, &res
	}
	if tc.Server.opts.Store == nil {
		res := toolError(CodeInternal, "Store is unavailable", false)
		return nil, &res
	}

	deps := make([]string, 0, len(list))
	seen := make(map[string]bool, len(list))
	for _, item := range list {
		depID, isString := item.(string)
		if !isString || strings.TrimSpace(depID) == "" {
			res := toolError(CodeInvalidArguments, "'depends_on_task_ids' contains something that is not a valid task UUID.", false)
			return nil, &res
		}
		depID = strings.TrimSpace(depID)
		if seen[depID] {
			continue
		}
		dep, err := tc.Server.opts.Store.GetTask(tc.Context, depID)
		if err != nil {
			res := toolError(CodeInvalidArguments, fmt.Sprintf("Prerequisite task '%s' does not exist.", depID), false)
			return nil, &res
		}
		if dep.ProjectID == nil || *dep.ProjectID != tc.Project.ID {
			res := toolError(CodeInvalidArguments, fmt.Sprintf("Prerequisite task '%s' is in a different project.", depID), false)
			return nil, &res
		}
		seen[depID] = true
		deps = append(deps, depID)
	}
	return deps, nil
}

// taskBlocked 判断任务是否被未完成的前置依赖阻塞。
func taskBlocked(tc ToolContext, taskID string) (bool, error) {
	if tc.Server.opts.Store == nil {
		return false, fmt.Errorf("store is unavailable")
	}
	tasks, err := tc.Server.opts.Store.ListTasks(tc.Context, "")
	if err != nil {
		return false, err
	}
	all := make(map[string]store.Task, len(tasks))
	for _, t := range tasks {
		all[t.ID] = t
	}
	task, ok := all[taskID]
	if !ok {
		return false, fmt.Errorf("task %q not found", taskID)
	}
	return kanban.Derive(task, all, kanban.RuntimeState{}).Blocked, nil
}

func handleKanbanCreateTask(tc ToolContext, args map[string]any) ToolResult {
	if guard := kanbanWriteGuard(tc); guard != nil {
		return *guard
	}
	title, ok := args["title"].(string)
	if !ok || strings.TrimSpace(title) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'title'.", false)
	}
	title = strings.TrimSpace(title)

	reqID := extractIdempotencyKey(args)
	if reqID == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'request_id'.", false)
	}

	var notes *string
	if n, ok := args["notes"].(string); ok {
		notes = &n
	}

	deps, depErr := parseDependsOnTaskIDs(tc, args)
	if depErr != nil {
		return *depErr
	}

	if tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}

	in := kanban.CreateTaskInput{
		ProjectID: tc.Project.ID,
		Title:     title,
		Notes:     notes,
		DependsOn: deps,
	}

	created, err := tc.Server.opts.Kanban.CreateTask(tc.Context, in)
	if err != nil {
		return toolError(CodeInternal, "Failed to create task: "+err.Error(), true)
	}

	// 依赖未完成时只建卡、不启动，与前端禁用“开始”的行为一致。
	if blocked, blockedErr := taskBlocked(tc, created.ID); blockedErr == nil && blocked {
		return toolError(
			CodePartialFailure,
			"The card was added, but it has not started because its prerequisites are still open.",
			false,
			fmt.Sprintf("Don't create the card again; it already exists (task_id %s) and can be started once its prerequisites are done.", created.ID),
		)
	}

	started, startErr := tc.Server.opts.Kanban.StartTask(tc.Context, created.ID, created.Revision)
	if startErr != nil {
		return toolError(
			CodePartialFailure,
			fmt.Sprintf("The card was added, but starting it failed: %v", startErr),
			false,
			fmt.Sprintf("Don't create the card again; it already exists. Look at it with kanban_read_card (task_id %s).", created.ID),
		)
	}

	return toolSuccess(map[string]any{
		"success":    true,
		"status":     "task_dispatched",
		"request_id": reqID,
		"task":       started,
	})
}

func handleKanbanMoveCard(tc ToolContext, args map[string]any) ToolResult {
	role, ownTaskID, guard := kanbanScope(tc)
	if guard != nil {
		return *guard
	}
	taskID, _ := args["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)
	if selfTarget := kanbanSelfTargetError(role, ownTaskID, taskID); selfTarget != nil {
		return *selfTarget
	}
	target, _ := args["target"].(string)
	if target != "inProgress" && target != "done" {
		return toolError(CodeInvalidArguments, "target has to be 'inProgress' or 'done'.", false)
	}

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Service is unavailable", false)
	}

	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}

	if target == "inProgress" {
		if task.Status == string(kanban.TaskStatusInProgress) {
			return kanbanCardResult(tc, task, "card", map[string]any{
				"task_id":             taskID,
				"target":              target,
				"already_in_progress": true,
			}, true)
		}
		if blocked, blockedErr := taskBlocked(tc, taskID); blockedErr == nil && blocked {
			return toolError(
				CodeInvalidArguments,
				fmt.Sprintf("Task '%s' cannot start: some of its prerequisites are not finished.", taskID),
				false,
				"Try again after the prerequisite tasks are done.",
			)
		}
		started, err := tc.Server.opts.Kanban.StartTask(tc.Context, taskID, task.Revision)
		if err != nil {
			return kanbanServiceError("Could not start task", err)
		}
		return kanbanCardResult(tc, started, "card", map[string]any{
			"task_id":      taskID,
			"target":       target,
			"turn_started": true,
		}, true)
	}

	// target == "done"：等待用户的卡片不能强推；待评审卡片必须走评审流程；
	// 既无运行中回合又非 in_progress 的卡片没有可结算的执行轮；仍在执行的卡片先中断执行轮再结算。
	if kanbanDisplayColumn(tc, taskID) == kanban.DisplayColumnNeedsYou {
		return toolError(
			CodeForbidden,
			fmt.Sprintf("Card '%s' is waiting for the user and cannot be pushed to done.", taskID),
			false,
			"Once the user has answered the pending approval or question, the card can be settled.",
		)
	}
	if task.Status == string(kanban.TaskStatusDone) {
		return kanbanCardResult(tc, task, "card", map[string]any{
			"task_id":      taskID,
			"target":       target,
			"already_done": true,
		}, true)
	}

	// review 卡片必须走人工评审流程结算，代理不能直接把待评审卡片推到完成。
	if task.Status == string(kanban.TaskStatusReview) {
		return toolError(
			CodeForbidden,
			fmt.Sprintf("Card '%s' is waiting for review; an agent cannot mark it done.", taskID),
			false,
			"Leave it for the user to decide, or record a decision with kanban_review_task.",
		)
	}

	// 既没有运行中的执行轮、卡片又不在 in_progress 时，结算为 done 没有任何可结算的执行轮，
	// 这类空结算只会掩盖真实状态，直接拒绝。
	executing := tc.Server.opts.Kanban.TaskExecuting(tc.Context, task)
	if !executing && task.Status != string(kanban.TaskStatusInProgress) {
		return toolError(
			CodeForbidden,
			fmt.Sprintf("Card '%s' is not in progress and nothing is running on it, so there is nothing to mark done.", taskID),
			false,
			fmt.Sprintf("Check the card's current state with kanban_read_card (task_id %s) first.", taskID),
		)
	}

	interruptRequested := false
	if executing {
		if err := tc.Server.opts.Kanban.InterruptTask(tc.Context, task); err != nil {
			return toolError(
				CodePartialFailure,
				fmt.Sprintf("Wrapping up the running card '%s' failed: %v", taskID, err),
				false,
				fmt.Sprintf("Work on the card is still going on; check it with kanban_read_card (task_id %s) before trying again.", taskID),
			)
		}
		interruptRequested = true
		// 中断期间的状态写入会推进版本号，重读后再结算。
		if fresh, err := tc.Server.opts.Store.GetTask(tc.Context, taskID); err == nil {
			task = fresh
		}
		if task.Status == string(kanban.TaskStatusDone) {
			return kanbanCardResult(tc, task, "card", map[string]any{
				"task_id":             taskID,
				"target":              target,
				"already_done":        true,
				"interrupt_requested": true,
			}, true)
		}
	}

	moved, err := tc.Server.opts.Kanban.MoveTask(tc.Context, taskID, kanban.TaskStatusDone, task.Revision)
	if err != nil {
		return kanbanServiceError("Moving the task to done failed", err)
	}
	payload := map[string]any{
		"task_id": taskID,
		"target":  target,
	}
	if interruptRequested {
		payload["interrupt_requested"] = true
	}
	return kanbanCardResult(tc, moved, "card", payload, true)
}

func handleKanbanStartTask(tc ToolContext, args map[string]any) ToolResult {
	role, ownTaskID, guard := kanbanScope(tc)
	if guard != nil {
		return *guard
	}
	taskID, _ := args["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)
	if selfTarget := kanbanSelfTargetError(role, ownTaskID, taskID); selfTarget != nil {
		return *selfTarget
	}

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Service is unavailable", false)
	}
	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}
	if blocked, blockedErr := taskBlocked(tc, taskID); blockedErr == nil && blocked {
		return toolError(
			CodeInvalidArguments,
			fmt.Sprintf("Task '%s' cannot start: some of its prerequisites are not finished.", taskID),
			false,
			"Try again after the prerequisite tasks are done.",
		)
	}

	started, err := tc.Server.opts.Kanban.StartTask(tc.Context, taskID, task.Revision)
	if err != nil {
		return kanbanServiceError("Could not start task", err)
	}
	return toolSuccess(map[string]any{
		"success": true,
		"task_id": taskID,
		"card":    started,
	})
}

func handleKanbanReviewTask(tc ToolContext, args map[string]any) ToolResult {
	role, ownTaskID, guard := kanbanScope(tc)
	if guard != nil {
		return *guard
	}
	taskID, _ := args["task_id"].(string)
	if strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)
	if selfTarget := kanbanSelfTargetError(role, ownTaskID, taskID); selfTarget != nil {
		return *selfTarget
	}

	decisionStr, _ := args["decision"].(string)
	var decision kanban.ReviewDecision
	switch decisionStr {
	case "accept":
		decision = kanban.ReviewAccept
	case "reject":
		decision = kanban.ReviewReject
	case "requestReview":
		decision = kanban.ReviewRequest
	default:
		return toolError(CodeInvalidArguments, "decision has to be accept, reject or requestReview.", false)
	}
	feedback, _ := args["feedback"].(string)

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Service is unavailable", false)
	}
	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}
	// 总管对自己目标下的任务不能连续打回，也不能自行接受需要用户亲手验证的任务。
	if guard := goalReviewGuard(tc, task, decision, args); guard != nil {
		return *guard
	}

	reviewed, err := tc.Server.opts.Kanban.ReviewTask(tc.Context, taskID, decision, feedback, task.Revision)
	if err != nil {
		return kanbanServiceError("The review decision could not be recorded", err)
	}
	return toolSuccess(map[string]any{
		"success":  true,
		"decision": decisionStr,
		"task_id":  taskID,
		"card":     reviewed,
	})
}

// goalOwnerSteward 是总管创建目标时写入 Goal.Owner 的标记值，与 app 包的 goalOwnerSteward 一致。
const goalOwnerSteward = "steward"

// acceptanceUserVerifyTag 是验收标准里「必须由用户亲手验证」的行首标签。
const acceptanceUserVerifyTag = "[你验]"

// goalReviewGuard 拦住总管对自己目标下任务的越级审批：同一任务第二次打回，
// 以及验收标准含 [你验] 的接受，都必须先问用户；user_confirmed 为真表示用户已明确同意。
// 普通会话（调用方会话不等于目标的 PlanThreadID）不受影响。
func goalReviewGuard(tc ToolContext, task store.Task, decision kanban.ReviewDecision, args map[string]any) *ToolResult {
	if decision != kanban.ReviewReject && decision != kanban.ReviewAccept {
		return nil
	}
	if confirmed, _ := args["user_confirmed"].(bool); confirmed {
		return nil
	}
	if tc.Server.opts.Store == nil || task.GoalID == nil || strings.TrimSpace(*task.GoalID) == "" {
		return nil
	}
	goal, err := tc.Server.opts.Store.GetGoal(tc.Context, *task.GoalID)
	if err != nil || goal.Owner != goalOwnerSteward || goal.PlanThreadID == nil {
		return nil
	}
	threadID := strings.TrimSpace(*goal.PlanThreadID)
	if threadID == "" || threadID != scopeThreadID(tc.Context) {
		return nil
	}
	var reason string
	switch {
	case decision == kanban.ReviewReject && task.RejectCount >= 1:
		reason = "这个任务已经被打回过一次，第二次打回要由用户决定。"
	case decision == kanban.ReviewAccept && strings.Contains(task.AcceptanceCriteria, acceptanceUserVerifyTag):
		reason = "这个任务的验收标准里有 [你验]，只能由用户确认后才能接受。"
	default:
		return nil
	}
	res := toolError(CodeForbidden, reason+"需要先问用户：用 notify_owner 说明情况，等用户决定。", false,
		"Ask the user first; once they agree, repeat the call with user_confirmed=true.")
	return &res
}

func handleKanbanCreateGoal(tc ToolContext, args map[string]any) ToolResult {
	// 只有普通会话可以建目标：任务、评审、规划线程均以只读拒绝，避免递归派发（D-KB-019）。
	if role, _ := tc.Server.threadRole(tc.Context, tc.Project.ID); role != threadRoleOrdinary {
		return toolError(
			CodeReadOnly,
			fmt.Sprintf("Goals can only be created from an ordinary conversation, and this thread is a %s.", role.label()),
			false,
			"Create it from the conversation this thread came from.",
		)
	}
	// 任务执行会话运行在任务 worktree 中，禁止再创建目标以防递归派发（D-KB-019）。
	if tc.WorkDir != "" && tc.Server.opts.Kanban != nil && tc.Server.opts.Kanban.IsTaskWorktreeDir(tc.WorkDir) {
		return toolError(CodeForbidden, "任务执行会话不能创建目标，以防递归派发", false)
	}
	prompt, _ := args["prompt"].(string)
	if strings.TrimSpace(prompt) == "" {
		return toolError(CodeInvalidArguments, "Missing or empty required argument: 'prompt'.", false)
	}
	if tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}
	goal, err := tc.Server.opts.Kanban.CreateGoal(tc.Context, tc.Project.ID, prompt)
	if err != nil {
		return toolError(CodeInternal, fmt.Sprintf("Could not create goal: %v", err), true)
	}

	title := goal.Title
	if customTitle, ok := args["title"].(string); ok && strings.TrimSpace(customTitle) != "" {
		title = strings.TrimSpace(customTitle)
	}

	return toolSuccess(map[string]any{
		"goal_id":      goal.ID,
		"title":        title,
		"status":       goal.Status,
		"preview_plan": true,
		"message":      fmt.Sprintf("Planning has started for goal '%s'; its progress shows in the goal's chat and in the Goals bar.", title),
	})
}

func handleKanbanGoalStatus(tc ToolContext, args map[string]any) ToolResult {
	goalID, _ := args["goal_id"].(string)
	if strings.TrimSpace(goalID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'goal_id'.", false)
	}
	goalID = strings.TrimSpace(goalID)

	if tc.Server.opts.Store == nil {
		return toolError(CodeInternal, "Store is unavailable", false)
	}
	goal, err := tc.Server.opts.Store.GetGoal(tc.Context, goalID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Goal '%s' not found.", goalID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if goal.ProjectID != tc.Project.ID {
		return toolError(CodeInvalidArguments, fmt.Sprintf("Goal '%s' is in a different project.", goalID), false)
	}

	taskIDs, err := tc.Server.opts.Store.ListGoalTaskIDs(tc.Context, goalID)
	tasks := make([]store.Task, 0)
	if err == nil {
		for _, tid := range taskIDs {
			if t, err := tc.Server.opts.Store.GetTask(tc.Context, tid); err == nil {
				tasks = append(tasks, t)
			}
		}
	}

	return toolSuccess(map[string]any{
		"goal_id":      goal.ID,
		"id":           goal.ID,
		"title":        goal.Title,
		"status":       goal.Status,
		"statusReason": goal.StatusReason,
		"planSummary":  goal.PlanSummary,
		"policy":       goal.Policy,
		"progress":     goal.Progress,
		"tasks":        tasks,
	})
}

func handleKanbanCreateDraft(tc ToolContext, args map[string]any) ToolResult {
	if guard := kanbanWriteGuard(tc); guard != nil {
		return *guard
	}
	title, ok := args["title"].(string)
	if !ok || strings.TrimSpace(title) == "" {
		return toolError(CodeInvalidArguments, "Missing or empty required argument: 'title'.", false)
	}
	title = strings.TrimSpace(title)

	var notes string
	if n, ok := args["notes"].(string); ok {
		notes = n
	}

	var goalID *string
	if g, ok := args["goal_id"].(string); ok && strings.TrimSpace(g) != "" {
		trimmed := strings.TrimSpace(g)
		goalID = &trimmed
	}

	deps, depErr := parseDependsOnTaskIDs(tc, args)
	if depErr != nil {
		return *depErr
	}

	if tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}

	in := kanban.CreateDraftInput{
		ProjectID: tc.Project.ID,
		Title:     title,
		Notes:     notes,
		GoalID:    goalID,
		DependsOn: deps,
	}

	created, err := tc.Server.opts.Kanban.CreateDraft(tc.Context, in)
	if err != nil {
		if errors.Is(err, kanban.ErrInvalidTransition) {
			return toolError(CodeInvalidArguments, err.Error(), false)
		}
		if errors.Is(err, store.ErrRevisionConflict) {
			return toolError(CodeConflict, "The goal was changed at the same time; try again.", true)
		}
		if errors.Is(err, kanban.ErrCycleDetected) {
			return toolError(CodeInvalidArguments, "These prerequisites would make tasks depend on each other in a loop.", false)
		}
		return toolError(CodeInternal, "Creating the task failed: "+err.Error(), false)
	}

	return kanbanCardResult(tc, created, "task", map[string]any{
		"success": true,
		"status":  "draft_created",
	}, true)
}

func handleKanbanUpdateCard(tc ToolContext, args map[string]any) ToolResult {
	if guard := kanbanWriteGuard(tc); guard != nil {
		return *guard
	}
	taskID, ok := args["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)

	var title *string
	if t, ok := args["title"].(string); ok {
		trimmed := strings.TrimSpace(t)
		title = &trimmed
	}
	var notes *string
	if n, ok := args["notes"].(string); ok {
		notes = &n
	}
	var notesAppend *string
	if na, ok := args["notes_append"].(string); ok {
		trimmed := strings.TrimSpace(na)
		notesAppend = &trimmed
	}
	var reviewSummary *string
	if rs, ok := args["review_summary"].(string); ok {
		reviewSummary = &rs
	}

	if title == nil && notes == nil && (notesAppend == nil || *notesAppend == "") && reviewSummary == nil {
		return toolError(CodeInvalidArguments, "Nothing to update: pass at least one of 'title', 'notes', 'notes_append' or 'review_summary'.", false)
	}

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Service is unavailable", false)
	}
	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}

	in := kanban.UpdateTaskInput{
		TaskID:           taskID,
		ExpectedRevision: task.Revision,
		Title:            title,
		Notes:            notes,
		NotesAppend:      notesAppend,
		ReviewSummary:    reviewSummary,
	}

	updated, err := tc.Server.opts.Kanban.UpdateTask(tc.Context, in)
	if err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			return toolError(CodeConflict, "The card was changed at the same time; try again.", true)
		}
		return toolError(CodeInternal, fmt.Sprintf("Could not update card: %v", err), true)
	}

	return kanbanCardResult(tc, updated, "card", map[string]any{
		"success": true,
		"task_id": taskID,
	}, true)
}

func handleKanbanSubmitResult(tc ToolContext, args map[string]any) ToolResult {
	if guard := kanbanWriteGuard(tc); guard != nil {
		return *guard
	}
	taskID, ok := args["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)
	outcome, _ := args["outcome"].(string)
	status, _ := args["status"].(string)
	if strings.TrimSpace(status) == "" && strings.TrimSpace(outcome) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'outcome'.", false)
	}
	summary, ok := args["summary"].(string)
	if !ok || strings.TrimSpace(summary) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'summary'.", false)
	}

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Service is unavailable", false)
	}
	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}

	callerThreadID := scopeThreadID(tc.Context)
	if status != "" {
		data, marshalErr := json.Marshal(args)
		var res kanban.ExecutorResult
		if marshalErr != nil { return toolError(CodeInvalidArguments, marshalErr.Error(), false) }
		if err := json.Unmarshal(data, &res); err != nil { return toolError(CodeInvalidArguments, err.Error(), false) }
		if err := res.Validate(); err != nil { return toolError(CodeInvalidArguments, err.Error(), false) }
		err = tc.Server.opts.Kanban.SubmitExecutorResult(tc.Context, task.ID, callerThreadID, res)
	} else {
		err = tc.Server.opts.Kanban.SubmitTaskResult(tc.Context, task.ID, callerThreadID, outcome, summary)
	}
	if err != nil {
		return kanbanServiceError("Cannot submit the task result", err)
	}

	return toolSuccess(map[string]any{
		"success":   true,
		"task_id":   task.ID,
		"outcome":   strings.TrimSpace(outcome),
		"status":    strings.TrimSpace(status),
		"thread_id": callerThreadID,
	})
}

func handleKanbanUpdateTask(tc ToolContext, args map[string]any) ToolResult {
	if guard := kanbanWriteGuard(tc); guard != nil {
		return *guard
	}
	taskID, ok := args["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)

	if statusRaw, ok := args["status"].(string); ok && statusRaw != "" {
		var target string
		if statusRaw == "in_progress" {
			target = "inProgress"
		} else if statusRaw == "done" {
			target = "done"
		} else {
			return toolError(CodeInvalidArguments, "status has to be 'in_progress' or 'done'.", false)
		}
		return handleKanbanMoveCard(tc, map[string]any{
			"task_id": taskID,
			"target":  target,
		})
	}

	var notesAppend *string
	if na, ok := args["notes_append"].(string); ok {
		trimmed := strings.TrimSpace(na)
		notesAppend = &trimmed
	}
	var reviewSummary *string
	if rs, ok := args["review_summary"].(string); ok {
		reviewSummary = &rs
	}

	if (notesAppend == nil || *notesAppend == "") && reviewSummary == nil {
		return toolError(CodeInvalidArguments, "Nothing to update: pass 'notes_append', 'review_summary', or both.", false)
	}

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Service is unavailable", false)
	}
	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, fmt.Sprintf("Task '%s' not found.", taskID), false)
		}
		return toolError(CodeInternal, err.Error(), true)
	}
	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}

	in := kanban.UpdateTaskInput{
		TaskID:           taskID,
		ExpectedRevision: task.Revision,
		NotesAppend:      notesAppend,
		ReviewSummary:    reviewSummary,
	}

	updated, err := tc.Server.opts.Kanban.UpdateTask(tc.Context, in)
	if err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			return toolError(CodeConflict, "The card was changed at the same time; try again.", true)
		}
		return toolError(CodeInternal, fmt.Sprintf("Could not update task: %v", err), true)
	}

	return kanbanCardResult(tc, updated, "card", map[string]any{
		"success": true,
		"task_id": taskID,
	}, true)
}

func handleKanbanDeleteCard(tc ToolContext, args map[string]any) ToolResult {
	role, ownTaskID, guard := kanbanScope(tc)
	if guard != nil {
		return *guard
	}
	taskID, ok := args["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	taskID = strings.TrimSpace(taskID)
	if selfTarget := kanbanSelfTargetError(role, ownTaskID, taskID); selfTarget != nil {
		return *selfTarget
	}

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Service is unavailable", false)
	}

	task, err := tc.Server.opts.Store.GetTask(tc.Context, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolSuccess(map[string]any{
				"success":         true,
				"deleted":         false,
				"already_deleted": true,
				"message":         fmt.Sprintf("Card '%s' was already deleted.", taskID),
				"task_id":         taskID,
			})
		}
		return toolError(CodeInternal, err.Error(), true)
	}

	if task.ProjectID == nil || *task.ProjectID != tc.Project.ID {
		return toolError(CodeNotFound, fmt.Sprintf("This project has no task '%s'.", taskID), false)
	}

	if err := tc.Server.opts.Kanban.DeleteTask(tc.Context, taskID, task.Revision); err != nil {
		if errors.Is(err, kanban.ErrInvalidTransition) {
			return toolError(CodeInvalidArguments, "An agent is still working on this card; settle it before deleting.", false)
		}
		if errors.Is(err, store.ErrNotFound) {
			return toolSuccess(map[string]any{
				"success":         true,
				"deleted":         false,
				"already_deleted": true,
				"message":         fmt.Sprintf("Card '%s' was already deleted.", taskID),
				"task_id":         taskID,
			})
		}
		if errors.Is(err, store.ErrRevisionConflict) {
			return toolError(CodeConflict, "The card was changed at the same time; try again.", true)
		}
		return toolError(CodeInternal, fmt.Sprintf("Could not delete card: %v", err), true)
	}

	return toolSuccess(map[string]any{
		"success": true,
		"deleted": true,
		"task_id": taskID,
	})
}

func handleKanbanDeleteTask(tc ToolContext, args map[string]any) ToolResult {
	return handleKanbanDeleteCard(tc, args)
}

func handleKanbanSetGoal(tc ToolContext, args map[string]any) ToolResult {
	if guard := kanbanWriteGuard(tc); guard != nil {
		return *guard
	}
	taskID, ok := args["task_id"].(string)
	if !ok || strings.TrimSpace(taskID) == "" {
		return toolError(CodeInvalidArguments, "Missing required argument: 'task_id'.", false)
	}
	prompt, ok := args["goal"].(string)
	if !ok {
		return toolError(CodeInvalidArguments, "Missing required argument: 'goal'.", false)
	}
	if tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}
	task, err := tc.Server.opts.Kanban.SetGoal(tc.Context, kanban.SetGoalInput{
		TaskID: strings.TrimSpace(taskID), ProjectID: tc.Project.ID, Prompt: prompt,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, "This project has no such task.", false)
		}
		if errors.Is(err, store.ErrRevisionConflict) {
			return toolError(CodeConflict, "The card was changed at the same time; try again.", true)
		}
		return toolError(CodeInternal, "Could not set goal: "+err.Error(), true)
	}
	return toolSuccess(map[string]any{"success": true, "task_id": task.ID, "goal": task.GoalPrompt})
}

func handleKanbanReadGoal(tc ToolContext, args map[string]any) ToolResult {
	goalID, ok := args["goal_id"].(string)
	if !ok || strings.TrimSpace(goalID) == "" {
		return toolError(CodeInvalidArguments, "Missing or invalid required argument: 'goal_id'.", false)
	}
	goalID = strings.TrimSpace(goalID)

	contentHash, ok := args["content_hash"].(string)
	if !ok || strings.TrimSpace(contentHash) == "" {
		return toolError(CodeInvalidArguments, "Missing or invalid required argument: 'content_hash'.", false)
	}
	contentHash = strings.TrimSpace(contentHash)

	if tc.Server.opts.Store == nil || tc.Server.opts.Kanban == nil {
		return toolError(CodeInternal, "Kanban service is unavailable", false)
	}

	goal, err := tc.Server.opts.Store.GetGoal(tc.Context, goalID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, "No stored goal text matches this goal_id and content_hash.", false)
		}
		return toolError(CodeInternal, "Failed to read goal: "+err.Error(), false)
	}
	if goal.ProjectID != tc.Project.ID {
		return toolError(CodeForbidden, "Not allowed: the goal is in a different project.", false)
	}

	prompt, err := tc.Server.opts.Kanban.ReadGoal(tc.Context, goalID, contentHash)
	if err != nil {
		if errors.Is(err, kanban.ErrHashMismatch) {
			return toolError(CodeIntegrityError, "The stored goal text does not match its hash.", false)
		}
		if errors.Is(err, store.ErrNotFound) {
			return toolError(CodeNotFound, "No stored goal text matches this goal_id and content_hash.", false)
		}
		return toolError(CodeInternal, "Reading the goal text failed: "+err.Error(), false)
	}

	return ToolResult{
		Text:    prompt,
		IsError: false,
	}
}
