package mcodeui

import (
	"testing"
	"time"
)

// newPendingTestState 构造带一个项目与一个已有会话的 chatState，便于断言侧栏与选中态。
func newPendingTestState(t *testing.T) (*FakeClient, *chatState) {
	t.Helper()
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Proj1"}},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "已有会话", UpdatedAt: now}},
		},
	)
	fake.SetProviders([]ProviderInfo{{ID: "claude", Name: "Claude", Available: true}})

	state := newChatState(fake)
	state.mu.Lock()
	state.projects = []Project{{ID: "p1", Name: "Proj1"}}
	state.threadsByProj = map[string][]Thread{
		"p1": {{ID: "t1", ProjectID: "p1", Title: "已有会话", UpdatedAt: now}},
	}
	state.threads = []Thread{{ID: "t1", ProjectID: "p1", Title: "已有会话", UpdatedAt: now}}
	state.selectedThreadID = "t1"
	state.newThreadProvider = "claude"
	state.mu.Unlock()
	return fake, state
}

// 点击新建只进入待创建状态：不建后端会话、侧栏会话数不变、不选中任何已有会话。
func TestPendingThread_ClickDoesNotCreate(t *testing.T) {
	fake, state := newPendingTestState(t)

	state.createThread("p1")

	if got := fake.CreateThreadCalls(); got != 0 {
		t.Fatalf("点击新建会话不应创建后端会话，实际调用 %d 次", got)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingThread == nil || state.pendingThread.ProjectID != "p1" {
		t.Fatalf("应进入待创建会话，got %+v", state.pendingThread)
	}
	if state.selectedThreadID != "" {
		t.Fatalf("待创建会话不应选中任何会话行，selectedThreadID=%q", state.selectedThreadID)
	}
	if got := len(state.threadsByProj["p1"]); got != 1 {
		t.Fatalf("侧栏会话数不应变化，got %d", got)
	}
}

// 输入首个非空文本时恰好创建一次，选项取自待创建状态，创建后选中新会话。
func TestPendingThread_CreatesOnFirstInputOnce(t *testing.T) {
	fake, state := newPendingTestState(t)

	state.createThread("p1")
	// 待创建期间改推理强度：写进待创建状态，创建时随会话一起下发。
	state.updateComposerPrefs("", func(p *ComposerPrefs) { p.Effort = "high" }, "设置推理强度失败")

	state.mu.Lock()
	state.draft = "你好"
	state.mu.Unlock()
	state.noteComposerDraft("", "你好")
	state.noteComposerDraft("", "你好")

	waitUntil(t, "首个输入后创建会话", func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID != "" && state.pendingThread == nil
	})

	if got := fake.CreateThreadCalls(); got != 1 {
		t.Fatalf("应恰好创建一次，got %d", got)
	}
	opts := fake.LastNewThreadOptions()
	if opts.Provider != "claude" || opts.Effort != "high" {
		t.Fatalf("创建选项应带上待创建状态的模型与推理强度，got %+v", opts)
	}
	if _, prefs, ok := fake.LastComposerPrefsCall(); !ok || prefs.Effort != "high" {
		t.Fatalf("创建后应补写推理强度，got %+v ok=%v", prefs, ok)
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.selectedThreadID == "" {
		t.Fatal("创建成功后应选中新会话")
	}
	if state.pendingDraftFill != nil {
		t.Fatalf("创建后不应再用草稿回填输入框，got %q", *state.pendingDraftFill)
	}
	if got := state.drafts[state.selectedThreadID].Text; got != "你好" {
		t.Fatalf("创建后已输入文本应挂在待创建会话下，got %q", got)
	}
	if got := len(state.threadsByProj["p1"]); got != 2 {
		t.Fatalf("创建后新会话应出现在侧栏，got %d 个会话", got)
	}
}

// 发送时创建会话，创建成功后接着把这条消息发出去，且不会重复创建。
func TestPendingThread_SendCreatesThenSends(t *testing.T) {
	fake, state := newPendingTestState(t)

	state.createThread("p1")
	state.sendMessage("第一条")
	state.sendMessage("第一条")

	waitUntil(t, "发送后创建会话并发出消息", func() bool {
		return fake.CreateThreadCalls() == 1 && fake.SendCalls() >= 1
	})
	if got := fake.CreateThreadCalls(); got != 1 {
		t.Fatalf("应恰好创建一次会话，got %d", got)
	}
	if got := fake.LastSentText(); got != "第一条" {
		t.Fatalf("应把首条消息发给新会话，got %q", got)
	}
}

// 待创建状态下切到其它会话直接丢弃待创建状态，不留下空会话。
func TestPendingThread_DiscardOnSwitch(t *testing.T) {
	fake, state := newPendingTestState(t)

	state.createThread("p1")
	state.selectThread("t1")

	if got := fake.CreateThreadCalls(); got != 0 {
		t.Fatalf("切走不应创建会话，got %d", got)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.pendingThread != nil {
		t.Fatalf("切到其它会话应丢弃待创建状态，got %+v", state.pendingThread)
	}
	if state.selectedThreadID != "t1" {
		t.Fatalf("选中会话应为 t1，got %q", state.selectedThreadID)
	}
}

// 待创建状态下更新子线程偏好与应用配对，应记住子线程模型与推理强度。
func TestPendingThread_RemembersSwarmBees(t *testing.T) {
	_, state := newPendingTestState(t)

	state.createThread("p1")
	// 待创建期间修改子线程提供方、模型与推理强度
	state.updateComposerPrefs("", func(p *ComposerPrefs) {
		p.BeesProvider = "pi"
		p.BeesModel = "magpie/jp/auto/deepseek-v4.1-flash"
		p.BeesEffort = "max"
	}, "设置子代理失败")

	swarm := loadLastSwarmSelection()
	if swarm.Bees == nil {
		t.Fatal("修改待创建会话的子线程偏好后，应记录到 lastSwarmSelection")
	}
	if swarm.Bees.Provider != "pi" || swarm.Bees.Model != "magpie/jp/auto/deepseek-v4.1-flash" || swarm.Bees.Effort != "max" {
		t.Fatalf("记录的子线程配置不符，got %+v", *swarm.Bees)
	}

	// 待创建期间应用配对
	pair := SwarmPair{
		LeadProvider:   "claude",
		LeadModel:      "opus",
		WorkerProvider: "pi",
		WorkerModel:    "magpie/jp/auto/glm-5.3-flash",
	}
	state.applyPendingSwarmPairLocked(pair, "high", "medium")
	rememberSwarmBees(pair.WorkerProvider, pair.WorkerModel, "medium")

	swarm = loadLastSwarmSelection()
	if swarm.Bees == nil || swarm.Bees.Provider != "pi" || swarm.Bees.Model != "magpie/jp/auto/glm-5.3-flash" || swarm.Bees.Effort != "medium" {
		t.Fatalf("应用配对后记录的子线程配置不符，got %+v", swarm.Bees)
	}
}

// 待创建会话选中的配对记在占位会话与创建选项上，创建时随参数下发。
func TestPendingThread_PairIDFlowsIntoCreateOptions(t *testing.T) {
	fake, state := newPendingTestState(t)

	state.createThread("p1")
	pair := SwarmPair{
		ID:             "pair-7",
		LeadProvider:   "claude",
		LeadModel:      "opus",
		WorkerProvider: "claude",
		WorkerModel:    "sonnet",
	}
	state.applyPendingSwarmPairLocked(pair, "high", "medium")

	state.mu.Lock()
	pendingThreadPairID, pendingOptionPairID := "", ""
	if p := state.pendingThread; p != nil {
		pendingThreadPairID = p.Thread.PairID
		pendingOptionPairID = p.Options.PairID
	}
	state.mu.Unlock()
	if pendingThreadPairID != "pair-7" || pendingOptionPairID != "pair-7" {
		t.Fatalf("待创建会话应记住配对 ID，thread=%q options=%q", pendingThreadPairID, pendingOptionPairID)
	}

	// 首个输入触发建会话，配对 ID 随 NewThreadOptions 下发。
	state.mu.Lock()
	state.draft = "你好"
	state.mu.Unlock()
	state.noteComposerDraft("", "你好")
	waitUntil(t, "首个输入后创建会话", func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID != "" && state.pendingThread == nil
	})

	if got := fake.LastNewThreadOptions().PairID; got != "pair-7" {
		t.Fatalf("创建选项应带上选中的配对 ID，got %q", got)
	}
}

// 新建会话时模型回退到目录里的其它模型，不得把回退结果写回“上次使用”的记忆。
func TestPendingThread_CreateKeepsLastModelSelection(t *testing.T) {
	// 整包运行时数据目录是全局共享的，先等上一轮后台写盘结束并把全局记忆状态与数据目录复位，
	// 避免别的用例遗留的 ui-state.json 或残留写盘 goroutine 把这里的“上次选择”冲成别的值。
	resetLastSelectionMergeState(t)
	fake, state := newPendingTestState(t)
	// 设置里的默认 Agent 记为 codex：建会话不应把它改成当前输入框的 Agent。
	st := NewSettingsState(settingsTestDir(t), nil, state)
	st.cfg.Models.Provider = "codex"
	setChatSettings(state, st)
	providerBefore := getChatSettings(state).cfg.Models.Provider
	// 目录里只剩 claude/sonnet，记忆里的 claude/opus-removed 已下架。
	state.mu.Lock()
	state.providers = []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "sonnet"}}}}
	state.newThreadProvider = "claude"
	state.mu.Unlock()

	remembered := ModelSelection{Provider: "claude", Model: "opus-removed", Effort: "high", Fast: true}
	setLastModelSelection(&remembered)
	defer setLastModelSelection(nil)

	state.createThread("p1")
	state.mu.Lock()
	state.draft = "你好"
	state.mu.Unlock()
	state.noteComposerDraft("", "你好")

	waitUntil(t, "首个输入后创建会话", func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID != "" && state.pendingThread == nil
	})

	if got := fake.CreateThreadCalls(); got != 1 {
		t.Fatalf("应恰好创建一次，got %d", got)
	}
	if opts := fake.LastNewThreadOptions(); opts.Model == "opus-removed" {
		t.Fatalf("已下架的记忆模型不应被沿用，got %+v", opts)
	}
	if got := getChatSettings(state).cfg.Models.Provider; got != providerBefore {
		t.Fatalf("建会话不应改写设置里的默认 Agent，want %q，got %q", providerBefore, got)
	}
	last, ok := loadLastModelSelection()
	if !ok || last != remembered {
		t.Fatalf("建会话不应改写“上次模型选择”，got %+v ok=%v", last, ok)
	}
}
