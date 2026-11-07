package remote

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// TestAttachStoreFinalizesOrphanRunningTurn 验证启动加载会把上次进程遗留的
// status=running 轮次收尾为 interrupted，并同步会话最近终态。
// 否则重启后看板与侧栏仍会把该会话当成“进行中”。
func TestAttachStoreFinalizesOrphanRunningTurn(t *testing.T) {
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
		ID: "t-orphan", ProjectID: "p1", Title: "孤儿轮次", Provider: "memory",
		CreatedAt: now, UpdatedAt: now, LastStatus: "completed",
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	// 模拟宿主在轮次执行中被杀：turn 行停在 running，从未写入结束状态。
	if err := s1.CreateTurn(ctx, store.Turn{
		ID: "turn-orphan", ThreadID: "t-orphan", TurnIndex: 0,
		Status: "running", StartedAt: now, UserPrompt: "还在跑",
	}); err != nil {
		t.Fatalf("create running turn: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	defer s2.Close()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s2); err != nil {
		t.Fatalf("attach store: %v", err)
	}

	turn, err := s2.GetTurn(ctx, "turn-orphan")
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != "interrupted" {
		t.Fatalf("孤儿轮次状态 = %q，期望 interrupted", turn.Status)
	}
	if turn.CompletedAt == nil {
		t.Fatal("孤儿轮次应收尾并写入 completed_at")
	}

	summary, err := backend.GetThread(ctx, "t-orphan")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if summary.IsRunning {
		t.Fatal("重启后会话不应被判为运行中")
	}
	if summary.LastStatus == nil || *summary.LastStatus != "interrupted" {
		t.Fatalf("会话最近终态 = %v，期望 interrupted", summary.LastStatus)
	}
}

// TestSummaryForPublishDropsStaleRunningFlag 验证没有真实运行实例（runTurnID 为空）时，
// 对外摘要不会报告运行中：收尾路径残留的粘滞标记不能把会话永久锁在“进行中”。
func TestSummaryForPublishDropsStaleRunningFlag(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.AddThread(wire.ThreadSummary{ID: "t-stale", ProjectID: "p1", Title: "粘滞运行"}, nil)

	// 模拟异常收尾：IsRunning 残留为 true，但没有任何正在执行的轮次。
	backend.mu.Lock()
	backend.threads["t-stale"].summary.IsRunning = true
	backend.mu.Unlock()

	summary, err := backend.GetThread(context.Background(), "t-stale")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if summary.IsRunning {
		t.Fatal("没有运行实例时摘要不应报告运行中")
	}

	// 真实开轮后仍要如实报告运行中。
	backend.mu.Lock()
	backend.threads["t-stale"].runTurnID = "turn-live"
	backend.mu.Unlock()
	summary, err = backend.GetThread(context.Background(), "t-stale")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if !summary.IsRunning {
		t.Fatal("存在运行中的轮次时摘要应报告运行中")
	}
}

// seedOrphanDB 建库并写入一个项目、一个线程、一个指定状态的轮次和若干工具消息，
// 用于模拟宿主上次异常退出后留下的持久化数据。
func seedOrphanDB(t *testing.T, dbPath, turnID, turnStatus string, tools []store.Message) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	if err := s.UpsertProject(ctx, store.Project{ID: "p1", Name: "P1", Path: t.TempDir(), CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatalf("upsert project: %v", err)
	}
	if err := s.CreateThread(ctx, store.Thread{
		ID: "t-orphan", ProjectID: "p1", Title: "孤儿轮次", Provider: "memory",
		CreatedAt: now, UpdatedAt: now, LastStatus: "completed",
	}); err != nil {
		t.Fatalf("create thread: %v", err)
	}
	turn := store.Turn{
		ID: turnID, ThreadID: "t-orphan", TurnIndex: 0,
		Status: turnStatus, StartedAt: now.Add(-time.Hour), UserPrompt: "还在跑",
	}
	if turnStatus != "running" {
		completed := now.Add(-time.Minute)
		turn.CompletedAt = &completed
	}
	if err := s.CreateTurn(ctx, turn); err != nil {
		t.Fatalf("create turn: %v", err)
	}
	for i := range tools {
		tools[i].ThreadID = "t-orphan"
		tools[i].TurnID = turnID
		tools[i].TurnIndex = 0
		if tools[i].CreatedAt.IsZero() {
			tools[i].CreatedAt = now
		}
		if _, err := s.AppendMessage(ctx, tools[i]); err != nil {
			t.Fatalf("append tool %s: %v", tools[i].ID, err)
		}
	}
}

// attachOrphanBackend 打开库并挂载后端（触发启动收尾），返回库与后端。
func attachOrphanBackend(t *testing.T, dbPath string) (*store.Store, *MemoryBackend) {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	backend := NewMemoryBackend(hub)
	t.Cleanup(func() {
		backend.Close()
		hub.Close()
		s.Close()
	})
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("attach store: %v", err)
	}
	return s, backend
}

