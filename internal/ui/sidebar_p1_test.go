package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestSidebarP1RowFocusWithinAndHitbox(t *testing.T) {
	const longTitle = "这是一个非常非常非常长以至于会在侧栏中被截断的会话标题用于验证Tooltip与焦点可达性"

	runCase := func(t *testing.T, compact bool) {
		t.Helper()
		testenv.TempHome(t)
		resetSidebarHoverCardForTest()
		t.Cleanup(resetSidebarHoverCardForTest)

		fake := NewFakeClient()
		now := time.Now()
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "mcode-p1", Path: "/tmp/mcode-p1"}},
			map[string][]Thread{
				"p1": {
					{ID: "t1", ProjectID: "p1", Title: longTitle, UpdatedAt: now},
				},
			},
		)

		state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
		t.Cleanup(state.close)
		if compact {
			st := getChatSettings(state)
			if st == nil {
				t.Fatal("期望设置状态已挂载")
			}
			st.cfg.Appearance.SidebarCompact = true
		}

		tt := ui.NewTester(view, 1000, 700)
		waitFor(t, tt, func() bool {
			return tt.HasText(longTitle)
		})

		// 验证长会话标题具备 Tooltip（指针停留在标题文本上超过 tooltipDelay 后出现 RoleTooltip 提示）。
		rowLabel := "打开会话 " + longTitle
		rowRect, ok := tt.Find(rowLabel)
		if !ok {
			t.Fatalf("找不到侧栏会话行 %q", rowLabel)
		}
		tt.Move(rowRect.X+40, rowRect.Y+rowRect.H/2)
		tt.Advance(620 * time.Millisecond)
		tt.Frame()
		tipAcc, ok := tt.AccessByRole(ui.RoleTooltip, "")
		if !ok {
			t.Fatalf("指针停留在长会话标题上后未出现 RoleTooltip")
		}
		if tipAcc.Name != longTitle {
			t.Fatalf("会话标题 Tooltip = %q，期望 %q", tipAcc.Name, longTitle)
		}

		// 指针移出侧栏行，确保未悬停时不显示「更多操作」按钮。
		tt.Move(900, 600)
		tt.Advance(250 * time.Millisecond)
		tt.Frame()
		if _, ok := tt.Find("更多操作"); ok {
			t.Fatalf("未悬停且未聚焦时不应显示「更多操作」按钮")
		}

		// 使用 Tab 导航到会话行，触发 row.FocusWithin()。
		for i := 0; i < 64 && !tt.Focused(rowLabel); i++ {
			tt.Key(0, ui.KeyTab)
		}
		if !tt.Focused(rowLabel) {
			t.Fatalf("无法通过 Tab 聚焦到侧栏会话行 %q", rowLabel)
		}
		tt.Frame()

		// 聚焦进入该行后，行尾「更多操作」按钮必须可见且热区 >= ControlSM。
		moreRect, ok := tt.Find("更多操作")
		if !ok {
			t.Fatalf("会话行获得键盘焦点后应显示「更多操作」按钮")
		}
		if moreRect.W < ControlSM || moreRect.H < ControlSM {
			t.Fatalf("「更多操作」热区 = %.1fx%.1f，期望至少 %.1fx%.1f", moreRect.W, moreRect.H, ControlSM, ControlSM)
		}
		moreAcc, ok := tt.Access("更多操作")
		if !ok {
			t.Fatalf("找不到「更多操作」无障碍节点")
		}
		if !moreAcc.Focusable {
			t.Fatalf("「更多操作」按钮应可键盘聚焦")
		}
		if moreAcc.Bounds.W < ControlSM || moreAcc.Bounds.H < ControlSM {
			t.Fatalf("「更多操作」无障碍边界 = %.1fx%.1f，期望至少 %.1fx%.1f", moreAcc.Bounds.W, moreAcc.Bounds.H, ControlSM, ControlSM)
		}

		// 再按一次 Tab，键盘焦点应可达行尾「更多操作」按钮且 row.FocusWithin() 仍保持其可见。
		tt.Key(0, ui.KeyTab)
		tt.Frame()
		if !tt.Focused("更多操作") {
			t.Fatalf("按 Tab 后应能聚焦到行尾「更多操作」按钮")
		}
	}

	t.Run("normal_mode", func(t *testing.T) {
		runCase(t, false)
	})
	t.Run("compact_mode", func(t *testing.T) {
		runCase(t, true)
	})
}

