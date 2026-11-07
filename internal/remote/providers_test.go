package remote

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"mcode/internal/config"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func strPtr(s string) *string {
	return &s
}

func waitForTurnDone(t *testing.T, b *MemoryBackend, threadID string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		th, err := b.GetThread(ctx, threadID)
		if err == nil && !th.IsRunning {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for thread %s turn to complete", threadID)
}

func TestProviders_MultiHarnessSelectionAndDefault(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	proj := wire.ProjectSummary{ID: newUUID(), Name: "test-proj", Path: "/tmp"}
	backend.AddProject(proj)

	harnessA := &mockHarness{kind: "a"}
	harnessB := &mockHarness{kind: "b"}

	backend.RegisterHarness(harnessA)
	backend.RegisterHarness(harnessB)

	// 1. 未指定 Provider 时使用默认 Provider "a"
	resDef, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread with default provider failed: %v", err)
	}
	thDef, err := backend.GetThread(ctx, resDef.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thDef.Provider != "a" {
		t.Errorf("expected default provider 'a', got %q", thDef.Provider)
	}

	// 2. 指定 Provider 为 "b" 创建会话
	resB, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Options: &wire.ComposerOptions{
			Provider: strPtr("b"),
		},
	})
	if err != nil {
		t.Fatalf("CreateThread with provider 'b' failed: %v", err)
	}
	thB, err := backend.GetThread(ctx, resB.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thB.Provider != "b" {
		t.Errorf("expected provider 'b', got %q", thB.Provider)
	}

	// 3. 给 thB 发送消息，验证只有 harnessB 被调用
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: resB.ThreadID,
		Text:     "hello to b",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage to thB failed: %v", err)
	}
	waitForTurnDone(t, backend, resB.ThreadID)

	harnessA.mu.Lock()
	aCalls := len(harnessA.turnCalls)
	harnessA.mu.Unlock()

	harnessB.mu.Lock()
	bCalls := len(harnessB.turnCalls)
	harnessB.mu.Unlock()

	if aCalls != 0 {
		t.Errorf("expected harness A calls to be 0, got %d", aCalls)
	}
	if bCalls != 1 {
		t.Errorf("expected harness B calls to be 1, got %d", bCalls)
	}

	// 4. SendMessage 中若指定了不同 provider，应被忽略，依然走会话的 provider
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: resB.ThreadID,
		Text:     "another message",
		ActionID: newUUID(),
		Options: &wire.ComposerOptions{
			Provider: strPtr("a"), // 与会话 provider "b" 不同，应被忽略
		},
	})
	if err != nil {
		t.Fatalf("SendMessage with conflicting provider failed: %v", err)
	}
	waitForTurnDone(t, backend, resB.ThreadID)

	harnessB.mu.Lock()
	bCallsAfter := len(harnessB.turnCalls)
	harnessB.mu.Unlock()
	if bCallsAfter != 2 {
		t.Errorf("expected harness B calls to be 2, got %d", bCallsAfter)
	}

	// 5. 给 thDef 发送消息，验证 harnessA 被调用
	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: resDef.ThreadID,
		Text:     "hello to def",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage to thDef failed: %v", err)
	}
	waitForTurnDone(t, backend, resDef.ThreadID)

	harnessA.mu.Lock()
	aCallsAfter := len(harnessA.turnCalls)
	harnessA.mu.Unlock()
	if aCallsAfter != 1 {
		t.Errorf("expected harness A calls to be 1, got %d", aCallsAfter)
	}
}

func TestProviders_UnknownProviderError(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	proj := wire.ProjectSummary{ID: newUUID(), Name: "test-proj", Path: "/tmp"}
	backend.AddProject(proj)

	backend.RegisterHarness(&mockHarness{kind: "a"})

	// 指定未注册的 provider "unknown-p"
	_, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Options: &wire.ComposerOptions{
			Provider: strPtr("unknown-p"),
		},
	})
	if err == nil {
		t.Fatalf("expected error for unknown provider, got nil")
	}

	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("expected *wire.RPCError, got: %T (%v)", err, err)
	}
	if rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected RPC error code %d, got %d", wire.InvalidParams, rpcErr.Code)
	}
	if !strings.Contains(rpcErr.Message, "unknown-p") {
		t.Errorf("expected error message to contain 'unknown-p', got: %s", rpcErr.Message)
	}
}

