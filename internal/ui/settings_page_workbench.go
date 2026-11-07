package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/projctx"
)

// advancedWorkbench 是工作台页的高级设置。
var advancedWorkbench = settingsSection{
	ID:    "advanced.workbench",
	Title: "高级",
	Rows: []setting{
		{
			ID:      "row-context-inject-budget",
			Title:   "注入字符上限",
			Aliases: []string{"项目文档", "注入", "字符", "上限", "context", "最大"},
			Control: number(val(func(c *config.Config) *int { return &c.Workbench.ContextInjectMaxCharacters }, config.DefaultContextInjectMaxCharacters), "字符", config.MinContextInjectMaxCharacters, config.MaxContextInjectMaxCharacters),
		},
		{
			ID:      "row-terminal-scrollback-lines",
			Title:   "回滚行数",
			Aliases: []string{"终端", "缓冲区", "行数", "scrollback", "历史"},
			Control: number(val(func(c *config.Config) *int { return &c.Workbench.TerminalScrollbackLines }, config.DefaultTerminalScrollback), "行", config.MinTerminalScrollback, 100000),
		},
		{
			ID:      "row-browser-quick-ports",
			Title:   "快捷端口",
			Aliases: []string{"浏览器", "端口", "预览", "port", "localhost", "逗号"},
			Control: text(val(func(c *config.Config) *string { return &c.Workbench.BrowserQuickPorts }, ""), "3000, 5173, 8080, 4321"),
		},
	},
}

