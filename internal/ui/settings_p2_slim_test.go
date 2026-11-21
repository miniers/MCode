package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/config"
	"mcode/internal/reviewcheck"
	"mcode/internal/testenv"
)

func TestSettingsP2Slim_NinePagesAndThreeGroups(t *testing.T) {
	pages := settingsPages()
	wantIDs := []string{"magpie", "general", "remote", "models", "conversation", "swarm", "extensions", "workbench", "about"}
	wantGroups := []string{"", "应用", "应用", "代理", "代理", "代理", "代理", "工作区", ""}
	if len(pages) != len(wantIDs) {
		t.Fatalf("一级设置页数量 = %d，期望 %d", len(pages), len(wantIDs))
	}
	for i := range wantIDs {
		if pages[i].id != wantIDs[i] || pages[i].group != wantGroups[i] {
			t.Fatalf("第 %d 页 = %s/%q，期望 %s/%q", i+1, pages[i].id, pages[i].group, wantIDs[i], wantGroups[i])
		}
	}

	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.open = true
	tt := ui.NewTester(func(c *ui.Context) { renderSettingsNav(c, st) }, 320, 1000)
	tt.Frame()

	for _, want := range []string{"连接 Magpie", "应用", "通用", "远程控制", "代理", "模型与供应商", "会话", "编排", "扩展", "工作区", "工作台", "关于"} {
		if !tt.HasText(want) {
			t.Fatalf("设置导航缺少 %q: %v", want, tt.Texts())
		}
	}
	for _, removed := range []string{"外观", "总管", "记忆", "版本控制", "存储"} {
		if tt.HasText(removed) {
			t.Fatalf("精简后的导航不应再显示独立页 %q: %v", removed, tt.Texts())
		}
	}
}

func TestSettingsP2Slim_LegacyPageDeepLinksAndJump(t *testing.T) {
	cases := []struct {
		legacy   string
		wantPage string
		wantJump string
	}{
		{"appearance", "general", "row-theme"},
		{"steward", "swarm", "row-steward-enabled"},
		{"nowledge", "extensions", "row-memory-backend"},
		{"sourceControl", "workbench", "row-sc-generate-commit-msg"},
		{"storage", "workbench", "row-storage-cleanup"},
	}

	for _, tc := range cases {
		t.Run(tc.legacy, func(t *testing.T) {
			if got := normalizeSettingsPageID(tc.legacy); got != tc.wantPage {
				t.Fatalf("normalizeSettingsPageID(%q) = %q，期望 %q", tc.legacy, got, tc.wantPage)
			}
			fake := NewFakeClient()
			fake.SetStorageReport(StorageReport{TotalBytes: 1024}, nil)
			chat := newChatState(fake)
			t.Cleanup(chat.close)
			st := NewSettingsState(settingsTestDir(t), nil, chat)
			tt := ui.NewTester(func(c *ui.Context) { renderSettingsDetail(c, st) }, 1000, 1200)

			st.OpenPage(tc.legacy)
			st.mu.Lock()
			page, jump := st.currentPage, st.jumpToItemID
			st.mu.Unlock()
			if page != tc.wantPage || jump != tc.wantJump {
				t.Fatalf("OpenPage(%q) 落点为 %q/%q，期望 %q/%q", tc.legacy, page, jump, tc.wantPage, tc.wantJump)
			}
			tt.Frame()
			if tc.legacy == "storage" {
				waitFor(t, tt, func() bool { return fake.StorageScanCalls() >= 1 })
			}
		})
	}
}

