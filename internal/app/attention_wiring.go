package app

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"mcode/internal/attention"
	"mcode/internal/automation"
	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
	"mcode/internal/swarm"
)

const (
	// attentionEventBuffer 是事件写库队列长度：观察者回调只做内存判定与入队，
	// 真正的写库交给单个 worker goroutine 串行完成。
	attentionEventBuffer = 256
	// attentionWriteTimeout 是单次写库的超时时间。
	attentionWriteTimeout = 5 * time.Second
	// attentionScanTimeout 是一次全量看板扫描的超时时间。
	attentionScanTimeout = 30 * time.Second
	// attentionStalledInterval 是卡住巡检间隔；阈值每次巡检现读配置，改设置后无需重启。
	attentionStalledInterval = 60 * time.Second
	// attentionKanbanDebounce 是看板变更的合并窗口，避免一次批量移动触发多次全量扫描。
	attentionKanbanDebounce = time.Second
	// automationNoticeSummaryLimit 是自动化完成通知正文的最大字数，系统通知展示不了长文本。
	automationNoticeSummaryLimit = 120
	// hostNoticeTitleMaxRunes 是宿主自拼通知标题的最大字数：通知是 IM 式短消息，
	// 标题过长在系统通知里会被直接截掉。
	hostNoticeTitleMaxRunes = 40
	// hostNoticeBodyMaxRunes 是宿主自拼通知正文的最大字数，避免报错详情把整条通知撑成一大段。
	hostNoticeBodyMaxRunes = 200
)

// attentionWiringOptions 是接线的构造参数；Hub、Kanban、Swarm、Client 为空时跳过对应事件源。
type attentionWiringOptions struct {
	// Service 是待处理事件服务，必填。
	Service *attention.Service
	// Hub 提供会话广播，会话类事件用它订阅 thread.summary 与 requests.changed。
	Hub *remote.StreamHub
	// Kanban 提供看板任务列表，用于快照比较。
	Kanban *kanban.Service
	// Swarm 提供 bee 编排状态，用于失败检测。
	Swarm *swarm.Manager
	// Client 提供 WatchKanban、WatchSwarm 订阅入口。
	Client *Client
	// Store 提供看板任务列表，无进展看门狗用它找运行中的任务会话。
	Store *store.Store
	// Sessions 提供会话运行状态与最近一次文件改动时刻。
	Sessions sessionProgressSource
	// DataDir 是配置目录，卡住巡检每次从这里现读阈值与开关。
	DataDir string
	// Now 提供当前时间，测试可注入；为空时使用 time.Now。
	Now func() time.Time
}

