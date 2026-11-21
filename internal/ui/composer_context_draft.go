package mcodeui

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

const (
	composerContextDraftModalWidth       float32 = 640
	composerContextDraftModalHeight      float32 = 540
	composerContextDraftModalMinWidth    float32 = 360
	composerContextDraftModalMinHeight   float32 = 280
	composerContextDraftPreviewMaxHeight float32 = 220
)

type composerContextDraftState struct {
	mu                sync.Mutex
	open              bool
	openThreadID      string
	expandedLocalID   string
	expandedSnippetID int
	editingLocalID    string
	editText          string
	editingSnippetID  int
	scroll            ui.ScrollState
}

var composerContextDraftStates sync.Map // map[*chatState]*composerContextDraftState

func getComposerContextDraftState(s *chatState) *composerContextDraftState {
	if s == nil {
		return nil
	}
	if v, ok := composerContextDraftStates.Load(s); ok {
		return v.(*composerContextDraftState)
	}
	st := &composerContextDraftState{}
	actual, _ := composerContextDraftStates.LoadOrStore(s, st)
	return actual.(*composerContextDraftState)
}

func openComposerContextDraftModal(s *chatState, focusLocalID string) {
	st := getComposerContextDraftState(s)
	if st == nil {
		return
	}
	s.mu.Lock()
	threadID := s.selectedThreadID
	s.mu.Unlock()

	st.mu.Lock()
	st.open = true
	st.openThreadID = threadID
	st.expandedLocalID = focusLocalID
	st.expandedSnippetID = 0
	st.editingLocalID = ""
	st.editingSnippetID = 0
	st.editText = ""
	st.mu.Unlock()
	s.invalidate()
}

func openComposerContextDraftForAttachment(s *chatState, localID string) {
	openComposerContextDraftModal(s, localID)
}

func closeComposerContextDraftModal(s *chatState) {
	st := getComposerContextDraftState(s)
	if st == nil {
		return
	}
	st.mu.Lock()
	wasOpen := st.open
	st.open = false
	st.openThreadID = ""
	st.expandedLocalID = ""
	st.expandedSnippetID = 0
	st.editingLocalID = ""
	st.editingSnippetID = 0
	st.editText = ""
	st.mu.Unlock()
	if wasOpen {
		s.invalidate()
	}
}

// contextSourceLabel 返回上下文条目的简洁中文来源标签。
func contextSourceLabel(kind string, isImage bool) string {
	switch kind {
	case contextKindReview:
		return "审阅评论"
	case contextKindAnnotation:
		return "浏览器标注"
	case contextKindConsole:
		return "控制台日志"
	case contextKindSelection:
		return "代码选区"
	case contextKindText:
		return "文本附件"
	}
	if isImage {
		return "图片附件"
	}
	return "文本附件"
}

func contextAttachmentSourceLabel(a composerAttachment) string {
	isImage := isAllowedImageMime(a.MimeType)
	kind := a.ContextKind
	if kind == "" {
		kind = classifyContextAttachment(a.Name, a.MimeType, a.InspectorRef)
	}
	if kind == "" && !isImage && a.PreviewText == "" {
		return "文件附件"
	}
	return contextSourceLabel(kind, isImage)
}

// contextAttachmentBadge 返回胶囊上显示的来源标签；仅对具名上下文或可读文本上下文显示，保持普通二进制附件简洁。
func contextAttachmentBadge(a composerAttachment) string {
	kind := a.ContextKind
	if kind == "" {
		kind = classifyContextAttachment(a.Name, a.MimeType, a.InspectorRef)
	}
	switch kind {
	case contextKindReview, contextKindAnnotation, contextKindConsole, contextKindSelection:
		return contextSourceLabel(kind, isAllowedImageMime(a.MimeType))
	case contextKindText:
		if a.PreviewText != "" {
			return contextSourceLabel(kind, false)
		}
	}
	return ""
}

func shouldShowContextDraftButton(attachments []composerAttachment, snippets []composerSnippet) bool {
	if len(attachments)+len(snippets) >= 2 {
		return true
	}
	for _, a := range attachments {
		if a.ContextKind != "" || a.PreviewText != "" || a.InspectorRef != nil {
			return true
		}
	}
	return false
}

