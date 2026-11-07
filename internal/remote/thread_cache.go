package remote

import (
	"fmt"
	"sort"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// warmCacheLimit 是内存中保留完整消息的已归档会话数量上限；超出的冷会话在 ListThreads 时淘汰。
const warmCacheLimit = 32

// touchThreadLocked 记录会话最近一次被访问的时间，供冷会话淘汰排序使用。调用方需持有 b.mu。
func (b *MemoryBackend) touchThreadLocked(td *memoryThread) {
	if td == nil {
		return
	}
	td.lastAccess = b.nowLocked()
}

// evictColdThreadsLocked 淘汰冷会话的消息：只在挂载 store 时生效。
// 候选会话必须已归档、完全空闲（无运行中的轮次、无排队与待发送消息、无待处理审批），
// 且内存中的每条消息都已持久化；按 lastAccess 排序，保留最近访问的 warmCacheLimit 个，
// 其余把 messages 置空并标记 messagesEvicted，下次访问由 ensureMessagesLocked 回载。
// 调用方需持有 b.mu。
func (b *MemoryBackend) evictColdThreadsLocked() {
	if b.store == nil {
		return
	}
	var candidates []*memoryThread
	for _, td := range b.threads {
		if td.messagesEvicted || !td.summary.IsArchived {
			continue
		}
		// 运行中的轮次判断与 ArchiveBeeSession / SessionState 一致。
		if td.runTurnID != "" || td.runCancel != nil || td.summary.IsRunning || td.phase != phaseIdle {
			continue
		}
		if td.pendingSend != nil || len(td.followUps) > 0 || len(td.pending) > 0 {
			continue
		}
		persistedAll := true
		for _, m := range td.messages {
			if !td.persisted[m.ID] {
				persistedAll = false
				break
			}
		}
		if !persistedAll {
			continue
		}
		candidates = append(candidates, td)
	}
	if len(candidates) <= warmCacheLimit {
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].lastAccess.Before(candidates[j].lastAccess)
	})
	for _, td := range candidates[:len(candidates)-warmCacheLimit] {
		td.messages = nil
		td.messagesEvicted = true
	}
}

// ensureMessagesLocked 在消息被淘汰后按需从 store 回载。调用方需持有 b.mu。
// 本函数负责冷会话淘汰后的回载，与 ensureMessagesLoadedLocked（负责首次惰性加载）协同，二者均会同时维护 messagesLoaded 与 messagesEvicted。
// 分页方式与 persist.go 的 AttachStore 一致：每页 100 条、从最新一页向前翻，
// 再逐条用 messageFromStore 转换（升序）。回载成功后清除 messagesEvicted 并设置 messagesLoaded。
func (b *MemoryBackend) ensureMessagesLocked(td *memoryThread, threadID string) error {
	if td == nil || !td.messagesEvicted {
		return nil
	}
	s := b.store
	if s == nil {
		return nil
	}

	ctx, cancel := persistCtx()
	defer cancel()

	var allStoreMsgs []store.Message
	beforeSeq := int64(0)
	for {
		// 与 AttachStore 一致用容错读取：含已隔离坏消息的会话回载时不能因解码失败而不可访问（C-ST-06）。
		page, hasMore, _, err := s.ListMessagesTolerant(ctx, threadID, beforeSeq, 100)
		if err != nil {
			return fmt.Errorf("list messages for thread %s: %w", threadID, err)
		}
		if len(page) == 0 {
			break
		}
		allStoreMsgs = append(page, allStoreMsgs...)
		if !hasMore {
			break
		}
		beforeSeq = page[0].Seq
	}

	msgs := make([]wire.ThreadMessage, len(allStoreMsgs))
	if td.persisted == nil {
		td.persisted = make(map[string]bool, len(allStoreMsgs))
	}
	for i, m := range allStoreMsgs {
		msgs[i] = messageFromStore(m)
		td.persisted[m.ID] = true
	}
	td.messages = msgs
	td.messagesLoaded = true
	td.messagesEvicted = false
	return nil
}
