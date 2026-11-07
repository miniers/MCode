package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// TestSwarmCapsuleRowsInsertedPerTurn 验证子线程胶囊行按段归位：上一段的胶囊行插在下一条
// 用户消息之前（即上一段所有条目之后），最后一段的胶囊行仍留在时间线末尾。
func TestSwarmCapsuleRowsInsertedPerTurn(t *testing.T) {
	turn2Start := capsuleTestTime(t, "2026-01-01T10:05:00Z")
	turn3Start := capsuleTestTime(t, "2026-01-01T10:10:00Z")
	items := []timelineItem{
		{kind: timelineItemSingle, id: "u1", turnIndex: 0, msg: Message{ID: "u1", Role: "user", CreatedAt: "2026-01-01T10:00:00Z"}},
		{kind: timelineItemSingle, id: "a1", turnIndex: 0, msg: Message{ID: "a1", Role: "assistant"}},
		{kind: timelineItemSingle, id: "u2", turnIndex: 1, msg: Message{ID: "u2", Role: "user", CreatedAt: "2026-01-01T10:05:00Z"}},
		{kind: timelineItemSingle, id: "a2", turnIndex: 1, msg: Message{ID: "a2", Role: "assistant"}},
		{kind: timelineItemSingle, id: "u3", turnIndex: 2, msg: Message{ID: "u3", Role: "user", CreatedAt: "2026-01-01T10:10:00Z"}},
		{kind: timelineItemSingle, id: "a3", turnIndex: 2, msg: Message{ID: "a3", Role: "assistant"}},
	}
	plans := []resolvedPlan{{ID: "p1", Plan: "计划正文", Approved: true, TurnIndex: 1}}

	got := attachResolvedPlans(items, plans)
	wantIDs := []string{
		"u1", "a1",
		capsuleTestRowID("u2"),
		"u2", "a2", "resolved-plan-p1",
		capsuleTestRowID("u3"),
		"u3", "a3",
		swarmDispatchRowID,
		swarmCapsuleRowID,
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("时间线条目数 = %d, want %d: %v", len(got), len(wantIDs), capsuleTestItemIDs(got))
	}
	for i, want := range wantIDs {
		if got[i].id != want {
			t.Fatalf("第 %d 项 = %q, want %q（全部: %v）", i, got[i].id, want, capsuleTestItemIDs(got))
		}
	}

	// 第一段行不设下界，上界是第二个边界（第二条用户消息）的时间。
	first := capsuleTestItem(t, got, capsuleTestRowID("u2"))
	if first.turnIndex != 0 || first.capsuleSpan.Latest {
		t.Fatalf("第一段行 turnIndex=%d latest=%v", first.turnIndex, first.capsuleSpan.Latest)
	}
	if !first.capsuleSpan.From.IsZero() || !first.capsuleSpan.Until.Equal(turn2Start) {
		t.Fatalf("第一段行区间 = [%v, %v), want [零值, %v)", first.capsuleSpan.From, first.capsuleSpan.Until, turn2Start)
	}

	// 中间段行从上一个边界的时间到下一个边界的时间。
	second := capsuleTestItem(t, got, capsuleTestRowID("u3"))
	if !second.capsuleSpan.From.Equal(turn2Start) || !second.capsuleSpan.Until.Equal(turn3Start) {
		t.Fatalf("第二段行区间 = [%v, %v), want [%v, %v)", second.capsuleSpan.From, second.capsuleSpan.Until, turn2Start, turn3Start)
	}

	// 末尾行代表最后一段：下界是最后一个边界的时间，不设上界。
	last := capsuleTestItem(t, got, swarmCapsuleRowID)
	if !last.capsuleSpan.Latest || !last.capsuleSpan.From.Equal(turn3Start) || !last.capsuleSpan.Until.IsZero() {
		t.Fatalf("末尾行区间 = [%v, %v) latest=%v, want [%v, +∞)", last.capsuleSpan.From, last.capsuleSpan.Until, last.capsuleSpan.Latest, turn3Start)
	}
}

