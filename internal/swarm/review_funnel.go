package swarm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"mcode/internal/reviewcheck"
	"mcode/internal/reviewfunnel"
	"mcode/internal/textgen"
)

// l0MaxOutput 是 L0 检查命令保留的输出上限，避免一次失败把整段日志灌进状态。
const l0MaxOutput = 64 * 1024

// reviewBrief 返回 bee 的完整任务描述：Prompt 是派发时的全文，Task 只是短标题。
func reviewBrief(h Bee) string {
	if prompt := strings.TrimSpace(h.Prompt); prompt != "" {
		return prompt
	}
	return h.Task
}

// startReviewFunnelWorkers 为有 L0/L2 工作的 lead 各起一个漏斗 worker。
// funnelBusy 保证每个 lead 同时只有一个 worker；Stop 之后不再起新 worker。
func (m *Manager) startReviewFunnelWorkers(ctx context.Context) {
	m.stateMu.Lock()
	if m.funnelCtx != nil {
		ctx = m.funnelCtx
	}
	running := m.running
	m.stateMu.Unlock()
	if !running && m.opts.Driver == nil {
		// 既没有调度循环也没有测试驱动时不起后台任务。
		return
	}

	leads := m.leadsNeedingFunnel()
	if len(leads) == 0 {
		return
	}

	m.stateMu.Lock()
	defer m.stateMu.Unlock()
	if !m.running && m.opts.Driver == nil {
		return
	}
	for _, leadID := range leads {
		if m.funnelBusy[leadID] {
			continue
		}
		m.funnelBusy[leadID] = true
		wctx := ctx
		leadID := leadID
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runReviewFunnel(wctx, leadID)
		}()
	}
}

// leadsNeedingFunnel 返回当前有漏斗工作的 lead：l0 尚未启动、l2 合入待重试，
// 或已有可以通过攒批条件发车的 l2_queued。
func (m *Manager) leadsNeedingFunnel() []string {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for leadID, lead := range m.leads {
		if leadHasFunnelWorkLocked(m, lead, now) {
			out = append(out, leadID)
		}
	}
	sort.Strings(out)
	return out
}

// leadHasFunnelWorkLocked 判断单个 lead 是否有可推进的漏斗工作；调用方需持有 m.mu。
func leadHasFunnelWorkLocked(m *Manager, lead *leadState, now time.Time) bool {
	idle := !lead.dispatching && m.queuedCountLocked(lead.leadThreadID) == 0
	if idle {
		for i := range lead.bees {
			if lead.bees[i].Status == StatusRunning {
				idle = false
				break
			}
		}
	}
	queued := 0
	var oldest time.Time
	for i := range lead.bees {
		b := &lead.bees[i]
		if b.ReviewState != ReviewRunning {
			continue
		}
		switch b.ReviewStage {
		case ReviewStageL0:
			if b.ReviewApplyPending || b.ReviewStartedAt == nil {
				return true
			}
		case ReviewStageL2:
			if b.ReviewApplyPending {
				return true
			}
		case ReviewStageL2Queued:
			if b.ReviewAttempts >= reviewMaxAttempts || b.ReviewApplyPending {
				continue
			}
			queued++
			at := reviewQueuedTime(*b)
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
		}
	}
	if queued == 0 {
		return false
	}
	return idle || queued >= reviewBatchMax || now.Sub(oldest) >= reviewBatchWindow
}