var pageWorkbench = settingsPage{
	ID:    "workbench",
	Title: "工作台",
	Icon:  IconLayoutDashboard(),
	Keywords: []string{
		"workbench", "kanban", "board", "task", "tasks", "editor", "browser",
		"preview", "console", "context", "knowledge", "inject", "injection",
		"font", "size", "工作台", "看板", "任务",
		"open in", "editors", "apps", "applications", "finder", "vscode",
		"cursor", "zed", "xcode", "iterm", "terminal", "shell", "qspace", "打开方式", "编辑器", "应用",
		"quick", "reply", "replies", "canned", "snippet", "template",
		"快捷回复", "常用语", "短语", "文件树",
	},
	Sections: []settingsSection{
		{
			ID:    "workbench.context",
			Title: "项目文档",
			Rows: []setting{
				{
					ID:      "row-context-guidance",
					Title:   "注入项目说明",
					Aliases: []string{"项目文档", "AGENTS.md", "首条消息", "说明", "概览"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.ContextInjectsGuidance }, true)),
				},
				{
					ID:      "row-context-doc-",
					Title:   "注入文档",
					Aliases: []string{"项目文档", "文档", "注入", "context", "自定义", "额外", "四篇"},
					Control: workbenchDocsControl{},
				},
			},
		},
		{
			ID:    "workbench.kanban",
			Title: "看板与任务",
			Rows: []setting{
				{
					ID:      "row-kanban-default-view",
					Title:   "默认视图",
					Aliases: []string{"看板", "视图", "kanban", "list", "任务", "工作区"},
					Control: choice(val(func(c *config.Config) *string { return &c.Workbench.KanbanDefaultView }, config.KanbanViewKanban), []choiceOption{
						{Value: config.KanbanViewKanban, Label: "看板"},
						{Value: config.KanbanViewList, Label: "列表"},
					}),
				},
				{
					ID:      "row-tasks-auto-merge-worktree",
					Title:   "验收后自动合并工作树",
					Aliases: []string{"看板", "任务", "验收", "合并", "worktree"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.TasksAutoMergeWorktree }, true)),
				},
				{
					ID:      "row-kanban-auto-review",
					Title:   "完成后自动转为 Review",
					Aliases: []string{"看板", "任务", "完成", "验收", "review", "审查", "子代理"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.KanbanAutoReview }, true)),
				},
				{
					ID:      "row-kanban-run-in-worktree",
					Title:   "在独立工作树中执行",
					Aliases: []string{"看板", "任务", "工作树", "worktree"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.KanbanRunInWorktree }, true)),
				},
				{
					ID:      "row-kanban-mcp-tools",
					Title:   "注册看板 MCP 工具",
					Aliases: []string{"看板", "MCP", "工具", "tool", "会话", "agent"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.KanbanMCPEnabled }, true)),
				},
				{
					ID:      "row-kanban-entry-ordinary",
					Title:   "普通会话暴露单一入口",
					Aliases: []string{"看板", "会话", "入口", "kanban", "tool"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.KanbanEntryOrdinary }, true)),
				},
				{
					ID:      "row-kanban-auto-start-on-create",
					Title:   "创建后自动启动",
					Aliases: []string{"看板", "任务", "创建", "启动"},
					Control: toggle(val(func(c *config.Config) *bool { return &c.Workbench.KanbanAutoStartOnCreate }, false)),
				},
				{
					ID:      "row-kanban-auto-rescue-stuck",
					Title:   "卡死任务自动救援",
					Aliases: []string{"看板", "任务", "卡死", "救援", "stuck"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.AutoRescueStuck }, true)),
				},
				{
					ID:      "row-kanban-stuck-nudge-timeout",
					Title:   "无活动提醒",
					Aliases: []string{"看板", "卡死", "提醒", "超时", "stuck", "分钟"},
					Control: nudgeTimeoutControl{},
					Enabled: func(st *SettingsState) bool { return st.cfg.Workbench.AutoRescueStuckEnabled() },
				},
				{
					ID:      "row-kanban-stuck-interrupt-timeout",
					Title:   "无活动中断",
					Aliases: []string{"看板", "卡死", "中断", "超时", "stuck"},
					Control: interruptTimeoutControl{},
					Enabled: func(st *SettingsState) bool { return st.cfg.Workbench.AutoRescueStuckEnabled() },
				},
			},
		},
		{
			ID:    "workbench.terminal",
			Title: "终端",
			Rows: []setting{
				{
					ID:      "row-terminal-shell",
					Title:   "默认终端",
					Aliases: []string{"终端", "shell", "zsh", "bash", "fish", "iterm"},
					Control: terminalShellControl{},
				},
			},
		},
		{
			ID:    "workbench.preview",
			Title: "Web 预览",
			Rows: []setting{
				{
					ID:      "row-browser-start-url",
					Title:   "起始地址",
					Aliases: []string{"浏览器", "地址", "URL", "localhost", "预览", "初始"},
					Control: text(val(func(c *config.Config) *string { return &c.Workbench.BrowserStartURL }, ""), config.DefaultBrowserStartURL),
				},
				{
					ID:      "row-browser-console-capture",
					Title:   "捕获控制台输出",
					Aliases: []string{"浏览器", "控制台", "console", "日志", "错误", "js"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.BrowserConsoleCapture }, true)),
				},
			},
		},
		{
			ID:    "workbench.editor",
			Title: "编辑器与文件树",
			Rows: []setting{
				{
					ID:      "row-editor-auto-save",
					Title:   "自动保存",
					Aliases: []string{"编辑器", "保存", "autosave", "代码"},
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.Workbench.EditorAutoSave }, true)),
				},
			},
		},
		{
			ID:    "workbench.openIn",
			Title: "",
			Rows: []setting{
				{
					ID:    "row-workbench-open-in",
					Title: "打开方式",
					Control: link(func(st *SettingsState) string {
						return fmt.Sprintf("%d 个应用", len(st.openInAppsDraft()))
					}, "open-in"),
				},
			},
		},
		advancedWorkbench,
	},
}

func init() {
	settingsTreePageDefs = append(settingsTreePageDefs, pageWorkbench)
}

// renderPageWorkbench 渲染工作台设置页：项目文档注入、看板任务自动化、终端、Web 预览、编辑器参数，
// 随后是打开方式与高级设置。
func renderPageWorkbench(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageWorkbench)
}

// workbenchDocsControl 渲染项目文档的注入开关。文档清单随项目变化，无法静态声明成行，
// 因此在这里按清单动态绘制开关，并把结果写回 ContextInjectCustomDocs。
type workbenchDocsControl struct{}

func (d workbenchDocsControl) render(c *ui.Context, st *SettingsState, s setting) {
	d.renderDisabled(c, st, s, false)
}

