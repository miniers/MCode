package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
	"mcode/internal/git"
)

// advancedSourceControl 是版本控制页的高级设置。
var advancedSourceControl = settingsSection{
	ID:    "advanced.sourceControl",
	Title: "高级",
	Rows: []setting{
		{
			ID:      "row-sc-init-submodules",
			Title:   "新建工作树时初始化子模块",
			Aliases: []string{"工作树", "子模块", "submodule", "worktree", "默认"},
			Lock:    "init-submodules",
			Control: gitToggleControl{
				b: ptr(func(c *config.Config) **bool { return &c.SourceControl.InitSubmodules }, true),
				sync: func(cfg *config.Config) {
					git.SetInitSubmodules(cfg.SourceControl.InitSubmodulesEnabled())
				},
			},
		},
		{
			ID:      "row-sc-clone-ignored",
			Title:   "新建工作树时克隆依赖目录",
			Aliases: []string{"工作树", "克隆", "依赖", "node_modules", "worktree", "APFS", "复制", "忽略"},
			Lock:    "clone-ignored-on-worktree",
			Enabled: func(st *SettingsState) bool { return git.CloneIgnoredSupported() },
			Control: gitToggleControl{
				b: ptr(func(c *config.Config) **bool { return &c.SourceControl.CloneIgnoredOnWorktree }, true),
				sync: func(cfg *config.Config) {
					git.SetCloneIgnored(cfg.SourceControl.CloneIgnoredOnWorktreeEnabled(), cfg.SourceControl.CloneIgnoredPatternList())
				},
			},
		},
		{
			ID:      "row-sc-clone-ignored-patterns",
			Title:   "复制的目录名单",
			Aliases: []string{"工作树", "克隆", "目录", "glob", "node_modules", "逗号", "通配"},
			Lock:    "clone-ignored-on-worktree",
			Enabled: func(st *SettingsState) bool {
				return git.CloneIgnoredSupported() && st.cfg.SourceControl.CloneIgnoredOnWorktreeEnabled()
			},
			Control: gitPatternsControl{},
		},
	},
}

