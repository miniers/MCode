// 本文件包含 MemoryBackend 的消息发送与轮次状态方法。
package remote

import (
	"context"
	"errors"
	"fmt"
	"log"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"strings"
	"time"
)

// AppendNotice 向会话时间线追加一条系统提示条目，并进行持久化与增量推送。
func (b *MemoryBackend) AppendNotice(threadID, text string) error {
	return b.appendSystemMessage(threadID, "info", text)
}

// appendSystemMessage 向会话时间线追加一条指定 Kind 的系统消息，并进行持久化与增量推送。
func (b *MemoryBackend) appendSystemMessage(threadID, kind, text string) error {
	nowTime := time.Now().UTC()

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return err
	}
	b.ensureMessagesLoadedLocked(td)
	msg, currentSummary := b.appendSystemMessageLocked(td, kind, text, nowTime)
	b.mu.Unlock()

	b.publishSystemMessage(threadID, msg, currentSummary, nowTime)
	return nil
}

// appendSystemMessageLocked 在已持有 b.mu 的前提下追加系统消息，返回消息副本与推送用摘要。
func (b *MemoryBackend) appendSystemMessageLocked(td *memoryThread, kind, text string, nowTime time.Time) (wire.ThreadMessage, wire.ThreadSummary) {
	now := nowTime.Format(time.RFC3339Nano)
	b.touchThreadLocked(td)

	turnIndex := 0
	var turnIDPtr *string
	if td.runTurnID != "" {
		tid := td.runTurnID
		turnIDPtr = &tid
	}
	if len(td.messages) > 0 {
		turnIndex = td.messages[len(td.messages)-1].TurnIndex
	}

	msg := wire.ThreadMessage{
		ID:        newUUID(),
		Role:      "system",
		Text:      text,
		CreatedAt: now,
		TurnIndex: turnIndex,
		TurnID:    turnIDPtr,
		Kind:      &kind,
	}

	td.messages = append(td.messages, msg)
	td.summary.UpdatedAt = now
	return msg, b.summaryForPublishLocked(td)
}

// publishSystemMessage 把新追加的系统消息写库并增量推送，最后刷新会话时间。
func (b *MemoryBackend) publishSystemMessage(threadID string, msg wire.ThreadMessage, currentSummary wire.ThreadSummary, nowTime time.Time) {
	b.persistMessages(threadID, []wire.ThreadMessage{msg})
	b.hub.Publish(threadID, []wire.ThreadMessage{msg}, nil, currentSummary)

	if s := b.store; s != nil {
		pCtx, pCancel := persistCtx()
		_ = s.TouchThread(pCtx, threadID, nowTime)
		pCancel()
	}
}

// RequestSplit 请求分割会话：在时间线末尾追加一条分割线（split_session），
// 从下一条消息起 provider 只看到分割线之后的历史。
// 分割线本身就是持久标记，重启后状态一致；已经处于待分割状态时幂等返回 nil。
func (b *MemoryBackend) RequestSplit(threadID string) error {
	nowTime := time.Now().UTC()

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return err
	}
	b.ensureMessagesLoadedLocked(td)
	if td.runTurnID != "" {
		b.mu.Unlock()
		return ErrSplitRunning
	}
	conversed := false
	for _, msg := range td.messages {
		if msg.Role == "user" || msg.Role == "assistant" {
			conversed = true
			break
		}
	}
	if !conversed {
		b.mu.Unlock()
		return ErrSplitEmpty
	}
	if pendingSplit(td.messages) {
		// 已经有一条还没生效的分割线，不追加第二条。
		b.mu.Unlock()
		return nil
	}
	msg, currentSummary := b.appendSystemMessageLocked(td, splitSessionKind, splitSessionText, nowTime)
	b.mu.Unlock()

	b.publishSystemMessage(threadID, msg, currentSummary, nowTime)
	return nil
}

// AppendAssistantMessage 追加一条总管主动发出的助手消息：Role 为 assistant、Kind 为空，
// 并标记为最终回复，作为可见回复展示（开场白就是这样做显的）。与 AppendNotice 一样持久化
// 并增量推送，只是不摥任何 Kind。
func (b *MemoryBackend) AppendAssistantMessage(threadID, text string) error {
	nowTime := time.Now().UTC()
	now := nowTime.Format(time.RFC3339Nano)

	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return store.ErrNotFound
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)

	turnIndex := 0
	var turnIDPtr *string
	if td.runTurnID != "" {
		tid := td.runTurnID
		turnIDPtr = &tid
	}
	if len(td.messages) > 0 {
		turnIndex = td.messages[len(td.messages)-1].TurnIndex
	}

	final := true
	msg := wire.ThreadMessage{
		ID:              newUUID(),
		Role:            "assistant",
		Text:            text,
		CreatedAt:       now,
		TurnIndex:       turnIndex,
		TurnID:          turnIDPtr,
		IsFinalResponse: &final,
	}

	td.messages = append(td.messages, msg)
	td.summary.UpdatedAt = now
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.persistMessages(threadID, []wire.ThreadMessage{msg})
	b.hub.Publish(threadID, []wire.ThreadMessage{msg}, nil, currentSummary)

	if s := b.store; s != nil {
		pCtx, pCancel := persistCtx()
		_ = s.TouchThread(pCtx, threadID, nowTime)
		pCancel()
	}

	return nil
}

