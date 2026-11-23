package mcodeui

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func countExactSidebarText(tt *ui.Tester, exact string) int {
	n := 0
	for _, txt := range tt.Texts() {
		if txt == exact {
			n++
		}
	}
	return n
}

func buildSidebarFocusZoneFixture(t *testing.T, reduceMotion bool) (*ui.Tester, *chatState) {
	t.Helper()
	testenv.TempHome(t)
	resetSidebarStateGroupLatch()
	resetSidebarHoverCardForTest()
	t.Cleanup(func() {
		resetSidebarStateGroupLatch()
		resetSidebarHoverCardForTest()
	})

	fake := newStewardFakeClient("steward-t")
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "steward-p", Name: "总管的家", Path: "/tmp/mcode-fz-steward"},
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "pa", Name: "项目甲", Path: "/tmp/mcode-fz-a"},
			{ID: "pb", Name: "项目乙", Path: "/tmp/mcode-fz-b"},
		},
		map[string][]Thread{
			"steward-p": {
				{ID: "steward-t", ProjectID: "steward-p", Title: "总管会话", UpdatedAt: now.Add(-time.Hour)},
			},
			"chats-builtin": {
				{ID: "chat-run", ProjectID: "chats-builtin", Title: "对话运行", IsRunning: true, UpdatedAt: now},
				{ID: "chat-idle", ProjectID: "chats-builtin", Title: "对话空闲", UpdatedAt: now.Add(-2 * time.Minute)},
			},
			"pa": {
				{ID: "a-pending", ProjectID: "pa", Title: "甲待审批", HasPendingRequest: true, UpdatedAt: now},
				{ID: "a-idle", ProjectID: "pa", Title: "甲空闲", UpdatedAt: now.Add(-3 * time.Minute)},
			},
			"pb": {
				{ID: "b-run", ProjectID: "pb", Title: "乙运行", IsRunning: true, UpdatedAt: now.Add(-time.Minute)},
				{ID: "b-idle", ProjectID: "pb", Title: "乙空闲", UpdatedAt: now.Add(-4 * time.Minute)},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduceMotion}})
	waitFor(t, tt, func() bool {
		return tt.HasText("总管") &&
			tt.HasText("甲待审批") &&
			tt.HasText("对话运行") &&
			tt.HasText("乙运行") &&
			tt.HasText("甲空闲") &&
			tt.HasText("乙空闲") &&
			tt.HasText("对话空闲")
	})
	return tt, state
}

