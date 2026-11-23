package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/testenv"
)

// TestUIReview15_ProviderAddTypeSwitchAllDirections 覆盖第 5 项：
// 添加供应商面板保持两段（智能体 / API Key），addType == 2 为 API Key 下的自定义端点子状态，
// 可通过「← API Key」回到 API 预设列表，也可直接切回智能体。
func TestUIReview15_ProviderAddTypeSwitchAllDirections(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.presets.loaded = true
	st.presets.items = []ProviderSetting{{ID: "deepseek", Name: "DeepSeek", Kind: "api"}}
	st.presets.addType = 2
	st.mu.Lock()
	openCustomEndpointForm(st, nil, endpointFormScopeCustom)
	st.mu.Unlock()

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-add-types")).Children(func() {
			renderProviderAddSheetBody(c, st, nil)
		})
	}, 1000, 3000)

	if !tt.HasText("端点名称") || !tt.HasText("← API Key") {
		t.Fatalf("addType==2 时应渲染自定义端点表单与返回按钮: %v", tt.Texts())
	}
	if tt.HasText("自定义端点") {
		t.Fatalf("类型分段不应包含第三段「自定义端点」: %v", tt.Texts())
	}

	if err := tt.Click("← API Key"); err != nil {
		t.Fatalf("点击 ← API Key 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.addType == 1 && tt.HasText("DeepSeek") && !tt.HasText("端点名称") && !st.endpoints.editing
	})

	if err := tt.Click("智能体"); err != nil {
		t.Fatalf("点击 智能体 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.addType == 0
	})

	if err := tt.Click("API Key"); err != nil {
		t.Fatalf("从智能体切到 API Key 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.addType == 1 && tt.HasText("DeepSeek")
	})

	if err := tt.Click("选择 自定义端点"); err != nil {
		t.Fatalf("进入自定义端点表单失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.addType == 2 && tt.HasText("端点名称") && tt.HasText("← API Key")
	})

	if err := tt.Click("智能体"); err != nil {
		t.Fatalf("从自定义端点切回 智能体 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.addType == 0 && !tt.HasText("端点名称") && !st.endpoints.editing
	})

	if err := tt.Click("API Key"); err != nil {
		t.Fatalf("从智能体切到 API Key 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.presets.addType == 1
	})
}

// TestUIReview15_WorkbenchProjctxBuiltOnceAcrossFrames 覆盖第 1 项：
// 工作台页连续渲染多帧时，projctx.New 只构建一次。
func TestUIReview15_WorkbenchProjctxBuiltOnceAcrossFrames(t *testing.T) {
	testenv.TempHome(t)
	project := newWorkbenchProject(t, map[string]string{"instructions": "# Rules"}, map[string]string{"api-design.md": "# API"})
	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Name: "demo", Path: project}}
	st := NewSettingsState(settingsTestDir(t), nil, s)

	before := workbenchContextBuilds.Load()
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	waitFor(t, tt, func() bool { return tt.HasText("Api Design") })

	for range 10 {
		tt.Frame()
	}

	if got := workbenchContextBuilds.Load() - before; got != 1 {
		t.Fatalf("工作台连续渲染多帧期间 projctx 构建次数 = %d，期望 1", got)
	}
}

// TestUIReview15_ShortcutsResetAllConfirmDialog 覆盖第 8 项：
// 点「全部恢复默认」先弹出 Danger 确认框，确认后才清空自定义快捷键。
func TestUIReview15_ShortcutsResetAllConfirmDialog(t *testing.T) {
	withShortcutGOOS(t, "darwin")
	t.Cleanup(func() { setShortcutOverrides(nil) })
	fake := NewFakeClient()
	dataDir := settingsTestDir(t)
	tt := ui.NewTester(Root(fake, WithSettings(dataDir, nil)), 1000, 3000)

	openShortcutsSettingsPage(t, tt)
	clickShortcutControl(t, tt, "快捷键 Git：本轮改动")
	tt.Type("Alt+Cmd+D")
	waitFor(t, tt, func() bool {
		id, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD)
		return ok && id == "toggle-diff"
	})

	if err := settingsClick(t, tt, "全部恢复默认"); err != nil {
		t.Fatalf("点击全部恢复默认失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("全部恢复默认快捷键？") })
	if id, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD); !ok || id != "toggle-diff" {
		t.Fatal("确认前不应清空自定义快捷键")
	}

	if err := tt.Click("取消"); err != nil {
		t.Fatalf("点击取消失败: %v", err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("全部恢复默认快捷键？") })
	if id, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD); !ok || id != "toggle-diff" {
		t.Fatal("取消确认后自定义快捷键应保留")
	}

	if err := settingsClick(t, tt, "全部恢复默认"); err != nil {
		t.Fatalf("再次点击全部恢复默认失败: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("全部恢复默认快捷键？") })
	if err := tt.Click("恢复默认"); err != nil {
		t.Fatalf("点击确认恢复默认失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, _, ok := matchShortcut(ui.Cmd|ui.Alt, ui.KeyD)
		return !ok && !tt.HasText("全部恢复默认快捷键？")
	})
}

// TestUIReview15_NavItemsTabFocusableAndActivatable 覆盖第 7 项：
// 左侧导航与搜索命中条目支持 Tab 聚焦，Enter / Space 可激活。
func TestUIReview15_NavItemsTabFocusableAndActivatable(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.open = true
	st.currentPage = "magpie"

	tt := ui.NewTester(func(c *ui.Context) { renderSettingsNav(c, st) }, 400, 900)

	focusedGeneral := false
	for range 20 {
		tt.Key(0, ui.KeyTab)
		if tt.Focused("通用") {
			focusedGeneral = true
			break
		}
	}
	if !focusedGeneral {
		t.Fatal("Tab 未能聚焦到左侧导航「通用」条目")
	}

	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.currentPage == "general"
	})

	// 下一项用 Space 也能激活。
	tt.Key(0, ui.KeyTab)
	if !tt.Focused("远程控制") {
		t.Fatal("Tab 未能聚焦到下一导航条目「远程控制」")
	}
	tt.Key(0, ui.KeySpace)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.currentPage == "remote"
	})

	// 搜索命中条目同样支持 Tab 聚焦与 Enter 激活。
	st.navSearch = "编辑主题"
	tt.Frame()
	focusedHit := false
	for range 20 {
		tt.Key(0, ui.KeyTab)
		if tt.Focused("编辑主题") {
			focusedHit = true
			break
		}
	}
	if !focusedHit {
		t.Fatal("Tab 未能聚焦到搜索命中条目「编辑主题」")
	}
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.currentPage == "general" && st.highlightItemID == "row-edit-themes"
	})
}

