package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// (a) 只有对话项目、没有其他项目时，触发 createNewThread 会在对话项目创建会话，不弹失败 toast。
func TestChatsCreateNewThreadFallback(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	fake.SetProjectsAndThreads(
		[]Project{{ID: "chats-builtin", Name: "对话"}},
		map[string][]Thread{},
	)
	fake.SetProviders([]ProviderInfo{{ID: "claude", Name: "Claude", Available: true}})

	state := newChatState(fake)
	t.Cleanup(state.close)
	state.mu.Lock()
	state.projects = []Project{{ID: "chats-builtin", Name: "对话"}}
	state.newThreadProvider = "claude"
	state.mu.Unlock()

	state.createNewThread()

	waitUntil(t, "对话项目创建会话", func() bool {
		return fake.CreateThreadCalls() == 1
	})
	if got := fake.LastCreatedProjectID(); got != "chats-builtin" {
		t.Fatalf("应在对话项目创建会话, got %q", got)
	}
	state.mu.Lock()
	toast := state.toastMessage
	state.mu.Unlock()
	if toast != "" {
		t.Fatalf("不应弹出失败 toast, got %q", toast)
	}
}

// (b) 有普通项目且没有选中会话时，⌘N 仍落在普通项目。
func TestChatsCmdNUsesRegularProjectWhenUnselected(t *testing.T) {
	testenv.TempHome(t)
	withShortcutGOOS(t, "darwin")
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "对话"},
			{ID: "p1", Name: "普通项目"},
		},
		map[string][]Thread{},
	)
	fake.SetProviders([]ProviderInfo{{ID: "claude", Name: "Claude", Available: true}})

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return len(state.projects) == 2
	})

	state.mu.Lock()
	state.selectedThreadID = ""
	state.mu.Unlock()

	tt.Key(ui.Cmd, ui.KeyN)
	waitFor(t, tt, func() bool {
		return fake.CreateThreadCalls() == 1
	})
	if got := fake.LastCreatedProjectID(); got != "p1" {
		t.Fatalf("⌘N 无选中会话时应落在普通项目 p1, got %q", got)
	}
}

// (c) withoutProjects 剔除多个 id、忽略空 id、不改入参。
func TestChatsWithoutProjects(t *testing.T) {
	input := []Project{
		{ID: "p1", Name: "普通项目1"},
		{ID: "steward", Name: "总管项目"},
		{ID: "chats", Name: "对话项目"},
		{ID: "p2", Name: "普通项目2"},
	}

	got := withoutProjects(input, "", "steward", "chats", "")
	if len(got) != 2 || got[0].ID != "p1" || got[1].ID != "p2" {
		t.Fatalf("应剔除 steward 与 chats 并保留普通项目, got %+v", got)
	}
	if len(input) != 4 || input[1].ID != "steward" || input[2].ID != "chats" {
		t.Fatalf("不应修改入参切片, got %+v", input)
	}
	got[0].Name = "修改副本"
	if input[0].Name != "普通项目1" {
		t.Fatalf("返回的切片不应与入参共享底层数组, got %+v", input)
	}
}

// (d) 命令面板 buildPaletteItems 的项目项里没有对话项目。
func TestChatsPaletteExcludesChatsProject(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	state := newChatState(fake)
	t.Cleanup(state.close)

	state.mu.Lock()
	state.projects = []Project{
		{ID: "chats-builtin", Name: "对话"},
		{ID: "p1", Name: "普通项目"},
	}
	state.mu.Unlock()

	items := buildPaletteItems(state)
	var seenP1 bool
	for _, it := range items {
		if it.kind == "project" && it.id == "project-chats-builtin" {
			t.Fatalf("命令面板项目列表不应包含对话项目: %+v", it)
		}
		if it.kind == "project" && it.id == "project-p1" {
			seenP1 = true
		}
	}
	if !seenP1 {
		t.Fatal("命令面板项目列表应保留普通项目 p1")
	}
}

// (e) 侧栏项目树不含对话项目，且对话段的 + 点击后在对话项目创建会话。
func TestChatsSidebarSectionAddAndFilter(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "p1", Name: "普通项目"},
		},
		map[string][]Thread{
			"chats-builtin": {{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now}},
			"p1":            {{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("临时会话") && tt.HasText("闲聊一号") && tt.HasText("项目会话")
	})

	if _, ok := tt.Find("收起项目 内置对话"); ok {
		t.Fatal("侧栏项目树不应渲染对话项目")
	}
	if _, ok := tt.Find("展开项目 内置对话"); ok {
		t.Fatal("侧栏项目树不应渲染对话项目")
	}

	if err := tt.Click("新建对话"); err != nil {
		t.Fatalf("点击对话段 + 按钮失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		return fake.CreateThreadCalls() == 1
	})
	if got := fake.LastCreatedProjectID(); got != "chats-builtin" {
		t.Fatalf("点击对话段 + 应在对话项目创建会话, got %q", got)
	}
}

