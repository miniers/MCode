package remote

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// syncPendingAskTrace 把留痕写库换成同步执行，断言不靠 sleep；测试结束后恢复默认实现。
func syncPendingAskTrace(t *testing.T) {
	t.Helper()
	prev := pendingAskTraceRun
	pendingAskTraceRun = func(fn func()) { fn() }
	t.Cleanup(func() { pendingAskTraceRun = prev })
}

// newAskTraceBackend 建一个挂了真实 store 的后端，返回后端与库文件路径。
func newAskTraceBackend(t *testing.T) (*MemoryBackend, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "pending_asks.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开 store 失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	if err := backend.AttachStore(context.Background(), s); err != nil {
		t.Fatalf("AttachStore 失败: %v", err)
	}
	backend.SetDataDir(filepath.Dir(dbPath))
	return backend, dbPath
}

// startAskTraceCard 建线程并跑一轮，返回撤卡相关推送与首张待处理卡片。
// 线程必须挂在已落库的项目下，否则 threads.project_id 外键失败，留痕行的外键也随之失败。
func startAskTraceCard(t *testing.T, backend *MemoryBackend, threadID string, h *withdrawHarness) (*withdrawEvents, wire.RemoteRequest) {
	t.Helper()
	ctx := context.Background()
	projectID := "p-" + threadID
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "留痕测试", Path: t.TempDir()})
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: projectID, Title: "留痕线程"}, nil)
	backend.SetHarness(h, "/workspace", harness.ModeAgent)

	ev := &withdrawEvents{
		requests: make(chan wire.RequestsChangedParams, 8),
		threads:  make(chan wire.ThreadsChanged, 8),
	}
	conn := newMockStreamConn()
	conn.notifyFn = func(method string, params any) error {
		switch method {
		case "requests.changed":
			if p, ok := params.(wire.RequestsChangedParams); ok {
				ev.requests <- p
			}
		case "threads.changed":
			if p, ok := params.(wire.ThreadsChanged); ok {
				ev.threads <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "发起审批",
		ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	return ev, waitFirstCard(t, ev.requests)
}

// askTraceRow 是断言用的 pending_asks 行快照。
type askTraceRow struct {
	state      string
	resolution string
	kind       string
	title      string
	threadID   string
	turnID     string
}

// readAskTrace 用只读连接读一行留痕，绕开 store 包未导出单行查询的限制。
func readAskTrace(t *testing.T, dbPath, requestID string) askTraceRow {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=query_only(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("打开只读连接失败: %v", err)
	}
	defer db.Close()

	var row askTraceRow
	err = db.QueryRowContext(context.Background(),
		"SELECT state, resolution, kind, title, thread_id, turn_id FROM pending_asks WHERE request_id = ?",
		requestID).Scan(&row.state, &row.resolution, &row.kind, &row.title, &row.threadID, &row.turnID)
	if err != nil {
		t.Fatalf("读取 pending_ask %s 失败: %v", requestID, err)
	}
	return row
}

// waitNoRequests 等到后端推送"待处理列表已清空"，代表答复后的清理流程已经走完。
func waitNoRequests(t *testing.T, ch <-chan wire.RequestsChangedParams) {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case p := <-ch:
			if len(p.Requests) == 0 {
				return
			}
		case <-timeout:
			t.Fatal("等待待处理列表清空超时")
		}
	}
}

// 卡片登记后留痕为 pending；答复为 allow 后变 settled，resolution 记录选项 ID。
func TestPendingAskTraceCreatedThenSettled(t *testing.T) {
	syncPendingAskTrace(t)
	backend, dbPath := newAskTraceBackend(t)

	threadID := "00000000-0000-0000-0000-000000001001"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_trace_settle",
		ToolName: "bash",
		Summary:  "run echo trace",
	})
	ev, req := startAskTraceCard(t, backend, threadID, h)

	created := readAskTrace(t, dbPath, req.RequestID)
	if created.state != store.PendingAskPending {
		t.Fatalf("卡片登记后留痕应为 pending，实际 %q", created.state)
	}
	if created.kind != req.Kind {
		t.Errorf("留痕 kind = %q，期望 %q", created.kind, req.Kind)
	}
	if created.title != derefString(req.Title) {
		t.Errorf("留痕 title = %q，期望 %q", created.title, derefString(req.Title))
	}
	if created.threadID != threadID {
		t.Errorf("留痕 thread_id = %q，期望 %q", created.threadID, threadID)
	}
	if created.turnID != derefString(req.TurnID) {
		t.Errorf("留痕 turn_id = %q，期望 %q", created.turnID, derefString(req.TurnID))
	}
	if created.resolution != "" {
		t.Errorf("pending 行不应带 resolution，实际 %q", created.resolution)
	}

	optAllow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitNoRequests(t, ev.requests)

	settled := readAskTrace(t, dbPath, req.RequestID)
	if settled.state != store.PendingAskSettled {
		t.Fatalf("答复后留痕应为 settled，实际 %q", settled.state)
	}
	if settled.resolution != "allow" {
		t.Errorf("settled resolution = %q，期望 allow", settled.resolution)
	}
}