// TestUIReview15_OpenInDeleteConfirmAndEndpointDeleteLabelAndCopyRemoval 覆盖第 6、8、9 项：
// 打开方式删除弹确认框、自定义端点删除按钮无障碍标签含端点名（空名用「删除端点」）、多余说明文案已移除。
func TestUIReview15_OpenInDeleteConfirmAndEndpointDeleteLabelAndCopyRemoval(t *testing.T) {
	testenv.TempHome(t)
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Workbench.OpenInApps = []config.OpenInAppConfig{
		{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: true},
	}

	ttOpenIn := ui.NewTester(func(c *ui.Context) { renderPageOpenIn(c, st) }, 800, 600)
	if err := ttOpenIn.Click("删除 Cursor"); err != nil {
		t.Fatalf("点击删除应用失败: %v", err)
	}
	waitFor(t, ttOpenIn, func() bool { return ttOpenIn.HasText("删除「Cursor」？") })
	if len(st.cfg.Workbench.OpenInApps) != 1 {
		t.Fatal("确认前不应删除应用")
	}
	if err := ttOpenIn.Click("取消"); err != nil {
		t.Fatalf("取消删除应用失败: %v", err)
	}
	waitFor(t, ttOpenIn, func() bool { return !ttOpenIn.HasText("删除「Cursor」？") })
	if len(st.cfg.Workbench.OpenInApps) != 1 {
		t.Fatal("取消后不应删除应用")
	}
	if err := ttOpenIn.Click("删除 Cursor"); err != nil {
		t.Fatalf("再次点击删除应用失败: %v", err)
	}
	waitFor(t, ttOpenIn, func() bool { return ttOpenIn.HasText("删除「Cursor」？") })
	if err := ttOpenIn.Click("确认删除 Cursor"); err != nil {
		t.Fatalf("确认删除应用失败: %v", err)
	}
	waitFor(t, ttOpenIn, func() bool { return len(st.cfg.Workbench.OpenInApps) == 0 })

	// 端点删除按钮 Label：有名字时「删除 <名>」，空名时「删除端点」。
	epNamed := CustomEndpoint{ID: "ep-named", Name: "我的端点", BaseURL: "https://a.example.com/v1"}
	epEmpty := CustomEndpoint{ID: "ep-empty", Name: "", BaseURL: "https://b.example.com/v1"}
	stEp := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	stEp.endpoints.loaded = true
	stEp.endpoints.endpoints = []CustomEndpoint{epNamed, epEmpty}
	ttEp := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-ep-del")).Children(func() {
			renderPageCustomEndpoints(c, stEp)
		})
	}, 900, 800)
	if _, ok := ttEp.Find("删除 我的端点"); !ok {
		t.Fatal("有名称的端点删除按钮应标为「删除 我的端点」")
	}
	if _, ok := ttEp.Find("删除端点"); !ok {
		t.Fatal("空名称的端点删除按钮应标为「删除端点」")
	}

	// 第 9 项：重置行说明与 Worktree 清理页头说明已删除。
	ttReset := ui.NewTester(func(c *ui.Context) { renderSettingsResetArea(c, st, "conversation") }, 800, 200)
	if ttReset.HasText("把本页设置改回默认值；快捷回复、模型组合、档位等列表数据不受影响。") {
		t.Fatal("重置行说明文案应已删除")
	}
	if !ttReset.HasText("恢复本页默认") {
		t.Fatal("重置按钮应保留")
	}

	resetArchiveWorktreeStateForTest()
	t.Cleanup(resetArchiveWorktreeStateForTest)
	globalArchiveWorktreeState.mu.Lock()
	globalArchiveWorktreeState.loaded = true
	globalArchiveWorktreeState.mu.Unlock()
	ttWt := ui.NewTester(func(c *ui.Context) { renderArchiveWorktreeSection(c, st) }, 900, 600)
	if ttWt.HasText("扫描各项目遗留的 Worktree（孤立目录、已合入、有未提交改动），勾选后可彻底删除并回收磁盘空间；正在被会话使用的 Worktree 不可选择。") {
		t.Fatal("Worktree 清理页头说明文案应已删除")
	}
	if !ttWt.HasText("扫描") {
		t.Fatal("Worktree 扫描按钮应保留")
	}
}