func formatContextTextStats(text string) string {
	runes := utf8.RuneCountInString(text)
	lines := strings.Count(text, "\n") + 1
	return fmt.Sprintf("%d 字 · %d 行", runes, lines)
}

func (s *chatState) updateSnippet(id int, newText string) bool {
	if s == nil || len(newText) == 0 {
		return false
	}
	s.mu.Lock()
	if s.sending {
		s.mu.Unlock()
		return false
	}
	updated := false
	for i := range s.snippets {
		if s.snippets[i].ID == id {
			s.snippets[i].Text = newText
			updated = true
			break
		}
	}
	if !updated {
		s.mu.Unlock()
		return false
	}
	s.syncSnippetDraftLocked()
	s.mu.Unlock()
	s.persistDraftsAsync()
	s.invalidate()
	return true
}

type contextDraftSnapshot struct {
	open              bool
	expandedLocalID   string
	expandedSnippetID int
	editingLocalID    string
	editingSnippetID  int
	editText          string
}

func snapshotContextDraftState(st *composerContextDraftState, currentThreadID string) contextDraftSnapshot {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.open && st.openThreadID != currentThreadID {
		st.open = false
		st.openThreadID = ""
		st.expandedLocalID = ""
		st.expandedSnippetID = 0
		st.editingLocalID = ""
		st.editingSnippetID = 0
		st.editText = ""
	}
	return contextDraftSnapshot{
		open:              st.open,
		expandedLocalID:   st.expandedLocalID,
		expandedSnippetID: st.expandedSnippetID,
		editingLocalID:    st.editingLocalID,
		editingSnippetID:  st.editingSnippetID,
		editText:          st.editText,
	}
}

