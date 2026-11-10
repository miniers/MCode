package mcodeui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"mcode/internal/automation"
)

func automationScheduleFromDraft(as *automationState) (AutomationSchedule, map[string]string) {
	sch := AutomationSchedule{Type: as.scheduleType, Timezone: strings.TrimSpace(as.timezone)}
	if sch.Type == "" {
		sch.Type = "interval"
	}
	errs := make(map[string]string)
	loc := time.Local
	if sch.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(sch.Timezone)
		if err != nil {
			errs["timezone"] = "请输入有效的时区"
			loc = time.Local
		}
	}
	switch sch.Type {
	case "interval":
		value, err := strconv.ParseFloat(strings.TrimSpace(as.intervalInput), 64)
		factor := 60.0
		switch as.intervalUnit {
		case "秒":
			factor = 1
		case "小时":
			factor = 3600
		}
		seconds := value * factor
		if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 1 || seconds >= float64(math.MaxInt64/int64(time.Second)) {
			errs["interval"] = "运行间隔必须大于 0"
		} else {
			sch.Seconds = int(math.Round(seconds))
		}
	case "daily", "weekdays", "weekly":
		sch.TimeOfDay = strings.TrimSpace(as.timeOfDay)
		if _, err := time.Parse("15:04", sch.TimeOfDay); err != nil {
			errs["time"] = "请输入有效时间"
		}
		if sch.Type == "weekly" {
			sch.DayOfWeek = as.dayOfWeek
			if sch.DayOfWeek < 0 || sch.DayOfWeek > 6 {
				errs["day"] = "请选择星期"
			}
		}
	case "cron":
		sch.Expression = strings.TrimSpace(as.cronExpr)
		if err := (automation.Schedule{Type: "cron", Expression: sch.Expression}).Validate(); err != nil {
			errs["cron"] = "请输入有效的 Cron 表达式"
		}
	case "once":
		text := strings.TrimSpace(as.onceDateStr)
		parsed, err := time.ParseInLocation("2006-01-02 15:04", text, loc)
		if err != nil {
			parsed, err = time.ParseInLocation("2006-01-02 15:04:05", text, loc)
		}
		if err != nil {
			errs["once"] = "请输入有效的日期和时间"
		} else {
			sch.Date = parsed
			if as.schedule.Type == "once" && !as.schedule.Date.IsZero() && sch.Timezone == as.schedule.Timezone && text == as.schedule.Date.In(loc).Format("2006-01-02 15:04") {
				sch.Date = as.schedule.Date
			}
		}
	default:
		errs["type"] = "请选择有效的调度类型"
	}
	return sch, errs
}

func (as *automationState) cancelSchedulePreview() {
	if as.previewTimer != nil {
		as.previewTimer.Stop()
		as.previewTimer = nil
	}
	if as.previewCancel != nil {
		as.previewCancel()
		as.previewCancel = nil
	}
	as.previewID++
	as.previewValid = false
	as.previewVisible = false
}

func (as *automationState) stopSchedulePreview() {
	as.previewEpoch++
	as.cancelSchedulePreview()
}

