package mcodeui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// mentionSegment 表示用户正文按 @会话引用 切开后的一个片段：纯文本，或命中当前会话列表的引用。
type mentionSegment struct {
	Text     string // 片段原文；命中引用时形如 "@标题" 或 "@会话ID"
	ThreadID string // 命中会话的 ID；纯文本片段为空
}

// splitThreadMentions 把用户正文按 @会话引用 切成片段。
// 匹配规则与后端（internal/remote/memory_backend.go 的 mentions 收集）一致：@ 后紧跟某个会话的
// 标题或会话 ID 即为命中；多个会话都能命中同一位置时取最长匹配（标题与 ID 同等对待）。
// 未命中的 @ 文本留在纯文本片段里，原样输出。
func splitThreadMentions(text string, threads []Thread) []mentionSegment {
	if text == "" {
		return nil
	}
	var out []mentionSegment
	start := 0 // 当前纯文本片段的起点
	for i := 0; i < len(text); {
		if text[i] != '@' {
			i++
			continue
		}
		rest := text[i+1:]
		bestID, bestLen := "", 0
		for _, th := range threads {
			if th.Title != "" && len(th.Title) > bestLen && strings.HasPrefix(rest, th.Title) {
				bestID, bestLen = th.ID, len(th.Title)
			}
			if th.ID != "" && len(th.ID) > bestLen && strings.HasPrefix(rest, th.ID) {
				bestID, bestLen = th.ID, len(th.ID)
			}
		}
		if bestLen == 0 {
			i++
			continue
		}
		if i > start {
			out = append(out, mentionSegment{Text: text[start:i]})
		}
		out = append(out, mentionSegment{Text: text[i : i+1+bestLen], ThreadID: bestID})
		i += 1 + bestLen
		start = i
	}
	if start < len(text) || len(out) == 0 {
		out = append(out, mentionSegment{Text: text[start:]})
	}
	return out
}

// hasThreadMention 判断片段列表里是否存在命中的 @会话引用。
func hasThreadMention(segments []mentionSegment) bool {
	for _, seg := range segments {
		if seg.ThreadID != "" {
			return true
		}
	}
	return false
}

// mentionThreadsSnapshot 返回当前会话所在项目的会话列表快照（持 s.mu），供 @会话引用解析使用：
// 与后端一致，只有同一项目下的会话才会被识别为引用；找不到当前会话时返回全部会话。
func mentionThreadsSnapshot(s *chatState) []Thread {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	projectID := ""
	for _, th := range s.threads {
		if th.ID == s.selectedThreadID {
			projectID = th.ProjectID
			break
		}
	}
	out := make([]Thread, 0, len(s.threads))
	for _, th := range s.threads {
		if projectID == "" || th.ProjectID == projectID {
			out = append(out, th)
		}
	}
	return out
}

// mentionThreadTitle 返回被引用会话的当前标题（会话已不存在时为空），用作胶囊的悬停提示。
func mentionThreadTitle(s *chatState, threadID string) string {
	if s == nil || threadID == "" {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return th.Title
		}
	}
	return ""
}

// 会话引用预览浮层的尺寸上限。
const (
	// mentionPreviewWidth 是预览浮层的卡片宽度。
	mentionPreviewWidth float32 = 380
	// mentionPreviewMaxHeight 是预览浮层正文区的最大高度，窗口更矮时按窗口收窄。
	mentionPreviewMaxHeight float32 = 420
)

// mentionPreviewLine 是摘要「近期对话」里的一行，role 为展示用的角色名（用户/助手）。
type mentionPreviewLine struct {
	role, text string
}

// mentionPreviewDigest 是被引用会话的三段式摘要：目标（首条用户需求）、
// 近期对话与结论（最后一条助手答复），裁剪上限与发给模型的引用块一致。
type mentionPreviewDigest struct {
	loading    bool   // 正在从后端读取历史
	missing    bool   // 读取失败后会话已不在列表里，视为已被删除
	err        string // 读取失败的原因
	goal       string
	recent     []mentionPreviewLine
	conclusion string
}

// mentionPreviewMu 保护下面几张表：胶囊浮层的开合状态、当前展开的那一枚、上一帧的关闭记录
// 与按会话 ID 缓存的摘要。
var (
	mentionPreviewMu        sync.Mutex
	mentionPreviewOpen      = map[string]bool{}
	mentionPreviewDismissed = map[string]bool{}
	mentionPreviewDigests   = map[string]*mentionPreviewDigest{}
	mentionPreviewActive    string // 当前允许展示浮层的胶囊 Key，保证同时只有一张浮层
)

// mentionPreviewKey 返回一枚引用胶囊的稳定 Key：消息 ID 与片段序号的组合。
func mentionPreviewKey(msgID string, index int) string {
	return fmt.Sprintf("%s|%d", msgID, index)
}

