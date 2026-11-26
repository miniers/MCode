package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSettingsDetailScrollResetsOnPageSwitch(t *testing.T) {
	const winH = 420
	st := NewSettingsState(settingsTestDir(t), nil, nil)
	st.open = true
	st.currentPage = "general"

	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, 960, winH)
	tt.Frame()

	firstBefore, ok := tt.Find("默认权限")
	if !ok {
		t.Fatalf("未找到通用页第一行：%v", tt.Texts())
	}

	detail, ok := tt.Find("设置详情列")
	if !ok {
		t.Fatalf("未找到设置详情列：%v", tt.Texts())
	}
	tt.Scroll(detail.X+detail.W/2, 200, 0, 10000)
	tt.Frame()

	firstAfterScroll, ok := tt.Find("默认权限")
	if !ok {
		t.Fatalf("滚动后未找到通用页第一行：%v", tt.Texts())
	}
	if firstAfterScroll.H != 0 || firstAfterScroll.Y >= firstBefore.Y {
		t.Fatalf("通用页滚到底后第一行应移出顶部可视区：before=%+v after=%+v", firstBefore, firstAfterScroll)
	}
	bottomAfterScroll, ok := tt.Find("全部快捷键")
	if !ok || bottomAfterScroll.H <= 0 || bottomAfterScroll.Y+bottomAfterScroll.H > float32(winH) {
		t.Fatalf("通用页滚到底后底部行应在窗口内：ok=%v rect=%+v", ok, bottomAfterScroll)
	}

	// 切换到同一页不重置滚动位置。
	st.selectSettingsPage("general")
	tt.Frame()
	bottomSamePage, ok := tt.Find("全部快捷键")
	if !ok {
		t.Fatalf("同页切换后未找到通用页底部行：%v", tt.Texts())
	}
	if bottomSamePage.Y != bottomAfterScroll.Y || bottomSamePage.H <= 0 {
		t.Fatalf("切到同一页不应重置滚动：got %+v, want %+v", bottomSamePage, bottomAfterScroll)
	}

	// 通过点击侧栏导航项切到另一页后，新页第一行回到详情区顶部附近。
	navAppearance, ok := tt.Find("外观")
	if !ok || navAppearance.X >= 220 {
		t.Fatalf("未在侧栏找到外观导航项：ok=%v rect=%+v", ok, navAppearance)
	}
	tt.ClickAt(navAppearance.X+navAppearance.W/2, navAppearance.Y+navAppearance.H/2)
	tt.Frame()
	if st.currentPage != "appearance" {
		t.Fatalf("点击外观后当前页 = %q，期望 appearance", st.currentPage)
	}
	firstGeneralAfterSwitch, ok := tt.Find("默认权限")
	if ok && firstGeneralAfterSwitch.H > 0 {
		t.Fatalf("切到外观页后通用页第一行不应可见：got %+v", firstGeneralAfterSwitch)
	}

	firstAppearance, ok := tt.Find("主题")
	if !ok {
		t.Fatalf("切到外观页后未找到第一行：%v", tt.Texts())
	}
	if firstAppearance.H <= 0 || firstAppearance.Y < detail.Y || firstAppearance.Y+firstAppearance.H > detail.Y+detail.H {
		t.Fatalf("切到外观页后第一行应在详情区内可见：got %+v detail=%+v", firstAppearance, detail)
	}

	// 跨页通过旧 ID 跳到页面底部的存储清理行，目标行应落在窗口内。
	st.selectSettingsPage("storage")
	tt.Frame()
	if st.currentPage != "sourceControl" {
		t.Fatalf("切到 storage 后当前页 = %q，期望 sourceControl", st.currentPage)
	}
	storageRow, ok := tt.Find("客户端不可用")
	if !ok {
		t.Fatalf("跳到 storage 后未找到底部存储清理行：%v", tt.Texts())
	}
	if storageRow.H <= 0 || storageRow.Y < 0 || storageRow.Y+storageRow.H > float32(winH) {
		t.Fatalf("跨页跳到底部存储清理行后应在窗口内（winH=%d）：got %+v", winH, storageRow)
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
