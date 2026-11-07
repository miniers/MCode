package mcodeui

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/git"
)

// findApprovalOption 查找指定 role 或标题对应的审批选项 ID。
func findApprovalOption(approval Approval, role string) string {
	for _, opt := range approval.Options {
		if opt.Role == role {
			return opt.ID
		}
	}
	for _, opt := range approval.Options {
		if role == "approve" && (opt.Title == "允许" || strings.Contains(opt.Title, "允许")) {
			return opt.ID
		}
		if role == "decline" && (opt.Title == "拒绝" || strings.Contains(opt.Title, "拒绝")) {
			return opt.ID
		}
	}
	if len(approval.Options) > 0 {
		if role == "approve" {
			return approval.Options[0].ID
		}
		return approval.Options[len(approval.Options)-1].ID
	}
	return ""
}

// cmdReturnApprovalTarget 返回 ⌘Return 应批准的审批请求。
// 判定规则：仅当未在答复中（responding=false）、composer 输入框未聚焦（composerFocused=false），
// 且列表中存在 Kind 不是 question 的待处理审批时返回第一个这样的审批与 true；计划也算可批准。
// 原因：P0-06 让 composer 聚焦时的 ⌘Return 用于消息入队，而 MyGo 中窗口级快捷键一旦注册，
// 聚焦输入框的 OnTextKey 就收不到 ⌘Return，所以 composer 聚焦时必须不注册，避免两者冲突；
// 提问需要先选答案，不能一键批准。
func cmdReturnApprovalTarget(approvals []Approval, responding, composerFocused bool) (Approval, bool) {
	if responding || composerFocused {
		return Approval{}, false
	}
	for _, a := range approvals {
		if a.Kind != ApprovalKindQuestion {
			return a, true
		}
	}
	return Approval{}, false
}

// ApprovalActionKind 表示审批快捷键触发的动作种类。
type ApprovalActionKind string

const (
	ApprovalActionNone         ApprovalActionKind = ""
	ApprovalActionSelectChoice ApprovalActionKind = "select_choice" // 问题卡片：选中/切换第 N 个选项
	ApprovalActionOption       ApprovalActionKind = "option"        // 普通卡片：触发第 N 个 Option
	ApprovalActionApprove      ApprovalActionKind = "approve"       // 普通卡片：Enter 批准
	ApprovalActionSubmit       ApprovalActionKind = "submit"        // 问题卡片：Enter 提交答案
	ApprovalActionDecline      ApprovalActionKind = "decline"       // Esc 拒绝或跳过
)

// ApprovalShortcutAction 描述单次按键在审批卡片上产生的动作。
type ApprovalShortcutAction struct {
	Kind     ApprovalActionKind
	Index    int    // 0-indexed choice or option 下标
	OptionID string // 对应的 Option ID
}

// approvalShortcutAction 纯函数：根据当前顶层审批卡片、按键事件与聚焦/草稿状态判定应执行的审批动作。
// 规则：
// 1. responding 为 true 或审批卡片为空时，不响应任何快捷键；
// 2. 修饰键不为 0 时，不响应；
// 3. 输入框聚焦且草稿非空时不拦截 Enter 和数字键（composerFocused && composerHasDraft）；
// 4. 问题卡片的“其他”文本框聚焦时不拦截 Enter 和数字键（otherFocused）；
// 5. 问题卡片：
//   - 数字键 1~9：若下标在当前题目选项范围内，返回 ApprovalActionSelectChoice；
//   - Enter：所有题目都已回答时返回 ApprovalActionSubmit；
//   - Esc：返回 ApprovalActionDecline；
//
// 6. 普通审批卡片：
//   - 数字键 1~9：若下标在 options 范围内，返回 ApprovalActionOption；
//   - Enter：若存在 approve 选项，返回 ApprovalActionApprove；
//   - Esc：若存在 decline 选项，返回 ApprovalActionDecline。
func approvalShortcutAction(
	target Approval,
	key ui.Key,
	mods ui.Modifiers,
	responding bool,
	composerFocused bool,
	composerHasDraft bool,
	otherFocused bool,
	questionAllAnswered bool,
	currentQuestionChoicesCount int,
) ApprovalShortcutAction {
	if responding || target.RequestID == "" || mods != 0 {
		return ApprovalShortcutAction{}
	}

	isTextInputBusy := (composerFocused && composerHasDraft) || otherFocused

	switch {
	case key == ui.KeyEscape:
		if target.Kind == ApprovalKindQuestion {
			return ApprovalShortcutAction{Kind: ApprovalActionDecline}
		}
		declineOpt := findApprovalOption(target, "decline")
		if declineOpt == "" {
			declineOpt = findApprovalOption(target, "cancel")
		}
		return ApprovalShortcutAction{Kind: ApprovalActionDecline, OptionID: declineOpt}

	case key == ui.KeyEnter:
		if isTextInputBusy {
			return ApprovalShortcutAction{}
		}
		if target.Kind == ApprovalKindQuestion {
			if questionAllAnswered {
				return ApprovalShortcutAction{Kind: ApprovalActionSubmit}
			}
			return ApprovalShortcutAction{}
		}
		approveOpt := findApprovalOption(target, "approve")
		if approveOpt != "" {
			return ApprovalShortcutAction{Kind: ApprovalActionApprove, OptionID: approveOpt}
		}
		return ApprovalShortcutAction{}

	case key >= ui.Key1 && key <= ui.Key9:
		if isTextInputBusy {
			return ApprovalShortcutAction{}
		}
		idx := int(key - ui.Key1)
		if target.Kind == ApprovalKindQuestion {
			if idx >= 0 && idx < currentQuestionChoicesCount {
				return ApprovalShortcutAction{Kind: ApprovalActionSelectChoice, Index: idx}
			}
			return ApprovalShortcutAction{}
		}
		if idx >= 0 && idx < len(target.Options) {
			return ApprovalShortcutAction{
				Kind:     ApprovalActionOption,
				Index:    idx,
				OptionID: target.Options[idx].ID,
			}
		}
		return ApprovalShortcutAction{}
	}

	return ApprovalShortcutAction{}
}

