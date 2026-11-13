package mcodeui

import (
	"context"
	"strings"
)

// steerableProviders 记录能在轮次执行中安全插入消息的 provider（后端实现 harness.Steerer 的那些）。
// 其余 provider 没有中途插话能力，插话只能退回排队，这里据实置灰/提示，不假装已经插入。
var steerableProviders = map[string]bool{
	"claude": true,
	"codex":  true,
	"mcode":  true,
	"omp":    true,
	"pi":     true,
}

// providerSupportsSteer 判断 provider 是否支持把排队消息插进当前轮次；
// pi 线程的 provider 可能带 "pi:" 端点前缀，归一后再查表。
func providerSupportsSteer(provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return false
	}
	if base, ok := strings.CutPrefix(provider, "pi:"); ok && base != "" {
		provider = "pi"
	}
	return steerableProviders[provider]
}

// steerQueued 把一条排队消息插进当前轮次：先从队列移除，再走普通发送路径。
// provider 支持中途插话时后端会把它注入正在执行的轮次；不支持时这里不做任何改动，
// 消息原地留在队列里并 toast 说明，避免“看起来插进去了、实际丢了或变成下一轮”。
// 发送失败把文本重新 Enqueue 回队尾并提示：插话失败可以接受，消息丢失不能接受。
func (s *chatState) steerQueued(item QueuedFollowUp) {
	if s == nil || item.ID == "" {
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	provider := ""
	for i := range s.threads {
		if s.threads[i].ID == threadID {
			provider = s.threads[i].Provider
			break
		}
	}
	if provider == "" && s.pendingThread != nil {
		provider = s.pendingThread.Thread.Provider
	}
	s.mu.Unlock()
	if threadID == "" {
		return
	}
	if !providerSupportsSteer(provider) {
		s.setToast("当前 Agent 不支持插入到正在执行的轮次，消息仍留在队列里")
		return
	}
	s.mu.Lock()
	removed, index, ok := s.takeQueuedLocked(threadID, item.ID)
	s.mu.Unlock()
	if !ok {
		return
	}
	s.invalidate()
	go func() {
		ctx := context.Background()
		if err := s.client.RemoveQueued(ctx, threadID, item.ID); err != nil {
			s.restoreQueued(threadID, removed, index)
			s.setToast("插入排队消息失败: " + err.Error())
			return
		}
		if _, err := s.client.Send(ctx, threadID, item.Text); err != nil {
			// 回填：宁可顺序变到队尾，也不能把用户写的内容弄丢。
			if qErr := s.client.Enqueue(ctx, threadID, item.Text, nil); qErr != nil {
				s.setToast("插入失败且回填队列失败: " + err.Error())
				return
			}
			s.setToast("插入失败，消息已退回队列末尾")
			s.invalidate()
			return
		}
		s.invalidate()
	}()
}

// reorderQueueItems 把 followUpID 移到 beforeFollowUpID 之前，返回新顺序。
// 找不到任一 ID、或两者相同时返回原顺序与 false（调用方据此跳过网络请求）。
func reorderQueueItems(items []QueuedFollowUp, followUpID, beforeFollowUpID string) ([]QueuedFollowUp, bool) {
	if followUpID == "" || followUpID == beforeFollowUpID {
		return items, false
	}
	from, to := -1, -1
	for i := range items {
		switch items[i].ID {
		case followUpID:
			from = i
		case beforeFollowUpID:
			to = i
		}
	}
	if from < 0 || to < 0 || from == to {
		return items, false
	}
	next := make([]QueuedFollowUp, 0, len(items))
	moved := items[from]
	for i := range items {
		if i == from {
			continue
		}
		if i == to {
			// 不论向前还是向后拖，落点都是“目标行之前”：先放 moved 再放目标行。
			next = append(next, moved)
		}
		next = append(next, items[i])
	}
	return next, true
}

// moveQueued 调整排队顺序：本地先乐观重排，再调后端 MoveQueued；
// 后端失败时重新拉取一次队列（拉取也失败就回滚到本地快照）并 toast，保证界面与后端一致。
func (s *chatState) moveQueued(followUpID, beforeFollowUpID string) {
	if s == nil || followUpID == "" || followUpID == beforeFollowUpID {
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	var current []QueuedFollowUp
	if threadID != "" {
		current = append([]QueuedFollowUp(nil), s.queue[threadID]...)
	}
	s.mu.Unlock()
	if threadID == "" {
		return
	}
	next, ok := reorderQueueItems(current, followUpID, beforeFollowUpID)
	if !ok {
		return
	}
	s.mu.Lock()
	if s.queue != nil {
		s.queue[threadID] = next
	}
	s.mu.Unlock()
	s.invalidate()
	go func() {
		ctx := context.Background()
		if err := s.client.MoveQueued(ctx, threadID, followUpID, beforeFollowUpID); err != nil {
			reloaded := false
			if items, qErr := s.client.Queue(ctx, threadID); qErr == nil {
				s.mu.Lock()
				if s.queue != nil {
					s.queue[threadID] = items
				}
				s.mu.Unlock()
				reloaded = true
			}
			if !reloaded {
				s.mu.Lock()
				if s.queue != nil {
					s.queue[threadID] = current
				}
				s.mu.Unlock()
			}
			s.setToast("调整排队顺序失败: " + err.Error())
			s.invalidate()
		}
	}()
}

// takeQueuedLocked removes one item while holding s.mu.
func (s *chatState) takeQueuedLocked(tid, id string) (QueuedFollowUp, int, bool) {
	for i, item := range s.queue[tid] {
		if item.ID == id {
			if s.queueRemoving == nil {
				s.queueRemoving = make(map[string]map[string]bool)
			}
			if s.queueRemoving[tid] == nil {
				s.queueRemoving[tid] = make(map[string]bool)
			}
			s.queueRemoving[tid][id] = true
			s.queue[tid] = append(append([]QueuedFollowUp(nil), s.queue[tid][:i]...), s.queue[tid][i+1:]...)
			return item, i, true
		}
	}
	return QueuedFollowUp{}, 0, false
}

func (s *chatState) restoreQueued(tid string, item QueuedFollowUp, index int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.queueRemoving[tid], item.ID)
	for _, existing := range s.queue[tid] {
		if existing.ID == item.ID {
			return
		}
	}
	items := s.queue[tid]
	if index > len(items) {
		index = len(items)
	}
	items = append(items, QueuedFollowUp{})
	copy(items[index+1:], items[index:])
	items[index] = item
	s.queue[tid] = items
}

func (s *chatState) filterRemovingQueuedLocked(tid string, items []QueuedFollowUp) []QueuedFollowUp {
	pending := s.queueRemoving[tid]
	if len(pending) == 0 {
		return items
	}
	seen := make(map[string]bool)
	filtered := make([]QueuedFollowUp, 0, len(items))
	for _, item := range items {
		if pending[item.ID] {
			seen[item.ID] = true
			continue
		}
		filtered = append(filtered, item)
	}
	for id := range pending {
		if !seen[id] {
			delete(pending, id)
		}
	}
	return filtered
}
