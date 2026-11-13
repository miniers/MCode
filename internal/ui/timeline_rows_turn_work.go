// 轮次工作摘要和工具思考内容。
package mcodeui

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"strings"
	"time"
	"unicode/utf8"
)

// turnWorkHeaderText 拼出回合折叠组头的摘要：结束态只说这一回合用了多久，例如「用时 3.2s」；
// 拿不到耗时（时间戳缺失或不足 1 秒）时写「过程」。计数与完成明细改放 Tooltip 与展开体尾部。
func turnWorkHeaderText(counts toolGroupCounts, elapsed string) string {
	if elapsed == "" {
		return "过程"
	}
	return "用时 " + elapsed
}

// turnHeaderRunningLabel 把运行中回合已处理的时长写成组头文案：只取整秒，避免小数位每秒来回跳。
func turnHeaderRunningLabel(elapsed time.Duration) string {
	secs := int(elapsed / time.Second)
	if secs < 0 {
		secs = 0
	}
	return fmt.Sprintf("已处理 %ds", secs)
}

// runningTurnHeaderLabel 返回运行态头此刻的文字：拿不到起始时刻时只写「已处理」，不编造数字。
func runningTurnHeaderLabel(item timelineItem, now time.Time) string {
	if item.workStartedAt.IsZero() {
		return "已处理"
	}
	return turnHeaderRunningLabel(now.Sub(item.workStartedAt))
}

// turnWorkHeaderLabel 返回回合折叠项组头的完整文字：
// 静默回合只说巡视了几次、不需要处理；其余回合只留用时，唤醒原因与计数移到 Tooltip。
func turnWorkHeaderLabel(item timelineItem, counts toolGroupCounts) string {
	return turnWorkHeaderText(counts, item.workElapsed)
}

// turnWorkHeaderAccessLabel 拼出回合折叠组头的无障碍名称：说明这是一段工具调用过程、用时多少，
// 以及点按后会展开还是收起。视觉文本仍由 headerText 提供，读屏用户靠它知道这行可点。
func turnWorkHeaderAccessLabel(item timelineItem, open bool) string {
	action := "点按展开"
	if open {
		action = "点按收起"
	}
	if item.workElapsed != "" {
		return "工具调用，用时 " + item.workElapsed + "，" + action
	}
	return "工具调用过程，" + action
}

// turnWorkHeaderTooltip 拼出回合折叠头的 Tooltip：运行中给实时计时，结束后给过程计数、
// 唤醒原因与 token/费用/工具明细，保证组头不再显示的计数与完成信息仍然可查。
func turnWorkHeaderTooltip(s *chatState, item timelineItem, counts toolGroupCounts, usage TokenUsage, hasUsage bool, now time.Time) string {
	if item.workRunning {
		if item.workStartedAt.IsZero() {
			return "本回合正在处理"
		}
		return "本回合" + turnHeaderRunningLabel(now.Sub(item.workStartedAt))
	}
	parts := make([]string, 0, 2)
	if summary := toolGroupSummaryText(counts); summary != "" {
		parts = append(parts, summary)
	}
	if hasUsage {
		stats := turnToolStatsFor(s, item.turnIndex)
		endMsg := turnEndMessageFor(s, item.turnIndex)
		start, end := turnFinishedSpanFor(s, endMsg)
		costUSD, hasCost := turnFinishedCost(s, endMsg, usage)
		detail := formatTurnFinishedTooltip(turnFinishedInfo{
			usage:   usage,
			stats:   stats,
			costUSD: costUSD,
			hasCost: hasCost,
			start:   start,
			end:     end,
		})
		if detail != "" {
			parts = append(parts, detail)
		}
	}
	return strings.Join(parts, "\n")
}

// runningTurnHasRunningTool 判断运行中的回合里是否还有工具胶囊在扫光：
// 有的话组头不再扫光，只数秒，避免同一屏出现两处同步高光。
// 中途正文之后的工具留在头外，所以按整轮消息判断，而不只看头里收起的那一段。
func runningTurnHasRunningTool(s *chatState, item timelineItem) bool {
	if s != nil {
		s.mu.Lock()
		running := false
		for _, m := range s.messages {
			if m.TurnIndex == item.turnIndex && isToolRunning(m) {
				running = true
				break
			}
		}
		s.mu.Unlock()
		return running
	}
	for _, m := range item.tools {
		if isToolRunning(m) {
			return true
		}
	}
	return false
}

