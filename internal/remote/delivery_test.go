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

// deliveryScriptedHarness 按脚本逐条发事件：每次 Run 从 scripts 取一份脚本，发完后等 releaseCh
// 放行。事件同步发出，因此测试在放行前后观察 store 都能得到确定顺序，不依赖 sleep。
type deliveryScriptedHarness struct {
	scripts   chan []harness.Event
	releaseCh chan struct{}
	startedCh chan struct{}
}

func (h *deliveryScriptedHarness) Kind() harness.Kind { return harness.Kind("delivery-scripted") }

func (h *deliveryScriptedHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	select {
	case h.startedCh <- struct{}{}:
	default:
	}
	var script []harness.Event
	select {
	case script = <-h.scripts:
	case <-ctx.Done():
		return ctx.Err()
	}
	for _, ev := range script {
		emit(ev)
	}
	select {
	case <-h.releaseCh:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// deliveryTestRow 是 thread_deliveries 中与断言相关的列。
type deliveryTestRow struct {
	messageID  string
	followUpID string
	state      string
	turnID     string
	failure    string
}

// queryDeliveryRows 用独立的只读连接读投递行，避免占用 store 的单连接池；文件库为 WAL，可并发读。
func queryDeliveryRows(dbPath, threadID string) ([]deliveryTestRow, error) {
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
	SELECT message_id, follow_up_id, state, IFNULL(turn_id, ''), IFNULL(failure, '')
	FROM thread_deliveries WHERE thread_id = ? ORDER BY rowid`, threadID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []deliveryTestRow
	for rows.Next() {
		var r deliveryTestRow
		if err := rows.Scan(&r.messageID, &r.followUpID, &r.state, &r.turnID, &r.failure); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func mustDeliveryRows(t *testing.T, dbPath, threadID string) []deliveryTestRow {
	t.Helper()
	rows, err := queryDeliveryRows(dbPath, threadID)
	if err != nil {
		t.Fatalf("读取投递行失败: %v", err)
	}
	return rows
}

// newDeliveryTestBackend 搭建带 store 的 backend 与一个空会话，并返回数据库文件路径供旁路读取。
func newDeliveryTestBackend(t *testing.T, h harness.Harness) (*MemoryBackend, *store.Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "delivery.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	t.Cleanup(hub.Close)

	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	b.SetDataDir(dir)
	b.RegisterHarness(h)
	if err := b.AttachStore(context.Background(), s); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	b.AddProject(wire.ProjectSummary{ID: "p-delivery", Name: "delivery", Path: dir})
	th, err := b.CreateThread(context.Background(), wire.ThreadCreateParams{
		ProjectID: "p-delivery",
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread 失败: %v", err)
	}
	return b, s, th.ThreadID, dbPath
}

func sendDeliveryText(t *testing.T, b *MemoryBackend, threadID, text string) {
	t.Helper()
	if _, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID: threadID,
		Text:     text,
		ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("SendMessage 失败: %v", err)
	}
}

// queueDeliveryText 在运行中排入一条消息，返回其 follow-up ID。
func queueDeliveryText(t *testing.T, b *MemoryBackend, threadID, text string) string {
	t.Helper()
	res, err := b.SendMessage(context.Background(), wire.SendMessageParams{
		ThreadID:    threadID,
		Text:        text,
		WhenRunning: "queue",
	})
	if err != nil {
		t.Fatalf("排队消息失败: %v", err)
	}
	if res.FollowUpID == nil {
		t.Fatal("期望消息进入队列")
	}
	return *res.FollowUpID
}

// waitDeliveryStarted 等本轮真正进入 harness，确保随后排队的消息看到运行中的会话。
func waitDeliveryStarted(t *testing.T, h *deliveryScriptedHarness) {
	t.Helper()
	select {
	case <-h.startedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("等待 harness 启动超时")
	}
}

// waitDeliveryIdle 等空闲通知，只用作兜底超时，不轮询。
func waitDeliveryIdle(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("等待会话空闲超时")
	}
}

// TestAcceptedFromExplicitEvent 显式 EventTurnAccepted 把出队投递行迁到 accepted。
func TestAcceptedFromExplicitEvent(t *testing.T) {
	h := &deliveryScriptedHarness{
		scripts:   make(chan []harness.Event, 4),
		releaseCh: make(chan struct{}, 4),
		startedCh: make(chan struct{}, 8),
	}
	b, s, threadID, dbPath := newDeliveryTestBackend(t, h)

	h.scripts <- []harness.Event{{Type: harness.EventText, Text: "第一轮回复"}}
	sendDeliveryText(t, b, threadID, "turn 1")
	waitDeliveryStarted(t, h)
	fuID := queueDeliveryText(t, b, threadID, "排队消息")
	h.scripts <- []harness.Event{{Type: harness.EventTurnAccepted}}

	// 空闲信号在收尾开头就发出，早于出队第二轮启动；先放行第一轮并等第二轮进入 harness，
	// 再挂空闲等待，才能读到第二轮收尾后的投递状态。
	h.releaseCh <- struct{}{}
	waitDeliveryStarted(t, h)
	idle := waitIdleSignal(t, b, threadID)
	h.releaseCh <- struct{}{}
	waitDeliveryIdle(t, idle)

	rows := mustDeliveryRows(t, dbPath, threadID)
	if len(rows) != 1 {
		t.Fatalf("期望 1 条投递行，实际 %d 条: %+v", len(rows), rows)
	}
	if rows[0].state != store.DeliveryAccepted {
		t.Fatalf("投递状态 = %q，期望 accepted: %+v", rows[0].state, rows[0])
	}
	if rows[0].followUpID != fuID {
		t.Fatalf("follow_up_id = %q，期望 %q", rows[0].followUpID, fuID)
	}
	if rows[0].turnID == "" {
		t.Fatal("accepted 行缺少 turn_id")
	}
	if !hasAuditEvent(auditEvents(t, s, threadID), store.FollowUpEventAccepted, "", fuID) {
		t.Fatalf("缺少 accepted 审计事件: %+v", auditEvents(t, s, threadID))
	}
}

// TestAcceptedFromFirstUpstreamEvent 只发正文、不发显式回执时，首个上游事件也算 accepted。
func TestAcceptedFromFirstUpstreamEvent(t *testing.T) {
	h := &deliveryScriptedHarness{
		scripts:   make(chan []harness.Event, 4),
		releaseCh: make(chan struct{}, 4),
		startedCh: make(chan struct{}, 8),
	}
	b, _, threadID, dbPath := newDeliveryTestBackend(t, h)

	h.scripts <- []harness.Event{{Type: harness.EventText, Text: "第一轮回复"}}
	sendDeliveryText(t, b, threadID, "turn 1")
	waitDeliveryStarted(t, h)
	fuID := queueDeliveryText(t, b, threadID, "排队消息")
	h.scripts <- []harness.Event{{Type: harness.EventText, Text: "第二轮回复"}}

	// 空闲信号在收尾开头就发出，早于出队第二轮启动；先放行第一轮并等第二轮进入 harness，
	// 再挂空闲等待，才能读到第二轮收尾后的投递状态。
	h.releaseCh <- struct{}{}
	waitDeliveryStarted(t, h)
	idle := waitIdleSignal(t, b, threadID)
	h.releaseCh <- struct{}{}
	waitDeliveryIdle(t, idle)

	rows := mustDeliveryRows(t, dbPath, threadID)
	if len(rows) != 1 || rows[0].state != store.DeliveryAccepted || rows[0].followUpID != fuID {
		t.Fatalf("首个上游事件应把投递行迁到 accepted: %+v", rows)
	}
}

// TestDispatchFailedMarksFailed 出队后首个事件就是错误、重试耗尽仍无上游输出时记为 failed。
func TestDispatchFailedMarksFailed(t *testing.T) {
	h := &deliveryScriptedHarness{
		scripts:   make(chan []harness.Event, 4),
		releaseCh: make(chan struct{}, 4),
		startedCh: make(chan struct{}, 8),
	}
	b, s, threadID, dbPath := newDeliveryTestBackend(t, h)

	h.scripts <- []harness.Event{{Type: harness.EventText, Text: "第一轮回复"}}
	sendDeliveryText(t, b, threadID, "turn 1")
	waitDeliveryStarted(t, h)
	fuID := queueDeliveryText(t, b, threadID, "排队消息")
	h.scripts <- []harness.Event{{Type: harness.EventError, Err: "provider 启动失败"}}

	// 空闲信号在收尾开头就发出，早于出队第二轮启动；先放行第一轮并等第二轮进入 harness，
	// 再挂空闲等待，才能读到第二轮收尾后的投递状态。
	h.releaseCh <- struct{}{}
	waitDeliveryStarted(t, h)
	idle := waitIdleSignal(t, b, threadID)
	h.releaseCh <- struct{}{}
	waitDeliveryIdle(t, idle)

	rows := mustDeliveryRows(t, dbPath, threadID)
	if len(rows) != 1 || rows[0].state != store.DeliveryFailed {
		t.Fatalf("错误首事件应把投递行迁到 failed: %+v", rows)
	}
	if rows[0].failure != "provider 启动失败" {
		t.Fatalf("failure = %q，期望原始错误文字", rows[0].failure)
	}
	if !hasAuditEvent(auditEvents(t, s, threadID), store.FollowUpEventFailed, "", fuID) {
		t.Fatalf("缺少 failed 审计事件: %+v", auditEvents(t, s, threadID))
	}
}

// TestDirectSendWritesNoDelivery 不是出队开始的普通发送不写 thread_deliveries。
func TestDirectSendWritesNoDelivery(t *testing.T) {
	h := &deliveryScriptedHarness{
		scripts:   make(chan []harness.Event, 4),
		releaseCh: make(chan struct{}, 4),
		startedCh: make(chan struct{}, 8),
	}
	b, _, threadID, dbPath := newDeliveryTestBackend(t, h)

	h.scripts <- []harness.Event{{Type: harness.EventText, Text: "直接回复"}}
	sendDeliveryText(t, b, threadID, "直接发送")
	waitDeliveryStarted(t, h)
	idle := waitIdleSignal(t, b, threadID)
	h.releaseCh <- struct{}{}
	waitDeliveryIdle(t, idle)

	if rows := mustDeliveryRows(t, dbPath, threadID); len(rows) != 0 {
		t.Fatalf("普通发送不应产生投递行: %+v", rows)
	}
}

// TestDispatchRowWrittenBeforeQueueRemoval 出队提交（删队列行）时，dispatching 行必须已经落库。
func TestDispatchRowWrittenBeforeQueueRemoval(t *testing.T) {
	h := &deliveryScriptedHarness{
		scripts:   make(chan []harness.Event, 4),
		releaseCh: make(chan struct{}, 4),
		startedCh: make(chan struct{}, 8),
	}
	b, s, threadID, dbPath := newDeliveryTestBackend(t, h)

	h.scripts <- []harness.Event{{Type: harness.EventText, Text: "第一轮回复"}}
	sendDeliveryText(t, b, threadID, "turn 1")
	waitDeliveryStarted(t, h)
	fuID := queueDeliveryText(t, b, threadID, "排队消息")
	h.scripts <- []harness.Event{{Type: harness.EventText, Text: "第二轮回复"}}

	sawDispatching := make(chan bool, 8)
	b.SetQueueChangedHook(func(snap wire.ThreadsQueueSnapshot) {
		if snap.ThreadID != threadID || len(snap.FollowUps) != 0 {
			return
		}
		rows, err := queryDeliveryRows(dbPath, threadID)
		if err != nil {
			sawDispatching <- false
			return
		}
		sawDispatching <- len(rows) == 1 && rows[0].state == store.DeliveryDispatching && rows[0].followUpID == fuID
	})

	idle := waitIdleSignal(t, b, threadID)
	h.releaseCh <- struct{}{}
	h.releaseCh <- struct{}{}
	waitDeliveryIdle(t, idle)

	select {
	case ok := <-sawDispatching:
		if !ok {
			t.Fatal("删队列行时没有看到 dispatching 投递行")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("没有等到空队列的提交回调")
	}
	if !hasAuditEvent(auditEvents(t, s, threadID), store.FollowUpEventDispatching, "", fuID) {
		t.Fatalf("缺少 dispatching 审计事件")
	}
}
