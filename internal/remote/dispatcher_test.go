package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

type fakeConn struct {
	info ClientInfo
}

func (c *fakeConn) Info() ClientInfo {
	return c.info
}

func (c *fakeConn) MarkAuthenticated(deviceID, label string) {
	c.info.Authenticated = true
	c.info.DeviceID = deviceID
	c.info.Label = label
}

func (c *fakeConn) Notify(method string, params any) error {
	return nil
}

func (c *fakeConn) Close() error {
	return nil
}

type fakeAuth struct {
	pairFunc         func(token, deviceName string, info ClientInfo) (PairResult, error)
	authenticateFunc func(token string, info ClientInfo) (string, string, error)
	newPairingFunc   func() (PairingInfo, error)
	revokeDeviceFunc func(deviceID string) (bool, error)
	renameDeviceFunc func(deviceID, name string) error
}

func (a *fakeAuth) Pair(token, deviceName string, info ClientInfo) (PairResult, error) {
	if a.pairFunc != nil {
		return a.pairFunc(token, deviceName, info)
	}
	return PairResult{}, ErrInvalidToken
}

func (a *fakeAuth) Authenticate(token string, info ClientInfo) (string, string, error) {
	if a.authenticateFunc != nil {
		return a.authenticateFunc(token, info)
	}
	return "", "", ErrInvalidToken
}

func (a *fakeAuth) NewPairingCode() (PairingInfo, error) {
	if a.newPairingFunc != nil {
		return a.newPairingFunc()
	}
	return PairingInfo{}, errors.New("pairing unavailable")
}

func (a *fakeAuth) RevokeDevice(deviceID string) (bool, error) {
	if a.revokeDeviceFunc != nil {
		return a.revokeDeviceFunc(deviceID)
	}
	return true, nil
}

func (a *fakeAuth) RenameDevice(deviceID, name string) error {
	if a.renameDeviceFunc != nil {
		return a.renameDeviceFunc(deviceID, name)
	}
	return nil
}

type fakeBackend struct {
	capabilitiesFunc   func(ctx context.Context) (wire.Capabilities, error)
	kanbanBoardFunc    func(ctx context.Context) (wire.KanbanBoard, error)
	listProjectsFunc   func(ctx context.Context) (wire.ProjectList, error)
	listThreadsFunc    func(ctx context.Context, params wire.ThreadListParams) (wire.ThreadList, error)
	getThreadFunc      func(ctx context.Context, threadID string) (wire.ThreadSummary, error)
	threadHistoryFunc  func(ctx context.Context, params wire.HistoryParams) (wire.HistoryPage, error)
	subscribeFunc      func(ctx context.Context, c Conn, threadID string) (wire.SubscribeResult, error)
	unsubscribeFunc    func(ctx context.Context, c Conn, threadID string) error
	sendMessageFunc    func(ctx context.Context, params wire.SendMessageParams) (wire.SendMessageResult, error)
	interruptFunc      func(ctx context.Context, params wire.InterruptParams) (wire.InterruptResult, error)
	createThreadFunc   func(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error)
	updateThreadFunc   func(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error)
	actionStatusFunc   func(ctx context.Context, actionID string) (wire.ActionStatusResult, error)
	listRequestsFunc   func(ctx context.Context, threadID string) (wire.RequestsListResult, error)
	respondRequestFunc func(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error)
	listProvidersFunc  func(ctx context.Context) (wire.ProvidersListResult, error)
	removeConnFunc     func(c Conn)
}

func (b *fakeBackend) Capabilities(ctx context.Context) (wire.Capabilities, error) {
	if b.capabilitiesFunc != nil {
		return b.capabilitiesFunc(ctx)
	}
	return wire.Capabilities{}, nil
}

func (b *fakeBackend) KanbanBoard(ctx context.Context) (wire.KanbanBoard, error) {
	if b.kanbanBoardFunc != nil {
		return b.kanbanBoardFunc(ctx)
	}
	return wire.KanbanBoard{}, nil
}

func (b *fakeBackend) ListProjects(ctx context.Context) (wire.ProjectList, error) {
	if b.listProjectsFunc != nil {
		return b.listProjectsFunc(ctx)
	}
	return wire.ProjectList{}, nil
}

func (b *fakeBackend) ListThreads(ctx context.Context, params wire.ThreadListParams) (wire.ThreadList, error) {
	if b.listThreadsFunc != nil {
		return b.listThreadsFunc(ctx, params)
	}
	return wire.ThreadList{}, nil
}

func (b *fakeBackend) GetThread(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
	if b.getThreadFunc != nil {
		return b.getThreadFunc(ctx, threadID)
	}
	return wire.ThreadSummary{}, nil
}

func (b *fakeBackend) ThreadHistory(ctx context.Context, params wire.HistoryParams) (wire.HistoryPage, error) {
	if b.threadHistoryFunc != nil {
		return b.threadHistoryFunc(ctx, params)
	}
	return wire.HistoryPage{}, nil
}

func (b *fakeBackend) Subscribe(ctx context.Context, c Conn, threadID string) (wire.SubscribeResult, error) {
	if b.subscribeFunc != nil {
		return b.subscribeFunc(ctx, c, threadID)
	}
	return wire.SubscribeResult{}, nil
}

func (b *fakeBackend) Unsubscribe(ctx context.Context, c Conn, threadID string) error {
	if b.unsubscribeFunc != nil {
		return b.unsubscribeFunc(ctx, c, threadID)
	}
	return nil
}

func (b *fakeBackend) SendMessage(ctx context.Context, params wire.SendMessageParams) (wire.SendMessageResult, error) {
	if b.sendMessageFunc != nil {
		return b.sendMessageFunc(ctx, params)
	}
	return wire.SendMessageResult{}, nil
}

func (b *fakeBackend) Interrupt(ctx context.Context, params wire.InterruptParams) (wire.InterruptResult, error) {
	if b.interruptFunc != nil {
		return b.interruptFunc(ctx, params)
	}
	return wire.InterruptResult{
		ThreadID: params.ThreadID,
		TurnID:   params.TurnID,
		State:    wire.InterruptStateStopping,
	}, nil
}

func (b *fakeBackend) CreateThread(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error) {
	if b.createThreadFunc != nil {
		return b.createThreadFunc(ctx, params)
	}
	return wire.ThreadCreateResult{}, nil
}

func (b *fakeBackend) UpdateThread(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error) {
	if b.updateThreadFunc != nil {
		return b.updateThreadFunc(ctx, params)
	}
	return wire.ThreadUpdateResult{}, nil
}

func (b *fakeBackend) ActionStatus(ctx context.Context, actionID string) (wire.ActionStatusResult, error) {
	if b.actionStatusFunc != nil {
		return b.actionStatusFunc(ctx, actionID)
	}
	return wire.ActionStatusResult{ActionID: actionID, State: wire.ActionStateUnknown}, nil
}

func (b *fakeBackend) ListRequests(ctx context.Context, threadID string) (wire.RequestsListResult, error) {
	if b.listRequestsFunc != nil {
		return b.listRequestsFunc(ctx, threadID)
	}
	return wire.RequestsListResult{ThreadID: threadID, Requests: []wire.RemoteRequest{}}, nil
}

