package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func newUIReview25SettingsTester(t *testing.T, page string, width, height int, setup func(*FakeClient, *chatState, *SettingsState)) (*ui.Tester, *SettingsState) {
	t.Helper()
	previousApply, previousTheme := applyThemeSource, appearanceThemeApplied
	t.Cleanup(func() {
		applyThemeSource = previousApply
		appearanceThemeApplied = previousTheme
	})
	applyThemeSource = func(mygo.ThemeSource) {}
	appearanceThemeApplied = ""

	fc := NewFakeClient()
	chat := newChatState(fc)
	t.Cleanup(chat.close)
	st := NewSettingsState(settingsTestDir(t), nil, chat)
	st.open = true
	st.currentPage = page
	if setup != nil {
		setup(fc, chat, st)
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderSettingsWindow(c, st)
	}, width, height)
	tt.Frame()
	return tt, st
}

func uiReview25AccessOnScreen(t *testing.T, tt *ui.Tester, role ui.Role, name string) ui.AccessInfo {
	t.Helper()
	winH := float32(tt.Image().Bounds().Dy())
	var last ui.AccessInfo
	var prevBounds ui.Rect
	var prevDY float32
	scale := float32(1)
	for i := 0; i < 100; i++ {
		info, ok := tt.AccessByRole(role, name)
		if !ok {
			t.Fatalf("找不到元素 (role=%v name=%q): %v", role, name, tt.AccessAll())
		}
		last = info
		detail, ok := tt.Find("设置详情列")
		if !ok {
			t.Fatalf("找不到设置详情列: %v", tt.Texts())
		}
		vpTop := max(detail.Y, float32(0))
		vpBot := min(detail.Y+detail.H, winH)
		fullyInWindow := !info.Offscreen && info.Bounds.W > 0 && info.Bounds.H > 0 &&
			info.Bounds.Y >= 0 && info.Bounds.Y+info.Bounds.H <= winH
		if fullyInWindow && ((info.Bounds.Y > vpTop+0.5 && info.Bounds.Y+info.Bounds.H < vpBot-0.5) || (i > 0 && info.Bounds == prevBounds)) {
			return info
		}
		prevBounds = info.Bounds

		vpH := max(vpBot-vpTop, float32(1))
		vpMid := vpTop + vpH/2
		maxStep := max(vpH*0.6, float32(200))
		fineStep := max(vpH*0.25, float32(120))
		var dy float32
		switch {
		case info.Offscreen || info.Bounds.W <= 0 || info.Bounds.H <= 0:
			if info.Bounds.Y >= vpBot {
				dy = min(info.Bounds.Y-vpMid, maxStep)
			} else {
				dy = -max(vpH*0.5, float32(160))
			}
		case info.Bounds.Y <= vpTop+0.5:
			dy = -fineStep
		default:
			dy = fineStep
		}
		if prevDY*dy < 0 {
			scale = max(scale*0.5, float32(0.05))
		}
		dy *= scale
		prevDY = dy
		tt.Scroll(detail.X+detail.W/2, detail.Y+detail.H/2, 0, dy)
		tt.Frame()
	}
	t.Fatalf("滚动后元素仍未完全进入可见区 (role=%v name=%q): %+v", role, name, last)
	return last
}

func assertUIReview25DetailBounds(t *testing.T, tt *ui.Tester, winW float32, cardLabel string, skipNames ...string) {
	t.Helper()
	skip := make(map[string]bool, len(skipNames))
	for _, n := range skipNames {
		skip[n] = true
	}
	detail, ok := tt.Find("设置详情列")
	if !ok {
		t.Fatalf("找不到设置详情列: %v", tt.Texts())
	}
	// 水平内边距由外层容器承担，卡片宽度不超过设置详情列自身右边界。
	limit := min(winW, detail.X+detail.W)
	if cardLabel != "" {
		card, ok := tt.Find(cardLabel)
		if !ok {
			t.Fatalf("找不到详情卡片 %q: %v", cardLabel, tt.Texts())
		}
		if card.W > 0 {
			if card.X+card.W > winW+0.5 {
				t.Errorf("详情卡片 %q 超出窗口右边界: card=%+v winW=%.1f", cardLabel, card, winW)
			}
			limit = min(limit, card.X+card.W)
		}
	}
	for _, a := range tt.AccessAll() {
		if a.Bounds.W <= 0 && a.Bounds.H <= 0 {
			continue
		}
		if a.Name == "设置详情列" || a.Bounds.X < detail.X || skip[a.Name] {
			continue
		}
		right := a.Bounds.X + a.Bounds.W
		if right > winW+0.5 || right > limit+0.5 {
			t.Errorf("控件超出右边界 (role=%v name=%q value=%q bounds=%+v, limit=%.1f, winW=%.1f)",
				a.Role, a.Name, a.Value, a.Bounds, limit, winW)
		}
	}
}