func (d workbenchDocsControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	docs := workbenchContextInjectEntries(workbenchContextManifest(st))
	if len(docs) == 0 {
		settingStatus(c, s.ID+"-empty", "未建立项目文档")
		return
	}
	standardCount := 0
	customCount := 0
	for _, doc := range docs {
		if doc.standard {
			standardCount++
		} else {
			customCount++
		}
	}
	ui.Column(c.Key(s.ID + "-list")).Gap(settingsCompactPadX).Children(func() {
		ui.Text(c, fmt.Sprintf("标准 %d · 自定义 %d", standardCount, customCount)).
			FontSize(TypeFootnote).
			TextColor(ThemeSecondaryText(c))
		for _, doc := range docs {
			doc := doc
			always := doc.refID == projctx.DocInstructions || doc.refID == projctx.DocOverview
			enabled := always || contextInjectDocEnabled(st.cfg.Workbench.ContextInjectCustomDocs, doc.refID)
			value := enabled
			rowID := s.ID + doc.refID
			ctl := toggle(bind[bool]{plain: func(*config.Config) *bool { return &value }, def: true})
			ui.Row(c.Key(rowID)).Gap(Space4).AlignItems(ui.Center).Children(func() {
				ui.Text(c, doc.title).FontSize(TypeProse).FontWeight(500).TextColor(ThemePrimaryText(c)).SingleLine().Grow(1)
				ctl.renderDisabled(c, st, setting{ID: rowID, Title: doc.title}, disabled || always)
				if always {
					settingBadge(c, rowID+"-always", "始终注入")
				} else if doc.standard && !doc.exists {
					settingBadge(c, rowID+"-missing", "未创建")
				}
			})
			if disabled || always || value == enabled {
				continue
			}
			commitSettingsControl(st, func(cfg *config.Config) {
				cfg.Workbench.ContextInjectCustomDocs = setContextInjectDocEnabled(cfg.Workbench.ContextInjectCustomDocs, doc.refID, value)
			})
		}
	})
}

func (d workbenchDocsControl) reset(cfg *config.Config) {
	cfg.Workbench.ContextInjectCustomDocs = nil
}

// nudgeTimeoutControl 在提醒阈值变化后把中断阈值抬到提醒之后，避免出现提醒晚于中断的组合。
type nudgeTimeoutControl struct{}

func (n nudgeTimeoutControl) number() numberControl {
	return number(val(func(c *config.Config) *int { return &c.Workbench.StuckNudgeTimeout }, config.DefaultStuckNudgeTimeout), "秒", 300, config.MaxStuckNudgeTimeout)
}

func (n nudgeTimeoutControl) render(c *ui.Context, st *SettingsState, s setting) {
	n.renderDisabled(c, st, s, false)
}

func (n nudgeTimeoutControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	old := st.cfg.Workbench.StuckNudgeTimeout
	n.number().renderDisabled(c, st, s, disabled)
	if disabled || st.cfg.Workbench.StuckNudgeTimeout == old {
		return
	}
	if st.cfg.Workbench.StuckInterruptTimeout > st.cfg.Workbench.StuckNudgeTimeout {
		return
	}
	commitSettingsControl(st, func(cfg *config.Config) {
		cfg.Workbench.StuckInterruptTimeout = cfg.Workbench.StuckNudgeTimeout + 60
	})
}

func (n nudgeTimeoutControl) reset(cfg *config.Config) {
	n.number().reset(cfg)
}

// interruptTimeoutControl 的下限跟随提醒阈值，保证中断总在提醒之后。
type interruptTimeoutControl struct{}

func (i interruptTimeoutControl) render(c *ui.Context, st *SettingsState, s setting) {
	i.renderDisabled(c, st, s, false)
}

func (i interruptTimeoutControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	nudge := st.cfg.Workbench.StuckNudgeTimeout
	if nudge <= 0 {
		nudge = config.DefaultStuckNudgeTimeout
	}
	lower := max(nudge+60, 360)
	lower = min(lower, config.MaxStuckInterruptTimeout)
	number(val(func(c *config.Config) *int { return &c.Workbench.StuckInterruptTimeout }, config.DefaultStuckInterruptTimeout), "秒", lower, config.MaxStuckInterruptTimeout).
		renderDisabled(c, st, s, disabled)
}

func (i interruptTimeoutControl) reset(cfg *config.Config) {
	cfg.Workbench.StuckInterruptTimeout = config.DefaultStuckInterruptTimeout
}

// terminalShellControl 渲染默认终端选择；选项来自当前系统可用的终端列表。
type terminalShellControl struct{}

func (t terminalShellControl) render(c *ui.Context, st *SettingsState, s setting) {
	t.renderDisabled(c, st, s, false)
}

