package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

const settingsRowTitleMinWidth float32 = 160

// settingsPage 是一个设置页的声明：标题、图标、检索词与按顺序排列的 section。
// 页面只声明，渲染、索引、重置、锁定全部由下面的渲染器与派生函数统一处理。
type settingsPage struct {
	ID       string
	Title    string
	Icon     *ui.SVG
	Keywords []string
	Sections []settingsSection
	Load     func(st *SettingsState)
}

// settingsSection 是页面或面板里的一组行；Visible 非空时由渲染器判断是否渲染。
type settingsSection struct {
	ID      string
	Title   string
	Rows    []setting
	Visible func(st *SettingsState) bool
}

// setting 是一行的唯一声明：标题、检索词、锁定键与控件。
// Status 只在运行时值需要写出来时给出（锁定原因、未知档位），返回空串就不渲染。
type setting struct {
	ID      string
	Title   string
	Aliases []string
	Lock    string
	Control settingControl
	Status  func(st *SettingsState, locked bool) string
	Enabled func(st *SettingsState) bool
	Visible func(st *SettingsState) bool
}

func settingVisible(st *SettingsState, s setting) bool {
	return s.Visible == nil || s.Visible(st)
}

func settingsSectionVisible(st *SettingsState, section settingsSection) bool {
	return section.Visible == nil || section.Visible(st)
}

func visibleSettingsRows(st *SettingsState, section settingsSection, inSheet bool) []setting {
	if !settingsSectionVisible(st, section) {
		return nil
	}
	var rows []setting
	for _, s := range section.Rows {
		if s.Control == nil || !settingVisible(st, s) {
			continue
		}
		if _, isLink := s.Control.(linkControl); inSheet && isLink {
			continue
		}
		rows = append(rows, s)
	}
	return rows
}

// settingControl 只负责在行内画出控件。
type settingControl interface {
	render(c *ui.Context, st *SettingsState, s setting)
}

// settingResettable 是控件的重置能力：只有带绑定的控件实现它。
type settingResettable interface {
	reset(cfg *config.Config)
}

// settingDisableable 是控件的禁用能力：锁定行由渲染器统一传 disabled，
// 控件自己不知道锁定键，也不调用 st.isLocked。
type settingDisableable interface {
	renderDisabled(c *ui.Context, st *SettingsState, s setting, disabled bool)
}

// settingsSheet 是二级面板的声明：结构同页面，但不进导航、不参与整页重置。
// Body 用于运行时才成形的整块内容（例如快捷键表），有 Body 时不再渲染 Sections。
type settingsSheet struct {
	Width    float32
	ID       string
	Title    string
	Sections []settingsSection
	Body     func(c *ui.Context, st *SettingsState)
	Load     func(st *SettingsState)

	// Header 非空时替换头部标题文本；关闭按钮仍由外壳画。
	Header func(c *ui.Context, st *SettingsState)
	// Footer 非空时在滚动区下方画固定操作栏（含发丝线）。
	Footer func(c *ui.Context, st *SettingsState)
	// FooterVisible 非空时仅在返回真时显示操作栏，为空时沿用 Footer 非空即显示的行为。
	FooterVisible func(st *SettingsState) bool
	// FitContent 为真且用户未调整高度时，按上一帧内容高度贴合，限制在 240 到 640 之间。
	FitContent bool
	// MinWidth 是最小宽度，0 表示沿用 settingsSheetMinWidth。
	MinWidth float32
	// BodyOwnsScroll 为真时 Body 自己负责滚动，外壳不再包 ui.Scroll。
	BodyOwnsScroll bool
}

// settingsSheetMeasure 保存上一帧读取的布局高度与面板使用的高度。
type settingsSheetMeasure struct {
	head, content, footer float32
	height                float32
}

var (
	settingsSheetMeasureMu sync.Mutex
	settingsSheetMeasures  = make(map[string]*settingsSheetMeasure)
)