func (b *fakeBackend) RespondRequest(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error) {
	if b.respondRequestFunc != nil {
		return b.respondRequestFunc(ctx, params)
	}
	return wire.RequestsRespondResult{
		ThreadID:  params.ThreadID,
		RequestID: params.RequestID,
		Kind:      "tool",
		Status:    wire.RequestStatusAccepted,
	}, nil
}

func (b *fakeBackend) ListProviders(ctx context.Context) (wire.ProvidersListResult, error) {
	if b.listProvidersFunc != nil {
		return b.listProvidersFunc(ctx)
	}
	return wire.ProvidersListResult{
		Providers: []wire.Provider{},
		Swarm: wire.SwarmComposerInfo{
			Enabled:        false,
			DefaultEnabled: false,
			Pairs:          []wire.SwarmPair{},
		},
	}, nil
}

func (b *fakeBackend) RemoveConn(c Conn) {
	if b.removeConnFunc != nil {
		b.removeConnFunc(c)
	}
}

func TestDispatcherAuthentication(t *testing.T) {
	auth := &fakeAuth{
		pairFunc: func(token, deviceName string, info ClientInfo) (PairResult, error) {
			if token == "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
				return PairResult{
					SessionToken: "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
					DeviceID:     "95a01908-6b43-40be-8d84-057bb3fa6826",
					Label:        "Test Device",
				}, nil
			}
			return PairResult{}, ErrInvalidToken
		},
	}
	backend := &fakeBackend{
		listProjectsFunc: func(ctx context.Context) (wire.ProjectList, error) {
			return wire.ProjectList{Revision: 1}, nil
		},
	}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: false}}
	ctx := context.Background()

	// 1. 未认证调用受保护方法 projects.list 被拒绝
	unauthReq := `{"jsonrpc":"2.0","method":"projects.list","params":{},"id":1}`
	respRaw := d.Handle(ctx, conn, []byte(unauthReq))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeUnauthorized {
		t.Errorf("expected unauthorized code %d, got: %+v", wire.ErrCodeUnauthorized, resp.Error)
	}

	// 2. session.pair 成功后调用 MarkAuthenticated 并允许后续受保护调用
	pairReq := `{"jsonrpc":"2.0","method":"session.pair","params":{"token":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","deviceName":"iPhone"},"id":2}`
	pairRespRaw := d.Handle(ctx, conn, []byte(pairReq))
	var pairResp wire.Response
	if err := json.Unmarshal(pairRespRaw, &pairResp); err != nil {
		t.Fatalf("unmarshal pair resp: %v", err)
	}
	if pairResp.Error != nil {
		t.Fatalf("pair unexpected error: %+v", pairResp.Error)
	}
	if !conn.Info().Authenticated {
		t.Errorf("connection should be marked authenticated")
	}
	if conn.Info().DeviceID != "95a01908-6b43-40be-8d84-057bb3fa6826" {
		t.Errorf("unexpected deviceID: %s", conn.Info().DeviceID)
	}

	// 3. 此时已认证，再次调用 projects.list 应该成功
	authProjRespRaw := d.Handle(ctx, conn, []byte(unauthReq))
	var projResp wire.Response
	if err := json.Unmarshal(authProjRespRaw, &projResp); err != nil {
		t.Fatalf("unmarshal proj resp: %v", err)
	}
	if projResp.Error != nil {
		t.Fatalf("projects.list unexpected error after auth: %+v", projResp.Error)
	}
}

func TestDispatcherBatching(t *testing.T) {
	auth := &fakeAuth{}
	backend := &fakeBackend{
		listProjectsFunc: func(ctx context.Context) (wire.ProjectList, error) {
			return wire.ProjectList{Revision: 2}, nil
		},
	}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	// 1. 正常批处理 2 项
	batchReq := `[
		{"jsonrpc":"2.0","method":"projects.list","params":{},"id":1},
		{"jsonrpc":"2.0","method":"projects.list","params":{},"id":2}
	]`
	respRaw := d.Handle(ctx, conn, []byte(batchReq))
	var responses []wire.Response
	if err := json.Unmarshal(respRaw, &responses); err != nil {
		t.Fatalf("unmarshal batch responses: %v", err)
	}
	if len(responses) != 2 {
		t.Fatalf("expected 2 responses, got %d", len(responses))
	}
	if string(responses[0].ID) != "1" || string(responses[1].ID) != "2" {
		t.Errorf("unexpected response IDs: %s, %s", responses[0].ID, responses[1].ID)
	}

	// 2. 空数组批处理返回 InvalidRequest
	emptyBatch := `[]`
	respEmpty := d.Handle(ctx, conn, []byte(emptyBatch))
	var emptyResp wire.Response
	if err := json.Unmarshal(respEmpty, &emptyResp); err != nil {
		t.Fatalf("unmarshal empty batch response: %v", err)
	}
	if emptyResp.Error == nil || emptyResp.Error.Code != wire.InvalidRequest {
		t.Errorf("expected InvalidRequest for empty batch, got: %+v", emptyResp.Error)
	}

	// 3. 超过 64 项批处理返回 InvalidRequest
	var bigBatch []string
	for i := 1; i <= 65; i++ {
		bigBatch = append(bigBatch, fmt.Sprintf(`{"jsonrpc":"2.0","method":"projects.list","params":{},"id":%d}`, i))
	}
	bigBatchJSON := "[" + fmt.Sprintf("%s", bigBatch[0])
	for _, item := range bigBatch[1:] {
		bigBatchJSON += "," + item
	}
	bigBatchJSON += "]"
	respBig := d.Handle(ctx, conn, []byte(bigBatchJSON))
	var bigResp wire.Response
	if err := json.Unmarshal(respBig, &bigResp); err != nil {
		t.Fatalf("unmarshal big batch resp: %v", err)
	}
	if bigResp.Error == nil || bigResp.Error.Code != wire.InvalidRequest {
		t.Errorf("expected InvalidRequest for batch > 64, got: %+v", bigResp.Error)
	}
}

func TestDispatcherParsingAndRouting(t *testing.T) {
	auth := &fakeAuth{}
	backend := &fakeBackend{}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	// 1. JSON 解析错误
	brokenJSON := `{"jsonrpc":"2.0", broken`
	respRaw := d.Handle(ctx, conn, []byte(brokenJSON))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal parse error response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != wire.ParseError {
		t.Errorf("expected ParseError, got: %+v", resp.Error)
	}
	if string(resp.ID) != "null" {
		t.Errorf("expected ID null on parse error, got: %s", string(resp.ID))
	}

	// 2. 未知方法返回 MethodNotFound 且 message 为 "Method not implemented"
	unknownMethodReq := `{"jsonrpc":"2.0","method":"custom.unknown","params":{},"id":100}`
	respRaw = d.Handle(ctx, conn, []byte(unknownMethodReq))
	var unkResp wire.Response
	if err := json.Unmarshal(respRaw, &unkResp); err != nil {
		t.Fatalf("unmarshal unknown method response: %v", err)
	}
	if unkResp.Error == nil || unkResp.Error.Code != wire.MethodNotFound {
		t.Errorf("expected MethodNotFound, got: %+v", unkResp.Error)
	}
	if unkResp.Error.Message != "Method not implemented" {
		t.Errorf("expected message 'Method not implemented', got: %s", unkResp.Error.Message)
	}
}

