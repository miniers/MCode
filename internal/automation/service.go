package automation

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"mcode/internal/harness"
	"mcode/internal/kanban"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
	"mcode/internal/store"
)

// Options 配置自动化服务所需的外部依赖。
type Options struct {
	Store        *store.Store
	Driver       kanban.SessionDriver
	Projects     kanban.ProjectResolver
	Evaluator    *Evaluator
	Now          func() time.Time
	Interval     time.Duration
	OnRunSettled func(Definition, store.AutomationRun)
	// DataDir 用于定位 <dataDir>/automation/scheduler.lock；为空时服务保持 passive，不触发任务。
	DataDir string
	// AcquireLock 仅用于测试注入；nil 时使用 filelock.TryAcquire。
	AcquireLock LockAcquirer
	// Logf 仅用于测试注入日志计数；nil 时使用 log.Printf。
	Logf func(format string, args ...any)
}

// awaitingInputTimeout 是一次运行等待用户回答的最长时长：
// 智能体反问用户后会话会一直停在「等待输入」，超过该时长仍未回答则按失败结算，
// 否则运行永久挂着、失败上限保护也永远不会触发。
const awaitingInputTimeout = 30 * time.Minute

// MaxActiveAutomationsPerThread 是单个会话可保留的启用中定时任务上限；
// 只对 Agent 通过工具创建的任务生效，UI 手动创建不受限。
const MaxActiveAutomationsPerThread = 50

// maxReceiptAsks 是运行结束时 Agent 仍未提交回执的最多追问次数，追问满即判失败。
const maxReceiptAsks = 2

// receiptAskGraceMin 是两次回执追问之间的最小宽限，调度间隔更小时按它兜底。
const receiptAskGraceMin = 30 * time.Second

// receiptAsk 记录某个运行已追问回执的次数与最后一次追问时间。
type receiptAsk struct {
	Count  int
	LastAt time.Time
}

// Service 提供自动化任务的管理、触发、调度与结果结算服务。
type Service struct {
	opts       Options
	interval   time.Duration
	stopCh     chan struct{}
	wg         sync.WaitGroup
	running    bool
	stateMu    sync.Mutex
	tickMu     sync.Mutex
	reconciled bool // 是否已对残留 running 运行对过账；由 tickMu 保证只执行一次
	inFlightMu sync.Mutex
	inFlight   map[string]string // automationID -> runID
	// awaitingSince 记录每个运行首次被观测到「等待输入」的时间，
	// 用于按等待时长而非运行时长计算超时。
	awaitingMu    sync.Mutex
	awaitingSince map[string]time.Time
	// trackingMu 保护回执追问与投递基线，二者都按 runID 记录。
	trackingMu  sync.Mutex
	receiptAsks map[string]receiptAsk
	runBaseline map[string]string
	changeMu    sync.Mutex
	onChange    func()

	// 跨进程调度锁：lockMu 串行化锁的检查、赋值与关闭，lockStatus 是只读快照。
	lockMu         sync.Mutex
	lock           LockHandle
	lockPath       string
	lockStatus     SchedulerLockStatus
	acquireLock    LockAcquirer
	logf           func(string, ...any)
	lastLockLogAt  time.Time
	lastLockLogErr string
}

// SetOnChange 注册配置或运行记录变化时的刷新通知。
func (s *Service) SetOnChange(fn func()) {
	s.changeMu.Lock()
	s.onChange = fn
	s.changeMu.Unlock()
}

func (s *Service) notifyChange() {
	s.changeMu.Lock()
	fn := s.onChange
	s.changeMu.Unlock()
	if fn != nil {
		fn()
	}
}

// New 实例化自动化服务。
func New(opts Options) *Service {
	interval := opts.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	s := &Service{
		opts:          opts,
		interval:      interval,
		inFlight:      make(map[string]string),
		awaitingSince: make(map[string]time.Time),
		receiptAsks:   make(map[string]receiptAsk),
		runBaseline:   make(map[string]string),
	}
	s.initSchedulerLock(opts)
	return s
}

func (s *Service) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now().UTC()
	}
	return time.Now().UTC()
}

// HasRunningRun 报告当前是否有自动化运行正在执行。
func (s *Service) HasRunningRun() bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	return len(s.inFlight) > 0
}

// NextRunWithin 报告是否存在已启用且会在 d 内到期的自动化（含已经到期未触发的）。
// 查询失败时返回 false，避免影响调用方（例如防休眠守卫）的其余逻辑。
func (s *Service) NextRunWithin(ctx context.Context, d time.Duration) bool {
	if s.opts.Store == nil {
		return false
	}
	// projectID 传空表示列出全部项目下的自动化。
	autos, err := s.opts.Store.ListAutomations(ctx, "")
	if err != nil {
		return false
	}
	deadline := s.now().Add(d)
	for _, a := range autos {
		if !a.Enabled || a.NextRunAt == nil {
			continue
		}
		if !a.NextRunAt.After(deadline) {
			return true
		}
	}
	return false
}

// CreateInput 描述创建新自动化任务的入参。
type CreateInput struct {
	ProjectID string
	Name      string
	Prompt    string
	Spec      Spec
	Enabled   bool
	// OriginThreadID 为创建方的线程作用域，heartbeat 模式把每轮运行投递回该线程。
	OriginThreadID string
	// CreatorThreadID 为通过工具创建的调用方会话，用于每会话任务数上限；为空表示不设限。
	CreatorThreadID string
}

