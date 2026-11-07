package wire

// KanbanDeleteTaskParams 对应 kanban.deleteTask 的请求参数。
// expectedRevision 大于 0 时按修订号做乐观并发校验，0 表示不校验（与桌面端删除一致）。
type KanbanDeleteTaskParams struct {
	TaskID           string `json:"taskID"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}

// KanbanRescueTaskParams 对应 kanban.rescueTask 的请求参数。
// action 取桌面端救援菜单的动作：interrupt（中断并重置为待办）、nudge（提醒 Agent）。
type KanbanRescueTaskParams struct {
	TaskID           string `json:"taskID"`
	Action           string `json:"action"`
	ExpectedRevision int    `json:"expectedRevision"`
	ActionID         string `json:"actionID"`
}