// batchApprovals 返回可参与“全部允许/全部拒绝”的审批（排除提问与计划）。
func batchApprovals(approvals []Approval) []Approval {
	var res []Approval
	for _, a := range approvals {
		if a.Kind != ApprovalKindQuestion && a.Kind != ApprovalKindPlan {
			res = append(res, a)
		}
	}
	return res
}

// formatApprovalToolName 把 MCP 工具名 mcp__server__tool 拆成“server · tool”，其他名称原样返回。
func formatApprovalToolName(name string) string {
	rest, ok := strings.CutPrefix(name, "mcp__")
	if !ok {
		return name
	}
	if server, tool, found := strings.Cut(rest, "__"); found && server != "" && tool != "" {
		return server + " · " + tool
	}
	return name
}

// joinApprovalTitle 拼接卡片标题前缀与工具名，工具名为空或与前缀相同时只返回前缀。
func joinApprovalTitle(prefix, name string) string {
	if name == "" || name == prefix {
		return prefix
	}
	return prefix + " · " + name
}

// approvalCardTitle 按审批类型返回卡片标题；Kind 为空或未知时沿用 Title。
func approvalCardTitle(a Approval) string {
	switch a.Kind {
	case ApprovalKindCommand:
		return joinApprovalTitle("执行命令", a.Title)
	case ApprovalKindFileChange:
		return "修改文件"
	case ApprovalKindTool, ApprovalKindPermissions:
		return joinApprovalTitle("调用工具", formatApprovalToolName(a.Title))
	case ApprovalKindCommit:
		return "提交代码"
	case ApprovalKindPlan:
		return "计划"
	case ApprovalKindQuestion:
		return "提问"
	}
	return a.Title
}

// commitApprovalPrompt 是提交类审批的说明文字。
const commitApprovalPrompt = "修改已完成，是否提交代码并将任务标记为已完成？"

// isRecommendedChoice 判断选项标签是否标注了推荐（含 "recommended" 或“推荐”，不区分大小写）。
func isRecommendedChoice(label string) bool {
	return strings.Contains(strings.ToLower(label), "recommended") || strings.Contains(label, "推荐")
}

// recommendedLabelMarks 列出标签末尾表示推荐的写法，显示时去掉，只影响显示不影响提交值。
var recommendedLabelMarks = []string{"(recommended)", "（推荐）", "(推荐)", "（recommended）", "recommended", "推荐"}

// displayChoiceLabel 去掉选项标签末尾的推荐标注（如 " (Recommended)"、"（推荐）"），
// 返回用于显示的纯文字；只剩标注本身时保持原样，避免显示空白。
func displayChoiceLabel(label string) string {
	text := strings.TrimSpace(label)
	for {
		next := text
		for _, mark := range recommendedLabelMarks {
			if len(next) <= len(mark) || !strings.EqualFold(next[len(next)-len(mark):], mark) {
				continue
			}
			next = strings.TrimSpace(next[:len(next)-len(mark)])
			break
		}
		if next == text {
			return text
		}
		text = next
	}
}

// defaultQuestionChoices 返回题目初始默认选中的选项下标：单选取第一个推荐项，多选取全部推荐项。
func defaultQuestionChoices(q ApprovalQuestion) []int {
	var res []int
	for i, ch := range q.Choices {
		if isRecommendedChoice(ch.Label) {
			res = append(res, i)
			if !q.AllowsMultiple {
				break
			}
		}
	}
	return res
}

// newQuestionDraft 按题目列表创建草稿，并预选推荐项。
func newQuestionDraft(qs []ApprovalQuestion) *questionDraft {
	d := &questionDraft{
		selected: make([][]int, len(qs)),
		others:   make([]string, len(qs)),
	}
	for i, q := range qs {
		d.selected[i] = defaultQuestionChoices(q)
	}
	return d
}

// toggleQuestionChoice 返回点击第 idx 个选项后的选中下标：单选直接替换，多选切换并保持升序。
func toggleQuestionChoice(q ApprovalQuestion, selected []int, idx int) []int {
	if idx < 0 || idx >= len(q.Choices) {
		return selected
	}
	if !q.AllowsMultiple {
		return []int{idx}
	}
	if i := slices.Index(selected, idx); i >= 0 {
		return slices.Delete(slices.Clone(selected), i, i+1)
	}
	res := append(slices.Clone(selected), idx)
	slices.Sort(res)
	return res
}

// questionShowsOther 判断题目是否显示填空输入框：允许其他答案或没有任何选项。
func questionShowsOther(q ApprovalQuestion) bool {
	return q.AllowsOther || len(q.Choices) == 0
}

// questionAnswerValues 返回单题答案；单选填空替换选项，多选填空追加到选项后。
func questionAnswerValues(q ApprovalQuestion, selected []int, other string) []string {
	text := strings.TrimSpace(other)
	if questionShowsOther(q) && !q.AllowsMultiple && text != "" {
		return []string{text}
	}
	var res []string
	for _, idx := range selected {
		if idx >= 0 && idx < len(q.Choices) {
			res = append(res, q.Choices[idx].Label)
		}
	}
	if questionShowsOther(q) {
		if text != "" {
			res = append(res, text)
		}
	}
	return res
}

// questionAnswered 判断单题是否已作答（至少一个选项或非空填空）。
func questionAnswered(q ApprovalQuestion, selected []int, other string) bool {
	return len(questionAnswerValues(q, selected, other)) > 0
}

// unansweredQuestionCount 统计尚未作答的题数。
func unansweredQuestionCount(qs []ApprovalQuestion, selected [][]int, others []string) int {
	n := 0
	for i, q := range qs {
		if !questionAnswered(q, draftAt(selected, i), draftAt(others, i)) {
			n++
		}
	}
	return n
}

// buildQuestionAnswers 组装 requests.respond 的 answers：键为题目 ID，值为该题答案列表。
func buildQuestionAnswers(qs []ApprovalQuestion, selected [][]int, others []string) map[string][]string {
	answers := make(map[string][]string, len(qs))
	for i, q := range qs {
		if vals := questionAnswerValues(q, draftAt(selected, i), draftAt(others, i)); len(vals) > 0 {
			answers[q.ID] = vals
		}
	}
	return answers
}