// runReviewFunnel 串行推进一个 lead 的漏斗：先顺序跑完所有 l0，再发 L2 批次，
// 直到没有可推进的工作。所有阻塞操作都在锁外执行，写回前按晚到结果规则校验。
func (m *Manager) runReviewFunnel(ctx context.Context, leadThreadID string) {
	defer func() {
		m.stateMu.Lock()
		delete(m.funnelBusy, leadThreadID)
		m.stateMu.Unlock()
		m.notifyChange(leadThreadID)
	}()

	attempted := make(map[string]bool)
	retriedApply := false
	for ctx.Err() == nil {
		if !retriedApply {
			if m.retryPendingReviewApplies(ctx, leadThreadID) {
				retriedApply = true
				continue
			}
		}
		if h, ok := m.nextL0Bee(leadThreadID, attempted); ok {
			attempted[h.ThreadID] = true
			m.runL0(ctx, leadThreadID, h)
			continue
		}
		now := m.now()
		batch := m.collectL2Batch(leadThreadID, now)
		if len(batch) == 0 {
			return
		}
		claimed := m.claimL2Batch(leadThreadID, batch, now)
		if len(claimed) == 0 {
			return
		}
		m.applyL2Batch(ctx, leadThreadID, claimed)
	}
}

// nextL0Bee 取下一个尚未启动检查的 l0 bee，按 Index 顺序。
// ReviewStartedAt 非空的 l0 不在本进程里重跑：重启后只能等 pollReview 超时转人工。
func (m *Manager) nextL0Bee(leadThreadID string, attempted map[string]bool) (Bee, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return Bee{}, false
	}
	var best Bee
	found := false
	for i := range lead.bees {
		b := &lead.bees[i]
		if b.ReviewState != ReviewRunning || b.ReviewStage != ReviewStageL0 || b.ReviewStartedAt != nil {
			continue
		}
		if attempted[b.ThreadID] {
			continue
		}
		if !found || b.Index < best.Index {
			best = b.clone()
			found = true
		}
	}
	return best, found
}

// retryPendingReviewApplies 只重试 L2 已通过但主工作区未落定的合入，不再调用模型。
func (m *Manager) retryPendingReviewApplies(ctx context.Context, leadThreadID string) bool {
	m.mu.Lock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		m.mu.Unlock()
		return false
	}
	var targets []Bee
	for i := range lead.bees {
		b := &lead.bees[i]
		if b.ReviewState != ReviewRunning || !b.ReviewApplyPending {
			continue
		}
		now := m.now()
		b.ReviewStartedAt = &now
		targets = append(targets, b.clone())
	}
	m.mu.Unlock()
	for _, h := range targets {
		m.applyReviewApprove(ctx, leadThreadID, h.ThreadID, h.ReviewReason)
		m.setReviewApplyPending(leadThreadID, h.ThreadID, h.ReviewPatchSHA)
	}
	return len(targets) > 0
}

// setReviewApplyPending 在合入后重新读取 bee：仍未通过且待合入时标记下轮只重试合入。
func (m *Manager) setReviewApplyPending(leadThreadID, beeThreadID, patchSHA string) {
	m.mu.Lock()
	var pending bool
	if lead, ok := m.leads[leadThreadID]; ok {
		for i := range lead.bees {
			b := &lead.bees[i]
			if b.ThreadID != beeThreadID || b.ReviewPatchSHA != patchSHA {
				continue
			}
			if b.ReviewState == ReviewRunning && b.PendingMerge {
				b.ReviewApplyPending = true
				pending = true
			} else {
				b.ReviewApplyPending = false
			}
			break
		}
	}
	m.mu.Unlock()
	if pending {
		m.notifyChange(leadThreadID)
	}
}

