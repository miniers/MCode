// 时间线行及上下文卡片的渲染。
package mcodeui

import (
	"context"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"strings"
	"time"
)

// renderTimelineItem 渲染时间线中的一个元素（单条消息或连续工具合并组）。
// 每行占满列表宽度并居中；内容列最宽由外观设置的内容宽度决定（标准 768），左右 padding 20 放在列上，不放在列表上，使滚动条留在列表右边缘。
// topGap 是该行上方要留的间距（列表不再统一设 Gap），由 timelineItemTopGap 决定。
func renderTimelineItem(c *ui.Context, s *chatState, item timelineItem, liveID, lastLiveID string, isLastAssistant bool, hasUsage bool, turnUsage TokenUsage, topGap float32) ui.Element {
	if item.kind == timelineItemTurnTime && item.stampAt.IsZero() {
		return ui.Element{}
	}
	// 关闭“显示思考过程”时，单条思考行整行不渲染；节拍里只有思考、没有工具的也整行不渲染
	// （有工具的节拍由 renderToolBeatItem 自己略过思考文字）。
	if !isShowReasoningEnabled(s) {
		switch item.kind {
		case timelineItemSingle:
			if isReasoningMessage(item.msg) {
				return ui.Element{}
			}
		case timelineItemToolBeat:
			if isReasoningMessage(item.msg) && len(item.beatTools) == 0 {
				return ui.Element{}
			}
		}
	}
	// 子线程胶囊行没有归它的子线程时整行不渲染：列表项仍在（Key 稳定），但既不占高度也不留间距。
	var capsuleLeadID string
	var capsuleBees []SwarmBee
	if item.kind == timelineItemSwarmCapsules {
		capsuleLeadID, capsuleBees = swarmTimelineCapsuleData(s, item.capsuleSpan)
		// 汇报已经出现在时间线里的子代理不再显示胶囊：普通会话里汇报行本身已有同名胶囊。
		capsuleBees = swarmCapsuleHideReported(s, capsuleBees)
		if len(capsuleBees) == 0 {
			return ui.Element{}
		}
	}
	// 派发占位行在没有占位内容时整行不渲染：列表项仍在（Key 稳定），但既不占高度也不留间距。
	var dispatchView swarmDispatchPlaceholderView
	if item.kind == timelineItemSwarmDispatch {
		dispatchView = swarmDispatchPlaceholder(s, liveID)
		if !dispatchView.Visible {
			return ui.Element{}
		}
	}
	// 底部统计行在有改动时让位给输入框上方的改动行，整行不渲染，避免底部多出一行空白。
	if item.kind == timelineItemSessionUsage && sessionStatsRowInComposer(s, item.threadID) {
		return ui.Element{}
	}
	colPadX := timelineColumnPadX(s)
	// 回合完成块放进列表行渲染时不带时间戳；只有在没有后端耗时记在一轮用量上时，才从现有字段推导：
	// 本回合用户消息的 CreatedAt 到该轮完成时间。同轮的多段助手回复共用这个区间，每个回合只渲染一次完成块。
	turnDur := time.Duration(0)
	if isLastAssistant && hasUsage && turnUsage.DurationMs <= 0 {
		start := turnStartTimeFor(s, item.turnIndex)
		if d, ok := turnEndDuration(start, item.finishedAt); ok {
			turnDur = d
		}
	}
	return flashTimelineRow(c, s, item, ui.Row(c.Key("timeline-row-"+item.id)).
		FillWidth().
		Justify(ui.Center).
		Padding(topGap, 0, 0, 0).
		Children(func() {
			ui.Column(c.Key("timeline-col-"+item.id)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Padding(0, colPadX).Children(func() {
				switch item.kind {
				case timelineItemStewardTask:
					renderStewardTaskCard(c, s, item.id, item.stewardTask)
				case timelineItemStewardAmend:
					renderStewardTaskChip(c, s, item.id, item.stewardTask)
				case timelineItemStewardTaskChips:
					renderStewardTaskChips(c, s, item.id, item.stewardTasks)
				case timelineItemTurnTime:
					renderTurnTimeRow(c, s, item)
				case timelineItemToolGroup:
					renderToolGroupItem(c, s, item.tools, item.id, liveID, item.groupExpanded)
				case timelineItemToolBeat:
					renderToolBeatItem(c, s, item, liveID)
				case timelineItemTurnWork:
					// 回合的工作过程折叠成一行：运行中只显示「已处理 Ns」，结束后显示可展开的「用时」。
					renderTurnWorkItem(c, s, item, turnUsage, hasUsage)
				case timelineItemSwarmReport:
					// 合并后的汇报行：相邻汇报消息在同一个胶囊行里流式排列。
					msgs := item.reportMsgs
					if len(msgs) == 0 {
						msgs = []Message{item.msg}
					}
					renderSwarmReportRow(c, s, msgs...)
				case timelineItemSwarmCapsules:
					renderSwarmTimelineCapsules(c, s, item.id, capsuleLeadID, capsuleBees)
				case timelineItemSwarmDispatch:
					renderSwarmDispatchPlaceholder(c, item.id, dispatchView)
				case timelineItemPendingPlan:
					renderTimelinePlanCard(c, s, item.resolvedPlan, true)
				case timelineItemResolvedPlan:
					renderResolvedPlanCard(c, s, item.resolvedPlan)
				case timelineItemAnsweredQuestion:
					renderAnsweredQuestionCard(c, s, item.answeredQuestion)
				case timelineItemRunning:
					renderRunningIndicator(c, item.id+"-row")
				case timelineItemBackgroundWait:
					renderBackgroundWaitBar(c, s, item.id)
				case timelineItemSessionUsage:
					renderSessionUsageRow(c, s, item.threadID, item.id)
				case timelineItemForkOrigin:
					renderForkOriginRow(c, s, item)
				case timelineItemForwardedContext:
					renderForwardedContextRow(c, s, item)
				default:
					// 有折叠头承载完成信息时，收尾答复不再单独画完成行（只保留改动文件卡片）。
					suppressTurnEnd := item.workHeaderID != ""
					renderMessageItem(c, s, item.msg, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, turnDur, suppressTurnEnd)
				}
				// 工具组的行也可能承载回合完成块（例如以思考行收尾的工具组）：
				// 流式中的最后一条消息还在组内时不渲染，等它定稿后再算。
				if isGroupRowKind(item.kind) && isLastAssistant && hasUsage &&
					!containsMessageID(item.tools, liveID) {
					if m, ok := groupTurnEndMessage(item.tools); ok {
						renderTurnFinishedBlock(c, s, m, turnUsage, turnDur)
					}
				}
			})
		}))
}

// flashTimelineRow 给跳转落点所在行加上淡出高亮：背景用强调色的 14% 透明度、2pt 描边用 50%，
// 1.4s 内淡到透明；动画 key 带跳转序号，连续两次跳转时从头重播。未命中目标时原样返回。
func flashTimelineRow(c *ui.Context, s *chatState, item timelineItem, row ui.Element) ui.Element {
	s.mu.Lock()
	flashID, flashSeq := s.flashMessageID, s.flashSeq
	s.mu.Unlock()
	if reduceMotion(c) || flashID == "" || !timelineItemContainsMessageID(item, flashID) {
		return row
	}
	k := row.Keyframes(fmt.Sprintf("jump-flash-%d", flashSeq), []ui.Keyframe{
		{Value: 1},
		{At: 80 * time.Millisecond, Value: 1},
		{At: jumpFlashDuration, Value: 0, Ease: motionEaseStandard},
	})
	return row.Background(ThemeAccent(c).Alpha(0.14*k)).
		Border(2, ThemeAccent(c).Alpha(0.5*k))
}

// turnEndDuration 从界面现有的字段推导本回合生成时长：回合完成时间减去本回合用户消息的发送时间。
// 用户消息只带 CreatedAt（可能为空或不是 RFC3339）、助手消息只带完成时间，两者都拿得到且结束晚于开始时
// 返回 true，否则返回 false，调用方不显示输出速度。这个区间包含工具调用等非生成时间。
func turnEndDuration(start, end time.Time) (time.Duration, bool) {
	if start.IsZero() || !end.After(start) {
		return 0, false
	}
	return end.Sub(start), true
}

// turnStartTime 返回本回合用户消息的发送时间：取同回合第一条能解析的 CreatedAt，拿不到时返回零值。
// messageTime 解析消息的 CreatedAt（RFC3339 或 RFC3339Nano）；字段为空或格式不对时返回 false。
// 现阶段转换层只给用户消息填 CreatedAt，助手消息拿不到时间，也就不显示速度（需要后端补时间或耗时）。
func messageTime(m Message) (time.Time, bool) {
	if m.CreatedAt == "" {
		return time.Time{}, false
	}
	if at, err := time.Parse(time.RFC3339Nano, m.CreatedAt); err == nil {
		return at, true
	}
	if at, err := time.Parse(time.RFC3339, m.CreatedAt); err == nil {
		return at, true
	}
	return time.Time{}, false
}

func messageFinishedAt(m Message) time.Time {
	at, _ := messageTime(m)
	return at
}

// turnStartTime 返回本回合用户消息的发送时间：取同回合第一条能解析的消息时间，拿不到时返回零值。
func turnStartTime(messages []Message, turnIndex int) time.Time {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].TurnIndex == turnIndex {
			start, _ := turnAuthoritativeSpan(messages, messages[i])
			if !start.IsZero() {
				return start
			}
			break
		}
	}
	for _, m := range messages {
		if m.TurnIndex != turnIndex || m.Role != "user" {
			continue
		}
		if at, ok := messageTime(m); ok {
			return at
		}
	}
	return time.Time{}
}

