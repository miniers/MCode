package remote

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"mcode/internal/remote/wire"
)

// TestSessionAlertDetector_SwarmBusyDefersDone 验证主线程空闲但仍忙（名下子线程未落定或回报未投递）
// 时不提醒，忙态清零后只提醒一次。
func TestSessionAlertDetector_SwarmBusyDefersDone(t *testing.T) {
	d := NewSessionAlertDetector()
	leadID := "lead-1"
	lead := wire.ThreadSummary{ID: leadID, Title: "主会话"}

	// 1. 主线程开始一轮，同时已派出子线程
	lead.IsRunning = true
	lead.SwarmBusy = true
	if _, ok := d.Observe(leadID, "thread.summary", lead); ok {
		t.Fatalf("unexpected alert when lead starts running")
	}

	// 2. 主线程这一轮结束，但子线程还在跑：不发提醒
	lead.IsRunning = false
	if alert, ok := d.Observe(leadID, "thread.summary", lead); ok {
		t.Fatalf("unexpected alert while swarm bees are still active: %+v", alert)
	}

	// 3. 子线程回报让主线程又跑了一轮，结束时仍然算忙：不发提醒
	lead.IsRunning = true
	if _, ok := d.Observe(leadID, "thread.summary", lead); ok {
		t.Fatalf("unexpected alert when lead runs again for swarm reports")
	}
	lead.IsRunning = false
	if alert, ok := d.Observe(leadID, "thread.summary", lead); ok {
		t.Fatalf("unexpected alert while swarm reports are still pending: %+v", alert)
	}

	// 4. 忙态清零：只提醒一次
	lead.SwarmBusy = false
	alert, ok := d.Observe(leadID, "thread.summary", lead)
	if !ok {
		t.Fatalf("expected done alert after swarm busy cleared")
	}
	if alert.Kind != SessionAlertDone || alert.ThreadID != leadID {
		t.Fatalf("unexpected alert: %+v", alert)
	}

	// 5. 再收到同样的空闲摘要不重复提醒
	if again, ok := d.Observe(leadID, "thread.summary", lead); ok {
		t.Fatalf("unexpected duplicate alert: %+v", again)
	}

	// 6. RunningCount 仍按 IsRunning 计数，忙态不参与统计
	if count := d.RunningCount(); count != 0 {
		t.Fatalf("expected running count 0, got %d", count)
	}
	lead.IsRunning = true
	d.Observe(leadID, "thread.summary", lead)
	if count := d.RunningCount(); count != 1 {
		t.Fatalf("expected running count 1, got %d", count)
	}
}

// TestSessionAlertDetector_SwarmBusyFailed 验证忙态清零后按 Attention 判定 failed 提醒。
func TestSessionAlertDetector_SwarmBusyFailed(t *testing.T) {
	d := NewSessionAlertDetector()
	leadID := "lead-2"
	lead := wire.ThreadSummary{ID: leadID, Title: "主会话", IsRunning: true, SwarmBusy: true}
	d.Observe(leadID, "thread.summary", lead)

	lead.IsRunning = false
	if alert, ok := d.Observe(leadID, "thread.summary", lead); ok {
		t.Fatalf("unexpected alert while swarm busy: %+v", alert)
	}

	lead.SwarmBusy = false
	lead.Attention = "failed"
	alert, ok := d.Observe(leadID, "thread.summary", lead)
	if !ok {
		t.Fatalf("expected failed alert after swarm busy cleared")
	}
	if alert.Kind != SessionAlertFailed || alert.Tag != "failed:"+leadID {
		t.Fatalf("unexpected alert: %+v", alert)
	}
}