func TestProviders_ListViaDispatcherAndSchemaValidation(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{kind: "claude"})
	backend.RegisterHarness(&mockHarness{kind: "codex"})

	// 设置默认 provider 为 codex，使得 codex 排在第一位
	if err := backend.SetDefaultProvider("codex"); err != nil {
		t.Fatalf("SetDefaultProvider failed: %v", err)
	}

	auth := &fakeAuth{}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	req := `{"jsonrpc":"2.0","method":"providers.list","id":1}`
	respRaw := d.Handle(ctx, conn, []byte(req))

	var rawMap map[string]any
	if err := json.Unmarshal(respRaw, &rawMap); err != nil {
		t.Fatalf("unmarshal response JSON failed: %v", err)
	}
	if rawMap["error"] != nil {
		t.Fatalf("unexpected RPC error: %+v", rawMap["error"])
	}

	resultMap, ok := rawMap["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %T", rawMap["result"])
	}

	// 校验 swarm 结构
	swarmMap, ok := resultMap["swarm"].(map[string]any)
	if !ok {
		t.Fatalf("expected swarm object in result")
	}
	if _, ok := swarmMap["enabled"].(bool); !ok {
		t.Errorf("missing or invalid 'enabled' in swarm")
	}
	if _, ok := swarmMap["defaultEnabled"].(bool); !ok {
		t.Errorf("missing or invalid 'defaultEnabled' in swarm")
	}
	if _, ok := swarmMap["pairs"].([]any); !ok {
		t.Errorf("missing or invalid 'pairs' in swarm")
	}

	// 校验 providers 列表及排序
	providersList, ok := resultMap["providers"].([]any)
	if !ok {
		t.Fatalf("expected providers array, got %T", resultMap["providers"])
	}
	if len(providersList) != 2 {
		t.Fatalf("expected 2 providers, got %d", len(providersList))
	}

	first := providersList[0].(map[string]any)
	second := providersList[1].(map[string]any)

	// 默认的 codex 应排在第 0 项，其余按字典序
	if first["id"] != "codex" || first["name"] != "Codex" {
		t.Errorf("expected first provider to be codex / Codex, got id=%v name=%v", first["id"], first["name"])
	}
	if second["id"] != "claude" || second["name"] != "Claude Code" {
		t.Errorf("expected second provider to be claude / Claude Code, got id=%v name=%v", second["id"], second["name"])
	}

	// 验证 schema 要求字段
	for i, item := range []map[string]any{first, second} {
		if item["auth"] != "signedIn" {
			t.Errorf("provider %d auth mismatch: %v", i, item["auth"])
		}
		if item["supportsPlanMode"] != true {
			t.Errorf("provider %d supportsPlanMode mismatch: %v", i, item["supportsPlanMode"])
		}
		if item["available"] != true {
			t.Errorf("provider %d available mismatch: %v", i, item["available"])
		}
		if _, ok := item["models"].([]any); !ok {
			t.Errorf("provider %d models should be an array", i)
		}
		if item["unavailableReason"] != nil {
			t.Errorf("provider %d unavailableReason should be null", i)
		}
	}
}

func TestProviders_LegacySetHarness(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	ctx := context.Background()
	proj := wire.ProjectSummary{ID: newUUID(), Name: "test-proj", Path: "/tmp"}
	backend.AddProject(proj)

	legacyMock := &mockHarness{kind: "custom-legacy"}
	backend.SetHarness(legacyMock, "/custom/dir", harness.ModeAgent)

	providers := backend.Providers()
	if len(providers) != 1 || providers[0] != "custom-legacy" {
		t.Fatalf("expected ['custom-legacy'], got %v", providers)
	}

	res, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	th, err := backend.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.Provider != "custom-legacy" {
		t.Errorf("expected provider 'custom-legacy', got %q", th.Provider)
	}

	_, err = backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: res.ThreadID,
		Text:     "hello legacy",
		ActionID: newUUID(),
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitForTurnDone(t, backend, res.ThreadID)

	legacyMock.mu.Lock()
	calls := len(legacyMock.turnCalls)
	legacyMock.mu.Unlock()
	if calls != 1 {
		t.Errorf("expected legacyMock to be called once, got %d", calls)
	}
}

