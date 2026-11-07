package kanban

import "context"

// SessionMessenger 由驱动可选实现，用于向运行中的会话投递催促消息。
type SessionMessenger interface {
	SendFollowUp(ctx context.Context, threadID, text string) error
}

// SessionInterrupter 由驱动可选实现，用于中断运行中的会话。
type SessionInterrupter interface {
	InterruptSession(ctx context.Context, threadID string) error
}

// ReadOnlySessionPromoter 由驱动可选实现：把仍处只读档的会话升到 auto；其他档位是用户选过的，保持不变。
type ReadOnlySessionPromoter interface {
	PromoteReadOnlySession(ctx context.Context, threadID string) error
}

// SessionUsageReader 由驱动可选实现，用于读取子会话的累计用量并写进编排记录。
// tokens 为会话累计的输入+输出 token（与界面会话用量同口径），toolCalls 为工具调用次数。
type SessionUsageReader interface {
	SessionUsage(ctx context.Context, threadID string) (tokens int, toolCalls int, err error)
}

// StuckPokeMessage 是任务长时间无进展时发送给 Agent 的催促文案。
const StuckPokeMessage = "Progress on this task seems to have stalled. Resume the work and briefly state what is done and what remains."
