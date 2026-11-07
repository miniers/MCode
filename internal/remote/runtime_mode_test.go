package remote

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 1. 新线程默认档位测试
func TestRuntimeMode_DefaultMode(t *testing.T) {
	ctx := context.Background()

	// 默认未设置 harnessMode 时，默认为 autoAcceptEdits
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "p1",
		ActionID:  "a1",
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	th, err := b.GetThread(ctx, res.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if th.RuntimeMode != "autoAcceptEdits" {
		t.Errorf("expected default runtimeMode 'autoAcceptEdits', got %q", th.RuntimeMode)
	}

	// 当后台设置为 ModeReadOnly 时，新线程显示 "supervised"
	mock := &mockHarness{}
	b.SetHarness(mock, t.TempDir(), harness.ModeReadOnly)
	resRO, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "p1",
		ActionID:  "a2",
	})
	if err != nil {
		t.Fatalf("CreateThread with readonly harness failed: %v", err)
	}
	thRO, err := b.GetThread(ctx, resRO.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thRO.RuntimeMode != "supervised" {
		t.Errorf("expected readonly backend thread to display 'supervised', got %q", thRO.RuntimeMode)
	}

	// 当后台设置为 ModeFullAccess 时，新线程为 "fullAccess"
	b.SetHarness(mock, t.TempDir(), harness.ModeFullAccess)
	resFA, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "p1",
		ActionID:  "a3",
	})
	if err != nil {
		t.Fatalf("CreateThread with fullAccess harness failed: %v", err)
	}
	thFA, err := b.GetThread(ctx, resFA.ThreadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if thFA.RuntimeMode != "fullAccess" {
		t.Errorf("expected fullAccess backend thread to display 'fullAccess', got %q", thFA.RuntimeMode)
	}
}

// 2. create 时指定档位
func TestRuntimeMode_CreateSpecified(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	// 2.1 通过 params.RuntimeMode 指定
	modeAuto := "auto"
	res1, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "a1",
		RuntimeMode: &modeAuto,
	})
	if err != nil {
		t.Fatalf("CreateThread with RuntimeMode failed: %v", err)
	}
	th1, _ := b.GetThread(ctx, res1.ThreadID)
	if th1.RuntimeMode != "auto" {
		t.Errorf("expected runtimeMode 'auto', got %q", th1.RuntimeMode)
	}

	// 2.2 通过 params.Options.RuntimeMode 指定
	modeFull := "fullAccess"
	res2, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "p1",
		ActionID:  "a2",
		Options: &wire.ComposerOptions{
			RuntimeMode: &modeFull,
		},
	})
	if err != nil {
		t.Fatalf("CreateThread with Options.RuntimeMode failed: %v", err)
	}
	th2, _ := b.GetThread(ctx, res2.ThreadID)
	if th2.RuntimeMode != "fullAccess" {
		t.Errorf("expected runtimeMode 'fullAccess', got %q", th2.RuntimeMode)
	}

	// 2.3 只读档 bee 通过 params.RuntimeMode 传 "readonly"，必须被接受并原样保存
	modeRO := "readonly"
	res3, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "a3",
		RuntimeMode: &modeRO,
	})
	if err != nil {
		t.Fatalf("CreateThread with readonly RuntimeMode failed: %v", err)
	}
	th3, _ := b.GetThread(ctx, res3.ThreadID)
	if th3.RuntimeMode != "readonly" {
		t.Errorf("expected runtimeMode 'readonly', got %q", th3.RuntimeMode)
	}
}

// 3. update 改档后下一轮 Turn.Mode 随之变化
func TestRuntimeMode_UpdateAffectsNextTurn(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{
				Type: harness.EventText,
				Text: "reply for turn",
			})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	b.SetHarness(mock, t.TempDir(), harness.ModeAgent)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	// 创建新线程，默认模式为 autoAcceptEdits
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID: "p1",
		ActionID:  "a1",
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := res.ThreadID

	// 第一轮发送消息
	_, err = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "turn 1 prompt",
		ActionID: "act-1",
	})
	if err != nil {
		t.Fatalf("SendMessage turn 1 failed: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	calls := mock.getTurnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 turn call, got %d", len(calls))
	}
	if calls[0].Mode != harness.PermissionMode("autoAcceptEdits") {
		t.Errorf("expected first turn Mode 'autoAcceptEdits', got %q", calls[0].Mode)
	}

	// 通过 UpdateThread 改为 fullAccess
	newMode := "fullAccess"
	upRes, err := b.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID:    threadID,
		RuntimeMode: &newMode,
	})
	if err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}
	if upRes.Thread.RuntimeMode != "fullAccess" {
		t.Errorf("expected updated thread runtimeMode 'fullAccess', got %q", upRes.Thread.RuntimeMode)
	}

	// 第二轮发送消息
	_, err = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "turn 2 prompt",
		ActionID: "act-2",
	})
	if err != nil {
		t.Fatalf("SendMessage turn 2 failed: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	calls = mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 turn calls, got %d", len(calls))
	}
	if calls[1].Mode != harness.PermissionMode("fullAccess") {
		t.Errorf("expected second turn Mode 'fullAccess', got %q", calls[1].Mode)
	}
}

