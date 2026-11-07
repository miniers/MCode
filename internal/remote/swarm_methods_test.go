package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

type swarmRecordConn struct {
	fakeConn
	mu            sync.Mutex
	notifications []string
	states        []wire.SwarmState
}

func (c *swarmRecordConn) Notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = append(c.notifications, method)
	if st, ok := params.(wire.SwarmState); ok {
		c.states = append(c.states, st)
	}
	return nil
}

type mockSwarmService struct {
	mu         sync.Mutex
	states     map[string]swarm.State
	owners     map[string]string
	stopBeeErr error
	stopAllErr error
	clearErr   error
	calls      map[string]int
}

func newMockSwarmService() *mockSwarmService {
	return &mockSwarmService{
		states: make(map[string]swarm.State),
		owners: make(map[string]string),
		calls:  make(map[string]int),
	}
}

// Settings 返回假服务的全局 Swarm 设置：默认开关打开，保持旧测试的可用性预期。
func (m *mockSwarmService) Settings() swarm.Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["Settings"]++
	return swarm.Settings{Enabled: true}
}

func (m *mockSwarmService) SetEnabled(lead string, enabled bool) swarm.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["SetEnabled"]++
	st := m.states[lead]
	st.LeadThreadID = lead
	st.Enabled = enabled
	m.states[lead] = st
	return st
}

func (m *mockSwarmService) State(lead string) swarm.State {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["State"]++
	return m.states[lead]
}

func (m *mockSwarmService) BeeOwner(threadID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["BeeOwner"]++
	lead, ok := m.owners[threadID]
	return lead, ok
}

func (m *mockSwarmService) StopBee(ctx context.Context, lead, bee string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["StopBee"]++
	if m.stopBeeErr != nil {
		return m.stopBeeErr
	}
	return nil
}

func (m *mockSwarmService) StopAll(ctx context.Context, lead string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["StopAll"]++
	if m.stopAllErr != nil {
		return m.stopAllErr
	}
	return nil
}

func (m *mockSwarmService) ClearFinished(ctx context.Context, lead string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls["ClearFinished"]++
	if m.clearErr != nil {
		return m.clearErr
	}
	return nil
}

func executeSwarmRPC(t *testing.T, d *Dispatcher, conn Conn, reqJSON string) wire.Response {
	t.Helper()
	resRaw := d.Handle(context.Background(), conn, []byte(reqJSON))
	var resp wire.Response
	if err := json.Unmarshal(resRaw, &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v, raw: %s", err, string(resRaw))
	}
	return resp
}

// 1. 覆盖 5 个方法的成功路径和参数缺失
func TestSwarmMethods_SuccessAndInvalidParams(t *testing.T) {
	leadID := "11111111-1111-1111-1111-111111111111"
	beeID := "22222222-2222-2222-2222-222222222222"
	actionID := "33333333-3333-3333-3333-333333333333"

	mockSvc := newMockSwarmService()
	mockSvc.states[leadID] = swarm.State{
		LeadThreadID: leadID,
		Enabled:      true,
		Bees: []swarm.Bee{
			{
				ThreadID:  beeID,
				Index:     0,
				Name:      "Bee 1",
				Task:      "Test task",
				Status:    swarm.BeeStatus("running"),
				StartedAt: time.Now().UTC(),
			},
		},
	}

	auth := &fakeAuth{}
	backend := &fakeBackend{
		getThreadFunc: func(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
			if threadID == leadID || threadID == beeID {
				return wire.ThreadSummary{ID: threadID}, nil
			}
			return wire.ThreadSummary{}, &wire.RPCError{Code: wire.ErrCodeResourceNotFound, Message: "thread not found"}
		},
	}

	d := NewDispatcher(auth, backend)
	d.SetSwarm(mockSvc)
	conn := &swarmRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	// --- 1. threads.hydra.get ---
	// 参数缺失
	resp := executeSwarmRPC(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.get","params":{},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for missing threadID, got: %+v", resp.Error)
	}
	// 成功获取
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.get","params":{"threadID":"%s"},"id":2}`, leadID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var state wire.SwarmState
	rawResult, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(rawResult, &state); err != nil {
		t.Fatalf("unmarshal state failed: %v", err)
	}
	if !state.SwarmAvailable || !state.SwarmEnabled || state.IsBee {
		t.Errorf("unexpected swarm state: %+v", state)
	}
	if len(state.Bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(state.Bees))
	}

	// --- 2. threads.hydra.set ---
	// 缺 actionID
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.set","params":{"threadID":"%s","enabled":false},"id":3}`, leadID))
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for missing actionID, got: %+v", resp.Error)
	}
	// 成功 set
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.set","params":{"threadID":"%s","enabled":false,"actionID":"%s"},"id":4}`, leadID, actionID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	rawResult, _ = json.Marshal(resp.Result)
	_ = json.Unmarshal(rawResult, &state)
	if state.SwarmEnabled {
		t.Errorf("expected SwarmEnabled=false after set, got true")
	}

	// --- 3. threads.swarm.stopBee ---
	// 缺 beeThreadID
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.stopBee","params":{"threadID":"%s","actionID":"%s"},"id":5}`, leadID, actionID))
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for missing beeThreadID, got: %+v", resp.Error)
	}
	// 成功 stopBee
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.stopBee","params":{"threadID":"%s","beeThreadID":"%s","actionID":"%s"},"id":6}`, leadID, beeID, actionID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// --- 4. threads.hydra.stopAll ---
	// 缺 actionID
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.stopAll","params":{"threadID":"%s"},"id":7}`, leadID))
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for missing actionID, got: %+v", resp.Error)
	}
	// 成功 stopAll
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.stopAll","params":{"threadID":"%s","actionID":"%s"},"id":8}`, leadID, actionID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	// --- 5. threads.hydra.clearFinished ---
	// 缺 actionID
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.clearFinished","params":{"threadID":"%s"},"id":9}`, leadID))
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for missing actionID, got: %+v", resp.Error)
	}
	// 成功 clearFinished
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.clearFinished","params":{"threadID":"%s","actionID":"%s"},"id":10}`, leadID, actionID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
}

