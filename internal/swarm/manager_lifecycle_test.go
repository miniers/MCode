package swarm

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"mcode/internal/kanban"
)

// archiveFakeDriver 在 manager_test.go 的 fakeDriver 之上实现 SessionArchiver，记录归档调用。
type archiveFakeDriver struct {
	*fakeDriver
	archMu   sync.Mutex
	archived []string
	err      error
}

func newArchiveFakeDriver() *archiveFakeDriver {
	return &archiveFakeDriver{fakeDriver: newFakeDriver()}
}

func (d *archiveFakeDriver) ArchiveBeeSession(ctx context.Context, threadID string) (bool, error) {
	d.archMu.Lock()
	defer d.archMu.Unlock()
	d.archived = append(d.archived, threadID)
	if d.err != nil {
		return false, d.err
	}
	return true, nil
}

func (d *archiveFakeDriver) archiveCount() int {
	d.archMu.Lock()
	defer d.archMu.Unlock()
	return len(d.archived)
}

// 开关关闭时不归档、不清理。
func TestManager_LifecycleSwitchesOff(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newArchiveFakeDriver()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport:    func(string, string) error { return nil },
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
	})

	leadID := "lead-life-off"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "T1", Prompt: "P1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)

	if got := driver.archiveCount(); got != 0 {
		t.Fatalf("开关关闭时不应归档，实际归档 %d 次", got)
	}
	st := mgr.State(leadID)
	if len(st.Bees) != 1 {
		t.Fatalf("开关关闭时不应清理 bee，实际 %d 个", len(st.Bees))
	}
	if !st.Bees[0].Reported {
		t.Fatalf("bee 应已汇报: %+v", st.Bees[0])
	}
	if st.Bees[0].ArchiveHandled || st.Bees[0].Archived {
		t.Fatalf("开关关闭时不应标记归档: %+v", st.Bees[0])
	}
}

// 开关打开后 bee 完成并汇报后被归档一次，再 tick 不会重复调用。
func TestManager_ArchivesFinishedBeesOnce(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newArchiveFakeDriver()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport:    func(string, string) error { return nil },
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree", ArchivesCodexBees: true}
	})

	leadID := "lead-archive-once"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "T1", Prompt: "P1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)

	if got := driver.archiveCount(); got != 1 {
		t.Fatalf("bee 汇报后应归档一次，实际 %d 次", got)
	}
	st := mgr.State(leadID)
	if len(st.Bees) != 1 || !st.Bees[0].ArchiveHandled || !st.Bees[0].Archived {
		t.Fatalf("归档状态未写回: %+v", st.Bees)
	}

	mgr.Tick(ctx)
	if got := driver.archiveCount(); got != 1 {
		t.Fatalf("已归档的 bee 不应重复归档，实际 %d 次", got)
	}
}

// AutoClearFinished 打开时，lead 全部 bee 结束并汇报后被移出 State。
func TestManager_AutoClearFinished(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport:    func(string, string) error { return nil },
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree", AutoClearFinished: true}
	})

	leadID := "lead-auto-clear"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees:         []BeeSpec{{Name: "Bee-1", Task: "T1", Prompt: "P1"}},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)

	if bees := mgr.State(leadID).Bees; len(bees) != 0 {
		t.Fatalf("全部结束并汇报后应被清理，实际剩 %d 个", len(bees))
	}
}

