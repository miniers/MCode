package mcodeui

import (
	"encoding/json"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

type themeControl struct{}

func (themeControl) render(c *ui.Context, st *SettingsState, s setting) {
	themeControl{}.renderDisabled(c, st, s, false)
}

func (themeControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	ids := []string{config.ThemeSystem, config.ThemeLight, config.ThemeDark}
	selected := -1
	for i, id := range ids {
		if st.cfg.Appearance.Theme == id || (i == 0 && st.cfg.Appearance.Theme == "") {
			selected = i
		}
	}
	ui.Column(c.Key(s.ID + "-choices")).Gap(Space2).AlignItems(ui.End).Children(func() {
		Segmented(c, s.ID+"-seg", &selected, []Segment{{Label: "跟随系统", Disabled: disabled}, {Label: "浅色", Disabled: disabled}, {Label: "深色", Disabled: disabled}}, func(i int) {
			commitSettingsControl(st, func(cfg *config.Config) { cfg.Appearance.Theme = ids[i] })
		}).Label("主题")
		ui.Row(c.Key(s.ID + "-custom")).Wrap().Gap(Space2).Children(func() {
			for _, theme := range st.cfg.Appearance.Themes {
				if config.IsBuiltinTheme(theme.ID) {
					continue
				}
				Capsule(c, CapsuleProps{Key: theme.ID, Text: theme.Name, Selected: st.cfg.Appearance.Theme == theme.ID, Interactive: !disabled, OnClick: func() {
					commitSettingsControl(st, func(cfg *config.Config) { cfg.Appearance.Theme = theme.ID })
				}}).Transition(motionFeedback(c))
			}
		})
	})
}

func (themeControl) reset(cfg *config.Config) { cfg.Appearance.Theme = config.ThemeSystem }

// appearanceThemeApplied 记录最近一次同步到运行时主题源的值：主题控件与整页重置都只改配置，
// 页面渲染时发现配置里的主题变了就立刻应用，避免要重启才生效。
var appearanceThemeApplied string

// syncAppearanceTheme 把配置里的主题同步到运行时主题源；值没变就不重复设置。
func syncAppearanceTheme(st *SettingsState) {
	st.cfgMu.Lock()
	appearance := st.cfg.Appearance
	source := appearance.Theme
	if source == "" {
		source = config.ThemeSystem
	}
	if !config.IsBuiltinTheme(source) && source != config.ThemeSystem {
		if theme, ok := appearance.ThemeByID(source); ok {
			source = theme.Base
		} else {
			source = config.ThemeSystem
		}
	}
	content, _ := json.Marshal(struct {
		Theme  string
		Themes []config.ThemeDef
	}{appearance.Theme, appearance.Themes})
	key := source + string(content)
	if key == appearanceThemeApplied {
		st.cfgMu.Unlock()
		return
	}
	appearanceThemeApplied = key
	setActiveTheme(appearance)
	st.cfgMu.Unlock()
	applyThemeSource(mygo.ThemeSource(source))
}

type motionControl struct{}

func (motionControl) render(c *ui.Context, st *SettingsState, s setting) {
	motionControl{}.renderDisabled(c, st, s, false)
}

func (motionControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	toggleFunc(func(st *SettingsState) (bool, bool) { return !st.cfg.Appearance.ReduceMotion, false },
		func(st *SettingsState, on bool) {
			commitSettingsControl(st, func(cfg *config.Config) { cfg.Appearance.ReduceMotion = !on })
		}).renderDisabled(c, st, s, disabled)
}

func (motionControl) reset(cfg *config.Config) { cfg.Appearance.ReduceMotion = false }

// appearanceThemeSection 是主题、布局与主题编辑入口。
var appearanceThemeSection = settingsSection{
	ID:    "appearance.theme",
	Title: "主题与排版",
	Rows: []setting{
		{
			ID:      "row-theme",
			Title:   "主题",
			Aliases: []string{"深色", "浅色", "暗色", "亮色", "跟随系统", "配色", "夜间"},
			Lock:    "theme",
			Control: themeControl{},
		},
		{ID: "row-edit-themes", Title: "编辑主题", Aliases: []string{"自定义主题", "颜色", "字号"}, Control: link(func(st *SettingsState) string {
			if st.cfg.Appearance.Theme == "" || st.cfg.Appearance.Theme == config.ThemeSystem {
				return "跟随系统"
			}
			return st.cfg.Appearance.ThemeDisplayName(st.cfg.Appearance.Theme)
		}, "themes")},
		{
			ID:      "row-conversation-density",
			Title:   "界面密度",
			Aliases: []string{"间距", "消息间距", "气泡", "紧凑", "舒适", "侧栏", "控件", "行高", "全窗口"},
			Lock:    "conversation-density",
			Control: choice(
				val(func(c *config.Config) *string { return &c.Appearance.ConversationDensity }, config.DensityComfortable),
				[]choiceOption{
					{Value: config.DensityComfortable, Label: "舒适"},
					{Value: config.DensityCompact, Label: "紧凑"},
				},
			),
		},
		{
			ID:      "row-content-width",
			Title:   "内容宽度",
			Aliases: []string{"宽度", "时间线宽度", "输入框宽度", "宽屏", "最大宽度", "按比例", "标准", "宽"},
			Lock:    "content-width",
			Control: choice(
				val(func(c *config.Config) *string { return &c.Appearance.ContentWidth }, config.ContentWidthStandard),
				[]choiceOption{
					{Value: config.ContentWidthStandard, Label: "标准"},
					{Value: config.ContentWidthWide, Label: "宽"},
					{Value: config.ContentWidthFull, Label: "按比例"},
				},
			),
		},
		{
			// 只有「按比例」档用得到百分比，其它档位这一行禁用。
			ID:      "row-content-width-percent",
			Title:   "比例宽度",
			Aliases: []string{"内容宽度", "百分比", "比例", "窗口宽度", "最大宽度"},
			Lock:    "content-width",
			Enabled: func(st *SettingsState) bool { return st.cfg.Appearance.ContentWidth == config.ContentWidthFull },
			Control: number(
				val(func(c *config.Config) *int { return &c.Appearance.ContentWidthPercent }, config.DefaultContentWidthPercent),
				"%", config.MinContentWidthPercent, config.MaxContentWidthPercent,
			),
		},
		{
			ID:      "row-sidebar-compact",
			Title:   "紧凑侧边栏",
			Aliases: []string{"侧边栏", "紧凑", "行高", "内边距", "截断", "单行", "会话标题"},
			Lock:    "sidebar-compact",
			Control: toggle(val(func(c *config.Config) *bool { return &c.Appearance.SidebarCompact }, false)),
		},
		{
			ID: "row-motion", Title: "动效", Aliases: []string{"动画", "减少动效", "过渡", "ReduceMotion"}, Lock: "motion", Control: motionControl{},
		},
	},
}

// appearanceFontsSection 是代码字体设置。
var appearanceFontsSection = settingsSection{
	ID:    "appearance.fonts",
	Title: "字体",
	Rows: []setting{
		{
			ID:      "row-code-font",
			Title:   "代码字体",
			Aliases: []string{"字体", "等宽", "代码块", "内联代码", "工具输出", "SF Mono", "Menlo", "JetBrains Mono", "monospace"},
			Lock:    "code-font",
			Control: text(val(func(c *config.Config) *string { return &c.Appearance.CodeFont }, ""), "系统默认"),
		},
	},
}

// pageAppearance 是「外观」页：主题、排版与字号。说明文字全部删除，检索词进 Aliases。
var pageAppearance = settingsPage{
	ID:       "appearance",
	Title:    "外观",
	Icon:     IconPalette(),
	Keywords: []string{"主题", "深色", "浅色", "文字大小", "字号", "字体", "缩放", "间距", "宽度", "侧边栏"},
	Sections: []settingsSection{appearanceThemeSection, appearanceFontsSection},
}

// 注册到页面树放在 init 里：包级变量不能引用页面的初始值，否则会和渲染链形成初始化循环。
func init() { settingsTreePageDefs = append(settingsTreePageDefs, pageAppearance) }

// renderPageAppearance 渲染外观页；渲染器只认页面树，保留这个入口给既有调用方。
func renderPageAppearance(c *ui.Context, st *SettingsState) {
	syncAppearanceTheme(st)
	renderSettingsTreePage(c, st, pageAppearance)
}