// 4. 非法值返回 InvalidParams
func TestRuntimeMode_InvalidParams(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	// create 时非法值
	invalidMode := "bogus"
	_, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "a1",
		RuntimeMode: &invalidMode,
	})
	if err == nil {
		t.Fatalf("expected error for invalid runtimeMode on create, got nil")
	}
	var rpcErr *wire.RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams error, got: %v", err)
	}

	// 正常创建线程
	validMode := "supervised"
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "a2",
		RuntimeMode: &validMode,
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// update 时非法值
	_, err = b.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID:    res.ThreadID,
		RuntimeMode: &invalidMode,
	})
	if err == nil {
		t.Fatalf("expected error for invalid runtimeMode on update, got nil")
	}
	if !errors.As(err, &rpcErr) || rpcErr.Code != wire.InvalidParams {
		t.Errorf("expected InvalidParams error, got: %v", err)
	}
}

// 5. 重新加载 store 后档位保留
func TestRuntimeMode_StorePersistence(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "mcode_test.db")
	s1, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 1: %v", err)
	}

	hub1 := NewStreamHub(StreamHubOptions{})
	defer hub1.Close()
	backend1 := NewMemoryBackend(hub1)
	if err := backend1.AttachStore(ctx, s1); err != nil {
		t.Fatalf("AttachStore 1: %v", err)
	}

	backend1.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})
	modeAuto := "auto"
	res, err := backend1.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "a1",
		RuntimeMode: &modeAuto,
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := res.ThreadID

	// 验证首次写入
	th1, _ := backend1.GetThread(ctx, threadID)
	if th1.RuntimeMode != "auto" {
		t.Fatalf("expected 'auto', got %q", th1.RuntimeMode)
	}

	// 修改档位为 supervised
	modeSup := "supervised"
	if _, err := backend1.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID:    threadID,
		RuntimeMode: &modeSup,
	}); err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}

	// 关闭 store 1
	_ = s1.Close()

	// 重新打开数据库并用新 backend 挂载
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open store 2: %v", err)
	}
	defer s2.Close()

	hub2 := NewStreamHub(StreamHubOptions{})
	defer hub2.Close()
	backend2 := NewMemoryBackend(hub2)
	if err := backend2.AttachStore(ctx, s2); err != nil {
		t.Fatalf("AttachStore 2: %v", err)
	}

	th2, err := backend2.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread on backend 2 failed: %v", err)
	}
	if th2.RuntimeMode != "supervised" {
		t.Errorf("expected reloaded thread runtimeMode 'supervised', got %q", th2.RuntimeMode)
	}
}

// 6. 后端默认为 readonly 时 Turn.Mode 始终是 readonly
func TestRuntimeMode_ReadOnlyBackendAlwaysReadOnly(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	b := NewMemoryBackend(hub)

	mock := &mockHarness{
		kind: "mock",
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			emit(harness.Event{
				Type: harness.EventText,
				Text: "reply",
			})
			emit(harness.Event{Type: harness.EventDone})
			return nil
		},
	}
	// 后端默认为 readonly
	b.SetHarness(mock, t.TempDir(), harness.ModeReadOnly)
	b.AddProject(wire.ProjectSummary{ID: "p1", Name: "Proj1", Path: t.TempDir()})

	// 即使线程创建时指定为 fullAccess
	modeFull := "fullAccess"
	res, err := b.CreateThread(ctx, wire.ThreadCreateParams{
		ProjectID:   "p1",
		ActionID:    "a1",
		RuntimeMode: &modeFull,
	})
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadID := res.ThreadID

	// 发送消息
	_, err = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run turn",
		ActionID: "act-1",
	})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	calls := mock.getTurnCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 turn call, got %d", len(calls))
	}
	// Turn.Mode 必须始终为 ModeReadOnly
	if calls[0].Mode != harness.ModeReadOnly {
		t.Errorf("expected Turn.Mode to be ModeReadOnly, got %q", calls[0].Mode)
	}

	// 即使通过 update 改档为 auto
	modeAuto := "auto"
	_, err = b.UpdateThread(ctx, wire.ThreadUpdateParams{
		ThreadID:    threadID,
		RuntimeMode: &modeAuto,
	})
	if err != nil {
		t.Fatalf("UpdateThread failed: %v", err)
	}

	_, err = b.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "run turn 2",
		ActionID: "act-2",
	})
	if err != nil {
		t.Fatalf("SendMessage turn 2 failed: %v", err)
	}
	waitThreadIdle(t, b, ctx, threadID)

	calls = mock.getTurnCalls()
	if len(calls) != 2 {
		t.Fatalf("expected 2 turn calls, got %d", len(calls))
	}
	if calls[1].Mode != harness.ModeReadOnly {
		t.Errorf("expected Turn.Mode to still be ModeReadOnly, got %q", calls[1].Mode)
	}
}
