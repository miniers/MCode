package mcodeui

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

var (
	paletteMu     sync.Mutex
	paletteStates = make(map[*chatState]*paletteState)
)

const (
	paletteWidth         float32 = 560
	paletteWindowInset   float32 = 40
	paletteCardInset     float32 = 80
	paletteListMaxHeight float32 = 360
)

// paletteState 维护单个聊天会话视图下的命令面板状态。
type paletteState struct {
	open            bool
	query           string
	prevQuery       string
	selected        int
	scrollSelection bool

	// 消息正文搜索结果：msgHits 是 msgQuery 的命中，msgPending 是已经排上防抖的查询串，
	// msgSeq 是请求序号（只有最新一次的异步结果会被采纳），msgTimer 是防抖定时器。
	msgHits    []MessageSearchHit
	msgQuery   string
	msgPending string
	msgSeq     uint64
	msgTimer   *time.Timer
}

// getPaletteState 获取或初始化当前 chatState 的命令面板状态。
func getPaletteState(s *chatState) *paletteState {
	if s == nil {
		return nil
	}
	paletteMu.Lock()
	defer paletteMu.Unlock()
	st, ok := paletteStates[s]
	if !ok {
		st = &paletteState{}
		paletteStates[s] = st
	}
	return st
}

// paletteItem 表示命令面板中的单个匹配候选条目。
// keywords 是只参与搜索、不显示的关键词（别名、别称），用来让「总管」「话题」这类说法也能命中。
type paletteItem struct {
	id       string
	kind     string
	title    string
	subtitle string
	keywords string
	run      func(s *chatState)
}

// sidePanePaletteItems 由 side_pane.go 的注册表生成右侧分栏面板的命令面板开关项。
// 循环变量先拷贝到局部变量，避免所有 run 闭包捕获同一个值。
func sidePanePaletteItems() []paletteItem {
	items := make([]paletteItem, 0, len(sidePanes))
	for i := range sidePanes {
		p := &sidePanes[i]
		toggle := p.toggle
		items = append(items, paletteItem{
			id:    p.paletteID,
			kind:  "command",
			title: p.paletteTitle,
			run: func(cs *chatState) {
				if toggle != nil {
					toggle(cs)
				}
			},
		})
	}
	return items
}

