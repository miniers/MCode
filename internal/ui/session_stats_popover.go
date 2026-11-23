package mcodeui

import (
	"fmt"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	// sessionStatsMinWidth 是浮层卡片的最小宽度。
	sessionStatsMinWidth float32 = 240
	// sessionStatsMaxWidth 是浮层卡片的最大宽度。
	sessionStatsMaxWidth float32 = 330
)

// sessionStatsOpens 记录各窗口「会话统计」浮层的开合状态，key 为窗口的 chatState。
// 与 threadChangeStatsStates 一样用包级 map，避免为一次性视图状态改动 chatState 字段表；
// 只在 UI 线程读写。
var (
	sessionStatsMu    sync.Mutex
	sessionStatsOpens = make(map[*chatState]bool)
)

// sessionStatsOpen 读取指定窗口的会话统计浮层是否打开。
func sessionStatsOpen(s *chatState) bool {
	if s == nil {
		return false
	}
	sessionStatsMu.Lock()
	defer sessionStatsMu.Unlock()
	return sessionStatsOpens[s]
}

// setSessionStatsOpen 写入指定窗口的会话统计浮层开合状态。
func setSessionStatsOpen(s *chatState, open bool) {
	if s == nil {
		return
	}
	sessionStatsMu.Lock()
	defer sessionStatsMu.Unlock()
	if !open {
		delete(sessionStatsOpens, s)
		return
	}
	sessionStatsOpens[s] = true
}

// sessionStatsRowInComposer 判断「会话统计」按钮是否由输入框上方的改动行承载：
// 有改动、没有排队消息、且不是浮动面板里的嵌套输入框时由改动行渲染，时间线底部整行跳过，
// 保证底部只占一行。没有满足条件时统计按钮落在时间线底部的统计行里。
func sessionStatsRowInComposer(s *chatState, threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	_, _, files, ok := threadChangeStatsFor(s, threadID)
	if !ok || files == 0 {
		return false
	}
	s.mu.Lock()
	embedded := s.embeddedBee
	queued := len(s.queue[threadID])
	s.mu.Unlock()
	return !embedded && queued == 0
}

// renderSessionStatsButton 渲染底部紧凑的「会话统计」文字按钮，点击展开完整会话统计浮层；
// 浮层由 appFlyoutPopover 承载。
func renderSessionStatsButton(c *ui.Context, s *chatState, threadID, key string) {
	renderSessionStatsButtonFloating(c, s, threadID, key, false)
}

func renderSessionStatsButtonFloating(c *ui.Context, s *chatState, threadID, key string, floating bool) {
	open := sessionStatsOpen(s)
	btn := ui.ButtonBase(c.Key(key+"-stats-btn")).
		Label("会话统计").
		Tooltip("查看会话统计").
		Height(ControlSM).
		Radius(RadiusLG).
		Padding(Space0, Space3).
		OnClick(func() {
			setSessionStatsOpen(s, !sessionStatsOpen(s))
		})
	{
		scale := motionPressScale(c, btn, "button-press-scale", btn.Pressed())
		btn.Scale(scale, scale)
	}
	composerFloatingTabBg(c, btn, btn.Hovered() || open, floating)
	btn.Children(func() {
		ui.Row(c.Key(key + "-stats-btn-content")).
			Gap(Space1).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, IconLayoutDashboard()).Size(inlineIconSize, inlineIconSize).TextColor(ThemeSecondaryText(c))
				ui.Text(c, "会话统计").
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c))
			})
	})

	// 局部开关在点外部或 Esc 时写回 false，再据此清空全局状态，
	// 避免按钮的取反与面板的关闭相互抵消。
	panelOpen := open
	background := ThemeSurfacePopover(c)
	native := flyoutHostWindow(c.Services()) != nil
	appFlyoutPopover(c, key+"-stats-popover", btn, &panelOpen, func() {
		setSessionStatsOpen(s, false)
	}, appFlyoutProps{Width: int(sessionStatsMaxWidth), Background: &background}, func(c *ui.Context) {
		renderSessionStatsPopover(c, s, threadID, key, native)
	})
	if open && !panelOpen {
		setSessionStatsOpen(s, false)
	}
}

