// 用户消息及附件的渲染。
package mcodeui

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// userCollapseLimits 返回当前会话的折叠阈值（行数、字数）。
func userCollapseLimits(s *chatState) (maxLines, maxRunes int) {
	if s != nil && s.embeddedBee {
		return userCollapseEmbeddedLines, userCollapseEmbeddedRunes
	}
	return userCollapseLines, userCollapseRunes
}

// shouldCollapseUserText 判断用户消息正文是否应默认折叠（超过 maxLines 行或 maxRunes 个字符）。
func shouldCollapseUserText(text string, maxLines, maxRunes int) bool {
	if strings.Count(text, "\n")+1 > maxLines {
		return true
	}
	return utf8.RuneCountInString(text) > maxRunes
}

// userPromptPreview 生成长提问折叠时的预览文本，按普通会话的默认阈值截断。
// 保留这个签名给本文件之外的调用点（如 timeline_mention_test.go）使用。
func userPromptPreview(text string) string {
	return userPromptPreviewWithLimits(text, userCollapseLines, userCollapseRunes)
}

// userPromptPreviewWithLimits 取前 maxLines 行、且不超过 maxRunes 个字符作为预览。
// 不再补省略号（折叠提示交给气泡里的展开胶囊）；按字数截断时尽量退到最后一个空白处，
// 避免断在半个词中间，退不到就硬切。
func userPromptPreviewWithLimits(text string, maxLines, maxRunes int) string {
	lines := strings.Split(text, "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	preview := strings.Join(lines, "\n")
	runes := []rune(preview)
	if len(runes) <= maxRunes {
		return preview
	}
	cut := runes[:maxRunes]
	// 最多往回退 40 个字符找空白，再多就会把预览缩得太短。
	for i := len(cut) - 1; i > 0 && len(cut)-i <= 40; i-- {
		if unicode.IsSpace(cut[i]) {
			cut = cut[:i]
			break
		}
	}
	return strings.TrimRightFunc(string(cut), unicode.IsSpace)
}

// userHiddenLineCount 返回折叠后被藏起来的行数：原文行数减预览行数，最小为 0。
// 为 0 表示只因字数折叠，预览行数没变。
func userHiddenLineCount(text, preview string) int {
	hidden := strings.Count(text, "\n") - strings.Count(preview, "\n")
	if hidden < 0 {
		return 0
	}
	return hidden
}

// userBubbleColor 保证气泡和渐隐终点使用相同底色。
func userBubbleColor(c *ui.Context) ui.Color {
	return ThemeSurfaceCard(c)
}

func isUserMessageExpanded(msgID string) bool {
	if msgID == "" {
		return false
	}
	userMsgExpandedMu.Lock()
	defer userMsgExpandedMu.Unlock()
	return userMsgExpanded[msgID]
}

func toggleUserMessageExpanded(s *chatState, msgID string) {
	if msgID == "" {
		return
	}
	userMsgExpandedMu.Lock()
	userMsgExpanded[msgID] = !userMsgExpanded[msgID]
	userMsgExpandedMu.Unlock()
	if s != nil {
		s.holdChatPlace()
		s.invalidate()
	}
}

// isImageMedia 判断媒体是否为图片。
func isImageMedia(media MessageMedia) bool {
	if isAllowedImageMime(media.MimeType) || strings.HasPrefix(strings.ToLower(media.MimeType), "image/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(media.Name))
	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".webp" {
		return true
	}
	if media.Width > 0 && media.Height > 0 {
		return true
	}
	return false
}

// renderUserMessageAttachments 在用户气泡顶部渲染附件条：图片媒体加载好缩略图后显示可点击
// 放大的预览图，加载中显示转圈占位卡，加载失败显示可点击重试的失败占位卡；
// PDF、docx、zip 等非图片媒体只显示文件胶囊，不做内容预览。
func renderUserMessageAttachments(c *ui.Context, s *chatState, m Message) {
	if len(m.Media) == 0 {
		return
	}

	threadID := selectedThreadIDSnapshot(s)
	services := c.Services()

	ui.Row(c.Key("msg-user-attachments-"+m.ID)).
		Gap(Space2).
		Padding(Space0, 0, Space1, 0).
		Wrap().
		AlignItems(ui.Center).
		Children(func() {
			for i, med := range m.Media {
				name := med.Name
				if !isImageMedia(med) {
					// 非图片附件不会解码或加载缩略图，只在图片之后补一个文件胶囊。
					renderUserMessageFileCapsule(c, m, i, med)
					continue
				}
				if name == "" {
					name = "image"
				}
				thumb := cachedMessageMedia(c, s, threadID, m.ID, med.ID, "thumb")
				if thumb != nil {
					// 缩略图最大 160×160，按原始比例等比缩小。
					img := ui.Image(c.Key(fmt.Sprintf("msg-user-attach-%s-%d", m.ID, i)), thumb).
						MaxWidth(160).
						MaxHeight(160).
						Radius(RadiusMD).
						Border(1, ThemeBorder(c)).
						Label(name).
						Tooltip(name)
					w, h := imageSourceSize(thumb)
					if w <= 0 || h <= 0 {
						// 位图尺寸未知时退回消息自带的宽高比。
						w, h = float32(med.Width), float32(med.Height)
					}
					if w > 0 && h > 0 {
						img.AspectRatio(w / h)
					}
					// 点击看大图：先用已加载的缩略图顶上，原图加载完成后由浮层替换。
					thumbSrc := thumb
					img.Cursor(ui.CursorPointer).OnClick(func() {
						s.mu.Lock()
						s.imagePreview = &imagePreviewState{
							src:  thumbSrc,
							alt:  name,
							full: &messageMediaRef{threadID: threadID, messageID: m.ID, mediaID: med.ID},
						}
						s.mu.Unlock()
						prefetchMessageMedia(services, s, threadID, m.ID, med.ID, "full")
						s.invalidate()
					})
					continue
				}
				if _, ok := s.client.(messageMediaReader); !ok || med.ID == "" || threadID == "" {
					// 客户端不支持读取消息媒体或缺少 ID，缩略图永远不会就绪，保持回形针胶囊。
					renderUserMessageFileCapsule(c, m, i, med)
					continue
				}
				// 缩略图未就绪：加载中与加载失败各用一张可区分的占位卡。
				renderUserMessageImagePlaceholder(c, s, m, i, med, threadID, name)
			}
		})
}

// userAttachmentPlaceholderSize 计算图片附件占位卡（加载中/加载失败）的尺寸。
// 与成功时的缩略图同一规则：最长边 160，按消息自带的宽高比等比缩小，宽高未知时 160×160；
// 两侧再夹一个最小边长，避免极端比例把「图片加载失败」「点击重试」这两行字压没。
func userAttachmentPlaceholderSize(med MessageMedia) (float32, float32) {
	const maxSide float32 = 160
	const minSide float32 = 48
	w, h := float32(med.Width), float32(med.Height)
	if w <= 0 || h <= 0 {
		return maxSide, maxSide
	}
	if w >= h {
		w, h = maxSide, maxSide*h/w
	} else {
		w, h = maxSide*w/h, maxSide
	}
	if w < minSide {
		w = minSide
	}
	if h < minSide {
		h = minSide
	}
	return w, h
}

// renderUserMessageImagePlaceholder 在缩略图未就绪时渲染与缩略图同尺寸的占位卡：
// 加载中显示转圈；失败时用危险色描边，中间一个告警图标加「图片加载失败」「点击重试」，
// 整张卡可点击，点击后清掉失败缓存让下一帧重新加载；悬停提示给出文件名与错误文本。
func renderUserMessageImagePlaceholder(c *ui.Context, s *chatState, m Message, idx int, med MessageMedia, threadID, name string) {
	key := fmt.Sprintf("msg-user-attach-%s-%d", m.ID, idx)
	w, h := userAttachmentPlaceholderSize(med)
	state, errText := messageMediaStatus(threadID, m.ID, med.ID, "thumb")

	card := ui.Row(c.Key(key)).
		Gap(Space1).
		Width(w).
		Height(h).
		Radius(RadiusMD).
		Justify(ui.Center).
		AlignItems(ui.Center)

	if state == messageMediaFailed {
		tip := name
		if errText != "" {
			tip = name + "\n" + errText
		}
		services := c.Services()
		card.
			Background(ThemeDanger(c).Alpha(0.06)).
			Border(1, ThemeDanger(c)).
			Label("重试加载图片").
			Tooltip(tip).
			Cursor(ui.CursorPointer).
			OnClick(func() {
				retryMessageMedia(services, threadID, m.ID, med.ID, "thumb")
				s.invalidate()
			})
		card.Children(func() {
			ui.Column(c.Key(key + "-failed")).
				AlignItems(ui.Center).
				Gap(Space0).
				Children(func() {
					ui.Icon(c, IconAlertTriangle()).Size(18, 18).TextColor(ThemeDanger(c))
					ui.Text(c, "图片加载失败").
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeDanger(c))
					ui.Text(c, "点击重试").
						FontSize(scaledFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
				})
		})
		return
	}

	card.
		Background(ThemeHover(c)).
		Border(1, ThemeBorder(c)).
		Label(name).
		Tooltip(name).
		Children(func() {
			LoadingIndicator(c, nil, "", 18, ThemeSecondaryText(c))
		})
}

// renderUserMessageFileCapsule 在用户气泡顶部为非图片附件渲染文件胶囊：
// 只显示文件图标与文件名（过长时中间省略），完整文件名放 hover 提示，
// 不解码也不做文本预览；消息附件没有本地路径，因此不加点击跳转。
func renderUserMessageFileCapsule(c *ui.Context, m Message, idx int, med MessageMedia) {
	name := med.Name
	if name == "" {
		name = "附件"
	}
	ui.Row(c.Key(fmt.Sprintf("msg-user-attach-%s-%d", m.ID, idx))).
		Gap(Space2).
		Padding(Space0, Space3, Space0, Space3).
		Radius(RadiusLG).
		Background(ThemeHover(c)).
		Border(1, ThemeBorder(c)).
		AlignItems(ui.Center).
		Tooltip(name).
		Children(func() {
			ui.Icon(c, IconFile()).Size(11, 11).TextColor(ThemeSecondaryText(c))
			ui.Text(c, truncateAttachmentName(name, 18)).
				FontSize(scaledFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c))
		})
}

// shouldAnimateMessageEnter 判断消息是否播放入场动画：只有第一次出现、且时间戳距今不到
// messageEnterWindow 的完成态消息才播；流式输出中的助手消息等完成态再补播一次。
func shouldAnimateMessageEnter(seen bool, msgTime, now time.Time, streaming bool) bool {
	if seen || streaming || msgTime.IsZero() {
		return false
	}
	return now.Sub(msgTime) < messageEnterWindow
}

// beginMessageEnter 判定这条消息本帧是否播入场动画，并把判定结果记进登记表，
// 保证同一条消息只播一次：流式中的助手消息只记「流式中出现过」，完成态再补播一次；
// 登记表超过上限就整表清空重建（清空后旧消息早已超出时间窗，不会重播）。
func (s *chatState) beginMessageEnter(m Message, streaming bool, now time.Time) bool {
	if s == nil || m.ID == "" {
		return false
	}
	msgTime, _ := messageTime(m)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, seen := s.seenMsgIDs[m.ID]; seen {
		return false
	}
	if streaming {
		if len(s.streamedMsgIDs) >= messageEnterCap {
			s.streamedMsgIDs = make(map[string]struct{})
		}
		s.streamedMsgIDs[m.ID] = struct{}{}
		return false
	}
	_, streamed := s.streamedMsgIDs[m.ID]
	delete(s.streamedMsgIDs, m.ID)
	animate := streamed || shouldAnimateMessageEnter(false, msgTime, now, false)
	if len(s.seenMsgIDs) >= messageEnterCap {
		s.seenMsgIDs = make(map[string]struct{})
	}
	s.seenMsgIDs[m.ID] = struct{}{}
	return animate
}

// animateMessageEnter 给消息行挂入场动画：Y 方向从 messageEnterShift 上落到 0、透明度从 0 淡入到 1。
func animateMessageEnter(c *ui.Context, el ui.Element, msgID string, play bool) ui.Element {
	if !play {
		return el
	}
	k := motionEnter(c, el, "msg-enter-"+msgID, 0)
	return el.Translate(0, (1-k)*messageEnterShift).Opacity(k)
}

// renderUserMessage 渲染靠右对齐的用户气泡。
// 超过折叠阈值（普通会话 8 行 / 600 字，子代理嵌入会话 5 行 / 400 字）时只显示预览：
// 正文底部渐隐，气泡内右下角给「展开 · 还有 N 行」胶囊；展开后同一位置改为「收起」，
// 操作栏里也补一个「收起」。若带图片附件在顶部展示缩略图条。
// 就地编辑中时原位改为多行输入框（见 timeline_menu.go 的 inlineEditState）。
func renderUserMessage(c *ui.Context, s *chatState, m Message) ui.Element {
	if st, ok := inlineEditOf(s); ok && st.msgID == m.ID {
		return renderUserMessageInlineEdit(c, s, m, st)
	}
	enter := s.beginMessageEnter(m, false, c.Now())

	maxLines, maxRunes := userCollapseLimits(s)
	collapsible := shouldCollapseUserText(m.Text, maxLines, maxRunes)
	expanded := false
	if collapsible {
		expanded = isUserMessageExpanded(m.ID)
	}

	displayText := m.Text
	hiddenLines := 0
	if collapsible && !expanded {
		displayText = userPromptPreviewWithLimits(m.Text, maxLines, maxRunes)
		hiddenLines = userHiddenLineCount(m.Text, displayText)
	}

	row := ui.Row(c.Key("msg-user-" + m.ID)).FillWidth().Justify(ui.End).ContextMenu(messageContextMenu(c, s, m))
	// 整行 hover：指针停在这个消息行上（气泡、附件或操作栏）都算
	rowHovered := row.Hovered()

	row.Children(func() {
		ui.Spacer(c)
		ui.Column(c.Key("msg-user-box-" + m.ID)).AlignItems(ui.End).Gap(Space1).MaxWidth(userBubbleMaxWidth(s)).Children(func() {
			// 顶部图片附件缩略图条
			renderUserMessageAttachments(c, s, m)

			// 保留用户气泡自身的对齐与间距。
			bubble := ui.Column(c.Key("msg-user-bubble-"+m.ID)).
				AlignItems(ui.End).
				Gap(Space1).
				MaxWidth(userBubbleMaxWidth(s)).
				Padding(Space4, Space5).
				Radius(RadiusLG).
				Background(userBubbleColor(c))
			bubble.Children(func() {
				// 正文包一层，方便把渐隐层错定在正文底部（而不是气泡底部，那里是胶囊）。
				// MaxWidthPercent(100)：气泡是 AlignItems(End) 的列，子元素不拉伸，只按固有宽度排版，
				// 固有宽度又是 min-content（最长不可断词）算的；含超宽表格行、长行内代码的正文
				// min-content 远大于气泡宽度，展开后会按那个宽度排版并因右对齐而向左溢出气泡。
				// 用百分比上限把正文宽度压回气泡内容宽度，随气泡/窗口变窄一起缩小。
				body := ui.Column(c.Key("msg-user-body-" + m.ID)).AlignItems(ui.End).MaxWidthPercent(100)
				body.Children(func() {
					// 正文里命中当前会话列表的 @会话引用渲染为可点击胶囊，其余文本原样渲染
					if segments := splitThreadMentions(displayText, mentionThreadsSnapshot(s)); hasThreadMention(segments) {
						renderUserMentionParagraph(c, s, m, segments)
					} else {
						ui.Text(c, displayText).
							MaxWidthPercent(100).
							Selectable().
							ContextMenu(messageContextMenu(c, s, m)).
							FontSize(scaledFont(TypeBody)).
							LineHeight(1.45).
							TextColor(ThemePrimaryText(c))
					}

					// 折叠态在正文底部叠一层渐隐：左右负偏移越过气泡内边距，和气泡内框对齐；
					// PassThrough 让指针落到底下的文字上，不影响选中。
					if collapsible && !expanded {
						fadeColor := userBubbleColor(c)
						ui.Box(c.Key("msg-user-fade-" + m.ID)).
							Absolute().
							Left(-Space5).
							Right(-Space5).
							Bottom(0).
							Height(userFadeHeight).
							LinearGradient(ui.LinearGradient{
								Angle: 180,
								From:  fadeColor.Alpha(0),
								To:    fadeColor,
							}).
							PassThrough()
					}
				})

				// 气泡内右下角的展开/收起胶囊
				if collapsible {
					renderUserMessageToggle(c, s, m, expanded, hiddenLines)
				}
			})

			// 气泡下方的操作栏：复制原文与就地修改；展开时长消息再多一个「收起」，
			// 这样滚到长消息底部也能收起；最后是「⋯」更多菜单，条目与右键菜单完全一致。
			// 与助手操作栏一样默认隐藏、hover 这一行才显示，
			// 隐藏时只把不透明度置零以保留占位。
			copyKey := "copy-btn-" + m.ID
			bar := ui.Row(c.Key("msg-user-actions-" + m.ID)).AlignItems(ui.Center).Gap(Space3)
			if !messageActionBarVisible(rowHovered, copyFeedbackActive(copyKey, time.Now())) {
				bar.Opacity(0)
			}
			bar.Children(func() {
				renderCopyMessageButton(c, s, m, copyKey)
				renderEditMessageButton(c, s, m)
				if collapsible && expanded {
					renderUserMessageCollapseButton(c, s, m)
				}
				renderMessageMoreButton(c, s, m)
			})
		})
	})

	return animateMessageEnter(c, row, m.ID, enter)
}

// renderUserMessageToggle 渲染用户气泡内右下角的展开/收起胶囊：
// 折叠态显示「展开 · 还有 N 行」（只因字数折叠时显示「展开全文」）加向下 chevron，展开态显示「收起」加向上 chevron。
func renderUserMessageToggle(c *ui.Context, s *chatState, m Message, expanded bool, hiddenLines int) {
	label := "展开全文"
	var chevron float32 // 0 表示 chevron 向下，180 表示向上
	if expanded {
		label = "收起"
		chevron = 180
	} else if hiddenLines > 0 {
		label = fmt.Sprintf("展开 · 还有 %d 行", hiddenLines)
	}

	btn := ui.ButtonBase(c.Key("msg-user-toggle-"+m.ID)).
		Label(label).
		Tooltip(label).
		Padding(Space0, Space3).
		Radius(RadiusMD).
		Background(ThemePrimaryText(c).Alpha(0.06)).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			toggleUserMessageExpanded(s, m.ID)
		})
	if btn.Hovered() {
		btn.Background(ThemePrimaryText(c).Alpha(0.12))
	}
	btn.Children(func() {
		ui.Row(c.Key("msg-user-toggle-row-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c))
			ui.Icon(c, IconChevronDown()).Size(10, 10).TextColor(ThemeSecondaryText(c)).Rotate(chevron)
		})
	})
}

