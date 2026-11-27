package mcodeui

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func newSidebarUnfoldMotionTester(t *testing.T, reduced bool, setup func(*FakeClient)) (*chatState, *ui.Tester) {
	t.Helper()
	testenv.TempHome(t)
	setAppReduceMotion(t, reduced)
	resetSidebarStateGroupLatch()
	resetSidebarHoverCardForTest()
	t.Cleanup(func() {
		resetSidebarStateGroupLatch()
		resetSidebarHoverCardForTest()
	})

	fake := NewFakeClient()
	setup(fake)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTesterWith(view, 1000, 900, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduced}})
	return state, tt
}

func settleSidebarUnfoldMotion(tt *ui.Tester) {
	tt.Advance(motionUnfold + time.Second)
	tt.Frame()
}

func assertUnfoldProgressBetween(t *testing.T, label string, start, mid, end float32, tt *ui.Tester) {
	t.Helper()
	if !(end > start) {
		t.Fatalf("%s 展开后位置/高度应增大: start=%v end=%v", label, start, end)
	}
	if !(mid > start && mid < end) {
		t.Fatalf("%s 半程应介于首帧与结束之间: start=%v mid=%v end=%v", label, start, mid, end)
	}
	if tt.FramePending() {
		t.Fatalf("%s 动画结束后不应再请求下一帧", label)
	}
}

func TestUnfoldMotionSidebarProjectBody(t *testing.T) {
	now := time.Now()
	_, tt := newSidebarUnfoldMotionTester(t, false, func(fake *FakeClient) {
		fake.SetProjectsAndThreads(
			[]Project{
				{ID: "p1", Name: "项目一", Path: "/tmp/p1"},
				{ID: "p2", Name: "项目二", Path: "/tmp/p2"},
			},
			map[string][]Thread{
				"p1": {
					{ID: "t1", ProjectID: "p1", Title: "会话甲", UpdatedAt: now},
					{ID: "t2", ProjectID: "p1", Title: "会话乙", UpdatedAt: now.Add(-time.Minute)},
				},
				"p2": {
					{ID: "t3", ProjectID: "p2", Title: "会话丙", UpdatedAt: now.Add(-2 * time.Minute)},
				},
			},
		)
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("会话甲") && tt.HasText("项目二")
	})
	settleSidebarUnfoldMotion(tt)

	if err := tt.Click("收起项目 项目一"); err != nil {
		t.Fatalf("点击收起项目失败: %v", err)
	}
	settleSidebarUnfoldMotion(tt)
	if _, ok := tt.Find("打开会话 会话甲"); ok {
		t.Fatalf("收起完成后不应渲染项目内会话: %q", tt.Texts())
	}

	if err := tt.Click("展开项目 项目一"); err != nil {
		t.Fatalf("点击展开项目失败: %v", err)
	}
	tt.Frame()
	tt.Frame()
	p2Start, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("找不到项目二标题行: %q", tt.Texts())
	}

	tt.Advance(motionUnfold / 2)
	tt.Frame()
	p2Mid, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("半程找不到项目二标题行: %q", tt.Texts())
	}

	settleSidebarUnfoldMotion(tt)
	p2End, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("结束找不到项目二标题行: %q", tt.Texts())
	}

	assertUnfoldProgressBetween(t, "项目会话区", p2Start.Y, p2Mid.Y, p2End.Y, tt)
}

