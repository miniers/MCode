// 助手消息与思考内容的渲染。
package mcodeui

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"strings"
	"time"
)

// renderMessageItem 渲染单条时间线消息。
func renderMessageItem(c *ui.Context, s *chatState, m Message, liveID, lastLiveID string, isLastAssistant bool, hasUsage bool, turnUsage TokenUsage, turnDur time.Duration, suppressTurnEnd bool) ui.Element {
	switch {
	case m.Kind == stewardReplyKind:
		return renderStewardReply(c, s, m)
	case m.Kind == stewardDecisionKind:
		return renderStewardDecisionCard(c, s, m)
	case m.Kind == stewardDecisionAnswerKind:
		return ui.Element{}
	case m.Kind == stewardResultKind:
		return renderStewardResult(c, s, m, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, turnDur, suppressTurnEnd)
	case m.Role == "user" && m.Kind == hostEventKind:
		return renderHostEventCard(c, s, m)

	case m.Role == "user":
		return renderUserMessage(c, s, m)

	case m.Role == "error" || m.Kind == "error" || strings.HasPrefix(m.Role, "error"):
		return renderErrorMessage(c, s, m)

	case isReasoningMessage(m):
		if !isShowReasoningEnabled(s) {
			return ui.Element{}
		}
		return renderReasoningMessage(c, s, m, liveID, isLastAssistant, hasUsage, turnUsage, turnDur)

	case m.Role == "tool":
		return renderSingleToolItem(c, s, m)

	case m.Kind == splitSessionKind:
		return renderSplitDivider(c, m)

	case m.Kind == contextCompactedKind:
		return renderContextCompactedDivider(c, m)

	case m.Role == "system":
		return renderSystemMessage(c, s, m)

	default:
		// assistant 角色
		return renderAssistantMessage(c, s, m, liveID, lastLiveID, isLastAssistant, hasUsage, turnUsage, turnDur, suppressTurnEnd)
	}
}

// renderContextCompactedDivider 渲染「上下文已压缩」边界行：左右两条淡色横线夹一段灰色小字，
// 与分割线同一套克制样式。它只是界线，上面的历史照常显示、可以滚动回看。
func renderContextCompactedDivider(c *ui.Context, m Message) ui.Element {
	text := strings.TrimSpace(m.Text)
	if text == "" {
		text = contextCompactedNotice
	}
	return ui.Row(c.Key("context-compacted-"+m.ID)).
		FillWidth().
		AlignItems(ui.Center).
		Gap(Space3).
		Margin(0, 0, 10, 0).
		Children(func() {
			ui.Box(c.Key("context-compacted-left-" + m.ID)).
				Height(Hairline).
				Grow(1).
				Background(ThemeDivider(c))
			ui.Text(c, text).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
			ui.Box(c.Key("context-compacted-right-" + m.ID)).
				Height(Hairline).
				Grow(1).
				Background(ThemeDivider(c))
		})
}

// shouldShowAssistantActions 判断助手回复是否渲染 hover 操作栏：只有本回合最后一条助手回复、
// 非流式、正文非空且已完全显示时才渲染；同轮中间的回复只保留正文与右键菜单。
func shouldShowAssistantActions(m Message, isStreaming, isLastAssistant, revealed bool) bool {
	return isLastAssistant && !isStreaming && revealed && strings.TrimSpace(m.Text) != ""
}

// messageActionBarVisible 判断消息操作栏此刻是否显示：指针停在消息行上，或复制对勾反馈仍在
// 显示窗口内（刚复制完就把指针移开时，反馈不能被立刻藏起来）。
func messageActionBarVisible(rowHovered, copied bool) bool {
	return rowHovered || copied
}

// renderCopyMessageButton 渲染消息操作栏的复制按钮：自绘无边框浅底胶囊，点击把正文原文写入剪贴板，
// 1.4 秒内图标换成绿色对勾。助手回复与用户提问的操作栏共用这一个按钮。
func renderCopyMessageButton(c *ui.Context, s *chatState, m Message, key string) {
	copied := copyFeedbackActive(key, time.Now())
	CopyButton(c, CopyButtonProps{
		Key: key, Label: "复制", Tooltip: "复制正文", Text: "复制", Copied: copied,
		Value: func() string { return m.Text }, OnCopy: func() { markCopyFeedback(s, key) },
	})
}

