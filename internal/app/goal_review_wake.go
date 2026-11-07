package app

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"mcode/internal/config"
	"mcode/internal/kanban"
	"mcode/internal/steward"
)

const (
	// goalOwnerSteward 是总管创建目标时写入 Goal.Owner 的标记值，与 kanban.CreateGoalOptions 的约定一致。
	goalOwnerSteward = "steward"
	// goalWakeDebounce 是看板变更的合并窗口，与 attention 接线保持一致。
	goalWakeDebounce = time.Second
	// goalWakeMinGap 是同一个目标两次唤醒之间的最小间隔，窗口内的事件合并成一条。
	goalWakeMinGap = 30 * time.Second
	// goalWakeFlushInterval 是待发唤醒的检查间隔。
	goalWakeFlushInterval = time.Second
	// goalWakeCommandTimeout 是一次验收命令执行的超时时间，比 kanban 的 15 分钟总预算更宽。
	goalWakeCommandTimeout = 20 * time.Minute
	// goalDailyReportInterval 是日报检查间隔。
	goalDailyReportInterval = time.Minute
	// autoCloseTopicsInterval 是话题自动结项的扫描间隔：回执满 72 小时的话题每小时扫一次就够。
	autoCloseTopicsInterval = time.Hour
	// settingGoalWakePrefix 是任务级唤醒去重键的前缀，值为「状态@Revision」。
	settingGoalWakePrefix = "steward.goal_wake."
	// settingGoalWakeGoalPrefix 是目标级唤醒去重键的前缀（暂停等目标状态变化）。
	settingGoalWakeGoalPrefix = "steward.goal_wake_goal."
	// settingGoalWakeDonePrefix 是「目标任务全部完成」唤醒的去重键前缀。
	settingGoalWakeDonePrefix = "steward.goal_wake_done."
	// settingGoalWakePlanPrefix 是「计划待审批」唤醒的去重键前缀。
	settingGoalWakePlanPrefix = "steward.goal_wake_plan."
	// settingDailyReportDay 保存最近一次发出日报的本地日期（YYYY-MM-DD）。
	settingDailyReportDay = "steward.daily_report_day"
	// dailyReportPrompt 是日报唤醒的固定正文；%s 是总管会话自身的 ID，用来让摘要通知点开就能回到会话。
	dailyReportPrompt = "现在是日报时间。请用 attention_list、kanban_goal_status、thread_status 汇总今天：推进了什么、卡在哪里、明天建议做什么，写成一份简短的今日要点。写完今日要点后，用 notify_owner 发一条简短通知，thread_id 传 %s：标题写「今日要点」，正文用一两句话说清最重要的一件事，并说明全文在会话里；如果今天没什么事，就不要发通知。"
	// dailyReportDateLayout 是日报日期键的格式，按本地时间取日期。
	dailyReportDateLayout = "2006-01-02"
)

// goalWakeKind 是唤醒总管的事件类型。
type goalWakeKind string

const (
	// goalWakeReview 表示任务新进入验收，需要总管按审批规则处理。
	goalWakeReview goalWakeKind = "review"
	// goalWakeRejected 表示任务被打回后重试耗尽。
	goalWakeRejected goalWakeKind = "rejected"
	// goalWakeNeedsApproval 表示任务在等审批。
	goalWakeNeedsApproval goalWakeKind = "needs_approval"
	// goalWakePlanReview 表示目标计划已出，等总管自审并拆分。
	goalWakePlanReview goalWakeKind = "plan_review"
	// goalWakeGoalPaused 表示目标被暂停。
	goalWakeGoalPaused goalWakeKind = "goal_paused"
	// goalWakeGoalDone 表示目标任务全部完成或目标已完成，请总管写结项报告。
	goalWakeGoalDone goalWakeKind = "goal_done"
)

// goalTaskSnapshot 是唤醒判定需要的任务字段快照。
type goalTaskSnapshot struct {
	GoalID      string
	Title       string
	Status      string
	Revision    int
	RejectCount int
	Acceptance  string
}

// goalSnapshot 是唤醒判定需要的目标字段快照：状态、Revision 与任务完成进度。
type goalSnapshot struct {
	Status   string
	Revision int
	Total    int
	Done     int
}