func TestDispatcherStrictParams(t *testing.T) {
	auth := &fakeAuth{}
	backend := &fakeBackend{}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	// 1. session.newPairingCode 传入非空对象返回 InvalidParams
	nonEmptyParamsReq := `{"jsonrpc":"2.0","method":"session.newPairingCode","params":{"unexpected":"value"},"id":1}`
	respRaw := d.Handle(ctx, conn, []byte(nonEmptyParamsReq))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal invalid params resp: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for session.newPairingCode with params, got: %+v", resp.Error)
	}

	// 2. threads.get 传入非法 UUID 格式返回 InvalidParams
	badUUIDReq := `{"jsonrpc":"2.0","method":"threads.get","params":{"threadID":"not-a-valid-uuid"},"id":2}`
	respRaw = d.Handle(ctx, conn, []byte(badUUIDReq))
	var badUUIDResp wire.Response
	if err := json.Unmarshal(respRaw, &badUUIDResp); err != nil {
		t.Fatalf("unmarshal bad UUID resp: %v", err)
	}
	if badUUIDResp.Error == nil || badUUIDResp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for bad UUID, got: %+v", badUUIDResp.Error)
	}

	// 3. 未知字段传入返回 InvalidParams
	unknownFieldReq := `{"jsonrpc":"2.0","method":"threads.get","params":{"threadID":"00000000-0000-0000-0000-000000000001","extra":"field"},"id":3}`
	respRaw = d.Handle(ctx, conn, []byte(unknownFieldReq))
	var unkFieldResp wire.Response
	if err := json.Unmarshal(respRaw, &unkFieldResp); err != nil {
		t.Fatalf("unmarshal unk field resp: %v", err)
	}
	if unkFieldResp.Error == nil || unkFieldResp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for unknown fields, got: %+v", unkFieldResp.Error)
	}
}

func TestDispatcher_SessionNewPairingCode_TailscalePairingURL(t *testing.T) {
	const tailscaleURL = "https://mcode.example.ts.net/#pair=abc"
	ctx := context.Background()
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	newPairingCode := func(auth *fakeAuth) wire.SessionNewPairingCodeResult {
		t.Helper()
		d := NewDispatcher(auth, &fakeBackend{})
		respRaw := d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"session.newPairingCode","params":{},"id":1}`))
		var resp wire.Response
		if err := json.Unmarshal(respRaw, &resp); err != nil {
			t.Fatalf("unmarshal resp: %v", err)
		}
		if resp.Error != nil {
			t.Fatalf("session.newPairingCode failed: %+v", resp.Error)
		}
		resultBytes, err := json.Marshal(resp.Result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var result wire.SessionNewPairingCodeResult
		if err := json.Unmarshal(resultBytes, &result); err != nil {
			t.Fatalf("unmarshal result: %v", err)
		}
		return result
	}

	// 1. Tailscale 可用时 tailscalePairingURL 透传到 RPC 结果
	auth := &fakeAuth{
		newPairingFunc: func() (PairingInfo, error) {
			return PairingInfo{
				PairingURL:          tailscaleURL,
				LANPairingURL:       "http://192.168.1.10:43117/#pair=abc",
				TailscalePairingURL: tailscaleURL,
				ExpiresAt:           time.Unix(0, 0).UTC(),
			}, nil
		},
	}
	result := newPairingCode(auth)
	if result.TailscalePairingURL != tailscaleURL {
		t.Errorf("TailscalePairingURL = %q, want %q", result.TailscalePairingURL, tailscaleURL)
	}

	// 2. Tailscale 不可用时字段为空，JSON 中应省略 tailscalePairingURL
	auth = &fakeAuth{
		newPairingFunc: func() (PairingInfo, error) {
			return PairingInfo{
				PairingURL:    "http://192.168.1.10:43117/#pair=abc",
				LANPairingURL: "http://192.168.1.10:43117/#pair=abc",
			}, nil
		},
	}
	d := NewDispatcher(auth, &fakeBackend{})
	respRaw := d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"session.newPairingCode","params":{},"id":2}`))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("session.newPairingCode failed: %+v", resp.Error)
	}
	resultBytes, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(resultBytes, &fields); err != nil {
		t.Fatalf("unmarshal result fields: %v", err)
	}
	if _, ok := fields["tailscalePairingURL"]; ok {
		t.Errorf("expected tailscalePairingURL to be omitted when empty, got %v", fields["tailscalePairingURL"])
	}
}

func TestDispatcherNotifications(t *testing.T) {
	auth := &fakeAuth{}
	backend := &fakeBackend{
		listProjectsFunc: func(ctx context.Context) (wire.ProjectList, error) {
			return wire.ProjectList{}, nil
		},
	}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	// 1. 单个 notification（无 id）不产生响应
	notif := `{"jsonrpc":"2.0","method":"projects.list","params":{}}`
	resp := d.Handle(ctx, conn, []byte(notif))
	if resp != nil {
		t.Errorf("expected nil response for notification, got: %s", string(resp))
	}

	// 2. 批处理全为 notification 时不产生响应
	batchNotif := `[
		{"jsonrpc":"2.0","method":"projects.list","params":{}},
		{"jsonrpc":"2.0","method":"projects.list","params":{}}
	]`
	respBatch := d.Handle(ctx, conn, []byte(batchNotif))
	if respBatch != nil {
		t.Errorf("expected nil response for all-notification batch, got: %s", string(respBatch))
	}
}

func TestDispatcherErrorMapping(t *testing.T) {
	auth := &fakeAuth{}
	backend := &fakeBackend{
		getThreadFunc: func(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
			return wire.ThreadSummary{}, &wire.RPCError{
				Code:    wire.ErrCodeTurnMismatch,
				Message: "turnID mismatch",
			}
		},
		listProjectsFunc: func(ctx context.Context) (wire.ProjectList, error) {
			return wire.ProjectList{}, errors.New("underlying sqlite database is locked")
		},
	}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	// 1. Backend 返回 *wire.RPCError 的错误码透传 (-32013)
	rpcErrReq := `{"jsonrpc":"2.0","method":"threads.get","params":{"threadID":"00000000-0000-0000-0000-000000000001"},"id":1}`
	respRaw := d.Handle(ctx, conn, []byte(rpcErrReq))
	var rpcErrResp wire.Response
	if err := json.Unmarshal(respRaw, &rpcErrResp); err != nil {
		t.Fatalf("unmarshal rpc err resp: %v", err)
	}
	if rpcErrResp.Error == nil || rpcErrResp.Error.Code != wire.ErrCodeTurnMismatch {
		t.Errorf("expected ErrCodeTurnMismatch (%d), got: %+v", wire.ErrCodeTurnMismatch, rpcErrResp.Error)
	}

	// 2. 普通 error 映射为 -32603 InternalError
	internalErrReq := `{"jsonrpc":"2.0","method":"projects.list","params":{},"id":2}`
	respRaw2 := d.Handle(ctx, conn, []byte(internalErrReq))
	var intErrResp wire.Response
	if err := json.Unmarshal(respRaw2, &intErrResp); err != nil {
		t.Fatalf("unmarshal int err resp: %v", err)
	}
	if intErrResp.Error == nil || intErrResp.Error.Code != wire.InternalError {
		t.Errorf("expected InternalError (%d), got: %+v", wire.InternalError, intErrResp.Error)
	}
}

