package mcodeui

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"

	"mcode/internal/testenv"
)

func TestSortableNativeDrop(t *testing.T) {
	for _, tc := range []struct {
		name, sourceScope, targetScope, targetID string
		want                                     []string
	}{
		{"同域", "list", "list", "b", []string{"a"}},
		{"跨域", "projects", "thread/queue", "b", nil},
		{"自身", "list", "list", "a", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var moves []string
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Children(func() {
					source := ui.Row(c.Key("source")).Label("拖拽源").Width(200).Height(50)
					SortableSource(source, tc.sourceScope, "a")
					target := ui.Row(c.Key("target")).Label("拖拽目标").Width(200).Height(50)
					if _, id := SortableTarget(target, tc.targetScope, tc.targetID); id != "" {
						moves = append(moves, id)
					}
				})
			}, 300, 200)
			from, ok := tt.Find("拖拽源")
			if !ok {
				t.Fatal("缺少拖拽源")
			}
			to, ok := tt.Find("拖拽目标")
			if !ok {
				t.Fatal("缺少拖拽目标")
			}
			x, y := from.X+from.W/2, from.Y+from.H/2
			tx, ty := to.X+to.W/2, to.Y+to.H/2
			tt.Press(x, y)
			for step := 1; step <= 4; step++ {
				fraction := float32(step) / 4
				tt.Move(x+(tx-x)*fraction, y+(ty-y)*fraction)
			}
			tt.Release(tx, ty)
			tt.Frame()
			if !reflect.DeepEqual(moves, tc.want) {
				t.Fatalf("移动 = %v，期望 %v", moves, tc.want)
			}
		})
	}
}

func TestSortableQueueWindowScopes(t *testing.T) {
	first, second := &chatState{}, &chatState{}
	scopeA := fmt.Sprintf("%p/thread/queue", first)
	scopeB := fmt.Sprintf("%p/thread/queue", second)
	for _, tc := range []struct {
		name, source, target string
		want                 string
	}{
		{"窗口一到窗口二", scopeA, scopeB, ""},
		{"窗口二到窗口一", scopeB, scopeA, ""},
		{"同窗口容器", scopeA, scopeA, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var overOnDrop, dropped string
			var crossScopeOver bool
			tt := ui.NewTester(func(c *ui.Context) {
				ui.Column(c).Children(func() {
					source := ui.Row(c).Label("拖拽源").Width(200).Height(50)
					SortableSource(source, tc.source, "a")
					target := ui.Row(c).Label("队列容器").Width(200).Height(50)
					over, id := SortableTarget(target, tc.target, "")
					if tc.source != tc.target && over != "" {
						crossScopeOver = true
					}
					if id != "" {
						overOnDrop, dropped = over, id
					}
				})
			}, 300, 200)
			from, ok := tt.Find("拖拽源")
			if !ok {
				t.Fatal("缺少拖拽源")
			}
			to, ok := tt.Find("队列容器")
			if !ok {
				t.Fatal("缺少队列容器")
			}
			x, y := from.X+from.W/2, from.Y+from.H/2
			tx, ty := to.X+to.W/2, to.Y+to.H/2
			tt.Press(x, y)
			for step := 1; step <= 4; step++ {
				fraction := float32(step) / 4
				tt.Move(x+(tx-x)*fraction, y+(ty-y)*fraction)
			}
			tt.Release(tx, ty)
			tt.Frame()
			if dropped != tc.want || overOnDrop != "" || crossScopeOver {
				t.Fatalf("容器落点 over=%q，drop=%q，跨域悬停=%v，期望 drop=%q", overOnDrop, dropped, crossScopeOver, tc.want)
			}
		})
	}
}

func TestSidebarSortableSearchFilter(t *testing.T) {
	testenv.TempHome(t)
	t.Setenv("MCODE_DATA", t.TempDir())
	t.Cleanup(waitUIStateWrites)
	fake := NewFakeClient()
	fake.SetProjectsAndThreads(
		[]Project{{ID: "p1", Name: "共同项目一"}, {ID: "p2", Name: "共同项目二"}},
		map[string][]Thread{},
	)
	state, view := buildRoot(fake)
	t.Cleanup(state.close)
	tt := ui.NewTester(view, 1000, 700)
	waitFor(t, tt, func() bool {
		_, first := tt.Find("共同项目一")
		_, second := tt.Find("共同项目二")
		return first && second
	})
	drag := func(source, target string) {
		t.Helper()
		from, ok := tt.Find(source)
		if !ok {
			t.Fatalf("缺少项目行 %q", source)
		}
		to, ok := tt.Find(target)
		if !ok {
			t.Fatalf("缺少项目行 %q", target)
		}
		x, y := from.X+from.W/2, from.Y+from.H/2
		tx, ty := to.X+to.W/2, to.Y+to.H/2
		tt.Press(x, y)
		for step := 1; step <= 4; step++ {
			fraction := float32(step) / 4
			tt.Move(x+(tx-x)*fraction, y+(ty-y)*fraction)
		}
		tt.Release(tx, ty)
		tt.Frame()
	}
	*getSearchQuery(state) = "共同项目"
	state.invalidate()
	tt.Frame()
	drag("共同项目二", "共同项目一")
	if order := sidebarProjectOrder(state); len(order) != 0 {
		t.Fatalf("搜索期间不应排序：%v", order)
	}
	*getSearchQuery(state) = ""
	state.invalidate()
	tt.Frame()
	drag("共同项目二", "共同项目一")
	if order := sidebarProjectOrder(state); !reflect.DeepEqual(order, []string{"p2", "p1"}) {
		t.Fatalf("拖动后的项目顺序 = %v", order)
	}
}
