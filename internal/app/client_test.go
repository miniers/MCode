package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/automation"
	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/providers/agy/accounts"
	"mcode/internal/providers/agy/quota"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	mcodeui "mcode/internal/ui"
)

func TestClient(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	client := app.Client()

	// 1. 验证 Projects 至少有 1 个
	projects, err := client.Projects(ctx)
	if err != nil {
		t.Fatalf("client.Projects failed: %v", err)
	}
	if len(projects) == 0 {
		t.Fatalf("expected at least 1 project, got %d", len(projects))
	}
	projID := projects[0].ID

	// 2. CreateThread 后 Threads 中包含新线程
	newThreadID, err := client.CreateThread(ctx, projID, "")
	if err != nil {
		t.Fatalf("client.CreateThread failed: %v", err)
	}
	if newThreadID == "" {
		t.Fatalf("expected non-empty threadID")
	}

	threads, err := client.Threads(ctx, projID)
	if err != nil {
		t.Fatalf("client.Threads failed: %v", err)
	}
	found := false
	for _, th := range threads {
		if th.ID == newThreadID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created thread %s not found in Threads list: %+v", newThreadID, threads)
	}

	// 3. Watch 新线程后调用 Send，在 5 秒内通过 onUpdate 收到 Role 为 assistant 且 IsFinal 的消息
	var mu sync.Mutex
	var isStopped bool
	var updatesAfterStop int
	finalCh := make(chan struct{}, 1)

	stop := client.Watch(newThreadID, func(u mcodeui.ThreadUpdate) {
		mu.Lock()
		defer mu.Unlock()
		if isStopped {
			updatesAfterStop++
		}
		for _, msg := range u.Messages {
			if msg.Role == "assistant" && msg.IsFinal {
				select {
				case finalCh <- struct{}{}:
				default:
				}
			}
		}
	}, func(approvals []mcodeui.Approval) {})

	if _, err := client.Send(ctx, newThreadID, "hello world"); err != nil {
		t.Fatalf("client.Send failed: %v", err)
	}

	select {
	case <-finalCh:
		// 收到 assistant 且 IsFinal 消息
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for assistant final message")
	}

	// 4. stop 之后再次 Send 不会触发回调
	mu.Lock()
	isStopped = true
	mu.Unlock()

	stop()

	if _, err := client.Send(ctx, newThreadID, "another message"); err != nil {
		t.Fatalf("client.Send after stop failed: %v", err)
	}

	time.Sleep(1 * time.Second)

	mu.Lock()
	count := updatesAfterStop
	mu.Unlock()
	if count > 0 {
		t.Fatalf("expected 0 updates after stop, got %d", count)
	}

	// 5. Close 后用同一个目录重新 Open，History 能读回 user 和 assistant 消息
	if err := app.Close(); err != nil {
		t.Fatalf("app.Close failed: %v", err)
	}

	app2, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("re-Open app failed: %v", err)
	}
	defer app2.Close()

	history, err := app2.Client().History(ctx, newThreadID)
	if err != nil {
		t.Fatalf("History failed: %v", err)
	}

	var hasUser, hasAssistant bool
	for _, msg := range history {
		if msg.Role == "user" {
			hasUser = true
		}
		if msg.Role == "assistant" {
			hasAssistant = true
		}
	}
	if !hasUser || !hasAssistant {
		t.Fatalf("expected both user and assistant messages in history, got hasUser=%v, hasAssistant=%v, history=%+v", hasUser, hasAssistant, history)
	}
}

func TestUIKind(t *testing.T) {
	tests := []struct {
		name string
		role string
		kind string
		want string
	}{
		{name: "thinking maps to reasoning", role: "assistant", kind: "thinking", want: "reasoning"},
		{name: "reasoning maps to reasoning", role: "assistant", kind: "reasoning", want: "reasoning"},
		{name: "assistant maps to text", role: "assistant", kind: "assistant", want: "text"},
		{name: "text maps to text", role: "assistant", kind: "text", want: "text"},
		{name: "empty kind with assistant role maps to text", role: "assistant", kind: "", want: "text"},
		{name: "empty kind with other role stays empty", role: "user", kind: "", want: ""},
		{name: "tool stays tool", role: "tool", kind: "tool", want: "tool"},
		{name: "other custom kind preserved", role: "assistant", kind: "custom", want: "custom"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := uiKind(tc.role, tc.kind)
			if got != tc.want {
				t.Errorf("uiKind(%q, %q) = %q, want %q", tc.role, tc.kind, got, tc.want)
			}
		})
	}
}

func TestClientInterrupt(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()

	projects, err := client.Projects(ctx)
	if err != nil {
		t.Fatalf("client.Projects failed: %v", err)
	}
	if len(projects) == 0 {
		t.Fatalf("expected at least 1 project, got %d", len(projects))
	}
	projID := projects[0].ID

	newThreadID, err := client.CreateThread(ctx, projID, "")
	if err != nil {
		t.Fatalf("client.CreateThread failed: %v", err)
	}

	// 没有在运行时调用 Interrupt 返回 nil
	if _, err := client.Interrupt(ctx, newThreadID); err != nil {
		t.Fatalf("client.Interrupt when not running expected nil, got %v", err)
	}

	stoppedCh := make(chan struct{}, 1)
	stop := client.Watch(newThreadID, func(u mcodeui.ThreadUpdate) {
		if !u.Thread.IsRunning {
			select {
			case stoppedCh <- struct{}{}:
			default:
			}
		}
	}, func(approvals []mcodeui.Approval) {})
	defer stop()

	// 订阅时会先收到一帧权威快照（此时会话空闲）：先把它消费掉，
	// 确保下面等的是本轮 Interrupt 之后的状态。
	select {
	case <-stoppedCh:
	default:
	}

	if _, err := client.Send(ctx, newThreadID, "hello interrupt"); err != nil {
		t.Fatalf("client.Send failed: %v", err)
	}

	if _, err := client.Interrupt(ctx, newThreadID); err != nil {
		t.Fatalf("client.Interrupt failed: %v", err)
	}

	select {
	case <-stoppedCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for Thread.IsRunning == false after Interrupt")
	}
}

func TestClient_AddProjectAndCreateThread(t *testing.T) {
	dataDir := t.TempDir()
	workDir1 := t.TempDir()
	workDir2 := t.TempDir()
	// 总管会在后台把主目录登记成项目，让项目数与断言赛跑，这里先关掉。
	disableStewardForTest(t, dataDir)

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir1,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()

	// 初始已有种子项目
	projs1, err := client.Projects(ctx)
	if err != nil {
		t.Fatalf("initial Projects: %v", err)
	}
	if len(projs1) != 1 {
		t.Fatalf("expected 1 initial project, got %d", len(projs1))
	}

	// 添加第二个项目
	p2, err := client.AddProject(ctx, workDir2)
	if err != nil {
		t.Fatalf("AddProject workDir2: %v", err)
	}
	if p2.ID == "" {
		t.Fatalf("expected non-empty ID for p2")
	}

	// 验证 Projects 返回 2 个
	projs2, err := client.Projects(ctx)
	if err != nil {
		t.Fatalf("Projects after add: %v", err)
	}
	if len(projs2) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projs2))
	}

	// 并在第二个项目下 CreateThread 成功
	thID, err := client.CreateThread(ctx, p2.ID, "")
	if err != nil {
		t.Fatalf("CreateThread in p2 failed: %v", err)
	}
	if thID == "" {
		t.Fatalf("expected non-empty thID")
	}

	// 验证 Threads(p2.ID) 包含该会话
	threads, err := client.Threads(ctx, p2.ID)
	if err != nil {
		t.Fatalf("Threads(p2.ID) failed: %v", err)
	}
	found := false
	for _, th := range threads {
		if th.ID == thID {
			found = true
			if th.ProjectID != p2.ID {
				t.Errorf("expected thread ProjectID %q, got %q", p2.ID, th.ProjectID)
			}
			break
		}
	}
	if !found {
		t.Fatalf("thread %s not found in p2 threads: %+v", thID, threads)
	}
}

