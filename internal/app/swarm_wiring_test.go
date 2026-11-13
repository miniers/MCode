package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/config"
	"mcode/internal/git"
	"mcode/internal/harness"
	"mcode/internal/harness/policy"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
	"mcode/internal/testenv"
)

type swarmFlowHarness struct {
	leadStarted chan struct{}
	leadRelease chan struct{}
	beeStarted  chan struct{}
	beeRelease  chan struct{}
}

func (*swarmFlowHarness) Kind() harness.Kind { return "swarm-flow" }
func (h *swarmFlowHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	reply := "done"
	var release chan struct{}
	switch {
	case strings.Contains(turn.Prompt, "LEAD_DELEGATE"):
		reply = "```swarm\n" + `[{"prompt":"HEAD_WORK","bee":"Ives"}]` + "\n```"
		close(h.leadStarted)
		release = h.leadRelease
	case swarm.IsBeePrompt(turn.Prompt) && strings.Contains(turn.Prompt, "HEAD_WORK"):
		reply = "bee done"
		if err := os.WriteFile(filepath.Join(turn.WorkDir, "bee-result.txt"), []byte("landed"), 0600); err != nil {
			return err
		}
		close(h.beeStarted)
		release = h.beeRelease
	case strings.Contains(turn.Prompt, "BAD_JSON"):
		reply = "```swarm\n{broken}\n```"
	}
	emit(harness.Event{Type: harness.EventText, Text: reply})
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func swarmRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init"}, {"commit", "--allow-empty", "-m", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func waitSwarm(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("Swarm flow timed out")
}

// sendSwarmMessage 以「后端接受这条消息」为条件发送：会话摘要里的 IsRunning 在轮次收尾钩子
// （Swarm 派发）跑完前就已置为 false，而 SendMessage 判的是内部运行标记，这段时间内直接发送
// 会被拒成 -32000「already running」。钩子结束时间不可预测，所以按接收条件重试而不是加长 sleep。
func sendSwarmMessage(t *testing.T, ctx context.Context, backend *remote.MemoryBackend, threadID, text string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: threadID, Text: text})
		if err == nil {
			return
		}
		var rpcErr *wire.RPCError
		if !errors.As(err, &rpcErr) || rpcErr.Code != wire.ErrCodeOperationRejected || time.Now().After(deadline) {
			thread, _ := backend.GetThread(ctx, threadID)
			t.Fatalf("发送 %q 失败: %v（isRunning=%v attention=%q swarmBusy=%v）",
				text, err, thread.IsRunning, thread.Attention, thread.SwarmBusy)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSwarmChatDispatchMergeReportAndQueuedFollowUp(t *testing.T) {
	testSwarmChatDispatchMergeReportAndQueuedFollowUp(t, false)
}

func TestSwarmChatReviewBeforeReportAndQueuedFollowUp(t *testing.T) {
	testSwarmChatDispatchMergeReportAndQueuedFollowUp(t, true)
}

func testSwarmChatDispatchMergeReportAndQueuedFollowUp(t *testing.T, review bool) {
	t.Helper()
	testenv.TempHome(t)
	ctx := context.Background()
	dir := swarmRepo(t)
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	backend := remote.NewMemoryBackend(hub)
	defer backend.Close()
	off := false
	backend.SetGeneralSettingsFunc(func() config.Config {
		return config.Config{Session: config.SessionConfig{ConciseRepliesEnabled: &off}}
	})
	h := &swarmFlowHarness{
		leadStarted: make(chan struct{}), leadRelease: make(chan struct{}),
		beeStarted: make(chan struct{}), beeRelease: make(chan struct{}),
	}
	backend.SetHarness(h, dir, harness.ModeFullAccess)
	backend.AddProject(wire.ProjectSummary{ID: "project", Name: "Project", Path: dir})
	backend.AddThread(wire.ThreadSummary{ID: "lead", ProjectID: "project", Provider: "swarm-flow"}, nil)
	mgr := swarm.NewManager(swarm.Options{
		Git: git.NewRunner("", 4), Driver: backend, WorktreeDir: t.TempDir(), PollInterval: testSwarmPollInterval,
		OnReport: func(lead, text string) error {
			return backend.SendSwarmReport(ctx, lead, text)
		},
		OnBeesIdle: func(ctx context.Context, leadThreadID string) {
			_ = backend.DrainQueuedFollowUps(ctx, leadThreadID)
		},
	})
	// 人工审查档：bee 完成后先停住等决定，主会话在决定前收不到汇报。
	// 未注入读取函数时 Manager.Settings() 的默认值要一并带上，否则派发会被 Enabled/AutoDelegate 的零值拒绝。
	if review {
		mgr.SetSettingsFunc(func() swarm.Settings {
			return swarm.Settings{
				Enabled:       true,
				AutoDelegate:  true,
				WorkspaceMode: "worktree",
				ReviewMode:    "human",
			}
		})
	}
	mgr.SetEnabled("lead", true)
	wireSwarm(ctx, mgr, backend, false)
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "LEAD_DELEGATE"}); err != nil {
		t.Fatalf("发送 LEAD_DELEGATE 失败: %v", err)
	}
	waitSwarm(t, func() bool {
		select {
		case <-h.leadStarted:
			return true
		default:
			return false
		}
	})
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "FOLLOW_UP", WhenRunning: "queue"}); err != nil {
		t.Fatalf("发送 FOLLOW_UP 失败: %v", err)
	}
	close(h.leadRelease)
	waitSwarm(t, func() bool {
		thread, _ := backend.GetThread(ctx, "lead")
		return !thread.IsRunning && len(mgr.State("lead").Bees) == 1
	})
	state := mgr.State("lead")
	if state.Bees[0].Name != "Aspen" {
		t.Fatalf("unknown name did not fall back: %+v", state.Bees)
	}
	queue, _ := backend.QueueList(ctx, "lead")
	if len(queue.FollowUps) != 1 {
		t.Fatal("queued follow-up ran before bee report")
	}
	waitSwarm(t, func() bool {
		select {
		case <-h.beeStarted:
			return true
		default:
			return false
		}
	})
	close(h.beeRelease)
	waitSwarm(t, func() bool {
		thread, _ := backend.GetThread(ctx, state.Bees[0].ThreadID)
		return !thread.IsRunning
	})
	mgr.Tick(ctx)
	if review {
		bee := mgr.State("lead").Bees[0]
		if !bee.PendingMerge {
			t.Fatal("bee 完成后未等待审查")
		}
		if bee.ReviewState != swarm.ReviewNeedsHuman {
			t.Fatalf("人工审查档下应等人工决定，实际 %s", bee.ReviewState)
		}
		if _, err := os.Stat(filepath.Join(dir, "bee-result.txt")); !os.IsNotExist(err) {
			t.Fatalf("批准前不应落地改动: %v", err)
		}
		// 主会话空闲时的新消息也必须继续排队，不能越过待审查的成果。
		if _, err := backend.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "AFTER_HEAD", WhenRunning: "queue"}); err != nil {
			t.Fatalf("发送 AFTER_HEAD 失败: %v", err)
		}
		queue, _ := backend.QueueList(ctx, "lead")
		// 审查未出结论前不汇报，队列里只有两条跟进消息。
		if len(queue.FollowUps) != 2 {
			t.Fatalf("待审查期间应只保留跟进消息: %+v", queue)
		}
		if last := queue.FollowUps[len(queue.FollowUps)-1].Text; last != "AFTER_HEAD" {
			t.Fatalf("AFTER_HEAD 未排在队列末尾: %q", last)
		}
		if _, err := mgr.ApproveMerge(ctx, "lead", bee.ThreadID); err != nil {
			t.Fatal(err)
		}
		// 审查通过后的汇报会作为高优跟进入队，并由 OnBeesIdle 推进成下一轮；
		// 队列内容何时被取走取决于后端调度，这里不在同一 tick 后做长度断言。
		mgr.Tick(ctx)
	}
	waitSwarm(t, func() bool {
		history, _ := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "lead"})
		want := "FOLLOW_UP"
		if review {
			want = "AFTER_HEAD"
		}
		for _, msg := range history.Messages {
			if msg.Role == "user" && msg.Text == want {
				return true
			}
		}
		return false
	})
	history, _ := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "lead"})
	var inputs []string
	for _, msg := range history.Messages {
		if msg.Role == "user" {
			inputs = append(inputs, msg.Text)
		}
	}
	wantInputs := 3
	if review {
		wantInputs = 4
	}
	if len(inputs) != wantInputs || !strings.HasPrefix(inputs[1], swarm.ReportPrefix) || !strings.Contains(inputs[1], "bee done") {
		t.Fatalf("expected report before follow-up: %q", inputs)
	}
	if data, err := os.ReadFile(filepath.Join(dir, "bee-result.txt")); err != nil || string(data) != "landed" {
		t.Fatalf("bee work did not land: %q, %v", data, err)
	}
	waitSwarm(t, func() bool {
		thread, _ := backend.GetThread(ctx, "lead")
		return !thread.IsRunning
	})
	sendSwarmMessage(t, ctx, backend, "lead", "BAD_JSON")
	waitSwarm(t, func() bool {
		// 解析失败的块改为以 user 消息回送主代理（主代理再跑一轮），不再是 system 通知。
		history, _ := backend.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "lead"})
		for _, msg := range history.Messages {
			if msg.Role == "user" && strings.HasPrefix(msg.Text, swarm.NoticePrefix) &&
				strings.Contains(msg.Text, "委派块没能解析") {
				return true
			}
		}
		return false
	})
}

