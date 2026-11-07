package mcodeui

import (
	"fmt"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestSidebarRelativeTimeAndStableTitleWidth(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = []Thread{{ID: "recent", ProjectID: "p1", Title: "时间测试会话", UpdatedAt: time.Now().Add(-90 * time.Second)}}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("时间测试会话") && tt.HasText("1分钟前") })
	status, ok := tt.Find("1分钟前")
	if !ok || status.W <= ControlMD {
		t.Fatalf("相对时间宽度 = %v，应大于 %v", status.W, ControlMD)
	}
	before, ok := tt.Find("时间测试会话")
	if !ok {
		t.Fatal("找不到会话标题")
	}
	tt.Move(before.X+before.W/2, before.Y+before.H/2)
	tt.Frame()
	after, ok := tt.Find("时间测试会话")
	if !ok || before.W != after.W {
		t.Fatalf("悬停前后标题宽度变化: %v -> %v", before.W, after.W)
	}
}

func TestSidebarPaginationAndStableTitleWidth(t *testing.T) {
	fake := NewFakeClient()
	threads := make([]Thread, 30)
	for i := range threads {
		threads[i] = Thread{ID: fmt.Sprintf("page-%02d", i), ProjectID: "p1", Title: fmt.Sprintf("分页会话%02d", i), UpdatedAt: time.Now().Add(-time.Duration(i) * time.Minute)}
	}
	fake.threads["p1"] = threads
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 2000)
	waitFor(t, tt, func() bool { return tt.HasText("分页会话07") && tt.HasText("加载更多") })
	if tt.HasText("分页会话08") {
		t.Fatal("默认不应显示第九条")
	}
	before, ok := tt.Find("分页会话00")
	if !ok {
		t.Fatal("找不到会话标题")
	}
	tt.Move(before.X+before.W/2, before.Y+before.H/2)
	tt.Frame()
	after, ok := tt.Find("分页会话00")
	if !ok || before.W != after.W {
		t.Fatalf("悬停前后标题宽度变化: %v -> %v", before.W, after.W)
	}
	if err := tt.Click("加载更多"); err != nil {
		t.Fatalf("点击加载更多: %v", err)
	}
	tt.Frame()
	state.mu.Lock()
	limit := state.visibleThreadsProj["p1"]
	state.mu.Unlock()
	if limit != 28 || !tt.HasText("分页会话27") || tt.HasText("分页会话28") {
		t.Fatalf("展开后应显示 28 条，limit=%d", limit)
	}
}

// 测试 ⌘B 快捷键和头部按钮切换侧边栏折叠状态（显隐带过渡动画）
func TestSidebarToggleCollapse(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	// 初始状态侧边栏未折叠
	if state.sidebarCollapsed {
		t.Fatalf("expected sidebarCollapsed to be false initially")
	}
	if !tt.HasText("在此项目中新建会话") {
		t.Fatalf("expected project '+' button in sidebar")
	}
	// 侧边栏顶部不再提供全局新建会话按钮，入口只保留标题栏与项目行
	if _, ok := tt.Find("sidebar-new-thread-btn"); ok {
		t.Fatalf("expected sidebar top new-thread button removed")
	}

	// 按 ⌘B 折叠侧边栏：过渡期间内容仍按目标宽度渲染，只按插值宽度裁切
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()

	if !state.sidebarCollapsed {
		t.Fatalf("expected sidebarCollapsed to be true after Cmd+B")
	}
	if !tt.HasText("在此项目中新建会话") {
		t.Fatalf("expected sidebar content kept while sliding out")
	}

	// 动画结束后回到无侧边栏的静态布局
	tt.Advance(sidebarSlideDuration + 100*time.Millisecond)
	tt.Frame()
	if tt.HasText("在此项目中新建会话") {
		t.Fatalf("expected sidebar to be collapsed and project '+' button removed")
	}

	// 再次按 ⌘B 展开侧边栏
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()

	if state.sidebarCollapsed {
		t.Fatalf("expected sidebarCollapsed to be false after second Cmd+B")
	}
	tt.Advance(sidebarSlideDuration + 100*time.Millisecond)
	tt.Frame()
	if !tt.HasText("在此项目中新建会话") {
		t.Fatalf("expected sidebar to be restored and show project '+' button")
	}

	// 通过聊天头部的图标按钮点击切换
	if err := tt.Click("显示/隐藏侧边栏 ⌘B"); err != nil {
		t.Fatalf("click toggle button failed: %v", err)
	}
	tt.Frame()
	if !state.sidebarCollapsed {
		t.Fatalf("expected sidebarCollapsed to be true after header button click")
	}
}