// 2. get 对 bee 线程返回 isBee=true 和 leadThreadID，并校验 summary 冲突与错误追加及 canStop
func TestSwarmMethods_GetBeeThread(t *testing.T) {
	leadID := "11111111-1111-1111-1111-111111111111"
	beeID := "22222222-2222-2222-2222-222222222222"
	now := time.Now().UTC()

	mockSvc := newMockSwarmService()
	mockSvc.owners[beeID] = leadID
	mockSvc.states[leadID] = swarm.State{
		LeadThreadID: leadID,
		Enabled:      true,
		Bees: []swarm.Bee{
			{
				ThreadID:      beeID,
				Index:         0,
				Name:          "Worker 1",
				Task:          "Conflict Task",
				Status:        swarm.BeeStatus("running"),
				Summary:       "Base summary",
				ConflictFiles: []string{"a.go", "b.go"},
				MergeError:    "Merge failed",
				StartedAt:     now,
			},
		},
	}

	auth := &fakeAuth{}
	backend := &fakeBackend{
		getThreadFunc: func(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
			return wire.ThreadSummary{ID: threadID}, nil
		},
	}

	d := NewDispatcher(auth, backend)
	d.SetSwarm(mockSvc)
	conn := &swarmRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}

	resp := executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.get","params":{"threadID":"%s"},"id":1}`, beeID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}

	var state wire.SwarmState
	rawResult, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(rawResult, &state); err != nil {
		t.Fatalf("unmarshal state failed: %v", err)
	}

	if !state.IsBee {
		t.Errorf("expected IsBee=true for bee thread, got false")
	}
	if state.LeadThreadID != leadID {
		t.Errorf("expected LeadThreadID=%s, got %s", leadID, state.LeadThreadID)
	}
	if state.Bee == nil {
		t.Fatalf("expected Bee pointer not nil")
	}
	if state.Bee.ThreadID != beeID {
		t.Errorf("expected Bee.ThreadID=%s, got %s", beeID, state.Bee.ThreadID)
	}
	if !state.Bee.CanStop {
		t.Errorf("expected CanStop=true for running bee, got false")
	}
	if !state.Bee.CanOpen {
		t.Errorf("expected CanOpen=true, got false")
	}
	// 冲突与合并错误改由独立字段承载，summary 保持管理器给的原文。
	if len(state.Bee.ConflictFiles) != 2 || state.Bee.ConflictFiles[0] != "a.go" || state.Bee.ConflictFiles[1] != "b.go" {
		t.Errorf("expected ConflictFiles=[a.go b.go], got %v", state.Bee.ConflictFiles)
	}
	if state.Bee.MergeError != "Merge failed" {
		t.Errorf("expected MergeError=%q, got %q", "Merge failed", state.Bee.MergeError)
	}
	if state.Bee.MergeState != "failed" {
		t.Errorf("expected MergeState=%q, got %q", "failed", state.Bee.MergeState)
	}
	if state.Bee.Summary != "Base summary" {
		t.Errorf("expected summary to stay untouched, got %q", state.Bee.Summary)
	}
}

