package mcodeui

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"mcode/internal/config"
	"mcode/internal/git"
)

var (
	userMsgExpandedMu sync.Mutex
	userMsgExpanded   = make(map[string]bool)

	// 复制反馈：按按钮 key 记录复制时刻，1.4 秒内按钮图标显示为绿色对勾。
	copyFeedbackMu sync.Mutex
	copyFeedbackAt = make(map[string]time.Time)

	// reasoningWasLive 记录曾经处于 live 状态的思考消息 ID，用于在 live 结束帧保护正在翻阅的用户不被突然收起。
	reasoningWasLiveMu sync.Mutex
	reasoningWasLive   = make(map[string]bool)
)

// copyFeedbackDuration 是复制按钮显示绿色对勾的时长。
const copyFeedbackDuration = 2500 * time.Millisecond

const (
	turnGap  float32 = 15 // token-exempt: 轮次间距采用设计稿尺寸。
	blockGap float32 = 10 // token-exempt: 轮内块间距采用设计稿尺寸。
)

// streamingCursor 是流式回复末尾的光标符号。
const streamingCursor = "▍"

// stewardSilentMarker 是总管被唤醒、但没有需要用户知道的事时整轮回复的唯一正文标记：
// 这一轮在时间线里整条隐藏。标记只在界面侧定义一份，后端提示词里写字面量，不往 internal/config 加符号。
const stewardSilentMarker = "[[静默]]"

// stewardWakeKind 是总管被宿主唤醒时落库的消息 Kind（取值与 internal/remote 的
// threadMessageKindStewardWake 一致）。这类消息是系统输入，不是用户自己说的话，
// 时间线里不产生行，也不渲染成「宿主转来」卡片。
const stewardWakeKind = "steward_wake"

const stewardReplyKind = "steward_reply"
const stewardDecisionKind = "steward_decision"
const stewardDecisionAnswerKind = "steward_decision_answer"
const stewardResultKind = "steward_result"

// hostEventKind 是宿主转来事件的 Kind：任务会话里仍然渲染成卡片，但同样不算用户开口。
const hostEventKind = "host_event"

// splitSessionKind 是分割会话的消息 Kind（取值与 internal/remote 一致）：
// 这条系统消息就是分割线本身，时间线里渲染成一条细线，分割线以上的历史照常显示。
const splitSessionKind = "split_session"

// contextCompactedKind 是「上下文已压缩」边界消息的 Kind（取值与 internal/remote 一致）：
// provider 压缩过后端落一条独立系统消息，时间线里渲染成一条细线，与分割线并列，不进回合折叠。
const contextCompactedKind = "context_compacted"

// contextCompactedNotice 是压缩边界行没有任何正文时的兑底文案。
const contextCompactedNotice = "已压缩上下文"

// isHiddenTriggerKind 判断 user 角色消息的 Kind 是否属于系统输入（宿主事件、总管唤醒）：
// 它们都不是用户自己说的话，不能算用户开口。
func isHiddenTriggerKind(kind string) bool {
	return kind == hostEventKind || kind == stewardWakeKind
}

// isHiddenTimelineMessage 判断消息是否不产生时间线行：总管唤醒这类系统输入，
// 以及整轮正文只有静默标记的助手回复。判断必须在构建 items 这一层做，
// 不能靠渲染返回空元素——每行外面还有一层带内边距的 Row，空元素会留下空白。
func isHiddenTimelineMessage(m Message) bool {
	if m.Kind == stewardDecisionAnswerKind {
		return true
	}
	if m.Role == "user" {
		return m.Kind == stewardWakeKind
	}
	if m.Role != "assistant" {
		return false
	}
	text := strings.TrimSpace(m.Text)
	if text == stewardSilentMarker {
		return true
	}
	// 流式输出时正文可能只是标记的前缀（如 "[[静"）：先不显示，避免闪现半截标记。
	return !m.IsFinal && text != "" && strings.HasPrefix(stewardSilentMarker, text)
}

// thinkPulseAlpha 返回「思考中…」呼吸标识的透明度：1400ms 一个周期的三角波
// （0ms 为 0.45、700ms 为 1、1400ms 回到 0.45，与原关键帧一致），相位按 8 档量化、
// 每档约 175ms，配合调用方 c.After(175ms) 请求下一帧即可，无需逐帧关键帧动画。
func thinkPulseAlpha(now time.Time) float64 {
	const (
		period = int64(1400) // 一个呼吸周期的毫秒数
		steps  = int64(8)    // 相位量化档数
		stepMs = period / steps
	)
	phase := now.UnixMilli() % period
	if phase < 0 {
		phase += period
	}
	// 四舍五入到最近一档，避免同一档内每次调用都取到不同值
	step := (phase + stepMs/2) / stepMs
	half := steps / 2
	if step > half { // 后半程透明度回落
		step = steps - step
	}
	return 0.45 + 0.55*float64(step)/float64(half)
}

// copyFeedbackActive 返回 key 对应的复制反馈是否仍在显示窗口（1.4 秒）内，供按钮决定显示对勾还是复制图标。
func copyFeedbackActive(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	copyFeedbackMu.Lock()
	at, ok := copyFeedbackAt[key]
	copyFeedbackMu.Unlock()
	return ok && now.Sub(at) < copyFeedbackDuration
}

// markCopyFeedback 记录一次复制成功，并触发重绘；反馈窗口结束后再触发一次重绘让图标恢复，
// 顺手清掉过期的记录（仿 composer_attach.go 的 setAttachmentNotice）。
func markCopyFeedback(s *chatState, key string) {
	if key == "" {
		return
	}
	copyFeedbackMu.Lock()
	copyFeedbackAt[key] = time.Now()
	copyFeedbackMu.Unlock()
	if s == nil {
		return
	}
	s.invalidate()
	time.AfterFunc(copyFeedbackDuration, func() {
		copyFeedbackMu.Lock()
		if at, ok := copyFeedbackAt[key]; ok && time.Now().Sub(at) >= copyFeedbackDuration {
			delete(copyFeedbackAt, key)
		}
		copyFeedbackMu.Unlock()
		s.invalidate()
	})
}

// 用户长消息的折叠阈值：普通会话 8 行 / 600 字；子代理嵌入会话 5 行 / 400 字，
// 因为那边的首条消息是派发提示词（机器写给自己看的），只需要露出开头几行。
const (
	userCollapseLines         = 8
	userCollapseRunes         = 600
	userCollapseEmbeddedLines = 5
	userCollapseEmbeddedRunes = 400
)

// userFadeHeight 是折叠态正文底部渐隐层的高度（pt）。
const userFadeHeight float32 = 28

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

// 消息入场动画：新到达的消息向上落位并淡入，时长只取 motion.go 的 token。
const (
	// messageEnterWindow 是入场动画的时间窗：只有这段时间内新到达的消息才播，
	// 加载历史时出现的旧消息一律不播。
	messageEnterWindow = 1500 * time.Millisecond
	// messageEnterShift 是入场动画的起始下移距离（pt）。
	messageEnterShift float32 = 8
	// messageEnterCap 是入场登记表的上限，超过就整表清空重建，避免长会话里无限增长。
	messageEnterCap = 2000
)

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
				ui.Text(c, "Esc 取消 · ⌘Enter 发送").FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
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

type timelineItemKind int

const (
	timelineItemSingle timelineItemKind = iota
	timelineItemToolGroup
	timelineItemSwarmReport   // 汇报行：相邻汇报消息合并成一行，行内胶囊横向流式排列
	timelineItemSwarmCapsules // 子线程胶囊行：每个 Swarm 子线程一枚，内容每帧读当前状态
	timelineItemSwarmDispatch // 派发占位行：派单块正在写、或已写完但子代理还没出现时占位，不可点击
	timelineItemResolvedPlan
	timelineItemAnsweredQuestion // 已答复提问的历史态卡片：提问卡片消灭后在时间线里留一条问答记录
	timelineItemRunning
	timelineItemSessionUsage
	// timelineItemForkOrigin 是时间线最顶部的 fork 来源条：只在当前会话带来源字段时出现，
	// 内容每帧按会话列表现查源会话标题（见 renderForkOriginRow）。
	timelineItemForkOrigin
	timelineItemForwardedContext
	// timelineItemToolBeat 是展开后的工具组内的一个“节拍”：一条思考消息加上它后面紧跟的连续工具调用，
	// 渲染成一行思考叙事加一行可换行的动作胶囊（行级虚拟化单项）。
	timelineItemToolBeat
	// timelineItemTurnWork 是已结束回合的折叠项：把用户消息与最终答复之间的思考、工具调用与中途正文
	// 收进一行组头（用时 Xs），展开后才显示里面的条目。
	timelineItemTurnWork
	// timelineItemBackgroundWait 是时间线末尾的等待条：回合已结束但仍有后台任务在跑时出现，
	// 用一行扫光文字提示会话还在等它们，点击打开输入区胶囊上已有的后台任务浮层。
	timelineItemBackgroundWait
	// 总管会话里每个回合开头的日期时间分隔行。
	timelineItemTurnTime
	timelineItemStewardTask
	timelineItemStewardAmend
	timelineItemStewardTaskChips
)

// runningIndicatorItem 构造时间线末尾的“回复中”指示项：会话运行期间常驻展示。
func runningIndicatorItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemRunning, id: runningIndicatorKey(threadID)}
}

// runningIndicatorKey 返回“回复中”指示元素的稳定 Key：按会话区分，避免切换会话时复用错误节点。
func runningIndicatorKey(threadID string) string {
	if threadID == "" {
		return "running-indicator"
	}
	return "running-indicator-" + threadID
}

// backgroundWaitItem 构造时间线末尾的“等待后台任务”指示项：回合已结束但后台任务仍在跑时展示。
func backgroundWaitItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemBackgroundWait, id: backgroundWaitKey(threadID), threadID: threadID}
}

// backgroundWaitKey 返回等待后台任务条的稳定 Key：按会话区分，避免切换会话时复用错误节点。
func backgroundWaitKey(threadID string) string {
	if threadID == "" {
		return "background-wait"
	}
	return "background-wait-" + threadID
}

// sessionUsageItem 构造时间线底部的会话统计入口行，按会话区分稳定 Key。
func sessionUsageItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemSessionUsage, id: "session-usage-" + threadID, threadID: threadID}
}

// forkOriginItem 构造时间线最顶部的 fork 来源条：只带源会话 ID，标题在渲染时现查。
func forkOriginItem(threadID, sourceThreadID string) timelineItem {
	return timelineItem{
		kind:             timelineItemForkOrigin,
		id:               "fork-origin-" + threadID,
		threadID:         threadID,
		forkFromThreadID: sourceThreadID,
	}
}

func forwardedContextItem(threadID string) timelineItem {
	return timelineItem{kind: timelineItemForwardedContext, id: "forwarded-context-" + threadID, threadID: threadID}
}

// timelineItem 表示时间线中的一个渲染项（单条消息、连续工具合并组或组内的一个节拍）。
type timelineItem struct {
	kind         timelineItemKind
	stewardTask  KanbanTask
	stewardTasks []KanbanTask
	msg          Message
	tools        []Message // 组头项与组内各节拍共用的整组消息
	reportMsgs   []Message // 合并后的连续 Swarm 汇报消息（汇报行专有，首条与 msg 相同）
	beatTools    []Message // 节拍内由这条思考触发的连续工具调用（没有前置思考的节拍只带工具）
	id           string
	threadID     string // 该行所属会话 ID（会话统计行用它定位数据），其它行可为空
	turnIndex    int
	resolvedPlan resolvedPlan
	// answeredQuestion 是已答复提问的历史态（只有 timelineItemAnsweredQuestion 用它）。
	answeredQuestion answeredQuestion
	// 该项消息的完成时间，由消息 CreatedAt 解析得到，拿不到时为零值。
	finishedAt time.Time
	// 时间分隔行要显示的时刻，只有 timelineItemTurnTime 使用。
	stampAt time.Time
	// groupID 是所属工具组 ID（组头项与组内各节拍共用）。
	groupID string
	// groupExpanded 标记工具组处于展开状态：展开时组内各节拍紧随组头项生成。
	groupExpanded bool
	// workItems 是回合折叠项展开后要渲染的条目：思考、工具组、工具节拍与中途正文，按原顺序排列。
	workItems []timelineItem
	// workElapsed 是回合折叠项组头显示的耗时短文本（如 "3.2s"），拿不到时间戳时为空。
	workElapsed string
	// workAttention 标记被折叠的回合里有工具执行失败，组头据此显示警示图标。
	workAttention bool
	// workRunning 标记这是运行中回合的临时折叠头：只显示实时计时，回合结束后会被摘掉重折。
	workRunning bool
	// workStartedAt 是运行中回合的起始时刻（本回合用户消息的发送时间），拿不到时为零值。
	workStartedAt time.Time
	// workHeaderID 打在折叠项之后那条负责收尾的答复上：非空表示回合完成信息已由折叠头承载，
	// 这条消息不再单独画完成行，只保留改动文件卡片。
	workHeaderID string
	// msgLastIndex 是该项覆盖的最后一条消息下标（回合折叠项覆盖被收起的整段，组头项覆盖整组）；
	// 未设置（0）时按 itemLastMsgIndex 回退到 msgIndex。
	msgLastIndex int
	// isFirst、isLast 标记该节拍是否为组内首个、最后一个（节拍分界见 groupSegments）。
	isFirst bool
	isLast  bool
	// msgIndex 是该项对应消息在原始 messages 切片中的起始下标。
	msgIndex int
	// capsuleSpan 是子线程胶囊行的回合区间：渲染时按它把该回合的子线程分到这一行（其它行为零值）。
	capsuleSpan swarmCapsuleTurnSpan
	// forkFromThreadID 是 fork 来源条指向的源会话 ID（只有来源条用它）。
	forkFromThreadID string
}

// isReasoningMessage 判断消息是否为思考过程消息。
func isReasoningMessage(m Message) bool {
	return m.Kind == "reasoning" || m.Role == "reasoning"
}

// isActivityMessage 判断消息是否属于时间线里的“活动”行（思考过程与工具调用）；
// 同一回合内连续的活动行会合并成一个可折叠的工具组。
func isActivityMessage(m Message) bool {
	return m.Role == "tool" || isReasoningMessage(m)
}

// buildTimelineItems 将扁平的消息列表聚合成时间线项，同一回合内连续的思考行与工具调用聚合为一个组。
// expanded 可选传入当前工具组的展开状态：折叠时只生成组头项，展开时再追加组内各节拍
// （一条思考加它后面紧跟的连续工具调用合成一个节拍）。
// 静态构建没有“进行中”的活动组，因此各工具组一律默认折叠。
func buildTimelineItems(messages []Message, expanded ...map[string]bool) []timelineItem {
	var exp map[string]bool
	if len(expanded) > 0 {
		exp = expanded[0]
	}
	return buildTimelineItemsLive(messages, exp, "")
}

// liveToolGroupID 返回运行中会话末尾活动段对应的工具组 ID：仅当会话仍在运行、
// 末尾消息是思考或工具行、且该段足够成组（至少两条且含工具调用）时才返回，否则返回空串。
func liveToolGroupID(messages []Message, running bool) string {
	if !running || len(messages) == 0 {
		return ""
	}
	last := messages[len(messages)-1]
	if !isActivityMessage(last) {
		return ""
	}
	start := len(messages) - 1
	for start > 0 && isActivityMessage(messages[start-1]) && messages[start-1].TurnIndex == last.TurnIndex {
		start--
	}
	run := messages[start:]
	if len(run) < 2 {
		return ""
	}
	for _, m := range run {
		if m.Role == "tool" {
			return "toolgroup-" + run[0].ID
		}
	}
	return ""
}

// buildTimelineItemsLive 与 buildTimelineItems 相同，另外接收当前正在进行的活动组 ID。
// 运行中的活动组同样默认折叠：折叠态在组头下方显示最近几条工具行的只读预览，组头用实时耗时
// 显示进度，避免流式过程中把整组节拍铺满时间轴；用户显式点开或收起后仍以用户的选择为准。
// liveGroupID 保留在签名里供调用方标记进行中的组（渲染期按消息 ID 判定 running）。
// 默认折叠的固定入口：关闭「自动折叠思考内容」时由 rebuildTimelineIncremental 改调
// buildTimelineItemsOpts 并传入 groupsOpenByDefault=true。
func buildTimelineItemsLive(messages []Message, exp map[string]bool, liveGroupID string) []timelineItem {
	return buildTimelineItemsOpts(messages, exp, liveGroupID, false)
}

// buildTimelineItemsOpts 是 buildTimelineItemsLive 的完整实现。groupsOpenByDefault 是用户在未
// 显式设置开合时工具组的默认值：关闭自动折叠思考时传 true，让思考与工具组成的组保持展开。
func buildTimelineItemsOpts(messages []Message, exp map[string]bool, liveGroupID string, groupsOpenByDefault bool) []timelineItem {
	return buildTimelineItemsFiltered(collapseTodoMessages(messages), exp, liveGroupID, groupsOpenByDefault)
}

func buildTimelineItemsFiltered(messages []Message, exp map[string]bool, liveGroupID string, groupsOpenByDefault bool) []timelineItem {
	var items []timelineItem
	n := len(messages)
	for i := 0; i < n; {
		m := messages[i]
		if isHiddenTimelineMessage(m) {
			// 隐藏触发与历史静默消息在构建层直接跳过。
			i++
			continue
		}
		if isActivityMessage(m) {
			start := i
			turnIndex := m.TurnIndex
			for i < n && isActivityMessage(messages[i]) && messages[i].TurnIndex == turnIndex {
				i++
			}
			run := messages[start:i]
			hasTool := false
			for _, rm := range run {
				if rm.Role == "tool" {
					hasTool = true
					break
				}
			}
			if len(run) >= 2 && hasTool {
				groupID := "toolgroup-" + run[0].ID
				// 各组按调用方给的默认值开合（通常折叠；关闭自动折叠思考时默认展开）；
				// 用户显式设置过开合状态时仍以设置为准。
				groupExpanded := toolGroupOpen(exp, groupID, groupsOpenByDefault)
				// 组头项在折叠与展开状态都存在，展开时再追加组内各段行
				items = append(items, timelineItem{
					kind:          timelineItemToolGroup,
					tools:         run,
					id:            groupID,
					turnIndex:     run[len(run)-1].TurnIndex,
					finishedAt:    messageFinishedAt(run[len(run)-1]),
					groupID:       groupID,
					groupExpanded: groupExpanded,
					msgIndex:      start,
				})
				if groupExpanded {
					items = appendGroupSegmentItems(items, run, start, groupID)
				}
			} else {
				// 不足两条或全是思考行：逐条单独展示，保持原有单行渲染
				for j, msg := range run {
					items = append(items, timelineItem{
						kind:       timelineItemSingle,
						msg:        msg,
						id:         msg.ID,
						turnIndex:  msg.TurnIndex,
						finishedAt: messageFinishedAt(msg),
						msgIndex:   start + j,
					})
				}
			}
			continue
		}

		if isSwarmReportMessage(m) {
			// 相邻的 Swarm 汇报消息合并成一个时间线条目：这些胶囊在同一行里横向流式排列，
			// 不再各自占一整行。条目 Key 固定为首条消息 ID，后续到达的汇报只增加行内胶囊。
			start := i
			for i < n && isSwarmReportMessage(messages[i]) {
				i++
			}
			run := messages[start:i]
			items = append(items, timelineItem{
				kind:       timelineItemSwarmReport,
				msg:        run[0],
				reportMsgs: run,
				id:         run[0].ID,
				turnIndex:  run[0].TurnIndex,
				msgIndex:   start,
			})
			continue
		}

		if isQuestionAnswerMessage(m) {
			// 后端写入的问答记录：直接渲染成问答卡片，位置就是这条消息在时间线里的位置，
			// 既不与工具消息合组，也不当成普通系统消息。正文不是合法 JSON 时退回下面的
			// 普通单条渲染（系统消息显示原文）。
			if q, ok := parseQuestionAnswerMessage(m); ok {
				items = append(items, timelineItem{
					kind:             timelineItemAnsweredQuestion,
					msg:              m,
					id:               answeredQuestionCardID(q.ID, m.ID),
					turnIndex:        m.TurnIndex,
					answeredQuestion: q,
					finishedAt:       messageFinishedAt(m),
					msgIndex:         i,
				})
				i++
				continue
			}
		}

		items = append(items, timelineItem{
			kind:       timelineItemSingle,
			msg:        m,
			id:         m.ID,
			turnIndex:  m.TurnIndex,
			finishedAt: messageFinishedAt(m),
			msgIndex:   i,
		})
		i++
	}
	return items
}

