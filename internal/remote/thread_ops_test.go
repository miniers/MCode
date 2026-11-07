package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/remote/wire"
)

type threadOpsRecordConn struct {
	fakeConn
	mu            sync.Mutex
	notifications []string
	queueSnaps    []wire.ThreadsQueueSnapshot
}

func (c *threadOpsRecordConn) Notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = append(c.notifications, method)
	if snap, ok := params.(wire.ThreadsQueueSnapshot); ok {
		c.queueSnaps = append(c.queueSnaps, snap)
	}
	return nil
}

type fakeThreadOps struct {
	*MemoryBackend
	mu                   sync.Mutex
	calls                map[string]int
	hook                 func(wire.ThreadsQueueSnapshot)
	projectDir           string
	resolvedProvider     string
	moveResult           wire.ThreadsMoveResult
	branchResult         wire.ThreadsBranchResult
	forwardResult        wire.ThreadsContinueResult
	settleResult         wire.ThreadsSettleActionResult
	queueListResult      wire.ThreadsQueueSnapshot
	queueUpdateResult    wire.ThreadsQueueUpdateResult
	queueRemoveResult    wire.ThreadsQueueRemoveResult
	queueSendNowResult   wire.ThreadsQueueSendNowResult
	queueInterruptResult wire.ThreadsQueueInterruptAndSendResult
}

func (f *fakeThreadOps) MoveThread(ctx context.Context, threadID, direction string) (wire.ThreadsMoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["MoveThread"]++
	return f.moveResult, nil
}

func (f *fakeThreadOps) BranchThread(ctx context.Context, p wire.ThreadsBranchParams) (wire.ThreadsBranchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["BranchThread"]++
	return f.branchResult, nil
}

func (f *fakeThreadOps) ForwardThread(ctx context.Context, p wire.ThreadsContinueParams) (wire.ThreadsContinueResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["ForwardThread"]++
	return f.forwardResult, nil
}

func (f *fakeThreadOps) CommandScope(ctx context.Context, threadID, projectID, provider string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["CommandScope"]++
	dir := f.projectDir
	if dir == "" {
		dir = "/test/project"
	}
	prov := f.resolvedProvider
	if prov == "" {
		if provider != "" {
			prov = provider
		} else {
			prov = "claude"
		}
	}
	return dir, prov, nil
}

func (f *fakeThreadOps) SettleAction(ctx context.Context, p wire.ThreadsSettleActionParams) (wire.ThreadsSettleActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["SettleAction"]++
	return f.settleResult, nil
}

func (f *fakeThreadOps) QueueList(ctx context.Context, threadID string) (wire.ThreadsQueueSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["QueueList"]++
	return f.queueListResult, nil
}

func (f *fakeThreadOps) QueueUpdate(ctx context.Context, p wire.ThreadsQueueUpdateParams) (wire.ThreadsQueueUpdateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["QueueUpdate"]++
	return f.queueUpdateResult, nil
}

func (f *fakeThreadOps) QueueRemove(ctx context.Context, p wire.ThreadsQueueRemoveParams) (wire.ThreadsQueueRemoveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["QueueRemove"]++
	return f.queueRemoveResult, nil
}

func (f *fakeThreadOps) QueueSendNow(ctx context.Context, p wire.ThreadsQueueSendNowParams) (wire.ThreadsQueueSendNowResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["QueueSendNow"]++
	return f.queueSendNowResult, nil
}

func (f *fakeThreadOps) QueueInterruptAndSend(ctx context.Context, p wire.ThreadsQueueInterruptAndSendParams) (wire.ThreadsQueueInterruptAndSendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["QueueInterruptAndSend"]++
	return f.queueInterruptResult, nil
}

func (f *fakeThreadOps) SetQueueChangedHook(fn func(wire.ThreadsQueueSnapshot)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["SetQueueChangedHook"]++
	f.hook = fn
}

func newTestFakeThreadOps() (*fakeThreadOps, *StreamHub) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	mb := NewMemoryBackend(hub)
	fake := &fakeThreadOps{
		MemoryBackend: mb,
		calls:         make(map[string]int),
		moveResult: wire.ThreadsMoveResult{
			Moved:  true,
			Thread: wire.ThreadSummary{ID: "00000000-0000-0000-0000-000000000001"},
		},
		branchResult: wire.ThreadsBranchResult{
			ThreadID: "00000000-0000-0000-0000-000000000002",
		},
		forwardResult: wire.ThreadsContinueResult{
			ThreadID: "00000000-0000-0000-0000-000000000003",
		},
		settleResult: wire.ThreadsSettleActionResult{
			ActionStatusResult: wire.ActionStatusResult{
				ActionID: "00000000-0000-0000-0000-000000000004",
				State:    wire.ActionStateAccepted,
			},
			SafeToRecoverDraft: true,
		},
		queueListResult: wire.ThreadsQueueSnapshot{
			ThreadID:  "00000000-0000-0000-0000-000000000001",
			Revision:  1,
			FollowUps: []wire.FollowUp{},
		},
		queueUpdateResult: wire.ThreadsQueueUpdateResult{
			ActionID:  "00000000-0000-0000-0000-000000000005",
			ThreadID:  "00000000-0000-0000-0000-000000000001",
			Revision:  2,
			FollowUps: []wire.FollowUp{},
		},
		queueRemoveResult: wire.ThreadsQueueRemoveResult{
			ActionID:           "00000000-0000-0000-0000-000000000006",
			ThreadID:           "00000000-0000-0000-0000-000000000001",
			Revision:           2,
			RemovedFollowUpIDs: []string{"00000000-0000-0000-0000-000000000007"},
		},
		queueSendNowResult: wire.ThreadsQueueSendNowResult{
			ActionID:  "00000000-0000-0000-0000-000000000008",
			ThreadID:  "00000000-0000-0000-0000-000000000001",
			Revision:  2,
			FollowUps: []wire.FollowUp{},
		},
		queueInterruptResult: wire.ThreadsQueueInterruptAndSendResult{
			ActionID: "00000000-0000-0000-0000-000000000009",
			ThreadID: "00000000-0000-0000-0000-000000000001",
			State:    wire.ThreadsQueueInterruptStateStopping,
			Queue: wire.ThreadsQueueSnapshot{
				ThreadID:  "00000000-0000-0000-0000-000000000001",
				Revision:  1,
				FollowUps: []wire.FollowUp{},
			},
		},
	}
	return fake, hub
}