// TestSwarmSettingsFromConfigMapsLifecycleAndPairs 验证自动清理/空闲隐藏/Codex 归档开关与模型配对到 swarm.Settings 的映射，
// 以及 config.Worker* 到 swarm.Pair Bee* 的换算和 nil effort 的 nil 安全解引用。
func TestSwarmSettingsFromConfigMapsLifecycleAndPairs(t *testing.T) {
	leadEffort := "high"
	workerEffort := "low"
	cfg := config.OrchestrationConfig{
		AutoClearFinished:     true,
		AutoHidesIdleBees:     true,
		ArchivesCodexBees:     true,
		RequestTimeoutMinutes: 240,
		Pairs: []config.SwarmPair{{
			ID:             "pair-1",
			Name:           "快速",
			LeadProvider:   "claude",
			LeadModel:      "opus",
			LeadEffort:     &leadEffort,
			LeadFastMode:   true,
			WorkerProvider: "codex",
			WorkerModel:    "gpt-5",
			WorkerEffort:   &workerEffort,
			WorkerFastMode: true,
		}},
	}

	got := SwarmSettingsFromConfig(cfg)
	if !got.AutoClearFinished || !got.AutoHidesIdleBees || !got.ArchivesCodexBees {
		t.Fatalf("生命周期开关未映射: %+v", got)
	}
	if got.RequestTimeoutMinutes != 240 {
		t.Fatalf("请求总时长预算未映射: %+v", got)
	}
	if len(got.Pairs) != 1 {
		t.Fatalf("配对未映射: %+v", got.Pairs)
	}
	p := got.Pairs[0]
	if p.ID != "pair-1" || p.Name != "快速" ||
		p.LeadProvider != "claude" || p.LeadModel != "opus" || p.LeadEffort != "high" || !p.LeadFastMode ||
		p.BeeProvider != "codex" || p.BeeModel != "gpt-5" || p.BeeEffort != "low" || !p.BeeFastMode {
		t.Fatalf("配对字段映射不正确: %+v", p)
	}

	// nil 的 effort 指针应安全解引用为空串。
	got2 := SwarmSettingsFromConfig(config.OrchestrationConfig{Pairs: []config.SwarmPair{{ID: "p2"}}})
	if len(got2.Pairs) != 1 || got2.Pairs[0].LeadEffort != "" || got2.Pairs[0].BeeEffort != "" {
		t.Fatalf("nil effort 未安全解引用: %+v", got2.Pairs)
	}
}

// TestSwarmSettingsFromConfigTiersAndBeeTiers 验证档位配置映射、默认档位映射、
// 配对 BeeTiers 转换及 FastMode 指针独立性（nil map 保持 nil）。
func TestSwarmSettingsFromConfigTiersAndBeeTiers(t *testing.T) {
	fastModeTrue := true
	cfg := config.OrchestrationConfig{
		DefaultTier: "edit",
		Tiers: []config.SwarmTier{
			{
				ID:           "read",
				Name:         "只读",
				Description:  "只读模式",
				ReadOnly:     true,
				Provider:     "claude",
				Model:        "haiku",
				Effort:       "low",
				FastMode:     false,
				SystemPrompt: "read prompt",
			},
			{
				ID:           "edit",
				Name:         "执行",
				Description:  "常规修改",
				ReadOnly:     false,
				Provider:     "codex",
				Model:        "gpt-5",
				Effort:       "medium",
				FastMode:     true,
				SystemPrompt: "edit prompt",
			},
		},
		Pairs: []config.SwarmPair{
			{
				ID:   "pair-with-tiers",
				Name: "配对1",
				BeeTiers: map[string]config.SwarmTierOverride{
					"edit": {
						Provider: "custom",
						Model:    "deepseek",
						Effort:   "high",
						FastMode: &fastModeTrue,
					},
					"read": {
						Provider: "claude",
						FastMode: nil,
					},
				},
			},
			{
				ID:       "pair-without-tiers",
				Name:     "配对2",
				BeeTiers: nil,
			},
		},
	}

	got := SwarmSettingsFromConfig(cfg)

	// 1. 验证 DefaultTier
	if got.DefaultTier != "edit" {
		t.Errorf("DefaultTier = %q, want %q", got.DefaultTier, "edit")
	}

	// 2. 验证 Tiers
	if len(got.Tiers) != 2 {
		t.Fatalf("Tiers length = %d, want 2", len(got.Tiers))
	}
	t0 := got.Tiers[0]
	if t0.ID != "read" || t0.Name != "只读" || t0.Description != "只读模式" || !t0.ReadOnly ||
		t0.Provider != "claude" || t0.Model != "haiku" || t0.Effort != "low" || t0.FastMode || t0.SystemPrompt != "read prompt" {
		t.Errorf("Tier[0] 转换不符合预期: %+v", t0)
	}
	t1 := got.Tiers[1]
	if t1.ID != "edit" || t1.Name != "执行" || t1.Description != "常规修改" || t1.ReadOnly ||
		t1.Provider != "codex" || t1.Model != "gpt-5" || t1.Effort != "medium" || !t1.FastMode || t1.SystemPrompt != "edit prompt" {
		t.Errorf("Tier[1] 转换不符合预期: %+v", t1)
	}

	// 3. 验证 Pairs BeeTiers
	if len(got.Pairs) != 2 {
		t.Fatalf("Pairs length = %d, want 2", len(got.Pairs))
	}
	p0 := got.Pairs[0]
	if p0.BeeTiers == nil || len(p0.BeeTiers) != 2 {
		t.Fatalf("Pair[0].BeeTiers 长度不匹配: %+v", p0.BeeTiers)
	}
	ovEdit, ok := p0.BeeTiers["edit"]
	if !ok || ovEdit.Provider != "custom" || ovEdit.Model != "deepseek" || ovEdit.Effort != "high" || ovEdit.FastMode == nil || !*ovEdit.FastMode {
		t.Fatalf("Pair[0].BeeTiers['edit'] 转换不符合预期: %+v", ovEdit)
	}

	// 验证 FastMode 指针独立性：修改源指针指向的值，不应影响转换后的副本
	fastModeTrue = false
	if !*ovEdit.FastMode {
		t.Errorf("FastMode 指针未独立深拷贝")
	}

	ovRead, ok := p0.BeeTiers["read"]
	if !ok || ovRead.Provider != "claude" || ovRead.FastMode != nil {
		t.Fatalf("Pair[0].BeeTiers['read'] nil FastMode 未保持 nil: %+v", ovRead)
	}

	// 验证 nil map 保持 nil
	p1 := got.Pairs[1]
	if p1.BeeTiers != nil {
		t.Errorf("nil BeeTiers 应保持 nil，实际为 %+v", p1.BeeTiers)
	}
}

type promptCaptureHarness struct {
	mu         sync.Mutex
	lastPrompt string
	prompts    chan string
}

func newPromptCaptureHarness() *promptCaptureHarness {
	return &promptCaptureHarness{
		prompts: make(chan string, 4),
	}
}

func (c *promptCaptureHarness) Kind() harness.Kind { return "prompt-capture" }
func (c *promptCaptureHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	c.mu.Lock()
	c.lastPrompt = turn.Prompt
	c.mu.Unlock()
	if c.prompts != nil {
		c.prompts <- turn.Prompt
	}
	emit(harness.Event{Type: harness.EventText, Text: "ok"})
	return nil
}

