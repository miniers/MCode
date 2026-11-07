package mcodeui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// 子代理浮窗的「已结束」外发光：卡片与顶栏胶囊在该子线程的人设色上亮一圈，
// 让用户一眼看出哪个子线程已经跑完了。

// swarmBeeDone 报告浮窗卡片上的子代理是否已经结束、该给人设色外发光：
// 复用 swarmBeeDormant 的终态判定（仍在运行、在等人工合并、在等用户回答/审批都不算结束），
// 并要求状态确实是终态，避免刚创建、状态还没回填的子代理被误判成已完成。
func swarmBeeDone(h SwarmBee, waiting bool) bool {
	switch h.Status {
	case "completed", "failed", "stopped":
	default:
		return false
	}
	return swarmBeeDormant(h, waiting)
}

// swarmBeeWaiting 复用 swarmWaitingBeeIDs 的判定，报告单个子代理是否在等用户回答提问或审批。
// 待处理请求集合按会话运行态版本缓存，这里只做一次查表（面板卡片每帧都会问）。
func swarmBeeWaiting(s *chatState, h SwarmBee) bool {
	if s == nil {
		return false
	}
	id := strings.TrimSpace(h.ThreadID)
	if id == "" {
		return false
	}
	return s.pendingRequestThreadIDs()[id]
}

// swarmDoneGlowColor 返回子代理结束时外发光与描边用的颜色：取该子线程的人设色并按 alpha 淡化。
// 人设色由 findSwarmPersona 给出（名单外/带轮次后缀的名字由它回落到 swarmAvatarColor），
// 因此发光的色相与卡片里名字、头像的配色一致。
func swarmDoneGlowColor(name string, alpha float32) ui.Color {
	return findSwarmPersona(strings.TrimSpace(name)).Color.Alpha(alpha)
}
