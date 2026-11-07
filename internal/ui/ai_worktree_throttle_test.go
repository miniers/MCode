package mcodeui

import (
	"testing"
	"time"
)

// aiWorktreeThrottleThreadID 是限频用例使用的会话 ID（NewFakeClient 内置的 t1）。
const aiWorktreeThrottleThreadID = "t1"

// newAIWorktreeThrottleState 搭一个选中了 t1 的 chatState；关窗时清理识别缓存。
func newAIWorktreeThrottleState(t *testing.T) *chatState {
	t.Helper()
	s := newChatState(NewFakeClient())
	s.selectThread(aiWorktreeThrottleThreadID)
	t.Cleanup(s.close)
	return s
}

// bumpAIWorktreeMessagesRev 模拟流式增量：只改消息版本，不改识别结果。
func bumpAIWorktreeMessagesRev(s *chatState) {
	s.mu.Lock()
	s.messagesRev++
	s.mu.Unlock()
}

// aiWorktreeThrottleSnapshot 记录限频相关的状态：上次排队时刻、排队长度、是否在识别、
// 是否已安排延后重绘、排队序号。
type aiWorktreeThrottleSnapshot struct {
	lastDetect time.Time
	queued     int
	pending    bool
	armed      bool
	gen        uint64
}

func aiWorktreeThrottleStateOf(s *chatState, threadID string) aiWorktreeThrottleSnapshot {
	st := getAIWorktreeState(s)
	st.mu.Lock()
	defer st.mu.Unlock()
	return aiWorktreeThrottleSnapshot{
		lastDetect: st.lastDetect[threadID],
		queued:     len(st.queue),
		pending:    st.pending[threadID],
		armed:      st.redetectArmed[threadID],
		gen:        st.gens[threadID],
	}
}

// waitAIWorktreeCondition 带超时轮询条件，用于等待后台识别结束，而不是固定 sleep。
func waitAIWorktreeCondition(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待超时：%s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// waitAIWorktreeSettled 等待会话识别完成（结果已写入且不再排队）。
func waitAIWorktreeSettled(t *testing.T, s *chatState, threadID string) {
	t.Helper()
	st := getAIWorktreeState(s)
	waitAIWorktreeCondition(t, 5*time.Second, "自建 worktree 识别结束", func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		_, known := st.found[threadID]
		return known && !st.pending[threadID] && len(st.queue) == 0
	})
}

// 已识别会话在间隔内不再随 messagesRev 变化重新识别，且只安排一个延后回调。
func TestAIWorktreeThrottle_SkipsRedetectWithinInterval(t *testing.T) {
	aiWorktreeRedetectInterval = time.Hour
	t.Cleanup(func() { aiWorktreeRedetectInterval = 0 })
	s := newAIWorktreeThrottleState(t)

	// 首次识别不受限频影响：立刻排队。
	s.aiWorktree(aiWorktreeThrottleThreadID)
	waitAIWorktreeSettled(t, s, aiWorktreeThrottleThreadID)
	before := aiWorktreeThrottleStateOf(s, aiWorktreeThrottleThreadID)
	if before.lastDetect.IsZero() {
		t.Fatal("首次识别没有记录排队时刻")
	}

	for i := 0; i < 5; i++ {
		bumpAIWorktreeMessagesRev(s)
		s.aiWorktree(aiWorktreeThrottleThreadID)
	}

	after := aiWorktreeThrottleStateOf(s, aiWorktreeThrottleThreadID)
	if !after.lastDetect.Equal(before.lastDetect) {
		t.Fatalf("间隔内不该重新识别：lastDetect %v → %v", before.lastDetect, after.lastDetect)
	}
	if after.gen != before.gen {
		t.Fatalf("间隔内不该再排队：gen %d → %d", before.gen, after.gen)
	}
	if after.queued != 0 || after.pending {
		t.Fatalf("间隔内队列不该增长：queued=%d pending=%v", after.queued, after.pending)
	}
	if !after.armed {
		t.Fatal("限频后应安排一次延后重绘")
	}
}

