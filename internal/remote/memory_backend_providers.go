// 本文件包含 MemoryBackend 的请求处理与提供商方法。
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/startuptrace"
	"strings"
	"time"
)

// ListRequests 查询会话中待处理的交互与审批请求。
func (b *MemoryBackend) ListRequests(ctx context.Context, threadID string) (wire.RequestsListResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	td, ok := b.threads[threadID]
	if !ok {
		return wire.RequestsListResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}

	requests := make([]wire.RemoteRequest, 0, len(td.pending))
	for _, pr := range td.pending {
		requests = append(requests, pr.req)
	}

	return wire.RequestsListResult{
		ThreadID: threadID,
		Requests: requests,
	}, nil
}

// RespondRequest 答复指定会话中的待处理审批请求。
func (b *MemoryBackend) RespondRequest(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error) {
	// Answers 非空时解析为 {"<questionID>": ["值"]}，解析失败返回 InvalidParams
	var answers map[string][]string
	hasAnswers := len(params.Answers) > 0 && !bytes.Equal(bytes.TrimSpace(params.Answers), []byte("null"))
	if hasAnswers {
		if err := json.Unmarshal(params.Answers, &answers); err != nil {
			return wire.RequestsRespondResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "invalid answers: must be an object of string arrays",
			}
		}
	}

	// OptionID 不在 allow/allow_always/deny 之内返回 InvalidParams
	if params.OptionID != nil {
		opt := *params.OptionID
		if opt != "allow" && opt != "allow_always" && opt != "deny" {
			return wire.RequestsRespondResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "invalid optionID: must be allow, allow_always, or deny",
			}
		}
	}

	b.mu.Lock()
	locked := true
	defer func() {
		if locked {
			b.mu.Unlock()
		}
	}()

	td, ok := b.threads[params.ThreadID]
	if !ok {
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}

	pr, ok := td.pending[params.RequestID]
	if !ok {
		// 已处理的请求会从 pending 移除，重复答复（如另一台设备已处理）统一返回 -32010。
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeRequestAlreadyHandled,
			Message: "request not found or already handled",
		}
	}

	// 已经标记 deliveryError 的请求，允许用户再次答复重试：清空 deliveryError 后重新投递。
	// 如果底层 Provider 已经不再等待这条请求，就把它从列表移除，并返回 already resolved 或 stale 一类的现有错误码。
	if pr.req.DeliveryError != nil {
		providerStale := false
		if (pr.ctx != nil && pr.ctx.Err() != nil) || !td.summary.IsRunning || !pr.providerWaiting {
			providerStale = true
		}
		if providerStale {
			delete(td.pending, params.RequestID)
			if len(td.pending) == 0 {
				td.summary.HasPendingRequest = false
				if td.summary.IsRunning {
					td.summary.Attention = "running"
				} else {
					td.summary.Attention = "ready"
				}
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			td.summary.UpdatedAt = now
			staleSummary := b.summaryForPublishLocked(td)
			stalePending := make([]wire.RemoteRequest, 0, len(td.pending))
			for _, p := range td.pending {
				stalePending = append(stalePending, p.req)
			}
			locked = false
			b.mu.Unlock()

			b.hub.Publish(params.ThreadID, nil, nil, staleSummary)
			b.hub.Notify(params.ThreadID, "requests.changed", wire.RequestsChangedParams{
				ThreadID: params.ThreadID,
				Requests: stalePending,
			})

			return wire.RequestsRespondResult{}, &wire.RPCError{
				Code:    wire.ErrCodeRequestExpired,
				Message: "request is already resolved or stale",
			}
		}

	} else if pr.handled {
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeRequestAlreadyHandled,
			Message: "request not found or already handled",
		}
	}

	if (params.RunID != "" && params.RunID != pr.req.RunID) || (pr.ctx != nil && pr.ctx.Err() != nil) {
		return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request is no longer deliverable"}
	}

	kind := pr.req.Kind
	confirmsDelivery := pr.confirmsDelivery
	// 问答记录在锁外写入，先拷一份请求快照（题目列表只在创建时写入，浅拷贝足够）。
	questionReq := pr.req

	// 仅提问与计划类请求接受 answers
	if hasAnswers && kind != string(harness.RequestKindQuestion) && kind != string(harness.RequestKindPlan) {
		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("answers are not supported for %s requests", kind),
		}
	}
	var optID string
	if params.OptionID != nil {
		optID = *params.OptionID
	}
	isSkip := params.Skip != nil && *params.Skip

	switch kind {
	case string(harness.RequestKindQuestion):
		if (optID != "" && optID != "deny") || (hasAnswers && (optID != "" || isSkip)) {
			return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "questions require answers or skip"}
		}
		if !isSkip && optID != "deny" {
			if err := validateQuestionAnswers(pr.req.Questions, answers); err != nil {
				return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: err.Error()}
			}
		}
	case string(harness.RequestKindPlan):
		if optID == "allow_always" || (isSkip && (optID != "" || hasAnswers)) || (!isSkip && optID == "" && !hasAnswers) {
			return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "plans require approval or rejection"}
		}
		if hasAnswers && (optID == "allow" || len(answers) != 1 || len(answers["feedback"]) != 1 || strings.TrimSpace(answers["feedback"][0]) == "") {
			return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: "plan answers must contain rejection feedback"}
		}
	}

	ack := make(chan error, 1)
	deliveryAck := make(chan error, 1)
	select {
	case pr.resCh <- approvalResponse{optionID: optID, skip: isSkip, answers: answers, ack: ack}:
	default:
		return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request response queue is full"}
	}
	pr.req.DeliveryError = nil
	pr.deliveryAck = deliveryAck
	pr.awaitingAck = confirmsDelivery
	pr.respondedAt = time.Now()
	pr.handled = true
	done, providerCtx := pr.done, pr.ctx
	timeout := b.approvalDeliveryTimeout

	locked = false
	b.mu.Unlock()

	var timeoutCh chan struct{}
	if confirmsDelivery {
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		timeoutCh = make(chan struct{})
		stopTimer := b.scheduleAfter(timeout, func() { close(timeoutCh) })
		defer stopTimer()
	}
	if done != nil {
		select {
		case err := <-ack:
			if err != nil {
				return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request response was not received"}
			}
		case <-done:
			select {
			case err := <-ack:
				if err != nil {
					return wire.RequestsRespondResult{}, err
				}
			default:
				return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request ended before response was received"}
			}
		case <-ctx.Done():
			if !confirmsDelivery {
				return wire.RequestsRespondResult{}, ctx.Err()
			}
		case <-timeoutCh:
		}
	}

	// confirmsDelivery 为 false 时，行为与现在完全一致（Interact 已在收到答复后完成卡片清理与广播）
	if !confirmsDelivery {
		// 交互类请求的答复已交给 Provider，写进会话历史，前端重启后仍能回放。
		if kind == string(harness.RequestKindQuestion) {
			b.appendQuestionAnswer(params.ThreadID, questionReq, answers, isSkip || optID == "deny")
		} else if kind == string(harness.RequestKindPlan) {
			b.appendPlanDecision(params.ThreadID, questionReq, optID, answers)
		}
		return wire.RequestsRespondResult{
			ThreadID:  params.ThreadID,
			RequestID: params.RequestID,
			Kind:      kind,
			Status:    wire.RequestStatusAccepted,
		}, nil
	}

	// 接收与底层写入共用同一个期限，取消或超时也要保留送达错误。
	var failed bool
	var failReason string

	var prCtxDone <-chan struct{}
	if providerCtx != nil {
		prCtxDone = providerCtx.Done()
	}

	select {
	case err := <-deliveryAck:
		if err != nil {
			failed = true
			failReason = err.Error()
		}
	case <-timeoutCh:
		select {
		case err := <-deliveryAck:
			if err != nil {
				failed = true
				failReason = err.Error()
			}
		default:
			failed = true
			failReason = "delivery confirmation timed out"
		}
	case <-prCtxDone:
		select {
		case err := <-deliveryAck:
			if err != nil {
				failed = true
				failReason = err.Error()
			}
		default:
			failed = true
			failReason = "turn ended before delivery confirmation"
		}
	case <-ctx.Done():
		select {
		case err := <-deliveryAck:
			if err != nil {
				failed = true
				failReason = err.Error()
			}
		default:
			failed = true
			failReason = "request canceled before delivery confirmation"
		}
	}

	if failed {
		// 送达失败或超时：不移除这条请求，在 pendingRequest 上记录 deliveryError 文本，发布请求变化通知
		b.mu.Lock()
		// Provider 可能已经重新等待同一请求，旧答复不能覆盖新的送达状态。
		if pr.done == done && pr.deliveryAck == deliveryAck {
			pr.awaitingAck = false
			errText := failReason
			pr.req.DeliveryError = &errText
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		var failPending []wire.RemoteRequest
		var failSummary wire.ThreadSummary
		if td, ok := b.threads[params.ThreadID]; ok {
			td.summary.UpdatedAt = now
			failSummary = b.summaryForPublishLocked(td)
			failPending = make([]wire.RemoteRequest, 0, len(td.pending))
			for _, p := range td.pending {
				failPending = append(failPending, p.req)
			}
		}
		b.mu.Unlock()

		if failSummary.ID != "" {
			b.hub.Publish(params.ThreadID, nil, nil, failSummary)
			b.hub.Notify(params.ThreadID, "requests.changed", wire.RequestsChangedParams{
				ThreadID: params.ThreadID,
				Requests: failPending,
			})
		}

		return wire.RequestsRespondResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("delivery failed: %s", failReason),
		}
	}

	// 送达成功：维持现有成功路径，从 td.pending 中移除卡片，更新 summary，发布广播并返回 accepted
	interactive := kind == string(harness.RequestKindPlan) || kind == string(harness.RequestKindQuestion)
	b.mu.Lock()
	if pr.done != done || pr.deliveryAck != deliveryAck {
		b.mu.Unlock()
		return wire.RequestsRespondResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "request response was superseded"}
	}
	pr.awaitingAck = false
	if done != nil {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	td, ok = b.threads[params.ThreadID]
	var cleanupPending []wire.RemoteRequest
	var cleanupSummary wire.ThreadSummary
	if ok {
		delete(td.pending, params.RequestID)
		if len(td.pending) == 0 {
			td.summary.HasPendingRequest = false
			if td.summary.IsRunning {
				td.summary.Attention = "running"
			} else {
				td.summary.Attention = "ready"
			}
		}
		if optID == "allow_always" && !interactive && pr.allowKey != "" {
			if td.alwaysAllow == nil {
				td.alwaysAllow = make(map[string]bool)
			}
			td.alwaysAllow[pr.allowKey] = true
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		td.summary.UpdatedAt = now
		cleanupSummary = b.summaryForPublishLocked(td)

		cleanupPending = make([]wire.RemoteRequest, 0, len(td.pending))
		for _, p := range td.pending {
			cleanupPending = append(cleanupPending, p.req)
		}
	}
	b.mu.Unlock()

	if cleanupSummary.ID != "" {
		b.hub.Publish(params.ThreadID, nil, nil, cleanupSummary)
		b.hub.Notify(params.ThreadID, "requests.changed", wire.RequestsChangedParams{
			ThreadID: params.ThreadID,
			Requests: cleanupPending,
		})
	}

	// 送达确认路径：确认答复已经交给 Provider 之后再写交互记录。
	if kind == string(harness.RequestKindQuestion) {
		b.appendQuestionAnswer(params.ThreadID, questionReq, answers, isSkip || optID == "deny")
	} else if kind == string(harness.RequestKindPlan) {
		b.appendPlanDecision(params.ThreadID, questionReq, optID, answers)
	}

	return wire.RequestsRespondResult{
		ThreadID:  params.ThreadID,
		RequestID: params.RequestID,
		Kind:      kind,
		Status:    wire.RequestStatusAccepted,
	}, nil
}

// ListProviders 列出已注册的所有 Provider。
func (b *MemoryBackend) ListProviders(ctx context.Context) (wire.ProvidersListResult, error) {
	kinds := b.Providers()

	b.mu.Lock()
	harnesses := make(map[string]harness.Harness, len(b.harnesses))
	for k, h := range b.harnesses {
		harnesses[k] = h
	}
	swarmAvail := b.swarmAvailable
	defaultProvider := b.defaultProvider
	defaultRuntimeMode := b.defaultRuntimeModeLocked()
	b.mu.Unlock()

	providers := make([]wire.Provider, 0, len(kinds))
	// enabled 是桌面设置里的 provider 启用状态；未知的 provider 不视为关闭。
	enabled := b.providerEnabled()
	// providerDisabled 判断某个 provider 是否在桌面设置里被关闭。
	providerDisabled := func(id string) bool {
		on, known := enabled[id]
		return known && !on
	}
	for _, k := range kinds {
		// 已关闭的 provider 照常下发（带 enabled=false）：远程端还需要用它解析
		// 已有会话的 provider/模型名称，是否在选择器里隐藏由前端决定。
		var enabledFlag *bool
		if providerDisabled(k) {
			off := false
			enabledFlag = &off
		}
		available := true
		var unavailableReason *string
		auth := "signedIn"

		if h, ok := harnesses[k]; ok {
			if rep, ok := h.(availabilityReporter); ok {
				var reason string
				available, reason = rep.Available()
				if !available {
					unavailableReason = &reason
					auth = "signedOut"
				}
			}
		}

		name := providerDisplayName(k)
		if h, ok := harnesses[k]; ok {
			if dn, ok := h.(interface{ DisplayName() string }); ok {
				if d := dn.DisplayName(); d != "" {
					name = d
				}
			}
		}

		// 未登录或没配 API Key 的供应商没有可选模型：不展示 openai 起始目录这类占位行，
		// 也不为它起后台探测目录。
		models := []wire.ProviderModel{}
		if available {
			models = ProviderModels(k)
			if k == "openai" && !hasAPICatalog(k) {
				// 配置里指定的模型作为首选行并入目录；没有配置时保留起始目录，避免整列不可选。
				var ids []string
				if h, ok := harnesses[k]; ok {
					if mi, ok := h.(interface{ ModelIDs() []string }); ok {
						ids = mi.ModelIDs()
					}
				}
				if len(ids) > 0 {
					models = withConfiguredModel(models, ids[0])
					seen := make(map[string]bool, len(models))
					for _, m := range models {
						seen[m.ID] = true
					}
					for _, id := range ids[1:] {
						if !seen[id] {
							models = append(models, wire.ProviderModel{ID: id, Name: id})
							seen[id] = true
						}
					}
				}
				applyModelsDev(k, models)
			}
			if len(models) == 0 && !hasAPICatalog(k) {
				if h, ok := harnesses[k]; ok {
					if mi, ok := h.(interface{ ModelIDs() []string }); ok {
						for _, id := range mi.ModelIDs() {
							models = append(models, wire.ProviderModel{
								ID:   id,
								Name: id,
							})
						}
					}
				}
			}
		}

		// 供应商可用但目录为空（例如 CLI 未安装、还在探测）时带上原因：
		// available 保持 true，auth 不变，由桌面端在选择器里展示原因与“重新探测”入口。
		// 已有可用性原因（未登录等）时不覆盖。
		if available && len(models) == 0 && unavailableReason == nil {
			if notice := ModelCatalogNotice(k); notice != "" {
				reason := notice
				unavailableReason = &reason
			}
		}

		providers = append(providers, wire.Provider{
			ID:                k,
			Name:              name,
			Auth:              auth,
			Models:            models,
			SupportsPlanMode:  true,
			SupportsImages:    providerSupportsImages(k),
			SupportsFast:      false,
			Available:         available,
			UnavailableReason: unavailableReason,
			Enabled:           enabledFlag,
		})
	}

	// 编排组合直接来自桌面配置的配对模板：两侧 provider 都可用（Available）且未被桌面设置关闭，
	// 且配置的模型在该 provider 目录里或已加入用户固定列表（对齐桌面的 readySwarmPairs 过滤）。
	// usableByID 只收录可用于新建会话的 provider：已关闭的 provider 可以用在已有会话里，
	// 但不应该被选进新的草稿或编排组合。
	swarmCfg := b.swarmConfig()
	usableByID := make(map[string]wire.Provider, len(providers))
	for _, p := range providers {
		if p.Available && !providerDisabled(p.ID) {
			usableByID[p.ID] = p
		}
	}
	// 通用配置在配对过滤、常用模型与草稿默认值里共用，只读取一次。
	generalCfg := b.generalSettings()
	swarmPairs := make([]wire.SwarmPair, 0, len(swarmCfg.Pairs))
	if swarmAvail {
		// 全局并发 Bee 上限沿用桌面设置；0 表示不限，留空由远程端按会话默认值处理。
		var maxBees *int
		if swarmCfg.MaxBees > 0 {
			mh := swarmCfg.MaxBees
			maxBees = &mh
		}
		for _, pair := range swarmCfg.Pairs {
			if pair.ID == "" || pair.LeadProvider == "" {
				continue
			}
			workerProvider := pair.WorkerProvider
			if workerProvider == "" {
				workerProvider = pair.LeadProvider
			}
			lead, ok := usableByID[pair.LeadProvider]
			if !ok || !providerAllowsModel(lead, pair.LeadModel, generalCfg.Models.Pins) {
				continue
			}
			worker, ok := usableByID[workerProvider]
			if !ok || !providerAllowsModel(worker, pair.WorkerModel, generalCfg.Models.Pins) {
				continue
			}
			wp := workerProvider
			row := wire.SwarmPair{
				ID:             pair.ID,
				Provider:       pair.LeadProvider,
				WorkerProvider: &wp,
				MaxBees:        maxBees,
			}
			if name := strings.TrimSpace(pair.Name); name != "" {
				row.Name = &name
			}
			if leadModel := strings.TrimSpace(pair.LeadModel); leadModel != "" {
				row.LeadModel = &leadModel
			}
			if pair.LeadEffort != nil {
				if effort := strings.TrimSpace(*pair.LeadEffort); effort != "" {
					row.LeadEffort = &effort
				}
			}
			if workerModel := strings.TrimSpace(pair.WorkerModel); workerModel != "" {
				row.WorkerModel = &workerModel
			}
			if len(pair.BeeTiers) > 0 {
				row.BeeTiers = make(map[string]wire.SwarmTierOverride, len(pair.BeeTiers))
				for tid, ov := range pair.BeeTiers {
					row.BeeTiers[tid] = wire.SwarmTierOverride{
						Provider: ov.Provider,
						Model:    ov.Model,
						Effort:   ov.Effort,
						FastMode: ov.FastMode,
					}
				}
			}
			swarmPairs = append(swarmPairs, row)
		}
	}

	// 常用模型（桌面「我的模型」固定列表）按配置顺序下发，与桌面模型选择器的精选顺序一致。
	catalog := make([]wire.ModelCatalogEntry, 0, len(generalCfg.Models.Pins))
	for _, pin := range generalCfg.Models.Pins {
		if pin.Provider == "" || pin.ModelID == "" {
			continue
		}
		// 常用模型不收录已关闭的 provider（它只用于已有会话的解析）。
		if providerDisabled(pin.Provider) {
			continue
		}
		catalog = append(catalog, wire.ModelCatalogEntry{Provider: pin.Provider, ModelID: pin.ModelID})
	}

	// 新建会话的默认草稿：宿主端的默认 Provider / 模型与各模式，
	// 移动端据此初始化 Composer，不必自行猜测或留空。
	if defaultProvider == "" {
		defaultProvider = "memory"
	}
	// 回落用的默认 Provider 也不能是已关闭的：桌面设置里关掉它之后，
	// 新草稿要落到一个未被关闭且可用的 provider 上。
	if providerDisabled(defaultProvider) {
		for _, p := range providers {
			if !p.Available || providerDisabled(p.ID) {
				continue
			}
			defaultProvider = p.ID
			break
		}
	}
	defaultSwarmEnabled := false
	draft := wire.ComposerDraftDefaults{
		Provider:        defaultProvider,
		FastMode:        false,
		RuntimeMode:     defaultRuntimeMode,
		InteractionMode: "build",
		WorkspaceMode:   "worktree",
		SwarmEnabled:    swarmAvail && defaultSwarmEnabled,
	}
	if m := DefaultModel(defaultProvider); m != "" {
		draftModel := m
		draft.Model = &draftModel
	}
	// 宿主注入的草稿复刻桌面新建会话的模型记忆优先级；provider 不可用时整体回落到宿主默认值。
	if injected, ok := b.draftDefaults(); ok {
		if injected.Provider == "" {
			injected.Provider = defaultProvider
		}
		// 注入的 provider 若已被关闭，与不可用一样整体回落到宿主默认草稿。
		if _, known := usableByID[injected.Provider]; known {
			draft = injected
			draft.SwarmEnabled = draft.SwarmEnabled && swarmAvail
			if draft.RuntimeMode == "" {
				draft.RuntimeMode = defaultRuntimeMode
			}
			if draft.InteractionMode == "" {
				draft.InteractionMode = "build"
			}
			if draft.WorkspaceMode != config.WorkspaceModeWorktree {
				draft.WorkspaceMode = config.WorkspaceModeLocal
			}
			if p := usableByID[draft.Provider]; draft.Model == nil || (derefString(draft.Model) != "" && !providerAllowsModel(p, derefString(draft.Model), generalCfg.Models.Pins)) {
				m := DefaultModel(draft.Provider)
				draft.Model = &m
			}
		}
	}
	// 模型记忆不记录编排组合 ID：主导与执行侧选择与某条已下发的组合完全一致时补上，
	// 让远程端新建会话默认选中桌面上的同一条组合。
	if draft.SwarmEnabled && derefString(draft.SwarmPairID) == "" {
		if id := matchSwarmPairID(swarmPairs, draft.Provider, derefString(draft.Model), derefString(draft.SwarmBeesProvider), derefString(draft.SwarmBeesModel)); id != "" {
			draft.SwarmPairID = &id
		}
	}

	// providers.list 在桌面启动路径上：首次调用完成时打点，便于看实测耗时。
	startuptrace.Mark("providers: listProviders done")
	return wire.ProvidersListResult{
		Providers: providers,
		Catalog:   catalog,
		Swarm: wire.SwarmComposerInfo{
			Enabled:        swarmAvail,
			DefaultEnabled: defaultSwarmEnabled,
			Pairs:          swarmPairs,
		},
		Draft: &draft,
	}, nil
}

// validModel 在目录校验之外放行用户已固定的模型 ID（设置页「添加自定义模型」写入的 pin）。
// 不修改 IsValidModel 本身，避免影响不认用户固定列表的调用方。
func (b *MemoryBackend) validModel(provider, model string) bool {
	if IsValidModel(provider, model) {
		return true
	}
	if provider == "" || model == "" {
		return false
	}
	return b.generalSettings().Models.HasModelPin(provider, model)
}

// ListProvidersForProject 接收 providers.list 的 projectID 透传。
// 当前 provider 目录与可用性均为全局设置（不随项目变化），因此结果与 ListProviders 一致；
// 保留该方法是为了让 Dispatcher 能把 projectID 交给后端，后续按项目覆盖目录时无需再改协议层。
func (b *MemoryBackend) ListProvidersForProject(ctx context.Context, projectID *string) (wire.ProvidersListResult, error) {
	return b.ListProviders(ctx)
}

// RememberSelection 记录用户主动确认的模型与编排选择（providers.rememberSelection）。
// 记忆只由用户主动操作写入：建会话只读记忆，不回写。
// 主导 provider 未注册或模型既不在目录里也未固定时返回 InvalidParams，且什么都不写；
// 未注入写回回调时只做校验、不写，仍正常返回。
func (b *MemoryBackend) RememberSelection(ctx context.Context, params wire.ProvidersRememberSelectionParams) error {
	provider := derefString(params.Provider)
	model := derefString(params.Model)
	if provider != "" {
		b.mu.Lock()
		_, known := b.harnesses[provider]
		b.mu.Unlock()
		if !known {
			return &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("unknown provider %q", provider),
			}
		}
		if model != "" && !b.validModel(provider, model) {
			return &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid model %q for provider %q", model, provider),
			}
		}
	}
	// 子代理字段任意一个非 nil 就整组写入，空串表示跟随主导。
	if fn := b.rememberSelection(); fn != nil {
		fn(params)
	}
	return nil
}
