package mcodeui

import (
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
)

const (
	// shortcutHelpWidth/Height 是快捷键浮层的默认尺寸。
	shortcutHelpKey                 = "modal-drag-shortcut-help"
	shortcutHelpWidth       float32 = 560
	shortcutHelpHeight      float32 = 420
	shortcutHelpWindowInset float32 = 80
	// shortcutHelpHeaderHeight 是浮层标题行的高度。
	shortcutHelpHeaderHeight float32 = 52
)

// shortcutHelpState 维护单个聊天窗口的快捷键浮层状态。
// 字段只在 UI 线程读写（键位分发、菜单派发与渲染都发生在界面线程），互斥锁只保护状态表。
type shortcutHelpState struct {
	open bool
	// query 是搜索框的文本；作为稳定指针交给 TextInput，避免每帧换指针导致输入被重置。
	query *string
	// focusPending 表示下一帧给搜索框要一次焦点。
	focusPending bool
}

var (
	shortcutHelpMu     sync.Mutex
	shortcutHelpStates = make(map[*chatState]*shortcutHelpState)
)

// getShortcutHelpState 获取或初始化当前 chatState 的快捷键浮层状态。
func getShortcutHelpState(s *chatState) *shortcutHelpState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	shortcutHelpMu.Lock()
	defer shortcutHelpMu.Unlock()
	if closed {
		delete(shortcutHelpStates, s)
		return nil
	}
	st, ok := shortcutHelpStates[s]
	if !ok {
		st = &shortcutHelpState{query: new(string)}
		shortcutHelpStates[s] = st
	}
	return st
}

// toggleShortcutHelp 打开或关闭快捷键浮层；打开时清空上一次的搜索词并让搜索框聚焦。
func (s *chatState) toggleShortcutHelp() {
	st := getShortcutHelpState(s)
	if st == nil {
		return
	}
	shortcutHelpMu.Lock()
	st.open = !st.open
	opened := st.open
	if opened {
		*st.query = ""
		st.focusPending = true
	}
	shortcutHelpMu.Unlock()
	if !opened {
		modalDragReset(shortcutHelpKey)
	}
	s.invalidate()
}

// closeShortcutHelp 关闭快捷键浮层。
func (s *chatState) closeShortcutHelp() {
	st := getShortcutHelpState(s)
	if st == nil {
		return
	}
	shortcutHelpMu.Lock()
	open := st.open
	st.open = false
	shortcutHelpMu.Unlock()
	modalDragReset(shortcutHelpKey)
	if open {
		s.invalidate()
	}
}

// IsOpen 报告快捷键浮层是否打开。
func (st *shortcutHelpState) IsOpen() bool {
	if st == nil {
		return false
	}
	shortcutHelpMu.Lock()
	defer shortcutHelpMu.Unlock()
	return st.open
}

// shortcutHelpGroup 是浮层里的一组快捷键，保持快捷键表里的分组顺序。
type shortcutHelpGroup struct {
	name  string
	items []appShortcut
}

// shortcutHelpGroups 按出现顺序把快捷键表分组。
func shortcutHelpGroups(list []appShortcut) []shortcutHelpGroup {
	groups := make([]shortcutHelpGroup, 0, 4)
	index := make(map[string]int, 4)
	for _, sc := range list {
		i, ok := index[sc.group]
		if !ok {
			i = len(groups)
			index[sc.group] = i
			groups = append(groups, shortcutHelpGroup{name: sc.group})
		}
		groups[i].items = append(groups[i].items, sc)
	}
	return groups
}

// shortcutHelpMatches 判断条目是否命中搜索词：标题、键位或分组名包含查询串即命中。
func shortcutHelpMatches(sc appShortcut, query string) bool {
	needle := strings.ToLower(strings.TrimSpace(query))
	if needle == "" {
		return true
	}
	if strings.Contains(strings.ToLower(sc.title), needle) {
		return true
	}
	if strings.Contains(strings.ToLower(sc.keys), needle) {
		return true
	}
	return strings.Contains(strings.ToLower(sc.group), needle)
}

// filterShortcutHelpGroups 按搜索词过滤分组，丢弃没有命中的分组。
func filterShortcutHelpGroups(groups []shortcutHelpGroup, query string) []shortcutHelpGroup {
	if strings.TrimSpace(query) == "" {
		return groups
	}
	filtered := make([]shortcutHelpGroup, 0, len(groups))
	for _, grp := range groups {
		items := make([]appShortcut, 0, len(grp.items))
		for _, sc := range grp.items {
			if shortcutHelpMatches(sc, query) {
				items = append(items, sc)
			}
		}
		if len(items) > 0 {
			filtered = append(filtered, shortcutHelpGroup{name: grp.name, items: items})
		}
	}
	return filtered
}