// renderTurnWorkHeaderText 渲染折叠组头的摘要文字：结束态只显示用时，组头的计数与完成明细
// 已经移到 Tooltip 与展开体尾部，这里不再需要滚动数字。
func renderTurnWorkHeaderText(c *ui.Context, item timelineItem, counts toolGroupCounts, color ui.Color) {
	ui.Text(c, turnWorkHeaderText(counts, item.workElapsed)).FontSize(scaledFont(TypeBody)).TextColor(color).SingleLine()
}

// turnWorkSummaryTools 返回用于组头计数的消息：关闭「显示思考过程」时不再统计思考次数，
// 与工具组组头的统计口径保持一致。
func turnWorkSummaryTools(s *chatState, tools []Message) []Message {
	if isShowReasoningEnabled(s) {
		return tools
	}
	filtered := make([]Message, 0, len(tools))
	for _, m := range tools {
		if !isReasoningMessage(m) {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// renderTurnWorkItem 渲染回合的工作过程折叠项：运行中是一行不可点的「已处理 Ns」计时头，
// 结束后变为可展开的「用时 Ns」；展开后按原顺序显示被收起的思考、工具组与中途正文，
// 末尾再补一行原本放在完成行里的回合详情。折叠只影响显示：组内条目保留原有 Key 与开合状态，
// 展开后仍能逐条点开工具详情。
func renderTurnWorkItem(c *ui.Context, s *chatState, item timelineItem, usage TokenUsage, hasUsage bool) ui.Element {
	running := item.workRunning
	explicitOpen, hasExplicit := false, false
	s.mu.Lock()
	if v, ok := s.expandedTool[item.id]; ok {
		explicitOpen, hasExplicit = v, true
	}
	s.mu.Unlock()

	// 用户显式设置过就按设置；否则关闭「自动折叠思考内容」时默认展开。
	// isAutoCollapseReasoningEnabled 会锁设置状态的锁，必须在释放 s.mu 之后调用。
	open := !isAutoCollapseReasoningEnabled(s)
	if hasExplicit {
		open = explicitOpen
	}
	if running {
		// 运行中的回合没有可折叠的完整过程：头只负责计时，展开体始终跟着最新内容走。
		open = true
	}

	counts := countToolGroupCategories(turnWorkSummaryTools(s, item.tools))
	headerText := turnWorkHeaderLabel(item, counts)
	if running {
		headerText = runningTurnHeaderLabel(item, c.Now())
	}
	btnLabel := "展开本回合的工作过程"
	if open {
		btnLabel = "收起本回合的工作过程"
	}
	chevronTarget := float32(0)
	if open {
		chevronTarget = 90
	}
	headerLabel := headerText
	if !running {
		headerLabel = turnWorkHeaderAccessLabel(item, open)
	}
	// Tooltip 承载组头不再显示的计数与完成明细，两种状态都能看到对应信息；
	// 结束态再把展开/收起提示放在最前面，兼顾可发现性。
	tooltip := turnWorkHeaderTooltip(s, item, counts, usage, hasUsage, c.Now())
	if !running {
		if tooltip == "" {
			tooltip = btnLabel
		} else {
			tooltip = btnLabel + "\n" + tooltip
		}
	}

	return ui.Column(c.Key("msg-" + item.id)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		Children(func() {
			header := ui.Row(c.Key("turnwork-header-"+item.id)).
				Gap(Space2).
				AlignItems(ui.Center).
				Padding(Space0, Space2).
				Radius(RadiusSM).
				MinHeight(24).
				Label(headerLabel).
				Tooltip(tooltip)
			if !running {
				header = header.Role(ui.RoleButton)
			}
			textColor := ThemeSecondaryText(c)
			var rotation float32
			if !running {
				header.Cursor(ui.CursorPointer).OnClick(func() { s.setToolGroupOpen(item.id, !open) })
				if header.Hovered() {
					header.Background(ThemeHover(c).Alpha(0.35))
					textColor = ThemePrimaryText(c)
				}
				rotation = header.AnimateTo(item.id+"/chevron", chevronTarget, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
			}
			header.Children(func() {
				if item.workAttention {
					ui.Icon(c, IconAlertTriangle()).Size(11, 11).Label("工具执行失败").TextColor(ThemeDanger(c))
				}
				if running {
					// 运行中的头只扫一道光；本回合已有工具胶囊在扫光时让位，避免同一屏两处同步高光。
					renderShimmerTitle(c, item.id+"/turn-header-shimmer", turnHeaderShimmerLabel,
						!runningTurnHasRunningTool(s, item), func() ui.Element {
							return ui.Text(c, headerText).FontSize(scaledFont(TypeBody)).TextColor(textColor).SingleLine()
						})
					// 只有运行态需要每秒重建这一行来更新计时；结束态不再持续重绘。
					c.After(time.Second)
					return
				}
				renderTurnWorkHeaderText(c, item, counts, textColor)
				ui.Icon(c, IconChevronRight()).Size(11, 11).TextColor(textColor).Rotate(rotation)
			})
			// 展开体统一走 motionUnfoldBody：展开时高度从 0 长到完整高度，收起时反向收拢。
			motionUnfoldBody(c, "turnwork-body-"+item.id, open, func() {
				for idx, inner := range item.workItems {
					renderTimelineItem(c, s, inner, "", "", false, false, TokenUsage{}, turnWorkInnerGap(idx))
				}
				renderTurnWorkFooter(c, s, item, usage, hasUsage)
			})
		})
}

// turnEndMessageFor 返回本回合最后一条助手正文消息（原来挂完成行的就是它），
// 折叠头需要它来取线程的 provider/model 与 token 明细；找不到时返回零值。
func turnEndMessageFor(s *chatState, turnIndex int) Message {
	if s == nil {
		return Message{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.messages) - 1; i >= 0; i-- {
		m := s.messages[i]
		if m.TurnIndex == turnIndex && m.Role == "assistant" && !isReasoningMessage(m) && strings.TrimSpace(m.Text) != "" {
			return m
		}
	}
	return Message{}
}

// renderTurnWorkFooter 在展开体末尾画一行回合完成详情：复用独立完成行的文字与结束时刻，
// 这样收进折叠头的信息（模型、工具数、改动、token、速度、结束时间）一条都不少。
func renderTurnWorkFooter(c *ui.Context, s *chatState, item timelineItem, usage TokenUsage, hasUsage bool) {
	if item.workRunning {
		return
	}
	// 历史回合可能没有用量记录：用量为空时 formatTurnFinishedBlockTextWithInfo 会退回
	// 模型/工具数/耗时，仍比整行完成信息丢掉更完整。
	if !hasUsage {
		usage = TokenUsage{}
	}
	stats := turnToolStatsFor(s, item.turnIndex)
	endMsg := turnEndMessageFor(s, item.turnIndex)
	start, end := turnFinishedSpanFor(s, endMsg)
	duration := resolveTurnDuration(usage, end.Sub(start))
	text := formatTurnFinishedBlockTextWithInfo("", stats, usage, duration)
	if text == "" {
		return
	}
	ui.Text(c, appendTurnEndClock(text, end)).
		FontSize(scaledFont(TypeCaption)).
		TextColor(ThemeSecondaryText(c)).
		Margin(4, 0, 0, 0)
}

// renderToolBeatItem 渲染工具组内的一个节拍：上方是思考叙事（这节拍没有前置思考时省略），
// 下方胶囊竖排，一行一个，点开后详情紧跟在该胶囊下方。
// 整段没有竖线、圆点或图标，只剩“叙事 + 动作胶囊”。
func renderToolBeatItem(c *ui.Context, s *chatState, item timelineItem, liveID string) ui.Element {
	beatTools := item.beatTools
	return ui.Column(c.Key("tool-beat-" + item.id)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		Gap(Space1).
		Children(func() {
			if isReasoningMessage(item.msg) && isShowReasoningEnabled(s) {
				renderToolBeatReasoning(c, s, item.msg, liveID)
			}
			if len(beatTools) > 0 {
				ui.Column(c.Key("tool-beat-chips-" + item.id)).
					FillWidth().
					Gap(Space1).
					AlignItems(ui.Start).
					Children(func() {
						opened := false
						for _, toolMsg := range beatTools {
							if items, ok := todoItemsFromMessage(toolMsg); ok {
								renderTodoCard(c, s, toolMsg, items)
								continue
							}
							ui.Row(c.Key("tool-beat-chip-row-"+toolMsg.ID)).
								Padding(0, 0, 0, toolBeatPadLeft).
								AlignItems(ui.Center).
								Children(func() {
									renderToolChip(c, s, toolMsg, beatTools)
								})
							if !opened && s.toolMessageExpanded(toolMsg) {
								renderToolDetail(c, s, toolMsg)
								opened = true
							}
						}
					})
			}
		})
}

// toolBeatReasoningNeedsTruncation 判断节拍里的思考正文是否需要折叠：超过 2 行或超过 180 个字符。
func toolBeatReasoningNeedsTruncation(text string) bool {
	trimmed := strings.TrimSpace(text)
	if utf8.RuneCountInString(trimmed) > 180 {
		return true
	}
	return strings.Count(trimmed, "\n")+1 > 2
}

// renderToolBeatReasoning 渲染节拍里的思考叙事：小号次要色正文，默认最多两行，
// 超出时在末尾给一个「展开」/「收起」文字按钮；展开后走思考正文的 Markdown 全量渲染。
// 展开状态记在 expandedReasoning 的 "beat-full:<ID>" 键下（true 表示已展开全文）。
// 这条思考还在流式输出时，沿用独立思考行的「思考中…」呼吸标识与流式渲染。
// 关闭「自动折叠思考内容」开关后不再做两行截断，也不渲染「收起」按钮。
func renderToolBeatReasoning(c *ui.Context, s *chatState, m Message, liveID string) {
	s.mu.Lock()
	full := s.expandedReasoning["beat-full:"+m.ID]
	s.mu.Unlock()
	isLive := liveID != "" && liveID == m.ID
	autoCollapse := isAutoCollapseReasoningEnabled(s)
	long := toolBeatReasoningNeedsTruncation(m.Text)

	ui.Column(c.Key("tool-beat-think-" + m.ID)).
		FillWidth().
		Gap(Space0).
		Children(func() {
			if isLive {
				// 流式输出时沿用思考行的「思考中…」呼吸标识；同样按 175ms 一档低频重建。
				// 减少动效时不呼吸，直接给满不透明度。
				pulse := float32(1)
				if !reduceMotion(c) {
					pulse = float32(thinkPulseAlpha(c.Now()))
					c.After(175 * time.Millisecond)
				}
				live := ui.Row(c.Key("tool-beat-think-live-" + m.ID)).AlignItems(ui.Center)
				live.Children(func() {
					ui.Text(c, reasoningHeaderLabel(true)).
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c).Alpha(pulse))
				})
			}

			// 流式输出时不折叠：和独立思考行一样把正在生成的正文整段渲染出来
			if long && !full && !isLive && autoCollapse {
				ui.Text(c, strings.TrimSpace(m.Text)).
					FontSize(scaledFont(TypeBody)).
					LineHeight(1.45).
					TextColor(ThemeSecondaryText(c)).
					MaxLines(2).
					Ellipsis("…")
				renderToolBeatReasoningToggle(c, s, m, "展开")
				return
			}

			ui.Column(c.Key("tool-beat-think-body-" + m.ID)).Selectable().FillWidth().Children(func() {
				if isLive {
					renderLiveMarkdown(c, s, m, toolBeatMarkdownOptions(c, s))
				} else {
					markdown.View(c, m.Text, toolBeatMarkdownOptions(c, s))
				}
			})
			if long && !isLive && autoCollapse {
				renderToolBeatReasoningToggle(c, s, m, "收起")
			}
		})
}

// renderToolBeatReasoningToggle 渲染思考叙事末尾的小号「展开」/「收起」文字按钮。
func renderToolBeatReasoningToggle(c *ui.Context, s *chatState, m Message, text string) {
	label := text + "思考"
	ui.ButtonBase(c.Key("tool-beat-think-toggle-"+m.ID)).
		Label(label).
		Tooltip(label).
		Padding(0, Space0).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			s.toggleReasoning("beat-full:" + m.ID)
		}).
		Children(func() {
			ui.Text(c, text).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c).Alpha(0.75))
		})
}
