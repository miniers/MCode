package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestDiffEmptyStateAndRetry(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		renderDiffEmptyState(c)
	}, 400, 300)
	if !tester.HasText(diffEmptyTitle) || !tester.HasText(diffEmptySubtitle) {
		t.Fatal("Diff 空态标题或说明未显示")
	}
	if _, ok := tester.Find("重试"); ok {
		t.Fatal("Diff 空态不应显示重试")
	}

	clicks := 0
	tester = ui.NewTester(func(c *ui.Context) {
		renderDiffStatusState(c, "diff-error", IconOctagonAlert(), "读取失败", "错误详情", func() { clicks++ })
	}, 400, 300)
	if !tester.HasText("读取失败") || !tester.HasText("错误详情") {
		t.Fatal("Diff 错误标题或说明未显示")
	}
	if err := tester.Click("重试"); err != nil {
		t.Fatal(err)
	}
	if clicks != 1 {
		t.Fatalf("重试次数 = %d", clicks)
	}
}

func TestSidebarNoResultsCard(t *testing.T) {
	tester := ui.NewTester(func(c *ui.Context) {
		renderSidebarNoResultsCard(c)
	}, 300, 200)
	if _, ok := tester.Find("无结果"); !ok {
		t.Fatal("侧栏未显示无结果")
	}
}