// TestSwarmLeadPrefaceDecoratorAppendsTierSection 验证在开启 Swarm 的 lead 会话中，
// 当配置了 Tiers 时，前言会追加 TierPromptSection 并以空行分隔；未配置时无档位段落。
func TestSwarmLeadPrefaceDecoratorAppendsTierSection(t *testing.T) {
	ctx := context.Background()
	dir := swarmRepo(t)
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	b := remote.NewMemoryBackend(hub)
	defer b.Close()

	h := newPromptCaptureHarness()
	b.SetHarness(h, dir, harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "P", Path: dir})
	b.AddThread(wire.ThreadSummary{ID: "lead-1", ProjectID: "p", Provider: "prompt-capture"}, nil)

	mgr := swarm.NewManager(swarm.Options{
		Git:          git.NewRunner("", 4),
		Driver:       b,
		WorktreeDir:  t.TempDir(),
		PollInterval: testSwarmPollInterval,
	})
	mgr.SetSettingsFunc(func() swarm.Settings {
		return swarm.Settings{
			Enabled:     true,
			DefaultTier: "edit",
			Tiers: []swarm.Tier{
				{ID: "read", Description: "只读"},
				{ID: "edit", Description: "常规编辑"},
			},
		}
	})
	mgr.SetEnabled("lead-1", true)

	wireSwarm(ctx, mgr, b, false)

	_, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead-1", Text: "user prompt"})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	var captured string
	select {
	case captured = <-h.prompts:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待 lead-1 捕获 prompt 超时")
	}

	sec := swarm.TierPromptSection(mgr.Settings().Tiers, mgr.Settings().DefaultTier, nil)
	if !strings.Contains(captured, swarm.LeadPolicy(false, "off", false)) {
		t.Fatalf("前言未包含 LeadPolicy:\ngot:\n%s\nwant:\n%s", captured, swarm.LeadPolicy(false, "off", false))
	}
	if !strings.Contains(captured, swarm.LeadPolicy(false, "off", false)+"\n\n"+sec) {
		t.Fatalf("前言未正确注入档位段落:\ngot:\n%s\nwant:\n%s", captured, swarm.LeadPolicy(false, "off", false)+"\n\n"+sec)
	}
	if !strings.HasSuffix(captured, "\n\n---\n\nuser prompt") {
		t.Fatalf("前言未以预期 prompt 结尾:\ngot:\n%s\nwant:\n%s", captured, "\n\n---\n\nuser prompt")
	}

	// 当 Tiers 为空时，不追加档位段落
	mgr.SetSettingsFunc(func() swarm.Settings {
		return swarm.Settings{
			Enabled: true,
		}
	})
	b.AddThread(wire.ThreadSummary{ID: "lead-2", ProjectID: "p", Provider: "prompt-capture"}, nil)
	mgr.SetEnabled("lead-2", true)
	_, err = b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead-2", Text: "user prompt 2"})
	if err != nil {
		t.Fatalf("SendMessage 2 failed: %v", err)
	}

	var capturedNoTiers string
	select {
	case capturedNoTiers = <-h.prompts:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待 lead-2 捕获 prompt 超时")
	}

	if !strings.Contains(capturedNoTiers, swarm.LeadPolicy(false, "off", false)) {
		t.Fatalf("无档位时前言未包含 LeadPolicy:\ngot:\n%s\nwant:\n%s", capturedNoTiers, swarm.LeadPolicy(false, "off", false))
	}
	if strings.Contains(capturedNoTiers, "Bee tiers:") {
		t.Fatalf("无档位时前言不应包含档位段落:\ngot:\n%s\nunwanted:\n%s", capturedNoTiers, "Bee tiers:")
	}
	if !strings.HasSuffix(capturedNoTiers, "\n\n---\n\nuser prompt 2") {
		t.Fatalf("无档位时前言未以预期 prompt 结尾:\ngot:\n%s\nwant:\n%s", capturedNoTiers, "\n\n---\n\nuser prompt 2")
	}
}

// TestSwarmLeadPolicyTracker 验证静态策略追踪器在通知轮次精简规则的核心逻辑：
// 1. 用户 prompt 得到完整规则并记录当前静态文本；
// 2. 紧接着的通知 prompt 只有 Marker、提醒和 limits，不含 SwarmLead 原文；
// 3. 切换只规划模式后的通知 prompt 因静态文本变化重新得到完整规则；
// 4. 没有记录的线程收到通知 prompt 时得到完整规则；
// 5. 线程 Forget 后收到通知 prompt 重新得到完整规则。
func TestSwarmLeadPolicyTracker(t *testing.T) {
	tracker := newSwarmLeadPolicyTracker()
	threadID := "thread-1"

	leadPolicyDefault := swarm.LeadPolicy(false, "off", false)
	leadPolicyPlansOnly := swarm.LeadPolicy(true, "off", false)
	tiers := swarm.TierPromptSection([]swarm.Tier{{ID: "t1", Description: "d1"}}, "t1", nil)
	limits := swarm.LimitsPromptSection(swarm.RequestLimits{MaxRounds: 5})

	// 1. 用户 prompt（非通知）：得到完整规则
	sec1 := tracker.RenderSections(threadID, false, false, false, leadPolicyDefault, tiers, limits)
	if sec1.SwarmLead != leadPolicyDefault || sec1.SwarmTiers != tiers || sec1.SwarmLimits != limits {
		t.Fatalf("非通知轮次应返回完整 sections: %+v", sec1)
	}
	block1 := policy.Render(hostPolicyCaps(), sec1)
	if !strings.Contains(block1, policy.Marker) || !strings.Contains(block1, leadPolicyDefault) || !strings.Contains(block1, tiers) {
		t.Fatalf("非通知轮次渲染结果应包含完整策略与档位:\n%s", block1)
	}

	// 2. 紧接着的通知 prompt：静态文本未变，得到精简规则
	sec2 := tracker.RenderSections(threadID, true, false, false, leadPolicyDefault, tiers, limits)
	if sec2.SwarmLead != swarm.LeadNoticePolicy(false, false) {
		t.Fatalf("通知轮次 SwarmLead 应换为提醒语:\ngot: %s\nwant: %s", sec2.SwarmLead, swarm.LeadNoticePolicy(false, false))
	}
	if sec2.SwarmTiers != "" {
		t.Fatalf("通知轮次 SwarmTiers 应留空，got: %s", sec2.SwarmTiers)
	}
	if sec2.SwarmLimits != limits {
		t.Fatalf("通知轮次 SwarmLimits 应保持原样:\ngot: %s\nwant: %s", sec2.SwarmLimits, limits)
	}
	block2 := policy.Render(hostPolicyCaps(), sec2)
	if !strings.Contains(block2, policy.Marker) {
		t.Fatalf("精简版应包含 policy.Marker:\n%s", block2)
	}
	if !strings.Contains(block2, swarm.LeadNoticeReminder) {
		t.Fatalf("精简版应包含提醒常量文案:\n%s", block2)
	}
	if !strings.Contains(block2, limits) {
		t.Fatalf("精简版应包含 limits:\n%s", block2)
	}
	if strings.Contains(block2, leadPolicyDefault) {
		t.Fatalf("精简版不应包含 SwarmLead 原文:\n%s", block2)
	}
	if strings.Contains(block2, "Bee tiers:") {
		t.Fatalf("精简版不应包含档位说明原文:\n%s", block2)
	}

	// 3. 切换只规划模式后：通知 prompt 因静态文本变化重新得到完整规则
	sec3 := tracker.RenderSections(threadID, true, true, false, leadPolicyPlansOnly, tiers, limits)
	if sec3.SwarmLead != leadPolicyPlansOnly || sec3.SwarmTiers != tiers {
		t.Fatalf("静态文本变化后通知轮次应重新获得完整规则: %+v", sec3)
	}
	block3 := policy.Render(hostPolicyCaps(), sec3)
	if !strings.Contains(block3, leadPolicyPlansOnly) || !strings.Contains(block3, tiers) {
		t.Fatalf("切换只规划后应渲染完整只规划规则:\n%s", block3)
	}
	if strings.Contains(block3, swarm.LeadNoticeReminder) {
		t.Fatalf("重新获得完整规则时不应包含精简提醒文案:\n%s", block3)
	}

	// 紧接着再次发送通知 prompt：进入只规划模式的精简版
	sec3Compact := tracker.RenderSections(threadID, true, true, false, leadPolicyPlansOnly, tiers, limits)
	if sec3Compact.SwarmLead != swarm.LeadNoticePolicy(true, false) || sec3Compact.SwarmTiers != "" {
		t.Fatalf("只规划后续通知轮次应返回只规划精简版: %+v", sec3Compact)
	}
	block3Compact := policy.Render(hostPolicyCaps(), sec3Compact)
	if !strings.Contains(block3Compact, "You only plan, every change goes to a bee.") {
		t.Fatalf("只规划精简版应包含仅规划提醒句子:\n%s", block3Compact)
	}

	// 4. 没有记录的线程收到通知 prompt 时得到完整规则
	freshThread := "thread-fresh"
	sec4 := tracker.RenderSections(freshThread, true, false, false, leadPolicyDefault, tiers, limits)
	if sec4.SwarmLead != leadPolicyDefault || sec4.SwarmTiers != tiers {
		t.Fatalf("无记录线程首次收到通知 prompt 应得到完整规则: %+v", sec4)
	}

	// 5. 清理记录后再次收到通知 prompt 重新得到完整规则
	tracker.Forget(freshThread)
	sec5 := tracker.RenderSections(freshThread, true, false, false, leadPolicyDefault, tiers, limits)
	if sec5.SwarmLead != leadPolicyDefault || sec5.SwarmTiers != tiers {
		t.Fatalf("清理记录后收到通知 prompt 应重新得到完整规则: %+v", sec5)
	}
}

