package mcodeui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
)

// 计划阅读相关：摘要卡的大纲提取、正文预览截断，以及「阅读全文」大弹窗。
// 摘要卡只显示大纲 + 少量正文预览，全文交给弹窗里的 markdown 视图，
// 避免窄窗口下只读 TextArea 把长计划挤成一小块。

const (
	// planOutlineMaxItems 是摘要卡大纲最多展示的标题条数。
	planOutlineMaxItems = 8
	// planTOCMaxItems 是阅读弹窗目录最多收录的标题条数（比摘要卡宽松得多）。
	planTOCMaxItems = 50
	// planPreviewMaxLines 是摘要卡正文预览的最大行数。
	planPreviewMaxLines = 6
	// planOutlineForTOCThreshold 达到该标题数时，阅读弹窗在文档开头拼一个目录。
	planOutlineForTOCThreshold = 3
	// planReaderWidthRatio/planReaderHeightRatio 是阅读弹窗相对窗口的尺寸上限比例。
	planReaderWidthRatio  float32 = 0.9
	planReaderHeightRatio float32 = 0.9
)

// planHeading 是从计划 Markdown 里提取出来的一条标题。
type planHeading struct {
	Level int    // 1~6，对应 # 的个数
	Text  string // 去掉 # 后的标题文本
}

// planOutline 从计划正文里提取 Markdown 标题，最多返回 limit 条。
// 只认行首（允许缩进）连续 1~6 个 # 加空格的 ATX 标题行；围栏代码块（``` / ~~~）内部的 # 行忽略。
func planOutline(plan string, limit int) []planHeading {
	if limit <= 0 {
		return nil
	}
	var out []planHeading
	fence := "" // 当前所处围栏代码块的围栏标记（"```" 或 "~~~"），空串表示不在代码块里
	for _, line := range strings.Split(plan, "\n") {
		trimmed := strings.TrimSpace(line)
		if marker := fenceMarker(trimmed); marker != "" {
			if fence == "" {
				fence = marker
			} else if fence == marker {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		h, ok := parsePlanHeading(line)
		if !ok {
			continue
		}
		out = append(out, h)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// fenceMarker 返回代码块围栏行的标记（``` 或 ~~~），不是围栏行时返回空串。
func fenceMarker(trimmed string) string {
	for _, marker := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, marker) {
			return marker
		}
	}
	return ""
}

// parsePlanHeading 解析一行 ATX 标题：行首（允许缩进）1~6 个 # 后跟空格或行尾。
func parsePlanHeading(line string) (planHeading, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return planHeading{}, false
	}
	rest := trimmed[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return planHeading{}, false
	}
	text := strings.TrimSpace(rest)
	if text == "" {
		return planHeading{}, false
	}
	return planHeading{Level: level, Text: text}, true
}

// planPreviewLines 返回计划正文的前 n 行，用于摘要卡的预览。
func planPreviewLines(plan string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Split(plan, "\n")
	if len(lines) <= n {
		return plan
	}
	return strings.Join(lines[:n], "\n")
}

// planReaderDocument 返回阅读弹窗里要显示的 Markdown：标题数达到阈值时，在正文前拼一个目录。
// 目录项是 [标题](#slug) 形式的链接，slug 与 markdown 视图给标题生成的锚点一致，点击即可跳转。
func planReaderDocument(plan string, headings []planHeading) string {
	if len(headings) < planOutlineForTOCThreshold {
		return plan
	}
	var b strings.Builder
	b.WriteString("## 目录\n\n")
	for _, h := range headings {
		b.WriteString(fmt.Sprintf("- [%s](#%s)\n", h.Text, markdown.Slug(h.Text)))
	}
	b.WriteString("\n")
	b.WriteString(plan)
	return b.String()
}

// renderPlanOutline 渲染摘要卡的大纲列表；没有标题时什么都不画。
func renderPlanOutline(c *ui.Context, key string, headings []planHeading) {
	if len(headings) == 0 {
		return
	}
	ui.Column(c.Key(key)).FillWidth().Gap(Space0).Children(func() {
		for i, h := range headings {
			idx, level, text := i, h.Level, h.Text
			// 用层级缩进体现大纲结构，最多缩进两级。
			indent := float32(0)
			if level > 1 {
				indent = float32(min(level-1, 2)) * 10
			}
			ui.Row(c.Key(fmt.Sprintf("%s-%d", key, idx))).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
				if indent > 0 {
					ui.Box(c).Size(indent, Hairline).Shrink(0)
				}
				ui.Text(c, text).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).SingleLine()
			})
		}
	})
}

// planReaderEntry 是阅读弹窗当前要显示的计划：来自待批请求或已裁决的历史计划。
type planReaderEntry struct {
	Plan   string // 计划正文
	PlanID string // 历史计划的 resolvedPlan.ID；待批计划为空
	Title  string // 弹窗标题
}