func TestProviders_WebSocketDisconnectRemovesHubSubscription(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	auth := &fakeAuth{}
	dispatcher := NewDispatcher(auth, backend)

	server := NewServer(ServerOptions{
		Assets:       testAssets,
		Entry:        EntryLAN,
		AllowedHosts: []string{"127.0.0.1", "localhost"},
		Handler:      dispatcher,
	})
	ts := httptest.NewServer(server)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	wsURL := strings.Replace(ts.URL, "http://", "ws://", 1) + "/ws"
	wsClient, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial failed: %v", err)
	}

	// 查找服务端内部对应的 wsConn 实例
	var srvConn Conn
	for i := 0; i < 50; i++ {
		server.connMu.RLock()
		for sc := range server.conns {
			srvConn = sc
			break
		}
		server.connMu.RUnlock()
		if srvConn != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if srvConn == nil {
		t.Fatalf("server did not record websocket connection")
	}

	// 创建项目与会话并订阅
	proj := wire.ProjectSummary{ID: newUUID(), Name: "test-proj", Path: "/tmp"}
	backend.AddProject(proj)
	thRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	subRes, err := backend.Subscribe(ctx, srvConn, thRes.ThreadID)
	if err != nil || !subRes.Subscribed {
		t.Fatalf("Subscribe failed: %v, subRes=%+v", err, subRes)
	}

	// 验证 hub 内部已存在该订阅者
	hub.mu.Lock()
	st, ok := hub.threads[thRes.ThreadID]
	_, hasSubBefore := st.subscribers[srvConn]
	hub.mu.Unlock()

	if !ok || !hasSubBefore {
		t.Fatalf("expected srvConn to be subscribed in hub before disconnect")
	}

	// 客户端主动断开 WebSocket 连接
	_ = wsClient.Close(websocket.StatusNormalClosure, "disconnect test")

	// 等待服务端 readPump 退出并调用 Close -> RemoveConn
	var removed bool
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		hub.mu.Lock()
		st, ok := hub.threads[thRes.ThreadID]
		if ok {
			_, stillPresent := st.subscribers[srvConn]
			removed = !stillPresent
		} else {
			removed = true
		}
		hub.mu.Unlock()
		if removed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !removed {
		t.Errorf("expected hub subscription to be removed after websocket disconnected")
	}
}

