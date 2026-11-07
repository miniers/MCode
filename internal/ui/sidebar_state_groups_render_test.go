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
		_, pendingHeader := tt.Find("等你处理 · A · 1")
		_, runningHeader := tt.Find("运行中 · A · 1")
		return pendingHeader && runningHeader && tt.HasText("B-空闲")
	})
	return tt, state
}

// TestSidebarStateGroupsRenderWithPagination 验证项目内的状态分组顺序、空组隐藏，
// 以及分页只作用于「其余」会话：两个状态组始终全量可见。
func TestSidebarStateGroupsRenderWithPagination(t *testing.T) {
	tt, _ := buildSidebarStateGroupsFixture(t)

	needsYou, ok := tt.Find("等你处理 · A · 1")
	if !ok {
		t.Fatalf("项目 A 应有「等你处理」组头，texts: %q", tt.Texts())
	}
	running, ok := tt.Find("运行中 · A · 1")
	if !ok {
		t.Fatalf("项目 A 应有「运行中」组头，texts: %q", tt.Texts())
	}
	firstRest, ok := tt.Find("打开会话 A-空闲1")
	if !ok {
		t.Fatalf("项目 A 应展示其余会话，texts: %q", tt.Texts())
	}
	if needsYou.Y >= running.Y {
		t.Fatalf("组头顺序应为等你处理在上、运行中在下: %v >= %v", needsYou.Y, running.Y)
	}
	if running.Y >= firstRest.Y {
		t.Fatalf("其余会话应排在两个状态组之后: 组头 %v，行 %v", running.Y, firstRest.Y)
	}

	// 项目 B 没有待处理/运行中的会话，不应出现任何状态组头。
	for _, label := range []string{"等你处理 · B · 1", "运行中 · B · 1"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("项目 B 不应出现状态组头 %q", label)
		}
	}

	// 待审批行尾是带「等你审批」标签的警示三角。
	if _, ok := tt.Find("等你审批"); !ok {
		t.Fatalf("待审批行应有「等你审批」标签，texts: %q", tt.Texts())
	}

	// 两个状态组全量显示；其余按初始分页窗口（8 条）折叠。
	for _, label := range []string{"打开会话 A-待审批", "打开会话 A-运行中"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("状态组会话应始终可见: %q", label)
		}
	}
	if _, ok := tt.Find("打开会话 A-空闲8"); !ok {
		t.Fatalf("其余会话应展示分页初始数（A-空闲8 可见），texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 A-空闲9"); ok {
		t.Fatalf("A-空闲9 应被初始分页隐藏")
	}
	if !tt.HasText("加载更多") {
		t.Fatalf("其余会话超过一页时应出现「加载更多」")
	}

	// 加载更多只展开其余会话，状态组仍全量可见。
	if err := tt.Click("加载更多"); err != nil {
		t.Fatalf("click 加载更多 failed: %v", err)
	}
	waitFor(t, tt, func() bool { return tt.HasText("A-空闲9") })
	if tt.HasText("加载更多") {
		t.Fatalf("全部展开后不应再有「加载更多」")
	}
	if _, ok := tt.Find("等你处理 · A · 1"); !ok {
		t.Fatalf("展开后「等你处理」组头不应消失")
	}
	if _, ok := tt.Find("运行中 · A · 1"); !ok {
		t.Fatalf("展开后「运行中」组头不应消失")
	}
}

// TestSidebarStateGroupsLatchHoverKeepsRowInPlace 验证指针停在会话行上时，
// 状态变化不会让行从指针下跳走；指针离开列表后才重新归组。
// 悬停的会话取「其余」组的第二行（A-空闲2）：第一行紧跟在运行中组之后，
// 归组前后位置相同，看不出上移。
func TestSidebarStateGroupsLatchHoverKeepsRowInPlace(t *testing.T) {
	tt, state := buildSidebarStateGroupsFixture(t)

	rect, ok := tt.Find("打开会话 A-空闲2")
	if !ok {
		t.Fatalf("找不到待悬停的空闲会话，texts: %q", tt.Texts())
	}
	tt.Move(rect.X+rect.W/2, rect.Y+rect.H/2)
	tt.Frame()

	// 指针停在行上时，会话从空闲变为运行中。
	state.applyThreadSummary(Thread{ID: "a-idle-2", ProjectID: "pa", IsRunning: true})
	tt.Frame()

	held, ok := tt.Find("打开会话 A-空闲2")
	if !ok {
		t.Fatalf("悬停行不应消失，texts: %q", tt.Texts())
	}
	if held.Y != rect.Y {
		t.Fatalf("指针在行上时不应跳行: y %v -> %v", rect.Y, held.Y)
	}
	if _, ok := tt.Find("运行中 · A · 1"); !ok {
		t.Fatalf("悬停期间分组计数应保持冻结（运行中仍为 1），texts: %q", tt.Texts())
	}

	// 指针移出列表后，重新归组，行回到运行中组。
	tt.Move(900, 600)
	tt.Frame()
	tt.Frame()

	moved, ok := tt.Find("打开会话 A-空闲2")
	if !ok {
		t.Fatalf("重新归组后行不应消失，texts: %q", tt.Texts())
	}
	if moved.Y >= rect.Y {
		t.Fatalf("移出后应归入运行中组并上移: y %v（原 %v）", moved.Y, rect.Y)
	}
	if _, ok := tt.Find("运行中 · A · 2"); !ok {
		t.Fatalf("重新归组后运行中计数应为 2，texts: %q", tt.Texts())
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