// SendMessage 接收用户消息并启动后台模拟回复。
func (b *MemoryBackend) SendMessage(ctx context.Context, params wire.SendMessageParams) (wire.SendMessageResult, error) {
	b.mu.Lock()
	desk := b.stewardDesk
	b.mu.Unlock()
	if desk != nil && desk.IsFeed(params.ThreadID) {
		return b.acceptStewardMessage(ctx, params, desk)
	}
	b.cancelAutoContinue(params.ThreadID)
	// C-TH-02/03: 临时线程发消息时转为正式线程
	_ = b.retainThreadInternal(ctx, params.ThreadID)
	// C-TH-06: 线程已休眠时自动取消休眠
	b.reactivateThreadIfDormant(ctx, params.ThreadID)
	trimmed := strings.TrimSpace(params.Text)

	// /compact 保持原样，交给 Provider 自身处理上下文压缩
	if trimmed == "/compact" {
		return b.sendMessage(ctx, params, false)
	}

	if trimmed == "/plan" {
		if params.ActionID != "" && b.isTombstoned(params.ActionID) {
			return wire.SendMessageResult{}, &wire.RPCError{
				Code:    wire.ErrCodeOperationRejected,
				Message: "动作已被客户端结算取消，未曾执行",
			}
		}

		b.mu.Lock()
		td, ok := b.threads[params.ThreadID]
		if !ok {
			b.mu.Unlock()
			rpcErr := &wire.RPCError{
				Code:    wire.ErrCodeResourceNotFound,
				Message: fmt.Sprintf("thread %s not found", params.ThreadID),
			}
			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID: params.ActionID,
					State:    wire.ActionStateFailed,
					ThreadID: &params.ThreadID,
					Error: &wire.ActionError{
						Code:    rpcErr.Code,
						Message: rpcErr.Message,
					},
				})
			}
			return wire.SendMessageResult{}, rpcErr
		}

		currMode := normalizeInteractionMode(td.summary.InteractionMode)
		newMode := "plan"
		if currMode == "plan" {
			newMode = "build"
		}

		// 复用已有模式更新与持久化路径
		if b.store != nil {
			pCtx, pCancel := persistCtx()
			effort := ""
			if td.summary.Effort != nil {
				effort = *td.summary.Effort
			}
			beesProvider := ""
			if td.summary.SwarmBeesProvider != nil {
				beesProvider = *td.summary.SwarmBeesProvider
			}
			beesModel := ""
			if td.summary.SwarmBeesModel != nil {
				beesModel = *td.summary.SwarmBeesModel
			}
			beesEffort := ""
			if td.summary.SwarmBeesEffort != nil {
				beesEffort = *td.summary.SwarmBeesEffort
			}
			if err := b.store.UpdateThreadComposerPrefs(pCtx, params.ThreadID, newMode, effort, beesProvider, beesModel, beesEffort, td.summary.Persona); err != nil {
				pCancel()
				b.mu.Unlock()
				return wire.SendMessageResult{}, err
			}
			pCancel()
		}

		td.summary.InteractionMode = newMode
		now := time.Now().UTC().Format(time.RFC3339Nano)
		td.summary.UpdatedAt = now
		currentSummary := b.summaryForPublishLocked(td)
		b.mu.Unlock()

		b.hub.Publish(params.ThreadID, nil, nil, currentSummary)

		result := wire.SendMessageResult{
			ActionID: params.ActionID,
			ThreadID: params.ThreadID,
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateAccepted,
				ThreadID: &params.ThreadID,
			})
		}
		return result, nil
	}

	if strings.HasPrefix(trimmed, "/plan ") {
		planMode := "plan"
		if params.Options == nil {
			params.Options = &wire.ComposerOptions{
				InteractionMode: &planMode,
			}
		} else {
			params.Options.InteractionMode = &planMode
		}

		b.mu.Lock()
		if td, ok := b.threads[params.ThreadID]; ok {
			td.summary.InteractionMode = "plan"
			if b.store != nil {
				pCtx, pCancel := persistCtx()
				effort := ""
				if td.summary.Effort != nil {
					effort = *td.summary.Effort
				}
				beesProvider := ""
				if td.summary.SwarmBeesProvider != nil {
					beesProvider = *td.summary.SwarmBeesProvider
				}
				beesModel := ""
				if td.summary.SwarmBeesModel != nil {
					beesModel = *td.summary.SwarmBeesModel
				}
				beesEffort := ""
				if td.summary.SwarmBeesEffort != nil {
					beesEffort = *td.summary.SwarmBeesEffort
				}
				_ = b.store.UpdateThreadComposerPrefs(pCtx, params.ThreadID, "plan", effort, beesProvider, beesModel, beesEffort, td.summary.Persona)
				pCancel()
			}
			td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
			currentSummary := b.summaryForPublishLocked(td)
			b.hub.Publish(params.ThreadID, nil, nil, currentSummary)
		}
		b.mu.Unlock()

		params.Text = strings.TrimSpace(trimmed[len("/plan "):])
	}

	return b.sendMessage(ctx, params, false)
}

// SendSwarmReport 在主会话忙碌时将汇报放在普通跟进之前。
func (b *MemoryBackend) SendSwarmReport(ctx context.Context, threadID, text string) error {
	_, err := b.sendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID, Text: text, WhenRunning: "queue",
	}, true)
	return err
}

// SendHostEvent 向总管会话投递一条宿主事件：行为与 SendSwarmReport 一致（忙碌时排队，
// 随后开新一轮模型回合，提示词装饰器照常生效），只把落库并广播的那条用户消息 Kind 换成
// host_event，让客户端能把它当成事件通知而不是用户输入。
func (b *MemoryBackend) SendHostEvent(ctx context.Context, threadID, text string) error {
	_, err := b.sendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID, Text: text, WhenRunning: "queue",
	}, true, threadMessageKindHostEvent)
	return err
}