type testMockHarness struct {
	kind harness.Kind
}

func (m *testMockHarness) Kind() harness.Kind {
	return m.kind
}

func (m *testMockHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	return nil
}

func TestClient_ProvidersAndCreateWithProvider(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	harness1 := &testMockHarness{kind: "mock-1"}
	harness2 := &testMockHarness{kind: "mock-2"}
	app.backend.RegisterHarness(harness1)
	app.backend.RegisterHarness(harness2)

	client := app.Client()

	providers, err := client.Providers(ctx)
	if err != nil {
		t.Fatalf("client.Providers failed: %v", err)
	}
	if len(providers) != 2 {
		t.Fatalf("expected 2 providers, got %d: %+v", len(providers), providers)
	}
	if providers[0].ID != "mock-1" || providers[1].ID != "mock-2" {
		t.Fatalf("unexpected providers order/IDs: %+v", providers)
	}

	projects, err := client.Projects(ctx)
	if err != nil {
		t.Fatalf("client.Projects failed: %v", err)
	}
	projID := projects[0].ID

	// 用第二个 provider 创建会话
	thID, err := client.CreateThread(ctx, projID, "mock-2")
	if err != nil {
		t.Fatalf("CreateThread with provider 'mock-2' failed: %v", err)
	}

	threads, err := client.Threads(ctx, projID)
	if err != nil {
		t.Fatalf("client.Threads failed: %v", err)
	}
	var targetThread *mcodeui.Thread
	for i := range threads {
		if threads[i].ID == thID {
			targetThread = &threads[i]
			break
		}
	}
	if targetThread == nil {
		t.Fatalf("created thread %s not found in threads list", thID)
	}
	if targetThread.Provider != "mock-2" {
		t.Fatalf("expected thread provider 'mock-2', got %q", targetThread.Provider)
	}
}

func TestClient_CreateThreadWithOptions(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	harness1 := &testMockHarness{kind: "mock-1"}
	harness2 := &testMockHarness{kind: "mock-2"}
	app.backend.RegisterHarness(harness1)
	app.backend.RegisterHarness(harness2)

	client := app.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("client.Projects failed: %v", err)
	}
	projID := projects[0].ID

	opts := mcodeui.NewThreadOptions{
		Provider:     "mock-2",
		Model:        "test-model",
		BeesProvider: "codex",
		BeesModel:    "gpt-5.5",
		BeesEffort:   "high",
	}
	thID, err := client.CreateThreadWithOptions(ctx, projID, opts)
	if err != nil {
		t.Fatalf("CreateThreadWithOptions failed: %v", err)
	}
	if thID == "" {
		t.Fatalf("expected non-empty thread ID")
	}

	threads, err := client.Threads(ctx, projID)
	if err != nil {
		t.Fatalf("client.Threads failed: %v", err)
	}
	var targetThread *mcodeui.Thread
	for i := range threads {
		if threads[i].ID == thID {
			targetThread = &threads[i]
			break
		}
	}
	if targetThread == nil {
		t.Fatalf("created thread %s not found in threads list", thID)
	}
	if targetThread.Provider != "mock-2" {
		t.Errorf("expected thread provider 'mock-2', got %q", targetThread.Provider)
	}
	if targetThread.Model != opts.Model || targetThread.BeesProvider != opts.BeesProvider || targetThread.BeesModel != opts.BeesModel || targetThread.BeesEffort != opts.BeesEffort {
		t.Errorf("created thread lost model options: %+v", targetThread)
	}
}

type usageEmittingHarness struct{}

func (h *usageEmittingHarness) Kind() harness.Kind {
	return "usage-emitter"
}

func (h *usageEmittingHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	emit(harness.Event{
		Type: harness.EventUsage,
		Usage: &harness.Usage{
			InputTokens:  1234,
			OutputTokens: 567,
			CachedTokens: 890,
		},
	})
	emit(harness.Event{
		Type: harness.EventText,
		Text: "reply done",
	})
	emit(harness.Event{
		Type:       harness.EventDone,
		StopReason: "completed",
	})
	return nil
}

func TestClient_Usage(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	app.backend.RegisterHarness(&usageEmittingHarness{})
	client := app.Client()

	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	projID := projects[0].ID

	thID, err := client.CreateThread(ctx, projID, "usage-emitter")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	doneCh := make(chan struct{}, 1)
	stop := client.Watch(thID, func(u mcodeui.ThreadUpdate) {
		for _, m := range u.Messages {
			if m.Role == "assistant" && m.IsFinal {
				select {
				case doneCh <- struct{}{}:
				default:
				}
			}
		}
	}, func([]mcodeui.Approval) {})
	defer stop()

	if _, err := client.Send(ctx, thID, "hi"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for final reply")
	}

	usage, err := client.Usage(ctx, thID)
	if err != nil {
		t.Fatalf("Usage failed: %v", err)
	}

	wantTurn := mcodeui.TokenUsage{
		Input:  1234,
		Output: 567,
		Cached: 890,
	}
	// DurationMs 是墙钟耗时，随机器负载波动，本测试只校验 token 计数。
	turn0 := usage.Turns[0]
	turn0.DurationMs = 0
	usage.Total.DurationMs = 0
	if !reflect.DeepEqual(turn0, wantTurn) {
		t.Errorf("turn 0 usage mismatch: got %+v, want %+v", turn0, wantTurn)
	}
	if !reflect.DeepEqual(usage.Total, wantTurn) {
		t.Errorf("total usage mismatch: got %+v, want %+v", usage.Total, wantTurn)
	}
}

