package remote

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// newAttachedBackend 用给定 store 新建并挂载一个 backend，模拟一次进程启动。
// 同一 store 可以先后挂载多个 backend，以此模拟重启；关闭顺序由 t.Cleanup 的 LIFO 保证。
func newAttachedBackend(t *testing.T, ctx context.Context, s *store.Store) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	backend := NewMemoryBackend(hub)
	if err := backend.AttachStore(ctx, s); err != nil {
		backend.Close()
		hub.Close()
		t.Fatalf("attach store: %v", err)
	}
	t.Cleanup(func() {
		backend.Close()
		hub.Close()
	})
	return backend
}

// infoNoticeTexts 返回会话时间线里的提示正文（只取 info 消息），保持时间顺序。
func infoNoticeTexts(t *testing.T, backend *MemoryBackend, threadID string) []string {
	t.Helper()
	page, err := backend.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("thread history %s: %v", threadID, err)
	}
	texts := make([]string, 0, len(page.Messages))
	for _, m := range page.Messages {
		if m.Kind != nil && *m.Kind == "info" {
			texts = append(texts, m.Text)
		}
	}
	return texts
}

// bootReconcileRawDB 直接打开库读状态：remote 包没有 pending_asks / thread_deliveries 的只读 API。
func bootReconcileRawDB(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// pendingAskRow 读一条 pending_asks 的终态与结算原因。
func pendingAskRow(t *testing.T, dbPath, requestID string) (state, resolution string) {
	t.Helper()
	db := bootReconcileRawDB(t, dbPath)
	if err := db.QueryRow(
		"SELECT state, resolution FROM pending_asks WHERE request_id = ?", requestID,
	).Scan(&state, &resolution); err != nil {
		t.Fatalf("read pending_ask %s: %v", requestID, err)
	}
	return state, resolution
}

// deliveryRowState 读一条 thread_deliveries 的状态。
func deliveryRowState(t *testing.T, dbPath, messageID string) string {
	t.Helper()
	db := bootReconcileRawDB(t, dbPath)
	var state string
	if err := db.QueryRow(
		"SELECT state FROM thread_deliveries WHERE message_id = ?", messageID,
	).Scan(&state); err != nil {
		t.Fatalf("read delivery %s: %v", messageID, err)
	}
	return state
}

// TestAttachStoreReconcilesAskAndDeliveryOnce 验证启动对账把上次进程遗留的待答卡片标为 expired、
// 停在途中的投递标为 unknown，并各写一条时间线提示；同一 store 再次启动不重复提示、不复活卡片。
func TestAttachStoreReconcilesAskAndDeliveryOnce(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcode.db")

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	now := time.Now().UTC()
	if err := s.UpsertProject(ctx, store.Project{ID: "p1", Name: "P1", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	if err := s.CreateThread(ctx, store.Thread{
		ID: "t1", ProjectID: "p1", Title: "重启对账", Provider: "memory",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	// 上次进程被杀时的残留：一张提问卡片还开着，一条排队消息已出队但没拿到回执。
	if err := s.InsertPendingAsk(ctx, store.PendingAsk{
		RequestID: "req-1", ThreadID: "t1", Kind: string(harness.RequestKindQuestion),
		Title: "确认目标", CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert pending ask: %v", err)
	}
	if err := s.InsertDelivery(ctx, store.Delivery{
		MessageID: "msg-1", ThreadID: "t1", FollowUpID: "fu-1", CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}

	backend := newAttachedBackend(t, ctx, s)

	// 重启后不重建可答卡片。
	reqs, err := backend.ListRequests(ctx, "t1")
	if err != nil {
		t.Fatalf("list requests: %v", err)
	}
	if len(reqs.Requests) != 0 {
		t.Fatalf("重启后不应有可答卡片，实际 %d 张", len(reqs.Requests))
	}

	// 时间线恰好两条提示：先卡片失效，后投递未知。
	want := []string{
		"上次有一个「提问」请求（确认目标）在宿主重启时失效，没有送达。",
		"上一条排队消息可能没有送达，本次没有重发。",
	}
	got := infoNoticeTexts(t, backend, "t1")
	if len(got) != len(want) {
		t.Fatalf("提示条数 = %d，期望 %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条提示 = %q，期望 %q", i+1, got[i], want[i])
		}
	}

	// 两行状态都已终态化。
	if state, resolution := pendingAskRow(t, dbPath, "req-1"); state != store.PendingAskExpired || resolution != "host_restarted" {
		t.Fatalf("pending_ask 状态 = %q/%q，期望 expired/host_restarted", state, resolution)
	}
	if state := deliveryRowState(t, dbPath, "msg-1"); state != store.DeliveryUnknown {
		t.Fatalf("delivery 状态 = %q，期望 unknown", state)
	}

	// 同一 store 再启动一次：幂等，不新增提示、不复活卡片。
	restarted := newAttachedBackend(t, ctx, s)
	reqs, err = restarted.ListRequests(ctx, "t1")
	if err != nil {
		t.Fatalf("list requests after restart: %v", err)
	}
	if len(reqs.Requests) != 0 {
		t.Fatalf("再次启动后不应有可答卡片，实际 %d 张", len(reqs.Requests))
	}
	if got := infoNoticeTexts(t, restarted, "t1"); len(got) != len(want) {
		t.Fatalf("再次启动后提示条数 = %d，期望 %d: %q", len(got), len(want), got)
	}
	// 库里的提示总数也保持两条，说明第二次启动没有写任何提示。
	msgs, _, err := s.ListMessages(ctx, "t1", 0, 50)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != len(want) {
		t.Fatalf("库中消息条数 = %d，期望 %d", len(msgs), len(want))
	}
}

// TestAttachStoreReconcileSkipsNoticeWhenThreadMissing 验证卡片所属会话不在内存中时（例如加载时被隔离），
// 启动对账照常把行标为 expired，只记日志、不写提示、不阻断启动。
func TestAttachStoreReconcileSkipsNoticeWhenThreadMissing(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcode.db")

	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	now := time.Now().UTC()
	if err := s1.UpsertProject(ctx, store.Project{ID: "p1", Name: "P1", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	if err := s1.CreateThread(ctx, store.Thread{
		ID: "t-gone", ProjectID: "p1", Title: "将被隔离", Provider: "memory",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	if err := s1.InsertPendingAsk(ctx, store.PendingAsk{
		RequestID: "req-gone", ThreadID: "t-gone", Kind: string(harness.RequestKindPlan),
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert pending ask: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	// 把线程行写成不可解码的时间：启动加载会把它隔离，内存里便没有该会话；
	// 但 pending_ask 仍指向数据库里存在的线程行，不违反外键。
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := raw.ExecContext(ctx, "UPDATE threads SET created_at = 'not-a-time' WHERE id = 't-gone'"); err != nil {
		t.Fatalf("corrupt thread row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	// 启动不应报错：对账遇到内存里没有的会话只记日志。
	backend := newAttachedBackend(t, ctx, s2)
	if _, err := backend.GetThread(ctx, "t-gone"); err == nil {
		t.Fatal("被隔离的会话不应出现在内存中")
	}

	if state, resolution := pendingAskRow(t, dbPath, "req-gone"); state != store.PendingAskExpired || resolution != "host_restarted" {
		t.Fatalf("pending_ask 状态 = %q/%q，期望 expired/host_restarted", state, resolution)
	}
	msgs, _, err := s2.ListMessages(ctx, "t-gone", 0, 50)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if len(msgs) != 0 {
		t.Fatalf("线程不在内存时不应写提示，实际 %d 条", len(msgs))
	}
}

// TestBootAskNoticeText 验证失效提示的文案拼装：title 为空时省略括号，未知 kind 写「等待」。
func TestBootAskNoticeText(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		title string
		want  string
	}{
		{
			name:  "带头衔",
			kind:  string(harness.RequestKindPlan),
			title: "重构队列",
			want:  "上次有一个「计划」请求（重构队列）在宿主重启时失效，没有送达。",
		},
		{
			name: "标题为空",
			kind: string(harness.RequestKindPermissions),
			want: "上次有一个「权限」请求在宿主重启时失效，没有送达。",
		},
		{
			name: "未知类型",
			kind: "futureKind",
			want: "上次有一个「等待」请求在宿主重启时失效，没有送达。",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bootAskNotice(tc.kind, tc.title); got != tc.want {
				t.Fatalf("提示文案 = %q，期望 %q", got, tc.want)
			}
		})
	}
}
