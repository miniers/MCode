package mcodeui

import (
	"bytes"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestMotionContentReducedAttachment(t *testing.T) {
	show := false
	tester := ui.NewTesterWith(func(c *ui.Context) {
		ui.Column(c.Key("attachment-shell")).Fill().Children(func() {
			if show {
				renderImageAttachmentCard(c, &chatState{}, composerAttachment{LocalID: "motion-content", Name: "图片"}, false)
			}
		})
	}, 200, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	empty := append([]byte(nil), tester.Image().Pix...)
	show = true
	tester.Frame()
	first := append([]byte(nil), tester.Image().Pix...)
	if bytes.Equal(empty, first) {
		t.Fatal("新附件没有显示")
	}
	tester.Advance(time.Second)
	if !bytes.Equal(first, tester.Image().Pix) {
		t.Fatal("减少动效时新附件应立即显示最终画面")
	}
}

func TestMotionContentReducedNumber(t *testing.T) {
	const key = "motion-content-reduced-number"
	t.Cleanup(func() {
		rollingNumbers.mu.Lock()
		delete(rollingNumbers.items, key)
		rollingNumbers.mu.Unlock()
	})
	value := 9
	tester := ui.NewTesterWith(func(c *ui.Context) {
		rollingNumber(c, key, value)
	}, 200, 100, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	for _, tc := range []struct {
		value int
		label string
		old   string
	}{{10, "10", "9"}, {9, "9", "0"}, {-2, "-2", "9"}} {
		value = tc.value
		tester.Frame()
		if _, ok := tester.Find(tc.label); !ok || rollingLabelShown(tester, tc.old) {
			t.Fatalf("减少动效应只显示终值 %s：%q", tc.label, tester.Texts())
		}
		tester.Frame()
		if tester.FramePending() {
			t.Fatal("减少动效下数字不应持续排帧")
		}
	}
}

func TestMotionContentNumberAnimation(t *testing.T) {
	const key = "motion-content-normal-number"
	t.Cleanup(func() {
		rollingNumbers.mu.Lock()
		delete(rollingNumbers.items, key)
		rollingNumbers.mu.Unlock()
	})
	value := 9
	tester := ui.NewTester(func(c *ui.Context) {
		rollingNumber(c, key, value)
	}, 200, 100)
	tester.Frame()
	if tester.FramePending() {
		t.Fatal("首次显示数字不应滚动")
	}
	value = 10
	tester.Frame()
	if !rollingLabelShown(tester, "9") || !rollingLabelShown(tester, "0") || !tester.FramePending() {
		t.Fatalf("数字变化应播放滚动：%q", tester.Texts())
	}
	tester.Advance(motionRoll + 100*time.Millisecond)
	tester.Frame()
	if rollingLabelShown(tester, "9") || tester.FramePending() {
		t.Fatalf("滚动结束后不应重播：%q", tester.Texts())
	}
	value = 9
	tester.Frame()
	tester.SetPreferences(ui.Preferences{ReduceMotion: true})
	tester.Frame()
	if rollingLabelShown(tester, "0") || !rollingLabelShown(tester, "9") {
		t.Fatalf("切换减少动效后应立即显示终值：%q", tester.Texts())
	}
	tester.Advance(time.Second)
	if tester.FramePending() {
		t.Fatal("切换减少动效后数字不应持续排帧")
	}
}

func TestMotionContentReducedOnboardingStep(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	u := onboardingState(s)
	u.open, u.step = true, onboardingWelcome
	tester := ui.NewTesterWith(func(c *ui.Context) {
		renderOnboardingDialog(c, s)
	}, 800, 700, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	if !tester.HasText("欢迎使用 mcode") {
		t.Fatal("未显示引导首页")
	}
	for _, tc := range []struct {
		step  onboardingStep
		title string
	}{{onboardingAgents, "选择你的智能体"}, {onboardingFinish, "准备好了"}} {
		u.mu.Lock()
		u.step = tc.step
		u.mu.Unlock()
		tester.Frame()
		if !tester.HasText(tc.title) {
			t.Fatalf("切换步骤后未立即显示 %s", tc.title)
		}
		first := append([]byte(nil), tester.Image().Pix...)
		tester.Advance(time.Second)
		if !bytes.Equal(first, tester.Image().Pix) {
			t.Fatal("减少动效时步骤切换应立即到达最终画面")
		}
	}
}
