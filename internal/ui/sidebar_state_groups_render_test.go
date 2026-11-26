package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// buildSidebarStateGroupsFixture 构造两个项目：项目 A 有 1 个待审批、1 个运行中、
// 9 个空闲会话；项目 B 只有 1 个空闲会话。每个用例开始都会重置分组闩锁。
func buildSidebarStateGroupsFixture(t *testing.T) (*ui.Tester, *chatState) {
	t.Helper()
	resetSidebarStateGroupLatch()
	// 用例期间指针可能停在行上；结束时清空闩锁，避免影响后续用例。
	t.Cleanup(resetSidebarStateGroupLatch)

	fake := NewFakeClient()
	now := time.Now()
	aThreads := []Thread{
		{ID: "a-pending", ProjectID: "pa", Title: "A-待审批", HasPendingRequest: true, UpdatedAt: now},
		{ID: "a-running", ProjectID: "pa", Title: "A-运行中", IsRunning: true, UpdatedAt: now.Add(-time.Minute)},
	}
	for i := 1; i <= 9; i++ {
		aThreads = append(aThreads, Thread{
			ID:        fmt.Sprintf("a-idle-%d", i),
			ProjectID: "pa",
			Title:     fmt.Sprintf("A-空闲%d", i),
			UpdatedAt: now.Add(-time.Duration(i+1) * time.Minute),
		})
	}
	fake.SetProjectsAndThreads([]Project{
		{ID: "pa", Name: "A", Path: "/tmp/mcode-state-groups-a"},
		{ID: "pb", Name: "B", Path: "/tmp/mcode-state-groups-b"},
	}, map[string][]Thread{
		"pa": aThreads,
		"pb": {
			{ID: "b-idle", ProjectID: "pb", Title: "B-空闲", UpdatedAt: now.Add(-20 * time.Minute)},
		},
	})

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		_, pendingRow := tt.Find("打开会话 A-待审批")
		_, runningRow := tt.Find("打开会话 A-运行中")
		return pendingRow && runningRow && tt.HasText("B-空闲")
	})
	return tt, state
}