// reportSettingsSheetContentHeight 供自行滚动的正文上报上一帧内容高度。
func reportSettingsSheetContentHeight(sheetID string, h float32) {
	settingsSheetMeasureMu.Lock()
	defer settingsSheetMeasureMu.Unlock()
	measure := settingsSheetMeasures[sheetID]
	if measure == nil {
		measure = &settingsSheetMeasure{}
		settingsSheetMeasures[sheetID] = measure
	}
	measure.content = h
}

// settingsTreePageDefs 是全部页面的注册表。各页面和面板文件在自己的 init() 里追加注册；
// 包级变量不能写初始值，否则会和渲染链形成初始化循环。
var settingsTreePageDefs []settingsPage

// settingsTreeSheetDefs 是全部面板的注册表。同上，由面板文件自己的 init() 追加。
var settingsTreeSheetDefs []settingsSheet

// settingsTreePages 返回注册的全部页面。
func settingsTreePages() []settingsPage { return settingsTreePageDefs }

// settingsTreeSheets 返回注册的全部面板。
func settingsTreeSheets() []settingsSheet { return settingsTreeSheetDefs }

// settingsTreePageOf 按 ID 查找已迁到页面树的页面。
func settingsTreePageOf(id string) (settingsPage, bool) {
	for _, p := range settingsTreePageDefs {
		if p.ID == id {
			return p, true
		}
	}
	return settingsPage{}, false
}

// settingsTreeItemOf 按行 ID 查找树里的条目，页面行与面板行都算。
func settingsTreeItemOf(id string) (settingsTreeItem, bool) {
	for _, item := range settingsTreeItems() {
		if item.ID == id {
			return item, true
		}
	}
	return settingsTreeItem{}, false
}

// settingsTreeSheetOf 按 ID 查找面板。
func settingsTreeSheetOf(id string) (settingsSheet, bool) {
	for _, sh := range settingsTreeSheetDefs {
		if sh.ID == id {
			return sh, true
		}
	}
	return settingsSheet{}, false
}

// settingsTreeItem 是搜索索引与快照使用的一行派生条目。
type settingsTreeItem struct {
	ID      string
	Title   string
	Aliases []string
	Page    string
	Section string
	Sheet   string
}

// settingsTreeItems 从注册表派生全部条目。
func settingsTreeItems() []settingsTreeItem {
	return deriveSettingsTreeItems(settingsTreePageDefs, settingsTreeSheetDefs)
}

// deriveSettingsTreeItems 遍历给定页面与面板生成条目：list 控件只产出前缀条目一条。
func deriveSettingsTreeItems(pages []settingsPage, sheets []settingsSheet) []settingsTreeItem {
	var items []settingsTreeItem
	for _, p := range pages {
		for _, section := range p.Sections {
			for _, s := range section.Rows {
				item := settingsTreeItem{
					ID:      s.ID,
					Title:   s.Title,
					Aliases: s.Aliases,
					Page:    p.ID,
					Section: section.ID,
				}
				if lc, ok := s.Control.(listControl); ok {
					item.ID = lc.prefix
				}
				items = append(items, item)
			}
		}
	}
	for _, sh := range sheets {
		for _, section := range sh.Sections {
			for _, s := range section.Rows {
				item := settingsTreeItem{
					ID:      s.ID,
					Title:   s.Title,
					Aliases: s.Aliases,
					Section: section.ID,
					Sheet:   sh.ID,
				}
				if lc, ok := s.Control.(listControl); ok {
					item.ID = lc.prefix
				}
				items = append(items, item)
			}
		}
	}
	return items
}

// renderSettingsTreePage 遍历页面树画出整页。
func renderSettingsTreePage(c *ui.Context, st *SettingsState, p settingsPage) {
	ui.Column(c.Key("settings-tree-page-" + p.ID)).Gap(settingsSectionGap).Children(func() {
		for _, section := range p.Sections {
			section := section
			renderSettingsTreeSection(c, st, section, false)
		}
	})
}