func TestSidebarP1BulkCheckAndPaginationKeyboard(t *testing.T) {
	testenv.TempHome(t)
	resetSidebarHoverCardForTest()
	t.Cleanup(resetSidebarHoverCardForTest)

	fake := NewFakeClient()
	now := time.Now()
	threads := make([]Thread, 0, sidebarThreadPageInit+2)
	for i := 0; i < sidebarThreadPageInit+2; i++ {
		threads = append(threads, Thread{
			ID:        fmt.Sprintf("t%d", i),
			ProjectID: "p1",
			Title:     fmt.Sprintf("会话 %02d", i),
			UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-bulk", Path: "/tmp/mcode-bulk"}},
		map[string][]Thread{"p1": threads},
	)

	state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 900)

	waitFor(t, tt, func() bool {
		return tt.HasText("加载更多")
	})

	moreAcc, ok := tt.Access("加载更多")
	if !ok {
		t.Fatal("找不到「加载更多」无障碍节点")
	}
	if !moreAcc.Focusable {
		t.Fatal("「加载更多」分页按钮应可键盘聚焦")
	}

	// 打开整理浮窗验证复选行的键盘聚焦与 Enter/Space 切换。
	if err := tt.RightClick("mcode-bulk"); err != nil {
		t.Fatalf("右键项目失败: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("整理会话…"); err != nil {
		t.Fatalf("选择「整理会话…」失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("包含置顶") && tt.HasText("包含已休眠")
	})

	for _, label := range []string{"包含置顶", "包含已休眠"} {
		acc, ok := tt.Access(label)
		if !ok {
			t.Fatalf("找不到复选行 %q", label)
		}
		if !acc.Focusable {
			t.Fatalf("复选行 %q 应可键盘聚焦", label)
		}
	}

	for i := 0; i < 64 && !tt.Focused("包含置顶"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused("包含置顶") {
		t.Fatal("无法通过 Tab 聚焦到「包含置顶」复选行")
	}
	tt.Key(0, ui.KeySpace)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.bulkIncludePinned
	})

	for i := 0; i < 16 && !tt.Focused("包含已休眠"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused("包含已休眠") {
		t.Fatal("无法通过 Tab 聚焦到「包含已休眠」复选行")
	}
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.bulkIncludeDormant
	})
}

