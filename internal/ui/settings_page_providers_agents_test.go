package mcodeui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/providers/agy/accounts"
)

// 内置 mcode 只为已有 mcode 会话保留：名称还在，供应商列表与状态判定不再为它特判。
func TestMcodeProviderFilteredFromList(t *testing.T) {
	if got := providerDisplayName("mcode"); got != "mcode" {
		t.Fatalf("providerDisplayName(mcode) = %q", got)
	}
	st := &SettingsState{providerRows: providerRowsState{cliIDs: []string{"mcode", "pi"}}}
	if providerConfigured(st, "mcode") || providerStatusFor(st, "mcode").Ready {
		t.Fatal("内置 mcode 不应再被视为已安装")
	}
	if _, ok := providerTerminalCommand(st, "mcode", "install"); ok {
		t.Fatal("内置 mcode 不应再有安装命令")
	}
	for _, e := range cliProviderListEntries(st, nil) {
		if e.ID == "mcode" {
			t.Fatalf("供应商列表不应包含 mcode：%+v", e)
		}
	}
}

func TestProviderTerminalCommandsAndInstallActions(t *testing.T) {
	st := &SettingsState{}
	agyInstall := "curl -fsSL https://antigravity.google/cli/install.sh | bash"
	claudeInstall := "npm install -g @anthropic-ai/claude-code"
	if runtime.GOOS == "windows" {
		agyInstall = "irm https://antigravity.google/cli/install.ps1 | iex"
		claudeInstall = "irm https://claude.ai/install.ps1 | iex"
	}
	for _, tc := range []struct {
		id, action, want string
		ok               bool
	}{
		{"claude", "install", claudeInstall, true},
		{"codex", "install", "npm install -g @openai/codex", true},
		{"mcode", "install", "", false},
		{"mcode", "update", "", false},
		{"pi", "install", "npm install -g @earendil-works/pi-coding-agent", true},
		{"pi", "login", "", false},
		{"agy", "install", agyInstall, true},
		{"agy", "login", "'agy'", true},
	} {
		t.Run(tc.id+"/"+tc.action, func(t *testing.T) {
			got, ok := providerTerminalCommand(st, tc.id, tc.action)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("得到 %q/%v，期望 %q/%v", got, ok, tc.want, tc.ok)
			}
		})
	}
	st.providerRows.available = map[string]bool{"agy": false, "pi": false}
	st.agy.localLogin = true
	st.agy.accounts = []accounts.Account{{ID: "account"}}
	for _, id := range []string{"agy", "pi"} {
		if label, action, ok := providerSheetPrimaryAction(st, id); !ok || label != "安装" || action != "install" {
			t.Errorf("%s 的操作为 %q/%q/%v", id, label, action, ok)
		}
		if !providerRowNotInstalled(st, id) {
			t.Errorf("%s 应归入未安装", id)
		}
	}
}

func TestProviderInstallScript(t *testing.T) {
	cmd := "npm install -g @openai/codex"
	got := providerInstallScript("windows", cmd)
	for _, want := range []string{
		"Get-Command npm -ErrorAction SilentlyContinue",
		"winget install -e --id OpenJS.NodeJS.LTS --accept-source-agreements --accept-package-agreements",
		"[Environment]::GetEnvironmentVariable('Path', 'Machine') + ';' + [Environment]::GetEnvironmentVariable('Path', 'User') + ';' + $env:Path",
		"需要先安装 Node.js：https://nodejs.org",
		"exit 1",
		cmd + "\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Windows 安装脚本缺少 %q", want)
		}
	}
	if strings.Count(got, "Get-Command npm -ErrorAction SilentlyContinue") != 2 {
		t.Error("应在安装 Node.js 前后检查 npm")
	}
	if got := providerInstallScript("windows", "irm https://claude.ai/install.ps1 | iex"); got != "irm https://claude.ai/install.ps1 | iex\n" {
		t.Errorf("非 npm 命令的 Windows 脚本 = %q", got)
	}
	if got := providerInstallScript("darwin", cmd); got != "#!/bin/zsh -l\n"+cmd+"\n" {
		t.Errorf("macOS 脚本 = %q", got)
	}
}

func TestRecheckProvidersOnFocus(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		pending, loading, wantLoaded bool
	}{
		{name: "pending", pending: true, wantLoaded: false},
		{name: "no pending", wantLoaded: true},
		{name: "loading", pending: true, loading: true, wantLoaded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &SettingsState{providerRows: providerRowsState{loaded: true, loading: tc.loading, recheckOnFocus: tc.pending}}
			st.recheckProvidersOnFocus()
			if st.providerRows.loaded != tc.wantLoaded || st.providerRows.recheckOnFocus {
				t.Errorf("loaded = %v, recheckOnFocus = %v", st.providerRows.loaded, st.providerRows.recheckOnFocus)
			}
		})
	}
}

func TestProviderClaudeWindowsInstallCommand(t *testing.T) {
	original := providerTerminalGOOS
	providerTerminalGOOS = "windows"
	defer func() { providerTerminalGOOS = original }()
	got, ok := providerTerminalCommand(&SettingsState{}, "claude", "install")
	if !ok || got != "irm https://claude.ai/install.ps1 | iex" {
		t.Fatalf("Windows Claude 安装命令 = %q/%v", got, ok)
	}
}

func openProvidersPageTest(t *testing.T, fake *FakeClient) (*ui.Tester, *SettingsState) {
	t.Helper()
	tt, st, _ := openProvidersPageDataDirTest(t, fake)
	return tt, st
}

// clickProviderDetailTabForTest 打开供应商面板；高级节需要先展开。
func clickProviderDetailTabForTest(t *testing.T, tt *ui.Tester, id, label string) {
	t.Helper()
	if err := tt.Click(providerDisplayName(id)); err != nil {
		t.Fatalf("打开供应商 %q：%v", id, err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("关闭供应商") })
	switch label {
	case "命令行", "高级", "调度", "终端登录":
		clickWhenSettled(t, tt, "展开高级")
	}
}

// 面板测量值和拖动偏移是包级状态，不清掉会在测试之间泄漏。
func resetProviderSheetLayoutForTest() {
	modalDragReset("settings-sheet-provider")
	settingsSheetMeasureMu.Lock()
	delete(settingsSheetMeasures, "provider")
	settingsSheetMeasureMu.Unlock()
}

// openProvidersPageDataDirTest 与 openProvidersPageTest 相同，但额外返回数据目录，
// 便于预置与校验 provider-status-cache.json。
func openProvidersPageDataDirTest(t *testing.T, fake *FakeClient) (*ui.Tester, *SettingsState, string) {
	t.Helper()
	resetProviderSheetLayoutForTest()
	t.Cleanup(resetProviderSheetLayoutForTest)
	dataDir := settingsTestDir(t)
	var st *SettingsState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
		st = getChatSettings(s)
	}), 1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("模型与供应商"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.providerRows.loaded
	})
	return tt, st, dataDir
}

// (a) orderedProviderIDs 已配置居前：例如 codex 可用、claude 不可用、agy 有账号 → 已配置组在前且组内保持原生顺序。
func TestProviders_OrderedProviderIDsConfiguredFirst(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &SettingsState{}
	st.providerRows = providerRowsState{
		available: map[string]bool{
			"codex":  true,
			"claude": false,
		},
		versions: map[string]ProviderVersionInfo{
			"claude": {Installed: ""},
			"codex":  {Installed: "1.0"},
		},
	}
	st.agy.accounts = []accounts.Account{
		{ID: "acc-1", Email: "test@example.com"},
	}

	ordered := orderedProviderIDs(st)
	expected := []string{"codex", "agy", "claude"}
	if len(ordered) != len(expected) {
		t.Fatalf("expected len %d, got %d: %v", len(expected), len(ordered), ordered)
	}
	for i, v := range expected {
		if ordered[i] != v {
			t.Fatalf("at index %d expected %q, got %q (full list: %v)", i, v, ordered[i], ordered)
		}
	}
}

func TestProviders_DefaultCollapsedAndToggle(t *testing.T) {
	st := &SettingsState{}
	openProviderSheet(st, "cli", "codex")
	kind, id := currentProviderSheet(st)
	if kind != "cli" || id != "codex" {
		t.Fatalf("当前供应商 = %q, %q", kind, id)
	}
}

func TestSettingsModels_MagpieSettingsLinkInModelsSection(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	state := st.agentMagpieFor("claude")
	state.loaded = true
	state.setting = routeTestSetting("claude", agentMagpieModeInject)
	openProviderSheet(st, "cli", "claude")
	body := ui.NewTester(func(c *ui.Context) {
		for _, def := range cliProviderSheetSections(st, "claude", nil) {
			def.Render(c, st)
		}
	}, 1200, 2400)
	body.Frame()
	if !body.HasText("Magpie 接入") || !body.HasText("仅 mcode") {
		t.Fatalf("模型节顶部缺少 Magpie 设置行：%v", body.Texts())
	}
	if body.HasText("直连") || body.HasText("全局") {
		t.Fatalf("模型节不应再显示 Magpie 模式分段：%v", body.Texts())
	}
}

// agy 面板包含账号列表与调度设置，不再需要独立设置页。
func TestProviders_AgySettingsInExpandedRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, p := range settingsPages() {
		if p.id == "agy" {
			t.Fatal("settingsPages() should not contain independent agy page")
		}
	}
	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{{ID: "acc-1", Email: "agy-user@example.com", Status: accounts.StatusActive}}
	fake.agyDefaultAccountID = "acc-1"
	tt, st := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "agy", "")
	// 账号与调度策略直接可见，终端登录需要展开高级。
	selectProviderSheetSectionForTest(t, tt, st, "sec-agy-accounts")
	waitFor(t, tt, func() bool { return tt.HasText("agy-user@example.com") })
	selectProviderSheetSectionForTest(t, tt, st, "sec-agy-strategy")
	waitFor(t, tt, func() bool { return tt.HasText("调度策略") })
	selectProviderSheetSectionForTest(t, tt, st, "sec-agy-advanced-login")
	if !tt.HasText("终端登录（备用）") {
		t.Fatalf("供应商面板应显示账号与调度设置: %v", tt.Texts())
	}
}