// stripSwarmFenceCached 返回去掉 swarm 派单代码块的正文，按消息 ID 缓存：
// 同一条消息每帧渲染时正文通常不变，而切行、小写比较、正则替换与拼接都要重做一遍；
// 浮窗同时打开多个时可见消息数成倍增长，这里是最直接的浪费。
func (s *chatState) stripSwarmFenceCached(m Message) string {
	if s == nil || m.ID == "" {
		return stripSwarmFence(m.Text)
	}
	s.mu.Lock()
	entry, ok := s.swarmFenceCache[m.ID]
	s.mu.Unlock()
	if ok && entry.src == m.Text {
		return entry.out
	}
	out := stripSwarmFence(m.Text)
	s.mu.Lock()
	if s.swarmFenceCache == nil {
		s.swarmFenceCache = make(map[string]swarmFenceEntry, swarmFenceCacheMax)
	} else if len(s.swarmFenceCache) >= swarmFenceCacheMax {
		s.swarmFenceCache = make(map[string]swarmFenceEntry, swarmFenceCacheMax)
	}
	s.swarmFenceCache[m.ID] = swarmFenceEntry{src: m.Text, out: out}
	s.mu.Unlock()
	return out
}

// renderAssistantMessage 渲染无气泡平铺、左对齐的助手 Markdown 回复（按 2.2、4.1 节）。
func renderAssistantMessage(c *ui.Context, s *chatState, m Message, liveID, lastLiveID string, isLastAssistant bool, hasUsage bool, turnUsage TokenUsage, turnDur time.Duration, suppressTurnEnd bool) ui.Element {
	m.Text = s.stripSwarmFenceCached(m)
	ensureCodeBlockContainer(s)
	isStreamingNow := liveID != "" && m.ID == liveID
	// 读上一帧记下的完全显示状态：头部与操作栏跟正文同帧渲染，晚一帧补上可以接受。
	revealed := s.assistantRevealed(m.ID)
	// 流式中先不播；IsFinal 一置位就算完成态，即便 liveID 还没切走也补播一次。
	enter := s.beginMessageEnter(m, isStreamingNow && !m.IsFinal, c.Now())

	col := ui.Column(c.Key("msg-md-" + m.ID)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Gap(Space1).Selectable()
	col = col.ContextMenu(messageContextMenuWithSelection(c, s, m, func() []ui.SelectedText {
		return col.SelectedTexts()
	}))
	// 整行 hover：指针停在这条消息的正文、元信息或操作栏上都算，操作栏据此显隐
	rowHovered := col.Hovered()

	col.Children(func() {
		// 最终回复上方的元信息行：模型图标、模型名、耗时与时间戳
		renderAssistantHeader(c, s, m, revealed)

		contentRevealed := renderAssistantContentWithMentions(c, s, m, isStreamingNow, lastLiveID)
		// 本会话里流式输出过的消息先记下「以流式状态渲染过」，正文显示完才露出时间与操作栏。
		s.markAssistantRevealed(m.ID, isStreamingNow || lastLiveID != "" && m.ID == lastLiveID, contentRevealed)

		// 助手回复下方淡色小图标操作栏（复制、分支、回报父会话、更多）：默认隐藏，hover 才显示。
		// 隐藏时只把不透明度置零，占位高度照旧，hover 时布局不跳动。
		lastLiveAssistant := lastLiveID != "" && m.ID == lastLiveID && s.lastAssistantPerTurn()[m.TurnIndex] == m.ID
		if shouldShowAssistantActions(m, isStreamingNow, isLastAssistant || lastLiveAssistant, revealed) {
			copyKey := "copy-btn-" + m.ID
			bar := ui.Row(c.Key("msg-actions-"+m.ID)).
				AlignItems(ui.Center).
				Gap(Space3).
				Margin(2, 0, 0, 0)
			if !messageActionBarVisible(rowHovered, copyFeedbackActive(copyKey, time.Now())) {
				bar.Opacity(0)
			}
			bar.Children(func() {
				renderCopyMessageButton(c, s, m, copyKey)

				// 分支按钮：确认后从这条回复分出新会话
				renderBranchActionButton(c, s, m)

				// 回报父会话按钮：只在子会话（Swarm bee）中显示
				if leadID := parentThreadID(s); leadID != "" {
					renderSendToParentButton(c, s, m, leadID)
				}

				// 更多按钮：弹出与右键完全同一个菜单
				renderMessageMoreButton(c, s, m)
			})
		}

		// 回合完成块：有折叠头时完成信息已经移到折叠头上，这条答复只保留改动文件卡片；
		// 没有折叠头（纯答复回合）才画完整的完成行。
		if suppressTurnEnd {
			renderTurnFilesCard(c, s, m)
		} else if !isStreamingNow && isLastAssistant && hasUsage {
			renderTurnFinishedBlock(c, s, m, turnUsage, turnDur)
		}
	})

	return animateMessageEnter(c, col, m.ID, enter)
}

// ensureCodeBlockContainer 为助手正文的 Markdown 代码块安装带语言标题栏与复制按钮的容器（只安装一次）。
// mygo 的 Markdown 组件不支持在渲染前拆出围栏代码块，但提供 Options.CodeBlock 钩子，用它包出标题栏即可，
// 流式渲染、语法高亮、横向滚动与选区复制都保持原样。
func ensureCodeBlockContainer(s *chatState) {
	if s.mdStaticOpts.CodeBlock != nil {
		return
	}
	renderer := func(c *ui.Context, b *markdown.CodeBlockView) {
		renderCodeBlockContainer(c, s, b)
	}
	s.mdStaticOpts.CodeBlock = renderer
	s.mdOpts.CodeBlock = renderer
}

// renderCodeBlockContainer 渲染围栏代码块：顶部标题栏左侧是语言名、右侧是复制按钮，正文走 mygo 的高亮块。
func renderCodeBlockContainer(c *ui.Context, s *chatState, b *markdown.CodeBlockView) {
	lang := strings.TrimSpace(b.Language)
	if lang == "" {
		lang = "代码"
	}
	key := fmt.Sprintf("code-block-%d", b.Index)

	ui.Column(c.Key(key)).
		Direction(ui.LeftToRight).
		AlignSelf(ui.Stretch).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Radius(RadiusLG).
		Clip().
		Children(func() {
			ui.Row(c.Key(key+"-header")).
				Height(codeBlockHeaderHeight).
				AlignItems(ui.Center).
				Padding(Space1, Space2).
				Background(ThemeHover(c)).
				BorderWidth(0, 0, 1, 0).
				BorderColor(ThemeBorder(c)).
				Children(func() {
					ui.Text(c, lang).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c)).Unselectable()
					ui.Spacer(c)
					copyKey := key + "-copy"
					copied := copyFeedbackActive(copyKey, time.Now())
					CopyButton(c, CopyButtonProps{
						Key: copyKey, Label: "复制代码", Tooltip: "复制代码", Text: "复制",
						Copied: copied, Disabled: b.Open, Value: b.Code,
						OnCopy: func() {
							markCopyFeedback(s, copyKey)
							s.setToast("已复制")
						},
					})
				})
			b.Body(c)
		})
}

