package mcodeui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 动效 token 是全局过渡的唯一来源：时长必须为正且有序，位移量要与设计稿一致。
func TestMotionTokens_OrderedAndBounded(t *testing.T) {
	durations := map[string]time.Duration{
		"motionFast":       motionFast,
		"motionUnfold":     motionUnfold,
		"motionPop":        motionPop,
		"motionHoverDelay": motionHoverDelay,
		"motionSweep":      motionSweep,
		"motionRoll":       motionRoll,
		"motionRollFade":   motionRollFade,
	}
	for name, d := range durations {
		if d <= 0 {
			t.Fatalf("%s 必须为正，got %v", name, d)
		}
	}
	if motionFast >= motionUnfold {
		t.Fatalf("motionFast 应短于 motionUnfold，got %v / %v", motionFast, motionUnfold)
	}
	if motionUnfold > motionPop*2 {
		t.Fatalf("motionUnfold 不应超过 motionPop 的两倍，got %v / %v", motionUnfold, motionPop)
	}
	if motionRollFade > motionRoll {
		t.Fatalf("motionRollFade 不应长于 motionRoll，got %v / %v", motionRollFade, motionRoll)
	}
	if motionRiseOffset != 4 {
		t.Fatalf("motionRiseOffset 应为 4 DIP，got %v", motionRiseOffset)
	}
	if motionHandleLine != 2 {
		t.Fatalf("motionHandleLine 应为 2 DIP，got %v", motionHandleLine)
	}
	for _, ease := range []ui.Easing{motionEaseStandard, motionEaseEmphasis} {
		if v := ease(0); v > 0.001 {
			t.Fatalf("曲线起点应为 0，got %v", v)
		}
		if v := ease(1); v < 0.999 {
			t.Fatalf("曲线终点应为 1，got %v", v)
		}
		prev := float32(0)
		for i := 1; i <= 10; i++ {
			v := ease(float32(i) / 10)
			if v+0.001 < prev {
				t.Fatalf("曲线应单调不减：第 %d 步 %v < 上一步 %v", i, v, prev)
			}
			prev = v
		}
	}
}

// 交错入场：正常动效时第 i 项按 step 递增，减少动效时整体关闭（首项也无延迟）。
func TestStaggerDelayForReduceMotion(t *testing.T) {
	var normalFirst, normalThird, reducedThird time.Duration
	tt := ui.NewTester(func(c *ui.Context) {
		if reduceMotion(c) {
			reducedThird = staggerDelayFor(c, 3, staggerStep)
			return
		}
		normalFirst = staggerDelayFor(c, 0, staggerStep)
		normalThird = staggerDelayFor(c, 3, staggerStep)
	}, 100, 100)
	tt.SetPreferences(ui.Preferences{TextScale: 1})
	if normalFirst != 0 {
		t.Fatalf("第 0 项不应有延迟，实际 %v", normalFirst)
	}
	if want := 3 * staggerStep; normalThird != want {
		t.Fatalf("正常动效时第 3 项延迟应为 %v，实际 %v", want, normalThird)
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	if reducedThird != 0 {
		t.Fatalf("减少动效时交错入场应关闭，实际 %v", reducedThird)
	}
}

// animateDuration 是全局过渡时长的唯一入口：减少动效时必须返回 0。
func TestAnimateDurationReduceMotion(t *testing.T) {
	var normal, reduced time.Duration
	tt := ui.NewTester(func(c *ui.Context) {
		if reduceMotion(c) {
			reduced = animateDuration(c, motionUnfold)
			return
		}
		normal = animateDuration(c, motionUnfold)
	}, 100, 100)
	tt.SetPreferences(ui.Preferences{TextScale: 1})
	if normal != motionUnfold {
		t.Fatalf("正常动效时应返回原时长 %v，实际 %v", motionUnfold, normal)
	}
	tt.SetPreferences(ui.Preferences{ReduceMotion: true, TextScale: 1})
	if reduced != 0 {
		t.Fatalf("减少动效时应返回 0，实际 %v", reduced)
	}
}

// 折叠内容展开后可见、展开途中有中间高度、收起完成后不再渲染。
// 外部变量改变后必须先 Frame() 触发重建：Advance 只在视图请求了帧（有动画或定时器）时才出帧，
// 光改外部状态不会让视图重画。
func TestMotionUnfoldBodyExpandsAndCollapses(t *testing.T) {
	open := false
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c.Key("unfold-shell")).Fill().Children(func() {
			motionUnfoldBody(c, "unfold-test", open, func() {
				ui.Text(c, "折叠内容")
			})
		})
	}, 300, 200)

	if _, ok := tt.Find("折叠内容"); ok {
		t.Fatal("收起状态下不应渲染折叠内容")
	}

	open = true
	tt.Frame()
	if _, ok := tt.Find("折叠内容"); !ok {
		t.Fatalf("展开开始后应建好折叠内容节点，texts: %q", tt.Texts())
	}

	// 推进一半时长：内容应露出中间高度，既不是 0 也不是完整高度。
	tt.Advance(motionUnfold / 2)
	mid, ok := tt.Find("折叠内容")
	if !ok || mid.H <= 0 {
		t.Fatalf("展开途中应显示部分内容，got %+v ok=%v", mid, ok)
	}

	tt.Advance(motionUnfold/2 + 40*time.Millisecond)
	full, ok := tt.Find("折叠内容")
	if !ok || full.H <= 0 {
		t.Fatalf("展开后应显示完整内容，got %+v ok=%v", full, ok)
	}
	if mid.H >= full.H {
		t.Fatalf("展开途中高度应小于完整高度，got 途中 %v / 完整 %v", mid.H, full.H)
	}

	open = false
	tt.Frame()
	tt.Advance(motionUnfold + 40*time.Millisecond)
	if _, ok := tt.Find("折叠内容"); ok {
		t.Fatal("收起完成后不应再渲染折叠内容")
	}
}