// planDeclineAnswers 返回计划驳回的 answers：反馈非空时为 {"feedback": [反馈]}，否则为 nil。
func planDeclineAnswers(feedback string) map[string][]string {
	if t := strings.TrimSpace(feedback); t != "" {
		return map[string][]string{"feedback": {t}}
	}
	return nil
}

// draftAt 安全读取草稿切片的第 i 项，越界时返回零值。
func draftAt[T any](list []T, i int) T {
	var zero T
	if i < 0 || i >= len(list) {
		return zero
	}
	return list[i]
}

// pendingApprovalByID 返回仍待处理、且 RequestID 相同的审批卡片；卡片已被响应或移出列表时返回 false。
// 快捷键在同一帧内可能触发多次（如数字键批准后 Enter/Esc 也命中），借此避免对同一张卡片重复回调后端。
func pendingApprovalByID(s *chatState, requestID string) (Approval, bool) {
	if s == nil || requestID == "" {
		return Approval{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.approvals {
		if a.RequestID == requestID {
			return a, true
		}
	}
	return Approval{}, false
}

// renderApproval 渲染当前会话待处理的审批卡片，停靠在输入框正上方，宽度与输入框对齐。
// 计划「阅读全文」大弹窗也挂在这里：它只在聊天会话里出现，随审批卡片一同渲染即可，
// 不必改根部挂载点。
func renderApproval(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	// 弹窗先于卡片渲染：它是覆盖层，外观与卡片无关；提前渲染也能让快捷键门控看到最新状态。
	renderPlanReaderModal(c, s)
	s.mu.Lock()
	if len(s.approvals) == 0 {
		s.mu.Unlock()
		return
	}
	approvals := make([]Approval, len(s.approvals))
	copy(approvals, s.approvals)
	// 在途响应未全部返回前，卡片与批量按钮保持锁定：批量允许会并发发出多个请求。
	responding := s.respondingApproval > 0
	composerFocused := s.composerFocused
	composerHasDraft := strings.TrimSpace(s.draft) != ""
	otherFocused := s.questionOtherFocused
	// 计划阅读弹窗打开时，卡片上的快捷键必须让位：Esc 只能关弹窗，绝不能驳回计划。
	readerOpen := s.planReaderOpen
	s.mu.Unlock()

	// 快捷键仅对最上面一张卡片生效，且仅在 !responding 时生效；
	// 计划阅读弹窗打开时数字键、Enter、Esc 都归弹窗，卡片一个都不接。
	if len(approvals) > 0 && !responding && !readerOpen {
		top := approvals[0]
		var questionAllAnswered bool
		var curChoicesCount int
		var qDraft *questionDraft
		var curQ ApprovalQuestion
		if top.Kind == ApprovalKindQuestion {
			s.mu.Lock()
			qDraft = s.questionDraftFor(top)
			qs := top.Questions
			unanswered := unansweredQuestionCount(qs, qDraft.selected, qDraft.others)
			questionAllAnswered = (unanswered == 0 && len(qs) > 0)
			qi := qDraft.current
			if qi >= 0 && qi < len(qs) {
				curQ = qs[qi]
				curChoicesCount = len(curQ.Choices)
			}
			s.mu.Unlock()
		}

		// 数字键 1~9
		for k := ui.Key1; k <= ui.Key9; k++ {
			if c.Shortcut(0, k) {
				action := approvalShortcutAction(top, k, 0, responding, composerFocused, composerHasDraft, otherFocused, questionAllAnswered, curChoicesCount)
				switch action.Kind {
				case ApprovalActionSelectChoice:
					s.mu.Lock()
					qi := qDraft.current
					if qi < len(qDraft.selected) {
						qDraft.selected[qi] = toggleQuestionChoice(curQ, qDraft.selected[qi], action.Index)
						if !curQ.AllowsMultiple {
							qDraft.others[qi] = ""
						}
					}
					s.mu.Unlock()
					s.invalidate()
				case ApprovalActionOption:
					// 只认仍在待办列表中的同一张卡片：本轮已通过数字键批准的卡片不能再按 Esc 拒绝
					if action.OptionID != "" {
						if target, ok := pendingApprovalByID(s, top.RequestID); ok {
							s.respondApproval(target, action.OptionID)
						}
					}
				}
			}
		}

		// Enter 键
		if c.Shortcut(0, ui.KeyEnter) {
			action := approvalShortcutAction(top, ui.KeyEnter, 0, responding, composerFocused, composerHasDraft, otherFocused, questionAllAnswered, curChoicesCount)
			switch action.Kind {
			case ApprovalActionSubmit:
				s.mu.Lock()
				answers := buildQuestionAnswers(top.Questions, qDraft.selected, qDraft.others)
				s.mu.Unlock()
				s.respondAnswers(top, "", answers, false)
			case ApprovalActionApprove:
				// 只认仍在待办列表中的同一张卡片：本轮已通过数字键批准的卡片不能再按 Esc 拒绝
				if action.OptionID != "" {
					if target, ok := pendingApprovalByID(s, top.RequestID); ok {
						s.respondApproval(target, action.OptionID)
					}
				}
			}
		}

		// Esc 键
		if c.Shortcut(0, ui.KeyEscape) {
			action := approvalShortcutAction(top, ui.KeyEscape, 0, responding, composerFocused, composerHasDraft, otherFocused, questionAllAnswered, curChoicesCount)
			switch action.Kind {
			case ApprovalActionDecline:
				if top.Kind == ApprovalKindQuestion {
					s.respondAnswers(top, "", nil, true)
				} else {
					if action.OptionID != "" {
						if target, ok := pendingApprovalByID(s, top.RequestID); ok {
							s.respondApproval(target, action.OptionID)
						}
					}
				}
			}
		}
	}

	// ⌘Return 批准第一个可批准的审批；不满足 cmdReturnApprovalTarget 时不注册，把按键留给 composer。
	// 阅读弹窗打开时不在这里注册：⌘Return 归弹窗的覆盖层（批准正在读的那份计划并关窗），
	// 否则历史计划弹窗里按 ⌘Return 会误批准另一张待批卡片。
	if !readerOpen {
		if target, ok := cmdReturnApprovalTarget(approvals, responding, composerFocused); ok {
			if c.Shortcut(ui.Cmd, ui.KeyEnter) {
				if optID := findApprovalOption(target, "approve"); optID != "" {
					s.respondApproval(target, optID)
				}
			}
		}
	}

	batch := batchApprovals(approvals)
	ui.Row(c.Key("approval-container")).Justify(ui.Center).Padding(0, Space6, Space3, Space6).Children(func() {
		ui.Column(c.Key("approval-stack")).Grow(1).MaxWidth(composerDockMaxWidth).Gap(Space4).
			// 卡片出现时淡入、自下而上 12pt 并轻微放大就位；消失时反向收起。
			Transition(ui.ElementTransition{
				Duration: 260 * time.Millisecond,
				Enter:    &ui.Motion{Y: 12, Opacity: 0, Scale: 0.96},
				Exit:     &ui.Motion{Y: 12, Opacity: 0, Scale: 0.96},
			}).
			Children(func() {
				if len(batch) >= 2 {
					ui.Row(c.Key("approval-batch-row")).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("%d 个待审批", len(batch))).
							Bold().
							FontSize(TypeBody).
							TextColor(ThemePrimaryText(c))
						ui.Spacer(c)
						ui.Row(c.Key("approval-batch-actions")).Gap(Space3).AlignItems(ui.Center).Children(func() {
							// 批量允许：主色实心胶囊
							ui.ButtonBase(c.Key("approval-batch-allow")).
								Label("全部允许").
								Tooltip("全部允许").
								Disabled(responding).
								Padding(Space1, Space4).
								Radius(RadiusXL).
								Background(ThemeAccent(c)).
								OnClick(func() {
									for _, appr := range batch {
										optID := findApprovalOption(appr, "approve")
										if optID != "" {
											s.respondApproval(appr, optID)
										}
									}
								}).
								Children(func() {
									ui.Text(c, "全部允许").
										FontSize(TypeFootnote).
										Bold().
										TextColor(ThemeOnAccent(c))
								})

							// 批量拒绝：浅底胶囊
							ui.ButtonBase(c.Key("approval-batch-decline")).
								Label("全部拒绝").
								Tooltip("全部拒绝").
								Disabled(responding).
								Padding(Space1, Space4).
								Radius(RadiusXL).
								Background(ThemeHover(c)).
								OnClick(func() {
									for _, appr := range batch {
										optID := findApprovalOption(appr, "decline")
										if optID != "" {
											s.respondApproval(appr, optID)
										}
									}
								}).
								Children(func() {
									ui.Text(c, "全部拒绝").
										FontSize(TypeFootnote).
										TextColor(ThemePrimaryText(c))
								})
						})
					})
				}
				for _, approval := range approvals {
					appr := approval
					renderApprovalCard(c, s, appr, responding)
				}
			})
	})
}

