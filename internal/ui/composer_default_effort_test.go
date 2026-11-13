package mcodeui

import (
	"context"
	"sync"
	"testing"
	"time"

	"mcode/internal/testenv"
)

// 本文件守护「切换模型时套用该模型默认推理强度」这一新行为：
// 取值优先级（用户偏好 > 模型默认档 > 空串）与三处写入点（待创建会话、已存在会话、执行侧）。

// defaultEffortState 构造一个已选中会话 t1 的 chatState，供切换模型用例复用。
// threadEffort/threadFast 是切换前会话的强度与快速模式，用于验证“旧值保留”与“旧值被覆盖”的差异。
func defaultEffortState(t *testing.T, threadEffort string, threadFast bool) (*FakeClient, *chatState) {
	t.Helper()
	// 记住最近一次模型选择会落盘，隔离到临时 HOME，避免污染真实配置。
	testenv.TempHome(t)
	fake := NewFakeClient()
	project := Project{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		Provider:  "claude",
		Model:     "claude-3-7-sonnet",
		Effort:    threadEffort,
		FastMode:  threadFast,
		UpdatedAt: time.Now(),
	}
	fake.SetProjectsAndThreads([]Project{project}, map[string][]Thread{"p1": {th}})
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Enabled: true})

	state := newChatState(fake)
	state.mu.Lock()
	state.projects = []Project{project}
	state.threadsByProj = map[string][]Thread{"p1": {th}}
	state.threads = []Thread{th}
	state.selectedThreadID = "t1"
	state.newThreadProvider = "claude"
	state.mu.Unlock()
	return fake, state
}

// threadEffortOf 读取 chatState 内存里指定会话的推理强度。
func threadEffortOf(state *chatState, threadID string) string {
	state.mu.Lock()
	defer state.mu.Unlock()
	for _, th := range state.threads {
		if th.ID == threadID {
			return th.Effort
		}
	}
	return ""
}

// awaitEffortBranchSettled 等到切换模型的后台分支走完强度判定，供“不应产生 ComposerPrefs 写入”的断言使用。
// 时序栅栏用快速模式重置：它在强度判定之后执行；同时确认没有在途的 ComposerPrefs 保存，
// 避免断言先于异步写入落地而假通过。
func awaitEffortBranchSettled(t *testing.T, fake *FakeClient, state *chatState) {
	t.Helper()
	waitUntil(t, "切换模型后台分支走完强度判定", func() bool {
		state.mu.Lock()
		saving := state.composerPrefsSaving["t1"]
		state.mu.Unlock()
		return fake.FastModeCallCount() > 0 && !saving
	})
}

// 用例 1：用户在设置里为模型配置的默认强度优先于模型自身默认档（medium），最终生效 high。
func TestComposerDefaultEffortUserPreferenceWins(t *testing.T) {
	fake, state := defaultEffortState(t, "low", false)
	m := ModelInfo{ID: "model-x", Name: "Model X", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}
	pin := modelPreferencePin("claude", m.ID)
	high := "high"
	if err := fake.UpdateModelPreference(context.Background(), pin, ModelPreference{Effort: &high}); err != nil {
		t.Fatalf("预置模型偏好失败: %v", err)
	}
	// 裸 chatState 没有设置页配置副本，defaultEffortForModel 会回退到偏好缓存，先把它预热好。
	state.loadModelPreference(pin)
	waitUntil(t, "模型偏好缓存加载完成", func() bool {
		return !state.modelPreferenceBusy(pin) && state.modelPreference(pin).Effort != nil
	})

	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{m}}
	state.applyModelPickerPick("t1", ModelPickerSideLead, "claude", prov, m)

	waitUntil(t, "切换模型后下发用户偏好的强度 high", func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.Effort == "high"
	})
	tid, prefs, ok := fake.LastComposerPrefsCall()
	if !ok {
		t.Fatalf("切换模型后没有下发 ComposerPrefs，期望 Effort=high（用户偏好）")
	}
	if tid != "t1" || prefs.Effort != "high" {
		t.Fatalf("用户偏好应优先于模型默认档: 期望 threadID=t1 Effort=high（用户偏好）, 实际 threadID=%q Effort=%q（模型默认档是 medium）", tid, prefs.Effort)
	}
	if got := threadEffortOf(state, "t1"); got != "high" {
		t.Fatalf("会话内存强度应同步为 high（用户偏好）, 实际 %q", got)
	}
}

