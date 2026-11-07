package automation

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule 定义自动化任务的调度配置。
type Schedule struct {
	Type       string     `json:"type"`
	Seconds    int        `json:"seconds,omitempty"`
	Date       *time.Time `json:"date,omitempty"`
	TimeOfDay  string     `json:"timeOfDay,omitempty"`
	Timezone   string     `json:"timezone,omitempty"`
	DayOfWeek  int        `json:"dayOfWeek,omitempty"`
	Expression string     `json:"expression,omitempty"`
}

// Validate 校验调度配置合法性。
func (s Schedule) Validate() error {
	switch s.Type {
	case "interval":
		if s.Seconds <= 0 {
			return fmt.Errorf("interval seconds must be greater than 0, got %d", s.Seconds)
		}
	case "once":
		if s.Date == nil {
			return errors.New("once schedule requires date")
		}
	case "daily":
		if err := validateTimeOfDay(s.TimeOfDay); err != nil {
			return err
		}
		if err := validateTimezone(s.Timezone); err != nil {
			return err
		}
	case "weekdays":
		if err := validateTimeOfDay(s.TimeOfDay); err != nil {
			return err
		}
		if err := validateTimezone(s.Timezone); err != nil {
			return err
		}
	case "weekly":
		if err := validateTimeOfDay(s.TimeOfDay); err != nil {
			return err
		}
		if err := validateTimezone(s.Timezone); err != nil {
			return err
		}
		if s.DayOfWeek < 0 || s.DayOfWeek > 6 {
			return fmt.Errorf("dayOfWeek must be between 0 and 6, got %d", s.DayOfWeek)
		}
	case "cron":
		if strings.TrimSpace(s.Expression) == "" {
			return errors.New("cron expression cannot be empty")
		}
		if err := validateTimezone(s.Timezone); err != nil {
			return err
		}
		if _, err := parseCron(s.Expression); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown schedule type: %q", s.Type)
	}
	return nil
}

func validateTimeOfDay(tod string) error {
	parts := strings.Split(tod, ":")
	if len(parts) != 2 {
		return fmt.Errorf("invalid timeOfDay format %q, expected HH:MM", tod)
	}
	hour, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || min < 0 || min > 59 {
		return fmt.Errorf("invalid timeOfDay %q, hour must be 0-23 and minute 0-59", tod)
	}
	return nil
}

func validateTimezone(tz string) error {
	if tz == "" {
		return nil
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return fmt.Errorf("invalid timezone %q: %w", tz, err)
	}
	return nil
}

// cronSpec 是解析后的标准五段 cron 表达式（分 时 日 月 周）。
// 每个字段以允许值集合表示；domStar/dowStar 记录该段字面量是否为 "*"，
// 用于实现“日与周任一匹配即可”的标准语义。
type cronSpec struct {
	minute  [60]bool
	hour    [24]bool
	dom     [32]bool // 下标即日期 1-31
	month   [13]bool // 下标即月份 1-12
	dow     [7]bool  // 下标即星期 0-6，周日为 0
	domStar bool
	dowStar bool
}

// parseCron 解析标准五段 cron 表达式。
// 支持语法："*"、单值 a、范围 a-b、列表 a,b,c、步长 */n 与 a-b/n；
// 取值范围：分 0-59、时 0-23、日 1-31、月 1-12、周 0-7（7 等同于周日 0）。
func parseCron(expr string) (cronSpec, error) {
	var spec cronSpec
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return spec, fmt.Errorf("cron 表达式需要 5 个字段（分 时 日 月 周），实际得到 %d 个: %q", len(fields), expr)
	}

	minute, _, err := parseCronField(fields[0], "分", 0, 59)
	if err != nil {
		return spec, err
	}
	hour, _, err := parseCronField(fields[1], "时", 0, 23)
	if err != nil {
		return spec, err
	}
	dom, domStar, err := parseCronField(fields[2], "日", 1, 31)
	if err != nil {
		return spec, err
	}
	month, _, err := parseCronField(fields[3], "月", 1, 12)
	if err != nil {
		return spec, err
	}
	dow, dowStar, err := parseCronField(fields[4], "周", 0, 7)
	if err != nil {
		return spec, err
	}

	for _, v := range minute {
		spec.minute[v] = true
	}
	for _, v := range hour {
		spec.hour[v] = true
	}
	for _, v := range dom {
		spec.dom[v] = true
	}
	for _, v := range month {
		spec.month[v] = true
	}
	for _, v := range dow {
		if v == 7 {
			v = 0 // 7 与 0 都表示周日
		}
		spec.dow[v] = true
	}
	spec.domStar = domStar
	spec.dowStar = dowStar
	return spec, nil
}