func TestProviders_SwarmAvailableInListProviders(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{kind: "codex"})
	backend.RegisterHarness(&mockHarness{kind: "claude"})
	backend.SetSwarm(true, func(threadID string) bool { return false })
	// 编排组合直接来自配置：一条 codex、一条 claude，另有一条引用未注册 provider。
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			MaxBees: 4,
			Pairs: []config.SwarmPair{
				{ID: "pair-codex", Name: "Codex 单机", LeadProvider: "codex", WorkerProvider: "codex"},
				{ID: "pair-claude", LeadProvider: "claude", WorkerProvider: "claude"},
				{ID: "pair-ghost", LeadProvider: "ghost", WorkerProvider: "ghost"},
			},
		}
	})

	auth := &fakeAuth{}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}
	ctx := context.Background()

	req := `{"jsonrpc":"2.0","method":"providers.list","id":1}`
	respRaw := d.Handle(ctx, conn, []byte(req))

	var rawMap map[string]any
	if err := json.Unmarshal(respRaw, &rawMap); err != nil {
		t.Fatalf("unmarshal response JSON failed: %v", err)
	}
	resultMap := rawMap["result"].(map[string]any)
	swarmMap := resultMap["swarm"].(map[string]any)

	if swarmMap["enabled"] != true {
		t.Errorf("expected swarm.enabled=true, got %v", swarmMap["enabled"])
	}
	if swarmMap["defaultEnabled"] != false {
		t.Errorf("expected swarm.defaultEnabled=false, got %v", swarmMap["defaultEnabled"])
	}
	pairs := swarmMap["pairs"].([]any)
	// 配置里的组合原样下发，引用未注册 provider 的那条被过滤。
	if len(pairs) != 2 {
		t.Fatalf("expected 2 swarm pairs, got %d: %v", len(pairs), pairs)
	}
	wantPairProviders := map[string]string{"pair-codex": "codex", "pair-claude": "claude"}
	for _, item := range pairs {
		pm, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("unexpected swarm pair entry: %v", item)
		}
		id, _ := pm["id"].(string)
		wantProvider, known := wantPairProviders[id]
		if !known {
			t.Fatalf("unexpected swarm pair id %q: %v", id, pm)
		}
		delete(wantPairProviders, id)
		if pm["provider"] != wantProvider {
			t.Errorf("expected pair %q provider=%q, got %v", id, wantProvider, pm["provider"])
		}
		if pm["workerProvider"] != wantProvider {
			t.Errorf("expected pair %q workerProvider=%q, got %v", id, wantProvider, pm["workerProvider"])
		}
		if pm["maxBees"] != float64(4) {
			t.Errorf("expected pair %q maxBees=4, got %v", id, pm["maxBees"])
		}
	}
	if len(wantPairProviders) != 0 {
		t.Errorf("missing swarm pairs in response: %v", wantPairProviders)
	}

	// 图片附件能力：claude / codex 支持，模拟器不支持
	providersList := resultMap["providers"].([]any)
	if len(providersList) == 0 {
		t.Fatalf("expected registered providers in list")
	}
	for _, p := range providersList {
		pm, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("unexpected provider entry: %v", p)
		}
		id, _ := pm["id"].(string)
		want := id == "claude" || id == "codex"
		if pm["supportsImages"] != want {
			t.Errorf("expected supportsImages=%v for provider %q, got %v", want, id, pm["supportsImages"])
		}
	}
}

type availabilityMockHarness struct {
	mockHarness
	available bool
	reason    string
}

func (m *availabilityMockHarness) Available() (bool, string) {
	return m.available, m.reason
}

