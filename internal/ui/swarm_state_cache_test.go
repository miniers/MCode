package mcodeui

import (
	"context"
	"sync"
	"testing"
	"time"
)

// swarmCountingClient 包一层 FakeClient 统计 Swarm RPC 次数，并可让在途请求阻塞在 gate 上，
// 用来复现“加载还没回来、事件又来了很多次”的突发场景。
type swarmCountingClient struct {
	Client
	mu    sync.Mutex
	calls int
	gate  chan struct{}
}

func (c *swarmCountingClient) Swarm(ctx context.Context, threadID string) (SwarmState, error) {
	c.mu.Lock()
	c.calls++
	gate := c.gate
	c.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return c.Client.Swarm(ctx, threadID)
}

func (c *swarmCountingClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// waitSwarmCalls 等到 RPC 次数达到 want（超时即失败）。
func waitSwarmCalls(t *testing.T, c *swarmCountingClient, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.callCount() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等 Swarm RPC 次数达到 %d 超时，实际 %d 次", want, c.callCount())
}

// TestLoadSwarmCoalescesWatchBurst 验证加载在途期间的连续事件只记一笔脏：
// 多次 loadSwarm 不会叠加 RPC，在途那次结束后只补发一次。
func TestLoadSwarmCoalescesWatchBurst(t *testing.T) {
	client := &swarmCountingClient{Client: NewFakeClient(), gate: make(chan struct{})}
	s := newChatState(client)
	s.selectedThreadID = "t1"
	t.Cleanup(func() { closeEmbeddedBees(s) })

	s.loadSwarm("t1")
	waitSwarmCalls(t, client, 1)

	for i := 0; i < 20; i++ {
		s.loadSwarm("t1")
	}
	if got := client.callCount(); got != 1 {
		t.Fatalf("在途加载期间的事件应被合并，实际发了 %d 次 RPC", got)
	}

	// 放行在途请求：结束后应补发一次，把窗口内攒下的变更取回来。
	close(client.gate)
	waitSwarmCalls(t, client, 2)

	// 补发结束后没有再叠加新的 RPC。
	time.Sleep(50 * time.Millisecond)
	if got := client.callCount(); got != 2 {
		t.Fatalf("补发之后不应继续加载，实际发了 %d 次 RPC", got)
	}

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	loading, pending := hp.loading, hp.loadPending
	swarmPanelMu.Unlock()
	if loading || pending {
		t.Fatalf("加载结束后应清掉在途与脏标记，loading=%v pending=%v", loading, pending)
	}
}

// TestSwarmStateViewReusedUntilStateChanges 验证派生视图只在状态落地新版本时重建：
// 状态不变时每帧拿到的都是同一个视图（不会重复拷贝 bees）。
func TestSwarmStateViewReusedUntilStateChanges(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeEmbeddedBees(s) })
	hp := getSwarmPanelState(s)

	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead1",
		Bees: []SwarmBee{
			{ThreadID: "h1", Name: "Aspen", Status: "running", StartedAt: time.Unix(100, 0)},
			{ThreadID: "h2", Name: "Birch", Status: "completed", StartedAt: time.Unix(200, 0)},
		},
	})
	swarmPanelMu.Unlock()

	first := swarmStateViewFor(s)
	if first == nil {
		t.Fatal("派生视图不应为 nil")
	}
	for i := 0; i < 3; i++ {
		if got := swarmStateViewFor(s); got != first {
			t.Fatalf("状态没变化时第 %d 次取到的视图应复用同一个，实际换成了新视图", i)
		}
	}
	if first.running != 1 || len(first.active) != 2 || !first.pending {
		t.Fatalf("派生数据不对：running=%d active=%d pending=%v", first.running, len(first.active), first.pending)
	}
	if first.statusByThread["h1"] != "running" || first.statusByThread["h2"] != "completed" {
		t.Fatalf("statusByThread = %v", first.statusByThread)
	}
	if len(first.runningIDs) != 1 || first.runningIDs[0] != "h1" {
		t.Fatalf("runningIDs = %v", first.runningIDs)
	}

	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{Available: true, Enabled: true, LeadThreadID: "lead1"})
	swarmPanelMu.Unlock()
	second := swarmStateViewFor(s)
	if second == first {
		t.Fatal("状态落地新版本后应重建派生视图")
	}
	if len(second.active) != 0 || second.pending || second.running != 0 {
		t.Fatalf("新视图派生数据不对：%+v", second)
	}
}

