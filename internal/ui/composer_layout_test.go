package mcodeui

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestComposerCardConcentric(t *testing.T) {
	for _, hero := range []bool{false, true} {
		card, button := composerShellRadii(hero)
		if card-button != Space2 {
			t.Fatalf("hero=%v 卡片和按钮应保持同心: card=%v button=%v", hero, card, button)
		}
	}
	if composerActionBtnSize != ControlMD {
		t.Fatalf("操作按钮边长 = %v, want %v", composerActionBtnSize, ControlMD)
	}
	if composerDockPadBottom != 10 {
		t.Fatalf("停靠时卡片距窗口底部应为 10: got %v", composerDockPadBottom)
	}
	if composerDockMaxWidth != timelineContentMaxWidthStandard {
		t.Fatalf("输入框宽度应等于标准正文宽度")
	}
}

func TestComposerControlSpacingAndContextRing(t *testing.T) {
	if composerControlGap != Space2 {
		t.Fatalf("底栏控件间距 = %v, want %v", composerControlGap, Space2)
	}
	if ratio := composerContextRingDiameter / composerActionBtnSize; ratio < 0.60 || ratio > 0.65 {
		t.Fatalf("上下文圆环直径与按钮高度之比 = %v, want 0.60–0.65", ratio)
	}
	if diameter := composerContextMeterImageSize * 20.5 / 24; diameter < composerContextRingDiameter-0.01 || diameter > composerContextRingDiameter+0.01 {
		t.Fatalf("上下文圆环实际直径 = %v, want %v", diameter, composerContextRingDiameter)
	}
}

func TestComposerWidths(t *testing.T) {
	for _, docked := range []bool{false, true} {
		s := newChatState(NewFakeClient())
		s.selectedThreadID = "t1"
		if docked {
			s.messages = []Message{{ID: "m1", Role: "user", Text: "你好"}}
		}
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c).Fill().Children(func() { renderComposer(c, s) })
		}, 1200, 700)
		rect, ok := tt.Find("消息输入框")
		if !ok || rect.W != composerDockMaxWidth {
			t.Errorf("docked=%v: 输入框宽度 = %v, want %v", docked, rect.W, composerDockMaxWidth)
		}
		s.close()
	}
}