// planReaderPlanForLocked 按 target 查找阅读弹窗要显示的计划正文。
// target 先按待批计划的 RequestID 找，再按历史计划的 resolvedPlan.ID 找；找不到返回 false。
// 调用方需持有 s.mu。
func (s *chatState) planReaderPlanForLocked(target string) (planReaderEntry, bool) {
	if s == nil || target == "" {
		return planReaderEntry{}, false
	}
	for _, a := range s.approvals {
		if a.RequestID == target && a.Kind == ApprovalKindPlan {
			return planReaderEntry{Plan: a.Detail, Title: approvalCardTitle(a)}, true
		}
	}
	for _, plans := range s.resolvedPlans {
		for _, p := range plans {
			if p.ID != target {
				continue
			}
			title := "计划 · Approved"
			if !p.Approved {
				title = "计划 · Dismissed"
			}
			return planReaderEntry{Plan: p.Plan, PlanID: p.ID, Title: title}, true
		}
	}
	return planReaderEntry{}, false
}

// openPlanReader 打开阅读弹窗，target 为待批请求 RequestID 或历史计划 ID。
func (s *chatState) openPlanReader(target string) {
	if s == nil || target == "" {
		return
	}
	s.mu.Lock()
	s.planReaderOpen = true
	s.planReaderTarget = target
	s.mu.Unlock()
	s.invalidate()
}

// closePlanReader 关闭阅读弹窗并清空目标。
func (s *chatState) closePlanReader() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.planReaderOpen = false
	s.planReaderTarget = ""
	s.mu.Unlock()
	s.invalidate()
}

// renderPlanReaderModal 渲染计划阅读大弹窗：约窗口 90% 宽高，正文可滚动。
// 待批计划的操作栏为「复制 | 驳回 | 批准计划」，历史计划为「复制 | 关闭」。
func renderPlanReaderModal(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	s.mu.Lock()
	open := s.planReaderOpen
	target := s.planReaderTarget
	entry, found := s.planReaderPlanForLocked(target)
	responding := s.respondingApproval > 0
	var approval Approval
	var draft *planDraft
	hasApproval := false
	if found && entry.PlanID == "" {
		for _, a := range s.approvals {
			if a.RequestID == target {
				approval, draft = a, s.planDraftFor(target)
				hasApproval = true
				break
			}
		}
	}
	s.mu.Unlock()

	if !open {
		modalDragReset(modalDragPlanReader)
		return
	}
	if !found {
		// 计划已被裁决、请求已消失或会话已切换：弹窗自动关闭。
		modalDragReset(modalDragPlanReader)
		s.closePlanReader()
		return
	}

	declining := false
	if draft != nil {
		s.mu.Lock()
		declining = draft.declining
		s.mu.Unlock()
	}

	winW, winH := c.Size()
	maxW, maxH := modalResizeSize(modalDragPlanReader, winW*planReaderWidthRatio, winH*planReaderHeightRatio, 360, 240, winW, winH)

	headings := planOutline(entry.Plan, planTOCMaxItems)
	doc := planReaderDocument(entry.Plan, headings)

	closeReader := func() {
		modalDragReset(modalDragPlanReader)
		s.closePlanReader()
	}

	modalDragModal(c, modalDragPlanReader, &open, func() {
		// 只负责尺寸：卡片取窗口约 90%×90%，再按 90% 比例收至窗口内（不用 100%，
		// 避免贴满时看起来像页面而不是弹窗）。外观全部由卡片自己画。
		card := ui.Column(c.Key("plan-reader-card")).
			Label("plan-reader-card").
			Width(maxW).
			Height(maxH).
			Radius(RadiusXL).
			Background(ThemeWindowBg(c)).
			Border(Hairline, ThemeBorder(c))
		card = applyElevation(c, card, ElevDialog)
		// 弹窗自己的快捷键用覆盖层注册，优先级高于窗口级绑定：Esc 交给 DialogBase 的遮罩处理
		// （只关弹窗，绝不驳回），⌘↩ 批准正在读的这份计划，单独 Enter 不批准以免误触。
		if hasApproval && !responding && card.OverlayShortcut(ui.Cmd, ui.KeyEnter) {
			if optID := findApprovalOption(approval, "approve"); optID != "" {
				closeReader()
				s.respondApproval(approval, optID)
			}
		}
		card.Children(func() {
			renderPlanReaderHeader(c, entry, closeReader)
			ui.Scroll(c.Key("plan-reader-scroll-"+target)).
				Grow(1).
				FillWidth().
				MinHeight(0).
				Padding(Space5, Space6).
				Children(func() {
					markdown.View(c.Key("plan-reader-markdown-"+target), doc, &s.mdStaticOpts)
				})
			renderPlanReaderFeedbackRow(c, approval, draft, hasApproval && declining)
			renderPlanReaderActions(c, s, entry, approval, draft, hasApproval, responding, declining, closeReader)
			modalResizeGrips(c, modalDragPlanReader, maxW, maxH)
		})
	})

	// DialogBase 已经把遮罩点击与 Esc 写回 open，这里只同步状态（不在这里触发重绘，
	// 避免在渲染里再排一帧）；下一页 modalDragModal 会自动 modalDragReset。
	if !open {
		modalDragReset(modalDragPlanReader)
		s.mu.Lock()
		s.planReaderOpen = false
		s.planReaderTarget = ""
		s.mu.Unlock()
	}
}