// turnStartTimeFor 读取会话消息并取本回合用户消息的发送时间（需要持 chatState 锁）。
func turnStartTimeFor(s *chatState, turnIndex int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return turnStartTime(s.messages, turnIndex)
}

// renderRunningIndicator 渲染“回复中”指示行：会话运行期间常驻展示，
// 在时间线末尾提示正在等待回复（低频点阵图标，只重绘该图形）。key 为元素唯一 Key。
func renderRunningIndicator(c *ui.Context, key string) ui.Element {
	return ui.Row(c.Key(key)).Gap(Space3).AlignItems(ui.Center).Padding(Space0, 0).Children(func() {
		renderRunningGlyph(c, "timeline-reply-run", 12, ThemeSecondaryText(c))
		ui.Text(c, "正在回复…").FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
	})
}

// renderForwardedContextRow 渲染历史消息及其来源分隔线。
func renderForwardedContextRow(c *ui.Context, s *chatState, item timelineItem) {
	fc, loaded := s.loadedForwardedContext(item.threadID)
	info, ok := continuationStartInfoFor(s, item.threadID)
	if !loaded || !ok {
		return
	}
	earlier, tail := splitForwardedTail(parseForwardedTurns(fc.Content))
	count := 0
	for _, turn := range earlier {
		if turn.Role != "omitted" {
			count++
		}
	}
	ui.Column(c.Key(item.id + "-context")).FillWidth().Gap(turnGap).Children(func() {
		ui.Column(c.Key(item.id + "-history")).FillWidth().Opacity(0.7).Gap(turnGap).Selectable().Children(func() {
			expanded := s.forwardedContextExpanded(item.threadID, false)
			if count > 0 {
				label := fmt.Sprintf("更早 %d 条", count)
				btn := ui.ButtonBase(c.Key(item.id+"-earlier")).Label(label).Cursor(ui.CursorPointer).Padding(Space1, Space2).Radius(RadiusSM)
				if btn.Hovered() {
					btn.Background(ThemeHover(c))
				}
				if btn.Clicked() {
					s.toggleForwardedContext(item.threadID, false)
				}
				btn.Children(func() {
					icon := IconChevronRight()
					if expanded {
						icon = IconChevronDown()
					}
					ui.Icon(c, icon).Size(10, 10).TextColor(ThemeSecondaryText(c))
					ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Unselectable()
				})
			}
			renderEarlier := func() {
				for i, turn := range earlier {
					renderForwardedTurn(c, s, fmt.Sprintf("%s-earlier-%d", item.id, i), turn)
				}
			}
			if count > 0 {
				motionUnfoldBody(c, item.id+"-earlier-body", expanded, func() {
					ui.Column(c.Key(item.id + "-earlier-turns")).FillWidth().Gap(turnGap).Children(renderEarlier)
				})
			} else {
				renderEarlier()
			}
			for i, turn := range tail {
				renderForwardedTurn(c, s, fmt.Sprintf("%s-tail-%d", item.id, i), turn)
			}
		})
		renderForwardedOriginRow(c, s, item.id, info)
	})
}