// assistantMetaText 拼装助手回复头部的元信息文本：模型名、耗时（如 12s）与时间戳（HH:mm），空的部分省略。
func assistantMetaText(model string, duration time.Duration, at time.Time) string {
	parts := make([]string, 0, 3)
	if name := strings.TrimSpace(model); name != "" {
		parts = append(parts, name)
	}
	if duration > 0 {
		parts = append(parts, formatShortDuration(duration))
	}
	if !at.IsZero() {
		parts = append(parts, at.Format("15:04"))
	}
	return strings.Join(parts, " · ")
}

// formatShortDuration 把耗时格式化为「12s」「1.2s」「2m30s」这类短文本。
func formatShortDuration(d time.Duration) string {
	if d < time.Minute {
		secs := d.Seconds()
		if secs < 10 {
			return fmt.Sprintf("%.1fs", secs)
		}
		return fmt.Sprintf("%ds", int(secs+0.5))
	}
	mins := int(d / time.Minute)
	secs := int((d % time.Minute) / time.Second)
	if secs == 0 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dm%ds", mins, secs)
}

// assistantMetaSource 返回助手回复头部可用的元信息：供应商、模型名（缺省用供应商）与时间戳。
// UI 的消息结构不带时间戳：只有会话空闲时最后一条消息才能用会话更新时间近似出 HH:mm。
func assistantMetaSource(s *chatState, m Message) (provider, model string, at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var th Thread
	found := false
	for _, t := range s.threads {
		if t.ID == s.selectedThreadID {
			th = t
			found = true
			break
		}
	}
	if !found {
		return "", "", time.Time{}
	}
	model = strings.TrimSpace(th.Model)
	if model == "" {
		model = strings.TrimSpace(th.Provider)
	}
	if !th.IsRunning && len(s.messages) > 0 && s.messages[len(s.messages)-1].ID == m.ID {
		at = th.UpdatedAt
	}
	return th.Provider, model, at
}

