package remote

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

func TestApprovals_AllowAndChangedNotification(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000071"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Approval Allow Thread"}, nil)

	var (
		approveDecision harness.Decision
		approveErr      error
	)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			dec, err := turn.Approver.Approve(ctx, harness.ApprovalRequest{
				ToolID:   "call_001",
				ToolName: "file_write",
				Summary:  "write to /workspace/main.go",
			})
			approveDecision = dec
			approveErr = err
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "写文件",
		ActionID: "00000000-0000-0000-0000-000000000701",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// 1. 订阅连接应收到 requests.changed 通知，且包含 1 个待处理工具审批
	var firstChange wire.RequestsChangedParams
	select {
	case firstChange = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first requests.changed")
	}

	if len(firstChange.Requests) != 1 {
		t.Fatalf("expected 1 pending request in changed notification, got %d", len(firstChange.Requests))
	}
	req := firstChange.Requests[0]
	if req.Kind != "tool" {
		t.Errorf("expected kind 'tool', got %s", req.Kind)
	}
	if req.Title == nil || *req.Title != "file_write" {
		t.Errorf("expected title 'file_write', got %v", req.Title)
	}
	if req.Detail == nil || *req.Detail != "write to /workspace/main.go" {
		t.Errorf("expected detail 'write to /workspace/main.go', got %v", req.Detail)
	}
	if len(req.Options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(req.Options))
	}
	if req.Options[0].ID != "allow" || req.Options[1].ID != "allow_always" || req.Options[2].ID != "deny" {
		t.Errorf("unexpected options: %+v", req.Options)
	}

	// 2. ListRequests 应能看到该请求，且线程摘要中 HasPendingRequest 为 true, Attention 为 "approval"
	listRes, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(listRes.Requests) != 1 || listRes.Requests[0].RequestID != req.RequestID {
		t.Errorf("unexpected ListRequests result: %+v", listRes)
	}

	th, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !th.HasPendingRequest {
		t.Errorf("expected thread HasPendingRequest to be true")
	}
	if th.Attention != "approval" {
		t.Errorf("expected thread Attention to be 'approval', got %q", th.Attention)
	}

	// 3. RespondRequest 选 allow 后 Approve 返回 DecisionAllow，请求被清空
	optAllow := "allow"
	respondRes, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	if respondRes.Status != wire.RequestStatusAccepted {
		t.Errorf("expected status %q, got %q", wire.RequestStatusAccepted, respondRes.Status)
	}
	if respondRes.RequestID != req.RequestID {
		t.Errorf("expected requestID %q, got %q", req.RequestID, respondRes.RequestID)
	}
	if respondRes.Kind != "tool" {
		t.Errorf("expected kind 'tool', got %q", respondRes.Kind)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	if approveDecision != harness.DecisionAllow {
		t.Errorf("expected DecisionAllow, got %v", approveDecision)
	}
	if approveErr != nil {
		t.Errorf("expected approveErr nil, got %v", approveErr)
	}

	// 请求被清空
	listAfter, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests after allow failed: %v", err)
	}
	if len(listAfter.Requests) != 0 {
		t.Errorf("expected 0 pending requests, got %d", len(listAfter.Requests))
	}

	thAfter, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread after allow failed: %v", err)
	}
	if thAfter.HasPendingRequest {
		t.Errorf("expected HasPendingRequest to be false after completion")
	}

	// 收到第二次 requests.changed 通知，列表为空
	select {
	case secondChange := <-changedCh:
		if len(secondChange.Requests) != 0 {
			t.Errorf("expected empty requests in second changed notification, got %d", len(secondChange.Requests))
		}
	case <-time.After(500 * time.Millisecond):
	}
}

func TestApprovals_Deny(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000072"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Approval Deny Thread"}, nil)

	var approveDecision harness.Decision

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			dec, _ := turn.Approver.Approve(ctx, harness.ApprovalRequest{
				ToolName: "rm",
				Summary:  "rm -rf /",
			})
			approveDecision = dec
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	backend.Subscribe(ctx, conn, threadID)

	backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "删除系统",
		ActionID: "00000000-0000-0000-0000-000000000702",
	})

	var change wire.RequestsChangedParams
	select {
	case change = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	optDeny := "deny"
	_, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     change.Requests[0].RunID,
		RequestID: change.Requests[0].RequestID,
		OptionID:  &optDeny,
	})
	if err != nil {
		t.Fatalf("RespondRequest deny failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	if approveDecision != harness.DecisionDeny {
		t.Errorf("expected DecisionDeny, got %v", approveDecision)
	}
}

func TestApprovals_AllowAlways(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000073"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Approval AllowAlways Thread"}, nil)

	var (
		dec1 harness.Decision
		dec2 harness.Decision
	)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			// 第一次调用 bash
			d1, _ := turn.Approver.Approve(ctx, harness.ApprovalRequest{
				ToolName: "bash",
				Summary:  "ls -la",
			})
			dec1 = d1

			// 第二次调用同名的 bash 工具，且命令与第一次完全相同
			d2, _ := turn.Approver.Approve(ctx, harness.ApprovalRequest{
				ToolName: "bash",
				Summary:  "ls -la",
			})
			dec2 = d2
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	backend.Subscribe(ctx, conn, threadID)

	backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行终端命令",
		ActionID: "00000000-0000-0000-0000-000000000703",
	})

	var change wire.RequestsChangedParams
	select {
	case change = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	// 第一次答复 allow_always
	optAlways := "allow_always"
	_, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     change.Requests[0].RunID,
		RequestID: change.Requests[0].RequestID,
		OptionID:  &optAlways,
	})
	if err != nil {
		t.Fatalf("RespondRequest allow_always failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	if dec1 != harness.DecisionAllow {
		t.Errorf("expected dec1 DecisionAllow, got %v", dec1)
	}
	// 第二次调用同一条命令应直接返回 DecisionAllow，不再阻塞等待答复
	if dec2 != harness.DecisionAllow {
		t.Errorf("expected dec2 DecisionAllow, got %v", dec2)
	}

	// 命令类工具按「工具名 + 完整命令」记账：同一条命令放行，不能退化成整个 bash 免审批。
	backend.mu.Lock()
	scopedAllow := backend.threads[threadID].alwaysAllow[alwaysAllowKey(harness.RequestKindCommand, harness.ApprovalRequest{ToolName: "bash", Summary: "ls -la"})]
	plainToolAllow := backend.threads[threadID].alwaysAllow["bash"]
	backend.mu.Unlock()
	if !scopedAllow {
		t.Error("expected alwaysAllow to record the bash + 'ls -la' key")
	}
	if plainToolAllow {
		t.Error("command tools must not be recorded by tool name alone")
	}
}

func TestApprovals_DuplicateRespond(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000074"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Approval Dup Thread"}, nil)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			turn.Approver.Approve(ctx, harness.ApprovalRequest{
				ToolName: "view_file",
				Summary:  "read file",
			})
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	backend.Subscribe(ctx, conn, threadID)

	backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "查看文件",
		ActionID: "00000000-0000-0000-0000-000000000704",
	})

	var change wire.RequestsChangedParams
	select {
	case change = <-changedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}

	req := change.Requests[0]
	optAllow := "allow"

	// 第一次答复正常成功
	res1, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err != nil || res1.Status != wire.RequestStatusAccepted {
		t.Fatalf("first RespondRequest failed: %v", err)
	}

	// 第二次答复同一个请求应返回 RequestAlreadyHandled 错误
	_, err = backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	})
	if err == nil {
		t.Fatalf("expected error on duplicate respond, got nil")
	}

	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *wire.RPCError, got: %T (%v)", err, err)
	}
	if rpcErr.Code != wire.ErrCodeRequestAlreadyHandled {
		t.Errorf("expected ErrCodeRequestAlreadyHandled (%d), got: %d", wire.ErrCodeRequestAlreadyHandled, rpcErr.Code)
	}

	waitThreadIdle(t, backend, ctx, threadID)
}

func TestApprovals_ContextCanceled(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000075"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Approval Cancel Thread"}, nil)

	var (
		approveDecision harness.Decision
		approveErr      error
	)

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			subCtx, cancel := context.WithCancel(ctx)
			go func() {
				time.Sleep(30 * time.Millisecond)
				cancel()
			}()

			dec, err := turn.Approver.Approve(subCtx, harness.ApprovalRequest{
				ToolName: "slow_tool",
				Summary:  "slow operation",
			})
			approveDecision = dec
			approveErr = err
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	backend.Subscribe(ctx, conn, threadID)

	backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行慢工具",
		ActionID: "00000000-0000-0000-0000-000000000705",
	})

	waitThreadIdle(t, backend, ctx, threadID)

	if approveDecision != harness.DecisionDeny {
		t.Errorf("expected DecisionDeny on canceled context, got %v", approveDecision)
	}
	if !errors.Is(approveErr, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", approveErr)
	}

	// 确认请求已被移除
	listRes, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(listRes.Requests) != 0 {
		t.Errorf("expected 0 pending requests after cancel, got %d", len(listRes.Requests))
	}
}

func TestApprovals_QuestionContextCanceledTrace(t *testing.T) {
	syncPendingAskTrace(t)
	backend, dbPath := newAskTraceBackend(t)
	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000076"
	backend.AddProject(wire.ProjectSummary{ID: "p-cancel-question", Name: "取消", Path: t.TempDir()})
	backend.AddThread(wire.ThreadSummary{ID: threadID, ProjectID: "p-cancel-question", Title: "取消提问"}, nil)
	cancelCh := make(chan context.CancelFunc, 1)
	resultCh := make(chan error, 1)
	backend.SetHarness(&mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		subCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		cancelCh <- cancel
		_, err := turn.Approver.(harness.Interactor).Interact(subCtx, harness.ApprovalRequest{
			ToolName: "AskUserQuestion", Kind: harness.RequestKindQuestion,
			Questions: []harness.Question{{ID: "q1", Prompt: "选什么？"}},
		})
		resultCh <- err
		return nil
	}}, "/workspace", harness.ModeAgent)
	conn := newMockStreamConn()
	changed := make(chan wire.RequestsChangedParams, 8)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			changed <- params.(wire.RequestsChangedParams)
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "提问", ActionID: newUUID()}); err != nil {
		t.Fatal(err)
	}
	request := waitFirstCard(t, changed)
	cancel := <-cancelCh
	cancel()
	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待取消超时")
	}
	list, err := backend.ListRequests(ctx, threadID)
	if err != nil || len(list.Requests) != 0 {
		t.Fatalf("取消后列表 = %+v，错误 = %v", list, err)
	}
	row := readAskTrace(t, dbPath, request.RequestID)
	if row.state != store.PendingAskExpired || row.resolution != askResolutionCallerCancelled {
		t.Fatalf("留痕状态 = %q/%q", row.state, row.resolution)
	}
	want := []string{"提问已取消，未收到回答。"}
	if got := infoNoticeTexts(t, backend, threadID); !reflect.DeepEqual(got, want) {
		t.Fatalf("取消提示 = %q", got)
	}
	backend.reconcileBoot(ctx, backend.store)
	if got := infoNoticeTexts(t, backend, threadID); !reflect.DeepEqual(got, want) {
		t.Fatalf("启动对账后提示 = %q", got)
	}
}