// TestSidebarFocusZoneDefaultAndDeduplication 验证默认状态下：
// 仅总管与「等你处理」会话出现在顶部聚焦区，运行中会话留在所属「对话」段或项目文件夹内原位，
// 聚焦区行内显示所属项目名、左缘与总管行对齐，且不在下方列表重复。
func TestSidebarFocusZoneDefaultAndDeduplication(t *testing.T) {
	tt, _ := buildSidebarFocusZoneFixture(t, true)

	stewardRow, ok := tt.Find("总管")
	if !ok {
		t.Fatalf("找不到总管行: %q", tt.Texts())
	}
	pendingRow, ok := tt.Find("打开会话 甲待审批")
	if !ok {
		t.Fatalf("找不到待审批行: %q", tt.Texts())
	}
	chatRunRow, ok := tt.Find("打开会话 对话运行")
	if !ok {
		t.Fatalf("找不到对话运行行: %q", tt.Texts())
	}
	projBRunRow, ok := tt.Find("打开会话 乙运行")
	if !ok {
		t.Fatalf("找不到项目乙运行行: %q", tt.Texts())
	}
	chatsHeader, ok := tt.Find("快速对话")
	if !ok {
		t.Fatalf("找不到「快速对话」段标题: %q", tt.Texts())
	}
	projAHeader, ok := tt.Find("收起项目 项目甲")
	if !ok {
		t.Fatalf("找不到项目甲标题行: %q", tt.Texts())
	}
	projBHeader, ok := tt.Find("收起项目 项目乙")
	if !ok {
		t.Fatalf("找不到项目乙标题行: %q", tt.Texts())
	}
	chatIdleRow, ok := tt.Find("打开会话 对话空闲")
	if !ok {
		t.Fatalf("找不到对话空闲行: %q", tt.Texts())
	}
	projAIdleRow, ok := tt.Find("打开会话 甲空闲")
	if !ok {
		t.Fatalf("找不到甲空闲行: %q", tt.Texts())
	}
	projBIdleRow, ok := tt.Find("打开会话 乙空闲")
	if !ok {
		t.Fatalf("找不到乙空闲行: %q", tt.Texts())
	}

	// 聚焦区自上而下：总管 -> 等你处理，位于「对话」段与项目树上方；运行中会话留在原段/原项目内。
	if stewardRow.Y >= pendingRow.Y || pendingRow.Y >= chatsHeader.Y {
		t.Fatalf("聚焦区应为总管在上、等你处理在下且都在「对话」段上方: steward=%v, pending=%v, chatsHeader=%v",
			stewardRow.Y, pendingRow.Y, chatsHeader.Y)
	}
	if chatsHeader.Y >= chatRunRow.Y || chatRunRow.Y >= chatIdleRow.Y || chatIdleRow.Y >= projAHeader.Y {
		t.Fatalf("对话运行与空闲会话应留在「对话」段内且位于项目列表之前: chatsHeader=%v, chatRun=%v, chatIdle=%v, projA=%v",
			chatsHeader.Y, chatRunRow.Y, chatIdleRow.Y, projAHeader.Y)
	}
	if projAHeader.Y >= projAIdleRow.Y || projBHeader.Y >= projBRunRow.Y || projBRunRow.Y >= projBIdleRow.Y {
		t.Fatalf("项目内运行中与空闲会话应留在各自项目下: projA=%v/%v, projB=%v/%v/%v",
			projAHeader.Y, projAIdleRow.Y, projBHeader.Y, projBRunRow.Y, projBIdleRow.Y)
	}

	// 聚焦区行左缘与总管行对齐，快速对话段内会话行与项目内会话行左缘对齐。
	if pendingRow.X != stewardRow.X || chatRunRow.X != projAIdleRow.X || chatIdleRow.X != projAIdleRow.X || projBRunRow.X != projAIdleRow.X {
		t.Fatalf("聚焦区行左缘应与总管行对齐，快速对话与项目内运行中会话行应与项目会话行对齐: steward=%v, pending=%v, chatRun=%v, chatIdle=%v, projBRun=%v, projAIdle=%v",
			stewardRow.X, pendingRow.X, chatRunRow.X, chatIdleRow.X, projBRunRow.X, projAIdleRow.X)
	}

	// 运行中会话不在聚焦区渲染（不带聚焦区项目标签），且各会话都不重复渲染。
	for _, title := range []string{"甲待审批", "对话运行", "乙运行"} {
		if n := countExactSidebarText(tt, "打开会话 "+title); n != 1 {
			t.Fatalf("会话 %q 应仅渲染 1 次，不能重复: got %d", title, n)
		}
	}
	if n := countText(tt, "项目甲"); n < 3 {
		t.Fatalf("项目甲名称应同时出现在聚焦区行内与项目标题行: got %d, texts=%q", n, tt.Texts())
	}
	if n := countText(tt, "项目乙"); n != 2 {
		t.Fatalf("项目乙名称应仅出现在项目标题行与标签中，不应出现在聚焦区: got %d, texts=%q", n, tt.Texts())
	}
}