// renderComposerContextDraftModal 渲染会话级「上下文草稿区」弹窗，支持预览、单条编辑/删除、合并与一键清空。
func renderComposerContextDraftModal(c *ui.Context, s *chatState) {
	st := getComposerContextDraftState(s)
	if st == nil {
		return
	}

	s.mu.Lock()
	currentThreadID := s.selectedThreadID
	attachments := append([]composerAttachment(nil), s.attachments...)
	snippets := append([]composerSnippet(nil), s.snippets...)
	sending := s.sending
	s.mu.Unlock()

	snap := snapshotContextDraftState(st, currentThreadID)
	if !snap.open {
		return
	}

	var mergeableIDs []string
	for _, a := range attachments {
		if isMergeableTextAttachment(a) {
			mergeableIDs = append(mergeableIDs, a.LocalID)
		}
	}
	totalCount := len(attachments) + len(snippets)

	winW, winH := c.Size()
	cardW := min(composerContextDraftModalWidth, max(composerContextDraftModalMinWidth, winW-Space8))
	cardH := min(composerContextDraftModalHeight, max(composerContextDraftModalMinHeight, winH-Space8))

	open := true
	ui.DialogBase(c, &open, func(backdrop, panel ui.Element) {
		backdrop.Absolute().Left(0).Top(0).Right(0).Bottom(0).Center().Background(ui.Color{}.Alpha(ScrimOpacity))
		panel.MaxWidth(winW - Space8).MaxHeight(winH - Space8).Padding(0)

		ui.Column(c.Key("composer-context-draft-modal")).
			Label("上下文草稿区").
			Width(cardW).
			Height(cardH).
			Background(ThemeSurface(c)).
			Border(Hairline, ThemeBorder(c)).
			Radius(RadiusLG).
			Transition(motionDialog(c)).
			Clip().
			Children(func() {
				// 顶栏
				ui.Row(c.Key("composer-context-draft-header")).
					FillWidth().
					Padding(Space3, Space5).
					Gap(Space3).
					AlignItems(ui.Center).
					BorderWidth(0, 0, Hairline, 0).
					BorderColor(ThemeBorder(c)).
					Children(func() {
						ui.Text(c, "上下文草稿区").
							FontSize(uiFont(TypeTitle)).
							FontWeight(600).
							TextColor(ThemePrimaryText(c))
						ui.Text(c, fmt.Sprintf("共 %d 项", totalCount)).
							FontSize(uiFont(TypeCaption)).
							TextColor(ThemeSecondaryText(c)).
							Grow(1)
						IconButton(c, IconButtonProps{
							Key:     "composer-context-draft-header-close",
							Icon:    IconX(),
							Label:   "关闭上下文草稿区",
							Tooltip: "关闭",
							Size:    ControlSM,
							Variant: ButtonGhost,
							OnClick: func() { closeComposerContextDraftModal(s) },
						})
					})

				// 内容列表
				ui.Scroll(c.Key("composer-context-draft-scroll")).
					FillWidth().
					Grow(1).
					MinHeight(0).
					TrackScroll(&st.scroll).
					Children(func() {
						ui.Column(c.Key("composer-context-draft-list")).
							FillWidth().
							Padding(Space4, Space5).
							Gap(Space3).
							Children(func() {
								if totalCount == 0 {
									ui.Text(c, "暂无上下文草稿").
										FontSize(uiFont(TypeFootnote)).
										TextColor(ThemeSecondaryText(c))
									return
								}
								for _, att := range attachments {
									renderContextDraftAttachmentItem(c, s, st, snap, att, sending)
								}
								for _, sn := range snippets {
									renderContextDraftSnippetItem(c, s, st, snap, sn, sending)
								}
							})
					})

				// 底栏操作区
				ui.Row(c.Key("composer-context-draft-footer")).
					FillWidth().
					Padding(Space3, Space5).
					Gap(Space2).
					AlignItems(ui.Center).
					BorderWidth(Hairline, 0, 0, 0).
					BorderColor(ThemeBorder(c)).
					Children(func() {
						if len(mergeableIDs) >= 2 {
							Button(c, ButtonOpts{
								Key:      "composer-context-draft-merge",
								Text:     "合并为一份",
								Label:    "合并为一份",
								Size:     ControlSM,
								Variant:  ButtonSecondary,
								Disabled: sending,
								OnClick: func() {
									if s.mergeContextAttachments(mergeableIDs) {
										s.mu.Lock()
										var newID string
										if n := len(s.attachments); n > 0 {
											newID = s.attachments[n-1].LocalID
										}
										s.mu.Unlock()
										st.mu.Lock()
										st.editingLocalID = ""
										st.editingSnippetID = 0
										st.editText = ""
										st.expandedLocalID = newID
										st.expandedSnippetID = 0
										st.mu.Unlock()
									}
								},
							})
						}
						ui.Box(c).Grow(1)
						if totalCount > 0 {
							Button(c, ButtonOpts{
								Key:      "composer-context-draft-clear",
								Text:     "清空全部",
								Label:    "清空全部",
								Size:     ControlSM,
								Variant:  ButtonDanger,
								Disabled: sending,
								OnClick: func() {
									s.clearContextDrafts()
									closeComposerContextDraftModal(s)
								},
							})
						}
						Button(c, ButtonOpts{
							Key:     "composer-context-draft-close",
							Text:    "关闭",
							Label:   "关闭",
							Size:    ControlSM,
							Variant: ButtonGhost,
							OnClick: func() { closeComposerContextDraftModal(s) },
						})
					})
			})
	})
	if !open || (snap.editingLocalID == "" && snap.editingSnippetID == 0 && c.Shortcut(0, ui.KeyEscape)) {
		closeComposerContextDraftModal(s)
	}
}

func renderContextDraftBadge(c *ui.Context, key, label string) {
	ui.Row(c.Key(key)).
		Padding(Space0, Space2).
		Radius(RadiusSM).
		Background(ThemeHover(c)).
		AlignItems(ui.Center).
		Children(func() {
			ui.Text(c, label).
				FontSize(uiFont(TypeCaption)).
				TextColor(ThemeSecondaryText(c)).
				SingleLine()
		})
}

