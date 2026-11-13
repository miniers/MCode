package remote

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// TestSessionAlertDetector_ApprovalOnce 验证新增审批只触发一次，重复请求不触发。
func TestSessionAlertDetector_ApprovalOnce(t *testing.T) {
	d := NewSessionAlertDetector()
	threadID := "thread-1"

	// 记录会话标题
	d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:        threadID,
		Title:     "代码重构任务",
		IsRunning: true,
	})

	reqTitle := "是否允许执行脚本"
	params1 := wire.RequestsChangedParams{
		ThreadID: threadID,
		Requests: []wire.RemoteRequest{
			{
				ThreadID:  threadID,
				RequestID: "req-1",
				Title:     &reqTitle,
			},
		},
	}

	// 首次收到快照只建立基线，不应把已有审批误报成新请求。
	alert, ok := d.Observe(threadID, "requests.changed", params1)
	if ok {
		t.Fatalf("unexpected alert for initial approval snapshot: %+v", alert)
	}

	// 再次收到相同请求，不应触发
	_, ok = d.Observe(threadID, "requests.changed", params1)
	if ok {
		t.Fatalf("unexpected alert on duplicate approval request")
	}

	// 新增第二条审批请求，应触发
	reqTitle2 := "是否允许删除文件"
	params2 := wire.RequestsChangedParams{
		ThreadID: threadID,
		Requests: []wire.RemoteRequest{
			{
				ThreadID:  threadID,
				RequestID: "req-1",
				Title:     &reqTitle,
			},
			{
				ThreadID:  threadID,
				RequestID: "req-2",
				Title:     &reqTitle2,
			},
		},
	}
	alert2, ok := d.Observe(threadID, "requests.changed", params2)
	if !ok {
		t.Fatalf("expected alert for second new approval request")
	}
	if alert2.Body != reqTitle2 {
		t.Errorf("expected body %s, got %s", reqTitle2, alert2.Body)
	}
}

// TestSessionAlertDetector_Done 验证从运行变为结束时触发，首次出现非运行状态不触发。
func TestSessionAlertDetector_Done(t *testing.T) {
	d := NewSessionAlertDetector()
	threadID := "thread-1"

	// 1. 首次出现非运行状态，不触发完成提醒
	alert, ok := d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:        threadID,
		Title:     "静态分析",
		IsRunning: false,
	})
	if ok {
		t.Fatalf("unexpected alert on initial non-running summary: %+v", alert)
	}

	// 2. 状态变为运行中，不触发完成提醒
	_, ok = d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:        threadID,
		Title:     "静态分析",
		IsRunning: true,
	})
	if ok {
		t.Fatalf("unexpected alert when transitioning to running")
	}

	// 3. 从运行变为结束，触发完成提醒
	alert, ok = d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:        threadID,
		Title:     "静态分析",
		IsRunning: false,
	})
	if !ok {
		t.Fatalf("expected alert when transitioning from running to done")
	}
	if alert.Kind != SessionAlertDone {
		t.Errorf("expected kind %s, got %s", SessionAlertDone, alert.Kind)
	}
	if alert.ThreadID != threadID {
		t.Errorf("expected threadID %s, got %s", threadID, alert.ThreadID)
	}
	if alert.Title != "会话已完成" {
		t.Errorf("expected title '会话已完成', got %s", alert.Title)
	}
	if alert.Body != "静态分析" {
		t.Errorf("expected body '静态分析', got %s", alert.Body)
	}
	if alert.Tag != "done:"+threadID {
		t.Errorf("expected tag done:%s, got %s", threadID, alert.Tag)
	}

	// 4. 再次收到非运行状态，不触发
	_, ok = d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:        threadID,
		Title:     "静态分析",
		IsRunning: false,
	})
	if ok {
		t.Fatalf("unexpected alert on subsequent non-running summary")
	}
}