// 测试侧边栏底部的 GitHub 克隆入口会打开克隆面板（入口收进「添加项目」浮层）
func TestSidebarGitHubCloneEntry(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1200, 800)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	if err := tt.Click("添加项目"); err != nil {
		t.Fatalf("click add project button failed: %v", err)
	}
	tt.Frame()
	if err := tt.Click("从 GitHub 克隆…"); err != nil {
		t.Fatalf("click clone entry failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		return tt.HasText("选择仓库，克隆到你指定的目录并作为项目打开。")
	})
	if cs := getGitHubCloneState(state); cs == nil || !cs.open {
		t.Fatalf("expected github clone panel opened")
	}
}

// 测试线程置顶与排序
func TestSidebarThreadPinning(t *testing.T) {
	fake := NewFakeClient()
	now := time.Now()
	// 设置同一个项目下有两条线程，t1 较新，t2 较旧
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "会话一", UpdatedAt: now.Add(time.Minute)},
		{ID: "t2", ProjectID: "p1", Title: "会话二", UpdatedAt: now},
	}

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("会话一") && tt.HasText("会话二")
	})

	// 右键会话二
	if err := tt.RightClick("会话二"); err != nil {
		t.Fatalf("right click '会话二' failed: %v", err)
	}
	tt.Frame()

	// 验证右键菜单项包含“置顶”
	menu := tt.Menu()
	hasPin := false
	for _, it := range menu {
		if it == "置顶" {
			hasPin = true
			break
		}
	}
	if !hasPin {
		t.Fatalf("expected menu to contain '置顶', got: %v", menu)
	}

	// 选中“置顶”
	if err := tt.ChooseMenuItem("置顶"); err != nil {
		t.Fatalf("choose '置顶' failed: %v", err)
	}

	// 等待排序更新，会话二排在最前
	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		ths := state.threadsByProj["p1"]
		return len(ths) == 2 && ths[0].ID == "t2" && ths[0].Pinned
	})

	// 再次右键会话二，菜单项变为“取消置顶”
	if err := tt.RightClick("会话二"); err != nil {
		t.Fatalf("second right click '会话二' failed: %v", err)
	}
	tt.Frame()

	menu = tt.Menu()
	hasUnpin := false
	for _, it := range menu {
		if it == "取消置顶" {
			hasUnpin = true
			break
		}
	}
	if !hasUnpin {
		t.Fatalf("expected menu to contain '取消置顶', got: %v", menu)
	}
}

// 测试线程重命名弹窗
func TestSidebarThreadRenameModal(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	if err := tt.RightClick("测试会话1"); err != nil {
		t.Fatalf("right click failed: %v", err)
	}
	tt.Frame()

	if err := tt.ChooseMenuItem("重命名…"); err != nil {
		t.Fatalf("choose '重命名…' failed: %v", err)
	}
	tt.Frame()

	if !state.renameModalOpen {
		t.Fatalf("expected renameModalOpen to be true")
	}
	if !tt.HasText("重命名会话") {
		t.Fatalf("expected '重命名会话' modal title")
	}

	// 模拟修改输入框并按回车
	state.mu.Lock()
	state.renameThreadTitle = "重命名后的标题"
	state.mu.Unlock()
	state.submitRename()

	waitFor(t, tt, func() bool {
		return tt.HasText("重命名后的标题")
	})
	if state.renameModalOpen {
		t.Fatalf("expected rename modal to be closed")
	}
}

