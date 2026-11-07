package mcodeui

import "context"

type sendDraftState int

const (
	sendStateSend sendDraftState = iota
	sendStateStop
	sendStateQueue
	sendStateSteer
	sendStateContinue
	sendStateSaveEdit
	// sendStateInject 表示运行中的消息插入当前回合：交给正在执行的轮次，
	// 由服务商在下一步读到；服务商不支持时后端退回排队。
	sendStateInject
)

// computeSendDraftState 根据当前会话运行状态、草稿内容、排队开关及编辑状态计算发送按钮的交互状态。
// 解耦设计：主发送按钮永远归位为发送按钮，继续操作由输入框上方独立胶囊承载。
func computeSendDraftState(isRunning, hasContent, canContinue, queueWhileRunning, editingQueued bool) sendDraftState {
	if editingQueued {
		return sendStateSaveEdit
	}
	if isRunning {
		if hasContent {
			if queueWhileRunning {
				return sendStateQueue
			}
			return sendStateSteer
		}
		return sendStateStop
	}
	// 非运行状态下按钮只表达「发送」：有草稿可发，空草稿禁用；canContinue 不再影响按钮形态。
	return sendStateSend
}

// runningSendDraftState 在 computeSendDraftState 的基础上叠加「运行中的消息插入当前回合」开关：
// 开启后运行中有内容的发送改为插入当前回合（服务商不支持时由后端退回排队），
// 未开启时保持原有行为（排队或中断重发）。
func runningSendDraftState(isRunning, hasContent, canContinue, queueWhileRunning, insertIntoRunningTurn, editingQueued bool) sendDraftState {
	state := computeSendDraftState(isRunning, hasContent, canContinue, queueWhileRunning, editingQueued)
	if insertIntoRunningTurn && (state == sendStateQueue || state == sendStateSteer) {
		return sendStateInject
	}
	return state
}

// queueUpdater 声明支持原地更新排队跟进项的客户端接口。
type queueUpdater interface {
	UpdateQueued(ctx context.Context, threadID, followUpID, text string) error
}

type queueAttachmentEditor interface {
	QueuedAttachments(ctx context.Context, threadID, followUpID string) ([]Attachment, error)
	UpdateQueuedWithAttachments(ctx context.Context, threadID, followUpID, text string, attachmentIDs []string) error
}

// steerSender 声明支持中断当前运行并立即发送转向纠偏消息的客户端接口。
type steerSender interface {
	InterruptAndSend(ctx context.Context, threadID, text string, attachmentIDs []string) error
}

// canRecallTurn 检查消息时间线末尾是否为尚未产生助手输出的用户提示词。
func canRecallTurn(msgs []Message) bool {
	if len(msgs) == 0 {
		return false
	}
	return msgs[len(msgs)-1].Role == "user"
}