func TestApprovals_QuestionTitle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		questions []harness.Question
		want      string
	}{
		{"header", []harness.Question{{Header: "  选择方案  ", Prompt: "问题"}}, "选择方案"},
		{"prompt", []harness.Question{{Prompt: "一二三四五六七八九十一二三四五六七八九十一二三四五"}}, "一二三四五六七八九十一二三四五六七八九十一二三四…"},
		{"fallback", []harness.Question{{Header: " ", Prompt: " "}}, "提问"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, req, result := startInteract(t, newUUID(), newUUID(), harness.ApprovalRequest{
				ToolName: "AskUserQuestion", Kind: harness.RequestKindQuestion, Questions: tc.questions,
			})
			if req.Title == nil || *req.Title != tc.want {
				t.Fatalf("标题 = %v，期望 %q", req.Title, tc.want)
			}
			skip := true
			if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
				ThreadID: req.ThreadID, RunID: req.RunID, RequestID: req.RequestID, Skip: &skip,
			}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-result:
			case <-time.After(2 * time.Second):
				t.Fatal("等待交互结束超时")
			}
		})
	}
}

type interactResult struct {
	resp harness.ApprovalResponse
	err  error
}

// startInteract 启动一轮对话并通过 Interact 发起请求，返回首个待处理请求与结果通道。
func startInteract(t *testing.T, threadID, actionID string, req harness.ApprovalRequest) (*MemoryBackend, wire.RemoteRequest, <-chan interactResult) {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	backend := NewMemoryBackend(hub)
	t.Cleanup(func() { backend.Close() })

	ctx := context.Background()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Interact Thread"}, nil)

	resultCh := make(chan interactResult, 1)
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				resultCh <- interactResult{err: errors.New("approver does not implement Interactor")}
				return nil
			}
			resp, err := ia.Interact(ctx, req)
			resultCh <- interactResult{resp: resp, err: err}
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: "交互", ActionID: actionID}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case change := <-changedCh:
		if len(change.Requests) != 1 {
			t.Fatalf("expected 1 pending request, got %d", len(change.Requests))
		}
		return backend, change.Requests[0], resultCh
	case r := <-resultCh:
		t.Fatalf("Interact returned before request was published: %+v", r)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for requests.changed")
	}
	return nil, wire.RemoteRequest{}, nil
}

func waitInteract(t *testing.T, ch <-chan interactResult) interactResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Interact result")
	}
	return interactResult{}
}

func TestApprovals_QuestionAnswers(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000081"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000801", harness.ApprovalRequest{
		ToolName: "AskUserQuestion",
		Kind:     harness.RequestKindQuestion,
		Questions: []harness.Question{{
			ID:      "q1",
			Header:  "库",
			Prompt:  "用哪个库？",
			Choices: []harness.QuestionChoice{{Label: "A", Detail: "甲"}, {Label: "B"}},
		}},
	})

	if req.Kind != "question" {
		t.Errorf("expected kind question, got %q", req.Kind)
	}
	if len(req.Options) != 0 {
		t.Errorf("expected no options, got %+v", req.Options)
	}
	if len(req.Questions) != 1 || req.Questions[0].ID != "q1" || len(req.Questions[0].Choices) != 2 {
		t.Fatalf("unexpected questions: %+v", req.Questions)
	}
	if d := req.Questions[0].Choices[0].Detail; d == nil || *d != "甲" {
		t.Errorf("expected first choice detail 甲, got %v", d)
	}
	if req.Questions[0].Choices[1].Detail != nil {
		t.Errorf("expected nil detail for empty choice detail")
	}

	_, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		Answers:   json.RawMessage(`{"q1":["A"]}`),
	})
	if err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}

	r := waitInteract(t, resultCh)
	if r.err != nil {
		t.Fatalf("Interact error: %v", r.err)
	}
	if r.resp.Decision != harness.DecisionAllow || r.resp.Skipped {
		t.Errorf("unexpected response: %+v", r.resp)
	}
	if !reflect.DeepEqual(r.resp.Answers, map[string][]string{"q1": {"A"}}) {
		t.Errorf("unexpected answers: %+v", r.resp.Answers)
	}
}

func TestApprovals_QuestionSkip(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000082"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000802", harness.ApprovalRequest{
		ToolName:  "AskUserQuestion",
		Kind:      harness.RequestKindQuestion,
		Questions: []harness.Question{{ID: "q1", Prompt: "继续吗？"}},
	})

	skip := true
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		Skip:      &skip,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}

	r := waitInteract(t, resultCh)
	if r.resp.Decision != harness.DecisionDeny || !r.resp.Skipped {
		t.Errorf("expected skipped deny, got %+v", r.resp)
	}
}