// attentionWiring 把会话、看板、自动化、bee 四类事件源接进待处理事件服务。
// hub 观察者回调只更新内存状态并把写库动作投进队列，避免阻塞广播路径；
// 其余事件源在各自的 goroutine 里读快照并写库。
type attentionWiring struct {
	svc     *attention.Service
	hub     *remote.StreamHub
	kanban  *kanban.Service
	swarm   *swarm.Manager
	client  *Client
	dataDir string
	now     func() time.Time
	store   *store.Store
	// sessions 是会话状态读取入口，无进展看门狗按它确认会话是否仍在跑。
	sessions sessionProgressSource
	// runningBees 提供运行中的 bee 快照，测试可注入；为空时读 swarm.Manager。
	runningBees func() map[string][]swarm.Bee

	mu sync.Mutex
	// alerts 是本接线私有的会话提醒探测器，与会话列表的提醒通知各记一份状态。
	alerts *remote.SessionAlertDetector
	// threads 缓存每个会话的提醒状态与进入时刻。
	threads map[string]*threadAlertState
	// ignoreThread 判定需要排除的会话，被忽略的会话不产生任何事件。
	ignoreThread func(threadID string) bool
	// kanbanSnapshot 是上一次看板扫描的任务快照，首次为 nil 时按全量基线处理。
	kanbanSnapshot map[string]kanbanTaskSnapshot
	// kanbanGoalSnapshot 是上一次看板扫描的目标快照，首次为 nil 时按全量基线处理。
	kanbanGoalSnapshot map[string]kanbanGoalSnapshot
	// kanbanTimer 是看板扫描的防抖定时器，kanbanGen 用于丢弃过期回调。
	kanbanTimer *time.Timer
	kanbanGen   int
	// beeSnapshot 记录每个 lead 会话下 bee 的状态，用于检测新进入失败的 bee。
	beeSnapshot map[string]map[string]string
	// mergeReviewSnapshot 记录每个 lead 会话下 bee 是否处于待审查状态，用于去重与离开时消解。
	mergeReviewSnapshot map[string]map[string]bool
	// noProgress 是无进展看门狗的对象状态表，只在看门狗内读写，受 mu 保护。
	noProgress map[string]*noProgressState
	// automationNotify 是自动化运行成功后的桌面通知实现，为空表示未接入桌面端。
	automationNotify func(title, body, threadID string)
	// automationStoppedNotified 记录已发过停用通知的自动化 ID，避免重复打扰；
	// 自动化重新启用后清除，下次再停用还能再通知一次。
	automationStoppedNotified map[string]struct{}

	removeHub    func()
	removeKanban func()
	removeSwarm  func()

	queue     chan func()
	done      chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

// threadAlertState 是单个会话的提醒缓存：标题、项目、运行状态与每类提醒的进入时刻。
// 进入时刻按（会话，类型）记住，同一段状态多次被观察时沿用同一个 EnteredAt，
// 记录层据此去重，不会产生重复事件；消解时清空，下次重新进入才取新时刻。
type threadAlertState struct {
	title     string
	projectID string
	observed  bool
	running   bool
	pending   bool
	entered   map[attention.Kind]time.Time
}

// newAttentionWiring 构造接线，不启动任何订阅。
func newAttentionWiring(opts attentionWiringOptions) *attentionWiring {
	return &attentionWiring{
		svc:                opts.Service,
		hub:                opts.Hub,
		kanban:             opts.Kanban,
		swarm:              opts.Swarm,
		client:             opts.Client,
		dataDir:            opts.DataDir,
		now:                opts.Now,
		store:              opts.Store,
		sessions:           opts.Sessions,
		alerts:             remote.NewSessionAlertDetector(),
		threads:            make(map[string]*threadAlertState),
		kanbanSnapshot:     make(map[string]kanbanTaskSnapshot),
		kanbanGoalSnapshot: make(map[string]kanbanGoalSnapshot),
		beeSnapshot:        make(map[string]map[string]string),
		noProgress:         make(map[string]*noProgressState),
		queue:              make(chan func(), attentionEventBuffer),
		done:               make(chan struct{}),
	}
}

// Start 启动四类事件源的订阅与后台 goroutine。可重复调用，只有第一次生效。
func (w *attentionWiring) Start() {
	if w.svc == nil {
		return
	}
	w.startOnce.Do(func() {
		// 删除会话钩子在 wireSwarm 里安装得更早，这里登记消解入口供它追加调用。
		registerDeletedThreadAttention(w)
		// 后端已从 store 恢复完会话，先一次性消解指向已删会话或已无待处理请求的审批提醒。
		bootCtx, cancel := context.WithTimeout(context.Background(), attentionBootTimeout)
		w.reconcileThreadApprovals(bootCtx)
		cancel()
		w.wg.Add(1)
		go w.worker()
		w.wg.Add(1)
		go w.stalledLoop()
		w.wg.Add(1)
		go w.noProgressLoop()
		w.observeThreads()
		w.observeKanban()
		w.observeSwarm()
		// 启动即做一次全量看板扫描；与上一轮重复的条目会被记录层去重。
		go w.scanKanban()
	})
}

// Stop 停止所有订阅与后台 goroutine，并等待已投递的写库任务结束。
func (w *attentionWiring) Stop() {
	if w.svc == nil {
		return
	}
	w.stopOnce.Do(func() {
		unregisterDeletedThreadAttention(w)
		close(w.done)
		w.mu.Lock()
		if w.kanbanTimer != nil {
			w.kanbanTimer.Stop()
			w.kanbanTimer = nil
		}
		removeHub, removeKanban, removeSwarm := w.removeHub, w.removeKanban, w.removeSwarm
		w.removeHub, w.removeKanban, w.removeSwarm = nil, nil, nil
		w.mu.Unlock()
		if removeHub != nil {
			removeHub()
		}
		if removeKanban != nil {
			removeKanban()
		}
		if removeSwarm != nil {
			removeSwarm()
		}
		w.wg.Wait()
	})
}

// SetIgnoreThread 设置会话忽略判定：被忽略的会话不产生任何事件，也不维护运行状态。
// 主代理用它排除自己派发的跟进会话，避免总管被当成待处理对象。
func (w *attentionWiring) SetIgnoreThread(fn func(threadID string) bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ignoreThread = fn
}

// nowTime 返回当前时间，未注入时钟时回退到 time.Now。
func (w *attentionWiring) nowTime() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// isIgnoredLocked 判断会话是否被忽略，调用方需持有 mu。
func (w *attentionWiring) isIgnoredLocked(threadID string) bool {
	if w.ignoreThread == nil || threadID == "" {
		return false
	}
	return w.ignoreThread(threadID)
}

// worker 串行执行投递进来的写库任务，保证同一会话的消解与记录按观察顺序生效。
func (w *attentionWiring) worker() {
	defer w.wg.Done()
	for {
		select {
		case <-w.done:
			return
		case job := <-w.queue:
			if job != nil {
				job()
			}
		}
	}
}

// push 把写库任务投进队列；队列满时丢弃并记日志，保证观察者回调不阻塞。
func (w *attentionWiring) push(job func()) {
	if job == nil {
		return
	}
	select {
	case w.queue <- job:
	default:
		log.Printf("attention: 写库队列已满，丢弃一条待处理事件")
	}
}

// record 写入一条待处理事件；收件箱属于旁路功能，写库失败只记日志。
// 返回值表示本次是否新建了记录：被去重或每日上限截停时返回 false。
func (w *attentionWiring) record(input attention.Input) bool {
	ctx, cancel := context.WithTimeout(context.Background(), attentionWriteTimeout)
	defer cancel()
	_, created, err := w.svc.Record(ctx, input)
	if err != nil {
		log.Printf("attention: 记录待处理事件失败（%s/%s）: %v", input.Kind, input.SubjectID, err)
		return false
	}
	return created
}

// resolveSubject 自动消解某个对象下指定类型的事件。
func (w *attentionWiring) resolveSubject(subjectType attention.SubjectType, subjectID string, kinds ...attention.Kind) {
	ctx, cancel := context.WithTimeout(context.Background(), attentionWriteTimeout)
	defer cancel()
	if _, err := w.svc.ResolveSubject(ctx, subjectType, subjectID, attention.ResolvedByAuto, kinds...); err != nil {
		log.Printf("attention: 自动消解待处理事件失败（%s/%s）: %v", subjectType, subjectID, err)
	}
}

// threadStateLocked 取（必要时新建）会话的提醒缓存，同时刷新标题与项目。
func (w *attentionWiring) threadStateLocked(threadID, title, projectID string) *threadAlertState {
	state, ok := w.threads[threadID]
	if !ok {
		state = &threadAlertState{entered: make(map[attention.Kind]time.Time)}
		w.threads[threadID] = state
	}
	if title != "" {
		state.title = title
	}
	if projectID != "" {
		state.projectID = projectID
	}
	return state
}

// resolveThreadLocked 准备按会话消解事件的任务，并清掉这些类型的进入时刻缓存，
// 保证下次重新进入同一状态时拿到新的 EnteredAt。
func (w *attentionWiring) resolveThreadLocked(threadID string, kinds ...attention.Kind) []func() {
	if state, ok := w.threads[threadID]; ok {
		for _, kind := range kinds {
			delete(state.entered, kind)
		}
	}
	resolveKinds := append([]attention.Kind(nil), kinds...)
	return []func(){func() { w.resolveSubject(attention.SubjectThread, threadID, resolveKinds...) }}
}

// trackThreadSummaryLocked 按 thread.summary 维护会话运行状态并产出消解任务：
// 由停止转为运行中时消解失败、额度与卡住提醒；待审批结束时消解审批提醒。
func (w *attentionWiring) trackThreadSummaryLocked(threadID string, summary wire.ThreadSummary) []func() {
	state := w.threadStateLocked(threadID, summary.Title, summary.ProjectID)
	var jobs []func()
	if state.observed {
		if !state.running && summary.IsRunning {
			jobs = append(jobs, w.resolveThreadLocked(threadID,
				attention.KindThreadFailed, attention.KindThreadQuota, attention.KindThreadStalled)...)
		}
		if state.pending && !summary.HasPendingRequest {
			jobs = append(jobs, w.resolveThreadLocked(threadID, attention.KindThreadApproval)...)
		}
	}
	state.observed = true
	state.running = summary.IsRunning
	state.pending = summary.HasPendingRequest
	return jobs
}

// alertJobsLocked 把一条会话提醒映射成待处理事件写入任务：Title 用会话标题，
// Detail 用提醒正文；done 提醒不单独记事件，只消解卡住与审批。
func (w *attentionWiring) alertJobsLocked(alert remote.SessionAlert) []func() {
	var kind attention.Kind
	switch alert.Kind {
	case remote.SessionAlertApproval:
		kind = attention.KindThreadApproval
	case remote.SessionAlertFailed:
		kind = attention.KindThreadFailed
	case remote.SessionAlertQuota:
		kind = attention.KindThreadQuota
	case remote.SessionAlertStalled:
		kind = attention.KindThreadStalled
	case remote.SessionAlertDone:
		return w.resolveThreadLocked(alert.ThreadID, attention.KindThreadStalled, attention.KindThreadApproval)
	default:
		return nil
	}
	// 提醒标题是「会话出错」这类状态描述，不能拿来覆盖已缓存的会话标题；
	// 没有缓存时退回到提醒正文，尽量保留能辨认会话的信息。
	state := w.threadStateLocked(alert.ThreadID, "", "")
	title := state.title
	if title == "" {
		title = alert.Body
	}
	enteredAt, ok := state.entered[kind]
	if !ok {
		enteredAt = w.nowTime()
		state.entered[kind] = enteredAt
	}
	input := attention.Input{
		Kind:        kind,
		SubjectType: attention.SubjectThread,
		SubjectID:   alert.ThreadID,
		ProjectID:   state.projectID,
		ThreadID:    alert.ThreadID,
		Title:       title,
		Detail:      alert.Body,
		EnteredAt:   enteredAt,
	}
	return []func(){func() { w.record(input) }}
}

// onHubEvent 处理会话广播，作为 hub 观察者运行在任意 goroutine：
// 只做内存判定与入队，不直接写库。
func (w *attentionWiring) onHubEvent(threadID, method string, params any) {
	// 会话列表变动只用于清理探测器里已删除会话的状态，不代表某个会话有进展。
	if method == "threads.changed" {
		w.mu.Lock()
		w.alerts.Observe(threadID, method, params)
		w.mu.Unlock()
		return
	}
	summary, isSummary := params.(wire.ThreadSummary)
	if !isSummary {
		if p, ok := params.(*wire.ThreadSummary); ok && p != nil {
			summary, isSummary = *p, true
		}
	}
	if threadID == "" && isSummary {
		threadID = summary.ID
	}
	if threadID == "" {
		return
	}
	w.mu.Lock()
	if w.isIgnoredLocked(threadID) {
		w.mu.Unlock()
		return
	}
	var jobs []func()
	if isSummary {
		jobs = append(jobs, w.trackThreadSummaryLocked(threadID, summary)...)
	}
	if alert, ok := w.alerts.Observe(threadID, method, params); ok {
		jobs = append(jobs, w.alertJobsLocked(alert)...)
	}
	w.mu.Unlock()
	for _, job := range jobs {
		w.push(job)
	}
}

// observeThreads 订阅会话广播。
func (w *attentionWiring) observeThreads() {
	if w.hub == nil {
		return
	}
	w.removeHub = w.hub.AddObserver(w.onHubEvent)
}

// checkStalled 巡检卡住会话：阈值与开关每次现读配置，改设置后无需重启。
func (w *attentionWiring) checkStalled() {
	if w.dataDir == "" {
		return
	}
	cfg, err := config.Load(w.dataDir)
	if err != nil {
		log.Printf("attention: 读取配置失败，跳过本轮卡住巡检: %v", err)
		return
	}
	if !cfg.Notifications.StalledAlertEnabled() {
		return
	}
	w.mu.Lock()
	alerts := w.alerts.CheckStalled(cfg.Notifications.StalledAlertAfter())
	var jobs []func()
	for _, alert := range alerts {
		if w.isIgnoredLocked(alert.ThreadID) {
			continue
		}
		jobs = append(jobs, w.alertJobsLocked(alert)...)
	}
	w.mu.Unlock()
	for _, job := range jobs {
		w.push(job)
	}
}

// stalledLoop 每 60 秒巡检一次卡住会话。
func (w *attentionWiring) stalledLoop() {
	defer w.wg.Done()
	ticker := time.NewTicker(attentionStalledInterval)
	defer ticker.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-ticker.C:
			w.checkStalled()
		}
	}
}