func execThreadOpsRPC(t *testing.T, d *Dispatcher, conn Conn, reqJSON string) wire.Response {
	t.Helper()
	resRaw := d.Handle(context.Background(), conn, []byte(reqJSON))
	var resp wire.Response
	if err := json.Unmarshal(resRaw, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v, raw: %s", err, string(resRaw))
	}
	return resp
}

// 1. 测试全部 10 个方法的正常路径与参数错误路径
func TestThreadOps_SuccessAndInvalidParams(t *testing.T) {
	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	validUUID1 := "00000000-0000-0000-0000-000000000001"
	validUUID2 := "00000000-0000-0000-0000-000000000002"
	validUUID3 := "00000000-0000-0000-0000-000000000003"

	// 1. threads.move
	// 成功
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.move","params":{"threadID":"%s","direction":"up"},"id":1}`, validUUID1)
	resp := execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.move failed: %v", resp.Error.Message)
	}
	// 参数错误：direction 非法
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.move","params":{"threadID":"%s","direction":"left"},"id":2}`, validUUID1)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for invalid direction, got: %+v", resp.Error)
	}

	// 2. threads.branch
	// 成功
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.branch","params":{"threadID":"%s","messageID":"%s","actionID":"%s"},"id":3}`, validUUID1, validUUID2, validUUID3)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.branch failed: %v", resp.Error.Message)
	}
	// 参数错误：messageID 非法 UUID
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.branch","params":{"threadID":"%s","messageID":"invalid","actionID":"%s"},"id":4}`, validUUID1, validUUID3)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for invalid messageID, got: %+v", resp.Error)
	}

	// 3. threads.continue
	// 成功
	fwdActionID := newUUID()
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.continue","params":{"threadID":"%s","actionID":"%s"},"id":5}`, validUUID1, fwdActionID)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.continue failed: %v", resp.Error.Message)
	}
	// 参数错误：threadID 非法 UUID
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.continue","params":{"threadID":"invalid","actionID":"%s"},"id":6}`, fwdActionID)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for invalid threadID, got: %+v", resp.Error)
	}

	// 4. threads.commands
	// 成功：使用 threadID
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s"},"id":7}`, validUUID1)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.commands failed: %v", resp.Error.Message)
	}
	// 成功：使用 projectID + provider
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"projectID":"%s","provider":"claude"},"id":8}`, validUUID1)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.commands with project+provider failed: %v", resp.Error.Message)
	}
	// 参数错误：同时提供 threadID 与 projectID
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s","projectID":"%s","provider":"claude"},"id":9}`, validUUID1, validUUID2)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams when both threadID and projectID given, got: %+v", resp.Error)
	}
	// 参数错误：只提供 projectID 没有 provider
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"projectID":"%s"},"id":10}`, validUUID1)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams when provider missing, got: %+v", resp.Error)
	}

	// 5. threads.settleAction
	// 成功
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.settleAction","params":{"actionID":"%s","actionJournalID":"j1","threadID":"%s"},"id":11}`, validUUID1, validUUID2)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.settleAction failed: %v", resp.Error.Message)
	}
	// 参数错误：缺少 projectID 和 threadID
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.settleAction","params":{"actionID":"%s","actionJournalID":"j1"},"id":12}`, validUUID1)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams when projectID and threadID both missing, got: %+v", resp.Error)
	}
	// 参数错误：actionJournalID 为空
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.settleAction","params":{"actionID":"%s","actionJournalID":"","threadID":"%s"},"id":13}`, validUUID1, validUUID2)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams when actionJournalID empty, got: %+v", resp.Error)
	}

	// 6. threads.queue.list
	// 成功
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.list","params":{"threadID":"%s"},"id":14}`, validUUID1)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.queue.list failed: %v", resp.Error.Message)
	}
	// 参数错误：threadID 非法
	req = `{"jsonrpc":"2.0","method":"threads.queue.list","params":{"threadID":"invalid"},"id":15}`
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for queue.list invalid threadID, got: %+v", resp.Error)
	}

	// 7. threads.queue.update
	// 成功
	qActionID1 := newUUID()
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.update","params":{"threadID":"%s","followUpID":"%s","actionID":"%s","text":"updated"},"id":16}`, validUUID1, validUUID2, qActionID1)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.queue.update failed: %v", resp.Error.Message)
	}
	// 参数错误：followUpID 非法
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.update","params":{"threadID":"%s","followUpID":"bad","actionID":"%s"},"id":17}`, validUUID1, newUUID())
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for queue.update invalid followUpID, got: %+v", resp.Error)
	}
	// 错误：带附件返回 OperationRejected
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.update","params":{"threadID":"%s","followUpID":"%s","actionID":"%s","attachments":["%s"]},"id":18}`, validUUID1, validUUID2, newUUID(), validUUID3)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected for queue.update with attachments, got: %+v", resp.Error)
	}

	// 8. threads.queue.remove
	// 成功
	qActionID2 := newUUID()
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.remove","params":{"threadID":"%s","followUpID":"%s","actionID":"%s"},"id":19}`, validUUID1, validUUID2, qActionID2)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.queue.remove failed: %v", resp.Error.Message)
	}
	// 参数错误：actionID 非法
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.remove","params":{"threadID":"%s","followUpID":"%s","actionID":"bad"},"id":20}`, validUUID1, validUUID2)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for queue.remove invalid actionID, got: %+v", resp.Error)
	}

	// 9. threads.queue.sendNow
	// 成功
	qActionID3 := newUUID()
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.sendNow","params":{"threadID":"%s","followUpID":"%s","actionID":"%s"},"id":21}`, validUUID1, validUUID2, qActionID3)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.queue.sendNow failed: %v", resp.Error.Message)
	}
	// 参数错误：threadID 非法
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.sendNow","params":{"threadID":"bad","followUpID":"%s","actionID":"%s"},"id":22}`, validUUID2, newUUID())
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for queue.sendNow invalid threadID, got: %+v", resp.Error)
	}

	// 10. threads.queue.interruptAndSend
	// 成功
	intActionID := newUUID()
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.interruptAndSend","params":{"threadID":"%s","actionID":"%s","text":"stop and send"},"id":23}`, validUUID1, intActionID)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.queue.interruptAndSend failed: %v", resp.Error.Message)
	}
	// 参数错误：text 为空且无附件
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.interruptAndSend","params":{"threadID":"%s","actionID":"%s","text":"   "},"id":24}`, validUUID1, newUUID())
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for queue.interruptAndSend with empty text, got: %+v", resp.Error)
	}
	// 错误：带附件返回 OperationRejected
	req = fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.interruptAndSend","params":{"threadID":"%s","actionID":"%s","text":"msg","attachments":["%s"]},"id":25}`, validUUID1, newUUID(), validUUID3)
	resp = execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected for queue.interruptAndSend with attachments, got: %+v", resp.Error)
	}
}