// (d) 多账号增删切换：FakeClient 预置两个 agy 账号，展开后点设为默认断言 AgySetDefault 被调用，删除确认后断言 AgyRemove 被调用，通过已有登录流程测试写法断言添加账号入口能触发登录调用。
func TestProviders_MultiAccountSwitchDeleteAdd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.agyAccounts = []accounts.Account{
		{ID: "acc-1", Email: "user1@example.com", Status: accounts.StatusActive},
		{ID: "acc-2", Email: "user2@example.com", Status: accounts.StatusActive},
	}
	fake.agyDefaultAccountID = "acc-1"

	fake.agyLoginWaitDone = make(chan struct{})
	tt, st := openProvidersPageTest(t, fake)

	clickProviderDetailTabForTest(t, tt, "agy", "")
	selectProviderSheetSectionForTest(t, tt, st, "sec-agy-accounts")
	waitFor(t, tt, func() bool {
		return tt.HasText("user1@example.com") && tt.HasText("user2@example.com")
	})

	// 1. 点设为默认（针对 acc-2）
	clickWhenSettled(t, tt, "设为默认")
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.agySetDefaultIDs) > 0 && fake.agySetDefaultIDs[len(fake.agySetDefaultIDs)-1] == "acc-2"
	})

	// 2. 删除确认后断言 AgyRemove 被调用（删除 acc-2）
	clickWhenSettled(t, tt, "btn-del-acc-2")
	waitFor(t, tt, func() bool {
		return tt.HasText("确认删除")
	})
	clickWhenSettled(t, tt, "确认删除")
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, id := range fake.agyRemovedIDs {
			if id == "acc-2" {
				return true
			}
		}
		return false
	})

	// 3. 通过已有登录流程测试写法断言添加账号入口能触发登录调用
	clickWhenSettled(t, tt, "浏览器登录")
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.agy.loginBrowserURL != ""
	})
}

// (e) providerRowSubtitle 在版本有更新时包含 "有新版本"。
func TestProviders_RowSubtitleUpdateAvailable(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &SettingsState{}
	st.providerRows.loaded = true
	st.providerRows.versions = map[string]ProviderVersionInfo{
		"claude": {
			Provider:        "claude",
			Installed:       "1.2.0",
			Latest:          "1.3.0",
			UpdateAvailable: true,
		},
		"codex": {
			Provider:        "codex",
			Installed:       "0.5.0",
			Latest:          "0.6.0",
			UpdateAvailable: true,
		},
	}
	st.providerRows.available = map[string]bool{
		"claude": true,
		"codex":  true,
	}

	subClaude := providerRowSubtitle(st, "claude")
	if !strings.Contains(subClaude, "有新版本") {
		t.Fatalf("expected claude subtitle to contain '有新版本', got: %s", subClaude)
	}
	if !strings.Contains(subClaude, "1.3.0") {
		t.Fatalf("expected claude subtitle to contain latest version 1.3.0, got: %s", subClaude)
	}

	subCodex := providerRowSubtitle(st, "codex")
	if !strings.Contains(subCodex, "有新版本") {
		t.Fatalf("expected codex subtitle to contain '有新版本', got: %s", subCodex)
	}
}

// TestProviders_ProviderStatusSummary 表驱动覆盖 providerStatusSummary 推导的三类 Provider 状态文案。
func TestProviders_ProviderStatusSummary(t *testing.T) {
	cases := []struct {
		name     string
		in       providerStatusInput
		ready    bool
		summary  string
		nextStep string
	}{
		{
			name:    "CLI 已安装",
			in:      providerStatusInput{Kind: providerKindCLI, Available: true, Version: ProviderVersionInfo{Installed: "1.2.0"}},
			ready:   true,
			summary: "已安装 · v1.2.0",
		},
		{
			name:    "CLI 只解析到路径",
			in:      providerStatusInput{Kind: providerKindCLI, Version: ProviderVersionInfo{ExecutablePath: "/opt/homebrew/bin/pi"}},
			ready:   true,
			summary: "已安装",
		},
		{
			name:     "CLI 未安装",
			in:       providerStatusInput{Kind: providerKindCLI},
			summary:  "未安装",
			nextStep: "点击安装",
		},
		{
			name: "CLI 有更新",
			in: providerStatusInput{Kind: providerKindCLI, Available: true,
				Version: ProviderVersionInfo{Installed: "1.2.0", Latest: "1.3.0", UpdateAvailable: true}},
			ready:   true,
			summary: "已安装 · v1.2.0 · 有新版本 1.3.0",
		},
		{
			name: "CLI 有更新但缺最新版本号",
			in: providerStatusInput{Kind: providerKindCLI, Available: true,
				Version: ProviderVersionInfo{Installed: "1.2.0", UpdateAvailable: true}},
			ready:   true,
			summary: "已安装 · v1.2.0 · 有新版本",
		},
		{
			name:     "登录型有账号但未安装",
			in:       providerStatusInput{Kind: providerKindLogin, AvailableKnown: true, AccountCount: 2},
			summary:  "未安装",
			nextStep: "点击安装",
		},
		{
			name:     "登录型有凭证但未安装",
			in:       providerStatusInput{Kind: providerKindLogin, AvailableKnown: true, LocalLogin: true},
			summary:  "未安装",
			nextStep: "点击安装",
		},
		{
			name:     "登录型已安装但未登录",
			in:       providerStatusInput{Kind: providerKindLogin, AvailableKnown: true, Available: true},
			summary:  "未登录",
			nextStep: "需要登录",
		},
		{
			name:     "登录型安装状态未知",
			in:       providerStatusInput{Kind: providerKindLogin},
			summary:  "未登录",
			nextStep: "需要登录",
		},
		{
			name:     "登录型未登录",
			in:       providerStatusInput{Kind: providerKindLogin},
			summary:  "未登录",
			nextStep: "需要登录",
		},
		{
			name:    "登录型已登录多账号",
			in:      providerStatusInput{Kind: providerKindLogin, AvailableKnown: true, Available: true, AccountCount: 2},
			ready:   true,
			summary: "已登录 2 个账号",
		},
		{
			name:    "登录型系统账号",
			in:      providerStatusInput{Kind: providerKindLogin, LocalLogin: true},
			ready:   true,
			summary: "已登录（系统账号）",
		},
		{
			name:     "API Key 未设置",
			in:       providerStatusInput{Kind: providerKindAPIKey},
			summary:  "未配置",
			nextStep: "填写 API Key",
		},
		{
			name:    "API Key 已配置",
			in:      providerStatusInput{Kind: providerKindAPIKey, HasAPIKey: true},
			ready:   true,
			summary: "已配置",
		},
		{
			name:    "加载中",
			in:      providerStatusInput{Kind: providerKindCLI, Loading: true},
			summary: "检查中…",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerStatusSummary(tc.in)
			if got.Ready != tc.ready || got.Summary != tc.summary || got.NextStep != tc.nextStep {
				t.Fatalf("providerStatusSummary(%+v) = %+v, want {Ready:%v Summary:%q NextStep:%q}", tc.in, got, tc.ready, tc.summary, tc.nextStep)
			}
			if tc.ready && got.NextStep != "" {
				t.Fatalf("可用时不应给出下一步，got %q", got.NextStep)
			}
			// 行内副标题在不可用时追加「 · <NextStep>」。
			wantLine := tc.summary
			if tc.nextStep != "" {
				wantLine += " · " + tc.nextStep
			}
			if line := providerSubtitleText(got); line != wantLine {
				t.Fatalf("providerSubtitleText = %q, want %q", line, wantLine)
			}
		})
	}
}

// (g) loadProviderRows 填充 CLI harness 的 settings/cliIDs/credits，忽略 Kind=="api" 的项，并为所有 Provider 请求余额。
func TestProviders_CLIRowsLoadSettingsAndCredits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true},
		{ID: "commandcode", Name: "Command Code", Kind: "cli"},
		{ID: "deepseek", Name: "DeepSeek", Kind: "api", BaseURL: "https://api.deepseek.com"},
	}
	fake.providerCreditsResult = map[string]ProviderCredits{
		"copilot": {Provider: "copilot", Total: 12.5, Currency: "USD", AmountText: "$12.50", Usable: true},
	}

	_, st := openProvidersPageTest(t, fake)

	st.mu.Lock()
	settingsCount := len(st.providerRows.settings)
	_, hasAPI := st.providerRows.settings["deepseek"]
	cliIDs := append([]string(nil), st.providerRows.cliIDs...)
	credits, hasCredits := st.providerRows.credits["copilot"]
	binDraft := st.providerRows.binDraft["copilot"]
	st.mu.Unlock()

	if settingsCount != 2 {
		t.Fatalf("expected 2 CLI settings, got %d", settingsCount)
	}
	if hasAPI {
		t.Fatal(`Kind=="api" 的设置不应进入 CLI settings`)
	}
	if len(cliIDs) != 2 || cliIDs[0] != "copilot" || cliIDs[1] != "commandcode" {
		t.Fatalf("expected cliIDs [copilot commandcode], got %v", cliIDs)
	}
	if !hasCredits || credits.Total != 12.5 {
		t.Fatalf("expected copilot credits 12.5, got %+v (present=%v)", credits, hasCredits)
	}
	if binDraft != "" {
		t.Fatalf("expected empty bin draft, got %q", binDraft)
	}

	fake.mu.Lock()
	creditCalls := append([]string(nil), fake.providerCreditsCalls...)
	fake.mu.Unlock()
	for _, want := range []string{"claude", "codex", "agy", "copilot"} {
		found := false
		for _, id := range creditCalls {
			if id == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected ProviderCredits called for %q, calls: %v", want, creditCalls)
		}
	}
}