// renderPlanReaderHeader 渲染弹窗标题行：图标、标题与右上角关闭按钮，整行兼作拖拽把手。
func renderPlanReaderHeader(c *ui.Context, entry planReaderEntry, closeReader func()) {
	accent := ThemeAccent(c)
	modalDragHandle(c, modalDragPlanReader, ui.Row(c.Key("plan-reader-header")).
		FillWidth().
		Padding(Space4, Space5).
		Gap(Space3).
		AlignItems(ui.Center).
		BorderWidth(0, 0, Hairline, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			ui.Icon(c, IconClipboardList()).Size(IconMD, IconMD).TextColor(accent)
			ui.Text(c, entry.Title).Bold().FontSize(TypeProse).TextColor(ThemePrimaryText(c)).SingleLine()
			ui.Spacer(c)
			IconButton(c.Key("plan-reader-close-btn"), IconButtonProps{
				Icon:    IconX(),
				Label:   "关闭",
				OnClick: closeReader,
				Variant: ButtonGhost,
				Size:    ControlSM,
			})
		}))
}

// renderPlanReaderFeedbackRow 渲染待批计划在弹窗里展开的驳回理由输入框（位于操作栏上方）。
// show 为 false 时占位但高度为零，不会把滚动区推上去。
func renderPlanReaderFeedbackRow(c *ui.Context, approval Approval, draft *planDraft, show bool) {
	if !show || draft == nil {
		return
	}
	ui.Row(c.Key("plan-reader-feedback-row")).
		FillWidth().
		Padding(Space3, Space5, 0, Space5).
		Children(func() {
			ui.TextArea(c.Key("plan-reader-feedback-"+approval.RequestID), &draft.feedback).
				Lines(2, 5).
				FontSize(TypeBody).
				LineHeight(1.3).
				Placeholder("驳回理由（可选），将反馈给 Agent").
				FillWidth().
				Padding(Space3, Space3).
				Radius(RadiusMD).
				Background(ThemeSurface(c)).
				Border(Hairline, ThemeBorder(c))
		})
}

// renderPlanReaderActions 渲染弹窗底部固定操作栏：待批计划为「复制 | 驳回 | 批准计划」，
// 历史计划为「复制 | 关闭」；驳回展开后变为「取消 | 确认驳回」。
func renderPlanReaderActions(c *ui.Context, s *chatState, entry planReaderEntry, approval Approval, draft *planDraft, hasApproval, responding, declining bool, closeReader func()) {
	ui.Row(c.Key("plan-reader-actions")).
		FillWidth().
		Padding(Space3, Space5).
		Gap(Space3).
		AlignItems(ui.Center).
		BorderWidth(Hairline, 0, 0, 0).
		BorderColor(ThemeBorder(c)).
		Children(func() {
			// 复制：历史计划与待批计划都提供。
			renderPlanCopyButton(c, "plan-reader-copy", entry.Plan)
			ui.Spacer(c)
			if !hasApproval {
				renderApprovalButton(c, "plan-reader-dismiss", "关闭", "关闭 (Esc)", false, false, closeReader)
				return
			}
			if declining && draft != nil {
				renderApprovalButton(c, "plan-reader-cancel", "取消", "取消驳回", false, responding, func() {
					s.mu.Lock()
					draft.declining = false
					s.mu.Unlock()
					s.invalidate()
				})
				renderApprovalButton(c, "plan-reader-confirm-decline", "确认驳回", "确认驳回", true, responding, func() {
					s.mu.Lock()
					answers := planDeclineAnswers(draft.feedback)
					s.mu.Unlock()
					closeReader()
					s.respondAnswers(approval, findApprovalOption(approval, "decline"), answers, false)
				})
				return
			}
			renderApprovalButton(c, "plan-reader-decline", "驳回", "驳回计划", false, responding, func() {
				s.mu.Lock()
				draft.declining = true
				s.mu.Unlock()
				s.invalidate()
			})
			renderApprovalButton(c, "plan-reader-approve", "批准计划", "批准 (⌘↩)", true, responding, func() {
				if optID := findApprovalOption(approval, "approve"); optID != "" {
					closeReader()
					s.respondApproval(approval, optID)
				}
			})
		})
}
