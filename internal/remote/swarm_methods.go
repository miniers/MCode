package remote

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/swarm"
)

const swarmBeeTimeLayout = "2006-01-02T15:04:05.000Z07:00"

// SwarmService 定义 Swarm 多 Agent 协作管理器业务接口。
type SwarmService interface {
	Settings() swarm.Settings
	SetEnabled(lead string, enabled bool) swarm.State
	State(lead string) swarm.State
	BeeOwner(threadID string) (string, bool)
	StopBee(ctx context.Context, lead, bee string) error
	StopAll(ctx context.Context, lead string) error
	ClearFinished(ctx context.Context, lead string) error
}

// SwarmRemoteExtras 定义 Swarm 编排在远程协议下的可选扩展能力。
// 未注入时，依赖它的 RPC 统一返回「编排服务不可用」，不影响已注册的基础 Swarm 方法。
type SwarmRemoteExtras interface {
	// PendingBeeQuestion 返回 bee 当前待回答的提问，没有提问时返回 nil。
	PendingBeeQuestion(ctx context.Context, beeThreadID string) *wire.SwarmBeeQuestion
	// ForwardBeeQuestion 把 bee 的提问转交主线程回答。
	ForwardBeeQuestion(ctx context.Context, leadThreadID, beeThreadID, requestID string) error
	// SwarmMergeDiff 返回 bee 待审查的补丁、是否被截断以及改动文件列表。
	SwarmMergeDiff(ctx context.Context, leadThreadID, beeThreadID string) (diff string, truncated bool, files []string, err error)
	// ApproveSwarmMerge 批准并应用 bee 的改动。
	ApproveSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error
	// DiscardSwarmMerge 放弃 bee 的改动。
	DiscardSwarmMerge(ctx context.Context, leadThreadID, beeThreadID string) error
	// SetSwarmPlansOnly 设置主会话的「只规划」覆盖；v 为 nil 表示清除覆盖。
	SetSwarmPlansOnly(ctx context.Context, threadID string, v *bool) error
	// SwarmPlansOnly 返回主会话当前生效的「只规划」取值，以及是否来自会话级覆盖。
	SwarmPlansOnly(leadThreadID string) (value bool, overridden bool)
	// SwarmTiers 返回当前的 Bee 档位列表。
	SwarmTiers() []wire.SwarmTier
	// SaveSwarmTiers 整表替换档位配置并返回生效后的列表。
	SaveSwarmTiers(ctx context.Context, tiers []wire.SwarmTier) ([]wire.SwarmTier, error)
}

// SetSwarmExtras 设置 Dispatcher 使用的 Swarm 扩展能力实现。
func (d *Dispatcher) SetSwarmExtras(e SwarmRemoteExtras) {
	d.swarmExtras = e
}

// SetSwarm 设置 Dispatcher 使用的 Swarm 服务实例。
func (d *Dispatcher) SetSwarm(h SwarmService) {
	d.swarm = h
	// 主会话摘要的 SwarmBusy 由 Manager 判定，供完成提醒只在整批落定后触发一次。
	// 这里用可选接口探测，不扩大 SwarmService 的约束。
	store, okStore := d.backend.(interface{ SetSwarmBusy(func(string) bool) })
	judge, okJudge := h.(interface{ Busy(string) bool })
	if okStore && okJudge {
		store.SetSwarmBusy(judge.Busy)
	}
}

// swarmMergeState 按优先级汇总 bee 的合并进度：审查中、审查未通过、落地等待、已放弃、已合入、失败，其余为空。
func swarmMergeState(h swarm.Bee) string {
	switch {
	case h.ReviewState == swarm.ReviewRunning:
		return "reviewing"
	case h.ReviewState == swarm.ReviewRejected:
		return "rejected"
	case h.PendingMerge:
		return "pending"
	case h.MergeDiscarded:
		return "discarded"
	case h.Merged:
		return "merged"
	case h.MergeError != "" || h.AutoMergeError != "" || h.AutoMergeState == "failed":
		return "failed"
	}
	return ""
}

