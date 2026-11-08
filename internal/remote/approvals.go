package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	storepkg "mcode/internal/store"
)

type approvalResponse struct {
	optionID string
	skip     bool
	answers  map[string][]string
	ack      chan error
}

type pendingRequest struct {
	req              wire.RemoteRequest
	toolID           string
	toolName         string
	allowKey         string
	summary          string
	resCh            chan approvalResponse
	handled          bool
	awaitingAck      bool
	respondedAt      time.Time
	deliveryAck      chan error
	confirmsDelivery bool
	providerWaiting  bool
	done             chan struct{}
	// doneOnce 保证 done 只关闭一次：Provider 清理路径与撤卡路径都可能关闭它。
	doneOnce sync.Once
	ctx      context.Context
	// withdrawCh 是撤卡方写入、Interact 主等待读取的专用撤回信号，容量 1、只写一次。
	withdrawCh   chan withdrawReason
	withdrawOnce sync.Once
	// settled 在计划请求得出答复（或被取消）后关闭，供同轮次重复到达的计划请求共享同一结论。
	settled chan struct{}
	// finalResp 是计划请求的答复，只在 settled 关闭后读取；nil 表示请求被取消。
	finalResp  *approvalResponse
	settleOnce sync.Once
}

// settle 记录计划请求的结论并唤醒等待中的重复请求；重复调用无副作用。
func (pr *pendingRequest) settle(resp *approvalResponse) {
	pr.settleOnce.Do(func() {
		pr.finalResp = resp
		close(pr.settled)
	})
}

// closeDone 关闭等待通道，重复调用无副作用。
func (pr *pendingRequest) closeDone() {
	pr.doneOnce.Do(func() {
		close(pr.done)
	})
}

// withdraw 通知 Interact 请求已被宿主撤回，并立刻唤醒等待送达确认的一方。
// 撤回信号只写一次，重复撤卡是空操作，不会阻塞也不会关闭已关闭的通道。
func (pr *pendingRequest) withdraw(reason withdrawReason) {
	pr.withdrawOnce.Do(func() {
		select {
		case pr.withdrawCh <- reason:
		default:
		}
	})
	pr.closeDone()
}

type threadApprover struct {
	b                *MemoryBackend
	threadID         string
	turnID           string
	sessionEpoch     int
	confirmsDelivery bool
}

// WorkDir 实现 harness.WorkDirProvider 接口，返回当前会话的工作目录。
func (a *threadApprover) WorkDir() string {
	a.b.mu.Lock()
	defer a.b.mu.Unlock()
	if td, ok := a.b.threads[a.threadID]; ok && td.workDir != "" {
		return td.workDir
	}
	return a.b.harnessWorkDir
}

// AckDelivery 实现 harness.DeliveryAcker 接口，接收 Provider 写入 CLI 后的最终送达确认。
func (a *threadApprover) AckDelivery(req harness.ApprovalRequest, err error) {
	a.b.mu.Lock()
	td, ok := a.b.threads[a.threadID]
	if !ok || td.sessionEpoch != a.sessionEpoch || len(td.pending) == 0 {
		a.b.mu.Unlock()
		return
	}

	var candidate *pendingRequest
	var candidateTime time.Time

	for _, pr := range td.pending {
		if !pr.handled || !pr.awaitingAck {
			continue
		}
		if pr.req.TurnID == nil || *pr.req.TurnID != a.turnID {
			continue
		}
		match := false
		if req.ToolID != "" {
			if pr.toolID == req.ToolID {
				match = true
			}
		} else {
			if pr.toolID == "" && pr.toolName == req.ToolName && pr.summary == req.Summary {
				match = true
			}
		}
		if !match {
			continue
		}
		if candidate == nil || pr.respondedAt.After(candidateTime) {
			candidate = pr
			candidateTime = pr.respondedAt
		}
	}

	var ackCh chan error
	if candidate != nil {
		ackCh = candidate.deliveryAck
		candidate.awaitingAck = false
		if err != nil {
			errText := err.Error()
			candidate.req.DeliveryError = &errText
		}
	}
	a.b.mu.Unlock()

	if ackCh != nil {
		select {
		case ackCh <- err:
		default:
			// 没有人在等就丢弃，不能阻塞
		}
	}
}

// Approve 实现 harness.Approver 接口，向前端分发工具授权请求并挂起等待答复。
func (a *threadApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	resp, err := a.Interact(ctx, req)
	return resp.Decision, err
}