// runL0 执行一个 bee 的确定性检查与 L1 规则。
func (m *Manager) runL0(ctx context.Context, leadThreadID string, h Bee) {
	if ctx.Err() != nil {
		return
	}
	patchPath, patchSHA, err := m.saveFunnelPatch(leadThreadID, h)
	if err != nil {
		m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "无法保存审查补丁："+err.Error())
		return
	}
	now := m.now()
	if !m.funnelUpdate(leadThreadID, h.ThreadID, ReviewStageL0, h.ReviewPatchSHA, func(b *Bee) {
		b.ReviewStartedAt = &now
		b.ReviewPatchPath = patchPath
		b.ReviewPatchSHA = patchSHA
	}) {
		return
	}
	h.ReviewStartedAt = &now
	h.ReviewPatchPath = patchPath
	h.ReviewPatchSHA = patchSHA
	m.persistLead(leadThreadID)
	m.notifyChange(leadThreadID)

	if ctx.Err() != nil {
		return
	}
	runtime := m.reviewProjectRuntime(ctx, leadThreadID, h.WorktreePath)
	changes := reviewChanges(h.ChangedFiles)
	allPlan := len(changes) > 0 && allOfClass(changes, reviewcheck.ClassPlan)

	var check reviewcheck.Result
	if allPlan {
		check = reviewcheck.Result{OK: true, Skipped: true, Display: "计划文档，跳过检查"}
	} else {
		check = m.runReviewCheck(ctx, h, runtime)
	}
	if ctx.Err() != nil {
		// Stop 或上层取消时检查结果不可信；保持 l0，由 pollReview 的超时兜底转人工。
		return
	}
	fingerprint := m.worktreeFingerprint(ctx, h)
	concerns := l0Concerns(h, check)
	if check.OK && !check.Skipped && strings.TrimSpace(check.Display) != "" {
		concerns = append(concerns, "L0 检查通过："+check.Display)
	}

	if !allPlan && !check.Skipped && !check.OK {
		reason := fmt.Sprintf("L0 检查失败：%s", check.Display)
		if check.TimedOut {
			reason += "（超时）"
		}
		if tail := strings.TrimSpace(reviewcheck.Tail(check.Output, 30, 4096)); tail != "" {
			reason += "\n" + tail
		}
		m.finishL0Reject(leadThreadID, h, fingerprint, concerns, reason)
		return
	}

	if h.ReworkOf != "" {
		// 返工只审增量：L1 的“小改动自动通过”不能套在返工上，直接进 L2。
		m.enterL2Queue(leadThreadID, h, fingerprint, concerns, check)
		return
	}

	added, deleted := patchLineCounts(h.MergePatch)
	if h.LinesAdded > 0 || h.LinesDeleted > 0 {
		added, deleted = h.LinesAdded, h.LinesDeleted
	}
	scope := reviewcheck.ParseScope(reviewBrief(h), h.ChangedFiles, h.WritePaths)
	decision, ok := reviewfunnel.DecideL1(reviewfunnel.L1Input{
		Changes:  changes,
		Added:    added,
		Deleted:  deleted,
		Scope:    scope,
		MaxLines: m.Settings().ReviewMaxChangedLines,
	})
	concerns = append(concerns, scopeConcerns(scope, changes)...)
	concerns = append(concerns, decision.Concerns...)
	if !ok || decision.Verdict != reviewfunnel.VerdictApprove {
		m.enterL2Queue(leadThreadID, h, fingerprint, concerns, check)
		return
	}

	layer := "l1"
	if !m.funnelUpdate(leadThreadID, h.ThreadID, ReviewStageL0, patchSHA, func(b *Bee) {
		b.ReviewLayer = layer
		b.ReviewConcerns = append([]string(nil), concerns...)
		b.ReviewReason = decision.Reason
		b.ReviewFingerprint = fingerprint
		b.ReviewApplyPending = false
	}) {
		return
	}
	m.persistLead(leadThreadID)
	m.notifyChange(leadThreadID)
	m.applyReviewApprove(ctx, leadThreadID, h.ThreadID, decision.Reason)
	m.setReviewApplyPending(leadThreadID, h.ThreadID, patchSHA)
}

// runReviewCheck 调用注入的检查函数或真实 reviewcheck；配置与命令都在锁外解析。
func (m *Manager) runReviewCheck(ctx context.Context, h Bee, rt reviewRuntimeInfo) reviewcheck.Result {
	if m.opts.ReviewRunCheck != nil {
		return m.opts.ReviewRunCheck(ctx, h.WorktreePath, h.ChangedFiles, rt.configured, rt.timeout)
	}
	cmd, ok := reviewcheck.ResolveCommand(h.WorktreePath, h.ChangedFiles, rt.configured, rt.timeout)
	if !ok {
		return reviewcheck.Result{OK: true, Skipped: true, Display: "未识别检查命令"}
	}
	cmd = reviewcheck.WithTests(cmd, rt.runTests)
	return reviewcheck.Run(ctx, h.WorktreePath, cmd, l0MaxOutput)
}