func (b *MemoryBackend) sendMessage(ctx context.Context, params wire.SendMessageParams, priority bool, userKind ...string) (wire.SendMessageResult, error) {
	// userKind 指定落库的用户消息 Kind（如 host_event）；为空表示普通用户消息。
	kind := ""
	if len(userKind) > 0 {
		kind = strings.TrimSpace(userKind[0])
	}
	if params.ActionID != "" && b.isTombstoned(params.ActionID) {
		return wire.SendMessageResult{}, &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: "动作已被客户端结算取消，未曾执行",
		}
	}

	if params.WhenRunning != "" && params.WhenRunning != "queue" {
		rpcErr := &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: fmt.Sprintf("invalid whenRunning %q", params.WhenRunning),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	if params.ActionID != "" {
		b.actions.record(wire.ActionStatusResult{
			ActionID: params.ActionID,
			State:    wire.ActionStatePending,
			ThreadID: &params.ThreadID,
		})
	}

	hasBees := b.hasWorkingSwarmBees(params.ThreadID)
	if params.WhenRunning == "" && len(params.Attachments) == 0 {
		b.mu.Lock()
		sendToBee := b.swarmSendToBee
		thread, exists := b.threads[params.ThreadID]
		mode := "build"
		if exists {
			mode = normalizeInteractionMode(thread.summary.InteractionMode)
		}
		if params.Options != nil && params.Options.InteractionMode != nil {
			mode = *params.Options.InteractionMode
		}
		b.mu.Unlock()
		// priority（swarm 汇报）只回投主代理，绝不转发给子代理。
		if sendToBee != nil && exists && mode != "plan" && !priority {
			beeID, err := sendToBee(ctx, params.ThreadID, params.Text)
			if err != nil {
				if params.ActionID != "" {
					b.actions.record(wire.ActionStatusResult{
						ActionID: params.ActionID, ThreadID: &params.ThreadID, State: wire.ActionStateFailed,
						Error: &wire.ActionError{Code: wire.ErrCodeOperationRejected, Message: err.Error()},
					})
				}
				return wire.SendMessageResult{}, err
			}
			if beeID != "" {
				if params.ActionID != "" {
					b.actions.record(wire.ActionStatusResult{
						ActionID: params.ActionID, ThreadID: &params.ThreadID, State: wire.ActionStateAccepted,
					})
				}
				return wire.SendMessageResult{
					ActionID: params.ActionID, ThreadID: params.ThreadID, DelegatedThreadID: &beeID,
				}, nil
			}
		}
	}
	general := b.generalSettings()
	queueWhileRunning := general.Session.QueueWhileRunning
	insertIntoRunningTurn := general.Session.InsertIntoRunningTurn
	b.mu.Lock()
	td, ok := b.threads[params.ThreadID]
	if !ok {
		b.mu.Unlock()
		rpcErr := &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: fmt.Sprintf("thread %s not found", params.ThreadID),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	// C-ST-07：消息被淘汰的归档会话在发送前先回载历史，避免上下文丢失。
	if err := b.ensureMessagesLocked(td, params.ThreadID); err != nil {
		b.mu.Unlock()
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error:    &wire.ActionError{Code: wire.InternalError, Message: err.Error()},
			})
		}
		return wire.SendMessageResult{}, err
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)

	if td.takingBack {
		b.mu.Unlock()
		return wire.SendMessageResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "turn is being taken back"}
	}
	isRunning := td.summary.IsRunning

	// followUpIntent 记录用户本意是插入当前回合、但服务端无法注入而降级成排队的情况；
	// 普通排队保持空串。
	followUpIntent := ""

	// 运行中的普通发送：开启「插入当前回合」时优先把消息注入正在执行的轮次
	// （harness.Steerer，Provider 相关），无法注入时退回排队而不是直接拒绝。
	if isRunning && params.WhenRunning == "" && !priority {
		if td.phase != phaseRunning {
			// 轮次仍在启动阶段，服务商还没进入可注入状态：开启插入时退回排队。
			if insertIntoRunningTurn {
				params.WhenRunning = "queue"
				followUpIntent = "insert"
			}
		} else if steerer, ok := b.threadHarnessLocked(td.summary.Provider).(harness.Steerer); ok && td.sessionID != "" && len(params.Attachments) == 0 && params.Options == nil {
			if td.hasWaitingQuestionLocked() {
				turnID := td.runTurnID
				fu := wire.FollowUp{ID: newUUID(), Text: params.Text, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Kind: kind}
				td.setPendingSendLocked(fu)
				_, batch, err := b.interruptLocked(td, turnID)
				b.mu.Unlock()
				b.commitQueueChange(params.ThreadID)
				b.finishWithdraw(batch, withdrawInterrupt)
				if err != nil {
					return wire.SendMessageResult{}, err
				}
				if params.ActionID != "" {
					b.actions.record(wire.ActionStatusResult{ActionID: params.ActionID, State: wire.ActionStateAccepted, ThreadID: &params.ThreadID, TurnID: &turnID, FollowUpID: &fu.ID})
				}
				return wire.SendMessageResult{ActionID: params.ActionID, ThreadID: params.ThreadID, TurnID: &turnID, FollowUpID: &fu.ID}, nil
			}
			turnID, sessionID := td.runTurnID, td.sessionID
			b.mu.Unlock()
			err := steerer.Steer(ctx, sessionID, params.Text)
			b.mu.Lock()
			if err == nil {
				if b.threads[params.ThreadID] != td || td.runTurnID != turnID {
					b.mu.Unlock()
					return wire.SendMessageResult{}, &wire.RPCError{Code: wire.ErrCodeOperationRejected, Message: "turn ended while steering"}
				}
				index := td.messages[len(td.messages)-1].TurnIndex
				msg := wire.ThreadMessage{ID: newUUID(), Role: "user", Text: params.Text, TurnID: &turnID, TurnIndex: index, CreatedAt: b.nowLocked().Format(time.RFC3339Nano)}
				applyTurnTiming(&msg, td.turnTiming)
				td.messages = append(td.messages, msg)
				// 转向的消息同样是用户发来的，刷新最后用户消息时间，与 startTurnLocked 保持一致。
				td.summary.LastUserMessageAt = &msg.CreatedAt
				summary := b.summaryForPublishLocked(td)
				b.mu.Unlock()
				b.persistMessages(params.ThreadID, []wire.ThreadMessage{msg})
				b.hub.Publish(params.ThreadID, []wire.ThreadMessage{msg}, nil, summary)
				if params.ActionID != "" {
					b.actions.record(wire.ActionStatusResult{ActionID: params.ActionID, State: wire.ActionStateAccepted, ThreadID: &params.ThreadID, TurnID: &turnID, MessageID: &msg.ID})
				}
				return wire.SendMessageResult{ActionID: params.ActionID, ThreadID: params.ThreadID, TurnID: &turnID, MessageID: &msg.ID}, nil
			}
			if !errors.Is(err, harness.ErrNotRunning) {
				b.mu.Unlock()
				return wire.SendMessageResult{}, err
			}
			if b.threads[params.ThreadID] != td {
				b.mu.Unlock()
				return wire.SendMessageResult{}, store.ErrNotFound
			}
			isRunning = td.summary.IsRunning
			params.WhenRunning = "queue"
			followUpIntent = "insert"
		} else if insertIntoRunningTurn {
			// 当前 Provider 不实现 harness.Steerer（或消息带附件/选项）时无法注入，退回排队。
			params.WhenRunning = "queue"
			followUpIntent = "insert"
		}
	}

	// 子代理汇报必须等整批落定：还有 bee 在跑时先把汇报排进队列，等全部结束时由统一的
	// 汇报开轮，避免单条汇报抢跑。priority 里的提问转交与通知不受此限：子代理提问要能当即
	// 送到主代理，否则子代理等回答、主代理等子代理收尾，会形成死锁。
	batchedReport := priority && swarm.IsReport(params.Text)
	if isRunning || (params.WhenRunning == "queue" && hasBees && (batchedReport || !priority)) {
		if params.WhenRunning == "queue" || (isRunning && queueWhileRunning) {
			queueAtts, err := b.attachments.pinAttachments(params.Attachments)
			if err != nil {
				b.mu.Unlock()
				if params.ActionID != "" {
					rpcErr := err.(*wire.RPCError)
					b.actions.record(wire.ActionStatusResult{
						ActionID: params.ActionID, State: wire.ActionStateFailed, ThreadID: &params.ThreadID,
						Error: &wire.ActionError{Code: rpcErr.Code, Message: rpcErr.Message},
					})
				}
				return wire.SendMessageResult{}, err
			}

			fuID := newUUID()
			nowStr := time.Now().UTC().Format(time.RFC3339Nano)
			fu := wire.FollowUp{
				ID:          fuID,
				Text:        params.Text,
				Attachments: queueAtts,
				CreatedAt:   nowStr,
				BundleID:    nil,
				Kind:        kind,
				Intent:      followUpIntent,
			}
			if priority {
				if td.priorityFollowUps == nil {
					td.priorityFollowUps = make(map[string]bool)
				}
				td.priorityFollowUps[fuID] = true
				td.followUps = append([]wire.FollowUp{fu}, td.followUps...)
			} else {
				td.followUps = append(td.followUps, fu)
			}
			if td.queueRevision == 0 {
				td.queueRevision = 1
			}
			td.queueRevision++
			rev := td.queueRevision
			b.mu.Unlock()

			b.commitQueueChange(params.ThreadID)

			if params.ActionID != "" {
				b.actions.record(wire.ActionStatusResult{
					ActionID:   params.ActionID,
					State:      wire.ActionStateAccepted,
					ThreadID:   &params.ThreadID,
					FollowUpID: &fuID,
					Revision:   &rev,
				})
			}

			queuedTrue := true
			return wire.SendMessageResult{
				ActionID:   params.ActionID,
				ThreadID:   params.ThreadID,
				Queued:     &queuedTrue,
				FollowUpID: &fuID,
			}, nil
		}

		b.mu.Unlock()
		rpcErr := &wire.RPCError{
			Code:    wire.ErrCodeOperationRejected,
			Message: fmt.Sprintf("thread %s is already running", params.ThreadID),
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	// 每次 send 时顺带清理超过 1 小时仍未被引用的附件
	if b.attachments != nil {
		b.attachments.cleanupExpired()
	}

	// 非排队路径：在发起 turn 之前逐个解析 attachmentID 并移动到会话媒体目录
	mediaRefs, mediaFilePaths, err := b.consumeAttachmentsLocked(params.ThreadID, params.Attachments, false)
	if err != nil {
		b.mu.Unlock()
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) {
			rpcErr = &wire.RPCError{
				Code:    wire.InternalError,
				Message: err.Error(),
			}
		}
		if params.ActionID != "" {
			b.actions.record(wire.ActionStatusResult{
				ActionID: params.ActionID,
				State:    wire.ActionStateFailed,
				ThreadID: &params.ThreadID,
				Error: &wire.ActionError{
					Code:    rpcErr.Code,
					Message: rpcErr.Message,
				},
			})
		}
		return wire.SendMessageResult{}, rpcErr
	}

	st := b.startTurnLocked(td, params.Text, kind)
	if len(mediaRefs) > 0 {
		st.userMsg.Media = mediaRefs
		if len(td.messages) > 0 && td.messages[len(td.messages)-1].ID == st.userMsg.ID {
			td.messages[len(td.messages)-1].Media = mediaRefs
		}
		st.imagePaths = mediaFilePaths
		st.imageNames = mediaRefNames(mediaRefs)
	}
	// 本次消息携带的 composer 选项只作用于本轮，不改写会话偏好。
	if opts := params.Options; opts != nil {
		if opts.InteractionMode != nil && *opts.InteractionMode != "" {
			st.interactionMode = *opts.InteractionMode
		}
		if opts.Effort != nil {
			st.effort = *opts.Effort
		}
	}
	b.mu.Unlock()

	b.publishStartedTurn(params.ThreadID, st)

	result := wire.SendMessageResult{
		ActionID:  params.ActionID,
		ThreadID:  params.ThreadID,
		MessageID: &st.userMsg.ID,
		TurnID:    &st.turnID,
	}
	if params.ActionID != "" {
		b.actions.record(wire.ActionStatusResult{
			ActionID:  params.ActionID,
			State:     wire.ActionStateAccepted,
			ThreadID:  &params.ThreadID,
			MessageID: &st.userMsg.ID,
			TurnID:    result.TurnID,
		})
	}

	return result, nil
}