func renderContextDraftAttachmentItem(c *ui.Context, s *chatState, st *composerContextDraftState, snap contextDraftSnapshot, a composerAttachment, sending bool) {
	isImage := isAllowedImageMime(a.MimeType)
	hasPreview := !isImage && a.PreviewText != ""
	truncated := hasPreview && a.Size > composerPreviewTextLimit
	expanded := snap.expandedLocalID == a.LocalID
	editing := snap.editingLocalID == a.LocalID
	sourceLabel := contextAttachmentSourceLabel(a)

	var stats string
	switch {
	case a.Uploading:
		stats = "上传中"
	case a.Err != "":
		stats = "上传失败"
	case hasPreview:
		stats = formatContextTextStats(a.PreviewText)
		if truncated {
			stats += " · 只读预览"
		}
	case a.Size > 0:
		stats = formatFileSize(int64(a.Size))
	}

	itemKey := "composer-context-draft-att-" + a.LocalID
	ui.Column(c.Key(itemKey)).
		FillWidth().
		Padding(Space3).
		Gap(Space2).
		Radius(RadiusMD).
		Background(ThemeSurfaceRaised(c)).
		Border(Hairline, ThemeBorder(c)).
		Children(func() {
			ui.Row(c.Key(itemKey + "-header")).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					if isImage && a.Thumb != nil {
						thumbBtn := ui.ButtonBase(c.Key(itemKey+"-thumb-btn")).
							Label("放大预览 "+a.Name).
							Size(composerAttachThumbSize, composerAttachThumbSize).
							Radius(RadiusSM).
							Clip().
							Cursor(ui.CursorPointer).
							Tooltip("点击放大").
							OnClick(func() { openComposerAttachmentPreview(s, a) })
						focusRing(c, thumbBtn)
						thumbBtn.Children(func() {
							ui.Image(c, a.Thumb).
								Key(itemKey+"-thumb").
								Width(composerAttachThumbSize).
								Height(composerAttachThumbSize)
						})
					}

					renderContextDraftBadge(c, itemKey+"-badge", sourceLabel)

					titleCol := ui.Column(c.Key(itemKey + "-meta")).
						Grow(1).
						MinWidth(0).
						Gap(Space0).
						Children(func() {
							ui.Text(c, a.Name).
								FontSize(uiFont(TypeFootnote)).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								SingleLine().
								Ellipsis("…")
							if stats != "" {
								statsColor := ThemeSecondaryText(c)
								if a.Err != "" {
									statsColor = ThemeDanger(c)
								}
								ui.Text(c, stats).
									FontSize(uiFont(TypeCaption)).
									TextColor(statsColor).
									SingleLine()
							}
						})
					_ = titleCol

					if a.Err != "" {
						Button(c, ButtonOpts{
							Key:      itemKey + "-retry",
							Text:     "重试",
							Label:    "重试 " + a.Name,
							Size:     ControlSM,
							Variant:  ButtonGhost,
							Disabled: sending,
							OnClick:  func() { s.retryAttachment(a.LocalID) },
						})
					}
					if isImage {
						Button(c, ButtonOpts{
							Key:     itemKey + "-zoom",
							Text:    "预览",
							Label:   "放大预览 " + a.Name,
							Size:    ControlSM,
							Variant: ButtonGhost,
							OnClick: func() { openComposerAttachmentPreview(s, a) },
						})
					} else if hasPreview {
						toggleText := "展开"
						if expanded {
							toggleText = "收起"
						}
						Button(c, ButtonOpts{
							Key:     itemKey + "-toggle",
							Text:    toggleText,
							Label:   toggleText + " " + a.Name,
							Size:    ControlSM,
							Variant: ButtonGhost,
							OnClick: func() {
								st.mu.Lock()
								if st.expandedLocalID == a.LocalID {
									st.expandedLocalID = ""
								} else {
									st.expandedLocalID = a.LocalID
									st.expandedSnippetID = 0
								}
								st.mu.Unlock()
								s.invalidate()
							},
						})
						if !truncated {
							Button(c, ButtonOpts{
								Key:      itemKey + "-edit",
								Text:     "编辑",
								Label:    "编辑 " + a.Name,
								Size:     ControlSM,
								Variant:  ButtonGhost,
								Disabled: sending,
								OnClick: func() {
									st.mu.Lock()
									st.editingLocalID = a.LocalID
									st.editingSnippetID = 0
									st.editText = a.PreviewText
									st.expandedLocalID = a.LocalID
									st.expandedSnippetID = 0
									st.mu.Unlock()
									s.invalidate()
								},
							})
						}
					}
					Button(c, ButtonOpts{
						Key:      itemKey + "-remove",
						Text:     "移除",
						Label:    "移除 " + a.Name,
						Size:     ControlSM,
						Variant:  ButtonDanger,
						Disabled: sending,
						OnClick: func() {
							st.mu.Lock()
							if st.editingLocalID == a.LocalID {
								st.editingLocalID = ""
								st.editText = ""
							}
							if st.expandedLocalID == a.LocalID {
								st.expandedLocalID = ""
							}
							st.mu.Unlock()
							s.removeAttachment(a.LocalID)
						},
					})
				})

			if editing {
				renderContextDraftEditor(c, s, st, itemKey+"-editor", "编辑 "+a.Name, snap.editText, sending, func() {
					st.mu.Lock()
					newText := st.editText
					st.mu.Unlock()
					if s.updateTextAttachment(a.LocalID, newText) {
						st.mu.Lock()
						st.editingLocalID = ""
						st.editText = ""
						st.expandedLocalID = a.LocalID
						st.mu.Unlock()
					}
				}, func() {
					st.mu.Lock()
					st.editingLocalID = ""
					st.editText = ""
					st.mu.Unlock()
					s.invalidate()
				})
				return
			}

			if expanded && hasPreview {
				if truncated {
					ui.Text(c, "内容超过 256KB，仅支持只读预览").
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c))
				}
				ui.Scroll(c.Key(itemKey + "-preview-scroll")).
					FillWidth().
					MaxHeight(composerContextDraftPreviewMaxHeight).
					Padding(Space2).
					Radius(RadiusSM).
					Background(ThemeSurface(c)).
					Border(Hairline, ThemeBorder(c)).
					Children(func() {
						ui.Text(c, snippetPreview(a.PreviewText)).
							FillWidth().
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemePrimaryText(c)).
							Selectable()
					})
			} else if hasPreview {
				if summary := snippetTitle(a.PreviewText); summary != "" {
					ui.Text(c, summary).
						FillWidth().
						FontSize(uiFont(TypeCaption)).
						TextColor(ThemeSecondaryText(c)).
						SingleLine().
						Ellipsis("…")
				}
			}
		})
}

