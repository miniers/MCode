package swarm

import (
	"context"
	"sort"
	"time"
)

// 审查漏斗攒批与超时参数。窗口与上限越小，L2 调用越频繁；越大，延迟越高。
const (
	// reviewBatchWindow 是 L2 批次的最长等待：最早排队的 bee 等满这么久就发车。
	reviewBatchWindow = 20 * time.Second
	// reviewBatchMax 是单次 L2 调用最多携带的 bee 数。
	reviewBatchMax = 6
	// reviewL2Timeout 是单次 L2 模型调用的上限（含等待）。
	reviewL2Timeout = 90 * time.Second
	// reviewMaxAttempts 是批量结果里缺失时允许的重试次数，超过就升级 L3。
	reviewMaxAttempts = 2
	// reviewQueueTimeout 是 l0/l2_queued 排队饿死的兜底时间。
	reviewQueueTimeout = 10 * time.Minute
	// reviewCheckBudgetMargin 加在项目检查超时之上，避免 pollReview 抢在命令自身超时前误判。
	reviewCheckBudgetMargin = 30 * time.Second
)

// collectL2Batch 判断该 lead 是否该发一批 L2：同一 lead 没有运行中的 bee、最早排队的
// bee 已等满窗口、或批内已有 reviewBatchMax 个 bee，满足任一条件就取最早的至多 6 个。
// 调用方不能在持锁时调用；返回的是 bee 副本，真正的状态认领由 claimL2Batch 完成。
func (m *Manager) collectL2Batch(leadThreadID string, now time.Time) []Bee {
	m.mu.Lock()
	defer m.mu.Unlock()

	lead, ok := m.leads[leadThreadID]
	if !ok {
		return nil
	}
	idle := !lead.dispatching && m.queuedCountLocked(leadThreadID) == 0
	if idle {
		for i := range lead.bees {
			if lead.bees[i].Status == StatusRunning {
				idle = false
				break
			}
		}
	}

	var queued []Bee
	for i := range lead.bees {
		h := lead.bees[i]
		if h.ReviewState != ReviewRunning || h.ReviewStage != ReviewStageL2Queued {
			continue
		}
		if h.ReviewApplyPending || h.ReviewAttempts >= reviewMaxAttempts {
			continue
		}
		queued = append(queued, h.clone())
	}
	if len(queued) == 0 {
		return nil
	}
	sort.SliceStable(queued, func(i, j int) bool {
		return reviewQueuedTime(queued[i]).Before(reviewQueuedTime(queued[j]))
	})

	oldest := reviewQueuedTime(queued[0])
	if !idle && len(queued) < reviewBatchMax && now.Sub(oldest) < reviewBatchWindow {
		return nil
	}
	if len(queued) > reviewBatchMax {
		queued = queued[:reviewBatchMax]
	}
	return queued
}

// reviewQueuedTime 返回排队起点：优先 ReviewQueuedAt，缺省回退到 FinishedAt/StartedAt。
func reviewQueuedTime(h Bee) time.Time {
	if h.ReviewQueuedAt != nil {
		return *h.ReviewQueuedAt
	}
	if h.FinishedAt != nil {
		return *h.FinishedAt
	}
	return h.StartedAt
}

// claimL2Batch 在锁内把本批 bee 认领为 l2，并重新校验它们仍停在 l2_queued。
// 返回真正认领成功的 bee（可能少于入参）。
func (m *Manager) claimL2Batch(leadThreadID string, batch []Bee, now time.Time) []Bee {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return nil
	}
	claimed := make([]Bee, 0, len(batch))
	for _, want := range batch {
		for i := range lead.bees {
			b := &lead.bees[i]
			if b.ThreadID != want.ThreadID || b.ReviewState != ReviewRunning || b.ReviewStage != ReviewStageL2Queued {
				continue
			}
			b.ReviewStage = ReviewStageL2
			b.ReviewStartedAt = &now
			b.ReviewApplyPending = false
			claimed = append(claimed, b.clone())
			break
		}
	}
	return claimed
}

// requeueL2Missing 处理批量结果里缺失的 bee：第一次回 l2_queued 单独重试，
// 第二次仍缺失就升级 L3。返回是否已经升级。
func (m *Manager) requeueL2Missing(ctx context.Context, leadThreadID, beeThreadID, patchSHA, reason string) bool {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return false
	}
	var promoted bool
	for i := range lead.bees {
		b := &lead.bees[i]
		if b.ThreadID != beeThreadID || b.ReviewState != ReviewRunning || b.ReviewStage != ReviewStageL2 {
			continue
		}
		if b.ReviewPatchSHA != patchSHA {
			break
		}
		b.ReviewAttempts++
		if b.ReviewAttempts >= reviewMaxAttempts {
			promoted = true
			break
		}
		now := m.now()
		b.ReviewStage = ReviewStageL2Queued
		b.ReviewQueuedAt = &now
		b.ReviewStartedAt = nil
		break
	}
	m.mu.Unlock()
	if promoted {
		m.promoteL3(ctx, leadThreadID, beeThreadID, ReviewStageL2, patchSHA, reason)
		return true
	}
	m.notifyChange(leadThreadID)
	return false
}