// TestSessionAlertDetector_IgnoresHelperThreadDone 验证子线程结束时不发提醒，主线程正常提醒。
func TestSessionAlertDetector_IgnoresHelperThreadDone(t *testing.T) {
	d := NewSessionAlertDetector()
	parentID := "lead-1"

	helpCases := []struct {
		name    string
		summary wire.ThreadSummary
	}{
		{
			name: "parent-set",
			summary: wire.ThreadSummary{
				ID:             "bee-1",
				Title:          "Cove: 子任务",
				IsRunning:      true,
				ParentThreadID: &parentID,
			},
		},
		{
			name: "helper-flag",
			summary: wire.ThreadSummary{
				ID:        "bee-2",
				Title:     "Aspen: 子任务",
				IsRunning: true,
				IsHelper:  true,
			},
		},
	}

	for _, tc := range helpCases {
		t.Run(tc.name, func(t *testing.T) {
			// 首次运行态
			if _, ok := d.Observe(tc.summary.ID, "thread.summary", tc.summary); ok {
				t.Fatalf("unexpected alert when helper thread starts running")
			}

			// 从运行变为结束，子线程不发提醒
			done := tc.summary
			done.IsRunning = false
			if alert, ok := d.Observe(tc.summary.ID, "thread.summary", done); ok {
				t.Fatalf("unexpected alert for helper thread done: %+v", alert)
			}

			// 失败状态同样不发提醒
			failed := tc.summary
			failed.IsRunning = true
			if _, ok := d.Observe(tc.summary.ID, "thread.summary", failed); ok {
				t.Fatalf("unexpected alert when helper thread restarts")
			}
			failed.IsRunning = false
			failed.Attention = "failed"
			if alert, ok := d.Observe(tc.summary.ID, "thread.summary", failed); ok {
				t.Fatalf("unexpected failed alert for helper thread: %+v", alert)
			}
		})
	}

	// 主线程（无 ParentThreadID、非 helper）仍正常发出提醒
	lead := wire.ThreadSummary{ID: "lead-1", Title: "主会话", IsRunning: true}
	if _, ok := d.Observe("lead-1", "thread.summary", lead); ok {
		t.Fatalf("unexpected alert when main thread starts running")
	}
	lead.IsRunning = false
	alert, ok := d.Observe("lead-1", "thread.summary", lead)
	if !ok {
		t.Fatalf("expected alert when main thread finishes")
	}
	if alert.Kind != SessionAlertDone || alert.ThreadID != "lead-1" {
		t.Errorf("unexpected alert for main thread: %+v", alert)
	}
}

// TestSessionAlertDetector_RunningCount 验证 RunningCount 统计运行中会话数量。
func TestSessionAlertDetector_RunningCount(t *testing.T) {
	d := NewSessionAlertDetector()
	if count := d.RunningCount(); count != 0 {
		t.Fatalf("expected initial running count 0, got %d", count)
	}

	d.Observe("t-1", "thread.summary", wire.ThreadSummary{ID: "t-1", IsRunning: true})
	if count := d.RunningCount(); count != 1 {
		t.Errorf("expected running count 1, got %d", count)
	}

	d.Observe("t-2", "thread.summary", wire.ThreadSummary{ID: "t-2", IsRunning: true})
	if count := d.RunningCount(); count != 2 {
		t.Errorf("expected running count 2, got %d", count)
	}

	d.Observe("t-1", "thread.summary", wire.ThreadSummary{ID: "t-1", IsRunning: false})
	if count := d.RunningCount(); count != 1 {
		t.Errorf("expected running count 1, got %d", count)
	}

	d.Observe("t-2", "thread.summary", wire.ThreadSummary{ID: "t-2", IsRunning: false})
	if count := d.RunningCount(); count != 0 {
		t.Errorf("expected running count 0, got %d", count)
	}
}

// TestLastActivityReturnsTouchedTime 验证 LastActivity 返回会话最近一次事件的时刻。
func TestLastActivityReturnsTouchedTime(t *testing.T) {
	base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	clock := base
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return clock }

	clock = base.Add(2 * time.Minute)
	d.Observe("t-1", "thread.summary", wire.ThreadSummary{ID: "t-1", Title: "会话", IsRunning: true})

	at, ok := d.LastActivity("t-1")
	if !ok {
		t.Fatal("expected ok for observed thread")
	}
	if !at.Equal(clock) {
		t.Errorf("expected last activity %v, got %v", clock, at)
	}

	// 新事件刷新最近活动时间
	clock = base.Add(5 * time.Minute)
	d.Observe("t-1", "requests.changed", wire.RequestsChangedParams{})
	at, ok = d.LastActivity("t-1")
	if !ok {
		t.Fatal("expected ok after second event")
	}
	if !at.Equal(clock) {
		t.Errorf("expected refreshed last activity %v, got %v", clock, at)
	}
}