// (f) worktree 快捷键目标是对话项目时不调用 MoveToWorktree / 不建 worktree。
func TestChatsWorktreeShortcutSkipsWorktree(t *testing.T) {
	testenv.TempHome(t)
	withShortcutGOOS(t, "darwin")
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	fake.SetProjectsAndThreads(
		[]Project{{ID: "chats-builtin", Name: "对话"}},
		map[string][]Thread{
			"chats-builtin": {{ID: "c1", ProjectID: "chats-builtin", Title: "对话会话", UpdatedAt: time.Now()}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return state.selectedThreadID == "c1"
	})

	tt.Key(ui.Cmd|ui.Alt, ui.KeyN)
	waitFor(t, tt, func() bool {
		return fake.CreateThreadCalls() == 1
	})

	state.mu.Lock()
	newID := state.selectedThreadID
	isWorktree := state.provisionalWorktrees[newID]
	state.mu.Unlock()
	if newID == "" || newID == "c1" {
		t.Fatalf("快捷键应创建并选中新会话, got %q", newID)
	}
	if isWorktree {
		t.Fatal("对话项目不应标记为 provisionalWorktrees")
	}

	// 输入首个字符触发转正，确认不会调用 MoveToWorktree。
	state.mu.Lock()
	state.pendingDraftFill = nil
	state.draft = "你好"
	state.mu.Unlock()
	state.noteComposerDraft(newID, "你好")
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		th, ok := state.threadSnapshotLocked(newID)
		return ok && !th.IsProvisional
	})
	fake.mu.Lock()
	moveCalls := len(fake.moveToWorktreeCalls)
	fake.mu.Unlock()
	if moveCalls != 0 {
		t.Fatalf("对话项目触发 worktree 快捷键不应调用 MoveToWorktree, got %d", moveCalls)
	}
}

// (g) 选中总管会话时触发 createNewThread，落在第一个普通项目，不落在总管项目。
func TestChatsCreateNewThreadSkipsStewardThread(t *testing.T) {
	testenv.TempHome(t)
	fake := newStewardFakeClient("steward-t")
	fake.SetChatsProjectID("chats-builtin")
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "steward-p", Name: "总管项目"},
			{ID: "chats-builtin", Name: "对话"},
			{ID: "p1", Name: "普通项目"},
		},
		map[string][]Thread{
			"steward-p": {{ID: "steward-t", ProjectID: "steward-p", Title: "总管"}},
			"p1":        {{ID: "t1", ProjectID: "p1", Title: "普通会话"}},
		},
	)
	fake.SetProviders([]ProviderInfo{{ID: "claude", Name: "Claude", Available: true}})

	state := newChatState(fake)
	t.Cleanup(state.close)
	state.mu.Lock()
	state.projects = []Project{
		{ID: "steward-p", Name: "总管项目"},
		{ID: "chats-builtin", Name: "对话"},
		{ID: "p1", Name: "普通项目"},
	}
	state.threads = []Thread{
		{ID: "steward-t", ProjectID: "steward-p", Title: "总管"},
		{ID: "t1", ProjectID: "p1", Title: "普通会话"},
	}
	state.threadsByProj = map[string][]Thread{
		"steward-p": {{ID: "steward-t", ProjectID: "steward-p", Title: "总管"}},
		"p1":        {{ID: "t1", ProjectID: "p1", Title: "普通会话"}},
	}
	state.selectedThreadID = "steward-t"
	state.newThreadProvider = "claude"
	state.mu.Unlock()

	state.createNewThread()

	waitUntil(t, "在第一个普通项目创建会话", func() bool {
		return fake.CreateThreadCalls() == 1
	})
	if got := fake.LastCreatedProjectID(); got != "p1" {
		t.Fatalf("选中总管会话时新建会话应落在第一个普通项目 p1, got %q", got)
	}
}

