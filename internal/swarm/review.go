package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mcode/internal/kanban"
)

// defaultReviewTimeout 是合并审查会话的最长等待时间；超时后停止会话并转人工裁决。
const defaultReviewTimeout = 10 * time.Minute

// reviewStartGracePeriod 是审查会话迟迟没有线程 ID 时转人工的宽限时间。
const reviewStartGracePeriod = 2 * time.Minute

// ReviewVerdict 是审查会话给出的结构化结论。
type ReviewVerdict struct {
	Verdict string `json:"verdict"` // approve | reject | needs_human
	Reason  string `json:"reason"`
}

// reviewFencePattern 匹配行首的审查结论块起始行：三个反引号紧跟 swarm-review。
// 用字符串拼接写反引号，避免源码里出现连续三个反引号。
var reviewFencePattern = regexp.MustCompile("(?im)^```" + "[ \\t]*swarm-review[ \\t]*\\r?\\n")

// ParseReviewVerdict 解析审查会话回复里的结论块，取最后一个 swarm-review 围栏块。
// 块体按 JSON 解码，verdict 必须是 approve、reject、needs_human 之一；
// reject 缺少原因时降级为 needs_human，解析失败返回 false。
func ParseReviewVerdict(text string) (ReviewVerdict, bool) {
	locs := reviewFencePattern.FindAllStringIndex(text, -1)
	if len(locs) == 0 {
		return ReviewVerdict{}, false
	}
	start := locs[len(locs)-1][1]
	end := len(text)
	if fences := findLineStartFences(text, start); len(fences) > 0 {
		end = fences[0]
	}
	body := strings.TrimSpace(text[start:end])
	if body == "" {
		return ReviewVerdict{}, false
	}
	var v ReviewVerdict
	if err := json.Unmarshal([]byte(repairInvalidEscapes(body)), &v); err != nil {
		return ReviewVerdict{}, false
	}
	v.Verdict = strings.ToLower(strings.TrimSpace(v.Verdict))
	v.Reason = strings.TrimSpace(v.Reason)
	switch v.Verdict {
	case "approve", "needs_human":
		return v, true
	case "reject":
		if v.Reason == "" {
			return ReviewVerdict{Verdict: "needs_human", Reason: "审查结论缺少原因"}, true
		}
		return v, true
	default:
		return ReviewVerdict{}, false
	}
}

// reviewPrompt 生成只读审查会话的提示词：说明审查输入、只读约束与结论块格式。
func reviewPrompt(h Bee, patchPath string) string {
	changed := strings.Join(h.ChangedFiles, ", ")
	if changed == "" {
		changed = "(none reported)"
	}
	summary := strings.TrimSpace(h.Summary)
	if summary == "" {
		summary = "(no report yet)"
	}
	openFence := "```" + "swarm-review"
	closeFence := "`" + "`" + "`"
	example := openFence + "\n" + `{"verdict": "approve", "reason": "one sentence"}` + "\n" + closeFence
	return fmt.Sprintf(`You are reviewing one bee's changes before they merge into the lead's checkout.

Bee: %s
Task: %s

Brief given to the bee:
%s

The bee's report:
%s

Changed files: %s

Patch to review: %s
The bee's working copy (read-only for you): %s

Judge whether the change is correct, whether it completes the task, and whether it is safe to merge.
You must not modify any file, must not run state-changing git commands, and must not dispatch sub-agents.

Reply with exactly one fenced block:

%s

verdict is one of approve, reject, needs_human. Use reject with a concrete reason when the change is wrong or incomplete; use needs_human when a human must decide.
`, h.Name, h.Task, h.Prompt, summary, changed, patchPath, h.WorktreePath, example)
}

// reviewRuntime 在发起审查的那一刻解析审查会话的 provider/model/effort：
// 设置里有任一显式值就按设置解析，否则跟随主会话当前模型，最后回落到 bee 默认模型。
func (m *Manager) reviewRuntime(ctx context.Context, leadThreadID string) (provider, model, effort string) {
	s := m.Settings()
	if s.ReviewProvider != "" || s.ReviewModel != "" || s.ReviewEffort != "" {
		return resolveBeeRuntime(s.ReviewProvider, s.ReviewModel, s.ReviewEffort, s)
	}
	if m.opts.Driver != nil {
		if src, ok := m.opts.Driver.(LeadRuntimeSource); ok {
			if p, mo, e, ok := src.LeadRuntime(ctx, leadThreadID); ok {
				return p, mo, e
			}
		}
	}
	return resolveBeeRuntime("", "", "", s)
}