func assertUIReview25SheetBounds(t *testing.T, tt *ui.Tester, winW, winH float32, sheetLabel string) {
	t.Helper()
	sheet, ok := tt.Find(sheetLabel)
	if !ok {
		t.Fatalf("找不到面板 %q: %v", sheetLabel, tt.Texts())
	}
	if sheet.X < -0.5 || sheet.Y < -0.5 || sheet.X+sheet.W > winW+0.5 || sheet.Y+sheet.H > winH+0.5 {
		t.Errorf("面板 %q 超出窗口: sheet=%+v win=%.0fx%.0f", sheetLabel, sheet, winW, winH)
	}
	cardLimit := min(winW, sheet.X+sheet.W)
	for _, a := range tt.AccessAll() {
		if a.Bounds.W <= 0 && a.Bounds.H <= 0 {
			continue
		}
		// 过滤面板之前的底层设置窗口控件。
		if a.Bounds.X < sheet.X-0.5 {
			continue
		}
		limit := cardLimit
		// modalResizeGripsMin 按面板内容框定位，会覆盖到外层 1pt 边框上。
		if a.Name == "调整弹窗大小" {
			limit = min(winW, sheet.X+sheet.W+Hairline)
		}
		right := a.Bounds.X + a.Bounds.W
		if right > limit+0.5 {
			t.Errorf("面板 %q 内控件超出右边界 (role=%v name=%q value=%q bounds=%+v, limit=%.1f)",
				sheetLabel, a.Role, a.Name, a.Value, a.Bounds, limit)
		}
	}
}

func TestUIReview25_SettingsNarrow640_SourceControl(t *testing.T) {
	tt, _ := newUIReview25SettingsTester(t, "sourceControl", 640, 700, nil)
	assertUIReview25DetailBounds(t, tt, 640, "设置卡片 sourceControl.commit")
	area := uiReview25AccessOnScreen(t, tt, ui.RoleTextField, "提交信息附加说明")
	if area.Bounds.W < 100 {
		t.Fatalf("640 提交信息附加说明宽度过窄: %+v", area.Bounds)
	}
}

func TestUIReview25_SettingsNarrow640_Workbench(t *testing.T) {
	tt, _ := newUIReview25SettingsTester(t, "workbench", 640, 700, nil)
	assertUIReview25DetailBounds(t, tt, 640, "设置卡片 workbench.instructions")
	assertUIReview25DetailBounds(t, tt, 640, "设置卡片 workbench.terminal")
	area := uiReview25AccessOnScreen(t, tt, ui.RoleTextField, "AGENTS.md")
	if area.Bounds.W < 100 {
		t.Fatalf("640 AGENTS.md 宽度过窄: %+v", area.Bounds)
	}
}

func TestUIReview25_SettingsNarrow640_Nowledge(t *testing.T) {
	tt, _ := newUIReview25SettingsTester(t, "extensions", 640, 700, func(fc *FakeClient, chat *chatState, _ *SettingsState) {
		fc.SetMemorySpaces([]MemorySpace{{ID: "default", Name: "Default"}, {ID: "mcode", Name: "mcode"}})
		chat.projects = []Project{
			{ID: "p1", Name: "MCode", Path: "/repo/mcode"},
			{ID: "p2", Name: "LongProjectNameForNarrowWidthCheck", Path: "/repo/long"},
		}
	})
	waitFor(t, tt, func() bool { return tt.HasText("MCode") })
	assertUIReview25DetailBounds(t, tt, 640, "设置卡片 nowledge.spaces")
}