func TestClient_SetThreadAgent(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	app.backend.RegisterHarness(&testMockHarness{kind: "memory"})

	client := app.Client()

	// 验证 Providers 返回的模型列表
	provs, err := client.Providers(ctx)
	if err != nil {
		t.Fatalf("Providers failed: %v", err)
	}
	if len(provs) == 0 {
		t.Fatalf("expected at least 1 provider")
	}
	var memProv *mcodeui.ProviderInfo
	for i := range provs {
		if provs[i].ID == "memory" {
			memProv = &provs[i]
			break
		}
	}
	if memProv == nil {
		t.Fatalf("expected memory provider in list")
	}
	if len(memProv.Models) == 0 || memProv.Models[0].ID != "echo" {
		t.Fatalf("expected memory provider to have echo model, got: %+v", memProv.Models)
	}

	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	projID := projects[0].ID

	// 创建线程
	thID, err := client.CreateThread(ctx, projID, "memory")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// 检查创建出的线程 model 默认为 echo
	threads, err := client.Threads(ctx, projID)
	if err != nil {
		t.Fatalf("Threads failed: %v", err)
	}
	var targetThread *mcodeui.Thread
	for i := range threads {
		if threads[i].ID == thID {
			targetThread = &threads[i]
			break
		}
	}
	if targetThread == nil {
		t.Fatalf("created thread not found")
	}
	if targetThread.Model != "echo" {
		t.Fatalf("expected thread model echo, got %q", targetThread.Model)
	}

	// 设置不存在的模型应当报错
	if err := client.SetThreadAgent(thID, "", "nonexistent-model"); err == nil {
		t.Fatalf("expected SetThreadAgent with invalid model to fail")
	}

	// 设置合法模型 echo 应当成功
	if err := client.SetThreadAgent(thID, "", "echo"); err != nil {
		t.Fatalf("SetThreadAgent failed: %v", err)
	}
}

func TestClient_Diff(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()

	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("client.Projects failed: %v", err)
	}
	projID := projects[0].ID

	// workDir 为临时目录，不是 git 仓库
	threadID, err := client.CreateThread(ctx, projID, "")
	if err != nil {
		t.Fatalf("client.CreateThread failed: %v", err)
	}

	diff, err := client.Diff(ctx, threadID, "")
	if err != nil {
		t.Fatalf("client.Diff on non-git repository expected no error, got: %v", err)
	}
	if diff.Diff != "" {
		t.Errorf("expected empty Diff for non-git repository, got %q", diff.Diff)
	}
	if diff.FailureReason == "" {
		t.Errorf("expected non-empty FailureReason for non-git repository, got empty")
	}

	// 不存在的 threadID 调用，断言返回 error
	if _, err := client.Diff(ctx, "nonexistent-thread-id", ""); err == nil {
		t.Fatalf("client.Diff with nonexistent threadID expected error, got nil")
	}
}

func TestClientAgyAccounts(t *testing.T) {
	client := NewClient(nil)

	// 1. 未配置 agy manager 时统一返回错误
	if _, err := client.AgyAccounts(); err == nil || err.Error() != "agy accounts are not configured" {
		t.Fatalf("expected 'agy accounts are not configured', got %v", err)
	}

	// 2. 用 TempDir 构建 accounts.Manager 并注入
	dataDir := t.TempDir()
	mgr, err := accounts.NewManager(accounts.Options{
		DataDir:  dataDir,
		HostHome: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	client.SetAgyAccounts(mgr)

	// 3. 验证 AgyAccounts 返回包含默认的 system 账号
	accs, err := client.AgyAccounts()
	if err != nil {
		t.Fatalf("AgyAccounts failed: %v", err)
	}
	if len(accs) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accs))
	}
	if accs[0].ID != accounts.SystemAccountID {
		t.Fatalf("expected account ID %q, got %q", accounts.SystemAccountID, accs[0].ID)
	}
}

// Open 时显式选择 agy：agy 必须成为默认 provider（新线程默认走 agy），
// 且账号管理器已注入 client，系统账号可见。
func TestOpenWithAgyProvider(t *testing.T) {
	app, err := Open(Options{
		DataDir:            t.TempDir(),
		Provider:           "agy",
		WorkDir:            t.TempDir(),
		Mode:               "autoAcceptEdits",
		AgyAccountsOptions: accounts.Options{HostHome: t.TempDir()},
	})
	if err != nil {
		t.Fatalf("Open with agy provider failed: %v", err)
	}
	defer app.Close()

	client := app.Client()

	accs, err := client.AgyAccounts()
	if err != nil {
		t.Fatalf("AgyAccounts failed: %v", err)
	}
	if len(accs) == 0 || accs[0].ID != accounts.SystemAccountID {
		t.Fatalf("expected system account from agy manager, got %+v", accs)
	}

	providers, err := client.Providers(context.Background())
	if err != nil {
		t.Fatalf("Providers failed: %v", err)
	}
	if len(providers) == 0 || providers[0].ID != "agy" {
		t.Fatalf("expected agy to be the default provider (first in list), got %+v", providers)
	}
}

func TestWrapConflictErr(t *testing.T) {
	if wrapConflictErr(nil) != nil {
		t.Fatalf("expected nil for nil error")
	}
	genericErr := errors.New("some error")
	if wrapConflictErr(genericErr) != genericErr {
		t.Fatalf("expected generic error untouched")
	}
	conflictErr := store.ErrRevisionConflict
	wrapped := wrapConflictErr(conflictErr)
	if !errors.Is(wrapped, mcodeui.ErrRevisionConflict) {
		t.Fatalf("expected wrapped error to match ErrRevisionConflict")
	}
}

func TestClientNilServices(t *testing.T) {
	c := NewClient(nil)
	ctx := context.Background()

	// 1. Kanban 方法在 kanban 为 nil 时返回错误
	if _, err := c.KanbanBoard(ctx, ""); err == nil {
		t.Errorf("expected error from KanbanBoard when kanban is nil")
	}
	if _, err := c.CreateTask(ctx, "p1", "title", "notes"); err == nil {
		t.Errorf("expected error from CreateTask when kanban is nil")
	}
	if _, err := c.MoveTask(ctx, "t1", "done", 1); err == nil {
		t.Errorf("expected error from MoveTask when kanban is nil")
	}
	if _, err := c.StartTask(ctx, "t1", 1); err == nil {
		t.Errorf("expected error from StartTask when kanban is nil")
	}
	if _, err := c.ReviewTask(ctx, "t1", "accept", "", 1); err == nil {
		t.Errorf("expected error from ReviewTask when kanban is nil")
	}
	if _, err := c.CreateGoal(ctx, "p1", "prompt"); err == nil {
		t.Errorf("expected error from CreateGoal when kanban is nil")
	}
	if _, err := c.StartGoal(ctx, "g1", 1); err == nil {
		t.Errorf("expected error from StartGoal when kanban is nil")
	}
	if _, err := c.PauseGoal(ctx, "g1", 1); err == nil {
		t.Errorf("expected error from PauseGoal when kanban is nil")
	}
	if _, err := c.CancelGoal(ctx, "g1", 1); err == nil {
		t.Errorf("expected error from CancelGoal when kanban is nil")
	}
	if _, err := c.ReplanGoal(ctx, "g1", "fb", 1); err == nil {
		t.Errorf("expected error from ReplanGoal when kanban is nil")
	}

	// 2. Automation 方法在 automation 为 nil 时返回错误
	if _, err := c.Automations(ctx, ""); err == nil {
		t.Errorf("expected error from Automations when automation is nil")
	}
	if _, err := c.CreateAutomation(ctx, mcodeui.AutomationInput{}); err == nil {
		t.Errorf("expected error from CreateAutomation when automation is nil")
	}
	if _, err := c.UpdateAutomation(ctx, "a1", mcodeui.AutomationInput{}, 1); err == nil {
		t.Errorf("expected error from UpdateAutomation when automation is nil")
	}
	if err := c.DeleteAutomation(ctx, "a1"); err == nil {
		t.Errorf("expected error from DeleteAutomation when automation is nil")
	}
	if _, err := c.RunAutomation(ctx, "a1"); err == nil {
		t.Errorf("expected error from RunAutomation when automation is nil")
	}
	if _, err := c.AutomationRuns(ctx, "a1", 10); err == nil {
		t.Errorf("expected error from AutomationRuns when automation is nil")
	}
}

