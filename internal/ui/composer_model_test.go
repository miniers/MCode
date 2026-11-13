package mcodeui

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// findTextRect 返回首个包含 text 的文本所在矩形；找不到时返回 false。
func findTextRect(t *testing.T, tt *ui.Tester, text string) (ui.Rect, bool) {
	t.Helper()
	for _, needle := range []string{text} {
		if r, ok := tt.Find(needle); ok {
			return r, true
		}
	}
	return ui.Rect{}, false
}

// tapText 点击文案所在位置（分段栏等非按钮标签），找不到时记为失败。
func tapText(t *testing.T, tt *ui.Tester, text string) {
	t.Helper()
	r, ok := findTextRect(t, tt, text)
	if !ok {
		t.Fatalf("text %q not found", text)
	}
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
}

// tapTextErr 点击文案对应的控件：先按无障碍名点击，找不到时再点击文案所在位置，把错误交回调用方。
func tapTextErr(t *testing.T, tt *ui.Tester, text string) error {
	t.Helper()
	err := tt.Click(text)
	if err == nil {
		return nil
	}
	if r, ok := findTextRect(t, tt, text); ok {
		tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
		return nil
	}
	return err
}

// clickEffortSlider 点击推理强度滑轨上 frac（0~1）处：滑轨左右各留半个滑块宽度（40）的内边距，
// 取值范围在其间线性映射，frac=0 为最低档、frac=1 为最高档。
func clickEffortSlider(t *testing.T, tt *ui.Tester, frac float32) {
	t.Helper()
	r, ok := tt.Find("推理强度")
	if !ok {
		t.Fatalf("effort slider not found, texts: %v", tt.Texts())
	}
	tt.ClickAt(r.X+40+(r.W-80)*frac, r.Y+r.H/2)
}

// waitText 轮询等待界面出现指定文案，超时返回 false。
func waitText(t *testing.T, tt *ui.Tester, text string) bool {
	t.Helper()
	ok := false
	waitFor(t, tt, func() bool {
		ok = tt.HasText(text)
		return ok
	})
	return ok
}

// 场景 1：初始渲染显示当前模型名。
func TestComposerModel_ShowCurrentModelName(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	providers := []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: false},
				{ID: "claude-3-5-haiku", Name: "Claude 3.5 Haiku", IsDefault: false},
			},
		},
	}
	fake.SetProviders(providers)
	now := time.Now()
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		Provider:  "claude",
		Model:     "claude-3-7-sonnet",
		UpdatedAt: now,
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 验证显示当前模型名 "Claude 3.7 Sonnet"
	waitFor(t, tt, func() bool {
		return tt.HasText("Claude 3.7 Sonnet")
	})
}

// 场景 2：新会话切换到 codex 的某个模型后，假 Client 收到 (threadID, "codex", modelID)。
func TestComposerModel_SwitchToOtherAgent(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	t.Cleanup(func() {
		composerModelPickerState.MenuOpen = false
	})
	fake := NewFakeClient()
	providers := []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: true},
			},
		},
		{
			ID:        "codex",
			Name:      "Codex",
			Available: true,
			Models: []ModelInfo{
				{ID: "o3-mini", Name: "o3-mini", IsDefault: false},
			},
		},
	}
	fake.SetProviders(providers)
	now := time.Now()
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		Provider:  "claude",
		Model:     "",
		UpdatedAt: now,
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 未指定模型时显示解析出的第一个模型名
	waitFor(t, tt, func() bool {
		return tt.HasText("Claude 3.7 Sonnet")
	})

	// 点击胶囊展开 Popover
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("选择其他模型")
	})

	// 点击卡片大标题进入二级列表
	if err := tapTextErr(t, tt, "选择其他模型"); err != nil {
		t.Fatalf("click model title failed: %v", err)
	}

	// 展开后能看到 "o3-mini"
	waitFor(t, tt, func() bool {
		return tt.HasText("o3-mini")
	})

	// 点击 "o3-mini"
	if err := tt.Click("o3-mini"); err != nil {
		t.Fatalf("click o3-mini failed: %v", err)
	}

	// 假 Client 收到 (threadID, "codex", "o3-mini")
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.SetAgentCalls) == 0 {
			return false
		}
		last := fake.SetAgentCalls[len(fake.SetAgentCalls)-1]
		return last.ThreadID == "t1" && last.Provider == "codex" && last.Model == "o3-mini"
	})

	// 界面更新显示 "o3-mini"
	waitFor(t, tt, func() bool {
		return tt.HasText("o3-mini")
	})
}

