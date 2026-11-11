package mcodeui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 长文本粘贴的最小阈值：行数或字数任一达到即视为长文本。
// 长文本不进输入框（会把输入框撑爆、难编辑），而是在附件行里放一张卡片：
// 点开可预览全文、放回输入框或移除；发送时接在正文后面，作为内联文本发给模型（不是文件附件）。
const (
	pastedTextMinLines = 20
	pastedTextMinRunes = 1500
)

const (
	// snippetPreviewMaxRunes 是卡片预览最多渲染的字数：再长的文本整段排版会拖慢每一帧。
	snippetPreviewMaxRunes = 20000
	// snippetTitleMaxRunes 是卡片标题最多显示的字数。
	snippetTitleMaxRunes = 24
	// snippetPreviewWidth 是预览浮层卡片的宽度上限。
	snippetPreviewWidth float32 = 520
	// snippetPreviewHeight 是预览正文的高度上限，超出后在浮层内滚动。
	snippetPreviewHeight float32 = 320
)

// composerSnippet 是一段待发送的长文本粘贴。ID 在 chatState 内自增（从 1 开始），只用作界面键。
type composerSnippet struct {
	ID   int
	Text string
}

// isLongPastedText 判断粘贴进来的文本是否算长文本。
func isLongPastedText(text string) bool {
	if text == "" {
		return false
	}
	if strings.Count(text, "\n")+1 >= pastedTextMinLines {
		return true
	}
	return utf8.RuneCountInString(text) >= pastedTextMinRunes
}

// snippetTitle 是卡片标题：第一行非空文本，连续空白压成一个空格。
func snippetTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.Join(strings.Fields(line), " "); line != "" {
			return line
		}
	}
	return ""
}

// snippetPreview 返回预览要渲染的文本，超过 snippetPreviewMaxRunes 时截断并以省略号结尾。
func snippetPreview(text string) string {
	if utf8.RuneCountInString(text) <= snippetPreviewMaxRunes {
		return text
	}
	return string([]rune(text)[:snippetPreviewMaxRunes]) + "…"
}

// composeWithSnippets 把粘贴卡片接在输入的正文后面，段与段之间空一行。
// 没有卡片时原样返回，保证普通消息一个字节都不变。
func composeWithSnippets(text string, snippets []composerSnippet) string {
	if len(snippets) == 0 {
		return text
	}
	parts := make([]string, 0, len(snippets)+1)
	if t := strings.TrimSpace(text); t != "" {
		parts = append(parts, t)
	}
	for _, sn := range snippets {
		parts = append(parts, sn.Text)
	}
	return strings.Join(parts, "\n\n")
}

// snippetIDs 返回卡片 ID 列表，供发送成功后按 ID 移除（发送期间新粘贴的卡片不受影响）。
func snippetIDs(snippets []composerSnippet) []int {
	ids := make([]int, len(snippets))
	for i, sn := range snippets {
		ids[i] = sn.ID
	}
	return ids
}

// pasteLongText 把长文本收成一张卡片并吞掉这次粘贴；短文本返回 false，交给输入框正常插入。
func (s *chatState) pasteLongText(text string) bool {
	if !isLongPastedText(text) {
		return false
	}
	s.mu.Lock()
	s.snippetSeq++
	s.snippets = append(s.snippets, composerSnippet{ID: s.snippetSeq, Text: text})
	s.syncSnippetDraftLocked()
	threadID := s.selectedThreadID
	s.mu.Unlock()
	s.persistDraftsAsync()
	// 与输入文字一样：新会话页有了内容，待创建会话才真正创建。
	if threadID == "" {
		s.createPendingThread(nil)
	}
	s.invalidate()
	return true
}

// outgoingTextLocked 返回要发出去的全文与参与其中的卡片 ID。调用方需持有 s.mu。
func (s *chatState) outgoingTextLocked(text string) (string, []int) {
	return composeWithSnippets(text, s.snippets), snippetIDs(s.snippets)
}

// dropSnippetsLocked 移除指定 ID 的卡片（发送成功后调用）。调用方需持有 s.mu。
func (s *chatState) dropSnippetsLocked(ids []int) {
	if len(ids) == 0 {
		return
	}
	kept := s.snippets[:0:0]
	for _, sn := range s.snippets {
		drop := false
		for _, id := range ids {
			if sn.ID == id {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, sn)
		}
	}
	s.snippets = kept
	s.syncSnippetDraftLocked()
}

// removeSnippet 删掉一张卡片。
func (s *chatState) removeSnippet(id int) {
	s.mu.Lock()
	s.dropSnippetsLocked([]int{id})
	if s.snippetPreviewID == id {
		s.snippetPreviewID = 0
	}
	s.mu.Unlock()
	s.persistDraftsAsync()
	s.invalidate()
}

