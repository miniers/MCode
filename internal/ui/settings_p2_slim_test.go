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
	wantIDs := []string{"general", "appearance", "remote", "models", "magpie", "conversation", "swarm", "steward", "extensions", "workbench", "sourceControl", "about"}
	wantGroups := []string{"基础", "基础", "基础", "模型与代理", "模型与代理", "模型与代理", "模型与代理", "模型与代理", "能力与工作区", "能力与工作区", "能力与工作区", ""}
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

	for _, want := range []string{"基础", "通用", "外观", "远程控制", "模型与代理", "模型与供应商", "Magpie", "会话", "编排", "总管", "能力与工作区", "扩展", "工作台", "版本与存储", "关于"} {
		if !tt.HasText(want) {
			t.Fatalf("设置导航缺少 %q: %v", want, tt.Texts())
		}
	}
	rendered := make(map[string]bool)
	for _, txt := range tt.Texts() {
		rendered[txt] = true
	}
	for _, removed := range []string{"记忆", "版本控制", "存储"} {
		if rendered[removed] {
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
		{"appearance", "appearance", ""},
		{"steward", "steward", ""},
		{"nowledge", "extensions", "row-memory-backend"},
		{"sourceControl", "sourceControl", ""},
		{"storage", "sourceControl", "row-storage-cleanup"},
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
	waitFor(t, tt, func() bool { return tt.HasText("DeepSeek") && tt.HasText("编辑预设 API Key") })
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

func TestSettingsModuleDividers_MergedPages(t *testing.T) {
	type moduleExpect struct {
		module     string
		wantRule   bool
		wantTitle  bool
		prevCard   string
		firstCard  string
		exactCount int
	}
	cases := []struct {
		pageID        string
		omittedTitles []string
		modules       []moduleExpect
	}{
		{
			pageID: "models",
			modules: []moduleExpect{
				{module: "收藏模型", wantRule: false, wantTitle: true, exactCount: 1},
				{module: "供应商", wantRule: false, wantTitle: true, exactCount: 1},
			},
		},
		{
			pageID: "extensions",
			modules: []moduleExpect{
				{module: "技能", wantRule: false, wantTitle: true, exactCount: 1},
				{module: "MCP 服务", wantRule: false, wantTitle: true, exactCount: 1},
				{module: "记忆", wantRule: false, wantTitle: true, exactCount: 1},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.pageID, func(t *testing.T) {
			testenv.TempHome(t)
			page, ok := settingsTreePageOf(tc.pageID)
			if !ok {
				t.Fatalf("找不到设置页 %q", tc.pageID)
			}
			chat := newChatState(NewFakeClient())
			t.Cleanup(chat.close)
			st := NewSettingsState(settingsTestDir(t), nil, chat)
			st.cfg.Orchestration.Steward.Enabled = true
			st.agentConfLoaded = true

			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).FillWidth().MaxWidth(settingsContentMaxWidth).Children(func() {
					renderSettingsTreePage(c, st, page)
				})
			}, 1000, 4000)
			tt.Frame()

			for _, omitted := range tc.omittedTitles {
				if n := countExactText(tt.Texts(), omitted); n != 0 {
					t.Errorf("%s 顶部不应出现重复模块标题 %q，实际出现 %d 次", tc.pageID, omitted, n)
				}
			}

			for _, m := range tc.modules {
				ruleName := "模块分隔 " + m.module
				ruleBounds, hasRule := tt.Find(ruleName)
				if hasRule != m.wantRule {
					t.Errorf("%s %q 存在=%v，期望=%v", tc.pageID, ruleName, hasRule, m.wantRule)
				}
				if m.wantRule && hasRule {
					prev, okPrev := tt.Find(m.prevCard)
					next, okNext := tt.Find(m.firstCard)
					if !okPrev || !okNext {
						t.Fatalf("%s 找不到相邻卡片 %q(%v) 或 %q(%v)", tc.pageID, m.prevCard, okPrev, m.firstCard, okNext)
					}
					if ruleBounds.Y < prev.Y+prev.H-0.5 || ruleBounds.Y+ruleBounds.H > next.Y+0.5 {
						t.Errorf("%s %q 纵坐标越界: prev=[%.1f,%.1f] rule=[%.1f,%.1f] next=[%.1f,%.1f]",
							tc.pageID, ruleName, prev.Y, prev.Y+prev.H, ruleBounds.Y, ruleBounds.Y+ruleBounds.H, next.Y, next.Y+next.H)
					}
				}
				if m.wantTitle {
					if n := countExactText(tt.Texts(), m.module); n != m.exactCount {
						t.Errorf("%s 模块标题 %q 出现 %d 次，期望 %d 次", tc.pageID, m.module, n, m.exactCount)
					}
					headingBounds, hasHeading := tt.Find(m.module)
					if !hasHeading {
						t.Errorf("%s 缺少模块标题 %q", tc.pageID, m.module)
					} else if m.wantRule && hasRule {
						if headingBounds.Y < ruleBounds.Y+ruleBounds.H-0.5 {
							t.Errorf("%s 模块标题 %q 应位于分隔线下方: ruleY=%.1f headingY=%.1f",
								tc.pageID, m.module, ruleBounds.Y, headingBounds.Y)
						}
					}
				}
			}

			ttNarrow := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).FillWidth().MaxWidth(settingsContentMaxWidth).Children(func() {
					renderSettingsTreePage(c, st, page)
				})
			}, 640, 4000)
			ttNarrow.Frame()
			for _, m := range tc.modules {
				if m.wantRule {
					ruleName := "模块分隔 " + m.module
					if b, ok := ttNarrow.Find(ruleName); !ok || b.W <= 0 || b.X+b.W > 640+0.5 {
						t.Errorf("%s 640px 窄窗分隔线 %q 越界或缺失: ok=%v bounds=%+v", tc.pageID, ruleName, ok, b)
					}
				}
				if m.wantTitle {
					if b, ok := ttNarrow.Find(m.module); !ok || b.W <= 0 || b.X+b.W > 640+0.5 {
						t.Errorf("%s 640px 窄窗模块标题 %q 越界或缺失: ok=%v bounds=%+v", tc.pageID, m.module, ok, b)
					}
				}
			}
		})
	}
}

