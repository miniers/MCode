package mcodeui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// 测试用例 1：点击齿轮设置按钮后进入设置面板，页面名按顺序排列。
func TestSettings_EnterAndPageList(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	expectedTitles := []string{
		"Magpie", "通用", "外观", "远程控制", "模型与供应商", "会话", "编排", "扩展", "记忆", "工作台", "版本控制", "关于",
	}
	pages := settingsPages()
	if len(pages) != len(expectedTitles) {
		t.Fatalf("导航应为 %d 页，实际为 %d 页", len(expectedTitles), len(pages))
	}
	for i, page := range pages {
		if page.title != expectedTitles[i] || page.icon == nil {
			t.Fatalf("导航第 %d 项名称或图标不正确: %+v", i, page)
		}
	}

	waitFor(t, tt, func() bool {
		// Magpie 入口显示的是连接状态文字（Magpie / 连接 Magpie / Magpie · 已连接），
		// 导航行本身按 Label("Magpie") 定位。
		if _, ok := tt.Find("Magpie"); !ok {
			return false
		}
		for _, title := range expectedTitles[1:] {
			if !tt.HasText(title) {
				return false
			}
		}
		return true
	})

	magpieRect, ok := tt.Find("Magpie")
	if !ok {
		t.Fatal("missing nav item Magpie")
	}
	generalRect, ok := tt.Find("通用")
	if !ok {
		t.Fatal("missing nav item 通用")
	}
	if magpieRect.Y >= generalRect.Y {
		t.Fatalf("nav item Magpie (y %.1f) should appear before 通用 (y %.1f)", magpieRect.Y, generalRect.Y)
	}

	texts := tt.Texts()
	for i, text := range texts {
		if text == "搜索设置" {
			texts = texts[i+1:]
			break
		}
	}
	var foundIndices []int
	for _, title := range expectedTitles[1:] {
		idx := -1
		for i, txt := range texts {
			if txt == title {
				idx = i
				break
			}
		}
		if idx == -1 {
			t.Fatalf("missing nav item title %q in rendered texts", title)
		}
		foundIndices = append(foundIndices, idx)
	}
	for i := 1; i < len(foundIndices); i++ {
		if foundIndices[i] <= foundIndices[i-1] {
			t.Fatalf("nav item %q (idx %d) appears before %q (idx %d)", expectedTitles[i+1], foundIndices[i], expectedTitles[i], foundIndices[i-1])
		}
	}
}

// TestSettingsNav_SearchSkipsPinnedMagpie 校验搜索把当前页过滤掉后，自动切页跳过置顶的
// Magpie 账号入口；只有它是唯一可见项时才切过去。
func TestSettingsNav_SearchSkipsPinnedMagpie(t *testing.T) {
	newTester := func(st *SettingsState) *ui.Tester {
		return ui.NewTester(func(c *ui.Context) {
			// 与设置面板真实布局一致：左导航、右详情，导航的切页请求下一帧由详情区执行。
			ui.Row(c.Key("test-settings-split")).Fill().Children(func() {
				ui.Column(c.Key("test-settings-nav")).Width(300).FillHeight().Children(func() {
					renderSettingsNav(c, st)
				})
				ui.Column(c.Key("test-settings-main")).Grow(1).Children(func() {
					renderSettingsDetail(c, st)
				})
			})
		}, 1200, 1600)
	}

	search := func(t *testing.T, query, want string) {
		t.Helper()
		st := NewSettingsState(settingsTestDir(t), nil, nil)
		st.open = true
		st.currentPage = "general"
		tt := newTester(st)
		tt.Frame()

		st.navSearch = query
		tt.Frame() // 导航请求切页
		tt.Frame() // 详情区渲染前执行切页
		st.mu.Lock()
		page := st.currentPage
		st.mu.Unlock()
		if page != want {
			t.Fatalf("搜索 %q 应切到 %q，实际 %q", query, want, page)
		}
	}

	// 「模型」同时命中 Magpie 网关的「默认模型」行，但不能因此切到置顶入口。
	search(t, "模型", "models")
	// 「人设」只命中 Magpie 页，此时 Magpie 是唯一可见项，应切过去。
	search(t, "人设", "magpie")
}

