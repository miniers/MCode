package mcodeui

import (
	"reflect"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/reviewcheck"
	"mcode/internal/testenv"
)

func TestSwarmPairs_SaveUpdateDeletePersistence(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	// 1. 新建配对
	effortHigh := "high"
	draft := config.SwarmPair{
		Name:           "Code Review Pair",
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-3-7-sonnet",
		LeadEffort:     &effortHigh,
		LeadFastMode:   true,
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-4o",
	}

	saved := swarmPairSave(st, draft, true)
	if saved.ID == "" {
		t.Fatalf("expected non-empty ID after swarmPairSave")
	}
	if len(st.cfg.Orchestration.Pairs) != 1 {
		t.Fatalf("expected 1 pair in state, got %d", len(st.cfg.Orchestration.Pairs))
	}

	// 重新从磁盘加载配置，验证持久化
	cfgDisk, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(cfgDisk.Orchestration.Pairs) != 1 {
		t.Fatalf("expected 1 pair persisted to disk, got %d", len(cfgDisk.Orchestration.Pairs))
	}
	if cfgDisk.Orchestration.Pairs[0].ID != saved.ID || cfgDisk.Orchestration.Pairs[0].Name != "Code Review Pair" {
		t.Fatalf("persisted pair mismatch: %+v", cfgDisk.Orchestration.Pairs[0])
	}

	// 2. 编辑配对
	saved.Name = "Updated Pair Name"
	saved.WorkerModel = "gpt-4o-mini"
	updated := swarmPairSave(st, saved, false)
	if updated.Name != "Updated Pair Name" || updated.WorkerModel != "gpt-4o-mini" {
		t.Fatalf("expected updated fields, got %+v", updated)
	}

	cfgDisk2, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(cfgDisk2.Orchestration.Pairs) != 1 || cfgDisk2.Orchestration.Pairs[0].Name != "Updated Pair Name" || cfgDisk2.Orchestration.Pairs[0].WorkerModel != "gpt-4o-mini" {
		t.Fatalf("persisted updated pair mismatch: %+v", cfgDisk2.Orchestration.Pairs[0])
	}

	// 3. 删除配对
	deleted := swarmPairDelete(st, saved.ID)
	if !deleted {
		t.Fatalf("expected swarmPairDelete to return true")
	}
	if len(st.cfg.Orchestration.Pairs) != 0 {
		t.Fatalf("expected 0 pairs in state after delete, got %d", len(st.cfg.Orchestration.Pairs))
	}

	cfgDisk3, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(cfgDisk3.Orchestration.Pairs) != 0 {
		t.Fatalf("expected 0 pairs persisted after delete, got %d", len(cfgDisk3.Orchestration.Pairs))
	}
}

func TestSwarmPairTitle(t *testing.T) {
	testenv.TempHome(t)
	providers := []ProviderInfo{
		{
			ID:   config.ProviderClaude,
			Name: "Claude",
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"},
			},
		},
		{
			ID:   config.ProviderCodex,
			Name: "Codex",
			Models: []ModelInfo{
				{ID: "gpt-4o", Name: "GPT-4o"},
			},
		},
	}

	// 自定义名称
	p1 := config.SwarmPair{
		Name: "我的配对",
	}
	if got := swarmPairTitle(p1, providers); got != "我的配对" {
		t.Errorf("expected '我的配对', got %q", got)
	}

	// 自动拼接模型名
	p2 := config.SwarmPair{
		LeadProvider:   config.ProviderClaude,
		LeadModel:      "claude-3-7-sonnet",
		WorkerProvider: config.ProviderCodex,
		WorkerModel:    "gpt-4o",
	}
	if got := swarmPairTitle(p2, providers); got != "Claude 3.7 Sonnet + GPT-4o" {
		t.Errorf("expected 'Claude 3.7 Sonnet + GPT-4o', got %q", got)
	}

	// 空值默认占位名
	p3 := config.SwarmPair{
		LeadProvider: config.ProviderClaude,
	}
	if got := swarmPairTitle(p3, providers); got != "任意模型 + 跟随主代理" {
		t.Errorf("expected '任意模型 + 跟随主代理', got %q", got)
	}
}