func TestDispatcherThreadsCreate(t *testing.T) {
	var receivedParams wire.ThreadCreateParams
	auth := &fakeAuth{}
	backend := &fakeBackend{
		createThreadFunc: func(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error) {
			receivedParams = params
			return wire.ThreadCreateResult{
				ActionID: params.ActionID,
				ThreadID: "33333333-3333-3333-3333-333333333333",
			}, nil
		},
	}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	// 1. 成功路由并回传 actionID/threadID，记录参数
	req := `{
		"jsonrpc": "2.0",
		"method": "threads.create",
		"params": {
			"projectID": "11111111-1111-1111-1111-111111111111",
			"actionID": "22222222-2222-2222-2222-222222222222",
			"options": {"model": "gpt-4"}
		},
		"id": 1
	}`
	respRaw := d.Handle(ctx, conn, []byte(req))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if receivedParams.ProjectID != "11111111-1111-1111-1111-111111111111" || receivedParams.ActionID != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("received params mismatch: %+v", receivedParams)
	}
	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got: %T", resp.Result)
	}
	if resMap["actionID"] != "22222222-2222-2222-2222-222222222222" || resMap["threadID"] != "33333333-3333-3333-3333-333333333333" {
		t.Errorf("result mismatch: %+v", resMap)
	}

	// 2. 缺 projectID 返回 InvalidParams
	missingProjReq := `{
		"jsonrpc": "2.0",
		"method": "threads.create",
		"params": {
			"actionID": "22222222-2222-2222-2222-222222222222"
		},
		"id": 2
	}`
	respRaw2 := d.Handle(ctx, conn, []byte(missingProjReq))
	var resp2 wire.Response
	if err := json.Unmarshal(respRaw2, &resp2); err != nil {
		t.Fatalf("unmarshal resp2: %v", err)
	}
	if resp2.Error == nil || resp2.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams when projectID is missing, got: %+v", resp2.Error)
	}

	// 3. 后端返回 *wire.RPCError 时原样透传错误码
	backend.createThreadFunc = func(ctx context.Context, params wire.ThreadCreateParams) (wire.ThreadCreateResult, error) {
		return wire.ThreadCreateResult{}, &wire.RPCError{
			Code:    wire.ErrCodeTurnMismatch,
			Message: "custom backend rpc error",
		}
	}
	respRaw3 := d.Handle(ctx, conn, []byte(req))
	var resp3 wire.Response
	if err := json.Unmarshal(respRaw3, &resp3); err != nil {
		t.Fatalf("unmarshal resp3: %v", err)
	}
	if resp3.Error == nil || resp3.Error.Code != wire.ErrCodeTurnMismatch {
		t.Errorf("expected ErrCodeTurnMismatch (%d), got: %+v", wire.ErrCodeTurnMismatch, resp3.Error)
	}
}

func TestDispatcher_RequestsList(t *testing.T) {
	ctx := context.Background()
	conn := &fakeConn{}
	conn.MarkAuthenticated("device-1", "MacBook Pro")

	var receivedThreadID string
	backend := &fakeBackend{
		listRequestsFunc: func(ctx context.Context, threadID string) (wire.RequestsListResult, error) {
			receivedThreadID = threadID
			return wire.RequestsListResult{
				ThreadID: threadID,
				Requests: []wire.RemoteRequest{},
			}, nil
		},
	}
	d := NewDispatcher(&fakeAuth{}, backend)

	// 1. 正常路由
	req := `{
		"jsonrpc": "2.0",
		"method": "requests.list",
		"params": {
			"threadID": "11111111-1111-1111-1111-111111111111"
		},
		"id": 1
	}`
	respRaw := d.Handle(ctx, conn, []byte(req))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if receivedThreadID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("received threadID mismatch: %s", receivedThreadID)
	}

	// 2. 非法 threadID 返回 InvalidParams
	badReq := `{
		"jsonrpc": "2.0",
		"method": "requests.list",
		"params": {
			"threadID": "not-a-uuid"
		},
		"id": 2
	}`
	respRaw2 := d.Handle(ctx, conn, []byte(badReq))
	var resp2 wire.Response
	if err := json.Unmarshal(respRaw2, &resp2); err != nil {
		t.Fatalf("unmarshal resp2: %v", err)
	}
	if resp2.Error == nil || resp2.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for bad threadID, got: %+v", resp2.Error)
	}
}

func TestDispatcher_RequestsRespond(t *testing.T) {
	ctx := context.Background()
	conn := &fakeConn{}
	conn.MarkAuthenticated("device-1", "MacBook Pro")

	var receivedParams wire.RequestsRespondParams
	backend := &fakeBackend{
		respondRequestFunc: func(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error) {
			receivedParams = params
			return wire.RequestsRespondResult{
				ThreadID:  params.ThreadID,
				RequestID: params.RequestID,
				Kind:      "tool",
				Status:    wire.RequestStatusAccepted,
			}, nil
		},
	}
	d := NewDispatcher(&fakeAuth{}, backend)

	// 1. 正常响应（optionID）
	req := `{
		"jsonrpc": "2.0",
		"method": "requests.respond",
		"params": {
			"threadID": "11111111-1111-1111-1111-111111111111",
			"runID": "run-001",
			"requestID": "req-001",
			"optionID": "allow"
		},
		"id": 1
	}`
	respRaw := d.Handle(ctx, conn, []byte(req))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if receivedParams.ThreadID != "11111111-1111-1111-1111-111111111111" ||
		receivedParams.RunID != "run-001" ||
		receivedParams.RequestID != "req-001" ||
		receivedParams.OptionID == nil || *receivedParams.OptionID != "allow" {
		t.Errorf("received params mismatch: %+v", receivedParams)
	}

	// 2. 正常响应（skip）
	reqSkip := `{
		"jsonrpc": "2.0",
		"method": "requests.respond",
		"params": {
			"threadID": "11111111-1111-1111-1111-111111111111",
			"runID": "run-001",
			"requestID": "req-001",
			"skip": true
		},
		"id": 2
	}`
	respRawSkip := d.Handle(ctx, conn, []byte(reqSkip))
	var respSkip wire.Response
	if err := json.Unmarshal(respRawSkip, &respSkip); err != nil {
		t.Fatalf("unmarshal respSkip: %v", err)
	}
	if respSkip.Error != nil {
		t.Fatalf("unexpected error: %+v", respSkip.Error)
	}
	if receivedParams.Skip == nil || !*receivedParams.Skip {
		t.Errorf("expected skip to be true in receivedParams")
	}

	// 3. 缺失必填字段 runID
	missingRunIDReq := `{
		"jsonrpc": "2.0",
		"method": "requests.respond",
		"params": {
			"threadID": "11111111-1111-1111-1111-111111111111",
			"requestID": "req-001",
			"optionID": "allow"
		},
		"id": 3
	}`
	respRaw3 := d.Handle(ctx, conn, []byte(missingRunIDReq))
	var resp3 wire.Response
	if err := json.Unmarshal(respRaw3, &resp3); err != nil {
		t.Fatalf("unmarshal resp3: %v", err)
	}
	if resp3.Error == nil || resp3.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams when runID is missing, got: %+v", resp3.Error)
	}

	// 4. optionID/answers/skip 均未提供（count == 0）
	noneReq := `{
		"jsonrpc": "2.0",
		"method": "requests.respond",
		"params": {
			"threadID": "11111111-1111-1111-1111-111111111111",
			"runID": "run-001",
			"requestID": "req-001"
		},
		"id": 4
	}`
	respRaw4 := d.Handle(ctx, conn, []byte(noneReq))
	var resp4 wire.Response
	if err := json.Unmarshal(respRaw4, &resp4); err != nil {
		t.Fatalf("unmarshal resp4: %v", err)
	}
	if resp4.Error == nil || resp4.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams when none provided, got: %+v", resp4.Error)
	}

	// 5. 同时提供 optionID 与 skip（count > 1）
	bothReq := `{
		"jsonrpc": "2.0",
		"method": "requests.respond",
		"params": {
			"threadID": "11111111-1111-1111-1111-111111111111",
			"runID": "run-001",
			"requestID": "req-001",
			"optionID": "allow",
			"skip": true
		},
		"id": 5
	}`
	respRaw5 := d.Handle(ctx, conn, []byte(bothReq))
	var resp5 wire.Response
	if err := json.Unmarshal(respRaw5, &resp5); err != nil {
		t.Fatalf("unmarshal resp5: %v", err)
	}
	if resp5.Error == nil || resp5.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams when multiple provided, got: %+v", resp5.Error)
	}
}