func TestUnfoldMotionSidebarDormantGroup(t *testing.T) {
	now := time.Now()
	_, tt := newSidebarUnfoldMotionTester(t, false, func(fake *FakeClient) {
		fake.SetProjectsAndThreads(
			[]Project{
				{ID: "p1", Name: "项目一", Path: "/tmp/p1"},
				{ID: "p2", Name: "项目二", Path: "/tmp/p2"},
			},
			map[string][]Thread{
				"p1": {
					{ID: "t1", ProjectID: "p1", Title: "活跃A", UpdatedAt: now},
					{ID: "d1", ProjectID: "p1", Title: "休眠1", Dormant: true, UpdatedAt: now.Add(-time.Minute)},
					{ID: "d2", ProjectID: "p1", Title: "休眠2", Dormant: true, UpdatedAt: now.Add(-2 * time.Minute)},
				},
				"p2": {
					{ID: "t2", ProjectID: "p2", Title: "活跃B", UpdatedAt: now.Add(-3 * time.Minute)},
				},
			},
		)
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("已休眠会话（2）") && tt.HasText("项目二")
	})
	settleSidebarUnfoldMotion(tt)

	if err := tt.Click("已休眠会话（2）"); err != nil {
		t.Fatalf("点击已休眠会话组失败: %v", err)
	}
	tt.Frame()
	tt.Frame()
	p2Start, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("找不到项目二标题行: %q", tt.Texts())
	}

	tt.Advance(motionUnfold / 2)
	tt.Frame()
	p2Mid, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("半程找不到项目二标题行: %q", tt.Texts())
	}

	settleSidebarUnfoldMotion(tt)
	p2End, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("结束找不到项目二标题行: %q", tt.Texts())
	}

	assertUnfoldProgressBetween(t, "已休眠会话组", p2Start.Y, p2Mid.Y, p2End.Y, tt)
}

func TestUnfoldMotionSidebarDormantProjects(t *testing.T) {
	fixedNow := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	prevNow := sidebarProjectNow
	sidebarProjectNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { sidebarProjectNow = prevNow })

	_, tt := newSidebarUnfoldMotionTester(t, false, func(fake *FakeClient) {
		projects := []Project{{ID: "p-active", Name: "活跃项目"}}
		threadsByProj := map[string][]Thread{
			"p-active": {{
				ID:        "t-active",
				ProjectID: "p-active",
				Title:     "近期会话",
				UpdatedAt: fixedNow.Add(-2 * 24 * time.Hour),
			}},
		}
		for i := 1; i <= 8; i++ {
			pid := fmt.Sprintf("p-old-%d", i)
			projects = append(projects, Project{ID: pid, Name: fmt.Sprintf("陈旧项目%d", i)})
			ths := []Thread{{
				ID:        fmt.Sprintf("t-old-%d", i),
				ProjectID: pid,
				Title:     fmt.Sprintf("旧会话%d", i),
				UpdatedAt: fixedNow.Add(-time.Duration(19+i) * 24 * time.Hour),
			}}
			if i == 8 {
				ths = append(ths, Thread{
					ID:        "t-old-8b",
					ProjectID: pid,
					Title:     "旧会话8b",
					UpdatedAt: fixedNow.Add(-28 * 24 * time.Hour),
				})
			}
			threadsByProj[pid] = ths
		}
		fake.SetProjectsAndThreads(projects, threadsByProj)
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("活跃项目") && tt.HasText("更多项目 (8)")
	})
	settleSidebarUnfoldMotion(tt)

	toggleRect, ok := tt.Find("更多项目 (8)")
	if !ok {
		t.Fatalf("找不到「更多项目 (8)」: %q", tt.Texts())
	}
	baseY := toggleRect.Y + toggleRect.H

	measureBodyHeight := func() float32 {
		var maxBottom float32
		for i := 1; i <= 8; i++ {
			label := fmt.Sprintf("收起项目 陈旧项目%d", i)
			if r, found := tt.Find(label); found && r.H > 0 {
				if b := r.Y + r.H - baseY; b > maxBottom {
					maxBottom = b
				}
			}
		}
		for _, label := range []string{"已休眠会话（2）", "打开会话 旧会话8b"} {
			if r, found := tt.Find(label); found && r.H > 0 {
				if b := r.Y + r.H - baseY; b > maxBottom {
					maxBottom = b
				}
			}
		}
		return maxBottom
	}

	if err := tt.Click("更多项目 (8)"); err != nil {
		t.Fatalf("点击「更多项目 (8)」失败: %v", err)
	}
	tt.Frame()
	tt.Frame()
	startH := measureBodyHeight()

	tt.Advance(motionUnfold / 2)
	tt.Frame()
	midH := measureBodyHeight()

	settleSidebarUnfoldMotion(tt)
	endH := measureBodyHeight()

	assertUnfoldProgressBetween(t, "更多项目组", startH, midH, endH, tt)
}

