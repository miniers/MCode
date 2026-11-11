package kanban

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"mcode/internal/git"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/store"
)

// ErrInvalidTransition 表示非法的状态迁移或无效输入。
var ErrInvalidTransition = errors.New("kanban: invalid transition")

// ErrInterruptUnsupported 表示当前会话驱动不支持中断执行轮。
var ErrInterruptUnsupported = errors.New("kanban: session driver does not support interrupt")

// ErrDependenciesNotDone 表示任务的前置依赖尚未全部完成，不能启动。
var ErrDependenciesNotDone = errors.New("kanban: prerequisites are not done")

// goalOwnerSteward 与新前台创建目标时写入的 Owner "steward" 一致。
const goalOwnerSteward = "steward"

// planningGoalStaleAfter 是 planning 状态目标的兜底超时：规划协程只活在进程内，
// 重启后不会续跑，超过该时长仍为 planning 的目标在启动恢复时置为 plan_failed。
const planningGoalStaleAfter = 30 * time.Minute

// SessionRequest 描述启动代理会话所需的请求参数。
type SessionRequest struct {
	LeadThreadID    string // Swarm 子会话从 Lead 读取模型偏好，其他调用方留空
	ProjectID       string
	WorkDir         string
	Title           string
	Prompt          string
	InteractionMode string // 非空时（"plan"/"build"）会话以该交互模式启动
	// RuntimeMode 非空时以该权限模式创建会话，取值同 ThreadCreateParams.RuntimeMode
	// （readonly/supervised/autoAcceptEdits/auto/fullAccess），空表示沿用现有默认行为。
	RuntimeMode string
	// 以下字段非空/为 true 时覆盖从 Lead 读取的 Bee 偏好（来自 Swarm 配对或 Profile）。
	Provider string // 非空时覆盖 provider，并令 Model 以本请求为准
	Model    string // 非空时覆盖 model；Provider 非空而 Model 为空表示使用该 provider 的默认模型
	Effort   string // 非空时覆盖 effort
	FastMode bool   // 为 true 时为新会话开启快速模式
}

// SessionState 描述代理会话的运行时执行状态。
type SessionState struct {
	Running          bool
	Failed           bool
	AwaitingApproval bool
	// AwaitingInput 表示会话正在等用户或主代理回答问题（与等审批区分开），
	// 期间会话没有进展属于正常等待，看门狗不应据此判卡死。
	AwaitingInput     bool
	FailureReason     string
	LastAssistantText string
	// Interrupted 表示最近一轮是被用户中断或取消结束的（不处于运行中时才可能为真）；
	// 供 Swarm 判断该 Lead 是否被用户停过，据此不自动续跑汇报。
	Interrupted bool
	// Progress 是不透明的进展指纹：会话有任何新输出（消息、流式文本、工具调用）时都会变化，
	// 供看门狗判断 bee 是否仍在推进；具体组成由各驱动自行决定，调用方只做相等比较。
	Progress string
	// LastFileChangeAt 是最近一次工具写文件或轮次开始的时刻；零值表示尚未观测到。
	LastFileChangeAt time.Time
	// HostToolsUnavailable 为真表示该会话的 provider 没有接入 mcode 的 MCP 工具，
	// 调不了 automation_report_result、kanban_submit_result 等回执工具；
	// 调度器据此不追问回执，直接按「会话空闲即完成」处理。
	HostToolsUnavailable bool
}

// SessionDriver 定义会话驱动接口。
type SessionDriver interface {
	StartSession(ctx context.Context, req SessionRequest) (threadID string, err error)
	SessionState(ctx context.Context, threadID string) (SessionState, error)
}

// ProjectResolver 定义项目根路径解析接口。
type ProjectResolver interface {
	ProjectRoot(ctx context.Context, projectID string) (string, error)
}

// WorktreeSetupRunner 为 SessionDriver 的可选扩展：任务 worktree 新建成功后执行项目的
// 「新建工作树时运行」脚本。未实现时跳过，不影响任务启动。
type WorktreeSetupRunner interface {
	RunWorktreeSetupScripts(ctx context.Context, projectID, projectRoot, worktreePath string)
}

// ReviewDecision 定义任务人工评审决策。
type ReviewDecision string

const (
	ReviewAccept  ReviewDecision = "accept"
	ReviewReject  ReviewDecision = "reject"
	ReviewRequest ReviewDecision = "requestReview"
)

// CreateTaskInput 描述创建新任务的入参。
type CreateTaskInput struct {
	ProjectID string
	Title     string
	Notes     *string
	ThreadID  *string
	Origin    string
	Provider  string
	Model     string
	// DependsOn 为前置任务 ID 列表，依赖未完成的任务不可启动。
	DependsOn []string
}

// TaskView 将存储任务与其派生看板视图聚合。
type TaskView struct {
	Task    store.Task `json:"task"`
	Derived Derived    `json:"derived"`
}

// Board 包含当前看板的单调版本号、任务视图列表及目标列表。
type Board struct {
	Revision int          `json:"revision"`
	Tasks    []TaskView   `json:"tasks"`
	Goals    []store.Goal `json:"goals"`
}

// Options 包含看板服务运行所需的可选与必选组件。
type Options struct {
	Store       *store.Store
	Git         *git.Runner
	Driver      SessionDriver
	StopSession func(ctx context.Context, threadID string) error
	Projects    ProjectResolver
	Planner     Planner
	WorktreeDir string
	// DataDir 为 mcode 数据根目录；非空时超长目标 Prompt 落盘到 <DataDir>/goals（D-KB-015），
	// 为空时该功能退化为全部存库。
	DataDir string
	// AutoMerge 返回验收通过后是否自动合并 worktree；nil 时视为默认开启。
	AutoMerge func() bool
	// AutoCleanupMergedWorktrees 返回合并成功后是否自动清理 worktree；nil 时视为默认开启。
	AutoCleanupMergedWorktrees func() bool
	// AutoReview 返回 agent 完成后是否自动转入 review；nil 时视为默认开启。
	AutoReview func() bool
	// AutoStartOnCreate 返回创建任务后是否立即排队启动；nil 视为默认关闭。
	AutoStartOnCreate func() bool
	// AutoRescueStuck 返回卡死任务自动救援是否开启；nil 时视为默认开启。
	AutoRescueStuck func() bool
	// RunInWorktree 返回看板任务是否在 worktree 中执行；nil 时视为开启。
	RunInWorktree func() bool
	// StuckNudgeTimeout 返回无活动提醒阈值；nil 视为 600s。
	StuckNudgeTimeout func() time.Duration
	// StuckInterruptTimeout 返回无活动强制中断超时；nil 视为 1800s。
	StuckInterruptTimeout func() time.Duration
	// IsReviewActive 返回审查会话是否正在运行；nil 时查询 ReviewThreadID 的会话状态。
	IsReviewActive func(taskID string) bool
	Now            func() time.Time
}

// optimisticDispatchTimeout 为乐观派发在内存中的最大保鲜期（30s），超时后自动恢复为持久化真实状态。
const optimisticDispatchTimeout = 30 * time.Second

// Service 提供看板管理核心服务。
type Service struct {
	opts         Options
	mu           sync.Mutex
	revision     atomic.Int64
	cbMu         sync.Mutex
	onChange     func()
	onResult     func(context.Context, store.Task, ExecutorResult)
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	writes       *WriteConcurrencyManager
	optimisticMu sync.Mutex
	optimistic   map[string]time.Time
	// receipts 保存执行会话的回执、追问与求助状态，由独立互斥锁保护
	receipts receiptState
}