// kanbanTaskSnapshot 是看板任务的待处理事件快照，用于比较两次扫描的差异。
type kanbanTaskSnapshot struct {
	Status        string
	Title         string
	ProjectID     string
	ThreadID      string
	FailureReason string
	// UpdatedAt 是任务进入当前状态的时刻，直接用作事件 EnteredAt。
	UpdatedAt time.Time
}

// kanbanAttentionAction 是一条看板快照差异算出的写库动作。
type kanbanAttentionAction struct {
	subjectID string
	kind      attention.Kind
	resolve   bool
	snapshot  kanbanTaskSnapshot
}

// kanbanAttentionKinds 是看板任务对应的待处理事件类型。
var kanbanAttentionKinds = []attention.Kind{
	attention.KindKanbanFailed,
	attention.KindKanbanNeedsApproval,
	attention.KindKanbanReview,
}

// diffKanbanAttention 比较两次看板扫描：新进入待审批、待复核记一条事件，
// 失败原因从空变非空或内容变化记一条事件；离开对应状态或任务消失时消解。
// 纯函数便于测试，状态未变时不产生任何动作。
// 返回值里消解动作排在记录动作之前：失败原因变化时先消解旧事件再写新事件，
// 否则同一类型的新事件会被随后的消解一起清掉。
func diffKanbanAttention(prev, next map[string]kanbanTaskSnapshot) []kanbanAttentionAction {
	var resolves, records []kanbanAttentionAction
	for id, task := range next {
		before, seen := prev[id]
		statusChanged := !seen || before.Status != task.Status
		switch task.Status {
		case string(kanban.TaskStatusNeedsApproval):
			if statusChanged {
				records = append(records, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanNeedsApproval, snapshot: task})
			}
		case string(kanban.TaskStatusReview):
			if statusChanged {
				records = append(records, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanReview, snapshot: task})
			}
		}
		if task.FailureReason != "" && (!seen || before.FailureReason != task.FailureReason) {
			records = append(records, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanFailed, snapshot: task})
		}
		if !seen {
			continue
		}
		if before.Status == string(kanban.TaskStatusNeedsApproval) && statusChanged {
			resolves = append(resolves, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanNeedsApproval, resolve: true, snapshot: task})
		}
		if before.Status == string(kanban.TaskStatusReview) && statusChanged {
			resolves = append(resolves, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanReview, resolve: true, snapshot: task})
		}
		if before.FailureReason != "" && task.FailureReason != before.FailureReason {
			resolves = append(resolves, kanbanAttentionAction{subjectID: id, kind: attention.KindKanbanFailed, resolve: true, snapshot: task})
		}
	}
	// 任务被删除或不再属于当前看板：消解它留下的全部事件。
	for id, before := range prev {
		if _, ok := next[id]; ok {
			continue
		}
		for _, kind := range kanbanAttentionKinds {
			resolves = append(resolves, kanbanAttentionAction{subjectID: id, kind: kind, resolve: true, snapshot: before})
		}
	}
	return append(resolves, records...)
}