// (h) 侧栏「对话」段仅在默认树视图且无搜索词时渲染，动态视图与搜索时不显示。
func TestChatsSidebarSectionHiddenInActivityAndSearch(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "p1", Name: "普通项目"},
		},
		map[string][]Thread{
			"chats-builtin": {{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now}},
			"p1":            {{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	// (a) 默认树视图下显示「临时会话」段。
	waitFor(t, tt, func() bool {
		_, hasNewChat := tt.Find("新建对话")
		return hasNewChat && tt.HasText("临时会话") && tt.HasText("闲聊一号") && tt.HasText("项目会话")
	})

	// (b) 打开动态视图后，「对话」段不再渲染。
	state.toggleActivityView()
	tt.Frame()
	if _, ok := tt.Find("新建对话"); ok {
		t.Fatal("动态视图下不应渲染「对话」段")
	}

	// 关闭动态视图后恢复显示。
	state.toggleActivityView()
	tt.Frame()
	if _, ok := tt.Find("新建对话"); !ok {
		t.Fatal("关闭动态视图后应恢复渲染「对话」段")
	}

	// (c) 侧栏搜索词非空时，「对话」段不渲染。
	*getSearchQuery(state) = "项目"
	state.invalidate()
	tt.Frame()
	if _, ok := tt.Find("新建对话"); ok {
		t.Fatal("侧栏搜索时不应渲染「对话」段")
	}
	if _, ok := tt.Find("打开会话 项目会话"); !ok {
		t.Fatal("侧栏搜索应保留命中的普通项目会话")
	}

	// 清空搜索词后恢复显示。
	*getSearchQuery(state) = ""
	state.invalidate()
	tt.Frame()
	if _, ok := tt.Find("新建对话"); !ok {
		t.Fatal("清空侧栏搜索词后应恢复渲染「对话」段")
	}
}

// (i) 项目树视图搜索「对话」会话标题时渲染「对话」段且仅列命中会话，对话项目名不参与匹配。
func TestChatsSidebarSearchTreeView(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "p1", Name: "普通项目"},
		},
		map[string][]Thread{
			"chats-builtin": {
				{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now},
				{ID: "c2", ProjectID: "chats-builtin", Title: "休眠闲聊", Dormant: true, UpdatedAt: now.Add(-time.Hour)},
				{ID: "c3", ProjectID: "chats-builtin", Title: "普通话题", UpdatedAt: now},
			},
			"p1": {
				{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("闲聊一号") && tt.HasText("项目会话")
	})

	*getSearchQuery(state) = "闲聊"
	state.invalidate()
	tt.Frame()

	for _, want := range []string{"新建对话", "打开会话 闲聊一号", "打开会话 休眠闲聊"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("树视图搜索应显示 %q: %q", want, tt.Texts())
		}
	}
	for _, unwanted := range []string{"打开会话 普通话题", "打开会话 项目会话", "收起项目 普通项目", "展开项目 普通项目"} {
		if _, ok := tt.Find(unwanted); ok {
			t.Fatalf("树视图搜索不应显示未命中项 %q: %q", unwanted, tt.Texts())
		}
	}

	// 对话项目名不参与匹配。
	*getSearchQuery(state) = "内置对话"
	state.invalidate()
	tt.Frame()
	if !tt.HasText("无结果") {
		t.Fatalf("对话项目名不应参与匹配，期望显示无结果卡片: %q", tt.Texts())
	}
}

// (j) 动态视图搜索能搜到「对话」项目里的活跃会话，并跳过休眠和子代理会话。
func TestChatsSidebarSearchActivityView(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "p1", Name: "普通项目"},
		},
		map[string][]Thread{
			"chats-builtin": {
				{ID: "c1", ProjectID: "chats-builtin", Title: "闲聊一号", UpdatedAt: now},
				{ID: "c2", ProjectID: "chats-builtin", Title: "休眠闲聊", Dormant: true, UpdatedAt: now.Add(-time.Hour)},
				{ID: "c3", ProjectID: "chats-builtin", Title: "闲聊子代理", IsHelper: true, ParentThreadID: "c1", UpdatedAt: now},
				{ID: "c4", ProjectID: "chats-builtin", Title: "无关对话", UpdatedAt: now},
			},
			"p1": {
				{ID: "t1", ProjectID: "p1", Title: "项目闲聊", UpdatedAt: now},
				{ID: "t2", ProjectID: "p1", Title: "普通会话", UpdatedAt: now},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("闲聊一号") && tt.HasText("项目闲聊")
	})

	state.toggleActivityView()
	*getSearchQuery(state) = "闲聊"
	state.invalidate()
	tt.Frame()

	for _, want := range []string{"打开会话 闲聊一号", "打开会话 项目闲聊"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("动态视图搜索应显示 %q: %q", want, tt.Texts())
		}
	}
	for _, unwanted := range []string{"新建对话", "打开会话 休眠闲聊", "打开会话 闲聊子代理", "打开会话 无关对话", "打开会话 普通会话"} {
		if _, ok := tt.Find(unwanted); ok {
			t.Fatalf("动态视图搜索不应显示 %q: %q", unwanted, tt.Texts())
		}
	}
	if tt.HasText("无结果") {
		t.Fatalf("动态视图搜索有命中时不应显示无结果卡片: %q", tt.Texts())
	}
}

// (k) 只有「对话」会话命中时，不显示无结果卡片。
func TestChatsSidebarSearchOnlyChatsNoEmptyCard(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "内置对话"},
			{ID: "p1", Name: "普通项目"},
		},
		map[string][]Thread{
			"chats-builtin": {
				{ID: "c1", ProjectID: "chats-builtin", Title: "专属闲聊", UpdatedAt: now},
			},
			"p1": {
				{ID: "t1", ProjectID: "p1", Title: "项目会话", UpdatedAt: now},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("专属闲聊") && tt.HasText("项目会话")
	})

	*getSearchQuery(state) = "专属"
	state.invalidate()
	tt.Frame()

	if _, ok := tt.Find("打开会话 专属闲聊"); !ok {
		t.Fatalf("仅对话命中时应显示命中会话: %q", tt.Texts())
	}
	if tt.HasText("无结果") {
		t.Fatalf("仅对话命中时不应显示无结果卡片: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 项目会话"); ok {
		t.Fatalf("未命中的普通项目会话不应显示: %q", tt.Texts())
	}

	state.toggleActivityView()
	tt.Frame()
	if _, ok := tt.Find("打开会话 专属闲聊"); !ok {
		t.Fatalf("动态视图下仅对话命中时应显示命中会话: %q", tt.Texts())
	}
	if tt.HasText("无结果") {
		t.Fatalf("动态视图下仅对话命中时不应显示无结果卡片: %q", tt.Texts())
	}
}

