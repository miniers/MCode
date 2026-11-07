package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 本文件覆盖「父会话 + 可折叠子容器」：子容器只装 bee 子代理，共用一份折叠状态
// （sidebarBeeFolded，按 parentID 记）；派生会话是独立会话，始终作为顶层行渲染。

// newForkChildSidebar 构造一个「来源会话 + 派生会话」的侧边栏，返回状态与 Tester。
func newForkChildSidebar(t *testing.T, threads []Thread) (*chatState, *ui.Tester) {
	t.Helper()
	ResetBeeFolded()
	t.Cleanup(ResetBeeFolded)
	resetSidebarHoverCardForTest()
	t.Cleanup(resetSidebarHoverCardForTest)

	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "mcode-test", Path: "/path/to/children"}},
		map[string][]Thread{"p1": threads},
	)
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		return len(threads) == 0 || tt.HasText(threads[0].Title)
	})
	return state, tt
}

// selectedThreadIDOf 读当前选中的会话 ID（渲染线程与测试共享同一份 chatState）。
func selectedThreadIDOf(s *chatState) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selectedThreadID
}

// TestGroupThreadsByParentKeepsForksTopLevel 验证派生会话是独立会话，始终留在顶层，
// 来源会话的子列表只装 bee 子代理。
func TestGroupThreadsByParentKeepsForksTopLevel(t *testing.T) {
	now := time.Now()
	threads := []Thread{
		{ID: "lead", Title: "来源会话", UpdatedAt: now.Add(-3 * time.Minute)},
		{ID: "fork-new", Title: "派生 新", ForkedFromThreadID: "lead", UpdatedAt: now},
		{ID: "fork-old", Title: "派生 旧", ForkedFromThreadID: "lead", UpdatedAt: now.Add(-time.Minute)},
		{ID: "bee-1", Title: "Aspen: 子任务", ParentThreadID: "lead", IsHelper: true, UpdatedAt: now},
	}

	topLevel, childrenByParent := GroupThreadsByParent(threads)

	topIDs := make(map[string]bool, len(topLevel))
	for _, th := range topLevel {
		topIDs[th.ID] = true
	}
	for _, wantID := range []string{"lead", "fork-new", "fork-old"} {
		if !topIDs[wantID] {
			t.Fatalf("顶层会话应包含 %s，实际: %+v", wantID, topLevel)
		}
	}
	children := childrenByParent["lead"]
	if len(children) != 1 || children[0].ID != "bee-1" {
		t.Fatalf("来源会话下应只有 bee 子行，实际: %+v", children)
	}
}

// TestGroupThreadsByParentPromotesOrphanFork 验证派生会话不管来源会话是否还在列表里、
// 来源会话本身是不是 bee 子行，都作为独立会话留在顶层。
func TestGroupThreadsByParentPromotesOrphanFork(t *testing.T) {
	now := time.Now()
	threads := []Thread{
		{ID: "lead", Title: "来源会话", UpdatedAt: now},
		{ID: "bee-1", Title: "Aspen: 子任务", ParentThreadID: "lead", IsHelper: true, UpdatedAt: now},
		// 来源会话不在本次列表里（例如已归档或被搜索过滤掉）。
		{ID: "fork-orphan", Title: "孤儿派生", ForkedFromThreadID: "missing", UpdatedAt: now},
		// 来源会话自己是 bee 子行：派生会话同样留在顶层。
		{ID: "fork-of-bee", Title: "派生自子代理", ForkedFromThreadID: "bee-1", UpdatedAt: now},
	}

	topLevel, childrenByParent := GroupThreadsByParent(threads)

	topIDs := make(map[string]bool, len(topLevel))
	for _, th := range topLevel {
		topIDs[th.ID] = true
	}
	for _, wantID := range []string{"lead", "fork-orphan", "fork-of-bee"} {
		if !topIDs[wantID] {
			t.Fatalf("顶层会话应包含 %s，实际: %+v", wantID, topLevel)
		}
	}
	if len(childrenByParent["lead"]) != 1 || childrenByParent["lead"][0].ID != "bee-1" {
		t.Fatalf("来源会话下应只有 bee 子行，实际: %+v", childrenByParent["lead"])
	}
	for pID, list := range childrenByParent {
		for _, th := range list {
			if th.ID == "fork-orphan" || th.ID == "fork-of-bee" {
				t.Fatalf("派生会话不应留在 %s 的子列表里: %+v", pID, th)
			}
		}
	}
}