// TestSidebarFocusZoneFallbackModes 验证有搜索词、过滤非「全部」、开启动态视图三种情况下，
// 都回到单一列表：没有聚焦区也没有重复。
func TestSidebarFocusZoneFallbackModes(t *testing.T) {
	t.Run("search_query", func(t *testing.T) {
		tt, state := buildSidebarFocusZoneFixture(t, true)
		*getSearchQuery(state) = "运行"
		state.invalidate()
		tt.Frame()

		chatsHeader, ok := tt.Find("快速对话")
		if !ok {
			t.Fatalf("搜索命中对话会话时应显示「快速对话」段: %q", tt.Texts())
		}
		chatRunRow, ok := tt.Find("打开会话 对话运行")
		if !ok {
			t.Fatalf("搜索应命中对话运行: %q", tt.Texts())
		}
		projBHeader, ok := tt.Find("收起项目 项目乙")
		if !ok {
			t.Fatalf("搜索应显示项目乙: %q", tt.Texts())
		}
		projBRunRow, ok := tt.Find("打开会话 乙运行")
		if !ok {
			t.Fatalf("搜索应命中乙运行: %q", tt.Texts())
		}
		if chatRunRow.Y <= chatsHeader.Y {
			t.Fatalf("搜索态应回到单一列表，「对话运行」应位于「对话」段下方: header=%v, row=%v", chatsHeader.Y, chatRunRow.Y)
		}
		if projBRunRow.Y <= projBHeader.Y {
			t.Fatalf("搜索态应回到单一列表，「乙运行」应位于项目乙下方: header=%v, row=%v", projBHeader.Y, projBRunRow.Y)
		}
		if n := countExactSidebarText(tt, "打开会话 对话运行"); n != 1 {
			t.Fatalf("搜索态不应重复渲染「对话运行」: got %d", n)
		}
		if n := countExactSidebarText(tt, "打开会话 乙运行"); n != 1 {
			t.Fatalf("搜索态不应重复渲染「乙运行」: got %d", n)
		}
	})

	t.Run("non_all_filter", func(t *testing.T) {
		tt, _ := buildSidebarFocusZoneFixture(t, true)
		if err := tt.Click("过滤对话"); err != nil {
			t.Fatalf("打开过滤菜单失败: %v", err)
		}
		tt.Frame()
		if err := tt.Click("过滤对话：运行中"); err != nil {
			t.Fatalf("选择「运行中」过滤失败: %v", err)
		}
		tt.Frame()

		chatsHeader, ok := tt.Find("快速对话")
		if !ok {
			t.Fatalf("非全部过滤应保留「快速对话」段: %q", tt.Texts())
		}
		chatRunRow, ok := tt.Find("打开会话 对话运行")
		if !ok {
			t.Fatalf("过滤运行中应显示「对话运行」: %q", tt.Texts())
		}
		projBHeader, ok := tt.Find("收起项目 项目乙")
		if !ok {
			t.Fatalf("过滤运行中应显示项目乙: %q", tt.Texts())
		}
		projBRunRow, ok := tt.Find("打开会话 乙运行")
		if !ok {
			t.Fatalf("过滤运行中应显示「乙运行」: %q", tt.Texts())
		}
		if chatRunRow.Y <= chatsHeader.Y || projBRunRow.Y <= projBHeader.Y {
			t.Fatalf("过滤态应回到各自段内（无聚焦区）: chats=%v/%v, projB=%v/%v",
				chatsHeader.Y, chatRunRow.Y, projBHeader.Y, projBRunRow.Y)
		}
		if _, ok := tt.Find("打开会话 甲待审批"); ok {
			t.Fatalf("过滤运行中不应显示待审批会话: %q", tt.Texts())
		}
		if _, ok := tt.Find("打开会话 甲空闲"); ok {
			t.Fatalf("过滤运行中不应显示空闲会话: %q", tt.Texts())
		}
		if n := countExactSidebarText(tt, "打开会话 对话运行"); n != 1 || countExactSidebarText(tt, "打开会话 乙运行") != 1 {
			t.Fatalf("过滤态不应重复渲染会话: chatRun=%d, bRun=%d",
				countExactSidebarText(tt, "打开会话 对话运行"), countExactSidebarText(tt, "打开会话 乙运行"))
		}
	})

	t.Run("activity_view", func(t *testing.T) {
		tt, state := buildSidebarFocusZoneFixture(t, true)
		state.toggleActivityView()
		tt.Frame()

		if _, ok := tt.Find("收起项目 项目甲"); ok {
			t.Fatalf("动态视图不应渲染项目树: %q", tt.Texts())
		}
		if _, ok := tt.Find("新建对话"); ok {
			t.Fatalf("动态视图不应渲染「对话」段: %q", tt.Texts())
		}
		if !tt.HasText("今天") {
			t.Fatalf("动态视图应显示时间分组「今天」: %q", tt.Texts())
		}
		for _, title := range []string{"甲待审批", "对话运行", "乙运行", "甲空闲"} {
			if n := countExactSidebarText(tt, "打开会话 "+title); n != 1 {
				t.Fatalf("动态视图下 %q 应只出现 1 次: got %d", title, n)
			}
		}

		// 关闭动态视图后恢复聚焦区（等你处理在上方，运行中回原位）。
		state.toggleActivityView()
		tt.Frame()
		chatsHeader, _ := tt.Find("快速对话")
		pendingRow, _ := tt.Find("打开会话 甲待审批")
		chatRunRow, _ := tt.Find("打开会话 对话运行")
		if pendingRow.Y >= chatsHeader.Y {
			t.Fatalf("退出动态视图后等你处理应恢复顶部聚焦区: pending=%v, chatsHeader=%v", pendingRow.Y, chatsHeader.Y)
		}
		if chatRunRow.Y <= chatsHeader.Y {
			t.Fatalf("退出动态视图后运行中应留在「对话」段内: chatRun=%v, chatsHeader=%v", chatRunRow.Y, chatsHeader.Y)
		}
	})
}