// buildPaletteItems 收集全部候选项目：命令优先，随后是所有会话与项目。
func buildPaletteItems(s *chatState) []paletteItem {
	if s == nil {
		return nil
	}

	items := make([]paletteItem, 0, 6)

	// 1. 命令优先
	items = append(items,
		paletteItem{
			id:    "cmd-new-thread",
			kind:  "command",
			title: "新建会话",
			run: func(cs *chatState) {
				cs.closeWorkspace()
				cs.createNewThread()
			},
		},
		paletteItem{
			id:    "cmd-add-project",
			kind:  "command",
			title: "添加项目",
			run: func(cs *chatState) {
				cs.promptAddProject()
			},
		},
		paletteItem{
			id:    "cmd-open-settings",
			kind:  "command",
			title: "打开设置",
			run: func(cs *chatState) {
				openSettingsWindow(cs, "")
			},
		},
	)
	for _, page := range settingsPages() {
		if page.id == "about" {
			continue
		}
		pageID := page.id
		items = append(items, paletteItem{
			id:       "cmd-settings-" + pageID,
			kind:     "command",
			title:    "设置：" + page.title,
			keywords: "设置 settings " + page.title + " " + strings.Join(page.keywords, " "),
			run: func(cs *chatState) {
				openSettingsWindow(cs, pageID)
			},
		})
	}
	items = append(items,
		paletteItem{
			id:    "cmd-check-update",
			kind:  "command",
			title: "检查更新",
			run: func(cs *chatState) {
				if getChatSettings(cs) == nil {
					return
				}
				openSettingsWindow(cs, "about")
				if st := currentSettingsWindowState(); st != nil {
					startUpdateCheck(st)
				}
			},
		},
		paletteItem{
			id:    "cmd-changelog",
			kind:  "command",
			title: "更新日志",
			run: func(cs *chatState) {
				if getChatSettings(cs) == nil {
					return
				}
				if versions := changelogVersions(); len(versions) > 0 {
					aboutChangelogExpanded[versions[0].Version] = true
				}
				openSettingsWindow(cs, "about")
				if st := currentSettingsWindowState(); st != nil {
					st.focusSettingsItem(settingsItemHit{Item: settingsItem{ID: "row-about-changelog", Page: "about"}})
				}
			},
		},
		paletteItem{
			id:    "cmd-open-tasks",
			kind:  "command",
			title: "打开任务",
			run: func(cs *chatState) {
				cs.openTasksWindow("tasks")
			},
		},
		paletteItem{
			id:    "cmd-open-automations",
			kind:  "command",
			title: "打开自动化",
			run: func(cs *chatState) {
				cs.openTasksWindow("automations")
			},
		},
		paletteItem{
			id:    "cmd-toggle-terminal",
			kind:  "command",
			title: "切换终端",
			run: func(cs *chatState) {
				toggleTerminal(cs)
			},
		},
	)

	// 总管入口：与侧栏、项目右键菜单共用同一组函数。
	items = append(items, stewardPaletteItems(s)...)

	// 右侧分栏面板（改动、文件管理、网页预览）的开关项由 side_pane.go 的注册表生成。
	items = append(items, sidePanePaletteItems()...)

	s.mu.Lock()
	selectedTID := s.selectedThreadID
	s.mu.Unlock()

	if s.openThreadWindow != nil && selectedTID != "" {
		items = append(items, paletteItem{
			id:    "cmd-open-thread-window",
			kind:  "command",
			title: "在新窗口打开当前会话",
			run: func(cs *chatState) {
				cs.mu.Lock()
				curTID := cs.selectedThreadID
				cs.mu.Unlock()
				if cs.openThreadWindow != nil && curTID != "" {
					cs.openThreadWindow(curTID)
				}
			},
		})
	}

	// 会话转发：选中会话时才出现，与右键菜单、⇧⌘D 共用 forwardSelectedThread。
	if selectedTID != "" {
		items = append(items, paletteItem{
			id:       "cmd-forward-thread",
			kind:     "command",
			title:    "转发到新会话",
			subtitle: "把当前会话的上下文带入新会话",
			keywords: "转发 继续 新会话 forward continue",
			run: func(cs *chatState) {
				cs.forwardSelectedThread()
			},
		})
	}

	// 2. 全部会话与 3. 全部项目，在 s.mu 保护下读取数据
	s.mu.Lock()
	projects := make([]Project, len(s.projects))
	copy(projects, s.projects)

	threads := make([]Thread, len(s.threads))
	copy(threads, s.threads)

	projNames := make(map[string]string, len(projects))
	for _, p := range projects {
		projNames[p.ID] = p.Name
	}
	s.mu.Unlock()

	for _, th := range threads {
		tID := th.ID
		title := th.Title
		if title == "" {
			title = "未命名会话"
		}
		subtitle := projNames[th.ProjectID]
		items = append(items, paletteItem{
			id:       "thread-" + tID,
			kind:     "thread",
			title:    title,
			subtitle: subtitle,
			run: func(cs *chatState) {
				cs.closeWorkspace()
				cs.selectThread(tID)
			},
		})
	}

	for _, p := range projects {
		pID := p.ID
		items = append(items, paletteItem{
			id:       "project-" + pID,
			kind:     "project",
			title:    p.Name,
			subtitle: p.Path,
			run: func(cs *chatState) {
				cs.mu.Lock()
				projThreads := cs.threadsByProj[pID]
				var firstThreadID string
				if len(projThreads) > 0 {
					firstThreadID = projThreads[0].ID
				}
				cs.mu.Unlock()

				if firstThreadID != "" {
					cs.closeWorkspace()
					cs.selectThread(firstThreadID)
				} else {
					cs.closeWorkspace()
					cs.createThread(pID)
				}
			},
		})
	}

	return items
}

// stewardPaletteItems 生成总管会话入口候选条目。
func stewardPaletteItems(s *chatState) []paletteItem {
	if s == nil {
		return nil
	}
	if _, ok := s.stewardClient(); !ok {
		return nil
	}

	nickname, _ := s.stewardIdentity()
	name := stewardName(nickname)
	return []paletteItem{{
		id:       "cmd-open-steward",
		kind:     "command",
		title:    "打开" + name,
		keywords: "总管 默默 steward 会话",
		run: func(cs *chatState) {
			cs.openStewardThread()
		},
	}}
}

func matchSubsequence(target []rune, pattern []rune) bool {
	if len(pattern) == 0 {
		return true
	}
	pi := 0
	for _, r := range target {
		if r == pattern[pi] {
			pi++
			if pi == len(pattern) {
				return true
			}
		}
	}
	return false
}

// filterPaletteItems 根据输入过滤命令面板候选条目。
// 前缀匹配得 3 分，子串匹配得 2 分，字符子序列（模糊）匹配得 1 分，无匹配丢弃。
// 稳定按分数降序排序，相同分数保留原有相对顺序，最多返回 50 条。
func filterPaletteItems(items []paletteItem, query string) []paletteItem {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		if len(items) > 50 {
			return items[:50]
		}
		return items
	}

	type scoredItem struct {
		item  paletteItem
		score int
	}

	patternRunes := []rune(q)
	var matched []scoredItem

	for _, it := range items {
		target := strings.ToLower(it.title + " " + it.subtitle + " " + it.keywords)
		score := 0
		if strings.HasPrefix(target, q) {
			score = 3
		} else if strings.Contains(target, q) {
			score = 2
		} else if matchSubsequence([]rune(target), patternRunes) {
			score = 1
		}

		if score > 0 {
			matched = append(matched, scoredItem{item: it, score: score})
		}
	}

	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].score > matched[j].score
	})

	if len(matched) > 50 {
		matched = matched[:50]
	}

	result := make([]paletteItem, len(matched))
	for i, sm := range matched {
		result[i] = sm.item
	}
	return result
}