// sessionStatsSummary 是会话统计浮层展示的一组数字，全部由已持久化的用量与消息推导。
type sessionStatsSummary struct {
	usage         ThreadUsage
	turns         int
	requests      int
	toolCalls     int
	topTools      []ToolCallCount
	totalDuration time.Duration
	// liveDuration 只供「总用时」展示：已完成轮次累计加上当前主回合的实时时长；
	// liveRunning 为真时每秒刷新。平均响应与输出速度仍用 totalDuration。
	liveDuration time.Duration
	liveRunning  bool
	avgResponse  time.Duration
	speed        string
	// speedSamples 是速度统计用的有效请求样本数，0 表示后端未提供样本（速度按整轮口径）。
	speedSamples int
	cacheHit     string
	changedFiles int
	additions    int
	deletions    int
	hasChanges   bool
	bees         int
	beeTokens    int
	beeToolCalls int
	costUSD      float64
	hasCost      bool
}

// collectSessionStats 汇总会话统计：用量来自 ThreadUsage，时长优先取后端记录的每轮耗时、
// 缺失时用消息时间戳推导，速度优先用各轮的逐请求样本，改动取改动统计缓存，
// 子代理取 Swarm 面板状态，费用按模型参考价估算。
func collectSessionStats(s *chatState, threadID string, now time.Time) sessionStatsSummary {
	var st sessionStatsSummary
	if s == nil {
		return st
	}
	s.mu.Lock()
	st.usage = s.threadUsage
	provider, model := threadAgentLocked(s, threadID)
	providers := s.providers
	st.totalDuration = sessionDurationForTurns(s.messages, s.threadUsage.Turns)
	start, running := sessionLiveTurnLocked(s, threadID)
	if running {
		// 当前轮的贡献整体换成实时值：先从副本里去掉这一轮的消息与用量再累计，
		// 避免旧算法可能已通过 usage/消息回退把它计入后再加 now-start 造成双计。
		msgs := make([]Message, 0, len(s.messages))
		for _, m := range s.messages {
			if m.TurnIndex == start.index && (m.TurnID == start.id || m.TurnID == "" || start.id == "") {
				continue
			}
			msgs = append(msgs, m)
		}
		usage := make(map[int]TokenUsage, len(s.threadUsage.Turns))
		for index, u := range s.threadUsage.Turns {
			if index != start.index {
				usage[index] = u
			}
		}
		st.liveDuration = sessionDurationForTurns(msgs, usage) + max(0, now.Sub(start.at))
		st.liveRunning = true
	} else {
		st.liveDuration = st.totalDuration
	}
	s.mu.Unlock()

	st.turns = len(st.usage.Turns)
	st.requests = st.usage.Requests
	st.toolCalls = st.usage.ToolCalls
	st.topTools = st.usage.TopTools
	if st.totalDuration > 0 {
		if st.requests > 0 {
			st.avgResponse = st.totalDuration / time.Duration(st.requests)
		} else if st.turns > 0 {
			st.avgResponse = st.totalDuration / time.Duration(st.turns)
		}
	}
	if stats, ok := requestSpeedStatsFor(sessionSpeedSamples(st.usage.Turns)); ok {
		st.speed = formatRequestSpeedShort(stats)
		st.speedSamples = stats.n
	} else {
		st.speed = formatTokensPerSecond(int(st.usage.Total.Output), st.totalDuration)
	}
	// 缓存命中率与会话统计浮层、完成行 Tooltip 共用同一口径：缓存 / 输入（输入已含缓存）。
	st.cacheHit = turnCacheHitRate(st.usage.Total)

	if add, del, files, ok := threadChangeStatsFor(s, threadID); ok {
		st.additions, st.deletions, st.changedFiles = add, del, files
		st.hasChanges = files > 0
	}

	if hp := getSwarmPanelState(s); hp != nil {
		swarmPanelMu.Lock()
		for _, h := range hp.state.Bees {
			st.bees++
			st.beeTokens += h.Tokens
			st.beeToolCalls += h.ToolCalls
		}
		swarmPanelMu.Unlock()
	}

	if st.usage.Total.Input > 0 || st.usage.Total.Output > 0 {
		if prov, ok := findProviderInfo(providers, provider); ok {
			if info, found := findModelInfo(prov.Models, model); found && info.Cost != nil {
				st.costUSD, st.hasCost = estimateThreadCost(st.usage.Total, *info.Cost)
			}
		}
	}
	return st
}

