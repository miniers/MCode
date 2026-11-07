package mcodeui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestButtonVariantsRender(t *testing.T) {
	for _, variant := range []ButtonVariant{ButtonPrimary, ButtonSecondary, ButtonGhost, ButtonDanger} {
		t.Run(fmt.Sprint(variant), func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				Button(c, ButtonOpts{Text: "操作", Variant: variant})
			}, 200, 100)
			if !tester.HasText("操作") {
				t.Fatal("按钮文字未渲染")
			}
		})
	}
}

// TestButtonSizesAndHitArea 断言布局尺寸等于 Size：按钮不再用最小尺寸把外框撑大。
// mygo 没有不占布局的扩展命中区，所以命中区只能等于 Size：32 的命中区要求暂未满足。
func TestButtonSizesAndHitArea(t *testing.T) {
	for _, size := range []float32{20, 24, 28, 32} {
		if got := buttonSize(size); got != size {
			t.Errorf("buttonSize(%v) = %v", size, got)
		}
	}
	for _, size := range []float32{ControlXS, ControlSM, ControlMD, ControlLG} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			tester := ui.NewTester(func(c *ui.Context) {
				IconButton(c, IconButtonProps{Label: "图标按钮", Size: size})
			}, 200, 100)
			bounds, ok := tester.Find("图标按钮")
			if !ok || bounds.W != size || bounds.H != size {
				t.Fatalf("布局尺寸应等于 Size %v：%+v，找到=%v", size, bounds, ok)
			}
		})
	}
}

func TestButtonDisabled(t *testing.T) {
	clicked := false
	tester := ui.NewTester(func(c *ui.Context) {
		Button(c, ButtonOpts{Text: "禁用按钮", Disabled: true, OnClick: func() { clicked = true }})
	}, 200, 100)
	if err := tester.Click("禁用按钮"); err != nil {
		t.Fatal(err)
	}
	if clicked {
		t.Fatal("禁用按钮触发了点击")
	}
}

func TestWindowActive(t *testing.T) {
	s := newChatState(nil)
	if !s.windowActive.Load() {
		t.Fatal("窗口初始状态应为激活")
	}
	s.setWindowActive(false)
	if s.windowActive.Load() {
		t.Fatal("窗口应为失焦状态")
	}
	s.setWindowActive(true)
	if !s.windowActive.Load() {
		t.Fatal("窗口应恢复激活状态")
	}
	if !windowActiveFor(nil) {
		t.Fatal("没有上下文时应默认为激活")
	}
	ui.NewTester(func(c *ui.Context) {
		if !windowActiveFor(c) {
			t.Error("没有关联状态时应默认为激活")
		}
	}, 100, 100)
}

func TestButtonBackground(t *testing.T) {
	colors := buttonColors{
		accent:          ui.Color{R: 200, A: 255},
		tertiary:        ui.Color{R: 100, A: 255},
		overlaySelected: ui.Color{R: 255, A: 26},
		overlayHover:    ui.Color{R: 255, A: 13},
		overlayPressed:  ui.Color{R: 255, A: 36},
		danger:          ui.Color{R: 255, A: 255},
	}
	for _, variant := range []ButtonVariant{ButtonSecondary, ButtonGhost} {
		base := buttonBackground(variant, true, true, 0, 0, colors)
		if base != colors.overlaySelected {
			t.Errorf("变体 %v 选中底色 = %v", variant, base)
		}
		hover := buttonBackground(variant, true, true, 1, 0, colors)
		if hover == ui.Transparent || hover.A <= base.A {
			t.Errorf("变体 %v 悬停丢失选中底色：%v", variant, hover)
		}
	}
	base := buttonBackground(ButtonDanger, false, true, 0, 0, colors)
	if hover := buttonBackground(ButtonDanger, false, true, 1, 0, colors); hover == base {
		t.Error("危险按钮悬停颜色未变化")
	}
	if got := buttonBackground(ButtonPrimary, false, false, 1, 1, colors); got == colors.accent || got != colors.tertiary.Alpha(0.18) {
		t.Errorf("失焦主按钮底色 = %v", got)
	}
}

func TestIconButtonLabelFallback(t *testing.T) {
	for _, tc := range []struct{ tooltip, label string }{
		{tooltip: "提示文字", label: "提示文字"},
		{label: "按钮"},
	} {
		tester := ui.NewTester(func(c *ui.Context) {
			IconButton(c, IconButtonProps{Tooltip: tc.tooltip})
		}, 200, 100)
		if _, ok := tester.Find(tc.label); !ok {
			t.Errorf("未找到回退标签 %q", tc.label)
		}
	}
}

func TestButtonPrimaryInactiveColor(t *testing.T) {
	accent, tertiary := ui.Color{R: 18, G: 52, B: 86, A: 255}, ui.Color{R: 171, G: 205, B: 239, A: 255}
	if got := buttonPrimaryColor(true, accent, tertiary); got != accent {
		t.Errorf("激活颜色 = %v", got)
	}
	if got := buttonPrimaryColor(false, accent, tertiary); got != tertiary.Alpha(0.18) {
		t.Errorf("失焦颜色 = %v", got)
	}
}

func TestInactiveAccent(t *testing.T) {
	accent, tertiary := ui.Color{R: 18, G: 52, B: 86, A: 255}, ui.Color{R: 171, G: 205, B: 239, A: 255}
	if got := inactiveAccentFor(true, accent, tertiary); got != accent {
		t.Errorf("聚焦颜色 = %v", got)
	}
	if got := inactiveAccentFor(false, accent, tertiary); got != tertiary.Alpha(inactiveAccentAlpha) {
		t.Errorf("失焦颜色 = %v", got)
	}
	if got := buttonPrimaryColor(false, accent, tertiary); got != inactiveAccentFor(false, accent, tertiary) {
		t.Errorf("主按钮失焦颜色与通用降级不一致：%v", got)
	}
	ui.NewTester(func(c *ui.Context) {
		if got := inactiveAccent(c, accent); got != accent {
			t.Errorf("没有窗口状态时应保持强调色：%v", got)
		}
	}, 100, 100)
}