func TestSwarmAutoMergeAndAutoPush_Independent(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	st.cfg.Orchestration.AutoMerge = false

	// 从均关闭的状态验证两个开关独立
	if st.cfg.Orchestration.AutoMerge {
		t.Fatalf("expected AutoMerge to be false by default")
	}
	if st.cfg.Orchestration.AutoPush {
		t.Fatalf("expected AutoPush to be false by default")
	}

	// 只开启 AutoPush，AutoMerge 保持 false
	tt := newSwarmPageTester(st)
	if err := tt.Click("切换自动推送"); err != nil {
		t.Fatal(err)
	}

	cfg1, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if !cfg1.Orchestration.AutoPush {
		t.Errorf("expected AutoPush to be true")
	}
	if cfg1.Orchestration.AutoMerge {
		t.Errorf("expected AutoMerge to remain false when only AutoPush is set")
	}

	// 重置 AutoPush，只开启 AutoMerge
	if err := tt.Click("切换自动推送"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("切换自动合并"); err != nil {
		t.Fatal(err)
	}

	cfg2, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if !cfg2.Orchestration.AutoMerge {
		t.Errorf("expected AutoMerge to be true")
	}
	if cfg2.Orchestration.AutoPush {
		t.Errorf("expected AutoPush to remain false when only AutoMerge is set")
	}
	if err := tt.Click("切换自动推送"); err != nil {
		t.Fatal(err)
	}
	cfg3, err := config.Load(dataDir)
	if err != nil || !cfg3.Orchestration.AutoMerge || !cfg3.Orchestration.AutoPush {
		t.Fatalf("both switches must be independently enabled: %+v, %v", cfg3.Orchestration, err)
	}
}

func newSwarmPageTester(st *SettingsState) *ui.Tester {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Width(1100).Children(func() { renderPageSwarm(c, st) })
	}, 1200, 7000)
	tt.Frame()
	return tt
}

// 页面状态按 SettingsState 复用同一个指针，Close 后清零。
func TestSettingsState_SwarmPagePointers(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	first := getSwarmUIState(st)
	if second := getSwarmUIState(st); second != first {
		t.Fatal("getSwarmUIState 必须复用同一个指针")
	}
	firstTiers := getSwarmTiersUIState(st)
	if second := getSwarmTiersUIState(st); second != firstTiers {
		t.Fatal("getSwarmTiersUIState 必须复用同一个指针")
	}

	st.Close()
	if st.swarmPage != nil {
		t.Fatal("Close 后 swarmPage 必须为 nil")
	}
	if st.swarmTiersPage != nil {
		t.Fatal("Close 后 swarmTiersPage 必须为 nil")
	}
}

func TestSwarmSettings_ConflictingMessageModesCanBeDisabled(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.AlwaysBees = true
	st.cfg.Orchestration.LeadPlansOnly = true
	tt := newSwarmPageTester(st)
	if err := tt.Click("切换主代理只规划"); err != nil {
		t.Fatal(err)
	}
	if st.cfg.Orchestration.LeadPlansOnly {
		t.Fatal("conflicting enabled mode must remain switchable off")
	}
	if err := tt.Click("切换消息直接交给子代理"); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(st.dataDir)
	if err != nil || loaded.Orchestration.AlwaysBees {
		t.Fatalf("both representations must persist disabled: %+v, %v", loaded.Orchestration, err)
	}
}