// mentionPreviewState 读取胶囊浮层的开合状态与上一帧的关闭记录。
func mentionPreviewState(key string) (open, dismissed bool) {
	mentionPreviewMu.Lock()
	defer mentionPreviewMu.Unlock()
	return mentionPreviewOpen[key], mentionPreviewDismissed[key]
}

// mentionPreviewSetOpen 写入开合状态，并把展开的那一枚记为当前浮层，避免多张浮层同时展开。
func mentionPreviewSetOpen(key string, open bool) {
	mentionPreviewMu.Lock()
	defer mentionPreviewMu.Unlock()
	mentionPreviewOpen[key] = open
	if open {
		mentionPreviewActive = key
	} else if mentionPreviewActive == key {
		mentionPreviewActive = ""
	}
}

// mentionPreviewIsActive 判断某枚胶囊是否是当前允许展示浮层的那一枚。
func mentionPreviewIsActive(key string) bool {
	mentionPreviewMu.Lock()
	defer mentionPreviewMu.Unlock()
	return mentionPreviewActive == key
}

// mentionPreviewDigestOf 返回缓存的会话摘要，未读取过时返回 nil。
func mentionPreviewDigestOf(threadID string) *mentionPreviewDigest {
	mentionPreviewMu.Lock()
	defer mentionPreviewMu.Unlock()
	return mentionPreviewDigests[threadID]
}

// mentionThreadExists 判断被引用的会话是否仍在当前会话列表里。
func mentionThreadExists(s *chatState, threadID string) bool {
	if s == nil || threadID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, th := range s.threads {
		if th.ID == threadID {
			return true
		}
	}
	return false
}

// startMentionPreviewLoad 打开浮层时读取并缓存被引用会话的摘要。
func startMentionPreviewLoad(s *chatState, threadID string) {
	if s == nil || threadID == "" {
		return
	}
	mentionPreviewMu.Lock()
	if cached := mentionPreviewDigests[threadID]; cached != nil && (cached.loading || cached.err == "") {
		mentionPreviewMu.Unlock()
		return
	}
	mentionPreviewDigests[threadID] = &mentionPreviewDigest{loading: true}
	mentionPreviewMu.Unlock()

	client := s.client
	go func() {
		digest := &mentionPreviewDigest{err: "后端连接不可用"}
		if client != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if msgs, err := client.History(ctx, threadID); err != nil {
				digest = &mentionPreviewDigest{err: err.Error(), missing: !mentionThreadExists(s, threadID)}
			} else {
				digest = buildMentionPreviewDigest(msgs)
			}
		}
		mentionPreviewMu.Lock()
		mentionPreviewDigests[threadID] = digest
		mentionPreviewMu.Unlock()
		s.invalidate()
	}()
}

// buildMentionPreviewDigest 把会话历史归约为三段式摘要：目标取首条用户需求，
// 结论取最后一条助手答复，近期对话取两者之间最新的若干条。
func buildMentionPreviewDigest(msgs []Message) *mentionPreviewDigest {
	digest := &mentionPreviewDigest{}
	entries := make([]mentionPreviewLine, 0, len(msgs))
	goalIndex, conclusionIndex := -1, -1
	for _, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		text := strings.TrimSpace(m.Text)
		if text == "" {
			continue
		}
		if m.Role == "user" {
			if goalIndex < 0 {
				goalIndex = len(entries)
			}
			entries = append(entries, mentionPreviewLine{role: "用户", text: text})
			continue
		}
		conclusionIndex = len(entries)
		entries = append(entries, mentionPreviewLine{role: "助手", text: text})
	}

	if goalIndex >= 0 {
		digest.goal = clipMentionPreviewText(entries[goalIndex].text, threadMentionGoalLimit)
	}
	for i, entry := range entries {
		if i == goalIndex || i == conclusionIndex {
			continue
		}
		digest.recent = append(digest.recent, mentionPreviewLine{
			role: entry.role,
			text: clipMentionPreviewText(entry.text, threadMentionRecentLimit),
		})
	}
	if len(digest.recent) > threadMentionRecentCount {
		digest.recent = digest.recent[len(digest.recent)-threadMentionRecentCount:]
	}
	if conclusionIndex >= 0 {
		digest.conclusion = clipMentionPreviewText(entries[conclusionIndex].text, threadMentionConclusionLimit)
	}
	return digest
}

// clipMentionPreviewText 把摘要片段裁剪到 limit 个字符以内并补省略号。
func clipMentionPreviewText(text string, limit int) string {
	runes := []rune(text)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return text
}

