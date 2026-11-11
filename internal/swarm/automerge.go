package swarm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mcode/internal/git"
)

// MergeStage 表示编排自动合并流水线的 7 个阶段（C-HY-15）。
type MergeStage int

const (
	// StageGathering 收集团队改动的文件。
	StageGathering MergeStage = iota
	// StageCommitting 写入提交（含冲突标记校验）。
	StageCommitting
	// StageDescribing 撰写提交说明。
	StageDescribing
	// StagePushing 推送分支。
	StagePushing
	// StageOpening 创建合并请求。
	StageOpening
	// StageMerging 合入。
	StageMerging
	// StageSyncing 同步本地检出。
	StageSyncing
)

// Label 返回阶段的中文名称，用于失败通知中标明阶段。
func (s MergeStage) Label() string {
	switch s {
	case StageGathering:
		return "收集子代理改动的文件"
	case StageCommitting:
		return "写入提交"
	case StageDescribing:
		return "撰写提交说明"
	case StagePushing:
		return "推送分支"
	case StageOpening:
		return "创建合并请求"
	case StageMerging:
		return "合入"
	case StageSyncing:
		return "同步本地检出"
	default:
		return "未知阶段"
	}
}

// BatchResult 描述一次批次自动合并的结果，供 Manager 持久化到批次内各 bee 上。
type BatchResult struct {
	State string // Bee.AutoMergeState 的取值
	SHA   string // 本地提交的 sha；空表示没有可提交的改动
	Stage string // 远端阶段失败或待重试时所处的 MergeStage 字符串形式
	Err   string // 失败原因
}

// maxMergeRetries 是远端阶段的最大重试次数（C-HY-16）。
const maxMergeRetries = 3

// retrySweepInterval 是扫尾任务检查待重试任务的周期。
const retrySweepInterval = 5 * time.Minute

// PipelineOptions 包含自动合并流水线的依赖与可选回调。
type PipelineOptions struct {
	Git      *git.Runner
	Forge    *git.ForgeClient // nil -> git.NewForgeClient(nil)
	Settings func() Settings
	Notify   func(leadThreadID, msg string)
	// Describe 为可选的提交说明生成器，入参为限定路径的 git diff 文本。
	Describe func(ctx context.Context, diff string) (string, error)
	// Push 为可选的推送实现，nil -> Git.PushRef。
	Push func(ctx context.Context, root, remote, localRef, remoteBranch string) error
	// Cleanup 为可选回调，用于清理批次内 bees 的 worktree。
	Cleanup func(ctx context.Context, b LandedBatch)
	// AfterFunc 为可选定时器实现，nil -> time.AfterFunc 包装。
	AfterFunc func(d time.Duration, f func()) func() bool
	// Remote 为远端名称，默认 "origin"。
	Remote string
	// OnResult 为可选回调，批次进入终态（done/skipped/failed）或转入待重试（pending）时上报一次。
	OnResult func(b LandedBatch, r BatchResult)
}

// preparedBatch 保存一次本地阶段的结果，供远端阶段与重试恢复复用。
type preparedBatch struct {
	sha        string // 本次创建的提交；空表示没有可提交的改动
	branch     string
	subject    string // 提交说明首行，同时作为 PR/MR 标题
	message    string // 完整提交说明，同时作为 PR/MR 正文
	forge      git.Forge
	prURL      string
	stagedKept []string
	commits    []string // 本批次已创建的提交，含逐 bee 落地提交
}

// retryJob 表示一个等待延时重试的远端阶段任务（C-HY-16）。
type retryJob struct {
	batch   LandedBatch
	manual  bool
	prep    *preparedBatch
	stage   MergeStage // 失败时所在阶段，恢复时从该阶段继续
	attempt int        // 本次重试序号（从 1 开始）
	due     time.Time
	cancel  func() bool
}

// Pipeline 是编排自动合并/自动推送流水线（C-HY-15/16）。
// AutoMerge 默认开启，仅控制本地提交；AutoPush 默认关闭，是唯一允许自动
// push 与创建/合入 PR/MR 的开关；手动触发（Trigger）不受开关限制。
type Pipeline struct {
	git       *git.Runner
	forge     *git.ForgeClient
	settings  func() Settings
	notify    func(leadThreadID, msg string)
	describe  func(ctx context.Context, diff string) (string, error)
	push      func(ctx context.Context, root, remote, localRef, remoteBranch string) error
	cleanup   func(ctx context.Context, b LandedBatch)
	afterFunc func(d time.Duration, f func()) func() bool
	remote    string
	onResult  func(b LandedBatch, r BatchResult)

	mu      sync.Mutex
	jobs    map[*retryJob]struct{}
	stopped bool
	stopCh  chan struct{}
	wg      sync.WaitGroup

	// checkoutMu 保护 checkoutLocks；checkoutLocks 按目标检出路径分配互斥锁，
	// 使同一 checkout 上的批次串行执行 git add/commit/push。
	checkoutMu    sync.Mutex
	checkoutLocks map[string]*sync.Mutex
}