func renderContextDraftSnippetItem(c *ui.Context, s *chatState, st *composerContextDraftState, snap contextDraftSnapshot, sn composerSnippet, sending bool) {
	title := snippetChipTitle(sn.Text)
	if title == "" {
		title = "粘贴片段 #" + strconv.Itoa(sn.ID)
	}
	expanded := snap.expandedSnippetID == sn.ID
	editing := snap.editingSnippetID == sn.ID
	itemKey := "composer-context-draft-snippet-" + strconv.Itoa(sn.ID)

	ui.Column(c.Key(itemKey)).
		FillWidth().
		Padding(Space3).
		Gap(Space2).
		Radius(RadiusMD).
		Background(ThemeSurfaceRaised(c)).
		Border(Hairline, ThemeBorder(c)).
		Children(func() {
			ui.Row(c.Key(itemKey + "-header")).
				FillWidth().
				Gap(Space2).
				AlignItems(ui.Center).
				Children(func() {
					renderContextDraftBadge(c, itemKey+"-badge", "粘贴片段")
					ui.Column(c.Key(itemKey + "-meta")).
						Grow(1).
						MinWidth(0).
						Gap(Space0).
						Children(func() {
							ui.Text(c, title).
								FontSize(uiFont(TypeFootnote)).
								FontWeight(500).
								TextColor(ThemePrimaryText(c)).
								SingleLine().
								Ellipsis("…")
							ui.Text(c, formatContextTextStats(sn.Text)).
								FontSize(uiFont(TypeCaption)).
								TextColor(ThemeSecondaryText(c)).
								SingleLine()
						})
					toggleText := "展开"
					if expanded {
						toggleText = "收起"
					}
					Button(c, ButtonOpts{
						Key:     itemKey + "-toggle",
						Text:    toggleText,
						Label:   toggleText + " " + title,
						Size:    ControlSM,
						Variant: ButtonGhost,
						OnClick: func() {
							st.mu.Lock()
							if st.expandedSnippetID == sn.ID {
								st.expandedSnippetID = 0
							} else {
								st.expandedSnippetID = sn.ID
								st.expandedLocalID = ""
							}
							st.mu.Unlock()
							s.invalidate()
						},
					})
					Button(c, ButtonOpts{
						Key:      itemKey + "-edit",
						Text:     "编辑",
						Label:    "编辑 " + title,
						Size:     ControlSM,
						Variant:  ButtonGhost,
						Disabled: sending,
						OnClick: func() {
							st.mu.Lock()
							st.editingSnippetID = sn.ID
							st.editingLocalID = ""
							st.editText = sn.Text
							st.expandedSnippetID = sn.ID
							st.expandedLocalID = ""
							st.mu.Unlock()
							s.invalidate()
						},
					})
					Button(c, ButtonOpts{
						Key:      itemKey + "-expand",
						Text:     "放入输入框",
						Label:    "放入输入框 " + title,
						Size:     ControlSM,
						Variant:  ButtonGhost,
						Disabled: sending,
						OnClick: func() {
							st.mu.Lock()
							if st.editingSnippetID == sn.ID {
								st.editingSnippetID = 0
								st.editText = ""
							}
							if st.expandedSnippetID == sn.ID {
								st.expandedSnippetID = 0
							}
							st.mu.Unlock()
							s.expandSnippet(sn.ID)
						},
					})
					Button(c, ButtonOpts{
						Key:      itemKey + "-remove",
						Text:     "移除",
						Label:    "移除 " + title,
						Size:     ControlSM,
						Variant:  ButtonDanger,
						Disabled: sending,
						OnClick: func() {
							st.mu.Lock()
							if st.editingSnippetID == sn.ID {
								st.editingSnippetID = 0
								st.editText = ""
							}
							if st.expandedSnippetID == sn.ID {
								st.expandedSnippetID = 0
							}
							st.mu.Unlock()
							s.removeSnippet(sn.ID)
						},
					})
				})

			if editing {
				renderContextDraftEditor(c, s, st, itemKey+"-editor", "编辑 "+title, snap.editText, sending, func() {
					st.mu.Lock()
					newText := st.editText
					st.mu.Unlock()
					if s.updateSnippet(sn.ID, newText) {
						st.mu.Lock()
						st.editingSnippetID = 0
						st.editText = ""
						st.expandedSnippetID = sn.ID
						st.mu.Unlock()
					}
				}, func() {
					st.mu.Lock()
					st.editingSnippetID = 0
					st.editText = ""
					st.mu.Unlock()
					s.invalidate()
				})
				return
			}

			if expanded {
				ui.Scroll(c.Key(itemKey + "-preview-scroll")).
					FillWidth().
					MaxHeight(composerContextDraftPreviewMaxHeight).
					Padding(Space2).
					Radius(RadiusSM).
					Background(ThemeSurface(c)).
					Border(Hairline, ThemeBorder(c)).
					Children(func() {
						ui.Text(c, snippetPreview(sn.Text)).
							FillWidth().
							FontSize(uiFont(TypeFootnote)).
							TextColor(ThemePrimaryText(c)).
							Selectable()
					})
			}
		})
}