// SetOnResult 设置执行结果落库后的回调。
func (s *Service) SetOnResult(fn func(context.Context, store.Task, ExecutorResult)) {
	s.cbMu.Lock()
	s.onResult = fn
	s.cbMu.Unlock()
}

func (s *Service) notifyResult(ctx context.Context, task store.Task, res ExecutorResult) {
	s.cbMu.Lock()
	fn := s.onResult
	s.cbMu.Unlock()
	if fn != nil {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("kanban: 结果回调异常: %v", r)
			}
		}()
		fn(ctx, task, res)
	}
}

// NewService 创建看板服务实例。如果 Planner 未提供，则默认使用 AgentPlanner。
func NewService(opts Options) *Service {
	if opts.Planner == nil {
		opts.Planner = NewAgentPlanner(opts.Driver, opts.Projects)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		opts:       opts,
		ctx:        ctx,
		cancel:     cancel,
		writes:     NewWriteConcurrencyManager(),
		optimistic: make(map[string]time.Time),
	}
	s.receipts.init()
	s.revision.Store(1)
	return s
}

// AcquireWrite 获取指定会话、任务及请求维度的看板写操作锁。
func (s *Service) AcquireWrite(sessionID, taskID, requestID string) (func(), error) {
	if s.writes == nil {
		return func() {}, nil
	}
	return s.writes.Acquire(sessionID, taskID, requestID)
}

// SetOnChange 设置状态变更监听回调，在每次成功变更后于锁外安全调用。
func (s *Service) SetOnChange(fn func()) {
	s.cbMu.Lock()
	defer s.cbMu.Unlock()
	s.onChange = fn
}

func (s *Service) notifyChange() {
	s.cbMu.Lock()
	fn := s.onChange
	s.cbMu.Unlock()
	if fn != nil {
		fn()
	}
}

// NotifyChanged 供绕过 Service 直接写库的调用方触发刷新。
func (s *Service) NotifyChanged() { s.notifyChange() }

// Close 取消内部上下文并等待所有后台规划任务平稳退出。
func (s *Service) Close() {
	s.cancel()
	s.wg.Wait()
}

// AutoReviewEnabled 返回任务完成后是否自动流转至 review 审查状态。
func (s *Service) AutoReviewEnabled() bool {
	if s.opts.AutoReview != nil {
		return s.opts.AutoReview()
	}
	return true
}

// AutoStartOnCreateEnabled 返回创建任务后是否立即排队启动。
func (s *Service) AutoStartOnCreateEnabled() bool {
	if s.opts.AutoStartOnCreate != nil {
		return s.opts.AutoStartOnCreate()
	}
	return false
}

// AutoRescueStuckEnabled 返回卡死任务自动救援是否开启。
func (s *Service) AutoRescueStuckEnabled() bool {
	if s.opts.AutoRescueStuck != nil {
		return s.opts.AutoRescueStuck()
	}
	return true
}

// GetStuckNudgeTimeout 返回无活动提醒阈值（默认 600 秒）。
func (s *Service) GetStuckNudgeTimeout() time.Duration {
	if s.opts.StuckNudgeTimeout != nil {
		return s.opts.StuckNudgeTimeout()
	}
	return 600 * time.Second
}

// GetStuckInterruptTimeout 返回无活动强制中断超时（默认 1800 秒）。
func (s *Service) GetStuckInterruptTimeout() time.Duration {
	if s.opts.StuckInterruptTimeout != nil {
		return s.opts.StuckInterruptTimeout()
	}
	return 1800 * time.Second
}

// Board 获取完整看板快照，Revision 初始为 1，每次变更单调递增。
func (s *Service) Board(ctx context.Context) (Board, error) {
	now := time.Now()
	if s.opts.Now != nil {
		now = s.opts.Now()
	}

	s.optimisticMu.Lock()
	currentRev := int(s.revision.Load())
	optimisticMap := make(map[string]bool, len(s.optimistic))
	for tid, markedAt := range s.optimistic {
		if now.Sub(markedAt) <= optimisticDispatchTimeout {
			optimisticMap[tid] = true
		}
	}
	s.optimisticMu.Unlock()

	tasks, err := s.opts.Store.ListTasks(ctx, "")
	if err != nil {
		return Board{}, err
	}
	goals, err := s.opts.Store.ListGoals(ctx, "")
	if err != nil {
		return Board{}, err
	}

	allTasks := make(map[string]store.Task, len(tasks))
	for _, t := range tasks {
		allTasks[t.ID] = t
	}

	taskViews := make([]TaskView, len(tasks))
	for i, t := range tasks {
		rt := RuntimeState{
			Now:          now,
			Heartbeat:    t.UpdatedAt,
			IsOptimistic: optimisticMap[t.ID],
		}
		if t.Status == string(TaskStatusInProgress) && t.ThreadID != nil && *t.ThreadID != "" && s.opts.Driver != nil {
			if st, err := s.opts.Driver.SessionState(ctx, *t.ThreadID); err == nil {
				rt.Running = st.Running
				rt.ThreadFailed = st.Failed
				rt.AwaitingApproval = st.AwaitingApproval
				rt.AwaitingInput = st.AwaitingInput
				// 会话仍在执行中不该被当作卡死：心跳取当前时间，
				// 否则长时间不写回任务行的健康回合会被 20/40 分钟阈值误判。
				if st.Running {
					rt.Heartbeat = now
				}
			}
		}
		taskViews[i] = TaskView{
			Task:    t,
			Derived: Derive(t, allTasks, rt),
		}
	}

	return Board{
		Revision: currentRev,
		Tasks:    taskViews,
		Goals:    goals,
	}, nil
}

// CreateTask 创建新任务，status 初始为 todo，priority 为 none。
func (s *Service) CreateTask(ctx context.Context, in CreateTaskInput) (store.Task, error) {
	if strings.TrimSpace(in.Title) == "" {
		return store.Task{}, ErrInvalidTransition
	}
	notes := ""
	if in.Notes != nil {
		notes = *in.Notes
	}
	var projectID *string
	if in.ProjectID != "" {
		projectID = &in.ProjectID
	}

	deps := in.DependsOn
	if deps == nil {
		deps = []string{}
	}

	t := store.Task{
		ID:        uuid.NewString(),
		ProjectID: projectID,
		ThreadID:  in.ThreadID,
		Title:     in.Title,
		Notes:     notes,
		Status:    string(TaskStatusTodo),
		Priority:  string(PriorityNone),
		DependsOn: deps,
		Meta:      EncodeTaskMeta(TaskMeta{Origin: in.Origin, Provider: in.Provider, Model: in.Model}),
	}

	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.opts.Store.CreateTask(ctx, t); err != nil {
		return store.Task{}, err
	}
	created, err := s.opts.Store.GetTask(ctx, t.ID)
	if err != nil {
		return t, nil
	}

	if s.AutoStartOnCreateEnabled() {
		depsDone := true
		for _, depID := range deps {
			depTask, err := s.opts.Store.GetTask(ctx, depID)
			if err != nil || depTask.Status != string(TaskStatusDone) {
				depsDone = false
				break
			}
		}
		if depsDone {
			if started, err := s.startTaskInternal(ctx, created.ID, created.Revision); err == nil {
				created = started
			}
		}
	}

	s.revision.Add(1)
	changed = true
	return created, nil
}