// renderSettingsTreeSheet 用 DialogBase 画面板：只允许从打开状态渲染，关闭时清空状态。
func renderSettingsTreeSheet(c *ui.Context, st *SettingsState, sh settingsSheet) {
	key := "settings-sheet-" + sh.ID
	openID, _ := settingsTreeSheetState(st)
	if openID != sh.ID {
		return
	}

	open := true
	windowWidth, windowHeight := c.Size()
	defaultWidth := sh.Width
	switch defaultWidth {
	case 0:
		defaultWidth = settingsSheetWidth
	case 720:
		defaultWidth = settingsSheetWideWidth
	}
	minWidth := sh.MinWidth
	if minWidth == 0 {
		minWidth = settingsSheetMinWidth
	}
	width, height := modalResizeSize(key, defaultWidth, 640, minWidth, 240, windowWidth, windowHeight)
	settingsSheetMeasureMu.Lock()
	measure := settingsSheetMeasures[sh.ID]
	if measure == nil {
		measure = &settingsSheetMeasure{}
		settingsSheetMeasures[sh.ID] = measure
	}
	previous := *measure
	settingsSheetMeasureMu.Unlock()
	if sh.FitContent && !modalResizeUserHeight(key) && previous.head > 0 && previous.content > 0 {
		height = max(float32(240), min(previous.head+previous.content+previous.footer, min(float32(640), windowHeight-modalDragPanelInset)))
	}
	var headHeight, contentHeight, footerHeight float32

	Sheet(c, SheetProps{Key: key, CardKey: "settings-sheet-card-" + sh.ID, Label: "设置面板 " + sh.Title, Open: &open, Width: width, Height: height}, func() {
		head := modalDragHandle(c, key, ui.Row(c.Key("settings-sheet-head-"+sh.ID)).
			Padding(Space5, Space5).
			AlignItems(ui.Center).
			Gap(Space4))
		head.Children(func() {
			if sh.Header != nil {
				sh.Header(c, st)
			} else {
				ui.Text(c, sh.Title).
					FontSize(TypeTitle).
					FontWeight(600).
					TextColor(ThemePrimaryText(c)).
					Grow(1).
					SingleLine()
			}
			IconButton(c, IconButtonProps{
				Key: "settings-sheet-close-" + sh.ID, Icon: IconX(), Label: "关闭" + sh.Title,
				Size: ControlSM, Variant: ButtonGhost, OnClick: func() { closeSettingsSheet(st) },
			}).Shrink(0)
		})
		headHeight = head.Bounds().H
		ui.Column(c.Key("settings-sheet-body-" + sh.ID)).
			Grow(1).
			Clip().
			Children(func() {
				// BodyOwnsScroll 时正文列自己滚动（已 Grow(1).Clip()）。
				if sh.Body != nil && sh.BodyOwnsScroll {
					sh.Body(c, st)
					return
				}
				// 快捷键表这类内容比面板高，正文必须能滚动，否则下半截被裁掉。
				ui.Scroll(c.Key("settings-sheet-scroll-" + sh.ID)).
					Fill().
					Children(func() {
						content := ui.Column(c.Key("settings-sheet-content-"+sh.ID)).FillWidth().Gap(settingsSectionGap).Padding(0, Space5, Space5, Space5)
						content.Children(func() {
							if sh.Body != nil {
								sh.Body(c, st)
								return
							}
							for _, section := range sh.Sections {
								section := section
								renderSettingsTreeSection(c, st, section, true)
							}
						})
						contentHeight = content.Bounds().H
					})
			})
		if sh.Footer != nil && (sh.FooterVisible == nil || sh.FooterVisible(st)) {
			ui.Box(c.Key("settings-sheet-footer-rule-" + sh.ID)).
				Height(Hairline).
				Background(ThemeHairline(c)).
				Shrink(0)
			footer := ui.Row(c.Key("settings-sheet-footer-"+sh.ID)).
				Label("面板操作栏 "+sh.Title).
				Padding(Space4, Space5).
				AlignItems(ui.Center).
				Gap(Space4).
				Shrink(0)
			footer.Children(func() { sh.Footer(c, st) })
			footerHeight = Hairline + footer.Bounds().H
		}
		modalResizeGripsMin(c, key, width, height, minWidth, 240)
	}).Clip()
	settingsSheetMeasureMu.Lock()
	measure.head, measure.footer, measure.height = headHeight, footerHeight, height
	if !sh.BodyOwnsScroll {
		measure.content = contentHeight
	}
	changed := *measure != previous
	settingsSheetMeasureMu.Unlock()
	if sh.FitContent && changed {
		c.Invalidate()
	}
	if currentID, _ := settingsTreeSheetState(st); !open || currentID != sh.ID {
		modalDragReset(key)
		settingsSheetMeasureMu.Lock()
		delete(settingsSheetMeasures, sh.ID)
		settingsSheetMeasureMu.Unlock()
		if !open {
			closeSettingsSheet(st)
		}
	}
}