// 场景 3：已有消息的会话里，其他 Agent 的模型点不了，不会产生调用。
func TestComposerModel_HasMessagesCannotSwitchAgent(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	t.Cleanup(func() {
		composerModelPickerState.MenuOpen = false
	})
	fake := NewFakeClient()
	providers := []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: true},
			},
		},
		{
			ID:        "codex",
			Name:      "Codex",
			Available: true,
			Models: []ModelInfo{
				{ID: "o3-mini", Name: "o3-mini", IsDefault: false},
			},
		},
	}
	fake.SetProviders(providers)
	now := time.Now()
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		Provider:  "claude",
		Model:     "",
		UpdatedAt: now,
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})
	fake.SetMessages("t1", []Message{
		{ID: "m1", Role: "user", Text: "你好已开始", TurnIndex: 1},
	})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})
	waitFor(t, tt, func() bool {
		return tt.HasText("你好已开始")
	})

	// 点击胶囊展开 Popover
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("选择其他模型")
	})

	// 点击卡片大标题进入列表
	if err := tapTextErr(t, tt, "选择其他模型"); err != nil {
		t.Fatalf("click model title failed: %v", err)
	}

	// 出现禁用提示 "下回合生效"
	waitFor(t, tt, func() bool {
		return tt.HasText("下回合生效")
	})

	// 尝试点击其他 Agent 的模型 "o3-mini"
	_ = tt.Click("o3-mini")
	tt.Frame()

	// 验证假 Client 没有收到对 codex 的调用
	fake.mu.Lock()
	for _, call := range fake.SetAgentCalls {
		if call.Provider == "codex" || call.Model == "o3-mini" {
			fake.mu.Unlock()
			t.Fatalf("unexpected call for codex on thread with messages: %+v", call)
		}
	}
	fake.mu.Unlock()
}

// 场景 4：同一个 Agent 内换模型时，收到的 provider 为空。
func TestComposerModel_SwitchWithinSameAgent(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	providers := []ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: false},
				{ID: "claude-3-5-haiku", Name: "Claude 3.5 Haiku", IsDefault: false},
			},
		},
	}
	fake.SetProviders(providers)
	now := time.Now()
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		Provider:  "claude",
		Model:     "claude-3-7-sonnet",
		UpdatedAt: now,
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("Claude 3.7 Sonnet")
	})

	// 点击模型胶囊展开 Popover（按触发按钮的无障碍名“选择模型”）
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}

	// 点击卡片大标题进入列表（无 pin 时一级页即为全量模型列表）
	if err := tapTextErr(t, tt, "选择其他模型"); err != nil {
		t.Fatalf("click model title failed: %v", err)
	}

	// 展开后看到 "Claude 3.5 Haiku"
	waitFor(t, tt, func() bool {
		return tt.HasText("Claude 3.5 Haiku")
	})

	// 点击 "Claude 3.5 Haiku"
	if err := tt.Click("Claude 3.5 Haiku"); err != nil {
		t.Fatalf("click Claude 3.5 Haiku failed: %v", err)
	}

	// 验证假 Client 收到的 provider 为空字符串
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		if len(fake.SetAgentCalls) == 0 {
			return false
		}
		last := fake.SetAgentCalls[len(fake.SetAgentCalls)-1]
		return last.ThreadID == "t1" && last.Provider == "" && last.Model == "claude-3-5-haiku"
	})

	// 界面胶囊更新显示 "Claude 3.5 Haiku"
	waitFor(t, tt, func() bool {
		return tt.HasText("Claude 3.5 Haiku")
	})
}

