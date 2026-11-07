package remote

import (
	"log"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// bindDeliveryLocked 把本轮标记为「由排队项出队开始」的投递，供事件循环与收尾路径迁移投递状态。
// 调用方需持有 b.mu，且在 startTurnLocked 之后、launchTurnRun 之前调用。
// 多条排队项合并成一轮时只取第一条的 follow_up_id：thread_deliveries 按用户消息（一轮一条）记账，
// 其余排队项的 dispatching 审计事件仍在出队处逐条写入。
func (b *MemoryBackend) bindDeliveryLocked(td *memoryThread, st *startedTurn, followUpID string) {
	if td == nil || st == nil || followUpID == "" {
		return
	}
	td.deliveryTurnID = st.turnID
	td.deliveryMessageID = st.userMsg.ID
	td.deliveryFollowUpID = followUpID
	st.deliveryFollowUpID = followUpID
}

// recordDispatchingForTurn 在出队正文落库之后补写 dispatching 投递行。
// 不是出队开始的轮次（没有 follow_up_id）不写行。
func (b *MemoryBackend) recordDispatchingForTurn(threadID string, st *startedTurn) {
	if st == nil {
		return
	}
	b.recordDispatching(threadID, st.userMsg.ID, st.deliveryFollowUpID)
}

// recordDispatching 写入一条 state=dispatching 的投递记录。
// messageID 是出队生成的用户消息 ID，followUpID 是所出队的排队项 ID。
// 写库失败只记日志，不阻塞出队；遗留的 dispatching 行由启动对账结算成 unknown。
func (b *MemoryBackend) recordDispatching(threadID, messageID, followUpID string) {
	if messageID == "" || followUpID == "" {
		return
	}
	b.mu.Lock()
	s := b.store
	b.mu.Unlock()
	if s == nil {
		return
	}
	pCtx, pCancel := persistCtx()
	defer pCancel()
	if err := s.InsertDelivery(pCtx, store.Delivery{
		MessageID:  messageID,
		ThreadID:   threadID,
		FollowUpID: followUpID,
		CreatedAt:  time.Now().UTC(),
	}); err != nil {
		log.Printf("store insert delivery %s for thread %s error: %v", messageID, threadID, err)
	}
}

// markDeliveryAccepted 把出队轮次的投递行迁到 accepted，并补一条 accepted 审计事件。
// 每轮只调用一次（显式回执与首个上游事件两条路径共用 deliverySettled 去重）。
func (b *MemoryBackend) markDeliveryAccepted(threadID, turnID, messageID, followUpID string) {
	if messageID == "" {
		return
	}
	b.mu.Lock()
	s := b.store
	if followUpID == "" {
		if td, ok := b.threads[threadID]; ok {
			followUpID = td.deliveryFollowUpID
		}
	}
	b.mu.Unlock()
	if s == nil {
		return
	}
	pCtx, pCancel := persistCtx()
	err := s.MarkDeliveryAccepted(pCtx, messageID, turnID)
	pCancel()
	if err != nil {
		log.Printf("store mark delivery %s accepted error: %v", messageID, err)
	}
	b.recordDeliveryAudit(threadID, store.FollowUpEventAccepted, followUpID, turnID)
}

// markDeliveryFailed 把出队轮次的投递行迁到 failed 并记录失败原因，同时补一条 failed 审计事件。
// 只在确认这一轮没有交给 provider 时调用；重复调用由 store 的 dispatching-only 迁移做幂等保护。
func (b *MemoryBackend) markDeliveryFailed(threadID, turnID, messageID, followUpID, failure string) {
	if messageID == "" {
		return
	}
	if failure == "" {
		failure = "投递失败"
	}
	b.mu.Lock()
	s := b.store
	if followUpID == "" {
		if td, ok := b.threads[threadID]; ok {
			followUpID = td.deliveryFollowUpID
		}
	}
	b.mu.Unlock()
	if s == nil {
		return
	}
	pCtx, pCancel := persistCtx()
	err := s.MarkDeliveryFailed(pCtx, messageID, failure)
	pCancel()
	if err != nil {
		log.Printf("store mark delivery %s failed error: %v", messageID, err)
	}
	b.recordDeliveryAudit(threadID, store.FollowUpEventFailed, followUpID, turnID)
}

// recordDeliveryAudit 在线程上挂一条投递审计事件并落库。
// 投递状态事件要求 source_id 非空；线程已消失或缺少 follow_up_id 时直接跳过。
func (b *MemoryBackend) recordDeliveryAudit(threadID, kind, followUpID, sourceID string) {
	if followUpID == "" || sourceID == "" {
		return
	}
	b.mu.Lock()
	td, ok := b.threads[threadID]
	if !ok {
		b.mu.Unlock()
		return
	}
	td.recordFollowUpEvents(newFollowUpEvent(kind, "", wire.FollowUp{ID: followUpID}, sourceID))
	b.mu.Unlock()
	b.commitQueueChange(threadID)
}
