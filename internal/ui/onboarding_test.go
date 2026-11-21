package mcodeui

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestOnboardingSinglePage 验证单页就绪清单：四行都渲染、已就绪无操作按钮、未就绪有操作按钮、计数正确且 codex 不再写死未知。
func TestOnboardingSinglePage(t *testing.T) {
	t.Run("四行与计数及操作按钮", func(t *testing.T) {
		old := onboardingComplete
		completed := 0
		onboardingComplete = func() error {
			completed++
			return nil
		}
		t.Cleanup(func() { onboardingComplete = old })

		s := newChatState(NewFakeClient())
		t.Cleanup(s.close)
		st := NewSettingsState(settingsTestDir(t), nil, s)
		setChatSettings(s, st)
		st.mu.Lock()
		st.providerRows.loaded = true
		st.providerRows.available = map[string]bool{"claude": true, "codex": true, "agy": false}
		st.providerRows.versions = map[string]ProviderVersionInfo{
			"claude": {Provider: "claude", Installed: "1.2.0"},
			"codex":  {Provider: "codex", Installed: "0.5.0"},
		}
		st.providerRows.login = map[string]string{
			"claude": ProviderLoginStateLoggedIn,
			"codex":  ProviderLoginStateLoggedOut,
		}
		st.endpoints.loaded = true
		st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Magpie: true, Enabled: true, HasAPIKey: true}}
		st.piMagpie.loaded = true
		st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "gw"}}
		st.mu.Unlock()

		u := onboardingState(s)
		u.open = true
		tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 900)
		tt.UseScreenReader()
		tt.Advance(time.Second)
		tt.Frame()

		for _, want := range []string{"欢迎使用 mcode", "Claude", "Codex", "Antigravity", "Magpie", "2/4 就绪", "开始使用"} {
			if !tt.HasText(want) {
				t.Fatalf("未找到 %q：%v", want, tt.Texts())
			}
		}
		if tt.HasText("稍后") {
			t.Fatalf("引导页不应再渲染重复的「稍后」按钮：%v", tt.Texts())
		}
		if tt.HasText("登录状态未知") || tt.HasText("已安装，登录状态未知") {
			t.Fatalf("codex 不应再显示「登录状态未知」：%v", tt.Texts())
		}
		if !tt.HasText("未登录（请在终端运行 codex login）") {
			t.Fatalf("codex 未登录时应走登录态摘要：%v", tt.Texts())
		}

		var buttons []string
		for _, a := range tt.AccessAll() {
			if a.Role == ui.RoleButton {
				buttons = append(buttons, a.Name)
			}
		}
		// 仅 codex(打开设置) 与 agy(安装) 两行有操作按钮，加上关闭/开始使用共 4 个按钮。
		if len(buttons) != 4 {
			t.Fatalf("按钮数量 = %d (%v)，期望只有未就绪两行带操作按钮", len(buttons), buttons)
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "打开设置"); !ok {
			t.Fatalf("未就绪的 codex 行应有「打开设置」按钮：%v", buttons)
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "安装"); !ok {
			t.Fatalf("未安装的 agy 行应有「安装」按钮：%v", buttons)
		}

		// 全部就绪时 4/4，且没有任何行操作按钮。
		st.mu.Lock()
		st.providerRows.login["codex"] = ProviderLoginStateLoggedIn
		st.providerRows.available["agy"] = true
		st.agy.localLogin = true
		st.mu.Unlock()
		tt.Advance(time.Second)
		tt.Frame()
		if !tt.HasText("4/4 就绪") {
			t.Fatalf("全部就绪时计数应为 4/4 就绪：%v", tt.Texts())
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "打开设置"); ok {
			t.Fatal("全部就绪时不应再有「打开设置」按钮")
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "安装"); ok {
			t.Fatal("全部就绪时不应再有「安装」按钮")
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "登录"); ok {
			t.Fatal("全部就绪时不应再有「登录」按钮")
		}

		if err := tt.Click("开始使用"); err != nil {
			t.Fatalf("点击「开始使用」失败：%v", err)
		}
		if completed != 1 {
			t.Fatalf("点击「开始使用」应标记引导完成，completed=%d", completed)
		}
		u.mu.Lock()
		stillOpen := u.open
		u.mu.Unlock()
		if stillOpen {
			t.Fatal("点击「开始使用」后弹窗应关闭")
		}
	})

	t.Run("单行已就绪与未就绪动作", func(t *testing.T) {
		s := newChatState(NewFakeClient())
		t.Cleanup(s.close)
		st := NewSettingsState(settingsTestDir(t), nil, s)
		st.mu.Lock()
		st.providerRows.loaded = true
		st.providerRows.available = map[string]bool{"claude": true, "codex": true, "agy": true}
		st.providerRows.versions = map[string]ProviderVersionInfo{
			"claude": {Provider: "claude", Installed: "1.2.0"},
			"codex":  {Provider: "codex", Installed: "0.5.0"},
		}
		st.providerRows.login = map[string]string{
			"claude": ProviderLoginStateLoggedIn,
			"codex":  ProviderLoginStateUnknown,
		}
		st.endpoints.loaded = true
		st.mu.Unlock()

		// 已就绪的 claude 行没有按钮；unknown 的 codex 行同样就绪且不显示「登录状态未知」。
		for _, id := range []string{"claude", "codex"} {
			tt := ui.NewTester(func(c *ui.Context) { onboardingProviderRow(c, s, st, id, 0) }, 600, 120)
			tt.UseScreenReader()
			for _, a := range tt.AccessAll() {
				if a.Role == ui.RoleButton {
					t.Fatalf("已就绪行 %s 不应有操作按钮：%v", id, tt.AccessAll())
				}
			}
			if tt.HasText("登录状态未知") {
				t.Fatalf("%s 不应显示「登录状态未知」：%v", id, tt.Texts())
			}
		}

		// 未登录的 agy 行有「登录」按钮；未连接的 Magpie 行有「打开设置」按钮。
		ttAgy := ui.NewTester(func(c *ui.Context) { onboardingProviderRow(c, s, st, "agy", 2) }, 600, 120)
		ttAgy.UseScreenReader()
		if _, ok := ttAgy.AccessByRole(ui.RoleButton, "登录"); !ok {
			t.Fatalf("未登录的 agy 行应显示「登录」按钮：%v", ttAgy.Texts())
		}

		ttMagpie := ui.NewTester(func(c *ui.Context) { onboardingMagpieRow(c, s, st, 3) }, 600, 120)
		ttMagpie.UseScreenReader()
		if _, ok := ttMagpie.AccessByRole(ui.RoleButton, "打开设置"); !ok {
			t.Fatalf("未连接的 Magpie 行应显示「打开设置」按钮：%v", ttMagpie.Texts())
		}
	})
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
	if !onboardingResume(s, st) || !u.open || u.suspendedForSettings {
		t.Fatal("关闭设置后应恢复引导弹窗")
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
	u.open = true
	tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 700)
	if !tt.HasText("欢迎使用 mcode") {
		t.Fatalf("其他窗口的设置不应隐藏引导：%q", tt.Texts())
	}

	u.suspendedForSettings = true
	st.mu.Lock()
	st.open = true
	st.mu.Unlock()
	if onboardingResume(s, st) {
		t.Fatal("本窗口设置打开时引导应隐藏")
	}
	st.mu.Lock()
	st.open = false
	st.mu.Unlock()
	if !onboardingResume(s, st) || !u.open || u.suspendedForSettings {
		t.Fatal("本窗口设置关闭后应恢复引导弹窗")
	}
}