// kanbanSnapshotFromTask 把存储层任务压成快照。
func kanbanSnapshotFromTask(task store.Task) kanbanTaskSnapshot {
	snapshot := kanbanTaskSnapshot{
		Status:    task.Status,
		Title:     task.Title,
		UpdatedAt: task.UpdatedAt,
	}
	if task.ProjectID != nil {
		snapshot.ProjectID = *task.ProjectID
	}
	if task.ThreadID != nil {
		snapshot.ThreadID = *task.ThreadID
	}
	if task.LastFailureReason != nil {
		snapshot.FailureReason = *task.LastFailureReason
	}
	return snapshot
}

// kanbanActionDetail 生成看板事件的正文。
func kanbanActionDetail(action kanbanAttentionAction) string {
	if action.kind == attention.KindKanbanFailed {
		return action.snapshot.FailureReason
	}
	if action.kind == attention.KindKanbanNeedsApproval {
		return "任务等待你的审批"
	}
	return "任务已完成，等待验收"
}

// applyKanbanActions 依次执行看板差异动作。
func (w *attentionWiring) applyKanbanActions(actions []kanbanAttentionAction) {
	for _, action := range actions {
		if action.resolve {
			w.resolveSubject(attention.SubjectKanbanTask, action.subjectID, action.kind)
			continue
		}
		w.record(attention.Input{
			Kind:        action.kind,
			SubjectType: attention.SubjectKanbanTask,
			SubjectID:   action.subjectID,
			ProjectID:   action.snapshot.ProjectID,
			ThreadID:    action.snapshot.ThreadID,
			Title:       action.snapshot.Title,
			Detail:      kanbanActionDetail(action),
			EnteredAt:   action.snapshot.UpdatedAt,
		})
	}
}

