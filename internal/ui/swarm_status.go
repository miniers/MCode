package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// Swarm 子代理状态收敛为固定的 5 态：空闲、运行中、待审查、已完成、失败。
// 后端与旧文案的其余取值在归一化时映射进来，界面上不再出现其它状态。
const (
	swarmStatusIdle          = "idle"
	swarmStatusRunning       = "running"
	swarmStatusWaitingReview = "waiting_review"
	swarmStatusCompleted     = "completed"
	swarmStatusErrored       = "errored"
)

// normalizeSwarmStatus 把后端状态或旧文案收敛到 5 态，无法识别的取值归一到 idle。
// stopped 表示用户主动停止，既不算完成也不算失败，同样归入 idle。
func normalizeSwarmStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case swarmStatusRunning:
		return swarmStatusRunning
	case swarmStatusWaitingReview, "needs_human":
		return swarmStatusWaitingReview
	case swarmStatusCompleted, "complete", "succeeded", "success", "done":
		return swarmStatusCompleted
	case swarmStatusErrored, "failed", "error":
		return swarmStatusErrored
	default:
		return swarmStatusIdle
	}
}

// MapSwarmBeeStatus 在 MapBeeStatus 的基础上补充合并审查信息：
// 等待用户审查合并的子代理归一到 waiting_review，其余仍按后端状态映射。
func MapSwarmBeeStatus(b SwarmBee) string {
	if b.PendingMerge || strings.EqualFold(strings.TrimSpace(b.ReviewState), "needs_human") {
		return swarmStatusWaitingReview
	}
	return MapBeeStatus(b.Status, false)
}

// swarmStatusLabel 将 Swarm 状态值转换为简短中文标签，未知状态显示为空闲。
func swarmStatusLabel(status string) string {
	switch normalizeSwarmStatus(status) {
	case swarmStatusRunning:
		return "运行中"
	case swarmStatusWaitingReview:
		return "待审查"
	case swarmStatusCompleted:
		return "已完成"
	case swarmStatusErrored:
		return "失败"
	default:
		return "空闲"
	}
}

// swarmStatusTone 返回状态对应的 StatusDot 状态与文字颜色。
// 形状只取 StatusDot 的 idle/running/attention/error；completed 复用 idle 的点配成功色文字。
func swarmStatusTone(c *ui.Context, status string) (Status, ui.Color) {
	switch normalizeSwarmStatus(status) {
	case swarmStatusRunning:
		return StatusRunning, ThemeAccent(c)
	case swarmStatusWaitingReview:
		return StatusAttention, ThemeWarning(c)
	case swarmStatusCompleted:
		return StatusIdle, ThemeSuccess(c)
	case swarmStatusErrored:
		return StatusError, ThemeDanger(c)
	default:
		return StatusIdle, ThemeTertiaryText(c)
	}
}

// swarmReportStatusText 把后端状态还原成汇报文本里的状态词，只用于匹配汇报条目的归属。
// 汇报里的状态是历史文案（运行中/已完成/失败/已停止），不参与界面显示。
func swarmReportStatusText(status string) string {
	switch normalizeSwarmStatus(status) {
	case swarmStatusRunning:
		return "运行中"
	case swarmStatusCompleted:
		return "已完成"
	case swarmStatusErrored:
		return "失败"
	}
	if strings.EqualFold(strings.TrimSpace(status), "stopped") {
		return "已停止"
	}
	return status
}

// swarmStatusColor 返回状态对应的文字颜色，供暂未升级到状态点的视图使用。
func swarmStatusColor(c *ui.Context, status string) ui.Color {
	_, color := swarmStatusTone(c, status)
	return color
}