func orphanToolMessage(t *testing.T, s *store.Store, id string) store.Message {
	t.Helper()
	msgs, _, err := s.ListMessages(context.Background(), "t-orphan", 0, 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	for _, m := range msgs {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %s not found", id)
	return store.Message{}
}

func TestAttachStoreFinalizesOrphanTurnAndToolsAtomically(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mcode.db")
	running := "running"
	completed := "completed"
	seedOrphanDB(t, dbPath, "turn-orphan", "running", []store.Message{
		{ID: "tool-half", Role: "tool", Kind: "tool", Text: "半个参数", ToolStatus: &running},
		{ID: "tool-empty", Role: "tool", Kind: "tool", Text: "", ToolStatus: &running},
		{ID: "tool-done", Role: "tool", Kind: "tool", Text: "已完成", ToolStatus: &completed},
		{ID: "msg-assistant", Role: "assistant", Kind: "text", Text: "普通回复"},
	})

	s, backend := attachOrphanBackend(t, dbPath)
	ctx := context.Background()

	turn, err := s.GetTurn(ctx, "turn-orphan")
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != "interrupted" || turn.CompletedAt == nil {
		t.Fatalf("孤儿轮次未收尾: %+v", turn)
	}

	for id, wantPrefix := range map[string]string{"tool-half": "半个参数", "tool-empty": ""} {
		m := orphanToolMessage(t, s, id)
		if m.ToolStatus == nil || *m.ToolStatus != "failed" || m.ToolCompletedAt == nil {
			t.Fatalf("%s 未置为 failed: %+v", id, m)
		}
		if !strings.HasPrefix(m.Text, wantPrefix) || !strings.Contains(m.Text, interruptedToolNotice) {
			t.Fatalf("%s 正文缺少中断说明: %q", id, m.Text)
		}
		if got := strings.Count(m.Text, interruptedToolNotice); got != 1 {
			t.Fatalf("%s 的中断说明出现 %d 次，期望 1: %q", id, got, m.Text)
		}
	}

	done := orphanToolMessage(t, s, "tool-done")
	if done.ToolStatus == nil || *done.ToolStatus != "completed" {
		t.Fatalf("已完成工具被改动: %+v", done)
	}
	if strings.Contains(done.Text, interruptedToolNotice) {
		t.Fatalf("已完成工具不应带中断说明: %q", done.Text)
	}

	msgs, _, err := s.ListMessages(ctx, "t-orphan", 0, 100)
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	foundAssistant := false
	for _, m := range msgs {
		if m.ID == "msg-assistant" && m.Text == "普通回复" && m.Role == "assistant" {
			foundAssistant = true
		}
	}
	if !foundAssistant {
		t.Fatalf("普通 assistant 消息被改动或丢失: %+v", msgs)
	}

	summary, err := backend.GetThread(ctx, "t-orphan")
	if err != nil {
		t.Fatalf("get thread: %v", err)
	}
	if summary.LastStatus == nil || *summary.LastStatus != "interrupted" {
		t.Fatalf("会话最近终态 = %v，期望 interrupted", summary.LastStatus)
	}
}

func TestAttachStoreHealsInterruptedTurnWithRunningTool(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mcode.db")
	running := "running"
	seedOrphanDB(t, dbPath, "turn-old", "interrupted", []store.Message{
		{ID: "tool-stale", Role: "tool", Kind: "tool", Text: "历史遗留", ToolStatus: &running},
	})

	s, _ := attachOrphanBackend(t, dbPath)
	ctx := context.Background()

	turn, err := s.GetTurn(ctx, "turn-old")
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != "interrupted" {
		t.Fatalf("历史轮次状态被改动: %+v", turn)
	}

	m := orphanToolMessage(t, s, "tool-stale")
	if m.ToolStatus == nil || *m.ToolStatus != "failed" || m.ToolCompletedAt == nil {
		t.Fatalf("历史遗留工具消息未收尾: %+v", m)
	}
	if !strings.Contains(m.Text, interruptedToolNotice) {
		t.Fatalf("历史遗留工具消息缺少中断说明: %q", m.Text)
	}
	if got := strings.Count(m.Text, interruptedToolNotice); got != 1 {
		t.Fatalf("历史遗留工具消息的中断说明出现 %d 次，期望 1: %q", got, m.Text)
	}
}

func TestFinalizeOrphanTurnsSecondRunIsNoop(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mcode.db")
	running := "running"
	seedOrphanDB(t, dbPath, "turn-orphan", "running", []store.Message{
		{ID: "tool-orphan", Role: "tool", Kind: "tool", Text: "运行中", ToolStatus: &running},
	})

	s, backend := attachOrphanBackend(t, dbPath)
	ctx := context.Background()

	firstTurn, err := s.GetTurn(ctx, "turn-orphan")
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	firstTool := orphanToolMessage(t, s, "tool-orphan")

	msgsBefore, _, err := s.ListMessages(ctx, "t-orphan", 0, 100)
	if err != nil {
		t.Fatalf("list messages before second run: %v", err)
	}
	eventsBefore, err := s.ListFollowUpEvents(ctx, "t-orphan", 0)
	if err != nil {
		t.Fatalf("list events before second run: %v", err)
	}

	// 启动收尾已完成，再跑一次必须完全无副作用（按方案传 nil 的线程映射）。
	backend.finalizeOrphanTurns(ctx, s, nil)

	secondTurn, err := s.GetTurn(ctx, "turn-orphan")
	if err != nil {
		t.Fatalf("get turn again: %v", err)
	}
	secondTool := orphanToolMessage(t, s, "tool-orphan")

	if firstTurn.CompletedAt != nil && secondTurn.CompletedAt != nil && !firstTurn.CompletedAt.Equal(*secondTurn.CompletedAt) {
		t.Fatalf("第二次收尾改动了轮次完成时间: %v -> %v", firstTurn.CompletedAt, secondTurn.CompletedAt)
	}
	if secondTurn.Status != "interrupted" {
		t.Fatalf("第二次收尾后轮次状态 = %q，期望 interrupted", secondTurn.Status)
	}
	if firstTool.ToolCompletedAt == nil || secondTool.ToolCompletedAt == nil || *firstTool.ToolCompletedAt != *secondTool.ToolCompletedAt {
		t.Fatalf("第二次收尾改动了工具完成时间: %v -> %v", firstTool.ToolCompletedAt, secondTool.ToolCompletedAt)
	}
	if got := strings.Count(secondTool.Text, interruptedToolNotice); got != 1 {
		t.Fatalf("第二次收尾后中断说明出现 %d 次，期望 1: %q", got, secondTool.Text)
	}

	msgsAfter, _, err := s.ListMessages(ctx, "t-orphan", 0, 100)
	if err != nil {
		t.Fatalf("list messages after second run: %v", err)
	}
	eventsAfter, err := s.ListFollowUpEvents(ctx, "t-orphan", 0)
	if err != nil {
		t.Fatalf("list events after second run: %v", err)
	}
	if len(msgsAfter) != len(msgsBefore) {
		t.Fatalf("第二次收尾改变了消息条数: %d -> %d", len(msgsBefore), len(msgsAfter))
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("第二次收尾改变了事件条数: %d -> %d", len(eventsBefore), len(eventsAfter))
	}
}

// TestAttachStoreFinalizesBackgroundToolMessages 验证启动对账会把 provider 托管的后台任务
// （tool_status=background，所属回合已经结束）收敛为 failed：重启后后台任务已随 provider
// 进程消失，不收敛界面会永久显示运行中。
func TestAttachStoreFinalizesBackgroundToolMessages(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mcode.db")
	background := "background"
	completed := "completed"
	seedOrphanDB(t, dbPath, "turn-done", "completed", []store.Message{
		{ID: "tool-bg", Role: "tool", Kind: "tool", Text: "后台命令", ToolStatus: &background},
		{ID: "tool-done", Role: "tool", Kind: "tool", Text: "已完成的工具", ToolStatus: &completed},
	})

	s, _ := attachOrphanBackend(t, dbPath)
	ctx := context.Background()

	turn, err := s.GetTurn(ctx, "turn-done")
	if err != nil {
		t.Fatalf("get turn: %v", err)
	}
	if turn.Status != "completed" {
		t.Fatalf("已结束回合被改动: %+v", turn)
	}

	bg := orphanToolMessage(t, s, "tool-bg")
	if bg.ToolStatus == nil || *bg.ToolStatus != "failed" || bg.ToolCompletedAt == nil {
		t.Fatalf("后台任务未在启动时收敛: %+v", bg)
	}
	if !strings.Contains(bg.Text, backgroundToolNotice) {
		t.Fatalf("后台任务缺少中断说明: %q", bg.Text)
	}
	if got := strings.Count(bg.Text, backgroundToolNotice); got != 1 {
		t.Fatalf("后台任务的中断说明出现 %d 次，期望 1: %q", got, bg.Text)
	}

	done := orphanToolMessage(t, s, "tool-done")
	if done.ToolStatus == nil || *done.ToolStatus != "completed" || done.ToolCompletedAt != nil {
		t.Fatalf("已完成的工具消息被改动: %+v", done)
	}
}