// Interact 实现 harness.Interactor 接口，按请求种类分发授权、计划或提问请求并挂起等待答复。
func (a *threadApprover) Interact(ctx context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	deny := harness.ApprovalResponse{Decision: harness.DecisionDeny}
	kind := req.Kind
	if kind == "" {
		kind = inferRequestKind(req.ToolName)
	}
	// 计划与提问必须由用户逐次答复，不受始终允许名单影响
	interactive := kind == harness.RequestKindPlan || kind == harness.RequestKindQuestion

	// 持久化的「始终允许」按数据目录全局一份、按项目键分桶。两者都要在进入主锁前取好：
	// 存储实例可能首次从磁盘加载，项目键要做符号链接解析，都不能在持有 b.mu 时进行。
	store := a.b.alwaysAllowStore()
	project := a.b.alwaysAllowProjectKey(a.threadID)
	// 总管的会话的内置 MCP 工具免审批：同样要在主锁之前算好，存储读取不能持锁进行。
	// 只认「总管的会话 + 内置 mcode 服务器」这一种组合，白名单由内置服务器自己拦截。
	stewardTool := a.b.stewardBuiltinMCPTool(ctx, a.threadID, req.ToolName)

	a.b.mu.Lock()
	td, ok := a.b.threads[a.threadID]
	if !ok {
		a.b.mu.Unlock()
		return deny, errors.New("thread not found")
	}
	if err := ctx.Err(); err != nil {
		a.b.mu.Unlock()
		return deny, err
	}
	if td.runTurnID != a.turnID || td.sessionEpoch != a.sessionEpoch {
		a.b.mu.Unlock()
		return deny, errors.New("turn is no longer active")
	}

	// 只读档 Swarm helper 会话自动拒绝除提问外的所有写操作/审批，不向前端分发；
	// 只读 shell 命令（ls、grep、wc 等）不改动任何东西，直接放行。
	// 判定必须用输入里的真实命令：Summary 是 provider 拼接出的展示串（如 `Bash {"command":…}`），取首词必然判否。
	if (td.isHelper || td.parentThreadID != "") && td.summary.RuntimeMode == string(harness.ModeReadOnly) {
		if req.Sensitive == "" && kind == harness.RequestKindCommand && isSafeReadOnlyCommand(approvalCommand(req)) {
			a.b.mu.Unlock()
			return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil
		}
		if kind != harness.RequestKindQuestion {
			a.b.mu.Unlock()
			return harness.ApprovalResponse{
				Decision: harness.DecisionDeny,
				Feedback: "只读档子代理不允许写操作",
			}, nil
		}
	}

	// 子代理（Swarm bee）无人值守：审批请求挂起会永久卡住本轮，前端也不会有人来答复。
	// 非敏感操作直接放行，改动落在独立 worktree，合并时还会按档位过滤；提问则给出固定答复。
	if td.isHelper || td.parentThreadID != "" {
		if req.Sensitive != "" {
			a.b.mu.Unlock()
			return harness.ApprovalResponse{
				Decision: harness.DecisionDeny,
				Feedback: "子代理无人值守，敏感操作已自动拒绝，请换一种不涉及该路径的做法",
			}, nil
		}
		if kind == harness.RequestKindQuestion {
			// 用允许加答案代替拒绝，避免 provider 把拒绝当作本轮中断；答案为模型可读的固定说明。
			answers := make(map[string][]string, len(req.Questions))
			for _, q := range req.Questions {
				answers[q.ID] = []string{"子代理无人值守，没有人回答提问，请自行判断后继续"}
			}
			a.b.mu.Unlock()
			return harness.ApprovalResponse{Decision: harness.DecisionAllow, Answers: answers}, nil
		}
		a.b.mu.Unlock()
		return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil
	}

	// 总管的会话调用内置 MCP 服务器上的工具直接放行：这些工具在只读档下本会被一律拒绝，
	// 而内置服务器已按白名单裁剪过（只留总管专属工具与既有只读工具），越权调用会被它拒绝。
	// 写文件、执行命令与第三方 MCP 服务器的工具都不命中这条豁免。
	if req.Sensitive == "" && stewardTool && kind == harness.RequestKindTool {
		a.b.mu.Unlock()
		return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil
	}

	// 先匹配完整键，再匹配命令前缀；持久名单按项目生效。
	allowed := func(key string) bool {
		return key != "" && (td.alwaysAllow[key] || (project != "" && store.Allowed(project, key)))
	}
	if req.Sensitive == "" && !interactive && (allowed(fullAllowKey(kind, req)) || commandPrefixesAllowed(kind, req, allowed)) {
		a.b.mu.Unlock()
		return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil
	}

	// MCP 工具命中免审批策略时直接放行，不再打扰用户
	if req.Sensitive == "" && !interactive && kind == harness.RequestKindTool && mcpToolAutoApproved(a.b.dataDir, req.ToolName) {
		a.b.mu.Unlock()
		return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil
	}

	var candidate *pendingRequest
	var candidateID string
	var latestTime time.Time

	// 检查当前线程是否存在同 ToolID（若 ToolID 为空则匹配 ToolName 与 Summary）且处于 deliveryError 状态的未完成待重试请求
	if td.pending != nil {
		for id, existing := range td.pending {
			if existing.req.DeliveryError == nil || existing.req.TurnID == nil || *existing.req.TurnID != a.turnID {
				continue
			}
			match := false
			if req.ToolID != "" {
				if existing.toolID == req.ToolID {
					match = true
				}
			} else {
				if existing.toolID == "" && existing.toolName == req.ToolName && existing.summary == req.Summary {
					match = true
				}
			}
			if match {
				if candidate == nil || existing.respondedAt.After(latestTime) {
					candidate = existing
					candidateID = id
					latestTime = existing.respondedAt
				}
			}
		}
	}

	// 计划请求单飞：ExitPlanMode 可能被重复投递（CLI 超时重试、进程重启后 resume），重试会带新的
	// tool_use_id 甚至没有 turnID，因此按「同一会话 + 计划正文」去重，直接挂到既有的同正文计划请求上，
	// 避免界面对同一份计划出现多张待批准卡片。
	if candidate == nil && kind == harness.RequestKindPlan {
		// 去重键用与卡片详情同源的正文，保证与 pending 里已有的 Detail 对比口径一致。
		key := strings.TrimSpace(requestDetail(kind, req))
		var reuse *pendingRequest
		var waitOn *pendingRequest
		for _, existing := range td.pending {
			if existing.req.Kind != string(harness.RequestKindPlan) || planDedupKey(existing.req.Detail) != key {
				continue
			}
			if existing.handled {
				// 已结算、只在等待送达确认：结论不会再变，直接复用，不再新建请求。
				if reuse == nil && existing.finalResp != nil {
					reuse = existing
				}
				continue
			}
			select {
			case <-existing.settled:
				// 已结算的未处理条目：结论可用就复用，被取消（finalResp 为 nil）则不复用。
				if reuse == nil && existing.finalResp != nil {
					reuse = existing
				}
				continue
			default:
			}
			waitOn = existing
			break
		}
		if waitOn != nil {
			settled := waitOn.settled
			a.b.mu.Unlock()
			select {
			case <-settled:
				if waitOn.finalResp == nil {
					return deny, errors.New("计划请求已取消")
				}
				return planApprovalResponse(*waitOn.finalResp), nil
			case <-ctx.Done():
				return deny, ctx.Err()
			}
		}
		if reuse != nil {
			a.b.mu.Unlock()
			return planApprovalResponse(*reuse.finalResp), nil
		}
	}

	var pr *pendingRequest
	var requestID string
	// createdAsk 在锁内备好新卡的留痕素材，并在解锁前登记异步插入。
	var createdAsk *wire.RemoteRequest

	if candidate != nil {
		pr = candidate
		requestID = candidateID
		pr.ctx = ctx
		pr.handled = false
		pr.awaitingAck = false
		pr.confirmsDelivery = a.confirmsDelivery
		pr.providerWaiting = true
		pr.done = make(chan struct{})
		pr.doneOnce = sync.Once{}
		pr.withdrawCh = make(chan withdrawReason, 1)
		pr.withdrawOnce = sync.Once{}
		// 清空可能残留的旧响应通道消息
		select {
		case <-pr.resCh:
		default:
		}
		if runID := a.b.hub.CurrentRunID(a.threadID); runID != "" {
			pr.req.RunID = runID
		}
		if a.turnID != "" {
			pr.req.TurnID = &a.turnID
		}
	} else {
		requestID = newUUID()
		runID := a.b.hub.CurrentRunID(a.threadID)

		var title *string
		titleText := req.ToolName
		if kind == harness.RequestKindQuestion {
			titleText = "提问"
			if len(req.Questions) > 0 {
				question := req.Questions[0]
				if header := strings.TrimSpace(question.Header); header != "" {
					titleText = header
				} else if prompt := strings.TrimSpace(question.Prompt); prompt != "" {
					runes := []rune(prompt)
					if len(runes) > 24 {
						prompt = string(runes[:24]) + "…"
					}
					titleText = prompt
				}
			}
		} else if kind == harness.RequestKindFileChange {
			for _, key := range []string{"file_path", "path", "notebook_path"} {
				if path := inputField(req.Input, key); path != "" {
					titleText = path
					break
				}
			}
		}
		if titleText != "" {
			t := titleText
			title = &t
		}

		detailStr := requestDetail(kind, req)
		var detail *string
		if detailStr != "" {
			detail = &detailStr
		}

		var diffPtr *string
		if req.Diff != "" {
			d := req.Diff
			diffPtr = &d
		}

		turnID := a.turnID
		if turnID == "" {
			turnID = newUUID()
		}

		var reason *string
		if req.Sensitive != "" {
			reason = &req.Sensitive
		}
		remoteReq := wire.RemoteRequest{
			ThreadID:  a.threadID,
			TurnID:    &turnID,
			RunID:     runID,
			RequestID: requestID,
			Kind:      string(kind),
			Title:     title,
			Detail:    detail,
			Diff:      diffPtr,
			Reason:    reason,
			Options:   requestOptions(kind, req),
			Questions: questionsToWire(req.Questions),
		}

		if td.pending == nil {
			td.pending = make(map[string]*pendingRequest)
		}
		resCh := make(chan approvalResponse, 1)
		pr = &pendingRequest{
			settled:          make(chan struct{}),
			req:              remoteReq,
			toolID:           req.ToolID,
			toolName:         req.ToolName,
			summary:          req.Summary,
			resCh:            resCh,
			done:             make(chan struct{}),
			withdrawCh:       make(chan withdrawReason, 1),
			ctx:              ctx,
			confirmsDelivery: a.confirmsDelivery,
			providerWaiting:  true,
		}
		td.pending[requestID] = pr
		created := remoteReq
		createdAsk = &created
	}

	// 交付确认路径只拿得到 pendingRequest，敏感请求不记账。
	pr.allowKey = ""
	if req.Sensitive == "" {
		pr.allowKey = alwaysAllowKey(kind, req)
	}

	td.summary.HasPendingRequest = true
	td.summary.Attention = "approval"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	td.summary.UpdatedAt = now
	summarySnapshot := a.b.summaryForPublishLocked(td)

	allPending := make([]wire.RemoteRequest, 0, len(td.pending))
	for _, p := range td.pending {
		allPending = append(allPending, p.req)
	}
	// 在解锁前登记插入信号，防止其他撤卡路径先发起结算。
	if createdAsk != nil {
		traceAskCreated(a.b, a.threadID, derefString(createdAsk.TurnID), *createdAsk)
	}
	a.b.mu.Unlock()

	// 推送摘要与待处理请求通知（锁外进行）
	a.b.hub.Publish(a.threadID, nil, nil, summarySnapshot)
	a.b.hub.Notify(a.threadID, "requests.changed", wire.RequestsChangedParams{
		ThreadID: a.threadID,
		Requests: allPending,
	})

	// 等待答复、外部取消或宿主撤卡，期间不持有任何互斥锁。
	var (
		resp      approvalResponse
		canceled  bool
		withdrawn bool
	)
	// 撤卡优先于取消：中断入口先撤卡再 runCancel，这里先做一次非阻塞检查，
	// 避免两个信号同时就绪时 select 随机选到 ctx.Done 而丢掉明确的撤回错误。
	select {
	case reason := <-pr.withdrawCh:
		withdrawn = true
		log.Printf("request %s withdrawn (%s)", pr.req.RequestID, reason)
	default:
	}
	if !withdrawn {
		select {
		case resp = <-pr.resCh:
			if err := ctx.Err(); err != nil {
				canceled = true
				if resp.ack != nil {
					resp.ack <- err
				}
			} else if resp.ack != nil {
				resp.ack <- nil
			}
		case <-ctx.Done():
			// 取消与撤卡同时发生时仍以撤卡为准。
			select {
			case reason := <-pr.withdrawCh:
				withdrawn = true
				log.Printf("request %s withdrawn (%s)", pr.req.RequestID, reason)
			default:
				canceled = true
			}
		case reason := <-pr.withdrawCh:
			// 撤卡与 ctx 取消解耦：卡片被归档、删除、中断或宿主关闭时立刻返回，不再等 Provider 收尾。
			withdrawn = true
			log.Printf("request %s withdrawn (%s)", pr.req.RequestID, reason)
		}
	}

	// 收到答复或取消时异步结算留痕；撤卡由 withdrawPendingLocked 负责。
	if !canceled && !withdrawn {
		traceAskSettled(a.b, requestID, askResolution(kind, resp))
	} else if canceled {
		// 轮次取消：建议计分记为未答复。撤卡已在撤卡路径结算过，靠 outcome='pending' 条件避免双写。
		traceSuggestionUnanswered(a.b, requestID, "canceled")
		noticeThreadID := ""
		if kind == harness.RequestKindQuestion {
			noticeThreadID = a.threadID
		}
		traceAskSettleWithNotice(a.b, requestID, storepkg.PendingAskExpired, askResolutionCallerCancelled, noticeThreadID)
	}

	// 计划结论广播给同轮次挂上来的重复请求，让它们用同一份答复收尾。
	if kind == harness.RequestKindPlan {
		if canceled || withdrawn {
			pr.settle(nil)
		} else {
			respCopy := resp
			pr.settle(&respCopy)
		}
	}

	// 「始终允许」按项目落盘：非送达确认模式下的内存记账在下面清理块里完成，送达确认模式在
	// RespondRequest 里完成，两条路径都经过这里，所以统一在这里持久化。失败只记日志，不放宽也不影响本次放行。
	if shouldPersistAlwaysAllow(kind, canceled, interactive, resp.optionID, pr.allowKey) {
		if err := store.Add(project, pr.allowKey); err != nil {
			log.Printf("persist always-allow key for %s failed: %v", project, err)
		}
	}

	// 请求处理完毕，清理状态并恢复线程摘要
	a.b.mu.Lock()
	pr.providerWaiting = false
	if !a.confirmsDelivery || canceled || withdrawn {
		pr.closeDone()
		td, ok = a.b.threads[a.threadID]
		var cleanupPending []wire.RemoteRequest
		var cleanupSummary wire.ThreadSummary
		if ok {
			delete(td.pending, requestID)
			if len(td.pending) == 0 {
				td.summary.HasPendingRequest = false
				if td.summary.IsRunning {
					td.summary.Attention = "running"
				} else {
					td.summary.Attention = "ready"
				}
			}
			if !canceled && !interactive && resp.optionID == "allow_always" && pr.allowKey != "" {
				if td.alwaysAllow == nil {
					td.alwaysAllow = make(map[string]bool)
				}
				td.alwaysAllow[pr.allowKey] = true
			}
			now = time.Now().UTC().Format(time.RFC3339Nano)
			td.summary.UpdatedAt = now
			cleanupSummary = a.b.summaryForPublishLocked(td)

			cleanupPending = make([]wire.RemoteRequest, 0, len(td.pending))
			for _, p := range td.pending {
				cleanupPending = append(cleanupPending, p.req)
			}
		}
		a.b.mu.Unlock()

		// 再次推送清理后的摘要与通知
		a.b.hub.Publish(a.threadID, nil, nil, cleanupSummary)
		a.b.hub.Notify(a.threadID, "requests.changed", wire.RequestsChangedParams{
			ThreadID: a.threadID,
			Requests: cleanupPending,
		})
	} else {
		// confirmsDelivery 模式下，卡片由 RespondRequest 在收到送达确认后删除，或在送达失败时保留并记录 deliveryError
		a.b.mu.Unlock()
	}

	if withdrawn {
		return deny, errRequestWithdrawn
	}
	if canceled {
		return deny, ctx.Err()
	}

	switch kind {
	case harness.RequestKindQuestion:
		if resp.skip || resp.optionID == "deny" {
			return harness.ApprovalResponse{Decision: harness.DecisionDeny, Skipped: true}, nil
		}
		return harness.ApprovalResponse{Decision: harness.DecisionAllow, Answers: resp.answers}, nil
	case harness.RequestKindPlan:
		if resp.optionID == "allow" || resp.optionID == "allow_always" {
			// 计划获批后把会话切回 build：否则后续轮次仍按 plan 模式（只读）启动，界面也停在 Plan。
			a.switchToBuild()
		}
		return planApprovalResponse(resp), nil
	}

	switch resp.optionID {
	case "allow", "allow_always":
		return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil
	default:
		return deny, nil
	}
}

