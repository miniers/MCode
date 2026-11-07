package mcodeui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 时间轴里的子线程胶囊：主会话的每个 Swarm 子线程渲染成一枚胶囊（头像、名字、状态与耗时）。
// 胶囊按“段”归位：时间线被用户消息与 Swarm 汇报行切成若干段，每段之前插一条胶囊行，
// 段内派出的子线程就留在它执行时所在的位置（下一批汇报行之前），只有最后一段出现在时间线末尾；
// 归属按子线程的 StartedAt 与各段边界的时间实时计算。
// 胶囊内容每帧按 chatState 当前的 SwarmState 渲染，因此会从「运行中」实时变成「已完成」，
// 而不是留在派出那一刻的状态。点胶囊弹出该子线程的汇报内容，并可一键把它的浮窗重新打开。

// swarmCapsuleRowID 是时间线末尾（最后一段）胶囊行的固定 Key；按段归位的行在它后面加边界条目的 id。
const swarmCapsuleRowID = "swarm-timeline-capsules"

// swarmCapsuleTurnSpan 描述一条胶囊行负责哪些子线程：StartedAt 落在 [From, Until) 内的子线程归这一行。
// From 为零值表示不设下界（第一段行用它兜住更早启动的子线程）；Latest 为 true 的行代表最后一段，
// 不设上界，并兜住 StartedAt 为零值、无法按时间归入任何段的子线程。
// 上下界由各段边界条目（用户消息或汇报行）的发送时间得到，边界不会随时间移动，所以渲染时只需比对 StartedAt。
type swarmCapsuleTurnSpan struct {
	From   time.Time
	Until  time.Time
	Latest bool
}

// swarmCapsuleRowIDForTurn 返回某个回合的兜底胶囊行 Key：只在边界条目没有 id 时使用，
// 回合号参与 Key，同一回合里的多条行再各自加边界序号。
func swarmCapsuleRowIDForTurn(turnIndex int) string {
	return swarmCapsuleRowID + "-t" + strconv.Itoa(turnIndex)
}

// swarmCapsuleRowKeyFor 返回第 j 个边界之前那条胶囊行的 Key：优先用边界条目自己的 id，
// 这样 Key 跨帧稳定且同帧内唯一；边界没有 id 时退回前一段的回合号加边界序号。
func swarmCapsuleRowKeyFor(turns []swarmCapsuleRowTurn, j int) string {
	if id := strings.TrimSpace(turns[j].itemID); id != "" {
		return swarmCapsuleRowID + "-b-" + id
	}
	return swarmCapsuleRowIDForTurn(turns[j-1].turnIndex) + "-b" + strconv.Itoa(j)
}

// swarmCapsuleRow 构造一条归位到某一段的胶囊行：id 为该行的 Key，turnIndex 为前一段的回合号，
// span 决定渲染时把哪些子线程分到这一行。
func swarmCapsuleRow(id string, turnIndex int, span swarmCapsuleTurnSpan) timelineItem {
	return timelineItem{
		kind:        timelineItemSwarmCapsules,
		id:          id,
		turnIndex:   turnIndex,
		capsuleSpan: span,
	}
}

// swarmCapsulePopoverWidth 是胶囊详情浮窗的卡片宽度：运行中的子线程与已完成的汇报共用。
const swarmCapsulePopoverWidth float32 = 460

// 两类胶囊（运行中的子线程、已完成的汇报）共用同一套外观：主文案与耗时字号取 Type*，
// 行内间距取 Space2，头像直接复用正文的 swarmMentionIconSize()。

// 胶囊浮窗的开合状态，均由 swarmFloatMu 保护（与浮窗状态同一把锁）：
//   - swarmCapsuleOpen 键为 leadID + "|" + bee 标识，值为浮窗是否展开；
//   - swarmCapsuleDismissed 记录上一帧浮窗是否被点外部或 Esc 关闭，
//     避免同一轮里胶囊按钮的 Clicked 又把刚关掉的浮窗打开。
var (
	swarmCapsuleOpen      = make(map[string]bool)
	swarmCapsuleDismissed = make(map[string]bool)
)