// TestLastActivityUnknownThread 验证未知会话返回零值与 false。
func TestLastActivityUnknownThread(t *testing.T) {
	d := NewSessionAlertDetector()
	at, ok := d.LastActivity("missing")
	if ok {
		t.Fatal("expected ok false for unknown thread")
	}
	if !at.IsZero() {
		t.Errorf("expected zero time for unknown thread, got %v", at)
	}
}

// TestStreamHub_AddObserver_MultipleAndRemove 验证多个观察者均能收到事件且 remove 后不再收到。
func TestStreamHub_AddObserver_MultipleAndRemove(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 50 * time.Millisecond})

	var mu sync.Mutex
	var setObsEvents []string
	var obs1Events []string
	var obs2Events []string

	hub.SetObserver(func(threadID, method string, params any) {
		mu.Lock()
		setObsEvents = append(setObsEvents, threadID+":"+method)
		mu.Unlock()
	})

	remove1 := hub.AddObserver(func(threadID, method string, params any) {
		mu.Lock()
		obs1Events = append(obs1Events, threadID+":"+method)
		mu.Unlock()
	})

	remove2 := hub.AddObserver(func(threadID, method string, params any) {
		mu.Lock()
		obs2Events = append(obs2Events, threadID+":"+method)
		mu.Unlock()
	})

	// 第一次发布事件
	hub.Publish("t-1", nil, nil, wire.ThreadSummary{ID: "t-1", Title: "Job 1", IsRunning: true})

	mu.Lock()
	if len(setObsEvents) != 1 || len(obs1Events) != 1 || len(obs2Events) != 1 {
		t.Fatalf("expected all 3 observers to receive event once, got set=%d, obs1=%d, obs2=%d",
			len(setObsEvents), len(obs1Events), len(obs2Events))
	}
	mu.Unlock()

	// 移除 observer 1
	remove1()

	// 第二次发布事件
	hub.Notify("t-1", "custom.event", nil)

	mu.Lock()
	if len(setObsEvents) != 2 {
		t.Errorf("expected setObserver to have 2 events, got %d", len(setObsEvents))
	}
	if len(obs1Events) != 1 {
		t.Errorf("expected obs1 to remain at 1 event after removal, got %d", len(obs1Events))
	}
	if len(obs2Events) != 2 {
		t.Errorf("expected obs2 to have 2 events, got %d", len(obs2Events))
	}
	mu.Unlock()

	// 移除 observer 2
	remove2()

	// 第三次发布事件
	hub.Notify("t-1", "custom.event2", nil)

	mu.Lock()
	if len(setObsEvents) != 3 {
		t.Errorf("expected setObserver to have 3 events, got %d", len(setObsEvents))
	}
	if len(obs1Events) != 1 {
		t.Errorf("expected obs1 to remain at 1 event, got %d", len(obs1Events))
	}
	if len(obs2Events) != 2 {
		t.Errorf("expected obs2 to remain at 2 events, got %d", len(obs2Events))
	}
	mu.Unlock()
}

// TestWatchSessionAlerts 验证 WatchSessionAlerts 回调及 RunningCount 变更响应。
func TestWatchSessionAlerts(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 50 * time.Millisecond})

	var alerts []SessionAlert
	var runningChanges []int
	var mu sync.Mutex

	stop := WatchSessionAlerts(hub, nil, func(alert SessionAlert) {
		mu.Lock()
		alerts = append(alerts, alert)
		mu.Unlock()
	}, func(n int) {
		mu.Lock()
		runningChanges = append(runningChanges, n)
		mu.Unlock()
	})
	defer stop()

	// 启动会话
	hub.Publish("t-1", nil, nil, wire.ThreadSummary{
		ID:        "t-1",
		Title:     "任务A",
		IsRunning: true,
	})

	// 审批事件
	reqTitle := "是否继续"
	hub.Notify("t-1", "requests.changed", wire.RequestsChangedParams{ThreadID: "t-1"})
	hub.Notify("t-1", "requests.changed", wire.RequestsChangedParams{
		ThreadID: "t-1",
		Requests: []wire.RemoteRequest{
			{RequestID: "r-1", Title: &reqTitle},
		},
	})

	// 结束会话
	hub.Publish("t-1", nil, nil, wire.ThreadSummary{
		ID:        "t-1",
		Title:     "任务A",
		IsRunning: false,
	})

	mu.Lock()
	defer mu.Unlock()

	if len(alerts) != 2 {
		t.Fatalf("expected 2 alerts (approval + done), got %d", len(alerts))
	}
	if alerts[0].Kind != SessionAlertApproval {
		t.Errorf("expected first alert approval, got %s", alerts[0].Kind)
	}
	if alerts[1].Kind != SessionAlertDone {
		t.Errorf("expected second alert done, got %s", alerts[1].Kind)
	}

	// runningChanges 应该经历 1 (true) -> 0 (false)
	if len(runningChanges) != 2 || runningChanges[0] != 1 || runningChanges[1] != 0 {
		t.Errorf("expected running changes [1, 0], got %v", runningChanges)
	}
}

