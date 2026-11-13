package remote

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"mcode/internal/remote/wire"
)

// SessionAlertKind 表示会话提醒的类别。
type SessionAlertKind string

const (
	// SessionAlertApproval 表示等待用户审批的提醒。
	SessionAlertApproval SessionAlertKind = "approval"
	// SessionAlertDone 表示会话执行结束的提醒。
	SessionAlertDone SessionAlertKind = "done"
	// SessionAlertFailed 表示会话因错误结束的提醒。
	SessionAlertFailed SessionAlertKind = "failed"
	// SessionAlertStalled 表示会话长时间没有新进展的提醒。
	SessionAlertStalled SessionAlertKind = "stalled"
	// SessionAlertQuota 表示会话因额度用尽或限流结束的提醒。
	SessionAlertQuota SessionAlertKind = "quota"
	// SessionAlertBackgroundLinger 表示回合已结束、但后台任务长时间仍在运行的提醒。
	SessionAlertBackgroundLinger SessionAlertKind = "background-linger"
)

const (
	// stalledCheckInterval 是卡住巡检的触发间隔。
	stalledCheckInterval = 30 * time.Second
	// stalledAlertWindow 是卡住提醒滚动限额的时间窗口。
	stalledAlertWindow = 24 * time.Hour
	// maxStalledAlertsPerWindow 是同一会话在窗口内最多产出的卡住提醒次数。
	maxStalledAlertsPerWindow = 3
	// backgroundLingerAlertAfter 是「只剩后台任务」持续多久后提醒一次的阈值。
	// dev server 这类一直不结束的后台任务，或结束帧丢失导致计数粘住时，至少提醒一次。
	backgroundLingerAlertAfter = 10 * time.Minute
)

// SessionAlert 封装会话事件中提取出的提醒信息。
type SessionAlert struct {
	Kind     SessionAlertKind
	ThreadID string
	Title    string
	Body     string
	Tag      string
}

// SessionAlertDetector 负责对会话流式事件进行有状态判定，检测审批与完成提醒。
// lastBusy 记录上一次观察到的有效忙态（IsRunning 或 SwarmBusy），
// 只有它由 true 变 false 才算这一轮真正结束；lastRunning 供 RunningCount 统计运行中会话数。
// lastActivity 记录每个会话最后一次事件的时间，lastSummary 保存最近一次摘要，
// 两者配合 CheckStalled 判定「运行中但长时间没有新进展」。
type SessionAlertDetector struct {
	mu           sync.Mutex
	lastRunning  map[string]bool
	lastBusy     map[string]bool
	seenRequests map[string]map[string]bool
	threadTitles map[string]string
	lastActivity map[string]time.Time
	lastSummary  map[string]wire.ThreadSummary
	// stalledAlerted 标记当前这段无进展是否已经提醒过：同一段无进展只提醒一次。
	stalledAlerted map[string]bool
	// stalledHistory 记录每个会话已发出的卡住提醒时间，用于滚动窗口限额。
	stalledHistory map[string][]time.Time
	// backgroundSince 记录会话进入「回合已结束、只剩后台任务」状态的时刻。
	backgroundSince map[string]time.Time
	// backgroundAlerted 标记这段后台等待是否已经提醒过：同一段等待只提醒一次。
	backgroundAlerted map[string]bool
	// now 提供当前时间，nil 时使用 time.Now；测试可注入固定时钟。
	now func() time.Time
}

// NewSessionAlertDetector 创建并初始化 SessionAlertDetector。
func NewSessionAlertDetector() *SessionAlertDetector {
	return &SessionAlertDetector{
		lastRunning:       make(map[string]bool),
		lastBusy:          make(map[string]bool),
		seenRequests:      make(map[string]map[string]bool),
		threadTitles:      make(map[string]string),
		lastActivity:      make(map[string]time.Time),
		lastSummary:       make(map[string]wire.ThreadSummary),
		stalledAlerted:    make(map[string]bool),
		stalledHistory:    make(map[string][]time.Time),
		backgroundSince:   make(map[string]time.Time),
		backgroundAlerted: make(map[string]bool),
		now:               time.Now,
	}
}

