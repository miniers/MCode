package mcodeui

import (
	"fmt"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestContainScrollKeepsWheelInside 覆盖 containScroll：内层列表滚到顶或底后滚轮不再带动外层，
// 指针在内层外面时外层照常滚动，内层里的按钮仍可点击。
func TestContainScrollKeepsWheelInside(t *testing.T) {
	var outer, inner ui.ScrollState
	clicks := 0
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("contain-page")).
			Fill().
			Padding(20).
			Children(func() {
				ui.Scroll(c.Key("contain-outer")).
					TrackScroll(&outer).
					Width(320).
					Height(400).
					Children(func() {
						ui.Column(c.Key("contain-outer-content")).
							Gap(12).
							Children(func() {
								ui.Text(c, "外层空白").
									FontSize(12)
								scroll := ui.Scroll(c.Key("contain-inner")).
									Width(300).
									MaxHeight(100)
								containScroll(scroll, &inner).Children(func() {
									ui.Column(c.Key("contain-inner-content")).
										Gap(4).
										Children(func() {
											for i := 0; i < 15; i++ {
												name := fmt.Sprintf("内层按钮 %d", i)
												ui.ButtonBase(c.Key(fmt.Sprintf("contain-item-%d", i))).
													Label(name).
													Height(20).
													OnClick(func() { clicks++ }).
													Children(func() {
														ui.Text(c, name).FontSize(12)
													})
											}
										})
								})
								ui.Box(c.Key("contain-filler")).
									Width(300).
									Height(800).
									Background(ThemeSurface(c))
							})
					})
			})
	}, 400, 600)

	// (c) 内层列表里的按钮仍可点击。
	if err := tt.Click("内层按钮 0"); err != nil {
		t.Fatalf("点击内层按钮失败: %v", err)
	}
	tt.Frame()
	if clicks != 1 {
		t.Fatalf("内层按钮点击 %d 次，想要 1 次", clicks)
	}

	item, ok := tt.Find("内层按钮 0")
	if !ok {
		t.Fatalf("找不到内层按钮: %v", tt.Texts())
	}
	x, y := item.X+item.W/2, item.Y+item.H/2

	if inner.MaxY <= 0 {
		t.Fatalf("内层列表应可滚动，MaxY = %v", inner.MaxY)
	}

	// (a) 指针在内层列表上一直向下滚：内层到底后外层不再跟着滚。
	for i := 0; i < 20; i++ {
		tt.Scroll(x, y, 0, 100)
	}
	if math.Abs(float64(inner.Y-inner.MaxY)) > 0.5 {
		t.Fatalf("内层应滚到底 %v，实际 %v", inner.MaxY, inner.Y)
	}
	if outer.Y != 0 {
		t.Fatalf("内层到底后外层不应滚动，外层 Y = %v", outer.Y)
	}

	// 向上滚同样在内层顶部停住，不带动外层。
	for i := 0; i < 20; i++ {
		tt.Scroll(x, y, 0, -100)
	}
	if inner.Y != 0 {
		t.Fatalf("内层应回到顶部，实际 %v", inner.Y)
	}
	if outer.Y != 0 {
		t.Fatalf("内层到顶后外层不应滚动，外层 Y = %v", outer.Y)
	}

	// (b) 指针在内层列表外面滚动时，外层照常滚。
	blank, ok := tt.Find("外层空白")
	if !ok {
		t.Fatalf("找不到外层文字: %v", tt.Texts())
	}
	bx, by := blank.X+blank.W/2, blank.Y+blank.H/2
	tt.Scroll(bx, by, 0, 100)
	if outer.Y <= 0 {
		t.Fatalf("指针在内层外时应滚动外层，外层 Y = %v", outer.Y)
	}
}