// 测试用例 2：在通用页切换权限档位后，config.json 里的 general.permission_mode 随之改变。
func TestSettings_SwitchPermissionMode(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("默认权限") && tt.HasText("完全访问")
	})

	// 五档权限走下拉：先点开显示当前档位的触发器，再选「监督」。
	if err := tt.Click("完全访问"); err != nil {
		t.Fatalf("open permission picker failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("监督") })
	if err := tt.Click("监督"); err != nil {
		t.Fatalf("click option '监督' failed: %v", err)
	}

	// 验证磁盘上的 config.json 中的 general.permission_mode 是否保存为 supervised
	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && cfg.Permissions.Mode == config.ModeSupervised
	})

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if cfg.Permissions.Mode != config.ModeSupervised {
		t.Fatalf("expected general.permission_mode = %q, got %q", config.ModeSupervised, cfg.Permissions.Mode)
	}
}

// 测试用例 3：通用设置不再保留“默认 Agent”行，改由新会话的模型选择记住上次使用的 Agent。
func TestSettings_NoDefaultProviderRow(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	locked := map[string]bool{"provider": true}
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, locked)), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("默认权限")
	})
	if tt.HasText("默认 Agent") {
		t.Fatalf("通用设置不应再出现“默认 Agent”行")
	}

	// 确保配置仍然保持初始的 claude，未发生非预期的修改
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load config failed: %v", err)
	}
	if cfg.Models.Provider != config.ProviderClaude {
		t.Fatalf("expected provider %q, got %q", config.ProviderClaude, cfg.Models.Provider)
	}
}

// 测试用例 4：关闭设置后回到会话界面。
func TestSettings_BackToChat(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("关闭设置")
	})

	if err := tt.Click("关闭设置"); err != nil {
		t.Fatalf("click '关闭设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return !tt.HasText("关闭设置") && tt.HasText("测试会话1") && tt.HasText("添加项目")
	})
}

// 场景：readonly 是合法档位，通用页必须能显示、能选中并落盘（E-02）。
func TestSettings_ReadOnlyPermissionMode(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})
	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("默认权限") && tt.HasText("完全访问")
	})

	// 只读也是合法档位，必须出现在下拉里并可选。
	if err := tt.Click("完全访问"); err != nil {
		t.Fatalf("open permission picker failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("只读") })
	if err := tt.Click("只读"); err != nil {
		t.Fatalf("click option '只读' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		cfg, err := config.Load(dataDir)
		return err == nil && cfg.Permissions.Mode == config.ModeReadOnly
	})
	if tt.HasText("（未知档位）") {
		t.Fatalf("只读档应在选项里，不应被当成未知档位")
	}
}

// 场景：配置里是界面不认识的权限档时，页面要把它写出来而不是静默不高亮。
func TestSettings_UnknownPermissionModeHint(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.cfg.Permissions.Mode = "bogus"

	tt := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 2400)
	if !tt.HasText("当前：bogus（未知档位）") {
		t.Fatalf("未知权限档应兜底显示当前值: %v", tt.Texts())
	}
}

// 测试用例 5：按 Esc 键关闭设置面板并回到会话界面。
func TestSettings_EscClosesSettings(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("关闭设置")
	})

	tt.Key(0, ui.KeyEscape)

	waitFor(t, tt, func() bool {
		_, input := tt.Find("消息输入框")
		return !tt.HasText("关闭设置") && input
	})
}

// 测试用例 6：未传 WithSettings Option 时，不显示齿轮入口。
func TestSettings_WithoutOptionNoGear(t *testing.T) {
	fake := NewFakeClient()
	tt := ui.NewTester(Root(fake), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("添加项目")
	})

	if tt.HasText("设置") {
		t.Fatalf("expected gear button '设置' not to be shown when WithSettings is omitted")
	}
}

// 测试用例 7：左侧导航搜索功能，按页面名过滤导航项。
func TestSettings_NavSearchFilter(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("通用") &&
			tt.HasText("外观") &&
			tt.HasText("模型与供应商") &&
			tt.HasText("远程控制") &&
			tt.HasText("版本控制") &&
			tt.HasText("记忆")
	})

	// 点击搜索框聚焦并输入 "供应商" 进行筛选
	if err := tt.Click("搜索设置"); err != nil {
		t.Fatalf("click '搜索' input failed: %v", err)
	}
	tt.Type("供应商")

	waitFor(t, tt, func() bool {
		return tt.HasText("模型与供应商") && !tt.HasText("外观") && !tt.HasText("远程控制")
	})
}

