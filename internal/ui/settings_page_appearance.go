package mcodeui

import (
	"encoding/json"
	"fmt"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// textScaleControl 是「文字大小」的档位分段：档位是 int，choice 只绑定字符串，
// 所以这里直接画分段按钮，并实现禁用与重置，让渲染器统一处理锁定。
type textScaleControl struct{}

func (textScaleControl) render(c *ui.Context, st *SettingsState, s setting) {
	textScaleControl{}.renderDisabled(c, st, s, false)
}

func (textScaleControl) renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool) {
	cur := st.cfg.Appearance.EffectiveTextScale()
	ui.Row(c.Key("seg-text-scale")).
		Background(ThemeOverlay(c, OverlaySubtle)).
		Padding(Space0).
		Radius(RadiusMD).
		Gap(Space0).
		AlignItems(ui.Center).
		Children(func() {
			for _, level := range config.TextScaleLevels {
				lv := level
				label := fmt.Sprintf("%d%%", lv)
				btn := ui.ButtonBase(c.Key(fmt.Sprintf("btn-text-scale-%d", lv))).
					Label(label).
					Padding(Space1, settingsCompactPadX).
					Radius(RadiusSM).
					Disabled(disabled)
				if !disabled {
					btn.OnClick(func() {
						if cur == lv {
							return
						}
						commitSettingsControl(st, func(cfg *config.Config) { cfg.Appearance.TextScale = lv })
					})
				}
				txtColor := ThemeSecondaryText(c)
				if cur == lv {
					btn.Background(ThemeSurface(c))
					txtColor = ThemePrimaryText(c)
				} else if btn.Hovered() && !disabled {
					btn.Background(ThemeOverlay(c, OverlayFaint))
					txtColor = ThemePrimaryText(c)
				}
				btn.Children(func() {
					ui.Text(c, label).FontSize(TypeBody).TextColor(txtColor)
				})
			}
		})
}

func (textScaleControl) reset(cfg *config.Config) { cfg.Appearance.TextScale = config.DefaultTextScale }

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

// appearanceThemeSection 是「主题与排版」节：主题、文字大小、密度、内容宽度与侧边栏。
var appearanceThemeSection = settingsSection{
	ID:    "appearance.theme",
	Title: "主题与排版",
	Rows: []setting{
		{
			ID:      "row-theme",
			Title:   "主题",
			Aliases: []string{"深色", "浅色", "暗色", "亮色", "跟随系统", "配色", "夜间"},
			Lock:    "theme",
			Control: choice(
				val(func(c *config.Config) *string { return &c.Appearance.Theme }, config.ThemeSystem),
				[]choiceOption{
					{Value: config.ThemeSystem, Label: "跟随系统"},
					{Value: config.ThemeLight, Label: "浅色"},
					{Value: config.ThemeDark, Label: "深色"},
				},
			),
		},
		{
			ID:      "row-text-scale",
			Title:   "文字大小",
			Aliases: []string{"字号", "缩放", "侧栏", "对话内容", "百分比"},
			Lock:    "text-scale",
			Control: textScaleControl{},
		},
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
	},
}

// appearanceFontsSection 是「字体与字号」节：代码字体与代码字号同组。
var appearanceFontsSection = settingsSection{
	ID:    "appearance.fonts",
	Title: "字体与字号",
	Rows: []setting{
		{
			ID:      "row-code-font",
			Title:   "代码字体",
			Aliases: []string{"字体", "等宽", "代码块", "内联代码", "工具输出", "SF Mono", "Menlo", "JetBrains Mono", "monospace"},
			Lock:    "code-font",
			Control: text(val(func(c *config.Config) *string { return &c.Appearance.CodeFont }, ""), "系统默认"),
		},
		{
			ID:      "row-code-font-size",
			Title:   "代码字号",
			Aliases: []string{"字号", "代码块", "等宽", "pt"},
			Lock:    "code-font-size",
			Control: number(
				val(func(c *config.Config) *int { return &c.Appearance.CodeFontSize }, config.DefaultCodeFontSize),
				"pt", config.MinCodeFontSize, config.MaxCodeFontSize,
			),
		},
		{
			ID:      "row-terminal-font-size",
			Title:   "终端字号",
			Aliases: []string{"终端", "字体", "字号", "font", "size", "文字"},
			Control: number(val(func(c *config.Config) *int { return &c.Workbench.TerminalFontSize }, config.DefaultTerminalFontSize), "pt", config.MinTerminalFontSize, config.MaxTerminalFontSize),
		},
		{
			ID:      "row-editor-font-size",
			Title:   "编辑器字号",
			Aliases: []string{"编辑器", "字体", "字号", "font", "源码"},
			Control: number(val(func(c *config.Config) *int { return &c.Workbench.EditorFontSize }, config.DefaultEditorFontSize), "pt", config.MinEditorFontSize, config.MaxEditorFontSize),
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