// Create 创建新自动化任务。
func (s *Service) Create(ctx context.Context, in CreateInput) (Definition, error) {
	if strings.TrimSpace(in.ProjectID) == "" {
		return Definition{}, fmt.Errorf("%w: projectID is required", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Name) == "" {
		return Definition{}, fmt.Errorf("%w: name is required", ErrInvalidInput)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return Definition{}, fmt.Errorf("%w: prompt is required", ErrInvalidInput)
	}
	if err := in.Spec.Schedule.Validate(); err != nil {
		return Definition{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}

	in.Spec.RuntimeMode = harness.NormalizeMode(in.Spec.RuntimeMode)
	in.Spec.State = RunState{}
	in.Spec.RunMode = in.Spec.NormalizedRunMode()
	// MaxRuns 是规范字段，同时镜像到旧字段，保证旧版本二进制仍能拦截。
	if in.Spec.MaxRuns > 0 {
		in.Spec.MaxIterations = in.Spec.MaxRuns
	}
	// heartbeat 需要知道把运行投递回哪个线程，没有来源线程时无法调度。
	if in.Spec.RunMode == RunModeHeartbeat {
		in.Spec.OriginThreadID = strings.TrimSpace(in.OriginThreadID)
		if in.Spec.OriginThreadID == "" {
			return Definition{}, fmt.Errorf("%w: heartbeat run mode requires a source thread", ErrInvalidInput)
		}
	} else {
		in.Spec.OriginThreadID = ""
	}
	// 每会话任务数上限：只有来自 Agent 会话的创建才计数，UI 手动创建不带来源会话。
	if threadID := strings.TrimSpace(in.CreatorThreadID); threadID != "" {
		live, err := s.countLiveAutomations(ctx, in.ProjectID, threadID)
		if err != nil {
			return Definition{}, err
		}
		if live >= MaxActiveAutomationsPerThread {
			return Definition{}, fmt.Errorf("%w: 本会话的定时任务已达上限 %d 条，先取消不用的任务再建",
				ErrInvalidInput, MaxActiveAutomationsPerThread)
		}
		in.Spec.CreatorThreadID = threadID
	}
	if in.Spec.ConfidenceThreshold == 0 {
		in.Spec.ConfidenceThreshold = 0.8
	}
	if in.Spec.MaxConsecutiveFailures == 0 {
		in.Spec.MaxConsecutiveFailures = 3
	}

	now := s.now()
	status := StatusPaused
	var nextRunAt *time.Time
	if in.Enabled {
		status = StatusActive
		if next, ok := in.Spec.Schedule.Next(now); ok {
			nextRunAt = &next
		}
	}

	rawSpec, err := EncodeSpec(in.Spec)
	if err != nil {
		return Definition{}, fmt.Errorf("encode spec: %w", err)
	}

	auto := store.Automation{
		ID:        uuid.NewString(),
		ProjectID: &in.ProjectID,
		Name:      in.Name,
		Prompt:    in.Prompt,
		Schedule:  rawSpec,
		Enabled:   in.Enabled,
		Status:    status,
		NextRunAt: nextRunAt,
		Revision:  1,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := s.opts.Store.CreateAutomation(ctx, auto); err != nil {
		return Definition{}, err
	}
	s.notifyChange()

	return Definition{Automation: auto, Spec: in.Spec}, nil
}

// countLiveAutomations 统计指定项目下由 threadID 创建且仍处于启用中的任务数。
// Spec 解析失败的行视为没有来源会话而跳过，避免脏数据阻塞新建。
func (s *Service) countLiveAutomations(ctx context.Context, projectID, threadID string) (int, error) {
	autos, err := s.opts.Store.ListAutomations(ctx, projectID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, a := range autos {
		if !a.Enabled {
			continue
		}
		if DecodeSpec(a.Schedule).CreatorThreadID == threadID {
			count++
		}
	}
	return count, nil
}

// Get 根据 ID 获取自动化任务详情。
func (s *Service) Get(ctx context.Context, id string) (Definition, error) {
	auto, err := s.opts.Store.GetAutomation(ctx, id)
	if err != nil {
		return Definition{}, err
	}
	return Definition{Automation: auto, Spec: DecodeSpec(auto.Schedule)}, nil
}

// List 列出指定项目的自动化任务，projectID 为空时列出全部。
func (s *Service) List(ctx context.Context, projectID string) ([]Definition, error) {
	autos, err := s.opts.Store.ListAutomations(ctx, projectID)
	if err != nil {
		return nil, err
	}
	defs := make([]Definition, len(autos))
	for i, a := range autos {
		defs[i] = Definition{Automation: a, Spec: DecodeSpec(a.Schedule)}
	}
	return defs, nil
}

// UpdateInput 描述更新自动化任务的配置入参。
type UpdateInput struct {
	Name    *string
	Prompt  *string
	Spec    *Spec
	Enabled *bool
}

// Update 更新自动化任务配置，仅修改用户配置字段并保留运行状态。
func (s *Service) Update(ctx context.Context, id string, in UpdateInput, expectedRevision int) (Definition, error) {
	auto, err := s.opts.Store.GetAutomation(ctx, id)
	if err != nil {
		return Definition{}, err
	}
	spec := DecodeSpec(auto.Schedule)
	now := s.now()

	if in.Name != nil {
		auto.Name = *in.Name
	}
	if in.Prompt != nil {
		auto.Prompt = *in.Prompt
	}

	scheduleChanged := false
	if in.Spec != nil {
		if err := in.Spec.Schedule.Validate(); err != nil {
			return Definition{}, fmt.Errorf("%w: %v", ErrInvalidInput, err)
		}
		oldSchedule, newSchedule := spec.Schedule, in.Spec.Schedule
		datesEqual := oldSchedule.Date == nil && newSchedule.Date == nil ||
			oldSchedule.Date != nil && newSchedule.Date != nil && oldSchedule.Date.Equal(*newSchedule.Date)
		scheduleChanged = oldSchedule.Type != newSchedule.Type || oldSchedule.Seconds != newSchedule.Seconds ||
			!datesEqual || oldSchedule.TimeOfDay != newSchedule.TimeOfDay || oldSchedule.Timezone != newSchedule.Timezone ||
			oldSchedule.DayOfWeek != newSchedule.DayOfWeek || oldSchedule.Expression != newSchedule.Expression
		spec.Schedule = in.Spec.Schedule
		spec.StopWhen = in.Spec.StopWhen
		if in.Spec.ConfidenceThreshold != 0 {
			spec.ConfidenceThreshold = in.Spec.ConfidenceThreshold
		} else if spec.ConfidenceThreshold == 0 {
			spec.ConfidenceThreshold = 0.8
		}
		if in.Spec.MaxConsecutiveFailures != 0 {
			spec.MaxConsecutiveFailures = in.Spec.MaxConsecutiveFailures
		} else if spec.MaxConsecutiveFailures == 0 {
			spec.MaxConsecutiveFailures = 3
		}
		spec.MaxRuns = in.Spec.MaxRuns
		spec.MaxIterations = in.Spec.MaxIterations
		if spec.MaxRuns > 0 {
			// 镜像写入旧字段，保证旧版本二进制仍能拦截。
			spec.MaxIterations = spec.MaxRuns
		}
		spec.CatchUp = in.Spec.CatchUp
		// 模型与权限设置整体以入参为准：空串表示恢复默认。
		spec.Provider = in.Spec.Provider
		spec.Model = in.Spec.Model
		spec.Effort = in.Spec.Effort
		spec.RuntimeMode = harness.NormalizeMode(in.Spec.RuntimeMode)
	}

	if in.Enabled != nil {
		if *in.Enabled {
			auto.Enabled = true
			auto.Status = StatusActive
			spec.State.ConsecutiveFailures = 0
			spec.State.StopReason = ""
			if next, ok := spec.Schedule.Next(now); ok {
				auto.NextRunAt = &next
			} else {
				auto.NextRunAt = nil
			}
		} else {
			auto.Enabled = false
			auto.Status = StatusPaused
			auto.NextRunAt = nil
		}
	} else if scheduleChanged && auto.Enabled {
		if next, ok := spec.Schedule.Next(now); ok {
			auto.NextRunAt = &next
		} else {
			auto.NextRunAt = nil
		}
	}

	rawSpec, err := EncodeSpec(spec)
	if err != nil {
		return Definition{}, fmt.Errorf("encode spec: %w", err)
	}
	auto.Schedule = rawSpec

	updated, err := s.opts.Store.UpdateAutomation(ctx, auto, expectedRevision)
	if err != nil {
		return Definition{}, err
	}

	s.notifyChange()
	return Definition{Automation: updated, Spec: spec}, nil
}

// Delete 删除自动化任务及其进行中的内存追踪状态。
func (s *Service) Delete(ctx context.Context, id string) error {
	s.inFlightMu.Lock()
	delete(s.inFlight, id)
	s.inFlightMu.Unlock()

	if err := s.opts.Store.DeleteAutomation(ctx, id); err != nil {
		return err
	}
	s.notifyChange()
	return nil
}

// Runs 列出指定自动化的运行记录（不含已归档）。
func (s *Service) Runs(ctx context.Context, id string, limit int) ([]store.AutomationRun, error) {
	return s.opts.Store.ListAutomationRuns(ctx, id, limit)
}

// RunsFiltered 列出指定自动化的运行记录；includeArchived 为 true 时一并返回已归档记录。
func (s *Service) RunsFiltered(ctx context.Context, id string, limit int, includeArchived bool) ([]store.AutomationRun, error) {
	return s.opts.Store.ListAutomationRunsFiltered(ctx, id, limit, includeArchived)
}

// MarkRunRead 把单条运行记录标记为已读。
// 运行结束时不论成功失败都保持未读，只有用户查看后才置为已读，失败运行同理。
func (s *Service) MarkRunRead(ctx context.Context, runID string) error {
	if s.opts.Store == nil {
		return fmt.Errorf("%w: store not configured", ErrInvalidInput)
	}
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("%w: runID is required", ErrInvalidInput)
	}
	if err := s.opts.Store.MarkAutomationRunRead(ctx, runID, s.now()); err != nil {
		return err
	}
	s.notifyChange()
	return nil
}

// MarkAllRunsRead 把指定自动化下所有未读运行记录标记为已读，返回受影响条数。
func (s *Service) MarkAllRunsRead(ctx context.Context, automationID string) (int64, error) {
	if s.opts.Store == nil {
		return 0, fmt.Errorf("%w: store not configured", ErrInvalidInput)
	}
	if strings.TrimSpace(automationID) == "" {
		return 0, fmt.Errorf("%w: automationID is required", ErrInvalidInput)
	}
	n, err := s.opts.Store.MarkAutomationRunsRead(ctx, automationID, s.now())
	if err != nil {
		return 0, err
	}
	s.notifyChange()
	return n, nil
}

// ArchiveRun 归档或取消归档单条运行记录。
func (s *Service) ArchiveRun(ctx context.Context, runID string, archived bool) error {
	if s.opts.Store == nil {
		return fmt.Errorf("%w: store not configured", ErrInvalidInput)
	}
	if strings.TrimSpace(runID) == "" {
		return fmt.Errorf("%w: runID is required", ErrInvalidInput)
	}
	if err := s.opts.Store.SetAutomationRunArchived(ctx, runID, archived); err != nil {
		return err
	}
	s.notifyChange()
	return nil
}

// UnreadCounts 返回项目下每个自动化的未读运行记录数（automationID -> 未读数）；projectID 为空统计全部。
func (s *Service) UnreadCounts(ctx context.Context, projectID string) (map[string]int, error) {
	if s.opts.Store == nil {
		return map[string]int{}, nil
	}
	return s.opts.Store.CountUnreadAutomationRunsByProject(ctx, projectID)
}

// RunNow 立即触发一次自动化运行，忽略 next_run_at，若已有运行在执行中则返回 ErrAlreadyRunning。
func (s *Service) RunNow(ctx context.Context, id string) (store.AutomationRun, error) {
	s.tickMu.Lock()
	defer s.tickMu.Unlock()

	// 未持有调度锁时不得手动触发，避免与 active 进程的 Tick 并发执行同一任务。
	if !s.ensureSchedulerLock() {
		return store.AutomationRun{}, ErrSchedulerPassive
	}

	s.inFlightMu.Lock()
	if _, ok := s.inFlight[id]; ok {
		s.inFlightMu.Unlock()
		return store.AutomationRun{}, ErrAlreadyRunning
	}
	s.inFlightMu.Unlock()

	runs, err := s.opts.Store.ListAutomationRunsFiltered(ctx, id, 1, true)
	if err == nil && len(runs) > 0 && runs[0].Status == RunRunning {
		s.inFlightMu.Lock()
		s.inFlight[id] = runs[0].ID
		s.inFlightMu.Unlock()
		return store.AutomationRun{}, ErrAlreadyRunning
	}

	auto, err := s.opts.Store.GetAutomation(ctx, id)
	if err != nil {
		return store.AutomationRun{}, err
	}

	return s.trigger(ctx, auto, s.now(), RunTriggerManual, 0)
}

// ReportRun 由运行发起方之外的外部报告者（内置 MCP 工具 automation_report_result）结算一次运行。
// 复用调度器的结算路径，保证停止条件、失败计数与下次调度时间与 Tick 结算保持一致。
// callerThreadID 为调用方线程作用域：运行记录已绑定线程时只允许该线程结算，其他线程（含没有线程作用域的调用方）一律返回 ErrRunNotOwned。
// status 仅接受 RunSucceeded 或 RunFailed；运行不存在、不属于该线程或已结算时分别返回 ErrRunNotFound、ErrRunNotOwned、ErrRunAlreadySettled。
func (s *Service) ReportRun(ctx context.Context, automationID, runID, callerThreadID, status, summary, stopReason string, stopConditionMet bool) error {
	if s.opts.Store == nil {
		return fmt.Errorf("%w: store not configured", ErrInvalidInput)
	}
	if status != RunSucceeded && status != RunFailed {
		return fmt.Errorf("%w: status must be %q or %q", ErrInvalidInput, RunSucceeded, RunFailed)
	}

	run, err := s.opts.Store.GetAutomationRun(ctx, runID)
	if err != nil || run.AutomationID != automationID {
		return ErrRunNotFound
	}
	// 所有权：运行记录绑定了线程时，只有该线程能结算；未绑定线程的旧记录不受限。
	if run.ThreadID != nil && *run.ThreadID != "" && *run.ThreadID != callerThreadID {
		return ErrRunNotOwned
	}
	if run.Status != RunRunning {
		return ErrRunAlreadySettled
	}

	errStr := ""
	if status == RunFailed {
		errStr = summary
	}

	var evalResult *EvaluationResult
	if stopConditionMet {
		reason := strings.TrimSpace(stopReason)
		if reason == "" {
			reason = "reported by agent"
		}
		evalResult = &EvaluationResult{Satisfied: true, Confidence: 1, Reason: reason}
	}

	if !s.settleRun(ctx, automationID, runID, status, summary, errStr, evalResult, stopConditionMet) {
		// 与调度器并发结算时由先到者生效，后到者按“已结算”上报。
		return ErrRunAlreadySettled
	}
	return nil
}

// Start 启动后台定时调度协程。
func (s *Service) Start() {
	s.stateMu.Lock()
	if s.running {
		s.stateMu.Unlock()
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	s.stateMu.Unlock()

	// 启动时先尝试一次取锁；失败也照常启动 ticker，每次 Tick 继续重试接管。
	// 对账不在启动时做：app 层的 OnRunSettled 接线可能还没就绪，对账产生的通知会被吞掉。
	s.ensureSchedulerLock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.Tick(context.Background())
			}
		}
	}()
}

// reconcileRunningRuns 对账数据库里残留的 running 运行：重启后内存中的 inFlight 已丢失，
// 不处理的话这些运行会永久卡住。没有线程的说明还没投递出去，直接记失败；
// 有线程的重新登记进 inFlight，交给正常结算流程继续观察。
func (s *Service) reconcileRunningRuns(ctx context.Context) {
	if s.opts.Store == nil {
		return
	}
	runs, err := s.opts.Store.ListRunningAutomationRuns(ctx)
	if err != nil {
		s.logf("automation: reconcile running runs: %v", err)
		return
	}
	for _, run := range runs {
		if run.ThreadID == nil || *run.ThreadID == "" {
			s.settleRun(ctx, run.AutomationID, run.ID, RunFailed, "", "mcode 重启时本轮尚未开始", nil, false)
			continue
		}
		s.inFlightMu.Lock()
		s.inFlight[run.AutomationID] = run.ID
		s.inFlightMu.Unlock()
	}
}

// Stop 停止调度循环并等待其退出，幂等。
func (s *Service) Stop() {
	s.stateMu.Lock()
	running := s.running
	if running {
		s.running = false
		close(s.stopCh)
	}
	s.stateMu.Unlock()
	if running {
		s.wg.Wait()
	}
	// 无论是否启动过都释放锁：只调用过 Tick/RunNow 的实例也可能自行取过锁。
	s.releaseSchedulerLock()
}

// awaitingInputElapsed 记录本次运行首次观测到「等待输入」的时刻，并返回已等待时长；
// 首次观测时返回 0，从下一次调度开始计时。
func (s *Service) awaitingInputElapsed(runID string, now time.Time) time.Duration {
	s.awaitingMu.Lock()
	defer s.awaitingMu.Unlock()
	since, ok := s.awaitingSince[runID]
	if !ok {
		s.awaitingSince[runID] = now
		return 0
	}
	return now.Sub(since)
}

// clearAwaitingInput 清除运行的等待输入计时，避免用户回答后再次反问时沿用旧的起始时间。
func (s *Service) clearAwaitingInput(runID string) {
	s.awaitingMu.Lock()
	delete(s.awaitingSince, runID)
	s.awaitingMu.Unlock()
}

// Tick 执行单次调度逻辑，非重入。
func (s *Service) Tick(ctx context.Context) {
	if !s.tickMu.TryLock() {
		return
	}
	defer s.tickMu.Unlock()

	now := s.now()
	// 每次 Tick 只取一次调度锁：加锁实现可能有副作用（例如失败后下一轮才成功的序列），
	// 重复调用会让同一次巡视里的两次取锁结果互相抵消，把失败的那次当成已接管。
	locked := s.ensureSchedulerLock()

	// 对账推迟到首个持有调度锁的 Tick：Start 阶段 app 层的 OnRunSettled 接线可能还没就绪，
	// 那时结算失败会让停用/失败通知被吞掉。对账登记进 inFlight 的运行在本次 Tick 就能结算。
	// 被动实例不能改动别人名下的在途运行，因此拿不到调度锁就不对账。
	if !s.reconciled && locked {
		s.reconciled = true
		s.reconcileRunningRuns(ctx)
	}

	// (a) 检查并结算所有进行中的任务
	s.inFlightMu.Lock()
	inFlightCopy := make(map[string]string, len(s.inFlight))
	for k, v := range s.inFlight {
		inFlightCopy[k] = v
	}
	s.inFlightMu.Unlock()

	for autoID, runID := range inFlightCopy {
		run, err := s.opts.Store.GetAutomationRun(ctx, runID)
		if err != nil {
			continue
		}
		if run.Status != RunRunning {
			s.inFlightMu.Lock()
			delete(s.inFlight, autoID)
			s.inFlightMu.Unlock()
			s.clearAwaitingInput(runID)
			s.clearRunTracking(runID)
			continue
		}
		if run.ThreadID == nil {
			s.settleRun(ctx, autoID, runID, RunFailed, "", "thread ID not set", nil, false)
			continue
		}
		if s.opts.Driver == nil {
			s.settleRun(ctx, autoID, runID, RunFailed, "", "session driver not configured", nil, false)
			continue
		}
		st, err := s.opts.Driver.SessionState(ctx, *run.ThreadID)
		if err != nil {
			s.settleRun(ctx, autoID, runID, RunFailed, "", err.Error(), nil, false)
			continue
		}
		if st.Running || st.AwaitingApproval {
			continue
		}
		// 等待输入同样表示会话没有结束：不能计成功，也不能清零失败计数。
		if st.AwaitingInput {
			if s.awaitingInputElapsed(runID, now) < awaitingInputTimeout {
				continue
			}
			s.settleRun(ctx, autoID, runID, RunFailed, "", "agent is waiting for user input and timed out", nil, false)
			continue
		}
		s.clearAwaitingInput(runID)
		if st.Failed {
			reason := st.FailureReason
			if reason == "" {
				reason = "session failed"
			}
			s.settleRun(ctx, autoID, runID, RunFailed, "", reason, nil, false)
			continue
		}
		// dedicated/heartbeat 的投递只是排队：会话进度与投递前一致，说明本轮还没开跑，
		// 不能当成执行完毕结算。standalone 每次新建会话，不记基线。
		// 基线判断必须排在中断判断之前：来源会话上一轮被用户中断过时 Interrupted 会一直为 true，
		// 先判中断就会把还在排队等执行的本轮误记成 stopped。
		if baseline, ok := s.runBaselineOf(runID); ok {
			if st.Progress == baseline {
				if now.Sub(run.StartedAt) < awaitingInputTimeout {
					continue
				}
				s.settleRun(ctx, autoID, runID, RunFailed, "", "本轮提示词一直没有开始执行", nil, false)
				continue
			}
			s.clearRunBaseline(runID)
		}
		// 用户中断了本轮：记 stopped，既不算成功也不算失败。
		if st.Interrupted {
			s.settleRun(ctx, autoID, runID, RunStopped, "", "本轮被用户中断", nil, false)
			continue
		}
		// 支持追问的驱动：会话已空闲而运行仍是 running，说明 Agent 没有提交运行回执。
		// 先追问两次，仍未回执才判失败；不支持追问的驱动保持旧的「空闲即完成」行为。
		// 会话的 provider 没接宿主工具时同样无法提交回执（omp 不接 MCP），追问只会白等，也按旧行为处理。
		if _, ok := s.opts.Driver.(kanban.SessionMessenger); ok && !st.HostToolsUnavailable {
			ask := s.receiptAskOf(runID)
			if ask.Count >= maxReceiptAsks {
				if now.Sub(ask.LastAt) < s.receiptAskGrace() {
					continue
				}
				s.settleRun(ctx, autoID, runID, RunFailed, "", "Agent 未提交运行回执（已追问 2 次）", nil, false)
				continue
			}
			if ask.Count > 0 && now.Sub(ask.LastAt) < s.receiptAskGrace() {
				continue
			}
			text := fmt.Sprintf("本轮还没有提交运行结果，请立即调用 automation_report_result（automation_id=%s、run_id=%s），不要重做已完成的工作。", autoID, runID)
			if err := s.deliverPrompt(ctx, *run.ThreadID, text); err != nil {
				s.logf("automation: send receipt reminder for run %s: %v", runID, err)
				continue
			}
			s.recordReceiptAsk(runID, now)
			continue
		}

		// 运行成功
		summary := st.LastAssistantText
		var evalResult *EvaluationResult
		var stopMet bool
		auto, aerr := s.opts.Store.GetAutomation(ctx, autoID)
		if aerr == nil {
			spec := DecodeSpec(auto.Schedule)
			if spec.StopWhen != "" {
				var workDir string
				if auto.ProjectID != nil && s.opts.Projects != nil {
					workDir, _ = s.opts.Projects.ProjectRoot(ctx, *auto.ProjectID)
				}
				diffSummary := ""
				if workDir != "" {
					diffSummary = gitDiffSummary(ctx, workDir)
				}
				res, met := s.opts.Evaluator.Evaluate(ctx, workDir, spec.StopWhen, spec.ConfidenceThreshold, summary, diffSummary)
				evalResult = &res
				stopMet = met
			}
		}
		s.settleRun(ctx, autoID, runID, RunSucceeded, summary, "", evalResult, stopMet)
	}

	// (b) 触发到期的自动化任务：本进程没有跨进程调度锁时不得触发任何任务。
	if !locked {
		return
	}
	if s.opts.Store == nil {
		return
	}
	due, err := s.opts.Store.ListDueAutomations(ctx, now)
	if err != nil {
		return
	}

	for _, auto := range due {
		s.inFlightMu.Lock()
		_, running := s.inFlight[auto.ID]
		s.inFlightMu.Unlock()
		if running {
			continue
		}

		runs, err := s.opts.Store.ListAutomationRuns(ctx, auto.ID, 1)
		if err == nil && len(runs) > 0 && runs[0].Status == RunRunning {
			s.inFlightMu.Lock()
			s.inFlight[auto.ID] = runs[0].ID
			s.inFlightMu.Unlock()
			continue
		}

		s.triggerDue(ctx, auto, now)
	}
}

// triggerDue 触发部分的决策：先拦截达到次数上限的任务，再按是否补跑决定记一条 skipped、
// 只推进下一次触发时间，或以 scheduled/catch_up 来源启动运行。
func (s *Service) triggerDue(ctx context.Context, auto store.Automation, now time.Time) {
	spec := DecodeSpec(auto.Schedule)
	if cap := spec.EffectiveMaxRuns(); cap > 0 && spec.State.TotalRunCount >= cap {
		_ = s.disableForCap(ctx, auto, cap)
		return
	}

	missed := 0
	if auto.NextRunAt != nil {
		missed, _, _ = missedForSpec(spec, *auto.NextRunAt, now)
	}
	if missed > 0 && !spec.CatchUpEnabled() {
		if spec.Schedule.Type == "once" {
			// once 只代表一个触发点：先记一条 skipped 供 UI 审计，再停用任务。
			s.createSkippedRun(ctx, auto, missed)
			s.disableForMissedOnce(ctx, auto)
			s.notifyChange()
		} else {
			// 周期任务关闭补跑：只把下一次触发时间推进到 now 之后。
			s.advanceWithoutRun(ctx, auto, spec, now)
		}
		return
	}

	kind := RunTriggerScheduled
	if missed > 0 {
		kind = RunTriggerCatchUp
	}
	_, _ = s.trigger(ctx, auto, now, kind, missed)
}

// advanceWithoutRun 在原 NextRunAt 已到期但无需运行时，把下次触发时间推进到 now 之后。
// 调度没有后续触发点时停用任务并标记完成；CAS 冲突时重读一次再算。
func (s *Service) advanceWithoutRun(ctx context.Context, auto store.Automation, spec Spec, now time.Time) {
	for attempt := 0; attempt < 2; attempt++ {
		if next, ok := spec.Schedule.Next(now); ok {
			auto.NextRunAt = &next
		} else {
			auto.Enabled = false
			auto.NextRunAt = nil
			auto.Status = StatusCompleted
			spec.State.StopReason = "Completed: one-time schedule finished."
		}
		rawSpec, err := EncodeSpec(spec)
		if err != nil {
			return
		}
		auto.Schedule = rawSpec
		if _, err := s.opts.Store.UpdateAutomation(ctx, auto, auto.Revision); err == nil {
			s.notifyChange()
			return
		} else if !errors.Is(err, store.ErrRevisionConflict) {
			return
		}
		latest, err := s.opts.Store.GetAutomation(ctx, auto.ID)
		if err != nil {
			return
		}
		auto = latest
		spec = DecodeSpec(auto.Schedule)
	}
}

// createSkippedRun 为“错过 once 且关闭补跑”记一条 skipped 运行，用于 UI 审计。
// 它不增加 TotalRunCount、不改变最近运行状态，也不进入 inFlight。
func (s *Service) createSkippedRun(ctx context.Context, auto store.Automation, missed int) {
	now := s.now()
	summary := "Missed one-time schedule (catch-up disabled)."
	run := store.AutomationRun{
		ID:            uuid.NewString(),
		AutomationID:  auto.ID,
		Status:        RunSkipped,
		Trigger:       RunTriggerCatchUp,
		MissedCount:   missed,
		StartedAt:     now,
		FinishedAt:    &now,
		ResultSummary: &summary,
		Revision:      1,
	}
	if err := s.opts.Store.CreateAutomationRun(ctx, run); err != nil {
		s.logf("automation: create skipped run for %s: %v", auto.ID, err)
	}
}

// disableForMissedOnce 停用错过唯一触发点且关闭补跑的 once 任务。
func (s *Service) disableForMissedOnce(ctx context.Context, auto store.Automation) {
	spec := DecodeSpec(auto.Schedule)
	auto.Enabled = false
	auto.NextRunAt = nil
	auto.Status = StatusCompleted
	spec.State.StopReason = "Completed: Missed one-time schedule (catch-up disabled)."
	rawSpec, err := EncodeSpec(spec)
	if err != nil {
		return
	}
	auto.Schedule = rawSpec
	if _, err := s.opts.Store.UpdateAutomation(ctx, auto, auto.Revision); err != nil {
		s.logf("automation: disable missed once task %s: %v", auto.ID, err)
	}
}

// disableForCap 因达到运行次数上限而停用自动化，并记录停止原因。
func (s *Service) disableForCap(ctx context.Context, auto store.Automation, cap int) error {
	spec := DecodeSpec(auto.Schedule)
	auto.Enabled = false
	auto.NextRunAt = nil
	auto.Status = StatusCompleted
	spec.State.StopReason = fmt.Sprintf("Completed: Reached maximum run count (%d).", cap)
	rawSpec, err := EncodeSpec(spec)
	if err != nil {
		return err
	}
	auto.Schedule = rawSpec
	if _, err := s.opts.Store.UpdateAutomation(ctx, auto, auto.Revision); err != nil {
		return err
	}
	s.notifyChange()
	return nil
}

func (s *Service) trigger(ctx context.Context, auto store.Automation, now time.Time, kind string, missed int) (store.AutomationRun, error) {
	spec := DecodeSpec(auto.Schedule)
	// 次数上限前置检查：已到上限的任务不再启动运行，并自动停用。
	if cap := spec.EffectiveMaxRuns(); cap > 0 && spec.State.TotalRunCount >= cap {
		_ = s.disableForCap(ctx, auto, cap)
		return store.AutomationRun{}, ErrMaxRunsReached
	}
	var nextRunAt *time.Time
	if next, ok := spec.Schedule.Next(now); ok {
		nextRunAt = &next
	}
	auto.NextRunAt = nextRunAt
	auto.LastRunAt = &now
	spec.State.TotalRunCount++
	spec.State.LastRunStatus = RunRunning
	rawSpec, err := EncodeSpec(spec)
	if err != nil {
		return store.AutomationRun{}, fmt.Errorf("encode spec: %w", err)
	}
	auto.Schedule = rawSpec

	updatedAuto, err := s.opts.Store.UpdateAutomation(ctx, auto, auto.Revision)
	if err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			latest, gerr := s.opts.Store.GetAutomation(ctx, auto.ID)
			if gerr != nil {
				return store.AutomationRun{}, gerr
			}
			auto = latest
			spec = DecodeSpec(auto.Schedule)
			if next, ok := spec.Schedule.Next(now); ok {
				nextRunAt = &next
			} else {
				nextRunAt = nil
			}
			auto.NextRunAt = nextRunAt
			auto.LastRunAt = &now
			spec.State.TotalRunCount++
			spec.State.LastRunStatus = RunRunning
			rawSpec, err = EncodeSpec(spec)
			if err != nil {
				return store.AutomationRun{}, fmt.Errorf("encode spec: %w", err)
			}
			auto.Schedule = rawSpec
			updatedAuto, err = s.opts.Store.UpdateAutomation(ctx, auto, auto.Revision)
			if err != nil {
				return store.AutomationRun{}, err
			}
		} else {
			return store.AutomationRun{}, err
		}
	}
	auto = updatedAuto
	defer s.notifyChange()

	runID := uuid.NewString()
	// 新运行记录不带 read_at，即默认未读；结算时也不会写入 read_at。
	run := store.AutomationRun{
		ID:           runID,
		AutomationID: auto.ID,
		Status:       RunRunning,
		Trigger:      kind,
		MissedCount:  missed,
		StartedAt:    now,
		Revision:     1,
	}
	if err := s.opts.Store.CreateAutomationRun(ctx, run); err != nil {
		return store.AutomationRun{}, fmt.Errorf("create automation run: %w", err)
	}

	if auto.ProjectID == nil || *auto.ProjectID == "" {
		s.settleRun(ctx, auto.ID, runID, RunFailed, "", "automation project ID is missing", nil, false)
		return s.opts.Store.GetAutomationRun(ctx, runID)
	}
	if s.opts.Projects == nil {
		s.settleRun(ctx, auto.ID, runID, RunFailed, "", "project resolver not configured", nil, false)
		return s.opts.Store.GetAutomationRun(ctx, runID)
	}
	workDir, err := s.opts.Projects.ProjectRoot(ctx, *auto.ProjectID)
	if err != nil {
		s.settleRun(ctx, auto.ID, runID, RunFailed, "", fmt.Sprintf("resolve project root: %v", err), nil, false)
		return s.opts.Store.GetAutomationRun(ctx, runID)
	}

	if s.opts.Driver == nil {
		s.settleRun(ctx, auto.ID, runID, RunFailed, "", "session driver not configured", nil, false)
		return s.opts.Store.GetAutomationRun(ctx, runID)
	}

	// 回执工具需要本轮 run_id，提示词里必须带上，否则 Agent 无从调用。
	runPrompt := buildRunPrompt(auto.Prompt, auto.ID, runID)
	threadID, err := s.startRunSession(ctx, auto, spec, workDir, runPrompt, runID)
	if err != nil {
		s.settleRun(ctx, auto.ID, runID, RunFailed, "", fmt.Sprintf("start session: %v", err), nil, false)
		return s.opts.Store.GetAutomationRun(ctx, runID)
	}

	run.ThreadID = &threadID
	if updatedRun, uerr := s.opts.Store.UpdateAutomationRun(ctx, run, run.Revision); uerr == nil {
		run = updatedRun
	}

	s.inFlightMu.Lock()
	s.inFlight[auto.ID] = runID
	s.inFlightMu.Unlock()

	return run, nil
}

