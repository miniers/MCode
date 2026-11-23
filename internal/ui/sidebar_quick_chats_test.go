package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func buildSidebarQuickChatsFixture(t *testing.T, chats []Thread, projThreads []Thread) (*ui.Tester, *chatState) {
	t.Helper()
	testenv.TempHome(t)
	setAppReduceMotion(t, true)
	resetSidebarStateGroupLatch()
	resetSidebarHoverCardForTest()
	t.Cleanup(func() {
		resetSidebarStateGroupLatch()
		resetSidebarHoverCardForTest()
	})

	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "p1", Name: "普通项目", Path: "/tmp/mcode-quick-chats-p1"},
		},
		map[string][]Thread{
			"chats-builtin": chats,
			"p1":            projThreads,
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTesterWith(view, 1000, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	waitFor(t, tt, func() bool {
		return tt.HasText("快速对话") && tt.HasText("普通项目")
	})
	return tt, state
}

// TestSidebarQuickChatsTitleAndNoLegacyLabel 验证标题文字为「快速对话」、显示计数，且不再出现旧的「对话」标签。
func TestSidebarQuickChatsTitleAndNoLegacyLabel(t *testing.T) {
	now := time.Now()
	tt, _ := buildSidebarQuickChatsFixture(t,
		[]Thread{
			{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now},
			{ID: "c2", ProjectID: "chats-builtin", Title: "闲聊二号", UpdatedAt: now.Add(-time.Minute)},
		},
		[]Thread{
			{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now.Add(-2 * time.Minute)},
		},
	)

	if _, ok := tt.Find("快速对话"); !ok {
		t.Fatalf("找不到「快速对话」标题: %q", tt.Texts())
	}
	if n := countExactSidebarText(tt, "对话"); n != 0 {
		t.Fatalf("不应再出现旧的「对话」段标签: got %d, texts=%q", n, tt.Texts())
	}
	if n := countExactSidebarText(tt, "(2)"); n != 1 {
		t.Fatalf("快速对话段应显示顶层会话计数 (2): got %d, texts=%q", n, tt.Texts())
	}
}

// TestSidebarQuickChatsAddButtonsAlwaysVisible 验证「快速对话」与项目标题行的两个「+」在未悬停时也可见且尺寸一致。
func TestSidebarQuickChatsAddButtonsAlwaysVisible(t *testing.T) {
	now := time.Now()
	tt, _ := buildSidebarQuickChatsFixture(t,
		[]Thread{
			{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now},
		},
		[]Thread{
			{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now.Add(-time.Minute)},
		},
	)

	// 将指针移到远离侧栏的区域，确认未悬停状态下两个「+」仍直接绘制可见。
	tt.Move(900, 600)
	tt.Frame()

	chatsAdd, ok := tt.Find("新建对话")
	if !ok {
		t.Fatalf("找不到「新建对话」按钮: %q", tt.Texts())
	}
	projAdd, ok := tt.Find("在此项目中新建会话")
	if !ok {
		t.Fatalf("找不到「在此项目中新建会话」按钮: %q", tt.Texts())
	}
	if chatsAdd.W != ControlMD || chatsAdd.H != ControlMD {
		t.Fatalf("快速对话「+」尺寸应为 ControlMD(%v): got %+v", ControlMD, chatsAdd)
	}
	if projAdd.W != ControlMD || projAdd.H != ControlMD {
		t.Fatalf("项目行「+」尺寸应为 ControlMD(%v): got %+v", ControlMD, projAdd)
	}

	img := tt.Image()
	if d := rectMaxChannelDelta(t, img, chatsAdd); d <= 10 {
		t.Fatalf("未悬停时快速对话「+」应可见，像素差=%d", d)
	}
	if d := rectMaxChannelDelta(t, img, projAdd); d <= 10 {
		t.Fatalf("未悬停时项目行「+」应可见，像素差=%d", d)
	}
}

// TestSidebarQuickChatsCollapseAndSearchExpand 验证点击标题行折叠/展开「快速对话」，
// 且输入搜索词时折叠态也能展示命中的会话。
func TestSidebarQuickChatsCollapseAndSearchExpand(t *testing.T) {
	now := time.Now()
	tt, state := buildSidebarQuickChatsFixture(t,
		[]Thread{
			{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now},
			{ID: "c2", ProjectID: "chats-builtin", Title: "专项排查", UpdatedAt: now.Add(-time.Minute)},
		},
		[]Thread{
			{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now.Add(-2 * time.Minute)},
		},
	)

	if _, ok := tt.Find("打开会话 闲聊一号"); !ok {
		t.Fatalf("初始展开态应显示闲聊一号: %q", tt.Texts())
	}

	// 点击标题行折叠，段内会话不再渲染，标题行与计数仍保留。
	if err := tt.Click("收起快速对话"); err != nil {
		t.Fatalf("点击收起快速对话失败: %v", err)
	}
	tt.Frame()

	state.mu.Lock()
	collapsed := state.collapsedProj["chats-builtin"]
	state.mu.Unlock()
	if !collapsed {
		t.Fatal("点击「快速对话」标题行后应写入 collapsedProj[chatsProjID]")
	}
	if _, ok := tt.Find("展开快速对话"); !ok {
		t.Fatalf("折叠后标题行标签应切为「展开快速对话」: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 闲聊一号"); ok {
		t.Fatalf("折叠后不应渲染段内会话「闲聊一号」: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 专项排查"); ok {
		t.Fatalf("折叠后不应渲染段内会话「专项排查」: %q", tt.Texts())
	}

	// 搜索时折叠态也展开并展示命中会话，且不改写折叠状态。
	*getSearchQuery(state) = "专项"
	state.invalidate()
	tt.Frame()

	if _, ok := tt.Find("打开会话 专项排查"); !ok {
		t.Fatalf("搜索时折叠态也应展示命中的「专项排查」: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 闲聊一号"); ok {
		t.Fatalf("搜索未命中的「闲聊一号」不应显示: %q", tt.Texts())
	}

	// 清空搜索恢复折叠；再次点击标题行恢复展开。
	*getSearchQuery(state) = ""
	state.invalidate()
	tt.Frame()
	if _, ok := tt.Find("打开会话 专项排查"); ok {
		t.Fatalf("清空搜索后应恢复折叠态: %q", tt.Texts())
	}

	if err := tt.Click("展开快速对话"); err != nil {
		t.Fatalf("点击展开快速对话失败: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("打开会话 闲聊一号"); !ok {
		t.Fatalf("再次点击标题行后应恢复显示「闲聊一号」: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 专项排查"); !ok {
		t.Fatalf("再次点击标题行后应恢复显示「专项排查」: %q", tt.Texts())
	}
}

// TestSidebarQuickChatsThreadTextLeftAlignedWithProject 验证快速对话段内会话行与项目内会话行文字左缘对齐。
func TestSidebarQuickChatsThreadTextLeftAlignedWithProject(t *testing.T) {
	now := time.Now()
	tt, _ := buildSidebarQuickChatsFixture(t,
		[]Thread{
			{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊对齐", UpdatedAt: now},
		},
		[]Thread{
			{ID: "t1", ProjectID: "p1", Title: "项目对齐", UpdatedAt: now.Add(-time.Minute)},
		},
	)

	chatRow, ok := tt.Find("打开会话 闲聊对齐")
	if !ok {
		t.Fatalf("找不到「打开会话 闲聊对齐」: %q", tt.Texts())
	}
	projRow, ok := tt.Find("打开会话 项目对齐")
	if !ok {
		t.Fatalf("找不到「打开会话 项目对齐」: %q", tt.Texts())
	}
	chatText, ok := tt.Find("闲聊对齐")
	if !ok {
		t.Fatalf("找不到会话标题「闲聊对齐」: %q", tt.Texts())
	}
	projText, ok := tt.Find("项目对齐")
	if !ok {
		t.Fatalf("找不到会话标题「项目对齐」: %q", tt.Texts())
	}
	headerText, ok := tt.Find("快速对话")
	if !ok {
		t.Fatalf("找不到「快速对话」标题: %q", tt.Texts())
	}

	if chatRow.X != projRow.X {
		t.Fatalf("快速对话行与项目会话行左缘应一致: chatRow=%v, projRow=%v", chatRow.X, projRow.X)
	}
	if chatText.X != projText.X {
		t.Fatalf("快速对话行与项目会话行文字左缘应一致: chatText=%v, projText=%v", chatText.X, projText.X)
	}
	if chatText.X <= headerText.X {
		t.Fatalf("段内会话文字应相对一级标题文字缩进: chatText=%v, headerText=%v", chatText.X, headerText.X)
	}
}

// TestSidebarQuickChatsHeaderVisibleWhenAllFocused 验证聚焦区开启且段内会话全都进入聚焦区时，标题行仍保留。
func TestSidebarQuickChatsHeaderVisibleWhenAllFocused(t *testing.T) {
	now := time.Now()
	tt, _ := buildSidebarQuickChatsFixture(t,
		[]Thread{
			{ID: "c-wait", ProjectID: "chats-builtin", Title: "对话待处理", HasPendingRequest: true, UpdatedAt: now},
		},
		[]Thread{
			{ID: "t1", ProjectID: "p1", Title: "项目空闲", UpdatedAt: now.Add(-time.Minute)},
		},
	)

	focusRow, ok := tt.Find("打开会话 对话待处理")
	if !ok {
		t.Fatalf("找不到聚焦区会话「对话待处理」: %q", tt.Texts())
	}
	chatsHeader, ok := tt.Find("快速对话")
	if !ok {
		t.Fatalf("会话全进聚焦区时仍应渲染「快速对话」标题行: %q", tt.Texts())
	}
	if _, ok := tt.Find("新建对话"); !ok {
		t.Fatalf("会话全进聚焦区时仍应保留「新建对话」按钮: %q", tt.Texts())
	}
	projHeader, ok := tt.Find("收起项目 普通项目")
	if !ok {
		t.Fatalf("找不到项目标题行: %q", tt.Texts())
	}
	if focusRow.Y >= chatsHeader.Y || chatsHeader.Y >= projHeader.Y {
		t.Fatalf("位置关系应为 聚焦区(%v) < 快速对话(%v) < 普通项目(%v)", focusRow.Y, chatsHeader.Y, projHeader.Y)
	}
	if n := countExactSidebarText(tt, "(0)"); n != 0 {
		t.Fatalf("段内无剩余会话时不应对外显示 (0): %q", tt.Texts())
	}
}

// TestSidebarQuickChatsHiddenWhenFilterEmpty 验证过滤非「全部」且段内无命中时，「快速对话」标题行不渲染。
func TestSidebarQuickChatsHiddenWhenFilterEmpty(t *testing.T) {
	now := time.Now()
	tt, _ := buildSidebarQuickChatsFixture(t,
		[]Thread{
			{ID: "c-idle", ProjectID: "chats-builtin", Title: "闲聊空闲", UpdatedAt: now},
		},
		[]Thread{
			{ID: "t-run", ProjectID: "p1", Title: "项目运行", IsRunning: true, UpdatedAt: now.Add(-time.Minute)},
		},
	)

	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("打开过滤菜单失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("过滤对话：运行中"); err != nil {
		t.Fatalf("选择「运行中」过滤失败: %v", err)
	}
	tt.Frame()

	if _, ok := tt.Find("快速对话"); ok {
		t.Fatalf("过滤非「全部」且快速对话无命中时不应渲染标题行: %q", tt.Texts())
	}
	if _, ok := tt.Find("新建对话"); ok {
		t.Fatalf("过滤非「全部」且快速对话无命中时不应渲染新建对话按钮: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 项目运行"); !ok {
		t.Fatalf("普通项目命中的「项目运行」仍应显示: %q", tt.Texts())
	}
}