// reviewRuntimeInfo 是 L0 需要的项目配置快照。
type reviewRuntimeInfo struct {
	dataDir    string
	projectDir string
	configured string
	timeout    time.Duration
	runTests   bool
}

// reviewProjectRuntime 解析项目配置：项目配置 Command 优先，其次 Settings.ReviewCheckCommand，
// 都为空时交给 ResolveCommand 自动识别。
func (m *Manager) reviewProjectRuntime(ctx context.Context, leadThreadID, beeWorktree string) reviewRuntimeInfo {
	dataDir := m.DataDir()
	m.mu.Lock()
	projectDir := beeWorktree
	leadWorkDir, projectID := "", ""
	if lead, ok := m.leads[leadThreadID]; ok {
		leadWorkDir, projectID = lead.leadWorkDir, lead.projectID
		if lead.leadWorkDir != "" {
			projectDir = lead.leadWorkDir
		}
	}
	m.mu.Unlock()
	if m.opts.ResolveProject != nil && leadWorkDir != "" {
		if pid, dir, ok := m.opts.ResolveProject(ctx, leadWorkDir); ok && dir != "" && (projectID == "" || pid == projectID) {
			projectDir = dir
		}
	}

	cfg := reviewcheck.ProjectConfig{}
	var err error
	if m.opts.ReviewProjectConfig != nil {
		cfg, err = m.opts.ReviewProjectConfig(dataDir, projectDir)
	} else {
		cfg, err = reviewcheck.LoadProjectConfig(dataDir, projectDir)
	}
	_ = err
	configured := strings.TrimSpace(cfg.Command)
	if configured == "" {
		configured = strings.TrimSpace(m.Settings().ReviewCheckCommand)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = reviewcheck.DefaultTimeout
	}
	return reviewRuntimeInfo{
		dataDir:    dataDir,
		projectDir: projectDir,
		configured: configured,
		timeout:    timeout,
		runTests:   cfg.RunTests,
	}
}

// reviewCheckBudget 是 L0 的兜底超时：项目检查超时再加 30 秒余量。
func (m *Manager) reviewCheckBudget(leadThreadID string) time.Duration {
	rt := m.reviewProjectRuntime(context.Background(), leadThreadID, "")
	return rt.timeout + reviewCheckBudgetMargin
}

// finishL0Reject 写回 L0 失败结论并驳回。
func (m *Manager) finishL0Reject(leadThreadID string, h Bee, fingerprint string, concerns []string, reason string) {
	if !m.funnelUpdate(leadThreadID, h.ThreadID, ReviewStageL0, h.ReviewPatchSHA, func(b *Bee) {
		b.ReviewLayer = "l0"
		b.ReviewConcerns = append([]string(nil), concerns...)
		b.ReviewReason = reason
		b.ReviewFingerprint = fingerprint
		b.ReviewStartedAt = nil
	}) {
		return
	}
	m.applyReviewReject(leadThreadID, h.ThreadID, reason)
}

// enterL2Queue 把 bee 从 l0 放到 l2_queued，并保留 L0 的检查结果与范围疑点。
func (m *Manager) enterL2Queue(leadThreadID string, h Bee, fingerprint string, concerns []string, check reviewcheck.Result) {
	now := m.now()
	if !m.funnelUpdate(leadThreadID, h.ThreadID, ReviewStageL0, h.ReviewPatchSHA, func(b *Bee) {
		b.ReviewStage = ReviewStageL2Queued
		b.ReviewQueuedAt = &now
		b.ReviewStartedAt = nil
		b.ReviewConcerns = append([]string(nil), concerns...)
		b.ReviewFingerprint = fingerprint
		b.ReviewApplyPending = false
	}) {
		return
	}
	m.persistLead(leadThreadID)
	m.notifyChange(leadThreadID)
}