// TestSwarmLeadPromptDecoratorNoticeLifecycle 通过端到端 backend 消息流程验证：
// 用户 prompt 得到完整规则；通知 prompt 得到精简版；切换只规划后重新得到完整规则；无记录线程收到通知得到完整规则。
func TestSwarmLeadPromptDecoratorNoticeLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := swarmRepo(t)
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	b := remote.NewMemoryBackend(hub)
	defer b.Close()

	h := newPromptCaptureHarness()
	b.SetHarness(h, dir, harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "P", Path: dir})
	b.AddThread(wire.ThreadSummary{ID: "lead-life", ProjectID: "p", Provider: "prompt-capture"}, nil)

	mgr := swarm.NewManager(swarm.Options{
		Git:          git.NewRunner("", 4),
		Driver:       b,
		WorktreeDir:  t.TempDir(),
		PollInterval: testSwarmPollInterval,
	})
	mgr.SetSettingsFunc(func() swarm.Settings {
		return swarm.Settings{
			Enabled:     true,
			DefaultTier: "edit",
			Tiers: []swarm.Tier{
				{ID: "edit", Description: "常规编辑"},
			},
		}
	})
	mgr.SetEnabled("lead-life", true)

	var (
		modeMu    sync.Mutex
		plansOnly bool
	)
	wireSwarm(ctx, mgr, b, false, func(string) bool {
		modeMu.Lock()
		defer modeMu.Unlock()
		return plansOnly
	})

	// 1. 发送用户 prompt，应得到完整规则
	sendSwarmMessage(t, ctx, b, "lead-life", "user first prompt")
	var capturedUser string
	select {
	case capturedUser = <-h.prompts:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待 lead-life 捕获用户 prompt 超时")
	}
	if !strings.Contains(capturedUser, policy.Marker) {
		t.Fatalf("用户 prompt 未包含 Marker:\n%s", capturedUser)
	}
	if !strings.Contains(capturedUser, swarm.LeadPolicy(false, "off", false)) {
		t.Fatalf("用户 prompt 未包含完整 LeadPolicy:\n%s", capturedUser)
	}
	if !strings.Contains(capturedUser, "Bee tiers:") {
		t.Fatalf("用户 prompt 未包含档位段落:\n%s", capturedUser)
	}

	// 2. 紧接着发送通知 prompt（通过 swarm.NoticePrefix 构造），应得到精简版
	noticePrompt := swarm.NoticePrefix + "子代理汇报：Ives 已结束工作。"
	sendSwarmMessage(t, ctx, b, "lead-life", noticePrompt)
	var capturedNotice string
	select {
	case capturedNotice = <-h.prompts:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待 lead-life 捕获通知 prompt 超时")
	}
	if !strings.Contains(capturedNotice, policy.Marker) {
		t.Fatalf("通知 prompt 未包含 Marker:\n%s", capturedNotice)
	}
	if !strings.Contains(capturedNotice, swarm.LeadNoticeReminder) {
		t.Fatalf("通知 prompt 未包含精简提醒文案:\n%s", capturedNotice)
	}
	if !strings.Contains(capturedNotice, "[Swarm limits]") {
		t.Fatalf("通知 prompt 未包含 limits 段落:\n%s", capturedNotice)
	}
	if strings.Contains(capturedNotice, swarm.LeadPolicy(false, "off", false)) {
		t.Fatalf("通知 prompt 不应包含完整 LeadPolicy 原文:\n%s", capturedNotice)
	}
	if strings.Contains(capturedNotice, "Bee tiers:") {
		t.Fatalf("通知 prompt 不应包含档位段落:\n%s", capturedNotice)
	}

	// 3. 切换只规划模式后发送通知 prompt，应重新得到完整规则
	modeMu.Lock()
	plansOnly = true
	modeMu.Unlock()
	noticePrompt2 := swarm.NoticePrefix + "子代理汇报：Ives 再次汇报。"
	sendSwarmMessage(t, ctx, b, "lead-life", noticePrompt2)
	var capturedNotice2 string
	select {
	case capturedNotice2 = <-h.prompts:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待 lead-life 捕获只规划通知 prompt 超时")
	}
	if !strings.Contains(capturedNotice2, swarm.LeadPolicy(true, "off", false)) {
		t.Fatalf("切换只规划后通知 prompt 未重新得到完整只规划规则:\n%s", capturedNotice2)
	}
	if !strings.Contains(capturedNotice2, "Bee tiers:") {
		t.Fatalf("切换只规划后通知 prompt 未重新包含档位段落:\n%s", capturedNotice2)
	}
	if strings.Contains(capturedNotice2, swarm.LeadNoticeReminder) {
		t.Fatalf("重新获得完整规则时通知 prompt 不应包含精简提醒:\n%s", capturedNotice2)
	}

	// 4. 没有记录的线程收到通知 prompt 时得到完整规则
	b.AddThread(wire.ThreadSummary{ID: "lead-fresh-lifecycle", ProjectID: "p", Provider: "prompt-capture"}, nil)
	mgr.SetEnabled("lead-fresh-lifecycle", true)
	freshNoticePrompt := swarm.NoticePrefix + "子代理汇报：新线程通知。"
	sendSwarmMessage(t, ctx, b, "lead-fresh-lifecycle", freshNoticePrompt)
	var capturedFresh string
	select {
	case capturedFresh = <-h.prompts:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待 lead-fresh-lifecycle 捕获通知 prompt 超时")
	}
	if !strings.Contains(capturedFresh, swarm.LeadPolicy(true, "off", false)) {
		t.Fatalf("无记录新线程收到通知 prompt 应得到完整规则:\n%s", capturedFresh)
	}
	if !strings.Contains(capturedFresh, "Bee tiers:") {
		t.Fatalf("无记录新线程收到通知 prompt 应包含档位段落:\n%s", capturedFresh)
	}
}

func TestSwarmPromptDecoratorSkipsUnregisteredHelperThread(t *testing.T) {
	ctx := context.Background()
	dir := swarmRepo(t)
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	b := remote.NewMemoryBackend(hub)
	defer b.Close()

	h := newPromptCaptureHarness()
	b.SetHarness(h, dir, harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "P", Path: dir})
	b.AddThread(wire.ThreadSummary{ID: "main", ProjectID: "p", Provider: "prompt-capture"}, nil)
	b.AddThread(wire.ThreadSummary{ID: "helper", ProjectID: "p", Provider: "prompt-capture"}, nil)
	b.AddThread(wire.ThreadSummary{ID: "plain", ProjectID: "p", Provider: "prompt-capture"}, nil)
	// 辅助标记已设置，但 helper 没有登记到 mgr 的 bees 里。
	b.SetThreadRelation("helper", "main", true)

	mgr := swarm.NewManager(swarm.Options{
		Git:          git.NewRunner("", 4),
		Driver:       b,
		WorktreeDir:  t.TempDir(),
		PollInterval: testSwarmPollInterval,
	})
	mgr.SetSettingsFunc(func() swarm.Settings {
		return swarm.Settings{
			Enabled:     true,
			DefaultTier: "edit",
			Tiers:       []swarm.Tier{{ID: "edit", Description: "常规编辑"}},
		}
	})
	// helper 即便开启编排，也不能被当成主会话；plain 未开启编排。
	mgr.SetEnabled("helper", true)
	wireSwarm(ctx, mgr, b, false)

	capture := func(threadID, text string) string {
		t.Helper()
		sendSwarmMessage(t, ctx, b, threadID, text)
		select {
		case got := <-h.prompts:
			return got
		case <-time.After(5 * time.Second):
			t.Fatalf("等待 %s 捕获 prompt 超时", threadID)
			return ""
		}
	}

	const text = "helper first prompt"
	if got := capture("helper", text); got != text {
		t.Fatalf("未登记的辅助会话 prompt 应保持原文，实际:\n%s", got)
	}

	got := capture("plain", "plain first prompt")
	if !strings.HasPrefix(got, swarm.NativeSubagentRule) {
		t.Fatalf("普通线程应带 NativeSubagentRule 前缀:\n%s", got)
	}
}

func TestSwarmStoreAdapterReopenAndProjectResolution(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mcode.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	mgr := swarm.NewManager(swarm.Options{Persister: swarmStoreAdapter{s: s}, PollInterval: testSwarmPollInterval})
	mgr.SetEnabled("lead", true)
	if err := (swarmStoreAdapter{s: s}).SaveLead(ctx, swarm.LeadRecord{
		LeadThreadID: "lead", Enabled: true, NextIndex: 7,
		Bees: []swarm.Bee{
			{ThreadID: "head", Name: "Aspen", Status: swarm.StatusRunning},
			{ThreadID: "pending", Status: swarm.StatusCompleted, PendingMerge: true, MergePatch: []byte("审查补丁")},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restarted := swarm.NewManager(swarm.Options{Persister: swarmStoreAdapter{s: s}, PollInterval: testSwarmPollInterval})
	if err := restarted.Restore(ctx); err != nil {
		t.Fatal(err)
	}
	state := restarted.State("lead")
	if !state.Enabled || len(state.Bees) != 2 || state.Bees[0].Status != swarm.StatusStopped ||
		!strings.Contains(state.Bees[0].Summary, "重启") {
		t.Fatalf("incorrect restored state: %+v", state)
	}
	if !state.Bees[1].PendingMerge || string(state.Bees[1].MergePatch) != "审查补丁" {
		t.Fatalf("待审查补丁未持久化: %+v", state.Bees[1])
	}

	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	b := remote.NewMemoryBackend(hub)
	defer b.Close()
	root := swarmRepo(t)
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	b.AddProject(wire.ProjectSummary{ID: "root", Name: "Root", Path: root})
	resolve := resolveSwarmProject(b)
	id, dir, ok := resolve(ctx, nested)
	if !ok || id != "root" || dir != root {
		t.Fatalf("nested project resolution: %s, %s, %v", id, dir, ok)
	}
	other := swarmRepo(t)
	id, dir, ok = resolve(ctx, other)
	if !ok || id == "" || dir != other {
		t.Fatalf("unregistered repository resolution: %s, %s, %v", id, dir, ok)
	}
}

func TestSwarmDirectMessageRoutingEnabled(t *testing.T) {
	ctx := context.Background()
	dir := swarmRepo(t)
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	b := remote.NewMemoryBackend(hub)
	defer b.Close()
	b.SetHarness(&testMockHarness{kind: "noop"}, dir, harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "P", Path: dir})
	b.AddThread(wire.ThreadSummary{ID: "lead", ProjectID: "p", Provider: "noop"}, nil)
	mgr := swarm.NewManager(swarm.Options{Git: git.NewRunner("", 4), Driver: b, WorktreeDir: t.TempDir(), PollInterval: testSwarmPollInterval})
	mgr.SetEnabled("lead", true)
	wireSwarm(ctx, mgr, b, true)
	result, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "lead", Text: "first task"})
	if err != nil || result.DelegatedThreadID == nil {
		t.Fatalf("direct message was not delegated: %+v, %v", result, err)
	}
	text := "second task"
	interrupt, err := b.QueueInterruptAndSend(ctx, wire.ThreadsQueueInterruptAndSendParams{ThreadID: "lead", Text: &text})
	if err != nil || interrupt.State != wire.ThreadsQueueInterruptStateSentToSwarm || len(mgr.State("lead").Bees) != 2 {
		t.Fatalf("sent_to_swarm is unreachable: %+v, %v", interrupt, err)
	}
}