var pageSourceControl = settingsPage{
	ID:    "sourceControl",
	Title: "版本控制",
	Icon:  IconGitBranch(),
	Keywords: []string{
		"git", "commit", "pull request", "titles", "text generation", "github", "clone",
		"repository", "repositories", "repo", "sign in", "token", "connect", "account",
		"版本控制", "仓库", "分支",
	},
	Sections: []settingsSection{
		{
			ID:    "sourceControl.commit",
			Title: "提交信息生成",
			Rows: []setting{
				{
					ID:      "row-sc-generate-commit-msg",
					Title:   "自动生成提交信息",
					Aliases: []string{"提交", "commit", "utility", "模型"},
					Lock:    "generate-commit-message",
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.SourceControl.GenerateCommitMessage }, true)),
				},
				{
					ID:      "row-sc-commit-instructions",
					Title:   "提交信息附加说明",
					Aliases: []string{"提交", "commit", "规范", "指令", "prompt"},
					Lock:    "commit-instructions",
					Control: commitInstructionsControl{},
				},
				{
					ID:      "row-sc-commit-model",
					Title:   "生成提交信息与会话标题的模型",
					Aliases: []string{"模型", "model", "utility", "会话标题", "提交"},
					Control: resettableCustomControl{
						renderFn: func(c *ui.Context, st *SettingsState, s setting) {
							catalog := settingsModelPickerCatalog(st)
							providers := catalog.Providers
							sel := modelSelectionFromConfig(st.cfg.SourceControl.CommitMessageModel)
							sel = resolveModelPickerSelection(providers, sel)
							renderModelPicker(c, catalog.apply(ModelPickerProps{
								Mode:         ModelPickerSimple,
								KeyPrefix:    "sc-commit-",
								ScopeKey:     "sc-commit",
								Selection:    sel,
								CapsuleLabel: modelShortDisplayName(providers, sel.Provider, sel.Model),
								Tooltip:      modelCapsuleFullNameTip("生成提交信息与会话标题的模型", providers, sel),
								OnPickModel: func(_ ModelPickerSide, prov ProviderInfo, m ModelInfo) {
									saveCommitMessageModel(st, ModelSelection{Provider: prov.ID, Model: m.ID, Effort: sel.Effort, Fast: sel.Fast})
								},
								OnEffort: func(_ ModelPickerSide, effort string) {
									saveCommitMessageModel(st, ModelSelection{Provider: sel.Provider, Model: sel.Model, Effort: effort, Fast: sel.Fast})
								},
								OnToggleFast: func(ModelPickerSide) {
									saveCommitMessageModel(st, ModelSelection{Provider: sel.Provider, Model: sel.Model, Effort: sel.Effort, Fast: !sel.Fast})
								},
								OnReset: func(ModelPickerSide) {
									saveCommitMessageModel(st, ModelSelection{Provider: sel.Provider, Model: sel.Model})
								},
								Invalidate: st.invalidate,
							}))
						},
						resetFn: func(cfg *config.Config) {
							cfg.SourceControl.CommitMessageModel = config.ModelSelectionConfig{}
						},
					},
				},
			},
		},
		{
			ID:    "sourceControl.worktrees",
			Title: "工作区与工作树",
			Rows: []setting{
				{
					ID:      "row-sc-default-workspace-mode",
					Title:   "默认工作区模式",
					Aliases: []string{"工作区", "工作树", "worktree", "local", "本地", "独立", "新会话", "形态", "目录"},
					Lock:    "default-workspace-mode",
					Control: choice(val(func(c *config.Config) *string { return &c.SourceControl.DefaultWorkspaceMode }, config.WorkspaceModeLocal), []choiceOption{
						{Value: config.WorkspaceModeLocal, Label: "本地工作区"},
						{Value: config.WorkspaceModeWorktree, Label: "独立工作树"},
					}),
				},
				{
					ID:      "row-sc-auto-cleanup-delete",
					Title:   "删除会话时自动清理工作树",
					Aliases: []string{"工作树", "删除", "清理", "cleanup", "会话", "分支", "移除"},
					Lock:    "auto-cleanup-worktrees-on-delete",
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.SourceControl.AutoCleanupWorktreesOnDelete }, true)),
				},
				{
					ID:      "row-sc-auto-cleanup-merged",
					Title:   "自动清理已合并的工作树",
					Aliases: []string{"工作树", "合并", "清理", "merged", "cleanup", "分支", "任务"},
					Lock:    "auto-cleanup-merged-worktrees",
					Control: toggle(ptr(func(c *config.Config) **bool { return &c.SourceControl.AutoCleanupMergedWorktrees }, true)),
				},
				{
					ID:      "row-sc-worktree-cleanup",
					Title:   "Worktree 清理",
					Aliases: []string{"工作树", "清理", "worktree"},
					Control: link(worktreeCleanupCount, "worktree"),
				},
			},
		},
		advancedSourceControl,
	},
}

func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageSourceControl) }

func renderPageSourceControl(c *ui.Context, st *SettingsState) {
	renderSettingsTreePage(c, st, pageSourceControl)
}

// commitInstructionsControl 保留多行输入：text 控件只有单行，附加说明需要整段编辑。
type commitInstructionsControl struct{}

func (commitInstructionsControl) render(c *ui.Context, st *SettingsState, s setting) {
	commitInstructionsControl{}.renderDisabled(c, st, s, false)
}

func (commitInstructionsControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	old := st.cfg.SourceControl.CommitInstructions
	TextField(c, TextFieldProps{
		Key:         s.ID + "-area",
		Value:       &st.cfg.SourceControl.CommitInstructions,
		Label:       s.Title,
		Placeholder: "Conventional Commits",
		Multiline:   true,
		MinLines:    3,
		MaxLines:    8,
		MinWidth:    420,
		FillWidth:   true,
		Disabled:    disabled,
		OnChange: func(string) {
			st.noteTextEdit()
		},
	})
	// 直接把值绑到配置字段：非输入路径（如重置）改了字段时同样要标脏。
	if !disabled && st.cfg.SourceControl.CommitInstructions != old {
		st.noteTextEdit()
	}
}

