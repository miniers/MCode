package mcodeui

import (
	"testing"
	"time"
)

func resetSceneScopedState(t *testing.T) string {
	t.Helper()
	path := resetLastSelectionMergeState(t)
	clearProjectModelBuckets()
	t.Cleanup(clearProjectModelBuckets)
	return path
}

func sceneTestProviders() []ProviderInfo {
	return []ProviderInfo{
		{
			ID:   "claude",
			Name: "Claude",
			Models: []ModelInfo{
				{ID: "sonnet", Efforts: []string{"low", "medium", "high"}, SupportsFast: true},
				{ID: "opus", Efforts: []string{"medium", "high"}},
			},
		},
		{
			ID:   "codex",
			Name: "Codex",
			Models: []ModelInfo{
				{ID: "gpt-5.5", Efforts: []string{"low", "medium", "high"}},
				{ID: "o3", Efforts: []string{"medium", "high"}},
			},
		},
	}
}

// TestSceneScopedSwarmQuickDoesNotAffectProject 验证快速对话关掉编排后，
// 项目场景新会话的编排开关仍保持项目场景自己的记录（无记录时默认开，全局总开关关时仍为关）。
func TestSceneScopedSwarmQuickDoesNotAffectProject(t *testing.T) {
	resetSceneScopedState(t)
	providers := sceneTestProviders()

	// 未记录过项目编排开关时默认开；快速对话显式关闭后，项目场景仍默认开。
	rememberSwarmEnabled("", false)
	gotDefault := newThreadOptionsForProject("p1", "claude", providers, nil)
	if !gotDefault.SwarmEnabled {
		t.Fatalf("快速对话关闭编排后，未记录过的项目场景应仍默认开启，got %+v", gotDefault)
	}

	// 项目场景显式记录为开之后，再次在快速对话里关闭编排，项目场景仍为开。
	rememberSwarmEnabled("p1", true)
	rememberSwarmEnabled("", false)
	gotProj := newThreadOptionsForProject("p1", "claude", providers, nil)
	if !gotProj.SwarmEnabled {
		t.Fatalf("快速对话关闭编排不应影响已开启的项目场景，got %+v", gotProj)
	}

	// 全局编排总开关关闭时，即使项目场景默认开也必须保持为关。
	chat := newChatState(NewFakeClient())
	defer chat.close()
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	off := false
	st.cfg.Orchestration.Enabled = &off
	setChatSettings(chat, st)
	defer setChatSettings(chat, nil)
	if opts := chat.newThreadOptions("p1"); opts.SwarmEnabled {
		t.Fatalf("全局编排总开关关闭时项目场景新会话应保持关闭，got %+v", opts)
	}
}

// TestSceneScopedSwarmProjectDoesNotAffectQuick 验证项目场景开启编排后，
// 快速对话新会话仍默认关闭（未记录时），且两个场景的显式开关互相独立。
func TestSceneScopedSwarmProjectDoesNotAffectQuick(t *testing.T) {
	resetSceneScopedState(t)
	providers := sceneTestProviders()

	rememberSwarmEnabled("p1", true)
	gotQuick := newThreadOptionsForProject("", "claude", providers, nil)
	if gotQuick.SwarmEnabled {
		t.Fatalf("项目场景开启编排后，未记录过的快速对话应仍默认关闭，got %+v", gotQuick)
	}

	// 快速对话显式开启、项目场景显式关闭时也互相独立。
	rememberSwarmEnabled("", true)
	rememberSwarmEnabled("p1", false)
	if got := newThreadOptionsForProject("", "claude", providers, nil); !got.SwarmEnabled {
		t.Fatalf("快速对话显式开启后应保持开启，got %+v", got)
	}
	if got := newThreadOptionsForProject("p1", "claude", providers, nil); got.SwarmEnabled {
		t.Fatalf("项目场景显式关闭后应保持关闭，got %+v", got)
	}
}