// 内置 mcode 不再作为供应商出现：即使后端仍以 Kind=cli 下发也要过滤掉。
func TestProviders_McodeHiddenFromList(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "mcode", Name: "mcode", Kind: "cli", Enabled: true},
	}
	_, st := openProvidersPageTest(t, fake)

	st.mu.Lock()
	cliIDs := append([]string(nil), st.providerRows.cliIDs...)
	_, hasSetting := st.providerRows.settings["mcode"]
	st.mu.Unlock()
	if len(cliIDs) != 0 || hasSetting {
		t.Fatalf("mcode 不应进入 CLI 供应商列表，cliIDs=%v hasSetting=%v", cliIDs, hasSetting)
	}
	for _, e := range append(cliProviderListEntries(st, nil), apiProviderListEntries(st)...) {
		if e.ID == "mcode" {
			t.Fatalf("供应商列表不应包含 mcode：%+v", e)
		}
	}
}

// Anthropic 是普通 API 行：只显示 API Key，保存后进入已配置状态。
func TestProviders_AnthropicAPIKeyOnly(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "anthropic", Name: "Anthropic", Kind: "api"},
	}
	page, st := openProvidersPageTest(t, fake)
	waitFor(t, page, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.loaded
	})

	found := false
	for _, e := range apiProviderListEntries(st) {
		if e.ID == "anthropic" {
			found = e.Kind == "preset"
		}
	}
	if !found {
		t.Fatalf("anthropic 应作为普通 API 行列出")
	}

	openProviderSheet(st, "preset", "anthropic")
	panel := ui.NewTester(func(c *ui.Context) { renderAPIProviderSheetSections(c, st, "preset", "anthropic") }, 1000, 1200)
	panel.Frame()
	if !panel.HasText("密钥（API Key）") || panel.HasText("接口地址（BaseURL）") || panel.HasText("默认模型") {
		t.Fatalf("anthropic 面板应只有 API Key 行：%v", panel.Texts())
	}

	st.mu.Lock()
	st.presets.apiKeyDraft = "sk-ant-ui"
	st.mu.Unlock()
	panel.Frame()
	clickWhenSettled(t, panel, "保存")
	waitFor(t, panel, func() bool {
		fake.mu.Lock()
		keys := append([]string(nil), fake.saveProviderSettingKeys...)
		fake.mu.Unlock()
		for _, key := range keys {
			if key == "sk-ant-ui" {
				return true
			}
		}
		return false
	})
	waitFor(t, panel, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		for _, item := range st.presets.items {
			if item.ID == "anthropic" {
				return item.HasAPIKey
			}
		}
		return false
	})
}

// (g2) 已启用的 API 预设也查余额，未启用的不查，重复 ID 只查一次。
func TestProviders_APIPresetCredits(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.providerSettings = []ProviderSetting{
		{ID: "deepseek", Name: "DeepSeek", Kind: "api", Enabled: true},
		{ID: "zai", Name: "ZAI", Kind: "api"},
		{ID: "meta", Name: "Meta AI", Kind: "api", Enabled: true},
	}
	fake.providerCreditsResult = map[string]ProviderCredits{
		"deepseek": {Provider: "deepseek", Total: 7, Currency: "USD", AmountText: "$7.00", Usable: true},
	}
	fake.mu.Unlock()

	_, st := openProvidersPageTest(t, fake)

	st.mu.Lock()
	credits, hasCredits := st.providerRows.credits["deepseek"]
	st.mu.Unlock()
	if !hasCredits || credits.Total != 7 {
		t.Fatalf("已启用的 API 预设应有余额，got %+v (present=%v)", credits, hasCredits)
	}

	fake.mu.Lock()
	calls := append([]string(nil), fake.providerCreditsCalls...)
	fake.mu.Unlock()
	counts := make(map[string]int, len(calls))
	for _, id := range calls {
		counts[id]++
	}
	if counts["deepseek"] != 1 || counts["meta"] != 1 {
		t.Fatalf("已启用的 API 预设应各查一次余额，calls: %v", calls)
	}
	if counts["zai"] != 0 {
		t.Fatalf("未启用的 API 预设不应查余额，calls: %v", calls)
	}
}

// (h) orderedProviderIDs 在原生 Provider 之后追加 CLI harness，并保持 ProviderSettings 返回顺序。
func TestProviders_OrderedProviderIDsIncludeCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &SettingsState{}
	st.providerRows = providerRowsState{
		available: map[string]bool{
			"codex": true,
		},
		versions: map[string]ProviderVersionInfo{
			"codex": {Installed: "1.0"},
		},
		cliIDs: []string{"mcode", "pi", "copilot"},
		settings: map[string]ProviderSetting{
			"mcode":   {ID: "mcode", Name: "mcode", Kind: "cli"},
			"pi":      {ID: "pi", Name: "Pi", Kind: "cli"},
			"copilot": {ID: "copilot", Name: "GitHub Copilot", Kind: "cli"},
		},
	}

	ordered := orderedProviderIDs(st)
	expected := []string{"codex", "claude", "agy", "mcode", "pi", "copilot"}
	if len(ordered) != len(expected) {
		t.Fatalf("expected len %d, got %d: %v", len(expected), len(ordered), ordered)
	}
	for i, v := range expected {
		if ordered[i] != v {
			t.Fatalf("at index %d expected %q, got %q (full list: %v)", i, v, ordered[i], ordered)
		}
	}
}

// (j) 保存「可执行文件路径」后 SaveProviderSetting 收到带 Bin 的设置。
func TestProviders_SaveBinSendsProviderSetting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true},
	}

	tt, st := openProvidersPageTest(t, fake)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.providerRows.settings) == 1
	})

	st.mu.Lock()
	st.providerRows.binDraft["copilot"] = " /opt/homebrew/bin/copilot "
	st.mu.Unlock()

	saveProviderRowBin(st, "copilot")

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, s := range fake.saveProviderSettingCalls {
			if s.ID == "copilot" && s.Bin == "/opt/homebrew/bin/copilot" {
				return true
			}
		}
		return false
	})

	st.mu.Lock()
	draft := st.providerRows.binDraft["copilot"]
	st.mu.Unlock()
	if draft != "/opt/homebrew/bin/copilot" {
		t.Fatalf("expected trimmed draft synced from save result, got %q", draft)
	}
	saved := ui.NewTester(func(c *ui.Context) { renderProviderCLIExpanded(c, st, "copilot", false, false) }, 1000, 4000)
	if !saved.HasText("已保存") {
		t.Fatal("保存成功后缺少已保存状态")
	}
}

// (k) 未登录的 copilot 展开区显示登录引导；Depleted 余额显示「余额不足」。
func TestProviders_CopilotLoginGuidanceAndDepletedBalance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true},
	}
	fake.providerCreditsResult = map[string]ProviderCredits{
		"copilot": {Provider: "copilot", Total: 0, Currency: "USD", AmountText: "$0.00", Usable: true},
	}

	tt, st := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "copilot", "")
	// 账号节直接显示余额与登录引导。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-account")
	waitFor(t, tt, func() bool { return tt.HasText("余额不足") })

	waitFor(t, tt, func() bool {
		return tt.HasText("尚未登录 GitHub Copilot") &&
			tt.HasText("在终端登录")
	})

	// 版本信息行在命令行节。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-cli")
	if !tt.HasText("已安装版本") {
		t.Fatal("expected version info row in expanded CLI row")
	}
}

// (l) 余额充足时显示金额，不显示「余额不足」。
func TestProviders_BalancePanelShowsAmount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true},
	}
	fake.providerCreditsResult = map[string]ProviderCredits{
		"copilot": {Provider: "copilot", Total: 12.5, Currency: "USD", AmountText: "$12.50", Usable: true},
	}

	tt, _ := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "copilot", "")
	waitFor(t, tt, func() bool { return tt.HasText("$12.50") })
	if tt.HasText("余额不足") {
		t.Fatal("余额充足时不应显示「余额不足」")
	}
}

// (m) droid 认证方式映射：网页登录 → browser，API Key → apiKey。
func TestProviders_ProviderAuthMapping(t *testing.T) {
	if got := providerAuthIndex("browser"); got != 0 {
		t.Fatalf("providerAuthIndex(browser) = %d, want 0", got)
	}
	if got := providerAuthIndex("apiKey"); got != 1 {
		t.Fatalf("providerAuthIndex(apiKey) = %d, want 1", got)
	}
	if got := providerAuthIndex(""); got != 0 {
		t.Fatalf("providerAuthIndex(\"\") = %d, want 0", got)
	}
	if got := providerAuthFromIndex(0); got != "browser" {
		t.Fatalf("providerAuthFromIndex(0) = %q, want browser", got)
	}
	if got := providerAuthFromIndex(1); got != "apiKey" {
		t.Fatalf("providerAuthFromIndex(1) = %q, want apiKey", got)
	}
}

// (n) droid 展开区显示认证方式与 ACP 重启提示。
func TestProviders_DroidAuthAndRestartNote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "droid", Name: "Droid", Kind: "cli", Enabled: true},
	}

	tt, st := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "droid", "")
	// 认证方式在账号节，重启提示在高级里的命令行节。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-account")
	waitFor(t, tt, func() bool {
		return tt.HasText("认证方式") &&
			tt.HasText("网页登录") &&
			tt.HasText("API Key")
	})
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-cli")
	waitFor(t, tt, func() bool {
		return tt.HasText("修改后需重启 MCode 生效")
	})
}