// 测试线程归档与删除
func TestSidebarThreadArchiveAndDelete(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "测试会话1", UpdatedAt: time.Now()},
		{ID: "t2", ProjectID: "p1", Title: "测试会话2", UpdatedAt: time.Now().Add(-time.Minute)},
	}

	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1") && tt.HasText("测试会话2")
	})

	// 选中会话一
	state.selectThread("t1")
	tt.Frame()

	// 右键归档会话一
	if err := tt.RightClick("测试会话1"); err != nil {
		t.Fatalf("right click '测试会话1' failed: %v", err)
	}
	tt.Frame()

	if err := tt.ChooseMenuItem("归档"); err != nil {
		t.Fatalf("choose '归档' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		// 归档后从列表消失，且清空选中状态
		return !tt.HasText("测试会话1") && state.selectedThreadID == ""
	})

	// 选中会话二
	state.selectThread("t2")
	tt.Frame()

	// 右键删除会话二
	if err := tt.RightClick("测试会话2"); err != nil {
		t.Fatalf("right click '测试会话2' failed: %v", err)
	}
	tt.Frame()

	if err := tt.ChooseMenuItem("删除…"); err != nil {
		t.Fatalf("choose '删除…' failed: %v", err)
	}
	tt.Frame()

	if !state.deleteModalOpen {
		t.Fatalf("expected deleteModalOpen to be true")
	}
	if !tt.HasText("删除后无法恢复，确定要删除此会话吗？") && !tt.HasText("删除后无法恢复") {
		t.Fatalf("expected delete warning text")
	}

	// 确认删除
	state.confirmDelete()

	waitFor(t, tt, func() bool {
		state.mu.Lock()
		defer state.mu.Unlock()
		return !tt.HasText("测试会话2") && state.selectedThreadID == ""
	})
}

// TestSidebarDeleteSkipsConfirmWhenDisabled 验证 E-01：删除前确认关闭时，右键「删除…」直接删除会话，不打开确认弹窗。
func TestSidebarDeleteSkipsConfirmWhenDisabled(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = []Thread{
		{ID: "t1", ProjectID: "p1", Title: "直删会话", UpdatedAt: time.Now()},
	}

	state, view := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	st.cfg.Archive.ConfirmBeforeDeleting = false
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("直删会话") })

	if err := tt.RightClick("直删会话"); err != nil {
		t.Fatalf("right click '直删会话' failed: %v", err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("删除…"); err != nil {
		t.Fatalf("choose '删除…' failed: %v", err)
	}
	tt.Frame()

	state.mu.Lock()
	open := state.deleteModalOpen
	state.mu.Unlock()
	if open {
		t.Fatalf("删除前确认关闭时不应打开确认弹窗")
	}
	if tt.HasText("删除后无法恢复") {
		t.Fatalf("删除前确认关闭时不应渲染确认文案")
	}

	waitFor(t, tt, func() bool { return !tt.HasText("直删会话") })
}

// 测试项目右键从侧边栏移除
func TestSidebarProjectContextMenuRemove(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("mcode-test")
	})

	if err := tt.RightClick("mcode-test"); err != nil {
		t.Fatalf("right click project failed: %v", err)
	}
	tt.Frame()

	menu := tt.Menu()
	expectedItems := []string{"新建会话", "在访达中显示", "复制路径", "从侧边栏移除…"}
	for _, exp := range expectedItems {
		found := false
		for _, m := range menu {
			if m == exp {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected project menu to contain %q, got: %v", exp, menu)
		}
	}

	// 触发从侧边栏移除…
	if err := tt.ChooseMenuItem("从侧边栏移除…"); err != nil {
		t.Fatalf("choose '从侧边栏移除…' failed: %v", err)
	}
	tt.Frame()

	if !state.removeProjModalOpen {
		t.Fatalf("expected removeProjModalOpen to be true")
	}
	if !tt.HasText(fmt.Sprintf("确定移除项目 %q 吗？该项目的会话记录会一并从 mcode 中删除，磁盘上的文件不会被删除。", "mcode-test")) {
		t.Fatalf("expected remove project warning text")
	}

	// 确认移除
	state.confirmRemoveProject()

	waitFor(t, tt, func() bool {
		return !tt.HasText("mcode-test")
	})
}

// 测试会话行多行标题与模型徽章渲染
func TestSidebarThreadRowRendering(t *testing.T) {
	fake := NewFakeClient()
	fake.SetProviders([]ProviderInfo{
		{
			ID:        "claude",
			Name:      "Anthropic",
			Available: true,
			Models: []ModelInfo{
				{ID: "claude-3-7-sonnet", Name: "Claude 3.7 Sonnet"},
			},
		},
	})
	fake.threads["p1"] = []Thread{
		{
			ID:        "t1",
			ProjectID: "p1",
			Title:     "第一行很长很长的会话标题需要折行展示第二行",
			Provider:  "claude",
			Model:     "claude-3-7-sonnet",
			UpdatedAt: time.Now(),
		},
	}

	_, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("第一行很长很长的会话标题需要折行展示第二行") && tt.HasText("Claude 3.7 Sonnet")
	})
}

