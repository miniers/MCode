package swarm

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/testenv"
)

// TestSwarmBeeTimeoutConfigDefaults 验证配置默认值，以及缺少该字段的旧配置使用默认值。
func TestSwarmBeeTimeoutConfigDefaults(t *testing.T) {
	// 1. Defaults 使用配置的默认超时时间。
	def := config.Defaults()
	if def.Orchestration.BeeTimeoutMinutes != config.DefaultSwarmBeeTimeoutMinutes {
		t.Fatalf("config.Defaults().Orchestration.BeeTimeoutMinutes 应为 %d，实际 %d", config.DefaultSwarmBeeTimeoutMinutes, def.Orchestration.BeeTimeoutMinutes)
	}
	if d := def.Orchestration.BeeTimeoutDuration(); d != config.DefaultSwarmBeeTimeoutMinutes*time.Minute {
		t.Fatalf("BeeTimeoutDuration() 应为 %v，实际 %v", config.DefaultSwarmBeeTimeoutMinutes*time.Minute, d)
	}

	// 2. 配置 JSON 中缺少 swarmBeeTimeoutMinutes 字段时，Normalize 后应得到默认值。
	rawLegacyJSON := []byte(`{
		"orchestration": {
			"max_bees": 2,
			"workspace_mode": "worktree"
		}
	}`)
	var cfg config.Config
	if err := json.Unmarshal(rawLegacyJSON, &cfg); err != nil {
		t.Fatalf("反序列化旧配置失败: %v", err)
	}
	// 未填充前零值为 0
	if cfg.Orchestration.BeeTimeoutMinutes != 0 {
		t.Fatalf("裸反序列化零值应为 0，实际 %d", cfg.Orchestration.BeeTimeoutMinutes)
	}
	// 经过 Normalize 规范化后应使用默认值。
	norm := config.Normalize(cfg)
	if norm.Orchestration.BeeTimeoutMinutes != config.DefaultSwarmBeeTimeoutMinutes {
		t.Fatalf("Normalize 后缺少超时的旧配置应为 %d，实际 %d", config.DefaultSwarmBeeTimeoutMinutes, norm.Orchestration.BeeTimeoutMinutes)
	}

	// 3. 显式设置为 0 时 BeeTimeoutDuration 也回退到默认值。
	var zeroCfg config.OrchestrationConfig
	if d := zeroCfg.BeeTimeoutDuration(); d != config.DefaultSwarmBeeTimeoutMinutes*time.Minute {
		t.Fatalf("零值 OrchestrationConfig 的 BeeTimeoutDuration() 应为 %v，实际 %v", config.DefaultSwarmBeeTimeoutMinutes*time.Minute, d)
	}
}

// TestSwarmManager_SetBeeTimeout 验证 SetBeeTimeout 加锁修改能被 Manager 正确读取。
func TestSwarmManager_SetBeeTimeout(t *testing.T) {
	mgr := NewManager(Options{})
	// 默认未设置时应为 40 分钟
	if d := mgr.BeeTimeout(); d != DefaultBeeTimeout {
		t.Fatalf("初始 BeeTimeout() 应为默认 40m，实际 %v", d)
	}

	// 动态设置 25 分钟
	mgr.SetBeeTimeout(25 * time.Minute)
	if d := mgr.BeeTimeout(); d != 25*time.Minute {
		t.Fatalf("设置后 BeeTimeout() 应为 25m，实际 %v", d)
	}

	// 传入 0 恢复
	mgr.SetBeeTimeout(0)
	if d := mgr.BeeTimeout(); d != DefaultBeeTimeout {
		t.Fatalf("恢复后 BeeTimeout() 应重置为 40m，实际 %v", d)
	}
}