func TestSidebarP1SortCacheUpdates(t *testing.T) {
	t.Run("dormant_sort_cache_updates_on_data_change", func(t *testing.T) {
		testenv.TempHome(t)
		resetSidebarHoverCardForTest()
		t.Cleanup(resetSidebarHoverCardForTest)

		fake := NewFakeClient()
		now := time.Now()
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "休眠排序项目", Path: "/tmp/dormant-sort"}},
			map[string][]Thread{
				"p1": {
					{ID: "a1", ProjectID: "p1", Title: "活跃会话", UpdatedAt: now},
					{ID: "d-old", ProjectID: "p1", Title: "休眠旧", Dormant: true, UpdatedAt: now.Add(-10 * time.Minute)},
					{ID: "d-new", ProjectID: "p1", Title: "休眠新", Dormant: true, UpdatedAt: now.Add(-5 * time.Minute)},
				},
			},
		)

		state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
		t.Cleanup(state.close)
		tt := ui.NewTester(view, 1000, 700)

		waitFor(t, tt, func() bool {
			return tt.HasText("活跃会话") && tt.HasText("已休眠会话（2）")
		})
		tt.Frame()

		if err := tt.Click("已休眠会话（2）"); err != nil {
			t.Fatalf("展开已休眠会话失败: %v", err)
		}
		waitFor(t, tt, func() bool {
			rNew, okNew := tt.Find("打开会话 休眠新")
			rOld, okOld := tt.Find("打开会话 休眠旧")
			return okNew && okOld && rNew.Y < rOld.Y
		})

		beforeSort := sidebarDormantSortBuilds.Load()
		// 无数据变更连续推进多帧（悬停、移动指针、推进时间），不应重复排序。
		if row, ok := tt.Find("打开会话 活跃会话"); ok {
			tt.Move(row.X+row.W/2, row.Y+row.H/2)
			tt.Frame()
		}
		tt.Move(900, 600)
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
		if got := sidebarDormantSortBuilds.Load(); got != beforeSort {
			t.Fatalf("空闲/悬停帧不应重算休眠排序: before=%d, got=%d", beforeSort, got)
		}

		// 数据变更：把「休眠旧」的活跃时间更新到比「休眠新」更近。
		newer := now.Add(-time.Minute)
		state.mu.Lock()
		for i := range state.threadsByProj["p1"] {
			if state.threadsByProj["p1"][i].ID == "d-old" {
				state.threadsByProj["p1"][i].UpdatedAt = newer
			}
		}
		for i := range state.threads {
			if state.threads[i].ID == "d-old" {
				state.threads[i].UpdatedAt = newer
			}
		}
		state.mu.Unlock()
		state.invalidate()

		waitFor(t, tt, func() bool {
			rNew, okNew := tt.Find("打开会话 休眠新")
			rOld, okOld := tt.Find("打开会话 休眠旧")
			return okNew && okOld && rOld.Y < rNew.Y
		})
		if got := sidebarDormantSortBuilds.Load(); got <= beforeSort {
			t.Fatalf("数据变更后休眠排序缓存应重算: before=%d, got=%d", beforeSort, got)
		}
	})

	t.Run("search_filter_dormant_sort_cache", func(t *testing.T) {
		now := time.Now()
		list := []Thread{
			{ID: "a1", Title: "检索活跃", UpdatedAt: now},
			{ID: "d1", Title: "检索休眠一", Dormant: true, UpdatedAt: now.Add(-10 * time.Minute)},
			{ID: "d2", Title: "检索休眠二", Dormant: true, UpdatedAt: now.Add(-5 * time.Minute)},
		}

		res1 := filterMatchingThreads(list, "检索", false, false)
		if len(res1) != 3 || res1[0].ID != "a1" || res1[1].ID != "d2" || res1[2].ID != "d1" {
			t.Fatalf("首次过滤排序结果异常: %+v", res1)
		}
		afterFirst := sidebarDormantSortBuilds.Load()

		res2 := filterMatchingThreads(list, "检索", false, false)
		if len(res2) != 3 || res2[1].ID != "d2" {
			t.Fatalf("命中缓存结果异常: %+v", res2)
		}
		if got := sidebarDormantSortBuilds.Load(); got != afterFirst {
			t.Fatalf("未变数据时 filterMatchingThreads 不应重复排序: before=%d, got=%d", afterFirst, got)
		}

		updated := append([]Thread(nil), list...)
		updated[1].LastUserMessageAt = now.Add(-time.Minute)
		res3 := filterMatchingThreads(updated, "检索", false, false)
		if len(res3) != 3 || res3[1].ID != "d1" || res3[2].ID != "d2" {
			t.Fatalf("数据变更后 filterMatchingThreads 应更新排序: %+v", res3)
		}
		if got := sidebarDormantSortBuilds.Load(); got <= afterFirst {
			t.Fatalf("数据变更后 filterMatchingThreads 应重算排序: before=%d, got=%d", afterFirst, got)
		}
	})

	t.Run("activity_sort_cache_updates_on_data_change", func(t *testing.T) {
		testenv.TempHome(t)
		resetSidebarHoverCardForTest()
		t.Cleanup(resetSidebarHoverCardForTest)

		fake := NewFakeClient()
		now := time.Now()
		todayBase := startOfLocalDay(now).Add(10 * time.Hour)
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "动态排序项目", Path: "/tmp/activity-sort"}},
			map[string][]Thread{
				"p1": {
					{ID: "t-first", ProjectID: "p1", Title: "动态会话甲", UpdatedAt: todayBase},
					{ID: "t-second", ProjectID: "p1", Title: "动态会话乙", UpdatedAt: todayBase.Add(-2 * time.Minute)},
				},
			},
		)

		state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
		t.Cleanup(state.close)
		tt := ui.NewTester(view, 1000, 700)

		waitSidebarSnapshotSettled(t, tt, state, 2, func() bool {
			return tt.HasText("动态会话甲") && tt.HasText("动态会话乙")
		})

		state.toggleActivityView()
		waitFor(t, tt, func() bool {
			r1, ok1 := tt.Find("打开会话 动态会话甲")
			r2, ok2 := tt.Find("打开会话 动态会话乙")
			return ok1 && ok2 && r1.Y < r2.Y
		})

		beforeActivity := sidebarActivitySortBuilds.Load()
		// 悬停与重绘但会话数据未变：不应重算活动排序。
		if row, ok := tt.Find("打开会话 动态会话甲"); ok {
			tt.Move(row.X+row.W/2, row.Y+row.H/2)
			tt.Frame()
		}
		tt.Move(900, 600)
		state.invalidate()
		tt.Frame()
		if got := sidebarActivitySortBuilds.Load(); got != beforeActivity {
			t.Fatalf("数据未变时活动排序不应重算: before=%d, got=%d", beforeActivity, got)
		}

		// 更新「动态会话乙」的 UpdatedAt 到「动态会话甲」之后，应触发重算并反转顺序。
		newer := todayBase.Add(5 * time.Minute)
		state.mu.Lock()
		for i := range state.threads {
			if state.threads[i].ID == "t-second" {
				state.threads[i].UpdatedAt = newer
			}
		}
		for i := range state.threadsByProj["p1"] {
			if state.threadsByProj["p1"][i].ID == "t-second" {
				state.threadsByProj["p1"][i].UpdatedAt = newer
			}
		}
		state.mu.Unlock()
		state.invalidate()

		waitFor(t, tt, func() bool {
			r1, ok1 := tt.Find("打开会话 动态会话甲")
			r2, ok2 := tt.Find("打开会话 动态会话乙")
			return ok1 && ok2 && r2.Y < r1.Y
		})
		if got := sidebarActivitySortBuilds.Load(); got <= beforeActivity {
			t.Fatalf("数据变更后活动排序缓存应重算: before=%d, got=%d", beforeActivity, got)
		}
	})
}

