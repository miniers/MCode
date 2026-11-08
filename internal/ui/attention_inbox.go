package mcodeui

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// attentionInboxLimit 是收件箱列表一次加载的最大条数；取满时浮层底部提示只显示最近若干条。
const attentionInboxLimit = 100

const (
	// attentionInboxMinWidth 是收件箱浮层卡片的最小宽度。
	attentionInboxMinWidth float32 = 360
	// attentionInboxMaxWidth 是收件箱浮层卡片的最大宽度。
	attentionInboxMaxWidth float32 = 400
)

// attentionInboxRefreshInterval 是订阅回调触发的刷新节流间隔：事件密集时每秒最多重新拉取一次。
const attentionInboxRefreshInterval = time.Second

// attentionInboxPopoverMaxHeight 是浮层内列表的最大高度，超出后在浮层内滚动。
const attentionInboxPopoverMaxHeight float32 = 360

// attentionInboxEmptyText 是列表为空时的文案：陈述各会话正在后台推进，
// 再说明什么情况下这里会出现内容，比「暂无」更能解释收件箱的作用。
const attentionInboxEmptyText = "各会话在后台推进。需要你批准或回答的事会出现在这里。"

// attentionInboxKindGroup 是收件箱的一个分组：切片顺序就是浮层里的展示顺序。
type attentionInboxKindGroup struct {
	Kind  string // 对应 AttentionItem.Kind
	Title string // 分组标题
}

// attentionInboxGroups 是收件箱的分组顺序与标题：会话相关分组在前，看板、自动化、bee 在后。
var attentionInboxGroups = []attentionInboxKindGroup{
	{Kind: "thread_stalled", Title: "会话卡住"},
	{Kind: "thread_failed", Title: "会话出错"},
	{Kind: "thread_quota", Title: "额度用尽"},
	{Kind: "thread_approval", Title: "等你审批"},
	{Kind: "kanban_failed", Title: "看板失败"},
	{Kind: "kanban_needs_approval", Title: "看板待审批"},
	{Kind: "kanban_review", Title: "看板待验收"},
	{Kind: "kanban_plan_failed", Title: "规划失败"},
	{Kind: "automation_failed", Title: "自动化失败"},
	{Kind: "automation_finished", Title: "自动化完成"},
	{Kind: "bee_failed", Title: "子代理失败"},
	{Kind: "thread_no_progress", Title: "任务无进展"},
}

// attentionInboxOtherKind 是未登记 Kind 的归组键，保证后端新增类型的事件不会从列表里消失。
const attentionInboxOtherKind = "__other__"

// attentionInboxKnownKinds 是已登记分组的 Kind 集合，供 groupAttentionItems 判断是否需要归入「其他」。
var attentionInboxKnownKinds = func() map[string]bool {
	m := make(map[string]bool, len(attentionInboxGroups))
	for _, g := range attentionInboxGroups {
		m[g.Kind] = true
	}
	return m
}()

// attentionInboxState 维护侧栏「待处理」入口的数量、列表、浮层开合与订阅，按 chatState 附加。
// open 只由 UI 线程读写（appPopover 关闭时会写回）；其余字段由 mu 保护，后台 goroutine 也会写。
type attentionInboxState struct {
	open bool

	mu        sync.Mutex
	count     int
	items     []AttentionItem
	loaded    bool
	loading   bool
	fetchedAt time.Time
	pending   bool
	retry     *time.Timer
	resolving map[string]bool
	// err 是「标记已处理」失败后写在浮层顶部的那句话；下一次成功标记或成功拉取列表时清掉。
	err       string
	watching  bool
	watchFor  Client
	watchStop func()
}

// attentionClient 返回实现了收件箱接口的客户端；旧客户端或测试替身返回 false。
func (s *chatState) attentionClient() (AttentionClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	c, ok := s.client.(AttentionClient)
	return c, ok
}

// getAttentionInboxState 返回 chatState 对应的收件箱状态，首次访问时创建。
func getAttentionInboxState(s *chatState) *attentionInboxState {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attentionInbox == nil {
		s.attentionInbox = &attentionInboxState{}
	}
	return s.attentionInbox
}

// toggleAttentionInbox 切换侧栏「待处理」浮层的开合；快捷键「打开待处理收件箱」
// 与点击入口行走同一份 open 状态。客户端未实现 AttentionClient 时入口本来就不渲染，这里直接忽略。
func (s *chatState) toggleAttentionInbox() {
	if s == nil {
		return
	}
	if _, ok := s.attentionClient(); !ok {
		return
	}
	st := getAttentionInboxState(s)
	if st == nil {
		return
	}
	st.open = !st.open
	s.invalidate()
}

