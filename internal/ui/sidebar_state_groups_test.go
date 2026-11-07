package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// resetSidebarStateGroupLatch 清空包级闩锁状态，避免用例互相影响。
func resetSidebarStateGroupLatch() {
	l := &sidebarStateGroupLatchState
	l.mu.Lock()
	l.frameHovered = false
	l.pointerInside = false
	l.groups = nil
	l.mu.Unlock()
}

// stateGroupBusy 返回按 ID 集合判定「忙」的回调，模拟 threadStillBusy。
func stateGroupBusy(ids ...string) func(Thread) bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return func(th Thread) bool { return set[th.ID] }
}

// TestSidebarStateGroupOfPriority 验证归类优先级：待处理优先于运行中，运行中优先于空闲。
func TestSidebarStateGroupOfPriority(t *testing.T) {
	cases := []struct {
		name string
		th   Thread
		busy bool
		want sidebarStateGroup
	}{
		{"待处理且仍在跑", Thread{ID: "t1", HasPendingRequest: true, IsRunning: true}, true, sidebarStateGroupNeedsYou},
		{"待处理但没跑", Thread{ID: "t2", HasPendingRequest: true}, false, sidebarStateGroupNeedsYou},
		{"仅运行中", Thread{ID: "t3", IsRunning: true}, true, sidebarStateGroupRunning},
		{"子代理还在跑", Thread{ID: "t4"}, true, sidebarStateGroupRunning},
		{"完全空闲", Thread{ID: "t5"}, false, sidebarStateGroupNone},
	}
	for _, tc := range cases {
		if got := sidebarStateGroupOf(tc.th, tc.busy); got != tc.want {
			t.Fatalf("%s: group = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSplitSidebarStateGroupsKeepsOrder 验证三段各自保持 active 的原顺序，且不修改入参。
func TestSplitSidebarStateGroupsKeepsOrder(t *testing.T) {
	active := []Thread{
		{ID: "a", HasPendingRequest: true},
		{ID: "b"},
		{ID: "c"},
		{ID: "d", HasPendingRequest: true},
		{ID: "e"},
		{ID: "f"},
	}
	original := append([]Thread(nil), active...)

	needsYou, running, rest := splitSidebarStateGroups(active, stateGroupBusy("b", "e"))

	assertStateGroupIDs(t, "等你处理", needsYou, []string{"a", "d"})
	assertStateGroupIDs(t, "运行中", running, []string{"b", "e"})
	assertStateGroupIDs(t, "其余", rest, []string{"c", "f"})

	if len(active) != len(original) {
		t.Fatalf("active 长度被修改: %d, want %d", len(active), len(original))
	}
	for i := range original {
		if active[i].ID != original[i].ID || active[i].HasPendingRequest != original[i].HasPendingRequest {
			t.Fatalf("active[%d] 被修改: %+v, want %+v", i, active[i], original[i])
		}
	}
}

// TestSplitSidebarStateGroupsNoPending 验证没有待处理会话时「等你处理」段为空。
func TestSplitSidebarStateGroupsNoPending(t *testing.T) {
	active := []Thread{{ID: "a"}, {ID: "b"}}

	needsYou, running, rest := splitSidebarStateGroups(active, stateGroupBusy("a"))
	if len(needsYou) != 0 {
		t.Fatalf("等你处理段应为空, got %d", len(needsYou))
	}
	assertStateGroupIDs(t, "运行中", running, []string{"a"})
	assertStateGroupIDs(t, "其余", rest, []string{"b"})

	// 空列表不产生任何分段。
	needsYou, running, rest = splitSidebarStateGroups(nil, nil)
	if len(needsYou)+len(running)+len(rest) != 0 {
		t.Fatalf("空列表不应产生分组: %d/%d/%d", len(needsYou), len(running), len(rest))
	}
}

// TestSidebarStateGroupLatchHoldsRowWhilePointerInside 验证指针停在行上时状态变化不换组，
// 指针离开后的下一帧才归位。
func TestSidebarStateGroupLatchHoldsRowWhilePointerInside(t *testing.T) {
	resetSidebarStateGroupLatch()
	latch := &sidebarStateGroupLatchState
	active := []Thread{{ID: "t1", Title: "正在跑的会话"}}

	// 第一帧：会话在运行中，指针落在行上。
	latch.beginFrame()
	_, running, _ := latch.split(active, stateGroupBusy("t1"))
	latch.markRowHovered()
	latch.endFrame()
	if len(running) != 1 {
		t.Fatalf("第一帧应归入运行中, got %d", len(running))
	}

	// 第二帧：会话变为待处理，但指针仍在行上，沿用旧归属。
	active[0].HasPendingRequest = true
	latch.beginFrame()
	needsYou, running, _ := latch.split(active, stateGroupBusy("t1"))
	latch.markRowHovered()
	latch.endFrame()
	if len(needsYou) != 0 || len(running) != 1 {
		t.Fatalf("指针悬停时应留在运行中: 等你处理 %d, 运行中 %d", len(needsYou), len(running))
	}

	// 第三帧：指针离开列表（本帧没有 markRowHovered），本帧仍沿用旧归属。
	latch.beginFrame()
	_, running, _ = latch.split(active, stateGroupBusy("t1"))
	latch.endFrame()
	if len(running) != 1 {
		t.Fatalf("指针刚离开的那一帧仍应沿用旧归属, got %d", len(running))
	}

	// 第四帧：恢复正常重算，会话归位到等你处理。
	latch.beginFrame()
	needsYou, running, rest := latch.split(active, stateGroupBusy("t1"))
	latch.endFrame()
	if len(needsYou) != 1 || len(running) != 0 || len(rest) != 0 {
		t.Fatalf("指针离开后应归位到等你处理: %d/%d/%d", len(needsYou), len(running), len(rest))
	}
}

// TestSidebarStateGroupLatchRequestsRegroupFrame 验证冻结期间有会话待归位时，
// 指针离开的那一帧结束会要求再重绘一帧完成归位；没有待归位时不额外请求。
func TestSidebarStateGroupLatchRequestsRegroupFrame(t *testing.T) {
	resetSidebarStateGroupLatch()
	latch := &sidebarStateGroupLatchState
	active := []Thread{{ID: "t1"}}

	// 第一帧：空闲，指针落在行上；没有待归位，不请求重绘。
	latch.beginFrame()
	latch.split(active, stateGroupBusy())
	latch.markRowHovered()
	if latch.endFrame() {
		t.Fatal("没有待归位时不应要求重绘")
	}

	// 第二帧：悬停中变为运行中，沿用旧归属并记下待归位；指针还在列表内，不请求。
	active[0].IsRunning = true
	latch.beginFrame()
	_, running, _ := latch.split(active, stateGroupBusy("t1"))
	if len(running) != 0 {
		t.Fatalf("悬停期间应保持旧归属, got 运行中 %d", len(running))
	}
	latch.markRowHovered()
	if latch.endFrame() {
		t.Fatal("指针仍在列表内时不应要求重绘")
	}

	// 第三帧：指针离开，本帧仍冻结；本帧结束应要求再重绘一帧。
	latch.beginFrame()
	latch.split(active, stateGroupBusy("t1"))
	if !latch.endFrame() {
		t.Fatal("有待归位时指针离开应要求重绘")
	}

	// 第四帧：重绘帧完成归位，不再重复请求。
	latch.beginFrame()
	_, running, _ = latch.split(active, stateGroupBusy("t1"))
	if len(running) != 1 {
		t.Fatalf("重绘帧应把会话归入运行中, got %d", len(running))
	}
	if latch.endFrame() {
		t.Fatal("归位后不应继续要求重绘")
	}
}

// TestSidebarStateGroupLatchDropsVanishedThreads 验证归属表只保留本次 active 中的会话，
// 消失的会话被清掉，新出现的会话按当前状态归类。
func TestSidebarStateGroupLatchDropsVanishedThreads(t *testing.T) {
	resetSidebarStateGroupLatch()
	latch := &sidebarStateGroupLatchState

	// 第一帧：t1 在运行中，指针落在行上。
	latch.beginFrame()
	latch.split([]Thread{{ID: "t1"}}, stateGroupBusy("t1"))
	latch.markRowHovered()
	latch.endFrame()

	// 第二帧：指针继续悬停，t1 消失、t2 新出现；t2 按当前状态归入运行中。
	latch.beginFrame()
	_, running, _ := latch.split([]Thread{{ID: "t2"}}, stateGroupBusy("t2"))
	latch.markRowHovered()
	latch.endFrame()
	if len(running) != 1 {
		t.Fatalf("新出现的会话应按当前状态归入运行中, got %d", len(running))
	}

	latch.mu.Lock()
	_, hasT1 := latch.groups["t1"]
	_, hasT2 := latch.groups["t2"]
	size := len(latch.groups)
	latch.mu.Unlock()
	if hasT1 {
		t.Fatal("消失的会话不应留在归属表里")
	}
	if !hasT2 {
		t.Fatal("新出现的会话应写入归属表")
	}
	if size != 1 {
		t.Fatalf("归属表应只剩 1 个会话, got %d", size)
	}

	// 指针离开后，t2 变为待处理即归位到等你处理。
	latch.beginFrame()
	latch.split([]Thread{{ID: "t2", HasPendingRequest: true}}, stateGroupBusy("t2"))
	latch.endFrame()

	latch.beginFrame()
	needsYou, running, _ := latch.split([]Thread{{ID: "t2", HasPendingRequest: true}}, stateGroupBusy("t2"))
	latch.endFrame()
	if len(needsYou) != 1 || len(running) != 0 {
		t.Fatalf("指针离开后应归位到等你处理: %d/%d", len(needsYou), len(running))
	}
}

// TestRenderSidebarStateGroupHeaderLabelsUnique 验证组头的 a11y Label 在两个项目、
// 同一项目的两个组之间都唯一，Tester 能按 Label 定位到各自一行。
func TestRenderSidebarStateGroupHeaderLabelsUnique(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("state-group-test")).Fill().Children(func() {
			renderSidebarStateGroupHeader(c, "p1", "项目甲", "等你处理", 1, 0)
			renderSidebarStateGroupHeader(c, "p1", "项目甲", "运行中", 2, 0)
			renderSidebarStateGroupHeader(c, "p2", "项目乙", "等你处理", 3, 0)
			renderSidebarStateGroupHeader(c, "p2", "项目乙", "运行中", 4, 0)
		})
	}, 480, 240)

	labels := []string{
		"等你处理 · 项目甲 · 1",
		"运行中 · 项目甲 · 2",
		"等你处理 · 项目乙 · 3",
		"运行中 · 项目乙 · 4",
	}
	rects := make(map[string]ui.Rect, len(labels))
	for _, label := range labels {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("找不到组头 Label %q, texts: %q", label, tt.Texts())
		}
		if n := countText(tt, label); n != 1 {
			t.Fatalf("组头 Label %q 应唯一, got %d", label, n)
		}
		rects[label] = r
	}

	// 同一项目的两个组各占一行，不重叠。
	if rects[labels[0]].Y == rects[labels[1]].Y {
		t.Fatalf("同一项目的两个组头不应在同一行: %v", rects[labels[0]].Y)
	}
	if rects[labels[2]].Y == rects[labels[3]].Y {
		t.Fatalf("同一项目的两个组头不应在同一行: %v", rects[labels[2]].Y)
	}

	// 可见文字是「标题  计数」。
	if n := countText(tt, "等你处理  1"); n != 1 {
		t.Fatalf("可见文字应为「等你处理  1」, got %d", n)
	}
	if n := countText(tt, "运行中  4"); n != 1 {
		t.Fatalf("可见文字应为「运行中  4」, got %d", n)
	}
}

// assertStateGroupIDs 按顺序比对一段会话的 ID。
func assertStateGroupIDs(t *testing.T, name string, got []Thread, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: 长度 = %d, want %d", name, len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i] {
			t.Fatalf("%s: 第 %d 个 = %s, want %s", name, i, got[i].ID, want[i])
		}
	}
}