func TestChatsMoveToProjectMenu(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "对话"},
			{ID: "p1", Name: "普通项目"},
			{ID: "p2", Name: "其他项目"},
		},
		map[string][]Thread{
			"chats-builtin": {{ID: "c1", ProjectID: "chats-builtin", Title: "对话会话", UpdatedAt: now}},
			"p1":            {{ID: "t1", ProjectID: "p1", Title: "普通会话", UpdatedAt: now}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("对话会话") && tt.HasText("普通会话")
	})

	// 1. 普通会话右键菜单：有「迁入 Worktree…」，没有「移到项目…」。
	if err := tt.RightClick("普通会话"); err != nil {
		t.Fatalf("RightClick 普通会话失败: %v", err)
	}
	tt.Frame()
	normalMenu := tt.Menu()
	var hasWorktree, hasMoveToProject bool
	for _, item := range normalMenu {
		if item == "迁入 Worktree…" {
			hasWorktree = true
		}
		if item == "移到项目…" {
			hasMoveToProject = true
		}
	}
	if !hasWorktree || hasMoveToProject {
		t.Fatalf("普通会话菜单应含「迁入 Worktree…」且不含「移到项目…」，实际: %v", normalMenu)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	// 2. 对话会话右键菜单：有「移到项目…」，没有「迁入 Worktree…」。
	if err := tt.RightClick("对话会话"); err != nil {
		t.Fatalf("RightClick 对话会话失败: %v", err)
	}
	tt.Frame()
	chatsMenu := tt.Menu()
	hasWorktree, hasMoveToProject = false, false
	for _, item := range chatsMenu {
		if item == "迁入 Worktree…" {
			hasWorktree = true
		}
		if item == "移到项目…" {
			hasMoveToProject = true
		}
	}
	if !hasMoveToProject || hasWorktree {
		t.Fatalf("对话会话菜单应含「移到项目…」且不含「迁入 Worktree…」，实际: %v", chatsMenu)
	}

	// 3. 点击「移到项目…」打开搜索浮层，输入关键词过滤并回车确认移动。
	if err := tt.ChooseMenuItem("移到项目…"); err != nil {
		t.Fatalf("ChooseMenuItem 移到项目… 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("移到项目")
		return ok && tt.Focused("搜索项目")
	})
	if _, ok := tt.AccessByRole(ui.RoleButton, "其他项目"); !ok {
		t.Fatal("打开「移到项目」浮层后应显示「其他项目」")
	}
	tt.Type("普通")
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleButton, "其他项目"); ok {
		t.Fatal("搜索「普通」后「其他项目」不应再显示")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "普通项目"); !ok {
		t.Fatal("搜索「普通」后「普通项目」应仍显示")
	}
	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		calls := len(fake.moveThreadToProjectCalls)
		fake.mu.Unlock()
		state.mu.Lock()
		inTarget := false
		for _, th := range state.threadsByProj["p1"] {
			if th.ID == "c1" {
				inTarget = true
				break
			}
		}
		inChats := len(state.threadsByProj["chats-builtin"])
		state.mu.Unlock()
		return calls == 1 && inTarget && inChats == 0
	})
	state.mu.Lock()
	selected := state.selectedThreadID
	state.mu.Unlock()
	if selected != "c1" {
		t.Fatalf("移动后应保持选中该会话 c1, got %q", selected)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("移到项目")
		return !ok
	})

	// 4. 重新打开浮层后按 Esc，应关闭浮层且不触发移动。
	fake2 := NewFakeClient()
	fake2.SetChatsProjectID("chats-builtin")
	fake2.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "对话"},
			{ID: "p1", Name: "普通项目"},
			{ID: "p2", Name: "其他项目"},
		},
		map[string][]Thread{
			"chats-builtin": {{ID: "c2", ProjectID: "chats-builtin", Title: "对话会话二", UpdatedAt: now}},
		},
	)
	state2, view2 := buildRoot(fake2)
	t.Cleanup(state2.close)
	tt2 := ui.NewTester(view2, 1000, 700)
	waitFor(t, tt2, func() bool {
		return tt2.HasText("对话会话二")
	})
	if err := tt2.RightClick("对话会话二"); err != nil {
		t.Fatalf("RightClick 对话会话二失败: %v", err)
	}
	tt2.Frame()
	if err := tt2.ChooseMenuItem("移到项目…"); err != nil {
		t.Fatalf("ChooseMenuItem 移到项目… 失败: %v", err)
	}
	waitFor(t, tt2, func() bool {
		_, ok := tt2.Find("移到项目")
		return ok && tt2.Focused("搜索项目")
	})
	tt2.Key(0, ui.KeyEscape)
	waitFor(t, tt2, func() bool {
		_, ok := tt2.Find("移到项目")
		return !ok
	})
	fake2.mu.Lock()
	escCalls := len(fake2.moveThreadToProjectCalls)
	fake2.mu.Unlock()
	if escCalls != 0 {
		t.Fatalf("按 Esc 关闭浮层不应发生移动调用, got %d", escCalls)
	}
}