// NewPipeline 创建流水线并填充默认依赖。
func NewPipeline(opts PipelineOptions) *Pipeline {
	p := &Pipeline{
		git:       opts.Git,
		forge:     opts.Forge,
		settings:  opts.Settings,
		notify:    opts.Notify,
		describe:  opts.Describe,
		push:      opts.Push,
		cleanup:   opts.Cleanup,
		afterFunc: opts.AfterFunc,
		remote:    opts.Remote,
		onResult:  opts.OnResult,
		jobs:      map[*retryJob]struct{}{},

		checkoutLocks: map[string]*sync.Mutex{},
	}
	if p.git == nil {
		p.git = git.NewRunner("", 4)
	}
	if p.forge == nil {
		p.forge = git.NewForgeClient(nil)
	}
	if p.settings == nil {
		p.settings = func() Settings { return Settings{} }
	}
	if p.notify == nil {
		p.notify = func(string, string) {}
	}
	if p.push == nil {
		p.push = p.git.PushRef
	}
	if p.afterFunc == nil {
		p.afterFunc = func(d time.Duration, f func()) func() bool {
			return time.AfterFunc(d, f).Stop
		}
	}
	if p.remote == "" {
		p.remote = "origin"
	}
	return p
}

// RunBatch 同步执行一次批次落地。manual 为 false 时严格遵守两个开关：
// AutoMerge 关闭则完全不动；AutoPush 关闭则只做本地提交，绝不 push 或调用 forge。
func (p *Pipeline) RunBatch(ctx context.Context, b LandedBatch, manual bool) error {
	// 过滤掉 ShadowRepo 或 SharedNoGit 为 true 的 bee（非 git 项目不能提交或推送）
	var validBees []Bee
	for _, bee := range b.Bees {
		if bee.ShadowRepo || bee.SharedNoGit {
			log.Printf("swarm: 跳过非 git 项目的 bee: %s (thread: %s)", bee.Name, bee.ThreadID)
			continue
		}
		validBees = append(validBees, bee)
	}
	if len(validBees) == 0 {
		p.runCleanup(ctx, b)
		p.report(b, BatchResult{State: AutoMergeDone})
		return nil
	}
	b.Bees = validBees

	if !manual && !p.currentSettings().AutoMerge {
		p.report(b, BatchResult{State: AutoMergeSkipped})
		return nil
	}
	// 本地提交与随后的远端阶段必须在同一把 checkout 锁内完成，
	// 否则其他批次的提交会混进本批次的推送里。
	unlock := p.lockCheckout(b.TargetWorkDir)
	defer unlock()
	prep, err := p.runLocal(ctx, b)
	if err != nil {
		p.report(b, BatchResult{State: AutoMergeFailed, Err: err.Error()})
		return err
	}
	if !manual && !p.currentSettings().AutoPush {
		if prep.sha != "" {
			extra := ""
			if len(prep.stagedKept) > 0 {
				extra = fmt.Sprintf("；这些路径你另有已暂存内容，未同步暂存区：%s", strings.Join(prep.stagedKept, "、"))
			}
			commits := prep.commits
			if len(commits) == 0 {
				commits = []string{prep.sha}
			}
			shorts := make([]string, len(commits))
			for i, sha := range commits {
				shorts[i] = shortSHA(sha)
			}
			p.notifyf(b.LeadThreadID, NoticePrefix+"已在本地提交 %d 个提交（%s）%s；自动推送未开启，可在 Git 菜单手动推送", len(commits), strings.Join(shorts, "、"), extra)
		} else {
			p.notifyf(b.LeadThreadID, NoticePrefix+"合并：没有需要提交的改动")
		}
		p.runCleanup(ctx, b)
		p.report(b, BatchResult{State: AutoMergeDone, SHA: prep.sha})
		return nil
	}
	if prep.sha == "" {
		if !manual {
			p.notifyf(b.LeadThreadID, NoticePrefix+"合并：没有需要提交的改动")
			p.report(b, BatchResult{State: AutoMergeDone})
			return nil
		}
		// 手动触发且没有新提交时，仍然推送当前 HEAD
		headSHA, err := p.git.RevParse(ctx, b.TargetWorkDir, "HEAD")
		if err != nil {
			wrapped := fmt.Errorf("swarm: 读取当前 HEAD 失败: %w", err)
			p.report(b, BatchResult{State: AutoMergeFailed, Err: wrapped.Error()})
			return wrapped
		}
		prep.sha = headSHA
	}
	// 远端阶段在上报结果前完成（done/failed/pending 均在 runRemote 内部上报）。
	return p.runRemote(ctx, b, manual, prep, StagePushing, 0)
}