func TestWatchKanbanStop(t *testing.T) {
	c := NewClient(nil)
	var called int
	stop := c.WatchKanban(func() {
		called++
	})

	c.notifyKanbanChanged()
	if called != 1 {
		t.Fatalf("expected callback called once, got %d", called)
	}

	stop()
	c.notifyKanbanChanged()
	if called != 1 {
		t.Fatalf("expected callback not called after stop, got %d", called)
	}
}

func TestKanbanBoardToUI_FilterAndProgress(t *testing.T) {
	p1 := "proj-1"
	p2 := "proj-2"
	g1 := "goal-1"
	g2 := "goal-2"
	reviewThread, feedback, reviewStatus := "review-thread", "merge conflict", "merge_failed"

	board := kanban.Board{
		Revision: 42,
		Tasks: []kanban.TaskView{
			{
				Task: store.Task{
					ID:                 "t1",
					ProjectID:          &p1,
					GoalID:             &g1,
					Status:             "done",
					ReviewThreadID:     &reviewThread,
					ReviewFeedback:     &feedback,
					ReviewStatus:       &reviewStatus,
					AcceptanceCriteria: "go test ./...",
				},
				Derived: kanban.Derived{DisplayColumn: kanban.DisplayColumnDone},
			},
			{
				Task: store.Task{
					ID:        "t2",
					ProjectID: &p1,
					GoalID:    &g1,
					Status:    "in_progress",
				},
				Derived: kanban.Derived{DisplayColumn: kanban.DisplayColumnInProgress},
			},
			{
				Task: store.Task{
					ID:        "t3",
					ProjectID: &p2,
					GoalID:    &g2,
					Status:    "todo",
				},
				Derived: kanban.Derived{DisplayColumn: kanban.DisplayColumnTodo},
			},
		},
		Goals: []store.Goal{
			{
				ID:        g1,
				ProjectID: p1,
				Title:     "Goal 1 with no progress map",
			},
			{
				ID:        g2,
				ProjectID: p2,
				Title:     "Goal 2 with progress map",
				Progress: map[string]any{
					"total": 10,
					"done":  5,
				},
			},
		},
	}

	// 1. projectID 为空，返回全部
	allUI := KanbanBoardToUI(board, "")
	if len(allUI.Tasks) != 3 || len(allUI.Goals) != 2 {
		t.Fatalf("expected 3 tasks and 2 goals, got %d tasks and %d goals", len(allUI.Tasks), len(allUI.Goals))
	}
	if task := allUI.Tasks[0]; task.ThreadID != "" || task.ReviewThreadID != reviewThread || task.ReviewFeedback != feedback || task.ReviewStatus != reviewStatus || task.AcceptanceCriteria != "go test ./..." || task.CanApprove {
		t.Errorf("unexpected review fields: %+v", task)
	}
	if task := allUI.Tasks[1]; task.ReviewThreadID != "" || task.AcceptanceCriteria != "" {
		t.Errorf("unexpected empty fields: %+v", task)
	}
	// g1 进度由任务统计：共 2 个任务，1 个完成
	if allUI.Goals[0].Total != 2 || allUI.Goals[0].Done != 1 {
		t.Errorf("expected g1 total=2, done=1, got total=%d, done=%d", allUI.Goals[0].Total, allUI.Goals[0].Done)
	}
	// g2 进度从 Progress 直接读取：total=10, done=5
	if allUI.Goals[1].Total != 10 || allUI.Goals[1].Done != 5 {
		t.Errorf("expected g2 total=10, done=5, got total=%d, done=%d", allUI.Goals[1].Total, allUI.Goals[1].Done)
	}

	// 2. 按 p1 过滤
	p1UI := KanbanBoardToUI(board, p1)
	if len(p1UI.Tasks) != 2 {
		t.Fatalf("expected 2 tasks for p1, got %d", len(p1UI.Tasks))
	}
	if len(p1UI.Goals) != 1 || p1UI.Goals[0].ID != g1 {
		t.Fatalf("expected 1 goal (g1) for p1, got %+v", p1UI.Goals)
	}
	if p1UI.Goals[0].Total != 2 || p1UI.Goals[0].Done != 1 {
		t.Errorf("expected g1 total=2, done=1, got total=%d, done=%d", p1UI.Goals[0].Total, p1UI.Goals[0].Done)
	}
}

func TestAutomationSpecRoundtrip(t *testing.T) {
	now := time.Now().Truncate(time.Second).UTC()
	in := mcodeui.AutomationInput{
		ProjectID: "proj-1",
		Name:      "Daily Summary",
		Prompt:    "Summarize daily progress",
		StopWhen:  "completed",
		Schedule: mcodeui.AutomationSchedule{
			Type:      "daily",
			TimeOfDay: "09:00",
			Timezone:  "Asia/Shanghai",
		},
		Enabled:                true,
		MaxConsecutiveFailures: 5,
		MaxIterations:          10,
	}

	spec := AutomationInputToSpec(in, automation.Spec{})
	if spec.ConfidenceThreshold != 0.8 {
		t.Errorf("expected default ConfidenceThreshold 0.8, got %f", spec.ConfidenceThreshold)
	}
	if spec.MaxConsecutiveFailures != 5 {
		t.Errorf("expected MaxConsecutiveFailures 5, got %d", spec.MaxConsecutiveFailures)
	}
	if spec.MaxIterations != 10 {
		t.Errorf("expected MaxIterations 10, got %d", spec.MaxIterations)
	}
	if spec.StopWhen != "completed" {
		t.Errorf("expected StopWhen 'completed', got %q", spec.StopWhen)
	}

	// Schedule 转换往返测试
	uiSched := AutomationScheduleToUI(spec.Schedule)
	if uiSched.Type != "daily" || uiSched.TimeOfDay != "09:00" || uiSched.Timezone != "Asia/Shanghai" {
		t.Errorf("unexpected uiSched: %+v", uiSched)
	}
	roundtripSched := AutomationScheduleFromUI(uiSched)
	if roundtripSched.Type != spec.Schedule.Type || roundtripSched.TimeOfDay != spec.Schedule.TimeOfDay {
		t.Errorf("unexpected roundtrip schedule: %+v", roundtripSched)
	}

	// Definition 转 UI 测试
	projID := in.ProjectID
	def := automation.Definition{
		Automation: store.Automation{
			ID:        "auto-1",
			ProjectID: &projID,
			Name:      in.Name,
			Prompt:    in.Prompt,
			Enabled:   true,
			Status:    "active",
			NextRunAt: &now,
			LastRunAt: &now,
			Revision:  2,
		},
		Spec: spec,
	}
	def.Spec.State = automation.RunState{
		TotalRunCount:       3,
		ConsecutiveFailures: 1,
		LastRunStatus:       "succeeded",
		StopReason:          "",
	}

	uiAuto := AutomationToUI(def)
	if uiAuto.ID != "auto-1" || uiAuto.ProjectID != "proj-1" || uiAuto.TotalRunCount != 3 || uiAuto.ConsecutiveFailures != 1 {
		t.Errorf("unexpected uiAuto: %+v", uiAuto)
	}
	if uiAuto.LastRunStatus != "succeeded" || uiAuto.MaxConsecutiveFailures != 5 || uiAuto.Revision != 2 {
		t.Errorf("unexpected uiAuto fields: %+v", uiAuto)
	}
}