func TestChatsMoveToProjectHoverSelect(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "对话"},
			{ID: "p1", Name: "普通项目"},
			{ID: "p2", Name: "其他项目"},
		},
		map[string][]Thread{
			"chats-builtin": {{ID: "c1", ProjectID: "chats-builtin", Title: "对话会话", UpdatedAt: now}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("对话会话")
	})
	if err := tt.RightClick("对话会话"); err != nil {
		t.Fatalf("RightClick 对话会话失败: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("移到项目…"); err != nil {
		t.Fatalf("ChooseMenuItem 移到项目… 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("移到项目")
		return ok && tt.Focused("搜索项目")
	})
	tt.Advance(500 * time.Millisecond)
	tt.Frame()

	target, ok := tt.AccessByRole(ui.RoleButton, "其他项目")
	if !ok || target.Bounds.W <= 0 || target.Bounds.H <= 0 {
		t.Fatalf("未找到「其他项目」按钮: ok=%v bounds=%+v", ok, target.Bounds)
	}
	tt.Move(target.Bounds.X+target.Bounds.W/2, target.Bounds.Y+target.Bounds.H/2)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)

	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.moveThreadToProjectCalls) == 1
	})
	fake.mu.Lock()
	calls := append([][2]string(nil), fake.moveThreadToProjectCalls...)
	fake.mu.Unlock()
	if len(calls) != 1 || calls[0] != [2]string{"c1", "p2"} {
		t.Fatalf("悬停「其他项目」后回车应移动到 p2，实际调用: %v", calls)
	}
}

func TestChatsMoveToProjectKeyboardScroll(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	projects := make([]Project, 0, 21)
	projects = append(projects, Project{ID: "chats-builtin", Name: "对话"})
	for i := 1; i <= 20; i++ {
		projects = append(projects, Project{
			ID:   fmt.Sprintf("p%02d", i),
			Name: fmt.Sprintf("项目-%02d", i),
		})
	}
	fake.SetProjectsAndThreads(
		projects,
		map[string][]Thread{
			"chats-builtin": {{ID: "c1", ProjectID: "chats-builtin", Title: "对话会话", UpdatedAt: now}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("对话会话")
	})
	if err := tt.RightClick("对话会话"); err != nil {
		t.Fatalf("RightClick 对话会话失败: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("移到项目…"); err != nil {
		t.Fatalf("ChooseMenuItem 移到项目… 失败: %v", err)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("移到项目")
		return ok && tt.Focused("搜索项目")
	})
	tt.Advance(500 * time.Millisecond)
	tt.Frame()

	first, ok := tt.AccessByRole(ui.RoleButton, "项目-01")
	if !ok {
		t.Fatal("未找到「项目-01」按钮")
	}
	// 先把指针停在第一行，验证后续键盘下移与滚动不会被静止悬停抢回。
	tt.Move(first.Bounds.X+first.Bounds.W/2, first.Bounds.Y+first.Bounds.H/2)
	tt.Frame()

	card, ok := tt.Find("移到项目")
	if !ok {
		t.Fatal("未找到「移到项目」浮层卡片")
	}
	lastBefore, ok := tt.AccessByRole(ui.RoleButton, "项目-20")
	if !ok {
		t.Fatal("未找到「项目-20」按钮")
	}
	if lastBefore.Bounds.Y+lastBefore.Bounds.H <= card.Y+card.H {
		t.Fatalf("初始时「项目-20」应超出浮层可视区，card=%+v last=%+v", card, lastBefore.Bounds)
	}

	for range 19 {
		tt.Key(0, ui.KeyDown)
		tt.Frame()
	}

	lastAfter, ok := tt.AccessByRole(ui.RoleButton, "项目-20")
	if !ok {
		t.Fatal("下移后未找到「项目-20」按钮")
	}
	if lastAfter.Bounds.Y < card.Y || lastAfter.Bounds.Y+lastAfter.Bounds.H > card.Y+card.H {
		t.Fatalf("键盘下移后「项目-20」应滚入浮层可视区，card=%+v last=%+v", card, lastAfter.Bounds)
	}

	tt.Key(0, ui.KeyEnter)
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.moveThreadToProjectCalls) == 1
	})
	fake.mu.Lock()
	calls := append([][2]string(nil), fake.moveThreadToProjectCalls...)
	fake.mu.Unlock()
	if len(calls) != 1 || calls[0] != [2]string{"c1", "p20"} {
		t.Fatalf("键盘下移到最后一项后回车应移动到 p20，实际调用: %v", calls)
	}
}

