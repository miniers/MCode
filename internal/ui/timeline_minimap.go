package mcodeui

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// 时间线迷你地图：时间线右缘的一根窄轨道，
// 每根刻度对应一条用户消息，指针悬停时立即在轨道左侧弹出预览卡片（标题与摘要），
// 点击跳到该消息；回复、工具调用、计划历史不生成刻度。
// 轨道以浮层方式贴在时间线上（AttachTo），不占列表布局宽度。

// minimapMinEntries 是显示迷你地图所需的最少条目数：提问不足 4 条时轨道不出现，
// 短会话直接滚轮翻更省事。
const minimapMinEntries = 4

// 刻度与轨道的几何常量。轨道高度有预算：间距先在宽松（8pt）与最紧（4pt）之间压缩，
// 仍放不下时只保留最近的一段连续条目，最上面一格用「+N」表示被隐藏的更早条目数，
// 保证上千条消息的会话也只渲染有限个元素。
//
// 每根刻度占一整行（高度 = 刻度 + 间距），行本身就是悬停与点击的命中区，所以轨道不留
// 纵向内边距也不留行间距：指针在行与行之间移动不会掉出轨道，滑过时预览卡片连续切换。
const (
	minimapRailWidth        float32 = 24    // 轨道容器宽度（固定，宽度变化只在刻度内发生）
	minimapRailPadX         float32 = 2     // 轨道左右内边距（刻度最宽 20 + 2*2 = 24）
	minimapTickHeight       float32 = 3     // 单根刻度高度
	minimapTickWidthMin     float32 = 8     // 最短刻度宽度（权重下限时）
	minimapTickWidthMax     float32 = 20    // 最长刻度宽度（权重上限、悬停或当前条目时）
	minimapRailSpacingRoomy float32 = 8     // 宽松间距（刻度较少时）
	minimapRailSpacingTight float32 = 4     // 最紧间距（刻度较多时）
	minimapRailMaxHeight    float32 = 470   // 轨道总高预算
	minimapCardWidth        float32 = 300   // 悬停预览卡片宽度
	minimapTitleWords               = 8     // 标题取消息的前多少个词
	minimapSnippetLimit             = 160   // 摘要截取的字符数上限
	minimapWeightBase               = 420.0 // 权重公式的分母：消息达到该字符数时满宽
	minimapWeightMin                = 0.15  // 权重下限
	minimapCacheLimit               = 400   // 条目文本缓存上限，超限整体清空
)

// 「+N」格与空闲变淡的常量。
const (
	minimapPlusRowHeight float32       = 14              // 「+N」格高度：要放下一行小字，比刻度行高
	minimapPlusFontSize  float32       = 9               // 「+N」的字号
	minimapIdleDelay     time.Duration = 2 * time.Second // 列表静止多久后轨道开始变淡
	minimapIdleAlpha     float32       = 0.4             // 变淡后的整体不透明度（仍可点击）
	minimapFadeLimit     int           = 64              // 变淡状态表的会话数上限，超限整体清空
)

// minimapEntry 表示时间线迷你地图上的一根刻度（对应用户发送的一条消息）。
type minimapEntry struct {
	ID      string  // 消息 ID，同时作为缓存键与渲染 key
	Index   int     // 对应 timelineItems 的下标，用于点击跳转
	Ordinal int     // 该条目在完整 entries 中的 1 基序号（抽样前），用于刻度标签
	Title   string  // 标题：消息前 8 个词，空文本时按图片附件数或附件名兜底
	Snippet string  // 折叠空白后的前 160 个字符；与标题相同则为空
	Weight  float64 // 0.15…1，按消息长度决定刻度宽度
}

// minimapEntryCache 按消息 ID 缓存条目的文本字段（标题、摘要、权重）：已发送的用户消息不变，
// 避免长对话每帧重算。上限 400，超限整体清空即可。
var minimapEntryCache = struct {
	mu    sync.Mutex
	items map[string]minimapEntry
}{items: make(map[string]minimapEntry)}

