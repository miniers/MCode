package mcodeui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/projctx"
)

// newWorkbenchProject 在临时目录中搭建 .mcode/context：标准文档按给定内容落盘，customs 写入 docs/。
func newWorkbenchProject(t *testing.T, standards map[string]string, customs map[string]string) string {
	t.Helper()
	project := t.TempDir()
	svc, err := projctx.New(project)
	if err != nil {
		t.Fatalf("projctx.New failed: %v", err)
	}
	for name, content := range standards {
		ref, err := projctx.ParseRef(name)
		if err != nil {
			t.Fatalf("ParseRef(%q) failed: %v", name, err)
		}
		dir := svc.DocsPath()
		if ref.Standard {
			dir = svc.ContextDir()
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ref.Name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s failed: %v", name, err)
		}
	}
	for name, content := range customs {
		if err := os.MkdirAll(svc.DocsPath(), 0o755); err != nil {
			t.Fatalf("mkdir failed: %v", err)
		}
		if err := os.WriteFile(filepath.Join(svc.DocsPath(), name), []byte(content), 0o644); err != nil {
			t.Fatalf("write custom %s failed: %v", name, err)
		}
	}
	return project
}

// TestSettingsPageWorkbench_Tree 验证工作台页已经走声明式页面树：
// 分节和打开方式入口直接可见，快捷回复列表不再重复显示。
func TestSettingsPageWorkbench_Tree(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("decisions.md", "# Decisions")
	fake.SetContextDoc("notes.md", "# Notes")

	s := newChatState(fake)
	project := newWorkbenchProject(t, map[string]string{"instructions": "# Rules"}, map[string]string{"api-design.md": "# API"})
	s.projects = []Project{{ID: "p1", Name: "demo", Path: project}}

	st := NewSettingsState(settingsTestDir(t), nil, s)
	st.cfg.Workbench.ContextInjectMaxCharacters = 12000
	st.cfg.Workbench.StuckNudgeTimeout = 600
	st.cfg.Workbench.StuckInterruptTimeout = 1800
	st.cfg.Workbench.EditorFontSize = 16

	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)

	texts := make(map[string]bool)
	for _, text := range tt.Texts() {
		texts[text] = true
	}
	for _, want := range []string{
		"项目文档", "注入项目说明", "注入文档", "标准 4 · 自定义 1",
		"Instructions", "Overview", "Decisions", "Notes", "Api Design",
		"看板与任务", "默认视图", "看板", "列表",
		"验收后自动合并工作树", "完成后自动送审", "在独立工作树中执行",
		"注册看板 MCP 工具", "普通会话暴露单一入口", "创建后自动启动", "卡死任务自动救援", "无活动中断",
		"终端", "默认终端", "Web 预览", "起始地址", "捕获控制台输出",
		"编辑器与文件树", "自动保存",
		"打开方式", "高级", "注入字符上限", "回滚行数", "快捷端口",
	} {
		if !texts[want] {
			t.Fatalf("工作台页缺少 %q: %v", want, tt.Texts())
		}
	}

	// 旧说明句与旧折叠区文案已经删除。
	for _, gone := range []string{
		"提交信息生成", "工作区与工作树", "Worktree 清理",
		"终端字号", "编辑器字号", "快捷回复列表",
		"注入项目说明时允许使用的最大字符数",
		"暂无自定义文档，可在项目 .mcode/context/docs 目录下添加 Markdown 文档后在此选择注入",
	} {
		if tt.HasText(gone) {
			t.Fatalf("工作台页不该再有 %q", gone)
		}
	}
}

func TestSettingsWorkbenchReviewValueUnchanged(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	value := false
	st.cfg.Workbench.KanbanAutoReview = &value
	ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	if st.cfg.Workbench.KanbanAutoReview == nil || *st.cfg.Workbench.KanbanAutoReview {
		t.Fatal("渲染设置页改变了自动送审配置")
	}
}