// 用例 2：模型没有任何用户偏好时，套用模型自身的默认档 medium。
func TestComposerDefaultEffortFallsBackToModelDefault(t *testing.T) {
	fake, state := defaultEffortState(t, "", false)
	m := ModelInfo{ID: "model-y", Name: "Model Y", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}
	pin := modelPreferencePin("claude", m.ID)
	if pref, err := fake.ModelPreferences(context.Background(), pin); err != nil || pref.Effort != nil {
		t.Fatalf("用例前提被破坏: 模型 %s 不应有用户偏好, pref=%+v err=%v", m.ID, pref, err)
	}

	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{m}}
	state.applyModelPickerPick("t1", ModelPickerSideLead, "claude", prov, m)

	waitUntil(t, "切换模型后套用模型默认档 medium", func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.Effort == "medium"
	})
	tid, prefs, ok := fake.LastComposerPrefsCall()
	if !ok {
		t.Fatalf("切换模型后没有下发 ComposerPrefs，期望套用模型默认档 Effort=medium")
	}
	if tid != "t1" || prefs.Effort != "medium" {
		t.Fatalf("无用户偏好时应套用模型默认档: 期望 threadID=t1 Effort=medium, 实际 threadID=%q Effort=%q", tid, prefs.Effort)
	}
	if got := threadEffortOf(state, "t1"); got != "medium" {
		t.Fatalf("会话内存强度应同步为 medium（模型默认档）, 实际 %q", got)
	}
}

// 用例 3：模型默认档落在后端白名单外（off）时必须丢弃，退回“旧强度仍被支持就保留”的原有逻辑。
func TestComposerDefaultEffortDropsDisallowedDefault(t *testing.T) {
	// 旧强度 low 在新模型档位里仍可用：off 被丢弃后应原样保留，且不产生任何 ComposerPrefs 写入。
	fake, state := defaultEffortState(t, "low", true)
	m := ModelInfo{ID: "model-z", Name: "Model Z", Efforts: []string{"off", "low", "medium"}, DefaultEffort: "off"}
	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{m}}

	var mu sync.Mutex
	var sentEfforts []string
	fake.composerPrefsHook = func(_ string, p ComposerPrefs) error {
		mu.Lock()
		sentEfforts = append(sentEfforts, p.Effort)
		mu.Unlock()
		return nil
	}

	state.applyModelPickerPick("t1", ModelPickerSideLead, "claude", prov, m)
	awaitEffortBranchSettled(t, fake, state)

	mu.Lock()
	sent := append([]string(nil), sentEfforts...)
	mu.Unlock()
	for _, e := range sent {
		if e == "off" {
			t.Fatalf("白名单外的默认档不得下发: 实际下发过 Effort=off, 全部下发值=%q", sent)
		}
	}
	if n := fake.ComposerPrefsCallCount(); n != 0 {
		t.Fatalf("白名单外的默认档应被丢弃并沿用旧强度，不应产生 ComposerPrefs 写入，实际调用 %d 次", n)
	}
	if eff := threadEffortOf(state, "t1"); eff != "low" {
		t.Fatalf("新模型仍支持旧强度 low，应原样保留，实际 %q", eff)
	}
}

// 用例 4：待创建会话路径一致，占位会话强度与创建选项强度必须同时写成模型默认档，否则创建时下发会漂移。
func TestComposerDefaultEffortPendingThreadSyncsOptions(t *testing.T) {
	testenv.TempHome(t)
	_, state := newPendingTestState(t)
	state.createThread("p1")
	m := ModelInfo{ID: "model-p", Name: "Model P", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}
	prov := ProviderInfo{ID: "codex", Name: "Codex", Available: true, Models: []ModelInfo{m}}

	state.applyModelPickerPick("", ModelPickerSideLead, "claude", prov, m)

	state.mu.Lock()
	pending := state.pendingThread
	state.mu.Unlock()
	if pending == nil {
		t.Fatalf("用例前提被破坏: 点击新建后应处于待创建会话状态")
	}
	if pending.Thread.Effort != "medium" {
		t.Fatalf("待创建会话的占位强度应为模型默认档 medium, 实际 %q", pending.Thread.Effort)
	}
	if pending.Options.Effort != "medium" {
		t.Fatalf("创建选项强度必须与占位强度一致（否则创建会话时下发会漂移）: Thread.Effort=%q, Options.Effort=%q", pending.Thread.Effort, pending.Options.Effort)
	}
}