// publishStartedTurn 落库并广播刚开启的一轮，再启动它的后台运行。普通发送、排队出队与接住
// 自发输出的轮次都走这里，保证开轮与运行之间的步骤顺序一致。
func (b *MemoryBackend) publishStartedTurn(threadID string, st *startedTurn) {
	b.persistMessages(threadID, []wire.ThreadMessage{st.userMsg})
	if st.titleChanged {
		b.persistThreadTitle(threadID, st.currentSummary.Title)
	}

	b.hub.Flush(threadID)
	b.hub.BeginRun(threadID)
	b.hub.Publish(threadID, []wire.ThreadMessage{st.userMsg}, nil, st.currentSummary)

	// 发启动运行前先记下这是要接自发输出的轮次：runHarness 据此把 Turn.Attach 置真。
	if st.attach {
		b.mu.Lock()
		if cur, ok := b.threads[threadID]; ok && cur.runTurnID == st.turnID {
			cur.attachTurnID = st.turnID
		}
		b.mu.Unlock()
	}
	b.launchTurnRun(threadID, st, st.userMsg.Text)
}

// handleUnsolicitedTurn 接住常驻进程自发跑出的一段输出：线程空闲时开一轮用 Turn.Attach 把它读回来，
// 输出照常落进线程消息。线程忙时记下通知，空闲后再尝试接回输出。
func (b *MemoryBackend) handleUnsolicitedTurn(threadID string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.isHelper || td.pendingSend != nil {
		b.mu.Unlock()
		return
	}
	if td.runTurnID != "" || td.summary.IsRunning || td.takingBack {
		td.unsolicitedPending = true
		b.mu.Unlock()
		return
	}
	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		log.Printf("ensure messages for thread %s error: %v", threadID, err)
	}
	b.ensureMessagesLoadedLocked(td)
	b.touchThreadLocked(td)
	st := b.startTurnLocked(td, unsolicitedTurnText, threadMessageKindHostEvent)
	st.attach = true
	b.mu.Unlock()

	b.publishStartedTurn(threadID, st)
}

