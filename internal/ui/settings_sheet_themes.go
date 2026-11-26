package mcodeui

import (
	"errors"
	"fmt"
	"math"
	"os"

	"strings"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

var sheetThemes = settingsSheet{
	ID: "themes", Title: "主题", Body: renderThemesSheet, Footer: func(c *ui.Context, st *SettingsState) { renderThemeActions(c, st, getThemesSheetState(st)) },
	Sections: []settingsSection{{ID: "themes.editor", Rows: []setting{{ID: "block-themes", Control: customFullWidth(renderThemesSheet)}}}},
}

func init() { settingsTreeSheetDefs = append(settingsTreeSheetDefs, sheetThemes) }

type themesSheetState struct {
	editingID    string
	menuOpen     bool
	piMenuOpen   bool
	piThemes     []piThemeFile
	confirm      string
	confirmID    string
	message      string
	messageUntil time.Time
	copiedUntil  time.Time
	// fileImportFailed 由后台 goroutine 置位，渲染时消费并显示失败提示。
	fileImportFailed int32
	// pendingFileTheme 只跨线程传递已解析的主题。
	pendingFileTheme atomic.Value
	names            map[string]*themeNameDraft
	colors           map[string]*ui.Color
	fonts            map[string]*float64
	// colorOpen 以「主题/颜色行序号」为地址：同一标题跨两组出现时各自有独立开关，面板 key 也随之唯一。
	colorOpen map[string]*bool
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

// themeFileSizeLimit 是“从文件导入”允许的最大文件大小，避免误选二进制或超大文件。
const themeFileSizeLimit = 1 << 20

// pickThemeFileDialog 唤起系统文件选择器并返回所选主题文件路径；取消时 ok 为 false。测试可替换。
var pickThemeFileDialog = func() (string, bool, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Title: "选择主题文件"})
	if err != nil {
		return "", false, err
	}
	if len(paths) == 0 {
		return "", false, nil
	}
	return paths[0], true, nil
}

