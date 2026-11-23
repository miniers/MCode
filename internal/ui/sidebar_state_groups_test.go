package mcodeui

import "testing"

// resetSidebarStateGroupLatch 清空包级闩锁状态，避免用例互相影响。
func resetSidebarStateGroupLatch() {
	l := &sidebarStateGroupLatchState
	l.mu.Lock()
	l.frameHovered = false
	l.pointerInside = false
	l.pendingRegroup = false
	l.groups = nil
	l.mu.Unlock()
}

// TestSidebarStateGroupOfPriority 验证归类规则：仅 HasPendingRequest 归入等你处理，其余（含运行中）留在原位。
func TestSidebarStateGroupOfPriority(t *testing.T) {
	cases := []struct {
		name string
		th   Thread
		want sidebarStateGroup
	}{
		{"待处理且仍在跑", Thread{ID: "t1", HasPendingRequest: true, IsRunning: true}, sidebarStateGroupNeedsYou},
		{"待处理但没跑", Thread{ID: "t2", HasPendingRequest: true}, sidebarStateGroupNeedsYou},
		{"仅运行中", Thread{ID: "t3", IsRunning: true}, sidebarStateGroupNone},
		{"完全空闲", Thread{ID: "t4"}, sidebarStateGroupNone},
	}
	for _, tc := range cases {
		if got := sidebarStateGroupOf(tc.th); got != tc.want {
			t.Fatalf("%s: group = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestSidebarStateGroupLatchHoldsRowWhilePointerInside 验证指针停在行上时状态变化不换组，
// 指针离开后的下一帧才归位。
func TestSidebarStateGroupLatchHoldsRowWhilePointerInside(t *testing.T) {
	resetSidebarStateGroupLatch()
	latch := &sidebarStateGroupLatchState
	active := []Thread{{ID: "t1", Title: "空闲会话"}}

	// 第一帧：会话不在等你处理组，指针落在行上。
	latch.beginFrame()
	needsYou, rest := latch.split(active)
	latch.markRowHovered()
	latch.endFrame()
	if len(needsYou) != 0 || len(rest) != 1 {
		t.Fatalf("第一帧应归入其余: 等你处理 %d, 其余 %d", len(needsYou), len(rest))
	}

	// 第二帧：会话变为待处理，但指针仍在行上，沿用旧归属。
	active[0].HasPendingRequest = true
	latch.beginFrame()
	needsYou, rest = latch.split(active)
	latch.markRowHovered()
	latch.endFrame()
	if len(needsYou) != 0 || len(rest) != 1 {
		t.Fatalf("指针悬停时应留在原组: 等你处理 %d, 其余 %d", len(needsYou), len(rest))
	}

	// 第三帧：指针离开列表（本帧没有 markRowHovered），本帧仍沿用旧归属。
	latch.beginFrame()
	needsYou, rest = latch.split(active)
	latch.endFrame()
	if len(needsYou) != 0 || len(rest) != 1 {
		t.Fatalf("指针刚离开的那一帧仍应沿用旧归属: 等你处理 %d, 其余 %d", len(needsYou), len(rest))
	}

	// 第四帧：恢复正常重算，会话归位到等你处理。
	latch.beginFrame()
	needsYou, rest = latch.split(active)
	latch.endFrame()
	if len(needsYou) != 1 || len(rest) != 0 {
		t.Fatalf("指针离开后应归位到等你处理: %d/%d", len(needsYou), len(rest))
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
	latch.split(active)
	latch.markRowHovered()
	if latch.endFrame() {
		t.Fatal("没有待归位时不应要求重绘")
	}

	// 第二帧：悬停中变为待处理，沿用旧归属并记下待归位；指针还在列表内，不请求。
	active[0].HasPendingRequest = true
	latch.beginFrame()
	needsYou, rest := latch.split(active)
	if len(needsYou) != 0 || len(rest) != 1 {
		t.Fatalf("悬停期间应保持旧归属, got 等你处理 %d, 其余 %d", len(needsYou), len(rest))
	}
	latch.markRowHovered()
	if latch.endFrame() {
		t.Fatal("指针仍在列表内时不应要求重绘")
	}

	// 第三帧：指针离开，本帧仍冻结；本帧结束应要求再重绘一帧。
	latch.beginFrame()
	latch.split(active)
	if !latch.endFrame() {
		t.Fatal("有待归位时指针离开应要求重绘")
	}

	// 第四帧：重绘帧完成归位，不再重复请求。
	latch.beginFrame()
	needsYou, rest = latch.split(active)
	if len(needsYou) != 1 || len(rest) != 0 {
		t.Fatalf("重绘帧应把会话归入等你处理, got %d/%d", len(needsYou), len(rest))
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

	// 第一帧：t1 在等你处理中，指针落在行上。
	latch.beginFrame()
	latch.split([]Thread{{ID: "t1", HasPendingRequest: true}})
	latch.markRowHovered()
	latch.endFrame()

	// 第二帧：指针继续悬停，t1 消失、t2 新出现；t2 按当前状态归入等你处理。
	latch.beginFrame()
	needsYou, _ := latch.split([]Thread{{ID: "t2", HasPendingRequest: true}})
	latch.markRowHovered()
	latch.endFrame()
	if len(needsYou) != 1 {
		t.Fatalf("新出现的会话应按当前状态归入等你处理, got %d", len(needsYou))
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

	// 指针离开后，t2 结束待处理即归位到其余。
	latch.beginFrame()
	latch.split([]Thread{{ID: "t2", HasPendingRequest: false}})
	latch.endFrame()

	latch.beginFrame()
	needsYou, rest := latch.split([]Thread{{ID: "t2", HasPendingRequest: false}})
	latch.endFrame()
	if len(needsYou) != 0 || len(rest) != 1 {
		t.Fatalf("指针离开后应归位到其余: %d/%d", len(needsYou), len(rest))
	}
}