// timelineGroupSegment 是展开后的工具组内的一个节拍：段首与段尾（不含）在 run 内的下标。
// 一条思考消息加上它后面紧跟的所有连续工具调用构成一段；组开头没有前置思考的工具单独成一段。
type timelineGroupSegment struct {
	start int
	end   int
}

// groupSegments 把一组活动消息切成展开后的节拍：一条思考消息加上它后面紧跟的所有连续工具调用为一段，
// 直到下一条思考消息（或组结束）为止；组开头、没有前置思考的工具调用单独成一段（该段的思考为空）。
func groupSegments(run []Message) []timelineGroupSegment {
	segs := make([]timelineGroupSegment, 0, len(run))
	for i := 0; i < len(run); {
		start := i
		if isReasoningMessage(run[i]) {
			i++
		}
		for i < len(run) && !isReasoningMessage(run[i]) && run[i].Role == "tool" {
			i++
		}
		if i == start {
			// 既不是思考行也不是工具行（不应出现）：单独成段，避免死循环
			i++
		}
		segs = append(segs, timelineGroupSegment{start: start, end: i})
	}
	return segs
}

// appendGroupSegmentItems 生成展开后工具组的各节拍条目：每段生成一个 beat 条目，
// 条目里带上段内的思考消息与它触发的工具调用；组开头没有思考的那段只带工具，思考为空。
func appendGroupSegmentItems(items []timelineItem, run []Message, start int, groupID string) []timelineItem {
	segs := groupSegments(run)
	for si, seg := range segs {
		segMsgs := run[seg.start:seg.end]
		var thought Message
		toolStart := seg.start
		if isReasoningMessage(segMsgs[0]) {
			thought = segMsgs[0]
			toolStart = seg.start + 1
		}
		items = append(items, timelineItem{
			kind:       timelineItemToolBeat,
			msg:        thought,
			tools:      run,
			beatTools:  run[toolStart:seg.end],
			id:         groupID + "/beat-" + segMsgs[0].ID,
			turnIndex:  segMsgs[0].TurnIndex,
			finishedAt: messageFinishedAt(segMsgs[len(segMsgs)-1]),
			groupID:    groupID,
			isFirst:    si == 0,
			isLast:     si == len(segs)-1,
			msgIndex:   start + seg.start,
		})
	}
	return items
}

// containsMessageID 判断消息列表里是否有指定 ID 的消息；空 ID 一律为 false。
func containsMessageID(messages []Message, id string) bool {
	if id == "" {
		return false
	}
	for _, m := range messages {
		if m.ID == id {
			return true
		}
	}
	return false
}

// itemHoldsTurnEnd 判断该时间线项是否承载本回合的回合完成块：即它渲染了本回合最后一条助手消息。
// 折叠的工具组头、展开后组内末段节拍、以及单条消息都可以承担；其余行不承担。
func itemHoldsTurnEnd(item timelineItem, lastAssistantPerTurn map[int]string) bool {
	id := lastAssistantPerTurn[item.turnIndex]
	if id == "" {
		return false
	}
	switch item.kind {
	case timelineItemSingle:
		return item.msg.ID == id
	case timelineItemToolGroup:
		// 展开的组不由组头承担：完成块跟在组内可见的最后一行后面
		return !item.groupExpanded && containsMessageID(item.tools, id)
	case timelineItemToolBeat:
		return item.isLast && containsMessageID(item.tools, id)
	case timelineItemTurnWork:
		// 回合折叠项不承载完成块：最终答复留在折叠项之外，完成块跟在它后面。
		return false
	default:
		return false
	}
}

// turnRunSuppressesEnd 判断「仍在进行中的回合」是否要压掉本回合的完成块：
// running 表示会话仍在跑这一轮，latestTurn 是消息里的最新回合号（-1 表示没有消息）。
// 回合进行中每一步（每次模型调用）结束都会先写一次该回合用量，只看用量有值就会在步骤之间
// 的间隙提前显示「已完成」；这里只压最新回合，历史回合在会话运行中也照常显示完成块。
func turnRunSuppressesEnd(running bool, latestTurn, turnIndex int) bool {
	return running && latestTurn >= 0 && turnIndex >= latestTurn
}

// groupTurnEndMessage 从组内消息里挑出代表本回合的助手消息，用作完成块的 Key 与用量来源：
// 取最后一条 assistant 消息，没有时退回最后一条消息。
func groupTurnEndMessage(tools []Message) (Message, bool) {
	if len(tools) == 0 {
		return Message{}, false
	}
	for i := len(tools) - 1; i >= 0; i-- {
		if tools[i].Role == "assistant" {
			return tools[i], true
		}
	}
	return tools[len(tools)-1], true
}

// isGroupRowKind 判断是否为工具组的组头行或组内节拍行。
func isGroupRowKind(kind timelineItemKind) bool {
	switch kind {
	case timelineItemToolGroup, timelineItemToolBeat:
		return true
	default:
		return false
	}
}

// turnWorkFoldPrefix 是回合折叠项的稳定 ID 前缀：完整 ID 为 "turnwork-" + 该回合用户消息 ID，
// 同一回合反复重建时 ID 不变，展开状态与展开动画都能延续。
const turnWorkFoldPrefix = "turnwork-"

// turnWorkFoldID 返回一个回合折叠项的 ID。
func turnWorkFoldID(headMsgID string) string { return turnWorkFoldPrefix + headMsgID }

// turnHeaderShimmerLabel 是运行中回合组头扫光条的 a11y Label，用来在测试里确认页面上只有一处运行中扫光。
const turnHeaderShimmerLabel = "运行中的回合头扫光"

// turnWorkInnerGap 返回折叠区里第 idx 个条目上方的间距：首项贴近组头，其余按普通列表间距。
func turnWorkInnerGap(idx int) float32 {
	if idx <= 0 {
		return 4
	}
	return 10
}

// itemLastMsgIndex 返回条目覆盖的最后一条消息下标：组头项覆盖整组消息，其余只有一条。
// 未设置 msgLastIndex 时退回 msgIndex，两者取大。
func itemLastMsgIndex(item timelineItem) int {
	last := item.msgLastIndex
	if last < item.msgIndex {
		last = item.msgIndex
	}
	if item.kind == timelineItemToolGroup && len(item.tools) > 1 {
		if end := item.msgIndex + len(item.tools) - 1; end > last {
			last = end
		}
	}
	return last
}

// itemIsActivity 判断条目在回合折叠里算不算「工作过程」：思考、工具组、工具节拍或单条工具调用。
func itemIsActivity(item timelineItem) bool {
	switch item.kind {
	case timelineItemToolGroup, timelineItemToolBeat:
		return true
	case timelineItemSingle:
		return isActivityMessage(item.msg)
	}
	return false
}

// itemTouchesIDs 判断条目涉及的消息里是否有 skipIDs 中的 ID。
func itemTouchesIDs(item timelineItem, ids map[string]bool) bool {
	if item.msg.ID != "" && ids[item.msg.ID] {
		return true
	}
	for _, list := range [][]Message{item.tools, item.beatTools, item.reportMsgs} {
		for _, m := range list {
			if m.ID != "" && ids[m.ID] {
				return true
			}
		}
	}
	return false
}

// timelineTurnWorkFold 描述一个回合里可以收进折叠项的那一段：foldStart 之前（用户消息）保持可见，
// [foldStart, answerIdx) 收进折叠项，answerIdx 起是最终答复及其后的条目。
type timelineTurnWorkFold struct {
	foldStart int
	answerIdx int
}

// findTurnWorkFold 在同一个回合的条目里找出可折叠的一段，并检查全部放行条件：
// 回合条目以用户消息开头、有非空的最终助手正文、正文之前至少有一条活动条目；
// 回合要求不在运行中、不含查找命中或待跳转目标、还没有折叠过。
// lastAssistant 是「回合号 -> 该回合最后一条助手消息 ID」，与渲染时的回合完成块同源。
func findTurnWorkFold(segment []timelineItem, lastAssistant map[int]string, runningTurn int, skipIDs map[string]bool) (timelineTurnWorkFold, bool) {
	foldStart := 1
	head := segment[0]
	if len(segment) < 3 {
		return timelineTurnWorkFold{}, false
	}
	if head.kind != timelineItemSingle || head.msg.Role != "user" {
		return timelineTurnWorkFold{}, false
	}
	turn := head.turnIndex
	// 运行中的回合不折叠：还在写的内容随时会变长，折叠起来反而让人猜不到进度。
	if turn == runningTurn {
		return timelineTurnWorkFold{}, false
	}
	answerID := lastAssistant[turn]
	if answerID == "" {
		return timelineTurnWorkFold{}, false
	}
	answerIdx := -1
	for i := foldStart; i < len(segment); i++ {
		if segment[i].kind == timelineItemTurnWork {
			// 已经折叠过的回合保持原样：折叠必须幂等，增量重建会把旧折叠项拼回来。
			return timelineTurnWorkFold{}, false
		}
		if segment[i].kind == timelineItemSingle && segment[i].msg.ID == answerID {
			answerIdx = i
		}
	}
	if answerIdx <= foldStart {
		return timelineTurnWorkFold{}, false
	}
	// 没有正文的回合不折叠：本来就没有答复可留着看，收起后会只剩一个空壳组头。
	if strings.TrimSpace(segment[answerIdx].msg.Text) == "" {
		return timelineTurnWorkFold{}, false
	}
	hasActivity := false
	for _, it := range segment[foldStart:answerIdx] {
		// 同一段里再出现用户消息，说明这不是一个规整的回合（例如多条提问挤在同一个 turnIndex），
		// 整段对折会把后面的提问一并藏起来，所以直接放弃折叠。
		if it.kind == timelineItemSingle && it.msg.Role == "user" {
			return timelineTurnWorkFold{}, false
		}
		if itemTouchesIDs(it, skipIDs) {
			// 查找命中或待跳转目标被收起来，高亮与跳转就落在看不见的行上。
			return timelineTurnWorkFold{}, false
		}
		if itemIsActivity(it) {
			hasActivity = true
		}
	}
	if !hasActivity {
		return timelineTurnWorkFold{}, false
	}
	return timelineTurnWorkFold{foldStart: foldStart, answerIdx: answerIdx}, true
}

// buildTurnWorkItem 把折叠区里的条目拼成回合折叠项：工具组按当前开合状态重建节拍（折叠区内默认展开），
// 展开折叠区后就能看到每一条命令；组头耗时取回合首尾时间，存在失败工具时置组头警示。
// 折叠区只覆盖最终答复之前的工作过程。
func buildTurnWorkItem(s *chatState, threadID string, segment []timelineItem, fold timelineTurnWorkFold) timelineItem {
	region := segment[fold.foldStart:fold.answerIdx]
	head := segment[0]

	var exp map[string]bool
	s.mu.Lock()
	if len(s.expandedTool) > 0 {
		exp = make(map[string]bool, len(s.expandedTool))
		for k, v := range s.expandedTool {
			exp[k] = v
		}
	}
	s.mu.Unlock()

	// 顶层已经展开过的工具组，它的节拍条目本来就在 region 里；这里若再按开合状态重建一遍，
	// 同一个节拍会出现两次（同一父节点下的 Key 重复，渲染直接 panic）。
	// 所以先记下 region 自带节拍的组，这些组只保留原有节拍，不再重建。
	regionBeatGroups := make(map[string]bool, len(region))
	for _, it := range region {
		if it.kind == timelineItemToolBeat && it.groupID != "" {
			regionBeatGroups[it.groupID] = true
		}
	}

	work := make([]timelineItem, 0, len(region))
	msgs := make([]Message, 0, len(region))
	for _, it := range region {
		switch it.kind {
		case timelineItemToolGroup:
			it.groupExpanded = toolGroupOpen(exp, it.groupID, true)
			work = append(work, it)
			if it.groupExpanded && !regionBeatGroups[it.groupID] {
				work = appendGroupSegmentItems(work, it.tools, it.msgIndex, it.groupID)
			}
			// 组头项的 tools 覆盖整组消息，节拍的 beatTools 都在里面，不重复计数。
			msgs = append(msgs, it.tools...)
		case timelineItemToolBeat:
			work = append(work, it)
		default:
			work = append(work, it)
			// 中途的助手正文不是工具调用，不进组头计数；思考行仍然算「思考 N 次」。
			if it.msg.Role != "assistant" || isReasoningMessage(it.msg) {
				msgs = append(msgs, it.msg)
			}
		}
	}
	attention := false
	for _, m := range msgs {
		if isToolFailed(m) {
			attention = true
			break
		}
	}

	// 折叠区可能一直铺到段尾（静默回合没有答复）：这时没有答复可用算耗时，完成时间取折叠区最后一项。
	elapsed := ""
	var finishedAt time.Time
	if fold.answerIdx < len(segment) {
		start, spanEnd := turnFinishedSpanFor(s, segment[fold.answerIdx].msg)
		if d := spanEnd.Sub(start); d >= time.Second {
			elapsed = formatShortDuration(d)
		}
		finishedAt = spanEnd
	} else if len(region) > 0 {
		finishedAt = region[len(region)-1].finishedAt
	}

	return timelineItem{
		kind:          timelineItemTurnWork,
		id:            turnWorkFoldID(head.msg.ID),
		threadID:      threadID,
		turnIndex:     head.turnIndex,
		tools:         msgs,
		workItems:     work,
		workElapsed:   elapsed,
		workAttention: attention,
		// msgIndex 指向折叠区覆盖的第一条消息（用户消息留在折叠项之外），
		// 这样增量重建的切点与段尾下标都落在折叠项自己身上，不会重复生成用户消息。
		msgIndex:     region[0].msgIndex,
		msgLastIndex: itemLastMsgIndex(region[len(region)-1]),
		finishedAt:   finishedAt,
	}
}

// turnWorkFoldSkipIDs 收集不能收进折叠区的消息 ID：会话内查找的命中项，以及待消费的跨会话跳转目标。
func (s *chatState) turnWorkFoldSkipIDs(threadID string) map[string]bool {
	if s == nil {
		return nil
	}
	var ids map[string]bool
	if st := getChatFindState(s); st != nil {
		query := strings.TrimSpace(st.query)
		if query != "" {
			s.mu.Lock()
			msgs := append([]Message(nil), s.messages...)
			s.mu.Unlock()
			for _, id := range findMessageMatches(msgs, query, st.caseSensitive) {
				if ids == nil {
					ids = make(map[string]bool)
				}
				ids[id] = true
			}
		}
	}
	messageJumpMu.Lock()
	jump, hasJump := messageJumps[s]
	messageJumpMu.Unlock()
	if hasJump && messageJumpApplies(jump, time.Now(), threadID) {
		if ids == nil {
			ids = make(map[string]bool)
		}
		ids[jump.messageID] = true
	}
	return ids
}

// turnWorkLastAnswers 按本份 items 统计每个回合最后一条助手正文消息 ID。
// 不读 s.messages：bee 浮窗会拿自己的消息走同一套重建逻辑，只有 items 才是本次折叠的真实数据。
func turnWorkLastAnswers(items []timelineItem) map[int]string {
	answers := make(map[int]string)
	for _, it := range items {
		if it.kind != timelineItemSingle || it.msg.Role != "assistant" || isReasoningMessage(it.msg) {
			continue
		}
		if strings.TrimSpace(it.msg.Text) == "" {
			continue
		}
		answers[it.turnIndex] = it.msg.ID
	}
	return answers
}

// isSplitSessionItem 判断条目的是不是分割线消息：分割线是界面上的界线，
// 只作为独立一行渲染，永远不进回合折叠区。
func isSplitSessionItem(item timelineItem) bool {
	return item.msg.Kind == splitSessionKind
}

// isContextCompactedItem 判断条目的是不是「上下文已压缩」边界消息：与分割线同一定位，
// 只作为独立一行渲染，不进回合折叠区。
func isContextCompactedItem(item timelineItem) bool {
	return item.msg.Kind == contextCompactedKind
}

// isTimelineBoundaryItem 判断条目是不是时间线上的界线（分割线、压缩边界）：
// 它们自己另起一段，既不会被收进折叠区，也不会把界线藏起来。
func isTimelineBoundaryItem(item timelineItem) bool {
	return isSplitSessionItem(item) || isContextCompactedItem(item)
}

// inlineRunningTurnHeader 把运行态折叠头摊回它收起的过程条目，供下一次重建复用；
// 已结束的折叠项不是运行态，原样保留。运行态头固定插在用户消息之后，所以只看 segment[1]。
func inlineRunningTurnHeader(segment []timelineItem) []timelineItem {
	if len(segment) < 2 || segment[1].kind != timelineItemTurnWork || !segment[1].workRunning {
		return segment
	}
	out := make([]timelineItem, 0, len(segment)-1+len(segment[1].workItems))
	out = append(out, segment[0])
	out = append(out, segment[1].workItems...)
	out = append(out, segment[2:]...)
	return out
}