// renderAssistantHeader 渲染助手最终回复上方的供应商图标与时间戳：正文完全显示后才出现，
// 不在打字动画进行中途弹出来。
func renderAssistantHeader(c *ui.Context, s *chatState, m Message, revealed bool) {
	if !m.IsFinal || !revealed {
		return
	}
	provider, _, at := assistantMetaSource(s, m)
	// 模型由回合完成块按需显示。
	meta := assistantMetaText("", 0, at)
	if provider == "" && meta == "" {
		return
	}
	ui.Row(c.Key("msg-meta-"+m.ID)).Gap(Space2).AlignItems(ui.Center).Margin(0, 0, 2, 0).Children(func() {
		if provider != "" {
			renderProviderIcon(c, provider, 11)
		}
		if meta != "" {
			ui.Text(c, meta).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
		}
	})
}

// reasoningHeaderLabel 返回思考行的标题文案：流式中显示「思考中…」，结束后显示「思考过程」。
func reasoningHeaderLabel(streaming bool) string {
	if streaming {
		return "思考中…"
	}
	return "思考过程"
}

// reasoningMarkdownOptions 返回独立思考行（基准字号 12）的 Markdown 配置。
func reasoningMarkdownOptions(c *ui.Context, s *chatState) *markdown.Options {
	return reasoningMarkdownOptionsAt(c, s, 12)
}

// toolBeatMarkdownOptions 返回工具组节拍内思考叙事（基准字号 13）的 Markdown 配置。
func toolBeatMarkdownOptions(c *ui.Context, s *chatState) *markdown.Options {
	return reasoningMarkdownOptionsAt(c, s, 13)
}

// reasoningMarkdownOptionsAt 返回指定基准字号的思考正文 Markdown 配置：小字号、次要文本色，其余留零值由 DefaultTheme 补齐。
// 这里只建一个稀疏主题并复用助手正文的代码块容器与揭示节奏，不整体复制 markdown.Options
// （它内部带 sync.Map 图片缓存，复制会触发 go vet 的 copylocks 且语义不正确）。
func reasoningMarkdownOptionsAt(c *ui.Context, s *chatState, baseSize float32) *markdown.Options {
	ensureCodeBlockContainer(s)

	key := reasoningMarkdownKey{state: s, size: baseSize}
	reasoningMDOptsMu.Lock()
	opts := reasoningMDOpts[key]
	if opts == nil {
		opts = &markdown.Options{}
		reasoningMDOpts[key] = opts
	}
	reasoningMDOptsMu.Unlock()

	if opts.Theme == nil {
		opts.Theme = &markdown.Theme{
			FontSize:   baseSize,
			LineHeight: 1.45,
			Text:       ThemeSecondaryText(c),
			Muted:      ThemeSecondaryText(c).Alpha(0.75),
		}
	}
	// 代码块跟随用户在设置里选择的等宽字体与字号（每帧同步，改动立即生效）
	if base := s.mdStaticOpts.Theme; base != nil {
		opts.Theme.Font, opts.Theme.CodeFont, opts.Theme.CodeSize = base.Font, base.CodeFont, base.CodeSize
	}
	// 思考正文字号同样跟随「文字大小」。
	opts.Theme.FontSize = scaledFont(baseSize)
	opts.CodeBlock = s.mdStaticOpts.CodeBlock
	// 思考正文同样支持本地图片与点击看大图。
	installImagePreviewHooks(opts, s)
	installLocalFileLinkHook(opts, s)
	// 思考正文里的表格同样可以展开到浮窗里看完整内容。
	installTableExpandHook(opts, s)
	opts.Reveal = s.mdOpts.Reveal
	return opts
}