// OnBatchLanded 是异步入口：后台执行 RunBatch(..., false) 并记录错误。
func (p *Pipeline) OnBatchLanded(ctx context.Context, b LandedBatch) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		if err := p.RunBatch(ctx, b, false); err != nil {
			log.Printf("swarm: 自动合并批次失败: %v", err)
		}
	}()
}

// ResumeBatch 是重启后的续做入口：sha 为空时批次在本地提交前就中断了，按普通批次重做；
// sha 不为空时跳过本地阶段，从 stage（零值时从推送阶段）继续远端阶段，不会重复提交。
func (p *Pipeline) ResumeBatch(ctx context.Context, b LandedBatch, sha string, stage MergeStage) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()

		p.mu.Lock()
		stopped := p.stopped
		p.mu.Unlock()
		if stopped {
			return
		}
		if !p.currentSettings().AutoMerge {
			p.report(b, BatchResult{State: AutoMergeSkipped})
			return
		}
		if sha == "" {
			// 批次在本地提交前就已中断，交给 RunBatch 从头处理（含开关处理与上报）。
			if err := p.RunBatch(ctx, b, false); err != nil {
				log.Printf("swarm: 续做合并批次失败: %v", err)
			}
			return
		}

		unlock := p.lockCheckout(b.TargetWorkDir)
		defer unlock()

		if !p.currentSettings().AutoPush {
			p.report(b, BatchResult{State: AutoMergeDone, SHA: sha})
			return
		}
		if stage < StagePushing {
			stage = StagePushing
		}
		prep := &preparedBatch{
			sha:    sha,
			branch: pipelineBranchName(b.LeadThreadID, sha),
		}
		// git.Runner 没有读取提交说明的方法，续做时用短 sha 生成一个简单标题。
		prep.subject = "chore: 合并子代理改动 " + shortSHA(sha)
		prep.message = prep.subject
		if err := p.runRemote(ctx, b, false, prep, stage, 1); err != nil {
			log.Printf("swarm: 续做远端合入失败: %v", err)
		}
	}()
}

// Trigger 手动触发：对每个批次执行 RunBatch(..., true)，返回第一个错误。
// 手动触发不受 AutoMerge/AutoPush 开关限制。
func (p *Pipeline) Trigger(ctx context.Context, batches []LandedBatch) error {
	for _, b := range batches {
		if err := p.RunBatch(ctx, b, true); err != nil {
			return err
		}
	}
	return nil
}

// Start 启动 5 分钟周期的扫尾任务，恢复尚未结案的待重试任务。
func (p *Pipeline) Start() {
	p.mu.Lock()
	if p.stopCh != nil {
		p.mu.Unlock()
		return
	}
	stopCh := make(chan struct{})
	p.stopCh = stopCh
	p.stopped = false
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(retrySweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case now := <-ticker.C:
				p.sweep(now)
			}
		}
	}()
}

// Stop 停止扫尾任务、取消全部重试定时器，并等待后台批次结束。
func (p *Pipeline) Stop() {
	p.mu.Lock()
	p.stopped = true
	if p.stopCh != nil {
		close(p.stopCh)
		p.stopCh = nil
	}
	for job := range p.jobs {
		if job.cancel != nil {
			job.cancel()
		}
	}
	p.jobs = map[*retryJob]struct{}{}
	p.mu.Unlock()
	p.wg.Wait()
}

// sweep 唤醒所有已到期但仍未被定时器消费的任务。
func (p *Pipeline) sweep(now time.Time) {
	p.mu.Lock()
	var due []*retryJob
	for job := range p.jobs {
		if !job.due.After(now) {
			due = append(due, job)
		}
	}
	p.mu.Unlock()
	for _, job := range due {
		if job.cancel != nil {
			job.cancel()
		}
		p.fire(job)
	}
}

// fire 执行一个重试任务；先重新检查 AutoPush，再恢复到失败阶段继续。
func (p *Pipeline) fire(job *retryJob) {
	p.mu.Lock()
	if _, ok := p.jobs[job]; !ok {
		p.mu.Unlock()
		return
	}
	if p.stopped {
		p.mu.Unlock()
		return
	}
	delete(p.jobs, job)
	p.mu.Unlock()

	if !job.manual && !p.currentSettings().AutoPush {
		p.notifyf(job.batch.LeadThreadID, NoticePrefix+"自动推送已关闭，已取消待重试的远端合入")
		p.report(job.batch, BatchResult{State: AutoMergeSkipped, SHA: job.prep.sha})
		return
	}
	// 重试同样按目标检出串行化，避免与其他批次的本地提交/推送交错
	unlock := p.lockCheckout(job.batch.TargetWorkDir)
	err := p.runRemote(context.Background(), job.batch, job.manual, job.prep, job.stage, job.attempt)
	unlock()
	if err != nil {
		log.Printf("swarm: 合并重试失败: %v", err)
	}
}