// TestSceneScopedBeesModels 验证快速对话与项目场景分别记录不同的执行侧模型，
// 各自拿到自己的模型，且未记录或不可用时退回跟随主导。
func TestSceneScopedBeesModels(t *testing.T) {
	resetSceneScopedState(t)
	providers := sceneTestProviders()

	// 快速对话未记录 Bees 时保持跟随主导。
	if got := newThreadOptionsForProject("", "claude", providers, nil); got.BeesProvider != "" || got.BeesModel != "" {
		t.Fatalf("快速对话未记录执行侧时应跟随主导，got %+v", got)
	}

	rememberSwarmBees("", "claude", "sonnet", "low")
	rememberSwarmBees("p1", "codex", "o3", "high")

	gotQuick := newThreadOptionsForProject("", "claude", providers, nil)
	if gotQuick.BeesProvider != "claude" || gotQuick.BeesModel != "sonnet" || gotQuick.BeesEffort != "low" {
		t.Fatalf("快速对话应拿到自己的执行侧模型 claude/sonnet/low，got %+v", gotQuick)
	}
	gotProj := newThreadOptionsForProject("p1", "claude", providers, nil)
	if gotProj.BeesProvider != "codex" || gotProj.BeesModel != "o3" || gotProj.BeesEffort != "high" {
		t.Fatalf("项目场景应拿到自己的执行侧模型 codex/o3/high，got %+v", gotProj)
	}

	// 执行侧模型已不在目录或 provider 停用时，两个场景同样退回跟随主导。
	rememberSwarmBees("", "codex", "removed-model", "low")
	gotMissing := newThreadOptionsForProject("", "claude", providers, nil)
	if gotMissing.BeesProvider != "" || gotMissing.BeesModel != "" {
		t.Fatalf("快速对话执行侧模型不在目录时应跟随主导，got %+v", gotMissing)
	}
	gotDisabled := newThreadOptionsForProject("p1", "claude", providers, nil, map[string]bool{"claude": true, "codex": false})
	if gotDisabled.BeesProvider != "" || gotDisabled.BeesModel != "" {
		t.Fatalf("项目场景执行侧 provider 停用时应跟随主导，got %+v", gotDisabled)
	}
}

// TestSceneScopedLeadModel 验证快速对话记录主导模型只写 last_model_quick，
// 不污染无项目记录的项目新会话；快速对话无 last_model_quick 时退回 last_model。
func TestSceneScopedLeadModel(t *testing.T) {
	path := resetSceneScopedState(t)
	providers := sceneTestProviders()

	// 先给项目场景记录一个主导模型，此时快速对话无 last_model_quick，应退回 last_model。
	chat := newChatState(NewFakeClient())
	defer chat.close()
	chat.mu.Lock()
	chat.threads = []Thread{
		{ID: "t-proj", ProjectID: "p1"},
		{ID: "t-quick", ProjectID: ""},
	}
	chat.mu.Unlock()

	projSel := ModelSelection{Provider: "codex", Model: "gpt-5.5", Effort: "medium"}
	chat.rememberModelSelectionForThread("t-proj", projSel)
	gotQuickFallback := newThreadOptionsForProject("", "claude", providers, nil)
	if gotQuickFallback.Provider != "codex" || gotQuickFallback.Model != "gpt-5.5" || gotQuickFallback.Effort != "medium" {
		t.Fatalf("快速对话无 last_model_quick 时应退回 last_model，got %+v", gotQuickFallback)
	}

	// 在快速对话里切换主导模型：只写 last_model_quick，不写 last_model，不影响其它无项目记录的项目。
	quickSel := ModelSelection{Provider: "claude", Model: "sonnet", Effort: "high", Fast: true}
	chat.rememberModelSelectionForThread("t-quick", quickSel)
	waitUIStateWrites()

	gotQuick := newThreadOptionsForProject("", "codex", providers, nil)
	if gotQuick.Provider != "claude" || gotQuick.Model != "sonnet" || gotQuick.Effort != "high" || !gotQuick.Fast {
		t.Fatalf("快速对话应优先使用 last_model_quick，got %+v", gotQuick)
	}
	gotOtherProj := newThreadOptionsForProject("p2", "claude", providers, nil)
	if gotOtherProj.Provider != "codex" || gotOtherProj.Model != "gpt-5.5" || gotOtherProj.Effort != "medium" {
		t.Fatalf("无项目记录的 p2 应继续沿用 last_model，不受快速对话影响，got %+v", gotOtherProj)
	}

	file := mustReadUIStateFile(t, path)
	if file.LastModel != projSel {
		t.Fatalf("last_model 不应被快速对话覆盖，got %+v，want %+v", file.LastModel, projSel)
	}
	if file.LastModelQuick == nil || *file.LastModelQuick != quickSel {
		t.Fatalf("last_model_quick 应为 %+v，got %+v", quickSel, file.LastModelQuick)
	}
}