// goalWakeTarget 是需要唤醒的目标会话：只有总管创建且已有规划会话的目标才在表里。
type goalWakeTarget struct {
	ThreadID string
	Title    string
}

// goalWakeAction 是一条需要投递给总管的唤醒事件。
type goalWakeAction struct {
	Kind      goalWakeKind
	GoalID    string
	ThreadID  string
	GoalTitle string
	TaskID    string
	Task      goalTaskSnapshot
	// SettingKey 与 SettingValue 组成去重记录：同一任务（或目标）同一状态同一 Revision 只发一次。
	SettingKey   string
	SettingValue string
}

// hostEventSender 把总管唤醒投递到会话；*remote.MemoryBackend 实现该接口，测试可注入假实现。
type hostEventSender interface {
	SendHostEvent(ctx context.Context, threadID, text string) error
	SendStewardWake(ctx context.Context, threadID, text string) error
}

// wakeStateStore 是唤醒去重与日报日期需要的设置读写；*store.Store 实现该接口。
type wakeStateStore interface {
	GetSetting(ctx context.Context, key string) (string, bool, error)
	SetSetting(ctx context.Context, key, value string) error
}

// dailyReportSettings 是日报判定需要的配置快照，独立出来方便测试注入时钟与开关。
type dailyReportSettings struct {
	Enabled bool
	Hour    int
	Minute  int
	Quiet   bool
}

// goalReviewWakeOptions 是唤醒器的构造参数；Store、Kanban、Host 缺失时唤醒器不启动。
type goalReviewWakeOptions struct {
	Store  wakeStateStore
	Kanban *kanban.Service
	// Client 提供 WatchKanban 订阅入口，为空时只靠启动时的一次扫描。
	Client *Client
	// Host 负责把事件投递到总管的话题会话。
	Host hostEventSender
	// DataDir 是配置目录，日报开关每次从这里现读。
	DataDir string
	// Now 提供当前时间，测试可注入；为空时使用 time.Now。
	Now func() time.Time
	// StewardThreadID 返回总管主会话 ID，为空表示总管会话还没就绪。
	StewardThreadID func() string
	// ReceiptTopic 在话题的目标完成或取消时把结论回执进总管账本；为空表示不做回执。
	ReceiptTopic func(ctx context.Context, threadID string) (bool, error)
	// AutoCloseTopics 把已回执且超过阈值的话题自动结项；为空表示不自动结项。
	AutoCloseTopics func(ctx context.Context) (int, error)
	// ReportSettings 读取日报配置，为空时从 DataDir 现读。
	ReportSettings func(now time.Time) dailyReportSettings
}

// goalReviewWaker 订阅看板变化，在总管创建的目标里出现待验收、被打回、待审批、
// 计划待审批、暂停或全部完成时，向该目标的话题会话投递一条宿主事件，让总管接着处理。
// 事件按「任务（或目标）+ 状态 + Revision」去重并落库，重启不会重发；
// 同一目标 30 秒内的事件合并成一条，避免连续移动卡片时反复打断总管。
type goalReviewWaker struct {
	store          wakeStateStore
	kanban         *kanban.Service
	client         *Client
	host           hostEventSender
	dataDir        string
	now            func() time.Time
	stewardThread  func() string
	receipt        func(ctx context.Context, threadID string) (bool, error)
	autoClose      func(ctx context.Context) (int, error)
	reportSettings func(now time.Time) dailyReportSettings

	mu sync.Mutex
	// taskSnapshot 与 goalSnapshot 是上一次扫描的快照，首次为空时按基线处理。
	taskSnapshot map[string]goalTaskSnapshot
	goalSnapshot map[string]goalSnapshot
	// sent 是内存里的去重记录，命中后不必再查库；重启后由设置键兜底。
	sent map[string]string
	// pending 是按目标累积的待发事件，pendingThread 记住投递目标。
	pending       map[string][]goalWakeItem
	pendingThread map[string]string
	// lastWake 记录每个目标上次唤醒的时刻，用于 30 秒合并窗口。
	lastWake map[string]time.Time
	// reportDay 是内存里的日报日期，避免每分钟重复查库。
	reportDay string
	// autoCloseAt 是上次话题自动结项的时刻，用来把频率限制在每小时一次。
	autoCloseAt time.Time
	// scanTimer 是看板扫描的防抖定时器，scanGen 用于丢弃过期回调。
	scanTimer *time.Timer
	scanGen   int

	removeKanban func()
	done         chan struct{}
	wg           sync.WaitGroup
	startOnce    sync.Once
	stopOnce     sync.Once
}