// runLocal 执行前三个本地阶段（收集 / 提交校验 / 撰写说明与提交）。
func (p *Pipeline) runLocal(ctx context.Context, b LandedBatch) (*preparedBatch, error) {
	root := b.TargetWorkDir
	prep := &preparedBatch{}

	// 双重防御：过滤掉 ShadowRepo 或 SharedNoGit 为 true 的 bee
	var validBees []Bee
	for _, bee := range b.Bees {
		if bee.ShadowRepo || bee.SharedNoGit {
			log.Printf("swarm: 跳过非 git 项目的 bee: %s (thread: %s)", bee.Name, bee.ThreadID)
			continue
		}
		validBees = append(validBees, bee)
	}
	if len(validBees) == 0 {
		return prep, nil
	}
	prep.subject, prep.message = defaultCommitMessage(validBees)
	var uncommitted []Bee
	var patches [][]byte
	for _, bee := range validBees {
		if bee.LandedCommit != "" {
			prep.commits = append(prep.commits, bee.LandedCommit)
		} else {
			if strings.TrimSpace(bee.LandedPatchPath) == "" {
				log.Printf("swarm: 跳过没有补丁存档的 bee: %s (thread: %s)", bee.Name, bee.ThreadID)
				continue
			}
			data, err := os.ReadFile(bee.LandedPatchPath)
			if err != nil {
				log.Printf("swarm: 跳过无法读取补丁存档的 bee: %s (thread: %s): %v", bee.Name, bee.ThreadID, err)
				continue
			}
			uncommitted = append(uncommitted, bee)
			patches = append(patches, data)
		}
	}
	if len(uncommitted) == 0 {
		if len(prep.commits) == 0 {
			return prep, nil
		}
		unlock := lockHead(root)
		defer unlock()
		var err error
		prep.sha, err = p.git.RevParse(ctx, root, "HEAD")
		return prep, err
	}
	b.Bees = uncommitted

	paths, err := p.gatherPaths(ctx, root, b.Bees)
	if err != nil {
		p.notifyf(b.LeadThreadID, NoticePrefix+"合并已停止（%s）：%v；改动仍保留在工作区 %s，需要手动处理", StageGathering.Label(), err, root)
		return nil, err
	}

	if len(paths) > 0 {
		conflicts, err := git.ScanConflictMarkers(root, paths)
		if err != nil {
			wrapped := fmt.Errorf("swarm: 扫描冲突标记失败: %w", err)
			p.notifyf(b.LeadThreadID, NoticePrefix+"合并已停止（%s）：%v；改动仍保留在工作区 %s，需要手动处理", StageCommitting.Label(), wrapped, root)
			return nil, wrapped
		}
		if len(conflicts) > 0 {
			p.notifyf(b.LeadThreadID, NoticePrefix+"合并已停止：以下文件存在冲突标记：%s", strings.Join(conflicts, "、"))
			return nil, fmt.Errorf("swarm: 改动文件存在冲突标记：%s", strings.Join(conflicts, "、"))
		}
	}

	prep, fallback, err := p.commitWithPatches(ctx, root, b, patches, prep)
	if err != nil {
		return nil, err
	}
	if !fallback {
		return prep, nil
	}
	return p.commitPathsFallback(ctx, root, b, paths, prep)
}

// commitWithPatches 按补丁列表仅向 HEAD 提交，不写工作区。
// 若返回 fallback 为 true，表示应回退到兼容路径（如 git.ErrNoHead）。
func (p *Pipeline) commitWithPatches(ctx context.Context, root string, b LandedBatch, patches [][]byte, prep *preparedBatch) (*preparedBatch, bool, error) {
	// 新路径下用补丁拼成的文本作为 diff 输入生成提交说明，不混入用户的未提交改动
	var diffBuilder strings.Builder
	for _, patch := range patches {
		diffBuilder.Write(patch)
		if len(patch) > 0 && patch[len(patch)-1] != '\n' {
			diffBuilder.WriteByte('\n')
		}
	}
	diffText := diffBuilder.String()
	if p.describe != nil && strings.TrimSpace(diffText) != "" {
		if msg, derr := p.describe(ctx, diffText); derr == nil && strings.TrimSpace(msg) != "" {
			prep.message = strings.TrimSpace(msg)
			prep.subject = firstLine(prep.message)
		}
	}

	unlock := lockHead(root)
	res, err := p.git.CommitPatchesOnHead(ctx, root, patches, prep.message)
	unlock()
	if err != nil {
		if errors.Is(err, git.ErrNoHead) {
			// 仓库尚无 HEAD 提交，退回兼容路径
			return prep, true, nil
		}
		wrapped := fmt.Errorf("swarm: 创建提交失败: %w", err)
		p.notifyf(b.LeadThreadID, NoticePrefix+"合并已停止（%s）：%v；改动仍保留在工作区 %s，需要手动处理", StageCommitting.Label(), wrapped, root)
		return nil, false, wrapped
	}

	if len(res.Conflicts) > 0 {
		err := fmt.Errorf("swarm: 补丁应用存在冲突：%s", strings.Join(res.Conflicts, "、"))
		p.notifyf(b.LeadThreadID, NoticePrefix+"合并已停止（%s）：%v；需要手动处理", StageCommitting.Label(), err)
		return nil, false, err
	}

	if res.SHA == "" {
		if len(prep.commits) > 0 {
			prep.sha, err = p.git.RevParse(ctx, root, "HEAD")
		}
		return prep, false, err
	}

	prep.sha = res.SHA
	prep.commits = append(prep.commits, res.SHA)
	prep.stagedKept = res.StagedKept
	if len(res.StagedKept) > 0 {
		log.Printf("swarm: 这些路径你另有已暂存内容，未同步暂存区：%s", strings.Join(res.StagedKept, "、"))
	}
	return prep, false, nil
}