// 场景 5：没有选中会话时不显示模型胶囊。
func TestComposerModel_HiddenWhenNoThread(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	fake := &FakeClient{
		projects:  []Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		threads:   make(map[string][]Thread),
		messages:  make(map[string][]Message),
		approvals: make(map[string][]Approval),
		watchers:  make(map[string][]*watchEntry),
		usage:     make(map[string]ThreadUsage),
	}
	tt := ui.NewTester(Root(fake), 1000, 700)
	tt.Frame()

	if tt.HasText("默认") || tt.HasText("Claude 3.7 Sonnet") {
		t.Errorf("model chip should not be displayed when no thread is selected, texts: %q", tt.Texts())
	}
}

// composerModelPrefsFixture 构造带 claude/codex 两个 Agent、且已设置 Bees 偏好的会话。
func composerModelPrefsFixture(t *testing.T, withMessages bool) (*FakeClient, *ui.Tester) {
	t.Helper()
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	t.Cleanup(func() { composerModelPickerState.MenuOpen = false })
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Enabled: true})
	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: true, Efforts: []string{"low", "medium", "high"}},
			},
		},
		{
			ID:        "codex",
			Name:      "Codex",
			Available: true,
			Models: []ModelInfo{
				{ID: "o3-mini", Name: "o3-mini", IsDefault: false, Efforts: []string{"low", "medium", "high"}},
			},
		},
	})
	th := Thread{
		ID:           "t1",
		ProjectID:    "p1",
		Title:        "测试会话1",
		Provider:     "claude",
		BeesProvider: "claude",
		BeesModel:    "claude-3-7-sonnet",
		BeesEffort:   "medium",
		UpdatedAt:    time.Now(),
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})
	if withMessages {
		fake.SetMessages("t1", []Message{
			{ID: "m1", Role: "user", Text: "你好已开始", TurnIndex: 1},
		})
	}
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1") && tt.HasText("Claude 3.7 Sonnet")
	})
	if withMessages {
		waitFor(t, tt, func() bool {
			return tt.HasText("你好已开始")
		})
	}
	return fake, tt
}

// 场景 6：Lead 段选择 effort=high，写入 prefs 且保留 Bees 字段。
func TestComposerModel_LeadEffort(t *testing.T) {
	fake, tt := composerModelPrefsFixture(t, false)

	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("主代理 · Medium") && tt.HasText("子代理 · Medium")
	})
	// 滑轨拖到最右端选中最高档 High。
	clickEffortSlider(t, tt, 1)

	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs == ComposerPrefs{
			InteractionMode: "build",
			Effort:          "high",
			BeesProvider:    "claude",
			BeesModel:       "claude-3-7-sonnet",
			BeesEffort:      "medium",
		}
	})

	// 点击重置按钮清空推理强度
	if err := tapTextErr(t, tt, "重置强度与快速模式"); err != nil {
		t.Fatalf("click reset failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, prefs, ok := fake.LastComposerPrefsCall()
		return ok && prefs.Effort == "" && fake.ComposerPrefsCallCount() == 2
	})
}