// planDedupKey 生成计划请求的去重键：计划正文去掉首尾空白后完全相等即视为同一份计划。
// 传输里计划正文落在 RemoteRequest.Detail，缺席时视为空正文。
func planDedupKey(detail *string) string {
	if detail == nil {
		return ""
	}
	return strings.TrimSpace(*detail)
}

// planApprovalResponse 把计划请求的答复转成 Provider 需要的响应：允许即放行，驳回带上反馈文本。
func planApprovalResponse(resp approvalResponse) harness.ApprovalResponse {
	if resp.optionID == "allow" || resp.optionID == "allow_always" {
		return harness.ApprovalResponse{Decision: harness.DecisionAllow, Answers: resp.answers}
	}
	out := harness.ApprovalResponse{Decision: harness.DecisionDeny, Answers: resp.answers, Skipped: resp.skip}
	if fb := resp.answers["feedback"]; len(fb) > 0 {
		out.Feedback = fb[0]
	}
	return out
}

// switchToBuild 在计划获批后把会话交互模式从 plan 切回 build，并写穿到 store、推送新摘要。
// 用独立上下文：本次请求的 ctx 可能随审批投递结束而被取消。
func (a *threadApprover) switchToBuild() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	th, err := a.b.GetThread(ctx, a.threadID)
	if err != nil || normalizeInteractionMode(th.InteractionMode) == "build" {
		return
	}
	_ = a.b.SetInteractionMode(ctx, a.threadID, "build")
}

