package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestToneColors(t *testing.T) {
	for _, tone := range []Tone{ToneNeutral, ToneAccent, ToneSuccess, ToneWarning, ToneDanger} {
		fg, bg, border := toneColors(nil, tone)
		if fg == (ui.Color{}) || bg == (ui.Color{}) || border == (ui.Color{}) {
			t.Fatalf("色调 %v 的颜色不得为空：%v %v %v", tone, fg, bg, border)
		}
		if fg == bg {
			t.Fatalf("色调 %v 的前景和背景不能相同：%v", tone, fg)
		}
	}
}

func TestStatusDotColors(t *testing.T) {
	seen := make(map[ui.Color]Status)
	for _, status := range []Status{StatusIdle, StatusRunning, StatusAttention, StatusError} {
		color := statusDotColor(nil, status)
		if color == (ui.Color{}) {
			t.Fatalf("状态 %v 的颜色为空", status)
		}
		if previous, ok := seen[color]; ok {
			t.Fatalf("状态 %v 和 %v 使用相同颜色", previous, status)
		}
		seen[color] = status
	}
}

func TestCapsuleRendersAndDismisses(t *testing.T) {
	clicked, dismissed := 0, 0
	tt := ui.NewTester(func(c *ui.Context) {
		Capsule(c, CapsuleProps{
			Text: "测试胶囊", Size: ControlSM, Interactive: true, Dismissible: true,
			OnClick: func() { clicked++ }, OnDismiss: func() { dismissed++ },
		})
	}, 300, 100)
	if !tt.HasText("测试胶囊") {
		t.Fatalf("未渲染胶囊文字：%q", tt.Texts())
	}
	closeBounds, ok := tt.Find("关闭 测试胶囊")
	if !ok || closeBounds.W < ControlSM || closeBounds.H < ControlSM {
		t.Fatalf("关闭按钮命中区不足：%+v，找到：%v", closeBounds, ok)
	}
	if err := tt.Click("关闭 测试胶囊"); err != nil {
		t.Fatalf("点击关闭按钮失败：%v", err)
	}
	if dismissed != 1 || clicked != 0 {
		t.Fatalf("关闭触发次数=%d，主体触发次数=%d", dismissed, clicked)
	}
}

func TestCapsuleSelectedColors(t *testing.T) {
	ui.NewTester(func(c *ui.Context) {
		fg, bg := capsuleSelectedColors(c, true)
		if fg != ThemeAccent(c) {
			t.Errorf("聚焦选中前景 = %v", fg)
		}
		if bg != ThemeAccentSubtle(c) {
			t.Errorf("聚焦选中底色 = %v", bg)
		}
		fg, bg = capsuleSelectedColors(c, false)
		if fg != ThemeAccent(c) {
			t.Errorf("失焦选中前景应保持不变：%v", fg)
		}
		if bg != ThemeTertiaryText(c).Alpha(inactiveAccentAlpha) {
			t.Errorf("失焦选中底色 = %v", bg)
		}
	}, 200, 100)
}

func TestStatusDotReduceMotion(t *testing.T) {
	var duration time.Duration
	tt := ui.NewTester(func(c *ui.Context) {
		duration = animateDuration(c, statusDotPulsePeriod)
		StatusDot(c, nil, StatusRunning, "运行中")
	}, 100, 100)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	if duration != 0 {
		t.Fatalf("减少动效时呼吸周期应为零，实际 %v", duration)
	}
	if _, ok := tt.Find("运行中"); !ok {
		t.Fatal("状态点缺少无障碍名称")
	}
}