// paletteVisibleItems 把过滤后的候选条目与消息正文搜索结果合成一份列表。
// 渲染与键盘上下键/Enter 都基于这份列表，保证选中下标与画面一致。
func paletteVisibleItems(s *chatState, p *paletteState, query string) []paletteItem {
	items := filterPaletteItems(buildPaletteItems(s), query)
	msgItems := paletteMessageItems(p, query)
	if len(msgItems) == 0 {
		return items
	}
	merged := make([]paletteItem, 0, len(items)+len(msgItems))
	merged = append(merged, items...)
	merged = append(merged, msgItems...)
	return merged
}

// move 移动选中的条目索引，并在 [0, n-1] 范围内 clamp。
func (p *paletteState) move(delta, n int) {
	p.scrollSelection = true
	if n <= 0 {
		p.selected = 0
		return
	}
	p.selected += delta
	if p.selected < 0 {
		p.selected = 0
	} else if p.selected >= n {
		p.selected = n - 1
	}
}

// openPalette 打开命令面板，并重置查询与选中项。
func openPalette(s *chatState) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	p.open = true
	p.query = ""
	p.prevQuery = ""
	p.selected = 0
	p.scrollSelection = true
	clearMessageSearch(p)
	if s != nil {
		s.invalidate()
	}
}

// closePalette 关闭命令面板。
func closePalette(s *chatState) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	p.open = false
	clearMessageSearch(p)
	if s != nil {
		s.invalidate()
	}
}

// togglePalette 切换命令面板的打开/关闭状态。
func togglePalette(s *chatState) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	if p.open {
		closePalette(s)
	} else {
		openPalette(s)
	}
}

// executePaletteSelection 执行当前选中的条目并关闭面板。
func executePaletteSelection(s *chatState, items []paletteItem) {
	p := getPaletteState(s)
	if p == nil {
		return
	}
	var target *paletteItem
	if p.selected >= 0 && p.selected < len(items) {
		target = &items[p.selected]
	}
	closePalette(s)
	if target != nil && target.run != nil {
		target.run(s)
	}
}