// kanbanGoalSnapshot 是看板目标的待处理事件快照，用于比较两次扫描的差异。
type kanbanGoalSnapshot struct {
	Status    string
	Title     string
	ProjectID string
	// Reason 是目标的 StatusReason，规划失败时作为事件正文。
	Reason string
	// UpdatedAt 是目标进入当前状态的时刻，直接用作事件 EnteredAt。
	UpdatedAt time.Time
}

// kanbanGoalAttentionAction 是一条目标快照差异算出的写库动作。
type kanbanGoalAttentionAction struct {
	subjectID string
	kind      attention.Kind
	resolve   bool
	snapshot  kanbanGoalSnapshot
}

// kanbanGoalAttentionKinds 是看板目标对应的待处理事件类型。
var kanbanGoalAttentionKinds = []attention.Kind{attention.KindKanbanPlanFailed}

// diffKanbanGoalAttention 比较两次目标扫描：进入规划失败记一条事件，
// 离开规划失败、原因变化或目标消失时消解。纯函数便于测试，状态未变时不产生任何动作。
// 返回值里消解动作排在记录动作之前，否则原因变化时同一类型的新事件会被随后的消解一起清掉。
func diffKanbanGoalAttention(prev, next map[string]kanbanGoalSnapshot) []kanbanGoalAttentionAction {
	var resolves, records []kanbanGoalAttentionAction
	for id, goal := range next {
		before, seen := prev[id]
		failed := goal.Status == string(kanban.GoalStatusPlanFailed)
		wasFailed := seen && before.Status == string(kanban.GoalStatusPlanFailed)
		if failed && (!wasFailed || before.Reason != goal.Reason) {
			records = append(records, kanbanGoalAttentionAction{subjectID: id, kind: attention.KindKanbanPlanFailed, snapshot: goal})
		}
		if wasFailed && (!failed || before.Reason != goal.Reason) {
			resolves = append(resolves, kanbanGoalAttentionAction{subjectID: id, kind: attention.KindKanbanPlanFailed, resolve: true, snapshot: before})
		}
	}
	// 目标被删除或不再属于当前看板：消解它留下的全部事件。
	for id, before := range prev {
		if _, ok := next[id]; ok {
			continue
		}
		for _, kind := range kanbanGoalAttentionKinds {
			resolves = append(resolves, kanbanGoalAttentionAction{subjectID: id, kind: kind, resolve: true, snapshot: before})
		}
	}
	return append(resolves, records...)
}

// kanbanGoalSnapshotFromGoal 把存储层目标压成快照。
func kanbanGoalSnapshotFromGoal(goal store.Goal) kanbanGoalSnapshot {
	snapshot := kanbanGoalSnapshot{
		Status:    goal.Status,
		Title:     goal.Title,
		ProjectID: goal.ProjectID,
		UpdatedAt: goal.UpdatedAt,
	}
	if goal.StatusReason != nil {
		snapshot.Reason = *goal.StatusReason
	}
	return snapshot
}

// applyKanbanGoalActions 依次执行目标差异动作。
func (w *attentionWiring) applyKanbanGoalActions(actions []kanbanGoalAttentionAction) {
	for _, action := range actions {
		if action.resolve {
			w.resolveSubject(attention.SubjectKanbanGoal, action.subjectID, action.kind)
			continue
		}
		w.record(attention.Input{
			Kind:        action.kind,
			SubjectType: attention.SubjectKanbanGoal,
			SubjectID:   action.subjectID,
			ProjectID:   action.snapshot.ProjectID,
			Title:       action.snapshot.Title,
			Detail:      action.snapshot.Reason,
			EnteredAt:   action.snapshot.UpdatedAt,
		})
	}
}

