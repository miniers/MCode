package mcodeui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/config"
)

// errorSummaryMaxRunes 是错误行首行摘要的最大字数，超出后截断并加省略号。
const errorSummaryMaxRunes = 120

// errorDetailMaxHeight 是展开后错误详情区的最大高度，超出部分在区内滚动。
const errorDetailMaxHeight float32 = 180

// errorSummaryLine 返回错误全文的首行摘要：取第一行、把空白压成单个空格，
// 超过 errorSummaryMaxRunes 个字时截断并加省略号。
func errorSummaryLine(text string) string {
	line := strings.TrimSpace(text)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	line = strings.Join(strings.Fields(line), " ")
	runes := []rune(line)
	if len(runes) > errorSummaryMaxRunes {
		return string(runes[:errorSummaryMaxRunes]) + "…"
	}
	return line
}

// errorNeedsDetailToggle 判断错误全文是否值得给一个「详情」开关：超过一行，或超过首行摘要的字数上限。
func errorNeedsDetailToggle(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, "\n") {
		return true
	}
	return len([]rune(trimmed)) > errorSummaryMaxRunes
}

// errorIsLastMessage 判断这条错误是不是当前会话的最后一条消息。
func errorIsLastMessage(messages []Message, m Message) bool {
	if len(messages) == 0 || m.ID == "" {
		return false
	}
	return messages[len(messages)-1].ID == m.ID
}

// errorRetryVisible 判断错误行是否显示「继续」：只有这条错误仍是会话最后一条消息、
// 且会话没有在运行时才显示，避免重复发送或打断正在进行的回合。
func errorRetryVisible(isLastMessage, threadRunning bool) bool {
	return isLastMessage && !threadRunning
}

// errorRetryPrompt 返回这条错误之前最近一条用户消息的正文，供只需要正文的调用方使用；
// 附件部分由 errorRetryTargetFor 一并返回，找不到消息时返回空。
func errorRetryPrompt(messages []Message, errorID string) string {
	return errorRetryTargetFor(messages, errorID).text
}

// errorDetailKey 返回错误详情展开状态在展开状态表里的键（复用 expandedTool，不新增状态字段）。
func errorDetailKey(messageID string) string {
	return "err-full:" + messageID
}

// isErrorDetailExpanded 读取这条错误是否已展开详情。
func isErrorDetailExpanded(s *chatState, messageID string) bool {
	if messageID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expandedTool[errorDetailKey(messageID)]
}