// sameAttentionClient 判断两个 Client 是否为同一实例：动态类型不可比较时按同一实例处理，
// 避免界面每帧重复注销并重建订阅。
func sameAttentionClient(a, b Client) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	ta, tb := reflect.TypeOf(a), reflect.TypeOf(b)
	if ta != tb {
		return false
	}
	if !ta.Comparable() {
		return true
	}
	return a == b
}

// refreshAttentionInbox 是渲染路径的刷新入口：只在需要时拉取，不会因为每帧调用而排队补拉。
func (s *chatState) refreshAttentionInbox() {
	s.loadAttentionInbox(false)
}

// refreshAttentionInboxFromWatch 是订阅回调的刷新入口：事件在加载途中到达时记一次待刷新，
// 避免节流或加载把变更丢掉。
func (s *chatState) refreshAttentionInboxFromWatch() {
	s.loadAttentionInbox(true)
}

// loadAttentionInbox 异步拉取未处理数量与列表，并通知界面重绘。
// 刷新按 attentionInboxRefreshInterval 节流：节流窗口内到达的请求记为待刷新，
// 窗口结束后由定时器补拉一次；queued 为 false 的渲染路径调用不会排队。
func (s *chatState) loadAttentionInbox(queued bool) {
	client, ok := s.attentionClient()
	if !ok {
		return
	}
	st := getAttentionInboxState(s)
	if st == nil {
		return
	}

	st.mu.Lock()
	if st.loading {
		if queued {
			st.pending = true
		}
		st.mu.Unlock()
		return
	}
	if !st.fetchedAt.IsZero() {
		if wait := attentionInboxRefreshInterval - time.Since(st.fetchedAt); wait > 0 {
			if queued {
				st.pending = true
				if st.retry == nil {
					st.retry = time.AfterFunc(wait, func() {
						st.mu.Lock()
						st.retry = nil
						pending := st.pending
						st.pending = false
						st.mu.Unlock()
						if pending {
							s.loadAttentionInbox(true)
						}
					})
				}
			}
			st.mu.Unlock()
			return
		}
	}
	st.pending = false
	st.loading = true
	st.mu.Unlock()

	go func() {
		ctx := context.Background()
		count, countErr := client.AttentionUnresolvedCount(ctx)
		items, listErr := client.ListAttention(ctx, attentionInboxLimit)

		st.mu.Lock()
		st.loading = false
		st.fetchedAt = time.Now()
		if countErr == nil {
			st.count = count
		}
		if listErr == nil {
			st.items = items
			st.loaded = true
			// 列表已经拿到，上一次标记失败的提示可以收了。
			st.err = ""
		} else {
			// 首次加载失败也要把错误交给浮层：loaded 仍为 false，浮层据此显示
			// 错误行与重试按钮，不能永远停在「加载中…」。
			st.err = "加载收件箱失败：" + listErr.Error()
		}
		pending := st.pending
		st.pending = false
		st.mu.Unlock()

		s.invalidate()
		if pending {
			// 加载期间又来了变更：补拉一次，仍受节流窗口约束。
			s.loadAttentionInbox(true)
		}
	}()
}

// retryAttentionInbox 立刻重新拉取收件箱：跳过刷新节流窗口，供浮层「重试」按钮使用。
func retryAttentionInbox(s *chatState) {
	st := getAttentionInboxState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.fetchedAt = time.Time{}
	st.mu.Unlock()
	s.loadAttentionInbox(true)
}

// ensureAttentionWatch 建立收件箱变更订阅，只建立一次；客户端被替换时注销旧订阅再重建。
// 注销函数由 stopAttentionWatch 在关窗时调用。
func (s *chatState) ensureAttentionWatch() {
	client, ok := s.attentionClient()
	if !ok {
		return
	}
	st := getAttentionInboxState(s)
	if st == nil {
		return
	}

	st.mu.Lock()
	if st.watching && sameAttentionClient(st.watchFor, s.client) {
		st.mu.Unlock()
		return
	}
	old := st.watchStop
	st.watchStop = nil
	st.watching = true
	st.watchFor = s.client
	st.mu.Unlock()

	if old != nil {
		old()
	}

	stop := client.WatchAttention(func() {
		s.refreshAttentionInboxFromWatch()
	})

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		if stop != nil {
			stop()
		}
		return
	}

	st.mu.Lock()
	st.watchStop = stop
	st.mu.Unlock()
}