// 用例 5：执行侧（Bees）切到有默认档的模型时，ComposerPrefs.BeesEffort 必须是该默认档。
func TestComposerDefaultEffortBeesSideAppliesDefault(t *testing.T) {
	fake, state := defaultEffortState(t, "", false)
	m := ModelInfo{ID: "model-b", Name: "Model B", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}
	prov := ProviderInfo{ID: "codex", Name: "Codex", Available: true, Models: []ModelInfo{m}}

	state.applyModelPickerPick("t1", ModelPickerSideBees, "claude", prov, m)

	waitUntil(t, "执行侧切模型后下发 BeesEffort=medium", func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.BeesEffort == "medium"
	})
	_, prefs, ok := fake.LastComposerPrefsCall()
	if !ok {
		t.Fatalf("执行侧切模型后没有下发 ComposerPrefs，期望 BeesEffort=medium")
	}
	if prefs.BeesProvider != "codex" || prefs.BeesModel != "model-b" || prefs.BeesEffort != "medium" {
		t.Fatalf("执行侧应写入新模型并套用其默认强度: 期望 BeesProvider=codex BeesModel=model-b BeesEffort=medium, 实际 BeesProvider=%q BeesModel=%q BeesEffort=%q", prefs.BeesProvider, prefs.BeesModel, prefs.BeesEffort)
	}
}

// 用例 6：回归护栏。目标模型既无用户偏好也无默认档时，旧强度仍在目标模型档位内就必须原样保留，
// 且不得产生新增的 ComposerPrefs 写入调用。
func TestComposerDefaultEffortKeepsOldEffortWithoutDefault(t *testing.T) {
	fake, state := defaultEffortState(t, "medium", true)
	m := ModelInfo{ID: "model-w", Name: "Model W", Efforts: []string{"low", "medium", "high"}}
	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{m}}
	pin := modelPreferencePin("claude", m.ID)
	if pref, err := fake.ModelPreferences(context.Background(), pin); err != nil || pref.Effort != nil {
		t.Fatalf("用例前提被破坏: 模型 %s 不应有用户偏好, pref=%+v err=%v", m.ID, pref, err)
	}

	before := fake.ComposerPrefsCallCount()
	state.applyModelPickerPick("t1", ModelPickerSideLead, "claude", prov, m)
	awaitEffortBranchSettled(t, fake, state)

	if after := fake.ComposerPrefsCallCount(); after != before {
		t.Fatalf("目标模型既无用户偏好也无默认档时不应新增 ComposerPrefs 写入: 切换前 %d 次, 切换后 %d 次", before, after)
	}
	if eff := threadEffortOf(state, "t1"); eff != "medium" {
		t.Fatalf("旧强度 medium 仍在目标模型档位内，应原样保留，实际 %q", eff)
	}
}

// settingsCopyEffort 读取设置页配置副本里该模型登记的推理强度，供“是否已镜像回副本”的断言使用。
// 副本由 st.cfgMu 保护（见 SettingsState 注释），这里按同一把锁读取。
func settingsCopyEffort(st *SettingsState, pin ModelPin) (string, bool) {
	if st == nil {
		return "", false
	}
	st.cfgMu.Lock()
	defer st.cfgMu.Unlock()
	pref := st.cfg.Models.ModelPreferenceFor(pin)
	if pref.Effort == nil {
		return "", false
	}
	return *pref.Effort, true
}

// 用例 7：设置页配置副本优先于模型默认档，且这条分支不依赖后端偏好与偏好缓存。
// 前面 6 个用例都是裸 chatState（getChatSettings 为 nil），走的全是偏好缓存分支；
// 这里守的是 defaultEffortForModel 的第一优先级：设置页刚配好的档位必须立刻生效，
// 不能被模型默认档盖掉，也不该依赖后端是否已返回该模型的偏好。
func TestComposerDefaultEffortSettingsCopyWins(t *testing.T) {
	fake, state := defaultEffortState(t, "low", false)
	m := ModelInfo{ID: "model-x", Name: "Model X", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}
	pin := modelPreferencePin("claude", m.ID)

	// 只写设置页配置副本，后端不预置任何偏好：最终生效 high 才说明取的是副本而不是缓存或后端。
	st := NewSettingsState(settingsTestDir(t), nil, state)
	high := "high"
	st.cfg.Models.SetModelPreference(pin, ModelPreference{Effort: &high})
	setChatSettings(state, st)

	if pref, err := fake.ModelPreferences(context.Background(), pin); err != nil || pref.Effort != nil {
		t.Fatalf("用例前提被破坏: 模型 %s 不应有后端偏好, pref=%+v err=%v", m.ID, pref, err)
	}
	if pref := state.modelPreference(pin); pref.Effort != nil {
		t.Fatalf("用例前提被破坏: 偏好缓存应为空, 实际 %+v", pref)
	}

	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{m}}
	state.applyModelPickerPick("t1", ModelPickerSideLead, "claude", prov, m)

	waitUntil(t, "切换模型后下发设置页副本的强度 high", func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.Effort == "high"
	})
	tid, prefs, ok := fake.LastComposerPrefsCall()
	if !ok || tid != "t1" || prefs.Effort != "high" {
		t.Fatalf("设置页副本的 high 应优先于模型默认档 medium: 期望 threadID=t1 Effort=high（设置页副本）, 实际 threadID=%q Effort=%q ok=%v", tid, prefs.Effort, ok)
	}
	if got := threadEffortOf(state, "t1"); got != "high" {
		t.Fatalf("会话内存强度应同步为设置页副本的 high（模型默认档是 medium）, 实际 %q", got)
	}
	// 这条分支只读副本与偏好缓存，切模型本身不应产生偏好写入。
	if pref, err := fake.ModelPreferences(context.Background(), pin); err != nil || pref.Effort != nil {
		t.Fatalf("读设置页副本不应写出后端偏好: pref=%+v err=%v", pref, err)
	}
}