// 2. 幂等性测试：针对 branch, forward, queue.update, queue.remove, queue.sendNow, queue.interruptAndSend
func TestThreadOps_Idempotency(t *testing.T) {
	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	threadID := newUUID()
	targetID := newUUID()

	methods := []struct {
		name       string
		method     string
		backendKey string
		makeReq    func(actionID string, variant bool) string
	}{
		{
			name:       "threads.branch",
			method:     "threads.branch",
			backendKey: "BranchThread",
			makeReq: func(actID string, variant bool) string {
				msgID := targetID
				if variant {
					msgID = newUUID()
				}
				return fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.branch","params":{"threadID":"%s","messageID":"%s","actionID":"%s"},"id":100}`, threadID, msgID, actID)
			},
		},
		{
			name:       "threads.continue",
			method:     "threads.continue",
			backendKey: "ForwardThread",
			makeReq: func(actID string, variant bool) string {
				th := threadID
				if variant {
					th = newUUID()
				}
				return fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.continue","params":{"threadID":"%s","actionID":"%s"},"id":101}`, th, actID)
			},
		},
		{
			name:       "threads.queue.update",
			method:     "threads.queue.update",
			backendKey: "QueueUpdate",
			makeReq: func(actID string, variant bool) string {
				txt := "text1"
				if variant {
					txt = "text2"
				}
				return fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.update","params":{"threadID":"%s","followUpID":"%s","actionID":"%s","text":"%s"},"id":102}`, threadID, targetID, actID, txt)
			},
		},
		{
			name:       "threads.queue.remove",
			method:     "threads.queue.remove",
			backendKey: "QueueRemove",
			makeReq: func(actID string, variant bool) string {
				followUp := targetID
				if variant {
					followUp = newUUID()
				}
				return fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.remove","params":{"threadID":"%s","followUpID":"%s","actionID":"%s"},"id":103}`, threadID, followUp, actID)
			},
		},
		{
			name:       "threads.queue.sendNow",
			method:     "threads.queue.sendNow",
			backendKey: "QueueSendNow",
			makeReq: func(actID string, variant bool) string {
				followUp := targetID
				if variant {
					followUp = newUUID()
				}
				return fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.sendNow","params":{"threadID":"%s","followUpID":"%s","actionID":"%s"},"id":104}`, threadID, followUp, actID)
			},
		},
		{
			name:       "threads.queue.interruptAndSend",
			method:     "threads.queue.interruptAndSend",
			backendKey: "QueueInterruptAndSend",
			makeReq: func(actID string, variant bool) string {
				txt := "original text"
				if variant {
					txt = "variant text"
				}
				return fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.queue.interruptAndSend","params":{"threadID":"%s","actionID":"%s","text":"%s"},"id":105}`, threadID, actID, txt)
			},
		},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			actID := newUUID()
			req1 := m.makeReq(actID, false)

			// 首次调用
			callsBefore := fake.calls[m.backendKey]
			resp1 := execThreadOpsRPC(t, d, conn, req1)
			if resp1.Error != nil {
				t.Fatalf("%s 首次调用失败: %v", m.name, resp1.Error.Message)
			}
			if fake.calls[m.backendKey] != callsBefore+1 {
				t.Fatalf("%s 后端未被调用，预期调用数 %d，实际 %d", m.name, callsBefore+1, fake.calls[m.backendKey])
			}

			// 二次调用（相同参数）：应命中缓存，后端调用数不增加，结果一致
			resp2 := execThreadOpsRPC(t, d, conn, req1)
			if resp2.Error != nil {
				t.Fatalf("%s 幂等重试失败: %v", m.name, resp2.Error.Message)
			}
			if fake.calls[m.backendKey] != callsBefore+1 {
				t.Fatalf("%s 幂等调用重复调用了后端，实际调用数: %d", m.name, fake.calls[m.backendKey])
			}

			r1, _ := json.Marshal(resp1.Result)
			r2, _ := json.Marshal(resp2.Result)
			if string(r1) != string(r2) {
				t.Fatalf("%s 幂等重试结果不匹配: %s vs %s", m.name, string(r1), string(r2))
			}

			// 复用同一 actionID 但使用不同参数：应返回 ErrCodeOperationRejected
			reqDiff := m.makeReq(actID, true)
			respDiff := execThreadOpsRPC(t, d, conn, reqDiff)
			if respDiff.Error == nil || respDiff.Error.Code != wire.ErrCodeOperationRejected {
				t.Fatalf("%s 复用 actionID 但参数不同未返回 ErrCodeOperationRejected，got: %+v", m.name, respDiff.Error)
			}
			if !strings.Contains(respDiff.Error.Message, "actionID reused with different payload") {
				t.Fatalf("%s 错误信息不符合要求: %s", m.name, respDiff.Error.Message)
			}
		})
	}
}