// TestSwarmTimelineCapsuleDataFiltersByTurn 验证按回合过滤子线程：每个子线程只出现在一行里，
// 没有 StartedAt 的子线程落到末尾（最新回合）行，非最新行缺少上界时不认领任何子线程。
func TestSwarmTimelineCapsuleDataFiltersByTurn(t *testing.T) {
	base := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	turn2Start := base.Add(5 * time.Minute)

	s := newChatState(NewFakeClient())
	s.selectedThreadID = "lead1"
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.setStateLocked(SwarmState{
		Available:    true,
		Enabled:      true,
		LeadThreadID: "lead1",
		Bees: []SwarmBee{
			// 早于第一回合启动的子线程归第一回合行（该行不设下界）。
			{ThreadID: "h0", Name: "Earlier", Status: "completed", StartedAt: base.Add(-time.Minute)},
			{ThreadID: "h1", Name: "Aspen", Status: "completed", StartedAt: base.Add(time.Minute)},
			{ThreadID: "h2", Name: "Cove", Status: "running", StartedAt: turn2Start.Add(time.Minute)},
			// 没有开始时间的子线程无法按时间归属，兜底落到末尾行。
			{ThreadID: "h3", Name: "Ives", Status: "completed"},
		},
	})
	swarmPanelMu.Unlock()

	leadID, firstBees := swarmTimelineCapsuleData(s, swarmCapsuleTurnSpan{Until: turn2Start})
	if leadID != "lead1" {
		t.Fatalf("leadID = %q, want lead1", leadID)
	}
	if ids := capsuleTestBeeIDs(firstBees); len(ids) != 2 || ids[0] != "h0" || ids[1] != "h1" {
		t.Fatalf("第一回合行子线程 = %v, want [h0 h1]", ids)
	}

	_, lastBees := swarmTimelineCapsuleData(s, swarmCapsuleTurnSpan{From: turn2Start, Latest: true})
	if ids := capsuleTestBeeIDs(lastBees); len(ids) != 2 || ids[0] != "h2" || ids[1] != "h3" {
		t.Fatalf("末尾行子线程 = %v, want [h2 h3]", ids)
	}

	// 上界为零的非最新行不认领任何子线程，避免与其它行重复。
	if _, bees := swarmTimelineCapsuleData(s, swarmCapsuleTurnSpan{}); len(bees) != 0 {
		t.Fatalf("无上界的非最新行子线程 = %v, want 空", capsuleTestBeeIDs(bees))
	}
}

// TestInsertSwarmCapsuleRowsWithoutTimestamps 验证用户消息缺少可用时间戳时边界向后顺延：
// 每个子线程仍能归到某一行，且不会出现两行抢同一个子线程（中间行退化为空区间）。
func TestInsertSwarmCapsuleRowsWithoutTimestamps(t *testing.T) {
	turn3Start := capsuleTestTime(t, "2026-01-01T10:00:00Z")
	items := []timelineItem{
		{kind: timelineItemSingle, id: "u1", turnIndex: 0, msg: Message{ID: "u1", Role: "user"}},
		{kind: timelineItemSingle, id: "u2", turnIndex: 1, msg: Message{ID: "u2", Role: "user"}},
		{kind: timelineItemSingle, id: "u3", turnIndex: 2, msg: Message{ID: "u3", Role: "user", CreatedAt: "2026-01-01T10:00:00Z"}},
	}
	got := insertSwarmCapsuleRows(items)

	// 第一段行没有下界，上界顺延到后面第一个可用时间（第三回合的开始时间）。
	first := capsuleTestItem(t, got, capsuleTestRowID("u2"))
	if !first.capsuleSpan.From.IsZero() || !first.capsuleSpan.Until.Equal(turn3Start) {
		t.Fatalf("第一段行区间 = [%v, %v), want [零值, %v)", first.capsuleSpan.From, first.capsuleSpan.Until, turn3Start)
	}
	// 第二段行的上下界都顺延到同一点，退化为空区间，不与第一段行抢子线程。
	second := capsuleTestItem(t, got, capsuleTestRowID("u3"))
	if !second.capsuleSpan.From.Equal(turn3Start) || !second.capsuleSpan.Until.Equal(turn3Start) {
		t.Fatalf("第二段行区间 = [%v, %v), want [%v, %v)", second.capsuleSpan.From, second.capsuleSpan.Until, turn3Start, turn3Start)
	}
}