// toggleErrorDetail 切换错误详情展开状态。行内容每帧重建，只需请求重绘，
// 不必让时间轴 item 缓存失效（错的是展开后的文本，不是行结构）。
func toggleErrorDetail(s *chatState, m Message) {
	if m.ID == "" {
		return
	}
	s.mu.Lock()
	key := errorDetailKey(m.ID)
	s.expandedTool[key] = !s.expandedTool[key]
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// renderErrorMessage 渲染错误卡片：左边错误图标，第一行是错误的首行摘要与操作按钮，
// 需要时提供「详情」展开完整文本、「复制」与「继续」（从中断处继续，不重发上一条消息）。
// 消息带着后端上报的失败类别，或原文命中限流/额度耗尽/登录失效关键词时，改用横幅行
// （见 renderProviderNoticeBanner）。
func renderErrorMessage(c *ui.Context, s *chatState, m Message) ui.Element {
	if kind, resetHint := classifyMessageNotice(m); kind != providerNoticeNone {
		return renderProviderNoticeBanner(c, s, m, kind, resetHint)
	}

	expanded := isErrorDetailExpanded(s, m.ID)
	summary := errorSummaryLine(m.Text)
	if summary == "" {
		summary = "发生未知错误"
	}

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)
	codeSz := codeFontSize()

	// 「继续」只在「这条错误仍是最新一条消息且线程没在跑」时出现：点击发送的是
	// 一条从中断处继续的提示，不再重发上一条用户消息。
	var messages []Message
	s.mu.Lock()
	messages = append([]Message(nil), s.messages...)
	threadID := s.selectedThreadID
	s.mu.Unlock()
	isLast := errorIsLastMessage(messages, m)
	showRetry := errorRetryVisible(isLast, s.isThreadRunning(threadID))

	return ui.Row(c.Key("msg-err-" + m.ID)).MaxWidth(timelineContentMaxWidth(s)).Children(func() {
		ui.Column(c.Key("err-card-"+m.ID)).
			FillWidth().
			Padding(Space3, Space4).
			Radius(RadiusLG).
			Gap(Space2).
			Background(ThemeDanger(c).Alpha(0.07)).
			Border(Hairline, ThemeDanger(c).Alpha(0.26)).
			Children(func() {
				ui.Row(c.Key("err-head-" + m.ID)).
					FillWidth().
					AlignItems(ui.Center).
					Gap(Space3).
					Children(func() {
						ui.Icon(c, IconOctagonAlert()).Size(IconSM, IconSM).TextColor(ThemeDanger(c))
						ui.Text(c, summary).
							FontSize(scaledFont(TypeFootnote)).
							Bold().
							TextColor(ThemeDanger(c)).
							SingleLine().
							Grow(1)
						if errorNeedsDetailToggle(m.Text) {
							label := "详情"
							if expanded {
								label = "收起"
							}
							renderErrorActionButton(c, "err-detail-btn-"+m.ID, label, "展开完整错误文本", IconChevronDown(), ThemeSecondaryText(c), func() {
								toggleErrorDetail(s, m)
							})
						}
						renderErrorCopyButton(c, s, m)
						if showRetry {
							label := "继续"
							tooltip := "从中断处继续执行，不重发上一条消息"
							if errorRetryInProgress(m.ID) {
								// 已经在发（自动重试刚到点或用户连点）：按钮只做进行中提示，beginErrorRetry 会挡住重复点击。
								label = "继续中…"
								tooltip = "正在发送继续提示"
							}
							renderErrorActionButton(c, "err-retry-btn-"+m.ID, label, tooltip, IconRefreshCw(), ThemeAccent(c), func() {
								retryErrorMessage(s, m)
							})
						}
					})
				if expanded {
					renderErrorDetail(c, m, fontFam, codeSz)
				}
			})
	})
}

// renderErrorDetail 渲染展开后的完整错误文本：等宽小字、限高滚动、可选中。
func renderErrorDetail(c *ui.Context, m Message, fontFam string, codeSz float32) {
	ui.Scroll(c.Key("err-detail-"+m.ID)).
		FillWidth().
		MaxHeight(errorDetailMaxHeight).
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeHover(c)).
		Border(Hairline, ThemeBorder(c)).
		Selectable().
		Children(func() {
			ui.Column(c.Key("err-detail-body-" + m.ID)).FillWidth().Children(func() {
				ui.Text(c, strings.TrimSpace(m.Text)).
					Font(fontFam).
					FontSize(codeSz).
					LineHeight(1.4).
					TextColor(ThemePrimaryText(c)).
					Selectable()
			})
		})
}

// renderErrorCopyButton 渲染错误卡片的「复制」按钮，复制后按钮内换成绿勾 1.4 秒，与其它复制按钮一致。
func renderErrorCopyButton(c *ui.Context, s *chatState, m Message) {
	key := "err-copy-" + m.ID
	copied := copyFeedbackActive(key, time.Now())
	text := "复制"
	if copied {
		text = "已复制"
	}
	CopyButton(c, CopyButtonProps{
		Key: key, Label: "复制", Tooltip: "复制完整错误信息", Text: text, Copied: copied,
		Value: func() string { return m.Text }, OnCopy: func() { markCopyFeedback(s, key) },
	})
}

// renderErrorActionButton 渲染错误卡片里的文字小按钮（详情、重试共用）。
func renderErrorActionButton(c *ui.Context, key, label, tooltip string, icon *ui.SVG, iconColor ui.Color, onClick func()) {
	btn := ui.ButtonBase(c.Key(key)).
		Label(label).
		Tooltip(tooltip).
		Padding(Space0, Space2).
		Radius(RadiusSM).
		Cursor(ui.CursorPointer).
		OnClick(onClick)
	if btn.Hovered() {
		btn.Background(ThemeSecondaryText(c).Alpha(0.12))
	}
	btn.Children(func() {
		ui.Icon(c, icon).Size(inlineIconSize, inlineIconSize).TextColor(iconColor)
		ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
	})
}
