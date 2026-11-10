package swarm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcode/internal/kanban"
)

func TestBeePreviewMatchesSessionAndReadOnly(t *testing.T) {
	driver := newFakeDriver()
	var request kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { request = req }
	mgr := NewManager(Options{
		Driver:           driver,
		DefaultBeeSource: func(ThreadContext) (string, string, error) { return "custom-jp", "auto/model", nil },
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, Tiers: []Tier{{ID: "read", ReadOnly: true}}}
	})
	preview := mgr.TierPreview("lead")["read"]
	bee, err := mgr.startSingleBee(context.Background(), "lead", "project", t.TempDir(), BeeSpec{ReadOnly: true}, 1, "lead")
	if err != nil {
		t.Fatal(err)
	}
	if request.Provider != preview.Provider || request.Model != preview.Model || bee.Provider != preview.Provider || bee.Model != preview.Model {
		t.Fatalf("preview=%+v request=%+v bee=%+v", preview, request, bee)
	}
	if request.RuntimeMode != "readonly" {
		t.Fatalf("RuntimeMode = %q", request.RuntimeMode)
	}
	p, model, _, err := mgr.effectiveBeeRuntime("pi", "", "", Settings{DefaultModel: "other"}, ThreadContext{})
	if err != nil || p != "pi" || model != "" {
		t.Fatalf("显式 pi 来源被覆盖：%q %q %v", p, model, err)
	}
}

// 测试 ThreadContext 在 manager 派发链路中的注入（配对覆盖与用户消息指定模型授权）。
func TestManager_ThreadContext_Wiring(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var startedRequests []kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) {
		startedRequests = append(startedRequests, req)
	}

	leadID := "lead-tc-01"
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		ThreadContext: func(lid string) ThreadContext {
			if lid != leadID {
				return ThreadContext{}
			}
			return ThreadContext{
				UserMessage: "请使用 gemini-pro 模型完成这个子任务",
				PairID:      "pair-main",
				BeeTiers: map[string]TierOverride{
					"fast": {Provider: "google", Model: "gemini-flash"},
				},
				ProviderEnabled: func(p string) bool { return true },
			}
		},
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "worktree",
			Tiers: []Tier{
				{ID: "fast", Provider: "openai", Model: "gpt-4o-mini"},
				{ID: "deep", Provider: "openai", Model: "gpt-4o"},
			},
		}
	})

	// 1. fast 档位通过 ThreadOverrides 覆盖为 google/gemini-flash
	bee1, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "Fast task", Prompt: "P1", Tier: "fast"},
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry(fast) failed: %v", err)
	}
	if bee1.Provider != "google" || bee1.Model != "gemini-flash" {
		t.Errorf("bee1 expected google/gemini-flash, got %s/%s", bee1.Provider, bee1.Model)
	}

	// 2. deep 档位通过用户输入授权 direct_model
	bee2, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "Deep task", Prompt: "P2", Tier: "deep", Provider: "google", Model: "gemini-pro"},
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry(deep) failed: %v", err)
	}
	if !bee2.DirectModel {
		t.Errorf("bee2 DirectModel expected true, got false")
	}
	if bee2.Provider != "google" || bee2.Model != "gemini-pro" {
		t.Errorf("bee2 expected google/gemini-pro, got %s/%s", bee2.Provider, bee2.Model)
	}

	if len(startedRequests) != 2 {
		t.Fatalf("expected 2 started sessions, got %d", len(startedRequests))
	}
}

