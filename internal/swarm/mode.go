package swarm

import "strings"

// 会话级编排工作模式的取值。
// 「关闭」不在这里：会话是否启用编排由 Manager.State(threadID).Enabled 决定。
const (
	// ModeInherited 跟随全局编排设置（会话未设置时的默认值）。
	ModeInherited = "inherited"
	// ModeCollaborative 协作模式：主导规划并委派大任务，小改动可亲自执行。
	ModeCollaborative = "collaborative"
	// ModeLeadPlansOnly 主导只规划不动手：所有改动（哪怕一行）都派给 bee。
	ModeLeadPlansOnly = "leadPlansOnly"
	// ModeAlwaysBees 每条消息直接派给 bee，绕过主代理。
	ModeAlwaysBees = "alwaysBees"
)

// GlobalMode 汇总解析「跟随设置」时参考的全局编排开关。
type GlobalMode struct {
	// AlwaysBees 对应全局「每条消息都派给 bee」。
	AlwaysBees bool
	// LeadPlansOnly 对应全局「主导只规划不动手」。
	LeadPlansOnly bool
}

// NormalizeMode 把空值或未知的会话模式归一为「跟随设置」。
// 旧值 alwaysHeads 为过渡期兼容，归一化为 alwaysBees。
func NormalizeMode(mode string) string {
	switch strings.TrimSpace(mode) {
	case ModeCollaborative:
		return ModeCollaborative
	case ModeLeadPlansOnly:
		return ModeLeadPlansOnly
	case ModeAlwaysBees, "alwaysHeads": // 过渡期兼容旧值
		return ModeAlwaysBees
	default:
		return ModeInherited
	}
}

// EffectiveMode 解析会话实际生效的模式：显式选择的模式优先，
// 「跟随设置」时按全局开关推导，优先级为每条都派 > 只规划 > 协作。
func EffectiveMode(threadMode string, global GlobalMode) string {
	switch NormalizeMode(threadMode) {
	case ModeCollaborative:
		return ModeCollaborative
	case ModeLeadPlansOnly:
		return ModeLeadPlansOnly
	case ModeAlwaysBees:
		return ModeAlwaysBees
	}
	if global.AlwaysBees {
		return ModeAlwaysBees
	}
	if global.LeadPlansOnly {
		return ModeLeadPlansOnly
	}
	return ModeCollaborative
}