// 3. 验证 threads.sendMessage 的 whenRunning 校验
func TestThreadOps_SendMessageWhenRunning(t *testing.T) {
	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	thID := newUUID()
	actID := newUUID()

	// whenRunning 非法值 "bogus"
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.sendMessage","params":{"threadID":"%s","actionID":"%s","text":"hi","whenRunning":"bogus"},"id":200}`, thID, actID)
	resp := execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for bogus whenRunning, got: %+v", resp.Error)
	}
	expectedMsg := `Invalid params: 'whenRunning' must be "queue"`
	if resp.Error.Message != expectedMsg {
		t.Fatalf("expected error message %q, got %q", expectedMsg, resp.Error.Message)
	}
}

// 4. 验证 hook 触发后已认证连接收到 threads.queue.changed 通知
func TestThreadOps_QueueNotification(t *testing.T) {
	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	if fake.hook == nil {
		t.Fatalf("SetQueueChangedHook 未在 NewDispatcher 中被调用设置")
	}

	thID := newUUID()
	testSnap := wire.ThreadsQueueSnapshot{
		ThreadID: thID,
		Revision: 5,
		FollowUps: []wire.FollowUp{
			{
				ID:        newUUID(),
				Text:      "queued message",
				CreatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}

	fake.hook(testSnap)

	conn.mu.Lock()
	defer conn.mu.Unlock()
	found := false
	for i, m := range conn.notifications {
		if m == "threads.queue.changed" {
			found = true
			if conn.queueSnaps[i].ThreadID != thID || conn.queueSnaps[i].Revision != 5 {
				t.Fatalf("received queue notification mismatch: %+v", conn.queueSnaps[i])
			}
			break
		}
	}
	if !found {
		t.Fatalf("expected connection to receive 'threads.queue.changed' notification")
	}
}

// 5. backend 未实现接口时返回 ErrCodeOperationRejected ("thread operations unavailable")
func TestThreadOps_BackendNotImplemented(t *testing.T) {
	auth := &fakeAuth{}
	backend := &fakeBackend{}
	d := NewDispatcher(auth, backend)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.move","params":{"threadID":"%s","direction":"up"},"id":300}`, newUUID())
	resp := execThreadOpsRPC(t, d, conn, req)
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Fatalf("expected ErrCodeOperationRejected when backend does not implement ThreadOpsBackend, got: %+v", resp.Error)
	}
	if !strings.Contains(resp.Error.Message, "thread operations unavailable") {
		t.Fatalf("expected error message to contain 'thread operations unavailable', got: %s", resp.Error.Message)
	}
}

// 6. threads.commands 过滤、排序、截断、内置命令及错误处理验证
func TestThreadOps_CommandsAssembly(t *testing.T) {
	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	origScanSkills := scanSkills
	defer func() { scanSkills = origScanSkills }()

	longDesc := strings.Repeat("A", 400)

	mockSkills := []agentconf.Skill{
		{
			Agent:       "claude",
			Scope:       "global",
			Name:        "beta-skill",
			Description: "Global Beta Skill",
			Path:        "/abs/path/to/beta/SKILL.md",
		},
		{
			Agent:       "claude",
			Scope:       "project",
			Name:        "beta-skill",
			Description: "Project Beta Skill Override",
			Path:        "/abs/path/to/project/beta/SKILL.md",
		},
		{
			Agent:       "claude",
			Scope:       "global",
			Name:        "alpha-skill",
			Description: longDesc,
			Path:        "/abs/path/to/alpha/SKILL.md",
		},
		{
			Agent:       "codex",
			Scope:       "global",
			Name:        "codex-skill",
			Description: "Codex only skill",
			Path:        "/abs/path/to/codex/SKILL.md",
		},
		{
			Agent:       "claude",
			Scope:       "project",
			Name:        "plan", // 与内置命令同名，内置命令必须优先
			Description: "Fake plan skill",
			Path:        "/abs/path/to/plan/SKILL.md",
		},
	}

	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return mockSkills, nil
	}

	fake.resolvedProvider = "claude"
	req := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s"},"id":400}`, newUUID())
	resp := execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("threads.commands failed: %v", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal commands result: %v", err)
	}
	var res wire.ThreadsCommandsResult
	if err := json.Unmarshal(data, &res); err != nil {
		t.Fatalf("unmarshal commands result: %v", err)
	}

	// 1) 绝对不能包含任何文件路径
	rawResultStr := string(data)
	if strings.Contains(rawResultStr, "/abs/path") || strings.Contains(rawResultStr, "SKILL.md") {
		t.Fatalf("result must not contain file paths: %s", rawResultStr)
	}

	// 2) claude provider 下内置命令包含 mode, plan, compact 等（来自 builtinSlashCatalog），且排在最前
	if len(res.Commands) < 9 {
		t.Fatalf("expected at least 9 builtin commands, got %d", len(res.Commands))
	}
	if res.Commands[0].Name != "mode" || !res.Commands[0].IsBuiltIn {
		t.Fatalf("first command must be builtin mode, got: %+v", res.Commands[0])
	}
	if res.Commands[1].Name != "plan" || !res.Commands[1].IsBuiltIn {
		t.Fatalf("second command must be builtin plan, got: %+v", res.Commands[1])
	}
	if res.Commands[2].Name != "compact" || !res.Commands[2].IsBuiltIn {
		t.Fatalf("third command must be builtin compact, got: %+v", res.Commands[2])
	}

	// 3) codex 的 skill 被过滤掉，只保留 claude 的 alpha-skill 与 beta-skill
	// beta-skill 保留了 project 作用域版本
	var names []string
	for _, c := range res.Commands {
		names = append(names, c.Name)
		if c.Name == "alpha-skill" {
			if len([]rune(c.Detail)) > 300 {
				t.Fatalf("alpha-skill detail exceeds 300 runes: %d", len([]rune(c.Detail)))
			}
		}
		if c.Name == "beta-skill" {
			if c.Detail != "Project Beta Skill Override" {
				t.Fatalf("expected project scope to override global for beta-skill, got detail: %s", c.Detail)
			}
		}
		if c.Name == "codex-skill" {
			t.Fatalf("codex-skill should be filtered out for claude provider")
		}
	}

	// 4) 命令顺序：内置命令 -> Provider 专有命令 -> 技能（按名称排序）
	expectedNames := []string{
		"mode", "plan", "compact", "clear", "model", "effort", "fast", "pair", "export",
		"review", "pr_comments", "bug", "init", "doctor",
		"alpha-skill", "beta-skill",
	}
	if len(names) != len(expectedNames) {
		t.Fatalf("expected command names %v, got %v", expectedNames, names)
	}
	for i, en := range expectedNames {
		if names[i] != en {
			t.Fatalf("command at index %d expected %s, got %s", i, en, names[i])
		}
	}

	// 5) scanSkills 失败时，返回基础内置与 provider 命令且 Error 字段有值
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return nil, errors.New("disk read failed")
	}
	respErr := execThreadOpsRPC(t, d, conn, req)
	if respErr.Error != nil {
		t.Fatalf("expected RPC success with result error message, got rpc error: %+v", respErr.Error)
	}
	dataErr, _ := json.Marshal(respErr.Result)
	var resErr wire.ThreadsCommandsResult
	_ = json.Unmarshal(dataErr, &resErr)
	if resErr.Error == nil || *resErr.Error != "命令列表加载失败，已显示内置命令" {
		t.Fatalf("expected error message '命令列表加载失败，已显示内置命令', got: %v", resErr.Error)
	}
	if len(resErr.Commands) != 14 || resErr.Commands[0].Name != "mode" || resErr.Commands[1].Name != "plan" {
		t.Fatalf("expected base commands returned when scanSkills fails, got: %+v", resErr.Commands)
	}
}

