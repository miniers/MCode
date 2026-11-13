// 本文件包含 MemoryBackend 的排队消息方法。
package remote

import (
	"context"
	"log"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"time"
)

// DrainQueuedFollowUps 在主代理空闲时把此前排队的汇报推进成一轮回复。
// 线程不存在、正在运行或队列为空时直接返回 nil，不产生副作用。
func (b *MemoryBackend) DrainQueuedFollowUps(ctx context.Context, threadID string) error {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || td.summary.IsRunning || td.runTurnID != "" || td.runCancel != nil || td.takingBack || len(td.followUps) == 0 {
		b.mu.Unlock()
		return nil
	}
	nextStarted, nextText, queueChanged := b.startNextQueuedTurnLocked(td, true, true)
	if nextStarted == nil || !queueChanged {
		b.mu.Unlock()
		return nil
	}
	finalSummary := nextStarted.currentSummary
	b.mu.Unlock()

	b.hub.Publish(threadID, nil, nil, finalSummary)
	// 正文先落库再删队列行，崩溃时最多重复出现一条已写正文的排队项，不会丢消息。
	b.persistMessages(threadID, []wire.ThreadMessage{nextStarted.userMsg})
	b.recordDispatchingForTurn(threadID, nextStarted)
	b.commitQueueChange(threadID)
	b.hub.Flush(threadID)
	if nextStarted.titleChanged {
		b.persistThreadTitle(threadID, nextStarted.currentSummary.Title)
	}
	b.hub.BeginRun(threadID)
	b.hub.Publish(threadID, []wire.ThreadMessage{nextStarted.userMsg}, nil, nextStarted.currentSummary)
	b.launchTurnRun(threadID, nextStarted, nextText)
	return nil
}

// startNextQueuedTurnLocked 只有附件准备成功才移除排队项；失败时保留供重试。
// allowQueue 为真时普通排队项可以出队。allowPriority 为真时，队首的 priority 条目
// （swarm 汇报与提问转交）即使 allowQueue 为假也能立即开轮，用于 lead 一轮正常结束但仍有
// 子代理在跑的情况；用户打断或本轮出错结束时调用方传 false，priority 条目留在队列里，
// 等用户下一句话或 DrainQueuedFollowUps 再投递，避免用户按了停止后主代理又自己跑起来。
func (b *MemoryBackend) startNextQueuedTurnLocked(td *memoryThread, allowQueue, allowPriority bool) (*startedTurn, string, bool) {
	b.ensureMessagesLoadedLocked(td)
	if td.takingBack {
		return nil, "", false
	}
	var selected []wire.FollowUp
	fromPendingSend := td.pendingSend != nil
	if fromPendingSend {
		selected = []wire.FollowUp{*td.pendingSend}
	} else if len(td.followUps) > 0 && (allowQueue || (allowPriority && td.isPriorityFollowUpLocked(td.followUps[0]))) {
		// priority 条目（swarm 汇报与提问转交）在 allowPriority 为真时不受 allowQueue 限制：只排在
		// 正在运行的那一轮之后，否则子代理提问会一直等主代理回答，而主代理等子代理收尾，形成死锁。
		count := 1
		first := td.followUps[0]
		if first.BundleID != nil && *first.BundleID != "" {
			for count < len(td.followUps) {
				next := td.followUps[count]
				if next.BundleID == nil || *next.BundleID != *first.BundleID {
					break
				}
				count++
			}
		}
		selected = td.followUps[:count]
	} else {
		return nil, "", false
	}
	var attIDs []string
	for _, fu := range selected {
		for _, att := range fu.Attachments {
			attIDs = append(attIDs, att.ID)
		}
	}
	// 出队前先取出排队项上记下的 Kind（如 host_event），写库时跟着本轮用户消息一起存。
	queuedKind := ""
	for _, fu := range selected {
		if fu.Kind != "" {
			queuedKind = fu.Kind
			break
		}
	}
	media, paths, err := b.consumeAttachmentsLocked(td.summary.ID, attIDs, true)
	if err != nil {
		log.Printf("consume attachments error on dequeue for thread %s: %v", td.summary.ID, err)
		return nil, "", false
	}
	if td.pendingSend != nil {
		td.pendingSend = nil
	} else {
		for _, fu := range selected {
			delete(td.priorityFollowUps, fu.ID)
		}
		td.followUps = normalizeBundles(td.followUps[len(selected):])
		if len(td.followUps) == 0 {
			// 队列清空时一并回收 priority 标记，避免已出队（或被删除/改写）的 ID 长期残留。
			td.priorityFollowUps = nil
		}
	}
	if td.queueRevision == 0 {
		td.queueRevision = 1
	}
	td.queueRevision++
	text := bundleFollowUpText(selected)
	st := b.startTurnLocked(td, text, queuedKind)
	// 投递状态行只记第一条排队项；审计事件仍按每个排队项逐条写 dispatching。
	b.bindDeliveryLocked(td, st, selected[0].ID)
	dispatchReason := ""
	if fromPendingSend {
		dispatchReason = "pending_send"
	}
	for _, fu := range selected {
		td.recordFollowUpEvents(newFollowUpEvent(store.FollowUpEventDispatching, dispatchReason, fu, st.turnID))
	}
	st.userMsg.Media = media
	td.messages[len(td.messages)-1].Media = media
	st.imagePaths = paths
	st.imageNames = mediaRefNames(media)
	return st, text, true
}