// swarmBeeToWire 将单个内部 bee 转换为 wire.SwarmBee。
func swarmBeeToWire(h swarm.Bee) wire.SwarmBee {
	// 落地等待中或待审查的 bee 不接受停止；只有运行中且未进入这两个状态的 bee 可停。
	canStop := h.Status == swarm.StatusRunning && !h.PendingMerge && h.MergeWaitSince == nil
	canOpen := true

	var finishedAt *string
	if h.FinishedAt != nil {
		s := h.FinishedAt.UTC().Format(swarmBeeTimeLayout)
		finishedAt = &s
	}

	// 手动合并错误优先，空时回退到自动合并流水线的错误说明。
	mergeError := h.MergeError
	if mergeError == "" {
		mergeError = h.AutoMergeError
	}

	var credibility *wire.SwarmCredibility
	if h.Credibility != nil {
		credibility = &wire.SwarmCredibility{
			Cited:      h.Credibility.Cited,
			Verified:   h.Credibility.Verified,
			Unverified: append([]string(nil), h.Credibility.Unverified...),
			NotRead:    append([]string(nil), h.Credibility.NotRead...),
		}
	}

	bee := wire.SwarmBee{
		ThreadID:          h.ThreadID,
		Index:             h.Index,
		Name:              h.Name,
		Task:              h.Task,
		Kind:              "native",
		Origin:            "delegated",
		Status:            string(h.Status),
		Summary:           h.Summary,
		Activity:          h.Activity,
		ToolCalls:         h.ToolCalls,
		Tokens:            h.Tokens,
		StartedAt:         h.StartedAt.UTC().Format(swarmBeeTimeLayout),
		FinishedAt:        finishedAt,
		Provider:          h.Provider,
		Model:             h.Model,
		Tier:              h.Tier,
		ReadOnly:          h.ReadOnly,
		DirectModel:       h.DirectModel,
		ShadowRepo:        h.ShadowRepo,
		SharedNoGit:       h.SharedNoGit,
		SharedNoGitReason: h.SharedNoGitReason,
		CanOpen:           canOpen,
		CanStop:           canStop,
		MergeState:        swarmMergeState(h),
		ReviewState:       string(h.ReviewState),
		ReviewReason:      h.ReviewReason,
		ReviewThreadID:    h.ReviewThreadID,
		ConflictFiles:     append([]string(nil), h.ConflictFiles...),
		ChangedFiles:      append([]string(nil), h.ChangedFiles...),
		MergeError:        mergeError,
		LinesAdded:        h.LinesAdded,
		LinesDeleted:      h.LinesDeleted,
		Credibility:       credibility,
	}
	applyReviewFunnelWire(&bee, h.ReviewLayer, h.ReviewConcerns, h.ReviewTokens, h.ReviewTokensEstimated)
	return bee
}

// applyReviewFunnelWire 把合并审查漏斗的结果写到 wire 层。
// concerns 必须深拷贝，避免远程快照与 swarm.Bee 共享底层数组后被并发改写。
func applyReviewFunnelWire(dst *wire.SwarmBee, layer string, concerns []string, tokens int, estimated bool) {
	if dst == nil {
		return
	}
	dst.ReviewLayer = layer
	dst.ReviewConcerns = append([]string(nil), concerns...)
	dst.ReviewTokens = tokens
	dst.ReviewTokensEstimated = estimated
}