// 3. stopBee 遇到 ErrBeeNotFound 时返回 not-found 错误码
func TestSwarmMethods_StopBee_NotFound(t *testing.T) {
	leadID := "11111111-1111-1111-1111-111111111111"
	beeID := "22222222-2222-2222-2222-222222222222"
	actionID := "33333333-3333-3333-3333-333333333333"

	mockSvc := newMockSwarmService()
	mockSvc.stopBeeErr = swarm.ErrBeeNotFound

	auth := &fakeAuth{}
	backend := &fakeBackend{
		getThreadFunc: func(ctx context.Context, threadID string) (wire.ThreadSummary, error) {
			return wire.ThreadSummary{ID: threadID}, nil
		},
	}

	d := NewDispatcher(auth, backend)
	d.SetSwarm(mockSvc)
	conn := &swarmRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}

	resp := executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.stopBee","params":{"threadID":"%s","beeThreadID":"%s","actionID":"%s"},"id":1}`, leadID, beeID, actionID))
	if resp.Error == nil {
		t.Fatalf("expected error, got nil")
	}
	if resp.Error.Code != wire.ErrCodeResourceNotFound {
		t.Errorf("expected ErrCodeResourceNotFound (%d), got: %d", wire.ErrCodeResourceNotFound, resp.Error.Code)
	}
}

// 4. swarm 未配置时 get 返回 swarmAvailable=false，其他方法返回 operation rejected
func TestSwarmMethods_Unavailable(t *testing.T) {
	leadID := "11111111-1111-1111-1111-111111111111"
	actionID := "33333333-3333-3333-3333-333333333333"

	auth := &fakeAuth{}
	backend := &fakeBackend{}

	d := NewDispatcher(auth, backend)
	conn := &swarmRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}

	// get 返回 SwarmAvailable=false, bees=[]
	resp := executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.get","params":{"threadID":"%s"},"id":1}`, leadID))
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	var state wire.SwarmState
	rawResult, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(rawResult, &state); err != nil {
		t.Fatalf("unmarshal state failed: %v", err)
	}
	if state.SwarmAvailable {
		t.Errorf("expected SwarmAvailable=false when unconfigured")
	}
	if state.Bees == nil || len(state.Bees) != 0 {
		t.Errorf("expected empty bees array, got: %+v", state.Bees)
	}

	// 其余方法返回 ErrCodeOperationRejected
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.set","params":{"threadID":"%s","enabled":true,"actionID":"%s"},"id":2}`, leadID, actionID))
	if resp.Error == nil || resp.Error.Code != wire.ErrCodeOperationRejected {
		t.Errorf("expected ErrCodeOperationRejected, got: %+v", resp.Error)
	}
}

// 5. swarmNotifyInterval 聚合测试：20ms 内连续调用三次，只收到一次通知
func TestSwarmMethods_NotifyAggregation(t *testing.T) {
	leadID := "11111111-1111-1111-1111-111111111111"

	mockSvc := newMockSwarmService()
	mockSvc.states[leadID] = swarm.State{
		LeadThreadID: leadID,
		Enabled:      true,
		Bees:         []swarm.Bee{},
	}

	auth := &fakeAuth{}
	backend := &fakeBackend{}

	d := NewDispatcher(auth, backend)
	d.SetSwarm(mockSvc)
	d.swarmNotifyInterval = 20 * time.Millisecond

	conn := &swarmRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	// 连续调用 3 次
	d.NotifySwarmChanged(leadID)
	d.NotifySwarmChanged(leadID)
	d.NotifySwarmChanged(leadID)

	// 等待聚合 timer 触发
	time.Sleep(50 * time.Millisecond)

	conn.mu.Lock()
	count := len(conn.notifications)
	var swarmChangedCount int
	for _, n := range conn.notifications {
		if n == "threads.swarm.changed" {
			swarmChangedCount++
		}
	}
	conn.mu.Unlock()

	if swarmChangedCount != 1 {
		t.Errorf("expected exactly 1 threads.swarm.changed notification, got %d (total notifications: %d)", swarmChangedCount, count)
	}
}