// mcpToolAutoApproved 判断工具名是否符合 mcp__<server>__<tool> 约定，且命中数据目录下策略文件的免审批开关。
func mcpToolAutoApproved(dataDir, toolName string) bool {
	if dataDir == "" {
		return false
	}
	server, tool, ok := agentconf.SplitMCPToolName(toolName)
	if !ok {
		return false
	}
	policy, err := agentconf.LoadMCPToolPolicy(agentconf.MCPToolPolicyPath(dataDir))
	if err != nil {
		return false
	}
	return policy.IsAutoApproved(server, tool)
}

// safeReadOnlyBinaries 是只读档直接放行的安全指令：它们只读取文件、输出环境信息，
// 或切换 shell 的当前目录（cd），都不写文件。
var safeReadOnlyBinaries = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "grep": true, "rg": true,
	"find": true, "wc": true, "pwd": true, "echo": true, "which": true, "stat": true,
	"file": true, "du": true, "tree": true, "sort": true, "uniq": true, "diff": true,
	"cd": true,
}

// safeReadOnlyNullRedirect 匹配丢弃到 /dev/null 的重定向：这些只丢弃输出，不写文件。
// 只有 /dev/null 后面紧跟空白、分号、竖线、& 或文本结尾才算无害；
// 尾部字符留在捕获组里原样放回，避免把两条命令粘连成一条（Go 正则不支持前瞻，只能这样写）。
var safeReadOnlyNullRedirect = regexp.MustCompile(`\d*&?>>?\s*/dev/null(\s|;|\||&|$)`)