// renderApprovalCard 按审批类型分发渲染单张审批请求卡片。
func renderApprovalCard(c *ui.Context, s *chatState, approval Approval, responding bool) {
	id := approval.RequestID
	switch approval.Kind {
	case ApprovalKindQuestion:
		renderQuestionCard(c, s, approval, responding)
	case ApprovalKindPlan:
		renderPlanCard(c, s, approval, responding)
	case ApprovalKindCommand:
		renderTypedApprovalCard(c, s, approval, responding, IconTerminal(), func() {
			ui.Column(c.Key("approval-command-" + id)).Children(func() {
				renderApprovalCode(c, "approval-detail-"+id, approval.Detail)
			})
		})
	case ApprovalKindFileChange:
		renderTypedApprovalCard(c, s, approval, responding, IconSquarePen(), func() {
			// 路径取自 Title，等宽单行显示
			if approval.Title != "" {
				ui.Row(c.Key("approval-path-" + id)).Children(func() {
					ui.Text(c, approval.Title).Font("monospace").FontSize(TypeFootnote).TextColor(ThemePrimaryText(c)).Selectable()
				})
			}
			if approval.Diff != "" {
				diffData := []byte(approval.Diff)
				if !bytes.Contains(diffData, []byte("diff --git ")) {
					diffData = append([]byte("diff --git a/file b/file\n"), diffData...)
				}
				files, err := git.ParseUnifiedDiff(diffData)
				if err == nil && len(files) > 0 {
					ui.Scroll(c.Key("approval-diff-scroll-"+id)).
						FillWidth().
						MaxHeight(180).
						Radius(RadiusMD).
						Background(ThemeHover(c)).
						Border(Hairline, ThemeBorder(c)).
						Padding(Space3, Space3).
						Selectable().
						Children(func() {
							ui.Column(c.Key("approval-diff-list-" + id)).
								FillWidth().
								Gap(Space3).
								Selectable().
								Children(func() {
									for idx, file := range files {
										renderInlineToolDiffFile(c, "approval-"+id, idx, file)
									}
								})
						})
					return
				}
			}
			renderApprovalCode(c, "approval-detail-"+id, approval.Detail)
		})
	case ApprovalKindTool, ApprovalKindPermissions:
		renderTypedApprovalCard(c, s, approval, responding, IconWrench(), func() {
			ui.Column(c.Key("approval-tool-" + id)).Children(func() {
				renderApprovalCode(c, "approval-detail-"+id, approval.Detail)
			})
		})
	case ApprovalKindCommit:
		renderTypedApprovalCard(c, s, approval, responding, IconGitBranch(), func() {
			ui.Row(c.Key("approval-commit-prompt-" + id)).Children(func() {
				ui.Text(c, commitApprovalPrompt).FontSize(TypeBody).TextColor(ThemePrimaryText(c)).Selectable()
			})
			ui.Column(c.Key("approval-commit-" + id)).Children(func() {
				renderApprovalCode(c, "approval-detail-"+id, approval.Detail)
			})
		})
	default:
		renderLegacyApprovalCard(c, s, approval, responding)
	}
}

// renderApprovalCode 渲染等宽只读代码框，最多显示 8 行，超长截断并支持滚动；text 为空时不渲染。
func renderApprovalCode(c *ui.Context, key, text string) {
	if text == "" {
		return
	}
	detail := text
	ui.TextArea(c.Key(key), &detail).
		ReadOnly(true).
		Lines(1, 8).
		Font("monospace").
		FontSize(TypeFootnote).
		LineHeight(1.3).
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeHover(c)).
		Border(Hairline, ThemeBorder(c))
}