// 6. capabilities 与 providers.list 验证
func TestSwarmMethods_CapabilitiesAndProvidersList(t *testing.T) {
	// 验证 capabilities 包含 5 个新方法与 threads.swarm feature
	methods := ImplementedMethods()
	methodMap := make(map[string]bool)
	for _, m := range methods {
		methodMap[m] = true
	}
	expectedMethods := []string{
		"threads.swarm.get",
		"threads.swarm.set",
		"threads.swarm.stopBee",
		"threads.swarm.stopAll",
		"threads.swarm.clearFinished",
	}
	for _, em := range expectedMethods {
		if !methodMap[em] {
			t.Errorf("expected ImplementedMethods to contain %q", em)
		}
	}

	features := DefaultFeatures()
	foundFeature := false
	for _, f := range features {
		if f == "threads.swarm" {
			foundFeature = true
			break
		}
	}
	if !foundFeature {
		t.Errorf("expected DefaultFeatures to contain 'threads.swarm'")
	}

	// 验证 MemoryBackend 在 SetSwarm(true, ...) 时的 ListProviders 响应
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.RegisterHarness(&mockHarness{kind: "claude"})

	ctx := context.Background()

	// 未启用 swarm 时
	resUnavail, err := backend.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}
	if resUnavail.Swarm.Enabled {
		t.Errorf("expected Swarm.Enabled=false before SetSwarm")
	}

	// 启用 swarm，并注入一条以 claude 为主/执行侧的组合，以及一条引用未注册 provider 的组合
	backend.SetSwarm(true, func(threadID string) bool { return true })
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			MaxBees: 3,
			Pairs: []config.SwarmPair{
				{
					ID:             "pair-claude",
					Name:           "Claude 单机配对",
					LeadProvider:   "claude",
					LeadModel:      "opus",
					WorkerProvider: "claude",
					WorkerModel:    "opus",
				},
				{
					ID:           "pair-ghost",
					LeadProvider: "ghost",
				},
			},
		}
	})

	resAvail, err := backend.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}
	if !resAvail.Swarm.Enabled {
		t.Errorf("expected Swarm.Enabled=true after SetSwarm(true)")
	}
	if resAvail.Swarm.DefaultEnabled {
		t.Errorf("expected Swarm.DefaultEnabled=false")
	}
	// 组合来自配置：引用未注册 provider 的那条被过滤，只有 claude 那条下发。
	if len(resAvail.Swarm.Pairs) != 1 {
		t.Fatalf("expected 1 swarm pair, got %d: %+v", len(resAvail.Swarm.Pairs), resAvail.Swarm.Pairs)
	}
	pair := resAvail.Swarm.Pairs[0]
	if pair.ID != "pair-claude" || pair.Provider != "claude" {
		t.Errorf("expected pair-claude with provider claude, got: %+v", pair)
	}
	if pair.Name == nil || *pair.Name != "Claude 单机配对" {
		t.Errorf("expected Name='Claude 单机配对', got: %v", pair.Name)
	}
	if pair.LeadModel == nil || *pair.LeadModel != "opus" {
		t.Errorf("expected LeadModel='opus', got: %v", pair.LeadModel)
	}
	if pair.WorkerProvider == nil || *pair.WorkerProvider != "claude" {
		t.Errorf("expected WorkerProvider='claude', got: %v", pair.WorkerProvider)
	}
	if pair.WorkerModel == nil || *pair.WorkerModel != "opus" {
		t.Errorf("expected WorkerModel='opus', got: %v", pair.WorkerModel)
	}
	if pair.MaxBees == nil || *pair.MaxBees != 3 {
		t.Errorf("expected MaxBees=3, got: %+v", pair.MaxBees)
	}

	// MaxBees=0 表示不限，下发 nil。
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{Pairs: []config.SwarmPair{{ID: "pair-claude", LeadProvider: "claude"}}}
	})
	resUnlimited, err := backend.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}
	if len(resUnlimited.Swarm.Pairs) != 1 {
		t.Fatalf("expected 1 swarm pair, got %d: %+v", len(resUnlimited.Swarm.Pairs), resUnlimited.Swarm.Pairs)
	}
	if mh := resUnlimited.Swarm.Pairs[0].MaxBees; mh != nil {
		t.Errorf("expected MaxBees=nil when Swarm.MaxBees=0, got: %v", *mh)
	}
}