// 验证 BuildReport 中的档位、模型、DirectModel、越权写入与重试提示输出。
func TestBuildReport_TiersAndViolations(t *testing.T) {
	bees := []Bee{
		{
			Name:        "Bee-1",
			Task:        "Normal task",
			Status:      StatusCompleted,
			Merged:      true,
			Tier:        "deep",
			Provider:    "anthropic",
			Model:       "opus",
			DirectModel: false,
		},
		{
			Name:        "Bee-2",
			Task:        "Direct model task",
			Status:      StatusCompleted,
			Merged:      true,
			Tier:        "deep",
			Provider:    "anthropic",
			Model:       "opus",
			DirectModel: true,
		},
		{
			Name:              "Bee-3",
			Task:              "Read-only violation",
			Status:            StatusCompleted,
			MergeDiscarded:    true,
			ReadOnlyViolation: true,
			Tier:              "read",
			Provider:          "gemini",
			Model:             "flash",
			MergeError:        "只读档子代理改动了文件，改动已丢弃，未合并",
		},
		{
			Name:     "Bee-4",
			Task:     "Failed task",
			Status:   StatusFailed,
			Tier:     "fast",
			Provider: "gemini",
			Model:    "flash",
			Summary:  "network timeout",
		},
		{
			Name:     "Bee-5",
			Task:     "Stopped task",
			Status:   StatusStopped,
			Tier:     "fast",
			Provider: "gemini",
			Model:    "flash",
			Summary:  "user cancelled",
		},
	}

	report := BuildReport(bees)

	// Bee-1: 档位与模型，无用户指定标记，无越权，无重试提示
	if !strings.Contains(report, "- 档位：deep · 模型：anthropic/opus\n") {
		t.Errorf("report missing Bee-1 tier/model line:\n%s", report)
	}

	// Bee-2: 用户指定模型
	if !strings.Contains(report, "- 档位：deep · 模型：anthropic/opus · 用户指定模型\n") {
		t.Errorf("report missing Bee-2 direct model line:\n%s", report)
	}

	// Bee-3: 越权写入
	if !strings.Contains(report, "- 越权写入：只读档子代理改动了文件，改动已丢弃，未合并\n") {
		t.Errorf("report missing Bee-3 violation line:\n%s", report)
	}

	// Bee-4: 失败重试提示
	if !strings.Contains(report, "- 提示：如需重试，先看失败原因并改 brief，重新派发时保持原档位\n") {
		t.Errorf("report missing retry hint line:\n%s", report)
	}
}

// 验证 shared 模式下只读档 bee 依然强制创建独立 worktree。
func TestManager_ReadOnlyBee_SharedModeGetsWorktree(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var startedRequests []kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) {
		startedRequests = append(startedRequests, req)
	}

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "shared", // shared 模式
		}
	})

	leadID := "lead-shared-ro"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Normal", Task: "normal task", Prompt: "P1", ReadOnly: false},
			{Name: "ReadOnly", Task: "readonly task", Prompt: "P2", ReadOnly: true},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	if len(bees) != 2 {
		t.Fatalf("expected 2 bees, got %d", len(bees))
	}

	// 普通 bee 在 shared 模式下不使用独立 worktree
	if bees[0].WorktreePath != "" {
		t.Errorf("normal bee in shared mode expected empty WorktreePath, got %q", bees[0].WorktreePath)
	}
	// 只读档 bee 即使在 shared 模式下也必须分配独立 worktree
	if bees[1].WorktreePath == "" {
		t.Errorf("readonly bee in shared mode expected non-empty WorktreePath")
	}

	if len(startedRequests) != 2 {
		t.Fatalf("expected 2 started requests, got %d", len(startedRequests))
	}
}

// 验证只读档 bee 在 worktree 中产生脏改动时被标记违规丢弃且不合入主仓库，未改动时正常合入。
func TestManager_ReadOnlyBee_DirtyWorktreeFlaggedAndNotMerged(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	wtBaseDir := t.TempDir()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: wtBaseDir,
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:       true,
			AutoDelegate:  true,
			WorkspaceMode: "worktree",
		}
	})

	leadID := "lead-ro-dirty"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "RO-Dirty", Task: "dirty task", Prompt: "P1", ReadOnly: true},
			{Name: "RO-Clean", Task: "clean task", Prompt: "P2", ReadOnly: true},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}

	dirtyBee := bees[0]
	cleanBee := bees[1]

	// 模拟 RO-Dirty 在其 worktree 中违规修改文件
	dirtyFile := filepath.Join(dirtyBee.WorktreePath, "violation.txt")
	if err := os.WriteFile(dirtyFile, []byte("forbidden edit\n"), 0644); err != nil {
		t.Fatalf("write violation.txt: %v", err)
	}

	// 模拟两个 bee 会话结束
	driver.setSessionState(dirtyBee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "dirty bee done",
	})
	driver.setSessionState(cleanBee.ThreadID, kanban.SessionState{
		Running:           false,
		LastAssistantText: "clean bee done",
	})

	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if len(st.Bees) != 2 {
		t.Fatalf("expected 2 bees in state, got %d", len(st.Bees))
	}

	h0 := st.Bees[0]
	if !h0.ReadOnlyViolation {
		t.Errorf("h0 expected ReadOnlyViolation=true")
	}
	if !h0.MergeDiscarded {
		t.Errorf("h0 expected MergeDiscarded=true")
	}
	if h0.Merged {
		t.Errorf("h0 expected Merged=false")
	}
	if h0.PendingMerge {
		t.Errorf("h0 expected PendingMerge=false")
	}
	if h0.MergeError != "只读档子代理改动了文件，改动已丢弃，未合并" {
		t.Errorf("h0 unexpected MergeError: %q", h0.MergeError)
	}
	// 主仓库不应存在 violation.txt
	if _, err := os.Stat(filepath.Join(dir, "violation.txt")); !os.IsNotExist(err) {
		t.Errorf("violation.txt must not exist in main repo dir")
	}

	h1 := st.Bees[1]
	if h1.ReadOnlyViolation {
		t.Errorf("h1 expected ReadOnlyViolation=false")
	}
	if !h1.Merged {
		t.Errorf("h1 expected Merged=true")
	}
	if h1.MergeError != "" {
		t.Errorf("h1 expected empty MergeError, got %q", h1.MergeError)
	}
}