// TestFilterSidebarThreads 验证过滤条件：等待优先于运行、子代理归属父会话、活跃时间窗口。
func TestFilterSidebarThreads(t *testing.T) {
	now := time.Now()
	busy := func(th Thread) bool { return th.IsRunning }

	threads := []Thread{
		{ID: "running", ProjectID: "chats", Title: "运行", IsRunning: true, UpdatedAt: now},
		{ID: "running-old", ProjectID: "chats", Title: "运行中带旧标记", IsRunning: true, UpdatedAt: now},
		{ID: "waiting", ProjectID: "chats", Title: "等待", HasPendingRequest: true, UpdatedAt: now},
		{ID: "finished", ProjectID: "chats", Title: "完成未看", UpdatedAt: now},
		{ID: "unread", ProjectID: "chats", Title: "未读", HasUnread: true, UpdatedAt: now},
		{ID: "parent", ProjectID: "chats", Title: "父会话", UpdatedAt: now},
		{ID: "bee", ProjectID: "chats", Title: "子代理", IsHelper: true, ParentThreadID: "parent", HasPendingRequest: true, UpdatedAt: now},
		{ID: "recent", ProjectID: "chats", Title: "今日", UpdatedAt: now.Add(-2 * time.Hour)},
		{ID: "week", ProjectID: "chats", Title: "本周", UpdatedAt: now.Add(-5 * 24 * time.Hour)},
		{ID: "old", ProjectID: "chats", Title: "陈旧", UpdatedAt: now.Add(-10 * 24 * time.Hour)},
		{ID: "ancient", ProjectID: "chats", Title: "远古", UpdatedAt: now.Add(-40 * 24 * time.Hour)},
	}
	state := &chatState{turnFinished: map[string]string{
		"finished":    turnDoneKindDone,
		"running-old": turnDoneKindDone,
	}}
	ids := func(list []Thread) []string {
		out := make([]string, 0, len(list))
		for _, th := range list {
			out = append(out, th.ID)
		}
		return out
	}
	cases := []struct {
		filter sidebarThreadFilter
		want   []string
	}{
		{sidebarFilterAll, []string{"running", "running-old", "waiting", "finished", "unread", "parent", "bee", "recent", "week", "old", "ancient"}},
		{sidebarFilterRunning, []string{"running", "running-old"}},
		{sidebarFilterWaiting, []string{"waiting", "finished", "unread", "parent", "bee"}},
		{sidebarFilterActive24h, []string{"running", "running-old", "waiting", "finished", "unread", "parent", "bee", "recent"}},
		{sidebarFilterActive7d, []string{"running", "running-old", "waiting", "finished", "unread", "parent", "bee", "recent", "week"}},
		{sidebarFilterActive30d, []string{"running", "running-old", "waiting", "finished", "unread", "parent", "bee", "recent", "week", "old"}},
	}
	for _, tc := range cases {
		got := ids(state.filterSidebarThreads(threads, tc.filter, now, busy))
		if len(got) != len(tc.want) {
			t.Fatalf("filter %d: got %v, want %v", tc.filter, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("filter %d: got %v, want %v", tc.filter, got, tc.want)
			}
		}
	}
}

// TestChatsSidebarFilterMenu 验证过滤按钮在「+」左侧打开下拉框，选择条件后列表即时收窄。
func TestChatsSidebarFilterMenu(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "chats-builtin", Name: "对话"}},
		map[string][]Thread{
			"chats-builtin": {
				{ID: "c1", ProjectID: "chats-builtin", Title: "运行对话", IsRunning: true, UpdatedAt: now},
				{ID: "c2", ProjectID: "chats-builtin", Title: "等待对话", HasPendingRequest: true, UpdatedAt: now},
				{ID: "c3", ProjectID: "chats-builtin", Title: "今日对话", UpdatedAt: now},
				{ID: "c4", ProjectID: "chats-builtin", Title: "陈旧对话", UpdatedAt: now.Add(-10 * 24 * time.Hour)},
				{ID: "c5", ProjectID: "chats-builtin", Title: "远古对话", UpdatedAt: now.Add(-40 * 24 * time.Hour)},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("运行对话") && tt.HasText("等待对话") && tt.HasText("今日对话") &&
			tt.HasText("陈旧对话") && tt.HasText("远古对话")
	})

	choose := func(label string) {
		t.Helper()
		if err := tt.Click("过滤对话"); err != nil {
			t.Fatalf("打开过滤下拉框失败: %v", err)
		}
		tt.Frame()
		if err := tt.Click("过滤对话：" + label); err != nil {
			t.Fatalf("选择过滤条件 %q 失败: %v", label, err)
		}
		tt.Frame()
	}
	expectVisible := func(want []string, hidden []string) {
		t.Helper()
		for _, s := range want {
			if _, ok := tt.Find("打开会话 " + s); !ok {
				t.Fatalf("过滤后应显示会话行 %q: %q", s, tt.Texts())
			}
		}
		for _, s := range hidden {
			if _, ok := tt.Find("打开会话 " + s); ok {
				t.Fatalf("过滤后不应显示会话行 %q: %q", s, tt.Texts())
			}
		}
	}

	choose("运行中")
	expectVisible([]string{"运行对话"}, []string{"等待对话", "今日对话", "陈旧对话", "远古对话"})

	choose("等待中")
	expectVisible([]string{"等待对话"}, []string{"运行对话", "今日对话", "陈旧对话", "远古对话"})

	choose("最近 24 小时活跃")
	expectVisible([]string{"运行对话", "等待对话", "今日对话"}, []string{"陈旧对话", "远古对话"})

	choose("最近 7 天活跃")
	expectVisible([]string{"今日对话"}, []string{"陈旧对话", "远古对话"})

	choose("最近 30 天活跃")
	expectVisible([]string{"今日对话", "陈旧对话"}, []string{"远古对话"})

	choose("全部")
	expectVisible([]string{"运行对话", "等待对话", "今日对话", "陈旧对话", "远古对话"}, nil)

	state.mu.Lock()
	filter := state.sidebarFilter
	state.mu.Unlock()
	if filter != sidebarFilterAll {
		t.Fatalf("选择全部后过滤条件应重置, got %d", filter)
	}
}

