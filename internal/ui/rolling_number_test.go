package mcodeui

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// rollingDigits 拆字符、rollingAlign 右对齐补位：位数变多时高位补空，变少时截掉高位。
func TestRollingDigitsAndAlign(t *testing.T) {
	digits := []struct {
		value int
		want  string
	}{{0, "0"}, {7, "7"}, {42, "42"}, {-12, "-12"}}
	for _, tc := range digits {
		if got := string(rollingDigits(tc.value)); got != tc.want {
			t.Fatalf("rollingDigits(%d) = %q, want %q", tc.value, got, tc.want)
		}
	}
	aligns := []struct {
		prev, next, want string
	}{
		{"99", "100", "\x0099"},
		// 位数变少：旧值右对齐截到新长度，剩下的两位都从 0 变成 9，都要滚动。
		{"100", "99", "00"},
		{"1234", "1235", "1234"},
	}
	for _, tc := range aligns {
		got := string(rollingAlign([]byte(tc.prev), []byte(tc.next)))
		if got != tc.want {
			t.Fatalf("rollingAlign(%q, %q) = %q, want %q", tc.prev, tc.next, got, tc.want)
		}
	}
}

// 滚动位移：k 到 1 时进入字符归位到 0，离场字符已完全移出到 -lineH（被裁剪隐藏）；值变大时进入字符在下方、离场字符向上走。
func TestRollingShift(t *testing.T) {
	cases := []struct {
		k, dir          float32
		wantIn, wantOut float32
	}{
		{0, 1, 16, 0},
		{0.5, 1, 8, -8},
		{0.5, -1, -8, 8},
		{1, 1, 0, -16},
	}
	for _, tc := range cases {
		in, out := rollingShift(tc.k, tc.dir, 16)
		if in != tc.wantIn || out != tc.wantOut {
			t.Fatalf("rollingShift(%v, %v, 16) = (%v, %v), want (%v, %v)", tc.k, tc.dir, in, out, tc.wantIn, tc.wantOut)
		}
	}
}

// 变化的数字位才滚动：进入字符从下方升起、离场字符向上离开；不变的数字位完全静止。
func TestRollingDigitCellMovesOnlyChangedChar(t *testing.T) {
	const lineH float32 = 16
	var inY, outY, stillY float32
	var hasOut bool
	first := true
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("digit-shell")).Fill().Children(func() {
			_, inMove, outMove, _ := rollingDigitCell(c, "digit-move", 0, '4', '5', 1, first, lineH)
			_, inStill, _, _ := rollingDigitCell(c, "digit-still", 0, '0', '0', 1, first, lineH)
			inY, outY, hasOut = inMove.Bounds().Y, outMove.Bounds().Y, outMove.Valid()
			stillY = inStill.Bounds().Y
		})
		first = false
	}, 200, 100)

	// 滚动进度为 0：进入的字符还在下方一整行的位置，离场字符停在原位。
	if math.Abs(float64(inY-lineH)) > 0.5 {
		t.Fatalf("进入字符应从下方一行开始，got y=%v", inY)
	}
	if math.Abs(float64(outY)) > 0.5 {
		t.Fatalf("离场字符应从原位开始，got y=%v", outY)
	}
	if !hasOut {
		t.Fatalf("滚动中应渲染离场字符，texts: %q", tt.Texts())
	}

	inY0, outY0, stillY0 := inY, outY, stillY
	tt.Advance(motionRoll / 2)

	if !(inY < inY0-0.5 && inY > -0.5) {
		t.Fatalf("进入字符应向上靠近原位，got %v -> %v", inY0, inY)
	}
	if !(outY < outY0-0.5) {
		t.Fatalf("离场字符应向上移动，got %v -> %v", outY0, outY)
	}
	if math.Abs(float64(stillY-stillY0)) > 0.5 {
		t.Fatalf("数值不变的数字位不应移动，got %v -> %v", stillY0, stillY)
	}
	if !hasOut {
		t.Fatal("滚动途中应保留离场字符")
	}
}

// 数值不变时整串数字静止、首帧不播放滚动；变化时只换末位字符。
func TestRollingNumberKeepsUnchangedDigitsStill(t *testing.T) {
	value := 1234
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("roll-shell")).Fill().Children(func() {
			rollingNumber(c, "roll-int", value)
		})
	}, 200, 100)

	whole, ok := tt.Find("1234")
	if !ok {
		t.Fatalf("应渲染整串数字，texts: %q", tt.Texts())
	}
	thousand, ok := tt.Find("1")
	if !ok {
		t.Fatalf("应渲染千位字符，texts: %q", tt.Texts())
	}
	// 首帧不滚动：再跑一帧，位置不变。
	tt.Frame()
	if again, ok := tt.Find("1234"); !ok || again.Y != whole.Y {
		t.Fatalf("数值不变时整串数字不应移动，got %+v -> %+v ok=%v", whole, again, ok)
	}

	value = 1235
	tt.Frame()
	if !rollingLabelShown(tt, "4") || !rollingLabelShown(tt, "5") {
		t.Fatalf("滚动中应同时有离场与进入字符，texts: %q", tt.Texts())
	}
	if y, ok := tt.Find("1"); !ok || y.Y != thousand.Y {
		t.Fatalf("千位不应移动，got %+v -> %+v ok=%v", thousand, y, ok)
	}

	tt.Advance(motionRoll + 100*time.Millisecond)
	if rollingLabelShown(tt, "4") {
		t.Fatalf("滚动结束后离场字符应消失，texts: %q", tt.Texts())
	}
	if !rollingLabelShown(tt, "5") {
		t.Fatalf("滚动结束后应保留进入字符，texts: %q", tt.Texts())
	}
}

// rollingLabelShown 报告当前帧是否有完全等于 s 的文字或 Label。
func rollingLabelShown(tt *ui.Tester, s string) bool {
	for _, text := range tt.Texts() {
		if text == s {
			return true
		}
	}
	return false
}