// renderApprovalReason 渲染审批原因说明，以及上次答复未送达的提示；两者都为空时什么都不画。
// DeliveryError 非空说明上一次答复没有投递给 Agent，卡片保留以便用户重新选择，
// 所以这里用危险色明确提示“上次没送达”，否则用户会以为答复已经生效。
func renderApprovalReason(c *ui.Context, approval Approval) {
	if approval.Reason != "" {
		ui.Row(c.Key("approval-reason-row")).Children(func() {
			ui.Text(c, approval.Reason).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).Selectable()
		})
	}
	if notice := approvalDeliveryNotice(approval.DeliveryError); notice != "" {
		ui.Row(c.Key("approval-delivery-row")).Children(func() {
			ui.Text(c, notice).FontSize(TypeFootnote).TextColor(ThemeDanger(c)).Selectable()
		})
	}
}

// approvalDeliveryNotice 拼出“上次答复未送达”提示；没有送达错误时返回空串。
func approvalDeliveryNotice(deliveryError string) string {
	reason := strings.TrimSpace(deliveryError)
	if reason == "" {
		return ""
	}
	return "上次答复未送达：" + reason + "，可重新选择"
}

// approvalAuxText 把审批原因与“上次答复未送达”提示合并成一条 Banner 文案。
func approvalAuxText(approval Approval) string {
	parts := make([]string, 0, 2)
	if reason := strings.TrimSpace(approval.Reason); reason != "" {
		parts = append(parts, reason)
	}
	if notice := approvalDeliveryNotice(approval.DeliveryError); notice != "" {
		parts = append(parts, notice)
	}
	return strings.Join(parts, " · ")
}

// approvalCardFrame 渲染审批卡的统一外壳：SurfaceCard 底色加左侧 2px 语义色条。
// MessageCard 只能整卡描边、放不下左侧色条，因此这里用同一套令牌自绘。
func approvalCardFrame(c *ui.Context, key string, accent ui.Color, body func()) {
	ui.Row(c.Key(key)).
		Clip().
		AlignItems(ui.Stretch).
		Radius(RadiusLG).
		Background(ThemeSurfaceCard(c)).
		Gap(Space3).
		Children(func() {
			ui.Box(c.Key(key + "-accent")).Width(Space0).AlignSelf(ui.Stretch).Background(accent)
			ui.Column(c.Key(key + "-content")).Grow(1).Gap(Space3).Padding(Space5).Children(body)
		})
}

// renderTypedApprovalCard 渲染命令、文件修改、工具、权限与提交类审批卡片：图标标题、正文、原因与右下角操作按钮。
func renderTypedApprovalCard(c *ui.Context, s *chatState, approval Approval, responding bool, icon *ui.SVG, body func()) {
	warnColor := ThemeWarning(c)

	approvalCardFrame(c, "approval-"+approval.RequestID, warnColor, func() {
		ui.Row(c.Key("approval-header-" + approval.RequestID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon).Size(IconMD, IconMD).TextColor(warnColor)
			ui.Text(c, approvalCardTitle(approval)).Bold().FontSize(TypeProse).TextColor(ThemePrimaryText(c)).Selectable()
		})
		body()
		if text := approvalAuxText(approval); text != "" {
			Banner(c, BannerProps{Key: "approval-banner-" + approval.RequestID, Tone: ToneWarning, Text: text})
		}
		renderApprovalOptions(c, s, approval, responding)
	})
}

// renderApprovalOptions 渲染底部操作按钮栏：左侧 Spacer，按钮完全右对齐，批准按钮为主样式。
func renderApprovalOptions(c *ui.Context, s *chatState, approval Approval, responding bool) {
	ui.Row(c.Key("approval-options-" + approval.RequestID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
		ui.Spacer(c)
		for idx, opt := range approval.Options {
			optID := opt.ID
			shortcutHint := ""
			if idx < 9 {
				shortcutHint = fmt.Sprintf("%d", idx+1)
			}
			switch opt.Role {
			case "approve":
				// 主高亮样式：强调色实心胶囊
				tooltip := "批准 (⌘↩ / ↩)"
				if shortcutHint != "" {
					tooltip = fmt.Sprintf("批准 (⌘↩ / ↩ / %s)", shortcutHint)
				}
				ui.ButtonBase(c.Key("approval-opt-"+approval.RequestID+"-"+opt.ID)).
					Label(opt.Title).
					Tooltip(tooltip).
					Disabled(responding).
					Padding(Space1, Space4).
					Radius(RadiusXL).
					Background(ThemeAccent(c)).
					OnClick(func() {
						s.respondApproval(approval, optID)
					}).
					Children(func() {
						ui.Row(c.Key("approval-opt-row-" + approval.RequestID + "-" + opt.ID)).Gap(Space2).AlignItems(ui.Center).Children(func() {
							ui.Text(c, opt.Title).
								FontSize(TypeFootnote).
								Bold().
								TextColor(ThemeOnAccent(c))
							if shortcutHint != "" {
								accentKeyBadge(c, "approval-opt-badge-"+approval.RequestID+"-"+opt.ID, shortcutHint)
							}
						})
					})
			default:
				// 次级样式：浅底胶囊（含 decline 及始终允许等选项）
				tooltip := opt.Title
				if opt.Role == "decline" {
					tooltip = fmt.Sprintf("%s (Esc)", opt.Title)
					if shortcutHint != "" {
						tooltip = fmt.Sprintf("%s (Esc / %s)", opt.Title, shortcutHint)
					}
				} else if shortcutHint != "" {
					tooltip = fmt.Sprintf("%s (%s)", opt.Title, shortcutHint)
				}
				ui.ButtonBase(c.Key("approval-opt-"+approval.RequestID+"-"+opt.ID)).
					Label(opt.Title).
					Tooltip(tooltip).
					Disabled(responding).
					Padding(Space1, Space4).
					Radius(RadiusXL).
					Background(ThemeHover(c)).
					OnClick(func() {
						s.respondApproval(approval, optID)
					}).
					Children(func() {
						ui.Row(c.Key("approval-opt-row-" + approval.RequestID + "-" + opt.ID)).Gap(Space2).AlignItems(ui.Center).Children(func() {
							ui.Text(c, opt.Title).
								FontSize(TypeFootnote).
								TextColor(ThemePrimaryText(c))
							if shortcutHint != "" {
								Kbd(c, "approval-opt-badge-"+approval.RequestID+"-"+opt.ID, shortcutHint)
							}
						})
					})
			}
		}
	})
}