func TestClientWorkspaceStartTask(t *testing.T) {
	a, err := Open(Options{
		DataDir: t.TempDir(), WorkDir: t.TempDir(), Provider: "memory", Mode: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	ctx := context.Background()
	c := a.Client()
	projects, err := c.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("projects: %v, %v", projects, err)
	}
	task, err := c.CreateTask(ctx, projects[0].ID, "工作区启动验证", "检查任务会话")
	if err != nil {
		t.Fatal(err)
	}
	started, err := c.StartTask(ctx, task.ID, task.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != "in_progress" || started.ThreadID == "" {
		t.Fatalf("task did not start a session: %+v", started)
	}
	threads, err := c.Threads(ctx, projects[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if th.ID == started.ThreadID {
			return
		}
	}
	t.Fatal("task session is not visible in the project")
}

func TestWatchAutomationsStop(t *testing.T) {
	c := NewClient(nil)
	called := 0
	stop := c.WatchAutomations(func() { called++ })
	c.notifyAutomationChanged()
	stop()
	stop()
	c.notifyAutomationChanged()
	if called != 1 {
		t.Fatalf("callback count: %d", called)
	}
}

func TestClient_Swarm_NilManager(t *testing.T) {
	c := NewClient(nil)
	ctx := context.Background()

	// 1. nil manager -> Swarm 返回 Available=false 且无 error
	st, err := c.Swarm(ctx, "thread-1")
	if err != nil {
		t.Fatalf("expected nil error from Swarm when manager is nil, got: %v", err)
	}
	if st.Available {
		t.Fatalf("expected Available=false, got true")
	}
	if len(st.Bees) != 0 {
		t.Fatalf("expected empty Bees slice, got %d bees", len(st.Bees))
	}

	// 2. nil manager -> SetSwarmEnabled 返回错误
	_, err = c.SetSwarmEnabled(ctx, "thread-1", true)
	if err == nil {
		t.Fatalf("expected error from SetSwarmEnabled when manager is nil")
	}

	// 3. 其他变更方法返回错误
	if err := c.StopSwarmBee(ctx, "lead-1", "bee-1"); err == nil {
		t.Fatalf("expected error from StopSwarmBee when manager is nil")
	}
	if err := c.StopAllSwarmBees(ctx, "lead-1"); err == nil {
		t.Fatalf("expected error from StopAllSwarmBees when manager is nil")
	}
	if err := c.ClearFinishedSwarmBees(ctx, "lead-1"); err == nil {
		t.Fatalf("expected error from ClearFinishedSwarmBees when manager is nil")
	}
	if _, err := c.SwarmMergeDiff(ctx, "lead-1", "bee-1"); err == nil {
		t.Fatalf("expected error from SwarmMergeDiff when manager is nil")
	}
	if err := c.ApproveSwarmMerge(ctx, "lead-1", "bee-1"); err == nil {
		t.Fatalf("expected error from ApproveSwarmMerge when manager is nil")
	}
	if err := c.DiscardSwarmMerge(ctx, "lead-1", "bee-1"); err == nil {
		t.Fatalf("expected error from DiscardSwarmMerge when manager is nil")
	}
}

func TestClient_Swarm_WithManager(t *testing.T) {
	c := NewClient(nil)
	ctx := context.Background()

	mgr := swarm.NewManager(swarm.Options{})
	c.SetSwarmManager(mgr)

	var mu sync.Mutex
	var notifiedID string
	var notifyCount int
	stop := c.WatchSwarm(func(leadThreadID string) {
		mu.Lock()
		notifiedID = leadThreadID
		notifyCount++
		mu.Unlock()
	})
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return notifyCount
	}

	leadID := "lead-thread-test"
	st, err := c.SetSwarmEnabled(ctx, leadID, true)
	if err != nil {
		t.Fatalf("SetSwarmEnabled failed: %v", err)
	}
	if !st.Enabled {
		t.Fatalf("expected Enabled=true, got false")
	}
	if !st.Available {
		t.Fatalf("expected Available=true, got false")
	}
	// 通知走合并窗口：等到窗口关闭后再断言送达。
	waitForSwarmNotify(t, func() bool { return count() == 1 })
	mu.Lock()
	gotID := notifiedID
	mu.Unlock()
	if gotID != leadID {
		t.Fatalf("expected WatchSwarm callback to receive %q, got %q", leadID, gotID)
	}
	mgr.SetEnabled(leadID, false)
	waitForSwarmNotify(t, func() bool { return count() == 2 })

	// 验证 stop func 取消注册
	stop()
	prevCount := count()
	c.notifySwarmChanged("another-lead")
	time.Sleep(2 * swarmNotifyDelay)
	if got := count(); got != prevCount {
		t.Fatalf("expected callback not to be called after stop, got count %d, want %d", got, prevCount)
	}
}

// waitForSwarmNotify 等待合并窗口内的 Swarm 通知送达，超时则判定失败。
func waitForSwarmNotify(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !done() {
		if time.Now().After(deadline) {
			t.Fatal("Swarm 变更通知未在合并窗口内送达")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestClient_SwarmNotifyCoalesced 验证同一主会话在合并窗口内的多次变更只推送一次，
// 且最后一次变更一定送达。
func TestClient_SwarmNotifyCoalesced(t *testing.T) {
	c := NewClient(nil)
	var mu sync.Mutex
	var leads []string
	stop := c.WatchSwarm(func(leadThreadID string) {
		mu.Lock()
		leads = append(leads, leadThreadID)
		mu.Unlock()
	})
	defer stop()

	for i := 0; i < 20; i++ {
		c.notifySwarmChanged("lead-coalesce")
	}
	waitForSwarmNotify(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(leads) > 0
	})

	mu.Lock()
	defer mu.Unlock()
	if len(leads) != 1 || leads[0] != "lead-coalesce" {
		t.Fatalf("窗口内的 20 次变更应合并成 1 次通知，实际 %d 次：%v", len(leads), leads)
	}
}

func TestClientSwarmLiveBeeProgress(t *testing.T) {
	backend := remote.NewMemoryBackend(nil)
	defer backend.Close()
	model := "test-model"
	activity := "正在执行测试"
	kind := "tool"
	backend.AddThread(wire.ThreadSummary{
		ID: "head", Provider: "memory", Model: &model, LastActivityText: &activity,
	}, []wire.ThreadMessage{
		{ID: "m1", Role: "assistant", Text: "正在分析代码", TurnIndex: 1},
		{ID: "m2", Role: "tool", Kind: &kind, Text: "go test", TurnIndex: 2},
		{ID: "m3", Role: "assistant", Text: "测试通过，继续检查", TurnIndex: 3},
	})
	client := NewClient(backend)
	st := client.swarmState(context.Background(), swarm.State{
		LeadThreadID: "lead", Enabled: true,
		Bees: []swarm.Bee{{ThreadID: "head", Status: swarm.StatusRunning}},
	}, "lead", false)
	bee := st.Bees[0]
	if bee.Provider != "memory" || bee.Model != model || bee.ToolCalls != 1 || bee.Activity != "测试通过，继续检查" {
		t.Fatalf("missing live bee progress: %+v", bee)
	}
}

func makeTestClientPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 100, G: 150, B: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestClient_SendWithAttachments(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	threadID, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// 1. 上传一个合法附件并获取 ID
	pngData := makeTestClientPNG(10, 10)
	att, err := client.UploadAttachment(ctx, "test.png", "image/png", pngData)
	if err != nil {
		t.Fatalf("UploadAttachment failed: %v", err)
	}

	// 2. 使用有效附件 ID 发送，应成功（附件被正确透传并处理）
	if _, err := client.SendWithAttachments(ctx, threadID, "hello with attachment", []string{att.ID}); err != nil {
		t.Fatalf("SendWithAttachments with valid attachment failed: %v", err)
	}

	// 3. 使用不存在的附件 ID 发送，验证附件 ID 透传到底层报错
	threadID2, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread 2 failed: %v", err)
	}
	invalidID := "non-existent-attachment-uuid"
	_, err = client.SendWithAttachments(ctx, threadID2, "hello with invalid attachment", []string{invalidID})
	if err == nil {
		t.Fatalf("expected error when sending nonexistent attachment ID, got nil")
	}
	if !strings.Contains(err.Error(), invalidID) && !strings.Contains(err.Error(), "attachment") {
		t.Fatalf("expected error mentioning invalid attachment ID, got: %v", err)
	}
}

func TestClient_UploadAttachment(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()

	// 1. 成功上传，返回 ID 非空、字段映射正确
	pngData := makeTestClientPNG(16, 16)
	att, err := client.UploadAttachment(ctx, "valid.png", "image/png", pngData)
	if err != nil {
		t.Fatalf("UploadAttachment failed: %v", err)
	}
	if att.ID == "" {
		t.Fatalf("expected non-empty attachment ID, got empty")
	}
	if att.Name != "valid.png" {
		t.Fatalf("expected Name valid.png, got %q", att.Name)
	}
	if att.MimeType != "image/png" {
		t.Fatalf("expected MimeType image/png, got %q", att.MimeType)
	}
	if att.ByteCount != len(pngData) {
		t.Fatalf("expected ByteCount %d, got %d", len(pngData), att.ByteCount)
	}

	// 2. 超过 2936012 字节报错
	oversized := make([]byte, 2936013)
	copy(oversized, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	_, err = client.UploadAttachment(ctx, "oversized.png", "image/png", oversized)
	if err == nil {
		t.Fatalf("expected error for data exceeding 2936012 bytes, got nil")
	}
}

func TestClient_SearchFiles(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	testFile := filepath.Join(workDir, "search_target.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	subDir := filepath.Join(workDir, "search_folder")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	threadID, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	// 搜索普通文件
	matches, err := client.SearchFiles(ctx, threadID, "search_target")
	if err != nil {
		t.Fatalf("SearchFiles failed: %v", err)
	}
	var foundFile bool
	for _, m := range matches {
		if m.Name == "search_target.txt" {
			foundFile = true
			if m.IsDir {
				t.Fatalf("expected file search_target.txt to have IsDir=false")
			}
			if m.Path != "search_target.txt" {
				t.Fatalf("expected Path search_target.txt, got %q", m.Path)
			}
		}
	}
	if !foundFile {
		t.Fatalf("expected to find search_target.txt, got: %+v", matches)
	}

	// 搜索目录
	matchesDir, err := client.SearchFiles(ctx, threadID, "search_folder")
	if err != nil {
		t.Fatalf("SearchFiles for dir failed: %v", err)
	}
	var foundDir bool
	for _, m := range matchesDir {
		if m.Name == "search_folder" {
			foundDir = true
			if !m.IsDir {
				t.Fatalf("expected search_folder to have IsDir=true")
			}
		}
	}
	if !foundDir {
		t.Fatalf("expected to find search_folder, got: %+v", matchesDir)
	}
}

func TestClient_FilesList(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(workDir, "list_target.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	subDir := filepath.Join(workDir, "list_folder")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "inner.txt"), []byte("inner"), 0644); err != nil {
		t.Fatalf("WriteFile inner failed: %v", err)
	}

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	threadID, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	listing, err := client.FilesList(ctx, threadID, "")
	if err != nil {
		t.Fatalf("FilesList failed: %v", err)
	}
	var foundFile, foundDir bool
	for _, entry := range listing.Entries {
		switch entry.Name {
		case "list_target.txt":
			foundFile = true
			if entry.IsDir {
				t.Fatalf("expected list_target.txt to have IsDir=false")
			}
			if entry.Path != "list_target.txt" {
				t.Fatalf("expected Path list_target.txt, got %q", entry.Path)
			}
		case "list_folder":
			foundDir = true
			if !entry.IsDir {
				t.Fatalf("expected list_folder to have IsDir=true")
			}
		}
	}
	if !foundFile || !foundDir {
		t.Fatalf("expected root listing to contain file and dir, got: %+v", listing.Entries)
	}

	sub, err := client.FilesList(ctx, threadID, "list_folder")
	if err != nil {
		t.Fatalf("FilesList subdir failed: %v", err)
	}
	if sub.Path != "list_folder" || len(sub.Entries) != 1 || sub.Entries[0].Name != "inner.txt" {
		t.Fatalf("unexpected subdir listing: %+v", sub)
	}

	if _, err := client.FilesList(ctx, threadID, "no_such_dir"); err == nil {
		t.Fatalf("expected error for missing directory, got nil")
	}
}

func TestClient_FilesRead(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(workDir, "read_target.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	threadID, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	content, err := client.FilesRead(ctx, threadID, "read_target.txt", 0, 0)
	if err != nil {
		t.Fatalf("FilesRead failed: %v", err)
	}
	if content.Kind != "text" {
		t.Fatalf("expected kind text, got %q", content.Kind)
	}
	if content.Text != "hello world" {
		t.Fatalf("unexpected text: %q", content.Text)
	}
	if content.TotalSize != int64(len("hello world")) {
		t.Fatalf("unexpected TotalSize: %d", content.TotalSize)
	}
	if content.HasMore {
		t.Fatalf("expected HasMore=false for small file")
	}

	if _, err := client.FilesRead(ctx, threadID, "no_such_file.txt", 0, 0); err == nil {
		t.Fatalf("expected error for missing file, got nil")
	}
	if _, err := client.FilesRead(ctx, threadID, "../outside.txt", 0, 0); err == nil {
		t.Fatalf("expected error for out-of-workspace path, got nil")
	}
}

func TestClient_Commands(t *testing.T) {
	dataDir := t.TempDir()
	workDir := t.TempDir()

	ctx := context.Background()
	app, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  workDir,
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer app.Close()

	client := app.Client()
	projects, err := client.Projects(ctx)
	if err != nil || len(projects) == 0 {
		t.Fatalf("Projects failed: %v", err)
	}
	threadID, err := client.CreateThread(ctx, projects[0].ID, "")
	if err != nil {
		t.Fatalf("CreateThread failed: %v", err)
	}

	cmds, err := client.Commands(ctx, threadID)
	if err != nil {
		t.Fatalf("client.Commands failed: %v", err)
	}

	var hasPlan bool
	for _, cmd := range cmds {
		if cmd.Name == "plan" {
			hasPlan = true
			if !cmd.IsBuiltIn {
				t.Fatalf("expected plan command to have IsBuiltIn=true")
			}
			if cmd.Kind != "builtin" {
				t.Fatalf("expected plan command to have Kind=builtin, got %q", cmd.Kind)
			}
			if cmd.Detail == "" {
				t.Fatalf("expected non-empty Detail for plan command")
			}
		}
	}
	if !hasPlan {
		t.Fatalf("expected commands to contain plan, got: %+v", cmds)
	}
}

// TestClientProviderQuota_DisabledAccountIsNotActive 验证停用的账号不再算在用：
// 额度窗口带上 Disabled 标记且 Active 为 false，在用标记落到路由回退后的账号上。
func TestClientProviderQuota_DisabledAccountIsNotActive(t *testing.T) {
	dir := t.TempDir()
	index := accounts.Index{Version: 1, DefaultAccountID: accounts.SystemAccountID, Accounts: []accounts.Account{
		{ID: accounts.SystemAccountID, Label: "系统账号", Status: accounts.StatusActive, On: true, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 40}}},
		{ID: "acc-iso", Label: "隔离账号", Email: "iso@example.com", Status: accounts.StatusActive, On: true, Order: 5, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 12}}},
	}}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agy-accounts.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	// 路由覆盖只把系统账号停用，索引里的 On 保持原样。
	routing := `{"accounts":{"system":{"on":false}}}`
	if err := os.WriteFile(filepath.Join(dir, "agy-routing.json"), []byte(routing), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := accounts.NewManager(accounts.Options{DataDir: dir, HostHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}

	windows, err := (&Client{agy: manager}).ProviderQuota(context.Background(), "agy")
	if err != nil || len(windows) != 2 {
		t.Fatalf("quota windows: %+v, %v", windows, err)
	}
	if windows[0].Account != "系统账号" || !windows[0].Disabled || windows[0].Active {
		t.Fatalf("停用的系统账号应标记 Disabled 且不算在用: %+v", windows[0])
	}
	if windows[1].Account != "隔离账号" || windows[1].Disabled || !windows[1].Active {
		t.Fatalf("回退后的隔离账号应算在用: %+v", windows[1])
	}
}

// TestClientProviderQuota_DisabledBoundAccountFallsBackToRouteTarget 验证会话绑定的账号事后被停用时
// 不再算在用：在用标记退回路由目标，界面不会一个在用账号都看不到。
func TestClientProviderQuota_DisabledBoundAccountFallsBackToRouteTarget(t *testing.T) {
	dir := t.TempDir()
	data, err := json.Marshal(accounts.Index{Version: 1, DefaultAccountID: accounts.SystemAccountID, Accounts: agyQuotaTestAccounts()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agy-accounts.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	// 路由覆盖把会话绑定的隔离账号停用，索引里的 On 保持原样。
	if err := os.WriteFile(filepath.Join(dir, "agy-routing.json"), []byte(`{"accounts":{"acc-iso":{"on":false}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := accounts.NewManager(accounts.Options{DataDir: dir, HostHome: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	manager.Bind("conv-1", "acc-iso")
	// 测试前提：路由目标确实是另一个启用账号，否则这条用例证明不了回退。
	if target := manager.QuotaView().Target; target != accounts.SystemAccountID {
		t.Fatalf("测试前提不成立，路由目标应为系统账号，得到 %q", target)
	}

	client := &Client{agy: manager}
	client.SetAgySessionLookup(func(ctx context.Context, threadID string) string {
		if threadID == "thread-1" {
			return "conv-1"
		}
		return ""
	})

	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "thread-1"), "agy")
	if err != nil || len(windows) != 2 {
		t.Fatalf("quota windows: %+v, %v", windows, err)
	}
	if windows[0].Account != "系统账号" || windows[0].Disabled || !windows[0].Active {
		t.Fatalf("绑定账号被停用后，在用标记应退回路由目标: %+v", windows[0])
	}
	if windows[1].Account != "隔离账号" || !windows[1].Disabled || windows[1].Active {
		t.Fatalf("停用的绑定账号不应算在用: %+v", windows[1])
	}
}

// TestClientProviderQuota_DedupesSystemAccountByEmail 验证系统账号从登录凭证解析出的邮箱
// 与某个隔离账号相同时额度只显示一份，并以邮箱代替 UUID 标题。
func TestClientProviderQuota_DedupesSystemAccountByEmail(t *testing.T) {
	hostHome := t.TempDir()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"same@example.com"}`))
	tokenPath := filepath.Join(hostHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
		t.Fatal(err)
	}
	token := `{"auth_method":"consumer","id_token":"header.` + payload + `.sig","token":{"access_token":"a"}}`
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	index := accounts.Index{Version: 1, DefaultAccountID: accounts.SystemAccountID, Accounts: []accounts.Account{
		{ID: accounts.SystemAccountID, Label: "系统账号", Status: accounts.StatusActive, On: true, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 40}}},
		{ID: "acc-iso", Email: "Same@Example.com", Label: "550e8400-e29b-41d4-a716-446655440000",
			Status: accounts.StatusActive, On: true, Order: 5, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 12}}},
	}}
	data, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agy-accounts.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	manager, err := accounts.NewManager(accounts.Options{DataDir: dir, HostHome: hostHome})
	if err != nil {
		t.Fatal(err)
	}

	windows, err := (&Client{agy: manager}).ProviderQuota(context.Background(), "agy")
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 {
		t.Fatalf("同邮箱账号只应显示一份，得到 %+v", windows)
	}
	// 两者都启用时保留隔离账号（私有 token 的额度更准），标题用邮箱。
	if windows[0].Account != "Same@Example.com" || windows[0].UsedPercent != 12 {
		t.Fatalf("应保留隔离账号的额度与邮箱标题: %+v", windows[0])
	}
	if !windows[0].Active || windows[0].Disabled {
		t.Fatalf("保留的账号应算在用: %+v", windows[0])
	}
}

// newAgyQuotaManager 用临时数据目录构造 agy 管理器：账目列表由调用方给定，
// 便于额度弹窗测试只关心「在用」标在哪个账号上。
func newAgyQuotaManager(t *testing.T, hostHome string, list []accounts.Account) *accounts.Manager {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(accounts.Index{Version: 1, DefaultAccountID: accounts.SystemAccountID, Accounts: list})
	if err != nil {
		t.Fatalf("marshal agy index: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agy-accounts.json"), data, 0600); err != nil {
		t.Fatalf("write agy index: %v", err)
	}
	manager, err := accounts.NewManager(accounts.Options{DataDir: dir, HostHome: hostHome})
	if err != nil {
		t.Fatalf("new agy manager: %v", err)
	}
	return manager
}

// agyQuotaTestAccounts 返回两个各带一份 gemini 额度的账号：系统账号（默认、路由目标）与隔离账号。
func agyQuotaTestAccounts() []accounts.Account {
	return []accounts.Account{
		{ID: accounts.SystemAccountID, Label: "系统账号", Status: accounts.StatusActive, On: true, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 40}}},
		{ID: "acc-iso", Label: "隔离账号", Email: "iso@example.com", Status: accounts.StatusActive, On: true, Order: 5, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 12}}},
	}
}