func TestSidebarFingerprintTracksRunState(t *testing.T) {
	now := time.Now()
	base := []Thread{
		{
			ID:         "t1",
			ProjectID:  "p1",
			Title:      "会话一",
			UpdatedAt:  now,
			LastStatus: "completed",
		},
	}
	baseFP := computeThreadsFingerprint(base)

	cases := []struct {
		name   string
		mutate func(*Thread)
	}{
		{
			name:   "IsRunning",
			mutate: func(th *Thread) { th.IsRunning = !th.IsRunning },
		},
		{
			name:   "SwarmBusy",
			mutate: func(th *Thread) { th.SwarmBusy = !th.SwarmBusy },
		},
		{
			name:   "HasPendingRequest",
			mutate: func(th *Thread) { th.HasPendingRequest = !th.HasPendingRequest },
		},
		{
			name:   "BackgroundTasks",
			mutate: func(th *Thread) { th.BackgroundTasks = 1 },
		},
		{
			name:   "LastStatus",
			mutate: func(th *Thread) { th.LastStatus = "failed" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := append([]Thread(nil), base...)
			tc.mutate(&next[0])
			if got := computeThreadsFingerprint(next); got == baseFP {
				t.Fatalf("仅修改 %s 后 computeThreadsFingerprint 未变化: %+v", tc.name, got)
			}
		})
	}

	t.Run("split_cache_reflects_run_state_change", func(t *testing.T) {
		testenv.TempHome(t)
		resetSidebarHoverCardForTest()
		t.Cleanup(resetSidebarHoverCardForTest)

		fake := NewFakeClient()
		fake.SetProjectsAndThreads(
			[]Project{{ID: "p1", Name: "运行态缓存项目", Path: "/tmp/run-state-cache"}},
			map[string][]Thread{
				"p1": {
					{ID: "t1", ProjectID: "p1", Title: "运行会话", IsRunning: true, UpdatedAt: now},
				},
			},
		)

		state, _ := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
		t.Cleanup(state.close)

		topRun := []Thread{
			{ID: "t1", ProjectID: "p1", Title: "运行会话", IsRunning: true, UpdatedAt: now},
		}
		active1, dormant1 := splitAndSortProjectThreads(state, "p1", topRun)
		if len(active1) != 1 || len(dormant1) != 0 || !active1[0].IsRunning {
			t.Fatalf("首次调用应返回 IsRunning=true 的会话: active=%+v dormant=%+v", active1, dormant1)
		}

		topIdle := []Thread{
			{ID: "t1", ProjectID: "p1", Title: "运行会话", IsRunning: false, UpdatedAt: now},
		}
		active2, dormant2 := splitAndSortProjectThreads(state, "p1", topIdle)
		if len(active2) != 1 || len(dormant2) != 0 {
			t.Fatalf("第二次调用返回数量异常: active=%+v dormant=%+v", active2, dormant2)
		}
		if active2[0].IsRunning {
			t.Fatalf("UpdatedAt 不变但 IsRunning 置为 false 后，缓存应返回 IsRunning=false，实际仍为 true")
		}
	})
}