// TestSwarmCapsuleRowsStewardLead 验证总管会话作为 Lead 时胶囊按回合归位：steward_assign_task
// 派出的子代理只出现在派活的那个回合里，完成后的汇报消息不再另起一条独立的时间线行。
func TestSwarmCapsuleRowsStewardLead(t *testing.T) {
	const stewardID = "steward-main"
	dispatchAt := capsuleTestTime(t, "2026-01-01T10:03:00Z")
	nextTurnStart := capsuleTestTime(t, "2026-01-01T10:10:00Z")
	st := SwarmState{
		LeadThreadID: stewardID,
		Available:    true,
		Enabled:      true,
		Bees: []SwarmBee{{
			ThreadID:  "bee1",
			Name:      "Aspen",
			Task:      "执行：修解析器",
			Status:    "completed",
			StartedAt: dispatchAt,
			Summary:   "解析器已修复。",
		}},
	}
	fake := newStewardFakeClient(stewardID)
	fake.SetSwarmState(stewardID, st)
	s := newChatState(fake)
	s.selectedThreadID = stewardID
	hp := getSwarmPanelState(s)
	swarmPanelMu.Lock()
	hp.loadedFor = stewardID
	hp.setStateLocked(st)
	swarmPanelMu.Unlock()

	report := Message{
		ID:        "r1",
		Role:      "user",
		TurnIndex: 1,
		CreatedAt: "2026-01-01T10:05:00Z",
		Text:      "[编排] 子代理汇报：Aspen 已结束工作。\n\n## Aspen：执行：修解析器 (已完成)\n- 合并结果：已合并\n- 汇报总结：解析器已修复。",
	}
	msgs := []Message{
		{ID: "u1", Role: "user", TurnIndex: 0, CreatedAt: "2026-01-01T10:00:00Z", Text: "把解析器修一下"},
		{ID: "a1", Role: "assistant", TurnIndex: 0, Text: "派 Aspen 去做"},
		report,
		{ID: "u2", Role: "user", TurnIndex: 2, CreatedAt: "2026-01-01T10:10:00Z", Text: "接着改词法器"},
	}
	items := attachResolvedPlans(buildTimelineItems(msgs), nil)

	reportAt := capsuleTestTime(t, "2026-01-01T10:05:00Z")
	// 子代理在派活的段里有一枚胶囊：该段到汇报行为止，汇报行之后的那段才是下一回合。
	dispatchTurn := capsuleTestItem(t, items, capsuleTestRowID("r1"))
	if !dispatchTurn.capsuleSpan.From.IsZero() || !dispatchTurn.capsuleSpan.Until.Equal(reportAt) {
		t.Fatalf("派活段行区间 = [%v, %v), want [零值, %v)", dispatchTurn.capsuleSpan.From, dispatchTurn.capsuleSpan.Until, reportAt)
	}
	leadID, bees := swarmTimelineCapsuleData(s, dispatchTurn.capsuleSpan)
	if leadID != stewardID {
		t.Fatalf("胶囊行 leadID = %q, want %q", leadID, stewardID)
	}
	if ids := capsuleTestBeeIDs(bees); len(ids) != 1 || ids[0] != "bee1" {
		t.Fatalf("派活回合行子线程 = %v, want [bee1]", ids)
	}
	// 汇报行之后的下一段从汇报时间到下一回合开始，不再包含这枚子代理。
	nextSegment := capsuleTestItem(t, items, capsuleTestRowID("u2"))
	if !nextSegment.capsuleSpan.From.Equal(reportAt) || !nextSegment.capsuleSpan.Until.Equal(nextTurnStart) {
		t.Fatalf("汇报行后段区间 = [%v, %v), want [%v, %v)", nextSegment.capsuleSpan.From, nextSegment.capsuleSpan.Until, reportAt, nextTurnStart)
	}
	// 末尾行不重复认领这枚子代理。
	last := capsuleTestItem(t, items, swarmCapsuleRowID)
	if _, lastBees := swarmTimelineCapsuleData(s, last.capsuleSpan); len(lastBees) != 0 {
		t.Fatalf("末尾行子线程 = %v, want 空", capsuleTestBeeIDs(lastBees))
	}

	// 汇报消息已经有胶囊展示，不再另起一条汇报行。
	tt := ui.NewTester(func(c *ui.Context) {
		s.init(c)
		// 后台加载会改写面板状态，每帧显式复位，保证渲染看到的就是这个 Lead。
		swarmPanelMu.Lock()
		hp.loadedFor = stewardID
		hp.setStateLocked(st)
		swarmPanelMu.Unlock()
		renderSwarmReportRow(c, s, report)
	}, 800, 600)
	tt.Frame()
	if tt.HasText("子代理汇报详情") {
		t.Fatalf("总管会话里汇报另起了一行: %v", tt.Texts())
	}
}