// (f) 检查更新返回 ErrNotImplemented 时显示 "暂不支持检查更新"。
func TestProviders_CheckUpdateNotImplemented(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.checkProviderUpdateErr = ErrNotImplemented

	tt, st := openProvidersPageTest(t, fake)

	clickProviderDetailTabForTest(t, tt, "codex", "")
	// 检查更新行在命令行节。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-cli")
	waitFor(t, tt, func() bool { return tt.HasText("检查更新") })

	// 点击“检查更新”
	if err := tt.Click("检查更新"); err != nil {
		t.Fatalf("failed to click '检查更新': %v", err)
	}

	// 断言显示“暂不支持检查更新”
	waitFor(t, tt, func() bool {
		return tt.HasText("暂不支持检查更新")
	})
}

// (o) provider-status-cache.json：落盘后可原样读回；文件缺失、损坏或版本不符时视为无效。
func TestProviders_StatusCacheRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)

	updateProviderStatusCache(dataDir, func(cache *providerStatusCache) {
		cache.Available = map[string]bool{"codex": true}
		cache.Versions = map[string]ProviderVersionInfo{
			"pi": {Provider: "pi", Installed: "1.3.0", ExecutablePath: "/opt/homebrew/bin/pi"},
		}
		cache.CLIIDs = []string{"pi"}
		cache.Settings = map[string]ProviderSetting{
			"pi": {ID: "pi", Name: "Pi", Kind: "cli", Enabled: true},
		}
		cache.AgyAccounts = []accounts.Account{{ID: "acc-1", Email: "agy@example.com"}}
		cache.AgyDefaultID = "acc-1"
		cache.AgyLocalLogin = true
	})

	path := providerStatusCachePath(dataDir)
	if path != filepath.Join(dataDir, providerStatusCacheFileName) {
		t.Fatalf("缓存路径 = %q，期望 %q", path, filepath.Join(dataDir, providerStatusCacheFileName))
	}

	cache, ok := loadProviderStatusCache(dataDir)
	if !ok {
		t.Fatal("期望能读回刚写入的缓存")
	}
	if cache.Version != providerStatusCacheVersion {
		t.Fatalf("缓存版本 = %d，期望 %d", cache.Version, providerStatusCacheVersion)
	}
	if cache.SavedAt == "" {
		t.Fatal("缓存应记录 SavedAt")
	}
	if !cache.Available["codex"] {
		t.Fatalf("Available 未保留：%+v", cache.Available)
	}
	if got := cache.Versions["pi"]; got.Installed != "1.3.0" || got.ExecutablePath != "/opt/homebrew/bin/pi" {
		t.Fatalf("Versions 未保留：%+v", got)
	}
	if len(cache.CLIIDs) != 1 || cache.CLIIDs[0] != "pi" {
		t.Fatalf("CLIIDs 未保留：%v", cache.CLIIDs)
	}
	if got := cache.Settings["pi"]; got.Name != "Pi" || got.Kind != "cli" {
		t.Fatalf("Settings 未保留：%+v", got)
	}
	if len(cache.AgyAccounts) != 1 || cache.AgyAccounts[0].ID != "acc-1" || cache.AgyDefaultID != "acc-1" || !cache.AgyLocalLogin {
		t.Fatalf("agy 快照未保留：%+v", cache)
	}

	// 增量更新：第二次写入不应丢掉第一次的字段
	updateProviderStatusCache(dataDir, func(cache *providerStatusCache) {
		cache.VersionErr = map[string]string{"claude": "boom"}
	})
	merged, ok := loadProviderStatusCache(dataDir)
	if !ok {
		t.Fatal("期望能读回合并后的缓存")
	}
	if merged.VersionErr["claude"] != "boom" || merged.Versions["pi"].Installed != "1.3.0" {
		t.Fatalf("增量写入丢字段：%+v", merged)
	}

	// 损坏内容
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadProviderStatusCache(dataDir); ok {
		t.Fatal("损坏的缓存应视为无效")
	}

	// 版本不符（例如旧版本写入的缓存）
	if err := os.WriteFile(path, []byte(`{"version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadProviderStatusCache(dataDir); ok {
		t.Fatal("版本不符的缓存应视为无效")
	}

	// 文件缺失
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := loadProviderStatusCache(dataDir); ok {
		t.Fatal("缺失的缓存应视为无效")
	}

	// 未指定 dataDir 时沿用 MCODE_DATA（与 ui-state.json 同目录）
	fallbackDir := t.TempDir()
	t.Setenv("MCODE_DATA", fallbackDir)
	t.Cleanup(waitUIStateWrites)
	if got := providerStatusCachePath(""); got != filepath.Join(fallbackDir, providerStatusCacheFileName) {
		t.Fatalf("空 dataDir 的回退路径 = %q", got)
	}
}

// (p) 缓存水合：不请求后端也能立即得到版本、可执行文件路径与 bin 草稿。
func TestProviders_ApplyStatusCacheSwarmtesRows(t *testing.T) {
	piSetting := ProviderSetting{ID: "pi", Name: "Pi", Kind: "cli", Enabled: true, Bin: "/custom/pi"}
	piSetting.PiCacheLong = true
	piSetting.PiVisionModel = "anthropic/claude-haiku-4-5"
	st := &SettingsState{}
	applyProviderStatusCache(st, providerStatusCache{
		Version:   providerStatusCacheVersion,
		Available: map[string]bool{"codex": true},
		Versions: map[string]ProviderVersionInfo{
			"pi": {Provider: "pi", Installed: "1.3.0", ExecutablePath: "/opt/homebrew/bin/pi"},
		},
		CLIIDs: []string{"pi"},
		Settings: map[string]ProviderSetting{
			"pi": piSetting,
		},
		Credits: map[string]ProviderCredits{
			"pi": {Provider: "pi", AmountText: "$1.00"},
		},
	})

	st.mu.Lock()
	loaded := st.providerRows.loaded
	ver := st.providerRows.versions["pi"]
	binDraft := st.providerRows.binDraft["pi"]
	visionDraft := st.providerRows.visionDraft["pi"]
	credits := st.providerRows.credits["pi"]
	cliIDs := append([]string(nil), st.providerRows.cliIDs...)
	st.mu.Unlock()

	if !loaded {
		t.Fatal("缓存水合后 providerRows.loaded 应为 true")
	}
	if ver.Installed != "1.3.0" || ver.ExecutablePath != "/opt/homebrew/bin/pi" {
		t.Fatalf("版本未水合：%+v", ver)
	}
	if binDraft != "/custom/pi" {
		t.Fatalf("bin 草稿应取已保存的 Bin，got %q", binDraft)
	}
	if visionDraft != "anthropic/claude-haiku-4-5" {
		t.Fatalf("视觉模型草稿应取已保存的值，got %q", visionDraft)
	}
	if credits.AmountText != "$1.00" {
		t.Fatalf("余额未水合：%+v", credits)
	}
	if len(cliIDs) != 1 || cliIDs[0] != "pi" {
		t.Fatalf("cliIDs 未水合：%v", cliIDs)
	}
	if !providerConfigured(st, "pi") {
		t.Fatal("缓存里有版本信息时 pi 应视为已配置")
	}
}

// (q) 后台刷新完成后把结果写回 provider-status-cache.json，供下次打开页面立即渲染。
func TestProviders_LoadWritesStatusCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "pi", Name: "Pi", Kind: "cli", Enabled: true},
	}
	fake.providerVersionResult = ProviderVersionInfo{
		Provider:       "pi",
		Installed:      "1.3.0",
		ExecutablePath: "/opt/homebrew/bin/pi",
	}
	fake.agyAccounts = []accounts.Account{{ID: "acc-1", Email: "agy@example.com", Status: accounts.StatusActive}}
	fake.agyDefaultAccountID = "acc-1"

	tt, st, dataDir := openProvidersPageDataDirTest(t, fake)

	waitFor(t, tt, func() bool {
		cache, ok := loadProviderStatusCache(dataDir)
		if !ok {
			return false
		}
		ver, hasVer := cache.Versions["pi"]
		return hasVer && ver.Installed == "1.3.0" && ver.ExecutablePath == "/opt/homebrew/bin/pi" &&
			len(cache.CLIIDs) == 1 && cache.CLIIDs[0] == "pi" &&
			len(cache.AgyAccounts) == 1 && cache.AgyDefaultID == "acc-1"
	})

	if st.dataDir != dataDir {
		t.Fatalf("测试用的数据目录不一致：%q vs %q", st.dataDir, dataDir)
	}
}

// (r) 版本探测只解析到路径（--version 失败）时仍算已安装，placeholder 显示检测到的路径。
func TestProviders_ResolvedPathCountsAsInstalled(t *testing.T) {
	if !providerVersionResolved(ProviderVersionInfo{ExecutablePath: "/opt/homebrew/bin/pi"}) {
		t.Fatal("解析到可执行文件路径时应视为已安装")
	}
	if providerVersionResolved(ProviderVersionInfo{}) {
		t.Fatal("既无版本号也无路径时不应视为已安装")
	}

	if got := providerBinPlaceholder(ProviderVersionInfo{ExecutablePath: " /opt/homebrew/bin/pi "}); got != "/opt/homebrew/bin/pi" {
		t.Fatalf("placeholder 应显示检测到的路径，got %q", got)
	}
	if got := providerBinPlaceholder(ProviderVersionInfo{}); got != "留空则从 PATH 查找" {
		t.Fatalf("未检测到路径时 placeholder 应回落，got %q", got)
	}

	st := &SettingsState{}
	st.providerRows.loaded = true
	st.providerRows.available = map[string]bool{"pi": false}
	st.providerRows.versions = map[string]ProviderVersionInfo{
		"pi": {Provider: "pi", ExecutablePath: "/opt/homebrew/bin/pi"},
	}
	if !providerConfigured(st, "pi") {
		t.Fatal("只解析到路径时 pi 也应视为已配置")
	}
	if got := providerRowSubtitle(st, "pi"); !strings.Contains(got, "已安装") {
		t.Fatalf("副标题应显示已安装，got %q", got)
	}
}

// (s) Command Code 的默认命令名是 cmd。
func TestProviders_CommandCodeDefaultBin(t *testing.T) {
	cases := map[string]string{
		"commandcode": "cmd",
		"pi":          "pi",
		"cline":       "cline",
		"copilot":     "copilot",
	}
	for id, want := range cases {
		if got := cliDefaultBin(id); got != want {
			t.Fatalf("cliDefaultBin(%q) = %q, want %q", id, got, want)
		}
	}
}

// (t) 展开 CLI harness 后版本信息行显示检测到的可执行文件路径。
func TestProviders_VersionRowShowsDetectedPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "pi", Name: "Pi", Kind: "cli", Enabled: true},
	}
	fake.providerVersionResult = ProviderVersionInfo{
		Provider:       "pi",
		Installed:      "1.3.0",
		ExecutablePath: "/opt/homebrew/bin/pi",
	}

	tt, st := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "pi", "")
	// 版本信息行在命令行节。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-cli")
	waitFor(t, tt, func() bool {
		return tt.HasText("可执行文件 /opt/homebrew/bin/pi")
	})
}

// (u) agy 行：账号管理器未接线但本机已有登录凭证时显示已登录，而不是「未登录」。
func TestProviders_AgyLocalLoginSubtitle(t *testing.T) {
	st := &SettingsState{}
	st.agy.localLogin = true

	if !providerConfigured(st, "agy") {
		t.Fatal("本机已登录时 agy 应视为已配置")
	}
	if got := providerRowSubtitle(st, "agy"); !strings.Contains(got, "已登录") {
		t.Fatalf("副标题应显示已登录，got %q", got)
	}

	empty := &SettingsState{}
	if providerConfigured(empty, "agy") {
		t.Fatal("无账号且本机未登录时 agy 不应视为已配置")
	}
	if got := providerRowSubtitle(empty, "agy"); got != "未登录 · 需要登录" {
		t.Fatalf("副标题 = %q, want 未登录 · 需要登录", got)
	}
}

// (u2) agy 行：账号列表里固定带一个系统账号，但只有本机存在凭证时它才算已登录账号。
func TestProviders_AgySystemAccountNeedsLocalLogin(t *testing.T) {
	st := &SettingsState{}
	st.agy.accounts = []accounts.Account{{ID: accounts.SystemAccountID, Status: accounts.StatusActive, On: true}}

	if got := providerRowSubtitle(st, "agy"); got != "未登录 · 需要登录" {
		t.Fatalf("只有系统账号且本机未登录时副标题 = %q, want 未登录 · 需要登录", got)
	}

	// 本机有凭证时系统账号才计入已登录账号数。
	st.agy.localLogin = true
	if got := providerRowSubtitle(st, "agy"); got != "已登录 1 个账号" {
		t.Fatalf("本机已登录时副标题 = %q, want 已登录 1 个账号", got)
	}

	// 非系统账号不受本机凭证影响。
	st.agy.localLogin = false
	st.agy.accounts = append(st.agy.accounts, accounts.Account{ID: "acc-1", Status: accounts.StatusActive})
	if got := providerRowSubtitle(st, "agy"); got != "已登录 1 个账号" {
		t.Fatalf("非系统账号不应受本机凭证影响，副标题 = %q, want 已登录 1 个账号", got)
	}
}

// (u3) agy 行：供应商列表确认 agy 没装、且没有任何账号时显示「未安装」而不是「未登录」。
func TestProviders_AgyNotInstalledSubtitle(t *testing.T) {
	st := &SettingsState{}
	st.providerRows.available = map[string]bool{"agy": false}

	if got := providerRowSubtitle(st, "agy"); got != "未安装 · 点击安装" {
		t.Fatalf("未安装且无账号时副标题 = %q, want 未安装 · 点击安装", got)
	}

	// 即使有已登录账号，确认缺少 agy 可执行文件时仍显示未安装。
	st.agy.accounts = []accounts.Account{{ID: "acc-1", Status: accounts.StatusActive, On: true}}
	if got := providerRowSubtitle(st, "agy"); got != "未安装 · 点击安装" {
		t.Fatalf("有账号但未安装时副标题 = %q, want 未安装 · 点击安装", got)
	}

	// 二进制存在时以账号为准。
	st.providerRows.available["agy"] = true
	if got := providerRowSubtitle(st, "agy"); got != "已登录 1 个账号" {
		t.Fatalf("已安装且有账号时副标题 = %q, want 已登录 1 个账号", got)
	}

	// 供应商列表还没拉到时不能当成未安装，保持原来的未登录文案。
	unknown := &SettingsState{}
	if got := providerRowSubtitle(unknown, "agy"); got != "未登录 · 需要登录" {
		t.Fatalf("安装状态未知时副标题 = %q, want 未登录 · 需要登录", got)
	}
}

// (v) 设置读取失败时保留上一次的缓存快照，避免把一次失败的加载固化下来。
func TestProviders_FailedLoadKeepsStatusCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dataDir := settingsTestDir(t)

	updateProviderStatusCache(dataDir, func(cache *providerStatusCache) {
		cache.Versions = map[string]ProviderVersionInfo{
			"pi": {Provider: "pi", Installed: "1.3.0"},
		}
	})

	fake := NewFakeClient()
	fake.providerSettingsErr = errors.New("读取设置失败")

	tt, st, _ := openProvidersPageDataDirTest(t, fake)

	// 等后台刷新结束（点击进入页面时 loading 为 true，刷新收尾时置回 false）
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.providerRows.loading
	})

	cache, ok := loadProviderStatusCache(dataDir)
	if !ok {
		t.Fatal("期望缓存仍可读取")
	}
	if got := cache.Versions["pi"].Installed; got != "1.3.0" {
		t.Fatalf("失败的加载不应覆盖缓存快照，got %q", got)
	}
}

// (w) 供应商设置读取失败时在区块顶部提示，并保留上次的 CLI 行（不能整页变「未安装」或整块消失）。
func TestProviders_SettingsLoadFailureShowsError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.mu.Lock()
	fake.providerSettings = []ProviderSetting{{ID: "pi", Name: "Pi", Kind: "cli", Enabled: true}}
	fake.mu.Unlock()

	tt, st, _ := openProvidersPageDataDirTest(t, fake)
	waitFor(t, tt, func() bool { return tt.HasText("Pi") })
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.providerRows.loading
	})

	fake.mu.Lock()
	fake.providerSettingsErr = errors.New("读取设置失败")
	fake.mu.Unlock()
	st.loadProviderRows()

	waitFor(t, tt, func() bool { return tt.HasText("供应商设置加载失败：读取设置失败") })
	if !tt.HasText("Pi") {
		t.Fatal("设置读取失败后 CLI 行不应整块消失")
	}
}

// Codex 经 Magpie 时限额面板按卡片账号分组，套餐用卡片自带的，不拼本机登录套餐；
// 本机登录账号单独标成「本机登录」。
func TestProviders_CodexMagpieQuotaGroupsPerAccount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	readAt := time.Now().Add(-2 * time.Minute)
	fake := NewFakeClient()
	fake.providerAccountResult = ProviderAccountInfo{Provider: "codex", Email: "a@b.com", Plan: "Pro 20x"}
	fake.providerQuotaResult = []ProviderQuotaWindow{
		{ID: "a@b.com/5 hours", Period: "5h", Label: "5 hours", UsedPercent: 85,
			Account: "a@b.com", Source: "magpie", Plan: "Plus", ReadAt: readAt},
		{ID: "a@b.com/7 days", Period: "7d", Label: "7 days", UsedPercent: 40,
			Account: "a@b.com", Source: "magpie", Plan: "Plus", ReadAt: readAt},
		{ID: "{{EMAIL_d949a3a8}}/30 days", Period: "30d", Label: "30 days", UsedPercent: 0,
			Account: "{{EMAIL_d949a3a8}}", Source: "magpie", Plan: "Free"},
	}

	tt, _ := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "codex", "")

	waitFor(t, tt, func() bool {
		return tt.HasText("方案用量限额 · a@b.com · Plus") &&
			tt.HasText("方案用量限额 · {{EMAIL_d949a3a8}} · Free")
	})
	if tt.HasText("方案用量限额 · Pro 20x") {
		t.Fatalf("本机登录套餐不能拼到 Magpie 额度上，texts: %q", tt.Texts())
	}
	if !tt.HasText("5 hours") || !tt.HasText("30 days") {
		t.Fatalf("两张卡片的窗口都应展示，texts: %q", tt.Texts())
	}
	if !tt.HasText("本机登录") {
		t.Fatalf("本机账号应明确标为「本机登录」，texts: %q", tt.Texts())
	}
}

// 直连 OpenAI 时限额面板沿用本机登录套餐（与 Magpie 卡片分组互不影响）。
func TestProviders_CodexDirectQuotaUsesLocalPlan(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerAccountResult = ProviderAccountInfo{Provider: "codex", Email: "a@b.com", Plan: "Pro 20x"}
	fake.providerQuotaResult = []ProviderQuotaWindow{
		{ID: "codex-primary", Period: "5h", Label: "5-hour limit", UsedPercent: 25, Source: "openai",
			ReadAt: time.Now().Add(-time.Minute)},
		{ID: "codex-secondary", Period: "7d", Label: "Weekly", UsedPercent: 60, Source: "openai"},
	}

	tt, _ := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "codex", "")
	waitFor(t, tt, func() bool { return tt.HasText("方案用量限额 · Pro 20x") })
}

// 刷新失败保留旧窗口，同时展示错误与过期标记；恢复后用新窗口替换并清除错误。
func TestProviders_QuotaRefreshFailureAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		id     string
		source string
	}{
		{id: "codex", source: "openai"},
		{id: "codex", source: "magpie"},
		{id: "claude"},
		{id: "copilot"},
	} {
		t.Run(tc.id+"/"+tc.source, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			oldQuota := []ProviderQuotaWindow{{
				ID: "primary", Label: "旧额度窗口", UsedPercent: 37,
				Source: tc.source, Account: "quota@example.com", Plan: "Plus",
				ReadAt: time.Now().Add(-usageStaleAfter - time.Minute),
			}}
			fake := NewFakeClient()
			fake.providerSettings = []ProviderSetting{{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true}}
			fake.providerQuotaResult = oldQuota
			tt, st := openProvidersPageTest(t, fake)
			waitFor(t, tt, func() bool {
				st.mu.Lock()
				defer st.mu.Unlock()
				return !st.providerRows.loading
			})
			clickProviderDetailTabForTest(t, tt, tc.id, "账号与额度")
			waitFor(t, tt, func() bool { return tt.HasText("旧额度窗口") })

			fake.mu.Lock()
			fake.providerQuotaErr = errors.New("额度服务暂时不可用")
			fake.mu.Unlock()
			st.loadProviderRows()
			waitFor(t, tt, func() bool {
				st.mu.Lock()
				defer st.mu.Unlock()
				return !st.providerRows.loading && st.providerRows.quotaErr[tc.id] != ""
			})
			waitFor(t, tt, func() bool { return tt.HasText("加载额度失败: 额度服务暂时不可用") })
			st.mu.Lock()
			quota := st.providerRows.quota[tc.id]
			st.mu.Unlock()
			if !reflect.DeepEqual(quota, oldQuota) {
				t.Fatalf("刷新失败应保留完整旧窗口，got %+v, want %+v", quota, oldQuota)
			}
			if !tt.HasText("旧额度窗口") || !tt.HasText("37%") || !tt.HasText(usageStaleSuffix) {
				t.Fatalf("刷新失败应同时展示旧额度与过期标记，texts: %q", tt.Texts())
			}

			newQuota := []ProviderQuotaWindow{{ID: "primary", Label: "新额度窗口", UsedPercent: 12, ReadAt: time.Now()}}
			fake.mu.Lock()
			fake.providerQuotaErr = nil
			fake.providerQuotaResult = newQuota
			fake.mu.Unlock()
			st.loadProviderRows()
			waitFor(t, tt, func() bool {
				st.mu.Lock()
				defer st.mu.Unlock()
				return !st.providerRows.loading && reflect.DeepEqual(st.providerRows.quota[tc.id], newQuota)
			})
			waitFor(t, tt, func() bool { return tt.HasText("新额度窗口") && tt.HasText("12%") })
			st.mu.Lock()
			errText := st.providerRows.quotaErr[tc.id]
			st.mu.Unlock()
			if errText != "" || tt.HasText("加载额度失败: 额度服务暂时不可用") || tt.HasText(usageStaleSuffix) || tt.HasText("旧额度窗口") {
				t.Fatalf("刷新恢复后应清除错误、旧额度与过期标记，quotaErr=%q, texts: %q", errText, tt.Texts())
			}
		})
	}
}

// 首次加载没有旧窗口时，额度读取失败仍须显示说明，不能静默隐藏。
func TestProviders_QuotaInitialFailureShowsError(t *testing.T) {
	for _, id := range []string{"codex", "claude", "copilot"} {
		t.Run(id, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fake := NewFakeClient()
			fake.providerSettings = []ProviderSetting{{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true}}
			fake.providerQuotaErr = errors.New("额度服务暂时不可用")
			tt, st := openProvidersPageTest(t, fake)
			clickProviderDetailTabForTest(t, tt, id, "账号与额度")
			waitFor(t, tt, func() bool { return tt.HasText("加载额度失败: 额度服务暂时不可用") })
			st.mu.Lock()
			quota := st.providerRows.quota[id]
			st.mu.Unlock()
			if len(quota) != 0 || tt.HasText("0%") || tt.HasText(usageStaleSuffix) {
				t.Fatalf("无旧数据时不应伪造额度或过期标记，quota=%+v, texts: %q", quota, tt.Texts())
			}
		})
	}
}

// 直连额度按来源 ReadAt 判断过期，不能因刚完成设置页刷新就当作新数据。
func TestProviders_DirectQuotaReadAtStale(t *testing.T) {
	for _, id := range []string{"codex", "claude", "copilot"} {
		t.Run(id, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			fake := NewFakeClient()
			fake.providerSettings = []ProviderSetting{{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true}}
			fake.providerQuotaResult = []ProviderQuotaWindow{
				{ID: "old", Label: "过期窗口", UsedPercent: 43, ReadAt: time.Now().Add(-usageStaleAfter - time.Minute)},
				{ID: "fresh", Label: "新鲜窗口", UsedPercent: 21, ReadAt: time.Now()},
				{ID: "unknown", Label: "未知时间窗口", UsedPercent: 9},
			}
			tt, _ := openProvidersPageTest(t, fake)
			clickProviderDetailTabForTest(t, tt, id, "账号与额度")
			waitFor(t, tt, func() bool { return tt.HasText("过期窗口") && tt.HasText(usageStaleSuffix) })
			if count := strings.Count(strings.Join(tt.Texts(), "\n"), usageStaleSuffix); count != 1 {
				t.Fatalf("仅过期窗口应带标记，got %d, texts: %q", count, tt.Texts())
			}
			if !tt.HasText("43%") || !tt.HasText("21%") || !tt.HasText("9%") {
				t.Fatalf("所有窗口的额度都应保留展示，texts: %q", tt.Texts())
			}
		})
	}
}

// 登录态：claude 行摘要按四种状态推导，其他 Provider 行不受影响。
func TestProviders_ProviderStatusSummaryClaudeLogin(t *testing.T) {
	cases := []struct {
		name    string
		login   string
		ready   bool
		summary string
	}{
		{
			name:    "已登录",
			login:   ProviderLoginStateLoggedIn,
			ready:   true,
			summary: "已安装 · v1.2.0 · 已登录",
		},
		{
			name:    "未登录",
			login:   ProviderLoginStateLoggedOut,
			summary: "已安装 · v1.2.0 · 未登录（请在终端运行 claude /login）",
		},
		{
			name:    "检测未上报",
			login:   ProviderLoginStateUnknown,
			ready:   true,
			summary: "已安装 · v1.2.0",
		},
		{
			name:    "不需要登录",
			login:   ProviderLoginStateNotRequired,
			ready:   true,
			summary: "已安装 · v1.2.0",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := providerStatusSummary(providerStatusInput{
				Kind:       providerKindCLI,
				Available:  true,
				LoginState: tc.login,
				Version:    ProviderVersionInfo{Installed: "1.2.0"},
			})
			if status.Summary != tc.summary {
				t.Fatalf("summary = %q, want %q", status.Summary, tc.summary)
			}
			if status.Ready != tc.ready {
				t.Fatalf("ready = %v, want %v", status.Ready, tc.ready)
			}
		})
	}
}

// 登录态：providerStatusFor 从 providerRows.login 取状态，claude 行变、codex 行不变。
func TestProviders_ClaudeLoginStateFromRows(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	st := &SettingsState{}
	st.providerRows.loaded = true
	st.providerRows.available = map[string]bool{"claude": true, "codex": true}
	st.providerRows.versions = map[string]ProviderVersionInfo{
		"claude": {Provider: "claude", Installed: "1.2.0"},
		"codex":  {Provider: "codex", Installed: "0.5.0"},
	}
	st.providerRows.login = map[string]string{"claude": ProviderLoginStateLoggedOut}

	claudeStatus := providerStatusFor(st, "claude")
	if claudeStatus.Ready {
		t.Fatal("未登录的 claude 行不应标记为可用")
	}
	if want := "已安装 · v1.2.0 · 未登录（请在终端运行 claude /login）"; providerRowSubtitle(st, "claude") != want {
		t.Fatalf("claude subtitle = %q, want %q", providerRowSubtitle(st, "claude"), want)
	}
	if want := "已安装 · v0.5.0"; providerRowSubtitle(st, "codex") != want {
		t.Fatalf("codex subtitle = %q, want %q", providerRowSubtitle(st, "codex"), want)
	}
}

// loginStateClient 给 FakeClient 补上 providerLoginStateReader 这个可选能力，
// 用于验证设置页在后台探测路径里读取并保存登录态。
type loginStateClient struct {
	*FakeClient
	states map[string]string
}

func (c *loginStateClient) ProviderLoginState(ctx context.Context, provider string) string {
	return c.states[provider]
}

// 登录态：可选接口实现后，后台刷新把 claude 登录态写进 providerRows.login。
func TestProviders_LoadProviderRowsReadsLoginState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	client := &loginStateClient{
		FakeClient: NewFakeClient(),
		states:     map[string]string{"claude": ProviderLoginStateLoggedOut},
	}
	client.providerVersionResult = ProviderVersionInfo{Provider: "claude", Installed: "1.2.0"}

	dataDir := settingsTestDir(t)
	var st *SettingsState
	tt := ui.NewTester(Root(client, WithSettings(dataDir, nil), func(s *chatState) {
		st = getChatSettings(s)
	}), 1200, 2400)
	waitFor(t, tt, func() bool { return tt.HasText("设置") })
	if err := tt.Click("设置"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("模型与供应商"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.providerRows.loaded && st.providerRows.login["claude"] != ""
	})

	st.mu.Lock()
	login := st.providerRows.login["claude"]
	st.mu.Unlock()
	if login != ProviderLoginStateLoggedOut {
		t.Fatalf("providerRows.login[claude] = %q, want %q", login, ProviderLoginStateLoggedOut)
	}
	if sub := providerRowSubtitle(st, "claude"); !strings.Contains(sub, "未登录") {
		t.Fatalf("claude subtitle 应提示未登录，got %q", sub)
	}
	if sub := providerRowSubtitle(st, "codex"); strings.Contains(sub, "未登录") {
		t.Fatalf("codex 行不应展示登录态，got %q", sub)
	}
}

// TestProviders_TrayQuotaSwitchToggle 覆盖：关闭某个供应商的菜单栏额度开关后，st.cfg.Startup.TrayQuotaHidden 包含该 id。
func TestProviders_TrayQuotaSwitchToggle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	tt, st := openProvidersPageTest(t, fake)

	// 初始状态下全部展示（TrayQuotaHidden 为空）
	if !trayQuotaShown(st.cfg, "claude") {
		t.Fatalf("初始状态 claude 菜单栏额度开关应开启")
	}

	clickProviderDetailTabForTest(t, tt, "claude", "")
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-account")
	if _, ok := tt.Find("菜单栏额度"); !ok {
		t.Fatalf("账号区缺少菜单栏额度开关: %v", tt.Texts())
	}

	// 切换关闭 claude 的菜单栏额度开关（开关 Label 与左侧说明同名，按文字点击会落在说明上，
	// 这里沿用状态辅助函数直接切换）。
	setTrayQuotaShown(st, "claude", false)
	tt.Frame()

	st.cfgMu.Lock()
	hidden := append([]string(nil), st.cfg.Startup.TrayQuotaHidden...)
	st.cfgMu.Unlock()

	found := false
	for _, id := range hidden {
		if id == "claude" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("关闭 claude 后 st.cfg.Startup.TrayQuotaHidden 应包含 claude，got: %v", hidden)
	}
	if trayQuotaShown(st.cfg, "claude") {
		t.Fatalf("trayQuotaShown(st.cfg, claude) 应为 false")
	}
}

// (j2) pi 表单里有「长缓存」开关与「视觉模型」输入框：改动后保存会带上新值。
func TestProviders_PiTuningRowsSaveValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "pi", Name: "Pi", Kind: "cli", Enabled: true},
	}

	tt, st := openProvidersPageTest(t, fake)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.providerRows.settings) == 1
	})

	clickProviderDetailTabForTest(t, tt, "pi", "")
	// pi 调优行在命令行节。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-cli")
	if !tt.HasText("长缓存") {
		t.Fatalf("pi 表单应展示长缓存开关，当前文本：%v", tt.Texts())
	}
	if !tt.HasText("视觉模型") {
		t.Fatalf("pi 表单应展示视觉模型输入框，当前文本：%v", tt.Texts())
	}

	// 打开长缓存开关：保存的设置里带 PiCacheLong。
	if err := tt.Click("切换长缓存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, s := range fake.saveProviderSettingCalls {
			if s.ID == "pi" && s.PiCacheLong {
				return true
			}
		}
		return false
	})
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return !st.providerRows.saving["pi"]
	})

	// 视觉模型草稿保存前去掉首尾空白。
	st.mu.Lock()
	st.providerRows.visionDraft["pi"] = " anthropic/claude-haiku-4-5 "
	st.mu.Unlock()
	saveProviderRowVision(st, "pi")

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		for _, s := range fake.saveProviderSettingCalls {
			if s.ID == "pi" && s.PiVisionModel == "anthropic/claude-haiku-4-5" {
				return true
			}
		}
		return false
	})

	st.mu.Lock()
	draft := st.providerRows.visionDraft["pi"]
	st.mu.Unlock()
	if draft != "anthropic/claude-haiku-4-5" {
		t.Fatalf("保存后草稿应同步为保存结果，got %q", draft)
	}
}

// (j3) 非 pi 的 Provider 表单里不出现 pi 专有设置。
func TestProviders_PiTuningRowsOnlyForPi(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{
		{ID: "copilot", Name: "GitHub Copilot", Kind: "cli", Enabled: true},
	}

	tt, st := openProvidersPageTest(t, fake)
	clickProviderDetailTabForTest(t, tt, "copilot", "")
	// 切到命令行节检查 pi 专有设置不会出现在其他 Provider 中。
	selectProviderSheetSectionForTest(t, tt, st, "sec-provider-cli")
	waitFor(t, tt, func() bool { return tt.HasText("可执行文件路径") })

	if tt.HasText("长缓存") || tt.HasText("视觉模型") {
		t.Fatalf("非 pi 的 Provider 表单不应出现 pi 专有设置，当前文本：%v", tt.Texts())
	}
}

// TestSettingsPageProviders_AgentMagpieSettingsLink 覆盖跳转入口和模型只读目录。
func TestSettingsPageProviders_AgentMagpieSettingsLink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "测试网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true, HasAPIKey: true}}
	fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
	fake.SetAgentMagpie(routeTestSetting("claude", agentMagpieModeOff))
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(&routeTestClient{FakeClient: fake}))
	lib := &modelsLibraryView{providers: []ProviderInfo{{ID: "claude", Name: "Claude", Models: []ModelInfo{{ID: "claude-test", Name: "Claude 测试模型"}}}}}
	id := "claude"
	panel := ui.NewTester(func(c *ui.Context) {
		for _, def := range cliProviderSheetSections(st, id, lib) {
			def.Render(c, st)
		}
	}, 1000, 4000)
	waitMagpie(t, panel, func() bool {
		connected, known := magpieConnected(st)
		value, routeKnown := magpieAgentRouteValue(st, "claude")
		return known && connected && routeKnown && value == "直连"
	})
	panel.Frame()
	if !panel.HasText("Magpie 接入") || !panel.HasText("直连") || !panel.HasText("Claude 测试模型") {
		t.Fatalf("Claude 模型节应显示跳转行和目录模型：%v", panel.Texts())
	}
	if panel.HasText("仅 mcode") || panel.HasText("全局") {
		t.Fatalf("供应商模型节不应显示 Magpie 模式分段：%v", panel.Texts())
	}
	// 跳转按钮在行右侧，点按钮内的路由值验证点击穿透到按钮。
	if err := panel.Click("直连"); err != nil {
		t.Fatalf("点击 Magpie 接入失败：%v", err)
	}
	st.mu.Lock()
	page := st.currentPage
	st.mu.Unlock()
	if page != "magpie" {
		t.Fatalf("点击 Magpie 接入后页面 = %q，期望 magpie", page)
	}

	id = "cline"
	cline := ui.NewTester(func(c *ui.Context) {
		for _, def := range cliProviderSheetSections(st, id, lib) {
			def.Render(c, st)
		}
	}, 1000, 4000)
	cline.Frame()
	if cline.HasText("Magpie 接入") {
		t.Fatalf("Cline 不应显示 Magpie 设置行：%v", cline.Texts())
	}
}

// TestSettingsPageProviders_CodexMagpieSettingsRow 覆盖 Codex 的只读 Magpie 接入行。
func TestSettingsPageProviders_CodexMagpieSettingsRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.SetAgentMagpie(routeTestSetting("codex", agentMagpieModeOff))
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	openProviderSheet(st, "cli", "codex")
	panel := ui.NewTester(func(c *ui.Context) { renderCLIProviderSheetSections(c, st, "codex", nil) }, 1000, 4000)
	waitFor(t, panel, func() bool { value, known := magpieAgentRouteValue(st, "codex"); return known && value == "直连" })
	if !panel.HasText("Magpie 接入") || !panel.HasText("直连") || panel.HasText("全局") || panel.HasText("仅 mcode") {
		t.Fatalf("Codex 模型节应显示只读 Magpie 接入行：%v", panel.Texts())
	}
}

// groupProviderIDs 按「有开关且启用 / 确定未安装 / 其余已安装」分三组，组内保持原顺序。
func TestProviders_GroupProviderIDs(t *testing.T) {
	cases := []struct {
		name        string
		ids         []string
		inputs      map[string]providerGroupInput
		wantEnabled []string
		wantOff     []string
		wantMissing []string
	}{
		{
			name: "按开关与安装状态分组",
			ids:  []string{"claude", "codex", "agy", "pi", "copilot"},
			inputs: map[string]providerGroupInput{
				"claude":  {HasSwitch: true, Enabled: true, Ready: true},
				"codex":   {HasSwitch: true, Ready: true},
				"agy":     {},
				"pi":      {HasSwitch: true, NotInstalled: true},
				"copilot": {HasSwitch: true, NotInstalled: true},
			},
			wantEnabled: []string{"claude"},
			wantOff:     []string{"codex", "agy"},
			wantMissing: []string{"pi", "copilot"},
		},
		{
			name: "没有开关的供应商就绪时算已启用",
			ids:  []string{"agy", "pi"},
			inputs: map[string]providerGroupInput{
				"agy": {Ready: true},
				"pi":  {HasSwitch: true},
			},
			wantEnabled: []string{"agy"},
			wantOff:     []string{"pi"},
		},
		{
			name: "开关已启用优先于未安装判定",
			ids:  []string{"pi"},
			inputs: map[string]providerGroupInput{
				"pi": {HasSwitch: true, Enabled: true, NotInstalled: true},
			},
			wantEnabled: []string{"pi"},
		},
		{
			name: "探测结果未知（加载中）不算未安装",
			ids:  []string{"pi", "copilot"},
			inputs: map[string]providerGroupInput{
				"pi":      {HasSwitch: true},
				"copilot": {HasSwitch: true, Enabled: true},
			},
			wantEnabled: []string{"copilot"},
			wantOff:     []string{"pi"},
		},
		{
			name: "空输入",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			enabled, installedOff, uninstalled := groupProviderIDs(tc.ids, tc.inputs)
			if !reflect.DeepEqual(enabled, tc.wantEnabled) {
				t.Fatalf("enabled = %v, want %v", enabled, tc.wantEnabled)
			}
			if !reflect.DeepEqual(installedOff, tc.wantOff) {
				t.Fatalf("installedOff = %v, want %v", installedOff, tc.wantOff)
			}
			if !reflect.DeepEqual(uninstalled, tc.wantMissing) {
				t.Fatalf("uninstalled = %v, want %v", uninstalled, tc.wantMissing)
			}
		})
	}
}

// 未安装的供应商也出现在统一列表里。
func TestProviders_UninstalledGroupCollapsedByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{{ID: "pi", Name: "Pi", Kind: "cli"}, {ID: "copilot", Name: "GitHub Copilot", Kind: "cli"}}
	fake.SetProviders([]ProviderInfo{{ID: "pi", Available: false}, {ID: "copilot", Available: false}})
	tt, _ := openProvidersPageTest(t, fake)
	if _, ok := tt.Find("Pi"); !ok {
		t.Fatalf("供应商列表缺少未安装的 Pi: %v", tt.Texts())
	}
	if _, ok := tt.Find("GitHub Copilot"); !ok {
		t.Fatalf("供应商列表缺少未安装的 GitHub Copilot: %v", tt.Texts())
	}
}

// Claude 面板的菜单栏额度开关位于账号节。
func TestProviders_TrayQuotaSwitchLivesInAccountTab(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	tt, st := openProvidersPageTest(t, NewFakeClient())
	clickProviderDetailTabForTest(t, tt, "claude", "")
	panel := ui.NewTester(func(c *ui.Context) { renderCLIProviderSheetSections(c, st, "claude", nil) }, 1000, 4000)
	panel.Frame()
	account, ok := panel.Find("设置卡片 sec-provider-account")
	if !ok {
		t.Fatalf("缺少账号节: %v", panel.Texts())
	}
	quota, ok := panel.Find("菜单栏额度")
	if !ok || quota.Y <= account.Y {
		t.Fatalf("菜单栏额度应在账号节中: %v", panel.Texts())
	}
	cli, ok := panel.Find("设置卡片 sec-provider-cli")
	if !ok || quota.Y >= cli.Y {
		t.Fatalf("菜单栏额度应位于命令行节之前: %v", panel.Texts())
	}
}

// 菜单栏额度行的文字与开关都应位于账号卡片的标准左右内边距内。
func TestProviders_TrayQuotaSwitchRespectsAccountCardInsets(t *testing.T) {
	for _, tc := range []struct {
		name  string
		width int
	}{
		{name: "窄面板", width: 360},
		{name: "宽面板", width: 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
			panel := ui.NewTester(func(c *ui.Context) {
				ui.Column(c.Key("test-provider-sheet")).FillWidth().Children(func() {
					renderCLIProviderSheetSections(c, st, "claude", nil)
				})
			}, tc.width, 4000)
			panel.Frame()
			account, ok := panel.Find("设置卡片 sec-provider-account")
			if !ok {
				t.Fatalf("缺少账号卡片: %v", panel.Texts())
			}
			quota, ok := panel.Find("菜单栏额度")
			if !ok {
				t.Fatalf("缺少菜单栏额度文字: %v", panel.Texts())
			}
			if quota.X < account.X+settingsRowPadX {
				t.Errorf("菜单栏额度文字未遵守左内边距: text=%+v card=%+v", quota, account)
			}
			switcher, ok := panel.Find("在菜单栏显示额度")
			if !ok {
				t.Fatalf("缺少菜单栏额度开关: %v", panel.Texts())
			}
			if switcher.X+switcher.W > account.X+account.W-settingsRowPadX {
				t.Errorf("菜单栏额度开关超出右内边距: switch=%+v card=%+v", switcher, account)
			}
		})
	}
}

// 逐项搜索跳转：供应商面板内的条目映射到对应供应商。
func TestSettingsItemProviderSheet(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		{id: "row-agy-terminal-login", want: "agy"},
		{id: "sec-agy-auto-warmup", want: "agy"},
		{id: "sec-agy-routing-strategy", want: "agy"},
		{id: "sec-agy-routing-affinity", want: "agy"},
		{id: "sec-agy-routing-sink", want: "agy"},
		{id: "row-claude-bin", want: "claude"},
		{id: "row-codex-bin", want: "codex"},
		{id: "row-provider-agy", want: ""},
		{id: "", want: ""},
	}
	for _, tc := range cases {
		if got := settingsItemProviderSheet(tc.id); got != tc.want {
			t.Fatalf("settingsItemProviderSheet(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// Cline 供应商面板不显示 Magpie 设置行。
func Test供应商面板_Cline不显示Magpie设置行(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.providerSettings = []ProviderSetting{{ID: "cline", Name: "Cline", Kind: "cli", Enabled: true}}
	fake.SetAgentMagpie(AgentMagpieSetting{Agent: "cline", Mode: agentMagpieModeOff})
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(fake))
	openProviderSheet(st, "cli", "cline")
	tt := ui.NewTester(func(c *ui.Context) { renderProviderSheetBody(c, st) }, 1200, 4000)
	if tt.HasText("Magpie 接入") {
		t.Fatalf("Cline 不应显示 Magpie 设置行: %v", tt.Texts())
	}
}

// claude 的节定义顺序固定：账号 → 模型 → 命令行，命令行归入高级。
func TestCLIProviderSheetSectionsOrder(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	lib := &modelsLibraryView{providers: []ProviderInfo{{ID: "claude", Models: []ModelInfo{{ID: "test-model", Name: "测试模型"}}}}}
	defs := cliProviderSheetSections(st, "claude", lib)
	wantIDs := []string{"sec-provider-account", "sec-provider-models", "sec-provider-cli"}
	wantTitles := []string{"账号", "模型", "命令行"}
	if len(defs) != len(wantIDs) {
		t.Fatalf("节数量 = %d, want %d", len(defs), len(wantIDs))
	}
	for i, def := range defs {
		if def.ID != wantIDs[i] || def.Title != wantTitles[i] {
			t.Fatalf("第 %d 节 = %q/%q, want %q/%q", i, def.ID, def.Title, wantIDs[i], wantTitles[i])
		}
		if def.Render == nil {
			t.Fatalf("第 %d 节 %q 缺少 Render", i, def.ID)
		}
	}
}

// Magpie 网关已连接时，非 Magpie agent 的 CLI 供应商仍显示自己的模型目录，不套用 Magpie 勾选面板。
func TestCLIProviderSheetSectionsNonMagpieAgentKeepsOwnModels(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fake := NewFakeClient()
	fake.customEndpoints = []CustomEndpoint{{ID: "gw", Name: "测试网关", BaseURL: "https://example.com/v1", Enabled: true, Magpie: true, HasAPIKey: true}}
	fake.SetPiMagpieEndpoints([]PiMagpieEndpoint{{ID: "gw"}})
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(&routeTestClient{FakeClient: fake}))
	lib := &modelsLibraryView{providers: []ProviderInfo{{ID: "antigravity", Name: "Antigravity", Models: []ModelInfo{{ID: "gemini-3-pro", Name: "Gemini 3 Pro"}}}}}
	tt := ui.NewTester(func(c *ui.Context) {
		for _, def := range cliProviderSheetSections(st, "antigravity", lib) {
			def.Render(c, st)
		}
	}, 1000, 4000)
	waitMagpie(t, tt, func() bool { connected, known := magpieConnected(st); return known && connected })
	tt.Frame()
	if !tt.HasText("Gemini 3 Pro") {
		t.Fatalf("antigravity 模型节未显示自身目录: %v", tt.Texts())
	}
	for _, unwanted := range []string{"先在下方勾选 Magpie 模型", "仅 mcode"} {
		if tt.HasText(unwanted) {
			t.Fatalf("antigravity 模型节不应出现 %q: %v", unwanted, tt.Texts())
		}
	}
}

// 供应商面板的身份区主操作：未安装给「安装」，有更新给「更新」，已是最新不给按钮。
func TestProviderSheetPrimaryAction(t *testing.T) {
	cases := []struct {
		name       string
		available  bool
		version    ProviderVersionInfo
		wantLabel  string
		wantAction string
		wantOK     bool
	}{
		{
			name:       "未安装",
			available:  false,
			version:    ProviderVersionInfo{},
			wantLabel:  "安装",
			wantAction: "install",
			wantOK:     true,
		},
		{
			name:       "有更新",
			available:  true,
			version:    ProviderVersionInfo{Installed: "1.2.0", Latest: "1.3.0", UpdateAvailable: true},
			wantLabel:  "更新",
			wantAction: "update",
			wantOK:     true,
		},
		{
			name:      "已是最新",
			available: true,
			version:   ProviderVersionInfo{Installed: "1.3.0"},
			wantOK:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &SettingsState{}
			st.providerRows = providerRowsState{
				available: map[string]bool{"claude": tc.available},
				versions:  map[string]ProviderVersionInfo{"claude": tc.version},
			}
			label, action, ok := providerSheetPrimaryAction(st, "claude")
			if ok != tc.wantOK || label != tc.wantLabel || action != tc.wantAction {
				t.Fatalf("providerSheetPrimaryAction() = %q/%q/%v, want %q/%q/%v", label, action, ok, tc.wantLabel, tc.wantAction, tc.wantOK)
			}
		})
	}
}

// selectProviderSheetSectionForTest 确保指定节可见；常用节无需操作，高级节先展开。
func selectProviderSheetSectionForTest(t *testing.T, tt *ui.Tester, st *SettingsState, sectionID string) {
	t.Helper()
	kind, id := currentProviderSheet(st)
	if kind == "" || id == "" {
		t.Fatalf("没有打开的供应商面板，无法切到节 %q", sectionID)
	}
	switch sectionID {
	case "sec-provider-cli", "sec-agy-routing", "sec-agy-advanced-login":
		if !providerSheetAdvancedOpen(st, kind, id) {
			// 长面板的折叠按钮可能在可视区外，坐标点击会落到遮罩；点击行为由 Claude 面板测试单独覆盖。
			setProviderSheetAdvancedOpen(st, kind, id, true)
			tt.Frame()
			waitFor(t, tt, func() bool { return tt.HasText("收起高级") })
		}
	}
	tt.Frame()
}