// 测试用例 8：逐一点击重构后的真实页面，断言出现真实页面内容，且不再出现占位提示。
func TestSettings_NavRealPages(t *testing.T) {
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	var settings *SettingsState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) {
		settings = getChatSettings(s)
	}), 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("设置")
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	pagesToCheck := []struct {
		title       string
		expectedTxt string
	}{
		{title: "编排", expectedTxt: "主代理只规划"},
		{title: "工作台", expectedTxt: "验收后自动合并工作树"},
		{title: "会话", expectedTxt: "已归档会话"},
		{title: "扩展", expectedTxt: "技能"},
	}
	for _, p := range pagesToCheck {
		if p.title == "编排" {
			settings.selectSettingsPage("swarm")
			tt.Frame()
		} else if err := tt.Click(p.title); err != nil {
			t.Fatalf("click %q failed: %v", p.title, err)
		}
		waitFor(t, tt, func() bool {
			return tt.HasText(p.expectedTxt) && !tt.HasText("该页面尚在建设中")
		})
	}
}

// 测试用例 9：模型页不再提供采样温度与上下文预算滑块，这些值不再由界面写入配置。
func TestSettings_ModelsPageBudget(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{
		{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}},
		{ID: "codex", Name: "Codex", Available: true, Models: []ModelInfo{{ID: "gpt-5.5", Name: "GPT-5.5"}}},
	})
	dataDir := settingsTestDir(t)
	var chat *chatState
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil), func(s *chatState) { chat = s }), 1000, 3000)

	waitFor(t, tt, func() bool {
		chat.mu.Lock()
		defer chat.mu.Unlock()
		return chat.initialized && len(chat.providers) == 2
	})

	if err := tt.Click("设置"); err != nil {
		t.Fatalf("click '设置' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("模型")
	})

	if err := tt.Click("模型与供应商"); err != nil {
		t.Fatalf("click '模型与供应商' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		return tt.HasText("收藏模型")
	})
	for _, label := range []string{"采样温度", "Temperature 滑块", "上下文窗口上限", "最大输出 Token 滑块"} {
		if tt.HasText(label) {
			t.Fatalf("模型页不应再出现 %q", label)
		}
	}
}

// 测试用例 10：配置读不出来时要把原因显示出来，而不是静默沿用旧值/默认值（J-03）。
func TestSettings_ConfigLoadErrorSurfaced(t *testing.T) {
	dataDir := settingsTestDir(t)
	// 非法字段值现在会被 config 逐字段重置（见 TestSettings_ConfigRepairWarningSurfaced），
	// 这里改用语法损坏的 JSON 制造真正的读盘失败。
	if err := os.WriteFile(config.ConfigPath(dataDir), []byte(`{`), 0o600); err != nil {
		t.Fatal(err)
	}

	st := NewSettingsState(dataDir, nil, nil)
	if !strings.Contains(st.errorMessage, "读取设置失败") {
		t.Fatalf("NewSettingsState 应把读盘失败写进错误条，实际 %q", st.errorMessage)
	}

	// Open 读盘失败时保留内存态，并继续显示错误原因，不能把它清空。
	st.errorMessage = ""
	st.Open()
	if !strings.Contains(st.errorMessage, "读取设置失败") {
		t.Fatalf("Open 应把读盘失败写进错误条，实际 %q", st.errorMessage)
	}
	if got := st.cfg.Permissions.Mode; got != config.Defaults().Permissions.Mode {
		t.Fatalf("读盘失败时不应改写内存态，实际 %q", got)
	}
}

