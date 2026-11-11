package mcodeui

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// sidebarActivityState 保存侧边栏「动态视图」的开关状态，按 *chatState 附加
// （同 worktreeUIState 模式），避免改动 model.go 中的 chatState 结构。
type sidebarActivityState struct {
	mu      sync.Mutex
	enabled bool
}

var (
	sidebarActivityMu     sync.Mutex
	sidebarActivityStates = make(map[*chatState]*sidebarActivityState)
)

// sidebarNoResultsPadY 是无结果卡片的上下内边距：在列表区里垂直居中一大块空白。
const sidebarNoResultsPadY = float32(48)

// sidebarEmptyPadX 是空列表提示行的左右内边距，比列表常规内边距再推远一点。
const sidebarEmptyPadX = float32(28)

// getSidebarActivityState 返回 chatState 对应的动态视图状态，首次访问时创建。
func getSidebarActivityState(s *chatState) *sidebarActivityState {
	sidebarActivityMu.Lock()
	defer sidebarActivityMu.Unlock()
	st, ok := sidebarActivityStates[s]
	if !ok {
		st = &sidebarActivityState{}
		sidebarActivityStates[s] = st
	}
	return st
}

// toggleActivityView 翻转动态视图开关并请求重绘（铃铛按钮与 ⌥⌘U 共用入口）。
func (s *chatState) toggleActivityView() {
	if s == nil {
		return
	}
	st := getSidebarActivityState(s)
	st.mu.Lock()
	st.enabled = !st.enabled
	st.mu.Unlock()
	s.invalidate()
}

