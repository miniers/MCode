package mcodeui

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/git"
)

// 记录当前正在提交审批响应的按钮 key，使该按钮展示 Loading 而同卡其余按钮保持禁用。
var (
	approvalActiveKeyMu sync.Mutex
	approvalActiveKeys  = make(map[*chatState]string)
)

// 可替换时钟与延时，便于测试打字期防误触与单选自动推进。
var (
	approvalNow              = time.Now
	questionAutoAdvanceDelay = 200 * time.Millisecond
)

type approvalGuardState struct {
	lastDraft      string
	draftChangedAt time.Time
	firstSeen      map[string]time.Time
}

type questionAutoAdvanceEntry struct {
	timer *time.Timer
	gen   uint64
}

var (
	approvalGuardMu sync.Mutex
	approvalGuards  = make(map[*chatState]*approvalGuardState)

	questionAutoAdvanceMu      sync.Mutex
	questionAutoAdvanceEntries = make(map[*chatState]map[string]questionAutoAdvanceEntry)
	questionAutoAdvanceNextGen uint64
)

func setActiveApprovalKey(s *chatState, key string) {
	if s == nil {
		return
	}
	approvalActiveKeyMu.Lock()
	if key == "" {
		delete(approvalActiveKeys, s)
	} else {
		approvalActiveKeys[s] = key
	}
	approvalActiveKeyMu.Unlock()
}

func activeApprovalKey(s *chatState) string {
	if s == nil {
		return ""
	}
	approvalActiveKeyMu.Lock()
	k := approvalActiveKeys[s]
	approvalActiveKeyMu.Unlock()
	return k
}

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