// swarmStateToWire 将内部 swarm.State 转换为符合 JSON-RPC 协议契约的 wire.SwarmState。
// bee 线程只返回自己的 bee 条目且 bees 为空，避免远程客户端重复渲染整个 bee 队列。
func swarmStateToWire(threadID string, st swarm.State, svc SwarmService) wire.SwarmState {
	// swarmAvailable 反映全局 Swarm 开关；服务缺失时保持可用。
	swarmAvailable := true
	if svc != nil {
		swarmAvailable = svc.Settings().Enabled
	}

	isBee := false
	leadID := ""
	bees := st.Bees
	swarmEnabled := st.Enabled
	// queued 在 bee 线程上取所属 lead 的派发队列长度
	queued := st.Queued

	if svc != nil {
		if lead, ok := svc.BeeOwner(threadID); ok {
			isBee = true
			leadID = lead
			leadState := svc.State(lead)
			bees = leadState.Bees
			swarmEnabled = leadState.Enabled
			queued = leadState.Queued
		}
	}

	if isBee {
		var beePtr *wire.SwarmBee
		for i := range bees {
			if bees[i].ThreadID != threadID {
				continue
			}
			wh := swarmBeeToWire(bees[i])
			beePtr = &wh
			break
		}
		return wire.SwarmState{
			ThreadID:       threadID,
			SwarmAvailable: swarmAvailable,
			SwarmEnabled:   swarmEnabled,
			IsBee:          true,
			LeadThreadID:   leadID,
			Bee:            beePtr,
			Bees:           []wire.SwarmBee{},
			Queued:         queued,
		}
	}

	wireBees := make([]wire.SwarmBee, 0, len(bees))
	for i := range bees {
		wireBees = append(wireBees, swarmBeeToWire(bees[i]))
	}

	return wire.SwarmState{
		ThreadID:       threadID,
		SwarmAvailable: swarmAvailable,
		SwarmEnabled:   swarmEnabled,
		IsBee:          false,
		Bees:           wireBees,
		Queued:         queued,
	}
}

// swarmWireState 在 swarmStateToWire 的基础上补齐扩展字段：只规划开关与各 bee 的待答提问。
func (d *Dispatcher) swarmWireState(ctx context.Context, threadID string, st swarm.State) wire.SwarmState {
	res := swarmStateToWire(threadID, st, d.swarm)
	if d.swarmExtras == nil {
		return res
	}

	leadID := res.LeadThreadID
	if leadID == "" {
		leadID = threadID
	}
	res.PlansOnly, res.PlansOnlyOverridden = d.swarmExtras.SwarmPlansOnly(leadID)

	if res.Bee != nil && res.Bee.Status == string(swarm.StatusRunning) {
		if q := d.swarmExtras.PendingBeeQuestion(ctx, res.Bee.ThreadID); q != nil {
			res.Bee.PendingQuestion = q
		}
	}
	for i := range res.Bees {
		// 提问只可能出现在运行中的 bee 上：每次都给全部 bee（包括已经结束的）查一遍审批表，
		// 开销会随累计 bee 总数线性增长，拖慢推送。
		if res.Bees[i].Status != string(swarm.StatusRunning) {
			continue
		}
		if q := d.swarmExtras.PendingBeeQuestion(ctx, res.Bees[i].ThreadID); q != nil {
			res.Bees[i].PendingQuestion = q
		}
	}
	return res
}

// swarmLeadID 把 bee 线程归一为其所属主会话 ID，非 bee 时原样返回。
func (d *Dispatcher) swarmLeadID(threadID string) string {
	if d.swarm != nil {
		if lead, ok := d.swarm.BeeOwner(threadID); ok {
			return lead
		}
	}
	return threadID
}

func mapSwarmError(err error, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	if errors.Is(err, swarm.ErrBeeNotFound) || errors.Is(err, swarm.ErrLeadNotFound) {
		return replyErr(wire.ErrCodeResourceNotFound, err.Error())
	}
	return replyWithBackendErr(err)
}

func (d *Dispatcher) handleThreadsSwarmGet(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmGetParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}

	if d.swarm == nil {
		return replySuccess(wire.SwarmState{
			ThreadID:       p.ThreadID,
			SwarmAvailable: false,
			SwarmEnabled:   false,
			IsBee:          false,
			Bees:           []wire.SwarmBee{},
		})
	}

	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	st := d.swarm.State(p.ThreadID)
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