// saveReviewPatch 把待审查的补丁快照写到 <数据目录>/swarm-reviews/<leadID>-<index>.patch。
func (m *Manager) saveReviewPatch(leadID string, index int, patch []byte) (string, error) {
	patchDir := filepath.Join(m.DataDir(), "swarm-reviews")
	if err := os.MkdirAll(patchDir, 0755); err != nil {
		return "", fmt.Errorf("create review patch dir failed: %w", err)
	}
	patchPath := filepath.Join(patchDir, fmt.Sprintf("%s-%d.patch", leadID, index))
	if err := os.WriteFile(patchPath, patch, 0600); err != nil {
		return "", fmt.Errorf("write review patch file failed: %w", err)
	}
	return patchPath, nil
}

// worktreeFingerprint 计算 bee 工作副本的指纹：排序后的 git status 条目加上 ChangedFiles
// 的文件内容一起做 sha256。任何一步失败都返回空串，空串表示无法比较。
func (m *Manager) worktreeFingerprint(ctx context.Context, h Bee) string {
	if m.opts.Git == nil {
		return ""
	}
	entries, err := m.opts.Git.Status(ctx, h.WorktreePath)
	if err != nil {
		return ""
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, fmt.Sprintf("%c%c %s\x00%s", e.Index, e.Worktree, e.Path, e.OrigPath))
	}
	sort.Strings(lines)

	hsh := sha256.New()
	for _, line := range lines {
		_, _ = hsh.Write([]byte(line))
		_, _ = hsh.Write([]byte{'\n'})
	}
	paths := append([]string(nil), h.ChangedFiles...)
	sort.Strings(paths)
	for _, p := range paths {
		data, err := os.ReadFile(filepath.Join(h.WorktreePath, p))
		if err != nil {
			return ""
		}
		_, _ = fmt.Fprintf(hsh, "%s\x00%d\x00", p, len(data))
		_, _ = hsh.Write(data)
	}
	return hex.EncodeToString(hsh.Sum(nil))
}

// startMergeReview 为待审查的 bee 起一个只读审查会话，并把会话与快照信息写回 bee。
// 任何一步失败都转人工审查，不阻塞 bee 的状态推进。
func (m *Manager) startMergeReview(ctx context.Context, leadThreadID, beeThreadID string) {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	var h Bee
	found := false
	if ok {
		for i := range lead.bees {
			if lead.bees[i].ThreadID != beeThreadID {
				continue
			}
			h = lead.bees[i].clone()
			found = true
			break
		}
	}
	m.mu.Unlock()
	if !found || h.ReviewState != ReviewRunning || h.ReviewThreadID != "" {
		return
	}

	fail := func(reason string) {
		m.markReviewNeedsHuman(leadThreadID, beeThreadID, "无法启动审查会话："+reason)
	}

	patchPath := h.ReviewPatchPath
	var err error
	if !reviewPatchReusable(patchPath) {
		patchPath, err = m.saveReviewPatch(leadThreadID, h.Index, h.MergePatch)
		if err != nil {
			fail(err.Error())
			return
		}
	}
	sum := sha256.Sum256(h.MergePatch)
	patchSHA := hex.EncodeToString(sum[:])
	fingerprint := m.worktreeFingerprint(ctx, h)
	provider, model, effort := m.reviewRuntime(ctx, leadThreadID)
	// provider 为空时不能只传 model：那会把模型套到主会话继承来的另一个 provider 上。
	if provider == "" {
		model = ""
	}

	if m.opts.Driver == nil {
		fail("未配置会话驱动")
		return
	}
	threadID, err := m.opts.Driver.StartSession(ctx, kanban.SessionRequest{
		LeadThreadID: leadThreadID,
		ProjectID:    h.ProjectID,
		WorkDir:      h.WorktreePath,
		Title:        "合并审查：" + h.Name,
		Prompt:       reviewPrompt(h, patchPath),
		RuntimeMode:  "readonly",
		Provider:     provider,
		Model:        model,
		Effort:       effort,
	})
	if err != nil {
		fail(err.Error())
		return
	}
	if leadThreadID != "" {
		if s, ok := m.opts.Driver.(ThreadRelationSetter); ok {
			s.SetThreadRelation(threadID, leadThreadID, true)
		} else if s, ok := m.opts.Driver.(ThreadRelationContextSetter); ok {
			_ = s.SetThreadRelation(ctx, threadID, leadThreadID, true)
		}
		if m.opts.Persister != nil {
			if s, ok := m.opts.Persister.(ThreadRelationContextSetter); ok {
				_ = s.SetThreadRelation(ctx, threadID, leadThreadID, true)
			} else if s, ok := m.opts.Persister.(ThreadRelationSetter); ok {
				s.SetThreadRelation(threadID, leadThreadID, true)
			}
		}
	}

	now := m.now()
	m.mu.Lock()
	written := false
	if lead, ok = m.leads[leadThreadID]; ok {
		for i := range lead.bees {
			b := &lead.bees[i]
			if b.ThreadID != beeThreadID || b.ReviewState != ReviewRunning || b.ReviewThreadID != "" {
				continue
			}
			b.ReviewThreadID = threadID
			b.ReviewStartedAt = &now
			b.ReviewPatchPath = patchPath
			b.ReviewPatchSHA = patchSHA
			b.ReviewFingerprint = fingerprint
			b.ReviewProvider = provider
			b.ReviewModel = model
			b.ReviewEffort = effort
			written = true
			break
		}
	}
	m.mu.Unlock()
	if !written {
		// 会话启动期间 bee 已被清理或转人工：停掉刚起的审查会话，避免留下孤儿。
		if m.opts.StopSession != nil {
			_ = m.opts.StopSession(ctx, threadID)
		}
		return
	}
	m.persistLead(leadThreadID)
	m.notifyChange(leadThreadID)
}