// goalWakeItem 是一条已组装好的待发事件。
type goalWakeItem struct {
	settingKey   string
	settingValue string
	text         string
}

// newGoalReviewWaker 构造唤醒器，不启动任何订阅。
func newGoalReviewWaker(opts goalReviewWakeOptions) *goalReviewWaker {
	w := &goalReviewWaker{
		store:          opts.Store,
		kanban:         opts.Kanban,
		client:         opts.Client,
		host:           opts.Host,
		dataDir:        opts.DataDir,
		now:            opts.Now,
		stewardThread:  opts.StewardThreadID,
		receipt:        opts.ReceiptTopic,
		autoClose:      opts.AutoCloseTopics,
		reportSettings: opts.ReportSettings,
		taskSnapshot:   make(map[string]goalTaskSnapshot),
		goalSnapshot:   make(map[string]goalSnapshot),
		sent:           make(map[string]string),
		pending:        make(map[string][]goalWakeItem),
		pendingThread:  make(map[string]string),
		lastWake:       make(map[string]time.Time),
		done:           make(chan struct{}),
	}
	if w.now == nil {
		w.now = time.Now
	}
	if w.reportSettings == nil {
		w.reportSettings = w.loadDailyReportSettings
	}
	return w
}

// wireGoalReviewWake 在总管与看板都可用时接线验收唤醒并启动；任一缺失返回 nil。
// 唤醒走 backend 的隐藏入口（SendStewardWake），总管会话 ID 取运行时的话题会话。
func wireGoalReviewWake(a *App, stewardRT *steward.Runtime) *goalReviewWaker {
	if a == nil || stewardRT == nil || a.kanban == nil || a.store == nil || a.backend == nil {
		return nil
	}
	w := newGoalReviewWaker(goalReviewWakeOptions{
		Store:           a.store,
		Kanban:          a.kanban,
		Client:          a.client,
		Host:            a.backend,
		DataDir:         a.dataDir,
		StewardThreadID: stewardRT.ThreadID,
		ReceiptTopic:    stewardRT.ReceiptTopic,
		// 目标完成或取消后回执，回执满 DefaultAutoCloseAfter 的话题由每小时一次的扫描自动结项。
		AutoCloseTopics: func(ctx context.Context) (int, error) {
			return stewardRT.AutoCloseReceipted(ctx, steward.DefaultAutoCloseAfter)
		},
	})
	w.Start()
	return w
}

// Start 启动订阅与后台 goroutine。可重复调用，只有第一次生效。
func (w *goalReviewWaker) Start() {
	if w == nil || w.store == nil || w.kanban == nil || w.host == nil {
		return
	}
	w.startOnce.Do(func() {
		w.wg.Add(1)
		go w.loop()
		if w.client != nil {
			w.removeKanban = w.client.WatchKanban(w.scheduleScan)
		}
		// 启动即扫描一次：错过关闭期间进入验收的任务在重启后仍会唤醒一次。
		go w.scan()
	})
}

// Stop 停止订阅与后台 goroutine。
func (w *goalReviewWaker) Stop() {
	if w == nil {
		return
	}
	w.stopOnce.Do(func() {
		close(w.done)
		w.mu.Lock()
		if w.scanTimer != nil {
			w.scanTimer.Stop()
			w.scanTimer = nil
		}
		remove := w.removeKanban
		w.removeKanban = nil
		w.mu.Unlock()
		if remove != nil {
			remove()
		}
		w.wg.Wait()
	})
}

// scheduleScan 延期合并看板变更：1 秒内的多次通知只扫描一次。
func (w *goalReviewWaker) scheduleScan() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.scanTimer != nil {
		w.scanTimer.Stop()
	}
	w.scanGen++
	gen := w.scanGen
	w.scanTimer = time.AfterFunc(goalWakeDebounce, func() {
		w.mu.Lock()
		if gen != w.scanGen {
			w.mu.Unlock()
			return
		}
		w.scanTimer = nil
		w.mu.Unlock()
		w.scan()
	})
}