// nowTime 返回检测器当前时间，未注入时钟时回退到 time.Now。
func (d *SessionAlertDetector) nowTime() time.Time {
	if d.now == nil {
		return time.Now()
	}
	return d.now()
}

// touch 记录会话最近一次事件的时间，并开启新的一段无进展观察期。
// 任何会话级事件（thread.summary、requests.changed 等）都算一次进展。
func (d *SessionAlertDetector) touch(threadID string) {
	if threadID == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastActivity[threadID] = d.nowTime()
	delete(d.stalledAlerted, threadID)
}

// Observe 消费 StreamHub 的观察者事件，若满足提醒条件则返回 SessionAlert 与 true。
func (d *SessionAlertDetector) Observe(threadID, method string, params any) (SessionAlert, bool) {
	// 会话列表变动只用于清理已删除会话的状态，不代表该会话有进展。
	if method == "threads.changed" {
		d.forgetRemovedThreads(params)
		return SessionAlert{}, false
	}
	d.touch(threadID)

	switch method {
	case "requests.changed":
		var reqs []wire.RemoteRequest
		switch p := params.(type) {
		case wire.RequestsChangedParams:
			reqs = p.Requests
		case *wire.RequestsChangedParams:
			if p == nil {
				return SessionAlert{}, false
			}
			reqs = p.Requests
		default:
			return SessionAlert{}, false
		}

		d.mu.Lock()
		seen, ok := d.seenRequests[threadID]
		if !ok {
			// 首次快照只建立基线，避免启动或重连时把已有审批误报成新请求。
			newSeen := make(map[string]bool, len(reqs))
			for i := range reqs {
				newSeen[reqs[i].RequestID] = true
			}
			d.seenRequests[threadID] = newSeen
			d.mu.Unlock()
			return SessionAlert{}, false
		}

		var newReq *wire.RemoteRequest
		newSeen := make(map[string]bool, len(reqs))
		for i := range reqs {
			rid := reqs[i].RequestID
			newSeen[rid] = true
			if !seen[rid] && newReq == nil {
				newReq = &reqs[i]
			}
		}
		d.seenRequests[threadID] = newSeen

		threadTitle := d.threadTitles[threadID]
		d.mu.Unlock()

		if newReq != nil {
			body := threadTitle
			if newReq.Title != nil && *newReq.Title != "" {
				body = *newReq.Title
			}
			return SessionAlert{
				Kind:     SessionAlertApproval,
				ThreadID: threadID,
				Title:    "需要你的审批",
				Body:     body,
				Tag:      "approval:" + threadID,
			}, true
		}

	case "thread.summary":
		var summary wire.ThreadSummary
		switch p := params.(type) {
		case wire.ThreadSummary:
			summary = p
		case *wire.ThreadSummary:
			if p == nil {
				return SessionAlert{}, false
			}
			summary = *p
		default:
			return SessionAlert{}, false
		}

		d.mu.Lock()
		d.threadTitles[threadID] = summary.Title
		d.lastSummary[threadID] = summary
		// 有效忙态：主线程自身在跑，或它名下的子线程还没全部落定/回报，
		// 或回合结束后仍有后台任务在跑。Swarm 模式下主线程送完子线程会先结束本轮，
		// 不能只看 IsRunning；后台任务结束时 CLI 会自动续跑，等续跑结束再发完成通知。
		busy := summary.IsRunning || summary.SwarmBusy || summary.BackgroundTasks > 0
		wasBusy, exists := d.lastBusy[threadID]
		d.lastRunning[threadID] = summary.IsRunning
		d.lastBusy[threadID] = busy
		if !busy {
			// 这一轮已结束：清掉卡住判定状态，下次运行重新计起。
			delete(d.stalledAlerted, threadID)
		}
		// 回合已结束、只剩后台任务时记下等待起点；其余状态清掉这段等待。
		if !summary.IsRunning && !summary.SwarmBusy && summary.BackgroundTasks > 0 {
			if _, ok := d.backgroundSince[threadID]; !ok {
				d.backgroundSince[threadID] = d.nowTime()
			}
		} else {
			delete(d.backgroundSince, threadID)
			delete(d.backgroundAlerted, threadID)
		}
		d.mu.Unlock()

		// 子线程（Swarm bee 等 helper 会话）结束时不提醒，避免桌面通知与远程推送噪音。
		if isHelperThread(summary) {
			return SessionAlert{}, false
		}

		if exists && wasBusy && !busy {
			kind := SessionAlertDone
			title := "会话已完成"
			tag := "done:" + threadID
			body := summary.Title
			if summary.Attention == "failed" {
				kind = SessionAlertFailed
				title = "会话出错"
				tag = "failed:" + threadID
				// 额度用尽或限流单独成一类提醒：能解析出重置时间时把恢复时刻写进正文。
				if summary.LastErrorText != "" && isUsageLimitError(summary.LastErrorText) {
					kind = SessionAlertQuota
					title = "额度已用完"
					tag = "quota:" + threadID
					if resetsAt, _, hasReset := parseResetTime(summary.LastErrorText, d.nowTime()); hasReset {
						body = quotaAlertBody(summary.Title, resetsAt)
					}
				}
			}
			return SessionAlert{
				Kind:     kind,
				ThreadID: threadID,
				Title:    title,
				Body:     body,
				Tag:      tag,
			}, true
		}
	}

	return SessionAlert{}, false
}