func (b *MemoryBackend) retryUnsolicitedTurn(threadID string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	h := b.threadHarnessLocked(td.summary.Provider)
	b.mu.Unlock()
	if checker, ok := h.(unsolicitedOutputChecker); ok && !checker.HasUnsolicitedOutput(threadID) {
		return
	}
	b.handleUnsolicitedTurn(threadID)
}

// takeUnsolicitedPendingLocked 在真正空闲时取走待重试通知。
func (td *memoryThread) takeUnsolicitedPendingLocked() bool {
	if td.takingBack || td.runTurnID != "" || td.summary.IsRunning || !td.unsolicitedPending {
		return false
	}
	td.unsolicitedPending = false
	return true
}

// bindUnsolicitedTurnHandlerLocked 把自发轮次回调登记到支持它的 harness 上（调用方持有 b.mu）。
// 常驻进程空闲时自己跑出新一轮时，harness 通过它请后端开一轮把这段输出接住。
func (b *MemoryBackend) bindUnsolicitedTurnHandlerLocked(h harness.Harness) {
	if h == nil {
		return
	}
	if notifier, ok := h.(harness.UnsolicitedTurnNotifier); ok {
		notifier.SetUnsolicitedTurnHandler(b.handleUnsolicitedTurn)
	}
	if notifier, ok := h.(harness.ProcessExitNotifier); ok {
		notifier.SetProcessExitHandler(func(threadID string, before time.Time) {
			go b.finalizeThreadBackgroundTasks(threadID, before)
		})
	}
}