func TestSettingsDependentControls(t *testing.T) {
	findRow := func(page settingsPage, id string) setting {
		t.Helper()
		for _, section := range page.Sections {
			for _, row := range section.Rows {
				if row.ID == id {
					return row
				}
			}
		}
		t.Fatalf("找不到设置行 %s", id)
		return setting{}
	}
	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Name: "demo", Path: newWorkbenchProject(t, nil, map[string]string{"api-design.md": "# API"})}}
	st := NewSettingsState(settingsTestDir(t), nil, s)
	kanban := findRow(pageWorkbench, "row-kanban-entry-ordinary")
	docs := findRow(pageWorkbench, "row-context-doc-")
	budget := findRow(pageWorkbench, "row-context-inject-budget")
	quit := findRow(pageGeneral, "row-quit-action")
	for _, enabled := range []bool{false, true} {
		st.cfg.Workbench.KanbanMCPEnabled = &enabled
		st.cfg.Workbench.ContextInjectsGuidance = &enabled
		st.cfg.Startup.ShowTrayIcon = enabled
		for _, row := range []setting{kanban, docs, budget, quit} {
			if row.Enabled == nil || row.Enabled(st) != enabled {
				t.Errorf("%s 的可用状态应为 %v", row.ID, enabled)
			}
		}
	}
	st.cfg.Startup.QuitAction = config.QuitActionMinimizeToTray
	st.cfg.Startup.ShowTrayIcon = false
	if got := quit.Control.(choiceControl).current(st); got != config.QuitActionQuit {
		t.Errorf("无托盘时应显示退出应用，实际 %q", got)
	}
	st.cfg.Startup.ShowTrayIcon = true
	if got := quit.Control.(choiceControl).current(st); got != config.QuitActionMinimizeToTray {
		t.Errorf("恢复托盘后应显示已保存的动作，实际 %q", got)
	}
	st.cfg.Startup.ShowTrayIcon = false
	st.cfg.Workbench.ContextInjectsGuidance = new(bool)
	st.cfg.Workbench.KanbanMCPEnabled = new(bool)
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageWorkbench(c, st)
		renderSettingsTreeSetting(c, st, quit)
	}, 1000, 4000)
	if !tt.HasText("退出应用") {
		t.Fatal("关闭窗口选项缺少退出应用")
	}
	if err := tt.Click("切换Api Design"); err != nil {
		t.Fatalf("点击文档开关失败: %v", err)
	}
	if err := tt.Click("切换普通会话暴露单一入口"); err != nil {
		t.Fatalf("点击看板入口开关失败: %v", err)
	}
	if err := tt.Click("退出应用"); err != nil {
		t.Fatalf("点击退出应用失败: %v", err)
	}
	tt.Frame()
	if contextInjectDocEnabled(st.cfg.Workbench.ContextInjectCustomDocs, "docs/api-design.md") {
		t.Fatal("禁用的文档开关不应写入配置")
	}
	if st.cfg.Startup.QuitAction != config.QuitActionMinimizeToTray {
		t.Fatal("无托盘时不应改写已保存的关闭窗口动作")
	}
	if !st.cfg.Workbench.KanbanEntryOrdinarySessions() {
		t.Fatal("禁用的看板入口开关不应写入配置")
	}
	*st.cfg.Workbench.KanbanMCPEnabled = true
	*st.cfg.Workbench.ContextInjectsGuidance = true
	tt.Frame()
	if err := tt.Click("切换普通会话暴露单一入口"); err != nil {
		t.Fatalf("点击已启用的看板入口开关失败: %v", err)
	}
	waitFor(t, tt, func() bool { return !st.cfg.Workbench.KanbanEntryOrdinarySessions() })
	if err := tt.Click("切换Api Design"); err != nil {
		t.Fatalf("点击已启用的文档开关失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return contextInjectDocEnabled(st.cfg.Workbench.ContextInjectCustomDocs, "docs/api-design.md")
	})
}