// scanKanban 全量读取所有项目的看板任务与目标，与上次快照比较后写入差异。
func (w *attentionWiring) scanKanban() {
	if w.kanban == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), attentionScanTimeout)
	board, err := w.kanban.Board(ctx)
	cancel()
	if err != nil {
		log.Printf("attention: 读取看板失败，跳过本轮扫描: %v", err)
		return
	}
	next := make(map[string]kanbanTaskSnapshot, len(board.Tasks))
	for _, view := range board.Tasks {
		next[view.Task.ID] = kanbanSnapshotFromTask(view.Task)
	}
	nextGoals := make(map[string]kanbanGoalSnapshot, len(board.Goals))
	for _, goal := range board.Goals {
		nextGoals[goal.ID] = kanbanGoalSnapshotFromGoal(goal)
	}
	w.mu.Lock()
	actions := diffKanbanAttention(w.kanbanSnapshot, next)
	w.kanbanSnapshot = next
	goalActions := diffKanbanGoalAttention(w.kanbanGoalSnapshot, nextGoals)
	w.kanbanGoalSnapshot = nextGoals
	w.mu.Unlock()
	w.applyKanbanActions(actions)
	w.applyKanbanGoalActions(goalActions)
}

// scheduleKanbanScan 延期合并看板变更：1 秒内的多次通知只扫描一次。
func (w *attentionWiring) scheduleKanbanScan() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.kanbanTimer != nil {
		w.kanbanTimer.Stop()
	}
	w.kanbanGen++
	gen := w.kanbanGen
	w.kanbanTimer = time.AfterFunc(attentionKanbanDebounce, func() {
		w.mu.Lock()
		if gen != w.kanbanGen {
			w.mu.Unlock()
			return
		}
		w.kanbanTimer = nil
		w.mu.Unlock()
		w.scanKanban()
	})
}

// observeKanban 订阅看板变更。
func (w *attentionWiring) observeKanban() {
	if w.client == nil || w.kanban == nil {
		return
	}
	w.removeKanban = w.client.WatchKanban(w.scheduleKanbanScan)
}

// automationAttentionInput 把自动化运行结算映射成待处理事件输入：
// 只有失败记 KindAutomationFailed 并唤醒总管；成功不建事件，运行中与其余状态同样不记录。
func automationAttentionInput(def automation.Definition, run store.AutomationRun) (attention.Input, bool) {
	// 成功的运行不需要人处理，每次都唤醒总管只会打扰用户，因此不产生注意事件。
	if run.Status != automation.RunFailed {
		return attention.Input{}, false
	}
	detail := ""
	if run.Error != nil {
		detail = *run.Error
	}
	enteredAt := run.StartedAt
	if run.FinishedAt != nil && !run.FinishedAt.IsZero() {
		enteredAt = *run.FinishedAt
	}
	input := attention.Input{
		Kind:        attention.KindAutomationFailed,
		SubjectType: attention.SubjectAutomationRun,
		SubjectID:   run.ID,
		Title:       def.Name,
		Detail:      detail,
		EnteredAt:   enteredAt,
	}
	if def.ProjectID != nil {
		input.ProjectID = *def.ProjectID
	}
	if run.ThreadID != nil {
		input.ThreadID = *run.ThreadID
	}
	return input, true
}

// HandleAutomationRun 是 automation.OnRunSettled 的回调：失败写进待处理事件并唤醒总管，
// 成功不建事件，改为直接发一条桌面通知（用户没在看该会话时才发）。
// 本轮之后自动化已停用时额外发一条停用通知：连续失败达到上限、次数上限、停止条件满足与
// 一次性任务结束后都不会再有新的运行，否则用户完全不知道它已经不跑了。
func (w *attentionWiring) HandleAutomationRun(def automation.Definition, run store.AutomationRun) {
	threadID := ""
	if run.ThreadID != nil {
		threadID = *run.ThreadID
	}
	// 重新启用后清除记录，下次停用重新通知一次。
	if def.Enabled {
		w.clearAutomationStoppedNotified(def.ID)
	}
	stoppedTitle, stoppedBody, stopped := automationStoppedNotice(def, run)
	if stopped {
		if notify := w.automationNotifier(); notify != nil && w.markAutomationStoppedNotified(def.ID) {
			notify(stoppedTitle, stoppedBody, threadID)
		}
	}

	input, ok := automationAttentionInput(def, run)
	if ok {
		w.record(input)
		return
	}
	if stopped {
		// 停用通知已经说明了本轮结果，不再重复发成功通知。
		return
	}
	title, body, ok := automationSuccessNotice(def, run)
	if !ok {
		return
	}
	if notify := w.automationNotifier(); notify != nil {
		notify(title, body, threadID)
	}
}

// automationNotifier 返回当前的自动化通知实现，并发安全。
func (w *attentionWiring) automationNotifier() func(title, body, threadID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.automationNotify
}

// markAutomationStoppedNotified 记录某个自动化已发过停用通知；已经记录过时返回 false。
func (w *attentionWiring) markAutomationStoppedNotified(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.automationStoppedNotified == nil {
		w.automationStoppedNotified = make(map[string]struct{})
	}
	if _, ok := w.automationStoppedNotified[id]; ok {
		return false
	}
	w.automationStoppedNotified[id] = struct{}{}
	return true
}