// buildMinimapEntries 为时间线上的用户消息生成迷你地图条目：只为用户发送的单条消息生成刻度。
// Index 与 Ordinal 每帧按当前时间线重新写入（插入计划历史态会改变下标；缓存只存文本字段）。
func buildMinimapEntries(items []timelineItem) []minimapEntry {
	var entries []minimapEntry
	for i := range items {
		item := items[i]
		if item.kind != timelineItemSingle || item.msg.Role != "user" {
			continue
		}
		entry := minimapEntryFor(item.msg)
		entry.Index = i
		entry.Ordinal = len(entries) + 1
		entries = append(entries, entry)
	}
	return entries
}

// minimapEntryFor 返回一条用户消息的条目，文本字段带缓存；无 ID 的消息不缓存，直接计算。
func minimapEntryFor(msg Message) minimapEntry {
	if msg.ID != "" {
		if cached, ok := minimapCacheLookup(msg.ID); ok {
			return cached
		}
	}
	entry := minimapEntry{
		ID:     msg.ID,
		Title:  minimapTitle(msg),
		Weight: minimapWeight(msg.Text),
	}
	entry.Snippet = minimapSnippet(msg.Text, minimapSnippetLimit)
	if entry.Snippet == entry.Title {
		entry.Snippet = ""
	}
	if msg.ID != "" {
		minimapCacheStore(entry)
	}
	return entry
}

func minimapCacheLookup(id string) (minimapEntry, bool) {
	minimapEntryCache.mu.Lock()
	defer minimapEntryCache.mu.Unlock()
	entry, ok := minimapEntryCache.items[id]
	return entry, ok
}

func minimapCacheStore(entry minimapEntry) {
	minimapEntryCache.mu.Lock()
	defer minimapEntryCache.mu.Unlock()
	if len(minimapEntryCache.items) >= minimapCacheLimit {
		minimapEntryCache.items = make(map[string]minimapEntry)
	}
	minimapEntryCache.items[entry.ID] = entry
}

// minimapTitle 返回刻度标题：消息前 8 个词；
// 文本为空时依次按图片附件数量、「N 张图片」、首个附件名或「消息」兜底。
func minimapTitle(msg Message) string {
	if title := minimapFirstWords(msg.Text, minimapTitleWords); title != "" {
		return title
	}
	images := 0
	for _, media := range msg.Media {
		if isImageMedia(media) {
			images++
		}
	}
	switch {
	case images == 1:
		return "1 张图片"
	case images > 1:
		return fmt.Sprintf("%d 张图片", images)
	}
	if len(msg.Media) > 0 {
		if name := strings.TrimSpace(msg.Media[0].Name); name != "" {
			return name
		}
	}
	return "消息"
}

// minimapFirstWords 取文本前 limit 个空白分隔的词；词数超出时以省略号结尾。
func minimapFirstWords(text string, limit int) string {
	words := strings.Fields(text)
	if len(words) == 0 || limit <= 0 {
		return ""
	}
	head := words
	if len(head) > limit {
		head = head[:limit]
	}
	joined := strings.Join(head, " ")
	if len(words) > limit {
		return joined + "…"
	}
	return joined
}

// minimapSnippet 折叠空白后截取前 limit 个字符（按字符而非字节计数，超出以省略号结尾）。
func minimapSnippet(text string, limit int) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if limit <= 0 || len(runes) <= limit {
		return flat
	}
	return string(runes[:limit]) + "…"
}

// minimapWeight 按消息长度计算刻度宽度权重（0.15…1）。
func minimapWeight(text string) float64 {
	weight := float64(len([]rune(text))) / minimapWeightBase
	if weight > 1 {
		return 1
	}
	if weight < minimapWeightMin {
		return minimapWeightMin
	}
	return weight
}

// minimapActiveIndex 返回视口对应的条目下标：最后一个 Index <= firstVisible 的条目，
// 没有则取第一个。列表尚未布局（lastVisible 在 firstVisible 之上）时按顶部处理。
func minimapActiveIndex(entries []minimapEntry, firstVisible, lastVisible int) int {
	if len(entries) == 0 {
		return -1
	}
	if lastVisible < firstVisible {
		firstVisible = 0
	}
	active := 0
	for i := range entries {
		if entries[i].Index <= firstVisible {
			active = i
		}
	}
	return active
}