// 场景 7：Bees 段选模型只写 BeesProvider/BeesModel，不调用 SetThreadAgent，且不受已有消息限制。
func TestComposerModel_BeesModel(t *testing.T) {
	fake, tt := composerModelPrefsFixture(t, true)

	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}
	if !waitText(t, tt, "子代理 · Medium") {
		t.Fatalf("bees tab not rendered")
	}
	// 点击顶部“执行”分段进入执行侧卡片
	tapText(t, tt, "子代理 · Medium")
	// 点击卡片大标题（“Medium”的标题按钮）进入二级列表
	tapText(t, tt, "选择其他模型")
	waitFor(t, tt, func() bool {
		return tt.HasText("跟随主导") && tt.HasText("o3-mini")
	})
	if tt.HasText("已开始的会话不能切换 Agent") || tt.HasText("下回合生效") {
		t.Fatalf("bees section should not restrict agent switching")
	}
	if err := tt.Click("o3-mini"); err != nil {
		t.Fatalf("click o3-mini failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.BeesProvider == "codex" && prefs.BeesModel == "o3-mini" &&
			prefs.BeesEffort == "medium" && prefs.Effort == ""
	})
	fake.mu.Lock()
	agentCalls := len(fake.SetAgentCalls)
	fake.mu.Unlock()
	if agentCalls != 0 {
		t.Fatalf("bees model pick should not call SetThreadAgent, got %d calls", agentCalls)
	}

	// 触发按钮仍显示 Lead 模型
	waitFor(t, tt, func() bool {
		return tt.HasText("Claude 3.7 Sonnet")
	})
}

// 场景 8：Bees 段 effort 选择写入 BeesEffort，保留 Lead effort 与 Bees 模型。
func TestComposerModel_BeesEffort(t *testing.T) {
	fake, tt := composerModelPrefsFixture(t, false)

	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("子代理 · Medium")
	})
	if err := tapTextErr(t, tt, "子代理 · Medium"); err != nil {
		t.Fatalf("click Bees tab failed: %v", err)
	}
	tt.Frame()
	// 滑轨拖到最左端选中最低档 Low。
	clickEffortSlider(t, tt, 0)

	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs == ComposerPrefs{
			InteractionMode: "build",
			BeesProvider:    "claude",
			BeesModel:       "claude-3-7-sonnet",
			BeesEffort:      "low",
		}
	})
}

// 场景 9：纯函数 modelCapsuleLabel 测试：拼接模型名与首字母大写的推理强度，为空时不追加。
func TestModelCapsuleLabel(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	cases := []struct {
		name     string
		effort   string
		expected string
	}{
		{"Claude 3.7 Sonnet", "", "Claude 3.7 Sonnet"},
		{"Claude 3.7 Sonnet", "   ", "Claude 3.7 Sonnet"},
		{"GPT-5", "high", "GPT-5 High"},
		{"GPT-5", "HIGH", "GPT-5 High"},
		{"GPT-5", "High", "GPT-5 High"},
		{"Opus 5.5", "medium", "Opus 5.5 Medium"},
		{"Claude 3.5 Haiku", "low", "Claude 3.5 Haiku Low"},
		{"Opus 5.5", "xhigh", "Opus 5.5 Extra High"},
		{"默认", "high", "默认 High"},
		{"", "high", "High"},
		{"", "", ""},
	}
	for _, tc := range cases {
		got := modelCapsuleLabel(tc.name, tc.effort)
		if got != tc.expected {
			t.Errorf("modelCapsuleLabel(%q, %q) = %q, want %q", tc.name, tc.effort, got, tc.expected)
		}
	}
}

// 场景 10：上下文用量比例与百分比计算纯函数测试。
func TestContextUsageCalculation(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	fracCases := []struct {
		used     int64
		limit    int64
		wantFrac float64
		wantPct  float64
	}{
		{0, 1000, 0.0, 0.0},
		{500, 1000, 0.5, 50.0},
		{1000, 1000, 1.0, 100.0},
		{1500, 1000, 1.0, 100.0}, // 超过上限截断为 1.0
		{500, 0, 0.0, 0.0},       // 上限为 0
		{500, -100, 0.0, 0.0},    // 上限为负
		{-50, 1000, 0.0, 0.0},    // 已用为负
	}
	for _, tc := range fracCases {
		gotFrac := contextUsageFraction(tc.used, tc.limit)
		if gotFrac != tc.wantFrac {
			t.Errorf("contextUsageFraction(%d, %d) = %v, want %v", tc.used, tc.limit, gotFrac, tc.wantFrac)
		}
		gotPct := contextUsagePercent(tc.used, tc.limit)
		if gotPct != tc.wantPct {
			t.Errorf("contextUsagePercent(%d, %d) = %v, want %v", tc.used, tc.limit, gotPct, tc.wantPct)
		}
	}

	tooltip := contextUsageTooltip(500, 2000)
	if tooltip != "已用 500 / 上限 2.0k tokens" {
		t.Errorf("contextUsageTooltip(500, 2000) = %q, want %q", tooltip, "已用 500 / 上限 2.0k tokens")
	}
}

