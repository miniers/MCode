package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func assertVerticalGutterPixel(t *testing.T, tt *ui.Tester, borderX, padX int, bodyRect ui.Rect) {
	t.Helper()
	img := tt.Image()
	if img == nil {
		t.Fatal("Tester.Image() 返回 nil")
	}
	midY := int(bodyRect.Y + bodyRect.H/2)
	b := img.Bounds()
	if borderX < b.Min.X || borderX >= b.Max.X || padX < b.Min.X || padX >= b.Max.X || midY < b.Min.Y || midY >= b.Max.Y {
		t.Fatalf("采样坐标越界: borderX=%d padX=%d midY=%d bounds=%v", borderX, padX, midY, b)
	}
	gotBorder := img.RGBAAt(borderX, midY)
	gotPad := img.RGBAAt(padX, midY)
	if gotBorder == gotPad {
		t.Fatalf("导引线像素与右侧留白一致，未绘制左边框: border(%d,%d)=%+v pad(%d,%d)=%+v",
			borderX, midY, gotBorder, padX, midY, gotPad)
	}
}

func TestTimelineGutterHelper(t *testing.T) {
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).FillWidth().Children(func() {
			ui.Column(c.Key("plain")).FillWidth().Children(func() {
				ui.Text(c, "PLAIN-TEXT")
			})
			timelineGutter(c, ui.Column(c.Key("gutter")).FillWidth(), Space2).Children(func() {
				ui.Text(c, "GUTTER-TEXT")
			})
		})
	}, 800, 300)

	plainRect, ok := tt.Find("PLAIN-TEXT")
	if !ok {
		t.Fatal("找不到 PLAIN-TEXT")
	}
	gutterRect, ok := tt.Find("GUTTER-TEXT")
	if !ok {
		t.Fatal("找不到 GUTTER-TEXT")
	}
	wantShift := Space2 + Hairline + Space3
	if got := gutterRect.X - plainRect.X; got != wantShift {
		t.Fatalf("timelineGutter 左偏移 = %.1f，期望 %.1f", got, wantShift)
	}
	assertVerticalGutterPixel(t, tt, int(Space2), int(Space2+Hairline+Space1), gutterRect)
}

func TestTimelineGutterToolDetail(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)

	const bodyText = "TOOL-DETAIL-GUTTER"
	m := Message{ID: "t1", Role: "tool", Text: bodyText}
	tt := ui.NewTester(func(c *ui.Context) {
		renderToolDetail(c, s, m)
	}, 800, 300)

	detailRect, ok := tt.Find(bodyText)
	if !ok {
		t.Fatalf("找不到工具详情正文: %v", tt.Texts())
	}
	wantX := Space3 + Hairline + Space3 + 1 + DensityComfortable.PadX
	if detailRect.X != wantX {
		t.Fatalf("工具详情正文 X = %.1f，期望 %.1f", detailRect.X, wantX)
	}
	assertVerticalGutterPixel(t, tt, int(Space3), int(Space3+Hairline+Space1), detailRect)
}

func TestTimelineGutterReasoningBody(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.expandedReasoning = map[string]bool{"r1": true}

	const bodyText = "THINK-BODY-GUTTER"
	m := Message{ID: "r1", Role: "assistant", Kind: "reasoning", Text: bodyText}
	tt := ui.NewTesterWith(func(c *ui.Context) {
		renderReasoningMessage(c, s, m, "", false, false, TokenUsage{}, 0)
	}, 800, 300, ui.TesterOptions{Preferences: ui.Preferences{ReduceMotion: true}})
	tt.Advance(motionUnfold + time.Second)

	bodyRect, ok := tt.Find(bodyText)
	if !ok {
		t.Fatalf("找不到独立思考正文: %v", tt.Texts())
	}
	wantX := Space2 + Hairline + Space3
	if bodyRect.X != wantX {
		t.Fatalf("独立思考正文 X = %.1f，期望 %.1f", bodyRect.X, wantX)
	}
	assertVerticalGutterPixel(t, tt, int(Space2), int(Space2+Hairline+Space1), bodyRect)
}

func TestTimelineGutterToolBeatReasoningBody(t *testing.T) {
	s := newChatState(NewFakeClient())
	t.Cleanup(s.close)
	s.expandedReasoning = map[string]bool{"beat-full:r2": true}

	const firstPara = "BEAT-THINK-PARA-1"
	m := Message{
		ID:   "r2",
		Role: "assistant",
		Kind: "reasoning",
		Text: firstPara + "\n\nBEAT-THINK-PARA-2\n\nBEAT-THINK-PARA-3",
	}
	tt := ui.NewTester(func(c *ui.Context) {
		renderToolBeatReasoning(c, s, m, "")
	}, 800, 300)

	bodyRect, ok := tt.Find(firstPara)
	if !ok {
		t.Fatalf("找不到节拍思考正文: %v", tt.Texts())
	}
	wantX := Hairline + Space3
	if bodyRect.X != wantX {
		t.Fatalf("节拍思考正文 X = %.1f，期望 %.1f", bodyRect.X, wantX)
	}
	assertVerticalGutterPixel(t, tt, 0, int(Hairline+Space1), bodyRect)
}