// TestSidebarFilterAppliesToProjectSessions 验证过滤不仅作用于「对话」段，也收窄普通项目下的会话；
// 过滤后没有命中会话的项目不再显示。
func TestSidebarFilterAppliesToProjectSessions(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{
			{ID: "chats-builtin", Name: "对话"},
			{ID: "p1", Name: "项目一"},
			{ID: "p2", Name: "项目二"},
		},
		map[string][]Thread{
			"p1": {
				{ID: "r1", ProjectID: "p1", Title: "项目运行", IsRunning: true, UpdatedAt: now},
				{ID: "w1", ProjectID: "p1", Title: "项目等待", HasPendingRequest: true, UpdatedAt: now},
			},
			"p2": {
				{ID: "o1", ProjectID: "p2", Title: "项目普通", UpdatedAt: now},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("项目运行") && tt.HasText("项目等待") && tt.HasText("项目普通")
	})

	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("打开过滤下拉框失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("过滤对话：运行中"); err != nil {
		t.Fatalf("选择运行中失败: %v", err)
	}
	tt.Frame()

	if _, ok := tt.Find("打开会话 项目运行"); !ok {
		t.Fatalf("运行中的项目会话应显示: %q", tt.Texts())
	}
	for _, hidden := range []string{"打开会话 项目等待", "打开会话 项目普通"} {
		if _, ok := tt.Find(hidden); ok {
			t.Fatalf("过滤运行中后不应显示 %q: %q", hidden, tt.Texts())
		}
	}
	if _, ok := tt.Find("收起项目 项目二"); ok {
		t.Fatalf("没有命中会话的项目二不应显示: %q", tt.Texts())
	}
	if _, ok := tt.Find("展开项目 项目二"); ok {
		t.Fatalf("没有命中会话的项目二不应显示: %q", tt.Texts())
	}
}

// TestSidebarFilterEmptyState 验证过滤把全部会话收窄掉时给出明确的空列表提示。
func TestSidebarFilterEmptyState(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	fake.SetProjectsAndThreads(
		[]Project{{ID: "chats-builtin", Name: "对话"}, {ID: "p1", Name: "项目一"}},
		map[string][]Thread{
			"p1": {{ID: "o1", ProjectID: "p1", Title: "项目普通", UpdatedAt: time.Now()}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("项目普通") })

	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("打开过滤下拉框失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("过滤对话：运行中"); err != nil {
		t.Fatalf("选择运行中失败: %v", err)
	}
	tt.Frame()

	if _, ok := tt.Find("打开会话 项目普通"); ok {
		t.Fatalf("过滤后不应显示普通会话: %q", tt.Texts())
	}
	if !tt.HasText("没有符合过滤的会话") {
		t.Fatalf("过滤后全部隐藏时应显示空列表提示: %q", tt.Texts())
	}
}

// TestSidebarFilterActivityView 验证动态视图沿用过滤条件，且在顶栏保留过滤入口。
func TestSidebarFilterActivityView(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "chats-builtin", Name: "对话"}, {ID: "p1", Name: "项目一"}},
		map[string][]Thread{
			"p1": {
				{ID: "r1", ProjectID: "p1", Title: "项目运行", IsRunning: true, UpdatedAt: now},
				{ID: "o1", ProjectID: "p1", Title: "项目普通", UpdatedAt: now},
			},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("项目运行") && tt.HasText("项目普通")
	})

	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("打开过滤下拉框失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("过滤对话：运行中"); err != nil {
		t.Fatalf("选择运行中失败: %v", err)
	}
	tt.Frame()

	state.toggleActivityView()
	tt.Frame()
	if _, ok := tt.Find("打开会话 项目运行"); !ok {
		t.Fatalf("动态视图应保留运行中的会话: %q", tt.Texts())
	}
	if _, ok := tt.Find("打开会话 项目普通"); ok {
		t.Fatalf("动态视图不应显示被过滤掉的会话: %q", tt.Texts())
	}
	if _, ok := tt.Find("过滤对话"); !ok {
		t.Fatalf("动态视图应保留过滤入口: %q", tt.Texts())
	}

	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("动态视图打开过滤下拉框失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("过滤对话：全部"); err != nil {
		t.Fatalf("选择全部失败: %v", err)
	}
	tt.Frame()
	for _, want := range []string{"打开会话 项目运行", "打开会话 项目普通"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("清除过滤后动态视图应显示 %q: %q", want, tt.Texts())
		}
	}
}