// reviewActive 确认 bee 仍停在同一轮审查里，避免重复推进状态。
func (m *Manager) reviewActive(leadThreadID, beeThreadID, reviewThreadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return false
	}
	for i := range lead.bees {
		if lead.bees[i].ThreadID != beeThreadID {
			continue
		}
		return lead.bees[i].ReviewState == ReviewRunning && lead.bees[i].ReviewThreadID == reviewThreadID
	}
	return false
}

// reviewSnapshotIntact 校验审查补丁文件与 bee 当前补丁仍等于发起审查时的快照。
func (m *Manager) reviewSnapshotIntact(leadThreadID string, h Bee) bool {
	if h.ReviewPatchPath == "" || h.ReviewPatchSHA == "" {
		return false
	}
	data, err := os.ReadFile(h.ReviewPatchPath)
	if err != nil {
		return false
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != h.ReviewPatchSHA {
		return false
	}
	m.mu.Lock()
	var current []byte
	found := false
	if lead, ok := m.leads[leadThreadID]; ok {
		for i := range lead.bees {
			if lead.bees[i].ThreadID == h.ThreadID {
				current = append([]byte(nil), lead.bees[i].MergePatch...)
				found = true
				break
			}
		}
	}
	m.mu.Unlock()
	if !found {
		return false
	}
	cur := sha256.Sum256(current)
	return hex.EncodeToString(cur[:]) == h.ReviewPatchSHA
}

// pollReviews 推进所有 running 状态的合并审查：处理未启动、超时、失败、解析结论与快照校验。
// 所有驱动调用与状态推进方法都在锁外执行。
func (m *Manager) pollReviews(ctx context.Context) {
	type reviewTarget struct {
		leadThreadID string
		bee          Bee
	}
	var targets []reviewTarget
	m.mu.Lock()
	for leadID, lead := range m.leads {
		for i := range lead.bees {
			if lead.bees[i].ReviewState != ReviewRunning {
				continue
			}
			targets = append(targets, reviewTarget{leadThreadID: leadID, bee: lead.bees[i].clone()})
		}
	}
	m.mu.Unlock()
	if len(targets) == 0 {
		return
	}

	now := m.now()
	for _, tgt := range targets {
		m.pollReview(ctx, tgt.leadThreadID, tgt.bee, now)
	}
}

// pollReview 推进单个 bee 的审查状态。审查漏斗的 l0/l2_queued/l2 阶段只做超时兜底，
// 实际推进由 review_funnel.go 的 worker 负责；l3 与空 stage 走原有的审查会话逻辑。
func (m *Manager) pollReview(ctx context.Context, leadThreadID string, h Bee, now time.Time) {
	switch h.ReviewStage {
	case ReviewStageL0:
		if h.ReviewApplyPending {
			// 只等主工作区落定后重试合入，不再跑检查或模型。
			return
		}
		if h.ReviewStartedAt == nil {
			// 已排队但还没轮到：只防排队饿死，不能套用“审查会话未启动”的 2 分钟宽限。
			since := h.ReviewQueuedAt
			if since == nil {
				since = h.FinishedAt
			}
			if since != nil && now.Sub(*since) > reviewQueueTimeout {
				if m.reviewActive(leadThreadID, h.ThreadID, "") {
					m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "L0 检查排队超时")
				}
			}
			return
		}
		// L0 是同步检查命令，预算取项目配置超时再加 30 秒余量。
		if now.Sub(*h.ReviewStartedAt) > m.reviewCheckBudget(leadThreadID) {
			if m.reviewActive(leadThreadID, h.ThreadID, "") {
				m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "L0 检查超时")
			}
		}
		return
	case ReviewStageL2Queued:
		since := h.ReviewQueuedAt
		if since == nil {
			since = h.FinishedAt
		}
		if since != nil && now.Sub(*since) > reviewQueueTimeout {
			if m.reviewActive(leadThreadID, h.ThreadID, "") {
				m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "L2 审查排队超时")
			}
		}
		return
	case ReviewStageL2:
		if h.ReviewApplyPending {
			// 结论已出，只等主工作区落定后重试合入，不能按 L2 调用超时处理。
			return
		}
		if h.ReviewStartedAt != nil && now.Sub(*h.ReviewStartedAt) > reviewL2Timeout {
			if m.reviewActive(leadThreadID, h.ThreadID, "") {
				m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "L2 审查超时")
			}
		}
		return
	}

	if h.ReviewThreadID == "" {
		since := h.ReviewStartedAt
		if since == nil {
			since = h.FinishedAt
		}
		// 两个时间都没有时不作判断：发起审查的路径随后会补上时间或直接转人工。
		if since != nil && now.Sub(*since) > reviewStartGracePeriod {
			if m.reviewActive(leadThreadID, h.ThreadID, "") {
				m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "审查会话未启动")
			}
		}
		return
	}
	if m.opts.Driver == nil {
		return
	}
	st, err := m.opts.Driver.SessionState(ctx, h.ReviewThreadID)
	if err != nil {
		// 驱动暂时读不到状态：留给下个 Tick 再试。
		return
	}
	if st.Running {
		if h.ReviewStartedAt != nil && now.Sub(*h.ReviewStartedAt) > defaultReviewTimeout {
			if m.opts.StopSession != nil {
				_ = m.opts.StopSession(ctx, h.ReviewThreadID)
			}
			if m.reviewActive(leadThreadID, h.ThreadID, h.ReviewThreadID) {
				m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "审查超时")
			}
		}
		return
	}
	if st.Failed {
		if m.reviewActive(leadThreadID, h.ThreadID, h.ReviewThreadID) {
			m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "审查会话失败："+st.FailureReason)
		}
		return
	}
	if !m.reviewActive(leadThreadID, h.ThreadID, h.ReviewThreadID) {
		return
	}
	if fp := m.worktreeFingerprint(ctx, h); fp != "" && h.ReviewFingerprint != "" && fp != h.ReviewFingerprint {
		m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "审查会话改动了工作副本")
		return
	}
	verdict, ok := ParseReviewVerdict(st.LastAssistantText)
	if !ok {
		m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "未能解析审查结论")
		return
	}
	switch verdict.Verdict {
	case "approve":
		if !m.reviewSnapshotIntact(leadThreadID, h) {
			m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "审查快照已变化，请人工确认")
			return
		}
		m.applyReviewApprove(ctx, leadThreadID, h.ThreadID, verdict.Reason)
	case "reject":
		m.applyReviewReject(leadThreadID, h.ThreadID, verdict.Reason)
	case "needs_human":
		m.markReviewNeedsHuman(leadThreadID, h.ThreadID, verdict.Reason)
	}
}