// 验证档位预览与真实派发共用同一套解析与回退规则：档位自带模型、会话子代理模型、
// 配对 bee_tiers 覆盖以及默认 provider/model/effort 回退。
func TestManager_TierPreview(t *testing.T) {
	const leadID = "lead-tier-preview"
	tiers := []Tier{
		{ID: "fast", Provider: "openai", Model: "gpt-4o-mini"},
		{ID: "deep", Provider: "openai", Model: "gpt-4o"},
		{ID: "bare"},
	}
	baseSettings := Settings{
		Enabled:         true,
		DefaultProvider: "anthropic",
		DefaultModel:    "claude-sonnet",
		DefaultEffort:   "medium",
		DefaultTier:     "fast",
		Tiers:           tiers,
	}

	// 1. 档位自带模型时返回档位模型；档位没有模型时回退到设置默认值（含默认推理强度）。
	plain := NewManager(Options{})
	plain.SetSettingsFunc(func() Settings { return baseSettings })
	got := plain.TierPreview(leadID)
	if rt := got["fast"]; rt.Provider != "openai" || rt.Model != "gpt-4o-mini" || rt.Effort != "" {
		t.Errorf("fast expected openai/gpt-4o-mini with empty effort, got %+v", rt)
	}
	if rt := got["bare"]; rt.Provider != "anthropic" || rt.Model != "claude-sonnet" || rt.Effort != "medium" {
		t.Errorf("bare expected fallback anthropic/claude-sonnet/medium, got %+v", rt)
	}
	if len(got) != len(tiers) {
		t.Errorf("expected one entry per tier, got %d", len(got))
	}

	// 2. 会话里选的子代理模型覆盖档位模型，且不再回退默认推理强度。
	session := NewManager(Options{ThreadContext: func(lid string) ThreadContext {
		if lid != leadID {
			return ThreadContext{}
		}
		return ThreadContext{BeeProvider: "google", BeeModel: "gemini-pro"}
	}})
	session.SetSettingsFunc(func() Settings { return baseSettings })
	got = session.TierPreview(leadID)
	if rt := got["fast"]; rt.Provider != "google" || rt.Model != "gemini-pro" || rt.Effort != "" {
		t.Errorf("fast expected session override google/gemini-pro, got %+v", rt)
	}

	// 3. 会话配对里给该档位配了 bee_tiers 时用配对映射，未配置的档位保持原样。
	pairMgr := NewManager(Options{ThreadContext: func(lid string) ThreadContext {
		if lid != leadID {
			return ThreadContext{PairID: "other-pair"}
		}
		return ThreadContext{PairID: "pair-1"}
	}})
	pairSettings := baseSettings
	pairSettings.Pairs = []Pair{{
		ID: "pair-1",
		BeeTiers: map[string]TierOverride{
			"deep": {Provider: "google", Model: "gemini-flash", Effort: "low"},
		},
	}}
	pairMgr.SetSettingsFunc(func() Settings { return pairSettings })
	got = pairMgr.TierPreview(leadID)
	if rt := got["deep"]; rt.Provider != "google" || rt.Model != "gemini-flash" || rt.Effort != "low" {
		t.Errorf("deep expected pair override google/gemini-flash/low, got %+v", rt)
	}
	if rt := got["fast"]; rt.Provider != "openai" || rt.Model != "gpt-4o-mini" {
		t.Errorf("fast expected unchanged tier model, got %+v", rt)
	}

	// 4. 没有配置档位时返回 nil。
	empty := NewManager(Options{})
	empty.SetSettingsFunc(func() Settings { return Settings{Enabled: true} })
	if got := empty.TierPreview(leadID); got != nil {
		t.Errorf("expected nil preview without tiers, got %v", got)
	}
}