func (s *Service) createChildTask(ctx context.Context, parent store.Task, f Followup) (store.Task, error) {
	priority := Priority(f.Priority)
	if !priority.Valid() {
		priority = PriorityNone
	}
	t := store.Task{ID: uuid.NewString(), ProjectID: parent.ProjectID, GoalID: parent.GoalID, GoalPrompt: parent.GoalPrompt, Title: f.Title, Notes: f.Body, Status: string(TaskStatusTodo), Priority: string(priority), DependsOn: []string{}, Meta: EncodeTaskMeta(TaskMeta{Origin: "delegated", ParentID: parent.ID})}
	var err error
	if parent.GoalID != nil && *parent.GoalID != "" {
		goal, e := s.opts.Store.GetGoal(ctx, *parent.GoalID)
		if e != nil {
			return store.Task{}, e
		}
		err = s.opts.Store.CreateGoalTask(ctx, t, goal.Revision)
	} else {
		err = s.opts.Store.CreateTask(ctx, t)
	}
	if err != nil {
		return store.Task{}, err
	}
	return s.opts.Store.GetTask(ctx, t.ID)
}

// MoveTask 移动任务状态，校验状态合法性及 CAS 乐观版本号。
func (s *Service) MoveTask(ctx context.Context, taskID string, status TaskStatus, expectedRevision int) (store.Task, error) {
	if !status.Valid() {
		return store.Task{}, ErrInvalidTransition
	}

	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	if !CanTransition(TaskStatus(task.Status), status) {
		return store.Task{}, ErrInvalidTransition
	}

	if status == TaskStatusCancelled && task.Status == string(TaskStatusInProgress) && task.ThreadID != nil && *task.ThreadID != "" && s.opts.StopSession != nil {
		if err := s.opts.StopSession(ctx, *task.ThreadID); err != nil {
			return store.Task{}, err
		}
	}
	if (task.Status == string(TaskStatusNeedsApproval) && status != TaskStatusNeedsApproval) || status == TaskStatusDone || status == TaskStatusFailed || status == TaskStatusCancelled {
		if err := s.closeTaskDecisions(ctx, &task); err != nil {
			return store.Task{}, err
		}
	}
	task.Status = string(status)
	updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
	if err != nil {
		return store.Task{}, err
	}
	if updated.GoalID != nil && *updated.GoalID != "" {
		s.updateGoalProgressLocked(ctx, *updated.GoalID)
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// TaskExecuting 报告任务的执行线程是否仍在运行，供 move_card 结算前判断是否需要先中断。
func (s *Service) TaskExecuting(ctx context.Context, task store.Task) bool {
	if task.ThreadID == nil || *task.ThreadID == "" || s.opts.Driver == nil {
		return false
	}
	state, err := s.opts.Driver.SessionState(ctx, *task.ThreadID)
	return err == nil && state.Running
}

// InterruptTask 中断任务正在运行的一轮，复用驱动可选的 SessionInterrupter 路径（D-KB-030）。
func (s *Service) InterruptTask(ctx context.Context, task store.Task) error {
	if task.ThreadID == nil || *task.ThreadID == "" {
		return ErrInvalidTransition
	}
	interrupter, ok := s.opts.Driver.(SessionInterrupter)
	if !ok {
		return ErrInterruptUnsupported
	}
	return interrupter.InterruptSession(ctx, *task.ThreadID)
}

// StartTask 启动执行 todo 任务，自动准备工作区与分支并启动会话。
func (s *Service) StartTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	updated, err := s.startTaskInternal(ctx, taskID, expectedRevision)
	if err != nil {
		return store.Task{}, err
	}
	if updated.GoalID != nil && *updated.GoalID != "" {
		s.updateGoalProgressLocked(ctx, *updated.GoalID)
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// ensureDependenciesDone 校验任务的前置依赖是否全部已完成，未就绪的任务不允许启动，
// 就绪时返回已完成的依赖任务，供启动提示词注入依赖上下文。
func (s *Service) ensureDependenciesDone(ctx context.Context, task store.Task) ([]store.Task, error) {
	deps := make([]store.Task, 0, len(task.DependsOn))
	for _, depID := range task.DependsOn {
		dep, err := s.opts.Store.GetTask(ctx, depID)
		if err != nil {
			return nil, fmt.Errorf("%w: prerequisite %q is missing", ErrDependenciesNotDone, depID)
		}
		if dep.Status != string(TaskStatusDone) {
			return nil, fmt.Errorf("%w: prerequisite %q is %s", ErrDependenciesNotDone, depID, dep.Status)
		}
		deps = append(deps, dep)
	}
	return deps, nil
}

func (s *Service) startTaskInternal(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {
	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	if task.Status != string(TaskStatusTodo) {
		return store.Task{}, ErrInvalidTransition
	}
	deps, err := s.ensureDependenciesDone(ctx, task)
	if err != nil {
		return store.Task{}, err
	}

	now := time.Now()
	if s.opts.Now != nil {
		now = s.opts.Now()
	}
	s.optimisticMu.Lock()
	s.optimistic[taskID] = now
	s.optimisticMu.Unlock()
	defer func() {
		s.optimisticMu.Lock()
		delete(s.optimistic, taskID)
		s.optimisticMu.Unlock()
	}()

	projectID := ""
	if task.ProjectID != nil {
		projectID = *task.ProjectID
	}
	var root string
	if s.opts.Projects != nil {
		root, _ = s.opts.Projects.ProjectRoot(ctx, projectID)
	}

	workDir := root
	branchName := kanbanBranchName(taskID)
	inWorktree := false
	runInWorktree := s.opts.RunInWorktree == nil || s.opts.RunInWorktree()
	if runInWorktree && s.opts.Git != nil && s.opts.WorktreeDir != "" && root != "" {
		if repoRoot, err := s.opts.Git.RepoRoot(ctx, root); err == nil {
			wtPath := s.TaskWorktreePath(taskID)
			if _, err := os.Stat(wtPath); os.IsNotExist(err) {
				if err := s.opts.Git.WorktreeAdd(ctx, repoRoot, wtPath, branchName, "HEAD"); err != nil {
					return store.Task{}, fmt.Errorf("create worktree: %w", err)
				}
				s.runWorktreeSetup(ctx, projectID, root, wtPath)
			}
			workDir = wtPath
			inWorktree = true
		}
	}

	prompt := buildStartPrompt(task, deps, branchName, inWorktree, s.isStewardOwnerTask(ctx, task))

	if s.opts.Driver == nil {
		return store.Task{}, errors.New("kanban: session driver is nil")
	}
	threadID, err := s.opts.Driver.StartSession(ctx, SessionRequest{
		ProjectID: projectID,
		WorkDir:   workDir,
		Title:     task.Title,
		Prompt:    prompt,
	})
	if err != nil {
		return store.Task{}, fmt.Errorf("start session: %w", err)
	}

	task.ThreadID = &threadID
	task.Status = string(TaskStatusInProgress)
	task.IsDraft = false
	task.AttemptCount++
	task.LastFailureReason = nil

	updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
	if err != nil {
		return store.Task{}, err
	}
	// 新一轮执行开始：清掉上一轮可能残留的回执、追问计数与求助标记
	s.clearReceiptRound(task.ID)
	return updated, nil
}

// TaskWorktreePath 返回任务 worktree 的绝对路径；未配置 WorktreeDir 时返回空串。
func (s *Service) TaskWorktreePath(taskID string) string {
	if s == nil || strings.TrimSpace(s.opts.WorktreeDir) == "" {
		return ""
	}
	return filepath.Join(s.opts.WorktreeDir, taskID)
}

// runWorktreeSetup 异步执行项目里勾选了「新建工作树时运行」的脚本；失败只记日志，不影响任务启动。
func (s *Service) runWorktreeSetup(ctx context.Context, projectID, projectRoot, worktreePath string) {
	runner, ok := s.opts.Driver.(WorktreeSetupRunner)
	if !ok || runner == nil {
		return
	}
	go runner.RunWorktreeSetupScripts(context.WithoutCancel(ctx), projectID, projectRoot, worktreePath)
}

// buildStartPrompt 组装任务启动提示词：任务本身与目标、评审反馈，以及已完成依赖的上下文、
// 上次失败原因、隔离工作区说明和看板协作约束。
func buildStartPrompt(task store.Task, deps []store.Task, branchName string, inWorktree, stewardOwner bool) string {
	var b strings.Builder
	b.WriteString(task.Title)
	if task.Notes != "" {
		b.WriteString("\n\n")
		b.WriteString(task.Notes)
	}
	if task.GoalPrompt != nil && *task.GoalPrompt != "" {
		b.WriteString("\n\n## Goal\n")
		b.WriteString(*task.GoalPrompt)
	}
	if strings.TrimSpace(task.AcceptanceCriteria) != "" {
		b.WriteString("\n\n## Acceptance Criteria\n完成前必须自行逐条满足以下验收标准，不要只报告已完成：\n")
		b.WriteString(strings.TrimSpace(task.AcceptanceCriteria))
	}
	if task.ReviewStatus != nil && *task.ReviewStatus == "revisions_requested" &&
		task.ReviewFeedback != nil && strings.TrimSpace(*task.ReviewFeedback) != "" {
		b.WriteString("\n\n## Review Feedback To Address\n")
		b.WriteString(*task.ReviewFeedback)
	}
	if len(deps) > 0 {
		b.WriteString("\n\n## Dependencies Completed\n以下前置任务已完成，直接基于它们的产出继续，不要重复实现：")
		for _, dep := range deps {
			b.WriteString("\n\n### ")
			b.WriteString(dep.Title)
			b.WriteString("\n- 分支: `")
			b.WriteString(kanbanBranchName(dep.ID))
			b.WriteString("`")
			if summary := taskDependencySummary(dep); summary != "" {
				b.WriteString("\n- 摘要: ")
				b.WriteString(summary)
			}
		}
	}
	if task.LastFailureReason != nil && strings.TrimSpace(*task.LastFailureReason) != "" {
		b.WriteString("\n\n## Previous Attempt\n上次尝试没有成功，原因：\n")
		b.WriteString(clipPromptRunes(strings.TrimSpace(*task.LastFailureReason), 600))
		b.WriteString("\n\n工作区里可能还留着上次尝试的改动，动手前先检查现有改动，在其基础上继续或按需清理，不要重复实现。")
	}
	if inWorktree {
		b.WriteString("\n\n## Workspace Environment\n当前任务运行在为它单独准备的隔离 worktree 中（分支 `")
		b.WriteString(branchName)
		b.WriteString("`）。不要在其中再创建新的 git worktree，所有改动直接在此工作区完成并提交。")
	}
	b.WriteString("\n\n## Board Rules\n- 需要人工确认时让卡片停在 Needs You（待你处理）等待用户处理，不要自行把卡片移出该列。")
	if stewardOwner {
		b.WriteString("\n- 你是这项工作的负责人，自行决定需要哪些调查、实现和检查。完成后提交 done；确实需要用户选择时才提交 needs_decision。不要为普通完成另请验收。")
	} else {
		b.WriteString("\n- 不要绕过人工评审直接把卡片结算为 Done，验收结论由用户给出。")
	}
	b.WriteString("\n\n## 结束方式\n结束时必须调用 kanban_submit_result，status 选择 done、failed、waiting、delegated 或 needs_decision，并填写对应结果。只有交付物已经到达用户手上，并且没有剩下需要用户决定、授权或验收的事，才算 done；工具调用成功或命令跑通不等于完成，还要用户拍板的用 needs_decision。")
	if stewardOwner {
		b.WriteString("\nsummary 会原样发给交代这项工作的用户。用用户的语言写一到三句，直接回答问题或说明完成了什么、查到了什么，并附有用的文件、提交、PR 或其他产出链接。不要加任务标题，不要写“已汇总给用户”“供主代理参考”“任务已完成”这类元描述，不要让用户再打开任务才能看到答案。只陈述已验证的事实；尚未运行检查、代码仍在独立分支、尚未合并或部署时要如实说明。需要用户决定时用 needs_decision，question 是不超过三十字的一个问题，并给出几个明确选项。")
	}
	return b.String()
}

// taskDependencySummary 取依赖任务的完成摘要，优先评审摘要，其次最近活动文本。
func taskDependencySummary(dep store.Task) string {
	text := ""
	if dep.ReviewSummary != nil && strings.TrimSpace(*dep.ReviewSummary) != "" {
		text = *dep.ReviewSummary
	} else if dep.LastActivityText != nil && strings.TrimSpace(*dep.LastActivityText) != "" {
		text = *dep.LastActivityText
	}
	return clipPromptRunes(strings.TrimSpace(text), 800)
}

// kanbanBranchName 返回任务对应的看板分支名，与创建 worktree 时使用的分支保持一致。
func kanbanBranchName(taskID string) string {
	prefix := taskID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return "mcode/kanban-" + prefix
}

// clipPromptRunes 按字符截断提示词片段，超长时以省略号收尾。
func clipPromptRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

// ReviewTask 对 review 状态的任务进行评审决策。
func (s *Service) ReviewTask(ctx context.Context, taskID string, decision ReviewDecision, feedback string, expectedRevision int) (store.Task, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	if task.Status != string(TaskStatusReview) {
		return store.Task{}, ErrInvalidTransition
	}

	if feedback != "" {
		task.ReviewFeedback = &feedback
	}

	switch decision {
	case ReviewAccept:
		// 先合并再置 done：合并失败时任务留在 review，不能出现卡片显示完成但代码没进主工作区。
		if !s.autoMergeTaskLocked(ctx, &task) {
			st := "merge_failed"
			task.ReviewStatus = &st
			// UI 卡片显示 LastActivityText，不显示 ReviewFeedback，把同一段失败文案也写过去。
			if task.ReviewFeedback != nil {
				task.LastActivityText = task.ReviewFeedback
			}
			break
		}
		task.Status = string(TaskStatusDone)
		st := "passed"
		task.ReviewStatus = &st
	case ReviewReject:
		// 驳回附带反馈退回 todo，重启时把评审反馈带进执行提示词。
		task.Status = string(TaskStatusTodo)
		st := "revisions_requested"
		task.ReviewStatus = &st
		// 打回次数只增不减，accept 不清零，供总管判断是否升级为打断级通知。
		task.RejectCount++
	case ReviewRequest:
		if s.opts.Driver == nil {
			return store.Task{}, errors.New("kanban: session driver is nil")
		}
		projectID := ""
		if task.ProjectID != nil {
			projectID = *task.ProjectID
		}
		var root string
		if s.opts.Projects != nil {
			root, _ = s.opts.Projects.ProjectRoot(ctx, projectID)
		}
		workDir := root
		if s.opts.WorktreeDir != "" {
			wtPath := s.TaskWorktreePath(taskID)
			if fi, err := os.Stat(wtPath); err == nil && fi.IsDir() {
				workDir = wtPath
			}
		}
		diff, _, _ := s.taskDiffLocked(ctx, taskID)
		reviewPrompt := fmt.Sprintf("请评审以下任务变更并给出评审结论：\n\n任务标题: %s\n任务说明: %s\n\n代码变更 (diff):\n%s", task.Title, task.Notes, diff)
		if feedback != "" {
			reviewPrompt += fmt.Sprintf("\n\n评审反馈:\n%s", feedback)
		}
		threadID, err := s.opts.Driver.StartSession(ctx, SessionRequest{
			ProjectID: projectID,
			WorkDir:   workDir,
			Title:     "Review: " + task.Title,
			Prompt:    reviewPrompt,
		})
		if err != nil {
			return store.Task{}, fmt.Errorf("start review session: %w", err)
		}
		task.ReviewThreadID = &threadID
		st := "requested"
		task.ReviewStatus = &st
	default:
		return store.Task{}, ErrInvalidTransition
	}

	updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
	if err != nil {
		return store.Task{}, err
	}
	if updated.GoalID != nil && *updated.GoalID != "" {
		s.updateGoalProgressLocked(ctx, *updated.GoalID)
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// TaskDiff 返回指定任务关联 worktree 的 Git unified diff。超过 512 KiB 时截断。
func (s *Service) TaskDiff(ctx context.Context, taskID string) (diff string, truncated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.taskDiffLocked(ctx, taskID)
}

func (s *Service) taskDiffLocked(ctx context.Context, taskID string) (string, bool, error) {
	if s.opts.Git == nil || s.opts.WorktreeDir == "" {
		return "", false, nil
	}
	path := s.TaskWorktreePath(taskID)
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return "", false, nil
	}
	raw, err := s.opts.Git.RawDiff(ctx, path, "HEAD")
	if err != nil {
		return "", false, err
	}
	const limit = 512 * 1024
	if len(raw) > limit {
		return string(raw[:limit]), true, nil
	}
	return string(raw), false, nil
}

// autoMergeTaskLocked 在验收通过时将任务 worktree 的改动以 squash 方式合并回主项目根目录，并按需清理 worktree 与分支。
// 主工作区存在未提交改动或正在进行 merge/rebase 时拒绝合并；冲突或提交失败时回滚主工作区。
// 失败时保留 worktree 并把原因写入 ReviewFeedback，返回 false；未启用合并或无需合并时返回 true。
// 调用方必须已持有 s.mu。
func (s *Service) autoMergeTaskLocked(ctx context.Context, task *store.Task) bool {
	autoMergeEnabled := true
	if s.opts.AutoMerge != nil {
		autoMergeEnabled = s.opts.AutoMerge()
	}
	if !autoMergeEnabled || s.opts.Git == nil || s.opts.WorktreeDir == "" {
		return true
	}
	taskID := task.ID
	wtPath := s.TaskWorktreePath(taskID)
	fi, err := os.Stat(wtPath)
	if err != nil || !fi.IsDir() {
		return true
	}

	projectID := ""
	if task.ProjectID != nil {
		projectID = *task.ProjectID
	}
	var root string
	if s.opts.Projects != nil {
		root, _ = s.opts.Projects.ProjectRoot(ctx, projectID)
	}
	if root == "" {
		return true
	}

	repoRoot, err := s.opts.Git.RepoRoot(ctx, root)
	if err != nil {
		msg := fmt.Sprintf("自动合并失败：%s，worktree 已保留在 %s", err.Error(), wtPath)
		task.ReviewFeedback = &msg
		return false
	}

	prefix := taskID
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	branch := "mcode/kanban-" + prefix

	// 1. worktree 内先提交全部未提交改动，确保 squash 合并带上完整变更
	if err := s.opts.Git.AddAll(ctx, wtPath); err != nil {
		msg := fmt.Sprintf("自动合并失败：%s，worktree 已保留在 %s", err.Error(), wtPath)
		task.ReviewFeedback = &msg
		return false
	}
	if _, diffErr := s.gitCmd(ctx, wtPath, "diff", "--cached", "--quiet"); diffErr != nil {
		if !isGitExitCode(diffErr, 1) {
			msg := fmt.Sprintf("自动合并失败：%s，worktree 已保留在 %s", diffErr.Error(), wtPath)
			task.ReviewFeedback = &msg
			return false
		}
		title := strings.TrimSpace(task.Title)
		commitMsg := "feat: complete task"
		if title != "" {
			commitMsg = fmt.Sprintf("feat(%s): complete task", title)
		}
		if _, err := s.gitCmd(ctx, wtPath, "commit", "-m", commitMsg); err != nil {
			msg := fmt.Sprintf("自动合并失败：%s，worktree 已保留在 %s", err.Error(), wtPath)
			task.ReviewFeedback = &msg
			return false
		}
	}

	// 2. 主工作区 dirty 或正处于 merge/rebase 时不合并，避免覆盖用户改动
	entries, statusErr := s.opts.Git.Status(ctx, repoRoot)
	if statusErr != nil {
		msg := fmt.Sprintf("自动合并失败：无法检查主工作区状态（%s），worktree 已保留在 %s", statusErr.Error(), wtPath)
		task.ReviewFeedback = &msg
		return false
	}
	if len(entries) > 0 {
		msg := fmt.Sprintf("自动合并已取消：主工作区存在未提交改动，请先提交或复原后再合并，worktree 已保留在 %s", wtPath)
		task.ReviewFeedback = &msg
		return false
	}
	if s.gitOperationInProgress(ctx, repoRoot) {
		msg := fmt.Sprintf("自动合并已取消：主工作区正在进行 merge/rebase，请先处理后再合并，worktree 已保留在 %s", wtPath)
		task.ReviewFeedback = &msg
		return false
	}

	// 3. 主工作区执行 squash 合并；冲突时先记录冲突文件再回滚
	if _, err := s.gitCmd(ctx, repoRoot, "merge", "--squash", branch); err != nil {
		var conflictFiles []string
		if out, listErr := s.gitCmd(ctx, repoRoot, "diff", "--name-only", "--diff-filter=U"); listErr == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if line = strings.TrimSpace(line); line != "" {
					conflictFiles = append(conflictFiles, line)
				}
			}
		}
		_, _ = s.gitCmd(ctx, repoRoot, "merge", "--abort")
		_, _ = s.gitCmd(ctx, repoRoot, "reset", "--hard", "HEAD")
		reason := "存在冲突"
		if len(conflictFiles) > 0 {
			reason = "存在冲突（" + strings.Join(conflictFiles, ", ") + "）"
		}
		msg := fmt.Sprintf("自动合并失败：%s，已回滚主工作区，worktree 已保留在 %s", reason, wtPath)
		task.ReviewFeedback = &msg
		return false
	}

	// 4. squash 结果无暂存改动时跳过提交，否则按固定消息格式提交
	if _, diffErr := s.gitCmd(ctx, repoRoot, "diff", "--cached", "--quiet"); diffErr != nil {
		if !isGitExitCode(diffErr, 1) {
			_, _ = s.gitCmd(ctx, repoRoot, "reset", "--hard", "HEAD")
			msg := fmt.Sprintf("自动合并失败：%s，已回滚主工作区，worktree 已保留在 %s", diffErr.Error(), wtPath)
			task.ReviewFeedback = &msg
			return false
		}
		title := strings.TrimSpace(task.Title)
		if title == "" {
			title = "complete task"
		}
		commitMsg := fmt.Sprintf("feat(%s): %s\n\nSquash-merged from branch %s (Task: %s)", title, title, branch, taskID)
		if _, err := s.gitCmd(ctx, repoRoot, "commit", "-m", commitMsg); err != nil {
			_, _ = s.gitCmd(ctx, repoRoot, "reset", "--hard", "HEAD")
			msg := fmt.Sprintf("自动合并失败：%s，已回滚主工作区，worktree 已保留在 %s", err.Error(), wtPath)
			task.ReviewFeedback = &msg
			return false
		}
	}

	// 5. 成功后按开关清理任务 worktree 与分支
	shouldCleanup := true
	if s.opts.AutoCleanupMergedWorktrees != nil {
		shouldCleanup = s.opts.AutoCleanupMergedWorktrees()
	}
	if shouldCleanup {
		_ = s.opts.Git.WorktreeRemove(ctx, repoRoot, wtPath, true)
		_ = s.opts.Git.BranchDelete(ctx, repoRoot, branch, true)
		_ = os.RemoveAll(wtPath)
	}
	return true
}

// gitCmd 在 dir 下执行 git 命令并返回标准输出，命令失败时把 stderr 摘要包装进错误返回。
func (s *Service) gitCmd(ctx context.Context, dir string, args ...string) ([]byte, error) {
	bin := "git"
	if s.opts.Git != nil && s.opts.Git.Bin != "" {
		bin = s.opts.Git.Bin
	}
	cmd := procattr.HideWindow(exec.CommandContext(ctx, bin, args...))
	cmd.Dir = dir
	// 继承登录 Shell 环境变量，保证 git hooks 能用到用户 PATH 下的工具
	cmd.Env = append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText != "" {
			return stdout.Bytes(), fmt.Errorf("git %s: %w: %s", args[0], err, errText)
		}
		return stdout.Bytes(), fmt.Errorf("git %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

// isGitExitCode 判断错误是否为 git 以指定退出码失败，例如 `git diff --quiet` 的退出码 1。
func isGitExitCode(err error, code int) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == code
}

// gitOperationInProgress 检查仓库是否处于 merge/cherry-pick/rebase 中间状态。
func (s *Service) gitOperationInProgress(ctx context.Context, repoRoot string) bool {
	for _, marker := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "rebase-merge", "rebase-apply"} {
		out, err := s.gitCmd(ctx, repoRoot, "rev-parse", "--git-path", marker)
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(repoRoot, path)
		}
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}

// recoverStalePlanningGoals 把超时仍停在 planning 的目标置为 plan_failed 并写原因，
// 否则重启后它既不会恢复也不会超时，只能取消重建。
func (s *Service) recoverStalePlanningGoals(ctx context.Context) {
	goals, err := s.opts.Store.ListGoals(ctx, "")
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, g := range goals {
		if g.Status != string(GoalStatusPlanning) || now.Sub(g.UpdatedAt) < planningGoalStaleAfter {
			continue
		}

		s.mu.Lock()
		latest, errGet := s.opts.Store.GetGoal(ctx, g.ID)
		if errGet != nil || latest.Status != string(GoalStatusPlanning) {
			s.mu.Unlock()
			continue
		}
		latest.Status = string(GoalStatusPlanFailed)
		reason := "规划超时：规划未在进程内完成，请重新规划"
		latest.StatusReason = &reason
		_, errUp := s.opts.Store.UpdateGoal(ctx, latest, latest.Revision)
		s.mu.Unlock()
		if errUp != nil {
			continue
		}
		s.revision.Add(1)
		s.notifyChange()
	}
}

// RecoverOrphanTasks 恢复执行会话已丢失的目标任务：对 GoalID 非空、状态为 in_progress
// 且 ThreadID 为空或会话不存在的任务，重置为待办并清除 ThreadID。返回恢复的任务数量。
func (s *Service) RecoverOrphanTasks(ctx context.Context) (int, error) {
	s.recoverStalePlanningGoals(ctx)
	if s.opts.Driver == nil {
		return 0, nil
	}
	tasks, err := s.opts.Store.ListTasks(ctx, "")
	if err != nil {
		return 0, err
	}

	recovered := 0
	for _, t := range tasks {
		if t.GoalID == nil || *t.GoalID == "" || t.Status != string(TaskStatusInProgress) {
			continue
		}
		if t.ThreadID != nil && *t.ThreadID != "" {
			if _, err := s.opts.Driver.SessionState(ctx, *t.ThreadID); !errors.Is(err, store.ErrNotFound) {
				continue
			}
		}

		s.mu.Lock()
		t.Status = string(TaskStatusTodo)
		t.ThreadID = nil
		text := "会话已丢失，任务已重置为待办"
		t.LastActivityText = &text
		_, err := s.opts.Store.UpdateTask(ctx, t, t.Revision)
		s.mu.Unlock()
		if err != nil {
			continue
		}
		s.revision.Add(1)
		recovered++
	}
	if recovered > 0 {
		s.notifyChange()
	}
	return recovered, nil
}

// CreateGoalOptions 描述创建目标的可选参数。
type CreateGoalOptions struct {
	ProjectID string
	Prompt    string
	// PlanThreadID 非空时直接作为目标的规划会话。
	PlanThreadID string
	// Owner 为目标创建者标记，steward 表示由总管创建，空串表示普通目标。
	Owner string
	// Policy 中给出的键覆盖默认策略，未给出的键保留默认值。
	Policy map[string]any
}

// CreateGoal 创建新目标并触发后台任务规划。
func (s *Service) CreateGoal(ctx context.Context, projectID, prompt string) (store.Goal, error) {
	return s.CreateGoalWithOptions(ctx, CreateGoalOptions{ProjectID: projectID, Prompt: prompt})
}

// CreateGoalWithOptions 按给定选项创建新目标并触发后台任务规划。
func (s *Service) CreateGoalWithOptions(ctx context.Context, opts CreateGoalOptions) (store.Goal, error) {
	projectID := opts.ProjectID
	prompt := opts.Prompt
	trimmedPrompt := strings.TrimSpace(prompt)
	if trimmedPrompt == "" {
		return store.Goal{}, ErrInvalidTransition
	}
	title := trimmedPrompt
	if idx := strings.IndexAny(title, "\r\n"); idx != -1 {
		title = strings.TrimSpace(title[:idx])
	}
	titleRunes := []rune(title)
	if len(titleRunes) > 80 {
		title = string(titleRunes[:80])
	}

	h := sha256.Sum256([]byte(prompt))
	contentHash := hex.EncodeToString(h[:])

	// 超长 Prompt 内容寻址落盘，库内只保留摘要（D-KB-015）。
	storedPrompt := s.materializeGoalPrompt(prompt, contentHash)

	goalID := uuid.NewString()
	policy := map[string]any{
		"maxConcurrency": 1,
		"previewPlan":    true,
		"autoAccept":     false,
		"maxAttempts":    2,
	}
	for key, value := range opts.Policy {
		policy[key] = value
	}
	g := store.Goal{
		ID:          goalID,
		ProjectID:   projectID,
		Title:       title,
		Prompt:      storedPrompt,
		ContentHash: contentHash,
		Status:      string(GoalStatusPlanning),
		Owner:       opts.Owner,
		Policy:      policy,
		Progress:    GoalProgress(nil),
	}
	if planThreadID := strings.TrimSpace(opts.PlanThreadID); planThreadID != "" {
		g.PlanThreadID = &planThreadID
	}

	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.opts.Store.CreateGoal(ctx, g); err != nil {
		return store.Goal{}, err
	}
	createdGoal, err := s.opts.Store.GetGoal(ctx, goalID)
	if err != nil {
		createdGoal = g
	}
	s.revision.Add(1)
	changed = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runPlan(createdGoal, nil, "")
	}()

	return createdGoal, nil
}

// StartGoal 将目标从 plan_review 或 paused 转换为 running 状态。
func (s *Service) StartGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	goal, err := s.opts.Store.GetGoal(ctx, goalID)
	if err != nil {
		return store.Goal{}, err
	}
	if goal.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	if goal.Status != string(GoalStatusPlanReview) && goal.Status != string(GoalStatusPaused) {
		return store.Goal{}, ErrInvalidTransition
	}

	goal.Status = string(GoalStatusRunning)
	updated, err := s.opts.Store.UpdateGoal(ctx, goal, expectedRevision)
	if err != nil {
		return store.Goal{}, err
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// PauseGoal 将 running 状态的目标置为 paused。
func (s *Service) PauseGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	goal, err := s.opts.Store.GetGoal(ctx, goalID)
	if err != nil {
		return store.Goal{}, err
	}
	if goal.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	if goal.Status != string(GoalStatusRunning) {
		return store.Goal{}, ErrInvalidTransition
	}

	goal.Status = string(GoalStatusPaused)
	updated, err := s.opts.Store.UpdateGoal(ctx, goal, expectedRevision)
	if err != nil {
		return store.Goal{}, err
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// CancelGoal 取消目标，非 completed/cancelled 状态均可取消。
func (s *Service) CancelGoal(ctx context.Context, goalID string, expectedRevision int) (store.Goal, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	goal, err := s.opts.Store.GetGoal(ctx, goalID)
	if err != nil {
		return store.Goal{}, err
	}
	if goal.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	if goal.Status == string(GoalStatusCompleted) || goal.Status == string(GoalStatusCancelled) {
		return store.Goal{}, ErrInvalidTransition
	}

	goal.Status = string(GoalStatusCancelled)
	updated, err := s.opts.Store.UpdateGoal(ctx, goal, expectedRevision)
	if err != nil {
		return store.Goal{}, err
	}
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// ReplanGoal 对处于 plan_review/plan_failed/paused 的目标清理待办任务并重新规划。
func (s *Service) ReplanGoal(ctx context.Context, goalID, feedback string, expectedRevision int) (store.Goal, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	goal, err := s.opts.Store.GetGoal(ctx, goalID)
	if err != nil {
		return store.Goal{}, err
	}
	if goal.Revision != expectedRevision {
		return store.Goal{}, store.ErrRevisionConflict
	}
	if goal.Status != string(GoalStatusPlanReview) &&
		goal.Status != string(GoalStatusPlanFailed) &&
		goal.Status != string(GoalStatusPaused) {
		return store.Goal{}, ErrInvalidTransition
	}

	taskIDs, err := s.opts.Store.ListGoalTaskIDs(ctx, goalID)
	if err != nil {
		return store.Goal{}, err
	}

	var retainedTaskIDs []string
	for _, tid := range taskIDs {
		t, err := s.opts.Store.GetTask(ctx, tid)
		if err != nil {
			continue
		}
		if t.Status == string(TaskStatusTodo) {
			_ = s.opts.Store.DeleteTask(ctx, tid)
		} else {
			retainedTaskIDs = append(retainedTaskIDs, tid)
		}
	}

	goal.Status = string(GoalStatusPlanning)
	updated, err := s.opts.Store.UpdateGoal(ctx, goal, expectedRevision)
	if err != nil {
		return store.Goal{}, err
	}
	s.revision.Add(1)
	changed = true

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runPlan(updated, retainedTaskIDs, feedback)
	}()

	return updated, nil
}

// RetryGoalTask 重试目标下失败或待办的任务，重置尝试次数并恢复为 todo 状态。
func (s *Service) RetryGoalTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	if task.GoalID == nil || *task.GoalID == "" {
		return store.Task{}, ErrInvalidTransition
	}
	if task.Status != string(TaskStatusRejected) && task.Status != string(TaskStatusTodo) {
		return store.Task{}, ErrInvalidTransition
	}

	task.Status = string(TaskStatusTodo)
	task.AttemptCount = 0
	task.LastFailureReason = nil
	task.ReviewStatus = nil

	updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
	if err != nil {
		return store.Task{}, err
	}
	s.updateGoalProgressLocked(ctx, *task.GoalID)
	s.revision.Add(1)
	changed = true
	return updated, nil
}

// SkipGoalTask 跳过目标任务：不计入失败，标记为 done 让依赖任务继续推进。
func (s *Service) SkipGoalTask(ctx context.Context, taskID string, expectedRevision int) (store.Task, error) {
	changed := false
	defer func() {
		if changed {
			s.notifyChange()
		}
	}()

	s.mu.Lock()
	defer s.mu.Unlock()

	task, err := s.opts.Store.GetTask(ctx, taskID)
	if err != nil {
		return store.Task{}, err
	}
	if task.Revision != expectedRevision {
		return store.Task{}, store.ErrRevisionConflict
	}
	if task.GoalID == nil || *task.GoalID == "" {
		return store.Task{}, ErrInvalidTransition
	}
	if task.Status == string(TaskStatusDone) || task.Status == string(TaskStatusInProgress) {
		return store.Task{}, ErrInvalidTransition
	}

	task.Status = string(TaskStatusDone)
	skipped := "skipped"
	task.ReviewStatus = &skipped
	summary := "The user skipped this task, so it was never carried out."
	if task.ReviewSummary == nil || strings.TrimSpace(*task.ReviewSummary) == "" {
		task.ReviewSummary = &summary
	}
	skippedText := "Skipped."
	task.LastActivityText = &skippedText

	updated, err := s.opts.Store.UpdateTask(ctx, task, expectedRevision)
	if err != nil {
		return store.Task{}, err
	}
	s.updateGoalProgressLocked(ctx, *task.GoalID)
	s.revision.Add(1)
	changed = true
	return updated, nil
}

func (s *Service) updateGoalProgressLocked(ctx context.Context, goalID string) {
	taskIDs, err := s.opts.Store.ListGoalTaskIDs(ctx, goalID)
	if err != nil {
		return
	}
	tasks := make([]store.Task, 0, len(taskIDs))
	for _, tid := range taskIDs {
		if t, err := s.opts.Store.GetTask(ctx, tid); err == nil {
			tasks = append(tasks, t)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		goal, err := s.opts.Store.GetGoal(ctx, goalID)
		if err != nil {
			return
		}
		goal.Progress = s.goalProgress(ctx, tasks)
		_, err = s.opts.Store.UpdateGoal(ctx, goal, goal.Revision)
		if err == nil || !errors.Is(err, store.ErrRevisionConflict) {
			return
		}
	}
}

func (s *Service) runPlan(goal store.Goal, retainedTaskIDs []string, feedback string) {
	if s.opts.Planner == nil {
		return
	}

	// 库内 Prompt 可能是落盘摘要，规划前回读全文（D-KB-015）；读不出来就按规划失败处理，
	// 不能拿摘要继续规划，否则目标会在需求残缺的情况下被标成 plan_review。
	expanded, err := s.expandGoalPrompt(goal)
	if s.ctx.Err() != nil {
		return
	}
	if err != nil {
		s.failGoalPlan(goal.ID, err)
		return
	}
	goal = expanded

	res, err := s.opts.Planner.Plan(s.ctx, goal, feedback)
	if s.ctx.Err() != nil {
		return
	}

	if err != nil {
		s.failGoalPlan(goal.ID, err)
		return
	}

	n := len(res.Tasks)
	newTaskIDs := make([]string, n)
	for i := 0; i < n; i++ {
		newTaskIDs[i] = uuid.NewString()
	}
	newTasks := make([]store.Task, n)
	for i, pt := range res.Tasks {
		deps := make([]string, 0, len(pt.DependsOn))
		for _, depIdx := range pt.DependsOn {
			if depIdx >= 0 && depIdx < n {
				deps = append(deps, newTaskIDs[depIdx])
			}
		}
		newTasks[i] = store.Task{
			ID:                 newTaskIDs[i],
			ProjectID:          &goal.ProjectID,
			GoalID:             &goal.ID,
			Title:              pt.Title,
			Notes:              pt.Notes,
			AcceptanceCriteria: formatAcceptanceCriteria(pt.AcceptanceCriteria),
			DependsOn:          deps,
			Status:             string(TaskStatusTodo),
			Priority:           string(PriorityNone),
		}
	}

	// 本轮新建的任务只有在规划真正落地（goal_tasks 关联 + 目标状态更新）后才保留：
	// 取消、版本冲突或写库失败时删掉，否则它们没有 goal_tasks 关联，会成为能被手工启动的幽灵卡片。
	createdTaskIDs := make([]string, 0, len(newTasks))
	committed := false
	defer func() {
		if committed {
			return
		}
		for _, id := range createdTaskIDs {
			_ = s.opts.Store.DeleteTask(context.Background(), id)
		}
	}()
	for _, t := range newTasks {
		if err := s.opts.Store.CreateTask(context.Background(), t); err != nil {
			continue
		}
		createdTaskIDs = append(createdTaskIDs, t.ID)
	}

	allTaskIDs := append(retainedTaskIDs, newTaskIDs...)

	for attempt := 0; attempt < 2; attempt++ {
		s.mu.Lock()
		latest, errGet := s.opts.Store.GetGoal(context.Background(), goal.ID)
		if errGet != nil {
			s.mu.Unlock()
			return
		}
		if latest.Status == string(GoalStatusCancelled) {
			s.mu.Unlock()
			return
		}
		errSet := s.opts.Store.SetGoalTasks(context.Background(), goal.ID, allTaskIDs, latest.Revision)
		if errSet != nil {
			s.mu.Unlock()
			if errors.Is(errSet, store.ErrRevisionConflict) {
				continue
			}
			return
		}

		latest, errGet = s.opts.Store.GetGoal(context.Background(), goal.ID)
		if errGet != nil {
			s.mu.Unlock()
			return
		}

		previewPlan := true
		if v, ok := latest.Policy["previewPlan"]; ok {
			if b, ok := v.(bool); ok {
				previewPlan = b
			}
		}

		if previewPlan {
			latest.Status = string(GoalStatusPlanReview)
		} else {
			latest.Status = string(GoalStatusRunning)
		}
		latest.PlanSummary = &res.Summary
		// 已有规划会话时保留原会话，不回写成规划返回的会话。
		if latest.PlanThreadID == nil || strings.TrimSpace(*latest.PlanThreadID) == "" {
			latest.PlanThreadID = &res.ThreadID
		}

		var allTasks []store.Task
		for _, tid := range allTaskIDs {
			if t, err := s.opts.Store.GetTask(context.Background(), tid); err == nil {
				allTasks = append(allTasks, t)
			}
		}
		latest.Progress = s.goalProgress(context.Background(), allTasks)

		_, errUp := s.opts.Store.UpdateGoal(context.Background(), latest, latest.Revision)
		if errUp == nil {
			committed = true
			s.revision.Add(1)
			s.mu.Unlock()
			s.notifyChange()
			return
		}
		s.mu.Unlock()
		if !errors.Is(errUp, store.ErrRevisionConflict) {
			return
		}
	}
}

// failGoalPlan 把目标置为 plan_failed 并记录原因；已被取消的目标不再改写。
func (s *Service) failGoalPlan(goalID string, cause error) {
	reason := cause.Error()
	for attempt := 0; attempt < 2; attempt++ {
		s.mu.Lock()
		latest, errGet := s.opts.Store.GetGoal(context.Background(), goalID)
		if errGet != nil {
			s.mu.Unlock()
			return
		}
		if latest.Status == string(GoalStatusCancelled) {
			s.mu.Unlock()
			return
		}
		latest.Status = string(GoalStatusPlanFailed)
		latest.StatusReason = &reason
		_, errUp := s.opts.Store.UpdateGoal(context.Background(), latest, latest.Revision)
		if errUp == nil {
			s.revision.Add(1)
			s.mu.Unlock()
			s.notifyChange()
			return
		}
		s.mu.Unlock()
		if !errors.Is(errUp, store.ErrRevisionConflict) {
			return
		}
	}
}

func isDone(t store.Task) bool {
	return t.Status == string(TaskStatusDone)
}

func (s *Service) isReviewActive(ctx context.Context, task store.Task) bool {
	if s.opts.IsReviewActive != nil {
		return s.opts.IsReviewActive(task.ID)
	}
	if task.ReviewThreadID == nil || *task.ReviewThreadID == "" || s.opts.Driver == nil {
		return false
	}
	state, err := s.opts.Driver.SessionState(ctx, *task.ReviewThreadID)
	return err == nil && state.Running
}

func (s *Service) goalProgress(ctx context.Context, tasks []store.Task) map[string]any {
	active := make(map[string]bool)
	for _, task := range tasks {
		if task.Status == string(TaskStatusReview) {
			active[task.ID] = s.isReviewActive(ctx, task)
		}
	}
	return goalProgress(tasks, func(id string) bool { return active[id] })
}

// GoalProgress 计算并汇总目标任务集的实时看板进度。
func GoalProgress(tasks []store.Task) map[string]any {
	return goalProgress(tasks, nil)
}

func goalProgress(tasks []store.Task, isReviewActive func(string) bool) map[string]any {
	total := len(tasks)
	done := 0
	running := 0
	waitingOnYou := 0
	failed := 0
	blocked := 0
	ready := 0

	byID := make(map[string]store.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}

	for _, t := range tasks {
		switch t.Status {
		case string(TaskStatusDone):
			done++
		case string(TaskStatusInProgress):
			running++
		case string(TaskStatusReview):
			if isReviewActive != nil && isReviewActive(t.ID) {
				running++
			} else {
				waitingOnYou++
			}
		case string(TaskStatusNeedsApproval):
			waitingOnYou++
		case string(TaskStatusRejected):
			failed++
		case string(TaskStatusTodo):
			allDepsDone := true
			for _, depID := range t.DependsOn {
				depTask, ok := byID[depID]
				if !ok || !isDone(depTask) {
					allDepsDone = false
					break
				}
			}
			if allDepsDone {
				ready++
			} else {
				blocked++
			}
		}
	}

	var fraction float64
	if total > 0 {
		fraction = float64(done) / float64(total)
	}
	needsAttention := failed > 0 || waitingOnYou > 0
	stalled := running == 0 && ready == 0 && done < total

	return map[string]any{
		"total":          total,
		"done":           done,
		"running":        running,
		"waitingOnYou":   waitingOnYou,
		"failed":         failed,
		"blocked":        blocked,
		"ready":          ready,
		"fraction":       fraction,
		"needsAttention": needsAttention,
		"stalled":        stalled,
	}
}