// appendSwarmCapsuleRow 在时间线末尾追加代表最新回合的子线程胶囊行。
// 该行是一个固定条目：内容由 renderSwarmTimelineCapsules 每帧按当前 Swarm 状态渲染，
// 没有子线程时整行渲染为空（不占高度），因此可以常驻在列表里而不影响布局。
// 它的下界取最后一条按段插好的胶囊行的上界（即最后一个边界的时间，可能是汇报行），
// 加上 Latest 标记后不再设上界，保证每个子线程只出现在一行里。
func appendSwarmCapsuleRow(items []timelineItem) []timelineItem {
	var from time.Time
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].kind == timelineItemSwarmCapsules {
			from = items[i].capsuleSpan.Until
			break
		}
	}
	return append(items, timelineItem{
		kind:        timelineItemSwarmCapsules,
		id:          swarmCapsuleRowID,
		turnIndex:   -1,
		capsuleSpan: swarmCapsuleTurnSpan{From: from, Latest: true},
	})
}

// swarmCapsuleRowTurn 记录一个分段边界（用户消息或 Swarm 汇报行）在时间线里的位置、
// 条目 id、所属回合与发送时间。
type swarmCapsuleRowTurn struct {
	itemIndex int
	itemID    string
	turnIndex int
	startedAt time.Time
}

// swarmCapsuleRowTurns 按时间线顺序取出所有分段边界：用户消息切出一个新回合，
// Swarm 汇报行（timelineItemSwarmReport，由相邻汇报消息合并而成）同样切一段——
// 同一个用户回合里多轮派出的子线程因此各自留在自己那批汇报行之前。
func swarmCapsuleRowTurns(items []timelineItem) []swarmCapsuleRowTurn {
	var turns []swarmCapsuleRowTurn
	for i, item := range items {
		switch item.kind {
		case timelineItemSingle:
			if item.msg.Role != "user" {
				continue
			}
		case timelineItemSwarmReport:
			// 汇报行也是分段边界。
		default:
			continue
		}
		at, _ := messageTime(item.msg)
		turns = append(turns, swarmCapsuleRowTurn{
			itemIndex: i,
			itemID:    item.id,
			turnIndex: item.turnIndex,
			startedAt: at,
		})
	}
	return turns
}

// swarmCapsuleRowBoundary 返回第 j 个边界的时间；该边界没有可用时间戳时
// 向后顺延到最近一个有时间的边界（没有再向前找），让缺失的边界不会把子线程排除在所有行之外。
func swarmCapsuleRowBoundary(turns []swarmCapsuleRowTurn, j int) time.Time {
	for k := j; k < len(turns); k++ {
		if !turns[k].startedAt.IsZero() {
			return turns[k].startedAt
		}
	}
	for k := j - 1; k >= 0; k-- {
		if !turns[k].startedAt.IsZero() {
			return turns[k].startedAt
		}
	}
	return time.Time{}
}

// insertSwarmCapsuleRows 按段把子线程胶囊行插进时间线：除第一个边界外，每个边界条目前插一行，
// 代表上一段派出的子线程，让这一批胶囊留在它执行时的位置（派出它的 lead 回复之后、下一批汇报行之前），
// 而不是全落到时间线最底部。边界包括用户消息与 Swarm 汇报行。
// 行与子线程的归属在渲染时按 StartedAt 与行的区间实时计算（见 swarmTimelineCapsuleData），
// 所以胶囊状态仍每帧刷新；这里只固定各段的边界。
func insertSwarmCapsuleRows(items []timelineItem) []timelineItem {
	turns := swarmCapsuleRowTurns(items)
	if len(turns) < 2 {
		return items
	}
	// 多留 3 个位置：末尾胶囊行、会话用量行与“回复中”指示都在这一步之后追加，
	// 预留后它们都落在同一块内存里，不再一帧把整表反复复制。
	res := make([]timelineItem, 0, len(items)+len(turns)-1+3)
	next := 0
	for i, item := range items {
		if next < len(turns) && i == turns[next].itemIndex {
			if next > 0 {
				prev := turns[next-1]
				// 第一段行不设下界，更早启动的子线程也归它；后续行从上一个边界的时间起算。
				var from time.Time
				if next > 1 {
					from = swarmCapsuleRowBoundary(turns, next-1)
				}
				res = append(res, swarmCapsuleRow(swarmCapsuleRowKeyFor(turns, next), prev.turnIndex, swarmCapsuleTurnSpan{
					From:  from,
					Until: swarmCapsuleRowBoundary(turns, next),
				}))
			}
			next++
		}
		res = append(res, item)
	}
	return res
}