// buildRunPrompt 在原始提示词后追加回执要求，让 Agent 知道本轮的 automation_id 与 run_id。
func buildRunPrompt(prompt, automationID, runID string) string {
	return fmt.Sprintf("%s\n\n本轮结束前必须调用 automation_report_result 工具，参数 automation_id=%s、run_id=%s、status 取 succeeded 或 failed、summary 写一两句结论；没调用就不算完成。", prompt, automationID, runID)
}

// startRunSession 按运行模式为一次运行选定会话线程：
// standalone 每次新建会话；dedicated 复用该自动化专属线程（首次运行时创建并固化）；
// heartbeat 把本轮 prompt 投递回创建自动化时的来源线程。
// prompt 已带有本轮 run_id 的回执要求，三种模式都必须用它而不是原始 auto.Prompt。
func (s *Service) startRunSession(ctx context.Context, auto store.Automation, spec Spec, workDir, prompt, runID string) (string, error) {
	switch spec.NormalizedRunMode() {
	case RunModeDedicated:
		if spec.DedicatedThreadID != "" {
			// 投递只是排队：先记下当前进度，结算时据此判断本轮有没有真的开跑。
			s.captureRunBaseline(ctx, spec.DedicatedThreadID, runID)
			deliverErr := s.deliverPrompt(ctx, spec.DedicatedThreadID, prompt)
			if deliverErr != nil && !threadNotFound(deliverErr) {
				return "", deliverErr
			}
			if deliverErr == nil {
				return spec.DedicatedThreadID, nil
			}
			// 用户把专属线程删了：固化的线程 ID 不清掉，之后每轮投递都会失败，
			// 连续失败还会停用自动化且永不恢复。这里丢掉旧线程与本轮基线，
			// 落到下面的新建会话分支；新线程由 persistDedicatedThread 写回。
			s.clearRunBaseline(runID)
		}
		threadID, err := s.opts.Driver.StartSession(ctx, kanban.SessionRequest{
			ProjectID: *auto.ProjectID,
			WorkDir:   workDir,
			Title:     "Automation: " + auto.Name,
			Prompt:    prompt,
			// 模型与权限模式来自自动化自身配置，空值由驱动沿用默认行为。
			Provider:    spec.Provider,
			Model:       spec.Model,
			Effort:      spec.Effort,
			RuntimeMode: spec.RuntimeMode,
		})
		if err != nil {
			return "", err
		}
		// 固化专属线程，后续运行复用同一个会话。
		s.persistDedicatedThread(ctx, auto.ID, threadID)
		return threadID, nil

	case RunModeHeartbeat:
		if spec.OriginThreadID == "" {
			return "", errors.New("heartbeat run mode has no source thread")
		}
		s.captureRunBaseline(ctx, spec.OriginThreadID, runID)
		if err := s.deliverPrompt(ctx, spec.OriginThreadID, prompt); err != nil {
			return "", err
		}
		return spec.OriginThreadID, nil

	default:
		return s.opts.Driver.StartSession(ctx, kanban.SessionRequest{
			ProjectID: *auto.ProjectID,
			WorkDir:   workDir,
			Title:     "Automation: " + auto.Name,
			Prompt:    prompt,
			// 模型与权限模式来自自动化自身配置，空值由驱动沿用默认行为。
			Provider:    spec.Provider,
			Model:       spec.Model,
			Effort:      spec.Effort,
			RuntimeMode: spec.RuntimeMode,
		})
	}
}