// sessionLiveTurn 是当前运行主回合的定位：轮次索引、TurnID（可能为空）与可解析的起点。
type sessionLiveTurn struct {
	index int
	id    string
	at    time.Time
}

// sessionLiveTurnLocked 在同一份快照里判定选中会话的主回合是否仍在执行（需持有 s.mu）：
// 仅当线程 IsRunning 且非乐观运行、当前轮（最大 TurnIndex 的实际消息轮）能解析起点、
// 没有完成时间也没有终态时返回 true。主轮一旦完成即冻结；SwarmBusy 只代表子代理等待，
// 不启动计时，也不把等待时间追加进总用时。
func sessionLiveTurnLocked(s *chatState, threadID string) (sessionLiveTurn, bool) {
	var cur sessionLiveTurn
	if threadID == "" || threadID != s.selectedThreadID || s.optimisticRunning[threadID] {
		return cur, false
	}
	running := false
	for _, th := range s.threads {
		if th.ID == threadID {
			running = th.IsRunning
			break
		}
	}
	if !running {
		return cur, false
	}
	latest := -1
	for _, m := range s.messages {
		if m.TurnID == "" && m.TurnIndex < 0 {
			continue
		}
		latest = max(latest, m.TurnIndex)
	}
	if latest < 0 {
		return cur, false
	}
	cur.index = latest
	for i := len(s.messages) - 1; i >= 0; i-- {
		if m := s.messages[i]; m.TurnIndex == latest && m.TurnID != "" {
			cur.id = m.TurnID
			break
		}
	}
	target := Message{TurnID: cur.id, TurnIndex: latest}
	for _, m := range s.messages {
		if !sameTurn(m, target) {
			continue
		}
		switch m.TurnStatus {
		case "completed", "failed", "interrupted":
			return cur, false
		}
		if _, err := time.Parse(time.RFC3339Nano, m.TurnCompletedAt); err == nil {
			return cur, false
		}
	}
	cur.at = turnStartTime(s.messages, latest)
	if cur.at.IsZero() {
		return cur, false
	}
	return cur, true
}

// sessionSpeedSamples 收集会话里各轮的请求速度样本；总数 Total 里不带样本，只用每轮明细。
func sessionSpeedSamples(turns map[int]TokenUsage) []RequestSpeed {
	samples := make([]RequestSpeed, 0, len(turns))
	for _, u := range turns {
		samples = append(samples, u.Requests...)
	}
	return samples
}

// threadAgentLocked 返回会话的 provider 与模型（需持有 s.mu），找不到时返回空串。
func threadAgentLocked(s *chatState, threadID string) (provider, model string) {
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.Provider, th.Model
		}
	}
	return "", ""
}

