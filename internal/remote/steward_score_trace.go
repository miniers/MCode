package remote

import (
	"context"
	"log"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// stewardScoreTraceTimeout 是建议计分写库的超时；写库失败只记日志，不影响审批主链路。
const stewardScoreTraceTimeout = 3 * time.Second

// stewardScoreTraceRun 决定计分写库的执行方式：默认丢给独立 goroutine，调用方完全不等待；
// 测试里替换成同步执行，用返回值直接断言，不靠 sleep。
var stewardScoreTraceRun = func(fn func()) {
	go fn()
}

// ApprovalRoleFamily 把选项 Role 或选项 ID 归一为 allow / deny；
// allow 与 allow_always 视为同一家族：建议"允许一次"、用户选"始终允许"算一致。
// 无法识别时返回空串，这类样本不参与一致率比较（只在日志里提示）。
func ApprovalRoleFamily(role, id string) string {
	switch role {
	case "allow", "approve", "approveAlways", "allow_always":
		return "allow"
	case "deny", "decline", "reject":
		return "deny"
	}
	switch id {
	case "allow", "allow_always":
		return "allow"
	case "deny":
		return "deny"
	}
	return ""
}

// optionRoleFor 从请求选项里找出该选项 ID 的 Role；找不到返回空串。
func optionRoleFor(options []wire.RemoteRequestOption, optionID string) string {
	for _, option := range options {
		if option.ID == optionID {
			return option.Role
		}
	}
	return ""
}

// stewardScoreForResponse 在锁内拼一份答复计分快照：只读 pendingRequest 的请求与工具名，不碰其它状态。
// 没有注入来源时按 direct 处理，桌面两条答复路径因此不用改。
func stewardScoreForResponse(req wire.RemoteRequest, toolName, requestID, chosenOption, source string) store.StewardApprovalScore {
	if source == "" {
		source = AnswerSourceDirect
	}
	return store.StewardApprovalScore{
		RequestID:    requestID,
		Kind:         req.Kind,
		ToolName:     toolName,
		ChosenOption: chosenOption,
		ChosenRole:   ApprovalRoleFamily(optionRoleFor(req.Options, chosenOption), chosenOption),
		AnswerSource: source,
	}
}

// TraceSuggestionRegistered 由 app 层登记一条总管建议。
// store 未挂载或请求 ID 为空时静默跳过；写库失败只记日志，绝不影响 SuggestApproval 的返回值。
func TraceSuggestionRegistered(b *MemoryBackend, score store.StewardApprovalScore) {
	stewardScoreTrace(b, score.RequestID, "register", func(ctx context.Context, s *store.Store) error {
		return s.UpsertStewardApprovalScore(ctx, score)
	})
}

// traceSuggestionAnswered 在答复成功后结算计分行：角色一致记 agree，否则记 disagree。
// 没有建议行或已被消失路径先结算时影响 0 行，属正常情况，不记日志、不重试。
func traceSuggestionAnswered(b *MemoryBackend, score store.StewardApprovalScore) {
	stewardScoreTrace(b, score.RequestID, "answer", func(ctx context.Context, s *store.Store) error {
		return s.RecordStewardAnswer(ctx, score.RequestID, score.Kind, score.ToolName, score.ChosenOption, score.ChosenRole, score.AnswerSource)
	})
}

// traceSuggestionUnanswered 把仍待定的计分行记为未答复；已结算的行靠 outcome='pending' 条件保持原样。
func traceSuggestionUnanswered(b *MemoryBackend, requestID, reason string) {
	stewardScoreTrace(b, requestID, "unanswered", func(ctx context.Context, s *store.Store) error {
		return s.MarkStewardScoreUnanswered(ctx, requestID, reason)
	})
}

// stewardScoreTrace 是三个写点共用的执行外壳：空值判断、超时、错误日志只写一处。
// b.store 与其它读点一致按不可变字段读取（只在 AttachStore 里赋值一次），不额外抢 b.mu。
func stewardScoreTrace(b *MemoryBackend, requestID, stage string, action func(context.Context, *store.Store) error) {
	if b == nil || requestID == "" {
		return
	}
	s := b.store
	if s == nil {
		return
	}
	stewardScoreTraceRun(func() {
		// 超时上下文在真正执行时创建，避免排队等待把预算吃完。
		ctx, cancel := context.WithTimeout(context.Background(), stewardScoreTraceTimeout)
		defer cancel()
		if err := action(ctx, s); err != nil {
			log.Printf("trace steward score %s %s: %v", requestID, stage, err)
		}
	})
}
