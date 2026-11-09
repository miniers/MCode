package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestEmptyStateWithoutOptionalContent(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		EmptyState(c, EmptyStateProps{Title: "空态标题"}).Label("空态外框")
	}, 300, 200)
	outer, ok := tester.Find("空态外框")
	if !ok {
		t.Fatal("找不到空态")
	}
	title, ok := tester.Find("空态标题")
	if !ok {
		t.Fatal("找不到标题")
	}
	if outer.H != title.H {
		t.Fatalf("无图标和说明时空态高度 = %v，标题高度 = %v", outer.H, title.H)
	}
}

func TestEmptyStateCompact(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		EmptyState(c, EmptyStateProps{Title: "紧凑标题", Compact: true}).Label("紧凑外框")
	}, 300, 200)
	outer, ok := tester.Find("紧凑外框")
	if !ok {
		t.Fatal("找不到紧凑空态")
	}
	title, ok := tester.Find("紧凑标题")
	if !ok || outer.H != title.H {
		t.Fatalf("紧凑空态高度 = %v，标题高度 = %v，标题存在 = %v", outer.H, title.H, ok)
	}
}

func TestEmptyStateAction(t *testing.T) {
	clicks := 0
	tester := ui.NewTester(func(c *ui.Context) {
		EmptyState(c, EmptyStateProps{Title: "空态标题", Action: func() {
			Button(c, ButtonOpts{Text: "执行操作", OnClick: func() { clicks++ }})
		}})
	}, 300, 200)
	if err := tester.Click("执行操作"); err != nil {
		t.Fatal(err)
	}
	if clicks != 1 {
		t.Fatalf("操作次数 = %d", clicks)
	}
}
