package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSwitchClick(t *testing.T) {
	on := false
	called := false
	tester := ui.NewTester(func(c *ui.Context) {
		Switch(c, "switch", &on, "测试开关", func(value bool) { called = value })
	}, 200, 100)
	if err := tester.Click("测试开关"); err != nil {
		t.Fatal(err)
	}
	if !called || !on {
		t.Fatal("点击未传出取反后的值")
	}
}

func TestSwitchDisabled(t *testing.T) {
	changed := false
	tester := ui.NewTester(func(c *ui.Context) {
		if SwitchDisabled(c, "switch", false, "禁用开关").Changed() {
			changed = true
		}
	}, 200, 100)
	if err := tester.Click("禁用开关"); err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("禁用开关响应了点击")
	}
}

func TestSegmentedClick(t *testing.T) {
	selected, changed := 0, -1
	tester := ui.NewTester(func(c *ui.Context) {
		Segmented(c, "segments", &selected, []Segment{{Label: "第一项"}, {Label: "第二项", Count: 12}}, func(index int) { changed = index })
	}, 300, 100)
	if _, ok := tester.Find("12"); !ok {
		t.Fatal("未显示选项数量")
	}
	if err := tester.Click("第二项"); err != nil {
		t.Fatal(err)
	}
	if selected != 1 || changed != 1 {
		t.Fatalf("选中 = %d，回调 = %d", selected, changed)
	}
}

func TestSwitchTrackColor(t *testing.T) {
	ui.NewTester(func(c *ui.Context) {
		theme := c.Theme()
		off := theme.Border.Mix(theme.Text, switchOffTextMix)
		if got := switchTrackColor(c, true, true); got != theme.Accent {
			t.Errorf("聚焦打开态轨道 = %v，期望 %v", got, theme.Accent)
		}
		if got := switchTrackColor(c, false, true); got != ThemeTertiaryText(c).Alpha(inactiveAccentAlpha) {
			t.Errorf("失焦打开态轨道 = %v", got)
		}
		if got := switchTrackColor(c, false, false); got != off {
			t.Errorf("关闭态轨道 = %v，期望 %v", got, off)
		}
		if got := inactiveAccent(c, theme.Accent); got != theme.Accent {
			t.Errorf("聚焦时强调色 = %v，期望 %v", got, theme.Accent)
		}
	}, 200, 100)
}