// stopAttentionWatch 注销收件箱订阅并停掉补拉定时器，关窗时调用。
func stopAttentionWatch(s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	st := s.attentionInbox
	s.mu.Unlock()
	if st == nil {
		return
	}

	st.mu.Lock()
	stop := st.watchStop
	st.watchStop = nil
	st.watchFor = nil
	st.watching = false
	timer := st.retry
	st.retry = nil
	st.pending = false
	st.mu.Unlock()

	if timer != nil {
		timer.Stop()
	}
	if stop != nil {
		stop()
	}
}

// resolveAttentionItem 手动消解一条事件：本地先摘掉该条，后端成功后立即重新拉取数量与列表。
func (s *chatState) resolveAttentionItem(id string) {
	client, ok := s.attentionClient()
	if !ok || id == "" {
		return
	}
	st := getAttentionInboxState(s)
	if st == nil {
		return
	}

	st.mu.Lock()
	if st.resolving == nil {
		st.resolving = make(map[string]bool)
	}
	if st.resolving[id] {
		st.mu.Unlock()
		return
	}
	st.resolving[id] = true
	// 记住被摸掉的条目和它原来排第几：后端失败时要放回原位，而不是随便接到末尾。
	var removed AttentionItem
	removedAt := -1
	kept := st.items[:0]
	for _, item := range st.items {
		if item.ID == id && removedAt < 0 {
			removed = item
			removedAt = len(kept)
			continue
		}
		kept = append(kept, item)
	}
	st.items = kept
	if st.count > 0 {
		st.count--
	}
	st.err = ""
	st.mu.Unlock()
	s.invalidate()

	go func() {
		defer func() {
			st.mu.Lock()
			delete(st.resolving, id)
			st.mu.Unlock()
		}()
		if err := client.ResolveAttention(context.Background(), id); err != nil {
			st.mu.Lock()
			// 失败就放回原处、计数加回，并在浮层顶部说一句：列表无声地少一条比报错更让人糊涂。
			if removedAt >= 0 {
				st.items = attentionInsertItem(st.items, removedAt, removed)
			}
			st.count++
			st.err = "标记没成功，请重试"
			st.mu.Unlock()
			s.invalidate()
			return
		}
		// 清掉刷新时间点，绕过节流立即重新拉取。
		st.mu.Lock()
		st.fetchedAt = time.Time{}
		st.mu.Unlock()
		s.loadAttentionInbox(true)
	}()
}

// attentionInsertItem 把 item 插到 list 的 index 位置：标记失败时用它把条目放回原位。
// index 越界时接在末尾，这比丢掉条目好。
func attentionInsertItem(list []AttentionItem, index int, item AttentionItem) []AttentionItem {
	if index < 0 || index > len(list) {
		index = len(list)
	}
	list = append(list, AttentionItem{})
	copy(list[index+1:], list[index:])
	list[index] = item
	return list
}

// openAttentionItem 按事件所属对象跳转：会话直接切换，看板与自动化打开任务窗口。
// 跳转不自动标记已处理，用户处理完再手动点「标记已处理」。
func (s *chatState) openAttentionItem(item AttentionItem) {
	if st := getAttentionInboxState(s); st != nil {
		st.open = false
	}
	switch item.SubjectType {
	case "kanban_task", "kanban_goal":
		// 与侧栏底栏看板按钮同一个入口。
		s.openTasksWindow("tasks")
	case "automation_run":
		// 任务窗口没有「按 ID 打开某条自动化」的入口：有会话时退化为跳会话，否则打开自动化视图。
		if item.ThreadID != "" {
			s.revealThread(item.ThreadID)
		} else {
			s.openTasksWindow("automations")
		}
	default:
		// thread、bee 以及任何带会话 ID 的事件都跳到对应会话。
		if item.ThreadID != "" {
			s.revealThread(item.ThreadID)
		}
	}
	s.invalidate()
}

// groupAttentionItems 按 Kind 归组，未登记的 Kind 归入 attentionInboxOtherKind。
func groupAttentionItems(items []AttentionItem) map[string][]AttentionItem {
	groups := make(map[string][]AttentionItem, len(attentionInboxGroups))
	for _, item := range items {
		kind := item.Kind
		if !attentionInboxKnownKinds[kind] {
			kind = attentionInboxOtherKind
		}
		groups[kind] = append(groups[kind], item)
	}
	return groups
}

