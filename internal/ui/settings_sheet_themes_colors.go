package mcodeui

import (
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// colorSections 按显式键列表排列颜色行：每个键只属于一组，换顺序或加槽都不会让区间串位。
// bases/titles 两张 map 必须覆盖这里与 config.ThemeColorKeys 的全部键。
var colorSections = []themeColorSection{
	{"强调", []string{"accent"}},
	{"背景", []string{"canvas", "main", "card", "raised", "popover", "code_background", "table_header", "composer_background", "user_message_background", "quote_bar"}},
	{"文字", []string{"text", "text_secondary", "text_tertiary", "code_text", "code_heading", "code_muted", "code_link", "code_strong", "quote_text"}},
	{"状态", []string{"danger", "warning", "success", "caution", "plan"}},
	{"线条", []string{"border", "divider"}},
}

// themeColorSection 是颜色行的一个分组。
type themeColorSection struct {
	title string
	keys  []string
}

// themeColorTitles 是颜色行的显示标题。表头与代码角色的标题与按钮标签分开：
// 输入框一项的键是 composer_background，标题不能由键推出。
var themeColorTitles = map[string]string{
	"canvas": "窗口", "main": "主区域", "card": "卡片", "raised": "浮起", "popover": "浮层", "code_background": "代码底色",
	"composer_background": "输入框", "table_header": "表头", "text": "主文字", "text_secondary": "次要文字",
	"text_tertiary": "辅助文字", "code_text": "代码文字", "accent": "强调色", "danger": "危险", "warning": "警告",
	"success": "成功", "caution": "提醒", "plan": "Plan", "border": "边框", "divider": "分隔线", "code_heading": "标题",
	"code_muted": "次要标记", "code_link": "链接", "code_strong": "粗体", "quote_text": "引用文字", "quote_bar": "引用竖线",
	"user_message_background": "用户消息",
}

// themeColorBases 把 palette 按 colorSections 的键摊平，供颜色行显示与拾色面板取基准色。
func themeColorBases(palette themePalette) map[string]ui.Color {
	codeLink := palette.CodeLink
	if codeLink == (ui.Color{}) {
		codeLink = palette.Accent
	}
	return map[string]ui.Color{
		"canvas": palette.Canvas, "main": palette.Main, "card": palette.Card, "raised": palette.Raised, "popover": palette.Popover,
		"code_background": palette.CodeBackground, "table_header": palette.TableHeader, "composer_background": palette.ComposerBackground,
		"user_message_background": palette.UserMessageBackground,
		"text":                    palette.Text, "text_secondary": palette.TextSecondary, "text_tertiary": palette.TextTertiary, "code_text": palette.CodeText,
		"accent": palette.Accent, "danger": palette.Danger, "warning": palette.Warning, "success": palette.Success, "caution": palette.Caution, "plan": palette.Plan,
		"border": palette.Border, "divider": palette.Divider,
		"code_heading": palette.CodeHeading, "code_muted": palette.CodeMuted, "code_link": codeLink, "code_strong": palette.CodeStrong,
		"quote_text": palette.QuoteText, "quote_bar": palette.QuoteBar,
	}
}

// renderColorSectionRows 按索引登记的顺序渲染一组颜色行，行之间用细线分隔。
func renderColorSectionRows(c *ui.Context, st *SettingsState, page *themesSheetState, id string, section themeColorSection) {
	bases := themeColorBases(resolvePalette(st.cfg.Appearance, id))
	for i, key := range section.keys {
		if i > 0 {
			renderSettingsDivider(c, "theme-divider-"+key)
		}
		renderThemeColorRow(c, themeColorRowPrefix+key, "theme-color-row-"+key, themeColorTitles[key], func() {
			renderThemeColor(c, st, page, id, key, themeColorTitles[key], bases[key])
		})
	}
}

// renderThemeColorRow 渲染一行颜色设置：布局与普通设置行一致（左侧标题、右侧控件），
// 行自身的 key 用索引里登记的 row-theme-color-<key>，让设置搜索能定位到这一行。
func renderThemeColorRow(c *ui.Context, rowKey, controlKey, title string, body func()) {
	var row ui.Element
	if settingsRowFocus.jumpID != "" || settingsRowFocus.highlightID != "" {
		row = settingRowPartsTall(c, rowKey, controlKey, title, func(row ui.Element) ui.Element {
			return applySettingsRowFocus(c, row, rowKey)
		}, body)
	} else {
		row = SettingRowKeyedTall(c, rowKey, controlKey, title, body)
	}
	// 行写成 Group 角色并以行 key 为无障碍名：搜索定位需要它作为可寻址目标。
	row.Label(rowKey).Role(ui.RoleGroup)
}

func renderThemeColor(c *ui.Context, st *SettingsState, page *themesSheetState, id, key, title string, color ui.Color) {
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	follows := theme.Colors[key] == config.ThemeAccentSystem
	_, changed := theme.Colors[key]
	renderThemeReset(c, "theme-reset-color-"+key, title, changed, func() { mutateTheme(st, func(a *config.AppearanceConfig) { a.ResetThemeColor(id, key) }) })
	label := formatThemeColor(color)
	if follows {
		label = "系统"
	}
	ui.Text(c, label).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
	// 颜色地址必须跨帧保留，供拾色面板持续写入；开关地址同样取自参数求值，
	// 不经过随后就地改写过的局部变量，面板才能跨帧保持打开。
	valueKey := id + "/" + key
	value := page.colors[valueKey]
	if value == nil {
		value = new(ui.Color)
		page.colors[valueKey] = value
	}
	*value = color
	open := page.colorOpen[valueKey]
	if open == nil {
		open = new(bool)
		page.colorOpen[valueKey] = open
	}
	// 按钮与色块都取 Space6 并量在色块自身：行高由 SettingRowKeyedTall 的右侧控件决定，按钮始终落在行内。
	trigger := ui.ButtonBase(c.Key("theme-color-"+id+"-"+key)).Label("编辑"+title).Disabled(follows).Size(Space6, Space6).Radius(RadiusXS).Cursor(ui.CursorPointer).OnClick(func() { *open = true })
	if !follows && trigger.Hovered() {
		trigger.Background(ThemeHover(c))
	}
	focusRing(c, trigger)
	scale := motionPressScale(c, trigger, "button-press-scale", !follows && trigger.Pressed())
	trigger.Scale(scale, scale)
	trigger.Children(func() {
		ui.Box(c.Key("theme-color-swatch-"+key)).Size(Space6, Space6).Radius(RadiusXS).Background(color).Border(Hairline, ThemeBorder(c))
	})
	PopoverCard(c, trigger, PopoverProps{Key: "theme-color-picker-" + id + "/" + key, Open: open}, func() {
		if ui.ColorPicker(c.Key("picker"), value).Changed() && !follows {
			next := formatThemeColor(*value)
			mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeColor(id, key, next) })
		}
	})
}
