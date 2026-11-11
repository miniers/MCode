package mcodeui

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

var sheetThemes = settingsSheet{
	ID: "themes", Title: "主题", Body: renderThemesSheet, Header: renderThemesHeader, Footer: func(c *ui.Context, st *SettingsState) { renderThemeActions(c, st, getThemesSheetState(st)) },
	Sections: []settingsSection{{ID: "themes.editor", Rows: []setting{{ID: "block-themes", Control: customFullWidth(renderThemesSheet)}}}},
}

func init() { settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetThemes) }

type themesSheetState struct {
	editingID    string
	menuOpen     bool
	colorOpen    map[string]*bool
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
			names: make(map[string]*themeNameDraft), colors: make(map[string]*ui.Color), fonts: make(map[string]*float64), colorOpen: make(map[string]*bool),
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

func themesPage(st *SettingsState, c *ui.Context) *themesSheetState {
	page := getThemesSheetState(st)
	if _, ok := st.cfg.Appearance.ThemeByID(page.editingID); !ok {
		page.editingID = st.cfg.Appearance.ActiveThemeID(isDark(c))
	}
	return page
}

func renderThemesHeader(c *ui.Context, st *SettingsState) {
	page := themesPage(st, c)
	ui.Row(c.Key("theme-list")).Grow(1).Gap(Space3).Wrap().Children(func() {
		ids := []string{config.ThemeLight, config.ThemeDark}
		for _, theme := range st.cfg.Appearance.Themes {
			if !config.IsBuiltinTheme(theme.ID) {
				ids = append(ids, theme.ID)
			}
		}
		for _, id := range ids {
			renderThemeListItem(c, st, page, id)
		}
		trigger := ui.ButtonBase(c.Key("theme-add-menu")).Label("添加主题").OnClick(func() { page.menuOpen = true })
		trigger.Children(func() {
			ui.Box(c).Size(settingsPreviewTileW, settingsPreviewTileH).Radius(RadiusMD).Border(Hairline, ThemeBorder(c)).Center().Children(func() { ui.Text(c, "+").FontSize(uiFont(TypeHeading)).TextColor(ThemeSecondaryText(c)) })
		})
		PopoverCard(c, PopoverProps{Key: "theme-add-popover", Anchor: trigger, Open: &page.menuOpen}, func() {
			Button(c, ButtonOpts{Key: "theme-add", Text: "新增", Variant: ButtonGhost, OnClick: func() { page.menuOpen = false; addSheetTheme(st, page) }})
			Button(c, ButtonOpts{Key: "theme-import", Text: "从剪贴板导入", Variant: ButtonGhost, OnClick: func() {
				page.menuOpen = false
				if !importSheetTheme(st, page, mygo.Clipboard.ReadText()) {
					page.message = "无法导入"
					page.messageUntil = c.Now().Add(2 * time.Second)
				} else {
					page.message = ""
				}
			}})
		})
	})
}

func renderThemesSheet(c *ui.Context, st *SettingsState) {
	page := themesPage(st, c)
	id := page.editingID
	palette := resolvePalette(st.cfg.Appearance, id)
	colors := []ui.Color{palette.Canvas, palette.Main, palette.Card, palette.Raised, palette.Popover, palette.Text, palette.TextSecondary, palette.TextTertiary, palette.Accent, palette.Danger, palette.Warning, palette.Success, palette.Caution, palette.Plan, palette.Border, palette.Divider}
	titles := []string{"窗口", "主区域", "卡片", "浮起", "浮层", "主文字", "次要文字", "辅助文字", "强调色", "危险", "警告", "成功", "提醒", "Plan", "边框", "分隔线"}
	ui.Column(c.Key("themes-editor")).FillWidth().Gap(settingsSectionGap).Children(func() {
		if !config.IsBuiltinTheme(id) {
			renderSettingsSection(c, "theme-name-section", "名称", func() { renderSettingRow(c, "row-theme-name", "名称", func() { renderThemeName(c, st, page, id) }) })
		}
		sections := []struct {
			title      string
			start, end int
		}{{"背景", 0, 5}, {"文字", 5, 8}, {"强调", 8, 9}, {"状态", 9, 14}, {"线条", 14, 16}}
		for _, section := range sections {
			renderSettingsSection(c, "theme-section-"+config.ThemeColorKeys[section.start], section.title, func() {
				for i := section.start; i < section.end; i++ {
					key := config.ThemeColorKeys[i]
					if i > section.start {
						renderSettingsDivider(c, "theme-divider-"+key)
					}
					renderSettingRow(c, "row-theme-color-"+key, titles[i], func() { renderThemeColor(c, st, page, id, key, colors[i]) })
				}
				if section.title == "强调" {
					renderSettingsDivider(c, "theme-divider-system")
					renderSettingRow(c, "row-theme-accent-system", "跟随系统", func() {
						theme, _ := st.cfg.Appearance.ThemeByID(id)
						follows := theme.Colors["accent"] == config.ThemeAccentSystem
						Switch(c, "theme-accent-system", &follows, "强调色跟随系统", func(on bool) {
							mutateTheme(st, func(a *config.AppearanceConfig) {
								if on {
									_ = a.SetThemeColor(id, "accent", config.ThemeAccentSystem)
								} else {
									a.ResetThemeColor(id, "accent")
								}
							})
						})
					})
				}
			})
		}
		renderSettingsSection(c, "theme-fonts", "字号", func() {
			titles := []string{"侧栏", "对话", "界面", "代码", "终端", "编辑器"}
			for i, key := range config.ThemeFontKeys {
				if i > 0 {
					renderSettingsDivider(c, "theme-font-divider-"+key)
				}
				renderSettingRow(c, "row-theme-font-"+key, titles[i], func() { renderThemeFont(c, st, page, id, key) })
			}
		})
	})
	renderThemeConfirm(c, st, page)
}

func themeFeedbackFade(c *ui.Context) ui.ElementTransition {
	return motionTransition(c, ui.ElementTransition{Duration: DurFast, ExitDuration: DurFast, Enter: &ui.Motion{Opacity: 0}, Exit: &ui.Motion{Opacity: 0}})
}

func renderThemeListItem(c *ui.Context, st *SettingsState, page *themesSheetState, id string) {
	name := st.cfg.Appearance.ThemeDisplayName(id)
	palette := resolvePalette(st.cfg.Appearance, id)
	selected := page.editingID == id
	button := ui.ButtonBase(c.Key("theme-list-" + id)).Label(name).AccessSelected(selected).OnClick(func() { page.editingID = id; st.invalidate() }).Transition(motionFeedback(c))
	button.Children(func() {
		ui.Column(c).Gap(Space2).AlignItems(ui.Center).Children(func() {
			tile := ui.Box(c.Key("theme-preview-"+id)).Size(settingsPreviewTileW, settingsPreviewTileH).Radius(RadiusMD).Background(palette.Canvas).Border(Hairline, ThemeBorder(c)).Padding(Space2)
			if selected {
				tile.Border(settingsSelectedBorder, ThemeAccent(c))
			}
			tile.Children(func() {
				ui.Column(c).Fill().Radius(RadiusSM).Background(palette.Card).Padding(Space2).Gap(Space1).Children(func() {
					ui.Row(c).Gap(Space2).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(Space2, Space2).Radius(RadiusPill).Background(palette.Accent)
						ui.Box(c).Width(Space7).Height(Space0).Background(palette.Text)
					})
					ui.Box(c).Width(Space8).Height(Space0).Background(palette.TextSecondary)
				})
			})
			ui.Text(c, name).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c)).SingleLine()
		})
	})
}