// clearAutomationStoppedNotified 在自动化重新启用后清除停用通知记录。
func (w *attentionWiring) clearAutomationStoppedNotified(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.automationStoppedNotified, id)
}

// automationStoppedNotice 判定本轮之后自动化是否已经停用，并组装停用通知文本。
// 用户手动暂停也会把 Enabled 改成 false，但 Status 为 paused 且没有停止原因，不属于自动停用；
// 连续失败、次数上限、停止条件与一次性任务结束都会把 Status 改成 failed/completed。
// 正文优先用停止原因，其次用本轮错误，都没有时给通用说明。
func automationStoppedNotice(def automation.Definition, run store.AutomationRun) (title, body string, ok bool) {
	if def.Enabled {
		return "", "", false
	}
	stopped := strings.TrimSpace(def.Spec.State.StopReason) != "" ||
		def.Status == automation.StatusFailed || def.Status == automation.StatusCompleted
	if !stopped {
		return "", "", false
	}
	body = strings.TrimSpace(def.Spec.State.StopReason)
	if body == "" && run.Error != nil {
		body = strings.TrimSpace(*run.Error)
	}
	if body == "" {
		body = "已达到停止条件"
	}
	return truncateHostNotice("自动化已停止："+def.Name, hostNoticeTitleMaxRunes),
		truncateNoticeText(body, hostNoticeBodyMaxRunes), true
}

// setAutomationNotify 设置自动化完成通知的实现，并发安全；传 nil 表示未接入。
func (w *attentionWiring) setAutomationNotify(fn func(title, body, threadID string)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.automationNotify = fn
}

// automationSuccessNotice 组装自动化成功运行的通知标题与正文。
// 只有成功状态才通知：失败走待处理事件唤醒总管，运行中与其余状态不打扰用户。
// 正文优先用运行结果摘要，没有摘要时退回「运行完成」。
func automationSuccessNotice(def automation.Definition, run store.AutomationRun) (title, body string, ok bool) {
	if run.Status != automation.RunSucceeded {
		return "", "", false
	}
	body = "运行完成"
	if run.ResultSummary != nil {
		if summary := truncateNoticeText(*run.ResultSummary, automationNoticeSummaryLimit); summary != "" {
			body = summary
		}
	}
	return truncateHostNotice("自动化完成："+def.Name, hostNoticeTitleMaxRunes), body, true
}