// TestSwarmCapsuleRowsSplitByReportRows 验证 Swarm 汇报行也切分胶囊段：同一个用户回合里多轮派出的
// 子线程分别留在各自汇报行之前，只有最后一段的子线程出现在时间线末尾。
func TestSwarmCapsuleRowsSplitByReportRows(t *testing.T) {
	t1 := capsuleTestTime(t, "2026-01-01T10:01:00Z")
	t2 := capsuleTestTime(t, "2026-01-01T10:02:00Z")
	t3 := capsuleTestTime(t, "2026-01-01T10:03:00Z")
	t4 := capsuleTestTime(t, "2026-01-01T10:04:00Z")
	t5 := capsuleTestTime(t, "2026-01-01T10:05:00Z")
	items := []timelineItem{
		{kind: timelineItemSingle, id: "u1", turnIndex: 0, msg: Message{ID: "u1", Role: "user", CreatedAt: "2026-01-01T10:00:00Z"}},
		{kind: timelineItemSingle, id: "a1", turnIndex: 0, msg: Message{ID: "a1", Role: "assistant"}},
		{kind: timelineItemSwarmReport, id: "r1", turnIndex: 0, msg: Message{ID: "r1", Role: "user", CreatedAt: "2026-01-01T10:02:00Z"}},
		{kind: timelineItemSingle, id: "a2", turnIndex: 0, msg: Message{ID: "a2", Role: "assistant"}},
		{kind: timelineItemSwarmReport, id: "r2", turnIndex: 0, msg: Message{ID: "r2", Role: "user", CreatedAt: "2026-01-01T10:04:00Z"}},
		{kind: timelineItemSingle, id: "a3", turnIndex: 0, msg: Message{ID: "a3", Role: "assistant"}},
	}
	got := appendSwarmCapsuleRow(insertSwarmCapsuleRows(items))

	// 两个汇报行前各插一条胶囊行，末尾一条 Latest 行。
	wantIDs := []string{
		"u1", "a1",
		capsuleTestRowID("r1"),
		"r1", "a2",
		capsuleTestRowID("r2"),
		"r2", "a3",
		swarmCapsuleRowID,
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("时间线条目数 = %d, want %d: %v", len(got), len(wantIDs), capsuleTestItemIDs(got))
	}
	for i, want := range wantIDs {
		if got[i].id != want {
			t.Fatalf("第 %d 项 = %q, want %q（全部: %v）", i, got[i].id, want, capsuleTestItemIDs(got))
		}
	}
	// 同一帧内所有胶囊行的 Key 必须互不相同。
	seen := make(map[string]bool, 3)
	for _, item := range got {
		if item.kind != timelineItemSwarmCapsules {
			continue
		}
		if seen[item.id] {
			t.Fatalf("胶囊行 Key 重复: %q（全部: %v）", item.id, capsuleTestItemIDs(got))
		}
		seen[item.id] = true
	}

	first := capsuleTestItem(t, got, capsuleTestRowID("r1"))
	if !first.capsuleSpan.From.IsZero() || !first.capsuleSpan.Until.Equal(t2) {
		t.Fatalf("第一段行区间 = [%v, %v), want [零值, %v)", first.capsuleSpan.From, first.capsuleSpan.Until, t2)
	}
	second := capsuleTestItem(t, got, capsuleTestRowID("r2"))
	if !second.capsuleSpan.From.Equal(t2) || !second.capsuleSpan.Until.Equal(t4) {
		t.Fatalf("第二段行区间 = [%v, %v), want [%v, %v)", second.capsuleSpan.From, second.capsuleSpan.Until, t2, t4)
	}
	last := capsuleTestItem(t, got, swarmCapsuleRowID)
	if !last.capsuleSpan.Latest || !last.capsuleSpan.From.Equal(t4) || !last.capsuleSpan.Until.IsZero() {
		t.Fatalf("末尾行区间 = [%v, %v) latest=%v, want [%v, +∞)", last.capsuleSpan.From, last.capsuleSpan.Until, last.capsuleSpan.Latest, t4)
	}

	// 按 StartedAt 验证归属：每枚子线程只落在一行里。
	spans := []swarmCapsuleTurnSpan{first.capsuleSpan, second.capsuleSpan, last.capsuleSpan}
	cases := []struct {
		name      string
		bee       SwarmBee
		wantIndex int
	}{
		{"t1", SwarmBee{ThreadID: "b1", StartedAt: t1}, 0},
		{"t3", SwarmBee{ThreadID: "b2", StartedAt: t3}, 1},
		{"t5", SwarmBee{ThreadID: "b3", StartedAt: t5}, 2},
	}
	for _, tc := range cases {
		for i, span := range spans {
			want := i == tc.wantIndex
			if got := swarmBeeInCapsuleSpan(tc.bee, span); got != want {
				t.Fatalf("%s 的子线程归入区间 [%v, %v) = %v, want %v", tc.name, span.From, span.Until, got, want)
			}
		}
	}
}