func TestApprovals_PlanDenyWithFeedback(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000083"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000803", harness.ApprovalRequest{
		ToolName: "ExitPlanMode",
		Kind:     harness.RequestKindPlan,
		Plan:     "# 计划",
		Summary:  "ExitPlanMode {}",
	})

	if req.Kind != "plan" {
		t.Errorf("expected kind plan, got %q", req.Kind)
	}
	if req.Detail == nil || *req.Detail != "# 计划" {
		t.Errorf("expected plan detail, got %v", req.Detail)
	}
	if len(req.Options) != 2 || req.Options[0].ID != "allow" || req.Options[1].ID != "deny" {
		t.Errorf("unexpected options: %+v", req.Options)
	}

	optDeny := "deny"
	params, err := json.Marshal(wire.RequestsRespondParams{
		ThreadID:  threadID,
		TurnID:    req.TurnID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optDeny,
		Answers:   json.RawMessage(`{"feedback":["先补测试"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := &fakeConn{info: ClientInfo{Entry: EntryTLS}}
	conn.MarkAuthenticated("device-1", "测试设备")
	d := NewDispatcher(&fakeAuth{}, backend)
	raw := d.Handle(context.Background(), conn, []byte(`{"jsonrpc":"2.0","id":1,"method":"requests.respond","params":`+string(params)+`}`))
	var response wire.Response
	if err := json.Unmarshal(raw, &response); err != nil || response.Error != nil {
		t.Fatalf("计划驳回反馈必须能通过 JSON-RPC：%s (%v)", raw, err)
	}

	r := waitInteract(t, resultCh)
	if r.resp.Decision != harness.DecisionDeny || r.resp.Feedback != "先补测试" {
		t.Errorf("unexpected response: %+v", r.resp)
	}
}

// waitPendingRequest 轮询等待会话出现首个待处理请求。
func waitPendingRequest(t *testing.T, backend *MemoryBackend, threadID string) wire.RemoteRequest {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		list, err := backend.ListRequests(context.Background(), threadID)
		if err != nil {
			t.Fatalf("ListRequests failed: %v", err)
		}
		if len(list.Requests) > 0 {
			return list.Requests[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for pending request")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 同一轮次重复投递的计划请求（如 ExitPlanMode 被 CLI 重试）只产生一张待批准卡片，
// 且批准后会话从 plan 切回 build。
func TestApprovals_PlanRequestSingleFlight(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000086"
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	backend := NewMemoryBackend(hub)
	t.Cleanup(func() { backend.Close() })

	ctx := context.Background()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Plan Single Flight Thread", InteractionMode: "plan"}, nil)

	results := make(chan interactResult, 2)
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				return errors.New("approver does not implement Interactor")
			}
			req := harness.ApprovalRequest{
				ToolName: "ExitPlanMode",
				Kind:     harness.RequestKindPlan,
				Plan:     "# 计划",
				Summary:  "ExitPlanMode {}",
			}
			// 连续两次投递同一次 ExitPlanMode 的审批请求：两张卡片不能同时出现。
			var wg sync.WaitGroup
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					resp, err := ia.Interact(ctx, req)
					results <- interactResult{resp: resp, err: err}
				}()
			}
			wg.Wait()
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "做个计划",
		ActionID: "00000000-0000-0000-0000-000000000806",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	req := waitPendingRequest(t, backend, threadID)
	// 两个 Interact 都已进入等待后仍然只有一张卡片
	time.Sleep(50 * time.Millisecond)
	list, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 1 {
		t.Fatalf("期望只有 1 张待批准计划卡片，实际 %d", len(list.Requests))
	}

	optAllow := "allow"
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}

	for i := 0; i < 2; i++ {
		r := waitInteract(t, results)
		if r.err != nil || r.resp.Decision != harness.DecisionAllow {
			t.Fatalf("重复的计划请求应共享批准结论，实际 %+v", r)
		}
	}

	th, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.InteractionMode != "build" {
		t.Errorf("批准计划后应切回 build，实际 %q", th.InteractionMode)
	}
}

// 同一会话里同正文的计划请求（ToolID 不同、TurnID 都不带）只产生一张待批准卡片，
// 批准一次后所有等待方拿到同一结论。
func TestApprovals_PlanRequestDeduplicatedByPlanBody(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000087"
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	backend := NewMemoryBackend(hub)
	t.Cleanup(func() { backend.Close() })
	ctx := context.Background()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Plan Dedup Thread", InteractionMode: "plan"}, nil)

	results := make(chan interactResult, 2)
	plans := []string{
		"  # 计划\n\n一、先读代码\n",
		"# 计划\n\n一、先读代码",
	}
	toolIDs := []string{"toolu_plan_first", "toolu_plan_retry"}
	// 第二次投递由测试侧放行：轮次里不能自己轮询待处理数量，因为测试会立刻答复第一张卡，
	// 那次轮询可能整段错过这张卡（先看到 0，5ms 后再看已被清理），从而永远等不到重试投递。
	deliverRetry := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				return errors.New("approver does not implement Interactor")
			}
			// 第一次投递先真正落库，第二次再作为 ExitPlanMode 的超时重试到达。
			var wg sync.WaitGroup
			interact := func(i int) {
				resp, err := ia.Interact(ctx, harness.ApprovalRequest{
					ToolName: "ExitPlanMode",
					ToolID:   toolIDs[i],
					Kind:     harness.RequestKindPlan,
					Plan:     plans[i],
					Summary:  "ExitPlanMode {}",
				})
				results <- interactResult{resp: resp, err: err}
			}
			wg.Add(1)
			go func() { defer wg.Done(); interact(0) }()
			<-deliverRetry
			wg.Add(1)
			go func() { defer wg.Done(); interact(1) }()
			wg.Wait()
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "做个计划",
		ActionID: "00000000-0000-0000-0000-000000000807",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	req := waitPendingRequest(t, backend, threadID)
	if req.Detail == nil || *req.Detail != plans[0] {
		t.Fatalf("unexpected plan detail: %v", req.Detail)
	}
	if req.TurnID == nil {
		t.Fatal("计划请求应当带有轮次 ID")
	}

	// 第一张卡已落库：放行重试投递，等它挂到这张卡上之后再答复，与 SingleFlight 用例同一套时序。
	close(deliverRetry)
	time.Sleep(50 * time.Millisecond)
	list, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 1 {
		t.Fatalf("同正文的两个计划请求只应有一张待批准卡片，实际 %d", len(list.Requests))
	}

	optAllow := "allow"
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}

	for i := 0; i < 2; i++ {
		r := waitInteract(t, results)
		if r.err != nil || r.resp.Decision != harness.DecisionAllow {
			t.Fatalf("同正文计划的两个请求应共享批准结论，实际 %+v", r)
		}
	}

	remaining, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(remaining.Requests) != 0 {
		t.Fatalf("批准后不应残留待处理计划，实际 %d", len(remaining.Requests))
	}
}

// waitPendingCount 轮询到指定数量的待处理请求；不使用 testing.T，可在轮次 goroutine 内安全调用。
func waitPendingCount(backend *MemoryBackend, threadID string, want int) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		list, err := backend.ListRequests(context.Background(), threadID)
		if err != nil {
			return err
		}
		if len(list.Requests) >= want {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for pending requests")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 正文不同的两个计划请求各自一张卡片，各自答复。
func TestApprovals_PlanRequestDifferentBodyKeepsTwo(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000088"
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	backend := NewMemoryBackend(hub)
	t.Cleanup(func() { backend.Close() })
	ctx := context.Background()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Plan Distinct Thread", InteractionMode: "plan"}, nil)

	results := make(chan interactResult, 2)
	plans := []string{"# 计划 A", "# 计划 B"}
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				return errors.New("approver does not implement Interactor")
			}
			var wg sync.WaitGroup
			interact := func(plan string) {
				resp, err := ia.Interact(ctx, harness.ApprovalRequest{
					ToolName: "ExitPlanMode",
					ToolID:   "toolu_plan_" + plan,
					Kind:     harness.RequestKindPlan,
					Plan:     plan,
					Summary:  "ExitPlanMode {}",
				})
				results <- interactResult{resp: resp, err: err}
			}
			wg.Add(1)
			go func() { defer wg.Done(); interact(plans[0]) }()
			// 第二张卡片必须在第一张仍待批时到达，才能验证不同正文不会被误合并。
			if err := waitPendingCount(backend, threadID, 1); err != nil {
				return err
			}
			wg.Add(1)
			go func() { defer wg.Done(); interact(plans[1]) }()
			wg.Wait()
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "两个计划",
		ActionID: "00000000-0000-0000-0000-000000000808",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	if err := waitPendingCount(backend, threadID, 2); err != nil {
		t.Fatalf("正文不同的计划应各自成为一张卡片: %v", err)
	}
	list, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(list.Requests) != 2 {
		t.Fatalf("正文不同的计划应各自一张卡片，实际 %d", len(list.Requests))
	}

	optAllow := "allow"
	for _, pending := range list.Requests {
		if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
			ThreadID:  threadID,
			RunID:     pending.RunID,
			RequestID: pending.RequestID,
			OptionID:  &optAllow,
		}); err != nil {
			t.Fatalf("RespondRequest failed: %v", err)
		}
	}
	for i := 0; i < 2; i++ {
		r := waitInteract(t, results)
		if r.err != nil || r.resp.Decision != harness.DecisionAllow {
			t.Fatalf("两张卡片都应获批，实际 %+v", r)
		}
	}
}

// 第一次计划请求被 ctx 取消后，重试必须新建请求，而不是复用已取消的结论。
func TestApprovals_PlanRequestRetriesAfterCancel(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000089"
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)

	backend := NewMemoryBackend(hub)
	t.Cleanup(func() { backend.Close() })
	ctx := context.Background()
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Plan Retry Thread", InteractionMode: "plan"}, nil)

	const plan = "# 计划\n\n一、先读代码"
	results := make(chan interactResult, 2)
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				return errors.New("approver does not implement Interactor")
			}
			firstCtx, cancelFirst := context.WithCancel(ctx)
			defer cancelFirst()
			firstDone := make(chan struct{})
			go func() {
				defer close(firstDone)
				resp, err := ia.Interact(firstCtx, harness.ApprovalRequest{
					ToolName: "ExitPlanMode",
					ToolID:   "toolu_plan_first",
					Kind:     harness.RequestKindPlan,
					Plan:     plan,
					Summary:  "ExitPlanMode {}",
				})
				results <- interactResult{resp: resp, err: err}
			}()
			// 等首个请求真正落库再取消，避免取消早于请求入表而测不到取消分支。
			if err := waitPendingCount(backend, threadID, 1); err != nil {
				return err
			}
			cancelFirst()
			<-firstDone

			resp, err := ia.Interact(ctx, harness.ApprovalRequest{
				ToolName: "ExitPlanMode",
				ToolID:   "toolu_plan_retry",
				Kind:     harness.RequestKindPlan,
				Plan:     plan,
				Summary:  "ExitPlanMode {}",
			})
			results <- interactResult{resp: resp, err: err}
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "计划重试",
		ActionID: "00000000-0000-0000-0000-000000000809",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	first := waitInteract(t, results)
	if !errors.Is(first.err, context.Canceled) {
		t.Fatalf("首次计划请求应被取消，实际 %+v", first)
	}

	retried := waitPendingRequest(t, backend, threadID)
	optAllow := "allow"
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     retried.RunID,
		RequestID: retried.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	second := waitInteract(t, results)
	if second.err != nil || second.resp.Decision != harness.DecisionAllow {
		t.Fatalf("取消后重试应新建请求并获批，实际 %+v", second)
	}
}

func TestApprovals_ToolRejectsAnswers(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000084"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000804", harness.ApprovalRequest{
		ToolName: "WebSearch",
		Summary:  "搜索",
	})
	if req.Kind != "tool" {
		t.Errorf("expected kind tool, got %q", req.Kind)
	}

	ctx := context.Background()
	optAllow := "allow"
	_, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
		Answers:   json.RawMessage(`{"q1":["A"]}`),
	})
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected OperationRejected, got %v", err)
	}

	// 非法 answers 返回 InvalidParams
	_, err = backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		Answers:   json.RawMessage(`["A"]`),
	})
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams, got %v", err)
	}

	// 被拒后请求仍待处理，可正常答复
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("RespondRequest allow failed: %v", err)
	}
	if r := waitInteract(t, resultCh); r.resp.Decision != harness.DecisionAllow {
		t.Errorf("expected allow, got %+v", r.resp)
	}
}

func TestInferRequestKind(t *testing.T) {
	cases := map[string]harness.RequestKind{
		"Bash":         harness.RequestKindCommand,
		"exec_command": harness.RequestKindCommand,
		"Edit":         harness.RequestKindFileChange,
		"MultiEdit":    harness.RequestKindFileChange,
		"apply_patch":  harness.RequestKindFileChange,
		"WebSearch":    harness.RequestKindTool,
		"":             harness.RequestKindTool,
	}
	for name, want := range cases {
		if got := inferRequestKind(name); got != want {
			t.Errorf("inferRequestKind(%q) = %q, want %q", name, got, want)
		}
	}

	cmd := requestDetail(harness.RequestKindCommand, harness.ApprovalRequest{Input: json.RawMessage(`{"command":"git status"}`), Summary: "Bash ..."})
	if cmd != "git status" {
		t.Errorf("command detail = %q", cmd)
	}
	file := requestDetail(harness.RequestKindFileChange, harness.ApprovalRequest{Input: json.RawMessage(`{"file_path":"/a.go"}`), Summary: "Edit ..."})
	if !strings.Contains(file, `"/a.go"`) {
		t.Errorf("fileChange detail = %q", file)
	}
	fallback := requestDetail(harness.RequestKindCommand, harness.ApprovalRequest{Input: json.RawMessage(`{}`), Summary: "Bash {}"})
	if fallback != "{}" {
		t.Errorf("fallback detail = %q", fallback)
	}
}

func TestApprovalDetailPreservesInput(t *testing.T) {
	for _, kind := range []harness.RequestKind{harness.RequestKindTool, harness.RequestKindFileChange} {
		t.Run(string(kind), func(t *testing.T) {
			input, _ := json.Marshal(map[string]string{
				"file_path": "/a.go",
				"content":   strings.Repeat("正文", 200) + "末尾参数",
			})
			detail := requestDetail(kind, harness.ApprovalRequest{
				Input: input, Summary: "截断摘要",
			})
			var got map[string]string
			if err := json.Unmarshal([]byte(detail), &got); err != nil || got["file_path"] != "/a.go" || !strings.HasSuffix(got["content"], "末尾参数") {
				t.Fatalf("审批详情丢失输入参数：%q (%v)", detail, err)
			}
		})
	}
}

func TestQuestionRejectsIncompleteAnswers(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000085"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000805", harness.ApprovalRequest{
		Kind: harness.RequestKindQuestion,
		Questions: []harness.Question{{
			ID: "q1", Choices: []harness.QuestionChoice{{Label: "A"}, {Label: "B"}},
		}},
	})
	for _, raw := range []string{"", `{}`, `{"q1":null}`, `{"q1":[null]}`, `{"q1":[]}`, `{"q1":[" "]}`, `{"other":["A"]}`, `{"q1":["A","B"]}`, `{"q1":["C"]}`} {
		_, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
			ThreadID: threadID, RunID: req.RunID, RequestID: req.RequestID, Answers: json.RawMessage(raw),
		})
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
			t.Fatalf("不完整答案 %q 应被拒绝且保留请求，got %v", raw, err)
		}
	}
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID: threadID, RunID: req.RunID, RequestID: req.RequestID, Answers: json.RawMessage(`{"q1":["A"]}`),
	}); err != nil {
		t.Fatal(err)
	}
	if r := waitInteract(t, resultCh); r.resp.Decision != harness.DecisionAllow {
		t.Fatalf("合法答案应获接受：%+v", r)
	}
}

func TestPlanRejectsInvalidResponses(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000086"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000806", harness.ApprovalRequest{
		Kind: harness.RequestKindPlan, Plan: "补充测试",
	})
	always, allow := "allow_always", "allow"
	for _, params := range []wire.RequestsRespondParams{
		{},
		{OptionID: &always},
		{Answers: json.RawMessage(`{}`)},
		{Answers: json.RawMessage(`{"feedback":null}`)},
		{Answers: json.RawMessage(`{"feedback":[null]}`)},
		{Answers: json.RawMessage(`{"feedback":[" "]}`)},
		{OptionID: &allow, Answers: json.RawMessage(`{"feedback":["需要修改"]}`)},
	} {
		params.ThreadID, params.RequestID, params.RunID = threadID, req.RequestID, req.RunID
		_, err := backend.RespondRequest(context.Background(), params)
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
			t.Fatalf("非法计划答复应被拒绝且保留请求：%+v (%v)", params, err)
		}
	}
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID: threadID, RunID: req.RunID, RequestID: req.RequestID, OptionID: &allow,
	}); err != nil {
		t.Fatal(err)
	}
	if r := waitInteract(t, resultCh); r.resp.Decision != harness.DecisionAllow {
		t.Fatalf("合法批准应获接受：%+v", r)
	}
}

func TestFileApprovalShowsPathAndChanges(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000087"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000807", harness.ApprovalRequest{
		ToolName: "Edit",
		Input:    json.RawMessage(`{"file_path":"/workspace/a.go","old_string":"旧内容","new_string":"新内容"}`),
	})
	if req.Title == nil || *req.Title != "/workspace/a.go" || req.Detail == nil || !strings.Contains(*req.Detail, "新内容") {
		t.Fatalf("文件审批必须显示路径及修改参数：%+v", req)
	}
	deny := "deny"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID: threadID, RunID: req.RunID, RequestID: req.RequestID, OptionID: &deny,
	}); err != nil {
		t.Fatal(err)
	}
	waitInteract(t, resultCh)
}

func TestInferRequestKind_CommitAndPermissions(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	commitTools := []string{"commit", "git_commit", "gitCommit", "push", "git_push", "gitPush"}
	for _, tool := range commitTools {
		if got := inferRequestKind(tool); got != harness.RequestKindCommit {
			t.Errorf("inferRequestKind(%q) = %q, want %q", tool, got, harness.RequestKindCommit)
		}
	}

	permTools := []string{"request_permissions", "requestPermissions", "permissions", "item/permissions/requestApproval"}
	for _, tool := range permTools {
		if got := inferRequestKind(tool); got != harness.RequestKindPermissions {
			t.Errorf("inferRequestKind(%q) = %q, want %q", tool, got, harness.RequestKindPermissions)
		}
	}
}

func TestRequestOptions_CommitExcludesAllowAlways(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	opts := requestOptions(harness.RequestKindCommit, harness.ApprovalRequest{})
	for _, opt := range opts {
		if opt.ID == "allow_always" || opt.Role == "approveAlways" {
			t.Errorf("requestOptions(Commit) must not include allow_always option, got: %+v", opt)
		}
	}
	if len(opts) != 2 {
		t.Errorf("expected 2 options for Commit, got %d: %+v", len(opts), opts)
	}

	permOpts := requestOptions(harness.RequestKindPermissions, harness.ApprovalRequest{})
	hasAlways := false
	for _, opt := range permOpts {
		if opt.ID == "allow_always" {
			hasAlways = true
			break
		}
	}
	if !hasAlways {
		t.Errorf("requestOptions(Permissions) should include allow_always, got: %+v", permOpts)
	}
}

func TestInteract_QuestionApproval_AnswersRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	threadID := "00000000-0000-0000-0000-000000000099"
	turnID := "00000000-0000-0000-0000-000000000909"
	backend, req, resultCh := startInteract(t, threadID, turnID, harness.ApprovalRequest{
		ToolName: "ask_question",
		Kind:     harness.RequestKindQuestion,
		Questions: []harness.Question{{
			ID:      "choice_q",
			Header:  "模式选择",
			Prompt:  "请选择运行模式",
			Choices: []harness.QuestionChoice{{Label: "单测模式"}, {Label: "联调模式"}},
		}},
	})

	if req.Kind != "question" {
		t.Fatalf("expected question kind, got %s", req.Kind)
	}

	_, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		Answers:   json.RawMessage(`{"choice_q":["单测模式"]}`),
	})
	if err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}

	r := waitInteract(t, resultCh)
	if r.err != nil {
		t.Fatalf("Interact failed with error: %v", r.err)
	}
	if r.resp.Decision != harness.DecisionAllow {
		t.Errorf("expected DecisionAllow, got %v", r.resp.Decision)
	}
	expectedAnswers := map[string][]string{"choice_q": {"单测模式"}}
	if !reflect.DeepEqual(r.resp.Answers, expectedAnswers) {
		t.Errorf("expected answers %+v, got %+v", expectedAnswers, r.resp.Answers)
	}
}

// 验证只读档 Swarm helper 会话自动拒绝除用户提问外的所有审批请求，且不向前端推送
func TestApprovals_ReadOnlyHelperThreadAutoDeny(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000088"
	backend.AddThread(wire.ThreadSummary{
		ID:          threadID,
		Title:       "Read-only Bee Thread",
		RuntimeMode: "readonly",
	}, nil)
	// 设置为 helper 会话
	backend.SetThreadRelation(threadID, "00000000-0000-0000-0000-000000000001", true)

	var (
		approveDecision harness.Decision
		approveErr      error
		interactResp    harness.ApprovalResponse
		interactErr     error
	)

	doneCh := make(chan struct{})

	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			defer close(doneCh)
			// 1. 写操作 Approve 自动拒绝
			approveDecision, approveErr = turn.Approver.Approve(ctx, harness.ApprovalRequest{
				ToolName: "file_write",
				Kind:     harness.RequestKindFileChange,
			})

			// 2. 命令操作 Interact 自动拒绝，携带 reason/feedback
			if ia, ok := turn.Approver.(harness.Interactor); ok {
				interactResp, interactErr = ia.Interact(ctx, harness.ApprovalRequest{
					ToolName: "bash",
					Kind:     harness.RequestKindCommand,
				})
			}
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	var requestNotified bool
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			requestNotified = true
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	_, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行任务",
		ActionID: "00000000-0000-0000-0000-000000000089",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("turn execution timed out")
	}

	if approveErr != nil {
		t.Fatalf("Approve returned unexpected error: %v", approveErr)
	}
	if approveDecision != harness.DecisionDeny {
		t.Errorf("expected Approve DecisionDeny, got %v", approveDecision)
	}

	if interactErr != nil {
		t.Fatalf("Interact returned unexpected error: %v", interactErr)
	}
	if interactResp.Decision != harness.DecisionDeny {
		t.Errorf("expected Interact DecisionDeny, got %v", interactResp.Decision)
	}
	if interactResp.Feedback != "只读档子代理不允许写操作" {
		t.Errorf("expected Feedback '只读档子代理不允许写操作', got %q", interactResp.Feedback)
	}
	if requestNotified {
		t.Errorf("expected no requests.changed notification pushed to UI for auto-denied request")
	}
}

// TestApprovals_ReadOnlyHelperAllowsSafeReadOnlyCommand 覆盖：只读档 helper 会话执行安全只读命令时
// 直接放行且不向前端分发审批；写命令与写工具依然被拦截。
func TestApprovals_ReadOnlyHelperAllowsSafeReadOnlyCommand(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-00000000008a"
	backend.AddThread(wire.ThreadSummary{
		ID:          threadID,
		Title:       "Read-only Safe Command Thread",
		RuntimeMode: "readonly",
	}, nil)
	backend.SetThreadRelation(threadID, "00000000-0000-0000-0000-000000000001", true)

	var (
		safeResp      harness.ApprovalResponse
		safeErr       error
		writeCmdResp  harness.ApprovalResponse
		writeCmdErr   error
		writeToolResp harness.ApprovalResponse
		writeToolErr  error
	)

	doneCh := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			defer close(doneCh)
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				t.Error("turn approver should implement harness.Interactor")
				return nil
			}
			// 1. 安全只读命令直接放行：真实命令在 input.command 里，
			//    summary 是 provider 拼接出的展示串（旧实现拿它判定，导致永远判否）
			safeResp, safeErr = ia.Interact(ctx, harness.ApprovalRequest{
				ToolName: "bash",
				Kind:     harness.RequestKindCommand,
				Input:    json.RawMessage(`{"command":"ls -la && git status"}`),
				Summary:  `Bash {"command":"ls -la && git status"}`,
			})
			// 2. 写命令仍然拦截
			writeCmdResp, writeCmdErr = ia.Interact(ctx, harness.ApprovalRequest{
				ToolName: "bash",
				Kind:     harness.RequestKindCommand,
				Summary:  "rm -rf /tmp/x",
			})
			// 3. 写工具仍然拦截
			writeToolResp, writeToolErr = ia.Interact(ctx, harness.ApprovalRequest{
				ToolName: "write",
				Kind:     harness.RequestKindFileChange,
				Summary:  "write /workspace/main.go",
			})
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	var requestNotified bool
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			requestNotified = true
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行任务",
		ActionID: "00000000-0000-0000-0000-00000000008b",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("turn execution timed out")
	}

	if safeErr != nil {
		t.Fatalf("安全只读命令不应返回错误: %v", safeErr)
	}
	if safeResp.Decision != harness.DecisionAllow {
		t.Errorf("安全只读命令应放行，得到 %v", safeResp.Decision)
	}

	if writeCmdErr != nil {
		t.Fatalf("写命令 Interact 不应返回错误: %v", writeCmdErr)
	}
	if writeCmdResp.Decision != harness.DecisionDeny {
		t.Errorf("写命令应被拒绝，得到 %v", writeCmdResp.Decision)
	}
	if writeCmdResp.Feedback != "只读档子代理不允许写操作" {
		t.Errorf("写命令应带只读档拒绝说明，得到 %q", writeCmdResp.Feedback)
	}

	if writeToolErr != nil {
		t.Fatalf("写工具 Interact 不应返回错误: %v", writeToolErr)
	}
	if writeToolResp.Decision != harness.DecisionDeny {
		t.Errorf("写工具应被拒绝，得到 %v", writeToolResp.Decision)
	}

	if requestNotified {
		t.Error("只读档拦截/放行都不应向前端推送 requests.changed")
	}
}

// TestApprovals_HelperThreadAutoApprovesUnattended 覆盖无人值守的 Swarm bee：
// 非敏感审批（命令、文件改动、计划）自动放行，敏感操作自动拒绝，提问得到固定答复，
// 全程不产生前端待处理请求。
func TestApprovals_HelperThreadAutoApprovesUnattended(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-00000000008c"
	backend.AddThread(wire.ThreadSummary{
		ID:          threadID,
		Title:       "Auto Bee Thread",
		RuntimeMode: "auto",
	}, nil)
	backend.SetThreadRelation(threadID, "00000000-0000-0000-0000-000000000001", true)

	var (
		commandResp   harness.ApprovalResponse
		commandErr    error
		fileResp      harness.ApprovalResponse
		fileErr       error
		planResp      harness.ApprovalResponse
		planErr       error
		sensitiveResp harness.ApprovalResponse
		sensitiveErr  error
		questionResp  harness.ApprovalResponse
		questionErr   error
		pendingSeen   bool
	)

	doneCh := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(runCtx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			defer close(doneCh)
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				t.Error("turn approver should implement harness.Interactor")
				return nil
			}
			checkPending := func() {
				res, err := backend.ListRequests(runCtx, threadID)
				if err != nil {
					t.Errorf("ListRequests: %v", err)
					return
				}
				if len(res.Requests) > 0 {
					pendingSeen = true
				}
			}

			commandResp, commandErr = ia.Interact(runCtx, harness.ApprovalRequest{
				ToolID:   "call_bee_auto_command",
				ToolName: "bash",
				Kind:     harness.RequestKindCommand,
				Input:    json.RawMessage(`{"command":"go test ./..."}`),
				Summary:  `Bash {"command":"go test ./..."}`,
			})
			checkPending()

			fileResp, fileErr = ia.Interact(runCtx, harness.ApprovalRequest{
				ToolID:   "call_bee_auto_file",
				ToolName: "write",
				Kind:     harness.RequestKindFileChange,
				Input:    json.RawMessage(`{"path":"/workspace/main.go"}`),
				Summary:  "/workspace/main.go",
			})
			checkPending()

			planResp, planErr = ia.Interact(runCtx, harness.ApprovalRequest{
				ToolID:   "call_bee_auto_plan",
				ToolName: "ExitPlanMode",
				Kind:     harness.RequestKindPlan,
				Plan:     "先改 A，再改 B",
			})
			checkPending()

			sensitiveResp, sensitiveErr = ia.Interact(runCtx, harness.ApprovalRequest{
				ToolID:    "call_bee_auto_sensitive",
				ToolName:  "bash",
				Kind:      harness.RequestKindCommand,
				Sensitive: "credential",
				Input:     json.RawMessage(`{"command":"cat ~/.ssh/id_rsa"}`),
			})
			checkPending()

			questionResp, questionErr = ia.Interact(runCtx, harness.ApprovalRequest{
				ToolID:   "call_bee_auto_question",
				ToolName: "AskUserQuestion",
				Kind:     harness.RequestKindQuestion,
				Questions: []harness.Question{{
					ID:     "q1",
					Prompt: "用哪个方案？",
					Choices: []harness.QuestionChoice{
						{Label: "A"},
						{Label: "B"},
					},
				}},
			})
			checkPending()
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAuto)

	conn := newMockStreamConn()
	var requestNotified bool
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			requestNotified = true
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行任务",
		ActionID: "00000000-0000-0000-0000-00000000008d",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("turn execution timed out")
	}

	for name, err := range map[string]error{
		"command":   commandErr,
		"file":      fileErr,
		"plan":      planErr,
		"sensitive": sensitiveErr,
		"question":  questionErr,
	} {
		if err != nil {
			t.Fatalf("%s Interact 不应返回错误: %v", name, err)
		}
	}
	if commandResp.Decision != harness.DecisionAllow {
		t.Errorf("子代理命令应自动放行，得到 %v", commandResp.Decision)
	}
	if fileResp.Decision != harness.DecisionAllow {
		t.Errorf("子代理文件改动应自动放行，得到 %v", fileResp.Decision)
	}
	if planResp.Decision != harness.DecisionAllow {
		t.Errorf("子代理计划应自动放行，得到 %v", planResp.Decision)
	}
	if sensitiveResp.Decision != harness.DecisionDeny {
		t.Errorf("子代理敏感操作应被拒绝，得到 %v", sensitiveResp.Decision)
	}
	if !strings.Contains(sensitiveResp.Feedback, "敏感操作") {
		t.Errorf("敏感操作拒绝应带说明，得到 %q", sensitiveResp.Feedback)
	}
	if questionResp.Decision != harness.DecisionAllow {
		t.Errorf("子代理提问应直接答复，得到 %v", questionResp.Decision)
	}
	wantAnswer := "子代理无人值守，没有人回答提问，请自行判断后继续"
	if answers := questionResp.Answers["q1"]; len(answers) != 1 || answers[0] != wantAnswer {
		t.Errorf("子代理提问答复应为 %q，得到 %v", wantAnswer, questionResp.Answers)
	}
	if pendingSeen {
		t.Error("子代理审批不应产生前端待处理请求")
	}
	if requestNotified {
		t.Error("子代理审批不应向前端推送 requests.changed")
	}
}

// TestApprovals_ChildThreadSensitiveFallback 覆盖未填 Sensitive 的子会话请求：
// 只有 parentThreadID、isHelper=false 时也按输入补算，凭证读取自动拒绝，普通只读命令放行。
func TestApprovals_ChildThreadSensitiveFallback(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-00000000008f"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Child Thread", RuntimeMode: "auto"}, nil)
	backend.SetThreadRelation(threadID, "00000000-0000-0000-0000-000000000001", false)

	var (
		credResp harness.ApprovalResponse
		credErr  error
		safeResp harness.ApprovalResponse
		safeErr  error
	)
	doneCh := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(runCtx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			defer close(doneCh)
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				t.Error("turn approver should implement harness.Interactor")
				return nil
			}
			credResp, credErr = ia.Interact(runCtx, harness.ApprovalRequest{
				ToolID:   "call_child_credential",
				ToolName: "Bash",
				Kind:     harness.RequestKindCommand,
				Input:    json.RawMessage(`{"command":"cat ~/.ssh/id_rsa"}`),
			})
			safeResp, safeErr = ia.Interact(runCtx, harness.ApprovalRequest{
				ToolID:   "call_child_safe",
				ToolName: "Bash",
				Kind:     harness.RequestKindCommand,
				Input:    json.RawMessage(`{"command":"ls"}`),
			})
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAuto)

	conn := newMockStreamConn()
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行任务",
		ActionID: "00000000-0000-0000-0000-0000000008f1",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("turn execution timed out")
	}

	if credErr != nil {
		t.Fatalf("凭证读取 Interact 不应返回错误: %v", credErr)
	}
	if credResp.Decision != harness.DecisionDeny {
		t.Errorf("子会话读取凭证应被自动拒绝，得到 %v", credResp.Decision)
	}
	if !strings.Contains(credResp.Feedback, "敏感操作") {
		t.Errorf("凭证拒绝应带敏感说明，得到 %q", credResp.Feedback)
	}
	if safeErr != nil {
		t.Fatalf("普通命令 Interact 不应返回错误: %v", safeErr)
	}
	if safeResp.Decision != harness.DecisionAllow {
		t.Errorf("子会话普通只读命令应自动放行，得到 %v", safeResp.Decision)
	}
}

// TestApprovals_ChildQuestionAutoForwarded 覆盖 AutoForwardQuestionSeconds>0：
// 子代理提问不返回固定答复，而是进入正常的挂起流程等待自动转交主线程。
func TestApprovals_ChildQuestionAutoForwarded(t *testing.T) {
	dataDir := t.TempDir()
	if err := config.Update(dataDir, func(cfg *config.Config) error {
		cfg.Orchestration.AutoForwardQuestionSeconds = 30
		return nil
	}); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	backend.SetDataDir(dataDir)
	defer backend.Close()

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-000000000090"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "Child Question", RuntimeMode: "auto"}, nil)
	backend.SetThreadRelation(threadID, "00000000-0000-0000-0000-000000000001", true)

	var (
		questionResp harness.ApprovalResponse
		questionErr  error
		pendingSeen  bool
	)
	doneCh := make(chan struct{})
	mock := &mockHarness{
		runFunc: func(runCtx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			defer close(doneCh)
			ia, ok := turn.Approver.(harness.Interactor)
			if !ok {
				t.Error("turn approver should implement harness.Interactor")
				return nil
			}
			approveCtx, cancel := context.WithTimeout(runCtx, 200*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				questionResp, questionErr = ia.Interact(approveCtx, harness.ApprovalRequest{
					ToolID:   "call_child_question_forward",
					ToolName: "AskUserQuestion",
					Kind:     harness.RequestKindQuestion,
					Questions: []harness.Question{{
						ID:     "q1",
						Prompt: "用哪个方案？",
					}},
				})
			}()
			for i := 0; i < 40; i++ {
				if res, err := backend.ListRequests(runCtx, threadID); err == nil && len(res.Requests) > 0 {
					pendingSeen = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			<-done
			return nil
		},
	}
	backend.SetHarness(mock, t.TempDir(), harness.ModeAuto)

	conn := newMockStreamConn()
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行任务",
		ActionID: "00000000-0000-0000-0000-0000000000901",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("turn execution timed out")
	}

	if !pendingSeen {
		t.Fatal("自动转交开启时子代理提问应挂起为待处理请求")
	}
	if questionErr == nil {
		t.Fatal("自动转交开启时不应立即返回固定答复")
	}
	wantFixed := "子代理无人值守，没有人回答提问，请自行判断后继续"
	if answers := questionResp.Answers["q1"]; len(answers) == 1 && answers[0] == wantFixed {
		t.Fatalf("不应返回固定答复: %v", answers)
	}
}

// TestApprovals_OrdinaryThreadStillPromptsInAutoMode 覆盖普通（非子代理）线程：
// auto 档下的命令审批仍然分发到前端并挂起等待答复。
func TestApprovals_OrdinaryThreadStillPromptsInAutoMode(t *testing.T) {
	threadID := "00000000-0000-0000-0000-00000000008e"
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	probe := probeApproval(t, backend, threadID, harness.ModeAuto, harness.ApprovalRequest{
		ToolID:   "call_ordinary_auto_command",
		ToolName: "Bash",
		Input:    json.RawMessage(`{"command":"go test ./..."}`),
		Summary:  `Bash {"command":"go test ./..."}`,
		Kind:     harness.RequestKindCommand,
	}, 150*time.Millisecond)

	if probe.decision == harness.DecisionAllow {
		t.Fatal("普通线程的命令审批不应自动放行")
	}
	if !probe.pendingSeen {
		t.Fatal("普通线程的命令审批应仍然分发到前端")
	}
}

// TestIsSafeReadOnlyCommand 覆盖安全只读命令判定的放行与拒绝边界。
func TestIsSafeReadOnlyCommand(t *testing.T) {
	allowed := []string{
		"ls -la",
		"ls",
		"/bin/ls -l",
		"cat README.md",
		"head -n 20 main.go",
		"tail -f app.log",
		"grep -rn TODO internal",
		"rg -n foo",
		"find . -name '*.go' -type f",
		"wc -l internal/remote/approvals.go",
		"pwd",
		"echo hello",
		"which go",
		"stat main.go",
		"file main.go",
		"du -sh internal",
		"tree -L 2 internal",
		"sort files.txt",
		"uniq -c files.txt",
		"diff a.txt b.txt",
		"git status",
		"git diff HEAD~1",
		"git log --oneline -5",
		"git show HEAD",
		"git branch -a",
		"git branch --show-current",
		"git blame main.go",
		"git log --stat --no-color",
		"git diff --numstat HEAD~2",
		"git status && git diff",
		"cat a.txt | grep foo | wc -l",
		"pwd; ls -la",
		// 丢弃或合并输出的重定向不写文件，与 cd 一样应放行。
		"ls /tmp 2>/dev/null",
		"ls /tmp 2> /dev/null",
		"grep -rn foo internal 2>&1 | head -20",
		"cat /tmp/a >/dev/null",
		"find /tmp -name x &>/dev/null",
		"cd /tmp && ls",
		"cd internal && grep -rn foo .",
	}
	for _, cmd := range allowed {
		if !isSafeReadOnlyCommand(cmd) {
			t.Errorf("应判定为安全只读命令: %q", cmd)
		}
	}

	blocked := []string{
		"",
		"   ",
		"rm -rf /tmp/x",
		"mv a b",
		"cp a b",
		"touch new.txt",
		"mkdir build",
		"chmod +x run.sh",
		"curl https://example.com",
		"bash -c 'rm -rf /tmp/x'",
		"ls > out.txt",
		"echo hi >> log.txt",
		"cat file < input",
		"ls `whoami`",
		"ls $(whoami)",
		"ls &",
		"ls && rm -rf /tmp/x",
		"git commit -m x",
		"git push origin main",
		"git branch -D main",
		"git branch --delete old",
		"git branch new-branch",
		"git checkout main",
		// 白名单子命令下的写盘/改配置选项：全部子命令统一拦截，不能只校验 git branch
		"git diff --output=out.patch",
		"git log --output=out.txt",
		"git show --output=/tmp/x",
		"git log -c",
		"git log -c core.pager=cat",
		"git status --work-tree=/tmp/x",
		"git diff --git-dir=/tmp/.git",
		"git log --exec-path=/tmp/bin",
		"git show --config-env=core.pager=PAGER",
		"find . -delete",
		"find . -exec rm {} ;",
		"find . -name '*.log' -fprint out.txt",
		"sort -o out.txt files.txt",
		"sort --output=out.txt files.txt",
		"sudo ls",
		"python script.py",
		// 写文件的重定向、非 /dev/null 的重定向、前缀相似的 /dev/nullx 都不能被摘掉。
		"ls > /tmp/out",
		"cat a 2>/tmp/err",
		"echo x >/dev/nullx",
		"cat a >> /dev/null_file",
		"cd /tmp && rm -rf x",
		"cat a 2>&1 > out",
	}
	for _, cmd := range blocked {
		if isSafeReadOnlyCommand(cmd) {
			t.Errorf("不应判定为安全只读命令: %q", cmd)
		}
	}
}

// TestAlwaysAllowKey 覆盖命令前缀和工具名记账，取不到命令文本时不记账。
func TestAlwaysAllowKey(t *testing.T) {
	ls := harness.ApprovalRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"ls -la"}`), Summary: `Bash {"command":"ls -la"}`}
	rm := harness.ApprovalRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"rm -rf /tmp/x"}`), Summary: `Bash {"command":"rm -rf /tmp/x"}`}

	lsKey := alwaysAllowKey(harness.RequestKindCommand, ls)
	if lsKey == "" || lsKey == ls.ToolName {
		t.Fatalf("命令类工具应按「工具名 + 命令」记账，得到 %q", lsKey)
	}
	if lsKey == alwaysAllowKey(harness.RequestKindCommand, rm) {
		t.Error("不同命令不应共用同一个会话级放行键")
	}
	// 只给出 Summary（部分 provider 只把命令写进摘要）时仍按命令区分。
	if got := alwaysAllowKey(harness.RequestKindCommand, harness.ApprovalRequest{ToolName: "bash", Summary: "pwd"}); got != "bash\x00prefix\x00pwd" {
		t.Errorf("Summary 回退的记账键不符: %q", got)
	}
	// 取不到命令文本时不记账，避免退化成整类 shell 免审批。
	if got := alwaysAllowKey(harness.RequestKindCommand, harness.ApprovalRequest{ToolName: "bash"}); got != "" {
		t.Errorf("缺少命令文本时不应记账，得到 %q", got)
	}
	// 其它工具保持按工具名记账。
	if got := alwaysAllowKey(harness.RequestKindFileChange, harness.ApprovalRequest{ToolName: "write"}); got != "write" {
		t.Errorf("非命令类工具应按工具名记账，得到 %q", got)
	}
}

// newAlwaysAllowProjectBackend 建一个挂了真实项目目录的会话后端，供「始终允许」持久化用例使用。
func newAlwaysAllowProjectBackend(t *testing.T, dataDir, projectDir, threadID string) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)
	backend.SetDataDir(dataDir)
	projectID := "project-" + threadID
	backend.AddProject(wire.ProjectSummary{ID: projectID, Name: "项目", Path: projectDir})
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "始终允许", ProjectID: projectID}, nil)
	return backend
}

// alwaysAllowBashRequest 返回同一条 bash 命令的审批请求，命令文本写进 Input，与真实 provider 一致。
func alwaysAllowBashRequest(command string) harness.ApprovalRequest {
	return harness.ApprovalRequest{
		ToolID:   "call_bash",
		ToolName: "bash",
		Summary:  "bash",
		Input:    json.RawMessage(`{"command":"` + command + `"}`),
	}
}

// startAlwaysAllowTurn 订阅会话并发起一轮运行，返回推送 requests.changed 的通道。
func startAlwaysAllowTurn(t *testing.T, backend *MemoryBackend, threadID, actionID string) <-chan wire.RequestsChangedParams {
	t.Helper()
	ctx := context.Background()
	conn := newMockStreamConn()
	changedCh := make(chan wire.RequestsChangedParams, 10)
	conn.notifyFn = func(method string, params any) error {
		if method == "requests.changed" {
			if p, ok := params.(wire.RequestsChangedParams); ok {
				changedCh <- p
			}
		}
		return nil
	}
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "执行命令",
		ActionID: actionID,
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	return changedCh
}

// waitPendingApproval 等下一次 requests.changed 里出现待处理请求，并返回第一条。
func waitPendingApproval(t *testing.T, changedCh <-chan wire.RequestsChangedParams) wire.RemoteRequest {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case change := <-changedCh:
			if len(change.Requests) > 0 {
				return change.Requests[0]
			}
		case <-timeout:
			t.Fatal("timed out waiting for pending request")
		}
	}
}

// TestApprovals_AllowAlwaysPersistsPerProject 覆盖：选「始终允许」后按项目目录落盘，
// 「重启」后同项目的新会话直接放行，不同命令不受影响。
func TestApprovals_AllowAlwaysPersistsPerProject(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	ctx := context.Background()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })
	project := normalizeAlwaysAllowProject(projectDir)
	key := alwaysAllowKey(harness.RequestKindCommand, alwaysAllowBashRequest("ls -la"))

	threadID := "00000000-0000-0000-0000-0000000000d1"
	backend := newAlwaysAllowProjectBackend(t, dataDir, projectDir, threadID)
	var (
		dec1 harness.Decision
		dec2 harness.Decision
	)
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			dec1, _ = turn.Approver.Approve(ctx, alwaysAllowBashRequest("ls -la"))
			dec2, _ = turn.Approver.Approve(ctx, alwaysAllowBashRequest("ls -la"))
			return nil
		},
	}
	backend.SetHarness(mock, projectDir, harness.ModeAgent)

	changedCh := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000d01")
	req := waitPendingApproval(t, changedCh)
	optAlways := "allow_always"
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAlways,
	}); err != nil {
		t.Fatalf("RespondRequest allow_always failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	if dec1 != harness.DecisionAllow || dec2 != harness.DecisionAllow {
		t.Fatalf("同一条命令应两次放行, got %v / %v", dec1, dec2)
	}
	if got := newAlwaysAllowStore(alwaysAllowStorePath(dataDir)).List(project); !slices.Contains(got, key) {
		t.Fatalf("「始终允许」应落盘到项目 %s, got %v", project, got)
	}
	if other := alwaysAllowKey(harness.RequestKindCommand, alwaysAllowBashRequest("rm -rf /tmp/x")); slices.Contains(newAlwaysAllowStore(alwaysAllowStorePath(dataDir)).List(project), other) {
		t.Fatal("不同的命令不应随「始终允许」一起落盘")
	}

	// 模拟进程重启：丢弃缓存实例，用同一数据目录与项目新建后端与新会话，同一条命令不再弹审批。
	forgetAlwaysAllowStore(dataDir)
	restartThreadID := "00000000-0000-0000-0000-0000000000d2"
	restarted := newAlwaysAllowProjectBackend(t, dataDir, projectDir, restartThreadID)
	var restartDec harness.Decision
	restarted.SetHarness(&mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			restartDec, _ = turn.Approver.Approve(ctx, alwaysAllowBashRequest("ls -la"))
			return nil
		},
	}, projectDir, harness.ModeAgent)
	if _, err := restarted.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: restartThreadID,
		Text:     "重启后再执行同一条命令",
		ActionID: "00000000-0000-0000-0000-000000000d02",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, restarted, ctx, restartThreadID)

	if restartDec != harness.DecisionAllow {
		t.Fatalf("重启后同项目的同一条命令应自动放行, got %v", restartDec)
	}
	if res, err := restarted.ListRequests(ctx, restartThreadID); err != nil || len(res.Requests) != 0 {
		t.Fatalf("自动放行不应产生待处理请求, err=%v requests=%d", err, len(res.Requests))
	}
}

// TestApprovals_AllowAlwaysOtherProjectStillPrompts 覆盖：持久化记录只对同一项目目录生效，别的项目仍弹审批。
func TestApprovals_AllowAlwaysOtherProjectStillPrompts(t *testing.T) {
	dataDir := t.TempDir()
	projectA := t.TempDir()
	projectB := t.TempDir()
	ctx := context.Background()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })

	key := alwaysAllowKey(harness.RequestKindCommand, alwaysAllowBashRequest("ls -la"))
	if err := alwaysAllowStoreFor(dataDir).Add(projectA, key); err != nil {
		t.Fatalf("预置项目 A 的始终允许失败: %v", err)
	}

	threadID := "00000000-0000-0000-0000-0000000000d3"
	backend := newAlwaysAllowProjectBackend(t, dataDir, projectB, threadID)
	var dec harness.Decision
	backend.SetHarness(&mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			dec, _ = turn.Approver.Approve(ctx, alwaysAllowBashRequest("ls -la"))
			return nil
		},
	}, projectB, harness.ModeAgent)

	changedCh := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000d03")
	req := waitPendingApproval(t, changedCh)
	optDeny := "deny"
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optDeny,
	}); err != nil {
		t.Fatalf("RespondRequest deny failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	if dec != harness.DecisionDeny {
		t.Fatalf("其他项目不应命中持久化的始终允许, got %v", dec)
	}
}

// TestApprovals_RevokeAlwaysAllowed 覆盖撤销：存储与线程内存里的同键都被清掉，之后同项目的新会话重新弹审批。
func TestApprovals_RevokeAlwaysAllowed(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	ctx := context.Background()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })
	key := alwaysAllowKey(harness.RequestKindCommand, alwaysAllowBashRequest("ls -la"))

	threadID := "00000000-0000-0000-0000-0000000000d4"
	backend := newAlwaysAllowProjectBackend(t, dataDir, projectDir, threadID)
	backend.SetHarness(&mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			turn.Approver.Approve(ctx, alwaysAllowBashRequest("ls -la"))
			return nil
		},
	}, projectDir, harness.ModeAgent)

	changedCh := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000d04")
	req := waitPendingApproval(t, changedCh)
	optAlways := "allow_always"
	if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  threadID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAlways,
	}); err != nil {
		t.Fatalf("RespondRequest allow_always failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)

	if got := backend.ListAlwaysAllowed(projectDir); !slices.Contains(got, key) {
		t.Fatalf("撤销前应能在持久化名单里看到该键, got %v", got)
	}
	backend.mu.Lock()
	inMemory := backend.threads[threadID].alwaysAllow[key]
	backend.mu.Unlock()
	if !inMemory {
		t.Fatal("撤销前线程内存里应有该键")
	}

	if err := backend.RevokeAlwaysAllowed(projectDir, key); err != nil {
		t.Fatalf("RevokeAlwaysAllowed failed: %v", err)
	}
	if got := backend.ListAlwaysAllowed(projectDir); len(got) != 0 {
		t.Fatalf("撤销后持久化名单应为空, got %v", got)
	}
	if got := newAlwaysAllowStore(alwaysAllowStorePath(dataDir)).List(projectDir); len(got) != 0 {
		t.Fatalf("撤销应落盘, got %v", got)
	}
	backend.mu.Lock()
	inMemory = backend.threads[threadID].alwaysAllow[key]
	backend.mu.Unlock()
	if inMemory {
		t.Fatal("撤销应同时清掉线程内存里的同键")
	}

	// 撤销后同项目的新会话重新弹审批
	nextThreadID := "00000000-0000-0000-0000-0000000000d5"
	next := newAlwaysAllowProjectBackend(t, dataDir, projectDir, nextThreadID)
	var dec harness.Decision
	next.SetHarness(&mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			dec, _ = turn.Approver.Approve(ctx, alwaysAllowBashRequest("ls -la"))
			return nil
		},
	}, projectDir, harness.ModeAgent)
	nextChangedCh := startAlwaysAllowTurn(t, next, nextThreadID, "00000000-0000-0000-0000-000000000d05")
	nextReq := waitPendingApproval(t, nextChangedCh)
	optDeny := "deny"
	if _, err := next.RespondRequest(ctx, wire.RequestsRespondParams{
		ThreadID:  nextThreadID,
		RunID:     nextReq.RunID,
		RequestID: nextReq.RequestID,
		OptionID:  &optDeny,
	}); err != nil {
		t.Fatalf("RespondRequest deny failed: %v", err)
	}
	waitThreadIdle(t, next, ctx, nextThreadID)

	if dec != harness.DecisionDeny {
		t.Fatalf("撤销后应重新弹审批, got %v", dec)
	}
}

// TestApprovals_PersistedAlwaysAllowSkipsInteractive 覆盖：计划与提问类请求即使命中了持久化的同键，
// 也必须逐次由用户答复，不会被自动放行。
func TestApprovals_PersistedAlwaysAllowSkipsInteractive(t *testing.T) {
	dataDir := t.TempDir()
	projectDir := t.TempDir()
	ctx := context.Background()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })
	for _, key := range []string{"ExitPlanMode", "AskUserQuestion"} {
		if err := alwaysAllowStoreFor(dataDir).Add(projectDir, key); err != nil {
			t.Fatalf("预置交互类记账键 %s 失败: %v", key, err)
		}
	}

	t.Run("计划请求仍逐次批准", func(t *testing.T) {
		threadID := "00000000-0000-0000-0000-0000000000d6"
		backend := newAlwaysAllowProjectBackend(t, dataDir, projectDir, threadID)
		resultCh := make(chan interactResult, 1)
		backend.SetHarness(&mockHarness{
			runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
				ia, ok := turn.Approver.(harness.Interactor)
				if !ok {
					resultCh <- interactResult{err: errors.New("approver does not implement Interactor")}
					return nil
				}
				resp, err := ia.Interact(ctx, harness.ApprovalRequest{
					ToolName: "ExitPlanMode",
					Kind:     harness.RequestKindPlan,
					Plan:     "# 计划",
					Summary:  "ExitPlanMode {}",
				})
				resultCh <- interactResult{resp: resp, err: err}
				return nil
			},
		}, projectDir, harness.ModeAgent)

		changedCh := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000d06")
		req := waitPendingApproval(t, changedCh)
		optAllow := "allow"
		if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
			ThreadID:  threadID,
			RunID:     req.RunID,
			RequestID: req.RequestID,
			OptionID:  &optAllow,
		}); err != nil {
			t.Fatalf("RespondRequest allow failed: %v", err)
		}
		r := waitInteract(t, resultCh)
		waitThreadIdle(t, backend, ctx, threadID)
		if r.err != nil || r.resp.Decision != harness.DecisionAllow {
			t.Fatalf("计划请求应经用户批准后放行, resp=%+v err=%v", r.resp, r.err)
		}
	})

	t.Run("提问请求仍逐次答复", func(t *testing.T) {
		threadID := "00000000-0000-0000-0000-0000000000d7"
		backend := newAlwaysAllowProjectBackend(t, dataDir, projectDir, threadID)
		resultCh := make(chan interactResult, 1)
		backend.SetHarness(&mockHarness{
			runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
				ia, ok := turn.Approver.(harness.Interactor)
				if !ok {
					resultCh <- interactResult{err: errors.New("approver does not implement Interactor")}
					return nil
				}
				resp, err := ia.Interact(ctx, harness.ApprovalRequest{
					ToolName:  "AskUserQuestion",
					Kind:      harness.RequestKindQuestion,
					Questions: []harness.Question{{ID: "q1", Prompt: "继续吗？"}},
				})
				resultCh <- interactResult{resp: resp, err: err}
				return nil
			},
		}, projectDir, harness.ModeAgent)

		changedCh := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000d07")
		req := waitPendingApproval(t, changedCh)
		skip := true
		if _, err := backend.RespondRequest(ctx, wire.RequestsRespondParams{
			ThreadID:  threadID,
			RunID:     req.RunID,
			RequestID: req.RequestID,
			Skip:      &skip,
		}); err != nil {
			t.Fatalf("RespondRequest skip failed: %v", err)
		}
		r := waitInteract(t, resultCh)
		waitThreadIdle(t, backend, ctx, threadID)
		if r.err != nil || !r.resp.Skipped {
			t.Fatalf("提问请求应经用户跳过, resp=%+v err=%v", r.resp, r.err)
		}
	})
}

// TestShouldPersistAlwaysAllow 覆盖持久化判定的边界：交互类与权限申请绝不持久化，取消与无键也不写。
func TestShouldPersistAlwaysAllow(t *testing.T) {
	cases := []struct {
		name        string
		kind        harness.RequestKind
		canceled    bool
		interactive bool
		optionID    string
		allowKey    string
		want        bool
	}{
		{"命令类放行落盘", harness.RequestKindCommand, false, false, "allow_always", "bash\x00ls -la", true},
		{"文件变更落盘", harness.RequestKindFileChange, false, false, "allow_always", "write", true},
		{"普通工具落盘", harness.RequestKindTool, false, false, "allow_always", "view_file", true},
		{"提交类落盘", harness.RequestKindCommit, false, false, "allow_always", "git_commit", true},
		{"计划不落盘", harness.RequestKindPlan, false, true, "allow_always", "ExitPlanMode", false},
		{"提问不落盘", harness.RequestKindQuestion, false, true, "allow_always", "AskUserQuestion", false},
		{"权限申请不落盘", harness.RequestKindPermissions, false, false, "allow_always", "request_permissions", false},
		{"只按本次允许不落盘", harness.RequestKindCommand, false, false, "allow", "bash\x00ls -la", false},
		{"取消不落盘", harness.RequestKindCommand, true, false, "allow_always", "bash\x00ls -la", false},
		{"无记账键不落盘", harness.RequestKindCommand, false, false, "allow_always", "", false},
	}
	for _, c := range cases {
		if got := shouldPersistAlwaysAllow(c.kind, c.canceled, c.interactive, c.optionID, c.allowKey); got != c.want {
			t.Errorf("%s: shouldPersistAlwaysAllow = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestStewardMainThreadAsksForReadOnlyCommand 覆盖总管的会话不再有只读命令豁免：
// 总管改为跟随全局默认权限档后，安全只读命令与普通会话一致，照样弹审批。
func TestStewardMainThreadAsksForReadOnlyCommand(t *testing.T) {
	backend := newStewardTestBackend(t, stewardTestThreadID)
	probe := probeApproval(t, backend, stewardTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
		ToolID:   "call_steward_read_cmd_001",
		ToolName: "Bash",
		Input:    json.RawMessage(`{"command":"ls"}`),
		Summary:  `Bash {"command":"ls"}`,
		Kind:     harness.RequestKindCommand,
	}, 150*time.Millisecond)

	if probe.decision == harness.DecisionAllow {
		t.Fatal("总管的会话的安全只读命令不应再直接放行")
	}
	if !probe.pendingSeen {
		t.Fatal("总管的会话的安全只读命令应进入审批")
	}
}

// TestStewardMainThreadAsksForReadTool 覆盖总管的会话不再有只读文件工具豁免：
// Read 这类只读文件工具与普通会话一致，照样弹审批。
func TestStewardMainThreadAsksForReadTool(t *testing.T) {
	backend := newStewardTestBackend(t, stewardTestThreadID)
	probe := probeApproval(t, backend, stewardTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
		ToolID:   "call_steward_read_001",
		ToolName: "Read",
		Input:    json.RawMessage(`{"file_path":"/workspace/main.go"}`),
		Summary:  `Read {"file_path":"/workspace/main.go"}`,
		Kind:     harness.RequestKindTool,
	}, 150*time.Millisecond)

	if probe.decision == harness.DecisionAllow {
		t.Fatal("总管的会话的只读文件工具不应再直接放行")
	}
	if !probe.pendingSeen {
		t.Fatal("总管的会话的只读文件工具应进入审批")
	}
}

func TestCommandAllowPrefixes(t *testing.T) {
	allowed := map[string]bool{"bash\x00prefix\x00go test": true}
	for _, tc := range []struct {
		command string
		want    bool
	}{
		{"go test ./internal/...", true},
		{"cd x && go test ./...", true},
		{"go build ./...", false},
		{"go test ./... && rm -rf y", false},
		{"go test $(echo x)", false},
	} {
		req := harness.ApprovalRequest{ToolName: "bash", Summary: tc.command}
		if got := commandPrefixesAllowed(harness.RequestKindCommand, req, func(key string) bool { return allowed[key] }); got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.command, got, tc.want)
		}
	}
	for _, tc := range []struct{ command, prefix string }{
		{"go test ./...", "go test"}, {"make build", "make build"}, {"ls -la", "ls"},
		{"bash -c x", ""}, {"python x.py", ""}, {"X=y go test", ""},
		{"go test >out", ""}, {"go test 2>&1", "go test"}, {"go test >/dev/null", "go test"},
	} {
		if got := commandAllowPrefix(tc.command); got != tc.prefix {
			t.Errorf("%q: got %q, want %q", tc.command, got, tc.prefix)
		}
		if tc.prefix == "" {
			req := harness.ApprovalRequest{ToolName: "bash", Summary: tc.command}
			if got := alwaysAllowKey(harness.RequestKindCommand, req); got != "bash\x00"+tc.command {
				t.Errorf("完整键 = %q", got)
			}
		}
	}
}

func TestCommandPrefixAndLegacyIntegration(t *testing.T) {
	dataDir, projectDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })
	threadID := "00000000-0000-0000-0000-0000000000e2"
	backend := newAlwaysAllowProjectBackend(t, dataDir, projectDir, threadID)
	legacy := alwaysAllowBashRequest("python x.py")
	if err := alwaysAllowStoreFor(dataDir).Add(projectDir, fullAllowKey(harness.RequestKindCommand, legacy)); err != nil {
		t.Fatal(err)
	}
	commands := []string{"go test ./...", "go test ./internal/...", "cd x && go test ./...", "python x.py", "go build", "go test ./... && rm -rf y", "go test $(echo x)"}
	backend.SetHarness(&mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		for _, command := range commands {
			_, _ = turn.Approver.Approve(ctx, alwaysAllowBashRequest(command))
		}
		return nil
	}}, projectDir, harness.ModeAgent)
	changed := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000e02")
	for i := 0; i < 4; i++ {
		pending := waitPendingApproval(t, changed)
		option := "deny"
		if i == 0 {
			option = "allow_always"
		}
		if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{ThreadID: threadID, RunID: pending.RunID, RequestID: pending.RequestID, OptionID: &option}); err != nil {
			t.Fatal(err)
		}
	}
	waitThreadIdle(t, backend, context.Background(), threadID)
}

func TestSensitiveApprovalAlwaysPrompts(t *testing.T) {
	dataDir, projectDir := t.TempDir(), t.TempDir()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })
	threadID := "00000000-0000-0000-0000-0000000000e1"
	backend := newAlwaysAllowProjectBackend(t, dataDir, projectDir, threadID)
	req := alwaysAllowBashRequest("go test ./...")
	req.Sensitive = "credential"
	if err := alwaysAllowStoreFor(dataDir).Add(projectDir, fullAllowKey(harness.RequestKindCommand, req)); err != nil {
		t.Fatal(err)
	}
	backend.SetHarness(&mockHarness{runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
		_, _ = turn.Approver.Approve(ctx, req)
		_, _ = turn.Approver.Approve(ctx, req)
		return nil
	}}, projectDir, harness.ModeAgent)
	changed := startAlwaysAllowTurn(t, backend, threadID, "00000000-0000-0000-0000-000000000e01")
	for i := 0; i < 2; i++ {
		pending := waitPendingApproval(t, changed)
		if pending.Reason == nil || *pending.Reason != "credential" || len(pending.Options) != 2 || pending.Options[0].ID != "allow" || pending.Options[1].ID != "deny" {
			t.Fatalf("敏感请求错误: %+v", pending)
		}
		option := "allow"
		if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{ThreadID: threadID, RunID: pending.RunID, RequestID: pending.RequestID, OptionID: &option}); err != nil {
			t.Fatal(err)
		}
	}
	waitThreadIdle(t, backend, context.Background(), threadID)
	if keys := backend.ListAlwaysAllowed(projectDir); len(keys) != 1 || keys[0] != fullAllowKey(harness.RequestKindCommand, req) {
		t.Fatalf("敏感请求不应记账: %v", keys)
	}
}

func TestProjectAllowOptions(t *testing.T) {
	options := requestOptions(harness.RequestKindCommand, harness.ApprovalRequest{Summary: "go test ./..."})
	if options[1].Title != "在此项目始终允许 go test" {
		t.Fatal(options)
	}
	options = requestOptions(harness.RequestKindPermissions, harness.ApprovalRequest{})
	if options[1].Title != "本会话始终允许" {
		t.Fatal(options)
	}
}

// TestStewardMainThreadStillAsksForWriteTool 覆盖总管的会话的写工具：Edit 与 Write 仍走审批。
func TestStewardMainThreadStillAsksForWriteTool(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		kind     harness.RequestKind
		input    string
	}{
		{name: "Edit", toolName: "Edit", kind: harness.RequestKindFileChange, input: `{"file_path":"/workspace/main.go","old_string":"a","new_string":"b"}`},
		{name: "Write", toolName: "Write", kind: harness.RequestKindTool, input: `{"file_path":"/workspace/main.go","content":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newStewardTestBackend(t, stewardTestThreadID)
			probe := probeApproval(t, backend, stewardTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
				ToolID:   "call_steward_write_" + tc.name,
				ToolName: tc.toolName,
				Input:    json.RawMessage(tc.input),
				Summary:  tc.toolName + " /workspace/main.go",
				Kind:     tc.kind,
			}, 150*time.Millisecond)

			if probe.decision == harness.DecisionAllow {
				t.Fatalf("%s 不应直接放行", tc.name)
			}
			if !probe.pendingSeen {
				t.Fatalf("%s 应仍然走审批", tc.name)
			}
		})
	}
}