// TestSidebarFocusZoneHoverLatchAndSettle 验证指针悬停在聚焦区或下方项目树会话行上时，
// 聚焦区刚处理完的会话仍留在聚焦区，指针离开后才归位到下方对应项目。
func TestSidebarFocusZoneHoverLatchAndSettle(t *testing.T) {
	t.Run("hover_on_focus_row", func(t *testing.T) {
		tt, state := buildSidebarFocusZoneFixture(t, false)

		chatsHeader, ok := tt.Find("快速对话")
		if !ok {
			t.Fatalf("找不到「快速对话」段标题: %q", tt.Texts())
		}
		pendingRow, ok := tt.Find("打开会话 甲待审批")
		if !ok {
			t.Fatalf("找不到聚焦区会话「甲待审批」: %q", tt.Texts())
		}
		if pendingRow.Y >= chatsHeader.Y {
			t.Fatalf("初始时「甲待审批」应在聚焦区内: row=%v, chatsHeader=%v", pendingRow.Y, chatsHeader.Y)
		}

		// 指针悬停在聚焦区行上，此时待处理请求解除。
		tt.Move(pendingRow.X+pendingRow.W/2, pendingRow.Y+pendingRow.H/2)
		tt.Frame()
		state.applyThreadSummary(Thread{ID: "a-pending", ProjectID: "pa", HasPendingRequest: false})
		tt.Frame()

		held, ok := tt.Find("打开会话 甲待审批")
		if !ok {
			t.Fatalf("悬停期间会话不应消失: %q", tt.Texts())
		}
		if held.Y >= chatsHeader.Y {
			t.Fatalf("指针悬停时刚解除待处理的会话应留在聚焦区: held=%v, chatsHeader=%v", held.Y, chatsHeader.Y)
		}
		if n := countExactSidebarText(tt, "打开会话 甲待审批"); n != 1 {
			t.Fatalf("悬停冻结期间不应在下方重复: got %d", n)
		}

		// 指针离开列表，等待折叠退场动画完成后归位到项目甲下方。
		tt.Move(900, 600)
		tt.Frame()
		tt.Advance(DurBase + DurSnap + 50*time.Millisecond)
		tt.Frame()

		projAHeader, ok := tt.Find("收起项目 项目甲")
		if !ok {
			t.Fatalf("找不到项目甲标题行: %q", tt.Texts())
		}
		settled, ok := tt.Find("打开会话 甲待审批")
		if !ok {
			t.Fatalf("指针离开后应能在项目甲下找到「甲待审批」: %q", tt.Texts())
		}
		if settled.Y <= projAHeader.Y {
			t.Fatalf("指针离开后「甲待审批」应归位到项目甲下方: settled=%v, projA=%v", settled.Y, projAHeader.Y)
		}
		if n := countExactSidebarText(tt, "打开会话 甲待审批"); n != 1 {
			t.Fatalf("归位后不应重复渲染: got %d", n)
		}
	})

	t.Run("hover_on_project_row", func(t *testing.T) {
		tt, state := buildSidebarFocusZoneFixture(t, false)

		chatsHeader, ok := tt.Find("快速对话")
		if !ok {
			t.Fatalf("找不到「快速对话」段标题: %q", tt.Texts())
		}
		idleRow, ok := tt.Find("打开会话 乙空闲")
		if !ok {
			t.Fatalf("找不到项目乙空闲行: %q", tt.Texts())
		}

		// 指针停在下方项目树行上，聚焦区的「甲待审批」解除时也不应立刻缩回导致列表上移。
		tt.Move(idleRow.X+idleRow.W/2, idleRow.Y+idleRow.H/2)
		tt.Frame()
		state.applyThreadSummary(Thread{ID: "a-pending", ProjectID: "pa", HasPendingRequest: false})
		tt.Frame()

		held, ok := tt.Find("打开会话 甲待审批")
		if !ok || held.Y >= chatsHeader.Y {
			t.Fatalf("指针停在项目树行时聚焦区解除的会话应保持锁定: ok=%v, held=%v, chatsHeader=%v", ok, held.Y, chatsHeader.Y)
		}
		idleHeld, ok := tt.Find("打开会话 乙空闲")
		if !ok || idleHeld.Y != idleRow.Y {
			t.Fatalf("悬停的项目树行位置不应跳变: before=%v, after=%v", idleRow.Y, idleHeld.Y)
		}

		tt.Move(900, 600)
		tt.Frame()
		tt.Advance(DurBase + DurSnap + 50*time.Millisecond)
		tt.Frame()

		projAHeader, _ := tt.Find("收起项目 项目甲")
		settled, ok := tt.Find("打开会话 甲待审批")
		if !ok || settled.Y <= projAHeader.Y {
			t.Fatalf("指针离开后「甲待审批」应归位到项目甲下方: ok=%v, settled=%v, projA=%v", ok, settled.Y, projAHeader.Y)
		}
	})
}

