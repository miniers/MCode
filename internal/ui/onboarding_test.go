package mcodeui

import (
	"errors"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestOnboardingSteps 验证新手引导的对应行为。
func TestOnboardingSteps(t *testing.T) {
	for _, tc := range []struct {
		route onboardingRoute
		steps []onboardingStep
	}{
		{onboardingRecommended, []onboardingStep{onboardingWelcome, onboardingAgents, onboardingMagpie, onboardingFinish}},
		{onboardingDirect, []onboardingStep{onboardingWelcome, onboardingAgents, onboardingFinish}},
	} {
		for i, step := range tc.steps {
			next, prev := step, step
			if i+1 < len(tc.steps) {
				next = tc.steps[i+1]
			}
			if i > 0 {
				prev = tc.steps[i-1]
			}
			if onboardingNextStep(tc.route, step) != next || onboardingPrevStep(tc.route, step) != prev {
				t.Fatalf("路线 %v 的步骤 %v 错误", tc.route, step)
			}
		}
	}
}

// TestOnboardingClose 验证新手引导的对应行为。
func TestOnboardingClose(t *testing.T) {
	old := onboardingComplete
	t.Cleanup(func() { onboardingComplete = old })
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	u := onboardingState(s)
	u.open = true
	onboardingComplete = func() error { return errors.New("保存失败") }
	closeOnboarding(s, true)
	if !u.open || u.saveError != "无法保存引导状态，请重试" {
		t.Fatal("保存失败时应保持打开并显示错误")
	}
	onboardingComplete = func() error { return nil }
	closeOnboarding(s, true)
	if u.open {
		t.Fatal("保存成功后应关闭")
	}
}

// TestOnboardingResume 验证新手引导的对应行为。
func TestOnboardingResume(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	u := onboardingState(s)
	u.open = true
	u.step = onboardingMagpie
	u.suspendedForSettings = true
	st.mu.Lock()
	st.open = true
	st.mu.Unlock()
	if onboardingResume(s, st) {
		t.Fatal("设置打开时向导应隐藏")
	}
	st.mu.Lock()
	st.open = false
	st.mu.Unlock()
	if !onboardingResume(s, st) || u.step != onboardingMagpie || u.suspendedForSettings {
		t.Fatal("关闭设置后应恢复原步骤")
	}
}

// TestOnboardingIgnoresOtherSettingsWindow 验证其他窗口的设置不影响当前引导。
func TestOnboardingIgnoresOtherSettingsWindow(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	setChatSettings(s, st)
	settingsWindowMu.Lock()
	settingsWindow = &settingsWindowEntry{state: &SettingsState{open: true}}
	settingsWindowMu.Unlock()
	t.Cleanup(resetSettingsWindowForTest)

	u := onboardingState(s)
	u.open, u.step = true, onboardingWelcome
	tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 700)
	if !tt.HasText("欢迎使用 mcode") {
		t.Fatalf("其他窗口的设置不应隐藏引导：%q", tt.Texts())
	}

	u.step, u.suspendedForSettings = onboardingMagpie, true
	st.mu.Lock()
	st.open = true
	st.mu.Unlock()
	if onboardingResume(s, st) {
		t.Fatal("本窗口设置打开时引导应隐藏")
	}
	st.mu.Lock()
	st.open = false
	st.mu.Unlock()
	if !onboardingResume(s, st) || u.step != onboardingMagpie || u.suspendedForSettings {
		t.Fatal("本窗口设置关闭后应恢复原步骤")
	}
}

// TestOnboardingSummaryAndPrerequisites 验证新手引导的对应行为。
func TestOnboardingSummaryAndPrerequisites(t *testing.T) {
	if got := onboardingSummaryRoute(onboardingDirect, "未连接"); got != "已跳过" {
		t.Fatal("直连路线应显示跳过")
	}
	if got := onboardingSummaryRoute(onboardingRecommended, "已连接"); got != "已连接" {
		t.Fatal("推荐路线应沿用 Magpie 状态")
	}
	if !onboardingInstallDisabled("claude", true, false, false) || onboardingInstallDisabled("agy", false, false, false) {
		t.Fatal("安装前置条件错误")
	}
}