// 测试用例 11：配置文件里的非法字段被逐字段重置时，设置页要把这件事显示出来，但不当成错误（E-07）。
func TestSettings_ConfigRepairWarningSurfaced(t *testing.T) {
	dataDir := settingsTestDir(t)
	if err := os.WriteFile(config.ConfigPath(dataDir), []byte(`{"general":{"permission_mode":"bogus"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	st := NewSettingsState(dataDir, nil, nil)
	if got := st.cfg.Permissions.Mode; got != config.ModeSupervised {
		t.Fatalf("非法 permission_mode 应被重置为 %q，实际 %q", config.ModeSupervised, got)
	}
	if st.errorMessage != "" {
		t.Fatalf("非法值已被重置，不应算读盘失败，实际 %q", st.errorMessage)
	}
	if !strings.Contains(st.configWarning, "已重置为默认") || !strings.Contains(st.configWarning, "permissions.mode") {
		t.Fatalf("应把非法字段的重置提示写进提示条，实际 %q", st.configWarning)
	}

	// 重新打开面板同样要把提示带出来，而不是只有首次加载才有。
	st.configWarning = ""
	st.Open()
	if !strings.Contains(st.configWarning, "permissions.mode") {
		t.Fatalf("Open 应重新给出重置提示，实际 %q", st.configWarning)
	}
}

// 测试用例 12：文本输入走防抖落盘，输入期间只改内存，到期或关闭面板才写盘（E-06）。
func TestSettings_TextEditDebouncedSave(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)

	st.cfg.SourceControl.CommitInstructions = "先只改内存"
	st.noteTextEdit()

	// 还在防抖窗口里：不应写盘。
	st.flushTextSave(false)
	if cfg, err := config.Load(dataDir); err == nil && cfg.SourceControl.CommitInstructions != "" {
		t.Fatalf("防抖窗口内不应写盘，磁盘上已是 %q", cfg.SourceControl.CommitInstructions)
	}

	waitForAutoDormant(t, func() bool {
		st.flushTextSave(false)
		cfg, err := config.Load(dataDir)
		return err == nil && cfg.SourceControl.CommitInstructions == "先只改内存"
	})

	// 关面板前的强制落盘：重新输入后立刻 Close 也要写进去。
	st.cfg.SourceControl.CommitInstructions = "关面板前也要存"
	st.noteTextEdit()
	st.Close()
	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
	if cfg.SourceControl.CommitInstructions != "关面板前也要存" {
		t.Fatalf("关闭面板时应强制落盘，磁盘上是 %q", cfg.SourceControl.CommitInstructions)
	}
}

// TestSettingsPages_NewStructure 验证导航顺序。
func TestSettingsPages_NewStructure(t *testing.T) {
	expected := []string{"magpie", "general", "appearance", "remote", "models", "conversation", "swarm", "extensions", "nowledge", "workbench", "sourceControl", "about"}
	groups := []string{"", "应用", "应用", "应用", "代理", "代理", "代理", "代理", "代理", "工作区", "工作区", ""}
	pages := settingsPages()
	if len(pages) != len(expected) {
		t.Fatalf("导航应为 %d 页，实际 %d 页", len(expected), len(pages))
	}
	for i, id := range expected {
		if pages[i].id != id || pages[i].group != groups[i] {
			t.Fatalf("导航第 %d 项应为 %s/%s，实际 %s/%s", i+1, id, groups[i], pages[i].id, pages[i].group)
		}
	}
}

func TestSettingsLegacyPageAndRowLinks(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	for _, tc := range []struct{ old, page string }{{"advanced", "general"}, {"sourceControl", "sourceControl"}, {"nowledge", "nowledge"}} {
		st.OpenPage(tc.old)
		if st.currentPage != tc.page {
			t.Errorf("页面 %q 定位到 %q，期望 %q", tc.old, st.currentPage, tc.page)
		}
	}
	rows := []struct{ id, page, sheet string }{
		{"row-ask-compact-before-resume", "conversation", ""},
		{"row-bee-message-retention", "conversation", ""},
		{"row-swarm-max-bees", "swarm", ""},
		{"row-swarm-max-rounds", "swarm", ""},
		{"row-swarm-bee-timeout", "swarm", ""},
		{"row-swarm-request-timeout", "swarm", ""},
		{"row-swarm-review-max-lines", "swarm", ""},
		{"row-steward-home-dir", "swarm", "steward"},
		{"row-steward-model", "swarm", "steward"},
		{"row-context-inject-budget", "workbench", ""},
		{"row-terminal-scrollback-lines", "workbench", ""},
		{"row-browser-quick-ports", "workbench", ""},
		{"row-kanban-stuck-interrupt-timeout", "workbench", ""},
		{"row-nowledge-inject-memory-tools-note", "nowledge", ""},
		{"row-nowledge-working-memory-budget", "nowledge", ""},
		{"row-nowledge-sync-helper-threads", "nowledge", ""},
		{"row-nowledge-sync-automation-threads", "nowledge", ""},
		{"row-sc-init-submodules", "sourceControl", ""},
		{"row-sc-clone-ignored", "sourceControl", ""},
		{"row-sc-clone-ignored-patterns", "sourceControl", ""},
	}
	st.cfg.Orchestration.Steward.Enabled = true
	st.cfg.Orchestration.ReviewMode = config.ReviewModeAI
	for _, row := range rows {
		t.Run(row.id, func(t *testing.T) {
			st.focusSettingsItem(settingsItemHit{Item: settingsItem{ID: row.id, Page: "advanced"}})
			if st.currentPage != row.page || st.openSheet != row.sheet || st.jumpToItemID != row.id || st.highlightItemID != row.id {
				t.Errorf("定位到 %q/%q 行 %q 高亮 %q，期望 %q/%q 行 %q", st.currentPage, st.openSheet, st.jumpToItemID, st.highlightItemID, row.page, row.sheet, row.id)
			}
		})
	}
}

// TestConversationPageMovedRows 验证会话相关的 8 行搬到「会话」页，通用页不再包含它们。
func TestConversationPageMovedRows(t *testing.T) {
	titles := []string{
		"显示思考过程", "自动折叠思考过程", "简洁 AI 回复",
		"运行中排队消息", "新消息插入当前回合", "用尽限额后自动继续",
		"会话完成动作", "删除会话前确认",
	}
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	conv := ui.NewTester(func(c *ui.Context) {
		renderPageConversation(c, st)
	}, 1000, 2800)
	for _, title := range titles {
		if !conv.HasText(title) {
			t.Fatalf("会话页缺少行 %q: %v", title, conv.Texts())
		}
	}

	general := ui.NewTester(func(c *ui.Context) { renderPageGeneral(c, st) }, 1000, 2800)
	for _, title := range titles {
		if general.HasText(title) {
			t.Fatalf("通用页不应再包含行 %q", title)
		}
	}
	for _, title := range []string{"启动与托盘", "通知与提示音", "权限", "默认权限", "关闭窗口时"} {
		if !general.HasText(title) {
			t.Fatalf("通用页缺少 %q: %v", title, general.Texts())
		}
	}
	// 默认终端搬到工作台页。
	if general.HasText("默认终端") {
		t.Fatalf("通用页不应再包含默认终端")
	}
}

// TestSettingsSaveDoesNotRaceWithEdits 覆盖「保存」与「同时改配置」的竞争：
// save 只读锁内拍下的深拷贝快照，不在写盘路径上再读可被并发修改的字段；
// go test -race 下这里不应报竞争，且数据目录里的 config.json 必须可读回。
func TestSettingsSaveDoesNotRaceWithEdits(t *testing.T) {
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, nil)
	// 固定从默认配置起步，避免受开发者本机已有配置影响。
	st.cfgMu.Lock()
	st.cfg = config.Defaults()
	st.base = cloneConfig(st.cfg)
	st.cfgMu.Unlock()

	for i := 0; i < 50; i++ {
		st.save()
		// 紧接着在同一 goroutine 改一个会被保存的字段，与后台写盘交错。
		st.cfgMu.Lock()
		st.cfg.Models.Roles.Lead.Model = fmt.Sprintf("model-%d", i)
		st.cfgMu.Unlock()
	}

	settingsSaveWriters.Wait()
	if _, err := config.Load(dataDir); err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}
}

// TestRememberUsedProviderRacesWithRenderReaders 覆盖「记上次使用的 Agent」写配置与渲染路径读配置的竞争：
// rememberUsedProvider 改 cfg 时同时持有 mu 与 cfgMu（顺序 mu → cfgMu），渲染路径在 mu 下读 cfg，
// 两者互斥；go test -race 下这里不应报竞争，且最后一次写入的值要落到配置里。
func TestRememberUsedProviderRacesWithRenderReaders(t *testing.T) {
	state := newChatState(NewFakeClient())
	st := NewSettingsState(settingsTestDir(t), nil, state)
	setChatSettings(state, st)

	var wg sync.WaitGroup
	wg.Add(1)
	// 模拟渲染路径：在 st.mu 下反复读 cfg。
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			isAutoCollapseReasoningEnabled(state)
		}
	}()
	// 主 goroutine 交替记两个 Agent，与上面读 cfg 的渲染路径并发。
	for i := 0; i < 200; i++ {
		provider := "claude"
		if i%2 == 1 {
			provider = "codex"
		}
		state.rememberUsedProvider(provider)
	}
	wg.Wait()
	settingsSaveWriters.Wait()

	st.cfgMu.Lock()
	got, selected := st.cfg.Models.Provider, st.cfg.Models.ProviderSelected
	st.cfgMu.Unlock()
	if !selected || got != "codex" {
		t.Fatalf("最后一次写入应生效，got provider=%q selected=%v", got, selected)
	}
}

// TestChatStateServicesRaceWithInvalidate 覆盖未 init 的 chatState 上 invalidate 与 setUIServices 的竞争：
// invalidate 会从后台 goroutine 调用（调用方可能已持有 s.mu），所以 services 由独立的 servicesMu 保护，
// 不能读在 servicesMu 外、写却在 s.mu 里。go test -race 下这里不应报竞争。
// 两个并发方都只能用 ui.Services 零值（包外无法构造非零句柄），但读写下的是同一字段，足以暴露竞争。
func TestChatStateServicesRaceWithInvalidate(t *testing.T) {
	state := newChatState(NewFakeClient())

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// 和 selectThread 起的后台 goroutine 一样，只是要求重绘。
				state.invalidate()
			}
		}
	}()
	// 主 goroutine 反复登记服务句柄，模拟 init 与子窗口绑定。
	for i := 0; i < 200; i++ {
		state.setUIServices(ui.Services{})
	}
	close(stop)
	wg.Wait()

	if got := state.uiServices(); got != (ui.Services{}) {
		t.Fatalf("uiServices 应读回最后一次登记的句柄")
	}
}

// TestConfigWritersRaceWithRenderReaders 覆盖三处写配置与渲染路径读配置的竞争：
// 模型偏好镜像、整页重置、模型页 syncSettings 都必须同时持有 mu 与 cfgMu（顺序 mu → cfgMu），
// 否则会和 isAutoCollapseReasoningEnabled 这类在 st.mu 下读 cfg 的渲染路径撞车。
func TestConfigWritersRaceWithRenderReaders(t *testing.T) {
	state := newChatState(NewFakeClient())
	st := NewSettingsState(settingsTestDir(t), nil, state)
	setChatSettings(state, st)

	pin := ModelPin{Provider: "claude", ModelID: "sonnet"}
	effort := "high"
	pref := ModelPreference{Effort: &effort}
	ps := newModelsPageState()
	ps.pins = []ModelPin{pin}
	ps.preferences = map[string]ModelPreference{pin.Key(): pref}

	var wg sync.WaitGroup
	wg.Add(1)
	// 模拟渲染路径：在 st.mu 下反复读 cfg。
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			isAutoCollapseReasoningEnabled(state)
		}
	}()

	for i := 0; i < 30; i++ {
		// 1) 模型偏好写入：写后端偏好后镜像回设置页副本（后台 goroutine 里写 st.cfg）。
		state.updateModelPreference(pin, func(p *ModelPreference) { *p = pref })
		// 2) 整页重置：把某一页的配置段改回默认值。
		applySettingsPageReset(st, "general")
		// 3) 模型页同步配置：把「收藏模型」列表与偏好写回 cfg。
		ps.syncSettings(st)
	}
	wg.Wait()
	settingsSaveWriters.Wait()

	// 最后一次同步是同步调用，结果必须落定。
	st.mu.Lock()
	pins := append([]ModelPin(nil), st.cfg.Models.Pins...)
	finishAction := st.cfg.Session.ThreadFinishAction
	st.mu.Unlock()
	if len(pins) != 1 || pins[0] != pin {
		t.Fatalf("「收藏模型」应同步为最后写入的列表，got %v", pins)
	}
	if want := config.Defaults().Session.ThreadFinishAction; finishAction != want {
		t.Fatalf("重置后会话完成动作应为默认值 %q，got %q", want, finishAction)
	}

	// 偏好镜像由后台 goroutine 完成，轮询到写回为止。
	deadline := time.Now().Add(5 * time.Second)
	for {
		st.cfgMu.Lock()
		got := st.cfg.Models.ModelPreferenceFor(pin)
		st.cfgMu.Unlock()
		if got.Effort != nil && *got.Effort == effort {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("最后一次模型偏好应镜像回设置页副本，got %+v", got)
		}
		time.Sleep(time.Millisecond)
	}
}

// TestConfigWritesHoldBothLocks 覆盖 updateAndSave 与设置页其它无需渲染就能直接调的写入路径：
// 写 st.cfg 必须同时持有 mu 与 cfgMu（顺序 mu → cfgMu），否则会和 isAutoCollapseReasoningEnabled
// 这类在 st.mu 下读 cfg 的渲染路径撞车，go test -race 会报出来。
func TestConfigWritesHoldBothLocks(t *testing.T) {
	state := newChatState(NewFakeClient())
	st := NewSettingsState(settingsTestDir(t), nil, state)
	setChatSettings(state, st)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	// 模拟渲染路径：在 st.mu 下反复读 cfg，写入方漏掉 mu 就会与它竞争。
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				isAutoCollapseReasoningEnabled(state)
			}
		}
	}()

	const rounds = 12
	lastMaxBees := 0
	for i := 0; i < rounds; i++ {
		lastMaxBees = i % 4
		// 1) 后台 goroutine 的通用写入入口。
		st.updateAndSave(func(cfg *config.Config) {
			cfg.Orchestration.MaxBees = lastMaxBees
		})
		// 2) 版本控制页：持久化提交信息生成模型。
		saveCommitMessageModel(st, ModelSelection{Provider: "claude", Model: "sonnet"})
		// 3) 记忆页：写回单个项目的记忆空间选择。
		st.setProjectSpaceOverride("/tmp/mcode-both-locks", "space-a")
	}
	close(stop)
	wg.Wait()
	settingsSaveWriters.Wait()

	// 最后一次写入是同步调用，结果必须落定。
	st.mu.Lock()
	maxBees := st.cfg.Orchestration.MaxBees
	model := st.cfg.SourceControl.CommitMessageModel
	space := st.cfg.Memory.SpaceOverrides["/tmp/mcode-both-locks"]
	st.mu.Unlock()
	if maxBees != lastMaxBees {
		t.Fatalf("updateAndSave 应写入最后一次的值 %d，got %d", lastMaxBees, maxBees)
	}
	if model.Provider != "claude" || model.Model != "sonnet" {
		t.Fatalf("提交信息模型应写入最后一次的选择，got %+v", model)
	}
	if space != "space-a" {
		t.Fatalf("记忆空间选择应写入 space-a，got %q", space)
	}
}

// TestSettings_NavSearchFieldHeight 验证导航搜索框外框高度为 ControlMD。
func TestSettings_NavSearchFieldHeight(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.open = true

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsNav(c, st)
	}, 900, 1400)
	tt.Frame()

	r, ok := tt.Find("搜索设置外框")
	if !ok {
		t.Fatalf("找不到导航搜索框: %v", tt.Texts())
	}
	if r.H != ControlMD {
		t.Fatalf("导航搜索框高 = %v，期望 %v", r.H, ControlMD)
	}
}

// TestSettings_AppearanceSectionsUseCards 验证外观页的每个分节都包进一张卡片：
// 分节标题留在卡片外，卡片本身按「设置卡片 」Label 统计。
func TestSettings_AppearanceSectionsUseCards(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "appearance"

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsDetail(c, st)
	}, 1200, 1600)
	tt.Frame()

	for _, title := range []string{"主题与排版", "字体与字号"} {
		if _, ok := tt.Find(title); !ok {
			t.Fatalf("外观页缺少分节标题 %q：%v", title, tt.Texts())
		}
	}

	cards := 0
	for _, text := range tt.Texts() {
		if strings.HasPrefix(text, "设置卡片 ") {
			cards++
		}
	}
	if cards < 2 {
		t.Fatalf("外观页应至少有两张卡片，实际 %d 张：%v", cards, tt.Texts())
	}
}