// scan 读一次看板，算出需要唤醒总管的事件并投递。
func (w *goalReviewWaker) scan() {
	ctx, cancel := context.WithTimeout(context.Background(), attentionScanTimeout)
	defer cancel()
	board, err := w.kanban.Board(ctx)
	if err != nil {
		log.Printf("steward wake: 读取看板失败，跳过本轮唤醒: %v", err)
		return
	}
	targets := make(map[string]goalWakeTarget)
	goals := make(map[string]goalSnapshot)
	for _, goal := range board.Goals {
		if goal.Owner != goalOwnerSteward || goal.PlanThreadID == nil {
			continue
		}
		threadID := strings.TrimSpace(*goal.PlanThreadID)
		if threadID == "" {
			continue
		}
		targets[goal.ID] = goalWakeTarget{ThreadID: threadID, Title: goal.Title}
		goals[goal.ID] = goalSnapshot{Status: goal.Status, Revision: goal.Revision}
	}
	tasks := make(map[string]goalTaskSnapshot)
	for _, view := range board.Tasks {
		task := view.Task
		if task.GoalID == nil {
			continue
		}
		snap, ok := goals[*task.GoalID]
		if !ok {
			continue
		}
		tasks[task.ID] = goalTaskSnapshot{
			GoalID:      *task.GoalID,
			Title:       task.Title,
			Status:      task.Status,
			Revision:    task.Revision,
			RejectCount: task.RejectCount,
			Acceptance:  task.AcceptanceCriteria,
		}
		snap.Total++
		if task.Status == string(kanban.TaskStatusDone) {
			snap.Done++
		}
		goals[*task.GoalID] = snap
	}
	w.mu.Lock()
	actions := diffGoalWake(w.taskSnapshot, tasks, w.goalSnapshot, goals, targets)
	receipts := diffGoalReceipt(w.goalSnapshot, goals, targets)
	w.taskSnapshot, w.goalSnapshot = tasks, goals
	w.mu.Unlock()
	for _, act := range actions {
		if !w.claim(act.SettingKey, act.SettingValue) {
			continue
		}
		// 验收命令可能跑十几分钟，组装正文与投递都放在独立 goroutine 里。
		go w.wake(act)
	}
	for _, threadID := range receipts {
		// 回执要读话题表与看板，同样不放在扫描循环里做。
		go w.receiptTopic(threadID)
	}
	w.maybeAutoCloseTopics()
}