func TestSessionAlertDetectorIgnoresMalformedEvents(t *testing.T) {
	d := NewSessionAlertDetector()
	d.Observe("t-1", "thread.summary", wire.ThreadSummary{IsRunning: true})
	reqs := wire.RequestsChangedParams{Requests: []wire.RemoteRequest{{RequestID: "r-1"}}}
	d.Observe("t-1", "requests.changed", reqs)
	for _, params := range []any{nil, (*wire.ThreadSummary)(nil), "invalid"} {
		if _, ok := d.Observe("t-1", "thread.summary", params); ok {
			t.Fatal("malformed summary triggered completion")
		}
	}
	if d.RunningCount() != 1 {
		t.Fatal("malformed summary changed running count")
	}
	for _, params := range []any{nil, (*wire.RequestsChangedParams)(nil), "invalid"} {
		d.Observe("t-1", "requests.changed", params)
	}
	if _, ok := d.Observe("t-1", "requests.changed", reqs); ok {
		t.Fatal("malformed request event reset deduplication")
	}
}

// TestSessionAlertDetector_StalledAlerts 验证卡住提醒：超过阈值提醒一次、同一段无进展不重复、新事件后再次超时会再提醒。
func TestSessionAlertDetector_StalledAlerts(t *testing.T) {
	base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	clock := base
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return clock }

	running := wire.ThreadSummary{ID: "t-1", Title: "长任务", IsRunning: true}
	if alert, ok := d.Observe("t-1", "thread.summary", running); ok {
		t.Fatalf("首次运行态不应提醒: %+v", alert)
	}

	// 未超过阈值
	clock = base.Add(9 * time.Minute)
	if alerts := d.CheckStalled(10 * time.Minute); len(alerts) != 0 {
		t.Fatalf("未超过阈值不应提醒，got %+v", alerts)
	}

	// 超过阈值提醒一次
	clock = base.Add(11 * time.Minute)
	alerts := d.CheckStalled(10 * time.Minute)
	if len(alerts) != 1 {
		t.Fatalf("超过阈值应提醒一次，got %+v", alerts)
	}
	alert := alerts[0]
	if alert.Kind != SessionAlertStalled {
		t.Errorf("expected kind %s, got %s", SessionAlertStalled, alert.Kind)
	}
	if alert.ThreadID != "t-1" {
		t.Errorf("expected threadID t-1, got %s", alert.ThreadID)
	}
	if alert.Title != "会话可能卡住了" {
		t.Errorf("expected title '会话可能卡住了', got %s", alert.Title)
	}
	if alert.Body != "长任务 · 已 10 分钟没有新进展" {
		t.Errorf("unexpected body: %s", alert.Body)
	}
	if alert.Tag != "stalled:t-1" {
		t.Errorf("expected tag stalled:t-1, got %s", alert.Tag)
	}

	// 同一段无进展不重复提醒
	clock = base.Add(20 * time.Minute)
	if alerts := d.CheckStalled(10 * time.Minute); len(alerts) != 0 {
		t.Fatalf("同一段无进展不应重复提醒，got %+v", alerts)
	}

	// 新事件到来后再次超时会再提醒
	clock = base.Add(21 * time.Minute)
	d.Observe("t-1", "thread.summary", running)
	clock = base.Add(32 * time.Minute)
	if alerts := d.CheckStalled(10 * time.Minute); len(alerts) != 1 {
		t.Fatalf("新事件后再次超时应再提醒一次，got %+v", alerts)
	}
}