// applyL2Batch 组装并发出一次 L2 批量审查，然后逐条应用结论。
func (m *Manager) applyL2Batch(ctx context.Context, leadThreadID string, batch []Bee) {
	budget := reviewfunnel.DefaultBudget()
	reqs := make([]reviewfunnel.L2Request, 0, len(batch))
	segmentBytes := make(map[int]int, len(batch))
	for _, h := range batch {
		req, ok := m.buildL2Request(ctx, leadThreadID, h, budget)
		if !ok {
			continue
		}
		reqs = append(reqs, req)
		if req.Rework != nil {
			// 返工只送上次原因与增量，公共前缀之外的段落按这些字段分摊。
			segmentBytes[req.Index] = len(req.Rework.PreviousReason) + len(req.Rework.Delta) + len(req.Summary) + len(req.Brief)
		} else {
			segmentBytes[req.Index] = len(req.Diff) + len(req.Summary) + len(req.Brief)
		}
		if segmentBytes[req.Index] == 0 {
			segmentBytes[req.Index] = 1
		}
	}
	if len(reqs) == 0 {
		return
	}
	ids := make([]int, 0, len(reqs))
	for _, r := range reqs {
		ids = append(ids, r.Index)
	}

	prompt, _ := reviewfunnel.BuildPrompt(reqs, budget)
	callCtx, cancel := context.WithTimeout(ctx, reviewL2Timeout)
	text, provider, model, fallbackConcerns, err := m.reviewGenerate(callCtx, prompt)
	cancel()
	if ctx.Err() != nil {
		// Stop 或上层取消时结果不可信，保持 l2 由 pollReview 的超时兜底。
		return
	}
	tokens := reviewfunnel.EstimateTokens(prompt)

	byIndex := make(map[int]Bee, len(batch))
	for _, h := range batch {
		byIndex[h.Index] = h
	}
	if err != nil {
		for _, h := range batch {
			m.promoteL3(ctx, leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, "L2 审查调用失败："+err.Error())
		}
		return
	}
	decisions, missing, err := reviewfunnel.ParseBatch(text, ids)
	if err != nil {
		for _, h := range batch {
			m.promoteL3(ctx, leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, "L2 审查结论无法解析："+err.Error())
		}
		return
	}
	missingSet := make(map[int]bool, len(missing))
	for _, idx := range missing {
		missingSet[idx] = true
	}
	totalSegment := 0
	for _, n := range segmentBytes {
		totalSegment += n
	}
	for _, req := range reqs {
		h, ok := byIndex[req.Index]
		if !ok {
			continue
		}
		if missingSet[req.Index] {
			if m.requeueL2Missing(ctx, leadThreadID, h.ThreadID, h.ReviewPatchSHA, "L2 审查结果缺少该 bee") {
				continue
			}
			// 第一次缺失会进入下一批单独重试，不再应用任何结论。
			continue
		}
		decision, ok := decisions[req.Index]
		if !ok {
			m.promoteL3(ctx, leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, "L2 审查结果缺少该 bee")
			continue
		}
		share := tokens
		if totalSegment > 0 {
			share = tokens * segmentBytes[req.Index] / totalSegment
		}
		if share < 1 {
			share = 1
		}
		m.applyL2Decision(ctx, leadThreadID, h, decision, share, fallbackConcerns, provider, model)
	}
}