// accentKeyBadge 在实心强调色按钮上画键帽。Kbd 只有浅色底，这里在它外面包一层
// 半透明白底，保证键帽在强调色上仍有清晰边界；通用样式仍由 Kbd 负责。
func accentKeyBadge(c *ui.Context, key, text string) ui.Element {
	return ui.Row(c.Key(key)).
		Padding(Space0, Space1).
		Radius(RadiusXS).
		Background(ThemeOnAccent(c).Alpha(0.18)).
		Children(func() {
			Kbd(c, key+"-cap", text)
		})
}

// renderLegacyApprovalCard 渲染 Kind 为空或未知的审批卡片。
func renderLegacyApprovalCard(c *ui.Context, s *chatState, approval Approval, responding bool) {
	warnColor := ThemeWarning(c)

	approvalCardFrame(c, "approval-"+approval.RequestID, warnColor, func() {
		// 头部：警告图标加标题/工具名
		ui.Row(c.Key("approval-header-" + approval.RequestID)).Gap(Space3).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, IconOctagonAlert()).Size(IconMD, IconMD).TextColor(warnColor)
			ui.Text(c, approval.Title).Bold().FontSize(TypeProse).TextColor(ThemePrimaryText(c)).Selectable()
		})

		// Detail：等宽代码框，最多显示 8 行，超长截断并支持滚动
		renderApprovalCode(c, "approval-detail-"+approval.RequestID, approval.Detail)
		if text := approvalAuxText(approval); text != "" {
			Banner(c, BannerProps{Key: "approval-banner-" + approval.RequestID, Tone: ToneWarning, Text: text})
		}
		renderApprovalOptions(c, s, approval, responding)
	})
}

// renderApprovalButton 渲染卡片内的胶囊按钮，primary 为强调色实心样式。
func renderApprovalButton(c *ui.Context, key, label, tooltip string, primary, disabled bool, onClick func()) {
	bg := ThemeHover(c)
	fg := ThemePrimaryText(c)
	if primary {
		bg = ThemeAccent(c)
		fg = ThemeOnAccent(c)
		if disabled {
			bg = bg.Alpha(0.45)
		}
	}
	ui.ButtonBase(c.Key(key)).
		Label(label).
		Tooltip(tooltip).
		Disabled(disabled).
		Padding(Space1, Space4).
		Radius(RadiusXL).
		Background(bg).
		OnClick(onClick).
		Children(func() {
			t := ui.Text(c, label).FontSize(TypeFootnote).TextColor(fg)
			if primary {
				t.Bold()
			}
		})
}

// renderPlanCopyButton 渲染计划复制按钮。
func renderPlanCopyButton(c *ui.Context, key, planText string) {
	ui.ButtonBase(c.Key(key)).
		Label("复制计划").
		Tooltip("复制计划").
		Padding(Space0, Space2).
		Radius(RadiusXS).
		Background(ThemeHover(c)).
		OnClick(func() {
			c.WriteClipboard(planText)
		}).
		Children(func() {
			ui.Row(c.Key(key + "-row")).Gap(Space1).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconCopy()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
				ui.Text(c, "复制").FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
			})
		})
}

// renderPlanDetailBody 渲染计划正文 Markdown 区域。
func renderPlanDetailBody(c *ui.Context, key, planText string, maxLines int) {
	if planText == "" {
		return
	}
	plan := planText
	ui.Column(c.Key(key + "-wrapper")).Children(func() {
		ui.TextArea(c.Key(key), &plan).
			ReadOnly(true).
			Lines(1, maxLines).
			FontSize(TypeBody).
			LineHeight(1.4).
			Padding(Space3, Space3).
			Radius(RadiusMD).
			Background(ThemeHover(c)).
			Border(Hairline, ThemeBorder(c))
	})
}

// renderPlanCard 渲染计划摘要卡：大纲 + 少量正文预览 + “阅读全文 / 驳回 / 批准计划”。
// 计划正文往往很长，摘要卡只给骨架，全文交给“阅读全文”打开的阅读大弹窗。
func renderPlanCard(c *ui.Context, s *chatState, approval Approval, responding bool) {
	id := approval.RequestID
	s.mu.Lock()
	draft := s.planDraftFor(id)
	declining := draft.declining
	s.mu.Unlock()

	accent := ThemeAccent(c)
	ui.Column(c.Key("approval-"+id)).
		Radius(RadiusLG).
		Background(accent.Alpha(0.06).Over(ThemeSurface(c))).
		Border(Hairline, accent.Alpha(0.25)).
		Padding(Space5).
		Gap(Space3).
		Children(func() {
			// 头部：图标、标题、“待实施”状态胶囊与复制按钮
			ui.Row(c.Key("approval-header-" + id)).Gap(Space3).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconClipboardList()).Size(IconMD, IconMD).TextColor(accent)
				ui.Text(c, approvalCardTitle(approval)).Bold().FontSize(TypeProse).TextColor(ThemePrimaryText(c)).Selectable()
				ui.Spacer(c)
				ui.Row(c.Key("plan-status-"+id)).Padding(Space0, Space3).Radius(RadiusMD).Background(accent.Alpha(0.15)).Children(func() {
					ui.Text(c, "待实施").FontSize(TypeCaption).Bold().TextColor(accent)
				})
				renderPlanCopyButton(c, "plan-copy-"+id, approval.Detail)
			})

			// 大纲：计划正文里的 Markdown 标题，最多 8 条
			renderPlanOutline(c, "plan-outline-"+id, planOutline(approval.Detail, planOutlineMaxItems))
			// 正文预览：只给开头几行，完整内容点“阅读全文”
			renderPlanDetailBody(c, "plan-detail-"+id, planPreviewLines(approval.Detail, planPreviewMaxLines), planPreviewMaxLines)
			renderApprovalReason(c, approval)

			if declining {
				ui.TextArea(c.Key("plan-feedback-"+id), &draft.feedback).
					Lines(2, 5).
					FontSize(TypeBody).
					LineHeight(1.3).
					Placeholder("驳回理由（可选），将反馈给 Agent").
					AutoFocus().
					Padding(Space3, Space3).
					Radius(RadiusMD).
					Background(ThemeSurface(c)).
					Border(Hairline, ThemeBorder(c))
			}

			ui.Row(c.Key("plan-actions-" + id)).Gap(Space3).AlignItems(ui.Center).Children(func() {
				ui.Spacer(c)
				if declining {
					renderApprovalButton(c, "plan-cancel-"+id, "取消", "取消驳回 (Esc)", false, responding, func() {
						s.mu.Lock()
						draft.declining = false
						s.mu.Unlock()
						s.invalidate()
					})
					renderApprovalButton(c, "plan-confirm-decline-"+id, "确认驳回", "确认驳回", true, responding, func() {
						s.mu.Lock()
						answers := planDeclineAnswers(draft.feedback)
						s.mu.Unlock()
						s.respondAnswers(approval, findApprovalOption(approval, "decline"), answers, false)
					})
					return
				}
				renderApprovalButton(c, "plan-read-"+id, "阅读全文", "阅读计划全文", false, false, func() {
					s.openPlanReader(id)
				})
				renderApprovalButton(c, "plan-decline-"+id, "驳回", "驳回计划 (Esc)", false, responding, func() {
					s.mu.Lock()
					draft.declining = true
					s.mu.Unlock()
					s.invalidate()
				})
				renderApprovalButton(c, "plan-approve-"+id, "批准计划", "批准 (⌘↩ / ↩)", true, responding, func() {
					if optID := findApprovalOption(approval, "approve"); optID != "" {
						s.respondApproval(approval, optID)
					}
				})
			})
		})
}