func TestSwarmSettings_AdditionalControlsPersist(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.AutoPopsBees = false
	tt := newSwarmPageTester(st)
	for _, label := range []string{"切换自动弹出子代理面板", "自动归档"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := config.Load(st.dataDir)
	if err != nil || !loaded.Orchestration.AutoPopsBees || !loaded.Orchestration.ArchivesCodexBees {
		t.Fatalf("additional Swarm controls must persist: %+v, %v", loaded.Orchestration, err)
	}
}

// 子线程提问自动转交：档位选择后写回配置并落盘。
func TestSwarmSettings_AutoForwardQuestionDelay(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.AutoForwardQuestionSeconds = 0
	// 转交行位于消息流向分组的末尾。
	var messages settingsSection
	for _, section := range pageSwarm.Sections {
		if section.ID == "swarm.messages" {
			messages = section
			break
		}
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsTreeSection(c, st, messages, false)
	}, 1000, 4000)
	tt.Frame()
	for _, text := range []string{"无人回答时转给主代理", "关闭"} {
		if !tt.HasText(text) {
			t.Fatalf("自动转交行缺少文案 %q: %v", text, tt.Texts())
		}
	}
	if err := tt.Click("3 分钟"); err != nil {
		t.Fatalf("click auto forward option failed: %v", err)
	}
	waitFor(t, tt, func() bool { return st.cfg.Orchestration.AutoForwardQuestionSeconds == 180 })

	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if loaded.Orchestration.AutoForwardQuestionSeconds != 180 {
		t.Fatalf("落盘的自动转交秒数 = %d，想要 180", loaded.Orchestration.AutoForwardQuestionSeconds)
	}
	if got := loaded.Orchestration.AutoForwardQuestionDelay(); got != 3*time.Minute {
		t.Fatalf("AutoForwardQuestionDelay() = %v，想要 3m", got)
	}
}

// 最多派发轮次：行标题、说明文字与当前取值按配置渲染，0 显示为不限制。
func TestSwarmSettings_MaxRoundsRow(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSection(c, st, advancedOrchestration, false) }, 1000, 4000)
	if !tt.HasText("最多派发轮次") {
		t.Fatalf("max rounds row must render with its description: %v", tt.Texts())
	}
	if err := tt.Click("编辑最多派发轮次"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Command("copy")
	if got := tt.Clipboard(); got != "10" {
		t.Fatalf("default max rounds = %q, want 10", got)
	}

	st.cfg.Orchestration.MaxRounds = 7
	tt2 := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSection(c, st, advancedOrchestration, false) }, 1000, 4000)
	if err := tt2.Click("编辑最多派发轮次"); err != nil {
		t.Fatal(err)
	}
	tt2.Command("selectAll")
	tt2.Command("copy")
	if got := tt2.Clipboard(); got != "7" {
		t.Fatalf("configured max rounds = %q, want 7", got)
	}
}

func TestSwarmSettings_PairEditorAndPreset(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	tt := newSwarmPageTester(st)
	if err := tt.Click("添加配对"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("编辑配对名称"); err != nil {
		t.Fatal(err)
	}
	tt.Type("测试配对")
	title, ok := tt.Find("添加模型配对")
	if !ok {
		t.Fatal("pair editor must be visible")
	}
	tt.Scroll(title.X+100, title.Y+60, 0, 10000)
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(st.dataDir)
	if err != nil || len(loaded.Orchestration.Pairs) != 1 || loaded.Orchestration.Pairs[0].Name != "测试配对" {
		t.Fatalf("pair editor must persist new pair: %+v, %v", loaded.Orchestration.Pairs, err)
	}
	if err := tt.Click("编辑"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("编辑模型配对") {
		t.Fatal("edit action must open pair editor")
	}
	if err := tt.Click("取消"); err != nil {
		// 编辑器复用滚动位置，未滚动时按钮在下方。
		title, _ = tt.Find("编辑模型配对")
		tt.Scroll(title.X+100, title.Y+60, 0, 10000)
		if err := tt.Click("取消"); err != nil {
			t.Fatal(err)
		}
	}
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	if len(st.cfg.Orchestration.Pairs) != 0 {
		t.Fatal("delete action must remove pair")
	}
	if err := tt.Click("预设组合"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("编排预设组合") || !tt.HasText("等待") || !tt.HasText("需要配置 Claude") {
		t.Fatalf("preset must show missing dependencies: %v", tt.Texts())
	}
}

func TestSwarmSettings_LockedAutoActions(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), map[string]bool{
		"swarm-auto-merge": true,
		"swarm-auto-push":  true,
	}, nil)
	tt := newSwarmPageTester(st)
	for _, label := range []string{"切换自动合并", "切换自动推送"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if !st.cfg.Orchestration.AutoMerge || st.cfg.Orchestration.AutoPush {
		t.Fatal("locked auto actions must not change")
	}
}

func TestSwarmPairCapabilitiesResetOnModelChange(t *testing.T) {
	testenv.TempHome(t)
	pair := config.SwarmPair{
		LeadProvider: config.ProviderClaude,
		LeadEffort:   swarmStringPtr("max"), LeadFastMode: true,
		WorkerEffort: swarmStringPtr("high"), WorkerFastMode: true,
	}
	resetSwarmPairCapabilities(&pair, true)
	if pair.LeadEffort != nil || pair.LeadFastMode || pair.WorkerEffort != nil || pair.WorkerFastMode {
		t.Fatal("lead model change must reset capabilities on both inherited sides")
	}
	pair.WorkerProvider, pair.WorkerModel = config.ProviderCodex, "gpt-5.5"
	pair.WorkerEffort, pair.WorkerFastMode = swarmStringPtr("high"), true
	resetSwarmPairCapabilities(&pair, true)
	if pair.WorkerEffort == nil || !pair.WorkerFastMode {
		t.Fatal("independent worker capabilities must remain intact")
	}
	resetSwarmPairCapabilities(&pair, false)
	if pair.WorkerEffort != nil || pair.WorkerFastMode {
		t.Fatal("worker model change must reset its capabilities")
	}
}

func TestSwarmSettings_PageRenderTexts(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)

	var settings *SettingsState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
		settings = getChatSettings(s)
	}), 1200, 7000)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("编排")
	})
	settings.selectSettingsPage("swarm")
	tt.Frame()

	waitFor(t, tt, func() bool {
		return tt.HasText("模型配对") &&
			tt.HasText("自动合并") &&
			tt.HasText("自动推送")
	})
}

