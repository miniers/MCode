package mcodeui

import (
	"fmt"
	"math"
	"time"
)

// paceState 是额度用量节奏的判定结果。
type paceState int

const (
	// paceStateUnknown 表示数据不足，不做预测。
	paceStateUnknown paceState = iota
	// paceStateNormal 表示按当前速度不会在重置前用尽。
	paceStateNormal
	// paceStateEarly 表示按当前速度会在重置前用尽。
	paceStateEarly
)

// paceMinUsedPercent 与 paceMinElapsedRatio 是开始预测的最低门槛：
// 已用不足 5%（已用比例）或已过去不足窗口时长的 3% 时不预测。
const (
	paceMinUsedPercent  = 5.0
	paceMinElapsedRatio = 0.03
)

// quotaElapsedRatioAt 按重置时刻与窗口分钟数计算时间已过比例（0 到 1）。
// 时刻为零、时长未知、窗口还没开始或已到重置时刻时返回 false。
func quotaElapsedRatioAt(now, resetsAt time.Time, minutes int) (float64, bool) {
	if now.IsZero() || resetsAt.IsZero() || minutes <= 0 {
		return 0, false
	}
	window := time.Duration(minutes) * time.Minute
	elapsed := now.Sub(resetsAt.Add(-window))
	if elapsed <= 0 || !resetsAt.After(now) {
		// 窗口还没开始（重置时间晚于一个完整周期）或已到重置时刻（数据尚未刷新）。
		return 0, false
	}
	return math.Max(0, math.Min(1, float64(elapsed)/float64(window))), true
}

// quotaElapsedRatio 计算一个额度窗口的时间已过比例：窗口总时长从 Period 或窗口名推断
// （复用 quotaStripWindowKey），起始时刻按「重置时间 - 总时长」倒推。
func quotaElapsedRatio(now time.Time, w ProviderQuotaWindow) (float64, bool) {
	_, minutes, ok := quotaStripWindowKey(w)
	if !ok {
		return 0, false
	}
	return quotaElapsedRatioAt(now, w.ResetsAt, minutes)
}

// quotaPace 按「已用比例 / 已过去时长」线性外推一个额度窗口的用量节奏。
// 窗口总时长从 Period 或窗口名推断（复用 quotaStripWindowKey），起始时刻按「重置时间 - 总时长」倒推。
// 若预计用尽早于重置时间，返回 paceStateEarly 与预计用尽时刻；否则返回 paceStateNormal。
func quotaPace(now time.Time, w ProviderQuotaWindow) (state paceState, runOutAt time.Time) {
	used := math.Max(0, math.Min(100, w.UsedPercent))
	if used < paceMinUsedPercent {
		return paceStateUnknown, time.Time{}
	}
	ratio, ok := quotaElapsedRatio(now, w)
	if !ok {
		return paceStateUnknown, time.Time{}
	}
	if float64(ratio) < paceMinElapsedRatio {
		return paceStateUnknown, time.Time{}
	}
	if used >= 100 {
		// 已经用满：用尽时刻就是当前时刻。
		return paceStateEarly, now
	}
	// ratio 已经按窗口时长归一化，这里换回已过去时长用于外推。
	_, minutes, ok := quotaStripWindowKey(w)
	if !ok || minutes <= 0 {
		return paceStateUnknown, time.Time{}
	}
	scale := float64(time.Duration(minutes) * time.Minute)
	elapsed := time.Duration(math.Round(ratio * scale))
	remaining := 100 - used
	// 剩余比例按「已用/已过去」的速度消耗，还剩多久用尽；四舍五入到纳秒，避免浮点截断带来的误差。
	runOut := now.Add(time.Duration(math.Round(float64(remaining) / used * float64(elapsed))))
	if runOut.Before(w.ResetsAt) {
		return paceStateEarly, runOut
	}
	return paceStateNormal, time.Time{}
}

// quotaBarSegmentKind 是额度进度条分段的类型。
type quotaBarSegmentKind int

const (
	// quotaBarSegmentNormal 是正常填充（未超出时间进度）。
	quotaBarSegmentNormal quotaBarSegmentKind = iota
	// quotaBarSegmentOver 是超过时间进度的部分。
	quotaBarSegmentOver
	// quotaBarSegmentGhost 是按当前配速预测的虚影。
	quotaBarSegmentGhost
	// quotaBarSegmentGhostOver 是会在重置前用尽、一直延伸到末端的虚影。
	quotaBarSegmentGhostOver
	// quotaBarSegmentEmpty 是空轨道。
	quotaBarSegmentEmpty
	// quotaBarSegmentMarker 是时间进度竖线，grow 为 0。
	quotaBarSegmentMarker
)

// quotaBarSegment 是额度进度条的一段：kind 决定颜色，grow 决定该段占整条宽度的比例。
type quotaBarSegment struct {
	kind quotaBarSegmentKind
	grow float32
}