func renderForwardedTurn(c *ui.Context, s *chatState, key string, turn forwardedTurn) {
	switch turn.Role {
	case "user":
		ui.Row(c.Key(key)).FillWidth().Justify(ui.End).Children(func() {
			ui.Column(c.Key(key+"-bubble")).MaxWidth(userBubbleMaxWidth(s)).Padding(Space4, Space5).Radius(RadiusLG).Background(userBubbleColor(c)).Children(func() {
				ui.Text(c, turn.Text).MaxWidthPercent(100).Selectable().FontSize(scaledFont(TypeBody)).LineHeight(1.45).TextColor(ThemePrimaryText(c))
			})
		})
	case "assistant":
		markdown.View(c.Key(key), turn.Text, &s.mdStaticOpts)
	case "omitted":
		ui.Row(c.Key(key)).FillWidth().AlignItems(ui.Center).Gap(Space3).Children(func() {
			ui.Box(c.Key(key + "-left")).Height(Hairline).Grow(1).Background(ThemeDivider(c))
			ui.Text(c, "…").FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Unselectable()
			ui.Box(c.Key(key + "-right")).Height(Hairline).Grow(1).Background(ThemeDivider(c))
		})
	}
}

func renderForwardedOriginRow(c *ui.Context, s *chatState, key string, info continuationStartInfo) {
	label := info.Label + "「" + info.DisplayTitle + "」"
	ui.Row(c.Key(key+"-origin-row")).FillWidth().AlignItems(ui.Center).Gap(Space3).Margin(0, 0, 10, 0).Children(func() {
		ui.Box(c.Key(key + "-origin-left")).Height(Hairline).Grow(1).Background(ThemeDivider(c))
		color := ThemeSecondaryText(c)
		if info.TargetThreadID != "" {
			color = ThemeAccent(c)
		}
		content := func() {
			ui.Icon(c, IconGitBranch()).Size(11, 11).TextColor(color)
			text := ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(color).SingleLine()
			if info.DisplayTitle != info.SourceTitle {
				text.Tooltip(info.SourceTitle)
			}
		}
		if info.TargetThreadID == "" {
			ui.Row(c.Key(key+"-origin-label")).Gap(Space2).AlignItems(ui.Center).Padding(Space0, Space1).Children(content)
		} else {
			btn := ui.ButtonBase(c.Key(key+"-origin-link")).Label(label).Cursor(ui.CursorPointer).Padding(Space0, Space1).Radius(RadiusSM)
			if btn.Hovered() {
				btn.Background(ThemeHover(c))
			}
			if btn.Clicked() {
				s.headerSelectThread(info.TargetThreadID)
			}
			btn.Children(content)
		}
		ui.Box(c.Key(key + "-origin-right")).Height(Hairline).Grow(1).Background(ThemeDivider(c))
	})
}