// threadNotFound 判断投递失败是否因为目标线程已不存在。
// 进程内存储层会直接返回 store.ErrNotFound；远程后端（internal/remote/memory_backend.go 的
// sendMessage）返回的是带「thread <id> not found」文案的 RPC 错误，不带哨兵，
// 所以再按文案兜底匹配一次。文案匹配同时要求出现 thread 与 not found，
// 避免把「binary not found」这类无关错误误判成线程被删。
func threadNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "thread") && strings.Contains(msg, "not found")
}

// deliverPrompt 向已有会话投递一轮对话；驱动不支持投递时返回错误。
func (s *Service) deliverPrompt(ctx context.Context, threadID, prompt string) error {
	messenger, ok := s.opts.Driver.(kanban.SessionMessenger)
	if !ok {
		return errors.New("session driver does not support follow-up prompts")
	}
	return messenger.SendFollowUp(ctx, threadID, prompt)
}

// persistDedicatedThread 将 dedicated 模式首次创建的专属线程写回 Spec，遇到并发写时重试。
func (s *Service) persistDedicatedThread(ctx context.Context, autoID, threadID string) {
	for attempt := 0; attempt < 3; attempt++ {
		auto, err := s.opts.Store.GetAutomation(ctx, autoID)
		if err != nil {
			return
		}
		spec := DecodeSpec(auto.Schedule)
		if spec.NormalizedRunMode() != RunModeDedicated || spec.DedicatedThreadID == threadID {
			return
		}
		spec.DedicatedThreadID = threadID
		rawSpec, err := EncodeSpec(spec)
		if err != nil {
			return
		}
		auto.Schedule = rawSpec
		if _, err := s.opts.Store.UpdateAutomation(ctx, auto, auto.Revision); err == nil {
			s.notifyChange()
			return
		}
	}
}