// parseCronField 解析单个 cron 字段，返回允许值列表以及该段字面量是否为 "*"。
func parseCronField(seg, name string, minValue, maxValue int) ([]int, bool, error) {
	if seg == "" {
		return nil, false, fmt.Errorf("cron %s 字段为空", name)
	}
	star := seg == "*"
	seen := make(map[int]bool)
	for _, part := range strings.Split(seg, ",") {
		lo, hi, step, err := parseCronRange(part, name, minValue, maxValue)
		if err != nil {
			return nil, false, err
		}
		for v := lo; v <= hi; v += step {
			seen[v] = true
		}
	}
	values := make([]int, 0, len(seen))
	for v := range seen {
		values = append(values, v)
	}
	return values, star, nil
}

// parseCronRange 解析单个列表项，例如 "a"、"a-b"、"*/n"、"a-b/n"。
func parseCronRange(part, name string, minValue, maxValue int) (int, int, int, error) {
	if part == "" {
		return 0, 0, 0, fmt.Errorf("cron %s 字段含空的列表项", name)
	}

	step := 1
	hasStep := false
	base := part
	if idx := strings.Index(part, "/"); idx >= 0 {
		base = part[:idx]
		rawStep := part[idx+1:]
		parsed, err := strconv.Atoi(rawStep)
		if err != nil || parsed <= 0 {
			return 0, 0, 0, fmt.Errorf("cron %s 字段 %q 的步长 %q 必须是正整数", name, part, rawStep)
		}
		step = parsed
		hasStep = true
	}

	lo, hi := minValue, maxValue
	switch {
	case base == "*":
		// 全范围，lo/hi 保持字段边界
	case strings.Contains(base, "-"):
		bounds := strings.SplitN(base, "-", 2)
		var err error
		if lo, err = parseCronValue(bounds[0], part, name, minValue, maxValue); err != nil {
			return 0, 0, 0, err
		}
		if hi, err = parseCronValue(bounds[1], part, name, minValue, maxValue); err != nil {
			return 0, 0, 0, err
		}
		if lo > hi {
			return 0, 0, 0, fmt.Errorf("cron %s 字段 %q 的范围起点大于终点", name, part)
		}
	default:
		var err error
		if lo, err = parseCronValue(base, part, name, minValue, maxValue); err != nil {
			return 0, 0, 0, err
		}
		hi = lo
		if hasStep {
			// "a/n" 表示从 a 到字段上界按 n 步进
			hi = maxValue
		}
	}
	return lo, hi, step, nil
}

// parseCronValue 解析单个数值并做范围校验。
func parseCronValue(raw, part, name string, minValue, maxValue int) (int, error) {
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("cron %s 字段 %q 含无法识别的值 %q", name, part, raw)
	}
	if v < minValue || v > maxValue {
		return 0, fmt.Errorf("cron %s 字段 %q 的值 %d 超出范围 %d-%d", name, part, v, minValue, maxValue)
	}
	return v, nil
}

