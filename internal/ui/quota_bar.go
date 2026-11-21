package mcodeui

import (
	"fmt"

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

// renderQuotaBarSegments 渲染额度进度条的分段子元素（弹窗额度条与托盘额度条共用）。
// 必须在调用方进度条 Row 的 Children 里调用：固定渲染竖线前后共 10 个按「侧+类型」命名的槽位，避免段数变化时 key 复用错位导致总 grow 偏离 1 而挤压闪动；
// Marker 段用固定宽度 quotaMarkerWidth 并带 markerLabel，方便测试按文字标签定位竖线。keyPrefix 是各段 key 的前缀。
// fill 是整条额度条的分档色：正常段与超出时间进度的段同色，虚影段用同色 35% 透明。
func renderQuotaBarSegments(c *ui.Context, keyPrefix string, segs []quotaBarSegment, fill ui.Color, markerLabel string) {
	for _, slot := range quotaBarSlots(keyPrefix, segs) {
		if slot.kind == quotaBarSegmentMarker {
			ui.Box(c.Key(slot.key)).
				Label(markerLabel).
				Width(quotaMarkerWidth).
				FillHeight().
				Background(ThemePrimaryText(c))
			continue
		}
		box := ui.Box(c.Key(slot.key)).Width(0).FillHeight()
		if reduceMotion(c) {
			box.Jump("grow", slot.grow)
		}
		grow := box.AnimateTo("grow", slot.grow, ui.Animation{Duration: animateDuration(c, DurBase), Ease: ui.EaseOut})
		box.Grow(grow)
		switch slot.kind {
		case quotaBarSegmentNormal, quotaBarSegmentOver:
			box.Background(fill)
		case quotaBarSegmentGhost, quotaBarSegmentGhostOver:
			box.Background(fill.Alpha(0.35))
		}
	}
}