// 过了间隔再调用会重新排队识别。
func TestAIWorktreeThrottle_RedetectsAfterInterval(t *testing.T) {
	aiWorktreeRedetectInterval = time.Hour
	t.Cleanup(func() { aiWorktreeRedetectInterval = 0 })
	s := newAIWorktreeThrottleState(t)

	s.aiWorktree(aiWorktreeThrottleThreadID)
	waitAIWorktreeSettled(t, s, aiWorktreeThrottleThreadID)

	st := getAIWorktreeState(s)
	past := time.Now().Add(-2 * time.Hour)
	st.mu.Lock()
	st.lastDetect[aiWorktreeThrottleThreadID] = past
	st.mu.Unlock()

	bumpAIWorktreeMessagesRev(s)
	s.aiWorktree(aiWorktreeThrottleThreadID)
	waitAIWorktreeSettled(t, s, aiWorktreeThrottleThreadID)

	after := aiWorktreeThrottleStateOf(s, aiWorktreeThrottleThreadID)
	if !after.lastDetect.After(past) {
		t.Fatalf("过了间隔应重新识别：lastDetect 仍是 %v", after.lastDetect)
	}
}

// 间隔为 0 时行为与不限频一致：messagesRev 一变就重新排队，且不安排定时器。
func TestAIWorktreeThrottle_ZeroIntervalRedetectsImmediately(t *testing.T) {
	aiWorktreeRedetectInterval = 0
	s := newAIWorktreeThrottleState(t)

	s.aiWorktree(aiWorktreeThrottleThreadID)
	waitAIWorktreeSettled(t, s, aiWorktreeThrottleThreadID)
	before := aiWorktreeThrottleStateOf(s, aiWorktreeThrottleThreadID)

	bumpAIWorktreeMessagesRev(s)
	s.aiWorktree(aiWorktreeThrottleThreadID)

	after := aiWorktreeThrottleStateOf(s, aiWorktreeThrottleThreadID)
	if after.gen <= before.gen {
		t.Fatalf("间隔为 0 应立即重新排队：gen %d → %d", before.gen, after.gen)
	}
	if after.armed {
		t.Fatal("间隔为 0 时不该安排延后重绘")
	}
	waitAIWorktreeSettled(t, s, aiWorktreeThrottleThreadID)
}

// 未选中的会话不因选中会话的 messagesRev 变化而重新识别。
func TestAIWorktreeThrottle_UnselectedThreadNotRedetected(t *testing.T) {
	aiWorktreeRedetectInterval = time.Hour
	t.Cleanup(func() { aiWorktreeRedetectInterval = 0 })
	s := newAIWorktreeThrottleState(t)

	const otherThreadID = "t2"
	s.aiWorktree(otherThreadID)
	waitAIWorktreeSettled(t, s, otherThreadID)
	before := aiWorktreeThrottleStateOf(s, otherThreadID)

	bumpAIWorktreeMessagesRev(s)
	s.aiWorktree(otherThreadID)

	after := aiWorktreeThrottleStateOf(s, otherThreadID)
	if after.gen != before.gen || after.queued != 0 || after.pending {
		t.Fatalf("未选中的会话不该重新识别：gen %d → %d queued=%d pending=%v",
			before.gen, after.gen, after.queued, after.pending)
	}
	if !after.lastDetect.Equal(before.lastDetect) {
		t.Fatalf("未选中会话的 lastDetect 不该变化：%v → %v", before.lastDetect, after.lastDetect)
	}
}

// 被限频后，延后回调到期会清掉 redetectArmed，让下一帧能重新识别。
func TestAIWorktreeThrottle_RedetectTimerDisarms(t *testing.T) {
	aiWorktreeRedetectInterval = 500 * time.Millisecond
	t.Cleanup(func() { aiWorktreeRedetectInterval = 0 })
	s := newAIWorktreeThrottleState(t)

	s.aiWorktree(aiWorktreeThrottleThreadID)
	waitAIWorktreeSettled(t, s, aiWorktreeThrottleThreadID)

	bumpAIWorktreeMessagesRev(s)
	s.aiWorktree(aiWorktreeThrottleThreadID)
	if !aiWorktreeThrottleStateOf(s, aiWorktreeThrottleThreadID).armed {
		t.Fatal("限频期间应安排延后重绘")
	}

	waitAIWorktreeCondition(t, 5*time.Second, "延后回调清掉 redetectArmed", func() bool {
		return !aiWorktreeThrottleStateOf(s, aiWorktreeThrottleThreadID).armed
	})
}