// finalizeThreadBackgroundTasks 在常驻进程退出后收敛该会话未收尾的后台任务。
// 工具消息状态收敛为 failed，后台任务计数归零，并广播给前端与落库。
func (b *MemoryBackend) finalizeThreadBackgroundTasks(threadID string, before time.Time) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}

	if err := b.ensureMessagesLocked(td, threadID); err != nil {
		b.mu.Unlock()
		return
	}
	b.ensureMessagesLoadedLocked(td)

	nowTime := b.nowLocked()
	nowStr := formatTime(nowTime)

	var msgToPublish []wire.ThreadMessage
	for idx := range td.messages {
		if td.messages[idx].ToolStatus == nil || *td.messages[idx].ToolStatus != "background" {
			continue
		}
		if !before.IsZero() {
			createdAt, err := time.Parse(time.RFC3339Nano, td.messages[idx].CreatedAt)
			if err != nil || !createdAt.Before(before) {
				continue
			}
		}
		failed := "failed"
		stamp := nowStr
		td.messages[idx].ToolStatus = &failed
		td.messages[idx].ToolCompletedAt = &stamp
		td.messages[idx].Text = appendToolNoticeText(td.messages[idx].Text, residentExitToolNotice)
		msgToPublish = append(msgToPublish, td.messages[idx])
	}
	b.enrichTurnMessagesLocked(td, msgToPublish)

	if len(msgToPublish) == 0 {
		b.mu.Unlock()
		return
	}

	changedIDs := make(map[string]bool, len(msgToPublish))
	for _, m := range msgToPublish {
		changedIDs[m.ID] = true
	}
	for toolID, msgID := range td.backgroundTools {
		if changedIDs[msgID] {
			delete(td.backgroundTools, toolID)
			delete(td.backgroundPending, toolID)
			for taskID, taskToolID := range td.backgroundTaskTools {
				if taskToolID == toolID {
					delete(td.backgroundTaskTools, taskID)
				}
			}
		}
	}
	td.bgCount.invalidate()
	td.summary.UpdatedAt = nowStr
	currentSummary := b.summaryForPublishLocked(td)
	b.mu.Unlock()

	b.persistMessages(threadID, msgToPublish)

	if len(msgToPublish) > 0 {
		b.hub.Publish(threadID, msgToPublish, nil, currentSummary)
	}
}

// startTurnLocked 必须在持有 b.mu 时调用：追加用户消息、更新会话摘要，并为本轮创建运行上下文。
// SendMessage 的正常路径与排队消息出队路径共用该函数，保证出队时不会出现会话短暂空闲的窗口。
// kind 可选：指定本轮用户消息的 Kind（如 host_event），为空时按普通用户消息落库。
func (b *MemoryBackend) startTurnLocked(td *memoryThread, text string, kind ...string) *startedTurn {
	var kindPtr *string
	if len(kind) > 0 && kind[0] != "" {
		k := kind[0]
		kindPtr = &k
	}
	b.ensureMessagesLoadedLocked(td)
	userMsgID := newUUID()
	startedAt := b.nowLocked().UTC()
	now := startedAt.Format(time.RFC3339Nano)

	// 统计 td.messages 中已有消息出现过的不同有效 TurnID 个数作为历史轮次数。
	// 相比仅统计 Role 为 "user" 的消息数，统计不同 TurnID 的个数更稳妥：
	// 每一轮对话（包含用户提问、思考过程、工具调用、助手答复及错误）均绑定同一个唯一的 TurnID。
	// 若存在无 user 角色的特殊轮次或消息过滤，不同 TurnID 集合依然能真实反映轮次推进；
	// 若遇到无 TurnID 的历史消息，兜底按 Role == "user" 累加，保证兼容性。
	// 起始值取 0（轮次编号从 0 开始递增）。
	seenTurns := make(map[string]struct{})
	userMsgCountWithoutTurnID := 0
	for _, m := range td.messages {
		if m.TurnID != nil && *m.TurnID != "" {
			seenTurns[*m.TurnID] = struct{}{}
		} else if m.Role == "user" {
			userMsgCountWithoutTurnID++
		}
	}
	startTurnIndex := 0
	turnIndex := startTurnIndex + len(seenTurns) + userMsgCountWithoutTurnID
	if turnIndex < td.nextTurnIndex {
		turnIndex = td.nextTurnIndex
	}
	if b.store != nil {
		ctx, cancel := persistCtx()
		if turns, err := b.store.ListRecentTurns(ctx, td.summary.ID, 1); err == nil && len(turns) > 0 && turnIndex <= turns[0].TurnIndex {
			turnIndex = turns[0].TurnIndex + 1
		}
		cancel()
	}
	td.nextTurnIndex = turnIndex + 1

	turnID := newUUID()
	turn := store.Turn{ID: turnID, ThreadID: td.summary.ID, TurnIndex: turnIndex, Status: "running", StartedAt: startedAt, UserPrompt: text}
	if td.turnTiming == nil {
		td.turnTiming = make(map[string]store.Turn)
	}
	td.turnTiming[turnID] = turn
	runCtx, runCancel := context.WithCancel(b.ctx)
	td.runTurnID = turnID
	td.runCancel = runCancel
	// 新一轮开始：清空上一轮记录的文件触碰路径。
	td.touchedPaths = nil
	// 新轮次开始也算一次落地时刻，避免上一轮的文件改动时间被长期沿用。
	td.lastFileChangeAt = startedAt
	// 记录本轮开轮输入的 Kind 与文本，供「本轮是否由用户亲自发消息触发」的判断使用。
	if kindPtr != nil {
		td.turnInputKind = *kindPtr
	} else {
		td.turnInputKind = ""
	}
	td.turnInputText = text

	userMsg := wire.ThreadMessage{
		ID:        userMsgID,
		Role:      "user",
		Text:      text,
		CreatedAt: now,
		TurnIndex: turnIndex,
		TurnID:    &turnID,
		Kind:      kindPtr,
	}
	applyTurnTiming(&userMsg, td.turnTiming)
	td.messages = append(td.messages, userMsg)

	var titleChanged bool
	// 宿主事件与唤醒触发不是用户输入，不能用它们的正文给会话起名。
	if kindPtr == nil && td.summary.Title == defaultThreadTitle {
		newTitle := generateThreadTitle(text)
		if newTitle != defaultThreadTitle {
			td.summary.Title = newTitle
			titleChanged = true
		}
	}

	td.summary.IsRunning = true
	td.phase = phaseStarting
	// 新的一轮开始：清掉上一轮的出错标记，避免结束后仍被当作失败。
	if td.summary.Attention == "failed" {
		td.summary.Attention = "running"
	}
	// 新一轮开始也清掉上一轮的错误原文，避免旧的额度提示重复触发。
	td.summary.LastErrorText = ""
	td.summary.UpdatedAt = now
	td.summary.LastUserMessageAt = &now
	actText := "Thinking..."
	td.summary.LastActivityText = &actText

	// 注释：params.Options.Provider 若与会话的 Provider 不同则直接忽略，因为 SessionID 只在同一个 provider 内有效。
	h := b.threadHarnessLocked(td.summary.Provider)
	if b.store != nil {
		ctx, cancel := persistCtx()
		err := b.store.CreateTurn(ctx, turn)
		cancel()
		if err != nil {
			log.Printf("store create turn %s error: %v", turnID, err)
		}
	}

	return &startedTurn{
		userMsg:         userMsg,
		turnID:          turnID,
		runCtx:          runCtx,
		turnIndex:       turnIndex,
		currentSummary:  td.summary,
		titleChanged:    titleChanged,
		h:               h,
		interactionMode: normalizeInteractionMode(td.summary.InteractionMode),
		effort:          derefString(td.summary.Effort),
	}
}