// 内置命令目录与 Provider 专有命令过滤及去重验证
func TestThreadOps_SlashCommands_CatalogAndProviders(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	origScanSkills := scanSkills
	defer func() { scanSkills = origScanSkills }()

	mockSkills := []agentconf.Skill{
		{Agent: "claude", Scope: "project", Name: "review", Description: "Should be overridden by provider review"},
		{Agent: "claude", Scope: "global", Name: "mode", Description: "Should be overridden by builtin mode"},
		{Agent: "claude", Scope: "global", Name: "extra-claude", Description: "Extra skill"},
		{Agent: "agy", Scope: "global", Name: "goal", Description: "Should be overridden by agy goal"},
		{Agent: "agy", Scope: "global", Name: "custom-agy-skill", Description: "Agy custom skill"},
	}
	scanSkills = func(opts agentconf.Options) ([]agentconf.Skill, error) {
		return mockSkills, nil
	}

	// 1. 测试 claude：结果必须包含 mode, model, compact, review, doctor，且 compact 属于 builtin，review 属于 command
	fake.resolvedProvider = "claude"
	reqClaude := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s"},"id":501}`, newUUID())
	respClaude := execThreadOpsRPC(t, d, conn, reqClaude)
	if respClaude.Error != nil {
		t.Fatalf("threads.commands for claude failed: %v", respClaude.Error.Message)
	}
	dataClaude, _ := json.Marshal(respClaude.Result)
	var resClaude wire.ThreadsCommandsResult
	_ = json.Unmarshal(dataClaude, &resClaude)

	claudeMap := make(map[string]wire.RemoteCommand)
	var claudeNames []string
	for _, cmd := range resClaude.Commands {
		if _, dup := claudeMap[cmd.Name]; dup {
			t.Fatalf("duplicate command in claude result: %s", cmd.Name)
		}
		claudeMap[cmd.Name] = cmd
		claudeNames = append(claudeNames, cmd.Name)
	}

	for _, reqName := range []string{"mode", "model", "compact", "review", "doctor"} {
		if _, ok := claudeMap[reqName]; !ok {
			t.Fatalf("expected claude commands to contain %q, but got %v", reqName, claudeNames)
		}
	}
	if !claudeMap["compact"].IsBuiltIn || claudeMap["compact"].Kind != "builtin" {
		t.Fatalf("compact must be builtin, got: %+v", claudeMap["compact"])
	}
	if claudeMap["review"].IsBuiltIn || claudeMap["review"].Kind != "command" {
		t.Fatalf("review must be provider command with kind=command, got: %+v", claudeMap["review"])
	}
	// 去重验证：同名 skill "review" 被 provider 专有命令覆盖
	if claudeMap["review"].Detail == "Should be overridden by provider review" {
		t.Fatalf("provider command should override skill with same name, got: %s", claudeMap["review"].Detail)
	}

	// 2. 测试 agy：不含 compact，包含 agy 专有命令（goal, browser, grill-me, boost, learn, schedule, teamwork-preview, help）
	// 以及 provider 目录中的 plan（与内置目录同名，去重后保留 builtin 版本）
	fake.resolvedProvider = "agy"
	reqAgy := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s"},"id":502}`, newUUID())
	respAgy := execThreadOpsRPC(t, d, conn, reqAgy)
	if respAgy.Error != nil {
		t.Fatalf("threads.commands for agy failed: %v", respAgy.Error.Message)
	}
	dataAgy, _ := json.Marshal(respAgy.Result)
	var resAgy wire.ThreadsCommandsResult
	_ = json.Unmarshal(dataAgy, &resAgy)

	agyMap := make(map[string]wire.RemoteCommand)
	var agyNames []string
	for _, cmd := range resAgy.Commands {
		if _, dup := agyMap[cmd.Name]; dup {
			t.Fatalf("duplicate command in agy result: %s", cmd.Name)
		}
		agyMap[cmd.Name] = cmd
		agyNames = append(agyNames, cmd.Name)
	}

	// agy 绝不能包含 compact
	if _, hasCompact := agyMap["compact"]; hasCompact {
		t.Fatalf("agy result must not contain compact command, got: %v", agyNames)
	}

	// agy 必须包含 agy 专有命令
	agyExpectedProv := []string{"goal", "browser", "grill-me", "boost", "learn", "schedule", "teamwork-preview", "help"}
	for _, reqName := range agyExpectedProv {
		cmd, ok := agyMap[reqName]
		if !ok {
			t.Fatalf("expected agy commands to contain %q, but got %v", reqName, agyNames)
		}
		if cmd.IsBuiltIn || cmd.Kind != "command" {
			t.Fatalf("agy provider command %q must have isBuiltIn=false and kind=command, got: %+v", reqName, cmd)
		}
	}

	// 且包含内置命令 mode、plan、model 等；plan 与 provider 目录同名，内置命令优先
	for _, reqBuiltin := range []string{"mode", "plan", "model", "effort", "fast", "pair", "export", "clear"} {
		cmd, ok := agyMap[reqBuiltin]
		if !ok {
			t.Fatalf("expected agy commands to contain builtin %q, but got %v", reqBuiltin, agyNames)
		}
		if !cmd.IsBuiltIn || cmd.Kind != "builtin" {
			t.Fatalf("builtin command %q must have isBuiltIn=true and kind=builtin, got: %+v", reqBuiltin, cmd)
		}
	}

	// 技能去重验证：同名 skill "goal" 被 provider 专有命令覆盖
	if agyMap["goal"].Detail == "Should be overridden by agy goal" {
		t.Fatalf("agy provider command should override skill with same name, got: %s", agyMap["goal"].Detail)
	}
	// 独立技能保留
	if _, ok := agyMap["custom-agy-skill"]; !ok {
		t.Fatalf("expected custom-agy-skill to be included in agy commands")
	}

	// 3. 测试 pi：provider 目录包含 model、compact、export，其中 compact 不适用于内置目录，会实际生效
	fake.resolvedProvider = "pi"
	reqPi := fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.commands","params":{"threadID":"%s"},"id":503}`, newUUID())
	respPi := execThreadOpsRPC(t, d, conn, reqPi)
	if respPi.Error != nil {
		t.Fatalf("threads.commands for pi failed: %v", respPi.Error.Message)
	}
	dataPi, _ := json.Marshal(respPi.Result)
	var resPi wire.ThreadsCommandsResult
	_ = json.Unmarshal(dataPi, &resPi)

	piMap := make(map[string]wire.RemoteCommand)
	var piNames []string
	for _, cmd := range resPi.Commands {
		if _, dup := piMap[cmd.Name]; dup {
			t.Fatalf("duplicate command in pi result: %s", cmd.Name)
		}
		piMap[cmd.Name] = cmd
		piNames = append(piNames, cmd.Name)
	}

	// 顺序：内置命令 -> Provider 专有命令（model、export 与内置同名被去重，compact 保留）
	expectedPiNames := []string{
		"mode", "plan", "clear", "model", "effort", "fast", "pair", "export",
		"compact", "reload", "login", "logout", "help",
	}
	if len(piNames) != len(expectedPiNames) {
		t.Fatalf("expected pi command names %v, got %v", expectedPiNames, piNames)
	}
	for i, en := range expectedPiNames {
		if piNames[i] != en {
			t.Fatalf("pi command at index %d expected %s, got %s", i, en, piNames[i])
		}
	}

	// pi 的 compact 来自 provider 目录，必须是 isBuiltIn=false、kind=command
	if cmd := piMap["compact"]; cmd.IsBuiltIn || cmd.Kind != "command" || cmd.Detail != "Compact the active conversation" {
		t.Fatalf("pi compact must be provider command, got: %+v", cmd)
	}
	// model、export 与内置目录同名，去重后保留 builtin 版本
	for _, reqBuiltin := range []string{"model", "export"} {
		if cmd := piMap[reqBuiltin]; !cmd.IsBuiltIn || cmd.Kind != "builtin" {
			t.Fatalf("pi builtin command %q must have isBuiltIn=true and kind=builtin, got: %+v", reqBuiltin, cmd)
		}
	}
}