// importSheetThemeFromFile 在后台 goroutine 里选文件、读取并解析；不写 Appearance，主题只在主线程写入。取消不提示。
func importSheetThemeFromFile(st *SettingsState, page *themesSheetState) {
	picker := pickThemeFileDialog
	if picker == nil {
		picker = func() (string, bool, error) { return "", false, errors.New("文件选择器不可用") }
	}
	go func() {
		fail := func() {
			atomic.StoreInt32(&page.fileImportFailed, 1)
			st.invalidate()
		}
		path, ok, err := picker()
		if !ok && err == nil {
			return
		}
		if err != nil {
			fail()
			return
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() > themeFileSizeLimit {
			fail()
			return
		}
		data, err := os.ReadFile(path)
		if err != nil {
			fail()
			return
		}

		theme, err := config.ParseThemeTransfer(data)
		if err != nil {
			fail()
			return
		}
		page.pendingFileTheme.Store(&theme)
		st.invalidate()
	}()
}

func addSheetTheme(st *SettingsState, page *themesSheetState) {
	mutateTheme(st, func(a *config.AppearanceConfig) {
		if theme, ok := a.AddTheme(page.editingID); ok {
			page.editingID = theme.ID
		}
	})
}

func importSheetTheme(st *SettingsState, page *themesSheetState, data string) bool {
	var id string
	mutateTheme(st, func(a *config.AppearanceConfig) {
		id, _ = applyImportedTheme(a, data)
	})
	ok := id != ""
	if ok {
		page.editingID = id
	}
	return ok
}

func applyImportedTheme(a *config.AppearanceConfig, data string) (string, bool) {
	theme, err := a.ImportTheme([]byte(data))
	if err != nil {
		return "", false
	}
	return theme.ID, true
}

func themesPage(st *SettingsState, c *ui.Context) *themesSheetState {
	page := getThemesSheetState(st)
	// 文件导入的 atomic 标记与待应用主题在主线程消费。
	if atomic.CompareAndSwapInt32(&page.fileImportFailed, 1, 0) {
		page.message = "无法导入"
		page.messageUntil = c.Now().Add(2 * time.Second)
	}
	if pending, _ := page.pendingFileTheme.Load().(*config.ThemeDef); pending != nil {
		page.pendingFileTheme.Store((*config.ThemeDef)(nil))
		mutateTheme(st, func(a *config.AppearanceConfig) {
			theme := a.AddThemeTransfer(*pending)
			page.editingID = theme.ID
		})
	}
	if _, ok := st.cfg.Appearance.ThemeByID(page.editingID); !ok {
		page.editingID = st.cfg.Appearance.ActiveThemeID(isDark(c))
	}
	return page
}

func renderThemeTiles(c *ui.Context, st *SettingsState, page *themesSheetState) {
	ui.Row(c.Key("theme-list")).FillWidth().Gap(Space3).Wrap().AlignItems(ui.Start).Children(func() {
		ids := builtinThemeOrder()
		for _, theme := range st.cfg.Appearance.Themes {
			if !config.IsBuiltinTheme(theme.ID) {
				ids = append(ids, theme.ID)
			}
		}
		for _, id := range ids {
			renderThemeListItem(c, st, page, id)
		}
		trigger := ui.ButtonBase(c.Key("theme-add-menu")).Label("添加主题").Radius(RadiusMD).Cursor(ui.CursorPointer).OnClick(func() { page.menuOpen = true })
		if trigger.Hovered() {
			trigger.Background(ThemeHover(c))
		}
		focusRing(c, trigger)
		scale := motionPressScale(c, trigger, "button-press-scale", trigger.Pressed())
		trigger.Scale(scale, scale)
		trigger.Children(func() {
			ui.Box(c).Size(settingsPreviewTileW, settingsPreviewTileH).Radius(RadiusMD).Border(Hairline, ThemeBorder(c)).Center().Children(func() { ui.Text(c, "+").FontSize(uiFont(TypeHeading)).TextColor(ThemeSecondaryText(c)) })
		})
		PopoverCard(c, trigger, PopoverProps{Key: "theme-add-popover", Open: &page.menuOpen}, func() {
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
			Button(c, ButtonOpts{Key: "theme-import-file", Text: "从文件导入", Variant: ButtonGhost, OnClick: func() {
				page.menuOpen = false
				importSheetThemeFromFile(st, page)
			}})
			Button(c, ButtonOpts{Key: "theme-import-pi", Text: "导入pi主题", Variant: ButtonGhost, OnClick: func() {
				page.menuOpen = false
				page.piThemes = listPiThemeFiles()
				page.piMenuOpen = true
			}})
		})
		PopoverCard(c, trigger, PopoverProps{Key: "theme-import-pi-popover", Open: &page.piMenuOpen}, func() {
			if len(page.piThemes) == 0 {
				Button(c, ButtonOpts{Key: "theme-pi-empty", Text: "没有主题", Variant: ButtonGhost, Disabled: true})
				return
			}
			for _, item := range page.piThemes {
				item := item
				Button(c, ButtonOpts{Key: "theme-pi-file-" + item.path, Text: item.name, Variant: ButtonGhost, OnClick: func() {
					page.piMenuOpen = false
					if !importPiThemeFile(st, page, item.path) {
						page.message = "无法导入"
						page.messageUntil = c.Now().Add(2 * time.Second)
					} else {
						page.message = ""
					}
				}})
			}
		})
	})
}

func renderThemesSheet(c *ui.Context, st *SettingsState) {
	page := themesPage(st, c)
	id := page.editingID

	ui.Column(c.Key("themes-editor")).FillWidth().Gap(settingsSectionGap).Children(func() {
		renderThemeTiles(c, st, page)
		if !config.IsBuiltinTheme(id) {
			renderSettingsSection(c, "theme-name-section", "", func() { renderSettingRow(c, "row-theme-name", "名称", func() { renderThemeName(c, st, page, id) }) })
		}
		for _, section := range colorSections {
			renderSettingsSection(c, "theme-section-"+section.title, section.title, func() {
				renderColorSectionRows(c, st, page, id, section)
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
				renderSettingRow(c, "row-theme-font-"+key, titles[i], func() { renderThemeFont(c, st, page, id, key, titles[i]) })
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
	button := ui.ButtonBase(c.Key("theme-list-" + id)).Label(name).AccessSelected(selected).Radius(RadiusMD).Cursor(ui.CursorPointer).OnClick(func() { page.editingID = id; st.invalidate() }).Transition(motionFeedback(c))
	if button.Hovered() {
		button.Background(ThemeHover(c))
	}
	focusRing(c, button)
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

func renderThemeFont(c *ui.Context, st *SettingsState, page *themesSheetState, id, key, title string) {
	value := st.cfg.Appearance.ThemeFonts(id)[key]
	lower, upper, _, _ := config.ThemeFontRange(key)
	theme, _ := st.cfg.Appearance.ThemeByID(id)
	_, changed := theme.Fonts[key]
	renderThemeReset(c, "theme-reset-font-"+key, title, changed, func() { mutateTheme(st, func(a *config.AppearanceConfig) { a.ResetThemeFont(id, key) }) })

	valueKey := id + "/" + key
	input := page.fonts[valueKey]
	if input == nil {
		input = new(float64)
		page.fonts[valueKey] = input
	}
	*input = float64(value)
	if settingsSlider(c, "theme-font-"+key, input, float64(lower), float64(upper), title+"字号") {
		next := int(math.Round(*input))
		mutateTheme(st, func(a *config.AppearanceConfig) { _ = a.SetThemeFont(id, key, next) })
	}
	label := fmt.Sprintf("%d pt", value)
	if config.IsThemeScaleFont(key) {
		label = fmt.Sprintf("%d%%", value)
	}
	ui.Text(c, label).MinWidth(settingsPreviewTileW).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
}

func renderThemeReset(c *ui.Context, key, title string, changed bool, reset func()) {
	if !changed {
		ui.Box(c.Key(key+"-spacer")).Size(Space6, Space6).Shrink(0)
		return
	}
	IconButton(c, IconButtonProps{Key: key, Icon: IconRefreshCw(), Label: "还原" + title, Tooltip: "还原", Size: Space6, Variant: ButtonGhost, OnClick: reset}).Transition(themeFeedbackFade(c))
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
	tone := ConfirmDanger
	cancelAutoFocus := false
	title := "还原此主题"
	body := "还原此主题的颜色与字号？"
	label := "还原"
	if deleting {
		title = "删除主题"
		body = "删除此主题？"
		label = "删除"
		cancelAutoFocus = true
	}
	RenderConfirmDialog(c, ConfirmDialog{Key: "theme-confirm", Open: page.confirm != "", Tone: tone, CancelAutoFocus: cancelAutoFocus, Title: title, Body: body, ConfirmLabel: label, OnCancel: func() { page.confirm = "" }, OnConfirm: func() {
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
