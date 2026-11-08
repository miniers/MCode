package remote

import (
	"errors"
	"log"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// errRequestWithdrawn 是卡片被宿主撤下时 Provider 侧 Interact 收到的错误。
// 它表示请求不再可答，与调用方取消、轮次结束都不是同一回事。
var errRequestWithdrawn = errors.New("request withdrawn by host")

// withdrawReason 说明卡片被撤下的入口，不同入口写不同的时间线提示。
type withdrawReason string

const (
	withdrawInterrupt withdrawReason = "interrupt" // 用户停止本轮、看门狗强制收尾或 bee 停止
	withdrawArchive   withdrawReason = "archive"   // 会话归档
	withdrawDelete    withdrawReason = "delete"    // 会话删除
	withdrawShutdown  withdrawReason = "shutdown"  // 宿主进程关闭
)

// withdrawBatch 是一次撤卡需要在锁外广播的素材。
// requests 只含真正撤下的、尚未答复的卡片；已答复等送达确认的卡片只记日志、不写提示。
type withdrawBatch struct {
	threadID string
	summary  wire.ThreadSummary
	requests []wire.RemoteRequest
	removed  int
}

// withdrawPendingLocked 撤掉该线程全部待处理卡片，返回真正撤下的请求。
// 调用方必须持有 b.mu；广播与时间线提示要在解锁后做，避免持锁回调观察者或再次加锁。
func (b *MemoryBackend) withdrawPendingLocked(td *memoryThread, reason withdrawReason) []wire.RemoteRequest {
	if td == nil || len(td.pending) == 0 {
		return nil
	}
	withdrawn := make([]wire.RemoteRequest, 0, len(td.pending))
	for id, pr := range td.pending {
		if pr.req.Kind == string(harness.RequestKindPlan) {
			// 计划请求单飞：同正文的重复等待方靠 settle 拿到"已取消"。
			pr.settle(nil)
		}
		if pr.handled {
			// 已答复、正在等送达确认的卡片不再可答：只关闭等待并记日志，不当作一张撤回的卡片。
			log.Printf("withdraw request %s of thread %s: already answered (%s)", id, td.summary.ID, reason)
			pr.closeDone()
			delete(td.pending, id)
			continue
		}
		pr.withdraw(reason)
		// 留痕结算与撤卡同频：默认实现只投递到独立 goroutine，不会在持锁路径上写库。
		// 已答复等送达确认的卡片不走这里（上面 continue），它已经结算为 settled。
		traceAskWithdrawn(b, id, reason)
		withdrawn = append(withdrawn, pr.req)
		delete(td.pending, id)
	}
	if len(td.pending) == 0 {
		td.summary.HasPendingRequest = false
		if td.summary.IsRunning {
			td.summary.Attention = "running"
		} else {
			td.summary.Attention = "ready"
		}
	}
	td.summary.UpdatedAt = b.nowLocked().Format(time.RFC3339Nano)
	return withdrawn
}

// takeWithdrawBatchLocked 在锁内撤卡并打包锁外广播素材；没有卡片时返回 nil。
func (b *MemoryBackend) takeWithdrawBatchLocked(td *memoryThread, reason withdrawReason) *withdrawBatch {
	if td == nil || len(td.pending) == 0 {
		return nil
	}
	removed := len(td.pending)
	requests := b.withdrawPendingLocked(td, reason)
	return &withdrawBatch{
		threadID: td.summary.ID,
		summary:  b.summaryForPublishLocked(td),
		requests: requests,
		removed:  removed,
	}
}

// publishWithdrawn 在锁外广播撤卡结果：待答列表清空、摘要复位。
func (b *MemoryBackend) publishWithdrawn(batch *withdrawBatch) {
	if batch == nil || batch.removed == 0 {
		return
	}
	b.hub.Publish(batch.threadID, nil, nil, batch.summary)
	b.hub.Notify(batch.threadID, "requests.changed", wire.RequestsChangedParams{
		ThreadID: batch.threadID,
		Requests: []wire.RemoteRequest{},
	})
}

// noticeWithdrawn 在锁外为撤卡写一条时间线提示。
// 删除的会话不再存在、没有撤下可答卡片时都不写，避免产生误导性记录。
func (b *MemoryBackend) noticeWithdrawn(batch *withdrawBatch, reason withdrawReason) {
	if batch == nil || len(batch.requests) == 0 || reason == withdrawDelete {
		return
	}
	if err := b.appendSystemNotice(batch.threadID, withdrawNoticeText(reason)); err != nil {
		log.Printf("append withdraw notice for thread %s: %v", batch.threadID, err)
	}
}

// finishWithdraw 在锁外完成撤卡后的广播与提示。
func (b *MemoryBackend) finishWithdraw(batch *withdrawBatch, reason withdrawReason) {
	b.publishWithdrawn(batch)
	b.noticeWithdrawn(batch, reason)
}

func withdrawNoticeText(reason withdrawReason) string {
	switch reason {
	case withdrawArchive:
		return "已撤回等待中的审批（会话已归档）"
	case withdrawShutdown:
		return "已撤回等待中的审批（宿主关闭）"
	default:
		return "已撤回等待中的审批（已中断）"
	}
}