// renderSettingsSection 渲染设置页的分节：标题在卡片外，内容整体包进 SectionCard。
// 标题不做吸顶：详情区的行定位（ScrollIntoView）会把目标行对到滚动区顶边，
// 吸顶标题会盖住它对不齐的那一段，而 mygo 没有 scroll-padding 之类的避让机制。
func renderSettingsSection(c *ui.Context, key string, title string, content func()) {
	ui.Column(c.Key(key)).Gap(settingsSectionTitleGap).Children(func() {
		if title != "" {
			SectionHeading(c, nil, title).
				Margin(0, 0, 0, settingsRowPadX)
		}
		SectionCard(c, key+"-card", "", content).Label("设置卡片 " + key)
	})
}

// renderSettingsDivider 渲染卡片内行之间的细线分隔。
func renderSettingsDivider(c *ui.Context, key string) {
	ui.Box(c.Key(key)).
		Height(Hairline).
		Background(ThemeHairline(c)).
		Margin(0, settingsDividerInset, 0, settingsDividerInset)
}

// settingsRowFocus 保存当前帧的行定位与高亮信息：jumpID 是要滚动到可视区的那一行，
// highlightID 是要高亮的那一行。它由 renderSettingsDetail 在每帧渲染详情区之前根据
// SettingsState 赋值，两个行渲染函数只读它，不改签名。
var settingsRowFocus struct {
	jumpID      string
	highlightID string
	dynamicJump bool
	// pulseSeq 是高亮脉冲的序号：每次高亮项变化都递增，拼进 Keyframes 的 key 让同一项
	// 再次命中时从头重播，而不是停在上一轮的末值。
	pulseSeq uint64
	// onJump 在完成一次性滚动后被调用，用来把 SettingsState.jumpToItemID 清空，避免每帧
	// 都把用户拉回那一行。
	onJump func(string)
}

const (
	// settingsSearchPulseStep 是一次「亮起 → 熄灭」的时长：整段脉冲播两次，正好覆盖
	// settingsItemHighlightDuration。
	settingsSearchPulseStep = settingsItemHighlightDuration / 4
	// settingsSearchPulseMaxAlpha 是描边脉冲的峰值透明度。
	settingsSearchPulseMaxAlpha = 0.7
	// settingsSearchPulseBorder 是搜索命中行的描边粗细。
	settingsSearchPulseBorder float32 = 2
	// settingsSearchPulseRadius 沿用设置卡的圆角。
	settingsSearchPulseRadius float32 = 10
)

// settingsRowHighlightPaint 是搜索命中行的一帧描边样式。
type settingsRowHighlightPaint struct {
	BorderWidth float32
	BorderAlpha float32
	// SelectedBackground 表示是否整行铺选中底色；搜索命中恒为 false（D-13）。
	SelectedBackground bool
}

// settingsRowHighlightPaintFor 计算搜索命中行的描边脉冲样式：命中只画 2pt accent 描边、
// 不铺选中底色，避免和真正的选中态撞在一起；pulse 的取值范围是 0…1。
func settingsRowHighlightPaintFor(key, highlightID string, pulse float32) settingsRowHighlightPaint {
	if key == "" || highlightID == "" || key != highlightID {
		return settingsRowHighlightPaint{}
	}
	pulse = max(0, min(pulse, 1))
	return settingsRowHighlightPaint{
		BorderWidth: settingsSearchPulseBorder,
		BorderAlpha: settingsSearchPulseMaxAlpha * pulse,
	}
}