func TestDispatcherThreadsActionStatus(t *testing.T) {
	ctx := context.Background()
	conn := &fakeConn{}
	conn.MarkAuthenticated("device-1", "Test Device")

	calledActionID := ""
	backend := &fakeBackend{
		actionStatusFunc: func(ctx context.Context, actionID string) (wire.ActionStatusResult, error) {
			calledActionID = actionID
			threadID := "11111111-1111-1111-1111-111111111111"
			return wire.ActionStatusResult{
				ActionID: actionID,
				State:    wire.ActionStateAccepted,
				ThreadID: &threadID,
			}, nil
		},
	}
	d := NewDispatcher(&fakeAuth{}, backend)

	// 1. 成功调用
	validReq := `{
		"jsonrpc": "2.0",
		"method": "threads.actionStatus",
		"params": {
			"actionID": "00000000-0000-0000-0000-000000000001"
		},
		"id": 1
	}`
	respRaw1 := d.Handle(ctx, conn, []byte(validReq))
	var resp1 wire.Response
	if err := json.Unmarshal(respRaw1, &resp1); err != nil {
		t.Fatalf("unmarshal resp1: %v", err)
	}
	if resp1.Error != nil {
		t.Fatalf("unexpected error: %+v", resp1.Error)
	}
	if calledActionID != "00000000-0000-0000-0000-000000000001" {
		t.Errorf("expected calledActionID to match, got %s", calledActionID)
	}

	// 2. 未认证连接拦截
	unauthConn := &fakeConn{}
	respRawUnauth := d.Handle(ctx, unauthConn, []byte(validReq))
	var respUnauth wire.Response
	if err := json.Unmarshal(respRawUnauth, &respUnauth); err != nil {
		t.Fatalf("unmarshal respUnauth: %v", err)
	}
	if respUnauth.Error == nil || respUnauth.Error.Code != wire.ErrCodeUnauthorized {
		t.Errorf("expected unauthorized error, got %+v", respUnauth.Error)
	}

	// 3. actionID 不是 UUID
	invalidUUIDReq := `{
		"jsonrpc": "2.0",
		"method": "threads.actionStatus",
		"params": {
			"actionID": "not-a-valid-uuid"
		},
		"id": 2
	}`
	respRaw2 := d.Handle(ctx, conn, []byte(invalidUUIDReq))
	var resp2 wire.Response
	if err := json.Unmarshal(respRaw2, &resp2); err != nil {
		t.Fatalf("unmarshal resp2: %v", err)
	}
	if resp2.Error == nil || resp2.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for non-UUID actionID, got: %+v", resp2.Error)
	}

	// 4. actionID 缺失
	missingActionIDReq := `{
		"jsonrpc": "2.0",
		"method": "threads.actionStatus",
		"params": {},
		"id": 3
	}`
	respRaw3 := d.Handle(ctx, conn, []byte(missingActionIDReq))
	var resp3 wire.Response
	if err := json.Unmarshal(respRaw3, &resp3); err != nil {
		t.Fatalf("unmarshal resp3: %v", err)
	}
	if resp3.Error == nil || resp3.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams when actionID is missing, got: %+v", resp3.Error)
	}

	// 5. 额外未知字段（decodeStrict 校验）
	extraFieldReq := `{
		"jsonrpc": "2.0",
		"method": "threads.actionStatus",
		"params": {
			"actionID": "00000000-0000-0000-0000-000000000001",
			"extra": 123
		},
		"id": 4
	}`
	respRaw4 := d.Handle(ctx, conn, []byte(extraFieldReq))
	var resp4 wire.Response
	if err := json.Unmarshal(respRaw4, &resp4); err != nil {
		t.Fatalf("unmarshal resp4: %v", err)
	}
	if resp4.Error == nil || resp4.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for extra unknown fields, got: %+v", resp4.Error)
	}
}