func TestProviders_AvailabilityAndEmptyCatalogModels(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	clineHarness := &availabilityMockHarness{
		mockHarness: mockHarness{kind: "cline"},
		available:   false,
		reason:      "cline not found",
	}
	openaiHarness := &availabilityMockHarness{
		mockHarness: mockHarness{kind: "openai"},
		available:   true,
		reason:      "",
	}

	backend.RegisterHarness(clineHarness)
	backend.RegisterHarness(openaiHarness)
	backend.SetSwarm(true, func(threadID string) bool { return false })
	// 编排组合直接来自配置：openai 两侧都可用因此下发，cline 不可用因此整条被过滤。
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			Pairs: []config.SwarmPair{
				{
					ID:             "pair-openai",
					Name:           "OpenAI 配对",
					LeadProvider:   "openai",
					LeadModel:      "gpt-4o",
					WorkerProvider: "openai",
					WorkerModel:    "gpt-4o-mini",
				},
				{
					ID:             "pair-cline",
					LeadProvider:   "cline",
					WorkerProvider: "cline",
				},
			},
		}
	})

	ctx := context.Background()
	resDirect, err := backend.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}

	var foundDirectCline, foundDirectOpenAI *wire.Provider
	for i := range resDirect.Providers {
		p := &resDirect.Providers[i]
		if p.ID == "cline" {
			foundDirectCline = p
		} else if p.ID == "openai" {
			foundDirectOpenAI = p
		}
	}
	if foundDirectCline == nil {
		t.Fatalf("expected cline provider in direct list")
	}
	if foundDirectOpenAI == nil {
		t.Fatalf("expected openai provider in direct list")
	}
	if foundDirectCline.Name != "Cline" {
		t.Errorf("expected cline name 'Cline', got %q", foundDirectCline.Name)
	}
	if foundDirectCline.Available != false {
		t.Errorf("expected cline Available=false, got %v", foundDirectCline.Available)
	}
	if foundDirectCline.UnavailableReason == nil || *foundDirectCline.UnavailableReason != "cline not found" {
		t.Errorf("expected cline UnavailableReason='cline not found', got %v", foundDirectCline.UnavailableReason)
	}
	if foundDirectCline.Auth != "signedOut" {
		t.Errorf("expected cline Auth='signedOut', got %q", foundDirectCline.Auth)
	}
	if !foundDirectCline.SupportsImages {
		t.Errorf("expected cline SupportsImages=true")
	}

	if foundDirectOpenAI.Name != "OpenAI" {
		t.Errorf("expected openai name 'OpenAI', got %q", foundDirectOpenAI.Name)
	}
	if foundDirectOpenAI.Available != true {
		t.Errorf("expected openai Available=true, got %v", foundDirectOpenAI.Available)
	}
	if foundDirectOpenAI.UnavailableReason != nil {
		t.Errorf("expected openai UnavailableReason=nil, got %v", foundDirectOpenAI.UnavailableReason)
	}
	if foundDirectOpenAI.Auth != "signedIn" {
		t.Errorf("expected openai Auth='signedIn', got %q", foundDirectOpenAI.Auth)
	}
	if !foundDirectOpenAI.SupportsImages {
		t.Errorf("expected openai SupportsImages=true")
	}

	// 且 swarmAvailable 为 true 时下发的组合与配置一一对应：只含 openai 那条，不含 cline
	if len(resDirect.Swarm.Pairs) != 1 {
		t.Fatalf("expected 1 swarm pair, got %d: %+v", len(resDirect.Swarm.Pairs), resDirect.Swarm.Pairs)
	}
	openaiPair := resDirect.Swarm.Pairs[0]
	if openaiPair.ID != "pair-openai" || openaiPair.Provider != "openai" {
		t.Errorf("expected pair-openai with provider openai, got: %+v", openaiPair)
	}
	if openaiPair.Name == nil || *openaiPair.Name != "OpenAI 配对" {
		t.Errorf("expected Name='OpenAI 配对', got: %v", openaiPair.Name)
	}
	if openaiPair.LeadModel == nil || *openaiPair.LeadModel != "gpt-4o" {
		t.Errorf("expected LeadModel='gpt-4o', got: %v", openaiPair.LeadModel)
	}
	if openaiPair.WorkerProvider == nil || *openaiPair.WorkerProvider != "openai" {
		t.Errorf("expected WorkerProvider='openai', got: %v", openaiPair.WorkerProvider)
	}
	if openaiPair.WorkerModel == nil || *openaiPair.WorkerModel != "gpt-4o-mini" {
		t.Errorf("expected WorkerModel='gpt-4o-mini', got: %v", openaiPair.WorkerModel)
	}
	// MaxBees 未配置（0）表示不限，下发 nil。
	if openaiPair.MaxBees != nil {
		t.Errorf("expected MaxBees=nil when Swarm.MaxBees=0, got: %v", *openaiPair.MaxBees)
	}
	for _, p := range resDirect.Swarm.Pairs {
		if p.Provider == "cline" || derefString(p.WorkerProvider) == "cline" {
			t.Errorf("expected swarm pairs NOT to contain cline, got %+v", p)
		}
	}

	// 通过 Dispatcher 经 JSON-RPC 调用 providers.list 进行 schema 校验
	auth := &fakeAuth{}
	d := NewDispatcher(auth, backend)
	conn := &fakeConn{info: ClientInfo{Authenticated: true}}

	req := `{"jsonrpc":"2.0","method":"providers.list","id":1}`
	respRaw := d.Handle(ctx, conn, []byte(req))

	var rawMap map[string]any
	if err := json.Unmarshal(respRaw, &rawMap); err != nil {
		t.Fatalf("unmarshal response JSON failed: %v", err)
	}
	if rawMap["error"] != nil {
		t.Fatalf("unexpected RPC error: %+v", rawMap["error"])
	}
	resultMap, ok := rawMap["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %T", rawMap["result"])
	}

	providersList, ok := resultMap["providers"].([]any)
	if !ok {
		t.Fatalf("expected providers array, got %T", resultMap["providers"])
	}

	var foundClineMap, foundOpenAIMap map[string]any
	for _, item := range providersList {
		pm := item.(map[string]any)
		if pm["id"] == "cline" {
			foundClineMap = pm
		} else if pm["id"] == "openai" {
			foundOpenAIMap = pm
		}
	}
	if foundClineMap == nil || foundOpenAIMap == nil {
		t.Fatalf("missing cline or openai in JSON-RPC providers list")
	}

	if foundClineMap["name"] != "Cline" || foundClineMap["available"] != false || foundClineMap["auth"] != "signedOut" {
		t.Errorf("unexpected cline JSON: %+v", foundClineMap)
	}
	if r, ok := foundClineMap["unavailableReason"].(string); !ok || r != "cline not found" {
		t.Errorf("expected cline unavailableReason 'cline not found', got %v", foundClineMap["unavailableReason"])
	}
	if foundOpenAIMap["name"] != "OpenAI" || foundOpenAIMap["available"] != true || foundOpenAIMap["auth"] != "signedIn" {
		t.Errorf("unexpected openai JSON: %+v", foundOpenAIMap)
	}
	if foundOpenAIMap["unavailableReason"] != nil {
		t.Errorf("expected openai unavailableReason nil, got %v", foundOpenAIMap["unavailableReason"])
	}

	swarmMap, ok := resultMap["swarm"].(map[string]any)
	if !ok {
		t.Fatalf("expected swarm object in result")
	}
	pairs, ok := swarmMap["pairs"].([]any)
	if !ok {
		t.Fatalf("expected pairs array in swarm")
	}
	for _, p := range pairs {
		pair := p.(map[string]any)
		if pair["provider"] == "cline" {
			t.Errorf("expected JSON swarm pairs NOT to contain cline")
		}
	}

	// 断言对空目录 provider 的模型校验放行
	if !IsValidModel("openai", "gpt-4o") {
		t.Errorf("expected IsValidModel('openai', 'gpt-4o') to be true")
	}
	if !IsValidModel("cline", "claude-3-7-sonnet") {
		t.Errorf("expected IsValidModel('cline', 'claude-3-7-sonnet') to be true")
	}
	if IsValidModel("openai", "") {
		t.Errorf("expected IsValidModel('openai', '') to be false")
	}
	if IsValidModel("cline", "") {
		t.Errorf("expected IsValidModel('cline', '') to be false")
	}

	// 断言空目录 provider 在 CreateThread 时接受指定非空模型
	proj := wire.ProjectSummary{ID: newUUID(), Name: "test-proj", Path: "/tmp"}
	backend.AddProject(proj)
	thRes, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: proj.ID,
		ActionID:  newUUID(),
		Model:     strPtr("gpt-4o"),
		Options: &wire.ComposerOptions{
			Provider: strPtr("openai"),
		},
	})
	if err != nil {
		t.Fatalf("CreateThread with openai custom model failed: %v", err)
	}
	th, err := backend.GetThread(ctx, thRes.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.Model == nil || *th.Model != "gpt-4o" {
		t.Errorf("expected thread model 'gpt-4o', got %v", th.Model)
	}
}