// minimapMaxTicks 返回轨道高度预算内能放下的刻度数量（按最紧间距、每根刻度一行计算）。
func minimapMaxTicks() int {
	return minimapFits(minimapRailMaxHeight, minimapRailSpacingTight)
}

// minimapFits 返回高度预算 height 内、按 spacing 间距能放下的刻度行数（至少一行）。
func minimapFits(height, spacing float32) int {
	n := int(height / (minimapTickHeight + spacing))
	if n < 1 {
		n = 1
	}
	return n
}

// minimapRow 是轨道上的一行：要么是一根刻度，要么是顶部表示「+N」的那一格。
type minimapRow struct {
	Entry  minimapEntry // 该行代表的提问；「+N」格是跳转目标（最早一条被隐藏的条目）
	Plus   int          // > 0 表示这一行是「+N」：被隐藏的更早条目数
	Active bool         // 这一行是否代表视口当前位置（视口落在被隐藏的区间时是「+N」格）
}

// minimapPlan 是一帧的轨道规划结果：从上到下要渲染的行、行间距与被隐藏的条目数。
type minimapPlan struct {
	Rows    []minimapRow
	Spacing float32
	Hidden  int
}

// minimapRowsAll 把全部条目排成刻度行，并标出当前条目。
func minimapRowsAll(entries []minimapEntry, active int) []minimapRow {
	rows := make([]minimapRow, len(entries))
	for i := range entries {
		rows[i] = minimapRow{Entry: entries[i], Active: i == active}
	}
	return rows
}

// minimapPlanRows 规划轨道上的行：条目放得下就全部显示（间距先宽松、不够再压到最紧）；
// 仍然放不下就只保留最近的一段连续条目，最上面一格换成「+N」，表示还有多少条更早的提问被隐藏。
// 视口落在被隐藏的区间时点亮「+N」那一格，当前条目的高亮不会因为截断而消失。
func minimapPlanRows(entries []minimapEntry, active int) minimapPlan {
	if len(entries) == 0 {
		return minimapPlan{}
	}
	if len(entries) <= minimapMaxTicks() {
		return minimapPlan{Rows: minimapRowsAll(entries, active), Spacing: minimapRailSpacing(len(entries))}
	}
	// 顶部留一格「+N」，其余放最近的条目。
	keep := minimapFits(minimapRailMaxHeight-minimapPlusRowHeight, minimapRailSpacingTight)
	if keep > len(entries)-1 {
		keep = len(entries) - 1
	}
	start := len(entries) - keep
	rows := make([]minimapRow, 0, keep+1)
	rows = append(rows, minimapRow{Entry: entries[0], Plus: start, Active: active >= 0 && active < start})
	for i := start; i < len(entries); i++ {
		rows = append(rows, minimapRow{Entry: entries[i], Active: i == active})
	}
	return minimapPlan{Rows: rows, Spacing: minimapRailSpacingTight, Hidden: start}
}

// minimapActivity 是某个会话上一帧的列表活动：可见范围、高亮条目与最近一次滚动的时间。
type minimapActivity struct {
	first, last, active int
	movedAt             time.Time
}

// minimapFadeState 按会话记录列表有没有在滚动，用来判断轨道该不该变淡。与条目文本缓存一样
// 用互斥锁保护（浮窗与主时间线可能在同一帧各自渲染轨道），会话数超限整体清空。
var minimapFadeState = struct {
	mu    sync.Mutex
	items map[string]minimapActivity
}{items: make(map[string]minimapActivity)}

