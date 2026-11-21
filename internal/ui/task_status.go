package mcodeui

import "github.com/egoist/mygo/ui"

type semanticStatus int

const (
	semanticNeutral semanticStatus = iota
	semanticAccent
	semanticSuccess
	semanticWarning
	semanticDanger
)

// semanticStatusColor 将跨模块共用的状态语义映射到主题色。
func semanticStatusColor(c *ui.Context, status semanticStatus) ui.Color {
	switch status {
	case semanticAccent:
		return ThemeAccent(c)
	case semanticSuccess:
		return ThemeSuccess(c)
	case semanticWarning:
		return ThemeWarning(c)
	case semanticDanger:
		return ThemeDanger(c)
	default:
		return ThemeSecondaryText(c)
	}
}

// kanbanColumnLabel 返回看板列的标准中文名称，未知列原样返回。
func kanbanColumnLabel(col string) string {
	switch col {
	case "todo":
		return "待办"
	case "in_progress":
		return "进行中"
	case "needs_you":
		return "待你处理"
	case "review":
		return "待审阅"
	case "done":
		return "已完成"
	default:
		return col
	}
}

// taskStatusLabel 返回任务状态（含列态 needs_you）的标准中文名称，未知状态原样返回。
func taskStatusLabel(status string) string {
	switch status {
	case "todo":
		return "待办"
	case "in_progress":
		return "进行中"
	case "needs_approval":
		return "待审批"
	case "waiting":
		return "等待中"
	case "review":
		return "待审阅"
	case "done":
		return "已完成"
	case "rejected":
		return "已驳回"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	case "needs_you":
		return "待你处理"
	default:
		return status
	}
}

// taskBadgeLabel 返回任务状态徽标的中文名称；未知状态与未知列返回空串。
func taskBadgeLabel(task KanbanTask) string {
	switch task.Status {
	case "waiting", "failed", "cancelled", "needs_approval", "rejected", "review", "done", "in_progress", "todo":
		return taskStatusLabel(task.Status)
	}
	col := kanbanDisplayColumn(task)
	switch col {
	case "todo", "in_progress", "needs_you", "review", "done":
		return kanbanColumnLabel(col)
	default:
		return ""
	}
}

// taskStatusSemantic 返回看板列与任务状态对应的语义色。
func taskStatusSemantic(status string) semanticStatus {
	switch status {
	case "in_progress", "review":
		return semanticAccent
	case "needs_you", "needs_approval", "rejected":
		return semanticWarning
	case "done":
		return semanticSuccess
	case "failed":
		return semanticDanger
	case "todo", "waiting", "cancelled":
		return semanticNeutral
	default:
		return semanticNeutral
	}
}

// goalStatusLabel 返回目标状态的标准中文名称，未知状态统一返回「处理中」。
func goalStatusLabel(status string) string {
	switch status {
	case "planning":
		return "规划中"
	case "plan_failed":
		return "规划失败"
	case "plan_review":
		return "方案待审"
	case "running":
		return "进行中"
	case "paused":
		return "已暂停"
	case "completed":
		return "已完成"
	case "cancelled":
		return "已取消"
	default:
		return "处理中"
	}
}

// goalStatusSemantic 返回目标状态对应的语义色。
func goalStatusSemantic(status string) semanticStatus {
	switch status {
	case "planning", "running":
		return semanticAccent
	case "plan_failed":
		return semanticDanger
	case "plan_review":
		return semanticWarning
	case "completed":
		return semanticSuccess
	case "paused", "cancelled":
		return semanticNeutral
	default:
		return semanticNeutral
	}
}

// automationRunStatusDict 返回自动化运行状态的中文名称与归类键。
func automationRunStatusDict(status string) (label string, kind string) {
	switch status {
	case "running":
		return "运行中", "running"
	case "succeeded", "success", "completed":
		return "成功", "succeeded"
	case "failed", "error":
		return "失败", "failed"
	case "skipped":
		return "已跳过", "stopped"
	case "stopped", "canceled", "cancelled":
		return "已停止", "stopped"
	case "":
		return "未知", ""
	default:
		return status, ""
	}
}

// attentionKindTitle 返回收件箱事件类型的标准中文标题。
func attentionKindTitle(kind string) string {
	switch kind {
	case "thread_stalled":
		return "会话卡住"
	case "thread_failed":
		return "会话出错"
	case "thread_quota":
		return "额度用尽"
	case "thread_approval":
		return "等待审批"
	case "kanban_failed":
		return "看板失败"
	case "kanban_needs_approval":
		return "看板待审批"
	case "kanban_review":
		return "看板待验收"
	case "kanban_plan_failed":
		return "规划失败"
	case "automation_failed":
		return "自动化失败"
	case "automation_finished":
		return "自动化完成"
	case "bee_failed":
		return "子代理失败"
	case "merge_review_pending":
		return "合并待审阅"
	case "thread_no_progress":
		return "任务无进展"
	default:
		return kind
	}
}

// isStewardTaskOrigin 报告任务是否来自总管（owner）。
func isStewardTaskOrigin(origin string) bool {
	return origin == "owner"
}

// isStewardAutomationOrigin 报告自动化是否由总管（steward）创建。
func isStewardAutomationOrigin(origin string) bool {
	return origin == "steward"
}

// renderStewardOriginBadge 渲染轻量「总管」来源胶囊标签。
func renderStewardOriginBadge(c *ui.Context, key string) {
	ui.Row(c.Key(key)).
		Label(key).
		Shrink(0).
		Padding(Space0, Space2).
		Radius(RadiusPill).
		Background(ThemeHover(c)).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, "总管").
				FontSize(uiFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
		})
}