// TestSwarmCapsuleDataCacheMatchesDirectFilter 验证按回合分组的胶囊结果与逐行直接过滤完全一致，
// 且同一版本里第二次取的是缓存（同一份切片）。
func TestSwarmCapsuleDataCacheMatchesDirectFilter(t *testing.T) {
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeEmbeddedBees(s) })
	hp := getSwarmPanelState(s)
	bees := []SwarmBee{
		{ThreadID: "h0", Name: "Earlier", Status: "completed", StartedAt: base.Add(-time.Minute)},
		{ThreadID: "h1", Name: "Aspen", Status: "completed", StartedAt: base.Add(time.Minute)},
		{ThreadID: "h2", Name: "Birch", Status: "running", StartedAt: base.Add(6 * time.Minute)},
		{ThreadID: "h3", Name: "Cove", Status: "running"}, // 没有 StartedAt：只落在最新回合行
	}
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{Available: true, Enabled: true, LeadThreadID: "lead1", Bees: bees})
	swarmPanelMu.Unlock()

	spans := []swarmCapsuleTurnSpan{
		{Until: base.Add(5 * time.Minute)},
		{From: base.Add(5 * time.Minute), Until: base.Add(10 * time.Minute)},
		{From: base.Add(10 * time.Minute), Latest: true},
		{}, // 非最新行且没有上界：不认领任何子线程
	}
	for _, span := range spans {
		wantLead := ""
		var want []SwarmBee
		for _, h := range getActiveSwarmBees(hp.state) {
			if swarmBeeInCapsuleSpan(h, span) {
				want = append(want, h)
			}
		}
		if len(want) > 0 {
			wantLead = "lead1"
		}

		lead, got := swarmTimelineCapsuleData(s, span)
		if lead != wantLead {
			t.Fatalf("span %+v 的 leadID = %q, want %q", span, lead, wantLead)
		}
		if len(got) != len(want) {
			t.Fatalf("span %+v 匹配到 %d 个子线程, want %d", span, len(got), len(want))
		}
		for i := range want {
			if got[i].ThreadID != want[i].ThreadID {
				t.Fatalf("span %+v 第 %d 个 = %q, want %q", span, i, got[i].ThreadID, want[i].ThreadID)
			}
		}

		// 同一版本再取一次：应直接命中缓存（同一份切片，不再重新过滤）。
		lead2, got2 := swarmTimelineCapsuleData(s, span)
		if lead2 != lead || len(got2) != len(got) {
			t.Fatalf("span %+v 第二次结果不一致: (%q, %d) vs (%q, %d)", span, lead2, len(got2), lead, len(got))
		}
		if len(got) > 0 && &got2[0] != &got[0] {
			t.Fatalf("span %+v 第二次应从缓存返回同一份切片", span)
		}
	}

	// 状态落地新版本后缓存作废，结果仍与原逻辑一致。
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{Available: true, Enabled: true, LeadThreadID: "lead1", Bees: bees[:1]})
	swarmPanelMu.Unlock()
	_, got := swarmTimelineCapsuleData(s, spans[0])
	if len(got) != 1 || got[0].ThreadID != "h0" {
		t.Fatalf("新版本下 span0 应只剩 h0, got %v", len(got))
	}
}

// TestSwarmWaitingBeeIDsFollowsRuntimeRev 验证“等用户回答”的判定按会话运行态版本缓存：
// 状态不变时不重扫，版本递增后立刻反映新的待处理请求。
func TestSwarmWaitingBeeIDsFollowsRuntimeRev(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeEmbeddedBees(s) })
	s.mu.Lock()
	s.threads = []Thread{
		{ID: "h1", HasPendingRequest: false},
		{ID: "h2", HasPendingRequest: true},
		{ID: "other", HasPendingRequest: true},
	}
	s.bumpThreadsRuntimeLocked()
	s.mu.Unlock()

	bees := []SwarmBee{{ThreadID: "h1"}, {ThreadID: "h2"}}
	if got := swarmWaitingBeeIDs(s, bees); len(got) != 1 || got[0] != "h2" {
		t.Fatalf("等待列表 = %v, want [h2]", got)
	}
	// 重复调用不改变结果（命中缓存）。
	if got := swarmWaitingBeeIDs(s, bees); len(got) != 1 || got[0] != "h2" {
		t.Fatalf("第二次等待列表 = %v, want [h2]", got)
	}

	// 模拟 Watch 推送：h1 也开始等用户回答，并递增运行态版本。
	s.mu.Lock()
	s.threads[0].HasPendingRequest = true
	s.bumpThreadsRuntimeLocked()
	s.mu.Unlock()
	if got := swarmWaitingBeeIDs(s, bees); len(got) != 2 {
		t.Fatalf("版本递增后等待列表 = %v, want h1 与 h2", got)
	}
	if !swarmBeeWaiting(s, SwarmBee{ThreadID: "h1"}) {
		t.Fatal("swarmBeeWaiting(h1) 应为 true")
	}
	if swarmBeeWaiting(s, SwarmBee{ThreadID: "h3"}) {
		t.Fatal("swarmBeeWaiting(h3) 应为 false")
	}
}