// buildL2Request 组装单个 bee 的 L2 请求；diff 或返工增量取不到时升级 L3 并返回 false。
func (m *Manager) buildL2Request(ctx context.Context, leadThreadID string, h Bee, budget reviewfunnel.Budget) (reviewfunnel.L2Request, bool) {
	req := reviewfunnel.L2Request{
		Index:   h.Index,
		Name:    h.Name,
		Brief:   reviewBrief(h),
		Summary: h.Summary,
		Check:   reviewcheck.Result{OK: true, Display: "L0 检查通过"},
		Scope:   reviewcheck.ParseScope(reviewBrief(h), h.ChangedFiles, h.WritePaths),
	}
	diff, truncated, err := m.reviewDiff(ctx, h.WorktreePath, h.BaseCommit, h.ChangedFiles, budget.PerRequestBytes)
	if err != nil {
		m.promoteL3(ctx, leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, "L2 读取 diff 失败："+err.Error())
		return reviewfunnel.L2Request{}, false
	}
	req.Diff = diff
	req.DiffTruncated = truncated
	if h.ReworkOf != "" {
		rework := &reviewfunnel.ReworkInput{
			PreviousReason: h.ReworkReason,
			OldPatchPath:   h.ReworkPatchPath,
		}
		old, ok := m.reworkSource(leadThreadID, h.ReworkOf)
		if !ok || old.WorktreePath == "" || !dirExists(old.WorktreePath) {
			rework.OldUnavailable = true
		} else {
			delta, err := m.reviewDiffBetween(ctx, old.WorktreePath, h.WorktreePath, h.ChangedFiles, budget.PerRequestBytes)
			if err != nil {
				rework.OldUnavailable = true
			} else {
				rework.Delta = delta
			}
		}
		req.Rework = rework
	}
	return req, true
}

// applyL2Decision 在校验快照后应用单条 L2 结论。
func (m *Manager) applyL2Decision(ctx context.Context, leadThreadID string, h Bee, decision reviewfunnel.Decision, tokens int, extraConcerns []string, provider, model string) {
	concerns := append([]string(nil), extraConcerns...)
	concerns = append(concerns, decision.Concerns...)
	switch decision.Verdict {
	case reviewfunnel.VerdictApprove:
		if !m.reviewSnapshotIntact(leadThreadID, h) {
			m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "L2 审查快照已变化")
			return
		}
		if fp := m.worktreeFingerprint(ctx, h); fp != "" && h.ReviewFingerprint != "" && fp != h.ReviewFingerprint {
			m.markReviewNeedsHuman(leadThreadID, h.ThreadID, "L2 审查快照已变化")
			return
		}
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "L2 审查通过"
		}
		if !m.funnelUpdate(leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, func(b *Bee) {
			b.ReviewLayer = "l2"
			b.ReviewConcerns = append([]string(nil), concerns...)
			b.ReviewReason = reason
			b.ReviewTokens = tokens
			b.ReviewTokensEstimated = true
			b.ReviewProvider = provider
			b.ReviewModel = model
			b.ReviewApplyPending = false
		}) {
			return
		}
		m.persistLead(leadThreadID)
		m.notifyChange(leadThreadID)
		m.applyReviewApprove(ctx, leadThreadID, h.ThreadID, reason)
		m.setReviewApplyPending(leadThreadID, h.ThreadID, h.ReviewPatchSHA)
	case reviewfunnel.VerdictReject:
		if allPlanChanges(h.ChangedFiles) {
			m.promoteL3(ctx, leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, "计划文档不接受 L2 驳回，转人工审查")
			return
		}
		reason := strings.TrimSpace(decision.Reason)
		if reason == "" {
			reason = "L2 审查未通过"
		}
		if !m.funnelUpdate(leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, func(b *Bee) {
			b.ReviewLayer = "l2"
			b.ReviewConcerns = append([]string(nil), concerns...)
			b.ReviewReason = reason
			b.ReviewTokens = tokens
			b.ReviewTokensEstimated = true
			b.ReviewApplyPending = false
		}) {
			return
		}
		m.applyReviewReject(leadThreadID, h.ThreadID, reason)
	default:
		m.promoteL3(ctx, leadThreadID, h.ThreadID, ReviewStageL2, h.ReviewPatchSHA, "L2 审查要求人工确认")
	}
}

// reviewModelFor 为某个 provider 选择模型与 effort：显式 provider 用用户配置，
// 回退到其他 provider 时改用该 provider 的最便宜模型，effort 也只跟随显式 provider。
func reviewModelFor(provider, explicit, model, effort string) (string, string) {
	if provider == explicit {
		if model = strings.TrimSpace(model); model != "" {
			return model, effort
		}
		return textgen.CheapestModel(provider), effort
	}
	return textgen.CheapestModel(provider), ""
}