// 场景 11：重置只清空强度与快速模式，不改变已选模型。
func TestComposerModel_ResetToDefault(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: true},
				{ID: "claude-3-5-haiku", Name: "Claude 3.5 Haiku", IsDefault: false},
			},
		},
	})
	now := time.Now()
	th := Thread{
		ID:           "t1",
		ProjectID:    "p1",
		Title:        "测试会话1",
		Provider:     "claude",
		Model:        "claude-3-5-haiku",
		Effort:       "high",
		BeesProvider: "claude",
		BeesModel:    "claude-3-5-haiku",
		BeesEffort:   "medium",
		UpdatedAt:    now,
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 胶囊只显示模型短名，推理强度改由信号格呈现，不再拼在文字里。
	waitFor(t, tt, func() bool {
		return tt.HasText("Claude 3.5 Haiku") && !tt.HasText("Claude 3.5 Haiku High")
	})

	// 点击胶囊展开 Popover
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}

	// 展开后看到重置按钮 "重置强度与快速模式"
	waitFor(t, tt, func() bool {
		return tt.HasText("重置强度与快速模式")
	})

	// 点击重置按钮
	tapText(t, tt, "重置强度与快速模式")

	// 偏好被清空
	waitFor(t, tt, func() bool {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		return ok && tid == "t1" && prefs.Effort == ""
	})

	// fake client 收到空的 ModelPreference
	waitFor(t, tt, func() bool {
		pref, err := fake.ModelPreferences(context.Background(), ModelPin{Provider: "claude", ModelID: "claude-3-5-haiku"})
		return err == nil && pref.Effort == nil && !pref.FastMode
	})
}

// 覆盖：Swarm 开启时卡片顶部渲染出“主导 · <档位>”和“执行 · <档位>”两段标签。
func TestComposerModel_SwarmSideTabs(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	t.Cleanup(func() {
		composerModelPickerState.MenuOpen = false
	})
	fake := NewFakeClient()
	fake.SetSwarmState("t1", SwarmState{LeadThreadID: "t1", Available: true, Enabled: true})
	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: false, Efforts: []string{"low", "medium", "high"}},
			},
		},
	})
	th := Thread{
		ID:           "t1",
		ProjectID:    "p1",
		Title:        "测试会话1",
		Provider:     "claude",
		Model:        "claude-3-7-sonnet",
		Effort:       "medium",
		BeesProvider: "claude",
		BeesModel:    "claude-3-7-sonnet",
		BeesEffort:   "high",
		UpdatedAt:    time.Now(),
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 点击胶囊展开 Popover
	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}

	// 验证渲染出两段标签：档位用英文名，与各侧当前档位一致。
	waitFor(t, tt, func() bool {
		return tt.HasText("主代理 · Medium") && tt.HasText("子代理 · High")
	})
}

// 覆盖：Efforts 为空时出现“该模型未提供可调的推理强度”且不渲染滑条。
func TestComposerModel_EffortsEmpty(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	t.Cleanup(func() {
		composerModelPickerState.MenuOpen = false
	})
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Claude",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet", IsDefault: false, Efforts: nil},
			},
		},
	})
	th := Thread{
		ID:        "t1",
		ProjectID: "p1",
		Title:     "测试会话1",
		Provider:  "claude",
		Model:     "claude-3-7-sonnet",
		UpdatedAt: time.Now(),
	}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{"p1": {th}})

	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	if err := tt.Click("选择模型"); err != nil {
		t.Fatalf("click model trigger failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("该模型未提供可调的推理强度")
	})

	// 确认不渲染滑条刻度
	if tt.HasText("最低") || tt.HasText("超高") {
		t.Fatalf("unexpected slider ticks rendered when efforts is empty")
	}
}

