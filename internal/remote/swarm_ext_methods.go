package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"mcode/internal/remote/wire"
)

// 本文件是编排扩展方法的远程实现：桌面端的续跑横幅（ui/swarm_resume_ui.go）依赖
// Client.ResumeInterruptedSwarm / Client.DismissInterruptedSwarm，移动端此前只能看到 bee 列表，
// 因此这里把中断条目查询、续跑、放弃续跑三条能力开放给 Web 端。
//
// threads.swarm.retryBee 对应失败或已停止 bee 的「沿用改动续做」：Manager.RetryBee 复用 worktree
// 重派的逻辑与自动重试分支同源，一个 bee 可以手动重派，不需要等到下一次自动重试。

// SwarmExtService 定义「重启中断续跑」所需的编排能力，由 internal/app 注入实现。
// 未注入时这些 RPC 统一返回 MethodNotFound，前端据此隐藏续跑横幅与重试入口。
type SwarmExtService interface {
	// Interrupted 返回条目总数与其中被打断 bee 的明细；count 与桌面端 SwarmState.Interrupted 同源。
	Interrupted(ctx context.Context, leadThreadID string) ([]wire.SwarmExtInterruptedBee, int, error)
	// ResumeInterrupted 续跑被打断的 bee 并放出重启前的排队条目，返回成功条目数。
	ResumeInterrupted(ctx context.Context, leadThreadID string) (int, error)
	// DismissInterrupted 放弃续跑：只清中断标记与排队条目，不删任何文件，返回清理条目数。
	DismissInterrupted(ctx context.Context, leadThreadID string) (int, error)
	// RetryBee 沿用指定失败或已停止 bee 的原 worktree 与半成品改动重新派发；
	// threadID 可以是 bee 自己的会话，实现侧按 BeeOwner 归一成所属主会话。
	RetryBee(ctx context.Context, threadID, beeThreadID string) error
	// ReportFull 从汇报存档读取被截断汇报的全文。bee 不属于 threadID 对应主会话时返回 invalid params，
	// 存档读不到时返回明确的错误（不回空串），前端据此提示而不是渲染空白。
	ReportFull(ctx context.Context, threadID, beeID string) (wire.SwarmExtReportResult, error)
}

// swarmExtServiceName 是 SwarmExtService 在服务注册表里的名字，与 internal/app 的注入保持一致。
const swarmExtServiceName = "swarmExt"

func init() {
	RegisterMethod("threads.swarm.interrupted", handleThreadsSwarmInterrupted)
	RegisterMethod("threads.swarm.resume", handleThreadsSwarmResume)
	RegisterMethod("threads.swarm.dismissInterrupted", handleThreadsSwarmDismissInterrupted)
	RegisterMethod("threads.swarm.retryBee", handleThreadsSwarmRetryBee)
	RegisterMethod("threads.swarm.report", handleThreadsSwarmReport)
}

// handleThreadsSwarmInterrupted 返回主会话下因重启中断的条目数与 bee 明细。
func handleThreadsSwarmInterrupted(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SwarmExtThreadParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	svc, ok := swarmExtService(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	bees, count, err := svc.Interrupted(ctx, p.ThreadID)
	if err != nil {
		return nil, swarmExtWrapError(err)
	}
	return wire.SwarmExtInterruptedResult{Count: count, Bees: bees}, nil
}

// handleThreadsSwarmResume 续跑被打断的子代理；成功后补一次编排状态推送。
func handleThreadsSwarmResume(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SwarmExtThreadParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	svc, ok := swarmExtService(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	n, err := svc.ResumeInterrupted(ctx, p.ThreadID)
	if err != nil {
		return nil, swarmExtWrapError(err)
	}
	swarmExtNotifyChanged(d, p.ThreadID)
	return wire.SwarmExtResumeResult{Resumed: n}, nil
}

// handleThreadsSwarmDismissInterrupted 放弃续跑被打断的子代理；成功后补一次编排状态推送。
func handleThreadsSwarmDismissInterrupted(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SwarmExtThreadParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	svc, ok := swarmExtService(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	n, err := svc.DismissInterrupted(ctx, p.ThreadID)
	if err != nil {
		return nil, swarmExtWrapError(err)
	}
	swarmExtNotifyChanged(d, p.ThreadID)
	return wire.SwarmExtDismissResult{Dismissed: n}, nil
}

// handleThreadsSwarmRetryBee 让指定失败或已停止的子代理沿用原 worktree 与半成品改动重新派发；
// 成功后与续跑一样补一次编排状态推送，前端按住 changed 通知刷新 bee 列表。
func handleThreadsSwarmRetryBee(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SwarmExtRetryBeeParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.BeeThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'beeThreadID' must be a valid UUID")
	}
	svc, ok := swarmExtService(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	// 参数里的 threadID 可能是 bee 自己的会话，服务侧按 BeeOwner 归一成主会话。
	if err := svc.RetryBee(ctx, p.ThreadID, p.BeeThreadID); err != nil {
		return nil, swarmExtWrapError(err)
	}
	swarmExtNotifyChanged(d, p.ThreadID)
	return wire.SwarmExtRetryBeeResult{OK: true}, nil
}

// handleThreadsSwarmReport 读取被截断汇报的全文：移动端拿到的是截断文本，
// 这里按 threadID + beeID 取存档里的完整汇报；参数非法时直接拒绝，不落到服务层。
func handleThreadsSwarmReport(ctx context.Context, d *Dispatcher, c Conn, params json.RawMessage) (any, error) {
	var p wire.SwarmExtReportParams
	if err := decodeExtParams(params, &p); err != nil {
		return nil, err
	}
	if !isValidUUID(p.ThreadID) {
		return nil, ExtInvalidParams("Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.BeeID) {
		return nil, ExtInvalidParams("Invalid params: 'beeID' must be a valid UUID")
	}
	svc, ok := swarmExtService(d)
	if !ok {
		return nil, ExtNotImplemented()
	}
	res, err := svc.ReportFull(ctx, p.ThreadID, p.BeeID)
	if err != nil {
		return nil, swarmExtWrapError(err)
	}
	return res, nil
}

// swarmExtService 取出 app 层注入的续跑服务；未接线或类型不符时返回 false。
func swarmExtService(d *Dispatcher) (SwarmExtService, bool) {
	if d == nil {
		return nil, false
	}
	return backendService[SwarmExtService](d.backend, swarmExtServiceName)
}

// swarmExtNotifyChanged 补发一次编排状态变更。
// 续跑与放弃都会触发 Manager 的变更回调，app 层的 Swarm 订阅本来就会推送 threads.swarm.changed；
// 这里再调一次是兜底（未起订阅时前端也能拿到最新状态），NotifySwarmChanged 对同一 lead 有时间窗去重，
// 因此两种情况都只会推送一次，载荷与内置方法推送的 wire.SwarmState 完全一致。
func swarmExtNotifyChanged(d *Dispatcher, leadThreadID string) {
	if d == nil || strings.TrimSpace(leadThreadID) == "" {
		return
	}
	d.NotifySwarmChanged(leadThreadID)
}

// swarmExtWrapError 把业务错误转成可读的拒绝错误，避免统一按内部错误脱敏后前端只看到一条笼统提示。
func swarmExtWrapError(err error) error {
	if err == nil {
		return nil
	}
	var extErr *ExtError
	if errors.As(err, &extErr) {
		return extErr
	}
	return &ExtError{Code: wire.ErrCodeOperationRejected, Message: err.Error()}
}