// launchTurnRun 为刚开始的执行轮启动后台 harness/模拟回复 goroutine。
func (b *MemoryBackend) launchTurnRun(threadID string, st *startedTurn, text string) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()

		// 步骤 2: C-RT-01 历史等待锁
		b.mu.Lock()
		td, ok := b.threads[threadID]
		if !ok {
			b.mu.Unlock()
			b.finishTurnEarly(nil, threadID, st.turnID, st.turnIndex, false, st.runCtx.Err() != nil)
			return
		}
		historyOp := td.historyOp
		b.mu.Unlock()

		if historyOp != nil {
			select {
			case <-historyOp:
			case <-st.runCtx.Done():
				b.finishTurnEarly(td, threadID, st.turnID, st.turnIndex, false, true)
				return
			}
		}

		// 步骤 1: 真正调用 runHarness 或 simulateAssistantReply 之前置为 phaseRunning（持锁设置，并确认 runTurnID 仍是本轮）
		b.mu.Lock()
		td, ok = b.threads[threadID]
		if !ok || td.runTurnID != st.turnID || st.runCtx.Err() != nil {
			b.mu.Unlock()
			// 本轮已被取消/被接替：startTurnLocked 已置 IsRunning，这里必须补上空闲收尾，
			// 否则该线程会永久卡在运行中。
			b.finishTurnEarly(td, threadID, st.turnID, st.turnIndex, false, st.runCtx.Err() != nil)
			return
		}
		td.phase = phaseRunning
		b.mu.Unlock()

		if st.h != nil {
			b.runHarness(st.runCtx, threadID, st.turnID, text, st.turnIndex, st.imagePaths, st.imageNames, st.interactionMode, st.effort)
		} else {
			b.simulateAssistantReply(st.runCtx, threadID, st.turnID, text, st.turnIndex)
		}
	}()
}

func (b *MemoryBackend) enrichTurnMessagesLocked(td *memoryThread, messages []wire.ThreadMessage) {
	missing := false
	for _, msg := range messages {
		if msg.TurnID != nil && *msg.TurnID != "" {
			if _, ok := td.turnTiming[*msg.TurnID]; !ok {
				missing = true
				break
			}
		}
	}
	if missing && b.store != nil {
		ctx, cancel := persistCtx()
		turns, err := b.store.ListTurns(ctx, td.summary.ID)
		cancel()
		if err != nil {
			log.Printf("store list turns %s error: %v", td.summary.ID, err)
		} else {
			if td.turnTiming == nil {
				td.turnTiming = make(map[string]store.Turn)
			}
			for _, turn := range turns {
				if _, ok := td.turnTiming[turn.ID]; !ok {
					td.turnTiming[turn.ID] = turn
				}
			}
		}
	}
	for i := range messages {
		applyTurnTiming(&messages[i], td.turnTiming)
	}
}