// 7. capabilities.get 包含新方法和新 feature
func TestThreadOps_Capabilities(t *testing.T) {
	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	auth := &fakeAuth{}
	d := NewDispatcher(auth, fake)
	conn := &threadOpsRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	req := `{"jsonrpc":"2.0","method":"capabilities.get","params":{},"id":500}`
	resp := execThreadOpsRPC(t, d, conn, req)
	if resp.Error != nil {
		t.Fatalf("capabilities.get failed: %v", resp.Error.Message)
	}

	data, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal capabilities: %v", err)
	}
	var caps wire.Capabilities
	if err := json.Unmarshal(data, &caps); err != nil {
		t.Fatalf("unmarshal capabilities: %v", err)
	}

	expectedMethods := []string{
		"threads.move",
		"threads.branch",
		"threads.continue",
		"threads.commands",
		"threads.settleAction",
		"threads.queue.list",
		"threads.queue.update",
		"threads.queue.remove",
		"threads.queue.sendNow",
		"threads.queue.interruptAndSend",
	}

	methodMap := make(map[string]bool)
	for _, m := range caps.Methods {
		methodMap[m] = true
	}
	for _, em := range expectedMethods {
		if !methodMap[em] {
			t.Errorf("expected capabilities.methods to contain %s", em)
		}
	}

	expectedFeatures := []string{
		"threads.queue",
		"threads.commands",
		"threads.continue",
		"threads.settleAction",
	}
	featureMap := make(map[string]bool)
	for _, f := range caps.Features {
		featureMap[f] = true
	}
	for _, ef := range expectedFeatures {
		if !featureMap[ef] {
			t.Errorf("expected capabilities.features to contain %s", ef)
		}
	}
}