// renderAttentionCountBadge 渲染未处理数量胶囊，样式沿用侧栏未读角标；count <= 0 时不渲染。
// 返回徽标元素，调用方可以再给它挂点击（总管段里的徽标就是弹浮层的按钮）。
func renderAttentionCountBadge(c *ui.Context, key string, count int) ui.Element {
	if count <= 0 {
		return ui.Element{}
	}
	return ui.Row(c.Key(key)).
		Label(fmt.Sprintf("待处理 %d 条", count)).
		Padding(Space0, Space2).
		Radius(RadiusMD).
		AlignItems(ui.Center).
		Background(ThemeAccent(c)).
		Children(func() {
			ui.Text(c, strconv.Itoa(count)).
				FontSize(scaledFont(TypeCaption)).
				FontWeight(600).
				TextColor(ThemeOnAccent(c))
		})
}

// renderAttentionInboxEntry 渲染侧栏「待处理」固定入口行：图标、文字与未处理数量胶囊，
// 点击展开按类型分组的列表浮层。客户端未实现 AttentionClient 时整行不渲染。
func renderAttentionInboxEntry(c *ui.Context, s *chatState) {
	if _, ok := s.attentionClient(); !ok {
		return
	}
	// 首次渲染拉取数量与列表并建立订阅；后续帧由节流挡住重复请求。
	s.refreshAttentionInbox()
	s.ensureAttentionWatch()

	st := getAttentionInboxState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	count := st.count
	st.mu.Unlock()

	row := ui.Row(c.Key("sidebar-attention-inbox")).
		MinHeight(HeightThreadRow).
		Padding(Space1, ListInset, Space1, ListInset).
		Margin(Space0, ListInset, 0, ListInset).
		Gap(Space3).
		AlignItems(ui.Center).
		Radius(RowCornerRadius).
		Label("待处理")

	if st.open {
		row.Background(ThemeSelected(c))
	} else if row.Hovered() {
		row.Background(ThemeHover(c))
	}

	if row.Clicked() {
		st.open = !st.open
		s.invalidate()
	}

	row.Children(func() {
		ui.Icon(c, IconTray()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
		ui.Text(c, "待处理").
			FontSize(scaledFont(TypeBody)).
			TextColor(ThemePrimaryText(c).Alpha(0.92)).
			Grow(1).
			SingleLine()
		renderAttentionCountBadge(c, "sidebar-attention-inbox-count", count)
	})

	appPopover(c.Key("sidebar-attention-inbox-popover"), row, &st.open, func() {
		st.open = false
	}, func() {
		renderAttentionInboxPopover(c, s)
	})
}

// renderAttentionInboxPopover 渲染收件箱浮层：按类型分组列出未处理事件，逐条提供跳转与「标记已处理」。
func renderAttentionInboxPopover(c *ui.Context, s *chatState) {
	st := getAttentionInboxState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	items := make([]AttentionItem, len(st.items))
	copy(items, st.items)
	loaded := st.loaded
	errText := st.err
	st.mu.Unlock()

	groups := groupAttentionItems(items)

	popoverCard(c, ui.Column(c.Key("sidebar-attention-inbox-menu")).
		MinWidth(attentionInboxMinWidth).
		MaxWidth(attentionInboxMaxWidth).
		Padding(Space2).
		Gap(Space0)).
		Children(func() {
			ui.Row(c.Key("sidebar-attention-inbox-header")).
				FillWidth().
				AlignItems(ui.Center).
				Padding(Space0, Space2, Space1, Space2).
				Children(func() {
					ui.Text(c, "待处理").
						FontSize(scaledFont(TypeBody)).
						FontWeight(600).
						TextColor(ThemePrimaryText(c)).
						Grow(1)
					if len(items) > 0 {
						ui.Text(c, fmt.Sprintf("%d 条", len(items))).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c))
					}
					// 总管入口：只用于查看已存在的总管会话，还没有会话时不显示。
					if s.stewardThreadID() != "" {
						nickname, _ := s.stewardIdentity()
						name := stewardName(nickname)
						Button(c.Key("sidebar-attention-inbox-steward"), ButtonOpts{
							Text:    name,
							Label:   "打开与" + name + "的对话",
							Size:    ControlXS,
							Variant: ButtonSecondary,
							OnClick: func() {
								st.open = false
								s.openStewardThread()
							},
						}).Margin(0, Space2, 0, 0)
					}
				})

			if errText != "" {
				ui.Row(c.Key("sidebar-attention-inbox-error")).
					FillWidth().
					AlignItems(ui.Center).
					Padding(Space0, Space2, Space1, Space2).
					Children(func() {
						ui.Text(c, errText).
							FontSize(scaledFont(TypeCaption)).
							TextColor(ThemeDanger(c)).
							Grow(1)
						Button(c.Key("sidebar-attention-inbox-retry"), ButtonOpts{
							Text:    "重试",
							Label:   "重试加载收件箱",
							Size:    ControlXS,
							Variant: ButtonSecondary,
							OnClick: func() {
								retryAttentionInbox(s)
							},
						})
					})
			}

			if !loaded {
				// 首次加载失败时上面已经显示错误行与重试按钮，不能再显示「加载中…」。
				if errText == "" {
					ui.Text(c, "加载中…").
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						Padding(Space2, Space2)
				}
				return
			}
			if len(items) == 0 {
				ui.Text(c, attentionInboxEmptyText).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Padding(Space2, Space2)
				return
			}

			ui.Scroll(c.Key("sidebar-attention-inbox-scroll")).
				MaxHeight(attentionInboxPopoverMaxHeight).
				Children(func() {
					ui.Column(c.Key("sidebar-attention-inbox-list")).
						Gap(Space0).
						Children(func() {
							for _, g := range attentionInboxGroups {
								if list := groups[g.Kind]; len(list) > 0 {
									renderAttentionInboxGroup(c, s, g.Kind, g.Title, list)
								}
							}
							if rest := groups[attentionInboxOtherKind]; len(rest) > 0 {
								renderAttentionInboxGroup(c, s, "other", "其他", rest)
							}
						})
				})

			if len(items) >= attentionInboxLimit {
				ui.Text(c, fmt.Sprintf("仅显示最近 %d 条", attentionInboxLimit)).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Padding(Space1, Space2, Space0, Space2)
			}
		})
}

