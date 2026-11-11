package mcodeui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

var sheetThemes = settingsSheet{
	ID: "themes", Title: "主题",
	Sections: []settingsSection{{ID: "themes.editor", Rows: []setting{{ID: "block-themes", Control: customFullWidth(renderThemesSheet)}}}},
}

func init() { settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetThemes) }

type themesSheetState struct {
	editingID    string
	confirm      string
	confirmID    string
	message      string
	messageUntil time.Time
	copiedUntil  time.Time
	names        map[string]*themeNameDraft
	colors       map[string]*ui.Color
	fonts        map[string]*float64
}

type themeNameDraft struct {
	name, synced string
	focused      bool
}

func getThemesSheetState(st *SettingsState) *themesSheetState {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.themesPage == nil {
		st.themesPage = &themesSheetState{
			names: make(map[string]*themeNameDraft), colors: make(map[string]*ui.Color), fonts: make(map[string]*float64),
		}
	}
	return st.themesPage
}

// mutateTheme 保持与设置页相同的锁顺序；连续颜色改动共用文本防抖与关闭时强制保存。
func mutateTheme(st *SettingsState, write func(*config.AppearanceConfig)) {
	st.mu.Lock()
	st.cfgMu.Lock()
	write(&st.cfg.Appearance)
	st.cfgMu.Unlock()
	st.mu.Unlock()
	st.noteTextEdit()
	st.invalidate()
}

func formatThemeColor(color ui.Color) string {
	if color.A == 255 {
		return fmt.Sprintf("#%02X%02X%02X", color.R, color.G, color.B)
	}
	return fmt.Sprintf("#%02X%02X%02X%02X", color.R, color.G, color.B, color.A)
}

func addSheetTheme(st *SettingsState, page *themesSheetState) {
	mutateTheme(st, func(a *config.AppearanceConfig) {
		if theme, ok := a.AddTheme(page.editingID); ok {
			page.editingID = theme.ID
		}
	})
}

func importSheetTheme(st *SettingsState, page *themesSheetState, data string) bool {
	ok := false
	mutateTheme(st, func(a *config.AppearanceConfig) {
		if theme, err := a.ImportTheme([]byte(data)); err == nil {
			page.editingID = theme.ID
			ok = true
		}
	})
	return ok
}

func renderThemesSheet(c *ui.Context, st *SettingsState) {
	root := ui.Column(c.Key("themes-editor")).FillWidth().Gap(Space4)
	page := getThemesSheetState(st)
	if _, ok := st.cfg.Appearance.ThemeByID(page.editingID); !ok {
		page.editingID = st.cfg.Appearance.ActiveThemeID(isDark(c))
	}
	root.Children(func() {
		ids := []string{config.ThemeLight, config.ThemeDark}
		for _, theme := range st.cfg.Appearance.Themes {
			if !config.IsBuiltinTheme(theme.ID) {
				ids = append(ids, theme.ID)
			}
		}
		ui.Column(c.Key("theme-list")).Gap(Space2).Children(func() {
			for _, id := range ids {
				renderThemeListItem(c, st, page, id)
			}
		})
		ui.Row(c.Key("theme-list-actions")).Gap(Space2).Wrap().Children(func() {
			Button(c, ButtonOpts{Key: "theme-add", Text: "新增", Variant: ButtonSecondary, OnClick: func() { addSheetTheme(st, page) }})
			Button(c, ButtonOpts{Key: "theme-import", Text: "导入", Variant: ButtonSecondary, OnClick: func() {
				if !importSheetTheme(st, page, mygo.Clipboard.ReadText()) {
					page.message = "无法导入"
					page.messageUntil = c.Now().Add(2 * time.Second)
				} else {
					page.message = ""
				}
			}})
			if page.message != "" && c.Now().Before(page.messageUntil) {
				ui.Text(c.Key("theme-import-error"), page.message).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).Transition(themeFeedbackFade(c))
				c.After(page.messageUntil.Sub(c.Now()))
			}
		})
		id := page.editingID
		palette := resolvePalette(st.cfg.Appearance, id)
		colors := []ui.Color{palette.Canvas, palette.Main, palette.Card, palette.Raised, palette.Popover, palette.Text, palette.TextSecondary, palette.TextTertiary, palette.Accent, palette.Danger, palette.Warning, palette.Success, palette.Caution, palette.Plan, palette.Border, palette.Divider}
		titles := []string{"窗口", "主区域", "卡片", "浮起", "浮层", "主文字", "次要文字", "辅助文字", "强调色", "危险", "警告", "成功", "提醒", "Plan", "边框", "分隔线"}
		sections := map[int]string{0: "背景", 5: "文字", 8: "强调", 9: "状态", 14: "线条"}
		for i, key := range config.ThemeColorKeys {
			if section, ok := sections[i]; ok {
				themeSectionTitle(c, "theme-section-"+key, section)
			}
			renderSettingRow(c, "row-theme-color-"+key, titles[i], func() {
				renderThemeColor(c, st, page, id, key, colors[i])
			})
		}
		themeSectionTitle(c, "theme-fonts", "字号")
		fontTitles := []string{"侧栏字号", "对话字号", "界面字号", "代码字号", "终端字号", "编辑器字号"}
		for i, key := range config.ThemeFontKeys {
			renderSettingRow(c, "row-theme-font-"+key, fontTitles[i], func() { renderThemeFont(c, st, page, id, key) })
		}
		renderThemeActions(c, st, page)
	})
	renderThemeConfirm(c, st, page)
}