// 7. threads.list 的 swarmEnabled 反映实时状态，而不是创建会话时的快照。
func TestSwarmMethods_ListThreadsLiveSwarmEnabled(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	proj, err := backend.AddProjectPath(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath failed: %v", err)
	}
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	backend.SetSwarm(true, func(threadID string) bool { return threadID == created.ThreadID })

	list, err := backend.ListThreads(ctx, wire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	var found bool
	for _, s := range list.Threads {
		if s.ID != created.ThreadID {
			continue
		}
		found = true
		if !s.SwarmEnabled {
			t.Errorf("expected swarmEnabled=true for thread %s in list", s.ID)
		}
	}
	if !found {
		t.Fatalf("created thread %s not found in list", created.ThreadID)
	}

	summary, err := backend.GetThread(ctx, created.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !summary.SwarmEnabled {
		t.Errorf("expected swarmEnabled=true from GetThread")
	}
}

// 8. SwarmThreadContext 验证跳过 [Hydra] 与以 / 开头的消息，并正确返回首个合规 user 消息、配对 ID 与档位配置
func TestSwarmMethods_SwarmThreadContext(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	proj, err := backend.AddProjectPath(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath failed: %v", err)
	}

	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := created.ThreadID

	// 生效配对在读取时按模型推断：配对与当前主导/子代理模型一致时，会话显式选的配对仍然生效。
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			Pairs: []config.SwarmPair{
				{ID: "pair-test-1", LeadProvider: "claude", LeadModel: "opus", WorkerProvider: "codex", WorkerModel: "gpt-5"},
			},
		}
	})
	setThreadLeadModel(t, backend, threadID, "claude", "opus")

	if err := backend.SetThreadSwarmPairID(ctx, threadID, "pair-test-1"); err != nil {
		t.Fatalf("SetThreadSwarmPairID failed: %v", err)
	}
	expectedTiers := `{"fast":{"model":"gemini-flash"}}`
	if err := backend.SetThreadBeeTiers(ctx, threadID, expectedTiers); err != nil {
		t.Fatalf("SetThreadBeeTiers failed: %v", err)
	}
	if _, err := backend.SetThreadComposerPrefs(ctx, threadID, ThreadComposerPrefs{
		InteractionMode: "build", BeesProvider: "codex", BeesModel: "gpt-5", BeesEffort: "high",
	}); err != nil {
		t.Fatalf("SetThreadComposerPrefs failed: %v", err)
	}

	// 注入多条历史消息：
	// 1. user: "第一条旧消息"
	// 2. assistant: "回复 1"
	// 3. user: "目标用户指令"
	// 4. assistant: "执行中"
	// 5. user: "[Hydra] 内部调度消息"
	// 6. user: "   /slash command   "
	// 7. assistant: "完成"
	msgs := []wire.ThreadMessage{
		{ID: "m1", Role: "user", Text: "第一条旧消息"},
		{ID: "m2", Role: "assistant", Text: "回复 1"},
		{ID: "m3", Role: "user", Text: "目标用户指令"},
		{ID: "m4", Role: "assistant", Text: "执行中"},
		{ID: "m5", Role: "user", Text: "[Hydra] 内部调度消息"},
		{ID: "m6", Role: "user", Text: "   /slash command   "},
		{ID: "m7", Role: "assistant", Text: "完成"},
	}
	backend.mu.Lock()
	if td, ok := backend.threads[threadID]; ok {
		td.messages = append(td.messages, msgs...)
	}
	backend.mu.Unlock()

	userMsg, pairID, tiers, beeProvider, beeModel, beeEffort := backend.SwarmThreadContext(threadID)
	if userMsg != "目标用户指令" {
		t.Errorf("expected userMessage '目标用户指令', got %q", userMsg)
	}
	if pairID != "pair-test-1" {
		t.Errorf("expected pairID 'pair-test-1', got %q", pairID)
	}
	if tiers != expectedTiers {
		t.Errorf("expected beeTiers %q, got %q", expectedTiers, tiers)
	}
	if beeProvider != "codex" || beeModel != "gpt-5" {
		t.Errorf("expected bees model codex/gpt-5, got %q/%q", beeProvider, beeModel)
	}
	if beeEffort != "high" {
		t.Errorf("expected bees effort high, got %q", beeEffort)
	}
}

// 9. SetThreadBeeTiers 验证持久化与非合法 JSON 对象的拦截
func TestSwarmMethods_SetThreadBeeTiers(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "tiers.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	proj, err := backend.AddProjectPath(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath failed: %v", err)
	}
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := created.ThreadID

	// 9.1 非合法 JSON 对象应被拒绝
	invalidCases := []string{
		`not-json`,
		`[1, 2, 3]`,
		`"just a string"`,
		`12345`,
		`true`,
	}
	for _, tc := range invalidCases {
		err := backend.SetThreadBeeTiers(ctx, threadID, tc)
		if err == nil {
			t.Errorf("expected error for non-object JSON %q, got nil", tc)
		}
	}

	// 9.2 合法 JSON 对象更新并持久化
	validTiers := `{"fast":{"provider":"gemini","model":"flash"}}`
	if err := backend.SetThreadBeeTiers(ctx, threadID, validTiers); err != nil {
		t.Fatalf("SetThreadBeeTiers failed: %v", err)
	}

	th, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.SwarmBeeTiers != validTiers {
		t.Errorf("expected SwarmBeeTiers %q, got %q", validTiers, th.SwarmBeeTiers)
	}

	// 验证 store 中已落盘
	storedThread, err := s.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("store.GetThread failed: %v", err)
	}
	if storedThread.BeeTiers != validTiers {
		t.Errorf("expected store BeeTiers %q, got %q", validTiers, storedThread.BeeTiers)
	}

	// 9.3 允许清空为空字符串
	if err := backend.SetThreadBeeTiers(ctx, threadID, ""); err != nil {
		t.Fatalf("SetThreadBeeTiers empty string failed: %v", err)
	}
	th, _ = backend.GetThread(ctx, threadID)
	if th.SwarmBeeTiers != "" {
		t.Errorf("expected empty SwarmBeeTiers, got %q", th.SwarmBeeTiers)
	}
}