func TestComposerModel_FastModeClickPersistsDefaultModel(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	composerModelPickerState.MenuOpen = false
	t.Cleanup(func() { composerModelPickerState.MenuOpen = false })
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{{
		ID: "claude", Name: "Claude", Available: true,
		Models: []ModelInfo{{
			ID: "", Name: "默认", IsDefault: true, SupportsFast: true,
			Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium",
		}},
	}})
	th := Thread{ID: "t1", ProjectID: "p1", Title: "快速模式测试", Provider: "claude", UpdatedAt: time.Now()}
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "测试", Path: t.TempDir()}}, map[string][]Thread{"p1": {th}})
	tt := ui.NewTester(Root(fake), 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("选择模型") })
	if err := tt.Click("选择模型"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("快速模式") })
	if err := tapTextErr(t, tt, "快速模式"); err != nil {
		t.Fatal(err)
	}
	pin := ModelPin{Provider: "claude", ModelID: "default"}
	// 主导侧快速模式写会话（SetThreadFastMode），不再写模型偏好。
	waitFor(t, tt, func() bool {
		call, ok := fake.LastFastModeCall()
		return ok && call.ThreadID == "t1" && call.FastMode && call.SpeedTier == ""
	})
	if err := tapTextErr(t, tt, "重置强度与快速模式"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		pref, _ := fake.ModelPreferences(context.Background(), pin)
		return !pref.FastMode && pref.Effort == nil
	})
}

func TestComposerModel_BeesOffTabEnablesSwarm(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	fake := NewFakeClient()
	s := newChatState(fake)
	s.selectedThreadID = "t1"
	th := Thread{ID: "t1"}
	tt := ui.NewTester(func(c *ui.Context) {
		renderSwarmSideTabs(c, s, &th, false)
	}, 330, 100)
	tt.Frame()
	if err := tt.Click("子代理 · 关闭"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		state, err := fake.Swarm(context.Background(), "t1")
		return err == nil && state.Enabled
	})
	if composerModelPickerState.Tab != "bees" {
		t.Fatalf("selected tab = %q, want bees", composerModelPickerState.Tab)
	}
}

// 覆盖：toggleModelFastMode 之后 fake client 收到 UpdateModelPreference 且 FastMode 为 true。
func TestComposerModel_ToggleModelFastMode(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	fake := NewFakeClient()
	s := newChatState(fake)
	pin := ModelPin{Provider: "claude", ModelID: "claude-3-7-sonnet"}

	// 初始状态下 FastMode 为 false，调用 toggleModelFastMode 翻转为 true
	s.toggleModelFastMode(pin)

	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Second)
	var pref ModelPreference
	var found bool
	for time.Now().Before(deadline) {
		p, err := fake.ModelPreferences(ctx, pin)
		if err == nil && p.FastMode {
			pref = p
			found = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatalf("expected fake client to receive FastMode=true, got: %+v", pref)
	}

	// 验证本地内存缓存亦同步为 FastMode=true
	awaitPreferenceWrites(t, s, pin)
	cached := s.modelPreference(pin)
	if !cached.FastMode {
		t.Fatalf("expected cached FastMode=true, got: %+v", cached)
	}
}