// safeReadOnlyFDMerge 匹配 2>&1 这类把一路输出并到另一路的重定向：只合并输出，不写文件。
var safeReadOnlyFDMerge = regexp.MustCompile(`\d*>&\d+`)

// safeReadOnlyGitSubcommands 是 git 里只读的子命令。
var safeReadOnlyGitSubcommands = map[string]bool{
	"status": true, "diff": true, "log": true, "show": true, "branch": true, "blame": true,
}

// safeReadOnlyGitDeniedFlags 是只读子命令下仍会写盘、改配置或执行外部程序的 git 选项：出现即不算只读。
// 例如 `git diff --output=<file>` 会把 diff 写进任意文件，`-c` / `--exec-path` 可改配置或换掉执行的程序。
var safeReadOnlyGitDeniedFlags = []string{
	"--output", "--exec-path", "--git-dir", "--work-tree", "--config-env", "-c",
}

// gitArgDenied 判断 git 参数是否命中禁止项，同时覆盖 `--flag=value` 与 `-cname=value` 两种连写形式。
func gitArgDenied(arg string) bool {
	for _, flag := range safeReadOnlyGitDeniedFlags {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return strings.HasPrefix(arg, "-c") && len(arg) > 2
}

// safeReadOnlyGitBranchFlags 是 `git branch` 中只查询引用、不新增或删除分支的选项。
var safeReadOnlyGitBranchFlags = map[string]bool{
	"-a": true, "-r": true, "-v": true, "-vv": true, "-l": true, "--list": true,
	"--all": true, "--remotes": true, "--verbose": true, "--show-current": true, "--no-color": true,
}

// safeReadOnlyFindFlags 是 find 中会删除文件或执行外部程序的选项，出现即不算只读。
var safeReadOnlyFindFlags = map[string]bool{
	"-delete": true, "-exec": true, "-execdir": true, "-ok": true, "-okdir": true,
	"-fprint": true, "-fprint0": true, "-fprintf": true, "-fls": true,
}

// isSafeReadOnlyCommand 判断命令是否仅由安全只读指令组成，供只读档直接放行。
// 重定向、命令替换、后台执行都可能写文件或执行未知程序，一律不算只读。
func isSafeReadOnlyCommand(cmd string) bool {
	text := strings.TrimSpace(cmd)
	if text == "" {
		return false
	}
	// 丢弃到 /dev/null 与 2>&1 这类合并输出的重定向都不写文件，先摘掉再走原有检查；
	// 只在判定用的局部变量上摘，不改动传入的命令文本。
	text = safeReadOnlyNullRedirect.ReplaceAllString(text, "$1")
	text = safeReadOnlyFDMerge.ReplaceAllString(text, "")
	if strings.ContainsAny(text, "`<>") || strings.Contains(text, "$(") {
		return false
	}
	segments := strings.NewReplacer("&&", "\n", "||", "\n", "|", "\n", ";", "\n").Replace(text)
	for _, segment := range strings.Split(segments, "\n") {
		if !isSafeReadOnlySegment(segment) {
			return false
		}
	}
	return true
}

// isSafeReadOnlySegment 校验单个命令段，允许空段（如结尾的 ;）。
func isSafeReadOnlySegment(segment string) bool {
	fields := strings.Fields(segment)
	if len(fields) == 0 {
		return true
	}
	for _, f := range fields {
		if strings.ContainsAny(f, "&<>`") {
			return false
		}
	}
	name := filepath.Base(fields[0])
	if name == "git" {
		if len(fields) < 2 || !safeReadOnlyGitSubcommands[fields[1]] {
			return false
		}
		// 白名单子命令统一叠禁写盘与改配置的选项，不能只校验 `git branch` 一个子命令。
		for _, arg := range fields[2:] {
			if gitArgDenied(arg) {
				return false
			}
		}
		if fields[1] == "branch" {
			for _, arg := range fields[2:] {
				if strings.HasPrefix(arg, "--color=") {
					continue
				}
				if !safeReadOnlyGitBranchFlags[arg] {
					return false
				}
			}
		}
		return true
	}
	if !safeReadOnlyBinaries[name] {
		return false
	}
	switch name {
	case "find":
		for _, arg := range fields[1:] {
			if safeReadOnlyFindFlags[arg] {
				return false
			}
		}
	case "sort":
		for _, arg := range fields[1:] {
			if arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "--output=") || strings.HasPrefix(arg, "-o") && len(arg) > 2 {
				return false
			}
		}
	}
	return true
}