// TestSwarmStreamDispatcherDispatchesInOrderAsEntriesClose 验证流式条目在各自闭合大括号到达时按顺序派发，
// 且同一轮委派只累计一次轮次。
func TestSwarmStreamDispatcherDispatchesInOrderAsEntriesClose(t *testing.T) {
	testenv.TempHome(t)

	var (
		mu      sync.Mutex
		prompts []string
		notices []string
		rounds  int
	)
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			mu.Lock()
			prompts = append(prompts, req.Entry.Prompt)
			mu.Unlock()
			return swarm.Bee{ThreadID: "bee-" + req.Entry.Name}, []string{"已派出 " + req.Entry.Name}, nil
		},
		func(string) int { mu.Lock(); defer mu.Unlock(); return rounds },
		func(string) int { mu.Lock(); defer mu.Unlock(); rounds++; return rounds },
		func(string) (string, bool) { return t.TempDir(), true },
		func(threadID, text string) error {
			mu.Lock()
			notices = append(notices, text)
			mu.Unlock()
			return nil
		},
		nil,
	)

	d.reset("lead")
	// 第一个对象闭合后应立即派发；第二个对象此时尚未闭合。
	d.feed("lead", "project", "```swarm\n[{\"prompt\":\"alpha\",\"bee\":\"Ives\"}")
	waitSwarm(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) == 1
	})
	d.feed("lead", "project", ",\n{\"prompt\":\"beta\",\"bee\":\"Aspen\"}")
	waitSwarm(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) == 2 && len(notices) == 2
	})
	d.feed("lead", "project", "]\n```")

	streamed, suppressed := d.finishTurn("lead")
	if streamed != 2 || suppressed {
		t.Fatalf("finishTurn = %d, %v，期望 2, false", streamed, suppressed)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || prompts[0] != "alpha" || prompts[1] != "beta" {
		t.Fatalf("流式条目未按闭合顺序派发: %q", prompts)
	}
	if rounds != 1 {
		t.Fatalf("同一轮只应累计一次委派轮次，实际 %d", rounds)
	}
	if len(notices) != 2 || notices[0] != "[编排] 已派出 Ives" || notices[1] != "[编排] 已派出 Aspen" {
		t.Fatalf("派发通知不正确: %q", notices)
	}
}

// TestSwarmStreamDispatcherSuppressesAfterRoundLimit 验证达到 3 轮委派上限后本轮条目被抑制且不再派发。
func TestSwarmStreamDispatcherSuppressesAfterRoundLimit(t *testing.T) {
	testenv.TempHome(t)

	var (
		mu         sync.Mutex
		rounds     = swarm.MaxDelegationRounds
		notices    []string
		dispatched int
	)
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			mu.Lock()
			dispatched++
			mu.Unlock()
			return swarm.Bee{}, nil, nil
		},
		func(string) int { mu.Lock(); defer mu.Unlock(); return rounds },
		func(string) int { mu.Lock(); defer mu.Unlock(); rounds++; return rounds },
		func(string) (string, bool) { return "", true },
		func(threadID, text string) error {
			mu.Lock()
			notices = append(notices, text)
			mu.Unlock()
			return nil
		},
		nil,
	)
	// 超限原因现在走 dispatcher.report 回送主代理；这里收进同一列表，仍断言只有一条。
	d.report = func(threadID, text string) error {
		mu.Lock()
		notices = append(notices, text)
		mu.Unlock()
		return nil
	}

	full := "```swarm\n[{\"prompt\":\"alpha\"}]\n```"
	d.reset("lead")
	d.feed("lead", "project", full)
	streamed, suppressed := d.settleTurn("lead", "project", full)
	if !suppressed || streamed != 0 {
		t.Fatalf("超限轮次应被抑制: streamed=%d, suppressed=%v", streamed, suppressed)
	}

	mu.Lock()
	defer mu.Unlock()
	if dispatched != 0 {
		t.Fatalf("超限轮次仍派发了 %d 个条目", dispatched)
	}
	// 超限提示改为描述「本轮块里的条目没有派出」，并带上实际上限。
	if len(notices) != 1 {
		t.Fatalf("超限回送应只有一条，实际 %d 条: %q", len(notices), notices)
	}
	notice := notices[0]
	if !strings.HasPrefix(notice, swarm.NoticePrefix) ||
		!strings.Contains(notice, "本轮委派块里的 1 条没有派出") ||
		!strings.Contains(notice, "3 轮派发上限") {
		t.Fatalf("轮次上限提示文案不正确: %q", notice)
	}
	if rounds != swarm.MaxDelegationRounds {
		t.Fatalf("超限轮次不应再累计轮次，实际 %d", rounds)
	}
}

// roundLimitHarness 记录派发次数、已计轮次与回送文案，供轮次上限相关测试共用。
type roundLimitHarness struct {
	mu         sync.Mutex
	limit      int
	rounds     int
	dispatched int
	notices    []string
}

// newRoundLimitHarness 构造一个按固定上限拦截的派发器；limit 为 0 表示不限轮次。
func newRoundLimitHarness(t *testing.T, limit int) (*roundLimitHarness, *swarmStreamDispatcher) {
	t.Helper()
	testenv.TempHome(t)
	h := &roundLimitHarness{limit: limit}
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			h.mu.Lock()
			h.dispatched++
			h.mu.Unlock()
			return swarm.Bee{}, nil, nil
		},
		func(string) int { h.mu.Lock(); defer h.mu.Unlock(); return h.rounds },
		func(string) int { h.mu.Lock(); defer h.mu.Unlock(); h.rounds++; return h.rounds },
		func(string) (string, bool) { return "", true },
		func(threadID, text string) error {
			h.mu.Lock()
			h.notices = append(h.notices, text)
			h.mu.Unlock()
			return nil
		},
		func() int { h.mu.Lock(); defer h.mu.Unlock(); return h.limit },
	)
	// 超限原因走 dispatcher.report 回送主代理，测试里收进同一列表。
	d.report = func(threadID, text string) error {
		h.mu.Lock()
		h.notices = append(h.notices, text)
		h.mu.Unlock()
		return nil
	}
	return h, d
}

// TestSwarmStreamDispatcherZeroLimitMeansUnlimited 验证轮次上限配成 0 时后续各轮仍能派发。
func TestSwarmStreamDispatcherZeroLimitMeansUnlimited(t *testing.T) {
	h, d := newRoundLimitHarness(t, 0)
	for i := 1; i <= 5; i++ {
		full := fmt.Sprintf("```swarm\n[{\"prompt\":\"task-%d\"}]\n```", i)
		d.reset("lead")
		d.feed("lead", "project", full)
		if _, suppressed := d.settleTurn("lead", "project", full); suppressed {
			t.Fatalf("上限为 0 时第 %d 轮不应被抑制", i)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dispatched != 5 || h.rounds != 5 {
		t.Fatalf("上限为 0 应派发 5 轮，实际派发 %d 次、累计 %d 轮", h.dispatched, h.rounds)
	}
	if len(h.notices) != 0 {
		t.Fatalf("无上限时不应回送超限提示: %q", h.notices)
	}
}

// TestSwarmStreamDispatcherBlocksAtConfiguredLimit 验证上限为 2 时第 3 轮被拦截且不再累计轮次。
func TestSwarmStreamDispatcherBlocksAtConfiguredLimit(t *testing.T) {
	h, d := newRoundLimitHarness(t, 2)
	for i := 1; i <= 2; i++ {
		full := fmt.Sprintf("```swarm\n[{\"prompt\":\"task-%d\"}]\n```", i)
		d.reset("lead")
		d.feed("lead", "project", full)
		if _, suppressed := d.settleTurn("lead", "project", full); suppressed {
			t.Fatalf("上限为 2 时第 %d 轮不应被抑制", i)
		}
	}

	full := "```swarm\n[{\"prompt\":\"task-3\"}]\n```"
	d.reset("lead")
	d.feed("lead", "project", full)
	if _, suppressed := d.settleTurn("lead", "project", full); !suppressed {
		t.Fatal("上限为 2 时第 3 轮应被拦截")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.dispatched != 2 || h.rounds != 2 {
		t.Fatalf("超限轮次不应派发也不应累计: 派发 %d 次、累计 %d 轮", h.dispatched, h.rounds)
	}
	if len(h.notices) != 1 || !strings.Contains(h.notices[0], "2 轮派发上限") {
		t.Fatalf("超限回送文案不正确: %q", h.notices)
	}
}

// TestSwarmStreamDispatcherReportsRequestTimeout 验证超出总时长预算的条目被拒绝时，主代理收到带预算说明的回送。
func TestSwarmStreamDispatcherReportsRequestTimeout(t *testing.T) {
	testenv.TempHome(t)

	var (
		mu      sync.Mutex
		reports []string
	)
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			return swarm.Bee{}, nil, fmt.Errorf("%w（3 小时）", swarm.ErrRequestTimeout)
		},
		func(string) int { return 0 },
		func(string) int { return 1 },
		func(string) (string, bool) { return "", true },
		func(string, string) error { return nil },
		nil,
	)
	d.report = func(threadID, text string) error {
		mu.Lock()
		reports = append(reports, text)
		mu.Unlock()
		return nil
	}

	d.dispatchJob(swarmStreamJob{leadThreadID: "lead", projectID: "project", entry: swarm.Delegation{Prompt: "alpha"}})

	mu.Lock()
	defer mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("超时应回送一条提示，实际 %d 条: %q", len(reports), reports)
	}
	if !strings.Contains(reports[0], "总时长预算") || !strings.Contains(reports[0], "3 小时") ||
		!strings.Contains(reports[0], "请用户发送新消息") {
		t.Fatalf("超时提示文案不正确: %q", reports[0])
	}
}