// diffGoalWake 比较两次扫描，算出需要唤醒总管的动作：任务新进入 review、needs_approval、
// rejected，目标进入 plan_review（计划待审批）、变为 paused，以及目标的任务全部完成（或目标已完成）。
// 纯函数便于测试；状态未变化时不产生动作，避免同一张卡片反复唤醒。
func diffGoalWake(prevTasks, nextTasks map[string]goalTaskSnapshot, prevGoals, nextGoals map[string]goalSnapshot, targets map[string]goalWakeTarget) []goalWakeAction {
	var actions []goalWakeAction
	for id, task := range nextTasks {
		target, ok := targets[task.GoalID]
		if !ok {
			continue
		}
		before, seen := prevTasks[id]
		if seen && before.Status == task.Status {
			continue
		}
		var kind goalWakeKind
		switch task.Status {
		case string(kanban.TaskStatusReview):
			kind = goalWakeReview
		case string(kanban.TaskStatusNeedsApproval):
			kind = goalWakeNeedsApproval
		case string(kanban.TaskStatusRejected):
			kind = goalWakeRejected
		default:
			continue
		}
		actions = append(actions, goalWakeAction{
			Kind:         kind,
			GoalID:       task.GoalID,
			ThreadID:     target.ThreadID,
			GoalTitle:    target.Title,
			TaskID:       id,
			Task:         task,
			SettingKey:   settingGoalWakePrefix + id,
			SettingValue: fmt.Sprintf("%s@%d", task.Status, task.Revision),
		})
	}
	for id, goal := range nextGoals {
		target, ok := targets[id]
		if !ok {
			continue
		}
		before, seen := prevGoals[id]
		if goal.Status == string(kanban.GoalStatusPlanReview) && (!seen || before.Status != goal.Status) {
			actions = append(actions, goalWakeAction{
				Kind:         goalWakePlanReview,
				GoalID:       id,
				ThreadID:     target.ThreadID,
				GoalTitle:    target.Title,
				SettingKey:   settingGoalWakePlanPrefix + id,
				SettingValue: fmt.Sprintf("plan_review@%d", goal.Revision),
			})
		}
		if goal.Status == string(kanban.GoalStatusPaused) && (!seen || before.Status != goal.Status) {
			actions = append(actions, goalWakeAction{
				Kind:         goalWakeGoalPaused,
				GoalID:       id,
				ThreadID:     target.ThreadID,
				GoalTitle:    target.Title,
				SettingKey:   settingGoalWakeGoalPrefix + id,
				SettingValue: fmt.Sprintf("%s@%d", goal.Status, goal.Revision),
			})
		}
		if goal.Status == string(kanban.GoalStatusCompleted) && (!seen || before.Status != goal.Status) {
			actions = append(actions, goalWakeAction{
				Kind:         goalWakeGoalDone,
				GoalID:       id,
				ThreadID:     target.ThreadID,
				GoalTitle:    target.Title,
				SettingKey:   settingGoalWakeDonePrefix + id,
				SettingValue: fmt.Sprintf("completed@%d", goal.Revision),
			})
			continue
		}
		if goal.Total > 0 && goal.Done == goal.Total && (!seen || before.Done != goal.Done || before.Total != goal.Total) {
			actions = append(actions, goalWakeAction{
				Kind:         goalWakeGoalDone,
				GoalID:       id,
				ThreadID:     target.ThreadID,
				GoalTitle:    target.Title,
				SettingKey:   settingGoalWakeDonePrefix + id,
				SettingValue: fmt.Sprintf("all_done:%d/%d", goal.Done, goal.Total),
			})
		}
	}
	return actions
}

// diffGoalReceipt 算出本轮需要回执进账本的话题会话：目标是总管创建的、已有规划会话，
// 且状态刚从别的值变为 completed 或 cancelled。回执本身按话题上的 receiptAt 去重，
// 这里只避免每次扫描都白跑一趟；重启后首轮快照为空，已完成的话题会再试一次，被 receiptAt 挡住。
func diffGoalReceipt(prev, next map[string]goalSnapshot, targets map[string]goalWakeTarget) []string {
	var threadIDs []string
	for id, goal := range next {
		if goal.Status != string(kanban.GoalStatusCompleted) && goal.Status != string(kanban.GoalStatusCancelled) {
			continue
		}
		if before, seen := prev[id]; seen && before.Status == goal.Status {
			continue
		}
		target, ok := targets[id]
		if !ok || strings.TrimSpace(target.ThreadID) == "" {
			continue
		}
		threadIDs = append(threadIDs, target.ThreadID)
	}
	return threadIDs
}

// receiptTopic 把话题结论回执进总管账本；失败只记日志，下一轮扫描或结项时还会再试。
func (w *goalReviewWaker) receiptTopic(threadID string) {
	if w.receipt == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), attentionWriteTimeout)
	defer cancel()
	if _, err := w.receipt(ctx, threadID); err != nil {
		log.Printf("steward wake: 话题 %s 回执失败: %v", threadID, err)
	}
}

// maybeAutoCloseTopics 每小时最多触发一次话题自动结项：在 waker 上记录上次执行时间，
// 时间取 waker 的时钟来源，时钟未注入时退回 time.Now。
func (w *goalReviewWaker) maybeAutoCloseTopics() {
	if w == nil || w.autoClose == nil {
		return
	}
	now := w.nowTime()
	w.mu.Lock()
	if !w.autoCloseAt.IsZero() && now.Sub(w.autoCloseAt) < autoCloseTopicsInterval {
		w.mu.Unlock()
		return
	}
	w.autoCloseAt = now
	w.mu.Unlock()
	// 自动结项要读话题表并整表写回，和回执一样放到后台做，不拖慢本轮扫描。
	go w.closeReceiptedTopics()
}