// captureRunBaseline 在投递前取一次会话进度作为基线；取不到时按已开跑处理。
func (s *Service) captureRunBaseline(ctx context.Context, threadID, runID string) {
	if threadID == "" || runID == "" || s.opts.Driver == nil {
		return
	}
	st, err := s.opts.Driver.SessionState(ctx, threadID)
	if err != nil {
		return
	}
	s.trackingMu.Lock()
	s.runBaseline[runID] = st.Progress
	s.trackingMu.Unlock()
}

// runBaselineOf 读取运行的投递前进度基线。
func (s *Service) runBaselineOf(runID string) (string, bool) {
	s.trackingMu.Lock()
	defer s.trackingMu.Unlock()
	baseline, ok := s.runBaseline[runID]
	return baseline, ok
}

// clearRunBaseline 清除运行的进度基线。
func (s *Service) clearRunBaseline(runID string) {
	s.trackingMu.Lock()
	delete(s.runBaseline, runID)
	s.trackingMu.Unlock()
}

// receiptAskOf 读取运行已追问回执的次数与最后一次追问时间。
func (s *Service) receiptAskOf(runID string) receiptAsk {
	s.trackingMu.Lock()
	defer s.trackingMu.Unlock()
	return s.receiptAsks[runID]
}

// recordReceiptAsk 记录一次回执追问。
func (s *Service) recordReceiptAsk(runID string, at time.Time) {
	s.trackingMu.Lock()
	ask := s.receiptAsks[runID]
	ask.Count++
	ask.LastAt = at
	s.receiptAsks[runID] = ask
	s.trackingMu.Unlock()
}