// TestSwarmStreamDispatcherSettleEnqueuesRemainingEntries 验证轮次结束时只补派发未流式派出的尾部条目。
func TestSwarmStreamDispatcherSettleEnqueuesRemainingEntries(t *testing.T) {
	testenv.TempHome(t)

	var (
		mu      sync.Mutex
		prompts []string
	)
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			mu.Lock()
			prompts = append(prompts, req.Entry.Prompt)
			mu.Unlock()
			return swarm.Bee{}, nil, nil
		},
		func(string) int { return 0 },
		func(string) int { return 1 },
		func(string) (string, bool) { return "", true },
		func(string, string) error { return nil },
		nil,
	)

	full := "```swarm\n[{\"prompt\":\"alpha\"},{\"prompt\":\"beta\"}]\n```"
	d.reset("lead")
	// 只喂到第一个对象闭合；第二个对象在轮次结束时才由完整回复补齐。
	d.feed("lead", "project", "```swarm\n[{\"prompt\":\"alpha\"},")
	waitSwarm(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) == 1
	})

	streamed, suppressed := d.settleTurn("lead", "project", full)
	if streamed != 1 || suppressed {
		t.Fatalf("settleTurn = %d, %v，期望 1, false", streamed, suppressed)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || prompts[0] != "alpha" || prompts[1] != "beta" {
		t.Fatalf("尾部条目未按序补齐: %v", prompts)
	}
}

// TestSwarmStreamDispatcherCapsEntriesPerBlock 验证单个 swarm 块的条目数超过并发上限时只派发前 N 个，
// 未派发的条目不入队，并在回合末回送一次 [编排] 提示让 lead 等这批 bee 汇报后另发一块。
func TestSwarmStreamDispatcherCapsEntriesPerBlock(t *testing.T) {
	testenv.TempHome(t)

	var (
		mu      sync.Mutex
		prompts []string
		notices []string
	)
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			mu.Lock()
			prompts = append(prompts, req.Entry.Prompt)
			mu.Unlock()
			return swarm.Bee{}, nil, nil
		},
		func(string) int { return 0 },
		func(string) int { return 1 },
		func(string) (string, bool) { return "", true },
		func(string, string) error { return nil },
		nil,
	)
	d.maxEntries = func() int { return 2 }
	d.report = func(threadID, text string) error {
		mu.Lock()
		notices = append(notices, text)
		mu.Unlock()
		return nil
	}

	full := "```swarm\n[{\"prompt\":\"alpha\"},{\"prompt\":\"beta\"},{\"prompt\":\"gamma\"},{\"prompt\":\"delta\"}]\n```"
	d.reset("lead")
	d.feed("lead", "project", full)
	waitSwarm(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) == 2
	})

	streamed, suppressed := d.settleTurn("lead", "project", full)
	if streamed != 2 || suppressed {
		t.Fatalf("settleTurn = %d, %v，期望 2, false", streamed, suppressed)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 2 || prompts[0] != "alpha" || prompts[1] != "beta" {
		t.Fatalf("超过并发上限的条目不应派发: %q", prompts)
	}
	if len(notices) != 1 {
		t.Fatalf("超限回送应只有一条，实际 %d 条: %q", len(notices), notices)
	}
	if !strings.HasPrefix(notices[0], swarm.NoticePrefix) ||
		!strings.Contains(notices[0], "已启动前 2 个") ||
		!strings.Contains(notices[0], "其余 2 个") ||
		!strings.Contains(notices[0], "并发上限 2") {
		t.Fatalf("超限提示文案不正确: %q", notices[0])
	}
}

// TestSwarmStreamDispatcherWithoutEntryCapDispatchesWholeBlock 验证未注入并发上限时整块条目全部派发。
func TestSwarmStreamDispatcherWithoutEntryCapDispatchesWholeBlock(t *testing.T) {
	testenv.TempHome(t)

	var (
		mu      sync.Mutex
		prompts []string
		notices []string
	)
	d := newSwarmStreamDispatcher(context.Background(),
		func(ctx context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
			mu.Lock()
			prompts = append(prompts, req.Entry.Prompt)
			mu.Unlock()
			return swarm.Bee{}, nil, nil
		},
		func(string) int { return 0 },
		func(string) int { return 1 },
		func(string) (string, bool) { return "", true },
		func(string, string) error { return nil },
		nil,
	)
	d.report = func(threadID, text string) error {
		mu.Lock()
		notices = append(notices, text)
		mu.Unlock()
		return nil
	}

	full := "```swarm\n[{\"prompt\":\"alpha\"},{\"prompt\":\"beta\"},{\"prompt\":\"gamma\"},{\"prompt\":\"delta\"}]\n```"
	d.reset("lead")
	d.feed("lead", "project", full)
	waitSwarm(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(prompts) == 4
	})

	streamed, suppressed := d.settleTurn("lead", "project", full)
	if streamed != 4 || suppressed {
		t.Fatalf("settleTurn = %d, %v，期望 4, false", streamed, suppressed)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 4 {
		t.Fatalf("未设上限时应派发全部条目: %q", prompts)
	}
	if len(notices) != 0 {
		t.Fatalf("未超限时不应回送提示: %q", notices)
	}
}

// staticLeadPersister 在测试中提供固定的 lead 记录，用于把子线程注册进 Manager。
type staticLeadPersister struct {
	records []swarm.LeadRecord
}

func (p staticLeadPersister) SaveLead(context.Context, swarm.LeadRecord) error { return nil }

func (p staticLeadPersister) LoadLeads(context.Context) ([]swarm.LeadRecord, error) {
	return p.records, nil
}

// questionMockHarness 在 HEAD_ASK 提示下发起一个提问并挂起等待答复，其余提示直接结束。
type questionMockHarness struct {
	answers chan map[string][]string
}

func (h *questionMockHarness) Kind() harness.Kind { return "question-mock" }

func (h *questionMockHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if !strings.Contains(turn.Prompt, "HEAD_ASK") {
		return nil
	}
	interactor, ok := turn.Approver.(harness.Interactor)
	if !ok {
		return errors.New("approver does not implement Interactor")
	}
	resp, err := interactor.Interact(ctx, harness.ApprovalRequest{
		Kind: harness.RequestKindQuestion,
		Questions: []harness.Question{{
			ID:      "q1",
			Header:  "范围",
			Prompt:  "要改哪个模块？",
			Choices: []harness.QuestionChoice{{Label: "后端"}, {Label: "前端"}},
		}},
	})
	if err != nil {
		return err
	}
	select {
	case h.answers <- resp.Answers:
	default:
	}
	return nil
}

// TestSwarmBeeAnswersRespondToBeeRequest 验证主线程回复里的答案块会被提交给子线程的待处理提问。
func TestSwarmBeeAnswersRespondToBeeRequest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := remote.NewStreamHub(remote.StreamHubOptions{})
	defer hub.Close()
	b := remote.NewMemoryBackend(hub)
	defer b.Close()
	asker := &questionMockHarness{answers: make(chan map[string][]string, 1)}
	b.SetHarness(asker, dir, harness.ModeFullAccess)
	b.AddProject(wire.ProjectSummary{ID: "p", Name: "P", Path: dir})
	b.AddThread(wire.ThreadSummary{ID: "lead", ProjectID: "p", Provider: "question-mock"}, nil)
	b.AddThread(wire.ThreadSummary{ID: "head", ProjectID: "p", Provider: "question-mock"}, nil)

	mgr := swarm.NewManager(swarm.Options{Persister: staticLeadPersister{records: []swarm.LeadRecord{{
		LeadThreadID: "lead",
		Enabled:      true,
		Bees:         []swarm.Bee{{ThreadID: "head", Name: "Ives", Status: swarm.StatusRunning}},
	}}}, PollInterval: testSwarmPollInterval})
	if err := mgr.Restore(ctx); err != nil {
		t.Fatalf("restore failed: %v", err)
	}

	if _, err := b.SendMessage(ctx, wire.SendMessageParams{ThreadID: "head", Text: "HEAD_ASK"}); err != nil {
		t.Fatalf("send to bee failed: %v", err)
	}

	var requestID string
	waitSwarm(t, func() bool {
		res, err := b.ListRequests(ctx, "head")
		if err != nil || len(res.Requests) == 0 {
			return false
		}
		requestID = res.Requests[0].RequestID
		return true
	})

	block := "```swarm-answer\n" +
		`{"bee":"head","request":"` + requestID + `","answers":{"q1":["后端"]}}` + "\n```\n"
	applyBeeAnswers(ctx, b, mgr, "lead", block)

	select {
	case got := <-asker.answers:
		if vals := got["q1"]; len(vals) != 1 || vals[0] != "后端" {
			t.Fatalf("bee received wrong answers: %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bee did not receive the forwarded answer")
	}

	waitSwarm(t, func() bool {
		page, err := b.ThreadHistory(ctx, wire.HistoryParams{ThreadID: "lead"})
		if err != nil {
			return false
		}
		for _, m := range page.Messages {
			if strings.Contains(m.Text, "已代为回答") {
				return true
			}
		}
		return false
	})
}