// renderForkOriginRow 渲染可跳转的 fork 来源条；来源缺失时显示已删除。
func renderForkOriginRow(c *ui.Context, s *chatState, item timelineItem) {
	sourceThreadID := item.forkFromThreadID
	title := mentionThreadTitle(s, sourceThreadID)
	missing := title == ""
	if missing {
		title = "已删除的会话"
	}
	label := "分叉自「" + title + "」"
	ui.Row(c.Key(item.id+"-row")).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Margin(0, 0, 10, 0).
		Children(func() {
			ui.Box(c.Key(item.id + "-line-left")).Height(Hairline).Grow(1).Background(ThemeDivider(c))
			if missing {
				ui.Row(c.Key(item.id+"-label")).Gap(Space2).AlignItems(ui.Center).Padding(Space0, Space1).Children(func() {
					ui.Icon(c, IconGitBranch()).Size(11, 11).TextColor(ThemeSecondaryText(c))
					ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).SingleLine()
				})
			} else {
				btn := ui.ButtonBase(c.Key(item.id+"-link")).
					Label(label).
					Cursor(ui.CursorPointer).
					Padding(Space0, Space1).
					Radius(RadiusSM)
				if btn.Hovered() {
					btn.Background(ThemeHover(c))
				}
				if btn.Clicked() {
					s.headerSelectThread(sourceThreadID)
				}
				btn.Children(func() {
					ui.Icon(c, IconGitBranch()).Size(11, 11).TextColor(ThemeAccent(c))
					ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(ThemeAccent(c)).SingleLine()
				})
			}
			ui.Box(c.Key(item.id + "-line-right")).Height(Hairline).Grow(1).Background(ThemeDivider(c))
		})
}

// renderSessionUsageRow 渲染时间线底部的会话统计入口：右对齐的一个小号「会话统计」按钮，
// 点击弹出完整会话统计浮层（见 session_stats_popover.go）。有改动时按钮由输入框上方的
// 改动行承载（sessionStatsRowInComposer），这一行已在 renderTimelineItem 里跳过。
func renderSessionUsageRow(c *ui.Context, s *chatState, threadID, key string) {
	ui.Row(c.Key(key)).
		FillWidth().
		Justify(ui.End).
		Margin(4, 0, 0, 0).
		Children(func() {
			renderSessionStatsButton(c, s, threadID, key)
		})
}