func TestDispatch_ThreadsInterrupt(t *testing.T) {
	ctx := context.Background()
	var calledParams wire.InterruptParams
	backend := &fakeBackend{
		interruptFunc: func(ctx context.Context, params wire.InterruptParams) (wire.InterruptResult, error) {
			calledParams = params
			if params.TurnID == "00000000-0000-0000-0000-000000000099" {
				return wire.InterruptResult{}, &wire.RPCError{
					Code:    wire.ErrCodeTurnMismatch,
					Message: "turnID mismatch",
				}
			}
			return wire.InterruptResult{
				ThreadID: params.ThreadID,
				TurnID:   params.TurnID,
				State:    wire.InterruptStateStopping,
			}, nil
		},
	}
	d := NewDispatcher(&fakeAuth{}, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true, DeviceID: "dev-1"}}

	// 1. 成功中断
	validReq := `{
		"jsonrpc": "2.0",
		"method": "threads.interrupt",
		"params": {
			"threadID": "00000000-0000-0000-0000-000000000001",
			"turnID": "00000000-0000-0000-0000-000000000002"
		},
		"id": 1
	}`
	respRaw := d.Handle(ctx, conn, []byte(validReq))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if calledParams.ThreadID != "00000000-0000-0000-0000-000000000001" || calledParams.TurnID != "00000000-0000-0000-0000-000000000002" {
		t.Errorf("calledParams mismatch: %+v", calledParams)
	}

	// 2. 未认证连接拦截
	unauthConn := &fakeConn{}
	respRawUnauth := d.Handle(ctx, unauthConn, []byte(validReq))
	var respUnauth wire.Response
	if err := json.Unmarshal(respRawUnauth, &respUnauth); err != nil {
		t.Fatalf("unmarshal respUnauth: %v", err)
	}
	if respUnauth.Error == nil || respUnauth.Error.Code != wire.ErrCodeUnauthorized {
		t.Errorf("expected unauthorized error, got %+v", respUnauth.Error)
	}

	// 3. threadID 缺失或非 UUID
	invalidThreadReq := `{
		"jsonrpc": "2.0",
		"method": "threads.interrupt",
		"params": {
			"threadID": "not-a-uuid",
			"turnID": "00000000-0000-0000-0000-000000000002"
		},
		"id": 2
	}`
	respRawInvalidThread := d.Handle(ctx, conn, []byte(invalidThreadReq))
	var respInvalidThread wire.Response
	if err := json.Unmarshal(respRawInvalidThread, &respInvalidThread); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if respInvalidThread.Error == nil || respInvalidThread.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for invalid threadID, got: %+v", respInvalidThread.Error)
	}

	// 4. turnID 缺失或非 UUID
	invalidTurnReq := `{
		"jsonrpc": "2.0",
		"method": "threads.interrupt",
		"params": {
			"threadID": "00000000-0000-0000-0000-000000000001",
			"turnID": "invalid-turn-id"
		},
		"id": 3
	}`
	respRawInvalidTurn := d.Handle(ctx, conn, []byte(invalidTurnReq))
	var respInvalidTurn wire.Response
	if err := json.Unmarshal(respRawInvalidTurn, &respInvalidTurn); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if respInvalidTurn.Error == nil || respInvalidTurn.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for invalid turnID, got: %+v", respInvalidTurn.Error)
	}

	// 5. 严格解码拦截未知字段
	extraFieldReq := `{
		"jsonrpc": "2.0",
		"method": "threads.interrupt",
		"params": {
			"threadID": "00000000-0000-0000-0000-000000000001",
			"turnID": "00000000-0000-0000-0000-000000000002",
			"extraField": "forbidden"
		},
		"id": 4
	}`
	respRawExtra := d.Handle(ctx, conn, []byte(extraFieldReq))
	var respExtra wire.Response
	if err := json.Unmarshal(respRawExtra, &respExtra); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if respExtra.Error == nil || respExtra.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for extra field, got: %+v", respExtra.Error)
	}

	// 6. 后端返回特定错误码（如 ErrCodeTurnMismatch）透传
	mismatchReq := `{
		"jsonrpc": "2.0",
		"method": "threads.interrupt",
		"params": {
			"threadID": "00000000-0000-0000-0000-000000000001",
			"turnID": "00000000-0000-0000-0000-000000000099"
		},
		"id": 5
	}`
	respRawMismatch := d.Handle(ctx, conn, []byte(mismatchReq))
	var respMismatch wire.Response
	if err := json.Unmarshal(respRawMismatch, &respMismatch); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if respMismatch.Error == nil || respMismatch.Error.Code != wire.ErrCodeTurnMismatch {
		t.Errorf("expected ErrCodeTurnMismatch, got: %+v", respMismatch.Error)
	}
}

func TestDispatcher_ThreadsUpdate(t *testing.T) {
	ctx := context.Background()
	backend := &fakeBackend{}
	auth := &fakeAuth{}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	var receivedParams wire.ThreadUpdateParams
	backend.updateThreadFunc = func(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error) {
		receivedParams = params
		return wire.ThreadUpdateResult{
			Thread: wire.ThreadSummary{
				ID:          params.ThreadID,
				Title:       "Updated Title",
				RuntimeMode: "auto",
			},
		}, nil
	}

	// 1. 成功修改 runtimeMode 和 title
	req := `{
		"jsonrpc": "2.0",
		"method": "threads.update",
		"params": {
			"threadID": "33333333-3333-3333-3333-333333333333",
			"title": "Updated Title",
			"runtimeMode": "auto"
		},
		"id": 1
	}`
	respRaw := d.Handle(ctx, conn, []byte(req))
	var resp wire.Response
	if err := json.Unmarshal(respRaw, &resp); err != nil {
		t.Fatalf("unmarshal resp: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if receivedParams.ThreadID != "33333333-3333-3333-3333-333333333333" || receivedParams.Title == nil || *receivedParams.Title != "Updated Title" || receivedParams.RuntimeMode == nil || *receivedParams.RuntimeMode != "auto" {
		t.Errorf("received params mismatch: %+v", receivedParams)
	}
	resMap, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got: %T", resp.Result)
	}
	threadMap, ok := resMap["thread"].(map[string]any)
	if !ok || threadMap["id"] != "33333333-3333-3333-3333-333333333333" || threadMap["title"] != "Updated Title" || threadMap["runtimeMode"] != "auto" {
		t.Errorf("result mismatch: %+v", resMap)
	}

	// 2. 缺 threadID 返回 InvalidParams
	missingIDReq := `{
		"jsonrpc": "2.0",
		"method": "threads.update",
		"params": {
			"runtimeMode": "auto"
		},
		"id": 2
	}`
	respRaw2 := d.Handle(ctx, conn, []byte(missingIDReq))
	var resp2 wire.Response
	if err := json.Unmarshal(respRaw2, &resp2); err != nil {
		t.Fatalf("unmarshal resp2: %v", err)
	}
	if resp2.Error == nil || resp2.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams when threadID is missing, got: %+v", resp2.Error)
	}

	// 3. 非法 UUID threadID 返回 InvalidParams
	invalidIDReq := `{
		"jsonrpc": "2.0",
		"method": "threads.update",
		"params": {
			"threadID": "not-a-uuid",
			"runtimeMode": "auto"
		},
		"id": 3
	}`
	respRaw3 := d.Handle(ctx, conn, []byte(invalidIDReq))
	var resp3 wire.Response
	if err := json.Unmarshal(respRaw3, &resp3); err != nil {
		t.Fatalf("unmarshal resp3: %v", err)
	}
	if resp3.Error == nil || resp3.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams when threadID is not a UUID, got: %+v", resp3.Error)
	}

	// 4. 后端返回错误透传
	backend.updateThreadFunc = func(ctx context.Context, params wire.ThreadUpdateParams) (wire.ThreadUpdateResult, error) {
		return wire.ThreadUpdateResult{}, &wire.RPCError{
			Code:    wire.InvalidParams,
			Message: "invalid runtimeMode",
		}
	}
	respRaw4 := d.Handle(ctx, conn, []byte(req))
	var resp4 wire.Response
	if err := json.Unmarshal(respRaw4, &resp4); err != nil {
		t.Fatalf("unmarshal resp4: %v", err)
	}
	if resp4.Error == nil || resp4.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams from backend, got: %+v", resp4.Error)
	}
}