// swarmDedupHarness 记录派发顺序与子代理快照，供跨回合去重相关测试共用：
// 派发时按名册顺序给 bee 起名并记入快照，测试可以改写状态来模拟失败或停止。
type swarmDedupHarness struct {
	mu      sync.Mutex
	bees    []swarm.Bee
	prompts []string
	notices []string
	stopped []string
	stopErr error
}

// dispatch 模拟一次派发：按名册顺序分配名字，返回运行中的 bee。
func (h *swarmDedupHarness) dispatch(_ context.Context, req swarm.SingleEntryRequest) (swarm.Bee, []string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	bee := swarm.Bee{
		ThreadID: "bee-" + req.Entry.Prompt,
		Name:     swarm.RosterName(len(h.bees)),
		Task:     req.Entry.Task,
		Status:   swarm.StatusRunning,
	}
	h.bees = append(h.bees, bee)
	h.prompts = append(h.prompts, req.Entry.Prompt)
	return bee, nil, nil
}

// snapshot 返回 bee 快照，供派发器查同一条目的 bee 是否还在跑。
func (h *swarmDedupHarness) snapshot() []swarm.Bee {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]swarm.Bee(nil), h.bees...)
}

// note 收下回送主代理的文案；report 与 notify 合并到一处，断言只看最终文案。
func (h *swarmDedupHarness) note(_ string, text string) error {
	h.mu.Lock()
	h.notices = append(h.notices, text)
	h.mu.Unlock()
	return nil
}

// setStatus 改写某个 prompt 对应 bee 的状态，模拟它已失败或已停止。
func (h *swarmDedupHarness) setStatus(prompt string, status swarm.BeeStatus) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.bees {
		if h.bees[i].ThreadID == "bee-"+prompt {
			h.bees[i].Status = status
		}
	}
}

// dispatched 返回累计派发过的 prompt。
func (h *swarmDedupHarness) dispatched() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.prompts...)
}

// addBee 直接往快照里塞一条 bee，模拟去重集合里没有登记、只剩 manager 快照的情形。
func (h *swarmDedupHarness) addBee(bee swarm.Bee) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bees = append(h.bees, bee)
}

// stoppedBees 返回 replaces 取代时被停掉的 bee 线程 ID。
func (h *swarmDedupHarness) stoppedBees() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.stopped...)
}

// noticeTexts 返回累计回送的文案并清空，便于逐回合断言。
func (h *swarmDedupHarness) noticeTexts() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]string(nil), h.notices...)
	h.notices = nil
	return out
}

func newSwarmDedupHarness(t *testing.T) (*swarmDedupHarness, *swarmStreamDispatcher) {
	t.Helper()
	testenv.TempHome(t)
	h := &swarmDedupHarness{}
	d := newSwarmStreamDispatcher(context.Background(),
		h.dispatch,
		func(string) int { return 0 },
		func(string) int { return 1 },
		func(string) (string, bool) { return "", true },
		h.note,
		nil,
	)
	d.report = h.note
	d.bees = func(string) []swarm.Bee { return h.snapshot() }
	d.stopBee = func(_ context.Context, _ string, beeThreadID string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.stopped = append(h.stopped, beeThreadID)
		return h.stopErr
	}
	return h, d
}

// TestSwarmStreamDispatcherNamesStartedEntriesOnParseFailure 验证委派块整块解析失败时，
// 回送的文案点名本回合已经流式派出的条目（task 标题 + bee 名字），并明确要求不要重发。
func TestSwarmStreamDispatcherNamesStartedEntriesOnParseFailure(t *testing.T) {
	h, d := newSwarmDedupHarness(t)

	// 第三条少了字段间的逗号：整块 JSON 解析失败，而它自己也不会被流式解析器派出，
	// 于是形成「前两条已启动、第三条非法」的局面。
	broken := "```swarm\n" +
		`[{"prompt":"alpha","task":"任务甲"},{"prompt":"beta","task":"任务乙"},{"prompt":"gamma" "task":"任务丙"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", broken)
	d.settleTurn("lead", "project", broken)

	if got := h.dispatched(); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Fatalf("只有前两条应被派出，实际 %v", got)
	}
	notices := h.noticeTexts()
	if len(notices) != 1 {
		t.Fatalf("解析失败应回送一条提示，实际 %d 条: %q", len(notices), notices)
	}
	for _, want := range []string{"委派块没能解析", "任务甲", "任务乙", "Aspen", "Birch", "不要重发"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("解析失败提示缺少 %q: %q", want, notices[0])
		}
	}
}

// TestSwarmStreamDispatcherSkipsEntriesDispatchedEarlierInRequest 验证跨助手回合去重：
// 主代理重发整块时，本次请求里已经派出且仍在运行的条目不重复派发，回合末回送跳过说明。
func TestSwarmStreamDispatcherSkipsEntriesDispatchedEarlierInRequest(t *testing.T) {
	h, d := newSwarmDedupHarness(t)

	broken := "```swarm\n" +
		`[{"prompt":"alpha","task":"任务甲"},{"prompt":"beta","task":"任务乙"},{"prompt":"gamma" "task":"任务丙"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", broken)
	d.settleTurn("lead", "project", broken)
	h.noticeTexts() // 丢掉上一回合的解析失败回送

	fixed := "```swarm\n" +
		`[{"prompt":"alpha","task":"任务甲"},{"prompt":"beta","task":"任务乙"},{"prompt":"gamma","task":"任务丙"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", fixed)
	d.settleTurn("lead", "project", fixed)

	if got := h.dispatched(); len(got) != 3 || got[2] != "gamma" {
		t.Fatalf("第二回合只应派出 gamma，实际 %v", got)
	}
	notices := h.noticeTexts()
	if len(notices) != 1 {
		t.Fatalf("跳过说明应只有一条，实际 %d 条: %q", len(notices), notices)
	}
	for _, want := range []string{"跳过 2 条", "任务甲", "任务乙", "Aspen", "Birch", "不要重发"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("跳过说明缺少 %q: %q", want, notices[0])
		}
	}
}

// TestSwarmStreamDispatcherRedispatchesAfterEarlierBeeFailed 验证跨回合去重的例外：
// 上一条对应的 bee 已经失败时，重发同一个 prompt 要重新派发。
func TestSwarmStreamDispatcherRedispatchesAfterEarlierBeeFailed(t *testing.T) {
	h, d := newSwarmDedupHarness(t)

	block := "```swarm\n" + `[{"prompt":"alpha","task":"任务甲"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)
	h.setStatus("alpha", swarm.StatusFailed)

	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)

	if got := h.dispatched(); len(got) != 2 || got[0] != "alpha" || got[1] != "alpha" {
		t.Fatalf("上一个 bee 已失败时应允许重派，实际 %v", got)
	}
	if notices := h.noticeTexts(); len(notices) != 0 {
		t.Fatalf("重派不应有跳过说明，实际 %q", notices)
	}
}

// TestSwarmStreamDispatcherKeepsRunningEntriesAcrossRequests 验证新的用户请求不再清空去重记录：
// bee 还在跑时同一 prompt 仍被跳过，只有它失败后才会重派。
func TestSwarmStreamDispatcherKeepsRunningEntriesAcrossRequests(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	block := "```swarm\n" + `[{"prompt":"alpha","task":"任务甲"}]` + "\n```"

	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)
	h.noticeTexts()

	// bee 还在跑：新的用户请求不应让同一条目重新排队。
	d.beginRequest("lead")
	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)
	if got := h.dispatched(); len(got) != 1 {
		t.Fatalf("bee 仍在运行时同一 prompt 不应重派，实际 %v", got)
	}
	if notices := h.noticeTexts(); len(notices) != 1 || !strings.Contains(notices[0], "任务甲") {
		t.Fatalf("应回送一条点名该条目的跳过说明，实际 %q", notices)
	}

	// bee 失败后，再一个用户请求就可以重新派发同一条目。
	h.setStatus("alpha", swarm.StatusFailed)
	d.beginRequest("lead")
	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)
	if got := h.dispatched(); len(got) != 2 || got[1] != "alpha" {
		t.Fatalf("bee 已失败时应允许重派，实际 %v", got)
	}
}