// renderResolvedPlanCard 默认只显示一行，整行点击展开计划。
func renderResolvedPlanCard(c *ui.Context, s *chatState, plan resolvedPlan) {
	renderTimelinePlanCard(c, s, plan, false)
}

func renderTimelinePlanCard(c *ui.Context, s *chatState, plan resolvedPlan, pending bool) {
	id := plan.ID
	if id == "" {
		id = fmt.Sprintf("plan-%d", plan.Time.UnixNano())
	}
	s.mu.Lock()
	collapsed, known := s.collapsedPlans[id]
	if !known {
		collapsed = true
	}
	s.mu.Unlock()
	key := "resolved-plan-" + id
	if pending {
		key = pendingPlanCardID(id)
		collapsed = false
	}
	ui.Column(c.Key(key)).Radius(RadiusXL).Background(ThemeSurface(c)).
		Border(Hairline, ThemeBorder(c)).Padding(Space4).Gap(Space3).
		MaxWidth(timelineContentMaxWidth(s)).Children(func() {
		header := ui.Row(c.Key(key + "-header")).Gap(Space3).AlignItems(ui.Center).FillWidth()
		if !pending {
			header.Cursor(ui.CursorPointer).OnClick(func() {
				s.mu.Lock()
				if s.collapsedPlans == nil {
					s.collapsedPlans = make(map[string]bool)
				}
				s.collapsedPlans[id] = !collapsed
				s.mu.Unlock()
				s.holdChatPlace()
				s.invalidate()
			})
		}
		header.Children(func() {
			ui.Icon(c, IconClipboardList()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
			if !pending {
				status := "计划 · 已驳回"
				if plan.Approved {
					status = "计划 · 已批准"
				}
				ui.Text(c, status).FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c)).Shrink(0)
			}
			ui.Text(c, planTitle(plan.Plan)).Bold().FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c)).MaxLines(1).Shrink(1)
			ui.Spacer(c)
			renderPlanCopyButton(c, key+"-copy", plan.Plan)
		})
		if !collapsed {
			body := ui.Column(c.Key(key + "-body")).FillWidth().Gap(Space3).
				Transition(motionCollapse(c)).Selectable()
			body = body.ContextMenu(messageContextMenuWithSelection(c, s, Message{Role: "assistant", Text: plan.Plan}, func() []ui.SelectedText {
				return body.SelectedTexts()
			}))
			body.Children(func() {
				renderPlanMarkdown(c, s, key+"-markdown", plan.Plan, 0)
				if !pending && !plan.Approved && plan.Feedback != "" {
					ui.Row(c.Key(key+"-feedback")).BorderStartWidth(Space1).BorderColor(ThemeBorder(c)).Padding(0, Space3).Children(func() {
						ui.Text(c, plan.Feedback).FontSize(scaledFont(TypeBody)).TextColor(ThemeSecondaryText(c)).Selectable()
					})
				}
			})
		}
	})
}

// truncateContinuationSourceTitle 安全截断来源标题：按 rune 截断，不会切坏多字节字符或 emoji。
func truncateContinuationSourceTitle(title string) string {
	runes := []rune(strings.TrimSpace(title))
	if len(runes) <= continuationSourceTitleMaxRunes {
		return string(runes)
	}
	return string(runes[:continuationSourceTitleMaxRunes-len("...")]) + "..."
}

// parseContinuationStartTitle 解析会话标题的转发/分叉前缀，返回来源标题与提示语。
// 前缀后没有标题（如仅 "Continued: "）时视为普通标题，不展示引导。
func parseContinuationStartTitle(title string) (sourceTitle, label string, ok bool) {
	trimmed := strings.TrimSpace(title)
	for _, p := range continuationStartPrefixes {
		rest, found := strings.CutPrefix(trimmed, p.prefix)
		if !found {
			continue
		}
		source := strings.TrimSpace(rest)
		if source == "" {
			return "", "", false
		}
		return source, p.label, true
	}
	return "", "", false
}