// TestClientProviderQuota_UsesBoundConversationAccount 验证「在用」标在当前会话实际绑定的账号上：
// 路由目标是系统账号（新会话会用它），而本会话已绑定隔离账号，标记要落在隔离账号。
func TestClientProviderQuota_UsesBoundConversationAccount(t *testing.T) {
	manager := newAgyQuotaManager(t, t.TempDir(), agyQuotaTestAccounts())
	manager.Bind("conv-1", "acc-iso")
	// 测试前提：路由目标确实是另一个账号，否则这条用例证明不了优先级。
	if target := manager.QuotaView().Target; target != accounts.SystemAccountID {
		t.Fatalf("测试前提不成立，路由目标应为系统账号，得到 %q", target)
	}

	client := &Client{agy: manager}
	client.SetAgySessionLookup(func(ctx context.Context, threadID string) string {
		if threadID == "thread-1" {
			return "conv-1"
		}
		return ""
	})

	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "thread-1"), "agy")
	if err != nil || len(windows) != 2 {
		t.Fatalf("quota windows: %+v, %v", windows, err)
	}
	if windows[0].Account != "系统账号" || windows[0].Active {
		t.Fatalf("会话绑定隔离账号时系统账号不应算在用: %+v", windows[0])
	}
	if windows[1].Account != "隔离账号" || !windows[1].Active {
		t.Fatalf("会话绑定的隔离账号应算在用: %+v", windows[1])
	}
}