// AutoClearFinished 打开但仍有 running bee 时不清理。
func TestManager_AutoClearFinished_KeepsRunningLead(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)
	driver := newFakeDriver()

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		OnReport:    func(string, string) error { return nil },
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree", AutoClearFinished: true}
	})

	leadID := "lead-auto-clear-running"
	bees, err := mgr.Dispatch(ctx, DispatchRequest{
		LeadThreadID: leadID,
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Bees: []BeeSpec{
			{Name: "Bee-1", Task: "T1", Prompt: "P1"},
			{Name: "Bee-2", Task: "T2", Prompt: "P2"},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch failed: %v", err)
	}
	driver.setSessionState(bees[0].ThreadID, kanban.SessionState{Running: false, LastAssistantText: "done"})
	mgr.Tick(ctx)

	st := mgr.State(leadID)
	if len(st.Bees) != 2 {
		t.Fatalf("仍有 running bee 时不应清理，实际剩 %d 个", len(st.Bees))
	}
	if st.Bees[0].Status != StatusCompleted || st.Bees[1].Status != StatusRunning {
		t.Fatalf("bee 状态不符合预期: %+v", st.Bees)
	}
}

// 落地失败或带冲突的 bee 不会被自动清理，避免丢掉唯一的成果副本。
func TestManager_AutoClearFinished_KeepsFailedMerge(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoClearFinished: true}
	})

	mgr.mu.Lock()
	mgr.leads["lead-err"] = &leadState{leadThreadID: "lead-err", bees: []Bee{
		{ThreadID: "h1", Status: StatusCompleted, Reported: true, MergeError: "git apply --3way failed"},
	}}
	mgr.leads["lead-conflict"] = &leadState{leadThreadID: "lead-conflict", bees: []Bee{
		{ThreadID: "h2", Status: StatusCompleted, Reported: true, Merged: true, ConflictFiles: []string{"a.go"}},
	}}
	mgr.mu.Unlock()

	mgr.autoClearFinished(ctx)

	for _, id := range []string{"lead-err", "lead-conflict"} {
		if got := len(mgr.State(id).Bees); got != 1 {
			t.Fatalf("%s 落地失败/有冲突时不应被自动清理，实际剩 %d 个", id, got)
		}
	}
}

// planEntry/DispatchSingleEntry 带 profile 时，Profile 文件通过 Options.ProfilesPath 指定，
// Provider/Model/Effort 与 Prompt 前缀都来自 Profile。
func TestManager_DispatchSingleEntryWithProfile(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	profilesPath := filepath.Join(t.TempDir(), "hydra-profiles.json")
	content := `[{"name":"quick","provider":"codex","model":"gpt-5","effort":"high","system_prompt":"专用人设"}]`
	if err := os.WriteFile(profilesPath, []byte(content), 0644); err != nil {
		t.Fatalf("write profiles: %v", err)
	}

	driver := newFakeDriver()
	var started kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = req }

	mgr := NewManager(Options{
		Git:          runner,
		Driver:       driver,
		WorktreeDir:  t.TempDir(),
		ProfilesPath: profilesPath,
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
	})

	bee, notices, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-profile",
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "T", Prompt: "P", Profile: "quick"},
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("不该产生提示: %v", notices)
	}
	if bee.Profile != "quick" {
		t.Fatalf("bee.Profile = %q，预期 quick", bee.Profile)
	}
	if started.Provider != "codex" || started.Model != "gpt-5" || started.Effort != "high" {
		t.Fatalf("SessionRequest 未采用 Profile 配置: %+v", started)
	}
	if !IsBeePrompt(started.Prompt) || !strings.HasPrefix(started.Prompt, "你是子代理 Aspen") {
		t.Fatalf("Prompt 应以 bee 前言开头: %q", started.Prompt)
	}
	if !strings.Contains(started.Prompt, "## 档位要求\n专用人设\n\n## 工作方式") {
		t.Fatalf("Prompt 缺少档位要求段: %q", started.Prompt)
	}
}