// TestSidebarFocusZoneFilterControlInTopBar 验证过滤控件内嵌在顶栏搜索框里，
// 不在「对话」段标题行里，且开启或关闭动态视图都在顶栏搜索框内。
func TestSidebarFocusZoneFilterControlInTopBar(t *testing.T) {
	tt, state := buildSidebarFocusZoneFixture(t, true)

	searchOuter, ok := tt.Find("侧栏搜索框")
	if !ok {
		t.Fatalf("找不到侧栏搜索框: %q", tt.Texts())
	}
	searchBox, ok := tt.Find("搜索对话...")
	if !ok {
		t.Fatalf("找不到搜索输入框: %q", tt.Texts())
	}
	filterBtn, ok := tt.Find("过滤对话")
	if !ok {
		t.Fatalf("默认态顶栏应显示过滤按钮: %q", tt.Texts())
	}
	bellBtn, ok := tt.Find("开启动态视图 (" + shortcutDisplay("⌥⌘U") + ")")
	if !ok {
		t.Fatalf("找不到动态视图铃铛按钮: %q", tt.Texts())
	}
	chatsHeader, ok := tt.Find("快速对话")
	if !ok {
		t.Fatalf("找不到「快速对话」段标题: %q", tt.Texts())
	}
	addChatBtn, ok := tt.Find("新建对话")
	if !ok {
		t.Fatalf("找不到「新建对话」按钮: %q", tt.Texts())
	}

	// 过滤按钮与铃铛按钮内嵌在搜索框内部（输入区右侧），且位于「对话」段标题行上方。
	if filterBtn.X < searchBox.X+searchBox.W || filterBtn.X+filterBtn.W > bellBtn.X+1 {
		t.Fatalf("过滤按钮应位于搜索输入右侧与铃铛按钮左侧之间: search=%+v, filter=%+v, bell=%+v", searchBox, filterBtn, bellBtn)
	}
	if filterBtn.X < searchOuter.X || filterBtn.Y < searchOuter.Y ||
		bellBtn.X+bellBtn.W > searchOuter.X+searchOuter.W || bellBtn.Y+bellBtn.H > searchOuter.Y+searchOuter.H {
		t.Fatalf("过滤与铃铛按钮应落在搜索框容器内部: outer=%+v, filter=%+v, bell=%+v", searchOuter, filterBtn, bellBtn)
	}
	if filterBtn.Y+filterBtn.H > addChatBtn.Y || addChatBtn.Y > chatsHeader.Y || addChatBtn.Y+addChatBtn.H < chatsHeader.Y+chatsHeader.H {
		t.Fatalf("过滤按钮不应在「对话」段标题行里: filter=%+v, chatsHeader=%+v, addChat=%+v", filterBtn, chatsHeader, addChatBtn)
	}

	// 开启动态视图后过滤按钮仍在顶栏原位。
	state.toggleActivityView()
	tt.Frame()
	filterActivity, ok := tt.Find("过滤对话")
	if !ok {
		t.Fatalf("动态视图下顶栏仍应保留过滤按钮: %q", tt.Texts())
	}
	if filterActivity.Y != filterBtn.Y {
		t.Fatalf("动态视图下过滤按钮顶栏位置应保持不变: before=%+v, after=%+v", filterBtn, filterActivity)
	}
}

