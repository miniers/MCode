package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestSidebarSearchProjectsAndCollapsedState(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "命中项目"}, {ID: "p2", Name: "其他项目"}},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "Database Alpha"}, {ID: "t2", ProjectID: "p1", Title: "DATABASE dormant", Dormant: true}},
			"p2": {{ID: "t3", ProjectID: "p2", Title: "无关会话"}},
		},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	// 项目列表先于会话发布，要等会话也加载完再搜索。
	waitFor(t, tt, func() bool {
		_, first := tt.Find("收起项目 命中项目")
		_, second := tt.Find("收起项目 其他项目")
		_, alpha := tt.Find("打开会话 Database Alpha")
		_, unrelated := tt.Find("打开会话 无关会话")
		return first && second && alpha && unrelated
	})
	state.mu.Lock()
	state.collapsedProj = map[string]bool{"p1": true}
	state.mu.Unlock()
	*getSearchQuery(state) = "database"
	state.invalidate()
	tt.Frame()
	for _, want := range []string{"收起项目 命中项目", "打开会话 Database Alpha", "打开会话 DATABASE dormant"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("搜索结果缺少 %q: %q", want, tt.Texts())
		}
	}
	_, otherExpanded := tt.Find("收起项目 其他项目")
	_, otherCollapsed := tt.Find("展开项目 其他项目")
	_, unrelated := tt.Find("打开会话 无关会话")
	if otherExpanded || otherCollapsed || unrelated {
		t.Fatalf("无命中项目不应显示: %q", tt.Texts())
	}
	state.mu.Lock()
	collapsed := state.collapsedProj["p1"]
	state.mu.Unlock()
	if !collapsed {
		t.Fatal("搜索不应改变项目折叠状态")
	}
	*getSearchQuery(state) = ""
	state.invalidate()
	tt.Frame()
	_, alphaVisible := tt.Find("打开会话 Database Alpha")
	_, otherExpanded = tt.Find("收起项目 其他项目")
	_, otherCollapsed = tt.Find("展开项目 其他项目")
	if alphaVisible || (!otherExpanded && !otherCollapsed) {
		t.Fatalf("清空搜索应恢复折叠状态: %q", tt.Texts())
	}
}

func TestSidebarSearchNoMatches(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开会话 测试会话1")
		return ok
	})
	*getSearchQuery(state) = "绝不存在的会话标题"
	state.invalidate()
	tt.Frame()
	_, projectExpanded := tt.Find("收起项目 mcode-test")
	_, projectCollapsed := tt.Find("展开项目 mcode-test")
	_, sessionVisible := tt.Find("打开会话 测试会话1")
	if projectExpanded || projectCollapsed || !tt.HasText("无结果") || !tt.HasText("试试输入会话标题或项目名称") || sessionVisible {
		t.Fatalf("全部无命中时应显示全局提示: %q", tt.Texts())
	}
}

func TestSidebarSearchProjectName(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "Alpha 项目"}, {ID: "p2", Name: "其他项目"}},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "第一会话"}, {ID: "t2", ProjectID: "p1", Title: "休眠会话", Dormant: true}},
			"p2": {{ID: "t3", ProjectID: "p2", Title: "无关会话"}},
		},
	)
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("打开会话 第一会话")
		return ok
	})
	*getSearchQuery(state) = "  ALPHA  "
	state.invalidate()
	tt.Frame()
	for _, want := range []string{"收起项目 Alpha 项目", "打开会话 第一会话", "打开会话 休眠会话"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("项目名搜索结果缺少 %q: %q", want, tt.Texts())
		}
	}
	_, otherExpanded := tt.Find("收起项目 其他项目")
	_, otherCollapsed := tt.Find("展开项目 其他项目")
	_, unrelated := tt.Find("打开会话 无关会话")
	if otherExpanded || otherCollapsed || unrelated {
		t.Fatalf("无命中项目不应显示: %q", tt.Texts())
	}
}

func TestSidebarSearchKeyboardVisibleResults(t *testing.T) {
	testenv.TempHome(t)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "项目一"}, {ID: "p2", Name: "项目二"}},
		map[string][]Thread{
			"p1": {{ID: "t1", ProjectID: "p1", Title: "目标一", UpdatedAt: time.Now()}, {ID: "t2", ProjectID: "p1", Title: "目标二", Dormant: true}},
			"p2": {{ID: "t3", ProjectID: "p2", Title: "无关会话"}},
		},
	)
	state, view := buildRoot(fake)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool { return tt.HasText("目标一") })
	if err := tt.Click("搜索对话..."); err != nil {
		t.Fatal(err)
	}
	*getSearchQuery(state) = "目标"
	state.invalidate()
	tt.Frame()
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	state.mu.Lock()
	index := state.searchHighlightIndex
	state.mu.Unlock()
	if index != 1 || !tt.HasText("目标二") || tt.HasText("无关会话") {
		t.Fatalf("向下应停在第二个可见结果，索引 %d: %q", index, tt.Texts())
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	state.mu.Lock()
	index = state.searchHighlightIndex
	state.mu.Unlock()
	if index != 0 {
		t.Fatalf("向上应回到第一个可见结果，索引 %d", index)
	}
}