// truncateHostNotice 按字数把文本压成一行并截断到不超过 limit，超出时用省略号占最后一位。
// 与 truncateNoticeText 的区别是截断后的长度（含省略号）也不超过 limit，
// 确保系统通知不会截掉省略号。
func truncateHostNotice(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 0 {
		return ""
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

// truncateNoticeText 把多行摘要压成一行并按字数截断，超出上限时补省略号。
// 系统通知只显示一两行，摘要里的换行和多余空白只会浪费展示空间。
func truncateNoticeText(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "…"
}

// beeFailure 描述一条 bee 失败事件。
type beeFailure struct {
	SubjectID string
	ThreadID  string
	ProjectID string
	Title     string
	Detail    string
	EnteredAt time.Time
}

// diffBeeFailures 比较同一个 lead 下两次 bee 快照：只有新进入失败状态才产出事件；
// prev 为 nil 表示首次观察，只建立基线，不补记历史失败。纯函数便于测试。
func diffBeeFailures(prev map[string]string, bees []swarm.Bee, leadThreadID string, observedAt time.Time) []beeFailure {
	if prev == nil {
		return nil
	}
	var failures []beeFailure
	for _, bee := range bees {
		if bee.Status != swarm.StatusFailed {
			continue
		}
		if status, ok := prev[bee.ThreadID]; ok && status == string(swarm.StatusFailed) {
			continue
		}
		enteredAt := observedAt
		if bee.FinishedAt != nil && !bee.FinishedAt.IsZero() {
			enteredAt = *bee.FinishedAt
		}
		failures = append(failures, beeFailure{
			SubjectID: bee.ThreadID,
			ThreadID:  leadThreadID,
			ProjectID: bee.ProjectID,
			Title:     bee.Task,
			Detail:    bee.Summary,
			EnteredAt: enteredAt,
		})
	}
	return failures
}

// beeStatusSnapshot 记录每个 bee 的当前状态。
func beeStatusSnapshot(bees []swarm.Bee) map[string]string {
	snapshot := make(map[string]string, len(bees))
	for _, bee := range bees {
		snapshot[bee.ThreadID] = string(bee.Status)
	}
	return snapshot
}

// beeMergeReview 是一条新进入待审查状态的 bee 提醒。
type beeMergeReview struct {
	SubjectID string
	ThreadID  string
	ProjectID string
	Title     string
	Detail    string
	EnteredAt time.Time
}

// beeMergeReviewPending 判定 bee 是否处于需要提醒的待审查状态：改动已暂存且不在 AI 审查中。
func beeMergeReviewPending(bee swarm.Bee) bool {
	return bee.PendingMerge && bee.ReviewState != swarm.ReviewRunning
}

// diffMergeReviewPending 比较同一个 lead 下两次 bee 快照：新进入待审查状态产出事件；
// prev 为 nil 表示首次观察（例如 MCode 重启后第一次看到该会话），此时为所有已在等待
// 人工审查的 bee 补发提醒，但不产生消解列表，避免把重启前就存在的状态当成刚离开。
// 返回新进入的提醒与已离开该状态的 bee 会话 ID，后者用于自动消解对应提醒。纯函数便于测试。
func diffMergeReviewPending(prev map[string]bool, bees []swarm.Bee, leadThreadID string, observedAt time.Time) ([]beeMergeReview, []string) {
	if prev == nil {
		// 基线按「所有 bee 之前都不在待审查」处理：既补发当前待审查的提醒，
		// 又因基线为空而不会产出消解列表。
		prev = map[string]bool{}
	}
	var pending []beeMergeReview
	var cleared []string
	seen := make(map[string]bool, len(bees))
	for _, bee := range bees {
		seen[bee.ThreadID] = true
		isPending := beeMergeReviewPending(bee)
		if isPending && !prev[bee.ThreadID] {
			detail := bee.Name + "：" + bee.Task
			if bee.ReviewReason != "" {
				detail += "；审查意见：" + bee.ReviewReason
			}
			// EnteredAt 必须取稳定时刻（bee 结束时间），不能用本次观察时刻：
			// MCode 重启后再次观察同一个 bee 时，只有稳定时刻才能算出同一个去重键，
			// attention.Service.Record 才会识别为重复，不会每次重启都新增一条提醒。
			enteredAt := observedAt
			if bee.FinishedAt != nil && !bee.FinishedAt.IsZero() {
				enteredAt = *bee.FinishedAt
			}
			pending = append(pending, beeMergeReview{
				SubjectID: bee.ThreadID,
				ThreadID:  leadThreadID,
				ProjectID: bee.ProjectID,
				Title:     "有改动待审查",
				Detail:    detail,
				EnteredAt: enteredAt,
			})
		}
		if !isPending && prev[bee.ThreadID] {
			cleared = append(cleared, bee.ThreadID)
		}
	}
	// bee 已从快照里消失（被清理或归档）也算离开待审查状态。
	for beeThreadID, wasPending := range prev {
		if wasPending && !seen[beeThreadID] {
			cleared = append(cleared, beeThreadID)
		}
	}
	return pending, cleared
}

// mergeReviewStatusSnapshot 记录每个 bee 是否处于待审查状态。
func mergeReviewStatusSnapshot(bees []swarm.Bee) map[string]bool {
	snapshot := make(map[string]bool, len(bees))
	for _, bee := range bees {
		snapshot[bee.ThreadID] = beeMergeReviewPending(bee)
	}
	return snapshot
}

// handleBeeLead 读取某个 lead 会话的 bee 状态并写入新出现的失败与待审查提醒。
func (w *attentionWiring) handleBeeLead(leadThreadID string) {
	if w.swarm == nil || leadThreadID == "" {
		return
	}
	select {
	case <-w.done:
		return
	default:
	}
	state := w.swarm.State(leadThreadID)
	observedAt := w.nowTime()
	w.mu.Lock()
	failures := diffBeeFailures(w.beeSnapshot[leadThreadID], state.Bees, leadThreadID, observedAt)
	reviews, clearedReviews := diffMergeReviewPending(w.mergeReviewSnapshot[leadThreadID], state.Bees, leadThreadID, observedAt)
	if w.beeSnapshot == nil {
		w.beeSnapshot = make(map[string]map[string]string)
	}
	w.beeSnapshot[leadThreadID] = beeStatusSnapshot(state.Bees)
	if w.mergeReviewSnapshot == nil {
		w.mergeReviewSnapshot = make(map[string]map[string]bool)
	}
	w.mergeReviewSnapshot[leadThreadID] = mergeReviewStatusSnapshot(state.Bees)
	w.mu.Unlock()
	for _, failure := range failures {
		w.record(attention.Input{
			Kind:        attention.KindBeeFailed,
			SubjectType: attention.SubjectBee,
			SubjectID:   failure.SubjectID,
			ProjectID:   failure.ProjectID,
			ThreadID:    failure.ThreadID,
			Title:       failure.Title,
			Detail:      failure.Detail,
			EnteredAt:   failure.EnteredAt,
		})
	}
	for _, review := range reviews {
		w.record(attention.Input{
			Kind:        attention.KindMergeReviewPending,
			SubjectType: attention.SubjectBee,
			SubjectID:   review.SubjectID,
			ProjectID:   review.ProjectID,
			ThreadID:    review.ThreadID,
			Title:       review.Title,
			Detail:      review.Detail,
			EnteredAt:   review.EnteredAt,
		})
	}
	for _, beeThreadID := range clearedReviews {
		w.resolveSubject(attention.SubjectBee, beeThreadID, attention.KindMergeReviewPending)
	}
}

// observeSwarm 订阅 bee 状态变更。
func (w *attentionWiring) observeSwarm() {
	if w.client == nil || w.swarm == nil {
		return
	}
	w.removeSwarm = w.client.WatchSwarm(func(leadThreadID string) {
		go w.handleBeeLead(leadThreadID)
	})
}