// 10. 验证配对变更清空档位覆盖配置
func TestSwarmMethods_ChangingPairClearsBeeTiers(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "pair_clear.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	defer s.Close()

	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	if err := backend.AttachStore(ctx, s); err != nil {
		t.Fatalf("AttachStore failed: %v", err)
	}

	proj, err := backend.AddProjectPath(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath failed: %v", err)
	}
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := created.ThreadID

	if err := backend.SetThreadSwarmPairID(ctx, threadID, "pair-A"); err != nil {
		t.Fatalf("SetThreadSwarmPairID failed: %v", err)
	}
	if err := backend.SetThreadBeeTiers(ctx, threadID, `{"fast":{"model":"flash"}}`); err != nil {
		t.Fatalf("SetThreadBeeTiers failed: %v", err)
	}

	// 设置相同 pairID，不应清空 beeTiers
	if err := backend.SetThreadSwarmPairID(ctx, threadID, "pair-A"); err != nil {
		t.Fatalf("SetThreadSwarmPairID same failed: %v", err)
	}
	th, _ := backend.GetThread(ctx, threadID)
	if th.SwarmBeeTiers != `{"fast":{"model":"flash"}}` {
		t.Errorf("expected beeTiers preserved on same pairID, got %q", th.SwarmBeeTiers)
	}

	// 变更到不同 pairID，应清空 beeTiers
	if err := backend.SetThreadSwarmPairID(ctx, threadID, "pair-B"); err != nil {
		t.Fatalf("SetThreadSwarmPairID different failed: %v", err)
	}
	th, _ = backend.GetThread(ctx, threadID)
	if th.SwarmBeeTiers != "" {
		t.Errorf("expected beeTiers cleared on pair change, got %q", th.SwarmBeeTiers)
	}
	stored, err := s.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("s.GetThread failed: %v", err)
	}
	if stored.BeeTiers != "" {
		t.Errorf("expected stored BeeTiers cleared, got %q", stored.BeeTiers)
	}
	// 显式选中的配对 ID 必须写穿 store，重新加载会话后仍保留
	if stored.SwarmPairID != "pair-B" {
		t.Errorf("expected stored SwarmPairID %q, got %q", "pair-B", stored.SwarmPairID)
	}

	// 清空配对后 store 应回读为空串
	if err := backend.SetThreadSwarmPairID(ctx, threadID, ""); err != nil {
		t.Fatalf("SetThreadSwarmPairID empty failed: %v", err)
	}
	storedCleared, err := s.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("s.GetThread after clear failed: %v", err)
	}
	if storedCleared.SwarmPairID != "" {
		t.Errorf("expected stored SwarmPairID cleared, got %q", storedCleared.SwarmPairID)
	}
}

// 11. 验证 wire 结构转换包含 Tier、ReadOnly 与 DirectModel 新增字段
func TestSwarmMethods_WireConversionIncludesNewBeeFields(t *testing.T) {
	st := swarm.State{
		LeadThreadID: "lead-001",
		Enabled:      true,
		Queued:       2,
		Bees: []swarm.Bee{
			{
				ThreadID:          "bee-001",
				Index:             1,
				Name:              "TestBee",
				Task:              "Code generation",
				Status:            swarm.StatusRunning,
				StartedAt:         time.Now().UTC(),
				Provider:          "claude",
				Model:             "sonnet",
				Tier:              "fast",
				ReadOnly:          true,
				DirectModel:       true,
				ShadowRepo:        true,
				SharedNoGit:       true,
				SharedNoGitReason: "fallback reason",
			},
		},
	}

	wireState := swarmStateToWire("lead-001", st, nil)
	if len(wireState.Bees) != 1 {
		t.Fatalf("expected 1 bee, got %d", len(wireState.Bees))
	}
	h := wireState.Bees[0]
	if h.Tier != "fast" {
		t.Errorf("expected Tier 'fast', got %q", h.Tier)
	}
	if !h.ReadOnly {
		t.Errorf("expected ReadOnly true, got %v", h.ReadOnly)
	}
	if !h.DirectModel {
		t.Errorf("expected DirectModel true, got %v", h.DirectModel)
	}
	if !h.ShadowRepo {
		t.Errorf("expected ShadowRepo true, got %v", h.ShadowRepo)
	}
	if !h.SharedNoGit {
		t.Errorf("expected SharedNoGit true, got %v", h.SharedNoGit)
	}
	if h.SharedNoGitReason != "fallback reason" {
		t.Errorf("expected SharedNoGitReason 'fallback reason', got %q", h.SharedNoGitReason)
	}
	if wireState.Queued != 2 {
		t.Errorf("expected Queued 2, got %d", wireState.Queued)
	}
}

// 11b. 验证 bee 线程转换时 queued 取所属 lead 的派发队列长度
func TestSwarmMethods_WireConversionQueuedOnBeeThread(t *testing.T) {
	leadID := "11111111-1111-1111-1111-111111111111"
	beeID := "22222222-2222-2222-2222-222222222222"

	mockSvc := newMockSwarmService()
	mockSvc.owners[beeID] = leadID
	mockSvc.states[leadID] = swarm.State{
		LeadThreadID: leadID,
		Enabled:      true,
		Queued:       2,
		Bees: []swarm.Bee{
			{
				ThreadID:  beeID,
				Index:     0,
				Name:      "Worker 1",
				Status:    swarm.StatusRunning,
				StartedAt: time.Now().UTC(),
			},
		},
	}

	// bee 线程自身的 State 没有队列信息，queued 必须来自所属 lead 的状态
	wireState := swarmStateToWire(beeID, mockSvc.State(beeID), mockSvc)
	if !wireState.IsBee {
		t.Fatalf("expected IsBee true, got false")
	}
	if wireState.Queued != 2 {
		t.Errorf("expected Queued 2 from lead state, got %d", wireState.Queued)
	}
}