// 「合并前审查」三档选择映射到 ReviewMode（off/ai/human，S-092）。
func TestSwarmReviewModePicker_WritesConsumedField(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	var done settingsSection
	for _, section := range pageSwarm.Sections {
		if section.ID == "swarm.done" {
			done = section
			break
		}
	}
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSection(c, st, done, false) }, 1000, 4000)
	tt.Frame()
	if err := tt.Click("AI 审查"); err != nil {
		t.Fatalf("click 'AI 审查' failed: %v", err)
	}
	tt.Frame()
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if cfg.Orchestration.ReviewMode != config.ReviewModeAI {
		t.Fatalf("选择 AI 审查应写入 ReviewMode=ai，实际为 %q（%+v）", cfg.Orchestration.ReviewMode, cfg.Orchestration)
	}

	if err := tt.Click("人工审查"); err != nil {
		t.Fatalf("click '人工审查' failed: %v", err)
	}
	tt.Frame()
	cfg, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if cfg.Orchestration.ReviewMode != config.ReviewModeHuman {
		t.Fatalf("选择人工审查应写入 ReviewMode=human，实际为 %q（%+v）", cfg.Orchestration.ReviewMode, cfg.Orchestration)
	}

	if err := tt.Click("关闭"); err != nil {
		t.Fatalf("click '关闭' failed: %v", err)
	}
	tt.Frame()
	cfg, err = config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if cfg.Orchestration.ReviewMode != config.ReviewModeOff {
		t.Fatalf("选择关闭应写入 ReviewMode=off，实际为 %q（%+v）", cfg.Orchestration.ReviewMode, cfg.Orchestration)
	}
}

// AI 审查档才显示「审查模型」行。
func TestSwarmReviewModeAIShowsReviewModelRow(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.ReviewMode = config.ReviewModeAI

	tt := newSwarmPageTester(st)
	if !tt.HasText("审查模型") {
		t.Fatalf("AI 审查档应显示「审查模型」行，实际文本：%v", tt.Texts())
	}
}