func TestSettingsSheetOpenIn(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	st.cfg.Workbench.OpenInApps = []config.OpenInAppConfig{
		{Name: "Cursor", AppPath: "/Applications/Cursor.app", Enabled: true},
		{Name: "Zed", AppPath: "/Applications/Zed.app", Enabled: true},
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageWorkbench(c, st)
		if openSheet, _ := settingsTreeSheetState(st); openSheet != "" {
			if sheet, ok := settingsTreeSheetOf(openSheet); ok {
				renderSettingsTreeSheet(c, st, sheet)
			}
		}
	}, 1000, 4000)

	count := fmt.Sprintf("%d 个应用", len(st.cfg.Workbench.OpenInApps))
	if !tt.HasText("打开方式") || !tt.HasText(count) || tt.HasText("Cursor") {
		t.Fatalf("打开方式入口或初始面板状态不正确: %v", tt.Texts())
	}
	if err := tt.Click(count); err != nil {
		t.Fatalf("点击打开方式入口失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		open, _ := settingsTreeSheetState(st)
		return open == "open-in" && tt.HasText("Cursor")
	})
	if err := tt.Click("关闭打开方式"); err != nil {
		t.Fatalf("关闭打开方式面板失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		open, _ := settingsTreeSheetState(st)
		return open == "" && !tt.HasText("Cursor")
	})
}

// TestWorkbenchSettings_ToggleAndPersist 验证开关写入配置，默认视图写回配置。
func TestWorkbenchSettings_ToggleAndPersist(t *testing.T) {
	fake := NewFakeClient()
	fake.SetContextDoc("api-design.md", "# API")

	s := newChatState(fake)
	s.projects = []Project{{ID: "p1", Name: "demo", Path: newWorkbenchProject(t, nil, map[string]string{"api-design.md": "# API"})}}
	dataDir := settingsTestDir(t)
	st := NewSettingsState(dataDir, nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)

	initialGuidance := st.cfg.Workbench.ContextGuidanceEnabled()
	if err := tt.Click("切换注入项目说明"); err != nil {
		t.Fatalf("click guidance switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Workbench.ContextInjectsGuidance != nil &&
			*st.cfg.Workbench.ContextInjectsGuidance != initialGuidance
	})
	if !st.cfg.Workbench.ContextGuidanceEnabled() {
		if err := tt.Click("切换注入项目说明"); err != nil {
			t.Fatalf("重新开启项目说明失败: %v", err)
		}
		waitFor(t, tt, func() bool { return st.cfg.Workbench.ContextGuidanceEnabled() })
	}

	if err := tt.Click("切换Api Design"); err != nil {
		t.Fatalf("click custom doc switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return slices.Contains(st.cfg.Workbench.ContextInjectCustomDocs, "docs/api-design.md")
	})

	if err := tt.Click("列表"); err != nil {
		t.Fatalf("click list view segment failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return st.cfg.Workbench.KanbanDefaultView == config.KanbanViewList
	})

	cfg, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("load saved config failed: %v", err)
	}
	if !contextInjectDocEnabled(cfg.Workbench.ContextInjectCustomDocs, "docs/api-design.md") {
		t.Fatalf("自定义文档开关未持久化: %+v", cfg.Workbench.ContextInjectCustomDocs)
	}
	if !slices.Contains(cfg.Workbench.ContextInjectCustomDocs, "docs/api-design.md") {
		t.Fatalf("自定义文档应以 docs/<file> 作为 key 保存: %+v", cfg.Workbench.ContextInjectCustomDocs)
	}
	if cfg.Workbench.KanbanDefaultView != config.KanbanViewList {
		t.Fatalf("默认视图未持久化: %q", cfg.Workbench.KanbanDefaultView)
	}
}

// countText 统计渲染树中文本包含 text 的节点数量；长文案换行时仍按完整文本登记。
func countText(tt *ui.Tester, text string) int {
	n := 0
	for _, t := range tt.Texts() {
		if strings.Contains(t, text) {
			n++
		}
	}
	return n
}