// renderAttentionInboxGroup 渲染一组事件：标题行带该组条数，下面是逐条事件。
func renderAttentionInboxGroup(c *ui.Context, s *chatState, key, title string, items []AttentionItem) {
	ui.Row(c.Key("attention-group-"+key)).
		FillWidth().
		AlignItems(ui.Center).
		Padding(Space1, Space2, Space0, Space2).
		Children(func() {
			ui.Text(c, title).
				FontSize(scaledFont(TypeCaption)).
				FontWeight(600).
				TextColor(ThemeSecondaryText(c)).
				Grow(1)
			ui.Text(c, strconv.Itoa(len(items))).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
		})
	for _, item := range items {
		renderAttentionInboxItem(c, s, item)
	}
}

// renderAttentionInboxItem 渲染一条事件：标题、单行详情与相对时间，右侧是「标记已处理」。
// 点击行本身按所属对象跳转，不自动标记已处理。
func renderAttentionInboxItem(c *ui.Context, s *chatState, item AttentionItem) {
	title := item.Title
	if title == "" {
		title = "未命名事项"
	}

	row := ui.Row(c.Key("attention-item-"+item.ID)).
		FillWidth().
		Padding(Space1, Space2).
		Radius(RadiusSM).
		Gap(Space3).
		AlignItems(ui.Center).
		Label("待处理事项 " + title)
	if row.Hovered() {
		row.Background(ThemeHover(c))
	}
	if row.Clicked() {
		s.openAttentionItem(item)
	}

	row.Children(func() {
		ui.Column(c.Key("attention-item-text-" + item.ID)).
			Gap(Space0).
			Grow(1).
			Children(func() {
				ui.Text(c, title).
					FontSize(scaledFont(TypeFootnote)).
					FontWeight(500).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
				ui.Row(c.Key("attention-item-meta-" + item.ID)).
					FillWidth().
					Gap(Space2).
					AlignItems(ui.Center).
					Children(func() {
						if item.Detail != "" {
							ui.Text(c, item.Detail).
								FontSize(scaledFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c)).
								Grow(1).
								SingleLine()
						}
						if rel := formatRelativeTime(item.CreatedAt); rel != "" {
							ui.Text(c, rel).
								FontSize(scaledFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c))
						}
					})
			})

		id := item.ID
		Button(c.Key("attention-resolve-"+id), ButtonOpts{
			Text:     "标记已处理",
			Label:    "标记已处理 " + title,
			Size:     ControlXS,
			Variant:  ButtonSecondary,
			OnClick:  func() { s.resolveAttentionItem(id) },
			Disabled: false,
		})
	})
}