// TestSidebarChildrenSummary 验证折叠行文案只统计 bee 子代理。
func TestSidebarChildrenSummary(t *testing.T) {
	cases := []struct {
		name       string
		children   []Thread
		wantCount  int
		wantRun    bool
		wantString string
	}{
		{
			name:       "只有子代理",
			children:   []Thread{{ID: "h1", IsHelper: true}},
			wantCount:  1,
			wantString: "1 个子代理",
		},
		{
			name:       "空列表",
			children:   nil,
			wantCount:  0,
			wantString: "0 个子代理",
		},
	}
	for _, tc := range cases {
		count, hasRunning, label := BeeFoldSummary(tc.children)
		if count != tc.wantCount || hasRunning != tc.wantRun || label != tc.wantString {
			t.Fatalf("%s: got (%d, %v, %q), want (%d, %v, %q)",
				tc.name, count, hasRunning, label, tc.wantCount, tc.wantRun, tc.wantString)
		}
	}
}

// TestSidebarForkRendersAsTopLevelRow 验证派生会话作为顶层行渲染，不出现子行标签或折叠文案，
// 点击后切换到该派生会话。
func TestSidebarForkRendersAsTopLevelRow(t *testing.T) {
	now := time.Now()
	state, tt := newForkChildSidebar(t, []Thread{
		{ID: "t1", ProjectID: "p1", Title: "来源会话", UpdatedAt: now.Add(-time.Minute)},
		{ID: "t2", ProjectID: "p1", Title: "派生出来的会话", ForkedFromThreadID: "t1", UpdatedAt: now},
	})

	if !tt.HasText("派生出来的会话") {
		t.Fatalf("派生会话应作为顶层行渲染，texts: %q", tt.Texts())
	}
	for _, unwanted := range []string{"打开派生会话", "收起子会话", "个派生会话"} {
		if tt.HasText(unwanted) {
			t.Fatalf("派生会话不应出现子行文案 %q，texts: %q", unwanted, tt.Texts())
		}
	}
	if err := tt.Click("派生出来的会话"); err != nil {
		t.Fatalf("点击派生会话行失败: %v", err)
	}
	waitFor(t, tt, func() bool { return selectedThreadIDOf(state) == "t2" })
}

// TestSidebarNoFoldControlWithoutChildren 验证没有子会话的行不显示折叠控件。
func TestSidebarNoFoldControlWithoutChildren(t *testing.T) {
	_, tt := newForkChildSidebar(t, []Thread{
		{ID: "t1", ProjectID: "p1", Title: "独立会话", UpdatedAt: time.Now()},
	})

	if tt.HasText("收起子会话") {
		t.Fatalf("没有子会话的行不应有折叠按钮，texts: %q", tt.Texts())
	}
	if tt.HasText("个子代理") || tt.HasText("个派生会话") {
		t.Fatalf("没有子会话的行不应有折叠行文案，texts: %q", tt.Texts())
	}
}

// TestSidebarBeeSubRowStillRendersBees 回归验证：派生会话进入顶层后，原有 bee 头像条仍然渲染在
// 来源会话的可折叠子容器里；派生会话本身不受折叠影响。
func TestSidebarBeeSubRowStillRendersBees(t *testing.T) {
	now := time.Now()
	_, tt := newForkChildSidebar(t, []Thread{
		{ID: "t1", ProjectID: "p1", Title: "来源会话", UpdatedAt: now.Add(-time.Minute)},
		{ID: "t2", ProjectID: "p1", Title: "派生出来的会话", ForkedFromThreadID: "t1", UpdatedAt: now},
		{ID: "h1", ProjectID: "p1", Title: "Aspen: 子任务", ParentThreadID: "t1", IsHelper: true, UpdatedAt: now},
	})

	if !tt.HasText("派生出来的会话") {
		t.Fatalf("派生会话应作为顶层行渲染，texts: %q", tt.Texts())
	}
	if _, ok := tt.Find("Aspen"); !ok {
		t.Fatalf("bee 头像应仍渲染（以人设名作为可点击标签），texts: %q", tt.Texts())
	}

	if err := tt.Click("收起子会话"); err != nil {
		t.Fatalf("点击连接线折叠按钮失败: %v", err)
	}
	tt.Frame()
	if !tt.HasText("1 个子代理") {
		t.Fatalf("折叠行应统计子代理，texts: %q", tt.Texts())
	}
	if tt.HasText("个派生会话") {
		t.Fatalf("折叠行不应再统计派生会话，texts: %q", tt.Texts())
	}
	if !tt.HasText("派生出来的会话") {
		t.Fatalf("折叠后派生会话作为顶层行仍应可见，texts: %q", tt.Texts())
	}
}