// settingsSearchPulseKeyframes 是搜索命中行的描边透明度关键帧：0 → 1 → 0 播两次。
func settingsSearchPulseKeyframes() []ui.Keyframe {
	return []ui.Keyframe{
		{Value: 0},
		{At: settingsSearchPulseStep, Value: 1, Ease: motionEaseStandard},
		{At: 2 * settingsSearchPulseStep, Value: 0, Ease: motionEaseStandard},
		{At: 3 * settingsSearchPulseStep, Value: 1, Ease: motionEaseStandard},
		{At: 4 * settingsSearchPulseStep, Value: 0, Ease: motionEaseStandard},
	}
}

// applySettingsRowFocus 给一行套上定位与高亮：命中高亮 key 时画一圈脉冲描边（不改底色），
// 命中定位 key 时滚动到可视区一次并回调清空。
func applySettingsRowFocus(c *ui.Context, row ui.Element, key string) ui.Element {
	if key == "" {
		return row
	}
	jump := settingsRowFocus.jumpID != "" && (key == settingsRowFocus.jumpID || settingsRowFocus.dynamicJump && strings.HasPrefix(key, settingsRowFocus.jumpID))
	if jump && settingsRowFocus.dynamicJump && settingsRowFocus.highlightID == settingsRowFocus.jumpID {
		settingsRowFocus.highlightID = key
	}
	if settingsRowFocus.highlightID != "" && key == settingsRowFocus.highlightID {
		pulse := row.Keyframes(fmt.Sprintf("settings-search-pulse-%d", settingsRowFocus.pulseSeq), settingsSearchPulseKeyframes())
		paint := settingsRowHighlightPaintFor(key, settingsRowFocus.highlightID, pulse)
		row = row.Border(paint.BorderWidth, ThemeAccent(c).Alpha(paint.BorderAlpha)).Radius(settingsSearchPulseRadius)
		if paint.SelectedBackground {
			row = row.Background(ThemeSelected(c))
		}
	}
	if jump {
		row = row.ScrollIntoView()
		settingsRowFocus.jumpID = ""
		if onJump := settingsRowFocus.onJump; onJump != nil {
			onJump(key)
		}
	}
	return row
}

// renderSettingRow 渲染卡片内的标题与控件：树渲染器与统一控件共用的私有原语，不带说明句。
func renderSettingRow(c *ui.Context, key, title string, control func()) {
	if settingsRowFocus.jumpID == "" && settingsRowFocus.highlightID == "" {
		SettingRow(c, key, title, control)
		return
	}
	settingRow(c, key, title, false, func(row ui.Element) ui.Element { return applySettingsRowFocus(c, row, key) }, control)
}

// renderStackedSettingRow 渲染标题和控件上下堆叠的设置行。
func renderStackedSettingRow(c *ui.Context, key, title string, control func()) {
	if settingsRowFocus.jumpID == "" && settingsRowFocus.highlightID == "" {
		StackedSettingRow(c, key, title, control)
		return
	}
	settingRow(c, key, title, true, func(row ui.Element) ui.Element { return applySettingsRowFocus(c, row, key) }, control)
}

// renderSettingsTreeSection 画一个 section：节标题、卡片与行之间的分隔线。
func renderSettingsTreeSection(c *ui.Context, st *SettingsState, section settingsSection, inSheet bool) {
	rows := visibleSettingsRows(st, section, inSheet)
	if len(rows) == 0 {
		return
	}
	renderSettingsSection(c, section.ID, section.Title, func() {
		for i, s := range rows {
			if i > 0 {
				renderSettingsDivider(c, section.ID+"-divider-"+s.ID)
			}
			renderSettingsTreeSetting(c, st, s)
		}
	})
}

