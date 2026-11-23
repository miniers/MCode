package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsDetailScrollResetsOnPageSwitch(t *testing.T) {
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 960, 420)
	tt.Frame()

	detail, ok := tt.Find("设置详情列")
	if !ok {
		t.Fatalf("未找到设置详情列：%v", tt.Texts())
	}
	tt.Scroll(detail.X+detail.W/2, 200, 0, 10000)
	tt.Frame()
	if st.detailScroll.Y <= 0 {
		t.Fatalf("通用页滚动后 Y 应大于 0，实际 %v（MaxY=%v）", st.detailScroll.Y, st.detailScroll.MaxY)
	}
	scrolledY := st.detailScroll.Y

	// 切换到同一页不重置滚动位置。
	st.selectSettingsPage("general")
	tt.Frame()
	if st.detailScroll.Y != scrolledY {
		t.Fatalf("切到同一页不应重置滚动：got %v, want %v", st.detailScroll.Y, scrolledY)
	}

	// 通过点击侧栏导航项切到另一页后，滚动位置归零。
	navAppearance, ok := tt.Find("外观")
	if !ok || navAppearance.X >= 220 {
		t.Fatalf("未在侧栏找到外观导航项：ok=%v rect=%+v", ok, navAppearance)
	}
	tt.ClickAt(navAppearance.X+navAppearance.W/2, navAppearance.Y+navAppearance.H/2)
	tt.Frame()
	if st.currentPage != "appearance" {
		t.Fatalf("点击外观后当前页 = %q，期望 appearance", st.currentPage)
	}
	if st.detailScroll.Y != 0 || st.detailScroll.X != 0 {
		t.Fatalf("切页后详情区滚动应归零，实际 X=%v Y=%v", st.detailScroll.X, st.detailScroll.Y)
	}

	// 显式置为较大偏移再调用 selectSettingsPage 切页，同样应归零。
	st.detailScroll.X = 16
	st.detailScroll.Y = 480
	st.selectSettingsPage("conversation")
	tt.Frame()
	if st.currentPage != "conversation" {
		t.Fatalf("切页后当前页 = %q，期望 conversation", st.currentPage)
	}
	if st.detailScroll.Y != 0 || st.detailScroll.X != 0 {
		t.Fatalf("selectSettingsPage 切页后滚动应归零，实际 X=%v Y=%v", st.detailScroll.X, st.detailScroll.Y)
	}
}

func TestSettingsNavAboutPinnedBottom(t *testing.T) {
	const winH = 760
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 960, winH)
	tt.Frame()

	storageRect, ok := tt.Find("版本与存储")
	if !ok {
		t.Fatalf("未找到版本与存储导航项：%v", tt.Texts())
	}
	aboutRect, ok := tt.Find("关于")
	if !ok {
		t.Fatalf("未找到关于导航项：%v", tt.Texts())
	}

	if aboutRect.Y <= storageRect.Y+storageRect.H {
		t.Fatalf("关于导航项应在所有普通导航项下方：storage=%+v about=%+v", storageRect, aboutRect)
	}
	if gap := aboutRect.Y - (storageRect.Y + storageRect.H); gap <= 100 {
		t.Fatalf("高窗口下关于导航项与最后一个普通导航项之间应有明显空隙，实际 gap=%v", gap)
	}
	if bottomMargin := float32(winH) - (aboutRect.Y + aboutRect.H); bottomMargin < 0 || bottomMargin > Space4 {
		t.Fatalf("关于导航项底边应贴近侧栏底边（winH=%d about=%+v bottomMargin=%v）", winH, aboutRect, bottomMargin)
	}

	// 搜索过滤掉关于时，底部不显示关于项。
	st.navSearch = "外观"
	tt.Frame()
	if _, ok := tt.Find("关于"); ok {
		t.Fatal("搜索过滤掉关于时，侧栏底部不应显示关于导航项")
	}

	// 搜索命中关于时，底部仍固定在侧栏底边。
	st.navSearch = "关于"
	tt.Frame()
	aboutFiltered, ok := tt.Find("关于")
	if !ok {
		t.Fatal("搜索关于时应显示关于导航项")
	}
	if bottomMargin := float32(winH) - (aboutFiltered.Y + aboutFiltered.H); bottomMargin < 0 || bottomMargin > Space4 {
		t.Fatalf("搜索命中关于时应固定在侧栏底边（winH=%d about=%+v bottomMargin=%v）", winH, aboutFiltered, bottomMargin)
	}
}