type customMetaHarness struct {
	kind        harness.Kind
	displayName string
	modelIDs    []string
}

func (m *customMetaHarness) Kind() harness.Kind {
	return m.kind
}

func (m *customMetaHarness) DisplayName() string {
	return m.displayName
}

func (m *customMetaHarness) ModelIDs() []string {
	return m.modelIDs
}

func (m *customMetaHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	return nil
}

func TestUnregisterHarness_DefaultProviderFallback(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	hB := &mockHarness{kind: "b"}
	hA := &mockHarness{kind: "a"}
	hC := &mockHarness{kind: "c"}

	// 注册 b，成为 defaultProvider
	backend.RegisterHarness(hB)
	backend.RegisterHarness(hA)
	backend.RegisterHarness(hC)

	provs := backend.Providers()
	if len(provs) != 3 || provs[0] != "b" {
		t.Fatalf("expected b to be first (default), got %v", provs)
	}

	// 注销非 default 的 c
	backend.UnregisterHarness("c")
	provs = backend.Providers()
	if len(provs) != 2 || provs[0] != "b" {
		t.Fatalf("expected b still default after removing c, got %v", provs)
	}

	// 注销 defaultProvider "b"，剩余 "a"，a 成为新 default
	backend.UnregisterHarness("b")
	provs = backend.Providers()
	if len(provs) != 1 || provs[0] != "a" {
		t.Fatalf("expected a to be default after removing b, got %v", provs)
	}

	// 再次注销 a，无剩余
	backend.UnregisterHarness("a")
	provs = backend.Providers()
	if len(provs) != 0 {
		t.Fatalf("expected 0 providers, got %v", provs)
	}
}