func (b *MemoryBackend) updateTurnFinish(threadID, turnID string, turnIndex int, status, errMsg string) {
	b.turnPersistMu.Lock()
	defer b.turnPersistMu.Unlock()

	completedAt := b.now().UTC()
	var existing store.Turn
	var getErr error
	if b.store != nil {
		ctx, cancel := persistCtx()
		existing, getErr = b.store.GetTurn(ctx, turnID)
		cancel()
		if getErr == nil && existing.Status != "running" && existing.Status != "starting" {
			b.mu.Lock()
			if td := b.threads[threadID]; td != nil {
				if td.turnTiming == nil {
					td.turnTiming = make(map[string]store.Turn)
				}
				td.turnTiming[turnID] = existing
				for i := range td.messages {
					applyTurnTiming(&td.messages[i], td.turnTiming)
				}
			}
			b.mu.Unlock()
			return
		}
	}
	b.mu.Lock()
	updateSummary := false
	var timing store.Turn
	if td, ok := b.threads[threadID]; ok {
		turn, exists := td.turnTiming[turnID]
		if exists && turn.Status != "running" && turn.Status != "starting" {
			if b.store == nil || (getErr != nil && !errors.Is(getErr, store.ErrNotFound)) || (getErr == nil && existing.Status != "running" && existing.Status != "starting") {
				b.mu.Unlock()
				return
			}
			status = turn.Status
			if turn.CompletedAt != nil {
				completedAt = *turn.CompletedAt
			}
		}
		if !exists {
			if b.store != nil && getErr == nil {
				turn = existing
			} else {
				turn = store.Turn{ID: turnID, ThreadID: threadID, TurnIndex: turnIndex}
			}
		}
		turn.Status, turn.CompletedAt = status, &completedAt
		timing = turn
		if td.turnTiming == nil {
			td.turnTiming = make(map[string]store.Turn)
		}
		td.turnTiming[turnID] = turn
		for i := range td.messages {
			applyTurnTiming(&td.messages[i], td.turnTiming)
		}
		if status != "" && (td.runTurnID == "" || td.runTurnID == turnID) {
			updateSummary = true
			lastStatus := status
			td.summary.LastStatus = &lastStatus
		}
	}
	b.mu.Unlock()

	if b.store == nil {
		return
	}

	ctx, cancel := persistCtx()
	defer cancel()

	if status != "" && updateSummary {
		if err := b.store.UpdateThreadLastStatus(ctx, threadID, status); err != nil && !errors.Is(err, store.ErrNotFound) {
			log.Printf("update thread last status %s: %v", threadID, err)
		}
	}

	if getErr != nil {
		if errors.Is(getErr, store.ErrNotFound) {
			if !timing.StartedAt.IsZero() {
				timing.ErrorMessage = errMsg
				if err := b.store.CreateTurn(ctx, timing); err != nil {
					log.Printf("create finished turn %s: %v", turnID, err)
				}
			} else {
				log.Printf("cannot finish turn %s: missing start time", turnID)
			}
		} else {
			log.Printf("get turn %s for finish error: %v", turnID, getErr)
		}
		return
	}

	if existing.Status != "running" && existing.Status != "starting" {
		return
	}
	b.mu.Lock()
	td, ok := b.threads[threadID]
	workDir := b.harnessWorkDir
	var touchedPaths []string
	if ok {
		if td.workDir != "" {
			workDir = td.workDir
		} else if proj, ok := b.projects[td.summary.ProjectID]; ok && proj.Path != "" {
			workDir = proj.Path
		}
		touchedPaths = append([]string(nil), td.touchedPaths...)
	}
	gitRunner := b.git
	var inTokens, outTokens int64
	if turnMap, ok := b.usage[threadID]; ok {
		if u, ok := turnMap[turnIndex]; ok {
			inTokens = int64(u.InputTokens)
			outTokens = int64(u.OutputTokens)
		}
	}
	b.mu.Unlock()

	existing.Status = status
	existing.CompletedAt = &completedAt
	existing.ErrorMessage = errMsg
	existing.InputTokens = inTokens
	existing.OutputTokens = outTokens

	if gitRunner != nil && workDir != "" {
		gitCtx, gitCancel := context.WithTimeout(context.Background(), 10*time.Second)
		if root, err := gitRunner.RepoRoot(gitCtx, workDir); err == nil && root != "" {
			ref := git.CheckpointRef(threadID, turnID)
			if start, err := gitRunner.RevParse(gitCtx, root, ref+"/start"); err == nil {
				existing.BaseCheckpoint = start
			}
			if end, err := gitRunner.RevParse(gitCtx, root, ref+"/end"); err == nil {
				existing.FinishCheckpoint = end
			}
			// 优先记录本会话工具实际触碰的路径；provider 未上报路径（touchedPaths 为空）时，
			// 保留按 start→finish 全库差异反推的回退逻辑。
			if files := normalizeTouchedFiles(workDir, root, touchedPaths); len(files) > 0 {
				existing.TouchedFiles = files
			} else if existing.BaseCheckpoint != "" {
				diffs, err := gitRunner.Diff(gitCtx, root, existing.BaseCheckpoint, existing.FinishCheckpoint, nil)
				if err == nil {
					seen := make(map[string]bool)
					var files []string
					for _, d := range diffs {
						p := d.NewPath
						if p == "" {
							p = d.OldPath
						}
						if p != "" && !seen[p] {
							seen[p] = true
							files = append(files, p)
						}
					}
					existing.TouchedFiles = files
				}
			}
		}
		gitCancel()
	}

	writeCtx, writeCancel := persistCtx()
	defer writeCancel()
	if err := b.store.UpdateTurn(writeCtx, existing); err != nil {
		log.Printf("store update turn %s error: %v", turnID, err)
	}

	// 一轮结束只是触发防抖：线程空闲一段时间后才真正同步，消息没变时不会再发请求（后台串行、可合并，失败只记日志）。
	b.scheduleNowledgeSync(threadID)
}