// TestSessionAlertDetector_HelperThreadSwarmBusy 验证子线程即使带 SwarmBusy 也不触发任何提醒。
func TestSessionAlertDetector_HelperThreadSwarmBusy(t *testing.T) {
	d := NewSessionAlertDetector()
	parentID := "lead-3"
	bee := wire.ThreadSummary{ID: "bee-1", Title: "Cove: 子任务", ParentThreadID: &parentID, IsRunning: true, SwarmBusy: true}
	d.Observe(bee.ID, "thread.summary", bee)

	bee.IsRunning = false
	bee.SwarmBusy = false
	if alert, ok := d.Observe(bee.ID, "thread.summary", bee); ok {
		t.Fatalf("unexpected alert for helper thread done: %+v", alert)
	}

	bee.IsHelper = true
	bee.ParentThreadID = nil
	bee.IsRunning = true
	d.Observe(bee.ID, "thread.summary", bee)
	bee.IsRunning = false
	if alert, ok := d.Observe(bee.ID, "thread.summary", bee); ok {
		t.Fatalf("unexpected alert for helper thread done: %+v", alert)
	}
}

// TestStreamHub_PublishNotifiesOnSwarmBusyChange 验证只有 SwarmBusy 变化时也会立即通知观察者，
// 否则提醒检测拿不到「忙态由 true 变 false」的那一刻。
func TestStreamHub_PublishNotifiesOnSwarmBusyChange(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{Interval: 50 * time.Millisecond})
	defer hub.Close()

	var mu sync.Mutex
	var events []wire.ThreadSummary
	stop := hub.AddObserver(func(threadID, method string, params any) {
		if method != "thread.summary" {
			return
		}
		summary, ok := params.(wire.ThreadSummary)
		if !ok {
			return
		}
		mu.Lock()
		events = append(events, summary)
		mu.Unlock()
	})
	defer stop()

	idle := wire.ThreadSummary{ID: "lead-1", Title: "主会话"}
	hub.Publish("lead-1", nil, nil, idle)

	busy := idle
	busy.SwarmBusy = true
	hub.Publish("lead-1", nil, nil, busy)

	hub.Publish("lead-1", nil, nil, idle)

	mu.Lock()
	defer mu.Unlock()
	if len(events) < 3 {
		t.Fatalf("expected at least 3 summary events, got %d: %+v", len(events), events)
	}
	if events[0].SwarmBusy || !events[1].SwarmBusy || events[2].SwarmBusy {
		t.Fatalf("unexpected swarmBusy sequence: %+v", events[:3])
	}
}