// TestOnboardingWizardAutoOpenOnce 验证新手引导的对应行为。
func TestOnboardingWizardAutoOpenOnce(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	WithOnboarding(true)(s)
	if !takeOnboardingAutoOpen(s) || takeOnboardingAutoOpen(s) {
		t.Fatal("自动打开标记应只消费一次")
	}
	other := newChatState(NewFakeClient())
	t.Cleanup(other.close)
	if takeOnboardingAutoOpen(other) {
		t.Fatal("普通窗口不应自动打开")
	}
}

// TestOnboardingRender 验证新手引导的对应行为。
func TestOnboardingRender(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	u := onboardingState(s)
	u.open = true
	u.step = onboardingWelcome
	tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 700)
	if !tt.HasText("欢迎使用 mcode") {
		t.Fatalf("未找到标题：%q", tt.Texts())
	}
}

// TestOnboardingStepRenderSmoke 验证主要步骤及减少动效模式可以渲染。
func TestOnboardingStepRenderSmoke(t *testing.T) {
	for _, tc := range []struct {
		step    onboardingStep
		texts   []string
		reduced bool
	}{
		{onboardingWelcome, []string{"Magpie", "pi", "统一路由", "额度与人设", "pi 子代理"}, false},
		{onboardingAgents, []string{"选择你的智能体"}, false},
		{onboardingFinish, []string{"准备好了"}, false},
		{onboardingWelcome, []string{"Magpie", "pi"}, true},
	} {
		s := newChatState(NewFakeClient())
		u := onboardingState(s)
		u.open, u.step = true, tc.step
		tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 700)
		if tc.reduced {
			tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
		}
		for _, text := range tc.texts {
			if !tt.HasText(text) {
				t.Errorf("步骤 %d 未找到 %q：%q", tc.step, text, tt.Texts())
			}
		}
		s.close()
	}
}

// TestOnboardingSuspendKeepsSuspendedWithoutWindow 验证无窗口时引导保持挂起。
func TestOnboardingSuspendKeepsSuspendedWithoutWindow(t *testing.T) {
	t.Cleanup(resetSettingsWindowForTest)
	resetSettingsWindowForTest()
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	setChatSettings(s, st)

	onboardingSuspend(s, "general", false)

	u := onboardingState(s)
	u.mu.Lock()
	suspended := u.suspendedForSettings
	u.mu.Unlock()
	if !suspended {
		t.Fatal("无窗口时引导应保持挂起")
	}
	if currentSettingsWindowState() != getChatSettings(s) {
		t.Fatal("当前设置状态应属于该会话")
	}
}

// TestOnboardingMagpieStep 验证推荐路径到连接 Magpie 后直接进入完成页，不再要求选模型。
func TestOnboardingMagpieStep(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	setChatSettings(s, st)
	st.mu.Lock()
	st.endpoints.loaded = true
	st.providerRows.loaded = true
	st.mu.Unlock()

	u := onboardingState(s)
	u.open, u.step = true, onboardingMagpie
	tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 700)
	if !tt.HasText("连接 Magpie") {
		t.Fatalf("未找到标题：%q", tt.Texts())
	}
	if tt.HasText("为 mcode 选择模型") || tt.HasText("选择模型") {
		t.Fatalf("不应再有选模型这一步：%q", tt.Texts())
	}
}

// TestOnboardingFinishReadyWithMagpie 验证推荐路径只要 Magpie 已连接就就绪，不要求勾选模型或安装 pi。
func TestOnboardingFinishReadyWithMagpie(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.mu.Lock()
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Magpie: true, Enabled: true, HasAPIKey: true}}
	st.piMagpie.loaded = true
	st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "gw"}}
	st.providerRows.loaded = true
	st.providerRows.available = map[string]bool{"pi": false}
	st.mu.Unlock()
	if !providerRowNotInstalled(st, "pi") {
		t.Fatal("测试前提：pi 应判定为未安装")
	}
	_, connected := onboardingMagpieStatus(st)
	if !connected {
		t.Fatal("测试前提：Magpie 应已连接")
	}
	if !onboardingFinishReady(onboardingRecommended, st, connected) {
		t.Fatal("Magpie 已连接时应就绪，无需勾选模型")
	}
	st.mu.Lock()
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Magpie: true, Enabled: true}}
	st.mu.Unlock()
	_, connected = onboardingMagpieStatus(st)
	if connected {
		t.Fatal("测试前提：缺少 API Key 时 Magpie 应未连接")
	}
	if onboardingFinishReady(onboardingRecommended, st, connected) {
		t.Fatal("Magpie 未连接时不应就绪")
	}
}