// TestSwarmStreamDispatcherRedispatchesCompletedEntryAfterNewRequest 验证已完成的去重记录
// 不再占着这个 prompt：用户在新请求里要求重做同一件事时应当重新派发。
func TestSwarmStreamDispatcherRedispatchesCompletedEntryAfterNewRequest(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	block := "```swarm\n" + `[{"prompt":"alpha","task":"任务甲"}]` + "\n```"

	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)
	h.noticeTexts()

	// bee 做完了：新请求要重做同一件事，就应当重新派发。
	h.setStatus("alpha", swarm.StatusCompleted)
	d.beginRequest("lead")
	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)

	if got := h.dispatched(); len(got) != 2 || got[1] != "alpha" {
		t.Fatalf("上一个 bee 已完成时应允许重派，实际 %v", got)
	}
	if notices := h.noticeTexts(); len(notices) != 0 {
		t.Fatalf("重派不应有跳过说明，实际 %q", notices)
	}
}

// TestSwarmStreamDispatcherRedispatchesSameTaskAfterBeeCompletedInRequest 验证同一请求内按任务标题
// 兜底时只认在跑的记录：上一个同名任务已完成，换措辞的新条目照常派发。
func TestSwarmStreamDispatcherRedispatchesSameTaskAfterBeeCompletedInRequest(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	first := "```swarm\n" + `[{"prompt":"alpha","task":"修复编译错误"}]` + "\n```"

	d.reset("lead")
	d.feed("lead", "project", first)
	d.settleTurn("lead", "project", first)
	h.noticeTexts()

	// 同一个请求的下一回合又出现同名任务，但上一个已经做完了，应当再派一次。
	h.setStatus("alpha", swarm.StatusCompleted)
	second := "```swarm\n" + `[{"prompt":"alpha again","task":" 修复编译错误 "}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", second)
	d.settleTurn("lead", "project", second)

	if got := h.dispatched(); len(got) != 2 || got[1] != "alpha again" {
		t.Fatalf("上一个同名任务已完成时应允许重派，实际 %v", got)
	}
	if notices := h.noticeTexts(); len(notices) != 0 {
		t.Fatalf("重派不应有跳过说明，实际 %q", notices)
	}
}

// TestSwarmStreamDispatcherSkipsIdenticalPromptAfterBeeCompletedInRequest 验证原有语义不变：
// 同一请求内完全相同的 brief 即使对应 bee 已完成，仍算重复。
func TestSwarmStreamDispatcherSkipsIdenticalPromptAfterBeeCompletedInRequest(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	block := "```swarm\n" + `[{"prompt":"alpha","task":"任务甲"}]` + "\n```"

	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)
	h.noticeTexts()

	h.setStatus("alpha", swarm.StatusCompleted)
	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)

	if got := h.dispatched(); len(got) != 1 {
		t.Fatalf("同一请求内完全相同的 brief 不应重派，实际 %v", got)
	}
	notices := h.noticeTexts()
	if len(notices) != 1 || !strings.Contains(notices[0], "任务甲") {
		t.Fatalf("应回送一条点名该条目的跳过说明，实际 %q", notices)
	}
}

// TestSwarmStreamDispatcherSkipsByTaskTitleAcrossRequests 验证主代理换个措辞重写同一条目时，
// 按任务标题（忽略大小写与首尾空白）仍能识别出它在跑，不再派一次。
func TestSwarmStreamDispatcherSkipsByTaskTitleAcrossRequests(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	first := "```swarm\n" + `[{"prompt":"alpha","task":"Migrate DB"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", first)
	d.settleTurn("lead", "project", first)
	h.noticeTexts()

	d.beginRequest("lead")
	second := "```swarm\n" + `[{"prompt":"alpha rewritten","task":" migrate db "}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", second)
	d.settleTurn("lead", "project", second)

	if got := h.dispatched(); len(got) != 1 || got[0] != "alpha" {
		t.Fatalf("任务标题相同时不应重派，实际 %v", got)
	}
	notices := h.noticeTexts()
	if len(notices) != 1 {
		t.Fatalf("应回送一条跳过说明，实际 %d 条: %q", len(notices), notices)
	}
	for _, want := range []string{"跳过 1 条", "Migrate DB", "Aspen"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("跳过说明缺少 %q: %q", want, notices[0])
		}
	}
}

// TestSwarmStreamDispatcherSkipsRunningBeeWithSameTask 验证去重集合里没有登记时，
// 仍会按 manager 快照里同任务的在跑 bee 判定重复（例如重启后只剩快照）。
func TestSwarmStreamDispatcherSkipsRunningBeeWithSameTask(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	h.addBee(swarm.Bee{ThreadID: "bee-old", Name: "Aspen", Task: "Alpha Job", Status: swarm.StatusRunning})

	block := "```swarm\n" + `[{"prompt":"alpha","task":"alpha job"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)

	if got := h.dispatched(); len(got) != 0 {
		t.Fatalf("快照里已有同任务的在跑 bee 时不应派发，实际 %v", got)
	}
	notices := h.noticeTexts()
	if len(notices) != 1 {
		t.Fatalf("应回送一条跳过说明，实际 %d 条: %q", len(notices), notices)
	}
	for _, want := range []string{"跳过 1 条", "Alpha Job", "Aspen"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("跳过说明缺少 %q: %q", want, notices[0])
		}
	}
}

// TestSwarmStreamDispatcherStopsReplacedBee 验证带 replaces 的条目不被去重拦住：
// 先停掉被点名的在跑 bee，再照常派发新条目。
func TestSwarmStreamDispatcherStopsReplacedBee(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	first := "```swarm\n" + `[{"prompt":"alpha","task":"任务甲"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", first)
	d.settleTurn("lead", "project", first)
	h.noticeTexts()

	// 同一个任务换措辞重写：不带 replaces 会被跳过，带了就应当取代。
	d.beginRequest("lead")
	replacing := "```swarm\n" + `[{"prompt":"alpha v2","task":"任务甲","replaces":"aspen"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", replacing)
	d.settleTurn("lead", "project", replacing)

	if got := h.dispatched(); len(got) != 2 || got[1] != "alpha v2" {
		t.Fatalf("带 replaces 的条目应照常派发，实际 %v", got)
	}
	if stopped := h.stoppedBees(); len(stopped) != 1 || stopped[0] != "bee-alpha" {
		t.Fatalf("应先停掉被取代的 bee-alpha，实际 %v", stopped)
	}
	if notices := h.noticeTexts(); len(notices) != 0 {
		t.Fatalf("成功取代不应有提示，实际 %q", notices)
	}
}

// TestSwarmStreamDispatcherReplacesUnknownBee 验证 replaces 点名的 bee 不存在时，
// 只回送一条提示，本条照常派发。
func TestSwarmStreamDispatcherReplacesUnknownBee(t *testing.T) {
	h, d := newSwarmDedupHarness(t)
	block := "```swarm\n" + `[{"prompt":"beta","task":"任务乙","replaces":"Ghoul"}]` + "\n```"
	d.reset("lead")
	d.feed("lead", "project", block)
	d.settleTurn("lead", "project", block)

	if got := h.dispatched(); len(got) != 1 || got[0] != "beta" {
		t.Fatalf("replaces 找不到目标时也应派发本条，实际 %v", got)
	}
	notices := h.noticeTexts()
	if len(notices) != 1 {
		t.Fatalf("应回送一条提示，实际 %d 条: %q", len(notices), notices)
	}
	for _, want := range []string{"没有找到名为 Ghoul", "本条已按新条目派出"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("提示缺少 %q: %q", want, notices[0])
		}
	}
}

// swarmTiersFromConfig 把档位的写入白名单透传到编排运行时，并复制切片避免共享底层数组。
func TestSwarmTiersFromConfigWritePaths(t *testing.T) {
	src := []config.SwarmTier{
		{ID: "deep", Name: "深度", WritePaths: []string{"docs/superpowers/plans/**/*.md"}},
		{ID: "edit", Name: "执行"},
	}

	got := swarmTiersFromConfig(src)
	if len(got) != 2 {
		t.Fatalf("Tiers length = %d, want 2", len(got))
	}
	if !reflect.DeepEqual(got[0].WritePaths, []string{"docs/superpowers/plans/**/*.md"}) {
		t.Fatalf("deep 档的 WritePaths = %v，想要默认白名单", got[0].WritePaths)
	}
	if len(got[1].WritePaths) != 0 {
		t.Fatalf("edit 档不该有写入白名单，得到 %v", got[1].WritePaths)
	}

	src[0].WritePaths[0] = "mutated"
	if !reflect.DeepEqual(got[0].WritePaths, []string{"docs/superpowers/plans/**/*.md"}) {
		t.Fatalf("WritePaths 应复制切片，得到 %v", got[0].WritePaths)
	}
}

// 审查设置：检查命令与行数上限原样映射，0 表示关闭行数规则。
func TestSwarmSettingsFromConfigMapsReviewFunnel(t *testing.T) {
	cfg := config.OrchestrationConfig{
		ReviewCheckCommand:    "go test ./...",
		ReviewMaxChangedLines: 80,
	}
	got := SwarmSettingsFromConfig(cfg)
	if got.ReviewCheckCommand != "go test ./..." || got.ReviewMaxChangedLines != 80 {
		t.Fatalf("审查设置未映射: %+v", got)
	}
	zero := SwarmSettingsFromConfig(config.OrchestrationConfig{})
	if zero.ReviewCheckCommand != "" || zero.ReviewMaxChangedLines != 0 {
		t.Fatalf("零值应原样保留: %+v", zero)
	}
}
