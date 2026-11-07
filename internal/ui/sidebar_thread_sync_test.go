package mcodeui

import (
	"testing"
	"time"
)

// TestApplyThreadSummaryClearsStaleRunning 验证后端推送的空闲摘要能纠正侧栏里过期的运行标记：
// 没订阅 Watch 的会话（切走后结束、在 web 端被停止等）结束后，左侧动态图标必须停下。
func TestApplyThreadSummaryClearsStaleRunning(t *testing.T) {
	s := &chatState{
		threads: []Thread{
			{ID: "t1", Title: "主会话", IsRunning: true, HasPendingRequest: true},
			{ID: "t2", Title: "其他会话", IsRunning: true},
		},
		threadsByProj: map[string][]Thread{
			"p1": {{ID: "t1", Title: "主会话", IsRunning: true}},
		},
		optimisticRunning: map[string]bool{"t1": true},
	}

	s.applyThreadSummary(Thread{ID: "t1", Title: "推送里的标题", IsRunning: false, LastStatus: "interrupted"})

	if s.threads[0].IsRunning || s.threads[0].HasPendingRequest {
		t.Fatalf("t1 应被纠正为空闲: %+v", s.threads[0])
	}
	if s.threads[0].Title != "主会话" {
		t.Fatalf("标题不应被推送覆盖，实际 %q", s.threads[0].Title)
	}
	if s.threads[0].LastStatus != "interrupted" {
		t.Fatalf("应记录最近一轮终态，实际 %q", s.threads[0].LastStatus)
	}
	if !s.threads[1].IsRunning {
		t.Fatal("其他会话不应被影响")
	}
	if s.threadsByProj["p1"][0].IsRunning {
		t.Fatal("分组列表里的运行标记也应一并纠正")
	}
	if s.optimisticRunning["t1"] {
		t.Fatal("后端确认空闲后应撤销乐观运行中标记")
	}
}

// TestApplyThreadSummarySwarmBee 验证子代理结束时，即使不在顶层 s.threads 中，
// 也能及时同步 Swarm 面板中的 bee 状态并使派生视图失效。
func TestApplyThreadSummarySwarmBee(t *testing.T) {
	s := &chatState{}
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		LeadThreadID: "lead-1",
		Bees: []SwarmBee{
			{ThreadID: "bee-1", Name: "Aspen", Status: "running"},
		},
	})
	swarmPanelMu.Unlock()

	// 初始视图应该为 running
	view := swarmStateViewFor(s)
	if view == nil || view.running != 1 {
		t.Fatalf("初始派生视图应有 1 个 running bee, 实际: %+v", view)
	}

	// 收到子代理完成的摘要推送
	s.applyThreadSummary(Thread{ID: "bee-1", IsHelper: true, IsRunning: false, LastStatus: "completed"})

	// 验证面板状态已转为 completed
	swarmPanelMu.Lock()
	hStatus := hp.state.Bees[0].Status
	swarmPanelMu.Unlock()
	if hStatus != "completed" {
		t.Fatalf("bee 状态应被更新为 completed, 实际 %q", hStatus)
	}

	// 验证派生视图重建后 running 为 0
	newView := swarmStateViewFor(s)
	if newView == nil || newView.running != 0 {
		t.Fatalf("重建后的派生视图 running 应为 0, 实际: %+v", newView)
	}
}

// newThreadSyncTestState 构造一个已完成初始化、已知一个会话的侧栏状态，供推送同步用例使用。
// 已知会话是 t1（标题「旧标题」），当前选中它。
func newThreadSyncTestState(t *testing.T) (*chatState, *FakeClient) {
	t.Helper()
	now := time.Now()
	projects := []Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}}
	known := Thread{ID: "t1", ProjectID: "p1", Title: "旧标题", UpdatedAt: now}
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(projects, map[string][]Thread{"p1": {known}})

	s := newChatState(fake)
	s.initialized = true
	s.projects = projects
	s.threadsByProj["p1"] = []Thread{known}
	s.threads = []Thread{known}
	s.selectedThreadID = "t1"
	return s, fake
}

// waitThreadSync 轮询等待条件成立：列表重拉在后台 goroutine 中执行。
func waitThreadSync(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", what)
}

// hasThread 判断列表里是否已有指定 ID 的会话（调用方持有 s.mu）。
func hasThread(list []Thread, id string) bool {
	for _, item := range list {
		if item.ID == id {
			return true
		}
	}
	return false
}