// TestDispatcher_RequestsRespond_RequiresHTTPS 验证当请求包含 isSecret 时，非 HTTPS(EntryTLS) 连接被拒绝 (-32012)。
func TestDispatcher_RequestsRespond_RequiresHTTPS(t *testing.T) {
	ctx := context.Background()
	secretReq := wire.RemoteRequest{
		ThreadID:  "11111111-1111-1111-1111-111111111111",
		RequestID: "req-secret-1",
		Questions: []wire.RemoteQuestion{
			{
				ID:       "q1",
				Prompt:   "Enter secret key",
				IsSecret: true,
			},
		},
	}

	backend := &fakeBackend{
		listRequestsFunc: func(ctx context.Context, threadID string) (wire.RequestsListResult, error) {
			return wire.RequestsListResult{
				ThreadID: threadID,
				Requests: []wire.RemoteRequest{secretReq},
			}, nil
		},
		respondRequestFunc: func(ctx context.Context, params wire.RequestsRespondParams) (wire.RequestsRespondResult, error) {
			return wire.RequestsRespondResult{
				ThreadID:  params.ThreadID,
				RequestID: params.RequestID,
				Kind:      "question",
				Status:    wire.RequestStatusAccepted,
			}, nil
		},
	}
	d := NewDispatcher(&fakeAuth{}, backend)

	respondBody := `{
		"jsonrpc": "2.0",
		"method": "requests.respond",
		"params": {
			"threadID": "11111111-1111-1111-1111-111111111111",
			"runID": "run-001",
			"requestID": "req-secret-1",
			"optionID": "submit"
		},
		"id": 1
	}`

	// 1. Entry 为 EntryLAN 时，应返回 -32012 (wire.ErrCodeRequiresHTTPS)
	lanConn := &fakeConn{
		info: ClientInfo{
			DeviceID:      "dev-lan",
			Authenticated: true,
			Entry:         EntryLAN,
		},
	}
	lanRespRaw := d.Handle(ctx, lanConn, []byte(respondBody))
	var lanResp wire.Response
	if err := json.Unmarshal(lanRespRaw, &lanResp); err != nil {
		t.Fatalf("unmarshal lanResp: %v", err)
	}
	if lanResp.Error == nil {
		t.Fatalf("expected error for isSecret request over EntryLAN, got success")
	}
	if lanResp.Error.Code != wire.ErrCodeRequiresHTTPS {
		t.Errorf("expected error code %d (-32012), got %d", wire.ErrCodeRequiresHTTPS, lanResp.Error.Code)
	}
	if lanResp.Error.Message != "Secret answers require the HTTPS entry" {
		t.Errorf("expected message 'Secret answers require the HTTPS entry', got %s", lanResp.Error.Message)
	}

	// 2. Entry 为 EntryTLS 时，应正常放行
	tlsConn := &fakeConn{
		info: ClientInfo{
			DeviceID:      "dev-tls",
			Authenticated: true,
			Entry:         EntryTLS,
		},
	}
	tlsRespRaw := d.Handle(ctx, tlsConn, []byte(respondBody))
	var tlsResp wire.Response
	if err := json.Unmarshal(tlsRespRaw, &tlsResp); err != nil {
		t.Fatalf("unmarshal tlsResp: %v", err)
	}
	if tlsResp.Error != nil {
		t.Fatalf("expected success for isSecret request over EntryTLS, got error: %+v", tlsResp.Error)
	}

	// 3. Entry 为 EntryTailscale 时，属于安全上下文，也应正常放行
	tailscaleConn := &fakeConn{
		info: ClientInfo{
			DeviceID:      "dev-tailscale",
			Authenticated: true,
			Entry:         EntryTailscale,
		},
	}
	tailscaleRespRaw := d.Handle(ctx, tailscaleConn, []byte(respondBody))
	var tailscaleResp wire.Response
	if err := json.Unmarshal(tailscaleRespRaw, &tailscaleResp); err != nil {
		t.Fatalf("unmarshal tailscaleResp: %v", err)
	}
	if tailscaleResp.Error != nil {
		t.Fatalf("expected success for isSecret request over EntryTailscale, got error: %+v", tailscaleResp.Error)
	}
}