// 编辑配对时草稿是深拷贝：弹层里清掉的档位映射不能写穿到运行态配置（S-094）。
func TestSwarmPairEdit_DraftDoesNotMutateRuntimeConfig(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	st.cfg.Orchestration.Pairs = []config.SwarmPair{{
		ID:           "pair-1",
		Name:         "配对一",
		LeadProvider: config.ProviderClaude,
		BeeTiers: map[string]config.SwarmTierOverride{
			"deep": {Model: "o1"},
		},
	}}

	tt := newSwarmPageTester(st)
	if err := tt.Click("编辑"); err != nil {
		t.Fatalf("click '编辑' failed: %v", err)
	}
	// 弹层里的「恢复继承」删除的是草稿里的映射，不应该改到 st.cfg。
	if err := tt.Click("恢复继承"); err != nil {
		t.Fatalf("click '恢复继承' failed: %v", err)
	}
	if got := st.cfg.Orchestration.Pairs[0].BeeTiers; len(got) != 1 || got["deep"].Model != "o1" {
		t.Fatalf("编辑草稿不应改动运行态配对的 BeeTiers，实际为 %+v", got)
	}

	if err := tt.Click("取消"); err != nil {
		t.Fatalf("click '取消' failed: %v", err)
	}
	st.save()
	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if len(loaded.Orchestration.Pairs) != 1 || len(loaded.Orchestration.Pairs[0].BeeTiers) != 1 {
		t.Fatalf("取消编辑后落盘的配对不应丢掉档位映射，实际为 %+v", loaded.Orchestration.Pairs)
	}
}

// newSwarmReviewSettingsWithProject 构造一个已选中项目会话的设置页状态，
// 供需要 currentProjectDir() 的审查漏斗测试使用。
func newSwarmReviewSettingsWithProject(t *testing.T, dataDir, projectDir string) *SettingsState {
	t.Helper()
	chat := &chatState{
		selectedThreadID: "thread-review",
		threads:          []Thread{{ID: "thread-review", ProjectID: "proj-review"}},
		projects:         []Project{{ID: "proj-review", Path: projectDir}},
	}
	return NewSettingsState(dataDir, nil, chat)
}

// AI 审查档显示漏斗的两项设置；没有打开项目时检查命令行提示先打开项目。
func TestSwarmReviewFunnelRowsInAIMode(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.ReviewMode = config.ReviewModeAI

	tt := newSwarmPageTester(st)
	for _, text := range []string{"检查命令（本项目）"} {
		if !tt.HasText(text) {
			t.Fatalf("AI 审查档缺少漏斗设置 %q，实际文本：%v", text, tt.Texts())
		}
	}
}

// off 与 human 档都不显示漏斗的两项设置。
func TestSwarmReviewFunnelRowsHiddenInOffMode(t *testing.T) {
	testenv.TempHome(t)
	for _, mode := range []string{config.ReviewModeOff, config.ReviewModeHuman} {
		st := NewSettingsState(settingsTestDir(t), nil, nil)
		st.cfg.Orchestration.ReviewMode = mode

		tt := newSwarmPageTester(st)
		for _, text := range []string{"检查命令（本项目）", "自动通过行数上限"} {
			if tt.HasText(text) {
				t.Fatalf("%s 档不应显示漏斗设置 %q，实际文本：%v", mode, text, tt.Texts())
			}
		}
	}
}

// 保存检查命令只覆盖 Command，项目配置里原有的超时与跑测试开关必须保留。
func TestSwarmReviewCheckCommandSave(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	projectDir := t.TempDir()
	if err := reviewcheck.SaveProjectConfig(dataDir, projectDir, reviewcheck.ProjectConfig{
		Command:        "",
		TimeoutSeconds: 300,
		RunTests:       true,
	}); err != nil {
		t.Fatalf("准备项目检查配置失败: %v", err)
	}

	st := newSwarmReviewSettingsWithProject(t, dataDir, projectDir)
	st.cfg.Orchestration.ReviewMode = config.ReviewModeAI
	tt := newSwarmPageTester(st)

	if err := tt.Click("编辑检查命令（本项目）"); err != nil {
		t.Fatalf("click check command input failed: %v", err)
	}
	tt.Type("go test ./...")
	waitFor(t, tt, func() bool { return st.reviewCheckCommand == "go test ./..." })

	if err := tt.Click("保存检查命令"); err != nil {
		t.Fatalf("click save check command failed: %v", err)
	}
	waitFor(t, tt, func() bool { return st.reviewCheckSaved && st.reviewCheckErr == "" })

	got, err := reviewcheck.LoadProjectConfig(dataDir, projectDir)
	if err != nil {
		t.Fatalf("读回项目检查配置失败: %v", err)
	}
	if got.Command != "go test ./..." {
		t.Fatalf("保存后的检查命令 = %q，想要 %q", got.Command, "go test ./...")
	}
	if got.TimeoutSeconds != 300 {
		t.Fatalf("保存检查命令不应覆盖超时，读回 %d 秒，想要 300", got.TimeoutSeconds)
	}
	if !got.RunTests {
		t.Fatal("保存检查命令不应覆盖 RunTests，读回被关掉了")
	}
}