// commitPathsFallback 仅供无 HEAD 的仓库按工作区路径提交。
func (p *Pipeline) commitPathsFallback(ctx context.Context, root string, b LandedBatch, paths []string, prep *preparedBatch) (*preparedBatch, error) {
	if len(paths) > 0 {
		if p.describe != nil {
			if diff, err := p.git.DiffPaths(ctx, root, paths); err == nil && strings.TrimSpace(diff) != "" {
				if msg, derr := p.describe(ctx, diff); derr == nil && strings.TrimSpace(msg) != "" {
					prep.message = strings.TrimSpace(msg)
					prep.subject = firstLine(prep.message)
				}
			}
		}
		unlock := lockHead(root)
		sha, err := p.git.CommitPaths(ctx, root, paths, prep.message)
		unlock()
		if err != nil {
			wrapped := fmt.Errorf("swarm: 创建提交失败: %w", err)
			p.notifyf(b.LeadThreadID, NoticePrefix+"合并已停止（%s）：%v；改动仍保留在工作区 %s，需要手动处理", StageCommitting.Label(), wrapped, root)
			return nil, wrapped
		}
		prep.sha = sha
		if sha != "" {
			prep.commits = append(prep.commits, sha)
		}
	}
	if prep.sha == "" && len(prep.commits) > 0 {
		prep.sha, _ = p.git.RevParse(ctx, root, "HEAD")
	}
	return prep, nil
}

// runRemote 执行远端阶段，start 指定从哪个阶段开始（重试时从失败阶段恢复），
// attempt 为当前已重试次数（首次执行时为 0）。
func (p *Pipeline) runRemote(ctx context.Context, b LandedBatch, manual bool, prep *preparedBatch, start MergeStage, attempt int) error {
	root := b.TargetWorkDir
	if prep.branch == "" {
		prep.branch = pipelineBranchName(b.LeadThreadID, prep.sha)
	}

	if start <= StagePushing {
		if prep.sha == "" {
			err := fmt.Errorf("swarm: 推送失败: 批次缺少提交 sha")
			p.report(b, BatchResult{State: AutoMergeFailed, Stage: stageString(StagePushing), Err: err.Error()})
			return err
		}
		// 推送本批次创建的提交而不是当前 HEAD：重试时 HEAD 可能已被其他批次推进，
		// 推 HEAD 会把别人的提交一并推上去。PushRef 会把该提交拼成 sha:refs/heads/branch。
		if err := p.push(ctx, root, p.remote, prep.sha, prep.branch); err != nil {
			return p.remoteFailure(b, manual, prep, StagePushing, attempt, err)
		}
	}

	if start <= StageOpening {
		remoteURL, err := p.git.RemoteURL(ctx, root)
		if err != nil {
			wrapped := fmt.Errorf("swarm: 读取远端地址失败: %w", err)
			p.report(b, BatchResult{State: AutoMergeFailed, SHA: prep.sha, Stage: stageString(StageOpening), Err: wrapped.Error()})
			return wrapped
		}
		forge := git.DetectForge(remoteURL)
		if forge == git.ForgeUnknown {
			p.notifyf(b.LeadThreadID, NoticePrefix+"%s；分支 %s 已推送，可手动处理", git.ErrForgeUnsupported.Error(), prep.branch)
			p.report(b, BatchResult{State: AutoMergeDone, SHA: prep.sha})
			return nil
		}
		prep.forge = forge
		base, err := p.git.DefaultRemoteBranch(ctx, root, p.remote)
		if err != nil {
			wrapped := fmt.Errorf("swarm: 探测默认分支失败: %w", err)
			p.report(b, BatchResult{State: AutoMergeFailed, SHA: prep.sha, Stage: stageString(StageOpening), Err: wrapped.Error()})
			return wrapped
		}
		prURL, err := p.forge.CreatePullRequest(ctx, root, forge, git.PullRequestSpec{
			Title: prep.subject,
			Body:  prep.message,
			Head:  prep.branch,
			Base:  base,
		})
		if err != nil {
			return p.remoteFailure(b, manual, prep, StageOpening, attempt, err)
		}
		prep.prURL = prURL
	}

	if start <= StageMerging {
		if err := p.forge.MergePullRequest(ctx, root, prep.forge, prep.prURL); err != nil {
			return p.remoteFailure(b, manual, prep, StageMerging, attempt, err)
		}
		p.notifyf(b.LeadThreadID, NoticePrefix+"已合入合并请求：%s", prep.prURL)
	}

	// 同步本地检出：失败或无法安全对齐只提示，不影响批次本身成功。
	base, err := p.git.DefaultRemoteBranch(ctx, root, p.remote)
	if err != nil {
		p.notifyf(b.LeadThreadID, NoticePrefix+"远端合入完成，但解析默认分支失败：%v", err)
		p.report(b, BatchResult{State: AutoMergeDone, SHA: prep.sha})
		return nil
	}
	unlock := lockHead(root)
	synced, err := p.git.SyncDefaultBranch(ctx, root, p.remote, base)
	unlock()
	switch {
	case err != nil:
		p.notifyf(b.LeadThreadID, NoticePrefix+"远端合入完成，但本地检出未同步（可稍后手动 git pull）：%v", err)
	case !synced:
		p.notifyf(b.LeadThreadID, NoticePrefix+"远端合入完成；本地检出保持原样，需要时可手动 git pull")
	default:
		p.notifyf(b.LeadThreadID, NoticePrefix+"远端合入完成并已同步本地检出：%s", prep.prURL)
	}
	p.report(b, BatchResult{State: AutoMergeDone, SHA: prep.sha})
	return nil
}