// TestSidebarStateGroupsRenderWithPagination 验证顶部聚焦区仅渲染等你处理会话，
// 运行中会话留在所属项目文件夹内原位，项目内不再渲染状态分组头，且分页作用于项目内会话。
func TestSidebarStateGroupsRenderWithPagination(t *testing.T) {
	tt, _ := buildSidebarStateGroupsFixture(t)

	pendingRow, ok := tt.Find("打开会话 A-待审批")
	if !ok {
		t.Fatalf("聚焦区应展示待审批会话，texts: %q", tt.Texts())
	}
	runningRow, ok := tt.Find("打开会话 A-运行中")
	if !ok {
		t.Fatalf("项目 A 内应展示运行中会话，texts: %q", tt.Texts())
	}
	if n := countExactSidebarText(tt, "打开会话 A-运行中"); n != 1 {
		t.Fatalf("运行中会话应只渲染 1 次，got %d", n)
	}
	projAHeader, ok := tt.Find("收起项目 A")
	if !ok {
		t.Fatalf("应展示项目 A 标题行，texts: %q", tt.Texts())
	}
	firstRest, ok := tt.Find("打开会话 A-空闲1")
	if !ok {
		t.Fatalf("项目 A 应展示其余会话，texts: %q", tt.Texts())
	}
	if pendingRow.Y >= projAHeader.Y {
		t.Fatalf("聚焦区待审批会话应在项目列表上方: pending=%v, projA=%v", pendingRow.Y, projAHeader.Y)
	}
	if projAHeader.Y >= runningRow.Y || runningRow.Y >= firstRest.Y {
		t.Fatalf("运行中会话应留在项目 A 内且按时间排在空闲会话前: projA=%v, running=%v, rest=%v", projAHeader.Y, runningRow.Y, firstRest.Y)
	}

	// 项目内不再出现状态组头。
	for _, label := range []string{"等你处理 · A · 1", "运行中 · A · 1", "等你处理 · B · 1", "运行中 · B · 1"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("项目内不应出现状态组头 %q", label)
		}
	}

	// 行尾保留「等你审批」状态提示。
	if !tt.HasText("等你审批") {
		t.Fatalf("聚焦区待审批行应显示「等你审批」，texts: %q", tt.Texts())
	}

	// 项目内按初始分页窗口（8 条：A-运行中 + A-空闲1..7）折叠。
	if _, ok := tt.Find("打开会话 A-空闲7"); !ok {
		t.Fatalf("项目内应展示分页初始数（A-空闲7 可见），texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 A-空闲8"); ok {
		t.Fatalf("A-空闲8 应被初始分页隐藏")
	}
	if _, ok := tt.Find("打开会话 A-空闲9"); ok {
		t.Fatalf("A-空闲9 应被初始分页隐藏")
	}
	if !tt.HasText("加载更多") {
		t.Fatalf("项目内会话超过一页时应出现「加载更多」")
	}

	// 加载更多展开项目内剩余会话，聚焦区与项目内会话都不重复。
	if err := tt.Click("加载更多"); err != nil {
		t.Fatalf("click 加载更多 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("A-空闲9") })
	if tt.HasText("加载更多") {
		t.Fatalf("全部展开后不应再有「加载更多」")
	}
	countRow := func(label string) int {
		n := 0
		for _, txt := range tt.Texts() {
			if txt == label {
				n++
			}
		}
		return n
	}
	if n := countRow("打开会话 A-待审批"); n != 1 {
		t.Fatalf("展开后 A-待审批应只出现 1 次，got %d", n)
	}
	if n := countRow("打开会话 A-运行中"); n != 1 {
		t.Fatalf("展开后 A-运行中应只出现 1 次，got %d", n)
	}
}

// TestSidebarStateGroupsLatchHoverKeepsRowInPlace 验证指针停在会话行上时，
// 状态变化不会让行从指针下跳走；指针离开列表后才归位到聚焦区。
func TestSidebarStateGroupsLatchHoverKeepsRowInPlace(t *testing.T) {
	tt, state := buildSidebarStateGroupsFixture(t)

	projAHeader, ok := tt.Find("收起项目 A")
	if !ok {
		t.Fatalf("找不到项目 A 标题行，texts: %q", tt.Texts())
	}
	rect, ok := tt.Find("打开会话 A-空闲2")
	if !ok {
		t.Fatalf("找不到待悬停的空闲会话，texts: %q", tt.Texts())
	}
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()

	// 指针停在行上时，会话从空闲变为待审批。
	state.applyThreadSummary(Thread{ID: "a-idle-2", ProjectID: "pa", HasPendingRequest: true})
	tt.Frame()

	held, ok := tt.Find("打开会话 A-空闲2")
	if !ok {
		t.Fatalf("悬停行不应消失，texts: %q", tt.Texts())
	}
	if held.Y != rect.Y {
		t.Fatalf("指针在行上时不应跳行: y %v -> %v", rect.Y, held.Y)
	}

	// 指针移出列表后，重新归组，行进入顶部聚焦区（位于项目 A 标题行上方）。
	tt.Move(900, 600)
	tt.Frame()
	tt.Advance(DurBase + 50*time.Millisecond)
	tt.Frame()

	moved, ok := tt.Find("打开会话 A-空闲2")
	if !ok {
		t.Fatalf("重新归组后行不应消失，texts: %q", tt.Texts())
	}
	projAAfter, ok := tt.Find("收起项目 A")
	if !ok {
		t.Fatalf("找不到项目 A 标题行，texts: %q", tt.Texts())
	}
	if moved.Y >= projAAfter.Y || moved.Y >= projAHeader.Y {
		t.Fatalf("移出后应归入聚焦区（项目 A 上方）: moved=%v, projA=%v", moved.Y, projAAfter.Y)
	}
}

// TestSidebarStateGroupsSearchFlattens 验证搜索态取消分组与分页：命中会话平铺展示，
// 原本折叠的会话也全部出现，状态组头与「加载更多」都不渲染。
func TestSidebarStateGroupsSearchFlattens(t *testing.T) {
	tt, state := buildSidebarStateGroupsFixture(t)

	if err := tt.Click("搜索对话..."); err != nil {
		t.Fatalf("click 搜索对话... failed: %v", err)
	}
	q := getSearchQuery(state)
	if q == nil {
		t.Fatal("搜索框状态未初始化")
	}
	*q = "空闲"
	state.invalidate()
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开会话 A-空闲6")
		return ok
	})

	if _, ok := tt.Find("等你处理 · A · 1"); ok {
		t.Fatalf("搜索态不应渲染「等你处理」组头")
	}
	if _, ok := tt.Find("运行中 · A · 1"); ok {
		t.Fatalf("搜索态不应渲染「运行中」组头")
	}
	if _, ok := tt.Find("打开会话 A-待审批"); ok {
		t.Fatalf("搜索态应过滤掉不匹配的待审批会话")
	}
	if tt.HasText("加载更多") {
		t.Fatalf("搜索态不应出现「加载更多」")
	}
	if _, ok := tt.Find("打开会话 B-空闲"); !ok {
		t.Fatalf("搜索态应平铺展示命中的项目 B 会话，texts: %q", tt.Texts())
	}
}

func TestSidebarVisibleThreadsMatchRenderedOrder(t *testing.T) {
	tt, state := buildSidebarStateGroupsFixture(t)
	got := state.visibleThreads()
	if len(got) != 10 {
		t.Fatalf("可见会话数 = %d", len(got))
	}
	var prev float32 = -1
	for _, th := range got {
		row, ok := tt.Find("打开会话 " + th.Title)
		if !ok || row.Y <= prev {
			t.Fatalf("会话顺序与渲染不一致: %q", th.Title)
		}
		prev = row.Y
	}
	state.mu.Lock()
	state.collapsedProj["pa"] = true
	state.threadsByProj["pb"] = append(state.threadsByProj["pb"], Thread{ID: "sleep", ProjectID: "pb", Title: "休眠", Dormant: true})
	state.mu.Unlock()
	state.invalidate()
	tt.Frame()
	tt.Advance(motionUnfold + time.Second)
	tt.Frame()
	got = state.visibleThreads()
	if len(got) != 2 || got[0].ID != "a-pending" || got[1].ID != "b-idle" {
		t.Fatalf("折叠项目后聚焦区与未折叠项目可见会话列表不符: %+v", got)
	}
}