// renderQuestionCard 渲染提问卡片：分题显示单选/多选/填空，底部为未答计数、跳过与提交。
func renderQuestionCard(c *ui.Context, s *chatState, approval Approval, responding bool) {
	id := approval.RequestID
	qs := approval.Questions
	s.mu.Lock()
	draft := s.questionDraftFor(approval)
	if draft.current >= len(qs) {
		draft.current = len(qs) - 1
	}
	if draft.current < 0 {
		draft.current = 0
	}
	current := draft.current
	unanswered := unansweredQuestionCount(qs, draft.selected, draft.others)
	s.mu.Unlock()

	accent := ThemeAccent(c)
	ui.Column(c.Key("approval-"+id)).
		Radius(RadiusLG).
		Background(accent.Alpha(0.06).Over(ThemeSurface(c))).
		Border(Hairline, accent.Alpha(0.25)).
		Padding(Space5).
		Gap(Space3).
		Children(func() {
			// 头部：图标、标题，多题时显示题号与上一题/下一题
			ui.Row(c.Key("approval-header-" + id)).Gap(Space3).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, IconBrain()).Size(IconMD, IconMD).TextColor(accent)
				ui.Text(c, approvalCardTitle(approval)).Bold().FontSize(TypeProse).TextColor(ThemePrimaryText(c)).Selectable()
				ui.Spacer(c)
				if len(qs) > 1 {
					ui.Text(c, fmt.Sprintf("第 %d / %d 题", current+1, len(qs))).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).Selectable()
					renderApprovalButton(c, "question-prev-"+id, "上一题", "上一题", false, current == 0, func() {
						s.mu.Lock()
						if draft.current > 0 {
							draft.current--
						}
						s.mu.Unlock()
						s.invalidate()
					})
					renderApprovalButton(c, "question-next-"+id, "下一题", "下一题", false, current >= len(qs)-1, func() {
						s.mu.Lock()
						if draft.current < len(qs)-1 {
							draft.current++
						}
						s.mu.Unlock()
						s.invalidate()
					})
				}
			})

			if len(qs) == 0 && approval.Detail != "" {
				ui.Row(c.Key("question-detail-" + id)).Children(func() {
					ui.Text(c, approval.Detail).FontSize(TypeBody).TextColor(ThemePrimaryText(c)).Selectable()
				})
			}
			if len(qs) > 0 {
				renderQuestionBody(c, s, approval, draft, current, responding)
			}
			renderApprovalReason(c, approval)

			ui.Row(c.Key("question-actions-" + id)).Gap(Space3).AlignItems(ui.Center).Children(func() {
				if unanswered > 0 {
					ui.Text(c, fmt.Sprintf("还有 %d 题未回答", unanswered)).FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c)).Selectable()
				}
				ui.Spacer(c)
				renderApprovalButton(c, "question-skip-"+id, "跳过", "跳过提问 (Esc)", false, responding, func() {
					s.respondAnswers(approval, "", nil, true)
				})
				submitTooltip := "提交答案"
				if unanswered == 0 && len(qs) > 0 {
					submitTooltip = "提交答案 (↩)"
				}
				renderApprovalButton(c, "question-submit-"+id, "提交", submitTooltip, true, responding || unanswered > 0 || len(qs) == 0, func() {
					s.mu.Lock()
					answers := buildQuestionAnswers(qs, draft.selected, draft.others)
					s.mu.Unlock()
					s.respondAnswers(approval, "", answers, false)
				})
			})
		})
}