// reviewGenerate 按 provider 顺序调用一次性模型；显式 provider 不可用时的回退写入 concerns。
func (m *Manager) reviewGenerate(ctx context.Context, prompt string) (text, provider, model string, concerns []string, err error) {
	s := m.Settings()
	explicit := strings.ToLower(strings.TrimSpace(s.ReviewProvider))
	order := textgen.ReviewProviderOrder(explicit, s.ReviewModel)
	if m.opts.ReviewGenerate != nil {
		provider = ""
		if len(order) > 0 {
			provider = order[0]
		}
		if provider == "" {
			provider = explicit
		}
		if provider == "" {
			provider = "claude"
		}
		model = strings.TrimSpace(s.ReviewModel)
		if model == "" {
			model = textgen.CheapestModel(provider)
		}
		text, err = m.opts.ReviewGenerate(ctx, provider, model, s.ReviewEffort, prompt)
		return text, provider, model, nil, err
	}

	var usable []string
	for _, p := range order {
		if textgen.AvailableProvider(p) {
			usable = append(usable, p)
		}
	}
	if explicit != "" && len(usable) > 0 && !containsString(usable, explicit) {
		concerns = append(concerns, fmt.Sprintf("审查 provider %s 不可用，已回退到 %s", explicit, usable[0]))
	}
	if len(usable) == 0 {
		return "", "", "", concerns, fmt.Errorf("没有可用的审查 provider")
	}
	var lastErr error
	for _, p := range usable {
		candidate, effort := reviewModelFor(p, explicit, s.ReviewModel, s.ReviewEffort)
		if candidate == "" {
			continue
		}
		text, err = textgen.GenerateWithOptions(ctx, textgen.Options{
			Provider: p,
			Model:    candidate,
			Effort:   effort,
		}, prompt)
		if err == nil {
			return text, p, candidate, concerns, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用的审查模型")
	}
	return "", "", "", concerns, lastErr
}

// promoteL3 把 bee 升级到 L3 审查会话，结论沿用 pollReview 的原有逻辑。
func (m *Manager) promoteL3(ctx context.Context, leadThreadID, beeThreadID string, from ReviewStage, patchSHA, reason string) {
	now := m.now()
	if !m.funnelUpdate(leadThreadID, beeThreadID, from, patchSHA, func(b *Bee) {
		b.ReviewStage = ReviewStageL3
		b.ReviewLayer = "l3"
		b.ReviewStartedAt = &now
		b.ReviewThreadID = ""
		b.ReviewApplyPending = false
		if strings.TrimSpace(reason) != "" {
			b.ReviewReason = reason
		}
	}) {
		return
	}
	m.persistLead(leadThreadID)
	m.notifyChange(leadThreadID)
	m.startMergeReview(ctx, leadThreadID, beeThreadID)
}

// saveFunnelPatch 保存审查补丁；已有同内容补丁时直接复用，避免覆盖仍被审查引用的文件。
func (m *Manager) saveFunnelPatch(leadThreadID string, h Bee) (string, string, error) {
	sum := sha256.Sum256(h.MergePatch)
	sha := hex.EncodeToString(sum[:])
	if h.ReviewPatchPath != "" {
		if data, err := os.ReadFile(h.ReviewPatchPath); err == nil && string(data) == string(h.MergePatch) {
			return h.ReviewPatchPath, sha, nil
		}
	}
	path, err := m.saveReviewPatch(leadThreadID, h.Index, h.MergePatch)
	if err != nil {
		return "", "", err
	}
	return path, sha, nil
}

// reviewPatchReusable 判断已有补丁文件是否仍可被 startMergeReview 复用。
func reviewPatchReusable(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.ReadFile(path)
	return err == nil
}

// funnelUpdate 在锁内重新读取 bee，确认 ThreadID、审查阶段与补丁快照都没变后再写回。
func (m *Manager) funnelUpdate(leadThreadID, beeThreadID string, stage ReviewStage, patchSHA string, fn func(*Bee)) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return false
	}
	for i := range lead.bees {
		b := &lead.bees[i]
		if b.ThreadID != beeThreadID {
			continue
		}
		if b.ReviewState != ReviewRunning || b.ReviewStage != stage || b.ReviewPatchSHA != patchSHA {
			return false
		}
		fn(b)
		return true
	}
	return false
}