// closeReceiptedTopics 在后台执行一次话题自动结项；失败只记日志，下一小时再试。
func (w *goalReviewWaker) closeReceiptedTopics() {
	ctx, cancel := context.WithTimeout(context.Background(), attentionWriteTimeout)
	defer cancel()
	if _, err := w.autoClose(ctx); err != nil {
		log.Printf("steward wake: 话题自动结项失败: %v", err)
	}
}

// claim 判断这条唤醒是否还需要发送：同一「任务（或目标）+ 状态 + Revision」只发一次。
// 内存命中直接返回；未命中时查设置键，重启后据此不重复发。读库失败时仍然发送。
func (w *goalReviewWaker) claim(key, value string) bool {
	w.mu.Lock()
	seen, ok := w.sent[key]
	w.mu.Unlock()
	if ok && seen == value {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), attentionWriteTimeout)
	defer cancel()
	if stored, has, err := w.store.GetSetting(ctx, key); err != nil {
		log.Printf("steward wake: 读取唤醒去重键 %s 失败: %v", key, err)
	} else if has && stored == value {
		w.mu.Lock()
		w.sent[key] = value
		w.mu.Unlock()
		return false
	}
	w.mu.Lock()
	w.sent[key] = value
	w.mu.Unlock()
	return true
}

// wake 组装一条唤醒正文并放进待发队列；验收事件先在工作树里跑一遍 [命令]。
func (w *goalReviewWaker) wake(act goalWakeAction) {
	text := buildGoalWakeText(act)
	if act.Kind == goalWakeReview {
		ctx, cancel := context.WithTimeout(context.Background(), goalWakeCommandTimeout)
		results, err := w.kanban.RunAcceptanceCommands(ctx, act.TaskID)
		cancel()
		text = buildGoalReviewWakeText(act, results, err)
	}
	w.enqueue(act.GoalID, act.ThreadID, goalWakeItem{
		settingKey:   act.SettingKey,
		settingValue: act.SettingValue,
		text:         text,
	})
}

// enqueue 把事件放进目标队列：距上次唤醒不足 30 秒时先攒着，到点后合并成一条。
func (w *goalReviewWaker) enqueue(goalID, threadID string, item goalWakeItem) {
	w.mu.Lock()
	w.pending[goalID] = append(w.pending[goalID], item)
	w.pendingThread[goalID] = threadID
	w.mu.Unlock()
	w.flushDue(w.nowTime())
}

// flushDue 发出所有已过合并窗口的待发事件，同一个目标的多条事件合并成一条宿主事件。
func (w *goalReviewWaker) flushDue(now time.Time) {
	type batch struct {
		goalID   string
		threadID string
		items    []goalWakeItem
	}
	var ready []batch
	w.mu.Lock()
	for goalID, items := range w.pending {
		if len(items) == 0 {
			continue
		}
		if last, ok := w.lastWake[goalID]; ok && now.Sub(last) < goalWakeMinGap {
			continue
		}
		ready = append(ready, batch{goalID: goalID, threadID: w.pendingThread[goalID], items: items})
		delete(w.pending, goalID)
		w.lastWake[goalID] = now
	}
	w.mu.Unlock()
	for _, item := range ready {
		w.deliver(item.goalID, item.threadID, item.items)
	}
}

// deliver 把合并后的事件投递给总管；投递成功后把去重记录写进设置键。
// 投递失败只记日志并保留内存去重，避免同一事件每秒重试打扰总管。
func (w *goalReviewWaker) deliver(goalID, threadID string, items []goalWakeItem) {
	if threadID == "" {
		return
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		texts = append(texts, item.text)
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), attentionWriteTimeout)
	err := w.host.SendStewardWake(sendCtx, threadID, strings.Join(texts, "\n\n"))
	cancel()
	if err != nil {
		log.Printf("steward wake: 向目标 %s 的会话投递事件失败: %v", goalID, err)
		return
	}
	writeCtx, cancel := context.WithTimeout(context.Background(), attentionWriteTimeout)
	defer cancel()
	for _, item := range items {
		if item.settingKey == "" {
			continue
		}
		if err := w.store.SetSetting(writeCtx, item.settingKey, item.settingValue); err != nil {
			log.Printf("steward wake: 记录唤醒去重键 %s 失败: %v", item.settingKey, err)
		}
	}
}