// 用例 8：输入框滑杆改强度必须镜像回设置页配置副本，切走再切回拿到的是最新值而不是副本里的旧档。
// 守两条契约：(1) 偏好写入（setModelEffortPreference → writeModelPreference）成功后把结果镜像回
// st.cfg.Models，否则设置页仍显示旧档；(2) 切回模型时 defaultEffortForModel 读到的是镜像后的 low，
// 而不是设置页里那份过期的 high。
func TestComposerDefaultEffortFollowsComposerSliderEdit(t *testing.T) {
	fake, state := defaultEffortState(t, "medium", false)
	m := ModelInfo{ID: "model-x", Name: "Model X", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}
	pin := modelPreferencePin("claude", m.ID)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	high := "high"
	st.cfg.Models.SetModelPreference(pin, ModelPreference{Effort: &high})
	setChatSettings(state, st)

	// 用户在输入框滑杆上把强度从 high 拖到 low：这条路径只写后端偏好，不直接碰设置页副本。
	state.setModelEffortPreference(pin, "low")
	// 等写入落定且镜像生效：写入方先关 done 再镜像，只看 busy 会撞进两步之间的窗口。
	waitUntil(t, "滑杆改强度后偏好写入落定并镜像回设置页副本 low", func() bool {
		if state.modelPreferenceBusy(pin) {
			return false
		}
		effort, ok := settingsCopyEffort(st, pin)
		return ok && effort == "low"
	})
	if effort, ok := settingsCopyEffort(st, pin); !ok || effort != "low" {
		t.Fatalf("滑杆改强度应镜像回设置页副本: 期望副本 Effort=low（设置页原值 high 应被覆盖）, 实际 ok=%v Effort=%q", ok, effort)
	}

	// 先切到一个既无默认档也无偏好的模型（旧强度 medium 仍被支持，不产生下发），再切回来。
	mz := ModelInfo{ID: "model-z", Name: "Model Z", Efforts: []string{"low", "medium", "high"}}
	provZ := ProviderInfo{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{mz}}
	state.applyModelPickerPick("t1", ModelPickerSideLead, "claude", provZ, mz)
	waitUntil(t, "切到 model-z 后会话模型落定", func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		for _, th := range state.threads {
			if th.ID == "t1" {
				return th.Model == "model-z"
			}
		}
		return false
	})

	before := fake.ComposerPrefsCallCount()
	prov := ProviderInfo{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{m}}
	state.applyModelPickerPick("t1", ModelPickerSideLead, "claude", prov, m)
	waitUntil(t, "切回目标模型后按滑杆最新值下发 low（不是副本里过期的 high）", func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return fake.ComposerPrefsCallCount() > before && ok && tid == "t1" && prefs.Effort == "low"
	})
	tid, prefs, ok := fake.LastComposerPrefsCall()
	if !ok || tid != "t1" || prefs.Effort != "low" {
		t.Fatalf("切回后应下发滑杆最新值 low（设置页副本里的 high 已过期）: 期望 threadID=t1 Effort=low, 实际 threadID=%q Effort=%q ok=%v", tid, prefs.Effort, ok)
	}
	if got := threadEffortOf(state, "t1"); got != "low" {
		t.Fatalf("会话内存强度应为滑杆最新值 low（不是过期的 high）, 实际 %q", got)
	}
}