// reworkSource 按 ThreadID 或名字找回返工对应的旧 bee。
func (m *Manager) reworkSource(leadThreadID, reworkOf string) (Bee, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	lead, ok := m.leads[leadThreadID]
	if !ok {
		return Bee{}, false
	}
	for i := range lead.bees {
		b := &lead.bees[i]
		if b.ThreadID == reworkOf || b.Name == reworkOf {
			return b.clone(), true
		}
	}
	return Bee{}, false
}

// reviewDiff 调用注入的 diff 实现或真实 git DiffContext。
func (m *Manager) reviewDiff(ctx context.Context, worktree, base string, paths []string, maxBytes int) (string, bool, error) {
	if m.opts.ReviewDiff != nil {
		return m.opts.ReviewDiff(ctx, worktree, base, paths, maxBytes)
	}
	if m.opts.Git == nil {
		return "", false, fmt.Errorf("git runner 不可用")
	}
	return m.opts.Git.DiffContext(ctx, worktree, base, paths, maxBytes)
}

// reviewDiffBetween 调用注入的增量 diff 或真实 git DiffBetween。
func (m *Manager) reviewDiffBetween(ctx context.Context, oldRoot, newRoot string, paths []string, maxBytes int) (string, error) {
	if m.opts.ReviewDiffBetween != nil {
		return m.opts.ReviewDiffBetween(ctx, oldRoot, newRoot, paths, maxBytes)
	}
	if m.opts.Git == nil {
		return "", fmt.Errorf("git runner 不可用")
	}
	return m.opts.Git.DiffBetween(ctx, oldRoot, newRoot, paths, maxBytes)
}

// reviewChanges 把文件路径转成 L1 需要的改动分类。
func reviewChanges(paths []string) []reviewfunnel.Change {
	changes := make([]reviewfunnel.Change, 0, len(paths))
	for _, p := range paths {
		changes = append(changes, reviewfunnel.Change{Path: p, Class: reviewcheck.Classify(p)})
	}
	return changes
}

// allOfClass 判断全部改动都属于同一类，且改动非空。
func allOfClass(changes []reviewfunnel.Change, class reviewcheck.Class) bool {
	if len(changes) == 0 {
		return false
	}
	for _, c := range changes {
		if c.Class != class {
			return false
		}
	}
	return true
}

// allPlanChanges 判断改动文件是否全是计划文档。
func allPlanChanges(paths []string) bool {
	return allOfClass(reviewChanges(paths), reviewcheck.ClassPlan)
}

// l0Concerns 汇总 L0 检查本身产生的疑点。
func l0Concerns(h Bee, check reviewcheck.Result) []string {
	var concerns []string
	if check.Skipped {
		concerns = append(concerns, "L0 跳过的检查："+check.Display)
	}
	return concerns
}

// scopeConcerns 把范围比对的硬/软越界写成审查疑点，供 L2 与汇报使用。
func scopeConcerns(scope reviewcheck.Scope, changes []reviewfunnel.Change) []string {
	var concerns []string
	for _, p := range scope.Hard {
		concerns = append(concerns, "越界（硬）："+p)
	}
	for _, p := range scope.Soft {
		concerns = append(concerns, "越界（软）："+p)
	}
	return concerns
}

// patchLineCounts 统计补丁里的新增/删除行数，作为 Bee 统计缺失时的回退。
func patchLineCounts(patch []byte) (added, deleted int) {
	for _, line := range strings.Split(string(patch), "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			deleted++
		}
	}
	return added, deleted
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if strings.EqualFold(item, want) {
			return true
		}
	}
	return false
}