// TestSessionAlertDetector_StalledDailyCap 验证同一会话滚动 24 小时内最多提醒 3 次。
func TestSessionAlertDetector_StalledDailyCap(t *testing.T) {
	base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	clock := base
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return clock }

	running := wire.ThreadSummary{ID: "t-1", Title: "长任务", IsRunning: true}
	for i := 0; i < 3; i++ {
		d.Observe("t-1", "thread.summary", running)
		clock = clock.Add(11 * time.Minute)
		if alerts := d.CheckStalled(10 * time.Minute); len(alerts) != 1 {
			t.Fatalf("第 %d 次应提醒，got %+v", i+1, alerts)
		}
	}

	// 第 4 次仍在 24 小时窗口内，被截停
	d.Observe("t-1", "thread.summary", running)
	clock = clock.Add(11 * time.Minute)
	if alerts := d.CheckStalled(10 * time.Minute); len(alerts) != 0 {
		t.Fatalf("24 小时内第 4 次不应提醒，got %+v", alerts)
	}
}

// TestSessionAlertDetector_StalledSkips 验证 helper、待审批、仅 SwarmBusy 的会话不产出卡住提醒。
func TestSessionAlertDetector_StalledSkips(t *testing.T) {
	parentID := "lead-1"
	for _, tc := range []struct {
		name     string
		summary  wire.ThreadSummary
		requests []wire.RemoteRequest
		want     int
	}{
		{"运行中正常提醒", wire.ThreadSummary{ID: "t-1", Title: "任务", IsRunning: true}, nil, 1},
		{"helper 标记不提醒", wire.ThreadSummary{ID: "t-1", IsRunning: true, IsHelper: true}, nil, 0},
		{"子线程不提醒", wire.ThreadSummary{ID: "t-1", IsRunning: true, ParentThreadID: &parentID}, nil, 0},
		{"有待审批不提醒", wire.ThreadSummary{ID: "t-1", IsRunning: true, HasPendingRequest: true}, nil, 0},
		{"仅 SwarmBusy 不提醒", wire.ThreadSummary{ID: "t-1", IsRunning: false, SwarmBusy: true}, nil, 0},
		{"审批请求未清空不提醒", wire.ThreadSummary{ID: "t-1", IsRunning: true}, []wire.RemoteRequest{{RequestID: "r-1"}}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
			clock := base
			d := NewSessionAlertDetector()
			d.now = func() time.Time { return clock }

			d.Observe("t-1", "thread.summary", tc.summary)
			if tc.requests != nil {
				d.Observe("t-1", "requests.changed", wire.RequestsChangedParams{ThreadID: "t-1", Requests: tc.requests})
			}
			clock = base.Add(11 * time.Minute)
			if got := len(d.CheckStalled(10 * time.Minute)); got != tc.want {
				t.Fatalf("got %d alerts, want %d", got, tc.want)
			}
		})
	}
}

// TestSessionAlertDetector_StalledClearedAfterFinish 验证会话结束后清理卡住判定状态，不再产出提醒。
func TestSessionAlertDetector_StalledClearedAfterFinish(t *testing.T) {
	base := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	clock := base
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return clock }

	running := wire.ThreadSummary{ID: "t-1", Title: "长任务", IsRunning: true}
	d.Observe("t-1", "thread.summary", running)
	clock = base.Add(11 * time.Minute)
	if alerts := d.CheckStalled(10 * time.Minute); len(alerts) != 1 {
		t.Fatalf("超过阈值应提醒一次，got %+v", alerts)
	}

	clock = clock.Add(time.Minute)
	d.Observe("t-1", "thread.summary", wire.ThreadSummary{ID: "t-1", Title: "长任务", IsRunning: false})
	if _, ok := d.stalledAlerted["t-1"]; ok {
		t.Fatal("会话结束后应清理卡住提醒标记")
	}
	clock = clock.Add(time.Hour)
	if alerts := d.CheckStalled(10 * time.Minute); len(alerts) != 0 {
		t.Fatalf("会话结束后不应再提醒，got %+v", alerts)
	}
}