// TestSidebarFocusZoneMotionAndReduceMotion 验证聚焦区行进出复用列表折叠过渡：
// 普通模式下推进时钟到动画结束后不再请求下一帧；ReduceMotion 开启时立即完成且无多余帧。
func TestSidebarFocusZoneMotionAndReduceMotion(t *testing.T) {
	newIdleFixture := func(reduceMotion bool) (*ui.Tester, *chatState) {
		testenv.TempHome(t)
		resetSidebarStateGroupLatch()
		resetSidebarHoverCardForTest()
		fake := NewFakeClient()
		now := time.Now()
		fake.SetProjectsAndThreads(
			[]Project{{ID: "pa", Name: "项目甲", Path: "/tmp/mcode-fz-motion"}},
			map[string][]Thread{
				"pa": {{ID: "t1", ProjectID: "pa", Title: "动效会话", UpdatedAt: now}},
			},
		)
		state, view := buildRoot(fake)
		t.Cleanup(state.close)
		tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: reduceMotion}})
		waitFor(t, tt, func() bool {
			_, ok := tt.Find("打开会话 动效会话")
			return ok
		})
		return tt, state
	}

	t.Run("normal_motion_settles_with_no_idle_frames", func(t *testing.T) {
		setAppReduceMotion(t, false)
		tt, state := newIdleFixture(false)
		t.Cleanup(func() { resetSidebarStateGroupLatch(); resetSidebarHoverCardForTest() })

		// 触发进入聚焦区（使用待审批，避免 running 200ms 点阵干扰静止帧判定）。
		state.applyThreadSummary(Thread{ID: "t1", ProjectID: "pa", HasPendingRequest: true})
		tt.Frame()
		tt.Advance(DurBase + DurSnap + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("聚焦区行进入动画结束后不应继续请求下一帧")
		}
		projA, _ := tt.Find("收起项目 项目甲")
		rowIn, _ := tt.Find("打开会话 动效会话")
		if rowIn.Y >= projA.Y {
			t.Fatalf("待审批后会话应进入聚焦区: row=%v, projA=%v", rowIn.Y, projA.Y)
		}

		// 触发退出聚焦区，动画结束后同样不得空转重绘。
		state.applyThreadSummary(Thread{ID: "t1", ProjectID: "pa", HasPendingRequest: false})
		tt.Frame()
		tt.Advance(DurBase + DurSnap + 50*time.Millisecond)
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("聚焦区行退出动画结束后不应继续请求下一帧")
		}
		projA, _ = tt.Find("收起项目 项目甲")
		rowOut, _ := tt.Find("打开会话 动效会话")
		if rowOut.Y <= projA.Y {
			t.Fatalf("退出聚焦区后会话应回到项目甲下方: row=%v, projA=%v", rowOut.Y, projA.Y)
		}
	})

	t.Run("reduce_motion_immediate", func(t *testing.T) {
		setAppReduceMotion(t, true)
		tt, state := newIdleFixture(true)
		t.Cleanup(func() { resetSidebarStateGroupLatch(); resetSidebarHoverCardForTest() })

		state.applyThreadSummary(Thread{ID: "t1", ProjectID: "pa", HasPendingRequest: true})
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("ReduceMotion 开启时聚焦区进入不应请求过渡帧")
		}
		projA, _ := tt.Find("收起项目 项目甲")
		rowIn, _ := tt.Find("打开会话 动效会话")
		if rowIn.Y >= projA.Y {
			t.Fatalf("ReduceMotion 下会话应立即进入聚焦区: row=%v, projA=%v", rowIn.Y, projA.Y)
		}

		state.applyThreadSummary(Thread{ID: "t1", ProjectID: "pa", HasPendingRequest: false})
		tt.Frame()
		if tt.FramePending() {
			t.Fatal("ReduceMotion 开启时聚焦区退出不应请求过渡帧")
		}
		projA, _ = tt.Find("收起项目 项目甲")
		rowOut, _ := tt.Find("打开会话 动效会话")
		if rowOut.Y <= projA.Y {
			t.Fatalf("ReduceMotion 下会话应立即回到项目甲下方: row=%v, projA=%v", rowOut.Y, projA.Y)
		}
	})
}