// clearRunTracking 清除运行的回执追问与进度基线记录。
func (s *Service) clearRunTracking(runID string) {
	s.trackingMu.Lock()
	delete(s.receiptAsks, runID)
	delete(s.runBaseline, runID)
	s.trackingMu.Unlock()
}

// receiptAskGrace 是两次回执追问之间、以及最后一次追问到判失败之间的宽限。
func (s *Service) receiptAskGrace() time.Duration {
	if s.interval > receiptAskGraceMin {
		return s.interval
	}
	return receiptAskGraceMin
}

// settleRun 结算一次运行并更新自动化状态；返回本次调用是否真正完成了结算。
func (s *Service) settleRun(ctx context.Context, autoID, runID, status, summary, errStr string, evalResult *EvaluationResult, stopConditionMet bool) bool {
	run, err := s.opts.Store.GetAutomationRun(ctx, runID)
	if err != nil {
		return false
	}
	if run.Status != RunRunning {
		return false
	}

	now := s.now()
	run.Status = status
	run.FinishedAt = &now
	if summary != "" {
		run.ResultSummary = &summary
	}
	if errStr != "" {
		run.Error = &errStr
	}
	run.ChangedFiles = gitChangedFiles(ctx, s.runWorkDir(ctx, autoID))

	updatedRun, err := s.opts.Store.UpdateAutomationRun(ctx, run, run.Revision)
	if err != nil {
		return false
	}
	defer s.notifyChange()

	var auto store.Automation
	var spec Spec
	for attempt := 0; attempt < 3; attempt++ {
		auto, err = s.opts.Store.GetAutomation(ctx, autoID)
		if err != nil {
			break
		}
		spec = DecodeSpec(auto.Schedule)
		spec.State.LastRunStatus = status
		if evalResult != nil {
			spec.State.LastEvaluation = evalResult
		}

		switch status {
		case RunFailed:
			spec.State.ConsecutiveFailures++
			maxFails := spec.MaxConsecutiveFailures
			if maxFails > 0 && spec.State.ConsecutiveFailures >= maxFails {
				auto.Enabled = false
				auto.NextRunAt = nil
				auto.Status = StatusFailed
				spec.State.StopReason = fmt.Sprintf("Paused: Reached maximum consecutive failure limit (%d).", maxFails)
			}
		case RunSucceeded:
			spec.State.ConsecutiveFailures = 0
		case RunStopped:
			// 用户中断既不算失败也不算成功：不计也不清零失败计数，也不评估停止条件，
			// 下一次运行时间照常推进。
		}

		if stopConditionMet {
			auto.Enabled = false
			auto.NextRunAt = nil
			auto.Status = StatusCompleted
			reason := "stop condition satisfied"
			if evalResult != nil && evalResult.Reason != "" {
				reason = evalResult.Reason
			}
			spec.State.StopReason = fmt.Sprintf("Completed: Stop condition satisfied (%s).", reason)
		} else if auto.Enabled {
			if maxRuns := spec.EffectiveMaxRuns(); maxRuns > 0 && spec.State.TotalRunCount >= maxRuns {
				auto.Enabled = false
				auto.NextRunAt = nil
				auto.Status = StatusCompleted
				spec.State.StopReason = fmt.Sprintf("Completed: Reached maximum run count (%d).", maxRuns)
			} else {
				if next, ok := spec.Schedule.Next(now); ok {
					auto.NextRunAt = &next
				} else {
					auto.Enabled = false
					auto.NextRunAt = nil
					auto.Status = StatusCompleted
					spec.State.StopReason = "Completed: one-time schedule finished."
				}
			}
		}

		rawSpec, encErr := EncodeSpec(spec)
		if encErr != nil {
			break
		}
		auto.Schedule = rawSpec

		updatedAuto, upErr := s.opts.Store.UpdateAutomation(ctx, auto, auto.Revision)
		if upErr == nil {
			auto = updatedAuto
			break
		}
		if !errors.Is(upErr, store.ErrRevisionConflict) {
			break
		}
	}

	s.inFlightMu.Lock()
	delete(s.inFlight, autoID)
	s.inFlightMu.Unlock()
	s.clearAwaitingInput(runID)
	s.clearRunTracking(runID)

	if s.opts.OnRunSettled != nil {
		s.opts.OnRunSettled(Definition{Automation: auto, Spec: spec}, updatedRun)
	}
	return true
}