// TestSceneScopedPersistenceReload 验证写盘并清缓存重新加载后，两个场景的模型与编排记录都保留，
// 且外部进程修改一个场景时不会冲掉另一个场景的脏写入。
func TestSceneScopedPersistenceReload(t *testing.T) {
	path := resetSceneScopedState(t)
	providers := sceneTestProviders()

	saveLastModelSelection("p1", ModelSelection{Provider: "codex", Model: "gpt-5.5", Effort: "high"})
	saveLastModelSelection("", ModelSelection{Provider: "claude", Model: "opus", Effort: "medium"})
	rememberSwarmEnabled("p1", false)
	rememberSwarmBees("p1", "codex", "o3", "medium")
	rememberSwarmEnabled("", true)
	rememberSwarmBees("", "claude", "sonnet", "low")
	waitUIStateWrites()

	// 清空全部内存缓存，模拟重启后重新读盘。
	clearLastSelectionGlobals()
	clearProjectModelBuckets()

	gotProj := newThreadOptionsForProject("p1", "claude", providers, nil)
	if gotProj.Provider != "codex" || gotProj.Model != "gpt-5.5" || gotProj.Effort != "high" ||
		gotProj.SwarmEnabled || gotProj.BeesProvider != "codex" || gotProj.BeesModel != "o3" || gotProj.BeesEffort != "medium" {
		t.Fatalf("重读盘后项目场景恢复不正确，got %+v", gotProj)
	}
	gotQuick := newThreadOptionsForProject("", "codex", providers, nil)
	if gotQuick.Provider != "claude" || gotQuick.Model != "opus" || gotQuick.Effort != "medium" ||
		!gotQuick.SwarmEnabled || gotQuick.BeesProvider != "claude" || gotQuick.BeesModel != "sonnet" || gotQuick.BeesEffort != "low" {
		t.Fatalf("重读盘后快速对话恢复不正确，got %+v", gotQuick)
	}

	// 外部修改项目场景后，本进程只写快速对话开关，项目场景外部改动与快速对话原有的 Bees 均保留。
	setLastSelectionStatInterval(t, 0)
	off := false
	on := true
	writeRawUIStateFile(t, path, uiStateFile{
		LastModel:      ModelSelection{Provider: "codex", Model: "o3"},
		LastModelQuick: &ModelSelection{Provider: "claude", Model: "opus"},
		LastSwarm:      &LastSwarmSelection{Enabled: &off, Bees: &ModelSelection{Provider: "codex", Model: "gpt-5.5"}},
		LastSwarmQuick: &LastSwarmSelection{Enabled: &on, Bees: &ModelSelection{Provider: "claude", Model: "sonnet"}},
	}, time.Now().Add(time.Minute))

	rememberSwarmEnabled("", false)
	waitUIStateWrites()

	file := mustReadUIStateFile(t, path)
	if file.LastModel.Model != "o3" || file.LastSwarm == nil || file.LastSwarm.Bees == nil || file.LastSwarm.Bees.Model != "gpt-5.5" {
		t.Fatalf("外部修改的项目场景记录应被保留，got LastModel=%+v LastSwarm=%+v", file.LastModel, file.LastSwarm)
	}
	if file.LastSwarmQuick == nil || file.LastSwarmQuick.Enabled == nil || *file.LastSwarmQuick.Enabled {
		t.Fatalf("快速对话开关应更新为 false，got %+v", file.LastSwarmQuick)
	}
	if file.LastSwarmQuick.Bees == nil || file.LastSwarmQuick.Bees.Model != "sonnet" {
		t.Fatalf("快速对话原有的执行侧模型应保留，got %+v", file.LastSwarmQuick.Bees)
	}
}