func renderThemeName(c *ui.Context, st *SettingsState, page *themesSheetState, id string) {
	theme, _ := st.cfg.Appearance.ThemeByID(id)
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
	TextField(c, TextFieldProps{Key: "theme-name-" + id, Value: &draft.name, Label: "主题名称", OnChange: func(v string) { draft.name = v }, OnSubmit: submit, OnFocusChange: func(focused bool) {
		if draft.focused && !focused {
			submit(draft.name)
		}
		draft.focused = focused
	}})
}

func renderThemeColor(c *ui.Context, st *SettingsState, page *themesSheetState, id, key string, color ui.Color) {
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	follows := theme.Colors[key] == config.ThemeAccentSystem
	_, changed := theme.Colors[key]
	renderThemeReset(c, "theme-reset-color-"+key, changed, func() { mutateTheme(st, func(a *config.AppearanceConfig) { a.ResetThemeColor(id, key) }) })
	label := formatThemeColor(color)
	if follows {
		label = "系统"
	}
	ui.Text(c, label).FontSize(uiFont(TypeFootnote)).FontFeatures("tnum").TextColor(ThemeSecondaryText(c))
	// 颜色地址必须跨帧保留，供拾色面板持续写入。
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
	trigger := ui.ButtonBase(c.Key("theme-color-" + id + "-" + key)).Label("编辑" + key).Disabled(follows).OnClick(func() { *open = true })
	trigger.Children(func() {
		ui.Box(c).Size(Space6, Space6).Radius(RadiusXS).Background(color).Border(Hairline, ThemeBorder(c))
	})
	PopoverCard(c, PopoverProps{Key: "theme-color-picker-" + valueKey, Anchor: trigger, Open: open}, func() {
		if ui.ColorPicker(c.Key("picker"), value).Changed() && !follows {
			next := formatThemeColor(*value)
			mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeColor(id, key, next) })
		}
	})
}