// renderReasoningMessage 渲染单行可折叠的思考行（按 2.3 节）：默认折叠，流式时展开并显示「思考中…」，
// 展开图标带 90 度旋转动画，正文按助手正文的 Markdown 方式渲染，字号小、颜色为次要文本色。
// 关闭「自动折叠思考内容」开关后，非流式的思考行默认展开，由用户点击自行收起。
func renderReasoningMessage(c *ui.Context, s *chatState, m Message, liveID string, isLastAssistant bool, hasUsage bool, turnUsage TokenUsage, turnDur time.Duration) ui.Element {
	s.mu.Lock()
	userOpen := s.expandedReasoning[m.ID]
	s.mu.Unlock()

	open := userOpen
	isLive := liveID != "" && m.ID == liveID
	autoCollapse := isAutoCollapseReasoningEnabled(s)
	// 关闭自动折叠时默认展开，用户点击后翻转。
	if !autoCollapse && !isLive {
		open = !userOpen
	}

	if isLive {
		reasoningWasLiveMu.Lock()
		reasoningWasLive[m.ID] = true
		reasoningWasLiveMu.Unlock()
		open = true
	} else {
		// 刚结束流式输出的那一帧：用户正在往上翻时先保持展开，别把视线跳走。
		atEnd := s != nil && s.chatListState.AtEnd()
		atBottom := !shouldShowJumpToLatest(atEnd, -1)
		reasoningWasLiveMu.Lock()
		hold, forget := reasoningHoldOpen(reasoningWasLive[m.ID], autoCollapse, atBottom)
		if forget {
			delete(reasoningWasLive, m.ID)
		}
		reasoningWasLiveMu.Unlock()
		if hold {
			open = true
		}
	}

	// 标题行：显示「思考过程 · 要旨」，要旨取正文第一行（流式中取最后一行，说明当前在想什么），
	// 正文为空时只显示标题本身。所有会话都带上要旨，不再只限总管会话。
	headerText := reasoningHeaderTitle(m.Text, isLive)
	headerTooltip := headerText
	if full := reasoningHeadlineFull(m.Text, isLive); full != "" {
		headerTooltip = reasoningHeaderLabel(isLive) + " · " + full
	}

	// 展开/收起箭头：同一个右箭头按顺时针 90 度插值旋转成下箭头
	chevronTarget := float32(0)
	if open {
		chevronTarget = 90
	}

	thinkLabel := "展开思考过程"
	if open {
		thinkLabel = "收起思考过程"
	}

	return ui.Column(c.Key("msg-reasoning-" + m.ID)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Gap(Space2).Children(func() {
		// 单行指示条：淡色、可点击展开
		header := ui.Row(c.Key("think-header-" + m.ID)).
			Gap(Space2).
			AlignItems(ui.Center).
			Label(thinkLabel).
			Tooltip(headerTooltip).
			OnClick(func() {
				if isLive {
					return
				}
				s.toggleReasoning(m.ID)
			})
		if !isLive {
			header.Cursor(ui.CursorPointer)
		}
		chevronRotation := header.AnimateTo("think-chevron", chevronTarget, ui.Animation{Duration: animateDuration(c, motionUnfold), Ease: motionEaseStandard})
		header.Children(func() {
			ui.Icon(c, IconBrain()).Size(12, 12).TextColor(ThemeSecondaryText(c))
			label := ui.Text(c, headerText).FontSize(scaledFont(TypeFootnote))
			// 摘要只占一行，超出宽度尾端省略：不抢正文的垂直空间。
			label.MaxLines(1).Ellipsis("…").Shrink(1).MinWidth(0)
			if isLive && !reduceMotion(c) {
				// 流式中「思考中…」呼吸：透明度在 0.45 与 1 之间往返循环。
				// 按 175ms 一档低频重建，而不是用无限循环关键帧逐显示刷新驱动整窗重建。
				pulse := thinkPulseAlpha(c.Now())
				c.After(175 * time.Millisecond)
				label.TextColor(ThemeSecondaryText(c).Alpha(float32(pulse)))
			} else {
				label.TextColor(ThemeSecondaryText(c))
			}
			ui.Icon(c, IconChevronRight()).Size(11, 11).TextColor(ThemeSecondaryText(c)).Rotate(chevronRotation)
		})

		// 展开后正文左侧缩进 24pt，与助手正文一样走 Markdown 渲染（字号 12、次要文本色）；
		// 展开/收起统一走 motionUnfoldBody：高度从 0 长到渲染后的完整高度，收起时反向收拢。
		motionUnfoldBody(c, "think-body-"+m.ID, open, func() {
			ui.Column(c.Key("think-body-"+m.ID)).Selectable().FillWidth().Padding(0, 0, 0, Space7).Children(func() {
				if isLive {
					renderLiveMarkdown(c, s, m, reasoningMarkdownOptions(c, s))
				} else {
					markdown.View(c, m.Text, reasoningMarkdownOptions(c, s))
				}
			})
		})

		// 回合完成块：本回合的 token 统计挂在独立的完成块上
		if isLastAssistant && m.ID != liveID && hasUsage {
			renderTurnFinishedBlock(c, s, m, turnUsage, turnDur)
		}
	})
}