// TestClientProviderQuota_FallsBackToRouteTarget 验证拿不到会话绑定（无会话 ID，
// 或会话还没跑过一轮）时，「在用」仍是新会话会路由到的账号。
func TestClientProviderQuota_FallsBackToRouteTarget(t *testing.T) {
	manager := newAgyQuotaManager(t, t.TempDir(), agyQuotaTestAccounts())
	labels := map[string]string{accounts.SystemAccountID: "系统账号", "acc-iso": "隔离账号"}
	want := labels[manager.QuotaView().Target]
	if want == "" {
		t.Fatalf("测试前提不成立，路由目标应在已知账号内")
	}

	client := &Client{agy: manager}
	client.SetAgySessionLookup(func(ctx context.Context, threadID string) string { return "" })

	cases := map[string]context.Context{
		"无会话 ID":  context.Background(),
		"会话未绑定账号": mcodeui.WithQuotaThread(context.Background(), "thread-new"),
	}
	for name, ctx := range cases {
		windows, err := client.ProviderQuota(ctx, "agy")
		if err != nil || len(windows) != 2 {
			t.Fatalf("%s: quota windows: %+v, %v", name, windows, err)
		}
		active := 0
		for _, w := range windows {
			if !w.Active {
				continue
			}
			active++
			if w.Account != want {
				t.Fatalf("%s: 在用应回退到路由目标 %q，得到 %+v", name, want, w)
			}
		}
		if active != 1 {
			t.Fatalf("%s: 应恰好一个账号算在用，得到 %d 个: %+v", name, active, windows)
		}
	}
}