// runningTurnHeaderItem 为运行中的回合造一个临时折叠头：把用户消息之后、第一条助手正文之前的
// 思考与工具调用收进头里，只留一行「已处理 Ns」实时计时；回合结束后这个头会被摘掉，再由
// applyTurnWorkFold 正常折叠整段。segment 里可能已经有一个上一帧的运行态头，先摊平再重建，
// 保证重复调用得到同样的结果。
func runningTurnHeaderItem(s *chatState, threadID string, segment []timelineItem) (timelineItem, []timelineItem, bool) {
	segment = inlineRunningTurnHeader(segment)
	if len(segment) == 0 || segment[0].kind != timelineItemSingle || segment[0].msg.Role != "user" {
		return timelineItem{}, segment, false
	}
	// 最终答复（或中途正文）出现后就留在头外：还在长出来的正文不能被藏起来。
	stop := len(segment)
	for i := 1; i < len(segment); i++ {
		it := segment[i]
		if it.kind == timelineItemSingle && it.msg.Role == "assistant" && !isReasoningMessage(it.msg) {
			stop = i
			break
		}
	}
	if stop <= 1 {
		return timelineItem{}, segment, false
	}
	region := segment[1:stop]
	msgs := make([]Message, 0, len(region))
	activity := false
	for _, it := range region {
		switch it.kind {
		case timelineItemToolGroup:
			msgs = append(msgs, it.tools...)
			activity = activity || len(it.tools) > 0
		case timelineItemToolBeat:
			msgs = append(msgs, it.beatTools...)
			if isReasoningMessage(it.msg) {
				msgs = append(msgs, it.msg)
			}
			activity = true
		default:
			if it.msg.Role != "assistant" || isReasoningMessage(it.msg) {
				msgs = append(msgs, it.msg)
			}
			activity = activity || itemIsActivity(it)
		}
	}
	if !activity {
		return timelineItem{}, segment, false
	}
	// rest 只保留用户消息与运行态头之外的条目（首条助手正文及其后的内容）：
	// 被头收住的过程条目已经在 workItems 里，不能再追加一次，否则同一条消息会渲染两遍。
	rest := make([]timelineItem, 0, 1+len(segment)-stop)
	rest = append(rest, segment[0])
	rest = append(rest, segment[stop:]...)
	return timelineItem{
		kind:          timelineItemTurnWork,
		id:            turnWorkFoldID(segment[0].msg.ID),
		threadID:      threadID,
		turnIndex:     segment[0].turnIndex,
		workRunning:   true,
		workStartedAt: turnStartTimeFor(s, segment[0].turnIndex),
		tools:         msgs,
		workItems:     append([]timelineItem(nil), region...),
		// 下标覆盖被收起的那段过程（不含用户消息）：增量切点落在过程内部时整段重建，
		// 用户消息留在已复用的前缀里，重建后再在它后面接上新的运行态头。
		msgIndex:     region[0].msgIndex,
		msgLastIndex: itemLastMsgIndex(region[len(region)-1]),
	}, rest, true
}

// applyTurnWorkFold 把已结束回合的思考、工具调用与中途正文收进一个折叠项，最终答复仍留在折叠项之外。
// 只在时间线重建的末尾调用：条目已按消息顺序生成，这里按 turnIndex 把它们分回各个回合。
func applyTurnWorkFold(s *chatState, threadID string, items []timelineItem) []timelineItem {
	if s == nil || len(items) == 0 {
		return items
	}
	runningTurn := -1
	if running, turn := s.runningTurn(threadID); running {
		runningTurn = turn
	}
	lastAssistant := turnWorkLastAnswers(items)
	skipIDs := s.turnWorkFoldSkipIDs(threadID)

	out := make([]timelineItem, 0, len(items))
	for i := 0; i < len(items); {
		end := i + 1
		for end < len(items) && items[end].turnIndex == items[i].turnIndex {
			// 分割线与压缩边界在这里结束当前回合的折叠区间：它们自己另起一段，既不会被收进折叠区，
			// 也不会把上一轮答复之后的界线藏起来（TurnIndex 与上一轮相同时尤其要紧）。
			if isTimelineBoundaryItem(items[end]) {
				break
			}
			end++
		}
		segment := items[i:end]
		isRunningTurn := runningTurn >= 0 && items[i].turnIndex == runningTurn
		// 上一帧的运行态头先摊回过程条目：运行中的回合要按最新消息重建，结束的回合要正常折叠。
		segment = inlineRunningTurnHeader(segment)
		if isRunningTurn {
			// 运行中的回合不折叠整段，只在用户消息之后放一行计时头，收住已经跑过的过程消息。
			// 已经出现的答复或中途正文留在头外，仍在生成的文字不能被藏起来。
			if header, rest, ok := runningTurnHeaderItem(s, threadID, segment); ok {
				out = append(out, rest[0])
				out = append(out, header)
				out = append(out, rest[1:]...)
			} else {
				out = append(out, segment...)
			}
			i = end
			continue
		}
		if fold, ok := findTurnWorkFold(segment, lastAssistant, runningTurn, skipIDs); ok {
			folded := buildTurnWorkItem(s, threadID, segment, fold)
			tail := append([]timelineItem(nil), segment[fold.answerIdx:]...)
			if len(tail) > 0 {
				// 完成信息已经由折叠头承载：负责收尾的答复只保留改动文件卡片，不再画第二条完成行。
				tail[0].workHeaderID = folded.id
			}
			out = append(out, segment[:fold.foldStart]...)
			out = append(out, folded)
			out = append(out, tail...)
		} else {
			out = append(out, segment...)
		}
		i = end
	}
	return out
}

// attachResolvedPlans 把已裁决的计划历史态、派发占位行与子线程胶囊行挂到时间线里。
// chat.go 每帧只调用这一个挂载入口，所以胶囊行也在这里挂载：insertSwarmCapsuleRows 在每条
// 用户消息之前插入代表上一回合的胶囊行（上一轮的胶囊留在它所属回合的末尾），
// appendSwarmCapsuleRow 再在末尾追加代表最新回合的胶囊行。
// 胶囊行的实际内容由 renderSwarmTimelineCapsules 按当前 Swarm 状态实时渲染，没有子线程时整行渲染为空。
// 派发占位行同样由渲染时按当前状态决定是否显示。
// attachResolvedPlans 把已裁决的计划历史态、派发占位行与子线程胶囊行拼进时间线。
// 主会话每帧都要重建一遍：这里按本帧最多要追加的条目数一次分配到位，
// 后面的 slices.Insert 与 append 都落在这块内存里，不再一帧把整表复制四五遍。
func attachResolvedPlans(items []timelineItem, plans []resolvedPlan) []timelineItem {
	return attachPlanAndQuestionItems(items, plans, nil)
}

// attachPlanAndQuestionItems 把已裁决的计划历史态、已答复的提问历史态、派发占位行与子线程胶囊行拼进时间线。
// 问答条目接在同回合的计划卡之后，两者共用同一套插入规则。
func attachPlanAndQuestionItems(items []timelineItem, plans []resolvedPlan, answered []answeredQuestion) []timelineItem {
	extra := len(plans) + len(answered) + 2 // 已裁决计划条 + 已答复提问条 + 派发占位行 + 末尾胶囊行
	if turns := swarmCapsuleRowTurns(items); len(turns) > 1 {
		extra += len(turns) - 1 // 每条用户消息之前插一行胶囊
	}
	out := make([]timelineItem, len(items), len(items)+extra)
	copy(out, items)
	return appendSwarmCapsuleRow(insertSwarmCapsuleRows(appendSwarmDispatchRow(attachAnsweredQuestionItems(attachResolvedPlanItems(out, plans), answered))))
}

// resolvedPlansKey 返回已裁决计划的指纹：拼装结果只在计划集合变化时需要重建。
// 计划卡片只渲染 ID、正文、裁决结果与所属回合，正文对同一个 ID 不会变，裁决时间不参与渲染，
// 所以指纹只要盖住 ID、裁决结果与回合号就够了。
func resolvedPlansKey(plans []resolvedPlan) string {
	if len(plans) == 0 {
		return ""
	}
	var b strings.Builder
	for _, p := range plans {
		b.WriteString(p.ID)
		if p.Approved {
			b.WriteByte('+')
		} else {
			b.WriteByte('-')
		}
		b.WriteString(strconv.Itoa(p.TurnIndex))
		b.WriteByte(';')
	}
	return b.String()
}

// timelineAssemblyKey 把计划指纹与问答指纹拼成一个缓存键；两者都为空时返回空串（与旧行为一致）。
func timelineAssemblyKey(plans []resolvedPlan, answered []answeredQuestion) string {
	planKey := resolvedPlansKey(plans)
	answerKey := answeredQuestionsKey(answered)
	switch {
	case answerKey == "":
		return planKey
	case planKey == "":
		return "q|" + answerKey
	default:
		return planKey + "|q|" + answerKey
	}
}

// assembledTimelineItems 返回主会话时间线的拼装结果（含已裁决计划条、派发占位行与各回合胶囊行）。
func (s *chatState) assembledTimelineItems(plans []resolvedPlan) []timelineItem {
	return s.assembledTimelineItemsWithQuestions(plans, nil)
}

// assembledTimelineItemsWithQuestions 在 assembledTimelineItems 的基础上再拼入已答复提问的历史态。
// 结果只取决于会话条目快照、展开状态、已裁决计划/已答复提问与选中会话，几者都没变时直接复用上一帧的结果：
// 静止时每帧重新拼一份几十到几百条的切片（每条 500 多字节）纯属浪费。
// 缓存键同时盖住计划指纹与问答指纹，新增问答记录时时间线自动重建。
func (s *chatState) assembledTimelineItemsWithQuestions(plans []resolvedPlan, answered []answeredQuestion) []timelineItem {
	if s == nil {
		return nil
	}
	key := timelineAssemblyKey(plans, answered)
	s.mu.Lock()
	selected := s.selectedThreadID
	s.mu.Unlock()
	isSteward := selected != "" && selected == s.stewardThreadID()
	var boardRev uint64
	if isSteward {
		boardRev = s.stewardBoardRev()
		key += "|steward|" + strconv.FormatUint(boardRev, 10)
	}

	s.mu.Lock()
	if selected == s.selectedThreadID && (!isSteward || boardRev == s.stewardBoard.rev) && s.timelineAssembledValid &&
		s.timelineAssembledSel == s.selectedThreadID &&
		s.timelineAssembledRev == s.timelineCacheRev &&
		s.timelineAssembledExpand == s.expandedRev &&
		s.timelineAssembledPlans == key {
		items := s.timelineAssembledItems
		s.mu.Unlock()
		return s.withForkOriginItem(items)
	}
	// 拼装期间不持锁（外部消息增量可能正在落地）；拼完再确认这几个输入没变才存缓存，
	// 否则只把本次结果交给调用方，下一帧重建。
	// 版本号必须取时间线缓存自己的版本（timelineCacheRev），不能取 messagesRev：
	// 首帧的时间线条目是按当时的空消息快照拼的，历史消息加载又先递增 messagesRev 再落地消息，
	// 两者抢跑时 base 是旧的、messagesRev 已是新值；按 messagesRev 记账会把这份没有消息的时间线
	// 记成“新版本”的结果，之后消息版本不再变化，缓存就一直命中，时间线永远空白。
	sel := s.selectedThreadID
	baseRev := s.timelineCacheRev
	expandedRev := s.expandedRev
	base := s.timelineCacheItems
	s.mu.Unlock()

	items := attachPlanAndQuestionItems(base, plans, answered)
	if isSteward && sel == selected {
		var taskIndex stewardTaskIndex
		taskIndex, boardRev = s.stewardTaskIndex()
		key = timelineAssemblyKey(plans, answered) + "|steward|" + strconv.FormatUint(boardRev, 10)
		items = insertStewardTaskRows(items, taskIndex)
		items = insertStewardTimeRows(items)
	}

	s.mu.Lock()
	if selected == sel && s.selectedThreadID == sel && s.timelineCacheRev == baseRev && s.expandedRev == expandedRev && (!isSteward || s.stewardBoard.rev == boardRev) {
		s.timelineAssembledSel = sel
		s.timelineAssembledRev = baseRev
		s.timelineAssembledExpand = expandedRev
		s.timelineAssembledPlans = key
		s.timelineAssembledItems = items
		s.timelineAssembledValid = true
	}
	s.mu.Unlock()
	return s.withForkOriginItem(items)
}

// withForkOriginItem 把带入的历史或 fork 来源条插到时间线最顶部。
// 故意不存进拼装缓存：来源字段随会话列表到来，与“会话 ID + 消息版本 + 展开状态 + 计划指纹”
// 这个缓存键不同步，现插现用才能保证来源条不会因缓存命中而缺失。
func (s *chatState) withForkOriginItem(items []timelineItem) []timelineItem {
	if s == nil {
		return items
	}
	s.mu.Lock()
	tid := s.selectedThreadID
	sourceThreadID := ""
	sourceMessageID := ""
	for _, th := range s.threads {
		if th.ID == tid {
			sourceThreadID = th.ForkedFromThreadID
			sourceMessageID = th.ForkedFromMessageID
			break
		}
	}
	s.mu.Unlock()
	if fc, loaded := s.loadedForwardedContext(tid); loaded && strings.TrimSpace(fc.Content) != "" {
		if _, ok := continuationStartInfoFor(s, tid); ok {
			if len(items) > 0 && items[0].kind == timelineItemForwardedContext {
				return items
			}
			return append([]timelineItem{forwardedContextItem(tid)}, items...)
		}
	}
	if tid == "" || sourceThreadID == "" {
		return items
	}
	// 转发会话的上下文卡片已经展示来源，也有「打开原会话」，再插来源条会重复。
	if sourceMessageID == "" {
		return items
	}
	if len(items) > 0 && items[0].kind == timelineItemForkOrigin {
		return items
	}
	return append([]timelineItem{forkOriginItem(tid, sourceThreadID)}, items...)
}

// attachResolvedPlanItems 将已裁决的计划历史态作为时间线条目，插在对应 turn 的末尾；如果难以定位 turn，追加在时间线末尾。
func attachResolvedPlanItems(items []timelineItem, plans []resolvedPlan) []timelineItem {
	if len(plans) == 0 {
		return items
	}
	res := items
	for _, p := range plans {
		planItem := timelineItem{
			kind:         timelineItemResolvedPlan,
			id:           "resolved-plan-" + p.ID,
			turnIndex:    p.TurnIndex,
			resolvedPlan: p,
		}
		if p.TurnIndex >= 0 {
			lastTurnIdx := -1
			for i := len(res) - 1; i >= 0; i-- {
				if res[i].turnIndex == p.TurnIndex {
					lastTurnIdx = i
					break
				}
			}
			if lastTurnIdx >= 0 {
				res = slices.Insert(res, lastTurnIdx+1, planItem)
				continue
			}
		}
		res = append(res, planItem)
	}
	return res
}

// isShowReasoningEnabled 检查当前配置中是否开启了显示思考过程。
func isShowReasoningEnabled(s *chatState) bool {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Session.ShowReasoningEnabled()
	}
	return true
}

// isAutoCollapseReasoningEnabled 检查通用会话设置中的思考自动折叠开关。
func isAutoCollapseReasoningEnabled(s *chatState) bool {
	st := getChatSettings(s)
	if st == nil {
		return true
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cfg.Session.AutoCollapseReasoningEnabled()
}

// isPinSentMessageEnabled 检查当前配置中是否开启了发送后置顶消息（取不到配置时视为关闭）。
// 它会锁设置状态的锁，必须在 s.mu 之外调用。
func isPinSentMessageEnabled(s *chatState) bool {
	if st := getChatSettings(s); st != nil {
		st.mu.Lock()
		defer st.mu.Unlock()
		return st.cfg.Session.PinSentMessageEnabled()
	}
	return false
}

// timelineItemTopGap 返回时间线第 i 项上方的间距，保留组头和等待行的特殊布局。
func timelineItemTopGap(items []timelineItem, i int, _ float32) float32 {
	if i <= 0 || i >= len(items) {
		return 0
	}
	if items[i].turnIndex != items[i-1].turnIndex {
		return turnGap
	}
	if items[i].kind == timelineItemToolBeat {
		if items[i].isFirst {
			return 4
		}
		return blockGap
	}
	// 等待后台任务条紧贴最后一条回复，不按列表间距拉开。
	if items[i].kind == timelineItemBackgroundWait {
		return 8
	}
	return blockGap
}

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
	if flashID == "" || !timelineItemContainsMessageID(item, flashID) {
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

// renderResolvedPlanCard 渲染已裁决的计划静态历史态卡片：标头为绿色药丸 Approved 或灰色药丸 Dismissed。
// 默认折叠，只露一行头部；展开后整篇 Markdown 铺开。
func renderResolvedPlanCard(c *ui.Context, s *chatState, plan resolvedPlan) {
	id := plan.ID
	if id == "" {
		id = fmt.Sprintf("plan-%d", plan.Time.UnixNano())
	}
	s.mu.Lock()
	collapsed, known := s.collapsedPlans[id]
	if !known {
		// 未记录折叠状态的历史条目（例如从旧版本恢复的）默认折叠。
		collapsed = true
	}
	s.mu.Unlock()

	accent := ThemeAccent(c)
	statusText := "Approved"
	statusBg := ThemeSuccess(c).Alpha(0.15)
	statusFg := ThemeSuccess(c)
	if !plan.Approved {
		statusText = "Dismissed"
		statusBg = ThemeSecondaryText(c).Alpha(0.15)
		statusFg = ThemeSecondaryText(c)
	}

	ui.Column(c.Key("resolved-plan-"+id)).
		Radius(RadiusXL).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Padding(Space5).
		Gap(Space4).
		Children(func() {
			ui.Row(c.Key("resolved-plan-header-" + id)).Gap(Space3).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconClipboardList()).Size(14, 14).TextColor(accent)
				ui.Text(c, "Plan").Bold().FontSize(scaledFont(TypeBody)).TextColor(ThemePrimaryText(c))
				ui.Row(c.Key("resolved-plan-pill-"+id)).Padding(Space0, Space3).Radius(RadiusLG).Background(statusBg).Children(func() {
					ui.Text(c, statusText).FontSize(scaledFont(TypeCaption)).Bold().TextColor(statusFg)
				})
				ui.Spacer(c)
				if plan.Plan != "" {
					renderPlanCopyButton(c, "resolved-plan-copy-"+id, plan.Plan)
					btnText := "折叠"
					if collapsed {
						btnText = "展开"
					}
					ui.ButtonBase(c.Key("resolved-plan-toggle-"+id)).
						Label(btnText).
						Tooltip(btnText).
						Padding(Space0, Space2).
						Radius(RadiusXS).
						Background(ThemeHover(c)).
						OnClick(func() {
							s.mu.Lock()
							if s.collapsedPlans == nil {
								s.collapsedPlans = make(map[string]bool)
							}
							s.collapsedPlans[id] = !collapsed
							s.mu.Unlock()
							s.holdChatPlace()
							s.invalidate()
						}).
						Children(func() {
							ui.Text(c, btnText).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
						})
				}
			})

			if !collapsed {
				renderPlanMarkdown(c, s, "resolved-plan-body-"+id, plan.Plan, 0)
			}
		})
}

// continuationStartPrefixes 是转发会话（Continued）与分支会话（Branch）标题的前缀及对应的来源提示语。
// 标题前缀只用于兼容旧会话，新会话按来源字段判定。
var continuationStartPrefixes = []struct {
	prefix string
	label  string
}{
	{"Continued: ", "继续自"},
	{"Branch: ", "分叉自"},
}

// continuationStartInfo 描述空转发/分叉会话的来源引导卡片内容。
type continuationStartInfo struct {
	Label          string // 来源提示语："继续自" 或 "分叉自"
	SourceTitle    string // 去掉前缀后的来源会话标题（完整原文，用于匹配来源会话）
	DisplayTitle   string // 卡片上展示的标题：过长时已截断并以 "..." 结尾
	TargetThreadID string // 同一项目内标题匹配来源会话的会话 ID，找不到时为空
}

