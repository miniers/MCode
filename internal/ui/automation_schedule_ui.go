package mcodeui

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

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
	{key: "interval", label: "每隔N分钟"},
	{key: "daily", label: "每天固定时间"},
	{key: "weekdays", label: "工作日固定时间"},
	{key: "weekly", label: "每周指定日"},
	{key: "cron", label: "Cron表达式"},
	{key: "once", label: "单次定时运行"},
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
	ui.Column(c.Key("form-sched-type-group")).
		FillWidth().
		Gap(Space2).
		Children(func() {
			ui.Text(c, "调度类型").
				FontSize(TypeFootnote).
				FontWeight(500).
				TextColor(ThemeSecondaryText(c))

			// 调度类型用分段控件，节省纵向空间
			typeSegments := make([]Segment, len(scheduleTypeOptions))
			curTypeIdx := 0
			for i, opt := range scheduleTypeOptions {
				typeSegments[i] = Segment{Label: opt.label}
				if opt.key == as.scheduleType {
					curTypeIdx = i
				}
			}
			Segmented(c, "automation-sched-type-seg", &curTypeIdx, typeSegments, func(idx int) {
				if idx < 0 || idx >= len(scheduleTypeOptions) {
					return
				}
				automationMu.Lock()
				as.scheduleType = scheduleTypeOptions[idx].key
				as.formErr = ""
				automationMu.Unlock()
				s.invalidate()
			}).Label("调度类型")
		})

	// 调度参数组：外套浅底容器
	ui.Column(c.Key("form-sched-params")).
		FillWidth().
		Padding(Space3, Space3).
		Radius(RadiusMD).
		Background(ThemeOverlay(c, 0.05)).
		Children(func() {
			switch as.scheduleType {
			case "interval":
				ui.Column(c.Key("sched-interval-group")).
					FillWidth().
					Gap(Space1).
					Children(func() {
						ui.Text(c, "运行间隔").
							FontSize(TypeFootnote).
							FontWeight(500).
							TextColor(ThemeSecondaryText(c))
						// 固定八档间隔；原始值不在八档内（如 1.5 分钟）时作为额外选项保留，
						// 避免打开表单就把日程吸附成预设值
						curInterval := strings.TrimSpace(as.intervalInput)
						if v, err := strconv.ParseFloat(curInterval, 64); err != nil ||
							math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
							curInterval = "60"
						}
						intervalLabels := make([]string, 0, len(intervalMinuteOptions)+1)
						intervalValues := make([]string, 0, len(intervalMinuteOptions)+1)
						for _, m := range intervalMinuteOptions {
							intervalLabels = append(intervalLabels, fmt.Sprintf("%d 分钟", m))
							intervalValues = append(intervalValues, strconv.Itoa(m))
						}
						curIntervalLabel := ""
						for i, v := range intervalValues {
							if v == curInterval {
								curIntervalLabel = intervalLabels[i]
								break
							}
						}
						if curIntervalLabel == "" {
							curIntervalLabel = curInterval + " 分钟"
							intervalLabels = append(intervalLabels, curIntervalLabel)
							intervalValues = append(intervalValues, curInterval)
						}
						intervalSel := ui.Select(c.Key("automation-interval-select"), &curIntervalLabel, intervalLabels).
							Label("运行间隔").
							Width(140)
						if intervalSel.Changed() {
							minutes := "60"
							for i, lbl := range intervalLabels {
								if lbl == curIntervalLabel {
									minutes = intervalValues[i]
									break
								}
							}
							automationMu.Lock()
							as.intervalInput = minutes
							as.formErr = ""
							automationMu.Unlock()
							s.invalidate()
						}
					})

			case "daily", "weekdays":
				ui.Column(c.Key("sched-time-group")).
					FillWidth().
					Gap(Space1).
					Children(func() {
						ui.Text(c, "运行时间 (HH:MM)").
							FontSize(TypeFootnote).
							FontWeight(500).
							TextColor(ThemeSecondaryText(c))
						ui.TextInput(c.Key("automation-time-input"), &as.timeOfDay).
							Placeholder("如 09:30").
							Label("automation-time-input").
							FillWidth()
					})

			case "weekly":
				ui.Row(c.Key("sched-weekly-group")).
					FillWidth().
					Gap(Space4).
					Children(func() {
						ui.Column(c.Key("sched-weekly-day-group")).
							Grow(1).
							Gap(Space1).
							Children(func() {
								ui.Text(c, "星期几").
									FontSize(TypeFootnote).
									FontWeight(500).
									TextColor(ThemeSecondaryText(c))
								ui.Row(c.Key("sched-day-options")).
									Gap(Space1).
									Children(func() {
										daySegments := make([]Segment, len(dayOfWeekOptions))
										curDayIdx := 0
										for i, d := range dayOfWeekOptions {
											daySegments[i] = Segment{Label: d.label}
											if as.dayOfWeek == d.day {
												curDayIdx = i
											}
										}
										Segmented(c, "automation-sched-day", &curDayIdx, daySegments, func(idx int) {
											if idx < 0 || idx >= len(dayOfWeekOptions) {
												return
											}
											automationMu.Lock()
											as.dayOfWeek = dayOfWeekOptions[idx].day
											automationMu.Unlock()
											s.invalidate()
										}).Label("星期几")
									})
							})

						ui.Column(c.Key("sched-weekly-time-group")).
							Width(120).
							Gap(Space1).
							Children(func() {
								ui.Text(c, "时间 (HH:MM)").
									FontSize(TypeFootnote).
									FontWeight(500).
									TextColor(ThemeSecondaryText(c))
								ui.TextInput(c.Key("automation-time-input"), &as.timeOfDay).
									Placeholder("如 10:00").
									Label("automation-time-input").
									FillWidth()
							})
					})

			case "cron":
				ui.Column(c.Key("sched-cron-group")).
					FillWidth().
					Gap(Space1).
					Children(func() {
						ui.Text(c, "Cron 表达式（支持 */N * * * * 分钟间隔）").
							FontSize(TypeFootnote).
							FontWeight(500).
							TextColor(ThemeSecondaryText(c))
						ui.TextInput(c.Key("automation-cron-input"), &as.cronExpr).
							Placeholder("如 */15 * * * *").
							Label("automation-cron-input").
							FillWidth()
					})

			case "once":
				ui.Column(c.Key("sched-once-group")).
					FillWidth().
					Gap(Space1).
					Children(func() {
						ui.Text(c, "执行时间 (2006-01-02 15:04)").
							FontSize(TypeFootnote).
							FontWeight(500).
							TextColor(ThemeSecondaryText(c))
						ui.TextInput(c.Key("automation-once-input"), &as.onceDateStr).
							Placeholder("如 2026-10-20 14:00").
							Label("automation-once-input").
							FillWidth()
					})
			}
		})

}
