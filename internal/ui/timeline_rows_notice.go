// 系统提示与轮次完成信息。
package mcodeui

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"math"
	"strings"
	"time"
)

// classifyNotice 依据关键词把系统通知归类；未命中任何关键词时为普通信息。
func classifyNotice(text string) noticeKind {
	lower := strings.ToLower(text)
	for _, rule := range noticeClassifyRules {
		for _, kw := range rule.keywords {
			if strings.Contains(lower, kw) {
				return rule.kind
			}
		}
	}
	return noticeInfo
}

// noticeStyle 返回通知行的图标与前景色：错误用 ThemeDanger，警告用 ThemeWarning，
// 工作树 / 模型 / 压缩用 ThemeAccent，普通信息用次要文本色。底色由 systemNoticePillBackground 决定。
func noticeStyle(c *ui.Context, kind noticeKind) (*ui.SVG, ui.Color) {
	switch kind {
	case noticeError:
		return IconOctagonAlert(), ThemeDanger(c)
	case noticeWarning:
		return IconOctagonAlert(), ThemeWarning(c)
	case noticeWorktree:
		return IconGitBranch(), ThemeAccent(c)
	case noticeModel:
		return IconCpu(), ThemeAccent(c)
	case noticeCompact:
		return IconFoldVertical(), ThemeAccent(c)
	default:
		return IconInfo(), ThemeSecondaryText(c)
	}
}

