package mcp

// kanbanEntryToolName 为普通会话唯一的看板入口工具名。
const kanbanEntryToolName = "kanban"

// kanbanToolNamePrefix 为内部类型化看板工具的名字前缀。
const kanbanToolNamePrefix = "kanban_"

// kanbanFamily 是看板入口族：普通会话只看到 `kanban`，看板线程按角色看类型化工具。
var kanbanFamily = &toolFamily{
	entry:       kanbanEntryToolName,
	prefix:      kanbanToolNamePrefix,
	capability:  CapKanban,
	description: "看板操作入口：所有看板读取与修改都通过这个工具完成，不要寻找单独的工具。先调用 action=help 查看可用操作及其参数，再用 action 加上 args 调用目标操作。",
	overview:    "看板操作入口：先用 action=help 查看操作，再用 action 加 args 调用。",
	exclude:     kanbanExcludedTools(),
}

// kanbanExcludedTools 排除沿用了 kanban_ 前缀的总管专属工具：kanban_move_task 属于总管白名单，
// 不属于看板入口族，避免 enum 与分派把它当成看板动作。
func kanbanExcludedTools() map[string]bool {
	out := make(map[string]bool, len(stewardToolNames)+len(stewardActionToolNames))
	for name := range stewardToolNames {
		out[name] = true
	}
	for name := range stewardActionToolNames {
		out[name] = true
	}
	return out
}
