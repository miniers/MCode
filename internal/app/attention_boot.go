package app

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"mcode/internal/attention"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

const (
	// attentionBootTimeout 是启动对账查询后端的一次性超时。
	attentionBootTimeout = 5 * time.Second
	// attentionBootListLimit 是一次启动对账最多检查的审批提醒条数；
	// 审批提醒是短生命周期事件，正常远小于该上限。
	attentionBootListLimit = 1000
)

// threadRequestLister 是启动对账需要的最小后端查询能力：返回会话当前的待处理卡片，
// 会话不存在时返回资源不存在错误。*remote.MemoryBackend 满足该接口。
type threadRequestLister interface {
	ListRequests(ctx context.Context, threadID string) (wire.RequestsListResult, error)
}

// deletedThreadAttention 保存删除会话钩子要用的消解入口。删除钩子安装在 wireSwarm 里，
// 早于注意力接线构造，因此由接线启动时登记、停止时撤销。
var deletedThreadAttention struct {
	mu     sync.RWMutex
	wiring *attentionWiring
}

// registerDeletedThreadAttention 登记删除会话时要消解审批提醒的接线。
func registerDeletedThreadAttention(w *attentionWiring) {
	deletedThreadAttention.mu.Lock()
	deletedThreadAttention.wiring = w
	deletedThreadAttention.mu.Unlock()
}

// unregisterDeletedThreadAttention 撤销登记；只有当前登记的仍是同一个接线时才清空，
// 避免后启动的实例被先停止的实例清掉。
func unregisterDeletedThreadAttention(w *attentionWiring) {
	deletedThreadAttention.mu.Lock()
	if deletedThreadAttention.wiring == w {
		deletedThreadAttention.wiring = nil
	}
	deletedThreadAttention.mu.Unlock()
}

// resolveThreadApprovalsOnDelete 是删除会话钩子的追加动作：会话已经不存在，
// 指向它的「等你审批」提醒必须立即消解，不能等下一次启动对账。
func resolveThreadApprovalsOnDelete(threadID string) {
	if threadID == "" {
		return
	}
	deletedThreadAttention.mu.RLock()
	w := deletedThreadAttention.wiring
	deletedThreadAttention.mu.RUnlock()
	if w == nil {
		return
	}
	w.resolveSubject(attention.SubjectThread, threadID, attention.KindThreadApproval)
}

// reconcileThreadApprovals 在启动时一次性消解失效的「等你审批」提醒：对应会话已不存在，
// 或会话当前没有任何待处理卡片（后端启动对账已把上次进程留下的卡片标为失效）。
// 重复执行幂等；无法确认的后端错误只记日志，保留提醒不动。
func (w *attentionWiring) reconcileThreadApprovals(ctx context.Context) {
	if w == nil || w.svc == nil {
		return
	}
	lister, ok := w.sessions.(threadRequestLister)
	if !ok {
		// 没有可查询的后端（例如测试只注入看门狗所需能力）时不做对账。
		return
	}
	events, err := w.svc.List(ctx, store.AttentionEventFilter{
		UnresolvedOnly: true,
		Kinds:          []string{string(attention.KindThreadApproval)},
		Limit:          attentionBootListLimit,
	})
	if err != nil {
		log.Printf("attention: 启动对账读取审批提醒失败: %v", err)
		return
	}
	// 同一会话可能有多条提醒，只查一次后端。
	checked := make(map[string]struct{}, len(events))
	for _, ev := range events {
		threadID := ev.SubjectID
		if threadID == "" {
			threadID = ev.ThreadID
		}
		if threadID == "" {
			continue
		}
		if _, ok := checked[threadID]; ok {
			continue
		}
		checked[threadID] = struct{}{}

		res, err := lister.ListRequests(ctx, threadID)
		if err != nil {
			if isThreadNotFound(err) {
				w.resolveSubject(attention.SubjectThread, threadID, attention.KindThreadApproval)
				continue
			}
			log.Printf("attention: 启动对账查询会话 %s 的待处理请求失败: %v", threadID, err)
			continue
		}
		if len(res.Requests) == 0 {
			w.resolveSubject(attention.SubjectThread, threadID, attention.KindThreadApproval)
		}
	}
}

// isThreadNotFound 判断后端错误是否为「会话不存在」。
func isThreadNotFound(err error) bool {
	var rpcErr *wire.RPCError
	return errors.As(err, &rpcErr) && rpcErr.Code == wire.ErrCodeResourceNotFound
}