// TestApplyThreadSummaryAddsUnknownThread 验证 web 端新建的会话经推送后无需重开窗口就出现在侧栏，
// 且不会把当前选中的会话切走。
func TestApplyThreadSummaryAddsUnknownThread(t *testing.T) {
	s, fake := newThreadSyncTestState(t)
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{"p1": {
			{ID: "t1", ProjectID: "p1", Title: "旧标题", UpdatedAt: now},
			{ID: "t2", ProjectID: "p1", Title: "web 新建会话", UpdatedAt: now},
		}},
	)

	s.applyThreadSummary(Thread{ID: "t2", ProjectID: "p1", Title: "web 新建会话"})

	waitThreadSync(t, "新会话出现在分组列表和总列表", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return hasThread(s.threadsByProj["p1"], "t2") && hasThread(s.threads, "t2")
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.selectedThreadID != "t1" {
		t.Fatalf("刷新列表不应切换选中会话，实际 %q", s.selectedThreadID)
	}
}

// TestApplyThreadSummaryRefreshesUnknownThreadOnce 验证列表一直不返回某会话时，
// 同一 ID 的连续摘要只触发一次列表重拉（流式期间不会反复拉取）。
func TestApplyThreadSummaryRefreshesUnknownThreadOnce(t *testing.T) {
	s, fake := newThreadSyncTestState(t)

	for i := 0; i < 3; i++ {
		s.applyThreadSummary(Thread{ID: "ghost", ProjectID: "p1", Title: "幽灵会话"})
	}
	waitThreadSync(t, "第一次摘要触发一次列表重拉", func() bool {
		return fake.ThreadsCalls("p1") == 1
	})

	for i := 0; i < 3; i++ {
		s.applyThreadSummary(Thread{ID: "ghost", ProjectID: "p1", Title: "幽灵会话"})
	}
	time.Sleep(100 * time.Millisecond)
	if got := fake.ThreadsCalls("p1"); got != 1 {
		t.Fatalf("同一未知会话应只重拉一次列表，实际调用 %d 次", got)
	}
}

// TestApplyThreadSummarySkipsHelperThreads 验证子线程（helper/bee）的未知摘要不触发列表重拉，
// 子线程由 refreshBeeThreads 单独处理。
func TestApplyThreadSummarySkipsHelperThreads(t *testing.T) {
	s, fake := newThreadSyncTestState(t)

	s.applyThreadSummary(Thread{ID: "bee-1", ProjectID: "p1", IsHelper: true, IsRunning: false})
	s.applyThreadSummary(Thread{ID: "bee-2", ProjectID: "p1", ParentThreadID: "t1", IsRunning: false})

	time.Sleep(100 * time.Millisecond)
	if got := fake.ThreadsCalls("p1"); got != 0 {
		t.Fatalf("子线程摘要不应触发列表重拉，实际调用 %d 次", got)
	}
}

// TestApplyThreadSummaryRefreshesRenamedThread 验证标题变化的摘要触发一次列表重拉，
// 侧栏标题随后端返回的新标题更新。
func TestApplyThreadSummaryRefreshesRenamedThread(t *testing.T) {
	s, fake := newThreadSyncTestState(t)
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/test"}},
		map[string][]Thread{"p1": {{ID: "t1", ProjectID: "p1", Title: "后端生成标题", UpdatedAt: now}}},
	)

	s.applyThreadSummary(Thread{ID: "t1", ProjectID: "p1", Title: "后端生成标题"})

	waitThreadSync(t, "侧栏标题更新为后端返回的标题", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		list := s.threadsByProj["p1"]
		return len(list) == 1 && list[0].Title == "后端生成标题"
	})

	// 标题已经同步，同一标题的重复摘要不再重拉。
	before := fake.ThreadsCalls("p1")
	s.applyThreadSummary(Thread{ID: "t1", ProjectID: "p1", Title: "后端生成标题"})
	time.Sleep(100 * time.Millisecond)
	if got := fake.ThreadsCalls("p1"); got != before {
		t.Fatalf("标题未变化时不应重拉列表，实际从 %d 次变为 %d 次", before, got)
	}
}

// TestApplyThreadSummarySkipsWhenNotInitialized 验证侧栏还没初始化时不触发列表重拉。
func TestApplyThreadSummarySkipsWhenNotInitialized(t *testing.T) {
	s, fake := newThreadSyncTestState(t)
	s.mu.Lock()
	s.initialized = false
	s.mu.Unlock()

	s.applyThreadSummary(Thread{ID: "t2", ProjectID: "p1", Title: "web 新建会话"})

	time.Sleep(100 * time.Millisecond)
	if got := fake.ThreadsCalls("p1"); got != 0 {
		t.Fatalf("未初始化时不应重拉列表，实际调用 %d 次", got)
	}
}
