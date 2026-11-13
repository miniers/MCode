// 本文件包含 MemoryBackend 的会话创建、更新与删除方法。
package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"mcode/internal/agentconf"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"strings"
	"time"
)

// CreateThread 创建新线程，支持按 ActionID 幂等。
func (b *MemoryBackend) CreateThread(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error) {
	return b.createThread(ctx, params, true)
}

// createThread 创建新线程；userInitiated 表示用户主动新建（threads.create）。
// 用户主动新建时会用宿主记忆补齐没传的选择；编排子代理、看板任务与自动化会话
// 走的是 StartSession 这条系统路径，不读也不写这份记忆。记忆只由用户主动操作
// （providers.rememberSelection）写入，建会话绝不回写。
func (b *MemoryBackend) createThread(ctx context.Context, params wire.ThreadCreateParams, userInitiated bool) (wire.ThreadCreateResult, error) {
	// 客户端没传（nil）时套用全局「新会话默认停用」名单；读盘必须在持锁前完成。
	// 空数组表示明确要求全部启用，不套默认。
	if params.MCPDisabled == nil {
		b.mu.Lock()
		dataDir := b.dataDir
		b.mu.Unlock()
		params.MCPDisabled = agentconf.NewThreadDisabledServers(agentconf.Options{DataDir: dataDir})
	}

	b.mu.Lock()

	if params.ActionID != "" {
		if tid, ok := b.actionThreads[params.ActionID]; ok {
			b.mu.Unlock()
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateAccepted,
				ThreadID: &tid,
			})
			return wire.ThreadCreateResult{
				ActionID: params.ActionID,
				ThreadID: tid,
			}, nil
		}
	}

	proj, ok := b.projects[params.ProjectID]
	if !ok {
		b.mu.Unlock()
		rpcErr := &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("project %s not found", params.ProjectID),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.ThreadCreateResult{}, rpcErr
	}

	threadID := newUUID()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// 用户主动新建时才用宿主记忆补齐没传的选择；只读记忆，不写记忆。
	if userInitiated {
		b.fillThreadCreateDefaultsLocked(&params)
	}

	provider := "memory"
	if b.defaultProvider != "" {
		provider = b.defaultProvider
	}
	if params.Options != nil && params.Options.Provider != nil {
		reqProvider := *params.Options.Provider
		if _, ok := b.harnesses[reqProvider]; !ok {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("unknown provider %q", reqProvider),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
		provider = reqProvider
	}

	var reqRuntimeMode string
	if params.RuntimeMode != nil && *params.RuntimeMode != "" {
		reqRuntimeMode = *params.RuntimeMode
	} else if params.Options != nil && params.Options.RuntimeMode != nil && *params.Options.RuntimeMode != "" {
		reqRuntimeMode = *params.Options.RuntimeMode
	}

	runtimeMode := b.defaultRuntimeModeLocked()
	if reqRuntimeMode != "" {
		if !isValidRuntimeMode(reqRuntimeMode) {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid runtimeMode %q", reqRuntimeMode),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
		runtimeMode = harness.NormalizeMode(reqRuntimeMode)
	}

	var reqModel *string
	if params.Model != nil {
		reqModel = params.Model
	} else if params.Options != nil && params.Options.Model != nil {
		reqModel = params.Options.Model
	}

	modelVal := DefaultModel(provider)
	if reqModel != nil {
		if !b.validModel(provider, *reqModel) {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid model %q for provider %q", *reqModel, provider),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
		modelVal = *reqModel
	}

	swarmEnabled := false
	if b.swarmEnabledFor != nil {
		swarmEnabled = b.swarmEnabledFor(threadID)
	}
	// 编排开关为 true（显式传入或从记忆补齐）时在创建时就开启编排，
	// 与 threads.swarm.set 走同一后端实现，否则首轮会按普通会话执行、不派子代理。
	swarmEnabler := b.swarmEnablerFunc
	wantSwarm := userInitiated && params.Options != nil && params.Options.SwarmEnabled != nil && *params.Options.SwarmEnabled

	var (
		hasExisting bool
		minSort     int64
	)
	for _, t := range b.threads {
		if t.summary.ProjectID == params.ProjectID {
			if !hasExisting || t.sortOrder < minSort {
				minSort = t.sortOrder
				hasExisting = true
			}
		}
	}
	var sortOrder int64
	if hasExisting {
		sortOrder = minSort - 1
	} else {
		sortOrder = 0
	}

	isProvisional := params.Provisional != nil && *params.Provisional
	summary := wire.ThreadSummary{
		ID:                threadID,
		ProjectID:         params.ProjectID,
		Title:             defaultThreadTitle,
		Provider:          provider,
		Model:             &modelVal,
		IsRunning:         false,
		IsArchived:        false,
		IsPinned:          false,
		IsDormant:         false,
		HasUnread:         false,
		Attention:         "normal",
		Section:           "recent",
		OrderKey:          formatOrderKey(sortOrder),
		UpdatedAt:         now,
		RuntimeMode:       runtimeMode,
		InteractionMode:   "build",
		FastMode:          false,
		MCPDisabled:       params.MCPDisabled,
		WorktreeRequested: false,
		SwarmEnabled:      swarmEnabled,
		IsProvisional:     isProvisional,
	}
	if params.Options != nil {
		summary.SwarmBeesProvider = optionalString(derefString(params.Options.SwarmBeesProvider))
		summary.SwarmBeesModel = optionalString(derefString(params.Options.SwarmBeesModel))
		if params.Options.SwarmBeesEffort != nil {
			summary.SwarmBeesEffort = optionalString(*params.Options.SwarmBeesEffort)
		}
		if params.Options.Effort != nil {
			summary.Effort = optionalString(*params.Options.Effort)
		}
		if params.Options.SwarmPairID != nil {
			summary.SwarmPairID = optionalString(*params.Options.SwarmPairID)
		}
		if params.Options.FastMode != nil {
			summary.FastMode = *params.Options.FastMode
		}
	}

	b.threads[threadID] = &memoryThread{
		summary:        summary,
		sortOrder:      sortOrder,
		queueRevision:  1,
		messages:       []wire.ThreadMessage{},
		messagesLoaded: true,
		persisted:      make(map[string]bool),
		pending:        make(map[string]*pendingRequest),
		alwaysAllow:    make(map[string]bool),
		parentThreadID: "",
		isHelper:       false,
		dormantAt:      nil,
	}
	if params.ActionID != "" {
		b.actionThreads[params.ActionID] = threadID
	}
	proj.ThreadCount++
	b.projects[params.ProjectID] = proj

	s := b.store
	b.mu.Unlock()

	if s != nil {
		storeThread := threadToStore(summary, "", sortOrder)
		storeThread.IsProvisional = isProvisional
		if err := s.CreateThread(ctx, storeThread); err != nil {
			b.mu.Lock()
			delete(b.threads, threadID)
			if params.ActionID != "" {
				delete(b.actionThreads, params.ActionID)
			}
			if p, ok := b.projects[params.ProjectID]; ok {
				p.ThreadCount--
				b.projects[params.ProjectID] = p
			}
			b.mu.Unlock()

			rpcErr := &wire.RPCError{
				Code:    wire.InternalError,
				Message: fmt.Sprintf("failed to persist thread: %v", err),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.ThreadCreateResult{}, rpcErr
		}
	}

	b.hub.Publish(threadID, nil, nil, summary)

	if wantSwarm && swarmEnabler != nil {
		swarmEnabler(threadID, true)
	}

	b.mu.Lock()
	hook := b.threadsChangedHook
	b.mu.Unlock()
	if hook != nil {
		hook(wire.ThreadsChanged{
			Threads: []wire.ThreadSummary{summary},
		})
	}

	if params.ActionID != "" {
		b.actions.record(wire.ActionStatusResult{
			ActionID: params.ActionID,
			State:    wire.ActionStateAccepted,
			ThreadID: &threadID,
		})
	}

	return wire.ThreadCreateResult{
		ActionID: params.ActionID,
		ThreadID: threadID,
	}, nil
}

// UpdateThread 更新指定会话的属性（支持 runtimeMode、title、provider、model），并通过 StreamHub 推送更新。
func (b *MemoryBackend) UpdateThread(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error) {
	if params.Effort != nil && !isValidEffort(*params.Effort) {
		return wire.ThreadUpdateResult{}, &wire.RPCError{Code: wire.InvalidParams, Message: fmt.Sprintf("invalid effort %q", *params.Effort)}
	}
	if params.RuntimeMode != nil && !isValidRuntimeMode(*params.RuntimeMode) {
		return wire.ThreadUpdateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid runtimeMode %q", *params.RuntimeMode),
		}
	}
	if params.Title != nil && len(*params.Title) == 0 {
		return wire.ThreadUpdateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "title cannot be empty",
		}
	}

	b.mu.Lock()
	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadUpdateResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
	}

	titleChanged := false
	runtimeModeChanged := false
	providerChanged := false
	modelChanged := false
	pinnedChanged := false
	archivedChanged := false
	dormantChanged := false
	unreadChanged := false

	targetProvider := td.summary.Provider
	if params.Provider != nil && *params.Provider != "" && *params.Provider != td.summary.Provider {
		newProvider := *params.Provider
		if td.summary.IsRunning || td.takingBack {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: "运行或撤回期间无法更换供应商",
			}
		}
		if _, exists := b.harnesses[newProvider]; !exists {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("unknown or unregistered provider %q", newProvider),
			}
		}
		targetProvider = newProvider
		providerChanged = true
	}

	var targetModel string
	if params.Model != nil {
		reqModel := *params.Model
		if !b.validModel(targetProvider, reqModel) {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, &wire.RPCError{
				Code:    wire.InvalidParams,
				Message: fmt.Sprintf("invalid model %q for provider %q", reqModel, targetProvider),
			}
		}
		targetModel = reqModel
		var currentModel string
		if td.summary.Model != nil {
			currentModel = *td.summary.Model
		}
		if td.summary.Model == nil || targetModel != currentModel || providerChanged {
			modelChanged = true
		}
	} else if providerChanged {
		targetModel = DefaultModel(targetProvider)
		modelChanged = true
	}
	effortChanged := params.Effort != nil && *params.Effort != derefString(td.summary.Effort)
	if effortChanged && b.store != nil {
		if err := b.store.UpdateThreadComposerPrefs(ctx, params.ThreadID, td.summary.InteractionMode, *params.Effort, derefString(td.summary.SwarmBeesProvider), derefString(td.summary.SwarmBeesModel), derefString(td.summary.SwarmBeesEffort), td.summary.Persona); err != nil {
			b.mu.Unlock()
			return wire.ThreadUpdateResult{}, err
		}
	}

	var (
		cascadedArchived  []wire.ThreadSummary
		cascadedDormant   []wire.ThreadSummary
		cascadedDormantAt *time.Time
	)

	if params.Title != nil && *params.Title != "" && *params.Title != td.summary.Title {
		td.summary.Title = *params.Title
		titleChanged = true
	}
	if params.RuntimeMode != nil && *params.RuntimeMode != "" && harness.NormalizeMode(*params.RuntimeMode) != td.summary.RuntimeMode {
		td.summary.RuntimeMode = harness.NormalizeMode(*params.RuntimeMode)
		runtimeModeChanged = true
	}
	if params.Pinned != nil && *params.Pinned != td.summary.IsPinned {
		td.summary.IsPinned = *params.Pinned
		pinnedChanged = true
	}
	if params.Archived != nil {
		if *params.Archived {
			// 对该线程整个家族（递归子线程及 helper 会话）全部归档，同时取消置顶、清除休眠；
			// 先撤掉家族内的待处理卡片，正在运行的成员再中断。
			familyIDs := b.familyThreadIDsLocked(params.ThreadID)
			type runningTarget struct {
				id     string
				turnID string
			}
			var toInterrupt []runningTarget
			var withdrew []*withdrawBatch
			for _, fid := range familyIDs {
				ftd, ok := b.threads[fid]
				if !ok {
					continue
				}
				if batch := b.takeWithdrawBatchLocked(ftd, withdrawArchive); batch != nil {
					withdrew = append(withdrew, batch)
				}
				if ftd.summary.IsRunning {
					toInterrupt = append(toInterrupt, runningTarget{id: fid, turnID: ftd.runTurnID})
				}
			}
			if len(withdrew) > 0 || len(toInterrupt) > 0 {
				b.mu.Unlock()
				// 先把卡片已撤销的消息发出去，再中断运行中的成员，避免被中断入口写成"已中断"。
				for _, batch := range withdrew {
					b.finishWithdraw(batch, withdrawArchive)
				}
				for _, target := range toInterrupt {
					_, _ = b.Interrupt(ctx, wire.InterruptParams{
						ThreadID: target.id,
						TurnID:   target.turnID,
					})
				}
				b.mu.Lock()
				td, ok = b.threads[params.ThreadID]
				if !ok {
					b.mu.Unlock()
					return wire.ThreadUpdateResult{}, &wire.RPCError{
						Code:    wire.ErrCodeResourceNotFound,
						Message: fmt.Sprintf("thread %s not found", params.ThreadID),
					}
				}
				familyIDs = b.familyThreadIDsLocked(params.ThreadID)
			}

			nowTime := b.nowLocked()
			nowStr := nowTime.Format(time.RFC3339Nano)
			for _, fid := range familyIDs {
				if ftd, ok := b.threads[fid]; ok {
					ftd.summary.IsArchived = true
					ftd.summary.IsPinned = false
					ftd.summary.IsDormant = false
					ftd.dormantAt = nil
					ftd.summary.UpdatedAt = nowStr
					if fid != params.ThreadID {
						cascadedArchived = append(cascadedArchived, ftd.summary)
					}
				}
			}
			archivedChanged = true
			pinnedChanged = true
			dormantChanged = true
		} else {
			if td.summary.IsArchived {
				// C-ST-07：取消归档时按需回载消息，让会话恢复为可完整访问的状态。
				if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
					b.mu.Unlock()
					return wire.ThreadUpdateResult{}, err
				}
				b.touchThreadLocked(td)
				td.summary.IsArchived = false
				archivedChanged = true
			}
		}
	}
	if params.Dormant != nil {
		if *params.Dormant {
			// 休眠不撤卡：会话只是暂时收起，待处理卡片在唤醒后仍需要可答。
			nowTime := b.nowLocked()
			nowStr := nowTime.Format(time.RFC3339Nano)
			td.summary.IsDormant = true
			td.dormantAt = &nowTime
			td.summary.IsPinned = false
			td.summary.HasUnread = false
			td.summary.UpdatedAt = nowStr
			dormantChanged = true
			pinnedChanged = true
			cascadedDormantAt = &nowTime

			// 级联到 helper 子线程
			familyIDs := b.familyThreadIDsLocked(params.ThreadID)
			for _, fid := range familyIDs {
				if fid == params.ThreadID {
					continue
				}
				if ftd, ok := b.threads[fid]; ok && ftd.isHelper {
					ftd.summary.IsDormant = true
					ftd.dormantAt = &nowTime
					ftd.summary.IsPinned = false
					ftd.summary.HasUnread = false
					ftd.summary.UpdatedAt = nowStr
					cascadedDormant = append(cascadedDormant, ftd.summary)
				}
			}
		} else {
			nowStr := b.nowLocked().Format(time.RFC3339Nano)
			td.summary.IsDormant = false
			td.dormantAt = nil
			dormantChanged = true

			// 取消休眠同步级联到 helper 子线程，避免父子会话休眠状态不一致
			familyIDs := b.familyThreadIDsLocked(params.ThreadID)
			for _, fid := range familyIDs {
				if fid == params.ThreadID {
					continue
				}
				if ftd, ok := b.threads[fid]; ok && ftd.isHelper {
					ftd.summary.IsDormant = false
					ftd.dormantAt = nil
					ftd.summary.UpdatedAt = nowStr
					cascadedDormant = append(cascadedDormant, ftd.summary)
				}
			}
		}
	}
	if params.Read != nil {
		targetUnread := !*params.Read
		if td.summary.HasUnread != targetUnread {
			td.summary.HasUnread = targetUnread
			unreadChanged = true
		}
	}
	if providerChanged {
		td.summary.Provider = targetProvider
		td.sessionID = ""
		td.sessionEpoch++
		// 会话被替换，转发上下文需要在新会话里重新送达。
		td.continuationSent = false
		td.needsHandoff = true
	}
	if modelChanged {
		m := targetModel
		td.summary.Model = &m
	}
	if effortChanged {
		td.summary.Effort = optionalString(*params.Effort)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if titleChanged || runtimeModeChanged || providerChanged || modelChanged || effortChanged || pinnedChanged || archivedChanged || dormantChanged || unreadChanged {
		td.summary.UpdatedAt = now
	}

	currentSummary := b.summaryForPublishLocked(td)
	currentDormantAt := td.dormantAt
	hook := b.threadsChangedHook
	b.mu.Unlock()

	if titleChanged {
		b.persistThreadTitle(params.ThreadID, currentSummary.Title)
	}
	if runtimeModeChanged {
		b.persistThreadRuntimeMode(params.ThreadID, currentSummary.RuntimeMode)
	}
	if providerChanged {
		b.persistThreadProvider(params.ThreadID, currentSummary.Provider)
		b.persistThreadSessionReset(params.ThreadID)
	}
	if modelChanged {
		var m string
		if currentSummary.Model != nil {
			m = *currentSummary.Model
		}
		b.persistThreadModel(params.ThreadID, m)
	}
	if pinnedChanged {
		b.persistThreadPinned(params.ThreadID, currentSummary.IsPinned)
	}
	if archivedChanged {
		b.persistThreadArchived(params.ThreadID, currentSummary.IsArchived)
	}
	if unreadChanged {
		b.persistThreadUnread(params.ThreadID, currentSummary.HasUnread)
	}
	if dormantChanged {
		b.persistThreadLifecycle(params.ThreadID, currentSummary.IsDormant, currentDormantAt, currentSummary.IsProvisional, currentSummary.HasUnread)
	}

	for _, sum := range cascadedArchived {
		b.persistThreadArchived(sum.ID, true)
		b.persistThreadPinned(sum.ID, false)
		b.persistThreadLifecycle(sum.ID, false, nil, sum.IsProvisional, sum.HasUnread)
		b.hub.Publish(sum.ID, nil, nil, sum)
	}
	for _, sum := range cascadedDormant {
		b.persistThreadPinned(sum.ID, sum.IsPinned)
		b.persistThreadLifecycle(sum.ID, sum.IsDormant, cascadedDormantAt, sum.IsProvisional, sum.HasUnread)
		b.hub.Publish(sum.ID, nil, nil, sum)
	}

	b.hub.Publish(params.ThreadID, nil, nil, currentSummary)

	if hook != nil {
		allChanged := []wire.ThreadSummary{currentSummary}
		allChanged = append(allChanged, cascadedArchived...)
		allChanged = append(allChanged, cascadedDormant...)
		hook(wire.ThreadsChanged{Threads: allChanged})
	}

	return wire.ThreadUpdateResult{
		Thread: currentSummary,
	}, nil
}