// 12. 验证 threads.swarm.setBeeTiers RPC 方法
func TestSwarmMethods_RPCSetBeeTiers(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	proj, err := backend.AddProjectPath(ctx, t.TempDir())
	if err != nil {
		t.Fatalf("AddProjectPath failed: %v", err)
	}
	created, err := backend.CreateThread(ctx, wire.ThreadCreateParams{ProjectID: proj.ID})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := created.ThreadID

	mockSvc := newMockSwarmService()
	mockSvc.states[threadID] = swarm.State{LeadThreadID: threadID, Enabled: true}

	auth := &fakeAuth{}
	d := NewDispatcher(auth, backend)
	d.SetSwarm(mockSvc)
	conn := &swarmRecordConn{fakeConn: fakeConn{info: ClientInfo{Authenticated: true}}}
	d.addAuthedConn(conn)

	// 非法 UUID
	resp := executeSwarmRPC(t, d, conn, `{"jsonrpc":"2.0","method":"threads.swarm.setBeeTiers","params":{"threadID":"not-a-uuid","beeTiers":"{}"},"id":1}`)
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for invalid threadID, got: %+v", resp.Error)
	}

	// 非法 JSON
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.setBeeTiers","params":{"threadID":"%s","beeTiers":"not-json"},"id":2}`, threadID))
	if resp.Error == nil || resp.Error.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams for invalid beeTiers, got: %+v", resp.Error)
	}

	// 成功调用
	tiersJSON := `{"fast":{"provider":"gemini","model":"flash"}}`
	resp = executeSwarmRPC(t, d, conn, fmt.Sprintf(`{"jsonrpc":"2.0","method":"threads.swarm.setBeeTiers","params":{"threadID":"%s","beeTiers":%q},"id":3}`, threadID, tiersJSON))
	if resp.Error != nil {
		t.Fatalf("unexpected RPC error: %+v", resp.Error)
	}

	th, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.SwarmBeeTiers != tiersJSON {
		t.Errorf("expected thread SwarmBeeTiers %q, got %q", tiersJSON, th.SwarmBeeTiers)
	}
}

// 13. 验证 SwarmProviderEnabled
func TestSwarmMethods_SwarmProviderEnabled(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if backend.SwarmProviderEnabled("claude") {
		t.Errorf("expected claude to not be enabled initially")
	}

	backend.RegisterHarness(&mockHarness{kind: "claude"})
	if !backend.SwarmProviderEnabled("claude") {
		t.Errorf("expected claude to be enabled after registration")
	}
}

