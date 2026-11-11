package remote

import (
	"context"
	"errors"
	"log"

	"mcode/internal/harness"
	"mcode/internal/store"
)

const (
	// bootAskResolution 是启动对账给上次进程遗留卡片写入的失效原因。
	bootAskResolution = "host_restarted"
	// bootDeliveryNotice 是排队消息停在发送途中时的时间线提示，同一线程每行一条。
	bootDeliveryNotice = "上一条排队消息可能没有送达，本次没有重发。"
)

// reconcileBoot 在启动加载末尾做一次对账：上次进程留下的待答卡片标为失效
// （pending → expired），停在发送途中的排队消息标为 unknown，然后逐行写时间线提示。
// 只改状态与写提示：不重建可答卡片、不重发消息、不自动续投。
// 调用前提：b.store 与 b.threads 已挂载（提示依赖内存线程表定位会话），且调用方不持有 b.mu。
// 任何一步失败都只记日志，不阻断启动。
func (b *MemoryBackend) reconcileBoot(ctx context.Context, s *store.Store) {
	if !s.IsPrimary() {
		log.Printf("store: 另一个宿主进程持有数据目录，跳过启动审批与投递对账")
		return
	}
	// 两张表都只返回“本次被转换的行”，且转换后的行不会再被返回，
	// 因此提示与状态迁移一样只发生一次，重复启动不会重复提示。
	asks, err := s.ExpirePendingAsks(ctx, bootAskResolution)
	if err != nil {
		log.Printf("boot reconcile expire pending asks error: %v", err)
	}
	deliveries, err := s.SettleInterruptedDeliveries(ctx)
	if err != nil {
		log.Printf("boot reconcile settle interrupted deliveries error: %v", err)
	}

	// 状态全部落库之后再写提示：中途崩溃最多丢一条提示，绝不会重复提示。
	for _, ask := range asks {
		if err := b.appendSystemNotice(ask.ThreadID, bootAskNotice(ask.Kind, ask.Title)); err != nil {
			logBootNoticeError("ask", ask.ThreadID, err)
		}
	}
	for _, delivery := range deliveries {
		if err := b.appendSystemNotice(delivery.ThreadID, bootDeliveryNotice); err != nil {
			logBootNoticeError("delivery", delivery.ThreadID, err)
		}
	}
}

// logBootNoticeError 记录提示写入失败。线程已不在内存（被删、或加载时被隔离）不算异常：
// 状态已经落库，跳过提示即可。
func logBootNoticeError(kind, threadID string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		log.Printf("boot reconcile skip %s notice: thread %s not loaded", kind, threadID)
		return
	}
	log.Printf("boot reconcile append %s notice for thread %s error: %v", kind, threadID, err)
}

// bootAskNotice 生成卡片失效的时间线提示；title 为空时省略括号部分。
func bootAskNotice(kind, title string) string {
	if title == "" {
		return "上次有一个「" + requestKindChineseName(kind) + "」请求在宿主重启时失效，没有送达。"
	}
	return "上次有一个「" + requestKindChineseName(kind) + "」请求（" + title + "）在宿主重启时失效，没有送达。"
}

// requestKindChineseName 把 harness.RequestKind 映射为提示文案里的中文名。
// 未知取值统一写「等待」：新增 kind 时文案仍然可读，也不会把英文常量漏给用户。
func requestKindChineseName(kind string) string {
	switch harness.RequestKind(kind) {
	case harness.RequestKindCommand:
		return "命令"
	case harness.RequestKindFileChange:
		return "文件修改"
	case harness.RequestKindTool:
		return "工具"
	case harness.RequestKindPlan:
		return "计划"
	case harness.RequestKindQuestion:
		return "提问"
	case harness.RequestKindCommit:
		return "提交"
	case harness.RequestKindPermissions:
		return "权限"
	default:
		return "等待"
	}
}