// approvalCommand 取出命令类请求的真实命令文本：优先输入里的 command 字段，
// 缺失时退回 Summary（个别 provider 只把命令写进摘要）。
func approvalCommand(req harness.ApprovalRequest) string {
	if cmd := strings.TrimSpace(inputField(req.Input, "command")); cmd != "" {
		return cmd
	}
	return strings.TrimSpace(req.Summary)
}

// fullAllowKey 保留旧版完整命令键，供兼容匹配及无法提取前缀时使用。
func fullAllowKey(kind harness.RequestKind, req harness.ApprovalRequest) string {
	if kind != harness.RequestKindCommand {
		return req.ToolName
	}
	command := approvalCommand(req)
	if command == "" {
		// 取不到命令文本时不记账：宁可每次询问，也不能退化成整类 shell 免审批。
		return ""
	}
	return req.ToolName + "\x00" + command
}

var commandPrefixArg = regexp.MustCompile(`^[a-z][a-z0-9:_-]*$`)
var commandProgram = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]*$`)
var commandAssignment = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*=`)
var commandNullRedirect = regexp.MustCompile(`(^|[[:space:]])(2>&1|2?>[[:space:]]*/dev/null)([[:space:]]|$)`)

// commandSegments 仅接受可以安全按简单命令段解析的语法。
func commandSegments(command string) []string {
	if strings.ContainsAny(command, "\n\r`'\"\\$(){}") || strings.Contains(command, "<<") {
		return nil
	}
	for {
		next := commandNullRedirect.ReplaceAllString(command, "$1$3")
		if next == command {
			break
		}
		command = next
	}
	if strings.ContainsAny(command, "<>") {
		return nil
	}
	command = strings.NewReplacer("&&", "\n", "||", "\n", "|", "\n", ";", "\n").Replace(command)
	if strings.Contains(command, "&") {
		return nil
	}
	var segments []string
	for _, part := range strings.Split(command, "\n") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			return nil
		}
		if fields[0] == "cd" {
			continue
		}
		segments = append(segments, part)
	}
	return segments
}

func commandSegmentPrefix(segment string) string {
	fields := strings.Fields(segment)
	if len(fields) == 0 || commandAssignment.MatchString(fields[0]) {
		return ""
	}
	name := filepath.Base(fields[0])
	switch name {
	case "sh", "bash", "zsh", "fish", "python", "python3", "node", "ruby", "perl", "php", "deno", "eval", "exec", "sudo", "su", "env", "xargs", "rm", "curl", "wget", "ssh", "scp", "dd", "chmod", "chown", "osascript", "security", "open":
		return ""
	}
	if !commandProgram.MatchString(name) {
		return ""
	}
	if len(fields) > 1 && commandPrefixArg.MatchString(fields[1]) {
		return name + " " + fields[1]
	}
	return name
}

func commandAllowPrefix(command string) string {
	segments := commandSegments(command)
	if len(segments) != 1 {
		return ""
	}
	return commandSegmentPrefix(segments[0])
}

func alwaysAllowKey(kind harness.RequestKind, req harness.ApprovalRequest) string {
	if kind == harness.RequestKindCommand {
		if prefix := commandAllowPrefix(approvalCommand(req)); prefix != "" {
			return req.ToolName + "\x00prefix\x00" + prefix
		}
	}
	return fullAllowKey(kind, req)
}