// settingRowLocked 判断一行是否被命令行参数锁定；st 为空时不锁定。
func settingRowLocked(st *SettingsState, s setting) bool {
	return st != nil && s.Lock != "" && st.isLocked(s.Lock)
}

// renderSettingsTreeSetting 画一行：锁定或条件禁用时统一禁用并在行尾加「锁定」徽标，
// 运行时状态放在行下方，不当说明句写。
func renderSettingsTreeSetting(c *ui.Context, st *SettingsState, s setting) {
	switch ctl := s.Control.(type) {
	case listControl:
		ctl.render(c, st, s)
		return
	case magpieAgentRouteControl:
		ctl.render(c, st, s)
		return
	case customControl:
		ctl.render(c, st, s)
		return
	}

	locked := settingRowLocked(st, s)
	disabled := locked || (s.Enabled != nil && !s.Enabled(st))
	renderRow := renderSettingRow
	switch s.Control.(type) {
	case textControl, textFuncControl:
		renderRow = renderStackedSettingRow
	}
	control := func() {
		ui.Row(c.Key(s.ID + "-control")).AlignItems(ui.Center).Gap(Space3).Children(func() {
			if d, ok := s.Control.(settingDisableable); ok {
				d.renderDisabled(c, st, s, disabled)
			} else {
				s.Control.render(c, st, s)
			}
			if locked {
				settingBadge(c, s.ID+"-locked", "锁定")
			}
		})
	}
	if link, ok := s.Control.(linkControl); ok {
		button := ui.ButtonBase(c.Key(s.ID+"-link")).Label(s.Title+" "+link.value(st)).FillWidth().
			Padding(settingsRowPadY, settingsRowPadX).MinHeight(settingsRowMinHeight).Disabled(disabled)
		if !disabled && button.Hovered() {
			button.Background(ThemeHover(c))
		}
		if !disabled && button.Clicked() {
			openSettingsSheet(st, link.sheet, s.ID)
		}
		applySettingsRowFocus(c, button, s.ID).Children(func() {
			ui.Row(c.Key(s.ID)).FillWidth().Gap(Space4).AlignItems(ui.Center).Children(func() {
				settingRowContent(c, s.ID, s.Title, control)
			})
		})
	} else {
		renderRow(c, s.ID, s.Title, control)
	}

	if s.Status == nil {
		return
	}
	status := s.Status(st, locked)
	if status == "" {
		return
	}
	ui.Column(c.Key(s.ID+"-status")).Padding(0, settingsRowPadX, settingsRowPadY, settingsRowPadX).Children(func() {
		settingStatus(c, s.ID+"-status-text", status)
	})
}

// settingsTreeSheetState 在锁内读取当前打开的面板与待跳转行。
func settingsTreeSheetState(st *SettingsState) (string, string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.openSheet, st.sheetJumpRow
}

// settingsSheetOpenHook 仅供测试统计面板打开次数（生产为 nil）：面板正文渲染期间
// 再打开另一个面板会让层级超过 1，Esc 要连按多次才能回到页面。
var settingsSheetOpenHook func(sheet string)

// openSettingsSheet 打开面板并记下要跳转的行，下一次渲染生效。
func openSettingsSheet(st *SettingsState, sheet string, row string) {
	openSettingsSheetFrom(st, sheet, row, "")
}

// openSettingsSheetFrom 打开面板，并在关闭时把高亮交还给 sourceKey 那一行。
func openSettingsSheetFrom(st *SettingsState, sheet string, row string, sourceKey string) {
	if hook := settingsSheetOpenHook; hook != nil {
		hook(sheet)
	}
	st.mu.Lock()
	st.openSheet = sheet
	st.sheetJumpRow = row
	// sourceKey 为空也照写，用来清掉上一次打开残留的来源行。
	st.sheetSourceRow = sourceKey
	st.mu.Unlock()
	st.invalidate()
}