// TestClientProviderQuota_BoundAccountSurvivesDedupe 验证绑定账号被同邮箱去重掉时，
// 「在用」转移给保留下来的那份，标记不会丢。
func TestClientProviderQuota_BoundAccountSurvivesDedupe(t *testing.T) {
	hostHome := t.TempDir()
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"email":"same@example.com"}`))
	tokenPath := filepath.Join(hostHome, ".gemini", "antigravity-cli", "antigravity-oauth-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
		t.Fatal(err)
	}
	token := `{"auth_method":"consumer","id_token":"header.` + payload + `.sig","token":{"access_token":"a"}}`
	if err := os.WriteFile(tokenPath, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}

	manager := newAgyQuotaManager(t, hostHome, []accounts.Account{
		{ID: accounts.SystemAccountID, Label: "系统账号", Status: accounts.StatusActive, On: true, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 40}}},
		{ID: "acc-iso", Email: "Same@Example.com", Label: "550e8400-e29b-41d4-a716-446655440000",
			Status: accounts.StatusActive, On: true, Order: 5, Weight: 100,
			Quota: map[string]accounts.QuotaGroup{quota.GroupGemini: {UsedPercent: 12}}},
	})
	// 会话绑定的是系统账号，而同邮箱去重时保留隔离账号。
	manager.Bind("conv-1", accounts.SystemAccountID)

	client := &Client{agy: manager}
	client.SetAgySessionLookup(func(ctx context.Context, threadID string) string {
		if threadID == "thread-1" {
			return "conv-1"
		}
		return ""
	})

	windows, err := client.ProviderQuota(mcodeui.WithQuotaThread(context.Background(), "thread-1"), "agy")
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != 1 {
		t.Fatalf("同邮箱账号只应显示一份，得到 %+v", windows)
	}
	if windows[0].Account != "Same@Example.com" || !windows[0].Active || windows[0].Disabled {
		t.Fatalf("绑定账号被去重后应在用标记应转给保留的账号: %+v", windows[0])
	}
}

// TestClientProviderQuota_OnlyIncludesGeminiGroups 验证账号的 Quota 同时含 Gemini 与 Claude 两组时，
// ProviderQuota("agy") 返回的窗口 ID 只有 "Gemini Models" 与 "Gemini Models 7d"。
func TestClientProviderQuota_OnlyIncludesGeminiGroups(t *testing.T) {
	manager := newAgyQuotaManager(t, t.TempDir(), []accounts.Account{
		{
			ID:     "acc-1",
			Label:  "测试账号",
			Status: accounts.StatusActive,
			On:     true,
			Quota: map[string]accounts.QuotaGroup{
				quota.GroupGemini:            {UsedPercent: 20},
				quota.GroupGemini + " 7d":    {UsedPercent: 35},
				quota.GroupClaudeGPT:         {UsedPercent: 50},
				quota.GroupClaudeGPT + " 7d": {UsedPercent: 65},
			},
		},
	})
	client := &Client{agy: manager}
	windows, err := client.ProviderQuota(context.Background(), "agy")
	if err != nil {
		t.Fatalf("ProviderQuota failed: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("expected exactly 2 windows, got %d: %+v", len(windows), windows)
	}
	if windows[0].ID != "Gemini Models" || windows[1].ID != "Gemini Models 7d" {
		t.Fatalf("expected window IDs [\"Gemini Models\", \"Gemini Models 7d\"], got [%q, %q]", windows[0].ID, windows[1].ID)
	}
	for _, w := range windows {
		if strings.Contains(w.Label, "Claude") || strings.Contains(w.ID, "Claude") {
			t.Fatalf("unexpected Claude window in ProviderQuota: %+v", w)
		}
	}
}