func (commitInstructionsControl) reset(cfg *config.Config) {
	cfg.SourceControl.CommitInstructions = ""
}

// resettableCustomControl 让复杂的现有控件也能参与页面树的重置。
type resettableCustomControl struct {
	renderFn func(c *ui.Context, st *SettingsState, s setting)
	resetFn  func(cfg *config.Config)
}

func (r resettableCustomControl) render(c *ui.Context, st *SettingsState, s setting) {
	r.renderFn(c, st, s)
}

func (r resettableCustomControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	r.renderFn(c, st, s)
}

func (r resettableCustomControl) reset(cfg *config.Config) {
	r.resetFn(cfg)
}

// gitToggleControl 在开关写入配置后同步 git 包的全局默认值。
type gitToggleControl struct {
	b    bind[bool]
	sync func(cfg *config.Config)
}

func (g gitToggleControl) render(c *ui.Context, st *SettingsState, s setting) {
	g.renderDisabled(c, st, s, false)
}

func (g gitToggleControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	old := g.b.value(&st.cfg)
	toggle(g.b).renderDisabled(c, st, s, disabled)
	if disabled || g.b.value(&st.cfg) == old {
		return
	}
	if g.sync != nil {
		g.sync(&st.cfg)
	}
}

func (g gitToggleControl) reset(cfg *config.Config) {
	g.b.reset(cfg)
	if g.sync != nil {
		g.sync(cfg)
	}
}

// gitPatternsControl 在文本写入配置后同步 git 包的忽略目录设置。
type gitPatternsControl struct{}

func (gitPatternsControl) bind() bind[string] {
	return bind[string]{
		field: func(c *config.Config) **string { return &c.SourceControl.CloneIgnoredPatterns },
		def:   config.DefaultCloneIgnoredPatterns,
	}
}

func (g gitPatternsControl) render(c *ui.Context, st *SettingsState, s setting) {
	g.renderDisabled(c, st, s, false)
}

func (g gitPatternsControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	old := st.cfg.SourceControl.CloneIgnoredPatternsText()
	text(g.bind(), config.DefaultCloneIgnoredPatterns).renderDisabled(c, st, s, disabled)
	if disabled || st.cfg.SourceControl.CloneIgnoredPatternsText() == old {
		return
	}
	git.SetCloneIgnored(st.cfg.SourceControl.CloneIgnoredOnWorktreeEnabled(), st.cfg.SourceControl.CloneIgnoredPatternList())
}

func (g gitPatternsControl) reset(cfg *config.Config) {
	// 目录名单是用户自己维护的列表数据，settingsResetKeepFields 也明确保留它；
	// 页面树重置不应把它改回默认值。
}

// modelSelectionFromConfig 把配置中的模型选择转换为组件使用的 ModelSelection。
func modelSelectionFromConfig(sel config.ModelSelectionConfig) ModelSelection {
	return ModelSelection{Provider: sel.Provider, Model: sel.Model, Effort: sel.Effort, Fast: sel.Fast}
}

// saveCommitMessageModel 持久化「提交信息 / 会话标题生成模型」的选择。
func saveCommitMessageModel(st *SettingsState, sel ModelSelection) {
	// 改 cfg 要同时持有 mu 与 cfgMu（顺序 mu → cfgMu），save 与 invalidate 在两把锁之外调。
	st.mu.Lock()
	st.cfgMu.Lock()
	st.cfg.SourceControl.CommitMessageModel = config.ModelSelectionConfig{
		Provider: sel.Provider,
		Model:    sel.Model,
		Effort:   sel.Effort,
		Fast:     sel.Fast,
	}
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.save()
	st.invalidate()
}

// renderSourceControlAdvancedSections 保留给旧分发分支引用；版本控制页已由页面树接管，进阶项在 advancedSourceControl。
func renderSourceControlAdvancedSections(c *ui.Context, st *SettingsState) {}