// TestSessionAlertDetector_ForgetsRemovedThreads 验证已删除会话的状态被清理。
func TestSessionAlertDetector_ForgetsRemovedThreads(t *testing.T) {
	clock := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return clock }

	d.Observe("t-1", "thread.summary", wire.ThreadSummary{ID: "t-1", Title: "长任务", IsRunning: true})
	d.Observe("t-1", "requests.changed", wire.RequestsChangedParams{
		ThreadID: "t-1",
		Requests: []wire.RemoteRequest{{RequestID: "r-1"}},
	})
	if len(d.lastSummary) != 1 {
		t.Fatalf("expected tracked state before removal")
	}

	d.Observe("t-1", "threads.changed", wire.ThreadsChanged{RemovedThreadIDs: []string{"t-1"}})

	if len(d.lastRunning) != 0 || len(d.lastBusy) != 0 || len(d.seenRequests) != 0 ||
		len(d.threadTitles) != 0 || len(d.lastActivity) != 0 || len(d.lastSummary) != 0 ||
		len(d.stalledAlerted) != 0 || len(d.stalledHistory) != 0 {
		t.Fatalf("removed thread state leaked: run=%d busy=%d req=%d title=%d activity=%d summary=%d alerted=%d history=%d",
			len(d.lastRunning), len(d.lastBusy), len(d.seenRequests), len(d.threadTitles),
			len(d.lastActivity), len(d.lastSummary), len(d.stalledAlerted), len(d.stalledHistory))
	}
	if count := d.RunningCount(); count != 0 {
		t.Fatalf("expected running count 0 after removal, got %d", count)
	}
}

// TestWatchSessionAlerts_StopIsIdempotent 验证 stop 可以安全重复调用，且巡检开关关闭时不启动 ticker。
func TestWatchSessionAlerts_StopIsIdempotent(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})

	stop := WatchSessionAlerts(hub, func() time.Duration { return time.Minute }, func(SessionAlert) {}, nil)
	stop()
	stop()

	stopDisabled := WatchSessionAlerts(hub, func() time.Duration { return 0 }, nil, nil)
	stopDisabled()
	stopDisabled()
}

// TestSessionAlertDetector_QuotaAlert 验证额度用尽结束时产出 quota 提醒，正文带预计恢复的本地时刻。
func TestSessionAlertDetector_QuotaAlert(t *testing.T) {
	now := time.Date(2026, 1, 2, 9, 0, 0, 0, time.Local)
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return now }

	threadID := "t-quota"
	if _, ok := d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:        threadID,
		Title:     "迁移任务",
		IsRunning: true,
	}); ok {
		t.Fatal("运行态不应提醒")
	}

	alert, ok := d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:            threadID,
		Title:         "迁移任务",
		Attention:     "failed",
		LastErrorText: "Claude usage limit reached. Your limit will reset at 3:30 pm.",
	})
	if !ok {
		t.Fatal("额度用尽结束应产出提醒")
	}
	if alert.Kind != SessionAlertQuota {
		t.Errorf("expected kind %s, got %s", SessionAlertQuota, alert.Kind)
	}
	if alert.Title != "额度已用完" {
		t.Errorf("expected title '额度已用完', got %s", alert.Title)
	}
	if alert.Tag != "quota:"+threadID {
		t.Errorf("expected tag quota:%s, got %s", threadID, alert.Tag)
	}
	if alert.Body != "迁移任务 · 预计 15:30 恢复" {
		t.Errorf("unexpected body: %s", alert.Body)
	}
}

// TestSessionAlertDetector_QuotaAlertWithoutResetTime 验证解析不出恢复时间时正文只保留会话标题。
func TestSessionAlertDetector_QuotaAlertWithoutResetTime(t *testing.T) {
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return time.Date(2026, 1, 2, 9, 0, 0, 0, time.Local) }

	threadID := "t-quota-plain"
	d.Observe(threadID, "thread.summary", wire.ThreadSummary{ID: threadID, Title: "配额任务", IsRunning: true})
	alert, ok := d.Observe(threadID, "thread.summary", wire.ThreadSummary{
		ID:            threadID,
		Title:         "配额任务",
		Attention:     "failed",
		LastErrorText: "API error: insufficient_quota, please check your plan",
	})
	if !ok {
		t.Fatal("额度用尽结束应产出提醒")
	}
	if alert.Kind != SessionAlertQuota {
		t.Errorf("expected kind %s, got %s", SessionAlertQuota, alert.Kind)
	}
	if alert.Body != "配额任务" {
		t.Errorf("expected body '配额任务', got %s", alert.Body)
	}
}

