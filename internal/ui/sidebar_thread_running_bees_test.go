package mcodeui

import (
	"testing"
	"time"
)

// dropTestSwarmPanelState 清理测试用 chatState 在全局 Swarm 面板状态里的缓存。
func dropTestSwarmPanelState(s *chatState) {
	swarmPanelMu.Lock()
	delete(swarmPanelStates, s)
	swarmPanelMu.Unlock()
}

// TestThreadHasRunningBees 验证主线程本回合已结束、但它派出的子代理仍在执行时，
// threadHasRunningBees 返回 true（侧栏左侧图标据此显示运行中动效）。
func TestThreadHasRunningBees(t *testing.T) {
	now := time.Now()
	s := &chatState{
		threads: []Thread{
			{ID: "lead-1", Title: "主会话", UpdatedAt: now},
			{ID: "bee-running", Title: "Aspen: 子任务", ParentThreadID: "lead-1", IsHelper: true, IsRunning: true, UpdatedAt: now},
			{ID: "lead-2", Title: "主会话 2", UpdatedAt: now},
			{ID: "bee-done", Title: "Birch: 已完成", ParentThreadID: "lead-2", IsHelper: true, LastStatus: "completed", UpdatedAt: now},
		},
	}
	defer dropTestSwarmPanelState(s)

	if !s.threadHasRunningBees("lead-1") {
		t.Fatalf("lead-1 有一个运行中的 bee，期望 true")
	}
	if s.threadHasRunningBees("lead-2") {
		t.Fatalf("lead-2 的 bee 已结束，期望 false")
	}
	if s.threadHasRunningBees("bee-running") {
		t.Fatalf("bee 自身没有子代理，期望 false")
	}
	if s.threadHasRunningBees("") {
		t.Fatalf("空 threadID 期望 false")
	}
}

// TestThreadHasRunningBees_SwarmStateFallback 验证会话列表还没刷到刚派出的 bee 时，
// 用 Swarm 面板状态兜底判定运行中。
func TestThreadHasRunningBees_SwarmStateFallback(t *testing.T) {
	s := &chatState{threads: []Thread{{ID: "lead-1", Title: "主会话"}}}
	defer dropTestSwarmPanelState(s)

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		LeadThreadID: "lead-1",
		Bees:         []SwarmBee{{ThreadID: "bee-new", Status: "running"}},
	})
	swarmPanelMu.Unlock()

	if !s.threadHasRunningBees("lead-1") {
		t.Fatalf("Swarm 面板里该主会话有运行中的 bee，期望 true")
	}
	if s.threadHasRunningBees("lead-2") {
		t.Fatalf("Swarm 面板缓存的是 lead-1，lead-2 期望 false")
	}

	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		LeadThreadID: "lead-1",
		Bees:         []SwarmBee{{ThreadID: "bee-new", Status: "completed"}},
	})
	swarmPanelMu.Unlock()

	if s.threadHasRunningBees("lead-1") {
		t.Fatalf("bee 已结束，期望 false")
	}
}