func TestUnfoldMotionSidebarQuickChats(t *testing.T) {
	now := time.Now()
	_, tt := newSidebarUnfoldMotionTester(t, false, func(fake *FakeClient) {
		fake.SetChatsProjectID("chats-builtin")
		fake.SetProjectsAndThreads(
			[]Project{
				{ID: "chats-builtin", Name: "内置对话"},
				{ID: "p1", Name: "普通项目", Path: "/tmp/p1"},
			},
			map[string][]Thread{
				"chats-builtin": {
					{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now},
					{ID: "c2", ProjectID: "chats-builtin", Title: "闲聊二号", UpdatedAt: now.Add(-time.Minute)},
				},
				"p1": {
					{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now.Add(-2 * time.Minute)},
				},
			},
		)
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("临时会话") && tt.HasText("闲聊一号") && tt.HasText("普通项目")
	})
	settleSidebarUnfoldMotion(tt)

	if err := tt.Click("收起临时会话"); err != nil {
		t.Fatalf("点击收起临时会话失败: %v", err)
	}
	settleSidebarUnfoldMotion(tt)
	if _, ok := tt.Find("打开会话 闲聊一号"); ok {
		t.Fatalf("收起临时会话后不应渲染会话: %q", tt.Texts())
	}

	if err := tt.Click("展开临时会话"); err != nil {
		t.Fatalf("点击展开临时会话失败: %v", err)
	}
	tt.Frame()
	tt.Frame()
	pStart, ok := tt.Find("收起项目 普通项目")
	if !ok {
		t.Fatalf("找不到普通项目标题行: %q", tt.Texts())
	}

	tt.Advance(motionUnfold / 2)
	tt.Frame()
	pMid, ok := tt.Find("收起项目 普通项目")
	if !ok {
		t.Fatalf("半程找不到普通项目标题行: %q", tt.Texts())
	}

	settleSidebarUnfoldMotion(tt)
	pEnd, ok := tt.Find("收起项目 普通项目")
	if !ok {
		t.Fatalf("结束找不到普通项目标题行: %q", tt.Texts())
	}

	assertUnfoldProgressBetween(t, "临时会话区", pStart.Y, pMid.Y, pEnd.Y, tt)
}