// cmdReturnApprovalTarget 在输入框聚焦时也允许批准计划，其它审批沿用原有规则。
func cmdReturnApprovalTarget(approvals []Approval, responding, composerFocused bool) (Approval, bool) {
	if responding {
		return Approval{}, false
	}
	for _, a := range approvals {
		if a.Kind != ApprovalKindQuestion && (!composerFocused || a.Kind == ApprovalKindPlan) {
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
	ApprovalActionNext         ApprovalActionKind = "next"          // 问题卡片：Enter 跳到下一题
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
//   - Enter：有下一题时返回 ApprovalActionNext，否则所有题目都已回答时返回 ApprovalActionSubmit；
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
	questionHasNext bool,
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
		if isTextInputBusy || target.Kind == ApprovalKindPlan {
			return ApprovalShortcutAction{}
		}
		if target.Kind == ApprovalKindQuestion {
			if questionHasNext {
				return ApprovalShortcutAction{Kind: ApprovalActionNext}
			}
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
		if isTextInputBusy || target.Kind == ApprovalKindPlan {
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

// shouldHoldApprovalShortcut 判断是否应暂缓审批快捷键：
// 1. 高风险普通审批禁止无修饰键裸 Enter 直接批准；
// 2. 输入框聚焦期间，最近 800ms 内草稿刚变动，或审批刚出现不足 600ms 时，暂缓 Enter 与数字键 1~9。
func shouldHoldApprovalShortcut(top Approval, key ui.Key, composerFocused bool, now, draftChangedAt, firstSeenAt time.Time) bool {
	if key == ui.KeyEnter && top.Kind != ApprovalKindQuestion && top.Kind != ApprovalKindPlan {
		if _, _, highRisk := approvalRiskBadge(top); highRisk {
			return true
		}
	}
	if key != ui.KeyEnter && (key < ui.Key1 || key > ui.Key9) {
		return false
	}
	if !composerFocused {
		return false
	}
	if !draftChangedAt.IsZero() && now.Sub(draftChangedAt) < 800*time.Millisecond {
		return true
	}
	if !firstSeenAt.IsZero() && now.Sub(firstSeenAt) < 600*time.Millisecond {
		return true
	}
	return false
}

func syncApprovalGuard(s *chatState, draft string, approvals []Approval, now time.Time) (draftChangedAt, firstSeenAt time.Time) {
	if s == nil {
		return time.Time{}, time.Time{}
	}
	approvalGuardMu.Lock()
	defer approvalGuardMu.Unlock()

	g := approvalGuards[s]
	if len(approvals) == 0 {
		if g == nil {
			if draft == "" {
				return time.Time{}, time.Time{}
			}
			approvalGuards[s] = &approvalGuardState{
				lastDraft:      draft,
				draftChangedAt: now,
			}
			return now, time.Time{}
		}
		g.firstSeen = nil
		if draft != g.lastDraft {
			g.lastDraft = draft
			g.draftChangedAt = now
		}
		if draft == "" && (g.draftChangedAt.IsZero() || now.Sub(g.draftChangedAt) >= 800*time.Millisecond) {
			delete(approvalGuards, s)
		}
		return g.draftChangedAt, time.Time{}
	}

	if g == nil {
		g = &approvalGuardState{}
		approvalGuards[s] = g
	}
	if draft != g.lastDraft {
		g.lastDraft = draft
		g.draftChangedAt = now
	}
	if g.firstSeen == nil {
		g.firstSeen = make(map[string]time.Time, len(approvals))
	}
	active := make(map[string]struct{}, len(approvals))
	for _, a := range approvals {
		if a.RequestID == "" {
			continue
		}
		active[a.RequestID] = struct{}{}
		if _, ok := g.firstSeen[a.RequestID]; !ok {
			g.firstSeen[a.RequestID] = now
		}
	}
	for id := range g.firstSeen {
		if _, ok := active[id]; !ok {
			delete(g.firstSeen, id)
		}
	}
	if topID := approvals[0].RequestID; topID != "" {
		firstSeenAt = g.firstSeen[topID]
	}
	return g.draftChangedAt, firstSeenAt
}

func cancelQuestionAutoAdvance(s *chatState, requestID string) {
	if s == nil || requestID == "" {
		return
	}
	questionAutoAdvanceMu.Lock()
	byReq := questionAutoAdvanceEntries[s]
	if entry, ok := byReq[requestID]; ok {
		if entry.timer != nil {
			entry.timer.Stop()
		}
		delete(byReq, requestID)
		if len(byReq) == 0 {
			delete(questionAutoAdvanceEntries, s)
		}
	}
	questionAutoAdvanceMu.Unlock()
}

func pruneQuestionAutoAdvance(s *chatState, approvals []Approval) {
	if s == nil {
		return
	}
	questionAutoAdvanceMu.Lock()
	byReq := questionAutoAdvanceEntries[s]
	if len(byReq) == 0 {
		questionAutoAdvanceMu.Unlock()
		return
	}
	if len(approvals) == 0 {
		for _, entry := range byReq {
			if entry.timer != nil {
				entry.timer.Stop()
			}
		}
		delete(questionAutoAdvanceEntries, s)
		questionAutoAdvanceMu.Unlock()
		return
	}
	for reqID, entry := range byReq {
		keep := false
		for _, a := range approvals {
			if a.RequestID == reqID {
				keep = true
				break
			}
		}
		if !keep {
			if entry.timer != nil {
				entry.timer.Stop()
			}
			delete(byReq, reqID)
		}
	}
	if len(byReq) == 0 {
		delete(questionAutoAdvanceEntries, s)
	}
	questionAutoAdvanceMu.Unlock()
}

func scheduleQuestionAutoAdvance(s *chatState, approval Approval, qi int) {
	if s == nil || approval.RequestID == "" {
		return
	}
	delay := questionAutoAdvanceDelay
	questionAutoAdvanceMu.Lock()
	byReq := questionAutoAdvanceEntries[s]
	if byReq == nil {
		byReq = make(map[string]questionAutoAdvanceEntry)
		questionAutoAdvanceEntries[s] = byReq
	}
	if old, ok := byReq[approval.RequestID]; ok && old.timer != nil {
		old.timer.Stop()
	}
	questionAutoAdvanceNextGen++
	gen := questionAutoAdvanceNextGen
	timer := time.AfterFunc(delay, func() {
		runQuestionAutoAdvance(s, approval.RequestID, qi, gen)
	})
	byReq[approval.RequestID] = questionAutoAdvanceEntry{timer: timer, gen: gen}
	questionAutoAdvanceMu.Unlock()
}

func runQuestionAutoAdvance(s *chatState, requestID string, qi int, gen uint64) {
	if s == nil || requestID == "" {
		return
	}
	questionAutoAdvanceMu.Lock()
	byReq := questionAutoAdvanceEntries[s]
	entry, ok := byReq[requestID]
	if !ok || entry.gen != gen {
		questionAutoAdvanceMu.Unlock()
		return
	}
	delete(byReq, requestID)
	if len(byReq) == 0 {
		delete(questionAutoAdvanceEntries, s)
	}
	questionAutoAdvanceMu.Unlock()

	var (
		found       Approval
		hasApproval bool
		advanceOnly bool
		submit      bool
		submitAns   map[string][]string
	)

	s.mu.Lock()
	if s.respondingApproval == 0 && !s.questionOtherFocused {
		for _, a := range s.approvals {
			if a.RequestID == requestID {
				found = a
				hasApproval = true
				break
			}
		}
		if hasApproval && found.Kind == ApprovalKindQuestion {
			draft := s.questionDraftFor(found)
			qs := found.Questions
			if draft != nil && draft.current == qi && qi >= 0 && qi < len(qs) && !qs[qi].AllowsMultiple && len(draftAt(draft.selected, qi)) > 0 {
				if qi < len(qs)-1 {
					draft.current++
					advanceOnly = true
				} else if unansweredQuestionCount(qs, draft.selected, draft.others) == 0 {
					submitAns = buildQuestionAnswers(qs, draft.selected, draft.others)
					submit = true
				}
			}
		}
	}
	s.mu.Unlock()

	if advanceOnly {
		s.invalidate()
		return
	}
	if submit {
		setActiveApprovalKey(s, "question-submit-"+found.RequestID)
		s.respondAnswers(found, "", submitAns, false)
	}
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
	switch a.Reason {
	case "credential":
		return joinApprovalTitle("读取凭证", a.Title)
	case "controlPlane":
		return joinApprovalTitle("改动 mcode 数据", a.Title)
	}
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

var (
	highRiskCmdRe    = regexp.MustCompile(`(?i)(?:\b(?:rm|rmdir|sudo|chmod|chown|mkfs|diskutil|truncate)\b|\bgit\s+(?:reset\s+--hard|clean)\b)`)
	netInstallCmdRe  = regexp.MustCompile(`(?i)\b(?:curl|wget|(?:npm|pnpm|yarn|bun|pip|pip3|uv|brew)\s+(?:add|install|update|upgrade|sync))\b`)
	readOnlySimpleRe = regexp.MustCompile(`^(?:pwd|ls|cat|head|tail|wc|stat)(?:\s+[\w./ -]+)?$`)
)

func approvalIsOutsideWorkspace(reason string) bool {
	if reason == "" {
		return false
	}
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "outside the workspace") ||
		strings.Contains(reason, "工作区以外") ||
		strings.Contains(reason, "工作区外")
}

// approvalRiskBadge 按审批来源、类型与命令内容判定风险徽章标签、色调与高风险标记。
func approvalRiskBadge(a Approval) (label string, tone Tone, highRisk bool) {
	if a.Reason == "credential" || a.Reason == "controlPlane" || approvalIsOutsideWorkspace(a.Reason) {
		return "高风险", ToneDanger, true
	}
	if a.Kind == ApprovalKindCommand && highRiskCmdRe.MatchString(a.Detail) {
		return "高风险", ToneDanger, true
	}
	if a.Kind == ApprovalKindCommand && netInstallCmdRe.MatchString(a.Detail) {
		return "联网/依赖", ToneWarning, false
	}
	if a.Kind == ApprovalKindFileChange {
		return "写入文件", ToneWarning, false
	}
	if a.Kind == ApprovalKindCommand && !strings.ContainsAny(a.Detail, "\r\n|><") &&
		readOnlySimpleRe.MatchString(strings.Trim(a.Detail, " \t")) {
		return "只读", ToneNeutral, false
	}
	return "", ToneNeutral, false
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
func renderApproval(c *ui.Context, s *chatState) {
	if s == nil {
		return
	}
	now := approvalNow()
	s.mu.Lock()
	draft := s.draft
	if len(s.approvals) == 0 {
		s.mu.Unlock()
		syncApprovalGuard(s, draft, nil, now)
		pruneQuestionAutoAdvance(s, nil)
		return
	}
	approvals := make([]Approval, len(s.approvals))
	copy(approvals, s.approvals)
	// 在途响应未全部返回前，卡片与批量按钮保持锁定：批量允许会并发发出多个请求。
	responding := s.respondingApproval > 0
	composerFocused := s.composerFocused
	composerHasDraft := strings.TrimSpace(draft) != ""
	otherFocused := s.questionOtherFocused
	s.mu.Unlock()
	if !responding {
		setActiveApprovalKey(s, "")
	}
	draftChangedAt, firstSeenAt := syncApprovalGuard(s, draft, approvals, now)
	pruneQuestionAutoAdvance(s, approvals)

	// 快捷键仅对最上面一张卡片生效，且仅在 !responding 时生效。
	if len(approvals) > 0 && !responding {
		top := approvals[0]
		var questionAllAnswered bool
		var questionHasNext bool
		var curChoicesCount int
		var qDraft *questionDraft
		var curQ ApprovalQuestion
		pickerOpen := false
		if top.Kind == ApprovalKindQuestion {
			s.mu.Lock()
			qDraft = s.questionDraftFor(top)
			// 题号浮层展开时，数字键、回车与 Esc 归浮层，卡片自己的快捷键不触发。
			pickerOpen = qDraft.pickerOpen
			qs := top.Questions
			unanswered := unansweredQuestionCount(qs, qDraft.selected, qDraft.others)
			questionAllAnswered = (unanswered == 0 && len(qs) > 0)
			questionHasNext = qDraft != nil && len(qs) > 1 && qDraft.current < len(qs)-1
			qi := qDraft.current
			if qi >= 0 && qi < len(qs) {
				curQ = qs[qi]
				curChoicesCount = len(curQ.Choices)
			}
			s.mu.Unlock()
		}

		// 数字键 1~9
		for k := ui.Key1; k <= ui.Key9; k++ {
			if !pickerOpen && !shouldHoldApprovalShortcut(top, k, composerFocused, now, draftChangedAt, firstSeenAt) && c.Shortcut(0, k) {
				action := approvalShortcutAction(top, k, 0, responding, composerFocused, composerHasDraft, otherFocused, questionAllAnswered, questionHasNext, curChoicesCount)
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
					if !curQ.AllowsMultiple {
						scheduleQuestionAutoAdvance(s, top, qi)
					} else {
						cancelQuestionAutoAdvance(s, top.RequestID)
					}
					s.invalidate()
				case ApprovalActionOption:
					// 只认仍在待办列表中的同一张卡片：本轮已通过数字键批准的卡片不能再按 Esc 拒绝
					if action.OptionID != "" {
						if target, ok := pendingApprovalByID(s, top.RequestID); ok {
							setActiveApprovalKey(s, "approval-opt-"+target.RequestID+"-"+action.OptionID)
							s.respondApproval(target, action.OptionID)
						}
					}
				}
			}
		}

		// Enter 键
		if top.Kind != ApprovalKindPlan && !pickerOpen && !shouldHoldApprovalShortcut(top, ui.KeyEnter, composerFocused, now, draftChangedAt, firstSeenAt) && c.Shortcut(0, ui.KeyEnter) {
			action := approvalShortcutAction(top, ui.KeyEnter, 0, responding, composerFocused, composerHasDraft, otherFocused, questionAllAnswered, questionHasNext, curChoicesCount)
			switch action.Kind {
			case ApprovalActionNext:
				cancelQuestionAutoAdvance(s, top.RequestID)
				s.mu.Lock()
				if qDraft != nil && qDraft.current < len(top.Questions)-1 {
					qDraft.current++
				}
				s.mu.Unlock()
				s.invalidate()
			case ApprovalActionSubmit:
				cancelQuestionAutoAdvance(s, top.RequestID)
				s.mu.Lock()
				answers := buildQuestionAnswers(top.Questions, qDraft.selected, qDraft.others)
				s.mu.Unlock()
				setActiveApprovalKey(s, "question-submit-"+top.RequestID)
				s.respondAnswers(top, "", answers, false)
			case ApprovalActionApprove:
				// 只认仍在待办列表中的同一张卡片：本轮已通过数字键批准的卡片不能再按 Esc 拒绝
				if action.OptionID != "" {
					if target, ok := pendingApprovalByID(s, top.RequestID); ok {
						setActiveApprovalKey(s, "approval-opt-"+target.RequestID+"-"+action.OptionID)
						s.respondApproval(target, action.OptionID)
					}
				}
			}
		}

		// Esc 键
		if !pickerOpen && c.Shortcut(0, ui.KeyEscape) {
			action := approvalShortcutAction(top, ui.KeyEscape, 0, responding, composerFocused, composerHasDraft, otherFocused, questionAllAnswered, questionHasNext, curChoicesCount)
			switch action.Kind {
			case ApprovalActionDecline:
				if top.Kind == ApprovalKindQuestion {
					cancelQuestionAutoAdvance(s, top.RequestID)
					setActiveApprovalKey(s, "question-skip-"+top.RequestID)
					s.respondAnswers(top, "", nil, true)
				} else if top.Kind == ApprovalKindPlan {
					setActiveApprovalKey(s, "plan-decline-"+top.RequestID)
					s.declinePlanDraft(top, false)
				} else {
					if action.OptionID != "" {
						if target, ok := pendingApprovalByID(s, top.RequestID); ok {
							setActiveApprovalKey(s, "approval-opt-"+target.RequestID+"-"+action.OptionID)
							s.respondApproval(target, action.OptionID)
						}
					}
				}
			}
		}
	}

	// ⌘Return 批准第一个可批准的审批；不满足 cmdReturnApprovalTarget 时不注册，把按键留给 composer。
	if target, ok := cmdReturnApprovalTarget(approvals, responding, composerFocused); ok {
		if c.Shortcut(ui.Cmd, ui.KeyEnter) {
			if optID := findApprovalOption(target, "approve"); optID != "" {
				if target.Kind == ApprovalKindPlan {
					setActiveApprovalKey(s, "plan-approve-"+target.RequestID)
				} else {
					setActiveApprovalKey(s, "approval-opt-"+target.RequestID+"-"+optID)
				}
				s.respondApproval(target, optID)
			}
		}
	}

	batch := batchApprovals(approvals)
	ui.Row(c.Key("approval-container")).Justify(ui.Center).Padding(0, Space6, Space3, Space6).PassThrough().Children(func() {
		ui.Column(c.Key("approval-stack")).Grow(1).MaxWidth(composerDockMaxWidth).Gap(Space4).
			PassThrough().
			Transition(motionListItem(c)).
			Children(func() {
				if len(batch) >= 2 {
					ui.Row(c.Key("approval-batch-row")).FillWidth().Wrap().Gap(Space2).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("%d 个待审批", len(batch))).
							Bold().
							FontSize(uiFont(TypeBody)).
							TextColor(ThemePrimaryText(c))
						ui.Spacer(c)
						ui.Row(c.Key("approval-batch-actions")).Wrap().Gap(Space3).AlignItems(ui.Center).Children(func() {
							allowKey := "approval-batch-allow"
							allowLoading := responding && activeApprovalKey(s) == allowKey
							Button(c, ButtonOpts{
								Key:      allowKey,
								Text:     "全部允许",
								Variant:  ButtonPrimary,
								Size:     ControlSM,
								Loading:  allowLoading,
								Disabled: responding && !allowLoading,
								Tooltip:  "全部允许",
								OnClick: func() {
									setActiveApprovalKey(s, allowKey)
									for _, appr := range batch {
										optID := findApprovalOption(appr, "approve")
										if optID != "" {
											s.respondApproval(appr, optID)
										}
									}
								},
							})

							declineKey := "approval-batch-decline"
							declineLoading := responding && activeApprovalKey(s) == declineKey
							Button(c, ButtonOpts{
								Key:      declineKey,
								Text:     "全部拒绝",
								Variant:  ButtonSecondary,
								Size:     ControlSM,
								Loading:  declineLoading,
								Disabled: responding && !declineLoading,
								Tooltip:  "全部拒绝",
								OnClick: func() {
									setActiveApprovalKey(s, declineKey)
									for _, appr := range batch {
										optID := findApprovalOption(appr, "decline")
										if optID != "" {
											s.respondApproval(appr, optID)
										}
									}
								},
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
			// 路径取自 Title，等宽显示
			if approval.Title != "" {
				ui.Row(c.Key("approval-path-" + id)).FillWidth().Children(func() {
					ui.Text(c, approval.Title).Font("monospace").FontSize(uiFont(TypeFootnote)).TextColor(ThemePrimaryText(c)).Grow(1).MinWidth(0).Selectable()
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
										renderInlineToolDiffFile(c, s, "approval-"+id, idx, file)
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
				ui.Text(c, commitApprovalPrompt).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).Selectable()
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
		FontSize(uiFont(TypeFootnote)).
		LineHeight(1.3).
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeHover(c)).
		Border(Hairline, ThemeBorder(c))
}

func approvalReasonText(a Approval) string {
	switch a.Reason {
	case "credential", "controlPlane":
		return ""
	default:
		return strings.TrimSpace(a.Reason)
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
	if reason := approvalReasonText(approval); reason != "" {
		parts = append(parts, reason)
	}
	if notice := approvalDeliveryNotice(approval.DeliveryError); notice != "" {
		parts = append(parts, notice)
	}
	return strings.Join(parts, " · ")
}

// approvalBannerTone 在存在未送达错误时返回危险色调，其余原因说明保留警告色调。
func approvalBannerTone(approval Approval) Tone {
	if approvalDeliveryNotice(approval.DeliveryError) != "" {
		return ToneDanger
	}
	return ToneWarning
}

// renderApprovalAuxBanner 渲染审批原因及“上次答复未送达”提示条；两者都为空时不渲染。
func renderApprovalAuxBanner(c *ui.Context, approval Approval) {
	if text := approvalAuxText(approval); text != "" {
		Banner(c, BannerProps{
			Key:  "approval-banner-" + approval.RequestID,
			Tone: approvalBannerTone(approval),
			Text: text,
		})
	}
}

// approvalCardFrame 渲染审批卡的统一外壳：SurfaceCard 底色加左侧 2px 语义色条。
// 卡片需要左侧色条，因此这里用同一套令牌自绘。
func approvalCardFrame(c *ui.Context, key string, accent ui.Color, body func()) {
	ui.Row(c.Key(key)).
		Clip().
		AlignItems(ui.Stretch).
		Radius(RadiusLG).
		Background(ThemeSurfaceCard(c)).
		Gap(Space3).
		Children(func() {
			ui.Box(c.Key(key + "-accent")).Width(Space0).AlignSelf(ui.Stretch).Background(accent)
			ui.Column(c.Key(key + "-content")).Grow(1).MinWidth(0).Gap(Space3).Padding(Space5).Children(body)
		})
}

// renderTypedApprovalCard 渲染命令、文件修改、工具、权限与提交类审批卡片：图标标题、正文、原因与右下角操作按钮。
func renderTypedApprovalCard(c *ui.Context, s *chatState, approval Approval, responding bool, icon *ui.SVG, body func()) {
	label, tone, highRisk := approvalRiskBadge(approval)
	accentColor := ThemeWarning(c)
	if highRisk {
		accentColor = ThemeDanger(c)
	}

	approvalCardFrame(c, "approval-"+approval.RequestID, accentColor, func() {
		ui.Row(c.Key("approval-header-" + approval.RequestID)).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icon).Size(IconMD, IconMD).TextColor(accentColor).Shrink(0)
			ui.Text(c, approvalCardTitle(approval)).Bold().FontSize(uiFont(TypeProse)).TextColor(ThemePrimaryText(c)).Grow(1).MinWidth(0).Selectable()
			if label != "" {
				Badge(c, "approval-risk-"+approval.RequestID, label, tone).Shrink(0)
			}
		})
		body()
		renderApprovalAuxBanner(c, approval)
		renderApprovalOptions(c, s, approval, responding)
	})
}

// renderApprovalOptions 渲染底部操作按钮栏：右对齐并在窄窗下自动换行，批准按钮为主样式。
func renderApprovalOptions(c *ui.Context, s *chatState, approval Approval, responding bool) {
	ui.Row(c.Key("approval-options-" + approval.RequestID)).FillWidth().Wrap().Justify(ui.End).Gap(Space3).AlignItems(ui.Center).Children(func() {
		for idx, opt := range approval.Options {
			optID := opt.ID
			shortcutHint := ""
			if idx < 9 {
				shortcutHint = fmt.Sprintf("%d", idx+1)
			}
			primary := opt.Role == "approve"
			var tooltip string
			switch opt.Role {
			case "approve":
				tooltip = "批准 (" + shortcutDisplay("⌘↩") + " / " + shortcutDisplay("↩") + ")"
				if shortcutHint != "" {
					tooltip = fmt.Sprintf("批准 (%s / %s / %s)", shortcutDisplay("⌘↩"), shortcutDisplay("↩"), shortcutHint)
				}
			case "decline":
				tooltip = fmt.Sprintf("%s (Esc)", opt.Title)
				if shortcutHint != "" {
					tooltip = fmt.Sprintf("%s (Esc / %s)", opt.Title, shortcutHint)
				}
			default:
				tooltip = opt.Title
				if shortcutHint != "" {
					tooltip = fmt.Sprintf("%s (%s)", opt.Title, shortcutHint)
				}
			}
			btnKey := "approval-opt-" + approval.RequestID + "-" + optID
			renderApprovalOptionButton(c, btnKey, approval.RequestID, optID, opt.Title, tooltip, shortcutHint, primary, responding, responding && activeApprovalKey(s) == btnKey, func() {
				setActiveApprovalKey(s, btnKey)
				s.respondApproval(approval, optID)
			})
		}
	})
}

// renderApprovalOptionButton 渲染带数字快捷键角标的审批选项按钮，统一焦点环、禁用色与加载态。
func renderApprovalOptionButton(c *ui.Context, key, reqID, optID, title, tooltip, shortcutHint string, primary, disabled, loading bool, onClick func()) {
	variant := ButtonSecondary
	fg := ThemePrimaryText(c)
	if primary {
		variant = ButtonPrimary
		fg = ThemeOnAccent(c)
	}
	wrapDisabledTooltip := disabled && tooltip != ""
	buildBtn := func(btnCtx *ui.Context) ui.Element {
		btn := ui.ButtonBase(btnCtx).
			Label(title).
			Height(ControlSM).
			Padding(0, Space3).
			Radius(RadiusSM).
			Gap(Space2)
		if wrapDisabledTooltip {
			btn.Description(tooltip)
		} else if tooltip != "" {
			btn.Tooltip(tooltip)
		}
		if disabled {
			btn.Disabled(true)
		} else if onClick != nil {
			btn.OnClick(onClick)
		}
		hoverTarget, pressTarget := float32(0), float32(0)
		if !disabled && btn.Hovered() {
			hoverTarget = 1
		}
		if !disabled && btn.Pressed() {
			pressTarget = 1
		}
		hover := btn.AnimateTo("button-hover", hoverTarget, ui.Animation{Duration: animateDuration(c, DurSnap), Ease: EaseSnap})
		press := btn.AnimateTo("button-press", pressTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: EaseSnap})
		bg := buttonBackground(variant, false, windowActiveFor(c), hover, press, buttonColors{
			accent: ThemeAccent(c), accentHover: ThemeAccentHover(c), accentPressed: ThemeAccentPressed(c),
			tertiary: ThemeTertiaryText(c), overlaySelected: ThemeOverlaySelected(c),
			overlayHover: ThemeOverlayHover(c), overlayPressed: ThemeOverlayPressed(c), danger: ThemeDanger(c),
		})
		if disabled && !loading {
			fg = ThemeTertiaryText(c)
		}
		btn.Background(bg)
		if variant == ButtonSecondary {
			btn.Border(1, ThemeBorder(c))
		}
		focusRing(c, btn)
		scale := motionPressScale(c, btn, "button-press-scale", !disabled && btn.Pressed())
		btn.Scale(scale, scale).Children(func() {
			ui.Row(c.Key("approval-opt-row-" + reqID + "-" + optID)).Gap(Space2).AlignItems(ui.Center).Children(func() {
				if loading {
					LoadingIndicator(c, nil, "", IconSM, fg)
				}
				t := ui.Text(c, title).FontSize(uiFont(TypeFootnote)).TextColor(fg)
				if primary {
					t.Bold()
				}
				if shortcutHint != "" {
					if primary {
						accentKeyBadge(c, "approval-opt-badge-"+reqID+"-"+optID, shortcutHint)
					} else {
						Kbd(c, "approval-opt-badge-"+reqID+"-"+optID, shortcutHint)
					}
				}
			})
		})
		return btn
	}
	if !wrapDisabledTooltip {
		buildBtn(c.Key(key))
		return
	}
	ui.Box(c.Key(key)).Shrink(0).Tooltip(tooltip).Children(func() {
		buildBtn(c)
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
		ui.Row(c.Key("approval-header-" + approval.RequestID)).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, IconOctagonAlert()).Size(IconMD, IconMD).TextColor(warnColor).Shrink(0)
			ui.Text(c, approval.Title).Bold().FontSize(uiFont(TypeProse)).TextColor(ThemePrimaryText(c)).Grow(1).MinWidth(0).Selectable()
		})

		// Detail：等宽代码框，最多显示 8 行，超长截断并支持滚动
		renderApprovalCode(c, "approval-detail-"+approval.RequestID, approval.Detail)
		renderApprovalAuxBanner(c, approval)
		renderApprovalOptions(c, s, approval, responding)
	})
}

// renderApprovalButton 渲染卡片内操作按钮（供跨文件调用复用 Button 统一样式与焦点环）。
func renderApprovalButton(c *ui.Context, key, label, tooltip string, primary, disabled bool, onClick func()) {
	variant := ButtonSecondary
	if primary {
		variant = ButtonPrimary
	}
	Button(c, ButtonOpts{
		Key:      key,
		Text:     label,
		Variant:  variant,
		Size:     ControlSM,
		Disabled: disabled,
		Tooltip:  tooltip,
		OnClick:  onClick,
	})
}

// renderPlanCopyButton 渲染计划复制按钮。
func renderPlanCopyButton(c *ui.Context, key, planText string) {
	CopyButton(c, CopyButtonProps{Key: key, Label: "复制", Tooltip: "复制", Text: "复制", Value: func() string { return planText }})
}

// renderPlanCard 只提供裁决操作，正文在时间线中阅读。
func renderPlanCard(c *ui.Context, s *chatState, approval Approval, responding bool) {
	id := approval.RequestID
	title := planTitle(approval.Detail)
	declineKey := "plan-decline-" + id
	approveKey := "plan-approve-" + id
	declineLoading := responding && activeApprovalKey(s) == declineKey
	approveLoading := responding && activeApprovalKey(s) == approveKey
	ui.Column(c.Key("approval-"+id)).FillWidth().Radius(RadiusLG).
		Background(ThemeSurface(c)).Border(Hairline, ThemeBorder(c)).
		Padding(Space3).Gap(Space2).Children(func() {
		ui.Row(c.Key("plan-row-" + id)).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, IconClipboardList()).Size(IconMD, IconMD).TextColor(ThemeAccent(c)).Shrink(0)
			ui.Text(c, title).Bold().FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).
				MaxLines(1).Ellipsis("…").MinWidth(0).Shrink(1).Tooltip(title)
			ui.Spacer(c)
			Button(c, ButtonOpts{
				Key:      declineKey,
				Text:     "驳回",
				Variant:  ButtonSecondary,
				Size:     ControlSM,
				Loading:  declineLoading,
				Disabled: responding && !declineLoading,
				Tooltip:  "驳回 (Esc)",
				OnClick: func() {
					setActiveApprovalKey(s, declineKey)
					s.declinePlanDraft(approval, false)
				},
			})
			Button(c, ButtonOpts{
				Key:      approveKey,
				Text:     "批准计划",
				Variant:  ButtonPrimary,
				Size:     ControlSM,
				Loading:  approveLoading,
				Disabled: responding && !approveLoading,
				Tooltip:  "批准计划 (" + shortcutDisplay("⌘↩") + ")",
				OnClick: func() {
					if optID := findApprovalOption(approval, "approve"); optID != "" {
						setActiveApprovalKey(s, approveKey)
						s.respondApproval(approval, optID)
					}
				},
			})
		})
		if notice := approvalDeliveryNotice(approval.DeliveryError); notice != "" {
			Banner(c, BannerProps{Key: "approval-banner-" + id, Tone: ToneDanger, Text: notice})
		}
	})
}

// 提问卡片（设计稿 A 版）的版式常量。
const (
	questionRowHeight      = ControlLG // 选项行高
	questionSlotSize       = IconMD    // 左侧序号位
	questionListMaxRows    = 7         // 选项列表最多显示的行数，超出后滚动
	questionPromptMaxLines = 6         // 题目最多显示的行数，超出后在题目区内滚动
	questionDetailMaxLines = 3         // 选项说明最多显示的行数，超出省略
	questionTextLineHeight = float32(1.4)
	// 任一选项说明超过该字数时，整题改用「标题一行、说明叠在下面」的版式；否则说明排在标题右侧同一行。
	questionStackedDetailRunes = 28
)

// questionStep 把开关状态换成动画目标值（0 或 1）；目标值只随状态变化，静止时动画不再续帧。
func questionStep(on bool) float32 {
	if on {
		return 1
	}
	return 0
}

func questionAnim(c *ui.Context, d time.Duration) ui.Animation {
	return ui.Animation{Duration: animateDuration(c, d), Ease: EaseOut}
}

// questionChoiceA11yLabel 拼出选项行的无障碍名称：标题、推荐标记与说明全文（行内的说明会被省略，读屏用户仍能拿到全文）。
func questionChoiceA11yLabel(title, detail string, recommended bool) string {
	parts := []string{title}
	if recommended {
		parts = append(parts, "推荐")
	}
	if detail != "" {
		parts = append(parts, detail)
	}
	return strings.Join(parts, "，")
}

// renderQuestionCard 渲染提问卡片（设计稿 A 版紧凑列表）：题目与进度、选项列表，底栏为补充要求、跳过与提交。
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

	ui.Column(c.Key("approval-"+id)).
		Radius(RadiusLG).
		Background(ThemeSurfaceCard(c)).
		Border(Hairline, ThemeBorder(c)).
		Children(func() {
			if len(qs) == 0 && approval.Detail != "" {
				ui.Row(c.Key("question-detail-"+id)).FillWidth().Padding(Space4, Space5, Space2, Space5).Children(func() {
					ui.Text(c, approval.Detail).FontSize(uiFont(TypeBody)).TextColor(ThemePrimaryText(c)).MinWidth(0).Grow(1).Selectable()
				})
			}
			if len(qs) > 0 {
				renderQuestionBody(c, s, approval, draft, current, responding)
			}
			if approvalAuxText(approval) != "" {
				ui.Column(c.Key("question-aux-"+id)).FillWidth().Padding(0, Space4, Space3, Space4).Children(func() {
					renderApprovalAuxBanner(c, approval)
				})
			}
			ui.Box(c.Key("question-divider-" + id)).FillWidth().Height(Hairline).Background(ThemeHairline(c))

			hasNext := len(qs) > 1 && current < len(qs)-1
			skipKey := "question-skip-" + id
			skipLoading := responding && activeApprovalKey(s) == skipKey
			showCustomNote := len(qs) > 0 && current >= 0 && current < len(qs) && !questionShowsOther(qs[current])
			ui.Row(c.Key("question-actions-"+id)).FillWidth().Wrap().Gap(Space3).AlignItems(ui.Center).Padding(Space3, Space4).Children(func() {
				if showCustomNote {
					Button(c, ButtonOpts{
						Key:      "question-custom-note-" + id,
						Text:     "补充要求",
						Variant:  ButtonGhost,
						Size:     ControlMD,
						Disabled: responding,
						Tooltip:  "在输入框补充要求",
						OnClick: func() {
							cancelQuestionAutoAdvance(s, id)
							if s != nil {
								s.mu.Lock()
								s.composerFocusRequest = true
								s.mu.Unlock()
								s.invalidate()
							}
						},
					})
				}
				ui.Spacer(c)
				Button(c, ButtonOpts{
					Key:      skipKey,
					Text:     "跳过",
					Variant:  ButtonGhost,
					Size:     ControlMD,
					Loading:  skipLoading,
					Disabled: responding && !skipLoading,
					Tooltip:  "跳过提问 (Esc)",
					OnClick: func() {
						cancelQuestionAutoAdvance(s, id)
						setActiveApprovalKey(s, skipKey)
						s.respondAnswers(approval, "", nil, true)
					},
				})
				if hasNext {
					Button(c, ButtonOpts{
						Key:      "question-next-" + id,
						Text:     "下一题",
						Variant:  ButtonPrimary,
						Size:     ControlMD,
						Disabled: responding,
						Tooltip:  "下一题 (↩)",
						OnClick: func() {
							cancelQuestionAutoAdvance(s, id)
							s.mu.Lock()
							if draft.current < len(qs)-1 {
								draft.current++
							}
							draft.pickerOpen = false
							s.mu.Unlock()
							s.invalidate()
						},
					})
					return
				}
				submitKey := "question-submit-" + id
				submitLoading := responding && activeApprovalKey(s) == submitKey
				submitTooltip := "提交答案"
				if unanswered == 0 && len(qs) > 0 {
					submitTooltip = "提交答案 (↩)"
				}
				Button(c, ButtonOpts{
					Key:      submitKey,
					Text:     "提交",
					Variant:  ButtonPrimary,
					Size:     ControlMD,
					Loading:  submitLoading,
					Disabled: (responding && !submitLoading) || unanswered > 0 || len(qs) == 0,
					Tooltip:  submitTooltip,
					OnClick: func() {
						cancelQuestionAutoAdvance(s, id)
						s.mu.Lock()
						answers := buildQuestionAnswers(qs, draft.selected, draft.others)
						draft.pickerOpen = false
						s.mu.Unlock()
						setActiveApprovalKey(s, submitKey)
						s.respondAnswers(approval, "", answers, false)
					},
				})
			})
		})
}

// renderQuestionBody 渲染当前题目：题目与题号触发器、选项列表和填空行。换题时整块淡入。
func renderQuestionBody(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, qi int, responding bool) {
	id := approval.RequestID
	qs := approval.Questions
	q := qs[qi]
	s.mu.Lock()
	selected := slices.Clone(draftAt(draft.selected, qi))
	var answered []bool
	if len(qs) > 1 {
		answered = make([]bool, len(qs))
		for i, aq := range qs {
			answered[i] = questionAnswered(aq, draftAt(draft.selected, i), draftAt(draft.others, i))
		}
	}
	s.mu.Unlock()

	stacked := false
	for _, choice := range q.Choices {
		if len([]rune(strings.TrimSpace(choice.Detail))) > questionStackedDetailRunes {
			stacked = true
			break
		}
	}

	body := ui.Column(c.Key(fmt.Sprintf("question-body-%s-%d", id, qi))).FillWidth()
	if k := motionEnter(c, body, "question-enter", 0); k < 1 {
		body.Opacity(k)
	}
	body.Children(func() {
		renderQuestionHead(c, s, approval, draft, qi, answered, responding)

		listMaxHeight := questionRowHeight*questionListMaxRows + Space4
		ui.Scroll(c.Key(fmt.Sprintf("question-list-scroll-%s-%d", id, qi))).FillWidth().MaxHeight(listMaxHeight).Children(func() {
			list := ui.Column(c.Key(fmt.Sprintf("question-list-%s-%d", id, qi))).
				FillWidth().
				Padding(Space1, Space2, Space3, Space2).
				Role(ui.RoleListBox).
				FocusGroup(ui.Vertical)
			if q.AllowsMultiple {
				list.Multiselectable(true)
			}
			list.Children(func() {
				for ci := range q.Choices {
					renderQuestionChoice(c, s, approval, draft, qi, ci, slices.Contains(selected, ci), stacked, responding)
				}
				if questionShowsOther(q) && qi < len(draft.others) {
					renderQuestionOtherRow(c, s, approval, draft, qi, responding)
				} else {
					s.mu.Lock()
					s.questionOtherFocused = false
					s.mu.Unlock()
				}
			})
		})
	})
}

// renderQuestionHead 渲染题目（Prompt，为空时用 Header）与多题时右侧的题号触发器；题目超过 6 行在题目区内滚动。
func renderQuestionHead(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, qi int, answered []bool, responding bool) {
	id := approval.RequestID
	q := approval.Questions[qi]
	title := strings.TrimSpace(q.Prompt)
	if title == "" {
		title = strings.TrimSpace(q.Header)
	}
	lineHeight := uiFont(TypeProse) * questionTextLineHeight
	// 顶对齐：题目多行时触发器贴在右上，与题目首行对齐。
	ui.Row(c.Key(fmt.Sprintf("question-head-%s-%d", id, qi))).FillWidth().AlignItems(ui.Start).Gap(Space4).Padding(Space4, Space5, Space2, Space5).Children(func() {
		if title != "" {
			ui.Scroll(c.Key(fmt.Sprintf("question-title-scroll-%s-%d", id, qi))).Grow(1).MinWidth(0).MaxHeight(lineHeight * questionPromptMaxLines).Children(func() {
				ui.Column(c.Key(fmt.Sprintf("question-title-%s-%d", id, qi))).FillWidth().Children(func() {
					ui.Text(c, title).Bold().FontSize(uiFont(TypeProse)).LineHeight(questionTextLineHeight).TextColor(ThemePrimaryText(c)).Selectable()
				})
			})
		} else {
			ui.Spacer(c)
		}
		if len(answered) > 1 {
			renderQuestionProgress(c, s, approval, draft, qi, answered, responding)
		}
	})
}

// 题号浮层（设计稿 C 版）：每行最多 6 格，超过 6 行滚动；格子里的题目最多取 24 个字。
const (
	questionPickerCols       = 6
	questionPickerRows       = 6
	questionPickerTitleRunes = 24
)

// questionPickerLabel 拼出题号格的名称与提示：「第 n 题：题目」，题目取 Prompt，为空取 Header，过长截断。
func questionPickerLabel(q ApprovalQuestion, n int) string {
	title := strings.Join(strings.Fields(q.Prompt), " ")
	if title == "" {
		title = strings.Join(strings.Fields(q.Header), " ")
	}
	if title == "" {
		return fmt.Sprintf("第 %d 题", n+1)
	}
	if r := []rune(title); len(r) > questionPickerTitleRunes {
		title = string(r[:questionPickerTitleRunes]) + "…"
	}
	return fmt.Sprintf("第 %d 题：%s", n+1, title)
}

// renderQuestionProgress 渲染多题的进度触发器「2/5 ▾」，点开后在下方浮出题号格，点哪格跳哪题；提交中禁用。
func renderQuestionProgress(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, current int, answered []bool, responding bool) {
	id := approval.RequestID
	total := len(answered)
	if responding {
		draft.pickerOpen = false
	}
	open := draft.pickerOpen
	accent := ThemeAccent(c)
	trigger := ui.ButtonBase(c.Key("question-progress-"+id)).
		Label(fmt.Sprintf("第 %d 题，共 %d 题", current+1, total)).
		HasPopup(ui.PopupDialog).
		Expanded(open).
		Disabled(responding).
		Height(ControlXS).
		Padding(0, Space2, 0, Space3).
		Radius(RadiusSM).
		Shrink(0).
		OnClick(func() {
			if togglePopoverOpen(&draft.pickerOpen) {
				draft.pickerFocus = current + 1
			}
			s.invalidate()
		})
	if responding {
		trigger.Opacity(0.4)
	}
	hover := trigger.AnimateTo("question-progress-hover", questionStep(!responding && trigger.Hovered()), questionAnim(c, DurFast))
	openK := trigger.AnimateTo("question-progress-open", questionStep(open), questionAnim(c, DurFast))
	okK := trigger.AnimateTo("question-progress-ok", questionStep(answered[current]), questionAnim(c, DurBase))
	trigger.Background(ui.Transparent.Mix(ThemeOverlayHover(c), hover).Mix(ThemeOverlaySelected(c), openK))
	focusRing(c, trigger)
	// 数字区宽度按总题数的字符数定死（「10/12」为 5 个字符，按 0.6 倍字号估宽），换题时不抖、不挤题目。
	numWidth := float32(2*len(strconv.Itoa(total))+1) * uiFont(TypeFootnote) * 0.6
	trigger.Children(func() {
		ui.Row(c.Key("question-progress-row-" + id)).AlignItems(ui.Center).Gap(Space1).Children(func() {
			ui.Row(c.Key("question-progress-num-" + id)).Width(numWidth).Justify(ui.Center).Children(func() {
				ui.RichText(c.Key("question-progress-text-"+id),
					ui.Span{Text: strconv.Itoa(current + 1), Weight: 600, Color: ThemePrimaryText(c).Mix(accent, okK)},
					ui.Span{Text: "/" + strconv.Itoa(total), Color: ThemeTertiaryText(c)},
				).FontSize(uiFont(TypeFootnote))
			})
			chevron := ui.Icon(c, IconChevronDown()).Size(IconSM, IconSM).TextColor(ThemeTertiaryText(c)).Shrink(0)
			chevron.Rotate(chevron.AnimateTo("question-progress-chevron", 180*questionStep(open), questionAnim(c, DurBase)))
		})
	})
	anchoredPopover(c, trigger, anchoredPopoverProps{Key: "question-picker-" + id, Open: &draft.pickerOpen, Align: BelowEnd}, func(_ ui.Element) {
		renderQuestionPicker(c, s, approval, draft, current, answered)
	})
}

// renderQuestionPicker 渲染题号浮层的内容：题号格按行排列，已答用 accent-subtle 底，当前题加 accent 描边与粗体。
func renderQuestionPicker(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, current int, answered []bool) {
	id := approval.RequestID
	total := len(answered)
	cols := min(total, questionPickerCols)
	gridWidth := float32(cols)*ControlMD + float32(cols-1)*Space1
	maxHeight := float32(questionPickerRows)*ControlMD + float32(questionPickerRows-1)*Space1
	popoverCard(c, ui.Column(c.Key("question-picker-card-"+id))).Padding(Space3).Children(func() {
		ui.Scroll(c.Key("question-picker-scroll-" + id)).MaxHeight(maxHeight).Children(func() {
			ui.Column(c.Key("question-picker-grid-" + id)).Width(gridWidth).Gap(Space1).Role(ui.RoleGroup).Label("题号").Children(func() {
				for start := 0; start < total; start += cols {
					ui.Row(c.Key(fmt.Sprintf("question-picker-row-%s-%d", id, start/cols))).Gap(Space1).Children(func() {
						for n := start; n < min(start+cols, total); n++ {
							renderQuestionPickerCell(c, s, approval, draft, n, current, cols, answered)
						}
					})
				}
			})
		})
	})
}

// renderQuestionPickerCell 渲染一个题号格：点击跳题并关浮层（点当前题只关浮层），方向键在格子间移动焦点。
func renderQuestionPickerCell(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, n, current, cols int, answered []bool) {
	id := approval.RequestID
	label := questionPickerLabel(approval.Questions[n], n)
	cell := ui.ButtonBase(c.Key(fmt.Sprintf("question-picker-cell-%s-%d", id, n))).
		Label(label).
		Tooltip(label).
		Size(ControlMD, ControlMD).
		Radius(RadiusMD).
		Shrink(0).
		OnClick(func() {
			cancelQuestionAutoAdvance(s, id)
			s.mu.Lock()
			draft.current = n
			draft.pickerOpen = false
			s.mu.Unlock()
			s.invalidate()
		})
	if draft.pickerFocus == n+1 {
		draft.pickerFocus = 0
		cell.Focus()
	}
	if cell.Focused() {
		to := n
		switch {
		case cell.Shortcut(0, ui.KeyLeft):
			to--
		case cell.Shortcut(0, ui.KeyRight):
			to++
		case cell.Shortcut(0, ui.KeyUp):
			to -= cols
		case cell.Shortcut(0, ui.KeyDown):
			to += cols
		}
		if to != n && to >= 0 && to < len(answered) {
			draft.pickerFocus = to + 1
			s.invalidate()
		}
	}
	accent := ThemeAccent(c)
	hover := cell.AnimateTo("question-picker-hover", questionStep(cell.Hovered()), questionAnim(c, DurFast))
	press := cell.AnimateTo("question-picker-press", questionStep(cell.Pressed()), questionAnim(c, DurFast))
	doneK := cell.AnimateTo("question-picker-done", questionStep(answered[n]), questionAnim(c, DurSnap))
	curK := cell.AnimateTo("question-picker-cur", questionStep(n == current), questionAnim(c, DurSnap))
	border := ThemeBorder(c)
	cell.Background(ui.Transparent.Mix(ThemeOverlayHover(c), hover).Mix(ThemeAccentSubtle(c), doneK).Mix(ThemeOverlayPressed(c), press)).
		Border(Hairline, border.Mix(border.Alpha(0), doneK).Mix(accent, curK))
	focusRing(c, cell)
	cell.Children(func() {
		ui.Row(c.Key(fmt.Sprintf("question-picker-num-%s-%d", id, n))).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			num := ui.Text(c, strconv.Itoa(n+1)).FontSize(uiFont(TypeFootnote)).TextColor(ThemeSecondaryText(c).Mix(ThemePrimaryText(c), curK).Mix(accent, doneK))
			if n == current {
				num.Bold()
			}
		})
	})
}

// questionRowBackground 按悬停、选中、按下三个动画进度混出选项行底色（按下盖过选中，选中盖过悬停）。
func questionRowBackground(c *ui.Context, hover, sel, press float32) ui.Color {
	return ui.Transparent.
		Mix(ThemeOverlayHover(c), hover).
		Mix(ThemeOverlaySelected(c), sel).
		Mix(ThemeOverlayPressed(c), press)
}

// renderQuestionSlot 渲染选项左侧的序号位：单选圆形、多选方形；选中后序号换成 accent 底白色勾。
// glyph 不为空时在未选中态画图标，否则画 digit；序号与勾在 sel 进度的前后半段依次淡出淡入。
func renderQuestionSlot(c *ui.Context, key string, digit string, glyph *ui.SVG, multi, responding bool, sel float32) {
	radius := RadiusPill
	if multi {
		radius = RadiusXS
	}
	accent := ThemeAccent(c)
	idle := ThemeTertiaryText(c)
	if responding {
		idle = idle.Alpha(0.6)
	}
	ui.Row(c.Key(key)).
		Size(questionSlotSize, questionSlotSize).
		Shrink(0).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Radius(radius).
		Background(accent.Alpha(sel)).
		Border(Hairline, ThemeOverlay(c, 0.22).Mix(accent, sel)).
		Children(func() {
			if sel < 0.5 {
				fade := 1 - 2*sel
				switch {
				case glyph != nil:
					ui.Icon(c, glyph).Size(IconSM-2, IconSM-2).TextColor(idle.Alpha(fade))
				case digit != "":
					ui.Text(c, digit).FontSize(uiFont(TypeCaption)).TextColor(idle.Alpha(fade))
				}
				return
			}
			grow := 2*sel - 1
			ui.Icon(c, IconCheck()).Size(IconSM, IconSM).TextColor(ThemeOnAccent(c).Alpha(grow)).Scale(0.5+0.5*grow, 0.5+0.5*grow)
		})
}

// renderQuestionRecommendedBadge 画设计稿里的「推荐」小标：accent-subtle 底、accent 字。
// 与 renderRecommendedBadge 的底色透明度和内边距不同，那个函数还被其他卡片使用，所以这里单独画。
func renderQuestionRecommendedBadge(c *ui.Context, key string) {
	ui.Row(c.Key(key)).Padding(0, Space2).Radius(RadiusXS).Background(ThemeAccentSubtle(c)).Shrink(0).Children(func() {
		ui.Text(c, "推荐").FontSize(uiFont(TypeCaption)).TextColor(ThemeAccent(c))
	})
}

// renderQuestionChoice 渲染一个选项行：高 32、圆角 8；标题单行省略，说明最多 3 行并放进 tooltip 与无障碍名称。
func renderQuestionChoice(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, qi, ci int, isSel, stacked, responding bool) {
	id := approval.RequestID
	q := approval.Questions[qi]
	choice := q.Choices[ci]
	title := displayChoiceLabel(choice.Label)
	detail := strings.TrimSpace(choice.Detail)
	recommended := isRecommendedChoice(choice.Label)
	key := fmt.Sprintf("%s-%d-%d", id, qi, ci)

	btn := ui.ButtonBase(c.Key("question-choice-"+key)).
		Label(questionChoiceA11yLabel(title, detail, recommended)).
		Role(ui.RoleOption).
		Checked(isSel).
		Disabled(responding).
		FillWidth().
		MinHeight(questionRowHeight).
		Padding(0, Space3).
		Radius(RadiusMD)
	if stacked {
		btn.Padding(Space3, Space3)
	}
	if detail != "" {
		btn.Tooltip(detail)
	} else {
		btn.Tooltip(title)
	}
	hover := btn.AnimateTo("question-hover", questionStep(!responding && btn.Hovered()), questionAnim(c, DurFast))
	press := btn.AnimateTo("question-press", questionStep(!responding && btn.Pressed()), questionAnim(c, DurFast))
	sel := btn.AnimateTo("question-sel", questionStep(isSel), questionAnim(c, DurSnap))
	btn.Background(questionRowBackground(c, hover, sel, press))
	focusRing(c, btn)
	btn.OnClick(func() {
		s.mu.Lock()
		if qi < len(draft.selected) {
			draft.selected[qi] = toggleQuestionChoice(q, draft.selected[qi], ci)
			if !q.AllowsMultiple {
				draft.others[qi] = ""
			}
		}
		s.mu.Unlock()
		if !q.AllowsMultiple {
			scheduleQuestionAutoAdvance(s, approval, qi)
		} else {
			cancelQuestionAutoAdvance(s, id)
		}
		s.invalidate()
	})

	titleColor, detailColor := ThemePrimaryText(c), ThemeSecondaryText(c)
	if responding {
		titleColor, detailColor = ThemeTertiaryText(c), ThemeTertiaryText(c)
	}
	digit := ""
	if ci < 9 {
		digit = strconv.Itoa(ci + 1)
	}
	btn.Children(func() {
		align := ui.Center
		if stacked {
			align = ui.Start
		}
		ui.Row(c.Key("question-choice-row-" + key)).FillWidth().Gap(Space4).AlignItems(align).Children(func() {
			renderQuestionSlot(c, "question-slot-"+key, digit, nil, q.AllowsMultiple, responding, sel)
			titleText := func() {
				ui.Text(c, title).FontSize(uiFont(TypeBody)).TextColor(titleColor).MaxLines(1).Ellipsis("…").MinWidth(0).Shrink(1)
			}
			if stacked {
				ui.Column(c.Key("question-choice-text-" + key)).Grow(1).MinWidth(0).Gap(Space1).Children(func() {
					ui.Row(c.Key("question-choice-title-" + key)).FillWidth().Gap(Space3).AlignItems(ui.Center).Children(func() {
						titleText()
						if recommended {
							renderQuestionRecommendedBadge(c, "question-choice-recommended-"+key)
						}
					})
					if detail != "" {
						ui.Text(c, detail).FontSize(uiFont(TypeFootnote)).TextColor(detailColor).MaxLines(questionDetailMaxLines).Ellipsis("…").MinWidth(0)
					}
				})
				return
			}
			titleText()
			if recommended {
				renderQuestionRecommendedBadge(c, "question-choice-recommended-"+key)
			}
			if detail != "" {
				ui.Text(c, detail).FontSize(uiFont(TypeFootnote)).TextColor(detailColor).MaxLines(1).Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
			} else {
				ui.Spacer(c)
			}
		})
	})
}

// renderQuestionOtherRow 渲染「其他」填空行（AllowsOther 或没有任何选项时）：序号位换成笔图标，填了内容即视为选中。
func renderQuestionOtherRow(c *ui.Context, s *chatState, approval Approval, draft *questionDraft, qi int, responding bool) {
	id := approval.RequestID
	q := approval.Questions[qi]
	key := fmt.Sprintf("%s-%d", id, qi)
	label := "其他"
	if len(q.Choices) == 0 {
		label = "输入答案"
	}
	filled := strings.TrimSpace(draft.others[qi]) != ""

	row := ui.Row(c.Key("question-other-row-"+key)).
		FillWidth().
		MinHeight(questionRowHeight).
		Padding(0, Space3).
		Gap(Space4).
		AlignItems(ui.Center).
		Radius(RadiusMD).
		Border(Hairline, ui.Transparent)
	hover := row.AnimateTo("question-hover", questionStep(!responding && row.Hovered()), questionAnim(c, DurFast))
	sel := row.AnimateTo("question-sel", questionStep(filled), questionAnim(c, DurSnap))
	row.Background(questionRowBackground(c, hover, sel, 0))
	row.Children(func() {
		renderQuestionSlot(c, "question-slot-other-"+key, "", IconPencilLine(), q.AllowsMultiple, responding, sel)
		in := ui.TextInputBase(c.Key("question-other-"+key), &draft.others[qi]).
			Label(label).
			Placeholder(label).
			Disabled(responding).
			FontSize(uiFont(TypeBody)).
			TextColor(ThemePrimaryText(c)).
			Grow(1).
			MinWidth(0).
			OnChange(func() {
				cancelQuestionAutoAdvance(s, id)
				s.mu.Lock()
				if !q.AllowsMultiple && strings.TrimSpace(draft.others[qi]) != "" {
					draft.selected[qi] = nil
				}
				s.mu.Unlock()
				s.invalidate()
			})
		focused := !responding && in.Focused()
		if focused {
			cancelQuestionAutoAdvance(s, id)
			row.Border(Hairline, ThemeAccent(c).Alpha(0.6))
		}
		s.mu.Lock()
		s.questionOtherFocused = focused
		s.mu.Unlock()
		if q.IsSecret {
			in.Password()
		}
	})
}

// renderRecommendedBadge 渲染选项的「推荐」小胶囊：强调色文字配低透明度强调底色。
func renderRecommendedBadge(c *ui.Context, key string) {
	accent := ThemeAccent(c)
	ui.Row(c.Key(key)).Padding(Hairline, Space2).Radius(RadiusXS).Background(accent.Alpha(0.15)).Shrink(0).Label("推荐").Children(func() {
		ui.Text(c, "推荐").FontSize(uiFont(TypeCaption)).TextColor(accent)
	})
}