// TestWorkbenchSettings_NoCustomDocs 验证没有自定义文档时只显示计数，不再渲染整行空态说明。
func TestWorkbenchSettings_NoCustomDocs(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.projects = []Project{{ID: "p1", Name: "demo", Path: newWorkbenchProject(t, nil, nil)}}

	st := NewSettingsState(settingsTestDir(t), nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 420, 4000)

	if !tt.HasText("标准 4 · 自定义 0") {
		t.Fatalf("计数应显示标准 4 与自定义 0: %v", tt.Texts())
	}
	if tt.HasText("暂无自定义文档") {
		t.Fatalf("没有自定义文档时不该再渲染空态说明: %v", tt.Texts())
	}
	if !tt.HasText("Notes") {
		t.Fatalf("标准文档仍应列出: %v", tt.Texts())
	}
}

// TestWorkbenchSettings_NoManifest 验证没有建立项目文档时只在注入文档行显示一次“未建立项目文档”。
func TestWorkbenchSettings_NoManifest(t *testing.T) {
	s := newChatState(NewFakeClient())
	// 未选择任何项目，workbenchContextManifest 返回 nil。
	st := NewSettingsState(settingsTestDir(t), nil, s)
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)

	if n := countText(tt, "未建立项目文档"); n != 1 {
		t.Fatalf("“未建立项目文档”应只出现一次，实际 %d 次: %v", n, tt.Texts())
	}
	if !tt.HasText("注入文档") {
		t.Fatalf("缺少注入文档行: %v", tt.Texts())
	}
	if tt.HasText("暂无自定义文档") {
		t.Fatalf("manifest 为 nil 时不应渲染自定义文档空态说明: %v", tt.Texts())
	}
}

// TestWorkbenchSettings_StandardDocsListed 验证四篇标准文档即使未创建也排在自定义文档之前，
// 开关按 refID 保存，并且与注入侧 BuildGuidanceWithOptions 的匹配 key 一致。
func TestWorkbenchSettings_StandardDocsListed(t *testing.T) {
	project := newWorkbenchProject(t, map[string]string{"instructions": "# Rules"}, map[string]string{"api-design.md": "# API"})
	s := newChatState(NewFakeClient())
	s.projects = []Project{{ID: "p1", Name: "demo", Path: project}}
	st := NewSettingsState(settingsTestDir(t), nil, s)

	docs := workbenchContextInjectEntries(workbenchContextManifest(st))
	if len(docs) != 5 {
		t.Fatalf("expected 4 standard + 1 custom docs, got %+v", docs)
	}
	wantRefs := []string{"instructions", "overview", "decisions", "notes", "docs/api-design.md"}
	wantTitles := []string{"Instructions", "Overview", "Decisions", "Notes", "Api Design"}
	for i, doc := range docs {
		if doc.refID != wantRefs[i] || doc.title != wantTitles[i] {
			t.Fatalf("doc %d mismatch: %+v", i, doc)
		}
	}
	if !docs[0].exists || docs[1].exists {
		t.Fatalf("标准文档存在状态错误: %+v", docs[:2])
	}

	// 注入侧：标准名匹配根部文件，自定义文档用 docs/<file> 匹配；历史保存的文件名仍需被识别。
	guidance, err := projctx.BuildGuidanceWithOptions(project, projctx.GuidanceOptions{CustomDocs: []string{contextInjectDocKey("api-design.md")}})
	if err != nil {
		t.Fatalf("BuildGuidanceWithOptions failed: %v", err)
	}
	if !strings.Contains(guidance, "# Rules") || !strings.Contains(guidance, "# API") {
		t.Fatalf("注入路径未读到标准/自定义文档: %q", guidance)
	}

	// 开关渲染：标准文档开关置灰，自定义文档可切换，保存的 key 为 refID。
	tt := ui.NewTester(func(c *ui.Context) { renderPageWorkbench(c, st) }, 1000, 4000)
	if err := tt.Click("切换Instructions"); err != nil {
		t.Fatalf("standard doc switch should be disabled: %v", err)
	}
	if contextInjectDocEnabled(st.cfg.Workbench.ContextInjectCustomDocs, "instructions") {
		t.Fatalf("置灰的标准文档开关不应写入配置: %+v", st.cfg.Workbench.ContextInjectCustomDocs)
	}
	if err := tt.Click("切换Api Design"); err != nil {
		t.Fatalf("click custom doc switch failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return slices.Contains(st.cfg.Workbench.ContextInjectCustomDocs, "docs/api-design.md")
	})
	if !contextInjectDocEnabled(st.cfg.Workbench.ContextInjectCustomDocs, "api-design.md") {
		t.Fatalf("历史文件名 key 应被识别: %+v", st.cfg.Workbench.ContextInjectCustomDocs)
	}
	if len(setContextInjectDocEnabled([]string{"instructions", "notes.md"}, "notes.md", false)) != 1 {
		t.Fatal("取消开关时应同时清除按文件名保存的历史 key")
	}
}