func TestSettingsP2Slim_SwarmReviewCheckAutoSave(t *testing.T) {
	testenv.TempHome(t)
	dataDir := settingsTestDir(t)
	projectDir := t.TempDir()
	st := newSwarmReviewSettingsWithProject(t, dataDir, projectDir)
	st.open = true
	st.currentPage = "swarm"
	st.cfg.Orchestration.ReviewMode = config.ReviewModeAI

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 1100, 7000)
	tt.Frame()

	if tt.HasText("保存检查命令") {
		t.Fatal("编排主页面检查命令不应再渲染独立保存按钮")
	}

	if err := tt.Click("编辑检查命令（本项目）"); err != nil {
		t.Fatalf("聚焦检查命令输入框失败: %v", err)
	}
	tt.Type("go vet ./...")
	waitFor(t, tt, func() bool { return st.reviewCheckCommand == "go vet ./..." })

	if !st.prepareWindowClose() {
		t.Fatal("prepareWindowClose 应刷盘检查命令")
	}
	got, err := reviewcheck.LoadProjectConfig(dataDir, projectDir)
	if err != nil || got.Command != "go vet ./..." {
		t.Fatalf("关窗刷盘后的检查命令 = %q, err=%v", got.Command, err)
	}
	if !st.savedBadgeVisible() {
		t.Fatal("保存检查命令后应点亮顶部「已保存」徽标")
	}

	// 防抖自动保存同样能在不点任何按钮的情况下落盘并点亮「已保存」徽标。
	st.cfgMu.Lock()
	st.showSaved = false
	st.cfgMu.Unlock()
	if err := tt.Click("编辑检查命令（本项目）"); err != nil {
		t.Fatalf("重新聚焦检查命令输入框失败: %v", err)
	}
	tt.Command("selectAll")
	tt.Type("go test ./internal/ui")
	waitFor(t, tt, func() bool {
		cfg, loadErr := reviewcheck.LoadProjectConfig(dataDir, projectDir)
		return loadErr == nil && cfg.Command == "go test ./internal/ui"
	})
	if !st.savedBadgeVisible() {
		t.Fatal("防抖自动保存检查命令后应点亮顶部「已保存」徽标")
	}
}