// renderMentionPreview 渲染一枚引用胶囊的预览浮层：点击胶囊弹出，点外部或 Esc 收起。
// 浮层锚定在气泡段落上（胶囊是段落里的内联文本，无法单独作为锚点）。
// 开合状态在渲染结束后写回，关闭记录按帧刷新，避免收起后下一次点击被旧记录挡住
// （同 renderSwarmCapsule 的处理）。
func renderMentionPreview(c *ui.Context, s *chatState, anchor ui.Element, key string, seg mentionSegment) {
	open, _ := mentionPreviewState(key)
	openedBefore := open
	closeRequested := false
	panel := appPopover(c.Key("mention-preview-"+key), anchor, &open, nil, func() {
		_, winHeight := c.Size()
		maxH := max(160, winHeight-200)
		if maxH > mentionPreviewMaxHeight {
			maxH = mentionPreviewMaxHeight
		}
		exists := mentionThreadExists(s, seg.ThreadID)
		digest := mentionPreviewDigestOf(seg.ThreadID)
		card := ui.Column(c.Key("mention-preview-card-"+key)).
			Width(mentionPreviewWidth).
			Padding(Space4, Space5, Space4, Space5).
			Gap(Space3).
			Radius(RadiusLG).
			Background(ThemeSurface(c)).
			Border(Hairline, ThemeBorder(c))
		applyElevation(c, card, ElevPopover).
			Children(func() {
				renderMentionPreviewHeader(c, s, key, seg)
				switch {
				case !exists || (digest != nil && digest.missing):
					ui.Text(c, "该会话已不存在").FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
				case digest == nil || digest.loading:
					renderSkeletonRows(c, "mention-preview-skeleton-"+key, 3, scaledFont(TypeCaption)+scaledFont(TypeFootnote)*1.45, Space3)
				case digest.err != "":
					ui.Text(c, "会话摘要读取失败："+digest.err).
						FontSize(scaledFont(TypeFootnote)).
						LineHeight(1.4).
						TextColor(ThemeDanger(c))
				default:
					renderMentionPreviewBody(c, key, digest, maxH)
				}
				if exists {
					renderMentionPreviewJump(c, s, key, seg, func() { closeRequested = true })
				}
			})
	})
	if closeRequested {
		open = false
	}
	mentionPreviewMu.Lock()
	mentionPreviewOpen[key] = open
	mentionPreviewDismissed[key] = openedBefore && !open && panel.Dismissed()
	if !open && mentionPreviewActive == key {
		mentionPreviewActive = ""
	}
	mentionPreviewMu.Unlock()
}

// renderMentionPreviewHeader 渲染浮层头部：被引用会话的当前标题与说明。
func renderMentionPreviewHeader(c *ui.Context, s *chatState, key string, seg mentionSegment) {
	title := mentionThreadTitle(s, seg.ThreadID)
	if title == "" {
		title = strings.TrimPrefix(seg.Text, "@")
	}
	ui.Column(c.Key("mention-preview-head-" + key)).Gap(Space0).Children(func() {
		ui.Text(c, title).
			FontSize(scaledFont(TypeBody)).
			FontWeight(600).
			TextColor(ThemePrimaryText(c)).
			SingleLine()
		ui.Text(c, "被引用的会话").
			FontSize(scaledFont(TypeCaption)).
			TextColor(ThemeSecondaryText(c))
	})
}

// renderMentionPreviewBody 渲染三段式摘要正文：目标、近期对话与结论，超出高度上限时内部滚动。
func renderMentionPreviewBody(c *ui.Context, key string, digest *mentionPreviewDigest, maxH float32) {
	if strings.TrimSpace(digest.goal) == "" && strings.TrimSpace(digest.conclusion) == "" && len(digest.recent) == 0 {
		ui.Text(c, "该会话还没有可展示的内容").FontSize(scaledFont(TypeFootnote)).TextColor(ThemeSecondaryText(c))
		return
	}
	ui.Scroll(c.Key("mention-preview-scroll-" + key)).FillWidth().MaxHeight(maxH).Children(func() {
		ui.Column(c.Key("mention-preview-body-" + key)).FillWidth().Gap(Space3).Children(func() {
			renderMentionPreviewSection(c, key+"-goal", "目标", digest.goal)
			if len(digest.recent) > 0 {
				ui.Text(c, "近期对话").
					FontSize(scaledFont(TypeCaption)).
					FontWeight(600).
					TextColor(ThemeAccent(c))
				ui.Column(c.Key("mention-preview-recent-" + key)).FillWidth().Gap(Space2).Children(func() {
					for i, line := range digest.recent {
						ui.Column(c.Key(fmt.Sprintf("mention-preview-recent-%s-%d", key, i))).
							FillWidth().
							Gap(Space0).
							Children(func() {
								ui.Text(c, line.role).
									FontSize(scaledFont(TypeCaption)).
									FontWeight(600).
									TextColor(ThemeSecondaryText(c))
								ui.Text(c, line.text).
									Selectable().
									FontSize(scaledFont(TypeFootnote)).
									LineHeight(1.4).
									TextColor(ThemePrimaryText(c).Alpha(0.9))
							})
					}
				})
			}
			renderMentionPreviewSection(c, key+"-conclusion", "结论", digest.conclusion)
		})
	})
}