// 条目声明了 skills 且设置了 SkillsGuidance 时，bee 启动提示词追加该函数返回的技能块。
func TestManager_DispatchSingleEntryWithSkills(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var started kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = req }

	var calls int
	var gotWorkDir string
	var gotNames []string
	block := "[mcode 已启用技能]\n- alpha（全局）: 全局 alpha (path: /tmp/alpha/SKILL.md)"
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		SkillsGuidance: func(workDir string, names []string) string {
			calls++
			gotWorkDir = workDir
			gotNames = names
			return block
		},
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
	})

	if _, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-skills",
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "T", Prompt: "P", Skills: []string{"alpha", "beta"}},
	}); err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if calls != 1 {
		t.Fatalf("SkillsGuidance 应被调用 1 次，实际 %d", calls)
	}
	if gotWorkDir == "" {
		t.Fatalf("SkillsGuidance 应拿到 bee 工作目录")
	}
	if !reflect.DeepEqual(gotNames, []string{"alpha", "beta"}) {
		t.Fatalf("SkillsGuidance 名字 = %#v", gotNames)
	}
	if !strings.Contains(started.Prompt, block) {
		t.Fatalf("提示词应包含技能块: %q", started.Prompt)
	}
	if !strings.HasSuffix(started.Prompt, "\n\n"+block) {
		t.Fatalf("技能块应追加在 bee 提示词正文之后: %q", started.Prompt)
	}
}

// 条目不带 skills 时 SkillsGuidance 不被调用，提示词与 beePrompt 逐字一致。
func TestManager_DispatchSingleEntryWithoutSkills(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var started kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = req }

	var calls int
	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		SkillsGuidance: func(string, []string) string {
			calls++
			return "[mcode 已启用技能]"
		},
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree"}
	})

	entry := Delegation{Task: "T", Prompt: "P"}
	if _, _, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-skills",
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        entry,
	}); err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if calls != 0 {
		t.Fatalf("未声明 skills 时 SkillsGuidance 不应被调用，实际 %d 次", calls)
	}
	if strings.Contains(started.Prompt, "[mcode 已启用技能]") {
		t.Fatalf("未声明 skills 时提示词不应含技能块: %q", started.Prompt)
	}
	if want := beePrompt(RosterName(0), entry, BeeConfig{}); started.Prompt != want {
		t.Fatalf("未声明 skills 时提示词应为 beePrompt，got %q want %q", started.Prompt, want)
	}
}

// Settings.Pairs 配合 Delegation.Pair 时，Provider/Model/Effort/FastMode 来自配对的 Bee 侧。
func TestManager_DispatchSingleEntryWithPair(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var started kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = req }

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
		ThreadContext: func(string) ThreadContext {
			return ThreadContext{UserMessage: "这次用 pair-1 跑"}
		},
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree",
			Pairs: []Pair{{
				ID: "pair-1", Name: "快手",
				BeeProvider: "gemini", BeeModel: "gemini-3", BeeEffort: "low", BeeFastMode: true,
			}},
		}
	})

	_, notices, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-pair",
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "T", Prompt: "P", Pair: "pair-1"},
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if len(notices) != 0 {
		t.Fatalf("不该产生提示: %v", notices)
	}
	if started.Provider != "gemini" || started.Model != "gemini-3" || started.Effort != "low" || !started.FastMode {
		t.Fatalf("SessionRequest 未采用配对 Bee 侧配置: %+v", started)
	}
}

// 验证当 ThreadContext 未授权配对（为 nil 或消息中未提及）时，产生未授权提示且不采用配对配置。
func TestManager_DispatchSingleEntryWithPair_Unauthorized(t *testing.T) {
	ctx := context.Background()
	runner, dir := initTestRepo(t)

	driver := newFakeDriver()
	var started kanban.SessionRequest
	driver.onStart = func(req kanban.SessionRequest) { started = req }

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      driver,
		WorktreeDir: t.TempDir(),
	})
	mgr.SetSettingsFunc(func() Settings {
		return Settings{
			Enabled: true, AutoDelegate: true, WorkspaceMode: "worktree",
			Pairs: []Pair{{
				ID: "pair-1", Name: "快手",
				BeeProvider: "gemini", BeeModel: "gemini-3", BeeEffort: "low", BeeFastMode: true,
			}},
		}
	})

	_, notices, err := mgr.DispatchSingleEntry(ctx, SingleEntryRequest{
		LeadThreadID: "lead-pair-unauth",
		ProjectID:    "proj-1",
		LeadWorkDir:  dir,
		Entry:        Delegation{Task: "T", Prompt: "P", Pair: "pair-1"},
	})
	if err != nil {
		t.Fatalf("DispatchSingleEntry failed: %v", err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "未授权配对") {
		t.Fatalf("期望包含 '未授权配对' 的 1 条提示，实际: %v", notices)
	}
	if started.Provider == "gemini" && started.Model == "gemini-3" {
		t.Fatalf("未授权配对时 SessionRequest 不应采用 gemini/gemini-3: %+v", started)
	}
}