// quotaAlertBody 拼出额度提醒正文：标题后附上预计恢复的本地时刻。
func quotaAlertBody(title string, resetsAt time.Time) string {
	at := resetsAt.Format("15:04")
	if title == "" {
		return "预计 " + at + " 恢复"
	}
	return title + " · 预计 " + at + " 恢复"
}

// CheckStalled 巡检所有运行中的会话，返回「长时间没有新进展」的提醒。
// 只判定最近一次摘要里 IsRunning 为 true 的会话（仅 SwarmBusy 不算，主线程等子线程是正常状态），
// 且要求不是 helper 会话、没有待审批请求、距最后一次事件已超过 after。
// 同一段无进展只提醒一次；同一会话在滚动 24 小时内最多提醒 3 次。
func (d *SessionAlertDetector) CheckStalled(after time.Duration) []SessionAlert {
	if after <= 0 {
		return nil
	}
	now := d.nowTime()
	minutes := int(after / time.Minute)

	d.mu.Lock()
	defer d.mu.Unlock()

	var alerts []SessionAlert
	for threadID, summary := range d.lastSummary {
		if !summary.IsRunning || isHelperThread(summary) || summary.HasPendingRequest {
			continue
		}
		// 还在等用户审批的会话不是在卡住，提醒交给审批通知。
		if len(d.seenRequests[threadID]) > 0 {
			continue
		}
		last, ok := d.lastActivity[threadID]
		if !ok || now.Sub(last) <= after {
			continue
		}
		// lastActivity 就是这段无进展的起点，已提醒过就不再重复。
		if d.stalledAlerted[threadID] {
			continue
		}
		history := pruneStalledHistory(d.stalledHistory[threadID], now)
		if len(history) >= maxStalledAlertsPerWindow {
			d.stalledHistory[threadID] = history
			continue
		}
		d.stalledHistory[threadID] = append(history, now)
		d.stalledAlerted[threadID] = true

		body := fmt.Sprintf("已 %d 分钟没有新进展", minutes)
		if summary.Title != "" {
			body = summary.Title + " · " + body
		}
		alerts = append(alerts, SessionAlert{
			Kind:     SessionAlertStalled,
			ThreadID: threadID,
			Title:    "会话可能卡住了",
			Body:     body,
			Tag:      "stalled:" + threadID,
		})
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].ThreadID < alerts[j].ThreadID })
	return alerts
}

