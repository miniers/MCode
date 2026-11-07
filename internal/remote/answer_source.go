package remote

import "context"

// answerSourceKey 是答复来源在 context 里的键；只在进程内传递，不进 wire 契约。
type answerSourceKey struct{}

// 答复来源：记录用户从哪条路径答复了请求，供总管建议计分统计。
const (
	AnswerSourceDirect  = "direct"  // 桌面卡片 / 桌面收件箱；缺省值
	AnswerSourceConfirm = "confirm" // 网页收件箱确认总管建议
	AnswerSourceRemote  = "remote"  // 网页在审批卡片上直接答
	AnswerSourceAuto    = "auto"    // 子线程提问的内部自动转发
)

// WithAnswerSource 标记本次答复来自哪条路径。用 context 而不是扩 RPC 参数：
// wire 是公开契约，为一个内部统计字段加字段不值得，且所有答复路径都已经透传 ctx。
// src 为空时原样返回，避免把空串写进 context 让下游误以为有值。
func WithAnswerSource(ctx context.Context, src string) context.Context {
	if ctx == nil || src == "" {
		return ctx
	}
	return context.WithValue(ctx, answerSourceKey{}, src)
}

// AnswerSourceOf 返回注入的答复来源；未注入时返回空串，由调用方回退到 direct。
func AnswerSourceOf(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	src, _ := ctx.Value(answerSourceKey{}).(string)
	return src
}