func (d *Dispatcher) handleThreadsSwarmSet(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmSetParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}

	if d.swarm == nil {
		return replyErr(wire.ErrCodeOperationRejected, "swarm service unavailable")
	}

	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	st := d.swarm.SetEnabled(p.ThreadID, p.Enabled)
	leadID := p.ThreadID
	if lead, ok := d.swarm.BeeOwner(p.ThreadID); ok {
		leadID = lead
	}
	d.NotifySwarmChanged(leadID)

	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

func (d *Dispatcher) handleThreadsSwarmSetBeeTiers(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmSetBeeTiersParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}

	if setter, ok := d.backend.(interface {
		SetThreadBeeTiers(ctx context.Context, threadID, beeTiers string) error
	}); ok {
		if err := setter.SetThreadBeeTiers(ctx, p.ThreadID, p.BeeTiers); err != nil {
			return replyWithBackendErr(err)
		}
	} else if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	leadID := p.ThreadID
	if d.swarm != nil {
		if lead, ok := d.swarm.BeeOwner(p.ThreadID); ok {
			leadID = lead
		}
		d.NotifySwarmChanged(leadID)
		st := d.swarm.State(leadID)
		return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
	}

	return replySuccess(wire.SwarmState{
		ThreadID:       p.ThreadID,
		SwarmAvailable: false,
		SwarmEnabled:   false,
		IsBee:          false,
		Bees:           []wire.SwarmBee{},
	})
}

func (d *Dispatcher) handleThreadsSwarmStopBee(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmStopBeeParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.BeeThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'beeThreadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}

	if d.swarm == nil {
		return replyErr(wire.ErrCodeOperationRejected, "swarm service unavailable")
	}

	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	if err := d.swarm.StopBee(ctx, p.ThreadID, p.BeeThreadID); err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}

	st := d.swarm.State(p.ThreadID)
	d.NotifySwarmChanged(p.ThreadID)
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

func (d *Dispatcher) handleThreadsSwarmStopAll(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmBatchParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}

	if d.swarm == nil {
		return replyErr(wire.ErrCodeOperationRejected, "swarm service unavailable")
	}

	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	if err := d.swarm.StopAll(ctx, p.ThreadID); err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}

	st := d.swarm.State(p.ThreadID)
	d.NotifySwarmChanged(p.ThreadID)
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

func (d *Dispatcher) handleThreadsSwarmClearFinished(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmBatchParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}

	if d.swarm == nil {
		return replyErr(wire.ErrCodeOperationRejected, "swarm service unavailable")
	}

	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	if err := d.swarm.ClearFinished(ctx, p.ThreadID); err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}

	st := d.swarm.State(p.ThreadID)
	d.NotifySwarmChanged(p.ThreadID)
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

// swarmThreadModes 是远程可设置的会话级 Swarm 工作模式取值集合。
var swarmThreadModes = map[string]struct{}{
	swarm.ModeInherited:     {},
	swarm.ModeCollaborative: {},
	swarm.ModeLeadPlansOnly: {},
	swarm.ModeAlwaysBees:    {},
}

// handleThreadsSwarmSetPlansOnly 处理 threads.hydra.setPlansOnly：plansOnly 为 null 表示清除会话级覆盖。
func (d *Dispatcher) handleThreadsSwarmSetPlansOnly(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmSetPlansOnlyParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}
	if d.swarm == nil || d.swarmExtras == nil {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}
	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	if err := d.swarmExtras.SetSwarmPlansOnly(ctx, p.ThreadID, p.PlansOnly); err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}

	st := d.swarm.State(p.ThreadID)
	d.NotifySwarmChanged(d.swarmLeadID(p.ThreadID))
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