// TestSwarmTurnEndDeferredUsesCachedState 验证回合完成块的推迟判定仍然只看“子代理是否还在跑”，
// 并且不再依赖逐帧重扫全部消息（用最大的回合号做边界）。
func TestSwarmTurnEndDeferredUsesCachedState(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(func() { closeEmbeddedBees(s) })
	s.mu.Lock()
	s.messages = []Message{{ID: "a0", TurnIndex: 0}, {ID: "a1", TurnIndex: 1}, {ID: "a2", TurnIndex: 2}}
	s.messagesRev++
	s.mu.Unlock()

	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead1",
		Bees:         []SwarmBee{{ThreadID: "h1", Name: "Aspen", Status: "running"}},
	})
	swarmPanelMu.Unlock()

	if !swarmTurnEndDeferred(s, 2) {
		t.Fatal("子代理还在跑且是最后一轮，完成块应推迟")
	}
	if swarmTurnEndDeferred(s, 1) {
		t.Fatal("非最后一轮不应推迟")
	}

	// 子代理全部结束：不再推迟。
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead1",
		Bees:         []SwarmBee{{ThreadID: "h1", Name: "Aspen", Status: "completed"}},
	})
	swarmPanelMu.Unlock()
	if swarmTurnEndDeferred(s, 2) {
		t.Fatal("子代理已结束，完成块不应再推迟")
	}
}

// TestParseSwarmReportCachedMatchesParse 验证汇报解析缓存与直接解析结果一致，并且正文变化后会重新解析。
func TestParseSwarmReportCachedMatchesParse(t *testing.T) {
	hdr := swarmNoticePrefix + "Aspen 的汇报\n\n## Aspen：实现浮窗卡片 (已完成)\n- 档位：标准 · 模型：x/y\n- 汇报总结：\n正文一段。\n"
	longer := hdr + "\n## Cove：核对布局 (已完成)\n- 档位：标准\n- 汇报总结：\n核对完毕。\n"

	wantTitle, wantEntries := parseSwarmReport(hdr)
	if len(wantEntries) != 1 {
		t.Fatalf("样本应解析出 1 条汇报，实际 %d", len(wantEntries))
	}
	title, entries := parseSwarmReportCached("msg-1", hdr)
	if title != wantTitle || len(entries) != len(wantEntries) || entries[0].Name != wantEntries[0].Name {
		t.Fatalf("缓存解析结果 = (%q, %d), want (%q, %d)", title, len(entries), wantTitle, len(wantEntries))
	}
	// 同一 ID 同一长度再取一次：命中缓存，结果不变。
	_, again := parseSwarmReportCached("msg-1", hdr)
	if len(again) != 1 || again[0].Name != entries[0].Name {
		t.Fatalf("第二次解析结果 = %d 条, want 1 条", len(again))
	}

	// 正文变长必须重新解析，不能返回只有 1 条的旧结果。
	_, wantLonger := parseSwarmReport(longer)
	_, changed := parseSwarmReportCached("msg-1", longer)
	if len(wantLonger) != 2 {
		t.Fatalf("加长样本应解析出 2 条汇报，实际 %d", len(wantLonger))
	}
	if len(changed) != len(wantLonger) {
		t.Fatalf("正文变化后条目数 = %d, want %d（说明没有重新解析）", len(changed), len(wantLonger))
	}
}

// TestThrottleBeeThreadsRefresh 验证按项目节流：窗口内第二次刷新被合并，窗口后的调用恢复。
func TestThrottleBeeThreadsRefresh(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(func() {
		// 关掉会话，避免节流排下的尾随刷新在测试结束后继续发请求。
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		closeEmbeddedBees(s)
	})
	key := beeThreadsRefreshKey{state: s, projectID: "p-throttle"}

	if !throttleBeeThreadsRefresh(key, s) {
		t.Fatal("窗口内第一次刷新应放行")
	}
	for i := 0; i < 5; i++ {
		if throttleBeeThreadsRefresh(key, s) {
			t.Fatalf("窗口内第 %d 次刷新应被合并", i+2)
		}
	}
}