// 测试右键菜单标记休眠调用：右键「标记休眠」走可撤销的休眠 API，并显示撤销条
func TestSidebarThreadDormant(t *testing.T) {
	fake := NewFakeClient()
	fake.markDormantWithUndoToken = "undo-settle"
	_, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool {
		return tt.HasText("测试会话1")
	})

	if err := tt.RightClick("测试会话1"); err != nil {
		t.Fatalf("right click failed: %v", err)
	}
	tt.Frame()

	menu := tt.Menu()
	hasDormant := false
	for _, it := range menu {
		if it == "标记休眠" {
			hasDormant = true
			break
		}
	}
	if !hasDormant {
		t.Fatalf("expected menu to contain '标记休眠', got: %v", menu)
	}

	if err := tt.ChooseMenuItem("标记休眠"); err != nil {
		t.Fatalf("choose '标记休眠' failed: %v", err)
	}

	waitFor(t, tt, func() bool {
		calls := fake.MarkDormantWithUndoCalls()
		return len(calls) == 1 && calls[0] == "t1" && tt.HasText("已休眠 1 个会话")
	})
}

// TestSidebarCompactModeSingleLineTitle 验证在侧边栏紧凑模式开启时，会话标题只显示一行。
func TestSidebarCompactModeSingleLineTitle(t *testing.T) {
	fake := NewFakeClient()
	longTitle := "第一行很长很长的会话标题需要折行展示第二行测试紧凑模式"
	fake.threads["p1"] = []Thread{
		{
			ID:        "t1",
			ProjectID: "p1",
			Title:     longTitle,
			UpdatedAt: time.Now(),
		},
	}

	state, view := buildRoot(fake)
	st := NewSettingsState(settingsTestDir(t), nil, state)
	st.cfg.Appearance.SidebarCompact = true
	setChatSettings(state, st)

	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return tt.HasText(longTitle)
	})

	rect, found := tt.Find(longTitle)
	if !found {
		t.Fatalf("expected to find long thread title in sidebar")
	}
	row, ok := tt.Find("打开会话 " + longTitle)
	if !ok || row.H != HeightThreadRowCompact*textScaleFactor() {
		t.Fatalf("紧凑会话行高度 = %.1f，期望 %.1f", row.H, HeightThreadRowCompact*textScaleFactor())
	}
	// 在紧凑模式下 SingleLine 渲染，标题文本高度应限制在单行范围内 (<= 22)
	if rect.H > 22 {
		t.Fatalf("expected single line title height <= 22 in compact mode, got %.1f", rect.H)
	}
}

// TestVisibleThreadWindow 验证侧边栏活跃会话分页窗口：默认截取前 8 条，
// 选中项落在窗口之外时追加到末尾，搜索（unlimited）与总数未超限时返回全部。
func TestVisibleThreadWindow(t *testing.T) {
	makeThreads := func(n int) []Thread {
		list := make([]Thread, 0, n)
		for i := 1; i <= n; i++ {
			list = append(list, Thread{
				ID:        fmt.Sprintf("t%d", i),
				ProjectID: "p1",
				Title:     fmt.Sprintf("会话%d", i),
			})
		}
		return list
	}
	ids := func(list []Thread) []string {
		out := make([]string, len(list))
		for i, th := range list {
			out[i] = th.ID
		}
		return out
	}

	cases := []struct {
		name        string
		total       int
		limit       int
		selectedID  string
		unlimited   bool
		wantVisible []string
		wantHidden  int
	}{
		{
			name:        "总数不超过 5 条时全部显示",
			total:       3,
			limit:       5,
			wantVisible: []string{"t1", "t2", "t3"},
			wantHidden:  0,
		},
		{
			name:        "总数 12 条默认只显示前 5 条",
			total:       12,
			limit:       5,
			wantVisible: []string{"t1", "t2", "t3", "t4", "t5"},
			wantHidden:  7,
		},
		{
			name:        "加载到 15 条时全部显示",
			total:       12,
			limit:       15,
			wantVisible: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12"},
			wantHidden:  0,
		},
		{
			name:        "搜索时不分页",
			total:       12,
			limit:       5,
			unlimited:   true,
			wantVisible: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8", "t9", "t10", "t11", "t12"},
			wantHidden:  0,
		},
		{
			name:        "选中项在窗口之外时追加到末尾",
			total:       12,
			limit:       5,
			selectedID:  "t9",
			wantVisible: []string{"t1", "t2", "t3", "t4", "t5", "t9"},
			wantHidden:  6,
		},
		{
			name:        "limit 为 0 时按默认 8 条处理",
			total:       12,
			limit:       0,
			wantVisible: []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"},
			wantHidden:  4,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			visible, hidden := visibleThreadWindow(makeThreads(tc.total), tc.limit, tc.selectedID, tc.unlimited)
			gotIDs := ids(visible)
			if len(gotIDs) != len(tc.wantVisible) {
				t.Fatalf("expected %d visible threads %v, got %d (%v)", len(tc.wantVisible), tc.wantVisible, len(gotIDs), gotIDs)
			}
			for i := range tc.wantVisible {
				if gotIDs[i] != tc.wantVisible[i] {
					t.Fatalf("expected visible threads %v, got %v", tc.wantVisible, gotIDs)
				}
			}
			if hidden != tc.wantHidden {
				t.Fatalf("expected hidden %d, got %d", tc.wantHidden, hidden)
			}
		})
	}
}