func sessionDurationForTurns(msgs []Message, usage map[int]TokenUsage) time.Duration {
	type turnKey struct {
		id    string
		index int
	}
	groups := make(map[turnKey][]Message)
	for _, m := range msgs {
		if m.TurnID == "" && m.TurnIndex < 0 {
			continue
		}
		key := turnKey{id: m.TurnID, index: m.TurnIndex}
		if key.id != "" {
			key.index = 0
		}
		groups[key] = append(groups[key], m)
	}
	var total time.Duration
	used := make(map[int]bool)
	counts := make(map[int]int)
	for _, group := range groups {
		counts[group[0].TurnIndex]++
	}
	for _, group := range groups {
		target := group[0]
		start, end := turnAuthoritativeSpan(group, target)
		running, terminal := false, false
		for _, m := range group {
			switch m.TurnStatus {
			case "running":
				running = true
			case "completed", "failed", "interrupted":
				terminal = true
			}
		}
		if running && !terminal && (start.IsZero() || end.IsZero() || end.Before(start)) {
			used[target.TurnIndex] = true
			continue
		}
		if !start.IsZero() && !end.IsZero() && !end.Before(start) {
			total += end.Sub(start)
		} else if u := usage[target.TurnIndex]; counts[target.TurnIndex] == 1 && !used[target.TurnIndex] && u.DurationMs > 0 {
			total += time.Duration(u.DurationMs) * time.Millisecond
		} else {
			start, end = turnSpanForMessage(group, target)
			if d, ok := turnEndDuration(start, end); ok {
				total += d
			}
		}
		used[target.TurnIndex] = true
	}
	for index, u := range usage {
		if !used[index] && u.DurationMs > 0 {
			total += time.Duration(u.DurationMs) * time.Millisecond
		}
	}
	return total
}

// estimateThreadCost 按模型参考价估算会话费用（美元）：缓存命中按缓存读取价、其余输入按
// 输入价、输出按输出价，单价单位为美元/百万 token。拿不到任何有效单价时返回 false。
func estimateThreadCost(u TokenUsage, cost ModelCost) (float64, bool) {
	if cost.Input <= 0 && cost.Output <= 0 && cost.CacheRead <= 0 {
		return 0, false
	}
	uncached := u.Input - u.Cached
	if uncached < 0 {
		uncached = 0
	}
	cacheRead := cost.CacheRead
	if cacheRead <= 0 {
		cacheRead = cost.Input
	}
	total := (float64(uncached)*cost.Input +
		float64(u.Cached)*cacheRead +
		float64(u.Output)*cost.Output) / 1_000_000
	return total, true
}

// formatStatsDuration 把时长格式化为紧凑可读文本，非正时长返回空串。
func formatStatsDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return formatDuration(d)
}

// formatLiveStatsDuration 把实时总用时格式化为统一紧凑文本，负值按 0s。
func formatLiveStatsDuration(d time.Duration) string {
	return formatDuration(d)
}