// TestSwarmCapsuleHideReportedBees 验证普通会话里已汇报的子代理不再显示胶囊行胶囊：
// 同名子代理后一轮重新派出（启动晚于旧汇报）时仍要显示，总管会话的胶囊行不受影响。
func TestSwarmCapsuleHideReportedBees(t *testing.T) {
	startedAt := capsuleTestTime(t, "2026-01-01T10:01:00Z")
	report := Message{
		ID:        "r1",
		Role:      "user",
		CreatedAt: "2026-01-01T10:03:00Z",
		Text:      "[编排] 子代理汇报：Birch 已结束工作。\n\n## Birch：执行：修解析器 (已完成)\n- 合并结果：已合并\n- 汇报总结：解析器已修复。",
	}
	bees := []SwarmBee{
		{ThreadID: "bee1", Name: "Birch", Status: "completed", StartedAt: startedAt},
		{ThreadID: "bee2", Name: "Cove", Status: "running", StartedAt: startedAt},
	}
	newLead := func() *chatState {
		s := newChatState(NewFakeClient())
		s.selectedThreadID = "lead1"
		s.messages = []Message{report}
		s.messagesRev++
		hp := getSwarmPanelState(s)
		swarmPanelMu.Lock()
		hp.setStateLocked(SwarmState{Available: true, Enabled: true, LeadThreadID: "lead1", Bees: bees})
		swarmPanelMu.Unlock()
		return s
	}

	// (a) Birch 的汇报晚于它启动：胶囊行不再显示 Birch，未被汇报的 Cove 保留。
	s := newLead()
	kept := swarmCapsuleHideReported(s, bees)
	if ids := capsuleTestBeeIDs(kept); len(ids) != 1 || ids[0] != "bee2" {
		t.Fatalf("过滤后的子线程 = %v, want [bee2]", ids)
	}
	// 没有子线程被过滤时原样返回入参切片，不为每帧多分配。
	if got := swarmCapsuleHideReported(s, bees[1:]); len(got) != 1 || &got[0] != &bees[1] {
		t.Fatalf("全都未汇报时没有原样返回入参切片")
	}

	// (b) 同名子代理在汇报之后重新派出：不被旧汇报误藏。
	later := []SwarmBee{{
		ThreadID:  "bee3",
		Name:      "Birch",
		Status:    "running",
		StartedAt: capsuleTestTime(t, "2026-01-01T10:04:00Z"),
	}}
	if got := swarmCapsuleHideReported(s, later); len(got) != 1 || got[0].ThreadID != "bee3" {
		t.Fatalf("汇报后重新派出的同名子代理 = %v, want [bee3]", capsuleTestBeeIDs(got))
	}

	// (c) 总管会话的汇报行已被同一枚胶囊吸收，胶囊行不再过滤。
	const stewardID = "steward-main"
	fake := newStewardFakeClient(stewardID)
	fake.SetSwarmState(stewardID, SwarmState{LeadThreadID: stewardID, Available: true, Enabled: true, Bees: bees})
	steward := newChatState(fake)
	steward.selectedThreadID = stewardID
	steward.messages = []Message{report}
	steward.messagesRev++
	hp := getSwarmPanelState(steward)
	swarmPanelMu.Lock()
	hp.loadedFor = stewardID
	hp.setStateLocked(SwarmState{LeadThreadID: stewardID, Available: true, Enabled: true, Bees: bees})
	swarmPanelMu.Unlock()
	if got := swarmCapsuleHideReported(steward, bees); len(got) != len(bees) {
		t.Fatalf("总管会话的子线程 = %v, want 全部保留", capsuleTestBeeIDs(got))
	}
}

// capsuleTestRowID 返回边界条目 id 对应的胶囊行 Key。
func capsuleTestRowID(boundaryID string) string {
	return swarmCapsuleRowID + "-b-" + boundaryID
}

// capsuleTestItem 按 id 取时间线条目，取不到时直接失败。
func capsuleTestItem(t *testing.T, items []timelineItem, id string) timelineItem {
	t.Helper()
	for _, item := range items {
		if item.id == id {
			return item
		}
	}
	t.Fatalf("找不到时间线条目 %q（全部: %v）", id, capsuleTestItemIDs(items))
	return timelineItem{}
}

// capsuleTestItemIDs 返回时间线条目的 id 列表，便于失败时打印实际顺序。
func capsuleTestItemIDs(items []timelineItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.id)
	}
	return ids
}

// capsuleTestBeeIDs 返回子线程的会话 ID 列表，便于比较归属结果。
func capsuleTestBeeIDs(bees []SwarmBee) []string {
	ids := make([]string, 0, len(bees))
	for _, h := range bees {
		ids = append(ids, h.ThreadID)
	}
	return ids
}

// capsuleTestTime 解析测试用的 RFC3339 时间。
func capsuleTestTime(t *testing.T, value string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("解析时间 %q: %v", value, err)
	}
	return at
}