// loop 每秒检查待发事件，每分钟检查一次日报；进入循环前先检查一次日报，
// 否则启动后要等一个 ticker 周期才会发。
func (w *goalReviewWaker) loop() {
	defer w.wg.Done()
	w.checkDailyReport(w.nowTime())
	flush := time.NewTicker(goalWakeFlushInterval)
	defer flush.Stop()
	report := time.NewTicker(goalDailyReportInterval)
	defer report.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-flush.C:
			w.flushDue(w.nowTime())
		case <-report.C:
			w.checkDailyReport(w.nowTime())
		}
	}
}

// checkDailyReport 在配置的日报时刻之后、非安静时段内发一次日报，同一天只发一次。
// 安静时段内直接返回，安静时段结束后的下一次检查再发；总管会话还没就绪时同样留到下次。
func (w *goalReviewWaker) checkDailyReport(now time.Time) {
	settings := w.reportSettings(now)
	if !settings.Enabled {
		return
	}
	if now.Hour() < settings.Hour || (now.Hour() == settings.Hour && now.Minute() < settings.Minute) {
		return
	}
	if settings.Quiet {
		return
	}
	day := now.Format(dailyReportDateLayout)
	w.mu.Lock()
	sent := w.reportDay == day
	w.mu.Unlock()
	if sent {
		return
	}
	readCtx, cancelRead := context.WithTimeout(context.Background(), attentionWriteTimeout)
	stored, has, err := w.store.GetSetting(readCtx, settingDailyReportDay)
	cancelRead()
	if err == nil && has && stored == day {
		w.mu.Lock()
		w.reportDay = day
		w.mu.Unlock()
		return
	}
	threadID := ""
	if w.stewardThread != nil {
		threadID = strings.TrimSpace(w.stewardThread())
	}
	if threadID == "" {
		return
	}
	sendCtx, cancelSend := context.WithTimeout(context.Background(), attentionWriteTimeout)
	err = w.host.SendStewardWake(sendCtx, threadID, fmt.Sprintf(dailyReportPrompt, threadID))
	cancelSend()
	if err != nil {
		log.Printf("steward wake: 发送日报失败: %v", err)
		return
	}
	w.mu.Lock()
	w.reportDay = day
	w.mu.Unlock()
	writeCtx, cancelWrite := context.WithTimeout(context.Background(), attentionWriteTimeout)
	if err := w.store.SetSetting(writeCtx, settingDailyReportDay, day); err != nil {
		log.Printf("steward wake: 记录日报日期失败: %v", err)
	}
	cancelWrite()
}

// loadDailyReportSettings 从数据目录现读日报配置，改设置后无需重启。
func (w *goalReviewWaker) loadDailyReportSettings(now time.Time) dailyReportSettings {
	if w.dataDir == "" {
		return dailyReportSettings{}
	}
	cfg, err := config.Load(w.dataDir)
	if err != nil {
		log.Printf("steward wake: 读取配置失败，跳过日报检查: %v", err)
		return dailyReportSettings{}
	}
	hour, minute := cfg.Orchestration.Steward.DailyReportClock()
	inQuiet, _ := cfg.Orchestration.Steward.QuietWindow(now)
	return dailyReportSettings{
		Enabled: cfg.Orchestration.Steward.DailyReport,
		Hour:    hour,
		Minute:  minute,
		Quiet:   inQuiet,
	}
}

// nowTime 返回当前时间，未注入时钟时回退到 time.Now。
func (w *goalReviewWaker) nowTime() time.Time {
	if w.now != nil {
		return w.now()
	}
	return time.Now()
}