// renderCommandPalette 渲染命令面板模态框。
func renderCommandPalette(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	p := getPaletteState(s)
	if p == nil || !p.open {
		return
	}

	filtered := paletteVisibleItems(s, p, p.query)

	if len(filtered) > 0 {
		if p.selected < 0 {
			p.selected = 0
		} else if p.selected >= len(filtered) {
			p.selected = len(filtered) - 1
		}
	} else {
		p.selected = 0
	}

	width, _ := c.Size()
	// 这里用 DialogBase 而不是 Modal，因为 Modal 给面板自带背景与内边距，卡片外会多出一圈黑框，
	// 面板只负责限制大小，外观全部由卡片自己负责。
	ui.DialogBase(c, &p.open, func(backdrop, panel ui.Element) {
		backdrop.Background(ui.Color{}.Alpha(ScrimOpacity))
		winW, winH := c.Size()
		panel.MaxWidth(winW - paletteWindowInset).MaxHeight(winH - paletteWindowInset)
		if mods, key, ok := shortcutBindingFor("command-palette"); ok && backdrop.OverlayShortcut(mods, key) {
			closePalette(s)
		}
		ui.Column(c.Key("command-palette-card")).
			Width(paletteWidth).
			MaxWidth(width - paletteCardInset).
			Background(ThemeSurface(c)).
			BorderColor(ThemeBorder(c)).
			BorderWidth(Hairline).
			Radius(RadiusLG).
			Padding(Space3).
			Gap(Space3).
			Children(func() {
				// 顶部搜索输入框
				ui.Row(c.Key("palette-input-wrapper")).
					FillWidth().
					Radius(RadiusMD).
					Background(ThemeHover(c).Alpha(0.25)).
					BorderColor(ThemeBorder(c)).
					BorderWidth(Hairline).
					Padding(Space2, Space3).
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Icon(c, IconSearch()).
							Size(IconSM, IconSM).
							TextColor(ThemeSecondaryText(c))

						in := ui.TextInput(c.Key("palette-query-input"), &p.query).
							Placeholder("搜索会话、项目、命令或消息").
							FontSize(TypeFootnote).
							TextColor(ThemePrimaryText(c)).
							Background(ui.Transparent).
							BorderWidth(0).
							Grow(1).
							AutoFocus()

						if in.Changed() || p.query != p.prevQuery {
							p.prevQuery = p.query
							p.selected = 0
							p.scrollSelection = true
							scheduleMessageSearch(s, p, p.query)
						}

						in.OnTextKey(func(k ui.TextKey) bool {
							if k.Text() != p.prevQuery {
								p.prevQuery = k.Text()
								p.selected = 0
								scheduleMessageSearch(s, p, k.Text())
							}
							current := paletteVisibleItems(s, p, k.Text())
							if k.Key == ui.KeyUp {
								p.move(-1, len(current))
								s.invalidate()
								return true
							}
							if k.Key == ui.KeyDown {
								p.move(1, len(current))
								s.invalidate()
								return true
							}
							if k.Key == ui.KeyEnter {
								executePaletteSelection(s, current)
								return true
							}
							if k.Key == ui.KeyEscape {
								closePalette(s)
								return true
							}
							return false
						})
					})

				// 候选项列表或空状态
				if len(filtered) == 0 {
					ui.Row(c.Key("palette-empty-row")).
						FillWidth().
						Padding(Space7, 0).
						Justify(ui.Center).
						Children(func() {
							ui.Text(c, "没有匹配项").
								FontSize(TypeFootnote).
								TextColor(ThemeSecondaryText(c))
						})
				} else {
					ui.Scroll(c.Key("palette-scroll")).
						MaxHeight(paletteListMaxHeight).
						FillWidth().
						Children(func() {
							ui.Column(c.Key("palette-items-list")).
								FillWidth().
								Gap(Space0).
								Children(func() {
									for i, it := range filtered {
										idx := i
										item := it
										isSelected := (p.selected == idx)

										row := ui.ButtonBase(c.Key("palette-item-"+item.id)).
											Label(item.title).
											FillWidth().
											Padding(Space3, Space3).
											Radius(RadiusSM).
											AlignItems(ui.Center).
											Justify(ui.Start).
											Gap(Space3).
											Cursor(ui.CursorPointer)

										if isSelected {
											row.Background(ThemeSelected(c))
											if p.scrollSelection {
												row.ScrollIntoView()
											}
										} else if row.Hovered() {
											row.Background(ThemeHover(c))
										}

										if row.Clicked() {
											p.selected = idx
											executePaletteSelection(s, filtered)
										}

										kindLabel := "命令"
										switch item.kind {
										case "thread":
											kindLabel = "会话"
										case "project":
											kindLabel = "项目"
										case "message":
											kindLabel = "消息"
										}

										row.Children(func() {
											ui.Box(c.Key("palette-kind-tag-"+item.id)).
												Padding(Space0, Space2).
												Radius(RadiusXS).
												Background(ThemeHover(c)).
												Children(func() {
													ui.Text(c, kindLabel).
														FontSize(TypeCaption).
														TextColor(ThemeSecondaryText(c))
												})

											ui.Text(c, item.title).
												SingleLine().
												FontSize(TypeFootnote).
												TextColor(ThemePrimaryText(c))

											if item.subtitle != "" {
												ui.Text(c, item.subtitle).
													SingleLine().
													FontSize(TypeCaption).
													TextColor(ThemeSecondaryText(c))
											}
										})
									}
								})
						})
				}

				// 面板页脚：左侧按键提示，右侧当前可见结果数。
				ui.Column(c.Key("palette-footer")).FillWidth().Gap(0).Children(func() {
					ui.Box(c.Key("palette-footer-divider")).FillWidth().Height(Hairline).Background(ThemeBorder(c))
					ui.Row(c.Key("palette-footer-row")).
						FillWidth().
						Height(ControlMD).
						AlignItems(ui.Center).
						Gap(Space4).
						Children(func() {
							renderPaletteFooterHint(c, "palette-footer-nav", "↑↓", "选择")
							renderPaletteFooterHint(c, "palette-footer-enter", "↵", "执行")
							renderPaletteFooterHint(c, "palette-footer-esc", "esc", "关闭")
							ui.Spacer(c)
							ui.Text(c, fmt.Sprintf("%d 项结果", len(filtered))).
								FontSize(TypeCaption).
								TextColor(ThemeSecondaryText(c))
						})
				})
			})
	})
	p.scrollSelection = false
}

// renderPaletteFooterHint 渲染页脚的一组键帽加说明文字，整组带无障碍标签便于测试与读屏。
func renderPaletteFooterHint(c *ui.Context, key, kbd, label string) {
	ui.Row(c.Key(key)).Gap(Space1).AlignItems(ui.Center).Label(kbd + " " + label).Children(func() {
		Kbd(c, key+"-kbd", kbd)
		ui.Text(c, label).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
	})
}