// TestDispatcher_PushMethods 验证 push 三个方法的认证、参数校验及开启/关闭行为。
func TestDispatcher_PushMethods(t *testing.T) {
	ctx := context.Background()
	conn := &fakeConn{
		info: ClientInfo{
			DeviceID:      "dev-push-1",
			Authenticated: true,
			Entry:         EntryTLS,
		},
	}
	d := NewDispatcher(&fakeAuth{}, &fakeBackend{})

	// 1. push 为 nil 时，返回 MethodNotFound
	pubReq := `{"jsonrpc":"2.0","method":"push.publicKey","id":1}`
	raw := d.Handle(ctx, conn, []byte(pubReq))
	var resp wire.Response
	_ = json.Unmarshal(raw, &resp)
	if resp.Error == nil || resp.Error.Code != wire.MethodNotFound {
		t.Fatalf("expected MethodNotFound when push is nil, got: %+v", resp.Error)
	}

	// 2. 初始化 DeviceStore 与 WebPushService
	dir := t.TempDir()
	storePath := dir + "/devices.json"
	keyPath := dir + "/vapid.pem"
	now := time.Now()
	store, _ := NewDeviceStore(storePath, func() time.Time { return now })
	_ = store.Add(DeviceRecord{
		DeviceID:  "dev-push-1",
		Label:     "Browser",
		PairedAt:  now,
		TokenHash: HashToken("tok1"),
	})
	pushSvc, err := NewWebPushService(WebPushOptions{
		KeyPath: keyPath,
		Devices: store,
		Now:     func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewWebPushService failed: %v", err)
	}
	d.SetPush(pushSvc)

	// 3. push.publicKey：非空参数校验
	rawBadPub := d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"push.publicKey","params":{"invalid":1},"id":2}`))
	var respBadPub wire.Response
	_ = json.Unmarshal(rawBadPub, &respBadPub)
	if respBadPub.Error == nil || respBadPub.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for push.publicKey with extra params, got: %+v", respBadPub.Error)
	}

	// 4. push.publicKey：正常获取
	rawPub := d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"push.publicKey","id":3}`))
	var respPub wire.Response
	_ = json.Unmarshal(rawPub, &respPub)
	if respPub.Error != nil {
		t.Fatalf("push.publicKey failed: %+v", respPub.Error)
	}
	var pubResult wire.PushPublicKeyResult
	resultBytes, _ := json.Marshal(respPub.Result)
	_ = json.Unmarshal(resultBytes, &pubResult)
	if pubResult.PublicKey == "" {
		t.Fatalf("expected non-empty publicKey")
	}

	// 5. push.subscribe：参数校验
	rawBadSub := d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"push.subscribe","params":{"endpoint":""},"id":4}`))
	var respBadSub wire.Response
	_ = json.Unmarshal(rawBadSub, &respBadSub)
	if respBadSub.Error == nil || respBadSub.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for push.subscribe with empty endpoint, got: %+v", respBadSub.Error)
	}

	// 6. push.subscribe：正常订阅
	validP256dh := "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	validAuth := "BTBZMqHH6r4Tts7J_aSIgg"
	subBody := fmt.Sprintf(`{"jsonrpc":"2.0","method":"push.subscribe","params":{"endpoint":"https://fcm.googleapis.com/s/1","p256dh":%q,"auth":%q},"id":5}`, validP256dh, validAuth)
	rawSub := d.Handle(ctx, conn, []byte(subBody))
	var respSub wire.Response
	_ = json.Unmarshal(rawSub, &respSub)
	if respSub.Error != nil {
		t.Fatalf("push.subscribe failed: %+v", respSub.Error)
	}
	var subResult wire.PushSubscribeResult
	resSubBytes, _ := json.Marshal(respSub.Result)
	_ = json.Unmarshal(resSubBytes, &subResult)
	if !subResult.Subscribed {
		t.Fatalf("expected subscribed == true")
	}

	// 7. push.unsubscribe：参数校验
	rawBadUnsub := d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"push.unsubscribe","params":{"endpoint":""},"id":6}`))
	var respBadUnsub wire.Response
	_ = json.Unmarshal(rawBadUnsub, &respBadUnsub)
	if respBadUnsub.Error == nil || respBadUnsub.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for push.unsubscribe with empty endpoint, got: %+v", respBadUnsub.Error)
	}

	// 8. push.unsubscribe：正常注销
	rawUnsub := d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"push.unsubscribe","params":{"endpoint":"https://fcm.googleapis.com/s/1"},"id":7}`))
	var respUnsub wire.Response
	_ = json.Unmarshal(rawUnsub, &respUnsub)
	if respUnsub.Error != nil {
		t.Fatalf("push.unsubscribe failed: %+v", respUnsub.Error)
	}
	var unsubResult wire.PushUnsubscribeResult
	resUnsubBytes, _ := json.Marshal(respUnsub.Result)
	_ = json.Unmarshal(resUnsubBytes, &unsubResult)
	if !unsubResult.Unsubscribed {
		t.Fatalf("expected unsubscribed == true")
	}

	// 9. push.subscribe：端点不在已知推送服务白名单时拒绝（allowedPushHosts）
	rawUnknown := d.Handle(ctx, conn, []byte(fmt.Sprintf(`{"jsonrpc":"2.0","method":"push.subscribe","params":{"endpoint":"https://evil.example.com/s/1","p256dh":%q,"auth":%q},"id":8}`, validP256dh, validAuth)))
	var respUnknown wire.Response
	_ = json.Unmarshal(rawUnknown, &respUnknown)
	if respUnknown.Error == nil || respUnknown.Error.Code != wire.InvalidParams {
		t.Fatalf("expected InvalidParams for unknown push host, got: %+v", respUnknown.Error)
	}
}

func TestDispatcherPairInternalErrorIsNotLeaked(t *testing.T) {
	const devicesPath = "/Users/kai/Library/Application Support/mcode/remote_devices.json"
	auth := &fakeAuth{
		pairFunc: func(token, deviceName string, info ClientInfo) (PairResult, error) {
			return PairResult{}, fmt.Errorf("open %s: permission denied", devicesPath)
		},
	}
	d := NewDispatcher(auth, &fakeBackend{})
	conn := &fakeConn{info: ClientInfo{}}

	req := `{"jsonrpc":"2.0","method":"session.pair","params":{"token":"123456"},"id":1}`
	raw := d.Handle(context.Background(), conn, []byte(req))

	var resp wire.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal pair resp: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("expected error reply for internal pairing failure")
	}
	if resp.Error.Code != wire.InternalError {
		t.Fatalf("expected internal error code %d, got %d", wire.InternalError, resp.Error.Code)
	}
	// 未认证调用方不能看到内部错误原文（设备库绝对路径、系统错误）
	if strings.Contains(resp.Error.Message, devicesPath) || strings.Contains(resp.Error.Message, "permission denied") {
		t.Fatalf("internal error detail leaked to unauthenticated caller: %s", resp.Error.Message)
	}
	if conn.Info().Authenticated {
		t.Fatalf("connection must not be authenticated after failed pairing")
	}
}

func TestDispatcherPairBlockedAddress(t *testing.T) {
	auth := &fakeAuth{
		pairFunc: func(token, deviceName string, info ClientInfo) (PairResult, error) {
			return PairResult{}, ErrPairingBlocked
		},
	}
	d := NewDispatcher(auth, &fakeBackend{})
	conn := &fakeConn{info: ClientInfo{}}

	req := `{"jsonrpc":"2.0","method":"session.pair","params":{"token":"123456"},"id":1}`
	raw := d.Handle(context.Background(), conn, []byte(req))

	var resp wire.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal pair resp: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeUnauthorized {
		t.Fatalf("expected unauthorized error for blocked address, got %+v", resp.Error)
	}
	if conn.Info().Authenticated {
		t.Fatalf("connection must not be authenticated after blocked pairing")
	}
}

// TestDispatcherRenameDevice 覆盖 session.renameDevice：成功改名、空名被拒与未认证被拒。
func TestDispatcherRenameDevice(t *testing.T) {
	ctx := context.Background()
	const deviceID = "95a01908-6b43-40be-8d84-057bb3fa6826"

	// 1. 已认证连接只能改自己的设备：deviceID 取连接身份，名称去空白后回写连接标签
	conn := &fakeConn{}
	conn.MarkAuthenticated(deviceID, "旧名称")
	var gotDeviceID, gotName string
	auth := &fakeAuth{
		renameDeviceFunc: func(deviceID, name string) error {
			gotDeviceID, gotName = deviceID, name
			return nil
		},
	}
	d := NewDispatcher(auth, &fakeBackend{})
	req := `{"jsonrpc":"2.0","method":"session.renameDevice","params":{"name":"  新名称  "},"id":1}`
	raw := d.Handle(ctx, conn, []byte(req))
	var resp wire.Response
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal rename resp: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected rename error: %+v", resp.Error)
	}
	if gotDeviceID != deviceID || gotName != "新名称" {
		t.Fatalf("unexpected rename args: deviceID=%q name=%q", gotDeviceID, gotName)
	}
	if conn.Info().Label != "新名称" {
		t.Fatalf("connection label not refreshed: %q", conn.Info().Label)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok || result["deviceID"] != deviceID || result["label"] != "新名称" {
		t.Fatalf("unexpected rename result: %+v", resp.Result)
	}

	// 2. 空白名称与超过 64 字符的名称都返回参数错误，且不落库
	called := false
	auth.renameDeviceFunc = func(deviceID, name string) error {
		called = true
		return nil
	}
	for _, params := range []string{`{"name":"   "}`, `{"name":"` + strings.Repeat("长", 65) + `"}`} {
		raw = d.Handle(ctx, conn, []byte(`{"jsonrpc":"2.0","method":"session.renameDevice","params":`+params+`,"id":2}`))
		var badResp wire.Response
		if err := json.Unmarshal(raw, &badResp); err != nil {
			t.Fatalf("unmarshal bad rename resp: %v", err)
		}
		if badResp.Error == nil || badResp.Error.Code != wire.InvalidParams {
			t.Fatalf("expected InvalidParams for %s, got %+v", params, badResp.Error)
		}
	}
	if called {
		t.Fatalf("auth must not be called for invalid names")
	}

	// 3. 未认证连接被拒绝
	unauthConn := &fakeConn{}
	raw = d.Handle(ctx, unauthConn, []byte(req))
	var unauthResp wire.Response
	if err := json.Unmarshal(raw, &unauthResp); err != nil {
		t.Fatalf("unmarshal unauth rename resp: %v", err)
	}
	if unauthResp.Error == nil || unauthResp.Error.Code != wire.ErrCodeUnauthorized {
		t.Fatalf("expected unauthorized for unauthenticated rename, got %+v", unauthResp.Error)
	}
}