// continuationStartFor 判断当前空会话是否需要展示转发/分叉引导卡片（Hero 模式）：
// 会话没有消息、且来源字段（新会话）或标题前缀（旧会话兼容）表明是转发/分叉（或已加载到带入上下文）时返回卡片内容。
// 标题前缀只用于兼容旧会话，新会话按来源字段判定。
func continuationStartFor(s *chatState, threadID string) (continuationStartInfo, bool) {
	if s == nil {
		return continuationStartInfo{}, false
	}
	s.mu.Lock()
	hasMessages := len(s.messages) > 0
	s.mu.Unlock()
	if hasMessages {
		return continuationStartInfo{}, false
	}
	return continuationStartInfoFor(s, threadID)
}

// continuationStartInfoFor 解析来源引导卡片所需的信息，不关心会话是否已有消息：
// Hero 与停靠两种模式共用它，前者额外要求会话为空。新会话按来源字段判定，标题前缀仅用于兼容旧会话；
// 字段为空时保留标题前缀解析及转发上下文回退逻辑。
func continuationStartInfoFor(s *chatState, threadID string) (continuationStartInfo, bool) {
	if s == nil {
		return continuationStartInfo{}, false
	}
	var (
		foundThread         bool
		forkedFromThreadID  string
		forkedFromMessageID string
		sourceTitle         string
		sourceInThreads     bool
	)
	s.mu.Lock()
	for _, th := range s.threads {
		if th.ID == threadID {
			forkedFromThreadID = th.ForkedFromThreadID
			forkedFromMessageID = th.ForkedFromMessageID
			foundThread = true
			break
		}
	}
	if !foundThread && s.isPendingThreadLocked(threadID) {
		forkedFromThreadID = s.pendingThread.Thread.ForkedFromThreadID
		forkedFromMessageID = s.pendingThread.Thread.ForkedFromMessageID
	}
	if forkedFromThreadID != "" {
		for _, th := range s.threads {
			if th.ID == forkedFromThreadID {
				sourceTitle = th.Title
				sourceInThreads = true
				break
			}
		}
	}
	s.mu.Unlock()

	if forkedFromThreadID != "" {
		label := "继续自"
		if forkedFromMessageID != "" {
			label = "分叉自"
		}
		targetThreadID := ""
		if sourceInThreads {
			targetThreadID = forkedFromThreadID
		}
		source := strings.TrimSpace(sourceTitle)
		if source == "" {
			fc, loaded := s.loadedForwardedContext(threadID)
			if loaded {
				source = strings.TrimSpace(fc.SourceTitle)
			}
		}
		if source == "" {
			return continuationStartInfo{}, false
		}
		return continuationStartInfo{
			Label:          label,
			SourceTitle:    source,
			DisplayTitle:   truncateContinuationSourceTitle(source),
			TargetThreadID: targetThreadID,
		}, true
	}

	title, projectID := s.continuationThreadTitle(threadID)
	source, label, ok := parseContinuationStartTitle(title)
	if !ok {
		fc, loaded := s.loadedForwardedContext(threadID)
		if !loaded || strings.TrimSpace(fc.SourceTitle) == "" {
			return continuationStartInfo{}, false
		}
		source, label = strings.TrimSpace(fc.SourceTitle), "继续自"
	}
	return continuationStartInfo{
		Label:          label,
		SourceTitle:    source,
		DisplayTitle:   truncateContinuationSourceTitle(source),
		TargetThreadID: s.continuationTargetThread(threadID, projectID, source),
	}, true
}

// continuationThreadTitle 返回会话标题与所属项目；待创建会话还没有会话行时取自占位状态。
func (s *chatState) continuationThreadTitle(threadID string) (title, projectID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.Title, th.ProjectID
		}
	}
	if s.isPendingThreadLocked(threadID) {
		return s.pendingThread.Thread.Title, s.pendingThread.ProjectID
	}
	return "", ""
}

// continuationTargetThread 在同一项目内按来源标题找到原会话，供「打开原会话」按钮跳转。
func (s *chatState) continuationTargetThread(threadID, projectID, sourceTitle string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID || th.Title != sourceTitle {
			continue
		}
		if projectID != "" && th.ProjectID != projectID {
			continue
		}
		return th.ID
	}
	return ""
}

func getForwardedContextState(s *chatState) *forwardedContextState {
	forwardedContextStateMu.Lock()
	defer forwardedContextStateMu.Unlock()
	st, ok := forwardedContextStates[s]
	if !ok {
		st = &forwardedContextState{
			contexts: make(map[string]ForwardedContext),
			loaded:   make(map[string]bool),
			inflight: make(map[string]bool),
			expanded: make(map[string]bool),
		}
		forwardedContextStates[s] = st
	}
	return st
}