func TestSettingsWorkbench_WorktreeSheetAndSourceControl(t *testing.T) {
	resetArchiveWorktreeStateForTest()
	t.Cleanup(resetArchiveWorktreeStateForTest)
	globalArchiveWorktreeState.mu.Lock()
	globalArchiveWorktreeState.loaded = true
	globalArchiveWorktreeState.items = []archiveWorktreeItem{{Path: "/wt/example", Clean: true}}
	globalArchiveWorktreeState.mu.Unlock()
	st := NewSettingsState(settingsTestDir(t), nil, newChatState(NewFakeClient()))
	tt := ui.NewTester(func(c *ui.Context) {
		renderPageSourceControl(c, st)
		if id, _ := settingsTreeSheetState(st); id != "" {
			if sh, ok := settingsTreeSheetOf(id); ok {
				renderSettingsTreeSheet(c, st, sh)
			}
		}
	}, 1000, 5000)
	page, ok := settingsTreePageOf("sourceControl")
	if !ok {
		t.Fatal("版本控制页未注册")
	}
	rows := make(map[string]bool)
	for _, section := range page.Sections {
		for _, row := range section.Rows {
			if rows[row.ID] {
				t.Fatalf("版本控制页行 ID 重复: %q", row.ID)
			}
			rows[row.ID] = true
		}
	}
	for _, id := range []string{
		"row-sc-generate-commit-msg", "row-sc-commit-instructions", "row-sc-commit-model",
		"row-sc-default-workspace-mode", "row-sc-auto-cleanup-delete", "row-sc-auto-cleanup-merged",
		"row-sc-worktree-cleanup",
	} {
		if !rows[id] {
			t.Fatalf("版本控制页缺少版本控制行: %q", id)
		}
	}
	if len(page.Sections) == 0 || page.Sections[len(page.Sections)-1].ID != "advanced.sourceControl" {
		t.Fatal("版本控制页末尾应为高级设置")
	}
	advancedRows := make(map[string]bool)
	for _, row := range page.Sections[len(page.Sections)-1].Rows {
		advancedRows[row.ID] = true
	}
	for _, id := range []string{"row-sc-init-submodules", "row-sc-clone-ignored", "row-sc-clone-ignored-patterns"} {
		if !advancedRows[id] {
			t.Fatalf("版本控制高级设置缺少行 %q", id)
		}
	}
	for _, text := range []string{"自动生成提交信息", "默认工作区模式", "Worktree 清理"} {
		if !tt.HasText(text) {
			t.Fatalf("版本控制页缺少 %q: %v", text, tt.Texts())
		}
	}
	if err := tt.Click("Worktree 清理 1 个"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("/wt/example") })
	if err := tt.Click("example"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("清理所选"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("清理 Worktree") && tt.HasText("确认删除") })
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, func() bool { return !tt.HasText("确认删除") })
}