// TestWatchdog_DynamicTimeoutApplied 验证看门狗按 SetBeeTimeout 的新值判定熔断。
func TestWatchdog_DynamicTimeoutApplied(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	var stopMu sync.Mutex
	var stopped []string
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
		StopSession: func(ctx context.Context, threadID string) error {
			stopMu.Lock()
			stopped = append(stopped, threadID)
			stopMu.Unlock()
			return nil
		},
	})

	// 将超时动态调低至 8 分钟（低于卡死判定上限 15 分钟）
	mgr.SetBeeTimeout(8 * time.Minute)

	leadID := "lead-timeout-test"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "DynamicHank", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID

	// 建立初始进度快照
	mgr.Tick(ctx)

	// 推进 5 分钟，未达动态配置的 8 分钟
	clock.Advance(5 * time.Minute)
	mgr.Tick(ctx)
	h := beeByThread(t, mgr, leadID, beeThreadID)
	if h.Status != StatusRunning {
		t.Fatalf("5 分钟时不应熔断（动态超时为 8 分钟），实际状态: %s", h.Status)
	}

	// 再推进 3 分钟（累计 8 分钟），此时达到新超时值，应熔断
	clock.Advance(3 * time.Minute)
	mgr.Tick(ctx)
	h = beeByThread(t, mgr, leadID, beeThreadID)
	if h.Status != StatusFailed {
		t.Fatalf("8 分钟时应判定为失败熔断，实际状态: %s", h.Status)
	}
	if !strings.Contains(h.Summary, "看门狗") || !strings.Contains(h.Summary, "8 分钟无输出且无文件改动") {
		t.Fatalf("熔断摘要应包含 8 分钟看门狗说明，实际: %s", h.Summary)
	}

	stopMu.Lock()
	stopCount := len(stopped)
	stopMu.Unlock()
	if stopCount != 1 || stopped[0] != beeThreadID {
		t.Fatalf("应恰好调用一次 StopSession，实际 %v", stopped)
	}
}

// TestWatchdog_RunTimeoutWithProgress 验证持续有输出的 bee 在运行时长达到超时上限后也会被停止，且等待用户的时间不计入。
func TestWatchdog_RunTimeoutWithProgress(t *testing.T) {
	testenv.TempHome(t)
	runner, dir := initTestRepo(t)
	ctx := context.Background()
	clock := newTestClock()
	driver := newFakeDriver()

	var stopMu sync.Mutex
	var stopped []string
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		Now:         clock.Now,
		StopSession: func(ctx context.Context, threadID string) error {
			stopMu.Lock()
			stopped = append(stopped, threadID)
			stopMu.Unlock()
			return nil
		},
	})
	mgr.SetBeeTimeout(20 * time.Minute)

	leadID := "lead-run-timeout"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "BusyHank", Task: "task", Prompt: "prompt"}},
	})
	if err != nil {
		t.Fatalf("Dispatch 失败: %v", err)
	}
	beeThreadID := bees[0].ThreadID
	mgr.Tick(ctx)

	// 中途等待用户回答 30 分钟，这段时间不计入运行上限。进入等待后立即轮询一次，记下等待起点。
	driver.setSessionState(beeThreadID, kanban.SessionState{Running: true, AwaitingInput: true})
	mgr.Tick(ctx)
	for i := 0; i < 3; i++ {
		clock.Advance(10 * time.Minute)
		mgr.Tick(ctx)
	}
	driver.setSessionState(beeThreadID, kanban.SessionState{Running: true})

	// 每 5 分钟都有新输出，看门狗不会判卡死；扣除等待后累计 15 分钟时仍应运行。
	for i := 1; i <= 4; i++ {
		mgr.mu.Lock()
		mgr.leads[leadID].bees[0].ToolCalls = i
		mgr.mu.Unlock()
		clock.Advance(5 * time.Minute)
		mgr.Tick(ctx)
		h := beeByThread(t, mgr, leadID, beeThreadID)
		if i < 4 && h.Status != StatusRunning {
			t.Fatalf("累计 %d 分钟且持续有输出时应运行，实际状态 %s", i*5, h.Status)
		}
	}

	h := beeByThread(t, mgr, leadID, beeThreadID)
	if h.Status != StatusFailed {
		t.Fatalf("累计 20 分钟应达运行上限被停止，实际状态 %s", h.Status)
	}
	if !strings.Contains(h.Summary, "20 分钟上限") {
		t.Fatalf("停止原因应说明运行上限，实际: %s", h.Summary)
	}
	stopMu.Lock()
	defer stopMu.Unlock()
	if len(stopped) != 1 || stopped[0] != beeThreadID {
		t.Fatalf("应恰好调用一次 StopSession，实际 %v", stopped)
	}
}