// 提问卡的答复不带选项 ID：如实作答记 answered，跳过记 skipped。
func TestPendingAskTraceQuestionResolution(t *testing.T) {
	cases := []struct {
		name       string
		params     func(req wire.RemoteRequest) wire.RequestsRespondParams
		resolution string
	}{
		{
			name: "answered",
			params: func(req wire.RemoteRequest) wire.RequestsRespondParams {
				return wire.RequestsRespondParams{
					ThreadID:  req.ThreadID,
					RunID:     req.RunID,
					RequestID: req.RequestID,
					Answers:   json.RawMessage(`{"q1":["A"]}`),
				}
			},
			resolution: askResolutionAnswered,
		},
		{
			name: "skipped",
			params: func(req wire.RemoteRequest) wire.RequestsRespondParams {
				skip := true
				return wire.RequestsRespondParams{
					ThreadID:  req.ThreadID,
					RunID:     req.RunID,
					RequestID: req.RequestID,
					Skip:      &skip,
				}
			},
			resolution: askResolutionSkipped,
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			syncPendingAskTrace(t)
			backend, dbPath := newAskTraceBackend(t)

			threadID := fmt.Sprintf("00000000-0000-0000-0000-0000000011%02d", i)
			h := newWithdrawHarness(false, harness.ApprovalRequest{
				ToolName:  "AskUserQuestion",
				Kind:      harness.RequestKindQuestion,
				Questions: []harness.Question{{ID: "q1", Prompt: "用哪个库？", Choices: []harness.QuestionChoice{{Label: "A"}}}},
			})
			ev, req := startAskTraceCard(t, backend, threadID, h)

			if _, err := backend.RespondRequest(context.Background(), tc.params(req)); err != nil {
				t.Fatalf("RespondRequest failed: %v", err)
			}
			waitNoRequests(t, ev.requests)

			row := readAskTrace(t, dbPath, req.RequestID)
			if row.state != store.PendingAskSettled {
				t.Fatalf("提问答复后留痕应为 settled，实际 %q", row.state)
			}
			if row.resolution != tc.resolution {
				t.Errorf("提问卡 resolution = %q，期望 %q", row.resolution, tc.resolution)
			}
		})
	}
}

// 归档撤卡后留痕变 withdrawn，resolution 记录撤卡入口。
func TestPendingAskTraceWithdrawnOnArchive(t *testing.T) {
	syncPendingAskTrace(t)
	backend, dbPath := newAskTraceBackend(t)

	threadID := "00000000-0000-0000-0000-000000001201"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_trace_withdraw",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	_, req := startAskTraceCard(t, backend, threadID, h)

	if row := readAskTrace(t, dbPath, req.RequestID); row.state != store.PendingAskPending {
		t.Fatalf("撤卡前留痕应为 pending，实际 %q", row.state)
	}

	archived := true
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}

	r := waitInteract(t, h.resultCh)
	if !errors.Is(r.err, errRequestWithdrawn) {
		t.Fatalf("归档撤卡后 Interact 应返回 errRequestWithdrawn，实际 %v", r.err)
	}
	row := readAskTrace(t, dbPath, req.RequestID)
	if row.state != store.PendingAskWithdrawn {
		t.Fatalf("归档撤卡后留痕应为 withdrawn，实际 %q", row.state)
	}
	if row.resolution != string(withdrawArchive) {
		t.Errorf("withdrawn resolution = %q，期望 %q", row.resolution, string(withdrawArchive))
	}
}

// 未挂 store 的后端（测试与本地内存模式）上留痕全是空操作：不 panic，答复与撤卡行为不变。
func TestPendingAskTraceWithoutStore(t *testing.T) {
	syncPendingAskTrace(t)
	backend := newWithdrawBackend(t)

	// 撤卡路径：卡片开着直接归档。
	threadID := "00000000-0000-0000-0000-000000001301"
	h := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_trace_no_store_withdraw",
		ToolName: "bash",
		Summary:  "run sleep 10",
	})
	_, _ = startAskTraceCard(t, backend, threadID, h)
	archived := true
	if _, err := backend.UpdateThread(context.Background(), wire.ThreadUpdateParams{
		ThreadID: threadID,
		Archived: &archived,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}
	if r := waitInteract(t, h.resultCh); !errors.Is(r.err, errRequestWithdrawn) {
		t.Fatalf("归档撤卡后 Interact 应返回 errRequestWithdrawn，实际 %v", r.err)
	}

	// 结算路径：新线程里答复一张审批卡。
	settleThreadID := "00000000-0000-0000-0000-000000001303"
	settleHarness := newWithdrawHarness(false, harness.ApprovalRequest{
		ToolID:   "call_trace_no_store_settle",
		ToolName: "bash",
		Summary:  "run echo trace",
	})
	ev, req := startAskTraceCard(t, backend, settleThreadID, settleHarness)
	optAllow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  settleThreadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitNoRequests(t, ev.requests)
	if r := waitInteract(t, settleHarness.resultCh); r.err != nil {
		t.Fatalf("Interact error: %v", r.err)
	}
}