// handleThreadsSwarmSetMode 处理 threads.hydra.setMode：保存会话级 Swarm 工作模式并返回更新后的摘要。
func (d *Dispatcher) handleThreadsSwarmSetMode(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmSetModeParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}
	if _, ok := swarmThreadModes[p.Mode]; !ok {
		return replyErr(wire.InvalidParams, "Invalid params: 'mode' must be one of inherited, collaborative, leadPlansOnly, alwaysBees")
	}

	// 工作模式只写穿会话摘要，桌面端 Client.SetThreadSwarmMode 没有额外副作用，因此直接调用 backend。
	setter, ok := d.backend.(interface {
		SetThreadSwarmMode(ctx context.Context, threadID, mode string) (wire.ThreadSummary, error)
	})
	if !ok {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}
	summary, err := setter.SetThreadSwarmMode(ctx, d.swarmLeadID(p.ThreadID), p.Mode)
	if err != nil {
		return replyWithBackendErr(err)
	}

	d.NotifySwarmChanged(d.swarmLeadID(p.ThreadID))
	return replySuccess(summary)
}

// handleThreadsSwarmForwardQuestion 处理 threads.hydra.forwardQuestion：把 bee 的提问转交主线程回答。
func (d *Dispatcher) handleThreadsSwarmForwardQuestion(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmForwardQuestionParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.BeeThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'beeThreadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.RequestID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'requestID' is required")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}
	if d.swarm == nil || d.swarmExtras == nil {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}
	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	leadID := d.swarmLeadID(p.ThreadID)
	if err := d.swarmExtras.ForwardBeeQuestion(ctx, leadID, p.BeeThreadID, p.RequestID); err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}

	st := d.swarm.State(p.ThreadID)
	d.NotifySwarmChanged(leadID)
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

// handleThreadsSwarmMergeDiff 处理 threads.hydra.mergeDiff：返回 bee 待审查的补丁与改动文件。
func (d *Dispatcher) handleThreadsSwarmMergeDiff(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmMergeDiffParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.BeeThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'beeThreadID' must be a valid UUID")
	}
	if d.swarm == nil || d.swarmExtras == nil {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}
	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	diff, truncated, files, err := d.swarmExtras.SwarmMergeDiff(ctx, d.swarmLeadID(p.ThreadID), p.BeeThreadID)
	if err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}
	if files == nil {
		files = []string{}
	}
	return replySuccess(wire.SwarmMergeDiffResult{Diff: diff, Truncated: truncated, FilesChanged: files})
}

// handleThreadsSwarmApproveMerge 处理 threads.hydra.approveMerge：批准并应用 bee 的改动。
func (d *Dispatcher) handleThreadsSwarmApproveMerge(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmMergeActionParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.BeeThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'beeThreadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}
	if d.swarm == nil || d.swarmExtras == nil {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}
	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	leadID := d.swarmLeadID(p.ThreadID)
	if err := d.swarmExtras.ApproveSwarmMerge(ctx, leadID, p.BeeThreadID); err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}

	st := d.swarm.State(p.ThreadID)
	d.NotifySwarmChanged(leadID)
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

// handleThreadsSwarmDiscardMerge 处理 threads.hydra.discardMerge：放弃 bee 的改动。
func (d *Dispatcher) handleThreadsSwarmDiscardMerge(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmMergeActionParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if !isValidUUID(p.ThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'threadID' must be a valid UUID")
	}
	if !isValidUUID(p.BeeThreadID) {
		return replyErr(wire.InvalidParams, "Invalid params: 'beeThreadID' must be a valid UUID")
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}
	if d.swarm == nil || d.swarmExtras == nil {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}
	if d.backend != nil {
		if _, err := d.backend.GetThread(ctx, p.ThreadID); err != nil {
			return replyWithBackendErr(err)
		}
	}

	leadID := d.swarmLeadID(p.ThreadID)
	if err := d.swarmExtras.DiscardSwarmMerge(ctx, leadID, p.BeeThreadID); err != nil {
		return mapSwarmError(err, replyErr, replyWithBackendErr)
	}

	st := d.swarm.State(p.ThreadID)
	d.NotifySwarmChanged(leadID)
	return replySuccess(d.swarmWireState(ctx, p.ThreadID, st))
}

