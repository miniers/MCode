package mcodeui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// renderQuotaBarSegments 渲染额度进度条的分段子元素（弹窗额度条与托盘额度条共用）。
// 必须在调用方进度条 Row 的 Children 里调用：普通段按 grow 占宽度，Marker 段用固定宽度
// quotaMarkerWidth 并带 markerLabel，方便测试按文字标签定位竖线。keyPrefix 是各段 key 的前缀。
// fill 是整条额度条的分档色：正常段与超出时间进度的段同色，虚影段用同色 35% 透明。
func renderQuotaBarSegments(c *ui.Context, keyPrefix string, segs []quotaBarSegment, fill ui.Color, markerLabel string) {
	for i, seg := range segs {
		key := fmt.Sprintf("%s-seg-%d", keyPrefix, i)
		if seg.kind == quotaBarSegmentMarker {
			ui.Box(c.Key(key)).
				Label(markerLabel).
				Width(quotaMarkerWidth).
				FillHeight().
				Background(ThemePrimaryText(c))
			continue
		}
		box := ui.Box(c.Key(key)).
			Width(0).
			Grow(seg.grow).
			FillHeight()
		switch seg.kind {
		case quotaBarSegmentNormal, quotaBarSegmentOver:
			box.Background(fill)
		case quotaBarSegmentGhost, quotaBarSegmentGhostOver:
			box.Background(fill.Alpha(0.35))
		}
	}
}