// swarmTimelineCapsuleData 返回某条胶囊行要展示的主会话 ID 与它的子线程列表。
// 当前会话不是主会话（自身就是 bee）、Swarm 未启用或这条行没有归它的子线程时返回空。
// 归属按“状态版本 + 回合区间”缓存：同一版本里每条行只过滤一次，
// 之后每帧直接用缓存，渲染路径不再对每条行全量遍历 bees。
func swarmTimelineCapsuleData(s *chatState, span swarmCapsuleTurnSpan) (string, []SwarmBee) {
	hp := getSwarmPanelState(s)
	if hp == nil {
		return "", nil
	}
	swarmPanelMu.Lock()
	view := hp.viewLocked()
	if len(view.active) == 0 {
		swarmPanelMu.Unlock()
		return "", nil
	}
	if data, ok := view.capsules[span]; ok {
		swarmPanelMu.Unlock()
		return data.leadID, data.bees
	}
	// 计算期间不持锁；回填前再用指针比对确认视图没被换掉。
	swarmPanelMu.Unlock()

	var data swarmCapsuleRowData
	matched := make([]SwarmBee, 0, len(view.active))
	for _, h := range view.active {
		if swarmBeeInCapsuleSpan(h, span) {
			matched = append(matched, h)
		}
	}
	if len(matched) > 0 {
		data = swarmCapsuleRowData{leadID: view.leadID, bees: matched}
	}

	swarmPanelMu.Lock()
	if hp.view == view {
		if view.capsules == nil {
			view.capsules = make(map[swarmCapsuleTurnSpan]swarmCapsuleRowData, 8)
		} else if len(view.capsules) >= swarmCapsuleCacheMax {
			// 回合行数异常多时清空重来，宁可多算一次也不让缓存无限增长。
			view.capsules = make(map[swarmCapsuleTurnSpan]swarmCapsuleRowData, 8)
		}
		view.capsules[span] = data
	}
	swarmPanelMu.Unlock()
	return data.leadID, data.bees
}

// swarmBeeInCapsuleSpan 判断一个子线程是否归这条胶囊行显示：没有 StartedAt 的子线程只落在
// 最新回合行；最新回合行不设上界；其余行必须上界可用且 StartedAt 落在 [From, Until) 内，
// 这样各行的区间互不重叠，同一个子线程不会出现在两行里。
func swarmBeeInCapsuleSpan(h SwarmBee, span swarmCapsuleTurnSpan) bool {
	if h.StartedAt.IsZero() {
		return span.Latest
	}
	if !span.From.IsZero() && h.StartedAt.Before(span.From) {
		return false
	}
	if span.Until.IsZero() {
		return span.Latest
	}
	return h.StartedAt.Before(span.Until)
}

// swarmReportedBeeTimes 返回“已被汇报的子代理名字 -> 汇报消息时间”，按 messagesRev 缓存：
// 胶囊行每帧都要判定哪些子代理的汇报已经出现在时间线里，逐帧重扫全部消息并解析汇报正文是纯浪费。
// 消息时间取不到时记零值，判定时按“只认名字”处理。返回值只读，调用方不得修改。
func (s *chatState) swarmReportedBeeTimes() map[string][]time.Time {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.swarmReportedValid && s.swarmReportedRev == s.messagesRev {
		return s.swarmReportedByName
	}
	reported := make(map[string][]time.Time)
	for _, m := range s.messages {
		if !isSwarmReportMessage(m) {
			continue
		}
		at, _ := messageTime(m)
		_, entries := parseSwarmReportCached(m.ID, m.Text)
		for _, e := range entries {
			if name := strings.TrimSpace(e.Name); name != "" {
				reported[name] = append(reported[name], at)
			}
		}
	}
	s.swarmReportedRev = s.messagesRev
	s.swarmReportedByName = reported
	s.swarmReportedValid = true
	return reported
}

