package mcodeui

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestThreadChangeStatsReuseAcrossSwitches 校验改动统计按会话缓存：
// 切走再切回同一会话时直接命中上次结果，不再重跑整会话 diff。
func TestThreadChangeStatsReuseAcrossSwitches(t *testing.T) {
	fake := NewFakeClient()
	fake.SetThreadDiffStats("t1", ThreadDiffStats{Files: 1, Additions: 1, Deletions: 1})
	s, _ := buildRoot(fake)
	t.Cleanup(s.close)
	t.Cleanup(func() { releaseThreadChangeStats(s) })

	waitStats := func(threadID string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if _, _, _, ok := threadChangeStatsFor(s, threadID); ok {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("会话 %s 的改动统计未加载", threadID)
	}
	diffCallsFor := func(threadID string) int {
		n := 0
		for _, id := range fake.DiffStatsCalls() {
			if id == threadID {
				n++
			}
		}
		return n
	}

	s.ensureThreadChangeStats("t1", false)
	waitStats("t1")
	if add, del, files, ok := threadChangeStatsFor(s, "t1"); !ok || add != 1 || del != 1 || files != 1 {
		t.Fatalf("t1 统计 = %d/%d/%d ok=%v，期望 1/1/1", add, del, files, ok)
	}
	if got := diffCallsFor("t1"); got != 1 {
		t.Fatalf("t1 改动统计调用次数 = %d，期望 1", got)
	}

	s.ensureThreadChangeStats("t2", false)
	waitStats("t2")

	// 切回 t1：命中缓存，不再发 diff。
	s.ensureThreadChangeStats("t1", false)
	time.Sleep(50 * time.Millisecond)
	if got := diffCallsFor("t1"); got != 1 {
		t.Fatalf("切回 t1 后改动统计调用次数 = %d，期望仍为 1", got)
	}
	if _, _, _, ok := threadChangeStatsFor(s, "t2"); !ok {
		t.Fatalf("切走后 t2 的统计应保留")
	}
}

// TestRetainToolDiffThreadsKeepsRecent 校验切换会话时改动缓存的保留策略：
// 最近几个会话的条目保留，在途占位条目丢弃，更早会话被淘汰。
func TestRetainToolDiffThreadsKeepsRecent(t *testing.T) {
	s := &chatState{}
	s.toolDiff.entries = map[string]*toolDiffEntry{
		"t1|turn-a": {},
		"t2|turn-b": {},
		"t1|turn-x": {},
	}
	s.toolDiff.turnKey = map[string]string{"t1|#1": "t1|turn-a"}
	s.toolDiff.reviews = map[string]*toolDiffEntry{"t2|turn-b": {}, "t3|turn-c": {}}
	// t2 是上一个访问过的会话。
	s.toolDiff.threads = []string{"t2"}

	s.retainToolDiffThreadsLocked("t1")
	if _, ok := s.toolDiff.entries["t1|turn-a"]; !ok {
		t.Fatalf("当前会话的改动缓存应保留")
	}
	if _, ok := s.toolDiff.entries["t2|turn-b"]; !ok {
		t.Fatalf("上一个会话的改动缓存应保留")
	}
	if _, ok := s.toolDiff.reviews["t3|turn-c"]; ok {
		t.Fatalf("更早会话的 review 缓存应淘汰")
	}

	// 在途占位条目在切换后已作废，必须丢掉，否则会一直停在“正在读取改动”。
	s.toolDiff.entries["t1|turn-loading"] = &toolDiffEntry{loading: true}
	s.retainToolDiffThreadsLocked("t1")
	if _, ok := s.toolDiff.entries["t1|turn-loading"]; ok {
		t.Fatalf("在途占位条目应在切换时丢弃")
	}

	// 超过保留上限时淘汰最旧的会话。
	for i := 0; i < kToolDiffThreadCache; i++ {
		s.retainToolDiffThreadsLocked("t" + string(rune('a'+i)))
	}
	if _, ok := s.toolDiff.entries["t1|turn-a"]; ok {
		t.Fatalf("超出保留上限后最早的会话应淘汰")
	}
}

// TestRetainToolDiffThreadsHonorsByteBudget 校验改动缓存按估算字节数兜底：
// 超出预算时从最久未访问的会话开始丢，当前会话始终保留。
func TestRetainToolDiffThreadsHonorsByteBudget(t *testing.T) {
	s := &chatState{}
	s.toolDiff.threads = []string{"t2", "t3"}
	s.toolDiff.entries = map[string]*toolDiffEntry{
		"t2|turn-a": {bytes: kToolDiffCacheBytes + 1024},
		"t3|turn-b": {bytes: 1024},
	}
	s.retainToolDiffThreadsLocked("t1")
	if _, ok := s.toolDiff.entries["t3|turn-b"]; ok {
		t.Fatalf("最久未访问会话的缓存应先被丢弃")
	}
	if _, ok := s.toolDiff.entries["t2|turn-a"]; ok {
		t.Fatalf("超出字节预算时较大的缓存应被丢弃")
	}
	if len(s.toolDiff.threads) != 1 || s.toolDiff.threads[0] != "t1" {
		t.Fatalf("当前会话应保留，实际 %v", s.toolDiff.threads)
	}
}

// TestThreadChangeStatsCacheLimit 校验统计缓存只留最近的若干会话，且不驱逐仍在加载的条目。
// 每条缓存只有三个数字，上限放宽到 50 是为了来回切换时都能命中。
func TestThreadChangeStatsCacheLimit(t *testing.T) {
	threadChangeStatsMu.Lock()
	defer threadChangeStatsMu.Unlock()

	st := &threadChangeStats{byThread: make(map[string]*threadChangeStatsEntry)}
	base := time.Now()
	for i := 0; i < kThreadChangeStatsCache+5; i++ {
		st.byThread["t"+strconv.Itoa(i)] = &threadChangeStatsEntry{loadedAt: base.Add(time.Duration(i) * time.Second)}
	}
	trimThreadChangeStatsLocked(st, "")

	if got := len(st.byThread); got != kThreadChangeStatsCache {
		t.Fatalf("缓存条目数 = %d, 期望 %d", got, kThreadChangeStatsCache)
	}
	for i := 0; i < 5; i++ {
		if _, ok := st.byThread["t"+strconv.Itoa(i)]; ok {
			t.Fatalf("最久未加载的 t%d 应被淘汰", i)
		}
	}
	if _, ok := st.byThread["t"+strconv.Itoa(kThreadChangeStatsCache+4)]; !ok {
		t.Fatal("最新加载的条目不应被淘汰")
	}

	// 在途加载的条目没有加载时间，不参与淘汰：全部在加载时保持原样而不是死循环。
	for _, e := range st.byThread {
		e.loading = true
	}
	trimThreadChangeStatsLocked(st, "")
	if got := len(st.byThread); got != kThreadChangeStatsCache {
		t.Fatalf("全部条目在加载中时不应淘汰，实际 = %d", got)
	}
}

// TestRetainToolDiffThreadsByteDropTrimsReviewSeq 校验字节兜底淘汰旧会话时，
// 同步从 reviewSeq 与 lastSeen 移除该会话的 key，不留悬空键占用缓存名额。
func TestRetainToolDiffThreadsByteDropTrimsReviewSeq(t *testing.T) {
	s := &chatState{}
	s.toolDiff.threads = []string{"t2", "t3"}
	s.toolDiff.entries = map[string]*toolDiffEntry{
		"t2|turn-a": {bytes: kToolDiffCacheBytes + 1024},
		"t3|turn-b": {bytes: 1024},
	}
	s.toolDiff.reviews = map[string]*toolDiffEntry{
		"t3|turn-b": {},
		"t2|turn-a": {},
		"t1|turn-0": {},
	}
	s.toolDiff.reviewSeq = []string{"t3|turn-b", "t2|turn-a", "t1|turn-0"}
	s.toolDiff.lastSeen = map[string]string{
		"t2": "m2",
		"t3": "m3",
	}

	s.retainToolDiffThreadsLocked("t1")

	for _, key := range s.toolDiff.reviewSeq {
		if tid, _, _ := strings.Cut(key, "|"); tid == "t2" || tid == "t3" {
			t.Fatalf("被字节兜底淘汰的会话 %s 仍残留 reviewSeq 键 %q", tid, key)
		}
	}
	if len(s.toolDiff.reviewSeq) != 1 || s.toolDiff.reviewSeq[0] != "t1|turn-0" {
		t.Fatalf("当前会话的 reviewSeq 应保留，实际 %v", s.toolDiff.reviewSeq)
	}
	if _, ok := s.toolDiff.lastSeen["t2"]; ok {
		t.Fatal("被淘汰会话 t2 的 lastSeen 应删除")
	}
	if _, ok := s.toolDiff.lastSeen["t3"]; ok {
		t.Fatal("被淘汰会话 t3 的 lastSeen 应删除")
	}
}

// TestToolDiffSwitchBackMarksChangedTurnsStale 校验切走再切回会话时：
// 离开期间未变化轮次继续沿用缓存，新增消息涉及的轮次以及回滚场景会标为 stale。
func TestToolDiffSwitchBackMarksChangedTurnsStale(t *testing.T) {
	fake := NewFakeClient()
	initialA := []Message{
		{ID: "m1", Role: "user", TurnIndex: 1, TurnID: "turn-1"},
		{ID: "m2", Role: "tool", TurnIndex: 1, TurnID: "turn-1"},
		{ID: "m3", Role: "user", TurnIndex: 2, TurnID: "turn-2"},
		{ID: "m4", Role: "tool", TurnIndex: 2, TurnID: "turn-2"},
	}
	fake.SetMessages("tA", initialA)
	fake.SetMessages("tB", []Message{{ID: "b1", Role: "user", TurnIndex: 1, TurnID: "turn-b"}})

	s, _ := buildRoot(fake)
	t.Cleanup(s.close)

	s.mu.Lock()
	s.toolDiff.entries = map[string]*toolDiffEntry{
		"tA|turn-1": {},
		"tA|turn-2": {},
	}
	s.toolDiff.turnKey = map[string]string{
		"tA|#1": "tA|turn-1",
		"tA|#2": "tA|turn-2",
	}
	s.mu.Unlock()

	waitLoaded := func(tid string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			s.mu.Lock()
			done := s.selectedThreadID == tid && s.timelineLoadingThread == ""
			s.mu.Unlock()
			if done {
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
		t.Fatalf("会话 %s 历史未加载完成", tid)
	}

	s.selectThread("tA")
	waitLoaded("tA")

	s.selectThread("tB")
	waitLoaded("tB")

	// 离开期间 tA 第二轮多出一条 tool 消息，切回后只应使第二轮失效。
	updatedA := append(append([]Message{}, initialA...), Message{
		ID:        "m5",
		Role:      "tool",
		TurnIndex: 2,
		TurnID:    "turn-2",
	})
	fake.SetMessages("tA", updatedA)

	s.selectThread("tA")
	waitLoaded("tA")

	s.mu.Lock()
	e1 := s.toolDiff.entries["tA|turn-1"]
	e2 := s.toolDiff.entries["tA|turn-2"]
	_, hasLastSeen := s.toolDiff.lastSeen["tA"]
	s.mu.Unlock()

	if e1 == nil || e1.stale {
		t.Fatalf("未变化的第一轮不应标为 stale: %+v", e1)
	}
	if e2 == nil || !e2.stale {
		t.Fatalf("离开期间有新增消息的第二轮应标为 stale: %+v", e2)
	}
	if hasLastSeen {
		t.Fatal("切回处理完成后应清理 tA 的 lastSeen 记录")
	}

	// 再次切走并模拟 tA 回滚（切走时的最后一条消息 m5 消失），切回后该会话缓存应全部标为 stale。
	s.selectThread("tB")
	waitLoaded("tB")
	fake.SetMessages("tA", initialA[:2])

	s.selectThread("tA")
	waitLoaded("tA")

	s.mu.Lock()
	e1AfterRollback := s.toolDiff.entries["tA|turn-1"]
	s.mu.Unlock()
	if e1AfterRollback == nil || !e1AfterRollback.stale {
		t.Fatalf("历史回滚找不到切走前最后消息时，保留的条目应标为 stale: %+v", e1AfterRollback)
	}
}
