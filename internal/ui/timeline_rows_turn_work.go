// 轮次工作摘要和工具思考内容。
package mcodeui

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"math"
	"strings"
	"sync"
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

// turnHeaderRunningLabel 把运行中回合已处理的时长写成组头文案：与会话统计同一套整秒时分秒
// （59s / 1m / 1h2m3s），主会话、子代理浮层与独立窗口共用，各自仍用自己线程的起点。
func turnHeaderRunningLabel(elapsed time.Duration) string {
	return "已处理 " + formatLiveStatsDuration(elapsed)
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
	if item.workTaskSpan.ok {
		return "任务用时 " + formatLiveStatsDuration(item.workTaskSpan.end.Sub(item.workTaskSpan.start))
	}
	return turnWorkHeaderText(counts, item.workElapsed)
}

// turnWorkHeaderAccessLabel 拼出回合折叠组头的无障碍名称：说明这是一段工具调用过程、用时多少，
// 以及点按后会展开还是收起。视觉文本仍由 headerText 提供，读屏用户靠它知道这行可点。
func turnWorkHeaderAccessLabel(item timelineItem, open bool) string {
	action := "点按展开"
	if open {
		action = "点按收起"
	}
	if item.workTaskSpan.ok {
		return "工具调用，任务用时 " + formatLiveStatsDuration(item.workTaskSpan.end.Sub(item.workTaskSpan.start)) + "，" + action
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
	stats := turnToolStatsFor(s, item.turnIndex)
	endMsg := turnEndMessageFor(s, item.turnIndex)
	start, end := turnFinishedSpanFor(s, endMsg)
	round := turnFinishedDuration(s, endMsg, usage)
	costUSD, hasCost := turnFinishedCost(s, endMsg, usage)
	if item.workTaskSpan.ok {
		start, end = item.workTaskSpan.start, item.workTaskSpan.end
	}
	info := turnFinishedInfo{
		usage:   usage,
		stats:   stats,
		costUSD: costUSD,
		hasCost: hasCost,
		start:   start,
		end:     end,
	}
	if item.workTaskSpan.ok {
		// 没有 usage 时也要保留任务起止与本轮用时，不能整段丢掉时间详情。
		parts = append(parts, taskFinishedTooltip(info, round))
		return strings.Join(parts, "\n")
	}
	if hasUsage {
		if detail := formatTurnFinishedTooltip(info); detail != "" {
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
	ui.Text(c, turnWorkHeaderLabel(item, counts)).FontSize(scaledFont(TypeBody)).TextColor(color).SingleLine()
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

	todoItems, hasTodo := todoItemsFromMessage(item.msg)
	canToggleFold := !running

	return ui.Column(c.Key("msg-" + item.id)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		Children(func() {
			renderHeader := func() {
				header := ui.Row(c.Key("turnwork-header-"+item.id)).
					Gap(Space2).
					AlignItems(ui.Center).
					Padding(Space0, Space2).
					Radius(RadiusSM).
					MinHeight(ControlSM).
					Label(headerLabel).
					Tooltip(tooltip)
				if canToggleFold {
					header = header.Role(ui.RoleButton).Focusable()
				}
				textColor := ThemeSecondaryText(c)
				var rotation float32
				if canToggleFold {
					header.Cursor(ui.CursorPointer).OnClick(func() { s.setToolGroupOpen(item.id, !open) }).
						Transition(motionTransition(c, ui.ElementTransition{Duration: DurFast, Ease: EaseOut, Colors: true}))
					focusRing(c, header)
					if header.Hovered() {
						header.Background(ThemeOverlayHover(c))
						textColor = ThemePrimaryText(c)
					}
					rotation = header.AnimateTo(item.id+"/chevron", chevronTarget, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
				}
				header.Children(func() {
					if item.workAttention {
						ui.Icon(c, IconAlertTriangle()).Size(IconSM, IconSM).Label("工具执行失败").TextColor(ThemeDanger(c))
					}
					if running {
						// 运行中的头只扫一道光；本回合已有工具胶囊在扫光时让位，避免同一屏两处同步高光。
						renderShimmerTitle(c, item.id+"/turn-header-shimmer", turnHeaderShimmerLabel,
							!runningTurnHasRunningTool(s, item), func() ui.Element {
								return ui.Text(c, headerText).FontSize(scaledFont(TypeBody)).TextColor(textColor).SingleLine()
							})
						// 只有运行态需要每秒重建这一行来更新计时；结束态不再持续重绘。
						afterNextSecond(c)
						return
					}
					renderTurnWorkHeaderText(c, item, counts, textColor)
					if canToggleFold {
						ui.Icon(c, IconChevronRight()).Size(IconSM, IconSM).Label("折叠箭头").TextColor(textColor).Rotate(rotation)
					}
				})
			}

			if hasTodo {
				ui.Row(c.Key("turnwork-header-line-" + item.id)).
					AlignItems(ui.Center).
					Gap(Space1).
					Children(func() {
						renderHeader()
						renderTurnChecklistToggle(c, s, item.msg, todoItems)
					})
				renderTodoCard(c, s, item.msg, todoItems)
			} else {
				renderHeader()
			}
			// 展开体统一走 motionUnfoldBody：展开时高度从 0 长到完整高度，收起时反向收拢。
			motionUnfoldBody(c, "turnwork-body-"+item.id, open, func() {
				for idx, inner := range item.workItems {
					renderTimelineItem(c, s, inner, "", "", false, false, TokenUsage{}, turnWorkInnerGap(idx))
				}
				renderTurnWorkFooter(c, s, item, usage, hasUsage)
			})
		})
}

// renderTurnChecklistToggle 渲染回合头右侧可独立点击的步骤清单入口：胶囊形小进度标记（标记 + done/total），
// 没有箭头；作为回合头折叠按钮的兄弟元素存在，点击只开合清单卡片，不触及「用时」折叠体。
func renderTurnChecklistToggle(c *ui.Context, s *chatState, m Message, items []todoItem) {
	running := s.isThreadRunning(selectedThreadIDSnapshot(s))
	expanded := s.todoCardExpanded(m, running)
	done, total := todoProgress(items)
	allDone := total > 0 && done == total
	action := "展开"
	if expanded {
		action = "收起"
	}
	label := fmt.Sprintf("%s步骤清单，已完成 %d/%d 步", action, done, total)

	btn := ui.Row(c.Key("turnwork-todo-toggle-"+m.ID)).
		Gap(Space2).
		AlignItems(ui.Center).
		Padding(Space0, Space3, Space0, Space2).
		Radius(RadiusPill).
		Height(ControlXS).
		Background(ThemeOverlayHover(c)).
		Cursor(ui.CursorPointer).
		Focusable().
		Role(ui.RoleButton).
		Label(label).
		Tooltip(label).
		Transition(motionTransition(c, ui.ElementTransition{Duration: DurFast, Ease: EaseOut, Colors: true})).
		OnClick(func() {
			s.toggleTodoCard(m.ID, !expanded)
		})
	focusRing(c, btn)
	textColor := ThemeSecondaryText(c)
	if btn.Hovered() || expanded {
		btn.Background(ThemeOverlaySelected(c))
		textColor = ThemePrimaryText(c)
	}
	btn.Children(func() {
		if allDone {
			ui.Icon(c, IconCircleCheck()).Size(IconSM, IconSM).TextColor(ThemeSuccess(c))
		} else {
			arc := ThemeSecondaryText(c)
			if running {
				arc = ThemeAccent(c)
			}
			ui.Image(c, turnChecklistRingSVG(done, total, ThemeDivider(c), arc)).Size(IconSM, IconSM).Fit(ui.Contain)
		}
		ui.Text(c, fmt.Sprintf("%d/%d", done, total)).FontSize(scaledFont(TypeBody)).FontFeatures("tnum").TextColor(textColor).SingleLine()
	})
}

var turnChecklistRingCache sync.Map // key string -> *ui.SVG

// turnChecklistRingSVG 画未完成时的进度环：24 视口、半径 10、线宽 2（与 circle-check 一致），
// 底环 track，弧长 done/total，从 12 点方向顺时针。颜色与比例都是离散值，按它们缓存解析结果。
func turnChecklistRingSVG(done, total int, track, arc ui.Color) *ui.SVG {
	key := fmt.Sprintf("%d/%d|%v|%v", done, total, track, arc)
	if v, ok := turnChecklistRingCache.Load(key); ok {
		return v.(*ui.SVG)
	}
	const radius = 10.0
	circumference := 2 * math.Pi * radius
	var b strings.Builder
	fmt.Fprintf(&b, `<circle cx="12" cy="12" r="%g" fill="none" stroke="#%02X%02X%02X" stroke-opacity="%.3f" stroke-width="2"/>`,
		radius, track.R, track.G, track.B, float64(track.A)/255)
	if total > 0 && done > 0 {
		fraction := math.Min(1, float64(done)/float64(total))
		fmt.Fprintf(&b, `<circle cx="12" cy="12" r="%g" fill="none" stroke="#%02X%02X%02X" stroke-opacity="%.3f" stroke-width="2" stroke-linecap="round" stroke-dasharray="%.2f %.2f" transform="rotate(-90 12 12)"/>`,
			radius, arc.R, arc.G, arc.B, float64(arc.A)/255, circumference*fraction, circumference)
	}
	svg := ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" viewBox="0 0 24 24">` + b.String() + `</svg>`))
	turnChecklistRingCache.Store(key, svg)
	return svg
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
	// 历史回合可能没有用量记录：用量为空时 formatTurnFinishedSummary 会退回
	// 模型/工具数/耗时，仍比整行完成信息丢掉更完整。
	if !hasUsage {
		usage = TokenUsage{}
	}
	text := turnWorkFooterText(s, item.turnIndex, usage, item.workTaskSpan)
	if text == "" {
		return
	}
	ui.Text(c, text).
		FontSize(scaledFont(TypeCaption)).
		TextColor(ThemeSecondaryText(c)).
		Margin(4, 0, 0, 0)
}

// turnWorkFooterText 拼装展开体尾部的完成详情：模型、用时、工具数、改动、token 与输出速度，
// 末尾追加与用时同源的结束时钟。task 有效时用任务整体跨度与任务结束时刻（与折叠头同源）；
// 否则用时与独立完成行走 turnFinishedDuration，DurationMs 只作速度分母回退；全部数据缺失时返回空字符串。
func turnWorkFooterText(s *chatState, turnIndex int, usage TokenUsage, task taskTimingSpan) string {
	stats := turnToolStatsFor(s, turnIndex)
	endMsg := turnEndMessageFor(s, turnIndex)
	_, end := turnFinishedSpanFor(s, endMsg)
	duration := turnFinishedDuration(s, endMsg, usage)
	text := formatTurnFinishedSummary("", stats, usage, duration)
	if task.ok {
		text = taskFinishedSummary("", stats, usage, task.end.Sub(task.start))
		end = task.end
	}
	// 速度分母始终是本回合的墙钟/DurationMs，不被任务等待时间稀释。
	text = appendTurnOutputSpeed(text, usage, resolveTurnDuration(usage, duration))
	return appendTurnEndClock(text, end)
}

// renderToolBeatItem 渲染工具组内的一个节拍：上方是思考叙事（这节拍没有前置思考时省略），
// 下方胶囊竖排，一行一个，点开后详情紧跟在该胶囊下方。
// 整段没有竖线、圆点或图标，只剩“叙事 + 动作胶囊”。
// 刚点开工具组的入场窗口内，用 AnimateTo 从 0 展开高度并渐显，避免 List 顶层及回合头内瞬间满高导致遮挡或闪现。
func renderToolBeatItem(c *ui.Context, s *chatState, item timelineItem, liveID string) ui.Element {
	beatTools := item.beatTools
	enterAnim := false
	needJump := false
	if s != nil {
		s.mu.Lock()
		if item.groupID != "" && item.groupID == s.toolGroupEnterID &&
			!s.toolGroupEnterAt.IsZero() && time.Since(s.toolGroupEnterAt) < toolGroupEnterWindow {
			enterAnim = true
			if s.toolGroupEnterMounted == nil {
				s.toolGroupEnterMounted = make(map[string]struct{})
			}
			if _, ok := s.toolGroupEnterMounted[item.id]; !ok {
				s.toolGroupEnterMounted[item.id] = struct{}{}
				needJump = true
			}
		} else if s.toolGroupEnterID != "" && (s.toolGroupEnterAt.IsZero() || time.Since(s.toolGroupEnterAt) >= toolGroupEnterWindow) {
			s.toolGroupEnterID = ""
			s.toolGroupEnterAt = time.Time{}
			s.toolGroupEnterMounted = nil
		}
		s.mu.Unlock()
	}

	clip := ui.Column(c.Key("tool-beat-" + item.id)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		Shrink(0)
	k := float32(1)
	if enterAnim && !reduceMotion(c) {
		if needJump {
			clip.Jump("tool-beat-enter", 0)
		}
		k = clip.AnimateTo("tool-beat-enter", 1, ui.Animation{
			Duration: animateDuration(c, motionUnfold),
			Ease:     motionEaseStandard,
		})
	}
	clip.Children(func() {
		inner := ui.Column(c.Key("tool-beat-body-" + item.id)).
			FillWidth().
			Gap(Space1).
			Shrink(0)
		inner.Children(func() {
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
							if _, ok := todoItemsFromMessage(toolMsg); ok {
								continue
							}
							if isCommandCard(toolMsg) {
								ui.Column(c.Key("tool-beat-chip-row-"+toolMsg.ID)).
									FillWidth().
									Padding(0, 0, 0, toolBeatPadLeft).
									Children(func() {
										renderCommandCard(c, s, toolMsg, beatTools)
									})
								continue
							}
							ui.Row(c.Key("tool-beat-chip-row-"+toolMsg.ID)).
								Padding(0, 0, 0, toolBeatPadLeft).
								AlignItems(ui.Center).
								Children(func() {
									renderToolChip(c, s, toolMsg, beatTools)
								})
							open := !opened && s.toolMessageExpanded(toolMsg)
							motionUnfoldBody(c, "tool-beat-detail-"+toolMsg.ID, open, func() {
								renderToolDetail(c, s, toolMsg)
							})
							if open {
								opened = true
							}
						}
					})
			}
		})
		if k < 1 {
			h := inner.LayoutBounds().H
			clip.ClipY().Height(k * h).Opacity(k)
		}
	})
	return clip
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
// 这条思考还在流式输出时，沿用独立思考行的「思考中…」标识与流式渲染。
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
				live := ui.Row(c.Key("tool-beat-think-live-" + m.ID)).AlignItems(ui.Center)
				live.Children(func() {
					ui.Text(c, reasoningHeaderLabel(true)).
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
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

			timelineGutter(c, ui.Column(c.Key("tool-beat-think-body-"+m.ID)).Selectable().FillWidth(), 0).Children(func() {
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
	btn := ui.ButtonBase(c.Key("tool-beat-think-toggle-"+m.ID)).
		Focusable().
		Role(ui.RoleButton).
		Label(label).
		Tooltip(label).
		Padding(0, Space0).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			s.toggleReasoning("beat-full:" + m.ID)
		})
	focusRing(c, btn)
	btn.Children(func() {
		ui.Text(c, text).
			FontSize(scaledFont(TypeCaption)).
			TextColor(ThemeSecondaryText(c).Alpha(0.75))
	})
}