// 覆盖：resetModelPreference 之后收到空的 ModelPreference，并且会话 Effort 被清空。
func TestComposerModel_ResetModelPreference(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	fake := NewFakeClient()
	now := time.Now()
	fake.SetProjectsAndThreads([]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}, map[string][]Thread{
		"p1": {
			{
				ID:        "t1",
				ProjectID: "p1",
				Title:     "测试会话1",
				Provider:  "claude",
				Effort:    "high",
				UpdatedAt: now,
			},
		},
	})
	pin := ModelPin{Provider: "claude", ModelID: "claude-3-7-sonnet"}
	// 先写入一个非空的模型偏好
	eff := "high"
	_ = fake.UpdateModelPreference(context.Background(), pin, ModelPreference{Effort: &eff, FastMode: true})

	s := newChatState(fake)
	s.threads = []Thread{
		{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "测试会话1",
			Provider:  "claude",
			Effort:    "high",
			UpdatedAt: now,
		},
	}

	// 执行 resetModelPreference
	s.resetModelPreference("t1", "lead", pin)

	// 验证会话 Effort 被清空
	deadline := time.Now().Add(2 * time.Second)
	var clearedPrefs bool
	for time.Now().Before(deadline) {
		tid, prefs, ok := fake.LastComposerPrefsCall()
		if ok && tid == "t1" && prefs.Effort == "" {
			clearedPrefs = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !clearedPrefs {
		t.Fatalf("expected thread Effort to be cleared")
	}

	// 验证 fake client 收到空的 ModelPreference
	var clearedPref bool
	for time.Now().Before(deadline) {
		p, err := fake.ModelPreferences(context.Background(), pin)
		if err == nil && p.Effort == nil && !p.FastMode {
			clearedPref = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !clearedPref {
		t.Fatalf("expected empty ModelPreference on fake client")
	}
}

// 覆盖：setModelEffortPreference 之后持久化的 Effort 正确。
func TestComposerModel_SetModelEffortPreference(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(lastSelectionWriteWG.Wait)
	fake := NewFakeClient()
	s := newChatState(fake)
	pin := ModelPin{Provider: "claude", ModelID: "claude-3-7-sonnet"}

	// 设置模型推理强度为 "high"
	s.setModelEffortPreference(pin, "high")

	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Second)
	var ok bool
	for time.Now().Before(deadline) {
		pref, err := fake.ModelPreferences(ctx, pin)
		if err == nil && pref.Effort != nil && *pref.Effort == "high" {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("expected fake client to persist Effort='high'")
	}

	// 再次调用覆盖为 "low"，并保留已有状态
	s.setModelEffortPreference(pin, "low")
	ok = false
	for time.Now().Before(deadline) {
		pref, err := fake.ModelPreferences(ctx, pin)
		if err == nil && pref.Effort != nil && *pref.Effort == "low" {
			ok = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("expected fake client to persist Effort='low'")
	}
}

func TestCurrentThreadContextTokensUsesLatestTurnUsage(t *testing.T) {
	s := &chatState{providers: []ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 1_000_000}, {ID: "haiku"}},
	}}}
	// 上下文占用只看最新一轮；全会话累计计费不能当作当前占用。
	s.threadUsage = ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 400, Output: 40}, 1: {Input: 300, Output: 200}},
		Total: TokenUsage{Input: 700, Output: 240},
	}

	used, limit, ok := currentThreadContextTokens(s, &Thread{Provider: "claude"})
	if !ok || used != 500 || limit != 1_000_000 {
		t.Errorf("default model: used=%d limit=%d ok=%v", used, limit, ok)
	}
	// agy 的轮次用量是整轮多步累计（计费口径），上下文占用要用单次请求的 ContextTokens。
	s.threadUsage.Turns[1] = TokenUsage{Input: 1_900_000, Output: 948_000, ContextTokens: 180_000}
	if used, limit, ok := currentThreadContextTokens(s, &Thread{Provider: "claude"}); !ok || used != 180_000 || limit != 1_000_000 {
		t.Errorf("context tokens must win over cumulative billing: used=%d limit=%d ok=%v", used, limit, ok)
	}
	s.threadUsage.Turns[1] = TokenUsage{Input: 300, Output: 200}
	// 目录里没有该模型的上限时不再用固定默认预算顶分母，直接报“无数据”。
	if used, limit, ok := currentThreadContextTokens(s, &Thread{Provider: "claude", Model: "haiku"}); ok || limit != 0 || used != 500 {
		t.Errorf("model without a limit: used=%d limit=%d ok=%v, want ok=false", used, limit, ok)
	}
	if used, limit, ok := currentThreadContextTokens(s, &Thread{Provider: "unknown"}); ok || limit != 0 || used != 500 {
		t.Errorf("unknown provider: used=%d limit=%d ok=%v, want ok=false", used, limit, ok)
	}
	if _, _, ok := currentThreadContextTokens(s, nil); ok {
		t.Error("nil thread must report ok=false")
	}
	// 没有任何轮次数据时视为未知用量。
	s.threadUsage = ThreadUsage{Turns: map[int]TokenUsage{}}
	if _, _, ok := currentThreadContextTokens(s, &Thread{Provider: "claude"}); ok {
		t.Error("no turn usage must report ok=false")
	}
}