func renderContextDraftEditor(c *ui.Context, s *chatState, st *composerContextDraftState, key, label, value string, sending bool, onSave, onCancel func()) {
	draft := value
	ui.Column(c.Key(key)).
		FillWidth().
		Gap(Space2).
		Children(func() {
			area := ui.TextArea(c.Key(key+"-textarea"), &draft).
				Label(label).
				Lines(5, 12).
				FillWidth().
				FontSize(uiFont(TypeFootnote)).
				TextColor(ThemePrimaryText(c)).
				Background(ThemeSurface(c)).
				Border(Hairline, ThemeBorder(c)).
				Radius(RadiusSM).
				Padding(Space2, Space3).
				AutoFocus().
				OnChange(func() {
					st.mu.Lock()
					st.editText = draft
					st.mu.Unlock()
				})
			area.OnTextKey(func(k ui.TextKey) bool {
				if k.Key == ui.KeyEscape {
					onCancel()
					return true
				}
				return false
			})
			ui.Row(c.Key(key + "-actions")).
				FillWidth().
				Gap(Space2).
				Justify(ui.End).
				Children(func() {
					Button(c, ButtonOpts{
						Key:     key + "-cancel",
						Text:    "取消",
						Label:   "取消",
						Size:    ControlSM,
						Variant: ButtonGhost,
						OnClick: onCancel,
					})
					Button(c, ButtonOpts{
						Key:      key + "-save",
						Text:     "保存",
						Label:    "保存",
						Size:     ControlSM,
						Variant:  ButtonPrimary,
						Disabled: sending || len(draft) == 0,
						OnClick:  onSave,
					})
				})
		})
}