// TestOnboardingToneAndPrerequisites 验证状态药丸色调映射与安装前置条件。
func TestOnboardingToneAndPrerequisites(t *testing.T) {
	if got := onboardingStatusTone(providerRowStatus{Ready: true, Summary: "已安装"}, false); got != ToneSuccess {
		t.Fatalf("就绪状态色调 = %v，期望 ToneSuccess", got)
	}
	if got := onboardingStatusTone(providerRowStatus{Summary: "已安装 · v1.0 · 未登录"}, false); got != ToneWarning {
		t.Fatalf("需要登录状态色调 = %v，期望 ToneWarning", got)
	}
	if got := onboardingStatusTone(providerRowStatus{Summary: "检查中"}, false); got != ToneWarning {
		t.Fatalf("检查中状态色调 = %v，期望 ToneWarning", got)
	}
	if got := onboardingStatusTone(providerRowStatus{Summary: "未安装"}, true); got != ToneNeutral {
		t.Fatalf("未安装状态色调 = %v，期望 ToneNeutral", got)
	}
	if got := onboardingStatusTone(providerRowStatus{Summary: "检测失败"}, false); got != ToneDanger {
		t.Fatalf("失败状态色调 = %v，期望 ToneDanger", got)
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
	tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 700)
	if !tt.HasText("欢迎使用 mcode") {
		t.Fatalf("未找到标题：%q", tt.Texts())
	}
}