// quotaBarSegments 把额度进度条拆成按顺序渲染的段。
// used 是已用比例，elapsed 是时间已过比例（两者都限制在 0 到 1）。竖线永远落在 elapsed 处；
// paceKnown 为 false 时不区分超速段，0 到 used 都是正常填充；projected 为真时再叠加预测虚影：
// 未超速时虚影覆盖 used 到 min(1, used/elapsed)，超速时虚影延伸到末端。
// 返回的各段 grow 之和为 1，可直接放进同一行按比例排布；长度为 0 的段会被丢弃。
func quotaBarSegments(used, elapsed float64, paceKnown, projected bool) []quotaBarSegment {
	used = math.Max(0, math.Min(1, used))
	elapsed = math.Max(0, math.Min(1, elapsed))

	// 先按区间收集非空段，再由补空轨道与竖线拆分统一处理。
	type span struct {
		kind     quotaBarSegmentKind
		from, to float64
	}
	var spans []span
	addSpan := func(kind quotaBarSegmentKind, from, to float64) {
		from = math.Max(0, from)
		to = math.Min(1, to)
		if to > from {
			spans = append(spans, span{kind: kind, from: from, to: to})
		}
	}

	over := paceKnown && used > elapsed
	fillEnd := used
	if over {
		fillEnd = elapsed
	}
	addSpan(quotaBarSegmentNormal, 0, fillEnd)
	if over {
		addSpan(quotaBarSegmentOver, elapsed, used)
	}
	if projected && paceKnown && elapsed > 0 {
		projectedUsed := math.Min(1, used/elapsed)
		if used <= elapsed {
			addSpan(quotaBarSegmentGhost, used, projectedUsed)
		} else {
			addSpan(quotaBarSegmentGhostOver, used, 1)
		}
	}

	// 段与段之间的空隙就是空轨道。
	filled := make([]span, 0, len(spans)+2)
	cursor := 0.0
	for _, s := range spans {
		if s.from > cursor {
			filled = append(filled, span{kind: quotaBarSegmentEmpty, from: cursor, to: s.from})
		}
		filled = append(filled, s)
		cursor = s.to
	}
	if cursor < 1 {
		filled = append(filled, span{kind: quotaBarSegmentEmpty, from: cursor, to: 1})
	}

	// 竖线按 elapsed 的位置插入；穿过某段时把该段拆成前后两段，保证竖线仍落在时间进度上。
	segs := make([]quotaBarSegment, 0, len(filled)+1)
	inserted := false
	push := func(kind quotaBarSegmentKind, grow float64) {
		if grow <= 0 {
			return
		}
		segs = append(segs, quotaBarSegment{kind: kind, grow: float32(grow)})
	}
	for _, s := range filled {
		if !inserted && elapsed <= s.from {
			segs = append(segs, quotaBarSegment{kind: quotaBarSegmentMarker})
			inserted = true
		}
		if !inserted && elapsed < s.to {
			push(s.kind, elapsed-s.from)
			segs = append(segs, quotaBarSegment{kind: quotaBarSegmentMarker})
			inserted = true
			push(s.kind, s.to-elapsed)
			continue
		}
		push(s.kind, s.to-s.from)
	}
	if !inserted {
		segs = append(segs, quotaBarSegment{kind: quotaBarSegmentMarker})
	}
	return segs
}

// formatPaceRunOut 把预计用尽时刻格式化成相对时间文案（如「约 2 小时后」「约 30 分钟后」「约 2 时 30 分后」），
// 供额度行拼成「预计 X 用尽」；时刻为零或不在未来时返回空串。
func formatPaceRunOut(now, runOutAt time.Time) string {
	if runOutAt.IsZero() {
		return ""
	}
	diff := runOutAt.Sub(now)
	if diff <= 0 {
		return ""
	}
	if diff < time.Minute {
		return "1 分钟内"
	}
	if diff < time.Hour {
		return fmt.Sprintf("约 %d 分钟后", int(diff.Minutes()))
	}
	if diff < 24*time.Hour {
		hours := int(diff.Hours())
		minutes := int(diff.Minutes()) % 60
		if minutes > 0 {
			return fmt.Sprintf("约 %d 时 %d 分后", hours, minutes)
		}
		return fmt.Sprintf("约 %d 小时后", hours)
	}
	return fmt.Sprintf("约 %d 天后", int(diff.Hours()/24))
}

// paceRunOutText 生成额度行上的提前用尽提示文案（如「预计约 2 小时后用尽」）；
// 不是「将提前用尽」状态时返回空串。
func paceRunOutText(now, runOutAt time.Time, state paceState) string {
	if state != paceStateEarly {
		return ""
	}
	if rel := formatPaceRunOut(now, runOutAt); rel != "" {
		return "预计" + rel + "用尽"
	}
	return "预计将提前用尽"
}