func themeSectionTitle(c *ui.Context, key, title string) {
	ui.Text(c.Key(key), title).FontSize(uiFont(TypeHeading)).TextColor(ThemePrimaryText(c))
}

func themeFeedbackFade(c *ui.Context) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{Duration: DurFast, ExitDuration: DurFast, Enter: &ui.Motion{Opacity: 0}, Exit: &ui.Motion{Opacity: 0}})
}

func renderThemeListItem(c *ui.Context, st *SettingsState, page *themesSheetState, id string) {
	ui.Row(c.Key("theme-list-" + id)).Gap(Space2).AlignItems(ui.Center).Children(func() {
		Capsule(c, CapsuleProps{Key: "select", Text: st.cfg.Appearance.ThemeDisplayName(id), Selected: page.editingID == id, Interactive: true, OnClick: func() { page.editingID = id; st.invalidate() }}).Transition(motionFeedback(c))
		if config.IsBuiltinTheme(id) {
			return
		}
		theme, _ := st.cfg.Appearance.ThemeByID(id)
		holder := ui.Box(c.Key("theme-name-" + id)).Grow(1)
		draft := page.names[id]
		if draft == nil {
			draft = &themeNameDraft{name: theme.Name, synced: theme.Name}
			page.names[id] = draft
		}
		if draft.synced != theme.Name {
			draft.name = theme.Name
			draft.synced = theme.Name
		}
		submit := func(string) {
			name := strings.TrimSpace(draft.name)
			if name == "" {
				draft.name = theme.Name
				return
			}
			if name != theme.Name {
				mutateTheme(st, func(a *config.AppearanceConfig) { a.RenameTheme(id, name) })
			}
		}
		holder.Children(func() {
			TextField(c, TextFieldProps{Key: "name", Value: &draft.name, Label: "主题名称", Grow: true, OnChange: func(v string) { draft.name = v }, OnSubmit: submit, OnFocusChange: func(focused bool) {
				if draft.focused && !focused {
					submit(draft.name)
				}
				draft.focused = focused
			}})
		})
	})
}

func renderThemeColor(c *ui.Context, st *SettingsState, page *themesSheetState, id, key string, color ui.Color) {
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	follows := theme.Colors[key] == config.ThemeAccentSystem
	if key == "accent" {
		ui.Text(c, "跟随系统").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
		Switch(c, "theme-accent-system", &follows, "强调色跟随系统", func(on bool) {
			mutateTheme(st, func(a *config.AppearanceConfig) {
				if on {
					_ = a.SetThemeColor(id, key, config.ThemeAccentSystem)
				} else {
					a.ResetThemeColor(id, key)
				}
			})
		})
	}
	// ColorWell 保留颜色地址供弹出面板持续写入，不能使用每帧新建的栈变量。
	holder := ui.Box(c.Key("theme-color-" + id + "-" + key))
	valueKey := id + "/" + key
	value := page.colors[valueKey]
	if value == nil {
		value = new(ui.Color)
		page.colors[valueKey] = value
	}
	*value = color
	holder.Children(func() {
		well := ui.ColorWell(c.Key("well"), value).Label("编辑" + key).Disabled(follows)
		if well.Changed() && !follows {
			next := formatThemeColor(*value)
			mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeColor(id, key, next) })
		}
	})
	_, changed := theme.Colors[key]
	renderThemeReset(c, "theme-reset-color-"+key, changed, func() { mutateTheme(st, func(a *config.AppearanceConfig) { a.ResetThemeColor(id, key) }) })
}

