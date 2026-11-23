package mcodeui

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"
)

// quotaBarSlot 是固定槽位布局中的一个子元素（槽位或时间进度竖线）。
type quotaBarSlot struct {
	key  string
	kind quotaBarSegmentKind
	grow float32
}

var quotaBarSlotKinds = [...]struct {
	kind quotaBarSegmentKind
	name string
}{
	{quotaBarSegmentNormal, "normal"},
	{quotaBarSegmentOver, "over"},
	{quotaBarSegmentGhost, "ghost"},
	{quotaBarSegmentGhostOver, "ghost-over"},
	{quotaBarSegmentEmpty, "empty"},
}

// quotaBarSlots 将分段列表映射到竖线前后各 5 个固定槽位（共 10 槽 + 1 竖线），
// 使每个槽位 key 永久绑定到固定侧别与段类型，不存在的槽位 grow 为 0。
func quotaBarSlots(keyPrefix string, segs []quotaBarSegment) []quotaBarSlot {
	var before, after [len(quotaBarSlotKinds)]float32
	afterMarker := false
	for _, seg := range segs {
		if seg.kind == quotaBarSegmentMarker {
			afterMarker = true
			continue
		}
		for i, spec := range quotaBarSlotKinds {
			if spec.kind == seg.kind {
				if afterMarker {
					after[i] += seg.grow
				} else {
					before[i] += seg.grow
				}
				break
			}
		}
	}

	slots := make([]quotaBarSlot, 0, len(quotaBarSlotKinds)*2+1)
	for i, spec := range quotaBarSlotKinds {
		slots = append(slots, quotaBarSlot{
			key:  fmt.Sprintf("%s-before-%s", keyPrefix, spec.name),
			kind: spec.kind,
			grow: before[i],
		})
	}
	slots = append(slots, quotaBarSlot{
		key:  fmt.Sprintf("%s-marker", keyPrefix),
		kind: quotaBarSegmentMarker,
	})
	for i, spec := range quotaBarSlotKinds {
		slots = append(slots, quotaBarSlot{
			key:  fmt.Sprintf("%s-after-%s", keyPrefix, spec.name),
			kind: spec.kind,
			grow: after[i],
		})
	}
	return slots
}

// renderQuotaBar 渲染额度进度条的分段子元素（弹窗额度条使用）。
// 对 used、elapsed、ghost 三个语义标量做动画而非分别动画各槽 grow，使虚影只从单一前沿扫过竖线并保持总 grow 为 1。
// 必须在调用方进度条 Row 的 Children 里调用：固定渲染竖线前后共 10 个按「侧+类型」命名的槽位，避免段数变化时 key 复用错位；
// Marker 段用固定宽度 quotaMarkerWidth 并带 markerLabel，方便测试按文字标签定位竖线。keyPrefix 是各段 key 的前缀。
// fill 是整条额度条的分档色：正常段与超出时间进度的段同色，虚影段用同色 35% 透明。
func renderQuotaBar(c *ui.Context, keyPrefix string, used, elapsed float64, paceKnown, projected bool, fill ui.Color, markerLabel string) {
	firstBox := ui.Box(c.Key(keyPrefix + "-before-normal")).Width(0).FillHeight()
	anim := ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut}
	usedTarget := float32(math.Max(0, math.Min(1, used)))
	elapsedTarget := float32(math.Max(0, math.Min(1, elapsed)))
	ghostTarget := float32(quotaBarGhostEnd(used, elapsed, paceKnown, projected))
	if reduceMotion(c) {
		firstBox.Jump("used", usedTarget)
		firstBox.Jump("elapsed", elapsedTarget)
		firstBox.Jump("ghost", ghostTarget)
	}
	animUsed := float64(firstBox.AnimateTo("used", usedTarget, anim))
	animElapsed := float64(firstBox.AnimateTo("elapsed", elapsedTarget, anim))
	animGhost := float64(firstBox.AnimateTo("ghost", ghostTarget, anim))
	markerAlphaTarget := float32(0.72)
	if projected {
		markerAlphaTarget = 1
	}
	if reduceMotion(c) {
		firstBox.Jump("marker-alpha", markerAlphaTarget)
	}
	markerAlpha := firstBox.AnimateTo("marker-alpha", markerAlphaTarget, ui.Animation{Duration: animateDuration(c, DurFast), Ease: ui.EaseOut})

	segs := quotaBarSegmentsTo(animUsed, animElapsed, animGhost, paceKnown)
	for i, slot := range quotaBarSlots(keyPrefix, segs) {
		if slot.kind == quotaBarSegmentMarker {
			ui.Box(c.Key(slot.key)).
				Label(markerLabel).
				Width(quotaMarkerWidth).
				FillHeight().
				Background(ThemePrimaryText(c).Alpha(markerAlpha))
			continue
		}
		box := firstBox
		if i > 0 {
			box = ui.Box(c.Key(slot.key)).Width(0).FillHeight()
		}
		box.Grow(slot.grow)
		switch slot.kind {
		case quotaBarSegmentNormal, quotaBarSegmentOver:
			box.Background(fill)
		case quotaBarSegmentGhost, quotaBarSegmentGhostOver:
			box.Background(fill.Alpha(0.35))
		}
	}
}