// minimapTouch 记录本帧列表的可见范围与高亮条目，返回距上一次滚动过了多久：
// 可见范围或高亮条目变了就算刚滚过（滚动总会先反映在这两者上）。
// 没有会话 ID 时不记账，返回 0 表示刚活动过。
func minimapTouch(threadID string, first, last, active int, now time.Time) time.Duration {
	if threadID == "" {
		return 0
	}
	minimapFadeState.mu.Lock()
	defer minimapFadeState.mu.Unlock()
	prev, ok := minimapFadeState.items[threadID]
	movedAt := prev.movedAt
	if !ok || prev.first != first || prev.last != last || prev.active != active {
		movedAt = now
	}
	if !ok && len(minimapFadeState.items) >= minimapFadeLimit {
		minimapFadeState.items = make(map[string]minimapActivity)
	}
	minimapFadeState.items[threadID] = minimapActivity{first: first, last: last, active: active, movedAt: movedAt}
	return now.Sub(movedAt)
}

// minimapOpacity 返回轨道整体的不透明度：指针在轨道上、或列表刚滚过（距上次滚动不足
// minimapIdleDelay）时完全不透明；静止超过该时长后降到 minimapIdleAlpha，但仍可点击。
func minimapOpacity(hovered bool, sinceRoll time.Duration) float32 {
	if hovered || sinceRoll < minimapIdleDelay {
		return 1
	}
	return minimapIdleAlpha
}

// minimapShown 报告提问条数是否够显示轨道（门槛见 minimapMinEntries）。
func minimapShown(entries []minimapEntry) bool {
	return len(entries) >= minimapMinEntries
}

// minimapRailSpacing 返回刻度间距：条目少时用宽松间距，条目多时压缩到最紧间距。
func minimapRailSpacing(count int) float32 {
	// 每根刻度占一行：count 行、每行刻度加间距，就是轨道需要的总高。
	roomy := float32(count) * (minimapTickHeight + minimapRailSpacingRoomy)
	if count <= 1 || roomy <= minimapRailMaxHeight {
		return minimapRailSpacingRoomy
	}
	return minimapRailSpacingTight
}

// minimapTickLabel 返回刻度的可访问标签：只含序号，不含消息正文。
// 标题与摘要常驻可见会让测试框架（ui.Tester 的 HasText/Texts）把刻度当成消息内容，
// 截断后保留的刻度会干扰按消息正文断言的用例；正文只出现在悬停时的预览卡片里。
func minimapTickLabel(entry minimapEntry) string {
	return fmt.Sprintf("跳到第 %d 条提问", entry.Ordinal)
}

// minimapTickTooltip 拼出刻度行的辅助说明：标题一行，摘要另起一行（摘要与标题相同时只显示标题）。
// 它挂在刻度行的 Description 上供辅助技术朗读，可见的正文由悬停预览卡片承担。
func minimapTickTooltip(entry minimapEntry) string {
	if entry.Snippet == "" {
		return entry.Title
	}
	return entry.Title + "\n" + entry.Snippet
}

// minimapPlusLabel 返回「+N」格的辅助说明与悬停提示：被隐藏的更早提问条数。
func minimapPlusLabel(hidden int) string {
	return fmt.Sprintf("更早还有 %d 条", hidden)
}