func TestCurrentThreadContextTokensPrefersReportedContextWindow(t *testing.T) {
	s := &chatState{providers: []ProviderInfo{{
		ID:     "claude",
		Models: []ModelInfo{{ID: "", IsDefault: true, ContextLimit: 200_000}},
	}}}
	s.threadUsage = ThreadUsage{
		Turns: map[int]TokenUsage{0: {Input: 300, Output: 200, ContextWindow: 1_000_000}},
		Total: TokenUsage{Input: 300, Output: 200},
	}
	// CLI 上报的实测窗口优先于目录里的静态上限（1M 变体在目录里可能仍写着 200K）。
	if used, limit, ok := currentThreadContextTokens(s, &Thread{Provider: "claude"}); !ok || used != 500 || limit != 1_000_000 {
		t.Errorf("reported window: used=%d limit=%d ok=%v", used, limit, ok)
	}
	// 没有上报（0）时沿用目录上限，行为不变。
	s.threadUsage.Turns[0] = TokenUsage{Input: 300, Output: 200}
	if used, limit, ok := currentThreadContextTokens(s, &Thread{Provider: "claude"}); !ok || used != 500 || limit != 200_000 {
		t.Errorf("fallback to catalog: used=%d limit=%d ok=%v", used, limit, ok)
	}
	// 目录里对不上模型时，CLI 上报的窗口也能让圆环有分母。
	s.threadUsage.Turns[0] = TokenUsage{Input: 300, Output: 200, ContextWindow: 200_000}
	if used, limit, ok := currentThreadContextTokens(s, &Thread{Provider: "unknown"}); !ok || used != 500 || limit != 200_000 {
		t.Errorf("reported window without catalog: used=%d limit=%d ok=%v", used, limit, ok)
	}
}

// Composer 侧只读取启用状态，固定模型列表本身不做删改：关闭 codex 后 pinnedModelPins 仍返回全部条目，
// providerEnabledState 把 codex 标记为关闭，供选择器在展示时过滤。
func TestProviderEnabledStateHidesDisabledProviderWithoutTouchingModelList(t *testing.T) {
	s := &chatState{}
	st := NewSettingsState(settingsTestDir(t), nil, s)
	pins := []ModelPin{
		{Provider: "claude", ModelID: "claude-3-7-sonnet"},
		{Provider: "codex", ModelID: "o3-mini"},
	}
	st.cfg.Models.Pins = pins
	st.cfg.Providers.Codex.Enabled = false
	setChatSettings(s, st)

	if got := pinnedModelPins(s); len(got) != 2 {
		t.Fatalf("固定模型列表不应被改动，实际 %+v", got)
	}
	enabled := providerEnabledState(s)
	if on, ok := enabled["codex"]; !ok || on {
		t.Fatalf("codex 应记为已关闭，实际 %+v", enabled)
	}
	if on, ok := enabled["claude"]; !ok || !on {
		t.Fatalf("claude 应记为启用，实际 %+v", enabled)
	}

	// 重新启用后启用状态随之恢复，条目立即可见。
	st.cfg.Providers.Codex.Enabled = true
	if on, ok := providerEnabledState(s)["codex"]; !ok || !on {
		t.Fatalf("重新启用后 codex 应为启用状态")
	}
}
