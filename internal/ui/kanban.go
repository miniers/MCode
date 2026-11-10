package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	// kanbanBadgeIconSize 是任务卡状态徽标里的小图标边长。
	kanbanBadgeIconSize float32 = 10
	// kanbanDetailChromeHeight 是任务详情弹窗标题与底部操作占用的总高度。
	kanbanDetailChromeHeight float32 = 70
	// kanbanDetailLogMaxHeight 是详情里执行日志滚动区的最大高度。
	kanbanDetailLogMaxHeight float32 = 180
	// kanbanDetailDiffMaxHeight 是详情里改动差异滚动区的最大高度。
	kanbanDetailDiffMaxHeight float32 = 220
	// kanbanCardHoverBorder 是任务卡悬停时的描边宽度。
	kanbanCardHoverBorder float32 = 1.5
)

// filterKanbanTasks 按查询串过滤任务：标题、备注、最近活动或失败原因包含该串即保留，
// 不区分大小写；空串原样返回。
func filterKanbanTasks(tasks []KanbanTask, query string) []KanbanTask {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return tasks
	}
	out := make([]KanbanTask, 0, len(tasks))
	for _, task := range tasks {
		if strings.Contains(strings.ToLower(task.Title), query) ||
			strings.Contains(strings.ToLower(task.Notes), query) ||
			strings.Contains(strings.ToLower(task.LastActivity), query) ||
			strings.Contains(strings.ToLower(task.LastFailureReason), query) {
			out = append(out, task)
		}
	}
	return out
}

// kanbanStatusLabel 返回任务状态的界面文案，未知状态原样返回。
func kanbanStatusLabel(status string) string {
	switch status {
	case "waiting":
		return "等待中"
	case "failed":
		return "失败"
	case "cancelled":
		return "已取消"
	case "todo":
		return "待办"
	case "in_progress":
		return "进行中"
	case "needs_you":
		return "等你处理"
	case "needs_approval":
		return "待审批"
	case "review":
		return "审查"
	case "done":
		return "完成"
	case "rejected":
		return "已驳回"
	default:
		return status
	}
}

// kanbanPriorityStyle 返回任务优先级的徽标样式：文案、图标与颜色。
// 服务端默认值是 "none"，此时 ok 为 false，卡片不渲染优先级徽标。
func kanbanPriorityStyle(c *ui.Context, p string) (label string, icon *ui.SVG, color ui.Color, ok bool) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "none":
		return "", nil, ui.Color{}, false
	case "urgent":
		return "紧急", IconFlame(), ThemeDanger(c), true
	case "high":
		return "高", IconChevronsUp(), ThemeWarning(c), true
	case "medium":
		return "中", IconCircleSmall(), ThemeCaution(c), true
	case "low":
		return "低", IconCircleSmall(), ThemeAccent(c), true
	default:
		return p, IconCircleSmall(), ThemeSecondaryText(c), true
	}
}

// kanbanStatusColor 返回看板列对应的状态色。
func kanbanStatusColor(c *ui.Context, col string) ui.Color {
	switch col {
	case "todo":
		return ThemeAccent(c)
	case "in_progress", "waiting":
		return ThemeWarning(c)
	case "needs_you":
		return ThemePlan(c)
	case "review":
		return ThemeCaution(c)
	case "done", "cancelled":
		return ThemeSuccess(c)
	case "rejected", "failed":
		return ThemeDanger(c)
	default:
		return ThemeSecondaryText(c)
	}
}

// kanbanDisplayColumn 返回任务所在的展示列：优先服务端派生的 Column，
// 为空时才回退到原始 Status（Status 含 needs_approval、rejected 等非展示列值）。
func kanbanDisplayColumn(task KanbanTask) string {
	if task.Column != "" {
		return task.Column
	}
	return task.Status
}