// 宿主代码直接指定 provider/model/effort 与写入白名单时，覆盖档位解析结果，
// 且即使用户消息里没点名该模型也生效；Host 为 nil 时仍按档位解析。
func TestManager_DispatchSingleEntry_HostOverride(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var started []kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = append(started, req) }

	mgr := NewManager(Options{Git: runner, Driver: driver, WorktreeDir: t.TempDir()})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled:      true,
			AutoDelegate: true,
			DefaultTier:  "fast",
			Tiers: []Tier{
				{ID: "fast", Description: "Quick responses", Provider: "openai", Model: "gpt-4o-mini"},
			},
		}
	})

	leadID := "lead-host-override"
	host := &HostOverride{
		Provider:    "anthropic",
		Model:       "claude-sonnet-4",
		Effort:      "high",
		RuntimeMode: "auto",
		WritePaths:  []string{"docs/plans/*.md"},
	}
	bee, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "T", Prompt: "P", Tier: "fast"},
		Host:         host,
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if bee.Provider != "anthropic" || bee.Model != "claude-sonnet-4" || bee.Effort != "high" {
		t.Fatalf("bee 未采用宿主覆盖值: %s/%s/%s", bee.Provider, bee.Model, bee.Effort)
	}
	if len(bee.WritePaths) != 1 || bee.WritePaths[0] != "docs/plans/*.md" {
		t.Fatalf("bee 未采用宿主写入白名单: %v", bee.WritePaths)
	}
	if bee.DirectModel {
		t.Fatal("宿主覆盖不应把 DirectModel 置为 true")
	}
	if len(started) != 1 || started[0].Model != "claude-sonnet-4" || started[0].Provider != "anthropic" {
		t.Fatalf("会话请求未采用宿主覆盖值: %+v", started)
	}
	if started[0].RuntimeMode != "auto" {
		t.Fatalf("会话请求未采用宿主指定的权限模式: %+v", started[0])
	}
	if bee.RuntimeMode != "auto" {
		t.Fatalf("bee 未记录宿主指定的权限模式: %q", bee.RuntimeMode)
	}

	// Host 为 nil 时行为不变：仍用档位解析结果，且没有写入白名单。
	plain, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "T2", Prompt: "P2", Tier: "fast"},
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if plain.Provider != "openai" || plain.Model != "gpt-4o-mini" || plain.Effort != "" {
		t.Fatalf("Host 为 nil 时应沿用档位配置: %s/%s/%s", plain.Provider, plain.Model, plain.Effort)
	}
	if len(plain.WritePaths) != 0 {
		t.Fatalf("Host 为 nil 时不该有写入白名单: %v", plain.WritePaths)
	}
	if plain.RuntimeMode != "" {
		t.Fatalf("Host 为 nil 时不该指定权限模式，应继续继承主线程: %q", plain.RuntimeMode)
	}
	if started[len(started)-1].RuntimeMode != "" {
		t.Fatalf("Host 为 nil 时会话请求不该带权限模式: %+v", started[len(started)-1])
	}
}

// 编排未开启时：宿主指定了覆盖项就返回 ErrSwarmDisabled，否则保持静默返回零值。
func TestManager_DispatchSingleEntry_HostOverrideSwarmDisabled(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{})
	mgr.SetSettingsFunc(func() Settings { return Settings{Enabled: false} })

	bee, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-disabled",
		Entry:        Delegation{Task: "T", Prompt: "P"},
		Host:         &HostOverride{Model: "claude-sonnet-4"},
	})
	if !errors.Is(err, ErrSwarmDisabled) {
		t.Fatalf("编排关闭且 Host 非空应返回 ErrSwarmDisabled，实际 %v", err)
	}
	if bee.ThreadID != "" {
		t.Fatalf("不该派发 bee，实际 %s", bee.ThreadID)
	}

	bee, notices, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-disabled",
		Entry:        Delegation{Task: "T", Prompt: "P"},
	})
	if err != nil {
		t.Fatalf("Host 为 nil 时应静默返回 nil 错误，实际 %v", err)
	}
	if bee.ThreadID != "" || len(notices) != 0 {
		t.Fatalf("Host 为 nil 时不该派发 bee: bee=%+v notices=%v", bee, notices)
	}
}