// 6. 父会话取消休眠时级联取消其名下 helper 子会话的休眠状态，普通子会话不受影响
func TestThreadOps_UnsettleCascadesToHelperThreads(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ctx := context.Background()

	fake, hub := newTestFakeThreadOps()
	defer hub.Close()
	defer fake.Close()

	backend := fake.MemoryBackend
	projID := "p-unsettle-cascade"
	backend.AddProject(wire.ProjectSummary{ID: projID, Name: "Proj", Path: t.TempDir()})

	newThread := func() string {
		res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: projID, ActionID: newUUID()})
		if err != nil {
			t.Fatalf("CreateThread failed: %v", err)
		}
		return res.ThreadID
	}

	parentID := newThread()
	helperID := newThread()
	plainChildID := newThread()

	backend.SetThreadRelation(helperID, parentID, true)
	backend.SetThreadRelation(plainChildID, parentID, false)

	dormant := true
	if _, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: parentID, Dormant: &dormant}); err != nil {
		t.Fatalf("settle parent failed: %v", err)
	}
	if _, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: plainChildID, Dormant: &dormant}); err != nil {
		t.Fatalf("settle plain child failed: %v", err)
	}

	helper, err := backend.GetThread(ctx, helperID)
	if err != nil {
		t.Fatalf("GetThread(helper) failed: %v", err)
	}
	if !helper.IsDormant {
		t.Fatalf("expected helper to be dormant after parent settle, got false")
	}

	undormant := false
	if _, err := backend.UpdateThread(ctx, wire.ThreadUpdateParams{ThreadID: parentID, Dormant: &undormant}); err != nil {
		t.Fatalf("unsettle parent failed: %v", err)
	}

	parent, err := backend.GetThread(ctx, parentID)
	if err != nil {
		t.Fatalf("GetThread(parent) failed: %v", err)
	}
	if parent.IsDormant {
		t.Fatalf("expected parent to be undormant")
	}
	if _, ok := backend.ThreadDormantAt(parentID); ok {
		t.Fatalf("expected parent dormantAt cleared")
	}

	helperAfter, err := backend.GetThread(ctx, helperID)
	if err != nil {
		t.Fatalf("GetThread(helper) after unsettle failed: %v", err)
	}
	if helperAfter.IsDormant {
		t.Fatalf("expected helper to be undormant after parent unsettle, got true")
	}
	if _, ok := backend.ThreadDormantAt(helperID); ok {
		t.Fatalf("expected helper dormantAt cleared after parent unsettle")
	}

	plainChild, err := backend.GetThread(ctx, plainChildID)
	if err != nil {
		t.Fatalf("GetThread(plain child) failed: %v", err)
	}
	if !plainChild.IsDormant {
		t.Fatalf("expected non-helper child to stay dormant, got false")
	}
}