// SetThreadComposerPrefs 更新会话级 composer 偏好并写穿到 store。仅供桌面端直接调用，不注册为 RPC。
func (b *MemoryBackend) SetThreadComposerPrefs(ctx context.Context, threadID string, p ThreadComposerPrefs) (wire.ThreadSummary, error) {
	if p.InteractionMode == "" {
		p.InteractionMode = "build"
	}
	if p.InteractionMode != "build" && p.InteractionMode != "plan" {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid interactionMode %q", p.InteractionMode),
		}
	}
	if !isValidEffort(p.Effort) {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid effort %q", p.Effort),
		}
	}
	if !isValidEffort(p.BeesEffort) {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid bees effort %q", p.BeesEffort),
		}
	}
	if !isValidPersona(p.Persona) {
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid persona %q", p.Persona),
		}
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.UpdateThreadComposerPrefs(ctx, threadID, p.InteractionMode, p.Effort, p.BeesProvider, p.BeesModel, p.BeesEffort, p.Persona); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.InteractionMode = p.InteractionMode
	td.summary.Effort = optionalString(p.Effort)
	td.summary.Persona = p.Persona
	td.summary.SwarmBeesProvider = optionalString(p.BeesProvider)
	td.summary.SwarmBeesModel = optionalString(p.BeesModel)
	td.summary.SwarmBeesEffort = optionalString(p.BeesEffort)
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// SetThreadMCPDisabled 更新会话级外部 MCP 禁用名单（agent:scope:name 键）并广播会话摘要。
// 空名单表示恢复为「沿用全局设置」。名单写穿到 store，重启后由 AttachStore 还原。
func (b *MemoryBackend) SetThreadMCPDisabled(ctx context.Context, threadID string, keys []string) (wire.ThreadSummary, error) {
	cleaned := make([]string, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		cleaned = append(cleaned, key)
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.SetThreadMCPDisabled(ctx, threadID, cleaned); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.MCPDisabled = cleaned
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// ThreadMCPDisabled 返回会话级外部 MCP 禁用名单的副本；会话不存在时返回 nil。
// 注入外部 MCP 时用它判断哪些服务被本会话关掉。
func (b *MemoryBackend) ThreadMCPDisabled(threadID string) []string {
	if threadID == "" {
		return nil
	}
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return nil
	}
	keys := append([]string(nil), td.summary.MCPDisabled...)
	b.mu.Unlock()
	return keys
}

// SetThreadFastMode 更新会话的快速模式与速率档位并写穿到 store。仅供桌面端直接调用，不注册为 RPC。
func (b *MemoryBackend) SetThreadFastMode(ctx context.Context, threadID string, fastMode bool, speedTier string) (wire.ThreadSummary, error) {
	// 关闭快速模式时速率档位无意义，强制置空避免残留陈旧档位。
	if !fastMode {
		speedTier = ""
	}

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return wire.ThreadSummary{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", threadID),
		}
	}
	// 先落盘再更新内存，保存失败不得向界面报告成功；锁内写入保证并发更新顺序一致。
	if b.store != nil {
		if err := b.store.UpdateThreadFastMode(ctx, threadID, fastMode, speedTier); err != nil {
			b.mu.Unlock()
			return wire.ThreadSummary{}, err
		}
	}
	td.summary.FastMode = fastMode
	td.summary.SpeedTier = optionalString(speedTier)
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, currentSummary)

	return currentSummary, nil
}

