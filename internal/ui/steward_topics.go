package mcodeui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 总管话题的界面状态：话题列表、历史折叠、卡片折叠、按钮进行中与两个弹窗的草稿。
// 状态按 chatState 懒创建（同 stewardOpenStates），避免往 model.go 里加字段。
const (
	stewardTopicsRefreshInterval      = 2 * time.Second
	stewardReviewTasksRefreshInterval = 5 * time.Second
)

// stewardTopicNoteDuration 是卡片上「已交给…」「已结项」这类成功提示的停留时间：
// 看完这一眼就够，留着反而会让人以为是当前状态。
const stewardTopicNoteDuration = 5 * time.Second

// stewardTopicRowHeight 是侧栏话题行的行高：上下内边距各 3，标题行（13 号字约 17 高）、
// 行距 1、状态小字（11 号字约 14 高），合计约 38；行内字号随「文字大小」缩放，行高按同一比例放大。
const stewardTopicRowHeight float32 = 38

// stewardTopicsScrollRows 是侧栏话题区最多露出的行数：多出来的在区内滚动，
// 免得话题把下面的项目列表挤出侧栏。
const stewardTopicsScrollRows = 5

// stewardTopicsScrollMaxHeight 是话题滚动区的最大高度（约 5 行话题）；话题不足 5 行时按内容高度。
func stewardTopicsScrollMaxHeight() float32 {
	return stewardTopicRowHeight * textScaleFactor() * stewardTopicsScrollRows
}

type stewardTopicsState struct {
	mu sync.Mutex

	topics    []StewardTopic
	loaded    bool
	loadErr   string
	loading   bool
	fetchedAt time.Time
	pending   bool
	retry     *time.Timer

	watching  bool
	watchFor  Client
	watchStop func()

	historyOpen bool
	collapsed   map[string]bool
	cardErrs    map[string]string
	cardNotes   map[string]string
	busy        map[string]bool

	reviewTasks   map[string][]KanbanTask
	reviewAt      map[string]time.Time
	reviewLoading map[string]bool

	// pendingOpen 存「刚建好的话题会话」，等下一帧在界面线程里跳过去。
	pendingOpen string

	startOpen        bool
	startProjectID   string
	startProjectName string
	startText        string
	startBusy        bool
	startErr         string

	rejectOpen     bool
	rejectThreadID string
	rejectTaskID   string
	rejectTitle    string
	rejectRevision int
	rejectText     string
	rejectBusy     bool
	rejectErr      string

	// 删除话题弹窗：删除话题会连带移除会话、话题记录与账本条目，必须二次确认。
	deleteOpen     bool
	deleteThreadID string
	deleteTitle    string
	deleteBusy     bool
	deleteErr      string

	// 「按推荐答案开工」确认弹窗：任务书还有未定问题时，点主按钮先确认一句再走原来的开工流程。
	startConfirmOpen      bool
	startConfirmThreadID  string
	startConfirmQuestions int

	// 「审查计划」会切到任务板：记下来处（原视图与原选中会话），任务板顶部给一条返回条。
	planReviewFromView   string
	planReviewFromThread string
}

var (
	stewardTopicsMu     sync.Mutex
	stewardTopicsStates = make(map[*chatState]*stewardTopicsState)
)

func getStewardTopicsState(s *chatState) *stewardTopicsState {
	if s == nil {
		return nil
	}
	stewardTopicsMu.Lock()
	defer stewardTopicsMu.Unlock()
	st := stewardTopicsStates[s]
	if st == nil {
		st = &stewardTopicsState{
			collapsed:     make(map[string]bool),
			cardErrs:      make(map[string]string),
			cardNotes:     make(map[string]string),
			busy:          make(map[string]bool),
			reviewTasks:   make(map[string][]KanbanTask),
			reviewAt:      make(map[string]time.Time),
			reviewLoading: make(map[string]bool),
		}
		stewardTopicsStates[s] = st
	}
	return st
}

// stewardTopicsClient 取话题客户端；宿主不支持这个接口时整块话题界面不渲染。
func (s *chatState) stewardTopicsClient() (StewardTopicsClient, bool) {
	if s == nil || s.client == nil {
		return nil, false
	}
	client, ok := s.client.(StewardTopicsClient)
	return client, ok
}

