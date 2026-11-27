package mcodeui

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
)

// chatsClient 是「对话」内置项目的可选客户端能力，只返回内置对话项目 ID。
type chatsClient interface {
	ChatsProjectID() string
}

// chatsProjectID 返回内置「对话」项目的 ID，客户端不支持时返回空串。
// 侧栏每帧都会调用它，因此只做类型断言加一次接口调用，不碰 s.mu，不做任何 IO。
func (s *chatState) chatsProjectID() string {
	if s == nil || s.client == nil {
		return ""
	}
	c, ok := s.client.(chatsClient)
	if !ok {
		return ""
	}
	return c.ChatsProjectID()
}

// sidebarThreadFilter 是侧栏会话过滤条件；入口在「对话」段标题行，作用于整个侧栏会话列表。
type sidebarThreadFilter int

const (
	sidebarFilterAll sidebarThreadFilter = iota
	sidebarFilterRunning
	sidebarFilterWaiting
	sidebarFilterActive24h
	sidebarFilterActive7d
	sidebarFilterActive30d
)

// sidebarThreadFilterOptions 是下拉框的展示顺序；label 同时用于按钮可访问名称与菜单项标题。
var sidebarThreadFilterOptions = []struct {
	key   string
	label string
	value sidebarThreadFilter
}{
	{"all", "全部", sidebarFilterAll},
	{"running", "运行中", sidebarFilterRunning},
	{"waiting", "等待中", sidebarFilterWaiting},
	{"24h", "最近 24 小时活跃", sidebarFilterActive24h},
	{"7d", "最近 7 天活跃", sidebarFilterActive7d},
	{"30d", "最近 30 天活跃", sidebarFilterActive30d},
}