// TestSidebarFilterWaitingIncludesFinishedUnseen 验证刚跑完但用户还没查看的会话归入等待中；
// 它同时也应被「运行中」排除。
func TestSidebarFilterWaitingIncludesFinishedUnseen(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	fake.SetProjectsAndThreads(
		[]Project{{ID: "chats-builtin", Name: "对话"}, {ID: "p1", Name: "项目一"}},
		map[string][]Thread{
			"p1": {{ID: "f1", ProjectID: "p1", Title: "完成未看", UpdatedAt: time.Now()}},
		},
	)

	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开会话 完成未看")
		return ok
	})

	// 模拟这一轮刚结束、用户还没打开会话：侧栏会亮完成角标并把标题染成强调色。
	state.mu.Lock()
	state.markTurnFinishedLocked("f1", "completed")
	state.mu.Unlock()
	state.invalidate()
	tt.Frame()

	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("打开过滤下拉框失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("过滤对话：等待中"); err != nil {
		t.Fatalf("选择等待中失败: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("打开会话 完成未看"); !ok {
		t.Fatalf("刚结束未查看的会话应归入等待中: %q", tt.Texts())
	}

	if err := tt.Click("过滤对话"); err != nil {
		t.Fatalf("再次打开过滤下拉框失败: %v", err)
	}
	tt.Frame()
	if err := tt.Click("过滤对话：运行中"); err != nil {
		t.Fatalf("选择运行中失败: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("打开会话 完成未看"); ok {
		t.Fatalf("已结束的会话不应归入运行中: %q", tt.Texts())
	}
}

// TestSidebarChatsIdleBeesHiddenUntilSelected 验证内置「对话」段同样遵守空闲子代理默认隐藏、
// 点击会话标题选中后直接展开头像条、切走后自动隐藏的规则。
func TestSidebarChatsIdleBeesHiddenUntilSelected(t *testing.T) {
	testenv.TempHome(t)
	ResetBeeFolded()
	ResetBeeShowAll()
	resetSidebarHoverCardForTest()
	t.Cleanup(func() {
		ResetBeeFolded()
		ResetBeeShowAll()
		resetSidebarHoverCardForTest()
	})

	fake := NewFakeClient()
	fake.SetChatsProjectID("chats-builtin")
	now := time.Now()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "chats-builtin", Name: "对话"}},
		map[string][]Thread{
			"chats-builtin": {
				{ID: "c-other", ProjectID: "chats-builtin", Title: "对话其他", UpdatedAt: now},
				{ID: "c-lead", ProjectID: "chats-builtin", Title: "对话主会话", UpdatedAt: now.Add(-time.Minute)},
				{ID: "c-bee-1", ProjectID: "chats-builtin", Title: "Aspen: 对话子任务 1", ParentThreadID: "c-lead", IsHelper: true, LastStatus: "completed", UpdatedAt: now.Add(-3 * time.Minute)},
				{ID: "c-bee-2", ProjectID: "chats-builtin", Title: "Birch: 对话子任务 2", ParentThreadID: "c-lead", IsHelper: true, LastStatus: "failed", UpdatedAt: now.Add(-2 * time.Minute)},
			},
		},
	)

	state, view := buildRoot(fake, WithSettings(settingsTestDir(t), nil))
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText("对话其他") && tt.HasText("对话主会话")
	})
	if selectedThreadIDOf(state) != "c-other" {
		if err := tt.Click("对话其他"); err != nil {
			t.Fatalf("选中对话其他失败: %v", err)
		}
		waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "c-other" })
	}
	tt.Move(0, 0)
	tt.Advance(DurSlow + 200*time.Millisecond)
	tt.Frame()

	if tt.HasText("2 个子代理") {
		t.Fatalf("未选中对话主会话时不应显示 '2 个子代理'，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("Aspen"); ok {
		t.Fatalf("未选中对话主会话时不应显示 Aspen，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("Birch"); ok {
		t.Fatalf("未选中对话主会话时不应显示 Birch，texts: %q", tt.Texts())
	}

	if err := tt.Click("对话主会话"); err != nil {
		t.Fatalf("点击对话主会话失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "c-lead" })
	tt.Move(0, 0)
	tt.Advance(DurSlow + 200*time.Millisecond)
	tt.Frame()

	if tt.HasText("2 个子代理") {
		t.Fatalf("选中对话主会话后不应显示 '2 个子代理'，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("Aspen"); !ok {
		t.Fatalf("选中对话主会话后应直接显示 Aspen，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("Birch"); !ok {
		t.Fatalf("选中对话主会话后应直接显示 Birch，texts: %q", tt.Texts())
	}

	if err := tt.Click("Aspen"); err != nil {
		t.Fatalf("点击子代理 Aspen 失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "c-bee-1" })
	tt.Move(0, 0)
	tt.Frame()
	if _, ok := tt.Find("Birch"); !ok {
		t.Fatalf("选中 Aspen 后 Birch 仍应保持可见，texts: %q", tt.Texts())
	}

	if err := tt.Click("对话其他"); err != nil {
		t.Fatalf("切回对话其他失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "c-other" })
	tt.Move(0, 0)
	tt.Advance(DurSlow + 200*time.Millisecond)
	tt.Frame()

	if tt.HasText("2 个子代理") {
		t.Fatalf("切走后不应显示 '2 个子代理'，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("Aspen"); ok {
		t.Fatalf("切走后不应显示 Aspen，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("Birch"); ok {
		t.Fatalf("切走后不应显示 Birch，texts: %q", tt.Texts())
	}
}