// SetThreadsChangedHook 设置会话列表变动的监听回调函数。
func (b *MemoryBackend) SetThreadsChangedHook(fn func(wire.ThreadsChanged)) {
	b.mu.Lock()
	b.threadsChangedHook = fn
	b.mu.Unlock()
}

// AddThreadsChangedListener 追加一个会话列表变动监听（不覆盖已有钩子），返回注销函数。
func (b *MemoryBackend) AddThreadsChangedListener(fn func(wire.ThreadsChanged)) (remove func()) {
	if fn == nil {
		return func() {}
	}
	b.mu.Lock()
	prev := b.threadsChangedHook
	b.threadsChangedHook = func(tc wire.ThreadsChanged) {
		if prev != nil {
			prev(tc)
		}
		fn(tc)
	}
	b.mu.Unlock()
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.threadsChangedHook != nil {
			b.threadsChangedHook = prev
		}
	}
}

// SetThreadDeletedHook 设置会话被删除前的钩子函数。
func (b *MemoryBackend) SetThreadDeletedHook(fn func(ctx context.Context, threadID string)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.threadDeletedHook = fn
}

// DeleteThread 删除指定会话及其整个家族子会话：从叶子到根递归删除、触发删除钩子、清理临时定时器、中断运行轮次，并发出 threads.changed 通知。
func (b *MemoryBackend) DeleteThread(ctx context.Context, threadID string) error {
	b.mu.Lock()
	if _, ok := b.threads[threadID]; !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	familyIDs := b.familyThreadIDsLocked(threadID)
	b.mu.Unlock()

	// 从叶子到根（familyThreadIDsLocked 返回根在前，因此反向遍历）
	deleteOrder := make([]string, len(familyIDs))
	for i, id := range familyIDs {
		deleteOrder[len(familyIDs)-1-i] = id
	}

	var removedIDs []string
	for _, targetID := range deleteOrder {
		b.mu.Lock()
		td, ok := b.threads[targetID]
		if !ok {
			b.mu.Unlock()
			continue
		}
		isRunning := td.summary.IsRunning
		runTurnID := td.runTurnID
		// 删除前先撤掉待处理卡片：会话即将消失，不写时间线提示，只在锁外广播空列表。
		withdrawnBatch := b.takeWithdrawBatchLocked(td, withdrawDelete)
		delHook := b.threadDeletedHook
		// 先在锁内取出 harness 引用，EvictThread 可能阻塞，必须放到锁外调用。
		evictor, _ := b.threadHarnessLocked(td.summary.Provider).(harness.ThreadEvictor)
		workDir := b.harnessWorkDir
		if td.workDir != "" {
			workDir = td.workDir
		} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
			workDir = proj.Path
		}
		gitRunner := b.git
		b.mu.Unlock()

		b.publishWithdrawn(withdrawnBatch)

		if isRunning {
			_, _ = b.Interrupt(ctx, wire.InterruptParams{
				ThreadID: targetID,
				TurnID:   runTurnID,
			})
		}

		// 每个被删线程在移除前调用 hook（不能持锁调用）
		if delHook != nil {
			delHook(ctx, targetID)
		}

		// C-GIT-11：删除线程前清理其检查点 ref，失败仅记录日志，不阻断删除
		if gitRunner != nil && workDir != "" {
			if root, err := gitRunner.RepoRoot(ctx, workDir); err == nil && root != "" {
				if err := gitRunner.DeleteCheckpoints(ctx, root, targetID); err != nil {
					log.Printf("delete checkpoints for thread %s failed: %v", targetID, err)
				}
			}
		}

		b.mu.Lock()
		// 取消该线程的临时清理定时器
		if timer, hasTimer := b.provisionalTimers[targetID]; hasTimer {
			timer.Stop()
			delete(b.provisionalTimers, targetID)
		}
		delete(b.threads, targetID)
		delete(b.usage, targetID)
		b.readPathsMu.Lock()
		delete(b.readPaths, targetID)
		b.readPathsMu.Unlock()
		for aid, tid := range b.actionThreads {
			if tid == targetID {
				delete(b.actionThreads, aid)
			}
		}
		s := b.store
		b.mu.Unlock()

		// 会话已从内存移除，顺手释放该 thread 空闲常驻的后端进程（不能持锁调用）。
		if evictor != nil {
			evictor.EvictThread(targetID)
		}

		// 删除消息之前先把会话同步到 Nowledge：删除后本地不再有可导入的正文。
		b.syncNowledgeThread(targetID, nowledgeSyncDeleteTimeout)

		if s != nil {
			if err := s.DeleteThread(ctx, targetID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		// 正文已删除：清掉该会话的同步状态与持久待同步标记。删除前同步失败的不再补传。
		b.forgetNowledgeSync(targetID)

		removedIDs = append(removedIDs, targetID)
	}

	b.mu.Lock()
	hook := b.threadsChangedHook
	b.mu.Unlock()

	payload := wire.ThreadsChanged{
		RemovedThreadIDs: removedIDs,
	}
	b.hub.Notify(threadID, "threads.changed", payload)
	if hook != nil {
		hook(payload)
	}

	return nil
}

// RemoveProject 从内存与 store 中移除指定项目行，不删磁盘文件。
// store 删除项目时会在同一事务里按外键级联删除该项目的会话，因此内存中也同步移除这些会话
// （含以会话 ID 为键的用量、审批、定时器等缓存），否则它们会以悬挂项目分组的形式留在内存里。
func (b *MemoryBackend) RemoveProject(ctx context.Context, projectID string) error {
	b.mu.Lock()
	if _, ok := b.projects[projectID]; !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	delete(b.projects, projectID)

	// 先按 DeleteThread 的方式中断该项目下正在运行的会话，再统一移除。
	// evictors 记录各会话所属的后端 harness，移除后要在锁外释放它们的空闲常驻进程。
	var removedIDs []string
	evictors := make(map[string]harness.ThreadEvictor)
	for id, td := range b.threads {
		if td.summary.ProjectID != projectID {
			continue
		}
		removedIDs = append(removedIDs, id)
		if ev, ok := b.threadHarnessLocked(td.summary.Provider).(harness.ThreadEvictor); ok {
			evictors[id] = ev
		}
		if td.summary.IsRunning {
			// 会话随项目一起被移除，撤下的卡片无需再广播或写时间线提示。
			_, _, _ = b.interruptLocked(td, td.runTurnID)
		}
	}
	hook := b.threadDeletedHook
	b.mu.Unlock()

	// 每个被移除的会话在移除前调用 hook（不能持锁调用）
	if hook != nil {
		for _, id := range removedIDs {
			hook(ctx, id)
		}
	}

	b.mu.Lock()
	for _, id := range removedIDs {
		td := b.threads[id]
		if td == nil {
			continue
		}
		// 中断后残留的取消函数与看门狗定时器一并关闭，避免它们继续引用已移除的会话。
		if td.runCancel != nil {
			td.runCancel()
			td.runCancel = nil
		}
		if td.watchdogStop != nil {
			td.watchdogStop()
			td.watchdogStop = nil
		}
		if timer, hasTimer := b.provisionalTimers[id]; hasTimer {
			timer.Stop()
			delete(b.provisionalTimers, id)
		}
		delete(b.threads, id)
		delete(b.usage, id)
		delete(b.guidanceDelivered, id)
		delete(b.autoContinueWaits, id)
		b.readPathsMu.Lock()
		delete(b.readPaths, id)
		b.readPathsMu.Unlock()
		for aid, tid := range b.actionThreads {
			if tid == id {
				delete(b.actionThreads, aid)
			}
		}
	}
	s := b.store
	changedHook := b.threadsChangedHook
	b.mu.Unlock()

	// 会话已从内存移除，释放它们空闲常驻的后端进程（EvictThread 可能阻塞，不能持锁调用）。
	for _, id := range removedIDs {
		if ev, ok := evictors[id]; ok {
			ev.EvictThread(id)
		}
	}

	// 与 DeleteThread 一致：会话已从内存移除后广播 threads.changed（不能持锁回调），
	// 否则 web 端等远程客户端会把这些会话一直留在列表里。
	if len(removedIDs) > 0 {
		payload := wire.ThreadsChanged{RemovedThreadIDs: removedIDs}
		for _, id := range removedIDs {
			b.hub.Notify(id, "threads.changed", payload)
		}
		if changedHook != nil {
			changedHook(payload)
		}
	}

	if s != nil {
		if err := s.DeleteProject(ctx, projectID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
	}
	return nil
}