// 14. 验证 bee 的 mergeState 优先级：pending > discarded > merged > failed > 空
func TestSwarmMethods_WireMergeStatePriority(t *testing.T) {
	cases := []struct {
		name string
		bee  swarm.Bee
		want string
	}{
		{
			name: "待审查优先于其他状态",
			bee:  swarm.Bee{PendingMerge: true, MergeDiscarded: true, Merged: true, AutoMergeState: "failed"},
			want: "pending",
		},
		{
			name: "已放弃优先于已合入",
			bee:  swarm.Bee{MergeDiscarded: true, Merged: true},
			want: "discarded",
		},
		{
			name: "已合入优先于失败",
			bee:  swarm.Bee{Merged: true, MergeError: "冲突"},
			want: "merged",
		},
		{
			name: "手动合并失败",
			bee:  swarm.Bee{MergeError: "冲突"},
			want: "failed",
		},
		{
			name: "自动合并阶段失败",
			bee:  swarm.Bee{AutoMergeState: "failed"},
			want: "failed",
		},
		{
			name: "自动合并错误说明",
			bee:  swarm.Bee{AutoMergeError: "推送失败"},
			want: "failed",
		},
		{
			name: "未进入合并流程",
			bee:  swarm.Bee{Status: swarm.StatusRunning},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := swarmBeeToWire(tc.bee).MergeState; got != tc.want {
				t.Fatalf("mergeState = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSwarmBeeToWireTimeMilliseconds(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*60*60)
	started := time.Date(2026, 11, 6, 12, 34, 56, 123456789, zone)
	finished := time.Date(2026, 11, 6, 12, 35, 1, 45000000, zone)
	got := swarmBeeToWire(swarm.Bee{StartedAt: started, FinishedAt: &finished})
	if got.StartedAt != "2026-11-06T04:34:56.123Z" {
		t.Errorf("startedAt = %q", got.StartedAt)
	}
	if got.FinishedAt == nil || *got.FinishedAt != "2026-11-06T04:35:01.045Z" {
		t.Errorf("finishedAt = %v", got.FinishedAt)
	}
}

// 15. 验证 mergeError 在手动合并错误为空时回退到自动合并错误
func TestSwarmMethods_WireMergeErrorFallback(t *testing.T) {
	bee := swarm.Bee{AutoMergeError: "推送失败"}
	if got := swarmBeeToWire(bee).MergeError; got != "推送失败" {
		t.Fatalf("mergeError = %q, want %q", got, "推送失败")
	}

	bee = swarm.Bee{MergeError: "工作区冲突", AutoMergeError: "推送失败"}
	if got := swarmBeeToWire(bee).MergeError; got != "工作区冲突" {
		t.Fatalf("mergeError = %q, want %q", got, "工作区冲突")
	}
}

// 16. 合并前审查状态参与 mergeState 汇总：审查中、审查未通过优先，其余分支保持原有映射
func TestSwarmMergeState_ReviewStates(t *testing.T) {
	cases := []struct {
		name string
		bee  swarm.Bee
		want string
	}{
		{
			name: "审查中",
			bee:  swarm.Bee{ReviewState: swarm.ReviewRunning},
			want: "reviewing",
		},
		{
			name: "审查未通过",
			bee:  swarm.Bee{ReviewState: swarm.ReviewRejected},
			want: "rejected",
		},
		{
			name: "待人工审查仍映射 pending",
			bee:  swarm.Bee{ReviewState: swarm.ReviewNeedsHuman, PendingMerge: true},
			want: "pending",
		},
		{
			name: "未进入审查时保持 pending",
			bee:  swarm.Bee{PendingMerge: true},
			want: "pending",
		},
		{
			name: "未进入审查时保持 merged",
			bee:  swarm.Bee{Merged: true},
			want: "merged",
		},
		{
			name: "未进入审查时保持 discarded",
			bee:  swarm.Bee{MergeDiscarded: true},
			want: "discarded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := swarmMergeState(tc.bee); got != tc.want {
				t.Fatalf("mergeState = %q, want %q", got, tc.want)
			}
		})
	}
}

// 17. 审查结论字段原样带到 wire 层，供远程客户端展示
func TestSwarmBeeToWire_ReviewFields(t *testing.T) {
	bee := swarm.Bee{
		ThreadID:              "bee-1",
		ReviewState:           swarm.ReviewNeedsHuman,
		ReviewReason:          "改动缺少测试",
		ReviewThreadID:        "review-thread-1",
		ReviewLayer:           "l2",
		ReviewConcerns:        []string{"internal/a.go:12 缺少错误处理"},
		ReviewTokens:          3200,
		ReviewTokensEstimated: true,
	}

	got := swarmBeeToWire(bee)
	if got.ReviewState != string(swarm.ReviewNeedsHuman) {
		t.Fatalf("reviewState = %q, want %q", got.ReviewState, swarm.ReviewNeedsHuman)
	}
	if got.ReviewReason != "改动缺少测试" {
		t.Fatalf("reviewReason = %q, want %q", got.ReviewReason, "改动缺少测试")
	}
	if got.ReviewThreadID != "review-thread-1" {
		t.Fatalf("reviewThreadId = %q, want %q", got.ReviewThreadID, "review-thread-1")
	}
	if got.ReviewLayer != "l2" || got.ReviewTokens != 3200 || !got.ReviewTokensEstimated || len(got.ReviewConcerns) != 1 {
		t.Fatalf("漏斗字段未映射: %+v", got)
	}
}

// applyReviewFunnelWire 写入四个漏斗字段，concerns 必须是深拷贝。
func TestApplyReviewFunnelWire(t *testing.T) {
	concerns := []string{"internal/a.go:12 缺少错误处理", "internal/b.go:7 未覆盖分支"}
	var bee wire.SwarmBee
	applyReviewFunnelWire(&bee, "l2", concerns, 1234, true)

	if bee.ReviewLayer != "l2" || bee.ReviewTokens != 1234 || !bee.ReviewTokensEstimated {
		t.Fatalf("漏斗字段未写入: %+v", bee)
	}
	if len(bee.ReviewConcerns) != 2 || bee.ReviewConcerns[0] != concerns[0] {
		t.Fatalf("concerns = %v, 期望 %v", bee.ReviewConcerns, concerns)
	}

	concerns[0] = "被改写的值"
	if bee.ReviewConcerns[0] == concerns[0] {
		t.Fatalf("concerns 未深拷贝，源切片改写后污染了 wire 快照")
	}

	// 空 concerns 写成 nil/空切片都行，序列化后都是省略；nil 目标不 panic。
	applyReviewFunnelWire(nil, "l0", nil, 0, false)
	empty := wire.SwarmBee{}
	applyReviewFunnelWire(&empty, "l1", nil, 0, false)
	if len(empty.ReviewConcerns) != 0 {
		t.Fatalf("空 concerns 应保持空，得到 %v", empty.ReviewConcerns)
	}
}