// TestSidebarRunningThreadStaysInProjectFolder 验证 IsRunning 会话在默认视图下仍渲染在所属项目文件夹内，
// 行首显示运行点阵图标，并且不出现在顶部聚焦区。
func TestSidebarRunningThreadStaysInProjectFolder(t *testing.T) {
	setAppReduceMotion(t, false)
	tt, _ := buildSidebarFocusZoneFixture(t, false)

	chatsHeader, ok := tt.Find("快速对话")
	if !ok {
		t.Fatalf("找不到「快速对话」段标题: %q", tt.Texts())
	}
	projBHeader, ok := tt.Find("收起项目 项目乙")
	if !ok {
		t.Fatalf("找不到项目乙标题行: %q", tt.Texts())
	}
	row, ok := tt.Find("打开会话 乙运行")
	if !ok {
		t.Fatalf("IsRunning 会话应仍渲染在所属项目文件夹内: %q", tt.Texts())
	}
	if row.Y <= chatsHeader.Y || row.Y <= projBHeader.Y {
		t.Fatalf("运行中会话应位于项目乙标题行下方而非聚焦区: row=%v, chats=%v, projB=%v", row.Y, chatsHeader.Y, projBHeader.Y)
	}
	if n := countExactSidebarText(tt, "打开会话 乙运行"); n != 1 {
		t.Fatalf("运行中会话应只渲染 1 次: got %d", n)
	}
	if n := countText(tt, "项目乙"); n != 2 {
		t.Fatalf("运行中会话不应在聚焦区带项目名渲染: got %d, texts=%q", n, tt.Texts())
	}

	// 行首运行点阵图标在正常动效下随时间变化。
	iconBand := image.Rect(int(row.X), int(row.Y), int(row.X+30), int(row.Y+row.H))
	before := cloneImage(tt.Image())
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if !bandDiffers(before, tt.Image(), iconBand) {
		t.Fatal("运行中会话行首应渲染动态点阵运行图标")
	}
}

// TestSidebarCollapsedProjectShowsRunningGlyph 验证项目折叠且存在非休眠运行中会话时，
// 文件夹头渲染 12×12 点阵运行图标；展开或会话停止运行后恢复文件夹图标。
func TestSidebarCollapsedProjectShowsRunningGlyph(t *testing.T) {
	setAppReduceMotion(t, false)
	tt, state := buildSidebarFocusZoneFixture(t, false)

	// 展开状态下文件夹头显示静态文件夹图标，不显示运行点阵。
	expandedHeader, ok := tt.Find("收起项目 项目乙")
	if !ok {
		t.Fatalf("找不到项目乙标题行: %q", tt.Texts())
	}
	expandedIconBand := image.Rect(int(expandedHeader.X), int(expandedHeader.Y), int(expandedHeader.X+20), int(expandedHeader.Y+expandedHeader.H))
	expandedBefore := cloneImage(tt.Image())
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if bandDiffers(expandedBefore, tt.Image(), expandedIconBand) {
		t.Fatal("项目未折叠时文件夹头图标应保持静止")
	}

	// 折叠项目乙：其中有运行中会话 b-run，文件夹头应渲染动态点阵运行图标。
	if err := tt.Click("收起项目 项目乙"); err != nil {
		t.Fatalf("折叠项目乙失败: %v", err)
	}
	tt.Frame()

	collapsedHeader, ok := tt.Find("展开项目 项目乙")
	if !ok {
		t.Fatalf("项目乙应处于折叠状态: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 乙运行"); ok {
		t.Fatal("折叠后项目乙内会话行不应继续展开显示")
	}
	collapsedIconBand := image.Rect(int(collapsedHeader.X), int(collapsedHeader.Y), int(collapsedHeader.X+20), int(collapsedHeader.Y+collapsedHeader.H))
	runningBefore := cloneImage(tt.Image())
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if !bandDiffers(runningBefore, tt.Image(), collapsedIconBand) {
		t.Fatal("折叠项目乙中有运行中会话时，文件夹头应渲染动态点阵运行图标")
	}

	// 会话结束运行后，文件夹头恢复静态折叠文件夹图标。
	state.applyThreadSummary(Thread{ID: "b-run", ProjectID: "pb", IsRunning: false, LastStatus: "completed"})
	tt.Frame()
	stoppedBefore := cloneImage(tt.Image())
	tt.Advance(250 * time.Millisecond)
	tt.Frame()
	if bandDiffers(stoppedBefore, tt.Image(), collapsedIconBand) {
		t.Fatal("会话停止运行后，折叠文件夹头应恢复静态文件夹图标")
	}
}