// expandSnippet 把卡片原文接到草稿末尾（空一行）并移除卡片，光标落到文末，输入框要回焦点。
func (s *chatState) expandSnippet(id int) {
	s.mu.Lock()
	var text string
	found := false
	for _, sn := range s.snippets {
		if sn.ID == id {
			text, found = sn.Text, true
			break
		}
	}
	if !found {
		s.mu.Unlock()
		return
	}
	next := text
	if draft := strings.TrimRight(s.draft, " \t\n"); draft != "" {
		next = draft + "\n\n" + text
	}
	s.pendingDraftFill = &next
	s.pendingDraftFillIfEmpty = false
	s.completionCaretToEnd = true
	s.composerFocusRequest = true
	s.snippetPreviewID = 0
	s.dropSnippetsLocked([]int{id})
	s.mu.Unlock()
	s.invalidate()
}

// syncSnippetDraftLocked 把当前卡片写进所选会话的草稿条目，供落盘时并入草稿文本。
// 正在编辑排队项时，输入框里的卡片属于排队项，不写进用户草稿。调用方需持有 s.mu。
func (s *chatState) syncSnippetDraftLocked() {
	if s.editingFollowUpID != "" {
		return
	}
	if s.drafts == nil {
		s.drafts = make(map[string]composerDraft)
	}
	entry := s.drafts[s.selectedThreadID]
	entry.Snippets = append([]composerSnippet(nil), s.snippets...)
	if strings.TrimSpace(entry.Text) == "" && len(entry.Snippets) == 0 && len(draftAttachments(entry.Attachments)) == 0 {
		delete(s.drafts, s.selectedThreadID)
		return
	}
	s.drafts[s.selectedThreadID] = entry
}

// snippetChipTitle 是卡片上显示的标题：超过 snippetTitleMaxRunes 时截断并以省略号结尾。
func snippetChipTitle(text string) string {
	title := snippetTitle(text)
	if utf8.RuneCountInString(title) <= snippetTitleMaxRunes {
		return title
	}
	return string([]rune(title)[:snippetTitleMaxRunes-1]) + "…"
}

// renderSnippetChip 渲染附件行里的一张长文本卡片：点卡片在上方弹出全文预览，
// 预览里可以把原文放回输入框；右侧 × 直接移除。
func renderSnippetChip(c *ui.Context, s *chatState, sn composerSnippet, previewOpen, sending bool) {
	key := "composer-snippet-" + strconv.Itoa(sn.ID)
	title := snippetChipTitle(sn.Text)
	chip := ui.ButtonBase(c.Key(key)).
		Label("预览粘贴的文本 "+title).
		Transition(motionListItem(c)).
		Padding(Space0, Space3, Space0, Space3).
		Radius(RadiusLG).
		Border(Hairline, ThemeBorder(c)).
		OnClick(func() {
			s.mu.Lock()
			if s.snippetPreviewID == sn.ID {
				s.snippetPreviewID = 0
			} else {
				s.snippetPreviewID = sn.ID
			}
			s.mu.Unlock()
			s.invalidate()
		})
	if previewOpen || chip.Hovered() {
		chip.Background(ThemeBorder(c))
	} else {
		chip.Background(ThemeHover(c))
	}
	chip.Children(func() {
		ui.Row(c.Key(key + "-content")).
			Gap(Space2).
			AlignItems(ui.Center).
			Children(func() {
				ui.Icon(c, IconFile()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				ui.Text(c, title).FontSize(TypeFootnote).TextColor(ThemePrimaryText(c)).SingleLine()
				ui.ButtonBase(c.Key(key+"-remove")).
					Label("移除粘贴的文本 "+title).
					Disabled(sending).
					Size(IconMD, IconMD).
					Radius(RadiusMD).
					Background(ui.Transparent).
					OnClick(func() { s.removeSnippet(sn.ID) }).
					Children(func() {
						ui.Text(c, "×").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
					})
			})
	})

	open := previewOpen
	appPopoverAbove(c.Key(key+"-preview"), chip, &open, func() {
		s.mu.Lock()
		if s.snippetPreviewID == sn.ID {
			s.snippetPreviewID = 0
		}
		s.mu.Unlock()
		s.invalidate()
	}, func() {
		popoverCard(c, ui.Column(c.Key(key+"-preview-card")).
			MaxWidth(snippetPreviewWidth).
			Padding(Space3).
			Gap(Space2)).
			Children(func() {
				ui.Scroll(c.Key(key + "-preview-scroll")).
					FillWidth().
					MaxHeight(snippetPreviewHeight).
					Children(func() {
						ui.Text(c, snippetPreview(sn.Text)).
							FillWidth().
							FontSize(TypeFootnote).
							TextColor(ThemePrimaryText(c)).
							Selectable()
					})
				ui.Row(c.Key(key + "-preview-actions")).
					FillWidth().
					Justify(ui.End).
					Children(func() {
						Button(c.Key(key+"-expand"), ButtonOpts{
							Text:     "放入输入框",
							Label:    "把粘贴的文本放入输入框",
							Size:     ControlXS,
							Variant:  ButtonSecondary,
							Disabled: sending,
							OnClick:  func() { s.expandSnippet(sn.ID) },
						})
					})
			})
	})
}