func commandPrefixesAllowed(kind harness.RequestKind, req harness.ApprovalRequest, allowed func(string) bool) bool {
	if kind != harness.RequestKindCommand {
		return false
	}
	segments := commandSegments(approvalCommand(req))
	if len(segments) == 0 {
		return false
	}
	for _, segment := range segments {
		prefix := commandSegmentPrefix(segment)
		if prefix == "" || !allowed(req.ToolName+"\x00prefix\x00"+prefix) {
			return false
		}
	}
	return true
}

// alwaysAllowStore 返回与该后端数据目录绑定的持久化「始终允许」存储；调用方不能持有 b.mu。
func (b *MemoryBackend) alwaysAllowStore() *alwaysAllowStore {
	b.mu.Lock()
	dataDir := b.dataDir
	b.mu.Unlock()
	return alwaysAllowStoreFor(dataDir)
}

// alwaysAllowProjectKey 返回持久化「始终允许」用的项目键：优先会话工作目录（Clean 后解析符号链接），
// 工作目录与项目路径都取不到时退回项目 ID；两者都没有则返回空串，此时既不查持久化名单也不写入。
func (b *MemoryBackend) alwaysAllowProjectKey(threadID string) string {
	b.mu.Lock()
	raw := b.alwaysAllowProjectRawLocked(threadID)
	b.mu.Unlock()
	return normalizeAlwaysAllowProject(raw)
}

// alwaysAllowProjectRawLocked 取项目键的原始值，调用方需持有 b.mu。
func (b *MemoryBackend) alwaysAllowProjectRawLocked(threadID string) string {
	td, ok := b.threads[threadID]
	if !ok {
		return ""
	}
	if dir, ok := b.threadWorkDirLocked(threadID); ok && dir != "" {
		return dir
	}
	return td.summary.ProjectID
}

// shouldPersistAlwaysAllow 判断本次答复是否要把「始终允许」写进持久化存储：
// 计划、提问这类交互请求与权限申请一律不持久化，被取消的答复、没有记账键的请求同样不写。
// 权限申请按工具名记账会连同后续不同的权限集一起免审，因此只保留会话内有效。
func shouldPersistAlwaysAllow(kind harness.RequestKind, canceled, interactive bool, optionID, allowKey string) bool {
	if canceled || interactive || allowKey == "" || optionID != "allow_always" {
		return false
	}
	switch kind {
	case harness.RequestKindCommand, harness.RequestKindFileChange, harness.RequestKindTool, harness.RequestKindCommit:
		return true
	default:
		return false
	}
}

// ListAlwaysAllowed 列出某个项目目录下持久化保存的「始终允许」记账键，供设置界面展示与撤销。
func (b *MemoryBackend) ListAlwaysAllowed(project string) []string {
	return b.alwaysAllowStore().List(project)
}

// RevokeAlwaysAllowed 撤销某个项目下的一条「始终允许」：清掉持久化记录，并把该项目所有线程内存里的同键
// 一并删除（其它项目的同键不受影响），撤销后同一条命令会重新弹出审批。key 取 ListAlwaysAllowed 里的原值。
func (b *MemoryBackend) RevokeAlwaysAllowed(project, key string) error {
	if key == "" {
		return nil
	}
	target := normalizeAlwaysAllowProject(project)

	// 项目键比对需要解析符号链接，先在锁内收集原始值，锁外比对后再回锁清理，避免持锁做文件系统调用。
	b.mu.Lock()
	rawProjects := make(map[string]string, len(b.threads))
	for id := range b.threads {
		rawProjects[id] = b.alwaysAllowProjectRawLocked(id)
	}
	b.mu.Unlock()

	threadIDs := make([]string, 0, len(rawProjects))
	for id, raw := range rawProjects {
		if normalizeAlwaysAllowProject(raw) == target {
			threadIDs = append(threadIDs, id)
		}
	}

	b.mu.Lock()
	for _, id := range threadIDs {
		if td, ok := b.threads[id]; ok {
			delete(td.alwaysAllow, key)
		}
	}
	b.mu.Unlock()

	return b.alwaysAllowStore().Remove(target, key)
}

// inferRequestKind 按工具名推断请求种类。
func inferRequestKind(toolName string) harness.RequestKind {
	switch toolName {
	case "Bash", "bash", "shell", "Shell", "exec_command", "command", "local_shell":
		return harness.RequestKindCommand
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "apply_patch", "edit", "write":
		return harness.RequestKindFileChange
	case "commit", "git_commit", "gitCommit", "push", "git_push", "gitPush":
		return harness.RequestKindCommit
	case "request_permissions", "requestPermissions", "permissions", "item/permissions/requestApproval":
		return harness.RequestKindPermissions
	default:
		return harness.RequestKindTool
	}
}