// 输入行数上限后写入 cfg.Orchestration.ReviewMaxChangedLines 并落盘。
func TestSwarmReviewMaxChangedLinesSave(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Orchestration.ReviewMode = config.ReviewModeAI
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreeSection(c, st, advancedOrchestration, false) }, 1000, 4000)
	if err := tt.Click("编辑自动通过行数上限"); err != nil {
		t.Fatal(err)
	}
	tt.Command("selectAll")
	tt.Type("123")
	waitFor(t, tt, func() bool { return st.cfg.Orchestration.ReviewMaxChangedLines == 123 })
	val := 123
	loaded, err := config.Load(st.dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if loaded.Orchestration.ReviewMaxChangedLines != val {
		t.Fatalf("落盘的行数上限 = %d，想要 %d", loaded.Orchestration.ReviewMaxChangedLines, val)
	}
}

func TestSettingsSwarm_TreeSectionsAndAdvanced(t *testing.T) {
	page, ok := settingsTreePageOf("swarm")
	if !ok {
		t.Fatal("编排页未注册")
	}
	ids := map[string]bool{"row-swarm-max-bees": true, "row-swarm-max-rounds": true, "row-swarm-bee-timeout": true, "row-swarm-request-timeout": true, "row-swarm-review-max-lines": true}
	var found bool
	for _, section := range page.Sections {
		for _, row := range section.Rows {
			if ids[row.ID] {
				if section.ID != "advanced.orchestration" {
					t.Fatalf("高级行出现在错误分节 %s: %s", section.ID, row.ID)
				}
				delete(ids, row.ID)
			} else if row.ID == "row-steward-home-dir" {
				t.Fatalf("总管高级行出现在编排页: %s", row.ID)
			}
		}
		if section.ID == "advanced.orchestration" {
			if found || len(section.Rows) != 5 {
				t.Fatalf("高级分节重复或行数错误: %d", len(section.Rows))
			}
			found = true
		}
	}
	if !found || len(ids) != 0 {
		t.Fatalf("缺少高级分节或高级行: %v", ids)
	}
}

func TestSwarmSections_Reorganized(t *testing.T) {
	sections := pageSwarm.Sections
	var messages, models, done, advanced int = -1, -1, -1, -1
	for i, sec := range sections {
		switch sec.ID {
		case "swarm.messages":
			messages = i
		case "swarm.models":
			models = i
		case "swarm.done":
			done = i
		case "advanced.orchestration":
			advanced = i
		case "swarm.forward", "swarm.plan":
			t.Fatalf("旧分节仍存在: %s", sec.ID)
		}
	}
	if messages < 0 || models != messages+1 || done != models+1 || advanced != len(sections)-1 {
		t.Fatalf("分节顺序错误: messages=%d models=%d done=%d advanced=%d", messages, models, done, advanced)
	}
	if sections[models].Title != "子代理默认模型" || sections[advanced].Title != "高级" {
		t.Fatal("分节标题错误")
	}
	ids := func(sec settingsSection) []string {
		var result []string
		for _, row := range sec.Rows {
			result = append(result, row.ID)
		}
		return result
	}
	if got, want := ids(sections[messages]), []string{"row-swarm-auto-delegate", "row-swarm-lead-plans-only", "row-swarm-send-to-bees", "row-swarm-auto-forward-question"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("消息行错误: %v", got)
	}
	if got, want := ids(sections[advanced]), []string{"row-swarm-max-bees", "row-swarm-max-rounds", "row-swarm-bee-timeout", "row-swarm-request-timeout", "row-swarm-review-max-lines"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("高级行错误: %v", got)
	}
}