// continuationSourceTitleMaxRunes 限制引导卡片上来源标题的最大字符数：超过时保留前 33 个字符
// 再加 "..."（合计 36 个字符），避免长标题在窄屏下把卡片撑变形。
const continuationSourceTitleMaxRunes = 36

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

// forwardedContextState 是按会话缓存转发上下文的小状态：内容不可变，每个会话只拉取一次，
// 并记住用户点击后的展开状态（键存在即覆盖模式默认值）。
type forwardedContextState struct {
	mu       sync.Mutex
	contexts map[string]ForwardedContext
	loaded   map[string]bool // 已尝试加载（含失败），不再自动重试
	inflight map[string]bool
	expanded map[string]bool
}

var (
	forwardedContextStates  = make(map[*chatState]*forwardedContextState)
	forwardedContextStateMu sync.Mutex
)

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

type forwardedTurn struct {
	Role string
	Text string
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

// continuationStartCardMaxWidth 是来源引导卡片的最大宽度：窄屏跟随容器宽度，宽屏不超过 520
// 并居中。
const continuationStartCardMaxWidth = 520

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

// swarmFenceEntry 是一条消息去掉 swarm 派单代码块后的正文缓存：src 用来判断正文是否又改过。
type swarmFenceEntry struct {
	src string
	out string
}

// swarmFenceCacheMax 是派单块正文缓存的最大条目数。
const swarmFenceCacheMax = 64

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

	col := ui.Column(c.Key("msg-md-" + m.ID)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Gap(Space1).Selectable().ContextMenu(messageContextMenu(c, s, m))
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

// codeBlockHeaderHeight 是代码块标题栏高度：控件高 ControlLG 加一格内边距。
const codeBlockHeaderHeight float32 = ControlLG + Space1

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

// reasoningMarkdownKey 是思考正文 Markdown 配置的缓存键：会话加基准字号。
type reasoningMarkdownKey struct {
	state *chatState
	size  float32
}

// reasoningMDOpts 保存每个 chatState 的思考正文 Markdown 配置（与助手正文分开，避免字号与颜色互相影响）。
// 按字号分开缓存：独立的思考行用 12，工具组节拍里的思考叙事用 13，两者可能在同一帧里同时存在。
var (
	reasoningMDOptsMu sync.Mutex
	reasoningMDOpts   = make(map[reasoningMarkdownKey]*markdown.Options)
)

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

// commandToolNames 为算作命令行执行的工具名（已转小写并去掉 _ 与 -）。
var commandToolNames = map[string]bool{
	"bash":         true,
	"shell":        true,
	"exec_command": true,
	"execcommand":  true,
	"command":      true,
	"runcommand":   true,
	"run_command":  true,
	"terminal":     true,
	"localshell":   true,
	"local_shell":  true,
	"exec":         true,
	"execute":      true,
}

var (
	toolCommandKeyRe      = regexp.MustCompile(`"(?:command|cmd|CommandLine|command_line|commandline|input|script|code)"\s*:\s*"((?:[^"\\]|\\.)*)"`)
	toolCommandArgsRe     = regexp.MustCompile(`"(?:args|argv)"\s*:\s*\[([^\]]+)\]`)
	toolExitCodeJSONRe    = regexp.MustCompile(`"(?:exitCode|exit_code|exitStatus|exit_status|status_code|exit)"\s*:\s*(-?\d+)`)
	toolExitCodeTextRe    = regexp.MustCompile(`(?i)(?:exit\s+code\s*[:=]?\s*|exit\s+status\s*[:=]?\s*|\bexit\s*[:=]?\s*|\[exit\s*[:=]?\s*|\(exit\s*[:=]?\s*)(-?\d+)`)
	toolDurationMsJSONRe  = regexp.MustCompile(`"(?:duration_ms|durationMs|elapsed_ms|elapsedMs)"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
	toolDurationSecJSONRe = regexp.MustCompile(`"(?:duration|elapsed|duration_seconds|durationSeconds)"\s*:\s*([0-9]+(?:\.[0-9]+)?)`)
	toolDurationStrJSONRe = regexp.MustCompile(`"(?:duration|elapsed)"\s*:\s*"([0-9]+(?:\.[0-9]+)?\s*(?:s|ms))"`)
	toolDurationTextRe    = regexp.MustCompile(`(?i)\b(?:in|took|duration:?|elapsed:?)\s*([0-9]+(?:\.[0-9]+)?\s*(?:s|ms))\b`)
	toolCommandArgItemRe  = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
)

// toolInputFields 解析工具消息中的完整输入 JSON 为 map[string]any，结果按消息 ID 缓存。
// 返回的 map 只读，调用方不得修改；m.ID 为空时不走缓存。
func toolInputFields(m Message) map[string]any {
	if m.ID == "" {
		return toolInputFieldsUncached(m)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasFields {
		fields := e.fields
		toolParseCacheMu.Unlock()
		return fields
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行：parseCommandToolInfoUncached 内部还会回调本函数，持锁调用会死锁。
	fields := toolInputFieldsUncached(m)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.fields, e.hasFields = fields, true
	} else {
		e = newToolParseCacheEntry(m)
		e.fields, e.hasFields = fields, true
		toolParseCachePut(m, e)
	}
	toolParseCacheMu.Unlock()
	return fields
}

// toolInputFieldsUncached 解析工具消息中的完整输入 JSON 为 map[string]any。
// 若 m.ToolInput 为空或解析失败，返回 nil。
func toolInputFieldsUncached(m Message) map[string]any {
	input := strings.TrimSpace(m.ToolInput)
	if input == "" {
		return nil
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(input), &fields); err != nil {
		return nil
	}
	return fields
}

// parseRFC3339Time 尝试用 RFC3339Nano 或 RFC3339 解析时间字符串。
func parseRFC3339Time(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// isToolBlockedByHook 判断工具结果是否由 PreToolUse hook 拦截/改道而来（例如 context-mode 插件把
// WebFetch 改道），这种结果是主动拦截而不是工具故障。新数据以 ToolStatus == "blocked" 为准；
// 老数据仍是 failed 并在正文里留一行 hook 错误，因此保留逐行文本嗅探作历史兜底。
// 逐行扫描而不是整段匹配，避免正文里零散的提及造成误判。
func isToolBlockedByHook(m Message) bool {
	if strings.EqualFold(strings.TrimSpace(m.ToolStatus), "blocked") {
		return true
	}
	// 老数据兜底：正文里单独成行的 PreToolUse hook 错误。
	for _, line := range strings.Split(m.Text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "PreToolUse:") && strings.Contains(line, " hook error:") {
			return true
		}
	}
	return false
}

// isToolFailed 判断工具执行是否失败。m.ToolStatus == "failed" 时视为失败，与现有 [failed] 判断取或；
// 被 PreToolUse hook 拦截/改道的结果虽以 [failed] 结尾，但它不是故障，不算失败。
func isToolFailed(m Message) bool {
	if isToolBlockedByHook(m) {
		return false
	}
	return m.ToolStatus == "failed" || strings.Contains(m.Text, "[failed]")
}

// CommandToolInfo 保存命令工具执行行的解析信息。
type CommandToolInfo struct {
	IsCommand bool   // 是否属于命令执行类工具
	Command   string // 命令正文（单行，超长截断）
	ExitCode  *int   // 退出码（0 为中性色，非 0 为错误色；拿不到为 nil）
	Duration  string // 耗时格式化文本（如「1.2s」；拿不到为空）
	Failed    bool   // 是否执行失败（m.ToolStatus == "failed" 或包含 [failed]）
}

// isCommandTool 判断消息是否属于命令执行类工具。
func isCommandTool(m Message) bool {
	if m.Role != "" && m.Role != "tool" {
		return false
	}
	if strings.EqualFold(m.Kind, "command") {
		return true
	}
	return commandToolNames[toolNameKey(toolMessageName(m))]
}

// truncateCommandLine 将命令正文规整为单行并在超出最大长度时截断。
func truncateCommandLine(cmd string, maxLen int) string {
	cmd = strings.TrimSpace(cmd)
	cmd = strings.ReplaceAll(cmd, "\r", "")
	cmd = strings.ReplaceAll(cmd, "\n", " ")
	cmd = strings.Join(strings.Fields(cmd), " ")
	runes := []rune(cmd)
	if len(runes) > maxLen {
		return string(runes[:maxLen]) + "..."
	}
	return cmd
}

// parseCommandToolInfo 解析命令工具消息中的命令正文、退出码和耗时，结果按消息 ID 缓存。
// 返回的 ExitCode 指针只读，调用方不得写入其指向的值；m.ID 为空时不走缓存。
func parseCommandToolInfo(m Message) CommandToolInfo {
	if m.ID == "" {
		return parseCommandToolInfoUncached(m)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasInfo {
		info := e.info
		toolParseCacheMu.Unlock()
		return info
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行，避免嵌套加锁。
	info := parseCommandToolInfoUncached(m)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.info, e.hasInfo = info, true
	} else {
		e = newToolParseCacheEntry(m)
		e.info, e.hasInfo = info, true
		toolParseCachePut(m, e)
	}
	toolParseCacheMu.Unlock()
	return info
}

// parseCommandToolInfoUncached 解析命令工具消息中的命令正文、退出码和耗时。
func parseCommandToolInfoUncached(m Message) CommandToolInfo {
	if !isCommandTool(m) {
		return CommandToolInfo{IsCommand: false}
	}

	info := CommandToolInfo{
		IsCommand: true,
		Failed:    isToolFailed(m),
	}

	// 1. 提取命令正文
	firstLine := m.Text
	if idx := strings.IndexByte(firstLine, '\n'); idx != -1 {
		firstLine = firstLine[:idx]
	}

	// 优先在 m.ToolInput（完整 JSON）上用 encoding/json 解析提取
	if fields := toolInputFields(m); fields != nil {
		for _, key := range []string{"command", "cmd", "CommandLine", "command_line", "commandline", "input", "script", "code"} {
			if val, ok := fields[key]; ok {
				if s, ok := val.(string); ok && strings.TrimSpace(s) != "" {
					info.Command = s
					break
				}
			}
		}
		if info.Command == "" {
			for _, key := range []string{"args", "argv"} {
				if val, ok := fields[key]; ok {
					switch arr := val.(type) {
					case []any:
						var parts []string
						for _, item := range arr {
							if item != nil {
								parts = append(parts, fmt.Sprint(item))
							}
						}
						if len(parts) > 0 {
							info.Command = strings.Join(parts, " ")
						}
					case []string:
						if len(arr) > 0 {
							info.Command = strings.Join(arr, " ")
						}
					}
					if info.Command != "" {
						break
					}
				}
			}
		}
	}

	// 解析失败或 ToolInput 为空时，回退到现有基于 Text 的正则逻辑
	if info.Command == "" {
		if match := toolCommandKeyRe.FindStringSubmatch(firstLine); len(match) > 1 {
			raw := match[1]
			if unq, err := strconv.Unquote(`"` + raw + `"`); err == nil {
				raw = unq
			}
			info.Command = raw
		} else if match := toolCommandArgsRe.FindStringSubmatch(firstLine); len(match) > 1 {
			var args []string
			for _, m := range toolCommandArgItemRe.FindAllStringSubmatch(match[1], -1) {
				arg := m[1]
				if unq, err := strconv.Unquote(`"` + arg + `"`); err == nil {
					arg = unq
				}
				args = append(args, arg)
			}
			if len(args) > 0 {
				info.Command = strings.Join(args, " ")
			}
		}
	}

	if info.Command == "" {
		tName := toolMessageName(m)
		trimmedFirst := strings.TrimSpace(firstLine)
		if strings.HasPrefix(trimmedFirst, tName) {
			rest := strings.TrimSpace(trimmedFirst[len(tName):])
			if !strings.HasPrefix(rest, "{") && rest != "" {
				info.Command = rest
			}
		}
	}

	if info.Command == "" {
		info.Command = toolSummary(m.Text)
	}
	info.Command = truncateCommandLine(info.Command, 80)

	// 2. 提取退出码 (ExitCode，沿用现有正则逻辑)
	if match := toolExitCodeJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
		if code, err := strconv.Atoi(match[1]); err == nil {
			info.ExitCode = &code
		}
	} else if match := toolExitCodeTextRe.FindStringSubmatch(m.Text); len(match) > 1 {
		if code, err := strconv.Atoi(match[1]); err == nil {
			info.ExitCode = &code
		}
	}

	// 3. 提取耗时 (Duration)
	// 优先由 ToolCompletedAt 与 CreatedAt 计算差值
	if start, ok1 := parseRFC3339Time(m.CreatedAt); ok1 {
		if end, ok2 := parseRFC3339Time(m.ToolCompletedAt); ok2 {
			dur := end.Sub(start)
			if dur < 0 {
				dur = 0
			}
			info.Duration = fmt.Sprintf("%.1fs", dur.Seconds())
		}
	}
	// 否则回退到现有正则逻辑
	if info.Duration == "" {
		if match := toolDurationMsJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
			if ms, err := strconv.ParseFloat(match[1], 64); err == nil {
				info.Duration = fmt.Sprintf("%.1fs", ms/1000.0)
			}
		} else if match := toolDurationStrJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
			strVal := strings.TrimSpace(match[1])
			if strings.HasSuffix(strVal, "ms") {
				numPart := strings.TrimSuffix(strVal, "ms")
				if ms, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", ms/1000.0)
				}
			} else if strings.HasSuffix(strVal, "s") {
				numPart := strings.TrimSuffix(strVal, "s")
				if s, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", s)
				}
			}
		} else if match := toolDurationSecJSONRe.FindStringSubmatch(m.Text); len(match) > 1 {
			if sec, err := strconv.ParseFloat(match[1], 64); err == nil {
				info.Duration = fmt.Sprintf("%.1fs", sec)
			}
		} else if match := toolDurationTextRe.FindStringSubmatch(m.Text); len(match) > 1 {
			strVal := strings.TrimSpace(match[1])
			if strings.HasSuffix(strVal, "ms") {
				numPart := strings.TrimSuffix(strVal, "ms")
				if ms, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", ms/1000.0)
				}
			} else if strings.HasSuffix(strVal, "s") {
				numPart := strings.TrimSuffix(strVal, "s")
				if s, err := strconv.ParseFloat(strings.TrimSpace(numPart), 64); err == nil {
					info.Duration = fmt.Sprintf("%.1fs", s)
				}
			}
		}
	}

	return info
}

// todoItem 表示待办清单中的一项。
type todoItem struct {
	Text   string // 待办正文
	Status string // pending / in_progress / completed
}

// todoTextKeys 是待办项正文的候选字段名；plan 类工具用 step，todos 类用 content。
var todoTextKeys = []string{"content", "step", "text", "task", "title"}

// parseTodoItems 解析 TodoWrite / update_plan 这类工具的输入 JSON（含 todos 或 plan 数组，
// 每项有 content/step 与 status），返回待办项列表；不是待办工具时 ok 为 false。
func parseTodoItems(input string) ([]todoItem, bool) {
	start := strings.IndexByte(input, '{')
	if start < 0 {
		return nil, false
	}
	var payload struct {
		Todos []map[string]any `json:"todos"`
		Plan  []map[string]any `json:"plan"`
	}
	if err := json.NewDecoder(strings.NewReader(input[start:])).Decode(&payload); err != nil {
		return nil, false
	}
	raw := payload.Todos
	if len(raw) == 0 {
		raw = payload.Plan
	}
	if len(raw) == 0 {
		return nil, false
	}

	items := make([]todoItem, 0, len(raw))
	for _, entry := range raw {
		text := firstTodoText(entry)
		if strings.TrimSpace(text) == "" {
			continue
		}
		status := "pending"
		if s, ok := entry["status"].(string); ok {
			status = normalizeTodoStatus(s)
		}
		items = append(items, todoItem{Text: text, Status: status})
	}
	if len(items) == 0 {
		return nil, false
	}
	return items, true
}

// todoItemsUncached 从一条消息里解析待办清单：优先解析结构化输入 ToolInput，
// 拿不到（旧消息或输入被截断）时回退到正文 m.Text。
func todoItemsUncached(m Message) ([]todoItem, bool) {
	if m.ToolInput != "" {
		if items, ok := parseTodoItems(m.ToolInput); ok {
			return items, true
		}
	}
	return parseTodoItems(m.Text)
}

// todoItemsFromMessage 解析一条消息的待办清单，结果按消息 ID 缓存：
// 时间线每帧都会对每条工具消息解析一次 JSON，而结果只取决于 ToolInput 与 Text。
// 返回的切片只读，调用方不得修改；m.ID 为空时不走缓存。
func todoItemsFromMessage(m Message) ([]todoItem, bool) {
	if m.ID == "" {
		return todoItemsUncached(m)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasTodo {
		items, ok := e.todoItems, e.todoOK
		toolParseCacheMu.Unlock()
		return items, ok
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行，避免与其它缓存路径嵌套加锁。
	items, ok := todoItemsUncached(m)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.todoItems, e.todoOK, e.hasTodo = items, ok, true
	} else {
		// 失配（Text 或 ToolInput 变了）时新建条目覆盖；输入字段只取决于 ToolInput，
		// 它没变就沿用旧条目上已算好的结果，不丢也不重算。
		// 待办结果同时取决于 ToolInput 与 Text，本次已经重新解析，无需沿用旧值。
		rebuilt := newToolParseCacheEntry(m)
		if hit && e.ToolInput == m.ToolInput {
			rebuilt.hasFields, rebuilt.fields = e.hasFields, e.fields
		}
		rebuilt.todoItems, rebuilt.todoOK, rebuilt.hasTodo = items, ok, true
		toolParseCachePut(m, rebuilt)
	}
	toolParseCacheMu.Unlock()
	return items, ok
}

// firstTodoText 取待办项第一个非空的正文字段。
func firstTodoText(entry map[string]any) string {
	for _, key := range todoTextKeys {
		if v, ok := entry[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// normalizeTodoStatus 把待办状态归一到 pending / in_progress / completed。
func normalizeTodoStatus(status string) string {
	s := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(status)))
	switch s {
	case "completed", "complete", "done", "finished":
		return "completed"
	case "in_progress", "inprogress", "active", "running", "doing":
		return "in_progress"
	default:
		return "pending"
	}
}

// todoCardKey 返回待办卡片开合状态在 s.expandedTool 里的键（与工具胶囊共用同一张表）。
func todoCardKey(msgID string) string { return "todo-" + msgID }

// todoProgress 返回待办清单的完成数与总数。
func todoProgress(items []todoItem) (done, total int) {
	for _, it := range items {
		if it.Status == "completed" {
			done++
		}
	}
	return done, len(items)
}

// todoProgressRatio 返回进度条的填充比例（0..1）：完成数除以总数，空列表为 0。
func todoProgressRatio(items []todoItem) float32 {
	done, total := todoProgress(items)
	if total <= 0 {
		return 0
	}
	return float32(done) / float32(total)
}

// todoFocusItem 返回收起摘要要显示的那一项：优先进行中，其次第一项未完成；
// 全部完成或空列表时 ok 为 false。
func todoFocusItem(items []todoItem) (todoItem, bool) {
	for _, it := range items {
		if it.Status == "in_progress" {
			return it, true
		}
	}
	for _, it := range items {
		if it.Status != "completed" {
			return it, true
		}
	}
	return todoItem{}, false
}

// todoCollapsedSummary 返回收起态那一行摘要：「完成数/总数 · 当前那一项」，
// 全部完成时显示「已全部完成」，空列表显示「暂无待办」。
func todoCollapsedSummary(items []todoItem) string {
	done, total := todoProgress(items)
	if total == 0 {
		return "暂无待办"
	}
	if done == total {
		return fmt.Sprintf("%d/%d · 已全部完成", done, total)
	}
	if it, ok := todoFocusItem(items); ok {
		return fmt.Sprintf("%d/%d · %s", done, total, strings.Join(strings.Fields(it.Text), " "))
	}
	return fmt.Sprintf("%d/%d", done, total)
}

// todoInputLooksLikePlan 廉价预判一段文本是否可能是待办工具输入，
// 让 latestTodoMessageID 不必为整段历史做 JSON 解析。
func todoInputLooksLikePlan(raw string) bool {
	if raw == "" || !strings.Contains(raw, `"status"`) {
		return false
	}
	return strings.Contains(raw, `"todos"`) || strings.Contains(raw, `"plan"`)
}

// latestTodoMessageID 返回给定消息里最后一条待办消息的 ID；没有待办消息时返回空。
// 倒序扫描，命中即停。
func latestTodoMessageID(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		for _, raw := range [2]string{m.ToolInput, m.Text} {
			if !todoInputLooksLikePlan(raw) {
				continue
			}
			if _, ok := parseTodoItems(raw); ok {
				return m.ID
			}
		}
	}
	return ""
}

// todoCardExpanded 返回待办卡片此刻是否展开：用户在卡片头部点过就以用户选择为准；
// 否则只有「当前线程最新的一条待办，且线程正在运行」时默认展开，其余情况默认收起。
// running 由调用方算好传入（s.isThreadRunning 内部自己加锁，不能在这里持锁调用）。
func (s *chatState) todoCardExpanded(m Message, running bool) bool {
	s.mu.Lock()
	open, decided := s.expandedTool[todoCardKey(m.ID)]
	latest := latestTodoMessageID(s.messages)
	s.mu.Unlock()
	if decided {
		return open
	}
	return running && latest == m.ID
}

// toggleTodoCard 记录用户对某条待办卡片开合的显式选择。
func (s *chatState) toggleTodoCard(msgID string, open bool) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	s.expandedTool[todoCardKey(msgID)] = open
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// renderTodoCard 把待办清单工具调用渲染成卡片：头部可折叠、下方一条 2px 进度条；
// 展开时每项前是状态图标（空心圆、进行中、对勾），已完成项置灰并划掉；
// 收起时只留一行摘要。
func renderTodoCard(c *ui.Context, s *chatState, m Message, items []todoItem) ui.Element {
	running := s.isThreadRunning(selectedThreadIDSnapshot(s))
	expanded := s.todoCardExpanded(m, running)
	done, total := todoProgress(items)

	return ui.Column(c.Key("msg-todo-"+m.ID)).
		FillWidth().
		MaxWidth(timelineContentMaxWidth(s)).
		Padding(Space4).
		Radius(RadiusLG).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		Gap(Space2).
		Children(func() {
			renderTodoCardHeader(c, s, m, done, total, expanded)
			renderTodoProgressBar(c, m, items)
			if !expanded {
				renderTodoCardSummary(c, m, items, running)
				return
			}
			for i, it := range items {
				ui.Row(c.Key(fmt.Sprintf("todo-item-%s-%d", m.ID, i))).
					Gap(Space3).
					AlignItems(ui.Start).
					Children(func() {
						renderTodoStatusIcon(c, fmt.Sprintf("todo-icon-%s-%d", m.ID, i), it.Status, running)
						textColor := ThemePrimaryText(c)
						if it.Status == "completed" {
							textColor = ThemeSecondaryText(c).Alpha(0.75)
						}
						text := ui.Text(c, it.Text).FontSize(scaledFont(TypeFootnote)).LineHeight(1.4).TextColor(textColor)
						if it.Status == "completed" {
							text.Strikethrough()
						}
					})
			}
		})
}

// renderTodoCardHeader 渲染待办卡片头部：整行可点击折叠，右侧是完成数与折叠箭头。
func renderTodoCardHeader(c *ui.Context, s *chatState, m Message, done, total int, expanded bool) {
	toggleLabel := "展开待办清单"
	chevronTarget := float32(0)
	if expanded {
		toggleLabel = "收起待办清单"
		chevronTarget = 90
	}
	btn := ui.ButtonBase(c.Key("todo-header-" + m.ID)).
		FillWidth().
		Label(toggleLabel).
		Tooltip(toggleLabel).
		Cursor(ui.CursorPointer).
		OnClick(func() {
			s.toggleTodoCard(m.ID, !expanded)
		})
	// 展开/收起箭头：同一个右箭头按顺时针 90 度插值旋转成下箭头
	rotation := btn.AnimateTo("todo-chevron-"+m.ID, chevronTarget, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard})
	btn.Children(func() {
		ui.Row(c.Key("todo-header-row-" + m.ID)).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, IconListTodo()).Size(12, 12).TextColor(ThemeSecondaryText(c))
			ui.Text(c, "待办清单").FontSize(scaledFont(TypeFootnote)).FontWeight(500).TextColor(ThemeSecondaryText(c))
			ui.Spacer(c)
			ui.Text(c, fmt.Sprintf("%d/%d", done, total)).FontSize(scaledFont(TypeCaption)).TextColor(ThemeSecondaryText(c))
			ui.Icon(c, IconChevronRight()).Size(11, 11).TextColor(ThemeSecondaryText(c)).Rotate(rotation)
		})
	})
}

// todoProgressBarHeight 是待办进度条的轨道高度（pt），圆角取完整胶囊。
const todoProgressBarHeight float32 = 2

// renderTodoProgressBar 渲染头部下方的 2px 进度条：轨道用分隔线色、填充用强调色，
// 两段的 Grow 权重就是 todoProgressRatio。
func renderTodoProgressBar(c *ui.Context, m Message, items []todoItem) {
	ProgressBar(c, ProgressBarProps{
		Key: "todo-progress-" + m.ID, Label: "待办进度", Ratio: todoProgressRatio(items),
		Height: todoProgressBarHeight, Track: ThemeDivider(c), Fill: ThemeAccent(c),
	})
}

// renderTodoCardSummary 渲染收起态的一行摘要：状态图标加 todoCollapsedSummary 的文本。
func renderTodoCardSummary(c *ui.Context, m Message, items []todoItem, running bool) {
	status := "pending"
	if it, ok := todoFocusItem(items); ok {
		status = it.Status
	} else if done, total := todoProgress(items); total > 0 && done == total {
		status = "completed"
	}
	ui.Row(c.Key("todo-summary-" + m.ID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
		renderTodoStatusIcon(c, "todo-summary-icon-"+m.ID, status, running)
		ui.Text(c, todoCollapsedSummary(items)).
			FontSize(scaledFont(TypeFootnote)).
			LineHeight(1.4).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
	})
}

// renderTodoStatusIcon 渲染待办项前的状态图标：待处理为空心圆，进行中为强调色圆点，已完成为绿色对勾。
// 进行中的图标只在线程确实还在运行时才转（run 为 true）；线程停下后显示静态圆点，避免假装仍在进行。
func renderTodoStatusIcon(c *ui.Context, key, status string, run bool) {
	switch status {
	case "completed":
		ui.Box(c.Key(key+"-box")).Size(11, 11).Center().Children(func() {
			ui.Icon(c, IconCheck()).Size(11, 11).TextColor(ThemeSuccess(c))
		})
	case "in_progress":
		ui.Box(c.Key(key+"-box")).Size(11, 11).Center().Children(func() {
			if run {
				renderRunningGlyph(c, key+"-run", 9, ThemeAccent(c))
				return
			}
			ui.Box(c.Key(key+"-dot")).Size(9, 9).Radius(RadiusPill).Background(ThemeAccent(c))
		})
	default:
		ui.Box(c.Key(key+"-box")).Size(11, 11).Center().Children(func() {
			ui.Box(c.Key(key+"-ring")).Size(9, 9).Radius(RadiusPill).Border(1.5, ThemeBorder(c))
		})
	}
}

// toolBackgroundRunDuration 计算后台任务从创建到现在的运行时长，并按秒请求下一帧，
// 让时长像运行中的命令一样每秒刷新；时间戳缺失或解析失败时 ok 为 false。
func toolBackgroundRunDuration(c *ui.Context, m Message) (string, bool) {
	start, okStart := parseRFC3339Time(m.CreatedAt)
	if !okStart {
		return "", false
	}
	elapsed := c.Now().Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}
	c.After(time.Second)
	return formatToolRunDuration(elapsed), true
}

// toolBackgroundBadge 返回后台任务胶囊上的状态文本「后台 · 12s」；不是后台任务时 ok 为 false。
// 缺少创建时间时只显示「后台」，时长由下一秒的刷新补上。
func toolBackgroundBadge(c *ui.Context, m Message) (text string, ok bool) {
	if !isToolBackground(m) {
		return "", false
	}
	duration, okStart := toolBackgroundRunDuration(c, m)
	if !okStart {
		return "后台", true
	}
	return "后台 · " + duration, true
}

// toolElapsedText 由工具消息的创建与完成时间计算显示用耗时；缺少时间戳时返回空。
func toolElapsedText(m Message) string {
	start, okStart := parseRFC3339Time(m.CreatedAt)
	end, okEnd := parseRFC3339Time(m.ToolCompletedAt)
	if !okStart || !okEnd {
		return ""
	}
	d := end.Sub(start)
	if d < 0 {
		d = 0
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// isToolRunning 判断工具是否正在执行。
func isToolRunning(m Message) bool {
	return strings.EqualFold(strings.TrimSpace(m.ToolStatus), "running")
}

// isToolBackground 判断工具是否是 provider 托管的后台任务：工具已交给供应商在后台继续跑，
// 消息里没有完成时间（ToolCompletedAt 为空），结束后状态会变回 completed / failed。
func isToolBackground(m Message) bool {
	return strings.EqualFold(strings.TrimSpace(m.ToolStatus), "background")
}

// toolRunStallAfter 是「输出多久没变化算空闲」的阈值。
const toolRunStallAfter = 30 * time.Second

// toolRunStallMinRun 是提示「无输出」所需的最短运行时长：短命命令很快就结束，不必惊扰用户。
const toolRunStallMinRun = 60 * time.Second

// formatToolRunDuration 把运行时长格式化为「12s」「3m05s」「1h02m」；负值按 0 处理。
func formatToolRunDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int((d%time.Minute)/time.Second))
	default:
		return fmt.Sprintf("%dh%02dm", int(d/time.Hour), int((d%time.Hour)/time.Minute))
	}
}

// toolIsRunningCommand 判断工具消息是否是一条仍在运行的命令类工具：
// 状态为 running，或状态未知且还没有完成时间戳；已 completed / failed / blocked 的一律算结束。
// provider 托管的后台任务（background）也按结束态处理：它不归本轮管，
// 卡片里另有实时时长，不应再给出终止按钮或输出停滞徽标。
func toolIsRunningCommand(m Message) bool {
	if !isCommandTool(m) {
		return false
	}
	if isToolRunning(m) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(m.ToolStatus)) {
	case "completed", "failed", "error", "blocked", "cancelled", "canceled", "background":
		return false
	}
	return strings.TrimSpace(m.ToolCompletedAt) == ""
}

// toolRunIdleState 是运行中命令工具的输出观测值：输出长度与最后一次变化的时刻。
type toolRunIdleState struct {
	textLen   int
	changedAt time.Time
}

// observeToolRun 记录这条运行中命令工具本次的输出长度与变化时刻，返回已运行时长与
// 距上次输出变化的时长；时间戳解析不了时 ok 为 false。
// 每帧渲染时调用一次：输出长度变化就刷新时刻，消息不再运行就删掉记录，避免 map 泄漏。
func (s *chatState) observeToolRun(m Message, now time.Time) (elapsed, idleFor time.Duration, ok bool) {
	start, okStart := parseRFC3339Time(m.CreatedAt)
	if !okStart {
		return 0, 0, false
	}
	elapsed = now.Sub(start)
	if elapsed < 0 {
		elapsed = 0
	}

	s.mu.Lock()
	if !toolIsRunningCommand(m) {
		delete(s.toolRunIdle, m.ID)
		s.mu.Unlock()
		return elapsed, 0, true
	}
	if s.toolRunIdle == nil {
		s.toolRunIdle = make(map[string]toolRunIdleState)
	}
	cur := s.toolRunIdle[m.ID]
	if cur.changedAt.IsZero() || cur.textLen != len(m.Text) {
		cur.textLen = len(m.Text)
		cur.changedAt = now
	}
	s.toolRunIdle[m.ID] = cur
	s.mu.Unlock()

	idleFor = now.Sub(cur.changedAt)
	if idleFor < 0 {
		idleFor = 0
	}
	return elapsed, idleFor, true
}

// threadIsBusy 判断指定会话这一轮是否还没结束（会话自己在跑或名下子线程仍在跑）；
// 会话 ID 为空或找不到该会话时按「不在运行」处理。
func (s *chatState) threadIsBusy(threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.IsRunning || th.SwarmBusy
		}
	}
	return false
}

// runningCommandBadge 返回运行中命令工具胶囊上的时长文本与「长时间无输出」标记；
// 不是运行中的命令工具，或时间戳解析不了时返回空文本。
// 当前会话确实还在运行时才按秒取下一帧（与 renderBeeWindow 同一套机制），让时长每秒刷新；
// 历史/中断会话里状态未知的工具不再触发每秒整窗重建。
func (s *chatState) runningCommandBadge(c *ui.Context, m Message) (text string, stalled bool) {
	if !toolIsRunningCommand(m) {
		return "", false
	}
	elapsed, idleFor, ok := s.observeToolRun(m, c.Now())
	if !ok {
		return "", false
	}
	// 状态明确为 running 的工具照旧每秒刷新时长；其余靠兜底判为运行中的工具，
	// 只有当前会话仍在跑（IsRunning 或 SwarmBusy）时才继续每秒重建。
	if isToolRunning(m) || s.threadIsBusy(selectedThreadIDSnapshot(s)) {
		c.After(time.Second)
	}
	text = formatToolRunDuration(elapsed)
	if elapsed >= toolRunStallMinRun && idleFor >= toolRunStallAfter {
		return text + " · 无输出", true
	}
	return text, false
}

// toolCallRowLabel 把标题拼成无障碍 Label（如 "读取 timeline_rows.go :120–525"），
// 屏幕阅读器与测试都靠它定位整行。
func toolCallRowLabel(verb, target, detail string) string {
	label := strings.TrimSpace(strings.Join([]string{verb, target, detail}, " "))
	if label == "" {
		return "工具调用"
	}
	return label
}

// toolChipHeight 是动作胶囊的高度，圆角取其一半。
const toolChipHeight float32 = 22

// toolBeatPadLeft 是节拍胶囊、折叠预览和详情区相对组头的缩进距离。
const toolBeatPadLeft float32 = 12

// renderSingleToolItem 渲染单条工具调用：一个左对齐的动作胶囊，点开后下方展开详情（按 2.4 节）。
func renderSingleToolItem(c *ui.Context, s *chatState, m Message) ui.Element {
	// 待办清单类工具（TodoWrite / update_plan）渲染成带状态图标的卡片
	if items, ok := todoItemsFromMessage(m); ok {
		return renderTodoCard(c, s, m, items)
	}

	open := s.toolMessageExpanded(m)

	// 单条工具没有节拍同伴，传自己即可满足“同一节拍只展开一个”的约定
	siblings := []Message{m}
	return ui.Column(c.Key("msg-tool-" + m.ID)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Gap(Space1).Children(func() {
		ui.Row(c.Key("tool-chips-" + m.ID)).
			Gap(Space2).
			Wrap().
			AlignItems(ui.Center).
			Children(func() {
				renderToolChip(c, s, m, siblings)
			})
		if open {
			renderToolDetail(c, s, m)
		}
	})
}

// toolMessageExpanded 默认展开等待审批的入参，用户选择优先。
func (s *chatState) toolMessageExpanded(m Message) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	open, explicit := s.expandedTool[m.ID]
	return open || !explicit && toolMessageState(m) == ToolPending
}

// toggleToolChip 展开或收起一个动作胶囊的详情：同一节拍同一时刻只展开一个，
// 点开的胶囊同时收起同节拍里的其它胶囊；编辑类工具展开时直接带上这次调用的改动。
func (s *chatState) toggleToolChip(m Message, beatTools []Message) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	previous, explicit := s.expandedTool[m.ID]
	open := !(previous || !explicit && toolMessageState(m) == ToolPending)
	for _, other := range beatTools {
		if other.ID != m.ID {
			s.expandedTool[other.ID] = false
		}
	}
	s.expandedTool[m.ID] = open
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()

	// 编辑类工具：展开胶囊时直接显示这次调用的改动，收起时一并关掉
	// （走 toggleToolDiff 以复用它在重新展开时清掉失败缓存的逻辑）。
	if isFileEditTool(m) {
		if open != s.isToolDiffOpen(m.ID) {
			s.toggleToolDiff(m)
			return
		}
	}
	s.invalidate()
}

// hideToolDetail 收起某个工具的详情区与内嵌改动。
func (s *chatState) hideToolDetail(m Message) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	s.expandedTool[m.ID] = false
	if s.toolDiff.open != nil {
		s.toolDiff.open[m.ID] = false
	}
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

// toolChipShortTitle 生成胶囊里的短标题（基于 toolCallTitle）：命令优先用输入里的 description
// （自然语言意图，截断到 40 个字符），没有 description 时从命令正文里语义提取出主命令
// （截断到 48 个字符）；读取与编辑显示文件名，搜索显示 pattern（截断到 24 个字符），
// 其余工具显示工具名加对象。
func toolChipShortTitle(m Message, verb, target string) string {
	category := classifyToolCategory(toolMessageName(m))
	if parseCommandToolInfo(m).IsCommand || category == toolCategoryCommand {
		if desc := toolChipDescription(m); desc != "" {
			return truncateRunes(desc, 40)
		}
		return truncateRunes(toolChipCommandShort(target), 48)
	}
	switch category {
	case toolCategoryRead, toolCategoryEdit:
		return target
	case toolCategorySearch:
		return truncateRunes(strings.Trim(target, `"`), 24)
	}
	return strings.TrimSpace(strings.TrimSpace(verb) + " " + target)
}

// toolChipDescription 取命令工具输入里的 description 字段（各家 agent 用它写一句自然语言意图），
// 规整为单行；没有该字段或内容为空时返回空串。
func toolChipDescription(m Message) string {
	desc, ok := toolFieldString(toolCallFields(m), "description")
	if !ok {
		return ""
	}
	return strings.Join(strings.Fields(desc), " ")
}

// toolChipCommandShort 从命令正文里语义提取最能说明意图的一段，用于胶囊标题：
// 先取首行并剥掉 bash -c / sh -lc 这类包装，再按 && || ; | 切段跳过 cd、export 之类的噪声段，
// for/while 循环取循环体里的主命令（前缀 for: / while:），然后去掉 rtk/env/sudo 等包装前缀
// 与 KEY=VALUE 赋值，最后保留最多 4 个 token（其中的路径换成文件名）。
// 提取不到时退回旧行为：首行的前两个 token。
func toolChipCommandShort(cmd string) string {
	line := cmd
	if idx := strings.IndexByte(line, '\n'); idx != -1 {
		line = line[:idx]
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}

	segments := toolChipCommandSegments(toolChipShellScript(line))
	main, prefix := "", ""
	for _, seg := range segments {
		if toolChipNoiseSegment(seg) {
			continue
		}
		main = seg
		if strings.HasPrefix(seg, "for ") || strings.HasPrefix(seg, "while ") {
			prefix = "for: "
			if strings.HasPrefix(seg, "while ") {
				prefix = "while: "
			}
			if body := toolChipLoopBody(segments); body != "" {
				main = body
			}
		}
		break
	}

	tokens := toolChipUnwrapTokens(strings.Fields(main))
	if len(tokens) == 0 {
		return toolChipCommandBeeShort(line)
	}
	if len(tokens) > 4 {
		tokens = tokens[:4]
	}
	toolChipBaseNames(tokens)
	return prefix + strings.Join(tokens, " ")
}

// toolChipShellScript 剥掉 `bash -c '...'`、`sh -lc "..."` 这类包装，返回引号内的脚本；
// 不是这种包装时原样返回。只去掉最外层引号，不解析转义。
func toolChipShellScript(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) < 3 {
		return cmd
	}
	switch fields[0] {
	case "bash", "sh", "zsh":
	default:
		return cmd
	}
	scriptAt := -1
	for i := 1; i < len(fields); i++ {
		if strings.HasPrefix(fields[i], "-") && strings.HasSuffix(fields[i], "c") {
			scriptAt = i
			break
		}
	}
	if scriptAt < 0 || scriptAt == len(fields)-1 {
		return cmd
	}
	script := strings.Join(fields[scriptAt+1:], " ")
	if len(script) >= 2 {
		first, last := script[0], script[len(script)-1]
		if (first == '\'' && last == '\'') || (first == '"' && last == '"') {
			script = script[1 : len(script)-1]
		}
	}
	return strings.TrimSpace(script)
}

// toolChipCommandSegments 按顶层分隔符 && || ; | 把脚本切成命令段（不处理引号内的分隔符）。
func toolChipCommandSegments(script string) []string {
	replacer := strings.NewReplacer("&&", "\n", "||", "\n", ";", "\n", "|", "\n")
	var segments []string
	for _, seg := range strings.Split(replacer.Replace(script), "\n") {
		if seg = strings.TrimSpace(seg); seg != "" {
			segments = append(segments, seg)
		}
	}
	return segments
}

// toolChipNoiseSegment 判断命令段是否只是噪声（空段，或 cd/export/set -x 之类的准备动作）。
func toolChipNoiseSegment(seg string) bool {
	seg = strings.TrimSpace(seg)
	if seg == "" {
		return true
	}
	for _, p := range []string{"cd ", "pushd ", "export ", "set -", "source ", ". "} {
		if strings.HasPrefix(seg, p) {
			return true
		}
	}
	return false
}

// toolChipLoopBody 返回 for/while 循环体里的第一个非噪声命令段，找不到时返回空串。
func toolChipLoopBody(segments []string) string {
	for i, seg := range segments {
		body := ""
		switch {
		case seg == "do":
		case strings.HasPrefix(seg, "do "):
			body = strings.TrimSpace(strings.TrimPrefix(seg, "do "))
		default:
			continue
		}
		if body != "" && !toolChipNoiseSegment(body) {
			return body
		}
		for _, next := range segments[i+1:] {
			if next == "done" {
				return ""
			}
			if !toolChipNoiseSegment(next) {
				return next
			}
		}
		return ""
	}
	return ""
}

// toolChipUnwrapTokens 反复去掉开头的包装前缀 token（rtk/env/time/sudo 等）与 KEY=VALUE 环境变量赋值。
func toolChipUnwrapTokens(fields []string) []string {
	for len(fields) > 0 {
		switch fields[0] {
		case "rtk", "env", "time", "sudo", "nohup", "exec":
			fields = fields[1:]
			continue
		}
		if toolChipEnvAssignment(fields[0]) {
			fields = fields[1:]
			continue
		}
		break
	}
	return fields
}

// toolChipEnvAssignment 判断 token 是否是 KEY=VALUE 形式的环境变量赋值。
func toolChipEnvAssignment(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 || strings.HasPrefix(tok, "-") {
		return false
	}
	for _, r := range tok[:eq] {
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// toolChipBaseNames 把 token 列表里含路径分隔符的路径换成文件名（原地修改）。
func toolChipBaseNames(tokens []string) {
	for i, tok := range tokens {
		if !strings.HasPrefix(tok, "-") && strings.ContainsAny(tok, `/\`) {
			tokens[i] = filepath.Base(tok)
		}
	}
}

// toolChipCommandBeeShort 是旧行为：取命令首行的前两个 token，并把其中的路径换成文件名。
func toolChipCommandBeeShort(line string) string {
	fields := strings.Fields(line)
	if len(fields) > 2 {
		fields = fields[:2]
	}
	toolChipBaseNames(fields)
	return strings.Join(fields, " ")
}

// truncateRunes 把文本截断到最多 n 个字符，超出时补省略号。
func truncateRunes(text string, n int) string {
	runes := []rune(text)
	if len(runes) <= n {
		return text
	}
	return string(runes[:n]) + "…"
}

// toolMessageState 将消息状态映射为统一的工具状态。
func toolMessageState(m Message) ToolState {
	switch {
	case isToolFailed(m) || isToolBlockedByHook(m):
		return ToolError
	case strings.EqualFold(strings.TrimSpace(m.ToolStatus), "pending"):
		return ToolPending
	case isToolRunning(m) || isToolBackground(m):
		return ToolRunning
	default:
		return ToolDone
	}
}

const toolPendingBorderOpacity float32 = 0.10

type toolMarkerShape int

const (
	toolMarkerHollow toolMarkerShape = iota
	toolMarkerSpinner
	toolMarkerCheck
	toolMarkerAlert
)

func toolStateMarker(state ToolState) (toolMarkerShape, Tone) {
	switch state {
	case ToolRunning:
		return toolMarkerSpinner, ToneAccent
	case ToolDone:
		return toolMarkerCheck, ToneSuccess
	case ToolError:
		return toolMarkerAlert, ToneDanger
	default:
		return toolMarkerHollow, ToneNeutral
	}
}

// renderToolChipStatus 渲染工具状态形状及语义色。
func renderToolChipStatus(c *ui.Context, m Message) {
	shape, tone := toolStateMarker(toolMessageState(m))
	color := ThemeTertiaryText(c)
	switch tone {
	case ToneAccent:
		color = ThemeAccent(c)
	case ToneSuccess:
		color = ThemeSuccess(c)
	case ToneDanger:
		color = ThemeDanger(c)
	}
	switch shape {
	case toolMarkerSpinner:
		LoadingIndicator(c, nil, "", IconSM, color)
	case toolMarkerHollow:
		ui.Box(c).Size(IconSM, IconSM).Radius(RadiusPill).Border(Space1/4, color)
	case toolMarkerCheck:
		ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(color)
	case toolMarkerAlert:
		ui.Icon(c, IconAlertTriangle()).Size(IconSM, IconSM).TextColor(color)
	}
}

// renderToolChipCategoryMark 渲染胶囊里的类别标记：命令用等宽的 $，编辑用笔图标，读取用眼睛图标，
// 搜索用放大镜图标，其余工具用扳手图标。
func renderToolChipCategoryMark(c *ui.Context, category toolCategory, isCommand bool, fontFam string) {
	if isCommand {
		ui.Text(c, "$").Font(fontFam).FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
		return
	}
	icon := IconWrench()
	switch category {
	case toolCategoryEdit:
		icon = IconPencilLine()
	case toolCategoryRead:
		icon = IconEye()
	case toolCategorySearch:
		icon = IconGlobe()
	}
	ui.Icon(c, icon).Size(11, 11).TextColor(ThemeSecondaryText(c))
}

// toolVerbShimmerWidth 是动词扫光条自身的宽度（DIP）。
const toolVerbShimmerWidth float32 = 24

// toolShimmerBandLabel 是扫光条的无障碍名，仅用于测试与调试定位这条装饰层。
const toolShimmerBandLabel = "运行中的工具扫光"

// renderToolChipTitle 渲染工具胶囊的标题：运行中时在标题上扫一道高光。扫光动画与常量
// 统一交给 renderShimmerTitle，与后台任务卡、运行中的回合头共用同一套实现；
// 离开 running / background 状态后高光消失。
func renderToolChipTitle(c *ui.Context, m Message, fn func() ui.Element) {
	renderShimmerTitle(c, "tool-shimmer-"+m.ID, toolShimmerBandLabel, isToolRunning(m) || isToolBackground(m), fn)
}

// renderToolChip 渲染一个动作胶囊：状态字形、类别标记、短标题、diff 统计与耗时，点击展开或收起这个工具的详情。
// beatTools 是同一节拍里的全部工具，用于保证同一节拍同时只展开一个胶囊。
// 编辑类工具的胶囊本身就是这次改动的开关，它的 Label 用「查看改动 <文件名>」/「收起改动 <文件名>」，
// 其余工具用完整标题（toolCallRowLabel），测试与屏幕阅读器都按它定位。
func renderToolChip(c *ui.Context, s *chatState, m Message, beatTools []Message) {
	verb, target, detail := toolCallTitle(m)
	// 动词按时态显示：运行中「正在读取」、完成后「已读取」；命令行「$」与未知工具名原样保留。
	verb = toolCallVerb(verb, m)
	cmdInfo := parseCommandToolInfo(m)
	category := classifyToolCategory(toolMessageName(m))
	adds, dels := toolCallDiffStat(m)
	duration := cmdInfo.Duration
	if duration == "" {
		duration = toolElapsedText(m)
	}
	// 运行中的命令工具改显示「已运行多久」；长时间没有新输出时改警告色并标注「无输出」。
	runStalled := false
	if toolText, stalled := s.runningCommandBadge(c, m); toolText != "" {
		duration, runStalled = toolText, stalled
	}
	// provider 托管的后台任务还在跑，耗时改成「后台 · 已运行时长」，每秒刷新。
	if toolText, ok := toolBackgroundBadge(c, m); ok {
		duration = toolText
	}

	open := s.toolMessageExpanded(m)
	failed := isToolFailed(m)

	label := toolCallRowLabel(verb, target, detail)
	if isFileEditTool(m) {
		label = "查看改动"
		if open {
			label = "收起改动"
		}
		if target != "" {
			label += " " + target
		}
	}

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)

	chip := ui.ButtonBase(c.Key("tool-chip-"+m.ID)).
		Gap(Space2).
		Height(toolChipHeight).
		Radius(toolChipHeight/2).
		Padding(0, Space3).
		Cursor(ui.CursorPointer).
		Label(label).
		Tooltip(toolCallRowLabel(verb, target, detail)).
		OnClick(func() {
			s.toggleToolChip(m, beatTools)
		})
	// 底色按状态分层：常态最淡，悬停加深，详情已展开时最深；失败的工具再加一圈红描边
	switch {
	case open:
		chip.Background(ThemeSurfaceRaised(c))
	case chip.Hovered():
		chip.Background(ThemeSecondaryText(c).Alpha(0.14))
	default:
		chip.Background(ThemeSurfaceCard(c))
	}
	if failed {
		chip.Border(1, ThemeDanger(c))
	} else if toolMessageState(m) == ToolPending {
		chip.Border(1, ThemeOverlay(c, toolPendingBorderOpacity))
	}

	chip.Children(func() {
		renderToolChipStatus(c, m)
		renderToolChipCategoryMark(c, category, cmdInfo.IsCommand, fontFam)
		// 运行中的工具在标题上扫一道高光，动词时态也在这里变成「正在读取」这类现在进行时。
		renderToolChipTitle(c, m, func() ui.Element {
			title := ui.Text(c, toolChipShortTitle(m, verb, target)).
				FontSize(scaledFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c)).
				SingleLine()
			if cmdInfo.IsCommand && toolChipDescription(m) == "" {
				// 命令文本用等宽字体，和终端的观感保持一致；有 description 时是自然语言，用默认字体
				title.Font(fontFam)
			}
			return title
		})
		if adds > 0 || dels > 0 {
			ui.Row(c.Key("tool-chip-diff-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
				if adds > 0 {
					ui.Text(c, fmt.Sprintf("+%d", adds)).
						Font(fontFam).
						FontSize(scaledFont(TypeCaption)).
						FontWeight(500).
						TextColor(ThemeSuccess(c))
				}
				if dels > 0 {
					ui.Text(c, fmt.Sprintf("−%d", dels)).
						Font(fontFam).
						FontSize(scaledFont(TypeCaption)).
						FontWeight(500).
						TextColor(ThemeDanger(c))
				}
			})
		}
		if duration != "" {
			color := ThemeSecondaryText(c).Alpha(0.85)
			if runStalled {
				color = ThemeWarning(c)
			}
			ui.Text(c, duration).FontSize(scaledFont(TypeCaption)).TextColor(color)
		}
	})
}

const foldRunThreshold = 300

// toolOutputFadeHeight 是工具输出折叠遮罩高度。 // token-exempt: 渐隐覆盖固定的预览边缘
const toolOutputFadeHeight float32 = 28

// renderToolDetail 渲染一个工具调用的详情区：左侧缩进 12pt 的圆角浅底卡片，
// 编辑类工具先在顶部给「收起改动」按钮与这次调用的行级 diff，下面是等宽小号字体的输出正文
// （超过 300 字符时默认折叠，用户可就地展开全文）。
// 工具组里的胶囊详情与未分组的单条工具共用这套渲染。
func renderToolDetail(c *ui.Context, s *chatState, m Message) {
	lines := strings.Split(m.Text, "\n")
	// 被 hook 拦截/改道的结果末尾会跟一行 [failed]，它只是改道的标记，详情区不显示这一行
	blockedByHook := isToolBlockedByHook(m)
	if blockedByHook && len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "[failed]" {
		lines = lines[:len(lines)-1]
	}
	diffOpen := isFileEditTool(m) && s.isToolDiffOpen(m.ID)

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)
	codeSz := codeFontSize()

	// 详情区卡片的内边距跟界面密度走；outputMax 是正文滚动区的最大高度，紧凑档更矮，避免占满时间线。
	dens := currentDensity(c, s)
	detailPad := dens.PadX
	outputMax := toolOutputMaxHeight
	if dens.isCompact() {
		outputMax = toolOutputMaxHeightCompact
	}

	ui.Column(c.Key("tool-detail-wrap-"+m.ID)).
		FillWidth().
		Margin(0, 0, 0, toolBeatPadLeft).
		Children(func() {
			ui.Column(c.Key("tool-detail-"+m.ID)).
				FillWidth().
				Padding(detailPad).
				Radius(RadiusMD).
				Background(ThemeHover(c)).
				Border(1, ThemeBorder(c)).
				Gap(Space2).
				Children(func() {
					if toolMessageState(m) == ToolPending && strings.TrimSpace(m.ToolInput) != "" {
						ui.Text(c, m.ToolInput).Font(fontFam).FontSize(scaledFont(codeSz)).TextColor(ThemeSecondaryText(c)).Selectable()
					}
					// 运行中的命令：顶部给一个「终止此命令」次级按钮，只杀这一条命令，不中断本轮。
					if toolIsRunningCommand(m) {
						renderKillCommandButton(c, s, m)
					}
					// 编辑类工具：胶囊展开后直接看到这次改动的行级 diff，顶部保留收起入口
					if diffOpen {
						ui.ButtonBase(c.Key("tool-detail-diff-toggle-"+m.ID)).
							Label("收起改动").
							Tooltip("收起改动").
							Padding(0, Space0).
							Cursor(ui.CursorPointer).
							OnClick(func() {
								s.hideToolDetail(m)
							}).
							Children(func() {
								ui.Text(c, "收起改动").
									FontSize(scaledFont(TypeCaption)).
									TextColor(ThemeAccent(c))
							})
						renderInlineToolDiff(c, s, m)
					}

					// 输出正文：等宽小号字，超长时保留前 300 个字符；
					// 正文区限高后可滚动，运行中默认贴底跟随新输出，用户上滚则停止跟随。
					if strings.TrimSpace(m.Text) != "" {
						// 拦截标记那行已从 lines 去掉，正文按处理后的行拼回
						text := strings.Join(lines, "\n")
						foldKey := "tool-output-fold-" + m.ID
						s.mu.Lock()
						expanded := s.expandedTool[foldKey]
						s.mu.Unlock()
						runes := []rune(text)
						folded := len(runes) > foldRunThreshold && !expanded
						preview := text
						if folded {
							preview = string(runes[:foldRunThreshold])
						}
						st := s.toolOutputScrollStateFor(m.ID)
						if !folded && isToolRunning(m) && (st.MaxY <= 0 || st.Y >= st.MaxY-toolOutputFollowThreshold) {
							st.Y = math.MaxFloat32
						}
						ui.Box(c.Key("tool-output-wrap-" + m.ID)).FillWidth().Children(func() {
							ui.Scroll(c.Key("tool-output-scroll-" + m.ID)).FillWidth().MaxHeight(outputMax).TrackScroll(st).Children(func() {
								ui.Text(c, preview).Font(fontFam).FontSize(scaledFont(codeSz)).LineHeight(1.4).TextColor(ThemeSecondaryText(c)).Selectable()
							})
							if folded {
								color := ThemeHover(c)
								ui.Box(c.Key("tool-output-fade-" + m.ID)).Absolute().Left(0).Right(0).Bottom(0).Height(toolOutputFadeHeight).LinearGradient(ui.LinearGradient{Angle: 180, From: color.Alpha(0), To: color}).PassThrough()
							}
						})
						if folded {
							Capsule(c, CapsuleProps{Text: "展开全部", Interactive: true, OnClick: func() {
								s.mu.Lock()
								if s.expandedTool == nil {
									s.expandedTool = make(map[string]bool)
								}
								s.expandedTool[foldKey] = true
								s.expandedRev++
								s.mu.Unlock()
								s.holdChatPlace()
								s.invalidate()
							}})
						}
					}
				})
		})
}

// renderKillCommandButton 渲染运行中命令上的「终止此命令」次级按钮：
// 请求进行中禁用并显示「正在终止…」，避免重复点击。
func renderKillCommandButton(c *ui.Context, s *chatState, m Message) {
	s.mu.Lock()
	killing := s.killingTools[m.ID]
	s.mu.Unlock()

	label := "终止此命令"
	fg := ThemeDanger(c)
	if killing {
		label = "正在终止…"
		fg = ThemeSecondaryText(c)
	}

	ui.ButtonBase(c.Key("tool-kill-"+m.ID)).
		Label(label).
		Tooltip("只终止这条命令，不中断本轮的其它执行").
		Disabled(killing).
		Padding(Space0, Space3).
		Radius(RadiusSM).
		Cursor(ui.CursorPointer).
		Background(ThemeSurface(c)).
		Border(1, ThemeBorder(c)).
		OnClick(func() {
			s.killRunningCommand(m.ID)
		}).
		Children(func() {
			ui.Row(c.Key("tool-kill-row-" + m.ID)).Gap(Space1).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconCircleStop()).Size(9, 9).TextColor(fg)
				ui.Text(c, label).FontSize(scaledFont(TypeCaption)).TextColor(fg)
			})
		})
}

// toolCallDiffStat 返回「这一次」工具调用自身的增删行数，供工具行右侧的差异角标使用。
//
// 只看这次调用的输入（编辑的 old/new 文本、写入内容、补丁正文），或这次调用输出里自带的
// unified diff。取不到就返回 0（不显示），绝不回退到整轮汇总：整轮 diff 是之前所有 edit 行
// 都显示同一对数字（如 +596 −106）的根因——同一文件在一轮里被改多次时，每行拿到的都是
// 该文件在本轮的累计改动；路径提取失败时（例如输入字段写作 "Path"）更是整轮所有文件的合计。
func toolCallDiffStat(m Message) (adds, dels int) {
	if !isFileEditTool(m) {
		return 0, 0
	}
	if raw := toolEmbeddedDiff(m.Text); raw != "" {
		if files, err := git.ParseUnifiedDiff([]byte(raw)); err == nil && len(files) > 0 {
			return diffStatTotals(files)
		}
	}
	fields := toolCallFields(m)
	if len(fields) == 0 {
		return 0, 0
	}
	if patch, ok := toolFieldString(fields, "patch", "input", "diff", "patch_text"); ok {
		if a, d, ok := patchDiffStat(patch); ok {
			return a, d
		}
	}
	oldText, hasOld := toolFieldString(fields, toolCallOldTextKeys...)
	newText, hasNew := toolFieldString(fields, toolCallNewTextKeys...)
	switch {
	case hasOld && hasNew:
		return lineDiffStat(oldText, newText)
	case hasNew:
		return len(splitDiffLines(newText)), 0
	case hasOld:
		return 0, len(splitDiffLines(oldText))
	}
	return 0, 0
}

// toolCallOldTextKeys 与 toolCallNewTextKeys 是编辑类工具输入里旧/新正文的候选字段名。
var (
	toolCallOldTextKeys = []string{
		"old_string", "oldstring", "oldtext", "old_text", "old_str", "oldstr", "old",
		"original", "target_content", "targetcontent",
	}
	toolCallNewTextKeys = []string{
		"new_string", "newstring", "newtext", "new_text", "new_str", "newstr", "new",
		"replacement", "replacementcontent", "replacement_content", "replacement_text",
		"new_content", "newcontent", "content", "file_text",
	}
)

// splitDiffLines 把文本切成按行的列表：统一 CRLF，忽略结尾换行造成的空行；空文本返回 nil。
func splitDiffLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// lineDiffStat 用「去掉公共前缀与后缀行」的近似算法统计一次改动的增删行数：
// 中间不重合的行按纯新增/纯删除计。对单次编辑足够精确，且不需要完整 LCS。
func lineDiffStat(oldText, newText string) (adds, dels int) {
	oldLines := splitDiffLines(oldText)
	newLines := splitDiffLines(newText)
	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	return len(newLines) - prefix - suffix, len(oldLines) - prefix - suffix
}

// patchDiffStat 从补丁文本（apply_patch 风格或 unified diff）统计增删行数；没有可识别的改动行时 ok 为 false。
func patchDiffStat(text string) (adds, dels int, ok bool) {
	for _, line := range splitDiffLines(text) {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			adds++
		case strings.HasPrefix(line, "-"):
			dels++
		}
	}
	return adds, dels, adds > 0 || dels > 0
}

// diffStatTotals 汇总一组文件改动的增删行数。
func diffStatTotals(files []git.FileDiff) (adds, dels int) {
	for _, file := range files {
		for _, hunk := range file.Hunks {
			for _, line := range hunk.Lines {
				switch line.Kind {
				case '+':
					adds++
				case '-':
					dels++
				}
			}
		}
	}
	return adds, dels
}

// toolCategory 是工具组摘要中的工具类别。
type toolCategory int

const (
	toolCategoryOther toolCategory = iota
	toolCategoryCommand
	toolCategoryEdit
	toolCategoryRead
	toolCategorySearch
)

// readToolNames 与 searchToolNames 为读取与搜索类工具名（已转小写并去掉 _ 与 -）。
var readToolNames = map[string]bool{
	"read":              true,
	"readfile":          true,
	"fileread":          true,
	"readfiles":         true,
	"readtextfile":      true,
	"readmultiplefiles": true,
	"readfilecontent":   true,
	"getfilecontents":   true,
	"getfilecontent":    true,
	"view":              true,
	"viewfile":          true,
	"openfile":          true,
	"cat":               true,
}

var searchToolNames = map[string]bool{
	"grep":           true,
	"rg":             true,
	"ripgrep":        true,
	"grepsearch":     true,
	"search":         true,
	"searchfiles":    true,
	"filesearch":     true,
	"find":           true,
	"findfiles":      true,
	"glob":           true,
	"codebasesearch": true,
}

// toolNameKeyStripper 是 toolNameKey 的固定替换器：每帧要为每条工具消息归类，
// 现造一个 strings.Replacer 会连索引一起重建一次。
var toolNameKeyStripper = strings.NewReplacer("_", "", "-", "")

// toolNameKey 规整工具名：小写、去掉 MCP 前缀与命名空间、去掉 _ 与 -，用于归类。
func toolNameKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if idx := strings.LastIndex(key, "__"); idx != -1 {
		key = key[idx+2:]
	}
	if idx := strings.LastIndexAny(key, "./:"); idx != -1 {
		key = key[idx+1:]
	}
	key = toolNameKeyStripper.Replace(key)
	switch key {
	case "readfile":
		return "read"
	case "writefile":
		return "write"
	case "editfile":
		return "edit"
	default:
		return key
	}
}

// classifyToolCategory 按工具名归类，供工具组摘要统计使用。
func classifyToolCategory(name string) toolCategory {
	key := toolNameKey(name)
	switch {
	case key == "":
		return toolCategoryOther
	case readToolNames[key]:
		return toolCategoryRead
	case searchToolNames[key]:
		return toolCategorySearch
	case fileEditToolNames[key]:
		return toolCategoryEdit
	case commandToolNames[key]:
		return toolCategoryCommand
	default:
		return toolCategoryOther
	}
}

// toolGroupCounts 是工具组摘要中各类别的出现次数，供组头的图标计数与文字摘要共用。
type toolGroupCounts struct {
	thoughts, commands, edits, reads, searches, others, total int
}

// countToolGroupCategories 统计一组工具调用（含思考行）里各类别的出现次数。
func countToolGroupCategories(items []Message) toolGroupCounts {
	var counts toolGroupCounts
	for _, m := range items {
		counts.total++
		switch {
		case isReasoningMessage(m):
			counts.thoughts++
		case parseCommandToolInfo(m).IsCommand:
			counts.commands++
		case isFileEditTool(m):
			counts.edits++
		default:
			switch classifyToolCategory(toolMessageName(m)) {
			case toolCategoryCommand:
				counts.commands++
			case toolCategoryEdit:
				counts.edits++
			case toolCategoryRead:
				counts.reads++
			case toolCategorySearch:
				counts.searches++
			default:
				counts.others++
			}
		}
	}
	return counts
}

// toolGroupSummary 按类别统计一组工具调用（含思考行）并拼出中文摘要，计数为 0 的类别省略。
func toolGroupSummary(items []Message) string {
	return toolGroupSummaryText(countToolGroupCategories(items))
}

// toolGroupCategoryNames 只显示组内出现的类别。
func toolGroupCategoryNames(counts toolGroupCounts) string {
	var names []string
	if counts.thoughts > 0 {
		names = append(names, "思考")
	}
	if counts.commands > 0 {
		names = append(names, "命令")
	}
	if counts.edits > 0 {
		names = append(names, "编辑")
	}
	if counts.reads > 0 {
		names = append(names, "读取")
	}
	if counts.searches > 0 {
		names = append(names, "搜索")
	}
	if counts.others > 0 {
		names = append(names, "工具")
	}
	if len(names) == 0 {
		return "工具"
	}
	return strings.Join(names, " · ")
}

// toolGroupSummaryText 把分类计数拼成中文摘要，计数为 0 的类别省略。
func toolGroupSummaryText(counts toolGroupCounts) string {
	parts := make([]string, 0, 6)
	if counts.thoughts > 0 {
		parts = append(parts, fmt.Sprintf("思考 %d 次", counts.thoughts))
	}
	if counts.commands > 0 {
		parts = append(parts, fmt.Sprintf("运行 %d 条命令", counts.commands))
	}
	if counts.edits > 0 {
		parts = append(parts, fmt.Sprintf("编辑 %d 个文件", counts.edits))
	}
	if counts.reads > 0 {
		parts = append(parts, fmt.Sprintf("读取 %d 个文件", counts.reads))
	}
	if counts.searches > 0 {
		parts = append(parts, fmt.Sprintf("搜索 %d 次", counts.searches))
	}
	if counts.others > 0 {
		parts = append(parts, fmt.Sprintf("调用 %d 个工具", counts.others))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("已运行 %d 个工具", counts.total)
	}
	return strings.Join(parts, " · ")
}

// toolCallPathKeys 是工具输入中文件路径字段的候选名（统一做不区分大小写的匹配）。
var toolCallPathKeys = []string{
	"path", "file_path", "filepath", "target_file", "notebook_path", "absolutepath", "file",
}

// toolCallPatternKeys / toolCallSearchPathKeys 是搜索类工具的搜索词与搜索范围字段。
var (
	toolCallPatternKeys    = []string{"pattern", "query", "regex", "glob", "search", "search_term"}
	toolCallSearchPathKeys = []string{"path", "dir", "directory", "root", "cwd", "folder"}
)

// createToolNames 为「新建文件」类工具名（已转小写并去掉 _ 与 -），其余修改类工具算「编辑」。
var createToolNames = map[string]bool{
	"write":            true,
	"writefile":        true,
	"createfile":       true,
	"writetofile":      true,
	"newfile":          true,
	"create":           true,
	"addfile":          true,
	"writefiletext":    true,
	"writefilecontent": true,
}

// toolCallFields 返回工具调用的输入字段：优先解析 m.ToolInput 的完整 JSON，
// 拿不到（旧消息或输入被截断）时回退到从 m.Text 首行提取。
func toolCallFields(m Message) map[string]any {
	if fields := toolInputFields(m); len(fields) > 0 {
		return fields
	}
	return toolTextFieldsCached(m)
}

// toolTextFieldsCached 从 m.Text 首行提取回退字段，结果按消息 ID 缓存；
// 结果只取决于 m.Text，因此单帧重复调用不再重复解码 JSON。
// 返回的 map 只读，调用方不得修改；m.ID 为空时不走缓存。
func toolTextFieldsCached(m Message) map[string]any {
	if m.ID == "" {
		return toolDisplayFieldsFromText(m.Text)
	}
	toolParseCacheMu.Lock()
	e, hit := toolParseCacheLookup(m)
	if hit && e.hasTextFields {
		fields := e.textFields
		toolParseCacheMu.Unlock()
		return fields
	}
	toolParseCacheMu.Unlock()

	// 解析在锁外进行，避免与其它缓存路径嵌套加锁。
	fields := toolDisplayFieldsFromText(m.Text)

	toolParseCacheMu.Lock()
	if hit && e.matches(m) {
		e.textFields, e.hasTextFields = fields, true
	} else {
		// 失配（Text 或 ToolInput 变了）时新建条目覆盖；输入字段只取决于 ToolInput，
		// 它没变就沿用旧条目上已算好的结果，不丢也不重算。
		rebuilt := newToolParseCacheEntry(m)
		if hit && e.ToolInput == m.ToolInput {
			rebuilt.hasFields, rebuilt.fields = e.hasFields, e.fields
		}
		rebuilt.textFields, rebuilt.hasTextFields = fields, true
		toolParseCachePut(m, rebuilt)
	}
	toolParseCacheMu.Unlock()
	return fields
}

// toolFieldString 不区分大小写地取输入字段的第一个非空字符串值：
// 各家工具的字段名大小写并不统一（Path/path/filePath）。
func toolFieldString(fields map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		for k, v := range fields {
			if !strings.EqualFold(k, key) {
				continue
			}
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s), true
			}
		}
	}
	return "", false
}

// toolFieldInt 不区分大小写地取输入字段的整数值：JSON 数字与数字字符串都接受。
func toolFieldInt(fields map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		for k, v := range fields {
			if !strings.EqualFold(k, key) {
				continue
			}
			switch n := v.(type) {
			case float64:
				return int(n), true
			case string:
				if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
					return i, true
				}
			}
		}
	}
	return 0, false
}

// toolCallFirstStringValue 取输入里第一个非空字符串值（按字段名排序，保证每次渲染结果一致）。
func toolCallFirstStringValue(fields map[string]any) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if s, ok := fields[k].(string); ok && strings.TrimSpace(s) != "" {
			return truncateCommandLine(s, 80)
		}
	}
	return ""
}

// toolCallPath 返回工具调用涉及的文件路径（完整路径，供 tooltip 与目录展示）；不涉及文件时返回空。
func toolCallPath(m Message) string {
	if p, ok := toolFieldString(toolCallFields(m), toolCallPathKeys...); ok {
		return p
	}
	// apply_patch 这类把路径写在补丁头或补丁正文里的工具交给提取逻辑
	if paths := toolCallEditPaths(m); len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// 工具补丁正文里的三种路径行：unified diff 的 +++ b/<path> 与 --- a/<path>、
// diff --git a/<p> b/<p>、apply_patch 的 *** Update/Add/Delete File: <path>。
var (
	toolPatchFileLineRe = regexp.MustCompile(`(?m)^(?:\+\+\+ b/|--- a/)([^\t\r\n]+)`)
	toolPatchGitLineRe  = regexp.MustCompile(`(?m)^diff --git a/\S+ b/([^\t\r\n]+)`)
	toolPatchHdrLineRe  = regexp.MustCompile(`(?m)^\*\*\* (?:Update|Add|Delete) File: ([^\r\n]+)`)
)

// toolPatchBodyPaths 从工具输入、文本与输出里的补丁正文中提取被修改的路径（去重，保持出现顺序）。
// 按 +++ b/（含删除文件时的 --- a/）→ diff --git → *** File: 的顺序尝试，先有结果的一层即返回。
func toolPatchBodyPaths(m Message) []string {
	src := m.ToolInput + "\n" + m.Text
	// 补丁正文有时被塞进 JSON 字符串，换行是转义后的 \n，先还原成真实换行再按行匹配
	src = strings.ReplaceAll(src, `\n`, "\n")
	for _, re := range []*regexp.Regexp{toolPatchFileLineRe, toolPatchGitLineRe, toolPatchHdrLineRe} {
		var paths []string
		seen := make(map[string]bool)
		for _, match := range re.FindAllStringSubmatch(src, -1) {
			p := strings.TrimSpace(match[1])
			if p == "" || p == "/dev/null" || seen[p] {
				continue
			}
			seen[p] = true
			paths = append(paths, p)
		}
		if len(paths) > 0 {
			return paths
		}
	}
	return nil
}

// toolCallEditPaths 返回编辑类工具涉及的全部文件路径（去重，保持出现顺序）：
// 先按大小写不敏感的路径字段（Path/path/filePath 等）取结构化的单个路径——此时以 ToolInput
// 为准，避免被截断的 Text 影响；其次用输入字段与 apply_patch 补丁头（toolEditedPaths），
// 拿不到时再从工具文本和输出里的补丁正文提取——入参为 "apply_patch {}" 时路径只存在于正文里。
func toolCallEditPaths(m Message) []string {
	// 这里直接读字段而不调 toolCallPath：toolCallPath 会回调本函数，互相调用会形成递归
	if isFileEditTool(m) {
		if p, ok := toolFieldString(toolCallFields(m), toolCallPathKeys...); ok {
			return []string{p}
		}
	}
	if paths := toolEditedPaths(m); len(paths) > 0 {
		return paths
	}
	if !isFileEditTool(m) {
		return nil
	}
	return toolPatchBodyPaths(m)
}

// toolCallEditVerb 区分「新建」与「编辑」：写入类工具返回「新建」，其余修改类工具返回「编辑」。
func toolCallEditVerb(name string) string {
	if createToolNames[toolNameKey(name)] {
		return "新建"
	}
	return "编辑"
}

// toolCallLineRange 返回读取类工具的行范围后缀（如 ":120–525"）：
// 优先 StartLine/EndLine，其次 offset/limit（offset 为起始行偏移），都没有时返回空。
func toolCallLineRange(fields map[string]any) string {
	start, hasStart := toolFieldInt(fields, "start_line", "startline", "start")
	end, hasEnd := toolFieldInt(fields, "end_line", "endline", "end")
	switch {
	case hasStart && hasEnd:
		return fmt.Sprintf(":%d–%d", start, end)
	case hasStart:
		return fmt.Sprintf(":%d", start)
	case hasEnd:
		return fmt.Sprintf(":1–%d", end)
	}
	offset, hasOffset := toolFieldInt(fields, "offset")
	limit, hasLimit := toolFieldInt(fields, "limit")
	switch {
	case hasOffset && hasLimit && limit > 0:
		return fmt.Sprintf(":%d–%d", offset, offset+limit)
	case hasOffset:
		return fmt.Sprintf(":%d", offset)
	}
	return ""
}

// toolCallTitle 生成工具行的可读标题，取代原来直翻的「工具名 {JSON}」截断文本：
// verb 是动词（读取/编辑/新建/搜索，命令为 "$"，其余工具用工具名），
// target 是主要对象（文件名、模式串、命令正文），detail 是补充信息（行范围、搜索范围）。
// 只从工具输入里取字段，不回显原始 JSON；字段名不区分大小写。
func toolCallTitle(m Message) (verb, target, detail string) {
	name := strings.TrimSpace(toolMessageName(m))
	originalName := name
	switch name {
	case "read_file", "write_file", "edit_file":
		name = toolNameKey(name)
	}
	fields := toolCallFields(m)

	switch classifyToolCategory(name) {
	case toolCategoryRead:
		if path := toolCallPath(m); path != "" {
			return "读取", filepath.Base(path), toolCallLineRange(fields)
		}
	case toolCategoryEdit:
		if paths := toolCallEditPaths(m); len(paths) > 0 {
			if len(paths) > 1 {
				return toolCallEditVerb(name), fmt.Sprintf("%d 个文件", len(paths)), ""
			}
			return toolCallEditVerb(name), filepath.Base(paths[0]), ""
		}
	case toolCategorySearch:
		if pattern, ok := toolFieldString(fields, toolCallPatternKeys...); ok {
			path, _ := toolFieldString(fields, toolCallSearchPathKeys...)
			return "搜索", `"` + truncateCommandLine(pattern, 60) + `"`, path
		}
	case toolCategoryCommand:
		if info := parseCommandToolInfo(m); info.Command != "" {
			return "$", info.Command, ""
		}
	}
	if isCommandTool(m) {
		if info := parseCommandToolInfo(m); info.Command != "" {
			return "$", info.Command, ""
		}
	}
	// 其余工具：动词用工具名，对象取第一个字符串参数；
	// 没有结构化输入时退回工具文本首行剩下的部分（旧消息或非 JSON 工具，如 "ls -la"），不回显 JSON
	if value := toolCallFirstStringValue(fields); value != "" {
		return name, value, ""
	}
	return name, toolCallTextRemainder(m, originalName), ""
}

// toolCallVerbTense 把工具行动词改成与当前状态一致的时态：运行中用「正在读取」「正在运行」，
// 结束后用「已读取」「已运行」，失败时按已完成（失败本身就是一种结束）处理。
// 命令行的「$」、未知工具名与空动词原样返回：这两类不是中文动词，分词时态反而难读。
func toolCallVerbTense(verb string, running, failed bool) string {
	done, doing, known := toolCallVerbTensePair(verb)
	switch {
	case !known:
		return verb
	case failed:
		return done
	case running:
		return doing
	default:
		return done
	}
}

// toolCallVerbTensePair 返回一个中文动词的完成体与进行体；不是内置动词时 known 为 false。
func toolCallVerbTensePair(verb string) (done, doing string, known bool) {
	switch verb {
	case "读取":
		return "已读取", "正在读取", true
	case "编辑":
		return "已编辑", "正在编辑", true
	case "新建":
		return "已新建", "正在新建", true
	case "搜索":
		return "已搜索", "正在搜索", true
	}
	return "", "", false
}

// toolCallVerb 返回工具行的中文动词：按工具状态选时态，命令行统一用 "$"。
func toolCallVerb(verb string, m Message) string {
	// 后台任务仍由 provider 继续执行，按进行时显示（「正在读取」），和运行中一致。
	return toolCallVerbTense(verb, isToolRunning(m) || isToolBackground(m), isToolFailed(m))
}

// toolCallTextRemainder 返回工具文本首行去掉工具名后的剩余部分；没有剩余内容或剩余内容以 JSON 开头时返回空。
func toolCallTextRemainder(m Message, name string) string {
	line := m.Text
	if idx := strings.IndexByte(line, '\n'); idx != -1 {
		line = line[:idx]
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), name))
	if rest == "" || strings.HasPrefix(rest, "{") {
		return ""
	}
	return truncateCommandLine(rest, 80)
}

// toolGroupStartTime 返回组内第一条工具消息的时间戳；组里没有带时间戳的工具行时返回零值。
func toolGroupStartTime(tools []Message) time.Time {
	for _, m := range tools {
		if m.Role != "tool" {
			continue
		}
		if at, ok := messageTime(m); ok {
			return at
		}
	}
	return time.Time{}
}

// toolGroupClock 把组内首条工具消息的时刻格式化为组头右侧的 HH:mm（本地时区），零值返回空。
func toolGroupClock(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.Local().Format("15:04")
}

// toolGroupHeaderTooltip 给组头摘要补上完整日期时间，方便悬停确认这组命令是什么时候跑的。
func toolGroupHeaderTooltip(summary string, at time.Time) string {
	if at.IsZero() {
		return summary
	}
	return summary + " · " + at.Local().Format("2006-01-02 15:04:05")
}

// renderToolGroupItem 渲染多个连续工具调用的合并组头（按 2.5 节）：一行文字摘要（如「用时 3.2s · 思考 4 次 · 运行 5 条命令」），
// 后面紧跟展开箭头，整行可点击展开或收起。展开后的节拍由列表虚拟化为独立行（renderToolBeatItem）。
// liveID 命中组内消息，或这个组就是运行中会话末尾的活动段时，摘要前显示运行中的点阵图标，
// 摘要带「工作中 · 」前缀、耗时每秒刷新，折叠态还会在组头下方列出最近几条工具行的只读预览。
func renderToolGroupItem(c *ui.Context, s *chatState, tools []Message, groupID string, liveID string, defaultOpen bool) ui.Element {
	// 展开状态与 buildTimelineItems 的判定保持一致：用户显式设置过就以设置为准，
	// 否则用默认值（所有组默认折叠，运行中的组靠组头实时耗时与折叠预览显示进度）。
	s.mu.Lock()
	explicitOpen, hasExplicit := s.expandedTool[groupID]
	s.mu.Unlock()
	open := defaultOpen
	if hasExplicit {
		open = explicitOpen
	}

	// 关闭“显示思考过程”时，摘要不能再统计思考次数
	summaryTools := tools
	if !isShowReasoningEnabled(s) {
		filtered := make([]Message, 0, len(tools))
		for _, m := range tools {
			if !isReasoningMessage(m) {
				filtered = append(filtered, m)
			}
		}
		summaryTools = filtered
	}
	btnLabel := "展开"
	arrowIcon := IconChevronRight()
	if open {
		btnLabel = "收起"
		arrowIcon = IconChevronDown()
	}
	running := toolGroupLive(s, tools, liveID)
	headerText := toolGroupCategoryNames(countToolGroupCategories(summaryTools))
	groupAt := toolGroupStartTime(tools)

	return ui.Column(c.Key("msg-group-" + groupID)).FillWidth().MaxWidth(timelineContentMaxWidth(s)).Children(func() {
		header := ui.Row(c.Key("group-header-"+groupID)).
			Gap(Space2).
			AlignItems(ui.Center).
			Padding(Space0, Space2).
			Radius(RadiusSM).
			MinHeight(24).
			Cursor(ui.CursorPointer).
			Label(btnLabel).
			Tooltip(toolGroupHeaderTooltip(headerText, groupAt)).
			OnClick(func() {
				s.setToolGroupOpen(groupID, !open)
			})
		// hover 时摘要变为主色，只铺一层很淡的底，避免组头看起来像一个按钮
		hovered := header.Hovered()
		textColor := ThemeSecondaryText(c)
		if hovered {
			header.Background(ThemeHover(c).Alpha(0.35))
			textColor = ThemePrimaryText(c)
		}
		header.Children(func() {
			if running {
				// running 成立即保证组内至少有一条消息，首条消息 ID 在同一视图内唯一。
				renderRunningGlyph(c, "tool-group-run-"+tools[0].ID, 12, ThemeSecondaryText(c))
				// 实时耗时每秒走一格，请求下一秒再重绘一行
				c.After(time.Second)
			}
			// 摘要已带上“工作中 · / 用时 Xs · ”前缀，耗时不再单独显示一次
			ui.Text(c, headerText).FontSize(scaledFont(TypeBody)).TextColor(textColor)
			ui.Icon(c, arrowIcon).Size(11, 11).TextColor(textColor)
			// 右侧时刻位：默认透明，组头 hover 或展开时淡入；组内没有时间戳就不画。
			if clock := toolGroupClock(groupAt); clock != "" {
				ui.Spacer(c)
				reveal := float32(0)
				if hovered || open {
					reveal = 1
				}
				stamp := ui.Text(c.Key("group-time-"+groupID), clock).
					FontSize(scaledFont(TypeCaption)).
					TextColor(ThemeSecondaryText(c)).
					Width(44).
					TextAlign(ui.End)
				stamp.Opacity(stamp.AnimateTo("tool-group-time-reveal", reveal, ui.Animation{Duration: animateDuration(c, motionFast), Ease: motionEaseStandard}))
			}
		})
		// 运行中且折叠时，组头下方给最近几条工具行做只读预览；组跑完或展开后不再渲染。
		if running && !open {
			renderToolGroupPreview(c, s, tools, groupID)
		}
	})
}

// toolGroupLive 判断这个工具组是不是当前会话末尾仍在进行的活动组：组内命中了正在流式输出的消息，
// 或线程仍在运行且组内最后一条消息就是整个会话的最后一条消息（即末尾活动段由它收尾）。
// 每次渲染都重新判定，组跑完或回合结束时立刻回到普通折叠行，不依赖缓存里的旧状态。
func toolGroupLive(s *chatState, tools []Message, liveID string) bool {
	if len(tools) == 0 {
		return false
	}
	if liveID != "" && containsMessageID(tools, liveID) {
		return true
	}
	s.mu.Lock()
	lastID := ""
	if len(s.messages) > 0 {
		lastID = s.messages[len(s.messages)-1].ID
	}
	threadID := s.selectedThreadID
	s.mu.Unlock()
	if lastID == "" || lastID != tools[len(tools)-1].ID {
		return false
	}
	return s.isThreadRunning(threadID)
}

// toolGroupPreviewCount 是运行中的工具组折叠时预览的工具行条数。
const toolGroupPreviewCount = 3

// toolGroupPreviewTools 返回折叠预览要显示的工具行：组内最后 limit 条工具消息，按原始顺序排列；
// 工具不足 limit 条时返回全部。
func toolGroupPreviewTools(tools []Message, limit int) []Message {
	if limit <= 0 {
		return nil
	}
	picked := make([]Message, 0, limit)
	for i := len(tools) - 1; i >= 0 && len(picked) < limit; i-- {
		if tools[i].Role != "tool" {
			continue
		}
		picked = append(picked, tools[i])
	}
	slices.Reverse(picked)
	return picked
}

// renderToolGroupPreview 渲染运行中工具组折叠态的预览：以节拍相同的缩进和间距竖排最近几条工具。
// 预览只读，不挂点击与展开，组跑完或用户展开后自然消失。
func renderToolGroupPreview(c *ui.Context, s *chatState, tools []Message, groupID string) {
	preview := toolGroupPreviewTools(tools, toolGroupPreviewCount)
	if len(preview) == 0 {
		return
	}
	ui.Column(c.Key("group-preview-"+groupID)).
		FillWidth().
		Gap(Space1).
		Padding(Hairline, Space2, Space0, toolBeatPadLeft).
		Children(func() {
			for _, m := range preview {
				renderToolChipReadOnly(c, s, m)
			}
		})
}

// renderToolChipReadOnly 渲染预览里的只读胶囊：状态字形、类别标记、短标题与耗时与普通胶囊一致，
// 但不响应点击，也不展开工具详情。
func renderToolChipReadOnly(c *ui.Context, s *chatState, m Message) {
	verb, target, detail := toolCallTitle(m)
	if isToolBackground(m) {
		// 后台任务仍由 provider 继续执行，动词用进行时，与普通胶囊一致。
		verb = toolCallVerb(verb, m)
	}
	cmdInfo := parseCommandToolInfo(m)
	category := classifyToolCategory(toolMessageName(m))
	duration := cmdInfo.Duration
	if duration == "" {
		duration = toolElapsedText(m)
	}
	// 只读预览里的后台任务同样显示实时运行时长，避免被误读成已完成。
	if toolText, ok := toolBackgroundBadge(c, m); ok {
		duration = toolText
	}

	var appCfg config.AppearanceConfig
	if st := getChatSettings(s); st != nil {
		appCfg = st.cfg.Appearance
	}
	fontFam := codeFontFamily(appCfg)
	label := "预览 " + toolCallRowLabel(verb, target, detail)

	chip := ui.Row(c.Key("tool-chip-preview-"+m.ID)).
		AlignSelf(ui.Start).
		Gap(Space2).
		Height(toolChipHeight).
		Radius(toolChipHeight/2).
		Padding(0, Space3).
		Label(label).
		Tooltip(toolCallRowLabel(verb, target, detail)).
		Background(ThemeSecondaryText(c).Alpha(0.06))
	if isToolFailed(m) {
		chip.Border(1, ThemeDanger(c))
	}

	chip.Children(func() {
		renderToolChipStatus(c, m)
		renderToolChipCategoryMark(c, category, cmdInfo.IsCommand, fontFam)
		title := ui.Text(c, toolChipShortTitle(m, verb, target)).
			FontSize(scaledFont(TypeFootnote)).
			TextColor(ThemeSecondaryText(c)).
			SingleLine()
		if cmdInfo.IsCommand && toolChipDescription(m) == "" {
			title.Font(fontFam)
		}
		if duration != "" {
			ui.Text(c, duration).
				FontSize(scaledFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c).Alpha(0.85))
		}
	})
}

// toolGroupElapsedText 返回整组耗时（如 "3.2s"）。
// 运行中的组用 now 减去组内第一条工具消息的开始时间（实时耗时，随每秒重绘增长）；
// 跑完的组由首尾消息的时间戳相减得出。拿不到时间时返回空。
func toolGroupElapsedText(tools []Message, running bool, now time.Time) string {
	if len(tools) == 0 {
		return ""
	}
	if running {
		// 组首可能是思考行，实时耗时从第一条工具调用开始算
		startMsg := tools[0]
		for _, m := range tools {
			if m.Role == "tool" {
				startMsg = m
				break
			}
		}
		start, ok := parseRFC3339Time(startMsg.CreatedAt)
		if !ok || !now.After(start) {
			return ""
		}
		return formatShortDuration(now.Sub(start))
	}
	if len(tools) < 2 {
		return ""
	}
	start, ok := parseRFC3339Time(tools[0].CreatedAt)
	if !ok {
		return ""
	}
	last := tools[len(tools)-1]
	end, ok := parseRFC3339Time(last.ToolCompletedAt)
	if !ok {
		end, ok = parseRFC3339Time(last.CreatedAt)
	}
	if !ok || !end.After(start) {
		return ""
	}
	return formatShortDuration(end.Sub(start))
}

// toolGroupRunningHint 返回最近一条运行中命令的摘要与并行条数：
// 摘要复用胶囊标题（有 description 时用 description，否则是精简后的命令本身），不携带命令输出。
func toolGroupRunningHint(tools []Message) (string, int) {
	snippet := ""
	running := 0
	for _, m := range tools {
		if !toolIsRunningCommand(m) {
			continue
		}
		running++
		verb, target, _ := toolCallTitle(m)
		if text := strings.TrimSpace(toolChipShortTitle(m, verb, target)); text != "" {
			snippet = truncateRunes(text, 24)
		}
	}
	return snippet, running
}

// toolGroupHeaderSummaryText 给组头摘要加上状态前缀：运行中为「工作中 · 」并在末尾报告正在跑的命令，
// 完成且有耗时为「用时 Xs · 」，否则只有现有的分类计数文本。
func toolGroupHeaderSummaryText(counts toolGroupCounts, running bool, elapsed string, tools []Message) string {
	summary := toolGroupSummaryText(counts)
	if running {
		text := "工作中 · " + summary
		if snippet, n := toolGroupRunningHint(tools); snippet != "" {
			text += " · 正在运行 " + snippet
			if n > 1 {
				text += fmt.Sprintf(" 等 %d 条", n)
			}
		}
		return text
	}
	if elapsed != "" {
		return "用时 " + elapsed + " · " + summary
	}
	return summary
}

// toolGroupOpen 返回工具组的实际开合状态：用户显式设置过就以设置为准，
// 没有设置过时用默认值（所有组默认折叠，运行中的组靠组头耗时与预览显示进度）。
func toolGroupOpen(expanded map[string]bool, groupID string, defaultOpen bool) bool {
	if v, ok := expanded[groupID]; ok {
		return v
	}
	return defaultOpen
}

// setToolGroupOpen 显式记录工具组的开合状态，覆盖默认值
// （运行中的活动组默认展开，用户点一下后按用户设置收起）。
func (s *chatState) setToolGroupOpen(groupID string, open bool) {
	s.mu.Lock()
	if s.expandedTool == nil {
		s.expandedTool = make(map[string]bool)
	}
	s.expandedTool[groupID] = open
	s.expandedRev++
	s.mu.Unlock()
	s.holdChatPlace()
	s.invalidate()
}

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

// noticeKind 系统通知的类型，决定通知行的图标与配色。
type noticeKind int

const (
	noticeInfo     noticeKind = iota // 普通信息
	noticeWarning                    // 警告：重试、超时、额度、版本过旧等
	noticeError                      // 错误：失败、异常、无法完成
	noticeWorktree                   // 工作树 / 分支切换
	noticeModel                      // 模型 / 供应商切换
	noticeCompact                    // 上下文压缩
)

// noticeClassifyRules 按顺序判定通知类型的关键词表（按小写文本匹配）：
// 先错误，再警告，然后按主题分类，都没命中则为普通信息。
// 关键词对齐 mcode 后端实际写入的系统消息文本
// （见 internal/remote/memory_backend.go 与 internal/app/swarm_wiring.go 的 AppendNotice 调用）。
var noticeClassifyRules = []struct {
	kind     noticeKind
	keywords []string
}{
	{noticeError, []string{"错误", "失败", "异常", "无法", "未能", "error", "failed"}},
	{noticeWarning, []string{
		"重试", "重新发送", "超时", "额度", "中断", "警告", "过旧", "升级", "失效", "意外退出", "强制结束",
		"retry", "timeout", "quota", "interrupt", "warning",
	}},
	{noticeCompact, []string{"压缩", "compact"}},
	{noticeWorktree, []string{"工作树", "worktree", "分支", "branch", "checkout", "已检出"}},
	{noticeModel, []string{"模型", "model", "供应商", "provider"}},
}

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

// requestSpeedStats 是一轮里多次模型请求的输出速度统计（t/s）：min/max 是各样本的极值，
// avg 按 token 加权，n 是有效样本数。
type requestSpeedStats struct {
	min, max, avg float64
	n             int
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

// resolveTurnDuration 返回本回合用于计算输出速度的生成时长：优先后端记录的耗时（TokenUsage.DurationMs），
// 没有时用 fallback（从界面消息时间戳推导出的时长）。两者都没有时返回 0，调用方不显示速度。
func resolveTurnDuration(usage TokenUsage, fallback time.Duration) time.Duration {
	if usage.DurationMs > 0 {
		return time.Duration(usage.DurationMs) * time.Millisecond
	}
	return fallback
}

// renderTurnFinishedBlock 渲染回合完成块：一条细分割线、完成图标、本回合的模型短名、
// 用时、工具调用与改动文件数、token 统计与输出速度；悬停显示 token 明细、费用、常用工具与起止时间。
// 只在该轮结束（非流式）时由该轮最后一条助手消息调用；时长优先取后端记录的耗时，没有时用 fallback。
// Swarm 主会话仍有运行中或排队中的子代理时，最后一轮先不显示完成块，等它们全部结束后自动恢复。
func renderTurnFinishedBlock(c *ui.Context, s *chatState, m Message, usage TokenUsage, fallbackDur time.Duration) {
	if swarmTurnEndDeferred(s, m.TurnIndex) {
		return
	}
	start, end := turnFinishedSpanFor(s, m)
	s.mu.Lock()
	authoritativeStart, authoritativeEnd := turnAuthoritativeSpan(s.messages, m)
	s.mu.Unlock()
	if !authoritativeStart.IsZero() && !authoritativeEnd.IsZero() && !authoritativeEnd.Before(authoritativeStart) {
		fallbackDur = authoritativeEnd.Sub(authoritativeStart)
	} else if d, ok := turnEndDuration(start, end); ok {
		fallbackDur = d
	} else {
		fallbackDur = resolveTurnDuration(usage, fallbackDur)
	}
	renderTurnFinishedBlockWithDuration(c, s, m, usage, fallbackDur)
}

// renderTurnFinishedBlockWithDuration 是完成块的实际渲染：duration 是已确定的本回合生成时长，
// 没有逐请求样本时按整轮口径算速度，为 0 或不足 0.5 秒则不显示（见 formatTokensPerSecond）。
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