func TestUIReview25_SettingsNarrow640_GeneralAndAppearanceAndThemesSlider(t *testing.T) {
	t.Run("通用页下拉", func(t *testing.T) {
		tt, _ := newUIReview25SettingsTester(t, "general", 640, 700, nil)
		assertUIReview25DetailBounds(t, tt, 640, "设置卡片 general.permissions")
	})

	t.Run("外观页主题分节", func(t *testing.T) {
		tt, _ := newUIReview25SettingsTester(t, "appearance", 640, 700, nil)
		assertUIReview25DetailBounds(t, tt, 640, "设置卡片 appearance.theme")
	})

	t.Run("主题面板滑块", func(t *testing.T) {
		modalDragReset("settings-sheet-themes")
		t.Cleanup(func() { modalDragReset("settings-sheet-themes") })
		tt, st := newUIReview25SettingsTester(t, "appearance", 640, 700, func(_ *FakeClient, _ *chatState, st *SettingsState) {
			openSettingsSheet(st, "themes", "")
		})
		t.Cleanup(func() { closeSettingsSheet(st) })
		waitFor(t, tt, func() bool {
			_, ok := tt.Find("设置面板 主题")
			return ok && tt.HasText("侧栏字号")
		})
		assertUIReview25SheetBounds(t, tt, 640, 700, "设置面板 主题")
	})
}

func TestUIReview25_SettingsNarrow640_ProviderSheet(t *testing.T) {
	resetProviderSheetLayoutForTest()
	t.Cleanup(resetProviderSheetLayoutForTest)
	tt, st := newUIReview25SettingsTester(t, "models", 640, 700, func(fc *FakeClient, _ *chatState, st *SettingsState) {
		fc.SetProviders([]ProviderInfo{{ID: "claude", Name: "Claude", Available: true, Models: []ModelInfo{{ID: "sonnet", Name: "Sonnet"}}}})
		openProviderSheet(st, "cli", "claude")
	})
	t.Cleanup(func() { closeSettingsSheet(st) })
	waitFor(t, tt, func() bool {
		_, ok := tt.Find("设置面板 供应商")
		return ok
	})
	sheet, ok := tt.Find("设置面板 供应商")
	if !ok || sheet.W > 640-modalDragPanelInset+0.5 {
		t.Fatalf("供应商面板宽度应夹紧到窗口留白内: %+v", sheet)
	}
	closeBtn, ok := tt.Find("关闭供应商")
	if !ok {
		t.Fatalf("找不到关闭供应商按钮: %v", tt.Texts())
	}
	if closeBtn.X < sheet.X || closeBtn.X+closeBtn.W > sheet.X+sheet.W || closeBtn.X+closeBtn.W > 640 || closeBtn.Y < 0 || closeBtn.Y+closeBtn.H > 700 {
		t.Errorf("关闭供应商按钮不在面板与 640x700 窗口内: btn=%+v sheet=%+v", closeBtn, sheet)
	}
	assertUIReview25SheetBounds(t, tt, 640, 700, "设置面板 供应商")
}

func TestUIReview25_SettingsWide960_PreserveSelectWidths(t *testing.T) {
	t.Run("通用页Select保持160", func(t *testing.T) {
		tt, _ := newUIReview25SettingsTester(t, "general", 960, 700, nil)
		sel, ok := tt.AccessByRole(ui.RolePopUpButton, "默认权限")
		if !ok {
			t.Fatalf("找不到默认权限下拉框: %v", tt.AccessAll())
		}
		if sel.Bounds.W != 160 {
			t.Fatalf("960 宽窗口默认权限下拉框宽度 = %.1f，期望 160", sel.Bounds.W)
		}
	})

	t.Run("工作台页Select保持220", func(t *testing.T) {
		tt, _ := newUIReview25SettingsTester(t, "workbench", 960, 700, nil)
		sel := uiReview25AccessOnScreen(t, tt, ui.RolePopUpButton, "默认终端")
		if sel.Bounds.W != settingsSidePanelWidth {
			t.Fatalf("960 宽窗口默认终端下拉框宽度 = %.1f，期望 %.0f", sel.Bounds.W, settingsSidePanelWidth)
		}
	})

	t.Run("扩展页记忆空间Select保持160", func(t *testing.T) {
		tt, _ := newUIReview25SettingsTester(t, "extensions", 960, 700, func(fc *FakeClient, chat *chatState, _ *SettingsState) {
			fc.SetMemorySpaces([]MemorySpace{{ID: "default", Name: "Default"}, {ID: "mcode", Name: "mcode"}})
			chat.projects = []Project{{ID: "p1", Name: "MCode", Path: "/repo/mcode"}}
		})
		waitFor(t, tt, func() bool { return tt.HasText("MCode") })
		sel := uiReview25AccessOnScreen(t, tt, ui.RolePopUpButton, "MCode")
		if sel.Bounds.W != settingsFieldMediumWidth {
			t.Fatalf("960 宽窗口项目记忆空间下拉框宽度 = %.1f，期望 %.0f (all=%v)", sel.Bounds.W, settingsFieldMediumWidth, fmt.Sprintf("%+v", sel))
		}
	})
}