// swarmCapsuleHideReported 过滤掉汇报已经出现在时间线里的子代理：普通会话里子代理完成后，
// 汇报行已经有一枚同名胶囊，胶囊行不必再重复显示同一枚；总管会话的汇报行本身已被隐藏，
// 胶囊行是唯一的展示，原样返回。
func swarmCapsuleHideReported(s *chatState, bees []SwarmBee) []SwarmBee {
	if s == nil || len(bees) == 0 {
		return bees
	}
	if swarmReportMergedIntoLeadCapsule(s, getSwarmPanelState(s)) {
		return bees
	}
	reported := s.swarmReportedBeeTimes()
	if len(reported) == 0 {
		return bees
	}
	var kept []SwarmBee
	for i, bee := range bees {
		if swarmBeeReported(reported, bee) {
			// 有子线程被过滤时才另建切片，全都保留时直接返回原切片，不为每帧多分配一次。
			if kept == nil {
				kept = make([]SwarmBee, 0, len(bees))
				kept = append(kept, bees[:i]...)
			}
			continue
		}
		if kept != nil {
			kept = append(kept, bee)
		}
	}
	if kept == nil {
		return bees
	}
	return kept
}

// swarmBeeReported 判断某个子代理的汇报是否已经在时间线里出现：存在同名汇报消息，
// 且它的时间不早于该子代理的启动时间；子代理没有启动时间时只按名字判定。
// 这样同名子代理在后一轮重新派出（启动晚于旧汇报）时不会被误藏。
func swarmBeeReported(reported map[string][]time.Time, bee SwarmBee) bool {
	name := strings.TrimSpace(bee.Name)
	if name == "" {
		return false
	}
	for _, at := range reported[name] {
		if bee.StartedAt.IsZero() || at.IsZero() || !at.Before(bee.StartedAt) {
			return true
		}
	}
	return false
}

// swarmBeesPending 判断 Swarm 主会话是否还有运行中或排队中的子代理：
// 子代理（bee）自身不算；派发队列里有等待启动的、或任一子代理仍在运行，都算还没结束。
func swarmBeesPending(st SwarmState) bool {
	if st.IsBee {
		return false
	}
	if st.Queued > 0 {
		return true
	}
	for _, h := range st.Bees {
		if h.Status == "running" {
			return true
		}
	}
	return false
}

// swarmTurnEndDeferred 判断某一回合的完成块是否要推迟显示：Swarm 子代理还没回来时，
// 主会话派出子代理后自己这一轮就结束了，此时不该显示「已完成」，只推迟会话最后一轮。
func swarmTurnEndDeferred(s *chatState, turnIndex int) bool {
	if s == nil {
		return false
	}
	view := swarmStateViewFor(s)
	if view == nil || !view.pending {
		return false
	}
	// 最大回合号按消息版本缓存：这行对每个回合完成块都会调一次，
	// 每帧重新扫一遍全部消息是纯浪费。
	return turnIndex >= s.maxMessagesTurn()
}

// swarmCapsuleKey 返回某枚胶囊的稳定 Key：优先用 bee 的会话 ID，
// 没有会话 ID 时带上子线程自己的序号（不是行内下标），保证胶囊换行后 Key 不变，
// 也避免同名或空 ID 的子线程互相覆盖浮窗状态。
func swarmCapsuleKey(leadID string, h SwarmBee, idx int) string {
	id := strings.TrimSpace(h.ThreadID)
	if id == "" {
		id = fmt.Sprintf("#%d", idx)
	}
	return leadID + "|" + id
}

// swarmCapsuleName 返回胶囊用的名字：名字缺失时统一退回「子线程」，头像与文案都用它。
func swarmCapsuleName(h SwarmBee) string {
	if name := strings.TrimSpace(h.Name); name != "" {
		return name
	}
	return "子线程"
}

// swarmCapsuleTitle 返回胶囊的主文案，如「Aspen 已完成」；名字缺失时用「子线程」。
func swarmCapsuleTitle(h SwarmBee) string {
	return swarmCapsuleName(h) + " " + swarmStatusLabel(h.Status)
}

