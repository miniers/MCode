package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/attention"
	"mcode/internal/kanban"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// bootReconcileSessions 是启动对账用的假后端：pending 给出会话当前的待处理卡片数，
// deleted 里的会话按后端契约返回资源不存在。
type bootReconcileSessions struct {
	pending map[string]int
	deleted map[string]bool
}

func (s *bootReconcileSessions) SessionState(ctx context.Context, threadID string) (kanban.SessionState, error) {
	return kanban.SessionState{}, nil
}

func (s *bootReconcileSessions) ListRequests(ctx context.Context, threadID string) (wire.RequestsListResult, error) {
	if s.deleted[threadID] {
		return wire.RequestsListResult{}, &wire.RPCError{
			Code:    wire.ErrCodeResourceNotFound,
			Message: "thread not found",
		}
	}
	return wire.RequestsListResult{
		ThreadID: threadID,
		Requests: make([]wire.RemoteRequest, s.pending[threadID]),
	}, nil
}

// openBootTestAttention 用临时库构造真实的待处理事件服务。
func openBootTestAttention(t *testing.T) *attention.Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "attention-boot.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("close store: %v", err)
		}
	})
	return attention.New(st, attention.Options{})
}

// recordBootApproval 写入一条指向指定会话的「等你审批」提醒。
func recordBootApproval(t *testing.T, svc *attention.Service, threadID string) {
	t.Helper()
	if _, _, err := svc.Record(context.Background(), attention.Input{
		Kind:        attention.KindThreadApproval,
		SubjectType: attention.SubjectThread,
		SubjectID:   threadID,
		ThreadID:    threadID,
		Title:       "会话 " + threadID,
		Detail:      "等待审批",
		EnteredAt:   time.Now(),
	}); err != nil {
		t.Fatalf("record approval for %s: %v", threadID, err)
	}
}

// unresolvedApprovalsFor 统计指定会话仍未消解的审批提醒条数。
func unresolvedApprovalsFor(t *testing.T, svc *attention.Service, threadID string) int {
	t.Helper()
	count := 0
	for _, ev := range listUnresolved(t, svc) {
		if ev.Kind == string(attention.KindThreadApproval) && ev.SubjectID == threadID {
			count++
		}
	}
	return count
}

// 启动对账：已删会话的提醒被消解，仍有待处理请求的会话保持不动；重复执行结果不变。
func TestReconcileThreadApprovalsResolvesDeletedThreadOnly(t *testing.T) {
	svc := openBootTestAttention(t)
	recordBootApproval(t, svc, "thread-deleted")
	recordBootApproval(t, svc, "thread-live")

	wiring := newAttentionWiring(attentionWiringOptions{
		Service: svc,
		Sessions: &bootReconcileSessions{
			pending: map[string]int{"thread-live": 1},
			deleted: map[string]bool{"thread-deleted": true},
		},
	})
	ctx := context.Background()
	wiring.reconcileThreadApprovals(ctx)

	if got := unresolvedApprovalsFor(t, svc, "thread-deleted"); got != 0 {
		t.Fatalf("已删会话的提醒应被消解，实际残留 %d 条", got)
	}
	if got := unresolvedApprovalsFor(t, svc, "thread-live"); got != 1 {
		t.Fatalf("仍有待处理请求的会话提醒不应被消解，实际 %d 条", got)
	}

	// 第二次执行不改动任何状态。
	wiring.reconcileThreadApprovals(ctx)
	if got := unresolvedApprovalsFor(t, svc, "thread-deleted"); got != 0 {
		t.Fatalf("重复对账后已删会话的提醒不应复活，实际 %d 条", got)
	}
	if got := unresolvedApprovalsFor(t, svc, "thread-live"); got != 1 {
		t.Fatalf("重复对账不应消解活会话的提醒，实际 %d 条", got)
	}
}

// 会话仍在但已没有待处理卡片（例如上次进程留下的卡片已被启动对账标为失效）时同样消解。
func TestReconcileThreadApprovalsResolvesThreadWithoutPending(t *testing.T) {
	svc := openBootTestAttention(t)
	recordBootApproval(t, svc, "thread-idle")

	wiring := newAttentionWiring(attentionWiringOptions{
		Service:  svc,
		Sessions: &bootReconcileSessions{pending: map[string]int{}},
	})
	wiring.reconcileThreadApprovals(context.Background())

	if got := unresolvedApprovalsFor(t, svc, "thread-idle"); got != 0 {
		t.Fatalf("已无待处理请求的会话提醒应被消解，实际残留 %d 条", got)
	}
}

// 删除会话钩子的追加动作：该会话的审批提醒立即消解，重复调用幂等。
func TestThreadDeletedHookResolvesApproval(t *testing.T) {
	svc := openBootTestAttention(t)
	recordBootApproval(t, svc, "thread-gone")

	wiring := newAttentionWiring(attentionWiringOptions{Service: svc})
	registerDeletedThreadAttention(wiring)
	t.Cleanup(func() { unregisterDeletedThreadAttention(wiring) })

	resolveThreadApprovalsOnDelete("thread-gone")
	if got := unresolvedApprovalsFor(t, svc, "thread-gone"); got != 0 {
		t.Fatalf("删除会话后其审批提醒应被消解，实际残留 %d 条", got)
	}

	resolveThreadApprovalsOnDelete("thread-gone")
	if got := unresolvedApprovalsFor(t, svc, "thread-gone"); got != 0 {
		t.Fatalf("重复触发删除钩子不应产生残留，实际 %d 条", got)
	}
}