func renderThemeFont(c *ui.Context, st *SettingsState, page *themesSheetState, id, key string) {
	value := st.cfg.Appearance.ThemeFonts(id)[key]
	lower, upper, _, _ := config.ThemeFontRange(key)
	if config.IsThemeScaleFont(key) {
		labels := make([]string, len(config.TextScaleLevels))
		for i, level := range config.TextScaleLevels {
			labels[i] = fmt.Sprintf("%d%%", level)
		}
		selected := fmt.Sprintf("%d%%", value)
		if ui.Select(c.Key("theme-font-"+key), &selected, labels).Label(key + "字号").Changed() {
			for i, label := range labels {
				if selected == label {
					next := config.TextScaleLevels[i]
					mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeFont(id, key, next) })
				}
			}
		}
	} else {
		holder := ui.Box(c.Key("theme-font-" + id + "-" + key))
		valueKey := id + "/" + key
		input := page.fonts[valueKey]
		if input == nil {
			input = new(float64)
			page.fonts[valueKey] = input
		}
		*input = float64(value)
		holder.Children(func() {
			if ui.NumberInput(c.Key("input"), input, float64(lower), float64(upper), 1).Label(key + "字号").Width(ControlLG).Changed() {
				next := int(*input)
				mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeFont(id, key, next) })
			}
		})
		ui.Text(c, "pt").FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
	}
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	_, changed := theme.Fonts[key]
	renderThemeReset(c, "theme-reset-font-"+key, changed, func() { mutateTheme(st, func(a *config.AppearanceConfig) { a.ResetThemeFont(id, key) }) })
}

func renderThemeReset(c *ui.Context, key string, changed bool, reset func()) {
	if !changed {
		return
	}
	IconButton(c, IconButtonProps{Key: key, Icon: IconRefreshCw(), Label: key, Tooltip: "还原", Variant: ButtonGhost, OnClick: reset}).Transition(themeFeedbackFade(c))
}

func renderThemeActions(c *ui.Context, st *SettingsState, page *themesSheetState) {
	id := page.editingID
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	ui.Row(c.Key("theme-actions")).Gap(Space2).Wrap().Children(func() {
		exportLabel := "导出"
		if c.Now().Before(page.copiedUntil) {
			exportLabel = "已复制"
			c.After(page.copiedUntil.Sub(c.Now()))
		}
		Button(c, ButtonOpts{Key: "theme-export", Text: exportLabel, Variant: ButtonSecondary, OnClick: func() {
			if data, err := st.cfg.Appearance.ExportTheme(id); err == nil {
				c.WriteClipboard(string(data))
				page.copiedUntil = c.Now().Add(2 * time.Second)
			}
		}})
		Button(c, ButtonOpts{Key: "theme-reset", Text: "还原此主题", Variant: ButtonSecondary, Disabled: len(theme.Colors) == 0 && len(theme.Fonts) == 0, OnClick: func() { page.confirm = "reset"; page.confirmID = id }})
		if !config.IsBuiltinTheme(id) {
			Button(c, ButtonOpts{Key: "theme-delete", Text: "删除主题", Variant: ButtonDanger, OnClick: func() { page.confirm = "delete"; page.confirmID = id }})
		}
	})
}

func renderThemeConfirm(c *ui.Context, st *SettingsState, page *themesSheetState) {
	deleting := page.confirm == "delete"
	title := "还原此主题"
	body := "还原此主题的颜色与字号？"
	label := "还原"
	if deleting {
		title = "删除主题"
		body = "删除此主题？"
		label = "删除"
	}
	RenderConfirmDialog(c, ConfirmDialog{Key: "theme-confirm", Open: page.confirm != "", Title: title, Body: body, ConfirmLabel: label, OnCancel: func() { page.confirm = "" }, OnConfirm: func() {
		id := page.confirmID
		mutateTheme(st, func(a *config.AppearanceConfig) {
			if deleting {
				a.RemoveTheme(id)
				page.editingID = a.ActiveThemeID(isDark(c))
			} else {
				a.ResetTheme(id)
			}
		})
		page.confirm = ""
	}})
}