// CheckBackgroundLinger 巡检回合已结束、只剩后台任务的会话，返回持续超过 after 的提醒。
// 跳过 helper 会话与已提醒过的会话；同一段后台等待只提醒一次。
func (d *SessionAlertDetector) CheckBackgroundLinger(after time.Duration) []SessionAlert {
	if after <= 0 {
		return nil
	}
	now := d.nowTime()

	d.mu.Lock()
	defer d.mu.Unlock()

	var alerts []SessionAlert
	for threadID, since := range d.backgroundSince {
		summary := d.lastSummary[threadID]
		if isHelperThread(summary) || d.backgroundAlerted[threadID] {
			continue
		}
		if now.Sub(since) <= after {
			continue
		}
		d.backgroundAlerted[threadID] = true

		body := fmt.Sprintf("回合已结束，%d 个后台任务仍在运行", summary.BackgroundTasks)
		if summary.Title != "" {
			body = summary.Title + " · " + body
		}
		alerts = append(alerts, SessionAlert{
			Kind:     SessionAlertBackgroundLinger,
			ThreadID: threadID,
			Title:    "后台任务仍在运行",
			Body:     body,
			Tag:      "bg-linger:" + threadID,
		})
	}
	sort.Slice(alerts, func(i, j int) bool { return alerts[i].ThreadID < alerts[j].ThreadID })
	return alerts
}

// pruneStalledHistory 丢弃滚动窗口之前的提醒记录。
func pruneStalledHistory(times []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-stalledAlertWindow)
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// forgetRemovedThreads 清理已删除会话的状态，避免 map 随会话数量无限增长。
func (d *SessionAlertDetector) forgetRemovedThreads(params any) {
	var removed []string
	switch p := params.(type) {
	case wire.ThreadsChanged:
		removed = p.RemovedThreadIDs
	case *wire.ThreadsChanged:
		if p == nil {
			return
		}
		removed = p.RemovedThreadIDs
	default:
		return
	}
	if len(removed) == 0 {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, id := range removed {
		delete(d.lastRunning, id)
		delete(d.lastBusy, id)
		delete(d.seenRequests, id)
		delete(d.threadTitles, id)
		delete(d.lastActivity, id)
		delete(d.lastSummary, id)
		delete(d.stalledAlerted, id)
		delete(d.stalledHistory, id)
		delete(d.backgroundSince, id)
		delete(d.backgroundAlerted, id)
	}
}

// RunningCount 返回当前状态为运行中的会话总数。
func (d *SessionAlertDetector) RunningCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	count := 0
	for _, running := range d.lastRunning {
		if running {
			count++
		}
	}
	return count
}

// LastActivity 返回会话最近一次会话级事件的时刻；从未观察过时 ok 为 false。
func (d *SessionAlertDetector) LastActivity(threadID string) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	at, ok := d.lastActivity[threadID]
	return at, ok
}

// WatchSessionAlerts 监听 StreamHub 上的会话事件，触发提醒或运行数变更回调。
// stalledAfter 返回卡住提醒的无进展阈值：为 nil 或返回值不大于 0 时跳过卡住巡检；
// 后台久跑巡检始终运行。返回的 stop 函数可安全重复调用，会同时停掉事件观察者与巡检 goroutine。
func WatchSessionAlerts(hub *StreamHub, stalledAfter func() time.Duration, onAlert func(SessionAlert), onRunningChanged func(n int)) (stop func()) {
	if hub == nil {
		return func() {}
	}
	detector := NewSessionAlertDetector()
	stopObserver := hub.AddObserver(func(threadID, method string, params any) {
		oldCount := 0
		if method == "thread.summary" && onRunningChanged != nil {
			oldCount = detector.RunningCount()
		}
		alert, ok := detector.Observe(threadID, method, params)
		if ok && onAlert != nil {
			onAlert(alert)
		}
		if method == "thread.summary" && onRunningChanged != nil {
			newCount := detector.RunningCount()
			if newCount != oldCount {
				onRunningChanged(newCount)
			}
		}
	})

	stopStalled := make(chan struct{})
	go func() {
		ticker := time.NewTicker(stalledCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopStalled:
				return
			case <-ticker.C:
				if stalledAfter != nil {
					if after := stalledAfter(); after > 0 {
						for _, alert := range detector.CheckStalled(after) {
							if onAlert != nil {
								onAlert(alert)
							}
						}
					}
				}
				for _, alert := range detector.CheckBackgroundLinger(backgroundLingerAlertAfter) {
					if onAlert != nil {
						onAlert(alert)
					}
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			stopObserver()
			// 关闭后巡检 goroutine 会在下一次 select 时退出；这里不等待，避免在 onAlert 里调用 stop 时自锁。
			close(stopStalled)
		})
	}
}