// 停止全部时应一并清掉该 lead 的排队条目，其它 lead 的排队条目不受影响。
func TestManager_StopAllClearsQueuedEntries(t *testing.T) {
	ctx := context.Background()
	runner, _ := initTestRepo(t)

	mgr := NewManager(Options{
		Git:         runner,
		Driver:      newFakeDriver(),
		WorktreeDir: t.TempDir(),
		OnReport:    func(string, string) error { return nil },
	})
	// 上限设为 1，让 lead B 占满名额：StopAll 之后的 processQueue 不会把它那条排队条目取走。
	mgr.SetSettingsFunc(func() Settings {
		return Settings{Enabled: true, MaxBees: 1}
	})

	mgr.SetEnabled("A", true)
	mgr.SetEnabled("B", true)

	mgr.mu.Lock()
	mgr.leads["B"].bees = append(mgr.leads["B"].bees, Bee{ThreadID: "bee-b-running", Status: StatusRunning})
	mgr.queue = append(mgr.queue,
		queuedBee{leadThreadID: "A", spec: BeeSpec{Name: "Bee-A1"}, index: 1},
		queuedBee{leadThreadID: "A", spec: BeeSpec{Name: "Bee-A2"}, index: 2},
		queuedBee{leadThreadID: "B", spec: BeeSpec{Name: "Bee-B1"}, index: 3},
	)
	mgr.mu.Unlock()

	if err := mgr.StopAll(ctx, "A"); err != nil {
		t.Fatalf("StopAll failed: %v", err)
	}

	if got := mgr.State("A").Queued; got != 0 {
		t.Fatalf("停止全部后 lead A 的排队条目应清空，实际 %d", got)
	}
	if got := mgr.State("B").Queued; got != 1 {
		t.Fatalf("lead B 的排队条目不应被清理，实际 %d", got)
	}
}

// InheritLead 把源会话的编排开关与只规划覆盖值继承给目标会话（转发会话场景）；
// 继承后两边各自独立，改目标不影响源；源未开启编排时目标保持未开启。
func TestManager_InheritLead(t *testing.T) {
	mgr := NewManager(Options{})

	plansOnly := true
	mgr.SetEnabled("lead-src", true)
	mgr.SetLeadPlansOnly("lead-src", &plansOnly)

	mgr.InheritLead("lead-src", "lead-dst")

	dst := mgr.State("lead-dst")
	if !dst.Enabled {
		t.Fatal("源会话开启编排时目标会话应继承为开启")
	}
	if dst.PlansOnly == nil || !*dst.PlansOnly {
		t.Fatalf("目标会话应继承只规划覆盖值，实际 %v", dst.PlansOnly)
	}

	// 改目标的只规划覆盖值不应影响源会话。
	off := false
	mgr.SetLeadPlansOnly("lead-dst", &off)
	if src := mgr.State("lead-src"); src.PlansOnly == nil || !*src.PlansOnly {
		t.Fatalf("改目标后源会话的只规划覆盖值不应变化，实际 %v", src.PlansOnly)
	}

	// 源未开启且没有只规划覆盖值时，目标保持未开启。
	mgr.InheritLead("lead-empty", "lead-dst2")
	dst2 := mgr.State("lead-dst2")
	if dst2.Enabled {
		t.Fatal("源会话未开启编排时目标会话不应变为开启")
	}
	if dst2.PlansOnly != nil {
		t.Fatalf("源会话没有只规划覆盖值时目标也不应有，实际 %v", dst2.PlansOnly)
	}
}