// buildGoalWakeText 组装除验收事件以外的唤醒正文：被打回、待审批、目标暂停、计划待审批与结项报告。
func buildGoalWakeText(act goalWakeAction) string {
	switch act.Kind {
	case goalWakePlanReview:
		return fmt.Sprintf("目标「%s」（ID: %s）的计划已出，等你自审。\n\n请按当前阶段指令自审并拆分任务：自审合格就直接调用 steward_start_goal 开工，并在 reason 里写清依据；拿不准就用 notify_owner 问用户。\n",
			act.GoalTitle, act.GoalID)
	case goalWakeRejected:
		return fmt.Sprintf("任务「%s」（ID: %s）被打回后重试已耗尽，状态为 rejected。\n打回次数：%d\n\n请用 kanban_task_diff 查看改动与打回理由，判断是放宽验收标准、拆分任务，还是用 notify_owner 问用户下一步。\n",
			act.Task.Title, act.TaskID, act.Task.RejectCount)
	case goalWakeNeedsApproval:
		return fmt.Sprintf("任务「%s」（ID: %s）在等你审批。\n\n请用 kanban_task_diff 审阅后按审批规则处理：验收标准含 [你验] 或需要扩大范围时，必须先用 notify_owner 问用户。\n",
			act.Task.Title, act.TaskID)
	case goalWakeGoalPaused:
		return fmt.Sprintf("目标「%s」（ID: %s）已暂停。\n\n请用 kanban_goal_status 查看进度，确认暂停原因；需要继续时用 notify_owner 问用户。\n",
			act.GoalTitle, act.GoalID)
	case goalWakeGoalDone:
		notifyHint := "写完后用 notify_owner 发一条一句话的摘要通知：标题写「" + act.GoalTitle + "」，正文写结论，详情留在会话里。"
		if act.ThreadID != "" {
			notifyHint = "写完后用 notify_owner 发一条一句话的摘要通知：标题写「" + act.GoalTitle + "」，正文写结论，thread_id 传 " + act.ThreadID + "，详情留在会话里。"
		}
		return fmt.Sprintf("目标「%s」（ID: %s）的任务已全部完成。\n\n请写结项报告：用 kanban_goal_status 汇总结果，写清做成了什么、有哪些遗留与风险；%s\n",
			act.GoalTitle, act.GoalID, notifyHint)
	}
	return fmt.Sprintf("目标「%s」（ID: %s）有新的进展，请用 kanban_goal_status 查看。\n", act.GoalTitle, act.GoalID)
}

// buildGoalReviewWakeText 组装「任务进入验收」的唤醒正文：任务标题与 ID、验收标准原文、
// 每条 [命令] 的执行结果（命令、退出码、是否超时、输出末尾）、打回次数与下一步指令。
func buildGoalReviewWakeText(act goalWakeAction, results []kanban.AcceptanceCommandResult, runErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "任务「%s」（ID: %s）进入验收。\n", act.Task.Title, act.TaskID)
	b.WriteString("\n验收标准原文：\n")
	if criteria := strings.TrimSpace(act.Task.Acceptance); criteria == "" {
		b.WriteString("（未填写验收标准）\n")
	} else {
		b.WriteString(criteria + "\n")
	}
	b.WriteString("\n[命令] 执行结果：\n")
	switch {
	case runErr != nil:
		fmt.Fprintf(&b, "命令没有执行成功：%v\n", runErr)
	case len(results) == 0:
		b.WriteString("验收标准里没有 [命令]，无需执行。\n")
	default:
		for i, result := range results {
			fmt.Fprintf(&b, "%d. %s\n", i+1, result.Command)
			fmt.Fprintf(&b, "   退出码：%s%s\n", acceptanceExitCodeText(result.ExitCode), acceptanceTimeoutText(result.TimedOut))
			if result.Err != "" {
				fmt.Fprintf(&b, "   错误：%s\n", result.Err)
			}
			fmt.Fprintf(&b, "   用时：%s\n", result.Duration.Round(time.Millisecond))
			output := strings.TrimRight(result.Output, "\n")
			if output == "" {
				b.WriteString("   输出：（空）\n")
				continue
			}
			b.WriteString("   输出末尾：\n")
			for _, line := range strings.Split(output, "\n") {
				b.WriteString("   | " + line + "\n")
			}
		}
	}
	fmt.Fprintf(&b, "\n打回次数：%d\n", act.Task.RejectCount)
	b.WriteString("\n请用 kanban_task_diff 审阅后按审批规则处理。\n")
	return b.String()
}

// acceptanceExitCodeText 把退出码转成正文文本：负值表示没有退出码（启动失败或超时）。
func acceptanceExitCodeText(code int) string {
	if code < 0 {
		return "无（启动失败或超时）"
	}
	return fmt.Sprintf("%d", code)
}

// acceptanceTimeoutText 在命令超时时补一句说明。
func acceptanceTimeoutText(timedOut bool) string {
	if timedOut {
		return "（已超时）"
	}
	return ""
}
