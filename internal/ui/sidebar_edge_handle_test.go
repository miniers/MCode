package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

// edgeDragShouldExpand 只在向右累计位移达到阈值时返回真。
func TestEdgeDragShouldExpand(t *testing.T) {
	cases := []struct {
		dx   float32
		want bool
	}{
		{-30, false},
		{0, false},
		{23.9, false},
		{24, true},
		{40, true},
	}
	for _, tc := range cases {
		if got := edgeDragShouldExpand(tc.dx); got != tc.want {
			t.Fatalf("edgeDragShouldExpand(%v) = %v, want %v", tc.dx, got, tc.want)
		}
	}
}

// 折叠态下渲染窗口左缘热区；向右拖拽超过阈值展开侧边栏，未达阈值保持折叠，
// 展开后不再渲染热区。
func TestSidebarEdgeHandleDrag(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.sidebarCollapsed = true

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Box(c.Key("edge-test-shell")).Fill().Children(func() {
			renderSidebarEdgeHandle(c, s)
		})
	}, 400, 300)

	r, ok := tt.Find("展开侧边栏")
	if !ok {
		t.Fatalf("折叠态应渲染左缘热区, texts: %q", tt.Texts())
	}
	if r.W != sidebarEdgeHandleWidth {
		t.Fatalf("热区宽度 = %v, want %v", r.W, sidebarEdgeHandleWidth)
	}
	if r.X != 0 || r.H != 300 {
		t.Fatalf("热区应贴窗口左缘且全高, rect = %+v", r)
	}

	// 未达阈值的拖拽不展开。
	x, y := r.X+2, r.Y+r.H/2
	tt.Press(x, y)
	tt.Move(x+10, y)
	tt.Release(x+10, y)
	tt.Frame()
	s.mu.Lock()
	collapsed := s.sidebarCollapsed
	s.mu.Unlock()
	if !collapsed {
		t.Fatal("拖拽未达阈值不应展开侧边栏")
	}

	// 超过阈值的拖拽展开侧边栏。
	tt.Press(x, y)
	tt.Move(x+30, y)
	tt.Release(x+30, y)
	tt.Frame()
	s.mu.Lock()
	collapsed = s.sidebarCollapsed
	s.mu.Unlock()
	if collapsed {
		t.Fatal("向右拖拽超过阈值应展开侧边栏")
	}
	if _, ok := tt.Find("展开侧边栏"); ok {
		t.Fatal("展开态不应渲染左缘热区")
	}
}

// worktreeTargetProjectID 优先选中会话所属项目，其次第一个项目，无项目时为空串。
func TestWorktreeTargetProjectID(t *testing.T) {
	testenv.TempHome(t)
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	if got := worktreeTargetProjectID(s); got != "" {
		t.Fatalf("无项目时应返回空串, got %q", got)
	}

	s.mu.Lock()
	s.projects = []Project{{ID: "p2", Name: "第二个"}, {ID: "p1", Name: "第一个"}}
	s.mu.Unlock()
	if got := worktreeTargetProjectID(s); got != "p2" {
		t.Fatalf("未选中会话时应退化为第一个项目, got %q", got)
	}

	s.mu.Lock()
	s.selectedThreadID = "t9"
	s.threads = []Thread{{ID: "t9", ProjectID: "p1", Title: "会话"}}
	s.mu.Unlock()
	if got := worktreeTargetProjectID(s); got != "p1" {
		t.Fatalf("应取选中会话所属项目, got %q", got)
	}

	// 选中会话缺少项目 ID 时退化为第一个项目。
	s.mu.Lock()
	s.threads = []Thread{{ID: "t9", Title: "会话"}}
	s.mu.Unlock()
	if got := worktreeTargetProjectID(s); got != "p2" {
		t.Fatalf("选中会话无项目时应退化为第一个项目, got %q", got)
	}
}