func TestSettingsP2Slim_SheetUnsavedCloseConfirm(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.open = true
	st.currentPage = "models"
	st.mu.Lock()
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api"}}
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsWindow(c, st) }, 1100, 900)

	// 1. 无未保存修改时，Esc 直接关闭面板。
	openSettingsSheet(st, "provider-add", "")
	waitFor(t, tt, func() bool { return tt.HasText("设置面板 添加供应商") })
	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		return sheet == ""
	})
	if tt.HasText("放弃未保存修改？") {
		t.Fatal("无未保存修改时不应弹出放弃确认框")
	}

	// 2. 有未保存修改时，关闭按钮与 Esc 都触发「放弃未保存修改？」确认框。
	openSettingsSheet(st, "provider-add", "")
	waitFor(t, tt, func() bool { return tt.HasText("设置面板 添加供应商") })
	if err := tt.Click("API Key"); err != nil {
		t.Fatalf("选择 API Key 类型失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("选择 DeepSeek") })
	if err := tt.Click("选择 DeepSeek"); err != nil {
		t.Fatalf("选择 DeepSeek 失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("编辑预设 API Key") })
	if err := tt.Click("编辑预设 API Key"); err != nil {
		t.Fatalf("点击预设 API Key 输入框失败: %v", err)
	}
	tt.Type("sk-draft-unsaved")
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.apiKeyDraft == "sk-draft-unsaved"
	})

	tt.Key(0, ui.KeyEscape)
	waitFor(t, tt, func() bool { return tt.HasText("放弃未保存修改？") })
	if sheet, _ := settingsTreeSheetState(st); sheet != "provider-add" {
		t.Fatalf("确认框弹出时面板应保持打开，实际 %q", sheet)
	}

	// 点「继续编辑」保留面板与草稿。
	if err := tt.Click("继续编辑"); err != nil {
		t.Fatalf("点击继续编辑失败: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("放弃未保存修改？") })
	st.mu.Lock()
	draft := st.presets.apiKeyDraft
	openSheet := st.openSheet
	st.mu.Unlock()
	if openSheet != "provider-add" || draft != "sk-draft-unsaved" {
		t.Fatalf("继续编辑应保留面板和草稿: sheet=%q draft=%q", openSheet, draft)
	}

	// 点右上角关闭按钮再次触发确认，点「放弃」才真正关闭并清草稿。
	if err := tt.Click("关闭添加供应商"); err != nil {
		t.Fatalf("点击关闭添加供应商失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("放弃未保存修改？") })
	if err := tt.Click("放弃"); err != nil {
		t.Fatalf("点击放弃失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		sheet, _ := settingsTreeSheetState(st)
		return sheet == "" && !tt.HasText("放弃未保存修改？")
	})
	st.mu.Lock()
	afterDraft := st.presets.apiKeyDraft
	st.mu.Unlock()
	if afterDraft != "" {
		t.Fatalf("放弃关闭后草稿应被清空，实际 %q", afterDraft)
	}

	// 3. 覆盖其余二级面板的脏态检测覆盖范围（provider / mcp-detail / mcp-add / magpie-gateway / magpie-personas / open-in）。
	st.mu.Lock()
	st.presets.editingID = "deepseek"
	st.presets.apiKeyDraft = "sk-provider"
	st.openSheet = "provider"
	st.mu.Unlock()
	if !settingsSheetHasUnsavedChanges(st) {
		t.Fatal("provider 面板填写新 API Key 后应判定为存在未保存修改")
	}
	closeSettingsSheet(st)

	uiSt := getMCPUIState(st)
	uiSt.mu.Lock()
	uiSt.isFormOpen = true
	uiSt.editingServer = "srv1"
	uiSt.editBaseName = "srv1"
	uiSt.nameInput = "srv1-edited"
	uiSt.mu.Unlock()
	st.mu.Lock()
	st.openSheet = "mcp-detail"
	st.mu.Unlock()
	if !settingsSheetHasUnsavedChanges(st) {
		t.Fatal("mcp-detail 编辑服务名后应判定为存在未保存修改")
	}
	closeSettingsSheet(st)

	uiSt.mu.Lock()
	uiSt.isFormOpen = true
	uiSt.editingServer = ""
	uiSt.nameInput = "new-mcp"
	uiSt.mu.Unlock()
	st.mu.Lock()
	st.openSheet = "mcp-add"
	st.mu.Unlock()
	if !settingsSheetHasUnsavedChanges(st) {
		t.Fatal("mcp-add 填写新服务名后应判定为存在未保存修改")
	}
	closeSettingsSheet(st)

	st.mu.Lock()
	st.openSheet = "magpie-gateway"
	openCustomEndpointForm(st, nil, endpointFormScopeMagpie)
	st.endpoints.editName = "Magpie-2"
	st.mu.Unlock()
	if !settingsSheetHasUnsavedChanges(st) {
		t.Fatal("magpie-gateway 修改网关名后应判定为存在未保存修改")
	}
	closeSettingsSheet(st)

	st.mu.Lock()
	st.openSheet = "magpie-personas"
	st.endpoints.endpoints = []CustomEndpoint{{ID: "gw1", Name: "Magpie", BaseURL: magpieDefaultBaseURL, Magpie: true}}
	openCustomEndpointForm(st, &st.endpoints.endpoints[0], endpointFormScopeMagpie)
	st.endpoints.editPersonaWebURL = "https://personas.example.com"
	st.mu.Unlock()
	if !settingsSheetHasUnsavedChanges(st) {
		t.Fatal("magpie-personas 修改人设地址后应判定为存在未保存修改")
	}
	closeSettingsSheet(st)

	openInSt := getOpenInPageState(st)
	openInSt.adding = true
	openInSt.draftName = "Zed"
	st.mu.Lock()
	st.openSheet = "open-in"
	st.mu.Unlock()
	if !settingsSheetHasUnsavedChanges(st) {
		t.Fatal("open-in 新增应用填写名称后应判定为存在未保存修改")
	}
	closeSettingsSheet(st)
}

func TestSettingsP2Slim_NoDuplicateOnboardingButtons(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	u := onboardingState(s)
	u.open = true
	onboardTT := ui.NewTester(func(c *ui.Context) { renderOnboardingDialog(c, s) }, 800, 900)
	onboardTT.Frame()
	if !onboardTT.HasText("开始使用") {
		t.Fatal("引导弹窗应保留「开始使用」主按钮")
	}
	if onboardTT.HasText("稍后") {
		t.Fatal("引导弹窗不应再显示重复的「稍后」按钮")
	}

	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.open = true
	navTT := ui.NewTester(func(c *ui.Context) { renderSettingsNav(c, st) }, 320, 900)
	navTT.Frame()
	if navTT.HasText("新用户引导") {
		t.Fatal("设置导航底部不应再显示重复的「新用户引导」按钮")
	}
}