// renderQuestionBody 渲染当前题目：Header、Prompt、选项列表、自由指示出口与填空输入框。
func renderQuestionBody(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, qi int, responding bool) {
	id := approval.RequestID
	q := approval.Questions[qi]
	s.mu.Lock()
	selected := slices.Clone(draftAt(draft.selected, qi))
	s.mu.Unlock()

	ui.Column(c.Key(fmt.Sprintf("question-body-%s-%d", id, qi))).Gap(Space3).Children(func() {
		if q.Header != "" {
			ui.Text(c, q.Header).Bold().FontSize(TypeBody).TextColor(ThemePrimaryText(c)).Selectable()
		}
		if q.Prompt != "" {
			ui.Text(c, q.Prompt).FontSize(TypeBody).TextColor(ThemePrimaryText(c)).Selectable()
		}
		for ci, choice := range q.Choices {
			ci, choice := ci, choice
			isSel := slices.Contains(selected, ci)
			btn := ui.ButtonBase(c.Key(fmt.Sprintf("question-choice-%s-%d-%d", id, qi, ci))).
				Label(choice.Label).
				Tooltip(choice.Label).
				Padding(Space2, Space3).
				Radius(RadiusSM)
			if isSel {
				btn.Background(ThemeSelected(c)).Border(Hairline, ThemeAccent(c).Alpha(0.6))
			} else {
				btn.Border(Hairline, ThemeBorder(c))
				if btn.Hovered() {
					btn.Background(ThemeHover(c))
				}
			}
			btn.OnClick(func() {
				s.mu.Lock()
				if qi < len(draft.selected) {
					draft.selected[qi] = toggleQuestionChoice(q, draft.selected[qi], ci)
					if !q.AllowsMultiple {
						draft.others[qi] = ""
					}
				}
				s.mu.Unlock()
				s.invalidate()
			})
			btn.Children(func() {
				// 选项行统一左对齐并占满按钮宽度，图标位固定 13×13，保证选中与未选中排版一致
				ui.Row(c.Key(fmt.Sprintf("question-choice-row-%s-%d-%d", id, qi, ci))).Gap(Space3).Justify(ui.Start).AlignItems(ui.Center).FillWidth().Children(func() {
					// 选中显示勾选；多选未选中显示空方框，单选未选中留白对齐
					switch {
					case isSel:
						ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(ThemeAccent(c))
					case q.AllowsMultiple:
						ui.Icon(c, IconSquare()).Size(IconSM, IconSM).TextColor(ThemeSecondaryText(c))
					default:
						// 固定宽度占位（不能用 Spacer：它会吃掉剩余空间把文字挤到右半边）
						ui.Box(c).Size(IconSM, IconSM).Shrink(0)
					}
					ui.Column(c.Key(fmt.Sprintf("question-choice-text-%s-%d-%d", id, qi, ci))).Grow(1).Gap(Space0).Children(func() {
						// 标签里的推荐标注只在显示时去掉，提交给 provider 的仍是原始 Label
						label := ui.Text(c, displayChoiceLabel(choice.Label)).FontSize(TypeBody).TextColor(ThemePrimaryText(c))
						if isSel {
							label.Bold()
						}
						if choice.Detail != "" {
							ui.Text(c, choice.Detail).FontSize(TypeCaption).TextColor(ThemeSecondaryText(c))
						}
					})
					// 推荐项单独用胶囊标注，不再靠整行强调色
					if isRecommendedChoice(choice.Label) {
						renderRecommendedBadge(c, fmt.Sprintf("question-choice-recommended-%s-%d-%d", id, qi, ci))
					}
					// 数字键 1~9 可直接选中当前题目的前 9 个选项：文字列已 Grow(1) 占满剩余宽度，
					// 角标因此自然贴在行尾；第 10 个及以后没有快捷键，不画角标。
					if ci < 9 {
						badgeKey := fmt.Sprintf("question-badge-%s-%d-%d", id, qi, ci)
						if q.AllowsMultiple {
							Kbd(c, badgeKey, strconv.Itoa(ci+1))
						} else {
							accentKeyBadge(c, badgeKey, strconv.Itoa(ci+1))
						}
					}
				})
			})
		}
		if questionShowsOther(q) && qi < len(draft.others) {
			placeholder := "输入其他答案"
			if len(q.Choices) == 0 {
				placeholder = "输入答案"
			}
			in := ui.TextInput(c.Key(fmt.Sprintf("question-other-%s-%d", id, qi)), &draft.others[qi]).
				Label(placeholder).
				Placeholder(placeholder).
				FontSize(TypeBody).
				Padding(Space2, Space3).
				Radius(RadiusMD).
				Background(ThemeSurface(c)).
				Border(Hairline, ThemeBorder(c)).
				OnChange(func() {
					s.mu.Lock()
					if !q.AllowsMultiple && strings.TrimSpace(draft.others[qi]) != "" {
						draft.selected[qi] = nil
					}
					s.mu.Unlock()
					s.invalidate()
				})
			focused := in.Focused()
			s.mu.Lock()
			s.questionOtherFocused = focused
			s.mu.Unlock()
			if q.IsSecret {
				in.Password()
			}
		} else {
			s.mu.Lock()
			s.questionOtherFocused = false
			s.mu.Unlock()
			// provider 不一定接受自由文本，所以不显示输入框；仍留一个出口：跳过提问后
			// 把焦点交给会话输入框，用户可以在下一条消息里补充指示。
			if len(q.Choices) > 0 {
				renderOtherInstructionButton(c, fmt.Sprintf("question-other-hint-%s-%d", id, qi), responding, func() {
					s.respondAnswers(approval, "", nil, true)
					s.mu.Lock()
					s.composerFocusRequest = true
					s.mu.Unlock()
					s.invalidate()
				})
			}
		}
	})
}

// renderRecommendedBadge 渲染选项的「推荐」小胶囊：强调色文字配低透明度强调底色。
func renderRecommendedBadge(c *ui.Context, key string) {
	accent := ThemeAccent(c)
	ui.Row(c.Key(key)).Padding(Hairline, Space2).Radius(RadiusXS).Background(accent.Alpha(0.15)).Shrink(0).Label("推荐").Children(func() {
		ui.Text(c, "推荐").FontSize(TypeCaption).TextColor(accent)
	})
}

// renderOtherInstructionButton 渲染「其他指示…」文字按钮：虚线边框，点击后跳过提问并用下一条消息补充说明。
func renderOtherInstructionButton(c *ui.Context, key string, disabled bool, onClick func()) {
	btn := ui.ButtonBase(c.Key(key)).
		Label("其他指示…").
		Tooltip("跳过提问，用下一条消息补充指示").
		Disabled(disabled).
		Padding(Space1, Space3).
		Radius(RadiusMD).
		Border(Hairline, ThemeBorder(c)).
		BorderStyle(ui.BorderDashed).
		AlignSelf(ui.Start).
		OnClick(onClick)
	if btn.Hovered() && !disabled {
		btn.Background(ThemeHover(c))
	}
	btn.Children(func() {
		ui.Text(c, "其他指示…").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
	})
}