// closeSettingsSheet 关闭面板，清空跳转行，并把高亮交还给打开面板的那一行。
func closeSettingsSheet(st *SettingsState) {
	st.mu.Lock()
	sourceKey := st.sheetSourceRow
	sheetID := st.openSheet
	changed := st.openSheet != "" || st.sheetJumpRow != "" || sourceKey != ""
	st.openSheet = ""
	st.sheetJumpRow = ""
	st.sheetSourceRow = ""
	if sourceKey != "" {
		st.highlightItemID = sourceKey
		st.highlightStartedAt = time.Now()
	}
	st.mu.Unlock()
	if sheetID != "" {
		modalDragReset("settings-sheet-" + sheetID)
		settingsSheetMeasureMu.Lock()
		delete(settingsSheetMeasures, sheetID)
		settingsSheetMeasureMu.Unlock()
	}
	if !changed {
		return
	}
	if sourceKey != "" {
		// 同一行重复回焦时也要重播描边脉冲；到期后叫醒界面一次把高亮清掉。
		settingsRowFocus.pulseSeq++
		time.AfterFunc(settingsItemHighlightDuration, st.invalidate)
	}
	st.invalidate()
}

// resetSettingsTreePage 恢复本页默认值：跳过锁定行，写回一次盘。
func resetSettingsTreePage(st *SettingsState, p settingsPage) {
	var controls []settingResettable
	for _, section := range p.Sections {
		if !settingsSectionVisible(st, section) {
			continue
		}
		for _, s := range section.Rows {
			if !settingVisible(st, s) || settingRowLocked(st, s) {
				continue
			}
			if r, ok := s.Control.(settingResettable); ok {
				controls = append(controls, r)
			}
		}
	}
	if len(controls) == 0 {
		return
	}

	// 与现有页面一致：改 cfg 同时持 mu 与 cfgMu（顺序 mu → cfgMu），save 在锁外调。
	st.mu.Lock()
	st.cfgMu.Lock()
	for _, r := range controls {
		r.reset(&st.cfg)
	}
	st.cfgMu.Unlock()
	st.mu.Unlock()

	st.save()
	st.invalidate()
}

// serializeVisibleSettingsTree 序列化指定状态下可见的页面与面板行。
func serializeVisibleSettingsTree(st *SettingsState, pages []settingsPage, sheets []settingsSheet) string {
	if st == nil {
		panic("serializeVisibleSettingsTree requires a non-nil state")
	}
	filter := func(sections []settingsSection, inSheet bool) []settingsSection {
		var result []settingsSection
		for _, section := range sections {
			section.Rows = visibleSettingsRows(st, section, inSheet)
			if len(section.Rows) > 0 {
				result = append(result, section)
			}
		}
		return result
	}
	filteredPages := append([]settingsPage(nil), pages...)
	for i := range filteredPages {
		filteredPages[i].Sections = filter(filteredPages[i].Sections, false)
	}
	filteredSheets := append([]settingsSheet(nil), sheets...)
	for i := range filteredSheets {
		filteredSheets[i].Sections = filter(filteredSheets[i].Sections, true)
	}
	return serializeSettingsTree(filteredPages, filteredSheets)
}

// serializeSettingsTree 把页面树的完整声明序列化成文本，供快照测试比较。
func serializeSettingsTree(pages []settingsPage, sheets []settingsSheet) string {
	var b strings.Builder
	b.WriteString("# settings tree\n")
	for _, p := range pages {
		fmt.Fprintf(&b, "page %s\n", p.ID)
		for _, section := range p.Sections {
			fmt.Fprintf(&b, "section %s %s\n", section.ID, section.Title)
			for _, s := range section.Rows {
				fmt.Fprintf(&b, "row %s %s\n", s.ID, s.Title)
			}
		}
	}
	for _, sh := range sheets {
		fmt.Fprintf(&b, "sheet %s %s\n", sh.ID, sh.Title)
		for _, section := range sh.Sections {
			fmt.Fprintf(&b, "section %s %s\n", section.ID, section.Title)
			for _, s := range section.Rows {
				fmt.Fprintf(&b, "row %s %s\n", s.ID, s.Title)
			}
		}
	}
	return b.String()
}
