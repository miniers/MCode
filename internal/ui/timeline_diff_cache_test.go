package mcodeui

import (
	"strconv"
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