// renderSessionStatsPopover 渲染会话统计浮层内容：Token、活动、常用工具、时间、改动、
// 子代理与费用分组，没有数据的分组整组跳过。
func renderSessionStatsPopover(c *ui.Context, s *chatState, threadID, key string, native bool) {
	st := collectSessionStats(s, threadID, c.Now())
	// 主回合仍在执行时每秒重渲染读秒；起点 0 秒也要安排下一帧。
	// 浮层关闭后内容不再渲染，终态不再续帧；读秒与 ReduceMotion 无关。
	if st.liveRunning {
		afterNextSecond(c)
	}

	var tokenRows, activityRows, toolRows, timeRows, changeRows, beeRows [][2]string
	total := st.usage.Total
	if total.Input > 0 || total.Output > 0 || total.Cached > 0 {
		tokenRows = append(tokenRows,
			[2]string{"输入", formatTokenCount(total.Input)},
			[2]string{"输出", formatTokenCount(total.Output)},
			[2]string{"缓存", formatTokenCount(total.Cached)},
		)
		if st.cacheHit != "" {
			tokenRows = append(tokenRows, [2]string{"缓存命中率", st.cacheHit})
		}
		tokenRows = append(tokenRows, [2]string{"合计", formatTokenCount(total.Input+total.Output) + " tokens"})
	}
	if st.turns > 0 {
		activityRows = append(activityRows, [2]string{"回合", fmt.Sprintf("%d", st.turns)})
	}
	if st.requests > 0 {
		activityRows = append(activityRows, [2]string{"模型请求", fmt.Sprintf("%d", st.requests)})
	}
	if st.toolCalls > 0 {
		activityRows = append(activityRows, [2]string{"工具调用", fmt.Sprintf("%d", st.toolCalls)})
	}
	for _, t := range st.topTools {
		if t.Count <= 0 {
			continue
		}
		toolRows = append(toolRows, [2]string{t.Name, fmt.Sprintf("%d 次", t.Count)})
	}
	if st.liveRunning || st.liveDuration > 0 {
		timeRows = append(timeRows, [2]string{"总用时", formatLiveStatsDuration(st.liveDuration)})
	}
	if d := formatStatsDuration(st.avgResponse); d != "" {
		timeRows = append(timeRows, [2]string{"平均响应", d})
	}
	if st.speed != "" {
		speed := st.speed
		if st.speedSamples > 0 {
			speed += fmt.Sprintf(" · %d 次请求", st.speedSamples)
		}
		timeRows = append(timeRows, [2]string{"输出速度", speed})
	}
	if st.hasChanges {
		changeRows = append(changeRows, [2]string{"文件", fmt.Sprintf("%d", st.changedFiles)})
		if st.additions > 0 {
			changeRows = append(changeRows, [2]string{"新增", fmt.Sprintf("+%d", st.additions)})
		}
		if st.deletions > 0 {
			changeRows = append(changeRows, [2]string{"删除", fmt.Sprintf("-%d", st.deletions)})
		}
	}
	if st.bees > 0 {
		beeRows = append(beeRows, [2]string{"数量", fmt.Sprintf("%d", st.bees)})
		if st.beeToolCalls > 0 {
			beeRows = append(beeRows, [2]string{"工具调用", fmt.Sprintf("%d", st.beeToolCalls)})
		}
		if st.beeTokens > 0 {
			beeRows = append(beeRows, [2]string{"tokens", formatTokenCount(int64(st.beeTokens))})
		}
	}

	card := ui.Column(c.Key(key+"-stats-card")).
		Gap(Space2).
		Padding(Space4, Space4).
		MinWidth(sessionStatsMinWidth).
		MaxWidth(sessionStatsMaxWidth)
	if !native {
		card = popoverCard(c, card)
	}
	card.Children(func() {
		ui.Text(c, "会话统计").
			FontSize(scaledFont(TypeFootnote)).
			Bold().
			TextColor(ThemePrimaryText(c))
		sessionStatsGroup(c, key+"-token", "Token", tokenRows)
		sessionStatsGroup(c, key+"-activity", "活动", activityRows)
		sessionStatsGroup(c, key+"-tools", "常用工具", toolRows)
		sessionStatsGroup(c, key+"-time", "时间", timeRows)
		sessionStatsGroup(c, key+"-change", "改动", changeRows)
		sessionStatsGroup(c, key+"-bees", "子代理", beeRows)
		if st.hasCost {
			sessionStatsGroup(c, key+"-cost", "", [][2]string{{"估算费用", fmt.Sprintf("≈ $%.2f", st.costUSD)}})
		}
		if len(tokenRows)+len(activityRows)+len(toolRows)+len(timeRows)+len(changeRows)+len(beeRows) == 0 {
			ui.Text(c, "暂无统计数据").
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
		}
	})
}

// sessionStatsGroup 渲染一个统计分组：小号加粗标题加一条细分隔线，下面是「标签 …… 数值」行。
// rows 为空时整组不渲染。
func sessionStatsGroup(c *ui.Context, key, title string, rows [][2]string) {
	if len(rows) == 0 {
		return
	}
	if title != "" {
		ui.Row(c.Key(key + "-header")).Gap(Space2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).
				FontSize(scaledFont(TypeCaption)).
				Bold().
				TextColor(ThemeSecondaryText(c))
			ui.Box(c.Key(key + "-header-line")).Height(Hairline).Grow(1).Background(ThemeDivider(c))
		})
	} else {
		ui.Box(c.Key(key + "-divider")).Height(Hairline).FillWidth().Background(ThemeDivider(c))
	}
	for i, r := range rows {
		ui.Row(c.Key(fmt.Sprintf("%s-row-%d", key, i))).
			FillWidth().
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			Gap(Space5).
			Children(func() {
				ui.Text(c, r[0]).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c))
				ui.Text(c, r[1]).
					FontSize(scaledFont(TypeCaption)).
					FontWeight(500).
					TextColor(ThemePrimaryText(c)).
					Selectable()
			})
	}
}