func (t terminalShellControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	options, current := terminalAppOptions(st.cfg.Workbench.TerminalShell)
	old := current
	sel := ui.Select(c.Key(s.ID+"-select"), &current, options).
		Label(s.Title).
		Width(settingsSidePanelWidth).
		Disabled(disabled)
	if disabled || (current == old && !sel.Changed()) {
		return
	}
	commitSettingsControl(st, func(cfg *config.Config) { cfg.Workbench.TerminalShell = current })
}

func (t terminalShellControl) reset(cfg *config.Config) {
	cfg.Workbench.TerminalShell = ""
}

type workbenchInjectDoc struct {
	refID    string // 配置中保存的 key
	title    string // 展示标题
	standard bool
	exists   bool
}

// workbenchContextManifest 读取当前项目文档清单；没有关联项目或读取失败时返回 nil。
func workbenchContextManifest(st *SettingsState) *projctx.Manifest {
	if st == nil {
		return nil
	}
	st.mu.Lock()
	chat := st.chat
	st.mu.Unlock()
	if chat == nil || chat.client == nil {
		return nil
	}
	projectPath := chat.getSelectedProjectPath()
	if projectPath == "" {
		return nil
	}
	svc, err := projctx.New(projectPath)
	if err != nil {
		return nil
	}
	manifest, err := svc.Manifest()
	if err != nil {
		return nil
	}
	return &manifest
}

// workbenchContextInjectEntries 把清单展开为注入开关列表：四篇标准文档在前（缺失也列出），自定义文档在后。
// refID 与注入侧 BuildGuidanceWithOptions 使用的 key 对齐：标准文档为名称，自定义文档为 docs/<file>。
func workbenchContextInjectEntries(manifest *projctx.Manifest) []workbenchInjectDoc {
	if manifest == nil {
		return nil
	}
	var docs []workbenchInjectDoc
	for _, info := range manifest.Standard() {
		docs = append(docs, workbenchInjectDoc{refID: info.RefID(), title: info.Title, standard: true, exists: info.Exists})
	}
	for _, info := range manifest.Custom() {
		docs = append(docs, workbenchInjectDoc{refID: info.RefID(), title: info.Title, exists: info.Exists})
	}
	return docs
}

// workbenchContextCustomDocs 返回 docs/ 下的自定义文档信息。
func workbenchContextCustomDocs(manifest *projctx.Manifest) []projctx.DocInfo {
	if manifest == nil {
		return nil
	}
	return manifest.Custom()
}

// contextInjectDocKey 把配置中保存的注入 key 规整为文档 refID；无法识别时原样返回。
// 历史配置里自定义文档按文件名保存（notes.md），这里规整为 docs/notes.md 以兼容旧值。
func contextInjectDocKey(name string) string {
	// docs/ 下的自定义文档不能交给 ParseRef：它去掉前缀后会按标准名匹配，docs/notes.md 会被当成标准文档 notes。
	if strings.HasPrefix(name, projctx.DocsDir+"/") {
		return name
	}
	ref, err := projctx.ParseRef(name)
	if err != nil {
		return name
	}
	if ref.Standard {
		return ref.Info.Name
	}
	return projctx.DocsDir + "/" + ref.Name
}

// contextInjectDocEnabled 判断文档是否已被选入注入列表；name 可以是 refID 或历史保存的文件名。
func contextInjectDocEnabled(names []string, name string) bool {
	want := contextInjectDocKey(name)
	for _, n := range names {
		if n == name || contextInjectDocKey(n) == want {
			return true
		}
	}
	return false
}

// setContextInjectDocEnabled 按开关状态增删注入文档，保持其余顺序不变；name 以 refID 形式保存。
func setContextInjectDocEnabled(names []string, name string, enabled bool) []string {
	key := contextInjectDocKey(name)
	out := make([]string, 0, len(names)+1)
	for _, n := range names {
		if n == key || n == name || contextInjectDocKey(n) == key {
			continue
		}
		out = append(out, n)
	}
	if enabled {
		out = append(out, key)
	}
	return out
}

// renderWorkbenchAdvancedSections 保留给旧分发分支引用；工作台页已由页面树接管，进阶项在 advancedWorkbench。
func renderWorkbenchAdvancedSections(c *ui.Context, st *SettingsState) {}