// handleSwarmTiersGet 处理 swarm.tiers.get：返回当前 Bee 档位配置。
func (d *Dispatcher) handleSwarmTiersGet(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	if !checkEmptyParams(params) {
		return replyErr(wire.InvalidParams, "Invalid params: 'swarm.tiers.get' takes no parameters")
	}
	if d.swarmExtras == nil {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}

	tiers := d.swarmExtras.SwarmTiers()
	if tiers == nil {
		tiers = []wire.SwarmTier{}
	}
	return replySuccess(wire.SwarmTiersResult{Tiers: tiers})
}

// handleSwarmTiersSet 处理 swarm.tiers.set：整表替换档位配置，校验规则与桌面端设置页一致。
func (d *Dispatcher) handleSwarmTiersSet(ctx context.Context, params json.RawMessage, replySuccess func(any) *wire.Response, replyErr func(int, string) *wire.Response, replyWithBackendErr func(error) *wire.Response) *wire.Response {
	var p wire.SwarmTiersSetParams
	if err := decodeStrict(params, &p); err != nil {
		return replyErr(wire.InvalidParams, "Invalid params: "+err.Error())
	}
	if strings.TrimSpace(p.ActionID) == "" {
		return replyErr(wire.InvalidParams, "Invalid params: 'actionID' is required")
	}
	if p.Tiers == nil {
		return replyErr(wire.InvalidParams, "Invalid params: 'tiers' is required")
	}
	if d.swarmExtras == nil {
		return replyErr(wire.ErrCodeOperationRejected, "编排服务不可用")
	}

	tiers, err := d.swarmExtras.SaveSwarmTiers(ctx, p.Tiers)
	if err != nil {
		return replyErr(wire.ErrCodeOperationRejected, err.Error())
	}
	if tiers == nil {
		tiers = []wire.SwarmTier{}
	}
	return replySuccess(wire.SwarmTiersResult{Tiers: tiers})
}

// NotifySwarmChanged 按 leadThreadID 做聚合广播，同一 lead 在时间窗口内只广播一次。
func (d *Dispatcher) NotifySwarmChanged(leadThreadID string) {
	if d.swarm == nil || leadThreadID == "" {
		return
	}

	interval := d.swarmNotifyInterval
	if interval <= 0 {
		interval = time.Second
	}

	d.swarmNotifyMu.Lock()
	if d.swarmNotifyTimers == nil {
		d.swarmNotifyTimers = make(map[string]*time.Timer)
	}
	if _, exists := d.swarmNotifyTimers[leadThreadID]; exists {
		d.swarmNotifyMu.Unlock()
		return
	}
	d.swarmNotifyTimers[leadThreadID] = time.AfterFunc(interval, func() {
		d.flushSwarmNotification(leadThreadID)
	})
	d.swarmNotifyMu.Unlock()
}

func (d *Dispatcher) flushSwarmNotification(leadThreadID string) {
	d.swarmNotifyMu.Lock()
	delete(d.swarmNotifyTimers, leadThreadID)
	svc := d.swarm
	d.swarmNotifyMu.Unlock()

	if svc == nil {
		return
	}

	// 重新发布 lead 摘要：Swarm 状态变更不走会话事件，不重新发布就看不到
	// SwarmBusy 由忙转闲的那一刻，完成提醒会漏发或提前发。
	if hook, ok := d.backend.(interface{ RepublishThreadSummary(string) }); ok {
		hook.RepublishThreadSummary(leadThreadID)
	}

	st := svc.State(leadThreadID)
	wireState := d.swarmWireState(context.Background(), leadThreadID, st)

	d.authedMu.RLock()
	conns := make([]Conn, 0, len(d.authedConns))
	for c := range d.authedConns {
		conns = append(conns, c)
	}
	d.authedMu.RUnlock()

	for _, c := range conns {
		_ = c.Notify("threads.swarm.changed", wireState)
	}
}