// TestSceneScopedChatsProject 验证属于内置「对话」项目的真实快速对话走快速对话场景：
// 写入时归一到 last_model_quick/last_swarm_quick 而不改项目场景记录，读取时按快速对话恢复。
func TestSceneScopedChatsProject(t *testing.T) {
	path := resetSceneScopedState(t)
	providers := sceneTestProviders()

	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	chat := newChatState(fake)
	defer chat.close()

	st := NewSettingsState(settingsTestDir(t), nil, chat)
	st.cfg.Providers.Claude.Enabled = true
	st.cfg.Providers.Codex.Enabled = true
	on := true
	st.cfg.Orchestration.Enabled = &on
	setChatSettings(chat, st)
	defer setChatSettings(chat, nil)

	chat.mu.Lock()
	chat.newThreadProvider = "claude"
	chat.providers = providers
	chat.threads = []Thread{
		{ID: "t-proj", ProjectID: "p1"},
		{ID: "t-chats", ProjectID: "chats-builtin"},
	}
	gotChatsRemember := chat.projectIDForRememberLocked("t-chats")
	gotProjRemember := chat.projectIDForRememberLocked("t-proj")
	chat.mu.Unlock()
	if gotChatsRemember != "" {
		t.Fatalf("内置对话项目会话应归一为空串，got %q", gotChatsRemember)
	}
	if gotProjRemember != "p1" {
		t.Fatalf("普通项目会话应保留项目 ID，got %q", gotProjRemember)
	}

	waitSwarmEnabledIdle := func() {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if !chat.swarmActionBusy("enabled") {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("等待 setSwarmEnabled 完成超时")
	}

	// 先在普通项目会话写入项目场景基线：开启编排、选择执行侧与主导模型。
	chat.mu.Lock()
	chat.selectedThreadID = "t-proj"
	chat.mu.Unlock()
	chat.setSwarmEnabled(true)
	waitSwarmEnabledIdle()

	projBeesDone := make(chan struct{})
	chat.updateComposerPrefsWith("t-proj", func(p *ComposerPrefs) {
		p.BeesProvider = "codex"
		p.BeesModel = "o3"
		p.BeesEffort = "high"
	}, "设置子代理模型失败", func(ComposerPrefs) { close(projBeesDone) })
	<-projBeesDone
	projLead := ModelSelection{Provider: "codex", Model: "gpt-5.5", Effort: "medium"}
	chat.rememberModelSelectionForThread("t-proj", projLead)

	// 再在内置「对话」项目会话里关闭编排、选执行侧模型、选主导模型，应只写快速对话场景。
	chat.mu.Lock()
	chat.selectedThreadID = "t-chats"
	chat.mu.Unlock()
	chat.setSwarmEnabled(false)
	waitSwarmEnabledIdle()

	chatsBeesDone := make(chan struct{})
	chat.updateComposerPrefsWith("t-chats", func(p *ComposerPrefs) {
		p.BeesProvider = "claude"
		p.BeesModel = "sonnet"
		p.BeesEffort = "low"
	}, "设置子代理模型失败", func(ComposerPrefs) { close(chatsBeesDone) })
	<-chatsBeesDone
	chatsLead := ModelSelection{Provider: "claude", Model: "opus", Effort: "high", Fast: true}
	chat.rememberModelSelectionForThread("t-chats", chatsLead)
	waitUIStateWrites()

	file := mustReadUIStateFile(t, path)
	if file.LastModel != projLead {
		t.Fatalf("项目场景主导模型不应被内置对话会话覆盖，got %+v，want %+v", file.LastModel, projLead)
	}
	if file.LastSwarm == nil || file.LastSwarm.Enabled == nil || !*file.LastSwarm.Enabled ||
		file.LastSwarm.Bees == nil || file.LastSwarm.Bees.Provider != "codex" || file.LastSwarm.Bees.Model != "o3" || file.LastSwarm.Bees.Effort != "high" {
		t.Fatalf("项目场景编排记录不应被内置对话会话覆盖，got %+v", file.LastSwarm)
	}
	if file.LastModelQuick == nil || *file.LastModelQuick != chatsLead {
		t.Fatalf("内置对话会话应写入 last_model_quick=%+v，got %+v", chatsLead, file.LastModelQuick)
	}
	if file.LastSwarmQuick == nil || file.LastSwarmQuick.Enabled == nil || *file.LastSwarmQuick.Enabled ||
		file.LastSwarmQuick.Bees == nil || file.LastSwarmQuick.Bees.Provider != "claude" || file.LastSwarmQuick.Bees.Model != "sonnet" || file.LastSwarmQuick.Bees.Effort != "low" {
		t.Fatalf("内置对话会话应写入 last_swarm_quick，got %+v", file.LastSwarmQuick)
	}

	// 新建会话：chats 项目读取快速对话场景记录，普通项目读取项目场景记录。
	gotChatsOpts := chat.newThreadOptions("chats-builtin")
	if gotChatsOpts.Provider != "claude" || gotChatsOpts.Model != "opus" || gotChatsOpts.Effort != "high" || !gotChatsOpts.Fast ||
		gotChatsOpts.SwarmEnabled || gotChatsOpts.BeesProvider != "claude" || gotChatsOpts.BeesModel != "sonnet" || gotChatsOpts.BeesEffort != "low" {
		t.Fatalf("chats 项目新建会话应读取快速对话记录，got %+v", gotChatsOpts)
	}
	gotProjOpts := chat.newThreadOptions("p1")
	if gotProjOpts.Provider != "codex" || gotProjOpts.Model != "gpt-5.5" || gotProjOpts.Effort != "medium" || gotProjOpts.Fast ||
		!gotProjOpts.SwarmEnabled || gotProjOpts.BeesProvider != "codex" || gotProjOpts.BeesModel != "o3" || gotProjOpts.BeesEffort != "high" {
		t.Fatalf("普通项目新建会话应读取项目场景记录，got %+v", gotProjOpts)
	}
}