func renderThemeFont(c *ui.Context, st *SettingsState, page *themesSheetState, id, key string) {
	value := st.cfg.Appearance.ThemeFonts(id)[key]
	lower, upper, _, _ := config.ThemeFontRange(key)
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	_, changed := theme.Fonts[key]
	renderThemeReset(c, "theme-reset-font-"+key, changed, func() { mutateTheme(st, func(a *config.AppearanceConfig) { a.ResetThemeFont(id, key) }) })
	valueKey := id + "/" + key
	input := page.fonts[valueKey]
	if input == nil {
		input = new(float64)
		page.fonts[valueKey] = input
	}
	*input = float64(value)
	if ui.Slider(c.Key("theme-font-"+key), input, float64(lower), float64(upper)).Step(1).Width(settingsPreviewTileW * 2).Label(key + "字号").Changed() {
		next := int(math.Round(*input))
		mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeFont(id, key, next) })
	}
	label := fmt.Sprintf("%d pt", value)
	if config.IsThemeScaleFont(key) {
		label = fmt.Sprintf("%d%%", value)
	}
	ui.Text(c, label).MinWidth(settingsPreviewTileW).FontSize(uiFont(TypeFootnote)).FontFeatures("tnum").TextColor(ThemeSecondaryText(c))
}

func renderThemeReset(c *ui.Context, key string, changed bool, reset func()) {
	if !changed {
		return
	}
	IconButton(c, IconButtonProps{Key: key, Icon: IconRefreshCw(), Label: key, Tooltip: "还原", Size: Space6, Variant: ButtonGhost, OnClick: reset}).Transition(themeFeedbackFade(c))
}

func renderThemeActions(c *ui.Context, st *SettingsState, page *themesSheetState) {
	id := page.editingID
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	ui.Row(c.Key("theme-actions")).Grow(1).Gap(Space2).Wrap().Children(func() {
		if page.message != "" && c.Now().Before(page.messageUntil) {
			ui.Text(c.Key("theme-import-error"), page.message).FontSize(uiFont(TypeFootnote)).TextColor(ThemeDanger(c)).Transition(themeFeedbackFade(c))
			c.After(page.messageUntil.Sub(c.Now()))
		}
		if !config.IsBuiltinTheme(id) {
			Button(c, ButtonOpts{Key: "theme-delete", Text: "删除主题", Variant: ButtonDanger, OnClick: func() { page.confirm = "delete"; page.confirmID = id }})
		}
		ui.Spacer(c)
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