// TestTopicThreadNotExemptFromReadCommand 覆盖话题会话：话题会话只拿内置 MCP 工具豁免，
// 只读命令仍按原流程走审批。
func TestTopicThreadNotExemptFromReadCommand(t *testing.T) {
	backend := newStewardTestBackend(t, stewardTestThreadID)
	setTopicThreads(t, backend, topicTestThreadID)
	probe := probeApproval(t, backend, topicTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
		ToolID:   "call_topic_read_cmd_001",
		ToolName: "Bash",
		Input:    json.RawMessage(`{"command":"git log --oneline -5"}`),
		Summary:  `Bash {"command":"git log --oneline -5"}`,
		Kind:     harness.RequestKindCommand,
	}, 150*time.Millisecond)

	if probe.decision == harness.DecisionAllow {
		t.Fatal("话题会话的只读命令不应直接放行")
	}
	if !probe.pendingSeen {
		t.Fatal("话题会话的只读命令应仍然走审批")
	}
}

// TestOrdinaryThreadNotExemptFromReadCommand 覆盖普通会话：总管的会话的只读豁免不外溢。
func TestOrdinaryThreadNotExemptFromReadCommand(t *testing.T) {
	backend := newStewardTestBackend(t, stewardTestThreadID)
	probe := probeApproval(t, backend, regularTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
		ToolID:   "call_regular_read_cmd_001",
		ToolName: "Bash",
		Input:    json.RawMessage(`{"command":"git log --oneline -5"}`),
		Summary:  `Bash {"command":"git log --oneline -5"}`,
		Kind:     harness.RequestKindCommand,
	}, 150*time.Millisecond)

	if probe.decision == harness.DecisionAllow {
		t.Fatal("普通会话的只读命令不应直接放行")
	}
	if !probe.pendingSeen {
		t.Fatal("普通会话的只读命令应仍然走审批")
	}
}