// requestDetail 优先显示命令或计划正文，其余显示完整输入，无有效输入时沿用 Summary。
func requestDetail(kind harness.RequestKind, req harness.ApprovalRequest) string {
	switch kind {
	case harness.RequestKindPlan:
		if req.Plan != "" {
			return req.Plan
		}
	case harness.RequestKindCommand:
		if v := inputField(req.Input, "command"); v != "" {
			return v
		}
	case harness.RequestKindCommit:
		for _, key := range []string{"message", "commit_message", "command", "msg"} {
			if v := inputField(req.Input, key); v != "" {
				return v
			}
		}
	case harness.RequestKindPermissions:
		if len(req.Input) > 0 {
			var permData struct {
				Permissions json.RawMessage `json:"permissions"`
			}
			if err := json.Unmarshal(req.Input, &permData); err == nil && len(permData.Permissions) > 0 {
				var list []string
				if err := json.Unmarshal(permData.Permissions, &list); err == nil && len(list) > 0 {
					return strings.Join(list, "\n")
				}
				var anyList []any
				if err := json.Unmarshal(permData.Permissions, &anyList); err == nil && len(anyList) > 0 {
					var lines []string
					for _, item := range anyList {
						if s, ok := item.(string); ok {
							lines = append(lines, s)
						} else if b, err := json.Marshal(item); err == nil {
							lines = append(lines, string(b))
						}
					}
					if len(lines) > 0 {
						return strings.Join(lines, "\n")
					}
				}
				var permMap map[string]any
				if err := json.Unmarshal(permData.Permissions, &permMap); err == nil && len(permMap) > 0 {
					var lines []string
					for k, v := range permMap {
						b, _ := json.Marshal(v)
						lines = append(lines, fmt.Sprintf("%s: %s", k, string(b)))
					}
					slices.Sort(lines)
					return strings.Join(lines, "\n")
				}
			}
		}
	}
	// 授权前必须能核对完整参数，不能沿用 provider 截断后的摘要。
	if len(req.Input) > 0 {
		if formatted, err := json.MarshalIndent(req.Input, "", "  "); err == nil {
			return string(formatted)
		}
	}
	if req.Summary != "" {
		return req.Summary
	}
	if len(req.Input) > 0 {
		return string(req.Input)
	}
	return ""
}

// validateQuestionAnswers 在消费请求前校验题目完整性与单选/多选约束。
func validateQuestionAnswers(questions []wire.RemoteQuestion, answers map[string][]string) error {
	if len(questions) == 0 || len(answers) != len(questions) {
		return errors.New("answers must contain every question")
	}
	for _, q := range questions {
		values := answers[q.ID]
		if len(values) == 0 || (!q.AllowsMultiple && len(values) != 1) {
			return fmt.Errorf("invalid answer count for question %q", q.ID)
		}
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("empty answer for question %q", q.ID)
			}
			if len(q.Choices) > 0 && !q.AllowsOther && !slices.ContainsFunc(q.Choices, func(c wire.RemoteQuestionChoice) bool {
				return c.Label == value
			}) {
				return fmt.Errorf("unknown choice for question %q", q.ID)
			}
		}
	}
	return nil
}

// inputField 从输入 JSON 对象中读取字符串字段；字符串数组按空格拼接。
func inputField(input json.RawMessage, key string) string {
	if len(input) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(input, &obj); err != nil {
		return ""
	}
	raw, ok := obj[key]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []string
	if err := json.Unmarshal(raw, &parts); err == nil {
		return strings.Join(parts, " ")
	}
	return ""
}

// requestOptions 按请求种类生成可选操作。
func requestOptions(kind harness.RequestKind, req harness.ApprovalRequest) []wire.RemoteRequestOption {
	if req.Sensitive != "" {
		return []wire.RemoteRequestOption{{ID: "allow", Title: "允许", Role: "approve"}, {ID: "deny", Title: "拒绝", Role: "decline"}}
	}
	switch kind {
	case harness.RequestKindPlan:
		return []wire.RemoteRequestOption{
			{ID: "allow", Title: "批准计划", Role: "approve"},
			{ID: "deny", Title: "驳回", Role: "decline"},
		}
	case harness.RequestKindCommit:
		return []wire.RemoteRequestOption{
			{ID: "allow", Title: "允许", Role: "approve"},
			{ID: "deny", Title: "拒绝", Role: "decline"},
		}
	case harness.RequestKindPermissions:
		return []wire.RemoteRequestOption{
			{ID: "allow", Title: "允许", Role: "approve"},
			{ID: "allow_always", Title: "本会话始终允许", Role: "approveAlways"},
			{ID: "deny", Title: "拒绝", Role: "decline"},
		}
	case harness.RequestKindQuestion:
		return []wire.RemoteRequestOption{}
	default:
		title := "在此项目始终允许"
		if kind == harness.RequestKindCommand {
			if prefix := commandAllowPrefix(approvalCommand(req)); prefix != "" {
				title += " " + prefix
			}
		}
		return []wire.RemoteRequestOption{
			{ID: "allow", Title: "允许", Role: "approve"},
			{ID: "allow_always", Title: title, Role: "approveAlways"},
			{ID: "deny", Title: "拒绝", Role: "decline"},
		}
	}
}

// questionsToWire 将 harness 题目转换为 wire 层题目，Detail 为空时置 nil。
func questionsToWire(qs []harness.Question) []wire.RemoteQuestion {
	out := make([]wire.RemoteQuestion, 0, len(qs))
	for _, q := range qs {
		choices := make([]wire.RemoteQuestionChoice, 0, len(q.Choices))
		for _, c := range q.Choices {
			var detail *string
			if c.Detail != "" {
				d := c.Detail
				detail = &d
			}
			choices = append(choices, wire.RemoteQuestionChoice{Label: c.Label, Detail: detail})
		}
		out = append(out, wire.RemoteQuestion{
			ID:             q.ID,
			Header:         q.Header,
			Prompt:         q.Prompt,
			Choices:        choices,
			AllowsMultiple: q.AllowsMultiple,
			AllowsOther:    q.AllowsOther,
			IsSecret:       q.IsSecret,
		})
	}
	return out
}