// renderUserMessageCollapseButton 渲染用户消息操作栏里的「收起」按钮，样式与 renderEditMessageButton 一致。
func renderUserMessageCollapseButton(c *ui.Context, s *chatState, m Message) {
	ui.ButtonBase(c.Key("msg-user-collapse-"+m.ID)).
		Label("收起").
		Tooltip("收起这条提问").
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		OnClick(func() {
			toggleUserMessageExpanded(s, m.ID)
		}).
		Children(func() {
			ui.Row(c.Key("msg-user-collapse-row-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconChevronDown()).Size(11, 11).TextColor(ThemeSecondaryText(c)).Rotate(180)
				ui.Text(c, "收起").FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
			})
		})
}

// renderUserMessageInlineEdit 渲染用户消息的就地编辑框：预填原文的多行输入框，下方是取消与发送按钮。
// Esc 取消，⌘Enter 发送；发送后从该消息之前最近的助手最终答复分支出新会话并直接发送编辑后的文本。
func renderUserMessageInlineEdit(c *ui.Context, s *chatState, m Message, st *inlineEditState) ui.Element {
	submit := func() {
		s.submitInlineEdit(st.msgID, st.draft)
	}

	return ui.Row(c.Key("msg-user-edit-" + m.ID)).FillWidth().Justify(ui.End).Children(func() {
		ui.Spacer(c)
		ui.Column(c.Key("msg-user-edit-box-" + m.ID)).AlignItems(ui.End).Gap(Space2).FillWidth().MaxWidth(userBubbleMaxWidth(s)).Children(func() {
			in := ui.TextArea(c.Key("msg-user-edit-input-"+m.ID), &st.draft).
				Lines(2, 8).
				FontSize(scaledFont(TypeBody)).
				LineHeight(1.45).
				TextColor(ThemePrimaryText(c)).
				Background(ThemeSurface(c)).
				Border(1, ThemeBorder(c)).
				Radius(RadiusLG).
				Padding(Space3, Space4).
				FillWidth().
				AutoFocus()
			in.OnTextKey(func(k ui.TextKey) bool {
				st.draft = k.Text()
				if k.Key == ui.KeyEscape && k.Mods == 0 {
					cancelInlineEdit(s)
					return true
				}
				if k.Key == ui.KeyEnter && (k.Mods&ui.Cmd != 0 || k.Mods&ui.Super != 0) {
					submit()
					return true
				}
				return false
			})

			ui.Row(c.Key("msg-user-edit-actions-" + m.ID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Esc 取消 · "+shortcutDisplay("⌘Enter")+" 发送").FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
				ui.Spacer(c)
				Button(c, ButtonOpts{
					Key:     "msg-user-edit-cancel-" + m.ID,
					Text:    "取消",
					Label:   "取消编辑",
					Variant: ButtonSecondary,
					Size:    ControlMD,
					OnClick: func() {
						cancelInlineEdit(s)
					},
				})
				Button(c, ButtonOpts{
					Key:     "msg-user-edit-send-" + m.ID,
					Text:    "发送",
					Label:   "发送编辑",
					Variant: ButtonPrimary,
					Size:    ControlMD,
					OnClick: func() {
						submit()
					},
				})
			})
		})
	})
}