// sidebarFilterActiveWindow 返回活跃时间过滤窗口；非时间类过滤返回 0。
func sidebarFilterActiveWindow(filter sidebarThreadFilter) time.Duration {
	switch filter {
	case sidebarFilterActive24h:
		return 24 * time.Hour
	case sidebarFilterActive7d:
		return 7 * 24 * time.Hour
	case sidebarFilterActive30d:
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

// sidebarThreadWaiting 判断会话是否在等用户处理：待审批/提问，或这一轮已经结束但用户还没查看结果
// （侧栏标题高亮、完成角标或未读点）。仍在运行的会话只有待审批算等待，避免旧的完成标记
// 把新开的一轮误算进等待中。
func (s *chatState) sidebarThreadWaiting(th Thread, children []Thread, busy func(Thread) bool) bool {
	if th.HasPendingRequest {
		return true
	}
	for _, child := range children {
		if child.HasPendingRequest {
			return true
		}
	}
	if busy != nil && busy(th) {
		return false
	}
	if s.sidebarThreadHasUnseenResult(th) {
		return true
	}
	for _, child := range children {
		if child.IsRunning || child.SwarmBusy {
			continue
		}
		if s.sidebarThreadHasUnseenResult(child) {
			return true
		}
	}
	return false
}

// sidebarThreadHasUnseenResult 判断空闲会话是否有用户还没查看的结果。
// turnFinished 在打开会话时清除；HasUnread 覆盖应用重启后仍存在的未读状态。
func (s *chatState) sidebarThreadHasUnseenResult(th Thread) bool {
	if s.threadTurnFinished(th.ID) != "" {
		return true
	}
	return s.hasThreadUnread(th)
}

// sidebarThreadRunning 判断会话或其任一子代理是否仍在运行；等待中的会话归入等待中，与
// 侧栏状态分组保持一致（等待优先于运行）。
func (s *chatState) sidebarThreadRunning(th Thread, children []Thread, busy func(Thread) bool) bool {
	if s.sidebarThreadWaiting(th, children, busy) {
		return false
	}
	if busy != nil && busy(th) {
		return true
	}
	for _, child := range children {
		if child.IsRunning || child.SwarmBusy || (busy != nil && busy(child)) {
			return true
		}
	}
	return false
}

// sidebarThreadMatchesFilter 判断一个顶层会话是否满足过滤条件。
func (s *chatState) sidebarThreadMatchesFilter(th Thread, children []Thread, filter sidebarThreadFilter, now time.Time, busy func(Thread) bool) bool {
	switch filter {
	case sidebarFilterRunning:
		return s.sidebarThreadRunning(th, children, busy)
	case sidebarFilterWaiting:
		return s.sidebarThreadWaiting(th, children, busy)
	default:
		window := sidebarFilterActiveWindow(filter)
		if window == 0 {
			return true
		}
		at := threadActivityTime(th)
		return !at.IsZero() && !at.Before(now.Add(-window))
	}
}

// filterSidebarThreads 按过滤条件保留顶层会话及其全部子代理，保持入参顺序。
// 侧栏「对话」段、普通项目与动态视图共用这份逻辑；子代理不单独作为匹配单位：
// 父会话命中时整组保留，避免子代理被提升成顶层会话。
func (s *chatState) filterSidebarThreads(threads []Thread, filter sidebarThreadFilter, now time.Time, busy func(Thread) bool) []Thread {
	if filter == sidebarFilterAll || len(threads) == 0 {
		return threads
	}
	topThs, beesByParent := GroupThreadsByParent(threads)
	keep := make(map[string]bool, len(threads))
	for _, th := range topThs {
		if s.sidebarThreadMatchesFilter(th, beesByParent[th.ID], filter, now, busy) {
			keep[th.ID] = true
			for _, bee := range beesByParent[th.ID] {
				keep[bee.ID] = true
			}
		}
	}
	filtered := make([]Thread, 0, len(threads))
	for _, th := range threads {
		if keep[th.ID] {
			filtered = append(filtered, th)
		}
	}
	return filtered
}

// renderSidebarChatsSection 渲染侧栏的「临时会话」一级行：气泡图标、标题、计数与常驻「+」按钮，
// 下方按最近活动平铺该项目的顶层会话；threadsFilter 非 nil 时只列传入的会话（搜索态过滤结果），
// 为 nil 时直接复用侧栏快照里算好的对话会话；focusZoneActive 为真时剔除已进入顶部聚焦区的会话。
// 非「全部」过滤且无命中会话时整段不渲染；聚焦区开启且本段会话全进聚焦区时仍保留标题行与分隔线。
// chatsProjectID 为空时整段不渲染。
func renderSidebarChatsSection(
	c *ui.Context,
	s *chatState,
	snap *sidebarSnapshot,
	selectedID string,
	threadsFilter []Thread,
	focusZoneActive bool,
	stateGroups map[string]sidebarStateGroup,
	resolvedTop []Thread,
	resolvedBees map[string][]Thread,
) {
	chatsProjID := s.chatsProjectID()
	if chatsProjID == "" {
		return
	}

	providers := snap.providers
	var topThs []Thread
	var beesByParent map[string][]Thread
	if threadsFilter != nil {
		rawThreads := withoutProvisional(threadsFilter)
		rawThreads = s.filterSidebarThreads(rawThreads, s.sidebarFilter, time.Now(), s.threadStillBusy)
		topThs, beesByParent = GroupThreadsByParent(rawThreads)
		topThs = SortThreads(topThs)
	} else {
		if resolvedTop != nil {
			topThs = resolvedTop
		} else {
			topThs = snap.chatsTopThreads
		}
		if resolvedBees != nil {
			beesByParent = resolvedBees
		} else {
			beesByParent = snap.chatsBeesByParent
		}
		if focusZoneActive && len(stateGroups) > 0 {
			filtered := make([]Thread, 0, len(topThs))
			for _, th := range topThs {
				if stateGroups[th.ID] == sidebarStateGroupNone {
					filtered = append(filtered, th)
				}
			}
			topThs = filtered
		}
	}

	filter := snap.key.filter
	isSearching := threadsFilter != nil
	if (isSearching || filter != sidebarFilterAll) && len(topThs) == 0 {
		return
	}

	beesByParent, allBeesByParent, hiddenBeeCountByParent, showAllBeesByParent := filterSidebarBeesForAutoHide(s, beesByParent, selectedID)

	m := currentSidebarMetrics(c, s)
	canCollapse := !isSearching && filter == sidebarFilterAll
	isCollapsed := snap.collapsed[chatsProjID] && canCollapse

	// 一级标题行：气泡图标 + 「临时会话」（与项目标题行同字号字重），右侧是会话计数与常驻「+」图标按钮，不挂载项目右键菜单。
	headerRow := ui.Row(c.Key("chats-section-header")).
		Height(m.rowHeight).
		Padding(0, m.padX).
		Margin(1, ListInset, 1, ListInset).
		Radius(RowCornerRadius).
		AlignItems(ui.Center)

	if headerRow.Hovered() {
		headerRow.Background(ThemeOverlayHover(c))
	}

	headerRow.Children(func() {
		toggleLabel := "展开临时会话"
		if !isCollapsed {
			toggleLabel = "收起临时会话"
		}
		titleArea := ui.Row(c.Key("chats-section-toggle")).
			AlignItems(ui.Center).
			Grow(1).
			Gap(Space3).
			Radius(RadiusXS).
			Label(toggleLabel).
			Focusable().
			KeepFocus()
		focusRing(c, titleArea)

		if titleArea.Clicked() && canCollapse {
			s.mu.Lock()
			if s.collapsedProj == nil {
				s.collapsedProj = make(map[string]bool)
			}
			s.collapsedProj[chatsProjID] = !isCollapsed
			s.mu.Unlock()
			s.invalidate()
		}

		titleArea.Children(func() {
			ui.Icon(c, IconMessageCircle()).
				Size(12, 12).
				TextColor(ThemeSecondaryText(c))

			ui.Text(c, "临时会话").
				FontSize(sidebarFont(FontSizeThreadTitle)).
				FontWeight(500).
				TextColor(ThemePrimaryText(c)).
				Grow(1).
				SingleLine()
		})

		if len(topThs) > 0 {
			ui.Text(c, "("+strconv.Itoa(len(topThs))+")").
				FontSize(sidebarFont(TypeCaption)).
				TextColor(ThemeTertiaryText(c)).
				Margin(0, Space1, 0, 0)
		}

		IconButton(c.Key("chats-add-thread"), IconButtonProps{
			Icon:    IconPlus(),
			Size:    ControlMD,
			Variant: ButtonGhost,
			Label:   "新建对话",
			OnClick: func() {
				s.mu.Lock()
				if s.newThreadProvider == "" {
					s.newThreadProvider = defaultNewThreadProvider(s.providers, newThreadProviderContext{threads: s.threads, stewardThreadID: s.stewardThreadID()})
				}
				s.mu.Unlock()
				s.closeWorkspace()
				s.createThread(chatsProjID)
			},
		})
	})

	motionUnfoldBody(c, "chats-section-body", !isCollapsed, func() {
		for _, th := range topThs {
			renderSidebarThreadRow(c, s, sidebarThreadRowOptions{
				projectID:      chatsProjID,
				thread:         th,
				bees:           beesByParent[th.ID],
				allBees:        allBeesByParent[th.ID],
				hiddenBeeCount: hiddenBeeCountByParent[th.ID],
				showAllBees:    showAllBeesByParent[th.ID],
				dormant:        th.Dormant,
				selected:       th.ID == selectedID,
				rowHeight:      m.rowHeight,
				padY:           m.padY,
				padX:           m.padX,
				indent:         m.folderIndent,
				compact:        m.compact,
				providers:      providers,
				latchHover:     focusZoneActive,
			})
		}
	})

	dividerPadY := Space2
	if m.compact {
		dividerPadY = Space1
	}
	ui.Box(c.Key("chats-section-divider")).
		FillWidth().
		Height(Hairline).
		Background(ThemeHairline(c)).
		Margin(dividerPadY, ListInset, dividerPadY, ListInset)
}

// renderSidebarFilterControl 渲染过滤按钮与下拉框，内嵌置于侧栏搜索框右侧，
// 确保任何视图下过滤状态都可见、可清除。
func renderSidebarFilterControl(c *ui.Context, s *chatState) {
	trigger := IconButton(c.Key("chats-filter"), IconButtonProps{
		Icon:    IconFilter(),
		Size:    ControlXS,
		Variant: ButtonGhost,
		Label:   "过滤对话",
		Tooltip: "过滤对话 · " + sidebarFilterLabel(s.sidebarFilter),
		Active:  s.sidebarFilter != sidebarFilterAll,
		OnClick: func() { s.sidebarFilterOpen = true },
	})
	appPopover(c.Key("chats-filter-popover"), trigger, &s.sidebarFilterOpen, nil, func() {
		popoverCard(c, ui.Column(c.Key("chats-filter-menu"))).
			Padding(Space1).
			Gap(Space0).
			MinWidth(200).
			FocusGroup(ui.Vertical).
			Children(func() {
				current := s.sidebarFilter
				for _, opt := range sidebarThreadFilterOptions {
					opt := opt
					isCurrent := current == opt.value
					var onClick func()
					if !isCurrent {
						onClick = func() {
							s.sidebarFilter = opt.value
							s.sidebarFilterOpen = false
							s.invalidate()
						}
					}
					menuRow(c, menuRowProps{
						Key:         "chats-filter-" + opt.key,
						Label:       "过滤对话：" + opt.label,
						Title:       opt.label,
						Checked:     isCurrent,
						Highlighted: isCurrent,
						OnClick:     onClick,
					})
				}
			})
	})
}

// sidebarFilterLabel 返回过滤条件的展示文案；未知值按「全部」处理。
func sidebarFilterLabel(filter sidebarThreadFilter) string {
	for _, opt := range sidebarThreadFilterOptions {
		if opt.value == filter {
			return opt.label
		}
	}
	return sidebarThreadFilterOptions[0].label
}