// releaseForwardedContextState 在窗口关闭时丢弃该会话的转发上下文缓存，与头部状态同步释放。
func releaseForwardedContextState(s *chatState) {
	forwardedContextStateMu.Lock()
	defer forwardedContextStateMu.Unlock()
	delete(forwardedContextStates, s)
}

// loadForwardedContext 在后台拉取会话带入的转发上下文并按 threadID 缓存。
// 渲染路径不能做同步 RPC：这里只起一次 goroutine，inflight 防止每帧重复发起；
// 拉取失败或没有上下文都记为已加载，不再重试。
func (s *chatState) loadForwardedContext(threadID string) {
	if threadID == "" || s.client == nil {
		return
	}
	st := getForwardedContextState(s)
	st.mu.Lock()
	if st.loaded[threadID] || st.inflight[threadID] {
		st.mu.Unlock()
		return
	}
	st.inflight[threadID] = true
	st.mu.Unlock()

	go func() {
		fc, ok, err := s.client.ThreadForwardedContext(context.Background(), threadID)
		st.mu.Lock()
		delete(st.inflight, threadID)
		st.loaded[threadID] = true
		if err == nil && ok {
			st.contexts[threadID] = fc
		} else {
			delete(st.contexts, threadID)
		}
		st.mu.Unlock()
		s.invalidate()
	}()
}

// loadedForwardedContext 读取已加载的转发上下文；第二个返回值表示是否已加载完成。
func (s *chatState) loadedForwardedContext(threadID string) (ForwardedContext, bool) {
	st := getForwardedContextState(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.loaded[threadID] {
		return ForwardedContext{}, false
	}
	return st.contexts[threadID], true
}

func (s *chatState) hasForwardedContextBody(threadID string) bool {
	s.loadForwardedContext(threadID)
	fc, loaded := s.loadedForwardedContext(threadID)
	return loaded && strings.TrimSpace(fc.Content) != ""
}

func parseForwardedTurns(content string) []forwardedTurn {
	var turns []forwardedTurn
	var role string
	var lines []string
	flush := func() {
		if role == "" {
			return
		}
		text := strings.TrimSpace(strings.Join(lines, "\n"))
		if text == "[trimmed]" {
			text = "…"
		} else if strings.HasSuffix(text, "\n[trimmed]") {
			text = strings.TrimSuffix(text, "[trimmed]") + "…"
		}
		turns = append(turns, forwardedTurn{Role: role, Text: text})
		lines = nil
	}
	previousBlank := true
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r")
		newRole := ""
		if previousBlank && line == "User:" {
			newRole = "user"
		} else if previousBlank && line == "Assistant:" {
			newRole = "assistant"
		} else if strings.HasPrefix(line, "[…") && strings.HasSuffix(line, "…]") {
			newRole = "omitted"
		}
		if newRole != "" {
			flush()
			role = newRole
			if role == "omitted" {
				turns = append(turns, forwardedTurn{Role: role, Text: line})
				role = ""
			}
		} else if role != "" {
			lines = append(lines, line)
		}
		previousBlank = strings.TrimSpace(line) == ""
	}
	flush()
	if len(turns) == 0 {
		return []forwardedTurn{{Role: "assistant", Text: strings.TrimSpace(content)}}
	}
	return turns
}

func splitForwardedTail(turns []forwardedTurn) (earlier, tail []forwardedTurn) {
	if len(turns) == 0 {
		return nil, nil
	}
	start := len(turns) - 1
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "user" {
			start = i
			break
		}
	}
	return turns[:start], turns[start:]
}