// TestMemoryBackend_SwarmBusySummary 验证会话摘要带上 SwarmBusy：主会话按钩子取值，
// helper 子会话恒为 false，且忙闲变化会通过 RepublishThreadSummary 重新发布。
func TestMemoryBackend_SwarmBusySummary(t *testing.T) {
	ctx := context.Background()
	hub := NewStreamHub(StreamHubOptions{Interval: 10 * time.Millisecond})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	leadID := "thread-lead-busy"
	beeID := "thread-bee-busy"
	backend.AddThread(wire.ThreadSummary{ID: leadID, ProjectID: "proj-busy", Title: "主会话"}, nil)
	backend.AddThread(wire.ThreadSummary{ID: beeID, ProjectID: "proj-busy", Title: "子会话"}, nil)
	backend.SetThreadRelation(beeID, leadID, true)

	var busyMu sync.Mutex
	busy := false
	setBusy := func(v bool) {
		busyMu.Lock()
		busy = v
		busyMu.Unlock()
	}
	backend.SetSwarmBusy(func(threadID string) bool {
		busyMu.Lock()
		defer busyMu.Unlock()
		return threadID == leadID && busy
	})

	setBusy(true)
	list, err := backend.ListThreads(ctx, wire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	for _, th := range list.Threads {
		switch th.ID {
		case leadID:
			if !th.SwarmBusy {
				t.Fatalf("expected lead summary to be swarm busy")
			}
		case beeID:
			if th.SwarmBusy {
				t.Fatalf("expected helper summary to be idle")
			}
		}
	}

	summary, err := backend.GetThread(ctx, leadID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if !summary.SwarmBusy {
		t.Fatalf("expected GetThread to report swarm busy")
	}

	// 忙闲标记变化时重新发布 lead 摘要，供提醒检测使用。
	var mu sync.Mutex
	var published []wire.ThreadSummary
	stop := hub.AddObserver(func(threadID, method string, params any) {
		if method != "thread.summary" || threadID != leadID {
			return
		}
		s, ok := params.(wire.ThreadSummary)
		if !ok {
			return
		}
		mu.Lock()
		published = append(published, s)
		mu.Unlock()
	})
	defer stop()

	backend.RepublishThreadSummary(leadID)
	setBusy(false)
	backend.RepublishThreadSummary(leadID)

	mu.Lock()
	defer mu.Unlock()
	if len(published) == 0 {
		t.Fatalf("expected republished lead summary")
	}
	if last := published[len(published)-1]; last.SwarmBusy {
		t.Fatalf("expected last published summary to be idle, got %+v", last)
	}
}

// TestSessionAlertDetector_BackgroundTasksDefersDone 验证回合结束后后台任务仍在跑时不发完成提醒：
// CLI 会自动续跑，要等续跑结束或后台计数归零才提醒一次。
func TestSessionAlertDetector_BackgroundTasksDefersDone(t *testing.T) {
	d := NewSessionAlertDetector()
	id := "bg-1"
	summary := wire.ThreadSummary{ID: id, Title: "后台会话", IsRunning: true}
	d.Observe(id, "thread.summary", summary)
	if count := d.RunningCount(); count != 1 {
		t.Fatalf("expected running count 1, got %d", count)
	}

	// 1. 回合结束但后台任务还在跑：不发提醒，RunningCount 只按 IsRunning 计数
	summary.IsRunning = false
	summary.BackgroundTasks = 1
	if alert, ok := d.Observe(id, "thread.summary", summary); ok {
		t.Fatalf("unexpected alert while background task is running: %+v", alert)
	}
	if count := d.RunningCount(); count != 0 {
		t.Fatalf("expected running count 0, got %d", count)
	}

	// 2. 后台任务结束时 CLI 自动续跑：再次进入运行态
	summary.IsRunning = true
	summary.BackgroundTasks = 0
	if _, ok := d.Observe(id, "thread.summary", summary); ok {
		t.Fatalf("unexpected alert when resume starts")
	}

	// 3. 续跑结束：发一次完成提醒
	summary.IsRunning = false
	alert, ok := d.Observe(id, "thread.summary", summary)
	if !ok {
		t.Fatalf("expected done alert after resume finished")
	}
	if alert.Kind != SessionAlertDone || alert.Tag != "done:"+id {
		t.Fatalf("unexpected alert: %+v", alert)
	}
	if again, ok := d.Observe(id, "thread.summary", summary); ok {
		t.Fatalf("unexpected duplicate alert: %+v", again)
	}
}

// TestSessionAlertDetector_BackgroundTasksClearedWithoutResume 验证后台计数直接归零（没有续跑）时发完成提醒。
func TestSessionAlertDetector_BackgroundTasksClearedWithoutResume(t *testing.T) {
	d := NewSessionAlertDetector()
	id := "bg-2"
	summary := wire.ThreadSummary{ID: id, Title: "后台会话", IsRunning: true}
	d.Observe(id, "thread.summary", summary)

	summary.IsRunning = false
	summary.BackgroundTasks = 1
	if _, ok := d.Observe(id, "thread.summary", summary); ok {
		t.Fatalf("unexpected alert while background task is running")
	}

	summary.BackgroundTasks = 0
	alert, ok := d.Observe(id, "thread.summary", summary)
	if !ok {
		t.Fatalf("expected done alert after background task cleared")
	}
	if alert.Kind != SessionAlertDone || alert.Tag != "done:"+id {
		t.Fatalf("unexpected alert: %+v", alert)
	}
}

// TestSessionAlertDetector_BackgroundLinger 验证只剩后台任务超过阈值时提醒一次，且同一段等待不重复。
func TestSessionAlertDetector_BackgroundLinger(t *testing.T) {
	now := time.Date(2026, 2, 3, 10, 0, 0, 0, time.Local)
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return now }

	id := "bg-linger"
	summary := wire.ThreadSummary{ID: id, Title: "后台会话", IsRunning: true}
	d.Observe(id, "thread.summary", summary)
	summary.IsRunning = false
	summary.BackgroundTasks = 1
	d.Observe(id, "thread.summary", summary)

	now = now.Add(9 * time.Minute)
	if alerts := d.CheckBackgroundLinger(backgroundLingerAlertAfter); len(alerts) != 0 {
		t.Fatalf("unexpected alert before threshold: %+v", alerts)
	}

	now = now.Add(2 * time.Minute)
	alerts := d.CheckBackgroundLinger(backgroundLingerAlertAfter)
	if len(alerts) != 1 {
		t.Fatalf("expected one linger alert, got %+v", alerts)
	}
	if alerts[0].Kind != SessionAlertBackgroundLinger || alerts[0].ThreadID != id || alerts[0].Tag != "bg-linger:"+id {
		t.Fatalf("unexpected alert: %+v", alerts[0])
	}
	if alerts[0].Title != "后台任务仍在运行" {
		t.Fatalf("unexpected title: %s", alerts[0].Title)
	}
	if !strings.Contains(alerts[0].Body, "1 个后台任务仍在运行") {
		t.Fatalf("unexpected body: %s", alerts[0].Body)
	}
	if again := d.CheckBackgroundLinger(backgroundLingerAlertAfter); len(again) != 0 {
		t.Fatalf("unexpected duplicate alert: %+v", again)
	}
}

// TestSessionAlertDetector_BackgroundLingerRealertsAfterResume 验证会话重新运行后再回到只剩后台，可以再次提醒。
func TestSessionAlertDetector_BackgroundLingerRealertsAfterResume(t *testing.T) {
	now := time.Date(2026, 2, 3, 11, 0, 0, 0, time.Local)
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return now }

	id := "bg-linger-rerun"
	summary := wire.ThreadSummary{ID: id, Title: "后台会话", IsRunning: false, BackgroundTasks: 1}
	d.Observe(id, "thread.summary", summary)
	now = now.Add(11 * time.Minute)
	if alerts := d.CheckBackgroundLinger(backgroundLingerAlertAfter); len(alerts) != 1 {
		t.Fatalf("expected first linger alert, got %+v", alerts)
	}

	// 续跑开始，后台等待状态结束
	summary.IsRunning = true
	d.Observe(id, "thread.summary", summary)
	// 再次只剩后台任务，重新开始计起
	summary.IsRunning = false
	d.Observe(id, "thread.summary", summary)
	now = now.Add(11 * time.Minute)
	if alerts := d.CheckBackgroundLinger(backgroundLingerAlertAfter); len(alerts) != 1 {
		t.Fatalf("expected second linger alert after re-entering, got %+v", alerts)
	}
}

// TestSessionAlertDetector_BackgroundLingerSkipsHelper 验证 helper 会话只剩后台任务时不提醒。
func TestSessionAlertDetector_BackgroundLingerSkipsHelper(t *testing.T) {
	now := time.Date(2026, 2, 3, 12, 0, 0, 0, time.Local)
	d := NewSessionAlertDetector()
	d.now = func() time.Time { return now }

	parentID := "lead-bg-helper"
	bee := wire.ThreadSummary{ID: "bee-bg-helper", Title: "Cove: 子任务", ParentThreadID: &parentID, BackgroundTasks: 1}
	d.Observe(bee.ID, "thread.summary", bee)

	now = now.Add(11 * time.Minute)
	if alerts := d.CheckBackgroundLinger(backgroundLingerAlertAfter); len(alerts) != 0 {
		t.Fatalf("unexpected linger alert for helper thread: %+v", alerts)
	}
}