// TestOnboardingSinglePageRenderSmoke 验证单页引导与减少动效模式可以渲染四行与底栏。
func TestOnboardingSinglePageRenderSmoke(t *testing.T) {
	for _, reduced := range []bool{false, true} {
		s := newChatState(NewFakeClient())
		u := onboardingState(s)
		u.open = true
		tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 900)
		if reduced {
			tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
		}
		for _, text := range []string{"欢迎使用 mcode", "Claude", "Codex", "Antigravity", "Magpie", "0/4 就绪", "开始使用"} {
			if !tt.HasText(text) {
				t.Errorf("reduced=%v 未找到 %q：%q", reduced, text, tt.Texts())
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

// TestOnboardingMagpieRow 验证单页引导展示 Magpie 状态行，不再有选模型步骤。
func TestOnboardingMagpieRow(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	setChatSettings(s, st)
	st.mu.Lock()
	st.endpoints.loaded = true
	st.providerRows.loaded = true
	st.mu.Unlock()

	u := onboardingState(s)
	u.open = true
	tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 900)
	if !tt.HasText("Magpie") || !tt.HasText("未连接") {
		t.Fatalf("未找到 Magpie 行：%q", tt.Texts())
	}
	if tt.HasText("为 mcode 选择模型") || tt.HasText("选择模型") {
		t.Fatalf("不应再有选模型文案：%q", tt.Texts())
	}
}

// TestOnboardingReadyCountWithMagpie 验证 Magpie 已连接时计数加 1，无需勾选模型或安装 pi。
func TestOnboardingReadyCountWithMagpie(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.mu.Lock()
	st.endpoints.loaded = true
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Magpie: true, Enabled: true, HasAPIKey: true}}
	st.piMagpie.loaded = true
	st.piMagpie.endpoints = []PiMagpieEndpoint{{ID: "gw"}}
	st.providerRows.loaded = true
	st.providerRows.available = map[string]bool{"claude": false, "codex": false, "agy": false, "pi": false}
	st.mu.Unlock()
	if !providerRowNotInstalled(st, "pi") {
		t.Fatal("测试前提：pi 应判定为未安装")
	}
	_, connected := onboardingMagpieStatus(st)
	if !connected {
		t.Fatal("测试前提：Magpie 应已连接")
	}
	if got := onboardingReadyCount(st); got != 1 {
		t.Fatalf("仅 Magpie 已连接时就绪计数 = %d，期望 1", got)
	}
	st.mu.Lock()
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw", Magpie: true, Enabled: true}}
	st.mu.Unlock()
	_, connected = onboardingMagpieStatus(st)
	if connected {
		t.Fatal("测试前提：缺少 API Key 时 Magpie 应未连接")
	}
	if got := onboardingReadyCount(st); got != 0 {
		t.Fatalf("Magpie 未连接时就绪计数 = %d，期望 0", got)
	}
}

func TestOnboardingProviderErrorMessage(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.mu.Lock()
	st.providerRows.updateMsg = map[string]string{"claude": "打开终端失败：测试"}
	st.mu.Unlock()
	tt := ui.NewTester(func(c *ui.Context) { onboardingProviderRow(c, s, st, "claude", 0) }, 800, 700)
	if !tt.HasText("打开终端失败：测试") {
		t.Fatalf("未显示错误：%v", tt.Texts())
	}
}