// gitOutput 在 workDir 执行 git 命令并返回标准输出。
func gitOutput(ctx context.Context, workDir string, args ...string) ([]byte, error) {
	cmd := procattr.HideWindow(exec.CommandContext(ctx, "git", args...))
	cmd.Dir = workDir
	// 继承登录 Shell 环境变量，保证 git hooks 能用到用户 PATH 下的工具
	cmd.Env = append(loginenv.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	return cmd.Output()
}

// gitDiffSummaryMax 是 gitDiffSummary 结果的最大字符数。
const gitDiffSummaryMax = 4000

// gitDiffSummary 在 workDir 汇总 diff --stat HEAD、未跟踪文件和截断后的 patch，总长不超过 gitDiffSummaryMax；
// 无任何变动时返回 NoCodeChangesMarker，git 失败时返回空字符串。
func gitDiffSummary(ctx context.Context, workDir string) string {
	if workDir == "" {
		return ""
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	statOut, err := gitOutput(timeoutCtx, workDir, "diff", "--stat", "HEAD")
	if err != nil {
		return ""
	}
	lsOut, err := gitOutput(timeoutCtx, workDir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return ""
	}
	if len(strings.TrimSpace(string(statOut))) == 0 && len(strings.TrimSpace(string(lsOut))) == 0 {
		return NoCodeChangesMarker
	}

	var sb strings.Builder
	sb.Write(statOut)
	if len(lsOut) > 0 {
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString("Untracked:\n")
		sb.Write(lsOut)
	}
	if remain := gitDiffSummaryMax - sb.Len(); remain > 0 {
		if patch, perr := gitOutput(timeoutCtx, workDir, "diff", "HEAD"); perr == nil && len(patch) > 0 {
			sb.WriteString("\n")
			sb.Write(patch)
		}
	}
	return truncateUTF8(sb.String(), gitDiffSummaryMax)
}

// truncateUTF8 将 s 截断到不超过 max 字节，且不切断多字节字符。
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// gitChangedFiles 统计 workDir 中相对 HEAD 的变更文件数（含未跟踪文件，去重）；git 失败时返回 nil。
func gitChangedFiles(ctx context.Context, workDir string) *int {
	if workDir == "" {
		return nil
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	diffOut, err := gitOutput(timeoutCtx, workDir, "diff", "--name-only", "HEAD")
	if err != nil {
		return nil
	}
	lsOut, err := gitOutput(timeoutCtx, workDir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, out := range [][]byte{diffOut, lsOut} {
		for _, line := range strings.Split(string(out), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				seen[line] = struct{}{}
			}
		}
	}
	n := len(seen)
	return &n
}

// runWorkDir 解析自动化所属项目的工作目录，失败时返回空字符串。
func (s *Service) runWorkDir(ctx context.Context, autoID string) string {
	if s.opts.Projects == nil {
		return ""
	}
	auto, err := s.opts.Store.GetAutomation(ctx, autoID)
	if err != nil || auto.ProjectID == nil {
		return ""
	}
	workDir, _ := s.opts.Projects.ProjectRoot(ctx, *auto.ProjectID)
	return workDir
}