func TestUnregisterHarness_DoesNotRerouteCustomThread(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()
	defaultHarness := &mockHarness{kind: "default"}
	backend.RegisterHarness(defaultHarness)
	backend.RegisterHarness(&mockHarness{kind: "custom-private"})
	ctx := context.Background()
	backend.AddProject(wire.ProjectSummary{ID: "project", Path: t.TempDir()})
	thread, err := backend.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "project", Options: &wire.ComposerOptions{Provider: strPtr("custom-private")},
	})
	if err != nil {
		t.Fatal(err)
	}
	backend.UnregisterHarness("custom-private")
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: thread.ThreadID, Text: "私密消息"}); err != nil {
		t.Fatal(err)
	}
	waitForTurnDone(t, backend, thread.ThreadID)
	defaultHarness.mu.Lock()
	defer defaultHarness.mu.Unlock()
	if len(defaultHarness.turnCalls) != 0 {
		t.Fatal("端点停用后不得将消息发送给默认供应商")
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	foundError := false
	for _, msg := range backend.threads[thread.ThreadID].messages {
		if strings.Contains(msg.Text, "已停用或删除") {
			foundError = true
		}
	}
	if !foundError {
		t.Fatal("会话应显示端点已停用或删除")
	}
}

func TestListProviders_DisplayNameAndModelIDs(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	customH := &customMetaHarness{
		kind:        "custom-test",
		displayName: "My Custom Gateway",
		modelIDs:    []string{"deepseek-v3", "deepseek-r1"},
	}
	backend.RegisterHarness(customH)

	res, err := backend.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}

	var found *wire.Provider
	for i := range res.Providers {
		if res.Providers[i].ID == "custom-test" {
			found = &res.Providers[i]
			break
		}
	}

	if found == nil {
		t.Fatalf("custom-test provider not found in %+v", res.Providers)
	}
	if found.Name != "My Custom Gateway" {
		t.Errorf("expected Name 'My Custom Gateway', got %q", found.Name)
	}
	if len(found.Models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(found.Models))
	}
	if found.Models[0].ID != "deepseek-v3" || found.Models[0].Name != "deepseek-v3" {
		t.Errorf("model 0 mismatch: %+v", found.Models[0])
	}
	if found.Models[1].ID != "deepseek-r1" || found.Models[1].Name != "deepseek-r1" {
		t.Errorf("model 1 mismatch: %+v", found.Models[1])
	}
}

// 没配 API Key（不可用）的 openai 不展示起始目录；配好之后才有模型行。
func TestListProviders_UnavailableProviderHasNoModels(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	h := &availabilityMockHarness{mockHarness: mockHarness{kind: "openai"}, available: false, reason: "API key is required"}
	backend.RegisterHarness(h)

	modelsOf := func() []wire.ProviderModel {
		res, err := backend.ListProviders(context.Background())
		if err != nil {
			t.Fatalf("ListProviders failed: %v", err)
		}
		for _, p := range res.Providers {
			if p.ID == "openai" {
				return p.Models
			}
		}
		t.Fatal("openai provider not listed")
		return nil
	}
	if got := modelsOf(); len(got) != 0 {
		t.Fatalf("unavailable openai should list no models, got %+v", got)
	}
	h.available = true
	if got := modelsOf(); len(got) == 0 {
		t.Fatal("available openai should list its starter catalog")
	}
}