func TestSettingsModuleDividers_HiddenModuleNoOrphanRule(t *testing.T) {
	chat := newChatState(NewFakeClient())
	t.Cleanup(chat.close)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	ctl := toggle(val(func(c *config.Config) *bool { return &c.Notifications.NotifyWhenFinished }, true))

	t.Run("中间模块隐藏时只为可见相邻不同模块画分隔", func(t *testing.T) {
		page := settingsPage{
			ID:    "hidden-mid",
			Title: "模块甲",
			Sections: []settingsSection{
				{ID: "sec-a", Title: "甲一", Module: "模块甲", Rows: []setting{{ID: "r-a", Title: "甲行", Control: ctl}}},
				{ID: "sec-b", Title: "乙一", Module: "模块乙", Visible: func(*SettingsState) bool { return false }, Rows: []setting{{ID: "r-b", Title: "乙行", Control: ctl}}},
				{ID: "sec-c", Title: "丙一", Module: "模块丙", Rows: []setting{{ID: "r-c", Title: "丙行", Control: ctl}}},
			},
		}
		tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, page) }, 1000, 1200)
		tt.Frame()

		if _, ok := tt.Find("模块分隔 模块乙"); ok || tt.HasText("模块乙") {
			t.Fatalf("隐藏模块不应渲染分隔线或标题: %v", tt.Texts())
		}
		if _, ok := tt.Find("模块分隔 模块甲"); ok || countExactText(tt.Texts(), "模块甲") != 0 {
			t.Fatalf("不应渲染模块分隔线或模块标题: %v", tt.Texts())
		}
		if _, ok := tt.Find("模块分隔 模块丙"); ok || tt.HasText("模块丙") {
			t.Fatalf("分节统一只显示自身标题，不应渲染模块分隔线或模块名: %v", tt.Texts())
		}
		if !tt.HasText("甲一") || !tt.HasText("丙一") {
			t.Fatalf("可见分节应显示自身分节标题: %v", tt.Texts())
		}
	})

	t.Run("同模块之间隔着隐藏模块时不画分隔", func(t *testing.T) {
		page := settingsPage{
			ID:    "hidden-same-module",
			Title: "测试页",
			Sections: []settingsSection{
				{ID: "sec-h0", Title: "隐首", Module: "隐藏首", Visible: func(*SettingsState) bool { return false }, Rows: []setting{{ID: "r-h0", Title: "隐行", Control: ctl}}},
				{ID: "sec-a1", Title: "甲一", Module: "测试页", Rows: []setting{{ID: "r-a1", Title: "行一", Control: ctl}}},
				{ID: "sec-b", Title: "乙一", Module: "隐藏中", Visible: func(*SettingsState) bool { return false }, Rows: []setting{{ID: "r-b", Title: "隐二", Control: ctl}}},
				{ID: "sec-a2", Title: "甲二", Module: "测试页", Rows: []setting{{ID: "r-a2", Title: "行二", Control: ctl}}},
			},
		}
		tt := ui.NewTester(func(c *ui.Context) { renderSettingsTreePage(c, st, page) }, 1000, 1200)
		tt.Frame()

		for _, forbidden := range []string{"隐藏首", "模块分隔 隐藏首", "隐藏中", "模块分隔 隐藏中", "模块分隔 测试页"} {
			if tt.HasText(forbidden) {
				t.Fatalf("不应出现 %q: %v", forbidden, tt.Texts())
			}
		}
		if countExactText(tt.Texts(), "测试页") != 0 {
			t.Fatalf("首可见模块与页标题相同时不应出现重复标题: %v", tt.Texts())
		}
	})
}