// activityViewEnabled 返回动态视图是否开启。
func (s *chatState) activityViewEnabled() bool {
	if s == nil {
		return false
	}
	st := getSidebarActivityState(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.enabled
}

// activityGroupTitles 是动态视图的分组标题，按从新到旧的顺序。
var activityGroupTitles = []string{"今天", "昨天", "过去 7 天", "更早"}

// activityGroup 是动态视图里的一组会话。
type activityGroup struct {
	Title   string
	Threads []Thread
}

// startOfLocalDay 返回时间所在本地日历日的零点。
func startOfLocalDay(t time.Time) time.Time {
	y, m, d := t.Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// activityGroupTitle 按本地日历日返回时间所属的动态视图分组标题。
func activityGroupTitle(t, now time.Time) string {
	if t.IsZero() {
		return "更早"
	}
	day := startOfLocalDay(t)
	today := startOfLocalDay(now)
	if !day.Before(today) {
		return "今天"
	}
	if !day.Before(today.AddDate(0, 0, -1)) {
		return "昨天"
	}
	if !day.Before(today.AddDate(0, 0, -6)) {
		return "过去 7 天"
	}
	return "更早"
}

// isActivityHiddenHelper 判断会话是否为子代理（Swarm bee 等辅助会话）。
// 子代理挂在主会话下，不进入动态视图；派生会话（ForkedFromThreadID）不算子代理，仍要显示。
// 判定口径与 internal/remote/session_settings.go 的 isHelperThread 一致。
func isActivityHiddenHelper(th Thread) bool {
	return th.IsHelper || th.ParentThreadID != ""
}

// groupThreadsByActivity 把会话按最后活跃时间（UpdatedAt，本地日历日）分为
// 今天 / 昨天 / 过去 7 天 / 更早；组内按时间倒序，空组省略。
// 已休眠（isThreadDormantOrArchived）会话不进入动态视图：它们在项目树里另有折叠分组；
// 子代理会话（isActivityHiddenHelper）同样不进入动态视图。
func groupThreadsByActivity(threads []Thread, now time.Time) []activityGroup {
	buckets := make(map[string][]Thread, len(activityGroupTitles))
	for _, th := range threads {
		if isThreadDormantOrArchived(th) {
			continue
		}
		if isActivityHiddenHelper(th) {
			continue
		}
		title := activityGroupTitle(th.UpdatedAt, now)
		buckets[title] = append(buckets[title], th)
	}
	groups := make([]activityGroup, 0, len(activityGroupTitles))
	for _, title := range activityGroupTitles {
		list := buckets[title]
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool {
			return list[i].UpdatedAt.After(list[j].UpdatedAt)
		})
		groups = append(groups, activityGroup{Title: title, Threads: list})
	}
	return groups
}

// searchHasAnyMatch 判断搜索词是否命中任一项目名或会话标题（「全部未命中」判定）。
// 空搜索词视为命中。
func searchHasAnyMatch(projects []Project, threadsByProj map[string][]Thread, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	for _, p := range projects {
		if strings.Contains(strings.ToLower(p.Name), q) {
			return true
		}
	}
	for _, list := range threadsByProj {
		for _, th := range list {
			if strings.Contains(strings.ToLower(th.Title), q) {
				return true
			}
		}
	}
	return false
}

// renderSidebarNoResultsCard 渲染搜索无结果的弱化卡片：
// 放大镜图标 + 「无结果」标题 + 提示副标题，在列表区居中展示。
func renderSidebarNoResultsCard(c *ui.Context) {
	ui.Column(c.Key("sidebar-no-results")).
		Padding(sidebarNoResultsPadY, Space5).
		AlignItems(ui.Center).
		Children(func() {
			EmptyState(c, EmptyStateProps{
				Icon: IconSearch(), Title: "无结果", Description: "试试输入会话标题或项目名称",
			})
		})
}

// sidebarActivityListOptions 是动态视图列表渲染所需的快照（renderSidebar 传入）。
type sidebarActivityListOptions struct {
	threads         []Thread // 全部会话快照（s.threads）
	projectNames    map[string]string
	providers       []ProviderInfo
	selectedID      string
	matchingThreads []Thread // 搜索命中的会话，键盘高亮用
	highlightIndex  int
	query           string // 已小写化的搜索词，空表示不过滤
	rowHeight       float32
	padY            float32
	padX            float32
	compact         bool
}

// renderActivityThreadList 渲染动态视图列表：按活跃时间分组，每组一个小节标题，
// 行内附带所属项目名；搜索词非空时按会话标题过滤，过滤后为空则显示「无结果」卡片。
func renderActivityThreadList(c *ui.Context, s *chatState, o sidebarActivityListOptions) {
	var filtered []Thread
	for _, th := range o.threads {
		if o.query != "" && !strings.Contains(strings.ToLower(th.Title), o.query) {
			continue
		}
		filtered = append(filtered, th)
	}
	groups := groupThreadsByActivity(filtered, time.Now())
	if len(groups) == 0 {
		if o.query != "" {
			renderSidebarNoResultsCard(c)
			return
		}
		ui.Text(c, "暂无会话").
			TextColor(ThemeSecondaryText(c)).
			FontSize(sidebarFont(TypeFootnote)).
			Padding(Space1, sidebarEmptyPadX)
		return
	}

	for gi, g := range groups {
		renderActivityGroupHeader(c, g.Title, gi == 0, o.padX)
		for _, th := range g.Threads {
			highlighted := o.query != "" &&
				o.highlightIndex >= 0 && o.highlightIndex < len(o.matchingThreads) &&
				o.matchingThreads[o.highlightIndex].ID == th.ID
			renderSidebarThreadRow(c, s, sidebarThreadRowOptions{
				projectID:   th.ProjectID,
				thread:      th,
				selected:    th.ID == o.selectedID,
				highlighted: highlighted,
				rowHeight:   o.rowHeight,
				padY:        o.padY,
				padX:        o.padX,
				compact:     o.compact,
				providers:   o.providers,
				projectName: o.projectNames[th.ProjectID],
			})
		}
	}
}

// renderActivityGroupHeader 渲染动态视图的小节标题（沿用次要色小号分组标题样式）。
func renderActivityGroupHeader(c *ui.Context, title string, isFirst bool, padX float32) {
	top := Space4
	if isFirst {
		top = Space0
	}
	ui.Row(c.Key("activity-header-"+title)).
		Padding(top, padX, Space1, padX).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, title).
				FontSize(sidebarFont(FontSizeDormantTitle)).
				FontWeight(600).
				TextColor(ThemeSecondaryText(c)).
				Grow(1).
				SingleLine()
		})
}