// commitQueueChange 在持锁复制快照后释放锁，并写穿至 store 及触发 queueChangedHook。
func (b *MemoryBackend) commitQueueChange(threadID string) {
	b.queuePersistMu.Lock()
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		b.queuePersistMu.Unlock()
		return
	}
	if td.queueRevision == 0 {
		td.queueRevision = 1
	}
	revision := td.queueRevision
	// 事件与快照在同一把锁下取出，避免事件与队列快照错位；未挂载 store 时事件随本次提交一起丢弃。
	events := td.pendingFollowUpEvents
	td.pendingFollowUpEvents = nil
	fus := td.queueSnapshotFollowUpsLocked()
	snap := wire.ThreadsQueueSnapshot{
		ThreadID:  threadID,
		Revision:  revision,
		FollowUps: fus,
	}
	// pendingSend 对客户端已出队，但真正开轮前仍须持久化，以免关闭时丢失。
	if td.pendingSend != nil {
		fus = append([]wire.FollowUp{*td.pendingSend}, fus...)
	}
	hook := b.queueChangedHook
	var listeners []func(wire.ThreadsQueueSnapshot)
	if len(b.queueListeners) > 0 {
		listeners = make([]func(wire.ThreadsQueueSnapshot), 0, len(b.queueListeners))
		for _, fn := range b.queueListeners {
			if fn != nil {
				listeners = append(listeners, fn)
			}
		}
	}
	b.mu.Unlock()

	b.persistQueueSnapshot(threadID, revision, fus, events)

	b.queuePersistMu.Unlock()
	if hook != nil {
		hook(snap)
	}
	for _, fn := range listeners {
		fn(snap)
	}
}

// 调用方持有 queuePersistMu，保证快照和审计事件按顺序写入。
func (b *MemoryBackend) persistQueueSnapshot(threadID string, revision int, fus []wire.FollowUp, events []store.FollowUpEvent) {
	b.mu.Lock()
	s := b.store
	replaceHook := b.replaceFollowUpsHook
	b.mu.Unlock()
	if s != nil {
		storeItems := make([]store.FollowUp, 0, len(fus))
		for i, fu := range fus {
			createdAt, err := parseTime(fu.CreatedAt)
			if err != nil || createdAt.IsZero() {
				createdAt = time.Now().UTC()
			}
			attIDs := make([]string, 0, len(fu.Attachments))
			attMetas := make([]store.FollowUpAttachment, 0, len(fu.Attachments))
			for _, att := range fu.Attachments {
				attIDs = append(attIDs, att.ID)
				meta := store.FollowUpAttachment{
					ID:       att.ID,
					Name:     att.Name,
					MimeType: att.MimeType,
				}
				if att.ByteCount != nil {
					meta.ByteCount = *att.ByteCount
				}
				if b.attachments != nil {
					if rec, ok := b.attachments.get(att.ID); ok {
						meta.Path = rec.path
						if meta.Name == "" {
							meta.Name = rec.name
						}
						if meta.MimeType == "" {
							meta.MimeType = rec.mimeType
						}
						if meta.ByteCount == 0 {
							meta.ByteCount = rec.byteCount
						}
					}
				}
				attMetas = append(attMetas, meta)
			}
			storeItems = append(storeItems, store.FollowUp{
				ID:             fu.ID,
				ThreadID:       threadID,
				Position:       i,
				Text:           fu.Text,
				Kind:           fu.Kind,
				Intent:         fu.Intent,
				Attachments:    attIDs,
				AttachmentMeta: attMetas,
				CreatedAt:      createdAt,
				UpdatedAt:      time.Now().UTC(),
			})
		}
		pCtx, pCancel := persistCtx()
		replace := s.ReplaceFollowUpsWithAudit
		if replaceHook != nil {
			replace = replaceHook
		}
		if err := replace(pCtx, threadID, int64(revision), storeItems, events); err != nil {
			log.Printf("store replace follow-ups error for thread %s: %v", threadID, err)
			b.mu.Lock()
			if td, ok := b.threads[threadID]; ok {
				td.pendingFollowUpEvents = append(events, td.pendingFollowUpEvents...)
				if !td.queueRetryPending && b.ctx.Err() == nil {
					td.queueRetryPending = true
					time.AfterFunc(queueRetryDelay, func() { b.retryQueueSnapshot(threadID) })
				}
			}
			b.mu.Unlock()
		}
		pCancel()
	}
}

func (b *MemoryBackend) retryQueueSnapshot(threadID string) {
	b.queuePersistMu.Lock()
	defer b.queuePersistMu.Unlock()
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok || b.ctx.Err() != nil {
		b.mu.Unlock()
		return
	}
	td.queueRetryPending = false
	if td.queueRevision == 0 {
		td.queueRevision = 1
	}
	revision := td.queueRevision
	events := td.pendingFollowUpEvents
	td.pendingFollowUpEvents = nil
	fus := td.queueSnapshotFollowUpsLocked()
	if td.pendingSend != nil {
		fus = append([]wire.FollowUp{*td.pendingSend}, fus...)
	}
	b.mu.Unlock()
	b.persistQueueSnapshot(threadID, revision, fus, events)
}

// resetThreadModel 把线程模型改成 model 并发布变更、写穿到 store；线程不存在时什么都不做。
func (b *MemoryBackend) resetThreadModel(threadID, model string) {
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	m := model
	td.summary.Model = &m
	td.summary.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	currentSummary := b.summaryForPublishLocked(td)
	hook := b.threadsChangedHook
	b.mu.Unlock()

	b.persistThreadModel(threadID, model)
	b.hub.Publish(threadID, nil, nil, currentSummary)
	if hook != nil {
		hook(wire.ThreadsChanged{Threads: []wire.ThreadSummary{currentSummary}})
	}
}