// TestSessionAlertDetector_FailedWithoutQuota 验证普通错误或缺少错误原文时仍产出 failed 提醒。
func TestSessionAlertDetector_FailedWithoutQuota(t *testing.T) {
	for _, tc := range []struct {
		name    string
		errText string
	}{
		{"普通错误", "network timeout"},
		{"无错误原文", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewSessionAlertDetector()
			threadID := "t-failed"
			d.Observe(threadID, "thread.summary", wire.ThreadSummary{ID: threadID, Title: "构建任务", IsRunning: true})
			alert, ok := d.Observe(threadID, "thread.summary", wire.ThreadSummary{
				ID:            threadID,
				Title:         "构建任务",
				Attention:     "failed",
				LastErrorText: tc.errText,
			})
			if !ok {
				t.Fatal("出错结束应产出提醒")
			}
			if alert.Kind != SessionAlertFailed {
				t.Errorf("expected kind %s, got %s", SessionAlertFailed, alert.Kind)
			}
			if alert.Title != "会话出错" {
				t.Errorf("expected title '会话出错', got %s", alert.Title)
			}
			if alert.Body != "构建任务" {
				t.Errorf("expected body '构建任务', got %s", alert.Body)
			}
			if alert.Tag != "failed:"+threadID {
				t.Errorf("expected tag failed:%s, got %s", threadID, alert.Tag)
			}
		})
	}
}

// TestTruncateLastErrorText 验证错误原文按 rune 截断到 500 字，短文本原样保留。
func TestTruncateLastErrorText(t *testing.T) {
	short := "network timeout"
	if got := truncateLastErrorText(short); got != short {
		t.Errorf("短文本不应被截断，got %q", got)
	}
	long := strings.Repeat("限", maxLastErrorTextRunes+100)
	if got := len([]rune(truncateLastErrorText(long))); got != maxLastErrorTextRunes {
		t.Errorf("expected %d runes, got %d", maxLastErrorTextRunes, got)
	}
}

// TestMemoryBackend_LastErrorTextRoundTrip 验证出错收尾把错误原文写进会话摘要，并在下一轮开始时清空。
func TestMemoryBackend_LastErrorTextRoundTrip(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()

	const limitErr = "Claude usage limit reached. Your limit will reset at 3:30 pm."
	var calls atomic.Int32
	startedCh := make(chan struct{})
	releaseCh := make(chan struct{})
	// release 可安全重复调用：用例中途 Fatal 时 defer 也能放行阻塞中的第二轮。
	release := sync.OnceFunc(func() { close(releaseCh) })
	defer release()
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			if calls.Add(1) == 1 {
				return errors.New(limitErr)
			}
			close(startedCh)
			select {
			case <-releaseCh:
			case <-ctx.Done():
			}
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace/project", harness.ModeAgent)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000a1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "额度任务"}, nil)

	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "开始任务",
		ActionID: "00000000-0000-0000-0000-0000000000b1",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	// 失败收尾与摘要更新不是原子的：等到 attention 落定再看错误原文。
	deadline := time.Now().Add(3 * time.Second)
	var failed wire.ThreadSummary
	for time.Now().Before(deadline) {
		th, err := backend.GetThread(ctx, threadID)
		if err == nil && !th.IsRunning && th.Attention == "failed" {
			failed = th
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if failed.Attention != "failed" {
		t.Fatalf("expected attention failed, got %q", failed.Attention)
	}
	if failed.LastErrorText != limitErr {
		t.Fatalf("expected lastErrorText %q, got %q", limitErr, failed.LastErrorText)
	}

	// 用写好的摘要驱动检测器，确认能产出 quota 提醒。
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return time.Date(2026, 1, 2, 9, 0, 0, 0, time.Local) }
	running := failed
	running.IsRunning = true
	running.Attention = "running"
	running.LastErrorText = ""
	d.Observe(threadID, "thread.summary", running)
	alert, ok := d.Observe(threadID, "thread.summary", failed)
	if !ok || alert.Kind != SessionAlertQuota {
		t.Fatalf("expected quota alert, got %+v (ok=%v)", alert, ok)
	}
	if alert.Body != "额度任务 · 预计 15:30 恢复" {
		t.Errorf("unexpected body: %s", alert.Body)
	}

	// 新一轮开始时清空错误原文。
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "继续任务",
		ActionID: "00000000-0000-0000-0000-0000000000b2",
	}); err != nil {
		t.Fatalf("SendMessage round 2 failed: %v", err)
	}
	select {
	case <-startedCh:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for round 2 to start")
	}
	runningSummary, err := backend.GetThread(ctx, threadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !runningSummary.IsRunning || runningSummary.Attention != "running" {
		t.Fatalf("expected running summary, got %+v", runningSummary)
	}
	if runningSummary.LastErrorText != "" {
		t.Errorf("新一轮开始应清空 lastErrorText, got %q", runningSummary.LastErrorText)
	}

	release()
	waitThreadIdle(t, backend, ctx, threadID)
}