// renderShortcutHelp 渲染快捷键浮层（D-15）：搜索、按组两列网格与「在设置中编辑」。
// Esc 与点击浮层外由 ui.DialogBase 关闭；关闭后把状态写回，避免下一帧重新弹出。
func renderShortcutHelp(c *ui.Context, s *chatState) {
	st := getShortcutHelpState(s)
	if st == nil {
		return
	}
	shortcutHelpMu.Lock()
	open := st.open
	shortcutHelpMu.Unlock()
	if !open {
		modalDragReset(shortcutHelpKey)
		return
	}

	windowWidth, windowHeight := c.Size()
	width, height := modalResizeSize(shortcutHelpKey, shortcutHelpWidth, shortcutHelpHeight, 360, 240, windowWidth, windowHeight)

	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
		modalDragPanel(c, shortcutHelpKey, panel)
		list := appShortcuts()
		panel.Children(func() {
			card := ui.Column(c.Key("shortcut-help-card")).
				Label("shortcut-help-card").
				Width(width).
				Height(height).
				Radius(RadiusLG).
				Background(ThemeWindowBg(c)).
				Border(Hairline, ThemeBorder(c)).
				Children(func() {
					modalDragHandle(c, shortcutHelpKey, ui.Row(c.Key("shortcut-help-header")).
						FillWidth().
						Height(shortcutHelpHeaderHeight).
						Padding(0, Space5).
						Gap(Space3).
						AlignItems(ui.Center).
						BorderWidth(0, 0, Hairline, 0).
						BorderColor(ThemeBorder(c)).
						Children(func() {
							ui.Text(c, "键盘快捷键").
								FontSize(TypeTitle).
								FontWeight(600).
								TextColor(ThemePrimaryText(c))
							ui.Box(c.Key("shortcut-help-header-spacer")).Grow(1)
							ui.Text(c, "Esc 关闭").
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
						}))

					ui.Column(c.Key("shortcut-help-body")).
						FillWidth().
						Grow(1).
						Padding(Space5, Space5).
						Gap(Space3).
						Children(func() {
							in := ui.TextInput(c.Key("shortcut-help-search"), st.query).
								Label("搜索快捷键").
								Placeholder("搜索快捷键").
								FontSize(TypeBody)
							if st.focusPending {
								in.Focus()
								st.focusPending = false
							}

							groups := filterShortcutHelpGroups(shortcutHelpGroups(list), *st.query)
							if len(groups) == 0 {
								ui.Text(c, "没有匹配的快捷键").
									FontSize(TypeFootnote).
									TextColor(ThemeSecondaryText(c))
								return
							}
							conflicts := shortcutConflicts(effectiveShortcutBindings())
							ui.Scroll(c.Key("shortcut-help-scroll")).
								FillWidth().
								Grow(1).
								Children(func() {
									ui.Column(c.Key("shortcut-help-groups")).
										FillWidth().
										Gap(Space3).
										Children(func() {
											for _, grp := range groups {
												renderShortcutHelpGroup(c, grp, conflicts)
											}
										})
								})
						})

					ui.Row(c.Key("shortcut-help-footer")).
						FillWidth().
						Height(shortcutHelpHeaderHeight).
						Padding(0, Space5).
						Gap(Space3).
						AlignItems(ui.Center).
						BorderWidth(Hairline, 0, 0, 0).
						BorderColor(ThemeBorder(c)).
						Children(func() {
							ui.Text(c, fmt.Sprintf("共 %d 项", len(list))).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
							// 没有设置状态时（如精简窗口/测试未注入设置）不渲染按钮，避免点了没反应。
							if getChatSettings(s) != nil {
								ui.Box(c.Key("shortcut-help-footer-spacer")).Grow(1)
								Button(c.Key("shortcut-help-open-settings"), ButtonOpts{
									Text:    "在设置中编辑",
									Label:   "在设置中编辑快捷键",
									Variant: ButtonSecondary,
									OnClick: func() {
										s.closeShortcutHelp()
										if st := getChatSettings(s); st != nil {
											st.OpenPage("shortcuts")
										}
									},
								})
							}
						})
					modalResizeGrips(c, shortcutHelpKey, width, height)
				})
			popoverCardShadow(card)
		})
	})

	if !open {
		s.closeShortcutHelp()
	}
}

// renderShortcutHelpGroup 渲染一组快捷键：组名在上，组内每行左侧标题、右侧键位。
func renderShortcutHelpGroup(c *ui.Context, grp shortcutHelpGroup, conflicts map[string][]string) {
	ui.Column(c.Key("shortcut-help-group-"+grp.name)).
		FillWidth().
		Padding(Space3, Space4).
		Radius(RadiusLG).
		Gap(Space0).
		Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).
		Children(func() {
			ui.Text(c, grp.name).
				FontSize(TypeCaption).
				FontWeight(600).
				TextColor(ThemeSecondaryText(c))
			for _, sc := range grp.items {
				renderShortcutHelpRow(c, sc, conflicts[sc.id])
			}
		})
}

// renderShortcutHelpRow 渲染一行快捷键：左边标题（冲突项带 10pt 红色角标），右边键位。
func renderShortcutHelpRow(c *ui.Context, sc appShortcut, conflicts []string) {
	ui.Row(c.Key("shortcut-help-row-" + sc.id)).
		FillWidth().
		Height(ControlMD).
		Gap(Space3).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, sc.title).
				FontSize(TypeBody).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()
			if len(conflicts) > 0 {
				ui.Row(c.Key("shortcut-help-conflict-"+sc.id)).
					Padding(Space0, Space2).
					Radius(RadiusXS).
					Background(ThemeDanger(c).Alpha(0.14)).
					Children(func() {
						ui.Text(c, "冲突").
							FontSize(TypeCaption).
							FontWeight(600).
							TextColor(ThemeDanger(c))
					})
			}
			Kbd(c, "shortcut-help-kbd-"+sc.id, sc.keys)
		})
}