// swarmCapsuleElapsed 返回胶囊上的耗时文案：终态子线程取开始到结束，
// 运行中的子线程取开始到当前（点一次即刷新），拿不到开始时间时返回空串。
func swarmCapsuleElapsed(h SwarmBee) string {
	if d := formatSwarmShortDuration(h.StartedAt, h.FinishedAt); d != "" {
		return "耗时 " + d
	}
	return ""
}

// swarmCapsuleReport 返回胶囊浮窗要展示的汇报正文：优先用 bee 的汇报摘要（工作汇报），
// 没有时依次退回当前活动与任务目标；都为空时返回空串，浮窗显示「暂无汇报」。
func swarmCapsuleReport(h SwarmBee) string {
	for _, text := range []string{h.Summary, h.Activity, h.Task} {
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// swarmCapsuleView 是一枚时间线胶囊要展示的内容。运行中的子线程胶囊与已完成的汇报胶囊
// 共用同一套外观（高度、圆角、内边距、头像尺寸与字号），差别只在主文案、状态点颜色与浮窗正文。
type swarmCapsuleView struct {
	Label        string   // 无障碍标签，也用于按标签定位胶囊
	Name         string   // 头像里的首字符来源
	Text         string   // 主文案，如「Aspen 运行中」「Cove 已完成工作」
	StatusColor  ui.Color // 状态点颜色
	TierName     string   // 档位展示名，为空时不显示档位徽章
	TierColor    ui.Color // 档位徽章的强调色
	TierReadOnly bool     // 档位是否只读，只读时徽章带锁图标
	Elapsed      string   // 耗时文案，为空时不显示
}

// renderSwarmCapsuleFlow 渲染横向流式的胶囊容器：胶囊从左到右排列，放不下时自动换行；
// 运行中的子线程行与已完成的汇报行共用这一套对齐与间距。
func renderSwarmCapsuleFlow(c *ui.Context, key string, children func()) ui.Element {
	return ui.Row(c.Key(key)).
		FillWidth().
		Wrap().
		Gap(Space2).
		AlignItems(ui.Center).
		Children(children)
}

// renderSwarmCapsuleButton 用统一的外观渲染一枚时间线胶囊，并挂上它的详情浮窗。
// opened 是调用方持有的浮窗开合状态，dismissed 表示上一帧浮窗刚被点外部或 Esc 关闭；
// body 按浮窗可用的最大高度渲染浮窗正文。
// 返回本帧结束时的开合状态与「浮窗刚被关闭」标记，由调用方写回自己的状态表。
func renderSwarmCapsuleButton(c *ui.Context, key string, view swarmCapsuleView, opened, dismissed bool, body func(maxH float32)) (open, wasDismissed bool) {
	open = opened
	btn := ui.ButtonBase(c.Key("swarm-capsule-"+key)).
		Label(view.Label).
		MaxWidthPercent(100).
		Radius(RadiusXL).
		Padding(Space1, Space3, Space1, Space3).
		Border(1, ThemeBorder(c)).
		Background(ThemeSurface(c))
	if opened {
		btn.Background(ThemeSelected(c))
	} else if btn.Hovered() {
		btn.Background(ThemeHover(c))
	}
	if btn.Clicked() {
		// 浮窗打开时点胶囊，PopoverBase 已先把状态置为关闭并把这次按下传给按钮；
		// 这里不能再把它打开，否则会互相抵消（同 renderSwarmReportCapsule）。
		open = shouldKeepSwarmReportOpen(opened, dismissed)
	}
	btn.Children(func() {
		ui.Row(c.Key("swarm-capsule-content-" + key)).
			AlignItems(ui.Center).
			Gap(Space2).
			Children(func() {
				renderSwarmPersonaIcon(c, "swarm-capsule-avatar-"+key, view.Name, swarmMentionIconSize())
				ui.Box(c.Key("swarm-capsule-dot-"+key)).
					Size(Space2, Space2).
					Radius(RadiusPill).
					Background(view.StatusColor)
				ui.Text(c, view.Text).
					FontSize(scaledFont(TypeFootnote)).
					FontWeight(600).
					TextColor(ThemePrimaryText(c)).
					SingleLine()
				// 档位徽章紧跟主文案，和 web 端子代理胶囊的信息顺序一致。
				if view.TierName != "" {
					renderSwarmTierChipColored(c, "swarm-capsule-tier-"+key, view.TierName, view.TierColor, view.TierReadOnly)
				}
				if view.Elapsed != "" {
					ui.Text(c, view.Elapsed).
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine()
				}
				ui.Icon(c, IconChevronRight()).Size(9, 9).TextColor(ThemeSecondaryText(c))
			})
	})

	_, winHeight := c.Size()
	maxH := max(150, winHeight-150)
	if maxH > 520 {
		maxH = 520
	}
	panel := appPopover(c.Key("swarm-capsule-popover-"+key), btn, &open, nil, func() {
		col := ui.Column(c.Key("swarm-capsule-popover-col-"+key)).
			Width(swarmCapsulePopoverWidth).
			Radius(RadiusLG).
			Background(ThemeSurface(c)).
			Border(1, ThemeBorder(c))
		applyElevation(c, col, ElevPopover).Children(func() {
			body(maxH)
		})
	})
	return open, opened && !open && panel.Dismissed()
}

// renderSwarmTimelineCapsules 渲染一条子线程胶囊行：每枚胶囊对应一个子线程，
// 显示头像、名字、当前状态与耗时；点开弹出汇报内容并提供「打开浮窗」按钮。
// rowID 是该行在时间线里的 Key（按段归位的行带边界条目 id），行内 flow 由它区分，
// 避免多条胶囊行的元素 Key 互相覆盖。
func renderSwarmTimelineCapsules(c *ui.Context, s *chatState, rowID, leadID string, bees []SwarmBee) {
	if len(bees) == 0 {
		return
	}
	renderSwarmCapsuleFlow(c, rowID+"-row", func() {
		for _, h := range bees {
			renderSwarmCapsule(c, s, leadID, h, swarmCapsuleKey(leadID, h, h.Index))
		}
	})
}

// renderSwarmCapsule 渲染单枚子线程胶囊及其汇报浮窗：外观与已完成的汇报胶囊完全一致，
// 只有状态点颜色、主文案与浮窗正文不同。
func renderSwarmCapsule(c *ui.Context, s *chatState, leadID string, h SwarmBee, key string) {
	swarmFloatMu.Lock()
	opened := swarmCapsuleOpen[key]
	dismissed := swarmCapsuleDismissed[key]
	swarmFloatMu.Unlock()

	// 浮窗里的「打开浮窗」按钮会在渲染过程中请求关闭浮窗，这里用一帧内的标志把它带回去，
	// 避免渲染结束后写回状态时又把浮窗打开。
	closeRequested := false
	// 档位展示名留空时不显示徽章；档位 ID 为空则不会有徽章。
	tierName := ""
	if h.Tier != "" {
		tierName = swarmTierName(s, h.Tier)
	}
	open, wasDismissed := renderSwarmCapsuleButton(c, key, swarmCapsuleView{
		Label:        "子线程 " + swarmCapsuleName(h) + " " + swarmStatusLabel(h.Status),
		Name:         swarmCapsuleName(h),
		Text:         swarmCapsuleTitle(h),
		StatusColor:  swarmStatusColor(c, h.Status),
		TierName:     tierName,
		TierColor:    swarmTierColor(c, h.Tier),
		TierReadOnly: h.ReadOnly,
		Elapsed:      swarmCapsuleElapsed(h),
	}, opened, dismissed, func(maxH float32) {
		ui.Column(c.Key("swarm-capsule-popover-body-"+key)).
			FillWidth().
			Padding(Space4, Space5, Space4, Space5).
			Gap(Space4).
			Children(func() {
				renderSwarmCapsuleHeader(c, s, h, key)
				if task := strings.TrimSpace(h.Task); task != "" {
					ui.Column(c.Key("swarm-capsule-task-"+key)).
						FillWidth().
						Padding(Space2, Space3).
						Radius(RadiusMD).
						Background(ThemeHover(c).Alpha(0.35)).
						Children(func() {
							ui.Text(c, "任务目标: "+task).
								FontSize(scaledFont(TypeFootnote)).
								TextColor(ThemePrimaryText(c).Alpha(0.88))
						})
				}
				report := swarmCapsuleReport(h)
				if report == "" {
					ui.Text(c, "暂无汇报").
						FontSize(scaledFont(TypeFootnote)).
						TextColor(ThemeSecondaryText(c))
				} else {
					ui.Text(c, "工作汇报").
						FontSize(scaledFont(TypeCaption)).
						FontWeight(600).
						TextColor(ThemeAccent(c))
					ui.Scroll(c.Key("swarm-capsule-report-" + key)).
						FillWidth().
						MaxHeight(maxH * 0.6).
						Children(func() {
							// 汇报正文按 Markdown 渲染（粗体、列表、表格、行内代码都还原）：
							// 静态整段呈现，字号与原来的正文一致，Markdown 视图自带选区复制。
							markdown.View(c, report, swarmReportMarkdownOptions(c, s, TypeFootnote))
						})
				}
				if strings.TrimSpace(h.MergeError) != "" {
					ui.Text(c, "合并错误: "+h.MergeError).
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeDanger(c))
				}
				renderSwarmCapsuleOpenFloatButton(c, s, leadID, h, key, func() { closeRequested = true })
			})
	})
	if closeRequested {
		open = false
	}
	swarmFloatMu.Lock()
	swarmCapsuleOpen[key] = open
	swarmCapsuleDismissed[key] = wasDismissed
	swarmFloatMu.Unlock()
}

// renderSwarmCapsuleHeader 渲染浮窗头部：头像、名字与档位徽章，以及状态与耗时。
func renderSwarmCapsuleHeader(c *ui.Context, s *chatState, h SwarmBee, key string) {
	ui.Row(c.Key("swarm-capsule-header-" + key)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Children(func() {
			renderSwarmGlyph(c, "capsule-pop-"+key, h, 22)
			ui.Column(c.Key("swarm-capsule-meta-" + key)).
				Grow(1).
				Gap(2).
				Children(func() {
					name := strings.TrimSpace(h.Name)
					if name == "" {
						name = "子线程"
					}
					ui.Row(c.Key("swarm-capsule-name-row-" + key)).
						AlignItems(ui.Center).
						Gap(Space2).
						Children(func() {
							ui.Text(c, name).
								FontSize(scaledFont(TypeBody)).
								FontWeight(600).
								TextColor(ThemePrimaryText(c)).
								SingleLine()
							// 浮窗头部同样展示档位，只读档位带锁图标。
							if h.Tier != "" {
								renderSwarmTierChip(c, "swarm-capsule-pop-tier-"+key, swarmTierName(s, h.Tier), h.Tier, h.ReadOnly)
							}
						})
					meta := swarmStatusLabel(h.Status)
					if elapsed := swarmCapsuleElapsed(h); elapsed != "" {
						meta += " · " + elapsed
					}
					ui.Text(c, meta).
						FontSize(scaledFont(TypeCaption)).
						TextColor(swarmStatusColor(c, h.Status)).
						SingleLine()
				})
		})
}

// renderSwarmCapsuleOpenFloatButton 渲染浮窗里的「打开浮窗」按钮：
// 点击后解除浮窗的关闭记录并把该子线程设为选中，让浮窗重新显示这个子线程。
func renderSwarmCapsuleOpenFloatButton(c *ui.Context, s *chatState, leadID string, h SwarmBee, key string, onOpened func()) {
	btn := ui.ButtonBase(c.Key("swarm-capsule-open-float-"+key)).
		Label("打开浮窗").
		Radius(RadiusSM).
		Padding(Space1, Space3).
		Background(ThemeAccent(c).Alpha(0.14))
	if btn.Hovered() {
		btn.Background(ThemeAccent(c).Alpha(0.24))
	}
	if btn.Clicked() && leadID != "" {
		if onOpened != nil {
			onOpened()
		}
		openSwarmFloatForBee(leadID, h.ThreadID)
		s.invalidate()
	}
	btn.Children(func() {
		ui.Text(c, "打开浮窗").
			FontSize(scaledFont(TypeFootnote)).
			FontWeight(500).
			TextColor(ThemeAccent(c))
	})
}
