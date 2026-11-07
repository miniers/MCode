package remote

import (
	"context"
	"log"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// pendingAskTraceTimeout 是留痕写库的超时；写库失败只记日志，不影响答复、撤卡与广播。
const pendingAskTraceTimeout = 5 * time.Second

// 提问卡在留痕里的 resolution：答复不带选项 ID，只区分"答了"和"跳过了"。
const (
	askResolutionAnswered = "answered"
	askResolutionSkipped  = "skipped"
)

// pendingAskTraceRun 决定留痕写库的执行方式：默认丢给独立 goroutine，调用方完全不等待；
// 测试里替换成同步执行，用 channel 驱动断言，不靠 sleep。
var pendingAskTraceRun = func(fn func()) {
	go fn()
}

// traceAskCreated 异步写入一条 pending 留痕行，失败只记日志。
// 只由卡片创建方在登记成功后调用一次；复用既有卡片的路径（送达失败重投、计划单飞）不重复写，
// 否则会撞主键或把已结算的行重置回 pending。
func traceAskCreated(b *MemoryBackend, threadID, turnID string, req wire.RemoteRequest) {
	if b == nil || req.RequestID == "" {
		return
	}
	// b.store 与其它读点一致按不可变字段读取（只在 AttachStore 里赋值一次），不额外抢 b.mu。
	s := b.store
	if s == nil {
		return
	}
	ask := store.PendingAsk{
		RequestID: req.RequestID,
		ThreadID:  threadID,
		TurnID:    turnID,
		Kind:      req.Kind,
		Title:     derefString(req.Title),
		State:     store.PendingAskPending,
	}
	pendingAskTraceRun(func() {
		ctx, cancel := context.WithTimeout(context.Background(), pendingAskTraceTimeout)
		defer cancel()
		if err := s.InsertPendingAsk(ctx, ask); err != nil {
			log.Printf("trace pending ask %s create: %v", ask.RequestID, err)
		}
	})
}

// traceAskSettled 异步把留痕行结算为 settled，resolution 记录答复的选项 ID 或 answered/skipped。
func traceAskSettled(b *MemoryBackend, requestID, resolution string) {
	traceAskSettle(b, requestID, store.PendingAskSettled, resolution)
}

// traceAskWithdrawn 异步把留痕行结算为 withdrawn，resolution 记录撤卡入口（interrupt/archive/delete/shutdown）。
// 删除会话时行已随外键级联消失，UPDATE 只影响 0 行、不算错误；真正的写库失败也只记日志。
func traceAskWithdrawn(b *MemoryBackend, requestID string, reason withdrawReason) {
	traceAskSettle(b, requestID, store.PendingAskWithdrawn, string(reason))
}

// traceAskSettle 是 settled/withdrawn 共用的异步结算实现；store 未挂载时空操作。
func traceAskSettle(b *MemoryBackend, requestID, state, resolution string) {
	if b == nil || requestID == "" {
		return
	}
	s := b.store
	if s == nil {
		return
	}
	pendingAskTraceRun(func() {
		// 超时上下文在真正执行时创建，避免排队等待把预算吃完。
		ctx, cancel := context.WithTimeout(context.Background(), pendingAskTraceTimeout)
		defer cancel()
		if err := s.SettlePendingAsk(ctx, requestID, state, resolution); err != nil {
			log.Printf("trace pending ask %s settle %s: %v", requestID, state, err)
		}
	})
}

// askResolution 把一次答复折算成留痕里的 resolution：
// 提问卡只记 answered/skipped（答复本身不带选项 ID），审批与计划记 allow/allow_always/deny。
func askResolution(kind harness.RequestKind, resp approvalResponse) string {
	if kind == harness.RequestKindQuestion {
		if resp.skip || resp.optionID == "deny" {
			return askResolutionSkipped
		}
		return askResolutionAnswered
	}
	return resp.optionID
}