func TestOnboardingProbingDisablesInstall(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.mu.Lock()
	st.providerRows.loaded = true
	st.providerRows.available = map[string]bool{"claude": false}
	st.mu.Unlock()
	u := onboardingState(s)
	u.open, u.probing = true, true
	tt := ui.NewTester(func(c *ui.Context) { onboardingProviderRow(c, s, st, "claude", 0) }, 800, 200)
	tt.UseScreenReader()
	info, ok := tt.AccessByRole(ui.RoleButton, "安装")
	if !ok || !info.Disabled {
		t.Fatalf("前置条件探测期间安装按钮应禁用：%+v", info)
	}
	u.mu.Lock()
	u.probing, u.nodeOK, u.npmOK = false, true, true
	u.mu.Unlock()
	tt.Frame()
	info, ok = tt.AccessByRole(ui.RoleButton, "安装")
	if !ok || info.Disabled {
		t.Fatalf("探测完成且 node/npm 可用后安装按钮应启用：%+v", info)
	}
}

// TestOnboardingHeroNoTreeRebuild 验证引导欢迎页与检查中徽章只做 Draw 级重画，不持续重建组件树。
func TestOnboardingHeroNoTreeRebuild(t *testing.T) {
	cases := []struct {
		name   string
		settle time.Duration
		view   func(*ui.Context)
	}{
		{"hero", 0, renderOnboardingHero},
		{"badge", 2 * motionIconFade, func(c *ui.Context) {
			onboardingBadge(c, "check", "检查中", ToneNeutral)
		}},
	}
	for _, tc := range cases {
		for _, reduced := range []bool{false, true} {
			mode := "animated"
			if reduced {
				mode = "reduced"
			}
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				setAppReduceMotion(t, reduced)
				builds := 0
				tt := ui.NewTesterWith(func(c *ui.Context) {
					builds++
					tc.view(c)
				}, 600, 200, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
				tt.Frame()
				if tc.settle > 0 {
					tt.Advance(tc.settle)
				}
				beforeBuilds := builds
				before := append([]byte(nil), tt.Image().Pix...)
				tt.Advance(motionSweep / 2)
				if builds != beforeBuilds {
					t.Errorf("视图重建：%d → %d", beforeBuilds, builds)
				}
				if tt.FramePending() {
					t.Error("不应请求组件树的下一帧")
				}
				if same := bytes.Equal(before, tt.Image().Pix); same != reduced {
					t.Errorf("图像相同=%v，减少动效=%v", same, reduced)
				}
			})
		}
	}
}

func TestOnboardingProviderLoadErrorAndRecheck(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.mu.Lock()
	st.providerRows.loaded = true
	st.providerRows.loadErr = "探测失败"
	st.mu.Unlock()

	if got := onboardingProviderStatus(st, "claude"); got.Summary != "检测失败" {
		t.Fatalf("loadErr 非空时状态 = %q，期望 检测失败", got.Summary)
	}
	tt := ui.NewTester(func(c *ui.Context) { onboardingProviderRow(c, s, st, "claude", 0) }, 800, 700)
	if !tt.HasText("检测失败") || tt.HasText("未安装") {
		t.Fatalf("应显示检测失败而非未安装：%v", tt.Texts())
	}

	st.mu.Lock()
	st.providerRows.loading = true
	st.mu.Unlock()
	if got := onboardingProviderStatus(st, "claude"); got.Summary != "检查中" {
		t.Fatalf("重新加载期间状态 = %q，期望 检查中", got.Summary)
	}
}

func TestOnboardingRemovedCopyAbsent(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	setChatSettings(s, st)
	st.mu.Lock()
	st.endpoints.loaded = true
	st.providerRows.loaded = true
	st.mu.Unlock()

	u := onboardingState(s)
	u.open = true
	tt := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 900)
	banned := []string{
		"深度整合", "也可以只用 Claude",
		"装好任意一个就能开始", "选择 Google 登录",
		"填写网关地址和 API Key", "127.0.0.1:3425",
		"选择你的智能体", "连接 Magpie", "准备好了",
		"统一路由", "额度与人设", "子代理",
		"上一步", "使用推荐配置", "只用直连",
	}
	for _, text := range banned {
		if tt.HasText(text) {
			t.Errorf("单页引导不应再包含 %q：%v", text, tt.Texts())
		}
	}
}