// systemNoticeActionable 判断系统通知是否带可操作入口（例如「重试」）：
// 这类通知 hover 时药丸上底色，纯提示不加 hover，免得看起来像按钮。
func systemNoticeActionable(text string) bool {
	lower := strings.ToLower(text)
	for _, kw := range []string{"重试", "重新发送", "重新加载", "retry"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// systemNoticePillBackground 返回系统通知药丸的底色：错误与警告保留约 8% 的低饱和底色，
// 其余类型透明；可操作的通知 hover 时统一换成悬停底色。
func systemNoticePillBackground(c *ui.Context, kind noticeKind, hovered bool) ui.Color {
	if hovered {
		return ThemeHover(c)
	}
	switch kind {
	case noticeError:
		return ThemeDanger(c).Alpha(0.08)
	case noticeWarning:
		return ThemeWarning(c).Alpha(0.08)
	}
	return ui.Transparent
}

// renderSystemMessage 渲染系统通知行：左右两条发丝线夹住居中的浅色药丸，左侧按通知类型显示图标。
// 供应商限流、额度耗尽、登录失效这几类需要用户干预的提示（或带着后端上报的失败类别）
// 改用横幅行。
func renderSystemMessage(c *ui.Context, s *chatState, m Message) ui.Element {
	if providerKind, resetHint := classifyMessageNotice(m); providerKind != providerNoticeNone {
		return renderProviderNoticeBanner(c, s, m, providerKind, resetHint)
	}
	kind := classifyNotice(m.Text)
	icon, fg := noticeStyle(c, kind)
	return ui.Row(c.Key("msg-sys-" + m.ID)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		AlignItems(ui.Center).
		Gap(Space4).
		Children(func() {
			ui.Box(c.Key("sys-rule-left-" + m.ID)).Label("sys-rule-left-" + m.ID).
				Height(Hairline).Grow(1).Shrink(1).MinWidth(0).Background(ThemeDivider(c))
			// 药丸要能被压缩到行宽以内：MinWidth(0) 让自动最小宽度不再等于整段文本的宽度，
			// Shrink(1) 让它与两条发丝线一起分配剩余空间；文本单行截断，完整内容放进 tooltip。
			// 药丸必须在行的 Children 里创建：mygo 在元素创建那一刻就把它挂到当前父节点，
			// 在 return 之前创建会变成通知行的前一个兄弟节点，进不了行内布局。
			pill := ui.Row(c.Key("sys-pill-"+m.ID)).
				Label("sys-pill-"+m.ID).
				AlignItems(ui.Center).
				Gap(Space2).
				Height(ControlSM).
				Padding(0, Space4).
				Radius(RadiusLG).
				Border(1, ThemeBorder(c)).
				Shrink(1).
				MinWidth(0).
				Tooltip(m.Text)
			// 可操作的通知 hover 时药丸上底色；纯提示不加 hover。
			pill.Background(systemNoticePillBackground(c, kind, systemNoticeActionable(m.Text) && pill.Hovered()))
			pill.Children(func() {
				ui.Icon(c, icon).Size(11, 11).TextColor(fg)
				ui.Text(c, m.Text).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Selectable().SingleLine().Shrink(1).MinWidth(0)
			})
			ui.Box(c.Key("sys-rule-right-" + m.ID)).Label("sys-rule-right-" + m.ID).
				Height(Hairline).Grow(1).Shrink(1).MinWidth(0).Background(ThemeDivider(c))
		})
}

// formatTurnFinishedText 拼装回合完成块的基础文本：只有「已完成」加上用时与输出 tokens
// （能推得时），不带模型与工具信息，供没有这些数据的调用方与单元测试使用。
// 不足 1 秒的时长不显示用时（样本太短，秒级显示没有意义，与不足 0.5 秒不显示速度一致）；
// 耗时与输出都拿不到时返回空字符串，调用方跳过整块。
func formatTurnFinishedText(usage TokenUsage, duration time.Duration) string {
	return formatTurnFinishedSummary("", turnToolStats{}, usage, duration)
}

// formatTokensPerSecond 把输出速度格式化为「38 t/s」，数值取整。
// 输出 token 为 0 或耗时不足 0.5 秒时返回空字符串（样本太小、数值不可信），调用方跳过该段。
func formatTokensPerSecond(outputTokens int, d time.Duration) string {
	if outputTokens <= 0 || d < 500*time.Millisecond {
		return ""
	}
	return fmt.Sprintf("%d t/s", int(math.Round(float64(outputTokens)/d.Seconds())))
}

// requestSpeedStatsFor 汇总一轮的请求速度样本，没有有效样本时返回 false（调用方回退到整轮口径）。
// 只统计有输出且生成时长不少于 0.2 秒的请求：再短的样本取整后波动过大，数值不可信。
// 平均速度按 Σ 输出 tokens / Σ 秒数计算，而不是各样本速度的算术平均，
// 避免几 token 的短请求把平均值拉偏。
func requestSpeedStatsFor(reqs []RequestSpeed) (requestSpeedStats, bool) {
	var st requestSpeedStats
	var tokens int64
	var seconds float64
	for _, r := range reqs {
		if r.OutputTokens <= 0 || r.DurationMs < 200 {
			continue
		}
		s := float64(r.DurationMs) / 1000
		tps := float64(r.OutputTokens) / s
		if st.n == 0 || tps < st.min {
			st.min = tps
		}
		if st.n == 0 || tps > st.max {
			st.max = tps
		}
		tokens += r.OutputTokens
		seconds += s
		st.n++
	}
	if st.n == 0 || seconds <= 0 {
		return requestSpeedStats{}, false
	}
	st.avg = float64(tokens) / seconds
	return st, true
}

// formatRequestSpeedShort 把请求速度统计格式化为完成行用的紧凑文本：
// 至少两个样本且取整后的极值不同时显示「41 t/s（32–58）」，否则只显示「41 t/s」。
func formatRequestSpeedShort(st requestSpeedStats) string {
	if st.n <= 0 {
		return ""
	}
	avg := int(math.Round(st.avg))
	if st.n >= 2 {
		lo, hi := int(math.Round(st.min)), int(math.Round(st.max))
		if lo != hi {
			return fmt.Sprintf("%d t/s（%d–%d）", avg, lo, hi)
		}
	}
	return fmt.Sprintf("%d t/s", avg)
}

// formatRequestSpeedDetail 把请求速度统计格式化为完整文本，供完成行悬停提示使用：
// 多个样本时给出平均、范围与请求数，单个样本只给平均与请求数。
func formatRequestSpeedDetail(st requestSpeedStats) string {
	if st.n <= 0 {
		return ""
	}
	if st.n == 1 {
		return fmt.Sprintf("输出速度 %d t/s · 1 次请求", int(math.Round(st.avg)))
	}
	return fmt.Sprintf("输出速度 平均 %d t/s · 范围 %d–%d t/s · %d 次请求",
		int(math.Round(st.avg)), int(math.Round(st.min)), int(math.Round(st.max)), st.n)
}

// appendTurnOutputSpeed 在完成行文本后追加输出速度：优先后端提供的逐请求样本
// （平均值与范围），没有有效样本时回退到整轮输出 tokens 除以生成时长。
// text 为空时原样返回，避免只拼出速度。
func appendTurnOutputSpeed(text string, usage TokenUsage, fallback time.Duration) string {
	if text == "" {
		return text
	}
	if st, ok := requestSpeedStatsFor(usage.Requests); ok {
		return text + " · " + formatRequestSpeedShort(st)
	}
	tokens, ok := turnOutputTokens(usage)
	if !ok {
		return text
	}
	speed := formatTokensPerSecond(tokens, fallback)
	if speed == "" {
		return text
	}
	return text + " · " + speed
}

// turnOutputTokens 返回本回合的输出 token 数；后端没给输出统计时返回 false，调用方跳过速度。
func turnOutputTokens(usage TokenUsage) (int, bool) {
	if usage.Output <= 0 {
		return 0, false
	}
	return int(usage.Output), true
}

// resolveTurnDuration 返回本回合计算输出速度的分母：优先后端记录的耗时（TokenUsage.DurationMs，不落库），
// 没有时用 fallback（本轮开始到结束的墙钟时长）。两者都没有时返回 0，调用方不显示速度。
// 它只用于速度回退，完成行与折叠体尾部显示的「用时」走 turnFinishedDuration。
func resolveTurnDuration(usage TokenUsage, fallback time.Duration) time.Duration {
	if usage.DurationMs > 0 {
		return time.Duration(usage.DurationMs) * time.Millisecond
	}
	return fallback
}

// turnFinishedDuration 返回本回合完成行与折叠体尾部共用的用时：本轮开始到结束的墙钟时长。
// 优先取消息上的权威起止时间（TurnStartedAt / TurnCompletedAt），其次从界面消息时间推导，
// 最后才回退到 TokenUsage.DurationMs（不落库，只用于速度回退），保证折叠前后与重启前后一致。
// 读取会话消息时自行加锁。
func turnFinishedDuration(s *chatState, m Message, usage TokenUsage) time.Duration {
	if s == nil {
		return resolveTurnDuration(usage, 0)
	}
	start, end := turnFinishedSpanFor(s, m)
	s.mu.Lock()
	authoritativeStart, authoritativeEnd := turnAuthoritativeSpan(s.messages, m)
	s.mu.Unlock()
	if !authoritativeStart.IsZero() && !authoritativeEnd.IsZero() && !authoritativeEnd.Before(authoritativeStart) {
		return authoritativeEnd.Sub(authoritativeStart)
	}
	if d, ok := turnEndDuration(start, end); ok {
		return d
	}
	return resolveTurnDuration(usage, 0)
}

// renderTurnFinishedBlock 渲染回合完成块：一条细分割线、完成图标、本回合的模型短名、
// 用时、工具调用与改动文件数、token 统计与输出速度；悬停显示 token 明细、费用、常用工具与起止时间。
// 只在该轮结束（非流式）时由该轮最后一条助手消息调用；用时见 turnFinishedDuration。
// Swarm 主会话仍有运行中或排队中的子代理时，最后一轮先不显示完成块，等它们全部结束后自动恢复。
func renderTurnFinishedBlock(c *ui.Context, s *chatState, m Message, usage TokenUsage, _ time.Duration) {
	if swarmTurnEndDeferred(s, m.TurnIndex) {
		return
	}
	renderTurnFinishedBlockWithDuration(c, s, m, usage, turnFinishedDuration(s, m, usage))
}

// renderTurnFinishedBlockWithDuration 是完成块的实际渲染：duration 是本轮开始到结束的墙钟用时，
// 没有逐请求样本时速度按 TokenUsage.DurationMs 回退（见 resolveTurnDuration），
// 为 0 或不足 0.5 秒则不显示（见 formatTokensPerSecond）。
// 模型短名、工具调用与改动文件数都从已加载的会话状态里按回合统计，缺数据时相应分段自动省略。
// 「已完成」行下面再挂一张本回合改动文件卡片（见 renderTurnFilesCard），没有改动时它不画。
func renderTurnFinishedBlockWithDuration(c *ui.Context, s *chatState, m Message, usage TokenUsage, duration time.Duration) {
	stats := turnToolStatsFor(s, m.TurnIndex)
	text := formatTurnFinishedSummary("", stats, usage, duration)
	text = appendTurnOutputSpeed(text, usage, resolveTurnDuration(usage, duration))
	if text == "" {
		return
	}
	start, end := turnFinishedSpanFor(s, m)
	// 文本末尾追加具体结束时间，让完成行不只看耗时也能对上时钟。
	text = appendTurnEndClock(text, end)
	costUSD, hasCost := turnFinishedCost(s, m, usage)
	tooltip := formatTurnFinishedTooltip(turnFinishedInfo{
		usage:   usage,
		stats:   stats,
		costUSD: costUSD,
		hasCost: hasCost,
		start:   start,
		end:     end,
	})
	row := ui.Row(c.Key("turn-finished-"+m.ID)).
		AlignItems(ui.Center).
		Gap(Space3).
		Margin(4, 0, 0, 0)
	if tooltip != "" {
		row = row.Tooltip(tooltip)
	}
	row.Children(func() {
		ui.Box(c.Key("turn-finished-line-" + m.ID)).Height(Hairline).Grow(1).Background(ThemeBorder(c))
		ui.Icon(c, IconCheck()).Size(11, 11).TextColor(ThemeSuccess(c))
		ui.Text(c, text).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
	})
	// 「已完成」行下面补一张本回合改动文件卡片，没有改动时它什么都不画。
	renderTurnFilesCard(c, s, m)
}

// formatTurnFinishedBlockText 拼装回合完成块的完整文本（不带模型与工具信息）：本回合 token 统计（含用时）
// 加上输出速度（能算得时），形如「已完成 · 用时 12s · 输出 456 · 38 t/s」。有逐请求样本时速度取
// 样本的平均值与范围，否则是输出 tokens 除以生成时长；时长不足 1 秒时不显示用时段，不足 0.5 秒、
// 没有输出 token 或拿不到时长时不带速度。
// 没有 token 统计时返回空字符串，调用方跳过整块。
func formatTurnFinishedBlockText(usage TokenUsage, duration time.Duration) string {
	return formatTurnFinishedBlockTextWithInfo("", turnToolStats{}, usage, duration)
}