// lockCheckout 锁定目标检出路径并返回解锁函数：同一路径的批次串行执行，
// 不同路径之间互不阻塞。
func (p *Pipeline) lockCheckout(path string) func() {
	key := filepath.Clean(path)
	p.checkoutMu.Lock()
	mu, ok := p.checkoutLocks[key]
	if !ok {
		mu = &sync.Mutex{}
		p.checkoutLocks[key] = mu
	}
	p.checkoutMu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// remoteFailure 处理远端阶段失败：可重试时安排指数退避重试（60s * 2^attempt，最多 3 次），
// 否则通知用户并返回原始错误。无论哪种结局，都在这里上报一次流水线结果。
func (p *Pipeline) remoteFailure(b LandedBatch, manual bool, prep *preparedBatch, stage MergeStage, attempt int, cause error) error {
	next := attempt + 1
	if !git.IsRetryableForgeError(cause) || next > maxMergeRetries {
		p.notifyf(b.LeadThreadID, NoticePrefix+"合并失败（%s）：%v", stage.Label(), cause)
		p.report(b, BatchResult{State: AutoMergeFailed, SHA: prep.sha, Stage: stageString(stage), Err: cause.Error()})
		return cause
	}
	delay := retryDelay(next)
	if !p.scheduleRetry(b, manual, prep, stage, next, delay) {
		// 流水线已停止（应用正在退出），重试无法安排；保留 pending 状态，下次启动时续做。
		return cause
	}
	p.notifyf(b.LeadThreadID, NoticePrefix+"%s暂时失败，将在 %s 后自动重试（第 %d/%d 次）：%v",
		stage.Label(), delay.Round(time.Second), next, maxMergeRetries, cause)
	p.report(b, BatchResult{State: AutoMergePending, SHA: prep.sha, Stage: stageString(stage), Err: cause.Error()})
	return cause
}

// scheduleRetry 登记重试任务并设置定时器；流水线已停止时返回 false。
func (p *Pipeline) scheduleRetry(b LandedBatch, manual bool, prep *preparedBatch, stage MergeStage, attempt int, delay time.Duration) bool {
	job := &retryJob{
		batch:   b,
		manual:  manual,
		prep:    prep,
		stage:   stage,
		attempt: attempt,
		due:     time.Now().Add(delay),
	}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return false
	}
	p.jobs[job] = struct{}{}
	afterFunc := p.afterFunc
	p.mu.Unlock()

	// 先登记任务再创建定时器：若定时器提前触发，fire 已能找到该任务
	cancel := afterFunc(delay, func() { p.fire(job) })
	p.mu.Lock()
	job.cancel = cancel
	p.mu.Unlock()
	return true
}