// renderTimelineMinimap 绘制迷你地图的刻度轨道：
// 轨道不是浮层，而是时间线容器内绝对定位的子元素，贴右缘、纵向居中，只负责定位、不带背景；
// 这样它始终排在所有浮层（PopoverBase 浮窗等）下面，不会盖住先打开的浮窗，也不占用列表宽度。
// 一根刻度对应一条用户消息，当前条目用强调色高亮，指针进入轨道后刻度整体变亮加宽，
// 悬停的那根立即在轨道左侧弹出预览卡片（标题与摘要），点击跳到该消息。
// 只有预览卡片走 Overlay；条目不足门槛时不渲染；长会话先压缩间距、再截断成最近的一段，
// 渲染的行数有上限，被隐藏的更早条目由最上面的「+N」一格代表。
// 指针不在轨道上且列表静止超过 minimapIdleDelay 后，轨道降到 minimapIdleAlpha，但仍可点击。
func renderTimelineMinimap(c *ui.Context, s *chatState, entries []minimapEntry) {
	if c == nil || s == nil || !minimapShown(entries) {
		return
	}

	first, last := s.chatListState.Visible()
	active := minimapActiveIndex(entries, first, last)
	plan := minimapPlanRows(entries, active)
	sinceRoll := minimapTouch(s.selectedThreadID, first, last, active, c.Now())

	var hoveredEntry minimapEntry
	var hoveredRow ui.Element
	hasHovered := false

	ui.Column(c.Key("timeline-minimap-wrap")).
		Absolute().Right(6).Top(0).Bottom(0).Width(minimapRailWidth).
		Justify(ui.Center).
		PassThrough().
		Children(func() {
			rail := ui.Column(c.Key("timeline-minimap")).
				Width(minimapRailWidth).
				Padding(0, minimapRailPadX).
				Gap(0).
				AlignItems(ui.End).
				Label("会话时间线迷你地图")
			// 悬停读的是上一帧的状态（mygo 的 Hovered 就是这样），本轮真正悬停的那根刻度
			// 还要看行自己的 Hovered；两者只用来决定提亮程度与是否弹卡片。
			railHovered := rail.Hovered()
			dim := minimapOpacity(railHovered, sinceRoll)
			if dim >= 1 && !railHovered {
				// 还没到变淡的时间点：只在到点时重绘一次（和「思考中…」低频重建一样用 c.After，
				// 不按帧刷新）；指针进出与滚动本来就会带来新的帧。
				if wait := minimapIdleDelay - sinceRoll; wait > 0 {
					c.After(wait)
				}
			}
			rail.Children(func() {
				for _, row := range plan.Rows {
					el, hovered := renderTimelineMinimapRow(c, s, row, plan.Spacing, railHovered, dim)
					if hovered && row.Plus == 0 {
						hoveredEntry, hoveredRow, hasHovered = row.Entry, el, true
					}
				}
			})
		})

	// 预览卡片仍走 Overlay（AttachTo 到悬停的那根刻度行），它是唯一需要盖在上层的部分。
	if hasHovered {
		ui.Overlay(c, func() {
			renderTimelineMinimapCard(c, hoveredEntry, hoveredRow)
		})
	}
}

// renderTimelineMinimapRow 渲染轨道上的一行：刻度（对应一条提问）或顶部的「+N」一格。
// 整行都是命中区（高度 = 刻度 + 间距），刻度在行内垂直居中、靠右对齐，宽度随消息长度（Weight）变化；
// 当前条目用强调色满宽高亮，指针进入轨道后其余刻度变亮加宽，悬停的那根最宽也最亮。
// dim 是变淡时乘进不透明度的系数，指针下的那根始终清晰。
// 返回行元素与它是否被悬停（「+N」不弹预览卡片），供调用方决定是否弹预览卡片。
func renderTimelineMinimapRow(c *ui.Context, s *chatState, row minimapRow, spacing float32, railHovered bool, dim float32) (ui.Element, bool) {
	if row.Plus > 0 {
		return renderTimelineMinimapMore(c, s, row, dim), false
	}
	entry := row.Entry
	el := ui.Row(c.Key("timeline-minimap-row-" + entry.ID)).
		Width(minimapRailWidth - 2*minimapRailPadX).
		Height(minimapTickHeight + spacing).
		AlignItems(ui.Center).
		Justify(ui.End).
		Label(minimapTickLabel(entry)).
		Description(minimapTickTooltip(entry))
	hovered := el.Hovered()

	base := minimapTickWidthMin + float32(entry.Weight)*(minimapTickWidthMax-minimapTickWidthMin)
	width := base
	alpha := float32(0.28)
	accent := false
	switch {
	case hovered:
		// 悬停优先于当前条目的强调色
		width = minimapTickWidthMax
		alpha = 0.95
	case row.Active:
		width = minimapTickWidthMax
		alpha = 1
		accent = true
	case railHovered:
		// 指针在轨道上时其余刻度也稍微变亮加宽，方便扫读
		width = min(minimapTickWidthMax, base+2)
		alpha = 0.4
	}
	if !hovered {
		// 空闲变淡时整体降透明度，指针下的那根刻度不跟着淡
		alpha *= dim
	}
	color := ThemePrimaryText(c).Alpha(alpha)
	if accent {
		color = ThemeAccent(c).Alpha(alpha)
	}

	el.Children(func() {
		ui.Box(c.Key("timeline-minimap-tick-" + entry.ID)).
			Width(width).
			Height(minimapTickHeight).
			Radius(RadiusPill).
			Background(color)
	})

	index := entry.Index
	el.OnClick(func() {
		// 跳到目标块时把目标带到顶部。mygo 的 ListState 只有按行滚动
		// （ScrollTo / ScrollIntoView / ScrollToEnd），没有带像素偏移的滚动 API。
		s.chatListState.ScrollTo(index, ui.Start)
	})
	return el, hovered
}

