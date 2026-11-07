package mcodeui

import (
	"testing"
	"time"
)

// TestApplyThreadSummaryMarksFinished 验证完成角标的判定：
// 只有「运行中 → 空闲」且用户当时没在看的会话才亮角标，重复的空闲推送不会重新点亮。
func TestApplyThreadSummaryMarksFinished(t *testing.T) {
	s := &chatState{
		threads: []Thread{
			{ID: "watching", Title: "正在看", IsRunning: true},
			{ID: "background", Title: "后台跑", IsRunning: true},
			{ID: "idle", Title: "本来就空闲"},
		},
		threadsByProj:     map[string][]Thread{},
		selectedThreadID:  "watching",
		turnFinished:      map[string]string{},
		optimisticRunning: map[string]bool{},
	}

	s.applyThreadSummary(Thread{ID: "background", IsRunning: false, LastStatus: "completed"})
	if got := s.threadTurnFinished("background"); got != turnDoneKindDone {
		t.Fatalf("后台会话正常跑完应为普通完成角标，实际 %q", got)
	}

	s.applyThreadSummary(Thread{ID: "watching", IsRunning: false, LastStatus: "completed"})
	if got := s.threadTurnFinished("watching"); got != "" {
		t.Fatalf("当前正在看的会话完成时不应亮角标，实际 %q", got)
	}

	s.applyThreadSummary(Thread{ID: "idle", IsRunning: false})
	if got := s.threadTurnFinished("idle"); got != "" {
		t.Fatalf("本地本来就空闲的会话不应被当成刚跑完，实际 %q", got)
	}

	s.applyThreadSummary(Thread{ID: "background", IsRunning: true})
	if got := s.threadTurnFinished("background"); got != "" {
		t.Fatalf("新一轮开始后应撤掉完成角标，实际 %q", got)
	}

	// 会话列表先把状态刷成空闲、乐观运行中标记还在：仍应认作刚跑完。
	s.threads = append(s.threads, Thread{ID: "optimistic"})
	s.optimisticRunning["optimistic"] = true
	s.applyThreadSummary(Thread{ID: "optimistic", IsRunning: false, LastStatus: "completed"})
	if got := s.threadTurnFinished("optimistic"); got != turnDoneKindDone {
		t.Fatalf("乐观运行中的会话结束时也应亮完成角标，实际 %q", got)
	}
}

// TestApplyThreadSummaryMarksFailed 验证只有报错的回合才用红色角标。
func TestApplyThreadSummaryMarksFailed(t *testing.T) {
	s := &chatState{
		threads: []Thread{
			{ID: "failed", IsRunning: true},
			{ID: "stopped", IsRunning: true},
			{ID: "empty-status", IsRunning: true},
		},
		threadsByProj:     map[string][]Thread{},
		turnFinished:      map[string]string{},
		optimisticRunning: map[string]bool{},
	}

	s.applyThreadSummary(Thread{ID: "failed", IsRunning: false, LastStatus: "failed"})
	if got := s.threadTurnFinished("failed"); got != turnDoneKindFailed {
		t.Fatalf("报错结束应为失败角标，实际 %q", got)
	}

	// 被停止、终态缺失都不算错误，不该用红色。
	s.applyThreadSummary(Thread{ID: "stopped", IsRunning: false, LastStatus: "interrupted"})
	if got := s.threadTurnFinished("stopped"); got != turnDoneKindDone {
		t.Fatalf("被停止的回合应为普通完成角标，实际 %q", got)
	}
	s.applyThreadSummary(Thread{ID: "empty-status", IsRunning: false})
	if got := s.threadTurnFinished("empty-status"); got != turnDoneKindDone {
		t.Fatalf("终态缺失时应按普通完成处理，实际 %q", got)
	}
}

// TestApplyThreadSummaryWaitsForSwarm 验证主会话回复结束、但名下子线程还在跑时不算这一轮跑完：
// 推送里 SwarmBusy 为真不亮完成角标，等子线程也结束（SwarmBusy 变假）才亮。
func TestApplyThreadSummaryWaitsForSwarm(t *testing.T) {
	s := &chatState{
		threads:           []Thread{{ID: "swarm", IsRunning: true}},
		threadsByProj:     map[string][]Thread{},
		selectedThreadID:  "other",
		turnFinished:      map[string]string{},
		optimisticRunning: map[string]bool{},
	}

	s.applyThreadSummary(Thread{ID: "swarm", IsRunning: false, SwarmBusy: true, LastStatus: "completed"})
	if got := s.threadTurnFinished("swarm"); got != "" {
		t.Fatalf("子线程还在跑时不应亮完成角标，实际 %q", got)
	}

	s.applyThreadSummary(Thread{ID: "swarm", IsRunning: false, SwarmBusy: false, LastStatus: "completed"})
	if got := s.threadTurnFinished("swarm"); got != turnDoneKindDone {
		t.Fatalf("子线程也结束后应亮完成角标，实际 %q", got)
	}
}

// TestHasThreadUnreadHidesWhileBusy 验证忙碌时不把会话当作未读：主会话运行中、SwarmBusy、
// 或仍有子代理在跑都不显示未读点；全部空闲后 HasUnread 仍为真则照常显示。
func TestHasThreadUnreadHidesWhileBusy(t *testing.T) {
	s := &chatState{
		threads: []Thread{
			{ID: "bee", IsHelper: true, ParentThreadID: "parent", IsRunning: true},
		},
	}

	if !s.hasThreadUnread(Thread{ID: "idle", HasUnread: true}) {
		t.Fatal("空闲会话的未读应显示")
	}
	if s.hasThreadUnread(Thread{ID: "read", HasUnread: false}) {
		t.Fatal("没有未读标记的会话不应显示未读")
	}
	if s.hasThreadUnread(Thread{ID: "running", HasUnread: true, IsRunning: true}) {
		t.Fatal("运行中的会话不应显示未读")
	}
	if s.hasThreadUnread(Thread{ID: "swarm-busy", HasUnread: true, SwarmBusy: true}) {
		t.Fatal("子线程未结束的会话不应显示未读")
	}
	if s.hasThreadUnread(Thread{ID: "parent", HasUnread: true}) {
		t.Fatal("子代理还在跑的主会话不应显示未读")
	}
}

// TestSelectThreadClearsFinished 验证打开会话即撤掉完成角标。
func TestSelectThreadClearsFinished(t *testing.T) {
	s := newChatState(NewFakeClient())
	s.turnFinished["t1"] = turnDoneKindDone

	s.selectThread("t1")

	if got := s.threadTurnFinished("t1"); got != "" {
		t.Fatalf("打开会话后应清除完成角标，实际 %q", got)
	}
}

// TestBulkMarkReadClearsFinished 验证「全部标记为已读」同时撤掉完成角标。
func TestBulkMarkReadClearsFinished(t *testing.T) {
	fake := NewFakeClient()
	s := newChatState(fake)
	s.turnFinished["t1"] = turnDoneKindDone
	s.threads = []Thread{{ID: "t1", ProjectID: "p1", HasUnread: true}}

	s.bulkMarkRead("p1", []string{"t1"})

	// 批量标记走后台请求，等回调把标记清掉。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, marked := s.turnFinished["t1"]
		s.mu.Unlock()
		if !marked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("标记已读后应清除完成角标")
}