// dayMatches 按标准 cron 语义判断日期是否匹配：日与周两段都受限时任一匹配即可，
// 只有一段受限时只看这一段。
func (c cronSpec) dayMatches(t time.Time) bool {
	domOK := c.dom[t.Day()]
	dowOK := c.dow[int(t.Weekday())]
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowOK
	case c.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// Next 计算在基准时间 after 之后的下一次触发时间，所有返回时间统一转为 UTC。
func (s Schedule) Next(after time.Time) (time.Time, bool) {
	switch s.Type {
	case "interval":
		if s.Seconds <= 0 {
			return time.Time{}, false
		}
		return after.Add(time.Duration(s.Seconds) * time.Second).UTC(), true

	case "once":
		if s.Date == nil || !s.Date.After(after) {
			return time.Time{}, false
		}
		return s.Date.UTC(), true

	case "daily":
		return s.nextDate(after, func(time.Weekday) bool { return true })

	case "weekdays":
		return s.nextDate(after, func(w time.Weekday) bool {
			return w >= time.Monday && w <= time.Friday
		})

	case "weekly":
		return s.nextDate(after, func(w time.Weekday) bool {
			return int(w) == s.DayOfWeek
		})

	case "cron":
		return s.nextCron(after)

	default:
		return time.Time{}, false
	}
}

// nextCron 在 cron 表达式基础上逐分钟向后查找下一次触发时间，最多查找 366 天。
func (s Schedule) nextCron(after time.Time) (time.Time, bool) {
	spec, err := parseCron(s.Expression)
	if err != nil {
		return time.Time{}, false
	}

	loc := time.Local
	if s.Timezone != "" {
		loc, err = time.LoadLocation(s.Timezone)
		if err != nil {
			return time.Time{}, false
		}
	}

	// 截断到整分钟再加一分钟，保证结果严格晚于 after。
	start := after.In(loc)
	cursor := time.Date(start.Year(), start.Month(), start.Day(), start.Hour(), start.Minute(), 0, 0, loc).Add(time.Minute)
	limit := cursor.AddDate(0, 0, 366)
	for cursor.Before(limit) {
		switch {
		case !spec.month[int(cursor.Month())]:
			// 月不匹配，直接跳到下月 1 日 0 点。
			cursor = time.Date(cursor.Year(), cursor.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)
		case !spec.dayMatches(cursor):
			// 日不匹配，跳到次日 0 点。
			cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
		case !spec.hour[cursor.Hour()]:
			// 时不匹配，跳到下一小时整点。
			cursor = time.Date(cursor.Year(), cursor.Month(), cursor.Day(), cursor.Hour(), 0, 0, 0, loc).Add(time.Hour)
		case !spec.minute[cursor.Minute()]:
			cursor = cursor.Add(time.Minute)
		default:
			return cursor.UTC(), true
		}
	}
	return time.Time{}, false
}

func (s Schedule) nextDate(baseline time.Time, dayFilter func(time.Weekday) bool) (time.Time, bool) {
	parts := strings.Split(s.TimeOfDay, ":")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	hour, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || hour < 0 || hour > 23 || min < 0 || min > 59 {
		return time.Time{}, false
	}

	loc := time.Local
	if s.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(s.Timezone)
		if err != nil {
			return time.Time{}, false
		}
	}

	baselineInLoc := baseline.In(loc)
	for dayOffset := 0; dayOffset < 14; dayOffset++ {
		candidateDay := baselineInLoc.AddDate(0, 0, dayOffset)
		candidate := time.Date(candidateDay.Year(), candidateDay.Month(), candidateDay.Day(), hour, min, 0, 0, loc)
		// DST 春季跳变时不存在的时刻，time.Date 可能回落成更早的时刻；
		// 此时顺延到跳变之后最近的有效时刻。
		for i := 0; i < 3 && (candidate.Hour() < hour || (candidate.Hour() == hour && candidate.Minute() < min)); i++ {
			candidate = candidate.Add(time.Hour)
		}
		if candidate.After(baseline) {
			if dayFilter(candidate.Weekday()) {
				return candidate.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

// onceMissedGrace 是 once 调度到期后仍按正常触发处理的宽限期；
// 超过该宽限期仍未触发，说明休眠或进程停摆错过了唯一触发点。
const onceMissedGrace = time.Minute

// missedOccurrenceLimit 是补跑统计最多枚举的触发点数量（含 due 本身），
// 以及最多回溯的时间跨度；超过任一上限都按截断处理，避免补跑数无界增长。
const (
	missedOccurrenceLimit = 1001
	missedLookbackLimit   = 366 * 24 * time.Hour
)

// MissedOccurrences 统计 due 到 now 之间（含 due 本身）已经到期的触发点数量与最后一个触发点。
// count 含 due：只有一个触发点时为 1；now 早于 due 时返回 0。truncated 表示枚举被上限截断，
// 此时 count 固定为 missedOccurrenceLimit，调用方据此把补跑数限制在 1000。
func MissedOccurrences(s Schedule, due, now time.Time) (count int, last time.Time, truncated bool) {
	due = due.UTC()
	now = now.UTC()
	if now.Before(due) {
		return 0, time.Time{}, false
	}

	if s.Type == "interval" {
		if s.Seconds <= 0 {
			return 0, time.Time{}, false
		}
		period := time.Duration(s.Seconds) * time.Second
		count = int(now.Sub(due)/period) + 1
		if count > missedOccurrenceLimit {
			return missedOccurrenceLimit, due.Add(time.Duration(missedOccurrenceLimit-1) * period), true
		}
		return count, due.Add(time.Duration(count-1) * period), false
	}

	count = 1
	last = due
	cursor := due
	for count < missedOccurrenceLimit {
		next, ok := s.Next(cursor)
		if !ok || next.After(now) {
			break
		}
		if next.Sub(due) > missedLookbackLimit {
			return missedOccurrenceLimit, last, true
		}
		count++
		last = next
		cursor = next
	}
	if count >= missedOccurrenceLimit {
		// 可能恰好枚举完最后一个点；只有仍存在已到期触发点时才标记截断。
		if next, ok := s.Next(cursor); ok && !next.After(now) {
			return missedOccurrenceLimit, last, true
		}
	}
	return count, last, false
}

// missedForSpec 把调度层统计换算成需要补跑的触发点数量。
// once 在首次运行尚未发生时只代表 due 这一个触发点：超过宽限期即视为错过，
// 宽限期内仍按正常触发处理；其余调度补跑除 due 之外的错过点。
func missedForSpec(spec Spec, due, now time.Time) (missed int, last time.Time, truncated bool) {
	count, last, truncated := MissedOccurrences(spec.Schedule, due, now)
	if spec.Schedule.Type == "once" && spec.State.TotalRunCount == 0 {
		if now.UTC().Sub(due.UTC()) > onceMissedGrace {
			return 1, due.UTC(), truncated
		}
		return 0, last, truncated
	}
	if count <= 1 {
		return 0, last, truncated
	}
	return count - 1, last, truncated
}