func TestUnfoldMotionSidebarReducedMotion(t *testing.T) {
	now := time.Now()
	_, tt := newSidebarUnfoldMotionTester(t, true, func(fake *FakeClient) {
		fake.SetProjectsAndThreads(
			[]Project{
				{ID: "p1", Name: "项目一", Path: "/tmp/p1"},
				{ID: "p2", Name: "项目二", Path: "/tmp/p2"},
			},
			map[string][]Thread{
				"p1": {
					{ID: "t1", ProjectID: "p1", Title: "活跃A", UpdatedAt: now},
					{ID: "d1", ProjectID: "p1", Title: "休眠B", Dormant: true, UpdatedAt: now.Add(-time.Minute)},
				},
				"p2": {
					{ID: "t2", ProjectID: "p2", Title: "活跃C", UpdatedAt: now.Add(-2 * time.Minute)},
				},
			},
		)
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("已休眠会话（1）") && tt.HasText("项目二")
	})

	if err := tt.Click("已休眠会话（1）"); err != nil {
		t.Fatalf("点击展开已休眠会话失败: %v", err)
	}
	tt.Frame()
	if !tt.HasText("休眠B") {
		t.Fatalf("减少动效开启时展开后首帧应立即显示休眠会话: %q", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("减少动效开启时展开后不应请求过渡帧")
	}
	p2First, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("找不到项目二标题行: %q", tt.Texts())
	}

	settleSidebarUnfoldMotion(tt)
	p2Settled, ok := tt.Find("收起项目 项目二")
	if !ok {
		t.Fatalf("稳态找不到项目二标题行: %q", tt.Texts())
	}
	if math.Abs(float64(p2First.Y-p2Settled.Y)) > 0.5 {
		t.Fatalf("减少动效开启时首帧应即为最终布局: first=%v settled=%v", p2First.Y, p2Settled.Y)
	}

	if err := tt.Click("收起项目 项目一"); err != nil {
		t.Fatalf("点击收起项目一失败: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("打开会话 活跃A"); ok {
		t.Fatalf("减少动效开启时收起后首帧应立即移除会话: %q", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("减少动效开启时收起后不应请求过渡帧")
	}

	if err := tt.Click("展开项目 项目一"); err != nil {
		t.Fatalf("点击展开项目一失败: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("打开会话 活跃A"); !ok {
		t.Fatalf("减少动效开启时再次展开首帧应立即显示会话: %q", tt.Texts())
	}
	if tt.FramePending() {
		t.Fatal("减少动效开启时再次展开不应请求过渡帧")
	}
}

func TestUnfoldMotionSidebarAsyncLoadNoEnter(t *testing.T) {
	const title = "异步加载会话"
	_, tt := newSidebarUnfoldMotionTester(t, false, func(fake *FakeClient) {
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "异步项目", Path: "/tmp/async"}},
			map[string][]Thread{
				"p1": {{ID: "t-async", ProjectID: "p1", Title: title, UpdatedAt: time.Now()}},
			},
		)
	})

	waitFor(t, tt, func() bool { return tt.HasText(title) })
	firstRow, ok := tt.Find("打开会话 " + title)
	if !ok {
		t.Fatalf("首帧找不到会话行: %q", tt.Texts())
	}

	tt.Advance(time.Second)
	tt.Frame()
	settledRow, ok := tt.Find("打开会话 " + title)
	if !ok {
		t.Fatalf("稳态找不到会话行: %q", tt.Texts())
	}
	if math.Abs(float64(firstRow.H-settledRow.H)) > 0.5 {
		t.Fatalf("异步加载会话首帧应即为最终行高: first=%v settled=%v", firstRow.H, settledRow.H)
	}
}

func TestUnfoldMotionSidebarPagination(t *testing.T) {
	now := time.Now()
	_, tt := newSidebarUnfoldMotionTester(t, false, func(fake *FakeClient) {
		threads := make([]Thread, 12)
		for i := range threads {
			threads[i] = Thread{
				ID:        fmt.Sprintf("page-%02d", i),
				ProjectID: "p1",
				Title:     fmt.Sprintf("分页会话%02d", i),
				UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
			}
		}
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "分页项目", Path: "/tmp/page"}},
			map[string][]Thread{"p1": threads},
		)
	})

	waitFor(t, tt, func() bool {
		return tt.HasText("分页会话07") && tt.HasText("加载更多")
	})
	settleSidebarUnfoldMotion(tt)

	if err := tt.Click("加载更多"); err != nil {
		t.Fatalf("点击加载更多失败: %v", err)
	}
	tt.Frame()
	if !tt.HasText("分页会话11") {
		t.Fatalf("点击加载更多后应新增分页会话: %q", tt.Texts())
	}

	tt.Advance(DurBase / 2)
	tt.Frame()
	midRow, midOk := tt.Find("打开会话 分页会话11")
	midPending := tt.FramePending()

	tt.Advance(DurBase + time.Second)
	tt.Frame()
	if tt.FramePending() {
		t.Fatal("分页入场动画结束后不应再请求下一帧")
	}
	endRow, endOk := tt.Find("打开会话 分页会话11")
	if !endOk {
		t.Fatalf("入场动画结束后找不到分页会话11: %q", tt.Texts())
	}
	if !(midOk && midRow.H < endRow.H) && !midPending {
		t.Fatalf("点击加载更多后新一页在半程应仍在入场中: midOk=%v midH=%v endH=%v framePending=%v", midOk, midRow.H, endRow.H, midPending)
	}
}