// TestParseForwardedContext 验证转发上下文的解析：正文取首尾标记之间，来源标题按句尾定界，
// 截断提示只看导言，缺标记时视为不是转发上下文。
func TestParseForwardedContext(t *testing.T) {
	prompt := forwardedContextPrompt("原会话", "User:\n原来的第一句", false, 0)
	fc, ok := parseForwardedContext(prompt)
	if !ok {
		t.Fatalf("正常导言应解析成功: %q", prompt)
	}
	if fc.SourceTitle != "原会话" {
		t.Fatalf("SourceTitle = %q, want 原会话", fc.SourceTitle)
	}
	if fc.Content != "User:\n原来的第一句" {
		t.Fatalf("Content = %q, want 原文", fc.Content)
	}
	if fc.Truncated {
		t.Fatalf("没有截断提示时 Truncated 应为 false")
	}

	// 标题里带引号：按句尾而不是第一个引号定界。
	quoted := forwardedContextPrompt(`他说 "你好" 的那次`, "hello", false, 0)
	fc, ok = parseForwardedContext(quoted)
	if !ok || fc.SourceTitle != `他说 "你好" 的那次` {
		t.Fatalf("标题含引号时解析错误，got %+v, %v", fc, ok)
	}

	// 正文里带截断提示、导言没有：不能算截断。
	body := "Assistant:\n" + mentionTrimmedNotice
	fc, ok = parseForwardedContext(forwardedContextPrompt("原会话", body, false, 0))
	if !ok || fc.Truncated {
		t.Fatalf("正文里的截断提示不应让 Truncated 为 true，got %+v, %v", fc, ok)
	}

	// 导言带截断提示：Truncated 为 true。
	fc, ok = parseForwardedContext(forwardedContextPrompt("原会话", body, true, 0))
	if !ok || !fc.Truncated {
		t.Fatalf("导言带截断提示时 Truncated 应为 true，got %+v, %v", fc, ok)
	}

	// 缺标记：不是转发上下文。
	if _, ok := parseForwardedContext("just a prompt"); ok {
		t.Fatalf("没有标记时不应解析成功")
	}
	if _, ok := parseForwardedContext(forwardedContextStart + "\n正文只有开始标记"); ok {
		t.Fatalf("只有开始标记时不应解析成功")
	}
}

// TestThreadForwardedContext 验证转发后能取回带入的上下文，普通会话返回 false。
func TestThreadForwardedContext(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	plainID := newUUID()
	backend.AddThread(wire.ThreadSummary{ID: plainID, Title: "普通会话", ProjectID: "p-fwd"}, []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "hello"},
	})
	if _, ok := backend.ThreadForwardedContext(plainID); ok {
		t.Fatalf("普通会话不应有转发上下文")
	}
	if _, ok := backend.ThreadForwardedContext("missing"); ok {
		t.Fatalf("不存在的会话不应有转发上下文")
	}

	res, err := backend.ForwardThread(ctx, wire.ThreadsContinueParams{ThreadID: plainID, ActionID: newUUID()})
	if err != nil {
		t.Fatalf("ForwardThread failed: %v", err)
	}
	fc, ok := backend.ThreadForwardedContext(res.ThreadID)
	if !ok {
		t.Fatalf("转发会话应能取回带入的上下文")
	}
	if fc.SourceTitle != "普通会话" {
		t.Fatalf("SourceTitle = %q, want 普通会话", fc.SourceTitle)
	}
	if !strings.Contains(fc.Content, "hello") {
		t.Fatalf("Content 应包含被转发的正文，got %q", fc.Content)
	}
	if strings.Contains(fc.Content, forwardedContextStart) || strings.Contains(fc.Content, forwardedContextEnd) {
		t.Fatalf("Content 不应包含标记本身，got %q", fc.Content)
	}
}
