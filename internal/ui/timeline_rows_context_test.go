package mcodeui

import (
	"bytes"
	"testing"

	"github.com/egoist/mygo/ui"
)

// TestUIReview3FlashTimelineRowReduceMotion 验证开启 ReduceMotion 时跳转落点行仍有静态高亮、
// 不请求下一帧动画，且清除 flashMessageID 后恢复无高亮状态。
func TestUIReview3FlashTimelineRowReduceMotion(t *testing.T) {
	setAppReduceMotion(t, true)
	options := ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}}

	s := newChatState(NewFakeClient())
	item := timelineItem{
		kind: timelineItemSingle,
		id:   "m1",
		msg:  Message{ID: "m1", Role: "assistant", Text: "目标消息"},
	}

	view := func(c *ui.Context) {
		row := ui.Row(c.Key("timeline-row-"+item.id)).Size(120, 40)
		flashTimelineRow(c, s, item, row)
	}
	expectedView := func(c *ui.Context) {
		ui.Row(c.Key("timeline-row-"+item.id)).
			Size(120, 40).
			Background(ThemeAccent(c).Alpha(0.14)).
			Border(2, ThemeAccent(c).Alpha(0.5))
	}

	tester := ui.NewTesterWith(view, 160, 80, options)
	tester.Frame()
	baselinePix := append([]byte(nil), tester.Image().Pix...)

	s.mu.Lock()
	s.flashMessageID = "m1"
	s.flashSeq = 1
	s.mu.Unlock()

	tester.Frame()
	if tester.FramePending() {
		t.Fatal("ReduceMotion 开启时落点高亮不应请求下一帧动画")
	}
	if bytes.Equal(tester.Image().Pix, baselinePix) {
		t.Fatal("ReduceMotion 开启且 flashMessageID 命中时应显示静态高亮")
	}

	expectedTester := ui.NewTesterWith(expectedView, 160, 80, options)
	expectedTester.Frame()
	if !bytes.Equal(tester.Image().Pix, expectedTester.Image().Pix) {
		t.Fatal("ReduceMotion 静态高亮样式与预期不一致")
	}

	clearJumpFlash(s, 1)
	tester.Frame()
	if tester.FramePending() {
		t.Fatal("清除落点高亮后不应请求下一帧动画")
	}
	if !bytes.Equal(tester.Image().Pix, baselinePix) {
		t.Fatal("清除 flashMessageID 后高亮应消失")
	}
}
