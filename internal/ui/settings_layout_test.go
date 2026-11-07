package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

// newSettingsDetailOnlyTester 只渲染设置详情列，按窗口宽度量列宽与左右留白。
func newSettingsDetailOnlyTester(t *testing.T, page string, width, height int) *ui.Tester {
	t.Helper()
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = page
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsDetail(c, st)
	}, width, height)
	tt.Frame()
	return tt
}

// TestSettingsLayout_DetailColumnWidth 校验详情列宽度上限 720：宽窗口下不跟着变宽，
// 左右留白对称；窄窗口下自适应收窄。
func TestSettingsLayout_DetailColumnWidth(t *testing.T) {
	const maxWidth float32 = 720

	for _, width := range []int{1600, 1200} {
		tt := newSettingsDetailOnlyTester(t, "appearance", width, 1000)
		column, ok := tt.Find("设置详情列")
		if !ok {
			t.Fatalf("%d 宽窗口里找不到详情列：%v", width, tt.Texts())
		}
		if column.W > maxWidth {
			t.Fatalf("%d 宽窗口里详情列宽 %.1f，超过上限 %.0f", width, column.W, maxWidth)
		}
		if width == 1600 {
			left := column.X
			right := float32(width) - (column.X + column.W)
			if diff := left - right; diff > 2 || diff < -2 {
				t.Fatalf("左右留白不对称：左 %.1f 右 %.1f（详情列 %.1f 宽）", left, right, column.W)
			}
		}
	}

	// 窄于上限时按窗口自适应，不留出 720 的固定宽度。
	tt := newSettingsDetailOnlyTester(t, "appearance", 700, 1000)
	column, ok := tt.Find("设置详情列")
	if !ok {
		t.Fatalf("700 宽窗口里找不到详情列：%v", tt.Texts())
	}
	if column.W >= maxWidth {
		t.Fatalf("700 宽窗口里详情列宽 %.1f，应当收窄到窗口以内", column.W)
	}
	if column.X+column.W > 700 {
		t.Fatalf("详情列右侧越界：X %.1f W %.1f", column.X, column.W)
	}
}

// TestSettingsLayout_Dialog 校验弹窗居中、导航纵向排列及小窗口边界。
func TestSettingsLayout_Dialog(t *testing.T) {
	modalDragReset(modalDragSettings)
	t.Cleanup(func() { modalDragReset(modalDragSettings) })
	for _, size := range []struct{ width, height int }{{1600, 1000}, {700, 500}} {
		st := NewSettingsState(settingsTestDir(t), nil, nil)
		st.open = true
		st.currentPage = "general"
		tt := ui.NewTester(func(c *ui.Context) { renderSettingsDialog(c, st) }, size.width, size.height)
		tt.Frame()
		card, ok := tt.Find("设置")
		if !ok {
			t.Fatal("找不到设置卡片")
		}
		w, h := float32(size.width), float32(size.height)
		if card.W < 0 || card.H < 0 || card.X < 0 || card.Y < 0 || card.X+card.W > w || card.Y+card.H > h {
			t.Fatalf("设置卡片超出窗口：%+v", card)
		}
		if size.width == 1600 {
			if card.W > 960 || card.H > 680 || absFloat32(card.X-(w-card.W)/2) > 2 || absFloat32(card.Y-(h-card.H)/2) > 2 {
				t.Fatalf("设置卡片尺寸或位置不正确：%+v", card)
			}
			first, firstOK := tt.Find("Magpie")
			second, secondOK := tt.Find("通用")
			if !firstOK || !secondOK || first.Y >= second.Y {
				t.Fatalf("导航应纵向排列：%+v %+v", first, second)
			}
		}
	}
}

func absFloat32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// TestSettingsLayout_PageTitleSmallerThanLegacyTitle 校验页面标题改用 TypePageTitle 字号。
// 字号读不出来，改为和 20 号字的参照文字比高度：两者在同一个 Tester 里量，避免写死行高。
func TestSettingsLayout_PageTitleSmallerThanLegacyTitle(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "appearance"

	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("test-legacy-title-probe")).Children(func() {
			ui.Text(c, "二十号字参照").FontSize(20)
		})
		renderSettingsDetail(c, st)
	}, 1200, 1600)
	tt.Frame()

	title, ok := tt.Find("外观")
	if !ok {
		t.Fatalf("找不到页面标题：%v", tt.Texts())
	}
	legacy, ok := tt.Find("二十号字参照")
	if !ok {
		t.Fatalf("找不到 20 号字参照文字：%v", tt.Texts())
	}
	if title.H >= legacy.H {
		t.Fatalf("页面标题行高 %.1f，应当小于 20 号字的行高 %.1f", title.H, legacy.H)
	}
}