// forwardedContextExpanded 返回卡片的展开状态：用户点击过就按用户的来，否则用模式默认值。
func (s *chatState) forwardedContextExpanded(threadID string, defaultExpanded bool) bool {
	st := getForwardedContextState(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	if v, ok := st.expanded[threadID]; ok {
		return v
	}
	return defaultExpanded
}

// toggleForwardedContext 切换卡片展开状态，并按会话记住用户的选择。
func (s *chatState) toggleForwardedContext(threadID string, defaultExpanded bool) {
	st := getForwardedContextState(s)
	st.mu.Lock()
	cur := defaultExpanded
	if v, ok := st.expanded[threadID]; ok {
		cur = v
	}
	st.expanded[threadID] = !cur
	st.mu.Unlock()
	s.invalidate()
}

// renderContinuationStartCard 在空转发/分叉会话的空态位置渲染居中的来源引导卡片，
// 返回是否渲染了卡片：渲染后调用方不再展示通用的新会话引导。
func renderContinuationStartCard(c *ui.Context, s *chatState, threadID string) bool {
	info, ok := continuationStartFor(s, threadID)
	if !ok {
		// 标题被改过的空转发会话解析不到前缀：也试一次加载，加载成功后下一帧按来源标题显示。
		if _, loaded := s.loadedForwardedContext(threadID); !loaded {
			s.loadForwardedContext(threadID)
		}
		return false
	}
	return renderContinuationContextCard(c, s, info)
}

// renderContinuationContextCard 渲染无带入内容时的来源卡片。
func renderContinuationContextCard(c *ui.Context, s *chatState, info continuationStartInfo) bool {
	key := "continuation-start"

	ui.Column(c.Key(key+"-card")).
		FillWidth().
		MaxWidth(continuationStartCardMaxWidth).
		AlignSelf(ui.Center).
		AlignItems(ui.Start).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusXL).
		Padding(Space5).
		Gap(Space4).
		Margin(0, 0, 22, 0).
		Children(func() {
			// 来源标题过长时用 tooltip 展示完整标题。
			header := ui.Row(c.Key(key + "-header")).
				FillWidth().
				Gap(Space3).
				AlignItems(ui.Center)
			header.Children(func() {
				ui.Icon(c, IconGitBranch()).Size(14, 14).TextColor(ThemeAccent(c))
				label := info.Label + "「" + info.DisplayTitle + "」"
				origin := ui.Text(c, label).
					Grow(1).
					FontSize(scaledFont(TypeBody)).
					TextColor(ThemeSecondaryText(c)).
					SingleLine()
				if info.DisplayTitle != info.SourceTitle {
					// 标题被截断时用 tooltip 补回完整原文，卡片宽度不必为长标题让步。
					origin.Tooltip(info.SourceTitle)
				}
			})

			if info.TargetThreadID != "" {
				renderOpenOriginalThreadButton(c, s, key, info.TargetThreadID)
			}
		})
	return true
}

// renderOpenOriginalThreadButton 渲染「打开原会话」按钮：点击切回来源会话。
func renderOpenOriginalThreadButton(c *ui.Context, s *chatState, key, targetThreadID string) {
	ui.Row(c.Key(key+"-actions")).Margin(2, 0, 0, 0).Children(func() {
		Button(c.Key(key+"-open-original"), ButtonOpts{
			Text:    "打开原会话",
			Variant: ButtonSecondary,
			Size:    ControlMD,
			OnClick: func() {
				s.selectThread(targetThreadID)
			},
		})
	})
}

// renderHostEventCard 渲染宿主转来的事件消息：这类消息以 Role user 落库，但不是用户自己说的话，
// 所以不套用户气泡，也不给编辑、重发、回退等操作，而是一张整宽卡片。
func renderHostEventCard(c *ui.Context, s *chatState, m Message) ui.Element {
	return ui.Column(c.Key("msg-host-event-"+m.ID)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		AlignSelf(ui.Center).
		AlignItems(ui.Start).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusXL).
		Padding(Space4, Space5, Space4, Space5).
		Gap(Space3).
		Margin(0, 0, 22, 0).
		Children(func() {
			ui.Row(c.Key("msg-host-event-meta-" + m.ID)).Gap(Space2).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconSparkles()).Size(11, 11).TextColor(ThemeSecondaryText(c).Alpha(0.7))
				ui.Text(c, "宿主转来").
					FontSize(scaledFont(FontSizeRelativeTime)).
					TextColor(ThemeSecondaryText(c).Alpha(0.85))
				if at, ok := messageTime(m); ok {
					ui.Text(c, "·").
						FontSize(scaledFont(FontSizeRelativeTime)).
						TextColor(ThemeSecondaryText(c).Alpha(0.5))
					ui.Text(c, formatRelativeTime(at)).
						FontSize(scaledFont(FontSizeRelativeTime)).
						TextColor(ThemeSecondaryText(c).Alpha(0.85))
				}
			})
			// 正文与普通消息一致：同样的字号、行高与可选中，只是没有气泡底色。
			ui.Text(c, m.Text).
				FillWidth().
				Selectable().
				FontSize(scaledFont(TypeBody)).
				LineHeight(1.45).
				TextColor(ThemePrimaryText(c))
		})
}