// renderTimelineMinimapMore 渲染轨道最上面的「+N」一格：N 是被隐藏的更早条目数，
// 悬停提示写「更早还有 N 条」，点击跳到最早一条被隐藏的条目；
// 视口落在被隐藏的区间时这一格点亮，免得当前条目的高亮因为截断而消失。
func renderTimelineMinimapMore(c *ui.Context, s *chatState, row minimapRow, dim float32) ui.Element {
	hint := minimapPlusLabel(row.Plus)
	el := ui.Row(c.Key("timeline-minimap-more")).
		Width(minimapRailWidth - 2*minimapRailPadX).
		Height(minimapPlusRowHeight).
		AlignItems(ui.Center).
		Justify(ui.End).
		Label(hint).
		Description(hint)
	hovered := el.Hovered()
	alpha := float32(0.55)
	switch {
	case hovered:
		alpha = 0.95
	case row.Active:
		alpha = 1
	}
	if !hovered {
		alpha *= dim
	}
	color := ThemePrimaryText(c).Alpha(alpha)
	if row.Active {
		color = ThemeAccent(c).Alpha(alpha)
	}
	el.Children(func() {
		// 「+」固定不动，只让数字滚动：隐藏条数变化时先右对齐再逐位滚动，
		// 位数不变的那几位原地不动。字号与颜色由父元素透给数字（rollingNumber 内部的
		// 文本不单独设字体，靠 mygo 的文字样式继承）。
		ui.Text(c, "+").
			FontSize(scaledFont(minimapPlusFontSize)).
			TextColor(color).
			SingleLine()
		rollingNumber(c, "timeline-minimap-more/plus", row.Plus).
			FontSize(scaledFont(minimapPlusFontSize)).
			TextColor(color)
	})
	index := row.Entry.Index
	el.OnClick(func() {
		s.chatListState.ScrollTo(index, ui.Start)
	})
	return el
}

// renderTimelineMinimapCard 在悬停刻度所在行的左侧弹出预览卡片：标题一行、摘要至多三行，
// 卡片右缘比刻度行再左 10pt，纵向以该行为中心（AttachTo 会把它夹在窗口可视区域内）。
// 卡片随手出现、随手消失（不像 Tooltip 那样要停留 600ms），指针可穿透它，不抢轨道与列表的悬停。
func renderTimelineMinimapCard(c *ui.Context, entry minimapEntry, row ui.Element) {
	column := ui.Column(c.Key("timeline-minimap-card")).
		Width(minimapCardWidth).
		Padding(Space3, Space4).
		Gap(Space0)
	column.Children(func() {
		ui.Text(c, entry.Title).
			FontSize(scaledFont(TypeBody)).
			FontWeight(600).
			TextColor(ThemePrimaryText(c)).
			SingleLine().
			Ellipsis("…")
		if entry.Snippet != "" {
			ui.Text(c, entry.Snippet).
				FontSize(scaledFont(TypeFootnote)).
				LineHeight(1.45).
				TextColor(ThemeSecondaryText(c)).
				MaxLines(3).
				Ellipsis("…")
		}
	})
	popoverCard(c, column).
		PassThrough().
		AttachTo(row, ui.AnchorLeft, ui.AnchorRight).
		Margin(0, Space3, 0, 0)
}
