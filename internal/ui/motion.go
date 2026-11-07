package mcodeui

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 动效 token：全应用的过渡时长、曲线与位移量只从这里取，避免各处各写一套 magic number。
// 时长按「越短越不打扰」排序：短过渡给开关与色块，中过渡给展开与浮层，长过渡只给数字滚动。
const (
	// motionFast 是开关、色块、图标这类即时反馈的过渡时长。
	motionFast = DurSnap
	// motionIconFade 是图标交叉淡入的时长：比 motionFast 更短，避免连点时拖尾。
	motionIconFade = DurFast
	// motionUnfold 是折叠区块展开/收起的过渡时长。
	motionUnfold = DurBase
	// motionPop 是浮层升起与淡入的过渡时长。
	motionPop = DurBase
	// motionHoverDelay 是悬停才出现的控件（时间线刻度、悬浮操作条）的延迟。
	motionHoverDelay = 150 * time.Millisecond
	// motionSweep 是一次扫光高亮从起点走到终点的时长。
	motionSweep = 1200 * time.Millisecond
	// motionRoll 是数字滚动一位的时长：位数多、变化频繁，给足时间看清。
	motionRoll = 900 * time.Millisecond
	// motionRollFade 是滚动中进/出字符的淡入淡出时长。
	motionRollFade = DurSlow
)

// motionRiseOffset 是浮层出现时抬起的距离（DIP）：配合淡入，浮层像是从锚点长出来的。
const motionRiseOffset float32 = 4

// motionHandleLine 是折叠区块左侧竖线的宽度（DIP），收起时高度收到 0，展开时随内容长高。
const motionHandleLine float32 = 2

// motionEaseStandard 是展开、浮层与手柄的统一曲线：起步快、收尾稳，短时长下前段更快、末端不拖
// （与 lody 的 easeSheet 同参数）。
var motionEaseStandard = EaseSnap

// motionEaseEmphasis 给需要「落定感」的位移用（数字滚动、展开）：后段比标准曲线更慢。
var motionEaseEmphasis = ui.CubicBezier(0.2, 0.9, 0.1, 1)

func reduceMotion(c *ui.Context) bool {
	return c.Preferences().ReduceMotion
}

func animateDuration(c *ui.Context, d time.Duration) time.Duration {
	if reduceMotion(c) {
		return 0
	}
	return d
}

// staggerDelayFor 返回第 i 项交错入场的延迟：减少动效时整体关闭（返回 0），
// 否则按 step 线性递增；i 为 0 或更小时没有延迟。
// 名字避开 tokens.go 里按令牌算基准延迟的 staggerDelay(i)。
func staggerDelayFor(c *ui.Context, i int, step time.Duration) time.Duration {
	if reduceMotion(c) || i <= 0 {
		return 0
	}
	return time.Duration(i) * step
}

// motionUnfoldBody 渲染可折叠内容：open 为真时按内容高度展开并淡入，为假时收起到 0 高。
// 收起过程中用 ClipY 裁掉溢出部分；内容高度未知（首帧、内容刚出现）时直接给满，
// 宁可先完整显示，也不让内容闪一下空白。
func motionUnfoldBody(c *ui.Context, key string, open bool, fn func()) {
	target := float32(0)
	if open {
		target = 1
	}
	clip := ui.Column(c.Key(key)).FillWidth().Shrink(0)
	k := clip.AnimateTo(key+"/unfold", target, ui.Animation{Duration: animateDuration(c, motionUnfold), Ease: motionEaseStandard})
	clip.Children(func() {
		// 完全收起时不再建内容子树：省掉布局与测量，也让已经收起的文字搜不到。
		if !open && k == 0 {
			return
		}
		inner := ui.Column(c.Key(key + "/body")).FillWidth().Shrink(0)
		inner.Children(fn)
		if h := inner.LayoutBounds().H; h > 0 && k < 1 {
			clip.ClipY().Height(k * h).Opacity(k)
		}
	})
}