// renderMentionPreviewSection 渲染摘要里的一段（标题 + 正文），内容为空时整段不占位。
func renderMentionPreviewSection(c *ui.Context, key, label, text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	ui.Column(c.Key("mention-preview-" + key)).FillWidth().Gap(Space0).Children(func() {
		ui.Text(c, label).
			FontSize(scaledFont(TypeCaption)).
			FontWeight(600).
			TextColor(ThemeAccent(c))
		ui.Text(c, text).
			Selectable().
			FontSize(scaledFont(TypeFootnote)).
			LineHeight(1.45).
			TextColor(ThemePrimaryText(c))
	})
}

// renderMentionPreviewJump 渲染浮层底部的「跳转到该会话」按钮：切换会话并收起浮层。
func renderMentionPreviewJump(c *ui.Context, s *chatState, key string, seg mentionSegment, onJumped func()) {
	btn := ui.ButtonBase(c.Key("mention-preview-jump-"+key)).
		Label("跳转到该会话").
		Radius(RadiusSM).
		Padding(Space1, Space3).
		Background(ThemeAccent(c).Alpha(0.14))
	if btn.Hovered() {
		btn.Background(ThemeAccent(c).Alpha(0.24))
	}
	if btn.Clicked() {
		if onJumped != nil {
			onJumped()
		}
		s.headerSelectThread(seg.ThreadID)
	}
	btn.Children(func() {
		ui.Text(c, "跳转到该会话").
			FontSize(scaledFont(TypeFootnote)).
			FontWeight(500).
			TextColor(ThemeAccent(c))
	})
}

// renderUserMentionParagraph 渲染带 @会话引用的用户气泡正文：引用是强调色调的圆角胶囊，
// 点击弹出该会话的摘要预览浮层，再由浮层里的「跳转到该会话」切换过去；悬停显示其当前标题。
// 底色、内边距与圆角由外层的用户气泡容器提供，这里只画段落本身，保证 mention 气泡与普通气泡外观一致。
// mygo 的段落只允许文本元素内联（非文本子元素会 panic），所以胶囊内不放图标，
// 浮层锚定在整段气泡上。
// MaxWidthPercent(100)：与普通正文同理，段落要压回气泡内容宽度，否则超宽段落会向左溢出气泡。
func renderUserMentionParagraph(c *ui.Context, s *chatState, m Message, segments []mentionSegment) {
	para := ui.RichText(c).
		MaxWidthPercent(100).
		Selectable().
		ContextMenu(messageContextMenu(c, s, m)).
		FontSize(scaledFont(TypeBody)).
		LineHeight(1.45).
		TextColor(ThemePrimaryText(c))
	para.Children(func() {
		for i, seg := range segments {
			if seg.ThreadID == "" {
				if seg.Text != "" {
					ui.Text(c, seg.Text)
				}
				continue
			}
			key := mentionPreviewKey(m.ID, i)
			open, dismissed := mentionPreviewState(key)
			tooltip := mentionThreadTitle(s, seg.ThreadID)
			if tooltip == "" {
				tooltip = seg.Text
			}
			ui.RichText(c.Key("mention-"+key), ui.Span{
				Text:       seg.Text,
				Color:      ThemeAccent(c),
				Weight:     500,
				Background: ThemeAccent(c).Alpha(0.18),
				Radius:     7,
			}).
				Tooltip(tooltip).
				Cursor(ui.CursorPointer).
				OnClick(func() {
					// 浮层展开时点胶囊，PopoverBase 已先把状态置为关闭并把这次按下传给胶囊，
					// 这里不能反手再打开，否则会互相抵消（同 renderSwarmCapsule）。
					if !open && !dismissed {
						startMentionPreviewLoad(s, seg.ThreadID)
					}
					mentionPreviewSetOpen(key, !open && !dismissed)
					s.invalidate()
				})
		}
	})

	// 段落构建完之后再渲染浮层：浮层是否展开由点击写下的状态决定，
	// 同一时间只渲染当前那一枚，其余胶囊的旧状态按帧清掉。
	for i, seg := range segments {
		if seg.ThreadID == "" {
			continue
		}
		key := mentionPreviewKey(m.ID, i)
		if open, _ := mentionPreviewState(key); open && mentionPreviewIsActive(key) {
			renderMentionPreview(c, s, para, key, seg)
			continue
		}
		// 未展开时每帧清掉旧的关闭记录，避免浮层收起后下一次点击又被它挡住。
		mentionPreviewMu.Lock()
		mentionPreviewOpen[key] = false
		mentionPreviewDismissed[key] = false
		mentionPreviewMu.Unlock()
	}
}