// retryDelay 返回第 attempt 次重试的等待时长：60s * 2^(attempt-1)。
func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 60 * time.Second
	}
	return time.Duration(1<<uint(attempt-1)) * 60 * time.Second
}

// gatherPaths 汇总批次内全部 bees 的改动文件，排除构建产物，并只保留当前仍有变更的路径。
func (p *Pipeline) gatherPaths(ctx context.Context, root string, bees []Bee) ([]string, error) {
	seen := map[string]bool{}
	var union []string
	for _, h := range bees {
		for _, path := range h.ChangedFiles {
			path = strings.TrimSpace(path)
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			union = append(union, path)
		}
	}
	union = git.FilterBuildOutputs(union)

	entries, err := p.git.Status(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("swarm: 读取工作区状态失败: %w", err)
	}
	changed := map[string]bool{}
	for _, e := range entries {
		changed[e.Path] = true
	}
	paths := []string{}
	for _, path := range union {
		if changed[path] {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// commitTypeOrder 定义提交类型按优先级从高到低的顺序，多个子代理合并时取最靠前的类型。
var commitTypeOrder = []string{"feat", "fix", "perf", "refactor", "docs", "test", "style", "chore"}

// commitTypeRank 返回类型的优先级序号，未知类型排在最后。
func commitTypeRank(typ string) int {
	for i, t := range commitTypeOrder {
		if t == typ {
			return i
		}
	}
	return len(commitTypeOrder)
}

// higherCommitType 返回两个类型中优先级更高的一个。
func higherCommitType(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" || commitTypeRank(a) <= commitTypeRank(b) {
		return a
	}
	return b
}

// splitCommitPrefix 识别任务文本开头的 Conventional Commits 前缀（如 fix: 、feat(ui): ），
// 返回其中的类型与去掉前缀后的任务文本。
func splitCommitPrefix(task string) (typ, rest string, ok bool) {
	s := strings.TrimSpace(task)
	i := strings.IndexAny(s, ":：")
	if i < 0 {
		return "", "", false
	}
	head := s[:i]
	name, scope, hasScope := strings.Cut(head, "(")
	if hasScope && !strings.HasSuffix(scope, ")") {
		return "", "", false
	}
	name = strings.ToLower(name)
	for _, t := range commitTypeOrder {
		if t == name {
			return t, strings.TrimSpace(strings.TrimLeft(s[i:], ":：")), true
		}
	}
	return "", "", false
}

// commitTypeFor 按任务文本与改动文件推断单个子代理的提交类型：
// 任务已带类型前缀时直接沿用；文件全为测试或文档时归为 test/docs；
// 否则按任务关键词判断，最后回退到 feat/chore。
func commitTypeFor(task string, files []string) string {
	task = strings.TrimSpace(task)
	if typ, _, ok := splitCommitPrefix(task); ok {
		return typ
	}
	allTest, allDocs := len(files) > 0, len(files) > 0
	for _, f := range files {
		path := filepath.ToSlash(strings.TrimSpace(f))
		if !strings.HasSuffix(path, "_test.go") && !strings.HasPrefix(path, "testdata/") && !strings.Contains(path, "/testdata/") {
			allTest = false
		}
		if !strings.HasSuffix(strings.ToLower(path), ".md") && !strings.HasPrefix(path, "docs/") && !strings.Contains(path, "/docs/") {
			allDocs = false
		}
	}
	if allTest {
		return "test"
	}
	if allDocs {
		return "docs"
	}

	lower := strings.ToLower(task)
	keywords := []struct {
		typ   string
		words []string
	}{
		{"fix", []string{"修复", "修正", "fix", "bug", "崩溃", "报错"}},
		{"style", []string{"gofmt", "格式化"}},
		{"refactor", []string{"重构", "拆分", "重命名", "refactor"}},
		{"perf", []string{"性能", "perf"}},
		{"docs", []string{"文档", "readme", "docs"}},
		{"test", []string{"测试", "test"}},
		{"feat", []string{"新增", "添加", "支持", "实现", "feat"}},
	}
	for _, group := range keywords {
		for _, word := range group.words {
			if strings.Contains(lower, word) {
				return group.typ
			}
		}
	}

	for _, f := range files {
		path := filepath.ToSlash(strings.TrimSpace(f))
		isTest := strings.HasSuffix(path, "_test.go") || strings.HasPrefix(path, "testdata/") || strings.Contains(path, "/testdata/")
		isDocs := strings.HasSuffix(strings.ToLower(path), ".md") || strings.HasPrefix(path, "docs/") || strings.Contains(path, "/docs/")
		if !isTest && !isDocs {
			return "feat"
		}
	}
	if len(files) == 0 {
		return "feat"
	}
	return "chore"
}

// defaultCommitMessage 生成兜底提交说明：首行带按改动性质推断的类型前缀（如 feat: / fix:），
// 约 72 字符，正文逐条列出子代理名称与任务。
func defaultCommitMessage(bees []Bee) (subject, message string) {
	tasks := make([]string, 0, len(bees))
	commitType := ""
	for _, h := range bees {
		task := singleLineText(h.Task)
		if task == "" {
			task = singleLineText(h.Name)
		}
		commitType = higherCommitType(commitType, commitTypeFor(task, h.ChangedFiles))
		if _, rest, ok := splitCommitPrefix(task); ok {
			task = rest
		}
		if task != "" {
			tasks = append(tasks, task)
		}
	}
	if len(tasks) == 0 {
		subject = "chore: 合并子代理改动"
	} else {
		if commitType == "" {
			commitType = "feat"
		}
		subject = truncateRunes(commitType+": "+strings.Join(tasks, "; "), 72)
	}

	var body strings.Builder
	body.WriteString(subject)
	for _, h := range bees {
		name := strings.TrimSpace(h.Name)
		if name == "" {
			name = "bee"
		}
		body.WriteString("\n\n- ")
		body.WriteString(name)
		body.WriteString(": ")
		body.WriteString(singleLineText(h.Task))
	}
	return subject, body.String()
}

// pipelineBranchName 生成推送分支名：mcode/swarm-<lead 前 8 位>-<sha 前 7 位>。
func pipelineBranchName(leadThreadID, sha string) string {
	lead := sanitizeRefPart(leadThreadID)
	if len(lead) > 8 {
		lead = lead[:8]
	}
	if lead == "" {
		lead = "lead"
	}
	if len(sha) > 7 {
		sha = sha[:7]
	}
	return "mcode/swarm-" + lead + "-" + sha
}

// sanitizeRefPart 过滤不能出现在 Git 引用中的字符，保证分支名合法。
func sanitizeRefPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// singleLineText 将多行文本压缩为单行。
func singleLineText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes 按字符截断文本，超长时以省略号收尾。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

// firstLine 取文本首行。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}

// shortSHA 返回短哈希（前 7 位）。
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// stageString 返回阶段的可持久化字符串形式。
func stageString(s MergeStage) string {
	return strconv.Itoa(int(s))
}

// MergeStageFromString 把持久化的阶段字符串还原为 MergeStage；
// 无法解析或超出范围时返回零值，由调用方按「从推送阶段开始」处理。
func MergeStageFromString(s string) MergeStage {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < int(StageGathering) || n > int(StageSyncing) {
		return StageGathering
	}
	return MergeStage(n)
}

// report 把批次结果交给可选回调；未配置回调时什么也不做。
func (p *Pipeline) report(b LandedBatch, r BatchResult) {
	if p.onResult == nil {
		return
	}
	p.onResult(b, r)
}

// currentSettings 返回当前配置快照。
func (p *Pipeline) currentSettings() Settings {
	if p.settings == nil {
		return Settings{}
	}
	return p.settings()
}

// runCleanup 在配置了清理回调时清理批次 worktree。
func (p *Pipeline) runCleanup(ctx context.Context, b LandedBatch) {
	if p.cleanup != nil {
		p.cleanup(ctx, b)
	}
}

// notifyf 发送带格式的通知。
func (p *Pipeline) notifyf(leadThreadID, format string, args ...any) {
	if p.notify == nil {
		return
	}
	p.notify(leadThreadID, fmt.Sprintf(format, args...))
}

// BatchesFromState 从主会话状态中收集待落地批次：
// 已合并且无错误（Merged && MergeError == ""）且没有冲突文件的 bees 按 TargetWorkDir 分组，
// 目标为空时使用 leadWorkDir；批次 ProjectID 优先取 bee 自身的 ProjectID。
// 带冲突文件的 bee 不入批：它的改动已经带着冲突标记落在目标工作区，入批会让
// runLocal 的冲突标记扫描命中并中止整批，连干净成员的成果也提交不了。
func BatchesFromState(st State, leadWorkDir, projectID string) []LandedBatch {
	var order []string
	groups := map[string][]Bee{}
	for _, h := range st.Bees {
		if !h.Merged || h.MergeError != "" || len(h.ConflictFiles) > 0 {
			continue
		}
		dir := h.TargetWorkDir
		if dir == "" {
			dir = leadWorkDir
		}
		if _, ok := groups[dir]; !ok {
			order = append(order, dir)
		}
		groups[dir] = append(groups[dir], h)
	}

	batches := make([]LandedBatch, 0, len(order))
	for _, dir := range order {
		bees := groups[dir]
		pid := projectID
		for _, h := range bees {
			if h.ProjectID != "" {
				pid = h.ProjectID
				break
			}
		}
		batches = append(batches, LandedBatch{
			LeadThreadID:  st.LeadThreadID,
			ProjectID:     pid,
			TargetWorkDir: dir,
			Bees:          bees,
		})
	}
	return batches
}