// TestSidebarThreadContextMenuGroups 右键会话的菜单按四组排列，首项是「转发到新会话」，
// 各分组里的菜单项都还在，点「转发到新会话」会触发转发。
func TestSidebarThreadContextMenuGroups(t *testing.T) {
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	// 打开新窗口的回调在测试里默认没有装配，这里补上以覆盖第一组的第二项。
	state.openThreadWindow = func(string) {}
	tt := ui.NewTester(view, 1000, 700)

	waitFor(t, tt, func() bool { return tt.HasText("测试会话1") })

	if err := tt.RightClick("测试会话1"); err != nil {
		t.Fatalf("right click '测试会话1' failed: %v", err)
	}
	tt.Frame()

	menu := tt.Menu()
	if len(menu) == 0 || menu[0] != "转发到新会话" {
		t.Fatalf("菜单第一项应为「转发到新会话」，实际 %v", menu)
	}

	// 逐个分隔符切分菜单，确认四组内容与顺序。
	var groups [][]string
	cur := []string{}
	for _, label := range menu {
		if label == "-" {
			groups = append(groups, cur)
			cur = []string{}
			continue
		}
		cur = append(cur, label)
	}
	groups = append(groups, cur)
	if len(groups) != 4 {
		t.Fatalf("菜单应分成 4 组，实际 %d 组: %v", len(groups), groups)
	}
	wantGroups := [][]string{
		{"转发到新会话", "在新窗口打开"},
		{"重命名…", "置顶", "标记休眠", "迁入 Worktree…"},
		{"导出会话…", "复制为 Markdown", "复制会话 ID", "复制路径", "在访达中显示"},
		{"归档", "删除…"},
	}
	for i, want := range wantGroups {
		if fmt.Sprint(groups[i]) != fmt.Sprint(want) {
			t.Fatalf("第 %d 组应为 %v，实际 %v", i+1, want, groups[i])
		}
	}

	if err := tt.ChooseMenuItem("转发到新会话"); err != nil {
		t.Fatalf("choose '转发到新会话' failed: %v", err)
	}
	waitFor(t, tt, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.ForwardCalls) == 1 && fake.ForwardCalls[0] == "t1"
	})
}

// TestSidebarSearchClearButton 校验搜索框输入后出现清空按钮，点击后查询串清空且按钮消失。
func TestSidebarSearchClearButton(t *testing.T) {
	fake := NewFakeClient()
	fake.threads["p1"] = []Thread{{ID: "search-clear", ProjectID: "p1", Title: "可清除的会话", UpdatedAt: time.Now()}}
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("可清除的会话") })

	if _, ok := tt.Find("清空"); ok {
		t.Fatal("空查询串不应显示清空按钮")
	}

	if err := tt.Click("搜索对话..."); err != nil {
		t.Fatalf("focus '搜索对话...' failed: %v", err)
	}
	tt.Type("可清除")
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("清空")
		return ok
	})

	if err := tt.Click("清空"); err != nil {
		t.Fatalf("click '清空' failed: %v", err)
	}
	state.mu.Lock()
	query := *getSearchQuery(state)
	state.mu.Unlock()
	if query != "" {
		t.Fatalf("点击清空后 query = %q，应为空", query)
	}
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("清空")
		return !ok && tt.HasText("可清除的会话")
	})
}