// sortStewardTopics 按最近更新倒序；侧栏与测试共用同一顺序。
func sortStewardTopics(topics []StewardTopic) []StewardTopic {
	out := append([]StewardTopic(nil), topics...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func splitStewardTopics(topics []StewardTopic) (active, closed []StewardTopic) {
	for _, topic := range topics {
		if topic.Closed {
			closed = append(closed, topic)
			continue
		}
		active = append(active, topic)
	}
	return active, closed
}

// stewardTopicGroup 是侧栏在办话题的一组：同一个项目的话题放在一起。
type stewardTopicGroup struct {
	// ProjectID 是组合依据的项目 ID；ProjectName 是展示用的项目名。
	ProjectID   string
	ProjectName string
	Topics      []StewardTopic
}

// groupStewardTopicsByProject 把已排序的在办话题按项目分组：组内保持输入顺序，
// 组之间按各组第一条话题的先后排序；项目名缺失时显示「未知项目」。
func groupStewardTopicsByProject(topics []StewardTopic, projectNames map[string]string) []stewardTopicGroup {
	if len(topics) == 0 {
		return nil
	}
	groups := make([]stewardTopicGroup, 0, len(topics))
	index := make(map[string]int, len(topics))
	for _, topic := range topics {
		if at, ok := index[topic.ProjectID]; ok {
			groups[at].Topics = append(groups[at].Topics, topic)
			continue
		}
		name := strings.TrimSpace(projectNames[topic.ProjectID])
		if name == "" {
			name = "未知项目"
		}
		index[topic.ProjectID] = len(groups)
		groups = append(groups, stewardTopicGroup{
			ProjectID:   topic.ProjectID,
			ProjectName: name,
			Topics:      []StewardTopic{topic},
		})
	}
	return groups
}

// stewardProjectNames 把 chatState 现有的项目列表转成「项目 ID → 项目名」，供侧栏话题分组用。
func (s *chatState) stewardProjectNames() map[string]string {
	names := make(map[string]string)
	if s == nil {
		return names
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, project := range s.projects {
		if project.ID == "" || project.Name == "" {
			continue
		}
		names[project.ID] = project.Name
	}
	return names
}

// stewardTopicStatusText 是侧栏状态小字与卡片阶段文字的同一份文案。
// 一律用用户能懂的说法描述「现在在等你做什么」，不吐内部阶段名。
func stewardTopicStatusText(topic StewardTopic) string {
	switch topic.Phase {
	case StewardTopicDrafting:
		if topic.OpenQuestions > 0 {
			return fmt.Sprintf("还有 %d 个问题要问你", topic.OpenQuestions)
		}
		return "等你确认任务"
	case StewardTopicPlanning:
		return "正在出方案"
	case StewardTopicPlanFailed:
		return "方案没出来"
	case StewardTopicPlanReview:
		return "方案等你过目"
	case StewardTopicRunning:
		text := fmt.Sprintf("%d/%d", topic.TasksDone, topic.TasksTotal)
		if topic.PendingUser > 0 {
			text += fmt.Sprintf(" · %d 项待你确认", topic.PendingUser)
		}
		return text
	case StewardTopicPaused:
		return "已暂停"
	case StewardTopicCompleted:
		return "做完了，等你验收"
	case StewardTopicCancelled:
		return "已取消"
	}
	if topic.Phase == "" {
		return "等你确认任务"
	}
	// 未知阶段不把内部枚举串原样吐给用户。
	return "处理中"
}

// refreshStewardTopics 首次渲染时拉一次；之后由 WatchStewardTopics 推送触发。
func (s *chatState) refreshStewardTopics() {
	s.loadStewardTopics(false)
}

func (s *chatState) refreshStewardTopicsFromWatch() {
	if st := getStewardTopicsState(s); st != nil {
		// 话题状态变了，评审任务缓存可能过期，下一帧重拉。
		st.mu.Lock()
		st.reviewTasks = make(map[string][]KanbanTask)
		st.reviewAt = make(map[string]time.Time)
		st.mu.Unlock()
	}
	s.loadStewardTopics(true)
}

func (s *chatState) loadStewardTopics(queued bool) {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil {
		return
	}
	st := getStewardTopicsState(s)
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
		if wait := stewardTopicsRefreshInterval - time.Since(st.fetchedAt); wait > 0 {
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
							s.loadStewardTopics(true)
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
		topics, err := client.StewardTopics(context.Background())
		s.mu.Lock()
		closed := s.closed
		selectedThreadID := s.selectedThreadID
		s.mu.Unlock()
		// stewardThreadID 会回到客户端，可能拿自己的锁，所以放在 s.mu 之外取。
		stewardThreadID := s.stewardThreadID()

		st.mu.Lock()
		st.loading = false
		st.fetchedAt = time.Now()
		if err == nil {
			// 用旧的列表与旧的 loaded 判断有没有新话题；命中的话下一帧渲染时跳过去。
			if next := stewardTopicToAutoOpen(st.topics, topics, st.loaded, selectedThreadID, stewardThreadID); next != "" {
				st.pendingOpen = next
			}
			st.topics = topics
			st.loaded = true
			st.loadErr = ""
		} else {
			// 拉取失败时只记错误，旧话题保留：看到旧数据总比列表突然清空强。
			st.loadErr = err.Error()
		}
		pending := st.pending
		st.pending = false
		st.mu.Unlock()

		if !closed {
			s.invalidate()
		}
		if pending {
			s.loadStewardTopics(true)
		}
	}()
}

// ensureStewardTopicsWatch 订阅话题推送；客户端换了要重新订阅。
func (s *chatState) ensureStewardTopicsWatch() {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil {
		return
	}
	st := getStewardTopicsState(s)
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

	stop := client.WatchStewardTopics(func() {
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		s.refreshStewardTopicsFromWatch()
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

func (s *chatState) stewardTopicFor(threadID string) (StewardTopic, bool) {
	st := getStewardTopicsState(s)
	if st == nil || threadID == "" {
		return StewardTopic{}, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, topic := range st.topics {
		if topic.ThreadID == threadID {
			return topic, true
		}
	}
	return StewardTopic{}, false
}

// stewardTopicThreadIDs 返回所有话题会话的 ID，项目树据此不再重复列出它们。
// 只有总管段真的会渲染话题行时才返回，避免话题会话在界面上凭空消失。
func (s *chatState) stewardTopicThreadIDs() map[string]bool {
	if _, ok := s.stewardClient(); !ok {
		return nil
	}
	if s.stewardThreadID() == "" {
		return nil
	}
	if _, ok := s.stewardTopicsClient(); !ok {
		return nil
	}
	st := getStewardTopicsState(s)
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if len(st.topics) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(st.topics))
	for _, topic := range st.topics {
		if topic.ThreadID != "" {
			ids[topic.ThreadID] = true
		}
	}
	return ids
}

// stewardTopicToAutoOpen 决定新话题出现后要不要自动打开：只有用户正停在总管主对话时
// 才把新话题接过来，在别的会话时不抢焦点。首次加载不跳，免得把旧话题当成新话题；
// 返回第一条新出现的未关闭话题，没有就返回空串。
func stewardTopicToAutoOpen(prev, next []StewardTopic, loaded bool, selectedThreadID, stewardThreadID string) string {
	if !loaded || stewardThreadID == "" || selectedThreadID != stewardThreadID {
		return ""
	}
	known := make(map[string]bool, len(prev))
	for _, topic := range prev {
		known[topic.ThreadID] = true
	}
	for _, topic := range next {
		if topic.Closed || topic.ThreadID == "" || known[topic.ThreadID] {
			continue
		}
		return topic.ThreadID
	}
	return ""
}

// takeStewardPendingTopic 取出后台刚建好的话题会话，交给界面线程跳转。
func (s *chatState) takeStewardPendingTopic() string {
	st := getStewardTopicsState(s)
	if st == nil {
		return ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	threadID := st.pendingOpen
	st.pendingOpen = ""
	return threadID
}

// openStewardTopicThread 打开话题背后的会话，和总管主会话一样先收起工作区。
func (s *chatState) openStewardTopicThread(threadID string) {
	if threadID == "" {
		return
	}
	s.closeWorkspace()
	s.revealThread(threadID)
	s.invalidate()
}

// renderSidebarStewardTopics 在总管行下面缩进列出未结项的话题，末尾是历史折叠行。
func renderSidebarStewardTopics(c *ui.Context, s *chatState) {
	if _, ok := s.stewardTopicsClient(); !ok {
		return
	}
	if threadID := s.takeStewardPendingTopic(); threadID != "" {
		s.openStewardTopicThread(threadID)
	}
	s.refreshStewardTopics()
	s.ensureStewardTopicsWatch()

	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	loaded := st.loaded
	loading := st.loading
	loadErr := st.loadErr
	historyOpen := st.historyOpen
	topics := append([]StewardTopic(nil), st.topics...)
	st.mu.Unlock()
	// 拉取失败时给一行说明与「重试」：话题区静默空白会让人以为话题都没了。
	// 旧数据还在就继续展示，只是顶部多一行提示。
	if loadErr != "" {
		renderSidebarStewardTopicsLoadError(c, s)
		if len(topics) == 0 {
			return
		}
	}
	if !loaded {
		// 首次加载还没回来时给一行「加载中…」，避免侧栏看起来是空的。
		if loading && len(topics) == 0 {
			renderSidebarStewardTopicsLoading(c)
		}
		return
	}

	active, closed := splitStewardTopics(topics)
	activeTopics := sortStewardTopics(active)
	closedTopics := sortStewardTopics(closed)
	// 在办话题涉及多个项目时按项目分组，组前加一行项目名；只有一个项目时与原来一致。
	groups := groupStewardTopicsByProject(activeTopics, s.stewardProjectNames())
	showProjectHeadings := len(groups) > 1

	// 话题行放进独立滚动区，最多露出 stewardTopicsScrollRows 行；「历史 N」行留在滚动区外，
	// 这样话题再多也只占固定高度，不会把下面的项目列表挤出侧栏。
	if len(activeTopics) > 0 || historyOpen && len(closedTopics) > 0 {
		ui.Scroll(c.Key("sidebar-steward-topics-scroll")).
			FillWidth().
			MaxHeight(stewardTopicsScrollMaxHeight()).
			Children(func() {
				ui.Column(c.Key("sidebar-steward-topics-list")).
					FillWidth().
					Children(func() {
						for _, group := range groups {
							if showProjectHeadings {
								renderSidebarStewardProjectHeading(c, group.ProjectName)
							}
							for _, topic := range group.Topics {
								renderSidebarStewardTopicRow(c, s, topic)
							}
						}
						// 展开历史后，历史话题与在办话题共用同一个滚动区。
						if !historyOpen {
							return
						}
						for _, topic := range closedTopics {
							renderSidebarStewardTopicRow(c, s, topic)
						}
					})
			})
	}
	if len(closed) == 0 {
		return
	}
	renderSidebarStewardHistoryRow(c, s, len(closed), historyOpen)
}

// retryStewardTopics 重试拉取话题列表：清掉上次的刷新时间点绕过节流，
// 清掉错误提示让「加载中…」出来，再走原来的加载路径。
func (s *chatState) retryStewardTopics() {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.fetchedAt = time.Time{}
	st.loadErr = ""
	st.mu.Unlock()
	s.loadStewardTopics(true)
	s.invalidate()
}

// renderSidebarStewardTopicsLoadError 在话题列表没拉出来时给一行说明与「重试」按钮：
// 静默空白会让人以为话题都没了。
func renderSidebarStewardTopicsLoadError(c *ui.Context, s *chatState) {
	ui.Row(c.Key("sidebar-steward-topics-load-error")).
		FillWidth().
		Padding(Space0, ListInset+stewardSectionAvatarSize+10, Space0, ListInset).
		Margin(0, ListInset, 0, ListInset).
		Gap(Space2).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, "话题列表没加载出来").
				Grow(1).
				SingleLine().
				FontSize(scaledFont(FontSizeProjectName)).
				TextColor(ThemeSecondaryText(c))
			Button(c.Key("sidebar-steward-topics-retry"), ButtonOpts{
				Text:    "重试",
				Label:   "重试加载话题列表",
				Variant: ButtonSecondary,
				Size:    ControlXS,
				OnClick: func() { s.retryStewardTopics() },
			})
		})
}

// renderSidebarStewardTopicsLoading 是话题首次加载时的一行占位。
func renderSidebarStewardTopicsLoading(c *ui.Context) {
	ui.Text(c, "加载中…").
		Margin(Space0, ListInset+stewardSectionAvatarSize+10, Space0, ListInset).
		FontSize(scaledFont(FontSizeProjectName)).
		TextColor(ThemeSecondaryText(c)).
		SingleLine()
}

// renderSidebarStewardProjectHeading 是在办话题的项目分组标题：小号、弱化色，
// 缩进与话题行对齐；只在话题跨多个项目时渲染。
func renderSidebarStewardProjectHeading(c *ui.Context, name string) {
	ui.Text(c, name).
		Margin(Space1, ListInset+stewardSectionAvatarSize+10, Space0, ListInset+stewardSectionAvatarSize+10).
		FontSize(scaledFont(FontSizeProjectName)).
		TextColor(ThemeSecondaryText(c)).
		SingleLine()
}

func renderSidebarStewardTopicRow(c *ui.Context, s *chatState, topic StewardTopic) {
	if topic.ThreadID == "" {
		return
	}
	s.mu.Lock()
	selected := s.selectedThreadID == topic.ThreadID
	s.mu.Unlock()

	title := topic.Title
	if title == "" {
		title = "未命名话题"
	}
	row := ui.Column(c.Key("sidebar-steward-topic-"+topic.ThreadID)).
		FillWidth().
		Padding(Space1, ListInset+stewardSectionAvatarSize+10, Space1, ListInset).
		Margin(0, ListInset, 0, ListInset).
		Gap(Space0).
		Radius(RowCornerRadius).
		Label(title)

	hovered := row.Hovered()
	// 省略号菜单打开时会拿到焦点，指针离开后只要焦点还在行内就保留按钮，
	// 否则菜单刚弹出按钮就消失；与 sidebar.go 的会话行同构。
	showRowActions := hovered || row.FocusWithin()
	if selected {
		row.Background(ThemeSelected(c))
	} else if hovered {
		row.Background(ThemeHover(c))
	}
	row.ContextMenu(stewardTopicContextMenu(c, s, topic))
	if row.Clicked() {
		s.openStewardTopicThread(topic.ThreadID)
	}

	row.Children(func() {
		titleRow := ui.Row(c.Key("sidebar-steward-topic-title-" + topic.ThreadID)).
			FillWidth().
			Gap(Space2).
			AlignItems(ui.Center)
		titleRow.Children(func() {
			ui.Text(c, title).
				Grow(1).
				SingleLine().
				FontSize(scaledFont(FontSizeThreadTitle)).
				FontWeight(500).
				TextColor(ThemePrimaryText(c).Alpha(0.92))
			if topic.PendingUser > 0 {
				ui.Box(c.Key("sidebar-steward-topic-dot-"+topic.ThreadID)).
					Size(6, 6).
					Radius(RadiusXS).
					Background(ThemeDanger(c))
			}
			if showRowActions {
				moreBtn := ui.ButtonBase(c.Key("steward-topic-more-"+topic.ThreadID)).
					Label("更多操作").
					Tooltip("更多操作").
					Radius(RadiusSM).
					Padding(Space0, Space1)
				if moreBtn.Hovered() {
					moreBtn.Background(ThemeHover(c))
				}
				moreBtn.Menu(stewardTopicContextMenu(c, s, topic))
				moreBtn.Children(func() {
					ui.Icon(c, IconMoreHorizontal()).
						Size(14, 14).
						TextColor(ThemeSecondaryText(c))
				})
			}
		})
		ui.Text(c, stewardTopicStatusText(topic)).
			SingleLine().
			FontSize(scaledFont(FontSizeProjectName)).
			TextColor(ThemeSecondaryText(c))
	})
}

func renderSidebarStewardHistoryRow(c *ui.Context, s *chatState, count int, open bool) {
	row := ui.Row(c.Key("sidebar-steward-history")).
		FillWidth().
		MinHeight(HeightThreadRowCompact).
		Padding(Space0, ListInset+stewardSectionAvatarSize+10, Space0, ListInset).
		Margin(Space0, ListInset, Space0, ListInset).
		Gap(Space2).
		AlignItems(ui.Center).
		Radius(RowCornerRadius).
		Label(fmt.Sprintf("历史 %d", count))

	if row.Hovered() {
		row.Background(ThemeHover(c))
	}
	if row.Clicked() {
		if st := getStewardTopicsState(s); st != nil {
			st.mu.Lock()
			st.historyOpen = !st.historyOpen
			st.mu.Unlock()
		}
		s.invalidate()
	}

	row.Children(func() {
		icon := IconChevronRight()
		if open {
			icon = IconChevronDown()
		}
		ui.Icon(c, icon).
			Size(10, 10).
			TextColor(ThemeSecondaryText(c))
		ui.Text(c, fmt.Sprintf("历史 %d", count)).
			FontSize(scaledFont(FontSizeProjectName)).
			TextColor(ThemeSecondaryText(c))
	})
}

// stewardTopicActionErrorText 把卡片操作失败归成人话：连不上宿主的底层错误统一提示稍后重试，
// 其余错误保留原文并在前面加一句说明，免得用户只看到一行英文报错。
func stewardTopicActionErrorText(err error) string {
	if err == nil {
		return ""
	}
	raw := err.Error()
	lower := strings.ToLower(raw)
	for _, needle := range []string{"rpc", "connection", "deadline", "eof"} {
		if strings.Contains(lower, needle) {
			return "没连上宿主，请稍后再试"
		}
	}
	return "操作没成功：" + raw
}

// stewardTopicBusyText 返回按钮进行中的文案：点下去立刻换成这句话，用户能看出这一下真的提交了。
// 没登记的动作返回空串，调用方继续用原按钮文字。
func stewardTopicBusyText(key string) string {
	switch {
	case key == "steward-topic-confirm", key == "steward-topic-start":
		return "开工中…"
	case key == "steward-topic-replan":
		return "重新规划中…"
	case key == "steward-topic-close":
		return "结项中…"
	case strings.HasPrefix(key, "steward-topic-task-accept"):
		return "通过中…"
	}
	return ""
}

// stewardTopicActionDone 收尾一次卡片操作：清掉进行中的按钮，把错误归成一句人话写进 cardErrs；
// 成功时写一行提示（stewardTopicNoteDuration 后自动消失）并重拉话题。note 为空表示这一步不给提示。
// busyKey 是 busy 表的键（任务按钮用「会话/任务」），cardKey 是卡片所在话题的会话 ID。
func (s *chatState) stewardTopicActionDone(st *stewardTopicsState, busyKey, cardKey string, err error, note string) {
	if st == nil {
		return
	}
	st.mu.Lock()
	st.busy[busyKey] = false
	if err != nil {
		st.cardErrs[cardKey] = stewardTopicActionErrorText(err)
		st.mu.Unlock()
		s.invalidate()
		return
	}
	delete(st.cardErrs, cardKey)
	if note != "" {
		st.cardNotes[cardKey] = note
	}
	st.mu.Unlock()
	s.invalidate()
	s.refreshStewardTopicsFromWatch()
	if note == "" {
		return
	}
	time.AfterFunc(stewardTopicNoteDuration, func() {
		st.mu.Lock()
		// 只清这一条：期间可能又写了一条新的提示。
		if st.cardNotes[cardKey] == note {
			delete(st.cardNotes, cardKey)
		}
		st.mu.Unlock()
		s.invalidate()
	})
}

func (s *chatState) confirmStewardTopic(threadID string) {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil || threadID == "" {
		return
	}
	st := getStewardTopicsState(s)
	st.mu.Lock()
	if st.busy[threadID] {
		st.mu.Unlock()
		return
	}
	st.busy[threadID] = true
	delete(st.cardErrs, threadID)
	st.mu.Unlock()
	s.invalidate()

	nickname, _ := s.stewardIdentity()
	go func() {
		err := client.ConfirmStewardTopic(context.Background(), threadID)
		s.stewardTopicActionDone(st, threadID, threadID, err, "已交给"+stewardName(nickname)+"开工")
	}()
}

// openStewardTopicStartConfirm 打开「按推荐答案开工」的轻确认，记住话题与未定问题数。
func (s *chatState) openStewardTopicStartConfirm(topic StewardTopic) {
	if topic.ThreadID == "" {
		return
	}
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.startConfirmOpen = true
	st.startConfirmThreadID = topic.ThreadID
	st.startConfirmQuestions = topic.OpenQuestions
	st.mu.Unlock()
	s.invalidate()
}

// closeStewardTopicStartConfirm 关掉轻确认：不记任何提交状态，用户随时可以再点一次。
func (s *chatState) closeStewardTopicStartConfirm() {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.startConfirmOpen = false
	st.startConfirmThreadID = ""
	st.startConfirmQuestions = 0
	st.mu.Unlock()
	s.invalidate()
}

// confirmStewardTopicStart 确认后走原来的开工流程：关弹窗、调 ConfirmStewardTopic。
func (s *chatState) confirmStewardTopicStart() {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	threadID := st.startConfirmThreadID
	st.mu.Unlock()
	s.closeStewardTopicStartConfirm()
	if threadID == "" {
		return
	}
	s.confirmStewardTopic(threadID)
}

// openStewardTopicPlanReview 打开话题所属目标的计划审查弹窗：先把工作区切到目标所在项目
// 的任务视图，再把审查弹窗指向该目标。加锁顺序与 workspace.go 的 openWorkspace 一致：
// 先 workspaceMu，再 kanbanMu。
func (s *chatState) openStewardTopicPlanReview(topic StewardTopic) {
	if s == nil || topic.GoalID == "" {
		return
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return
	}

	s.mu.Lock()
	fromThread := s.selectedThreadID
	s.mu.Unlock()

	workspaceMu.Lock()
	if topic.ProjectID != "" && ws.projectID != topic.ProjectID {
		// 话题在别的项目：换项目后由看板刷新逻辑重建 kanbanState。
		ws.projectID = topic.ProjectID
	}
	fromView := ws.view
	ws.view = "tasks"
	workspaceMu.Unlock()

	// 记下来处，任务板顶部给一条「回到方案」的返回条。已经在任务板里再点审查时
	// 不要用自己的来处覆盖第一次记下的会话。
	if fromView != "" && fromView != "tasks" {
		if st := getStewardTopicsState(s); st != nil {
			st.mu.Lock()
			st.planReviewFromView = fromView
			st.planReviewFromThread = fromThread
			st.mu.Unlock()
		}
	}
	// 切到任务视图后自动化视图不再显示，顺手停掉它的订阅，与 openWorkspace 同构。
	stopAutomationWatch(s)

	ks := getKanbanState(s)
	if ks == nil {
		return
	}
	kanbanMu.Lock()
	if ks.planReviewSeen == nil {
		ks.planReviewSeen = make(map[string]bool)
	}
	ks.planReviewSeen[topic.GoalID] = true
	ks.planReviewGoalID = topic.GoalID
	ks.planReviewReplanOpen = false
	ks.planReviewFeedback = ""
	kanbanMu.Unlock()

	s.invalidate()
}

// stewardPlanReviewOrigin 读「审查计划」记下的来处（原视图与原选中会话），没有记录时两个都是空串。
func stewardPlanReviewOrigin(s *chatState) (view, threadID string) {
	st := getStewardTopicsState(s)
	if st == nil {
		return "", ""
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.planReviewFromView, st.planReviewFromThread
}

// syncStewardPlanReviewOrigin 在用户自己离开任务板（或本来就停在聊天视图）时清掉来处记录，
// 返回条只在「点审查计划带过去」的那一趟里出现。每帧调用一次，成本只是两次读值。
func syncStewardPlanReviewOrigin(s *chatState) {
	if s == nil {
		return
	}
	ws := getWorkspaceState(s)
	if ws == nil {
		return
	}
	workspaceMu.Lock()
	view := ws.view
	workspaceMu.Unlock()

	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.planReviewFromView != "" && view != "tasks" {
		st.planReviewFromView = ""
		st.planReviewFromThread = ""
	}
	st.mu.Unlock()
}

// backToStewardPlanReviewOrigin 从任务板返回审查计划之前的视图与会话，并清掉来处记录。
func (s *chatState) backToStewardPlanReviewOrigin() {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	view := st.planReviewFromView
	threadID := st.planReviewFromThread
	st.planReviewFromView = ""
	st.planReviewFromThread = ""
	st.mu.Unlock()

	if threadID != "" {
		s.revealThread(threadID)
	} else {
		s.closeWorkspace()
	}
	// 原来就在自动化视图时，连同自动化工作区一起开回去。
	if view == "automations" {
		s.openWorkspace(view)
	}
}

// renderStewardPlanReviewReturnBar 在任务板顶部给一条返回条：只有从「审查计划」切过来时才渲染，
// 点一下回到原来的视图与会话。
func renderStewardPlanReviewReturnBar(c *ui.Context, s *chatState) {
	view, _ := stewardPlanReviewOrigin(s)
	if view == "" {
		return
	}
	nickname, _ := s.stewardIdentity()
	text := "← 回到" + stewardName(nickname) + "的方案"

	bar := ui.Row(c.Key("kanban-steward-plan-return")).
		Label(text).
		FillWidth().
		Padding(Space3, Space4).
		Radius(RadiusMD).
		AlignItems(ui.Center).
		Gap(Space2).
		Cursor(ui.CursorPointer)
	if bar.Hovered() {
		bar.Background(ThemeHover(c))
	}
	if bar.Clicked() {
		s.backToStewardPlanReviewOrigin()
	}
	bar.Children(func() {
		ui.Icon(c, IconArrowLeft()).
			Size(11, 11).
			TextColor(ThemeAccent(c))
		ui.Text(c, text).
			FontSize(scaledFont(FontSizeProjectName)).
			FontWeight(500).
			TextColor(ThemeAccent(c))
	})
}

func (s *chatState) closeStewardTopic(threadID string) {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil || threadID == "" {
		return
	}
	st := getStewardTopicsState(s)
	st.mu.Lock()
	if st.busy[threadID] {
		st.mu.Unlock()
		return
	}
	st.busy[threadID] = true
	delete(st.cardErrs, threadID)
	st.mu.Unlock()
	s.invalidate()

	go func() {
		err := client.CloseStewardTopic(context.Background(), threadID)
		s.stewardTopicActionDone(st, threadID, threadID, err, "已结项")
	}()
}

// replanStewardTopic 在规划失败后让总管重新规划：与 closeStewardTopic 同一写法，
// 只是把客户端调用换成 ReplanStewardTopic，成功后由推送触发重拉。
func (s *chatState) replanStewardTopic(threadID string) {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil || threadID == "" {
		return
	}
	st := getStewardTopicsState(s)
	st.mu.Lock()
	if st.busy[threadID] {
		st.mu.Unlock()
		return
	}
	st.busy[threadID] = true
	delete(st.cardErrs, threadID)
	st.mu.Unlock()
	s.invalidate()

	go func() {
		err := client.ReplanStewardTopic(context.Background(), threadID)
		s.stewardTopicActionDone(st, threadID, threadID, err, "已重新规划")
	}()
}

// startStewardTopicGoal 在用户点「直接开工」时让 plan_review 的话题跳过等审：
// 与 replanStewardTopic 同一写法，只是把客户端调用换成 StartStewardTopicGoal，成功后由推送触发重拉。
func (s *chatState) startStewardTopicGoal(threadID string) {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil || threadID == "" {
		return
	}
	st := getStewardTopicsState(s)
	st.mu.Lock()
	if st.busy[threadID] {
		st.mu.Unlock()
		return
	}
	st.busy[threadID] = true
	delete(st.cardErrs, threadID)
	st.mu.Unlock()
	s.invalidate()

	nickname, _ := s.stewardIdentity()
	go func() {
		err := client.StartStewardTopicGoal(context.Background(), threadID)
		s.stewardTopicActionDone(st, threadID, threadID, err, "已交给"+stewardName(nickname)+"开工")
	}()
}

// openStewardDeleteTopicModal 打开删除话题的确认弹窗，记住标题用于说明文字。
func (s *chatState) openStewardDeleteTopicModal(topic StewardTopic) {
	if topic.ThreadID == "" {
		return
	}
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.deleteOpen = true
	st.deleteThreadID = topic.ThreadID
	st.deleteTitle = topic.Title
	st.deleteBusy = false
	st.deleteErr = ""
	st.mu.Unlock()
	s.invalidate()
}

// closeStewardDeleteTopicModal 关闭删除话题弹窗并清掉错误提示。
func (s *chatState) closeStewardDeleteTopicModal() {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.deleteOpen = false
	st.deleteThreadID = ""
	st.deleteTitle = ""
	st.deleteBusy = false
	st.deleteErr = ""
	st.mu.Unlock()
	s.invalidate()
}

// confirmStewardDeleteTopic 确认删除话题：成功后关弹窗、把话题会话从本地清掉再重拉列表；
// 失败时弹窗保持打开、错误写在弹窗里。forgetDeletedThread 自己要锁，这里不持 st.mu 调用。
func (s *chatState) confirmStewardDeleteTopic() {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil {
		return
	}
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.deleteBusy {
		st.mu.Unlock()
		return
	}
	threadID := st.deleteThreadID
	if threadID == "" {
		st.mu.Unlock()
		return
	}
	st.deleteBusy = true
	st.deleteErr = ""
	st.mu.Unlock()
	s.invalidate()

	go func() {
		err := client.DeleteStewardTopic(context.Background(), threadID)
		if err != nil {
			st.mu.Lock()
			st.deleteBusy = false
			st.deleteErr = err.Error()
			st.mu.Unlock()
			s.invalidate()
			return
		}
		s.closeStewardDeleteTopicModal()
		s.forgetDeletedThread(threadID)
		s.refreshStewardTopicsFromWatch()
	}()
}

// stewardTopicContextMenu 是话题行的右键菜单：侧栏行与卡片右上角的「更多操作」按钮共用同一份菜单。
// 卡片上只留一个主按钮，计划阶段的「审查计划」就退到这里。
func stewardTopicContextMenu(c *ui.Context, s *chatState, topic StewardTopic) func(*ui.Menu) {
	return func(m *ui.Menu) {
		if stewardTopicPlanReviewInMenu(topic) {
			if m.Item("审查计划").Chosen() {
				s.reviewStewardTopicPlan(topic)
			}
		}
		if m.Item("删除话题…").Danger().Chosen() {
			s.openStewardDeleteTopicModal(topic)
		}
	}
}

// stewardTopicPlanReviewInMenu 报告「审查计划」是否该出现在菜单里：计划阶段且已经建出目标。
func stewardTopicPlanReviewInMenu(topic StewardTopic) bool {
	return topic.Phase == StewardTopicPlanReview && topic.GoalID != ""
}

// reviewStewardTopicPlan 从菜单打开计划审查；话题正忙时忽略，避免重复提交。
func (s *chatState) reviewStewardTopicPlan(topic StewardTopic) {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	busy := st.busy[topic.ThreadID]
	st.mu.Unlock()
	if busy {
		return
	}
	s.openStewardTopicPlanReview(topic)
}

// ensureStewardReviewTasks 拉该话题 Goal 下处于 review 的任务；带节流，避免每帧请求。
func (s *chatState) ensureStewardReviewTasks(threadID, projectID, goalID string) {
	client := s.client
	if client == nil || threadID == "" || projectID == "" || goalID == "" {
		return
	}
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.reviewLoading[threadID] {
		st.mu.Unlock()
		return
	}
	if at, ok := st.reviewAt[threadID]; ok && time.Since(at) < stewardReviewTasksRefreshInterval {
		st.mu.Unlock()
		return
	}
	st.reviewLoading[threadID] = true
	st.mu.Unlock()

	go func() {
		board, err := client.KanbanBoard(context.Background(), projectID)
		tasks := make([]KanbanTask, 0, len(board.Tasks))
		if err == nil {
			for _, task := range board.Tasks {
				if task.GoalID != goalID {
					continue
				}
				if task.Status != "review" && task.Column != "review" {
					continue
				}
				tasks = append(tasks, task)
			}
		}
		st.mu.Lock()
		st.reviewLoading[threadID] = false
		st.reviewAt[threadID] = time.Now()
		if err == nil {
			st.reviewTasks[threadID] = tasks
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

func (s *chatState) acceptStewardTask(threadID string, task KanbanTask) {
	client := s.client
	if client == nil || task.ID == "" {
		return
	}
	st := getStewardTopicsState(s)
	key := threadID + "/" + task.ID
	st.mu.Lock()
	if st.busy[key] {
		st.mu.Unlock()
		return
	}
	st.busy[key] = true
	delete(st.cardErrs, threadID)
	st.mu.Unlock()
	s.invalidate()

	go func() {
		_, err := client.ReviewTask(context.Background(), task.ID, "accept", "", task.Revision)
		if err == nil {
			st.mu.Lock()
			delete(st.reviewAt, threadID)
			st.mu.Unlock()
		}
		s.stewardTopicActionDone(st, key, threadID, err, "已通过")
	}()
}

func (s *chatState) openStewardRejectDialog(threadID string, task KanbanTask) {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.rejectOpen = true
	st.rejectThreadID = threadID
	st.rejectTaskID = task.ID
	st.rejectTitle = task.Title
	st.rejectRevision = task.Revision
	st.rejectText = ""
	st.rejectErr = ""
	st.rejectBusy = false
	st.mu.Unlock()
	s.invalidate()
}

func (s *chatState) closeStewardRejectDialog() {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.rejectOpen = false
	st.rejectBusy = false
	st.rejectErr = ""
	st.rejectText = ""
	st.mu.Unlock()
	s.invalidate()
}

func (s *chatState) submitStewardReject() {
	client := s.client
	if client == nil {
		return
	}
	st := getStewardTopicsState(s)
	st.mu.Lock()
	if st.rejectBusy {
		st.mu.Unlock()
		return
	}
	threadID := st.rejectThreadID
	taskID := st.rejectTaskID
	revision := st.rejectRevision
	feedback := strings.TrimSpace(st.rejectText)
	if feedback == "" {
		st.rejectErr = "请填写打回意见"
		st.mu.Unlock()
		s.invalidate()
		return
	}
	st.rejectBusy = true
	st.rejectErr = ""
	st.mu.Unlock()
	s.invalidate()

	go func() {
		_, err := client.ReviewTask(context.Background(), taskID, "reject", feedback, revision)
		st.mu.Lock()
		st.rejectBusy = false
		if err != nil {
			st.rejectErr = err.Error()
		} else {
			st.rejectOpen = false
			delete(st.reviewAt, threadID)
		}
		st.mu.Unlock()
		s.invalidate()
		if err == nil {
			s.refreshStewardTopicsFromWatch()
		}
	}()
}

func (s *chatState) openStewardStartTopicDialog(projectID, projectName string) {
	if _, ok := s.stewardTopicsClient(); !ok || projectID == "" {
		return
	}
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.startOpen = true
	st.startProjectID = projectID
	st.startProjectName = projectName
	st.startText = ""
	st.startErr = ""
	st.startBusy = false
	st.mu.Unlock()
	s.invalidate()
}

func (s *chatState) closeStewardStartTopicDialog() {
	st := getStewardTopicsState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	st.startOpen = false
	st.startBusy = false
	st.startErr = ""
	st.startText = ""
	st.mu.Unlock()
	s.invalidate()
}

func (s *chatState) submitStewardStartTopic() {
	client, ok := s.stewardTopicsClient()
	if !ok || client == nil {
		return
	}
	st := getStewardTopicsState(s)
	st.mu.Lock()
	if st.startBusy {
		st.mu.Unlock()
		return
	}
	projectID := st.startProjectID
	request := strings.TrimSpace(st.startText)
	if projectID == "" || request == "" {
		st.startErr = "先写一句想让它做什么"
		st.mu.Unlock()
		s.invalidate()
		return
	}
	st.startBusy = true
	st.startErr = ""
	st.mu.Unlock()
	s.invalidate()

	go func() {
		threadID, err := client.StartStewardTopic(context.Background(), projectID, request)
		st.mu.Lock()
		st.startBusy = false
		if err != nil {
			st.startErr = err.Error()
		} else {
			st.startOpen = false
			st.pendingOpen = threadID
		}
		st.mu.Unlock()
		s.invalidate()
		if err == nil {
			s.refreshStewardTopicsFromWatch()
		}
	}()
}