// 桌面设置里关闭的 provider 仍会下发（enabled=false），但不会进入编排组合、常用模型与新建草稿。
func TestListProviders_DisabledProviderKeptWithEnabledFalse(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	backend.RegisterHarness(&mockHarness{kind: "codex"})
	backend.RegisterHarness(&mockHarness{kind: "claude"})
	if err := backend.SetDefaultProvider("claude"); err != nil {
		t.Fatalf("SetDefaultProvider failed: %v", err)
	}
	backend.SetSwarm(true, func(threadID string) bool { return false })
	backend.SetProviderEnabledFunc(func() map[string]bool {
		return map[string]bool{"codex": true, "claude": false}
	})
	backend.SetSwarmConfigFunc(func() config.OrchestrationConfig {
		return config.OrchestrationConfig{
			Pairs: []config.SwarmPair{
				{ID: "pair-codex", LeadProvider: "codex", WorkerProvider: "codex"},
				{ID: "pair-claude", LeadProvider: "claude", WorkerProvider: "claude"},
			},
		}
	})
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Models: config.ModelsConfig{
			Pins: []config.ModelPin{
				{Provider: "claude", ModelID: "claude-sonnet"},
				{Provider: "codex", ModelID: "gpt-5.5"},
			},
		}}
	})
	backend.SetDraftDefaultsFunc(func() wire.ComposerDraftDefaults {
		model := "claude-sonnet"
		return wire.ComposerDraftDefaults{Provider: "claude", Model: &model}
	})

	res, err := backend.ListProviders(context.Background())
	if err != nil {
		t.Fatalf("ListProviders failed: %v", err)
	}

	var claude, codex *wire.Provider
	for i := range res.Providers {
		switch res.Providers[i].ID {
		case "claude":
			claude = &res.Providers[i]
		case "codex":
			codex = &res.Providers[i]
		}
	}
	if claude == nil {
		t.Fatalf("disabled provider claude must still be listed: %+v", res.Providers)
	}
	if claude.Enabled == nil || *claude.Enabled {
		t.Fatalf("expected claude Enabled=false, got %v", claude.Enabled)
	}
	// 关闭不等于不可用：已有会话还要能继续发消息。
	if !claude.Available {
		t.Errorf("expected claude still Available=true")
	}
	if claude.UnavailableReason != nil {
		t.Errorf("expected claude UnavailableReason=nil, got %v", *claude.UnavailableReason)
	}
	if codex == nil {
		t.Fatalf("expected codex in list: %+v", res.Providers)
	}
	if codex.Enabled != nil {
		t.Errorf("expected codex Enabled=nil, got %v", *codex.Enabled)
	}

	for _, pair := range res.Swarm.Pairs {
		if pair.ID == "pair-claude" {
			t.Errorf("swarm pair referencing disabled provider must not be listed: %+v", res.Swarm.Pairs)
		}
	}
	if len(res.Swarm.Pairs) != 1 || res.Swarm.Pairs[0].ID != "pair-codex" {
		t.Errorf("expected only pair-codex, got %+v", res.Swarm.Pairs)
	}

	for _, entry := range res.Catalog {
		if entry.Provider == "claude" {
			t.Errorf("catalog must not include disabled provider: %+v", res.Catalog)
		}
	}
	if len(res.Catalog) != 1 || res.Catalog[0].Provider != "codex" {
		t.Errorf("expected only codex in catalog, got %+v", res.Catalog)
	}

	// 注入的草稿 provider 已关闭：与不可用一样回落到宿主默认草稿，
	// 而宿主默认 provider 本身已关闭时再落到第一个可用且未关闭的 provider。
	if res.Draft == nil {
		t.Fatal("expected draft in providers.list result")
	}
	if res.Draft.Provider != "codex" {
		t.Errorf("expected draft provider codex, got %q", res.Draft.Provider)
	}
	if got := derefString(res.Draft.Model); got != DefaultModel("codex") {
		t.Errorf("expected draft model %q, got %q", DefaultModel("codex"), got)
	}
}