func (as *automationState) updateSchedulePreview(s *chatState) {
	client, supported := s.client.(automationPreviewClient)
	sch, errs := automationScheduleFromDraft(as)
	valid := supported && len(errs) == 0
	if as.previewValid == valid && (!valid || automationSchedulesEqual(as.previewSchedule, sch)) {
		return
	}
	as.cancelSchedulePreview()
	id := as.previewID
	epoch := as.previewEpoch
	as.previewValid, as.previewSchedule, as.previewVisible = valid, sch, false
	if !valid {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	as.previewCancel = cancel
	as.previewTimer = time.AfterFunc(200*time.Millisecond, func() {
		if ctx.Err() != nil {
			return
		}
		result, err := client.PreviewAutomationSchedule(ctx, sch, time.Now(), 3)
		automationMu.Lock()
		defer automationMu.Unlock()
		if as.previewEpoch != epoch {
			return
		}
		s.updateWorkspace(func() {
			automationMu.Lock()
			defer automationMu.Unlock()
			current, errs := automationScheduleFromDraft(as)
			if as.previewID != id || ctx.Err() != nil || len(errs) != 0 || !automationSchedulesEqual(current, sch) {
				return
			}
			as.preview = result
			as.previewVisible = err == nil && (len(result.Times) > 0 || result.Exhausted)
			s.invalidate()
		})
	})
}

func automationSchedulesEqual(a, b AutomationSchedule) bool {
	return a.Type == b.Type && a.Seconds == b.Seconds && a.Date.Equal(b.Date) && a.TimeOfDay == b.TimeOfDay && a.Timezone == b.Timezone && a.DayOfWeek == b.DayOfWeek && a.Expression == b.Expression
}

func automationShortTime(t, now time.Time) string {
	y, m, d := t.Date()
	ny, nm, nd := now.Date()
	if y == ny && m == nm && d == nd {
		return "今天 " + t.Format("15:04")
	}
	ty, tm, td := now.AddDate(0, 0, 1).Date()
	if y == ty && m == tm && d == td {
		return "明天 " + t.Format("15:04")
	}
	days := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
	return fmt.Sprintf("%d月%d日 %s %s", m, d, days[t.Weekday()], t.Format("15:04"))
}

func automationTimeTypeIndex(kind string) int {
	switch kind {
	case "daily":
		return 0
	case "weekdays":
		return 1
	case "weekly":
		return 2
	}
	return -1
}

func (as *automationState) switchScheduleType(kind string) {
	if i := automationTimeTypeIndex(as.scheduleType); i >= 0 {
		as.typeTimes[i] = as.timeOfDay
	}
	as.scheduleType = kind
	if i := automationTimeTypeIndex(kind); i >= 0 {
		as.timeOfDay = as.typeTimes[i]
		if as.timeOfDay == "" {
			as.timeOfDay = "09:00"
		}
	}
}

// automationScheduleText 生成调度描述，如“每 60 分钟”“每天 09:00”“每周一 09:00”。
func automationScheduleText(sch AutomationSchedule) string {
	clock := sch.TimeOfDay
	if clock == "" {
		clock = "09:00"
	}
	weeklyDay := "每周"
	for _, d := range dayOfWeekOptions {
		if d.day == sch.DayOfWeek {
			weeklyDay = d.label
			break
		}
	}
	switch sch.Type {
	case "interval":
		seconds := sch.Seconds
		if seconds <= 0 {
			seconds = 3600
		}
		if seconds%60 != 0 {
			return fmt.Sprintf("每 %d 秒", seconds)
		}
		minutes := seconds / 60
		if minutes >= 60 && minutes%60 == 0 {
			return fmt.Sprintf("每 %d 小时", minutes/60)
		}
		return fmt.Sprintf("每 %d 分钟", minutes)
	case "daily":
		return "每天 " + clock
	case "weekdays":
		return "工作日 " + clock
	case "weekly":
		return weeklyDay + " " + clock
	case "cron":
		if sch.Expression == "" {
			return "Cron 调度"
		}
		return "Cron " + sch.Expression
	case "once":
		if sch.Date.IsZero() {
			return "单次运行"
		}
		return "单次 " + sch.Date.Local().Format("2006-01-02 15:04")
	case "":
		return "未设置调度"
	}
	return sch.Type
}

// 调度类型定义
var scheduleTypeOptions = []struct {
	key   string
	label string
}{
	{key: "daily", label: "每天"},
	{key: "weekdays", label: "工作日"},
	{key: "weekly", label: "每周"},
	{key: "interval", label: "间隔"},
	{key: "once", label: "单次"},
	{key: "cron", label: "高级 Cron"},
}

// 星期几选项（周日开始）
var dayOfWeekOptions = []struct {
	day   int
	label string
}{
	{day: 0, label: "周日"},
	{day: 1, label: "周一"},
	{day: 2, label: "周二"},
	{day: 3, label: "周三"},
	{day: 4, label: "周四"},
	{day: 5, label: "周五"},
	{day: 6, label: "周六"},
}

// 间隔分钟档位：固定八档
var intervalMinuteOptions = []int{5, 15, 30, 60, 120, 240, 720, 1440}

// formatIntervalMinutesText 把秒数转成间隔选择器的分钟文本，非预设值（如 90 秒即 1.5 分钟）保持原样。
func formatIntervalMinutesText(seconds int) string {
	if seconds <= 0 {
		return "60"
	}
	return strconv.FormatFloat(float64(seconds)/60, 'f', -1, 64)
}

func renderAutomationScheduleFields(c *ui.Context, s *chatState, as *automationState) {
	// 调度类型选择
	ui.Row(c.Key("form-sched-type-group")).
		FillWidth().
		Wrap().
		Gap(Space2).
		Children(func() {
			typeLabels := make([]string, len(scheduleTypeOptions))
			curType := "间隔"
			for i, opt := range scheduleTypeOptions {
				typeLabels[i] = opt.label
				if opt.key == as.scheduleType {
					curType = opt.label
				}
			}
			if ui.Select(c.Key("automation-sched-type-select"), &curType, typeLabels).Label("调度类型").Width(150).Changed() {
				for _, opt := range scheduleTypeOptions {
					if opt.label == curType {
						as.switchScheduleType(opt.key)
						break
					}
				}
				as.formErr = ""
				s.invalidate()
			}
			ui.Column(c.Key("form-sched-params")).
				Children(func() {
					switch as.scheduleType {
					case "interval":
						ui.Column(c.Key("sched-interval-group")).
							Gap(Space1).
							Children(func() {
								ui.Row(c.Key("automation-interval-row")).Wrap().Gap(Space2).Children(func() {
									ui.TextInput(c.Key("automation-interval-input"), &as.intervalInput).Label("运行间隔").Width(110)
									if as.intervalUnit == "" {
										as.intervalUnit = "分钟"
									}
									ui.Select(c.Key("automation-interval-unit"), &as.intervalUnit, []string{"秒", "分钟", "小时"}).Label("间隔单位").Width(100)
								})
								if msg := as.scheduleErrors["interval"]; msg != "" {
									ui.Text(c, msg).TextColor(ThemeDanger(c)).FontSize(TypeFootnote)
								}
							})

					case "daily", "weekdays":
						ui.Column(c.Key("sched-time-group")).
							Width(150).
							Gap(Space1).
							Children(func() {
								ui.TextInput(c.Key("automation-time-input"), &as.timeOfDay).
									Placeholder("如 09:30").
									Label("automation-time-input").
									FillWidth()
								if msg := as.scheduleErrors["time"]; msg != "" {
									ui.Text(c, msg).TextColor(ThemeDanger(c)).FontSize(TypeFootnote)
								}
							})

					case "weekly":
						ui.Row(c.Key("sched-weekly-group")).
							Wrap().
							Gap(Space2).
							Children(func() {
								ui.Column(c.Key("sched-weekly-day-group")).
									Gap(Space1).
									Children(func() {
										labels := make([]string, len(dayOfWeekOptions))
										day := "周一"
										for i, opt := range dayOfWeekOptions {
											labels[i] = opt.label
											if opt.day == as.dayOfWeek {
												day = opt.label
											}
										}
										if ui.Select(c.Key("automation-sched-day"), &day, labels).Label("星期几").Width(110).Changed() {
											for _, opt := range dayOfWeekOptions {
												if opt.label == day {
													as.dayOfWeek = opt.day
													break
												}
											}
										}
									})

								ui.Column(c.Key("sched-weekly-time-group")).
									Width(120).
									Gap(Space1).
									Children(func() {
										ui.TextInput(c.Key("automation-time-input"), &as.timeOfDay).
											Placeholder("如 10:00").
											Label("automation-time-input").
											FillWidth()
										if msg := as.scheduleErrors["time"]; msg != "" {
											ui.Text(c, msg).TextColor(ThemeDanger(c)).FontSize(TypeFootnote)
										}
									})
							})

					case "cron":
						ui.Column(c.Key("sched-cron-group")).
							Width(210).
							Gap(Space1).
							Children(func() {
								ui.TextInput(c.Key("automation-cron-input"), &as.cronExpr).
									Placeholder("如 */15 * * * *").
									Label("automation-cron-input").
									FillWidth()
								if msg := as.scheduleErrors["cron"]; msg != "" {
									ui.Text(c, msg).TextColor(ThemeDanger(c)).FontSize(TypeFootnote)
								}
							})

					case "once":
						ui.Column(c.Key("sched-once-group")).
							Width(200).
							Gap(Space1).
							Children(func() {
								ui.TextInput(c.Key("automation-once-input"), &as.onceDateStr).
									Placeholder("如 2026-10-20 14:00").
									Label("automation-once-input").
									FillWidth()
								if msg := as.scheduleErrors["once"]; msg != "" {
									ui.Text(c, msg).TextColor(ThemeDanger(c)).FontSize(TypeFootnote)
								}
							})
					}
				})
		})
	ui.Row(c.Key("automation-timezone-row")).FillWidth().Gap(Space2).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "时区").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
		ui.TextInput(c.Key("automation-timezone-input"), &as.timezone).Label("时区").Placeholder("时区（IANA）").Grow(1)
	})
	if msg := as.scheduleErrors["timezone"]; msg != "" {
		ui.Text(c, msg).TextColor(ThemeDanger(c)).FontSize(TypeFootnote)
	}
	as.updateSchedulePreview(s)
	if as.previewVisible {
		ui.Column(c.Key("automation-preview")).Gap(Space1).Transition(motionTransition(c, ui.ElementTransition{Duration: DurBase, Enter: &ui.Motion{Opacity: 0}})).Children(func() {
			ui.Text(c, "预计运行").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			loc := time.Local
			if as.timezone != "" {
				if zone, err := time.LoadLocation(as.timezone); err == nil {
					loc = zone
				}
			}
			for i, t := range as.preview.Times {
				if i >= 3 || (as.scheduleType == "once" && i >= 1) {
					break
				}
				ui.Text(c, automationShortTime(t.In(loc), time.Now().In(loc))).FontSize(TypeFootnote).TextColor(ThemePrimaryText(c))
			}
			if len(as.preview.Times) == 0 && as.preview.Exhausted {
				ui.Text(c, "没有即将到来的运行").FontSize(TypeFootnote).TextColor(ThemeSecondaryText(c))
			}
		})
	}

}
