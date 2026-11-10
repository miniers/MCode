package automation

import (
	"context"
	"fmt"
	"time"
)

// PreviewMaxCount 是一次预览最多返回的候选数。
const PreviewMaxCount = 5

// SchedulePreview 包含预览结果及搜索范围内是否还有候选。
type SchedulePreview struct {
	Times     []time.Time
	Exhausted bool
}

// PreviewSchedule 从 after 开始预览，间隔调度以 after 为上次运行时间。
// 时间沿用 Schedule 的时区计算，并与 Schedule.Next 一样返回 UTC。
func PreviewSchedule(ctx context.Context, s Schedule, after time.Time, count int) (SchedulePreview, error) {
	if err := s.Validate(); err != nil {
		return SchedulePreview{}, err
	}
	if count < 1 || count > PreviewMaxCount {
		return SchedulePreview{}, fmt.Errorf("preview count must be between 1 and %d, got %d", PreviewMaxCount, count)
	}

	var result SchedulePreview
	cursor := after
	for len(result.Times) <= count {
		if err := ctx.Err(); err != nil {
			return SchedulePreview{}, err
		}
		next, ok := s.Next(cursor)
		if !ok || !next.After(cursor) {
			result.Exhausted = true
			break
		}
		if len(result.Times) == count {
			break
		}
		result.Times = append(result.Times, next)
		cursor = next
	}
	return result, nil
}
