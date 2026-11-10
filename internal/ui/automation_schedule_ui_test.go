package mcodeui

import (
	"testing"
	"time"
)

func TestAutomationScheduleDraftParsing(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind string
		want AutomationSchedule
	}{
		{"daily", AutomationSchedule{Type: "daily", Timezone: "Asia/Shanghai", TimeOfDay: "09:30"}},
		{"weekdays", AutomationSchedule{Type: "weekdays", Timezone: "Asia/Shanghai", TimeOfDay: "09:30"}},
		{"weekly", AutomationSchedule{Type: "weekly", Timezone: "Asia/Shanghai", TimeOfDay: "09:30", DayOfWeek: 2}},
		{"interval", AutomationSchedule{Type: "interval", Timezone: "Asia/Shanghai", Seconds: 90}},
		{"once", AutomationSchedule{Type: "once", Timezone: "Asia/Shanghai", Date: time.Date(2030, 1, 2, 3, 4, 0, 0, loc)}},
		{"cron", AutomationSchedule{Type: "cron", Timezone: "Asia/Shanghai", Expression: "0 9 * * 1-5"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			as := &automationState{scheduleType: tc.kind, timezone: "Asia/Shanghai", intervalInput: "90", intervalUnit: "秒", timeOfDay: "09:30", dayOfWeek: 2, onceDateStr: "2030-01-02 03:04", cronExpr: "0 9 * * 1-5"}
			got, errs := automationScheduleFromDraft(as)
			if len(errs) != 0 || got.Type != tc.want.Type || got.Seconds != tc.want.Seconds || !got.Date.Equal(tc.want.Date) || got.TimeOfDay != tc.want.TimeOfDay || got.Timezone != tc.want.Timezone || got.DayOfWeek != tc.want.DayOfWeek || got.Expression != tc.want.Expression {
				t.Fatalf("got %+v, errors %v; want %+v", got, errs, tc.want)
			}
		})
	}
}

func TestAutomationScheduleDraftInvalid(t *testing.T) {
	for _, value := range []string{"0", "-1", "NaN", "Infinity", "abc"} {
		_, errs := automationScheduleFromDraft(&automationState{scheduleType: "interval", intervalInput: value})
		if errs["interval"] == "" {
			t.Errorf("interval %q accepted", value)
		}
	}
	for _, tc := range []struct {
		state automationState
		field string
	}{
		{automationState{scheduleType: "cron", cronExpr: "99 9 * * *"}, "cron"},
		{automationState{scheduleType: "once", onceDateStr: "invalid"}, "once"},
		{automationState{scheduleType: "daily", timeOfDay: "25:00"}, "time"},
		{automationState{scheduleType: "daily", timeOfDay: "09:00", timezone: "Invalid/Zone"}, "timezone"},
	} {
		_, errs := automationScheduleFromDraft(&tc.state)
		if errs[tc.field] == "" {
			t.Errorf("missing %s error: %v", tc.field, errs)
		}
	}
}

func TestAutomationScheduleDraftRoundTrip(t *testing.T) {
	for _, sch := range []AutomationSchedule{
		{Type: "interval", Seconds: 90},
		{Type: "once", Date: time.Date(2030, 1, 2, 3, 4, 37, 0, time.UTC)},
		{Type: "once", Timezone: "Asia/Shanghai", Date: time.Date(2030, 1, 2, 3, 4, 37, 0, time.UTC)},
	} {
		as := &automationState{}
		as.fillAutomation(Automation{Schedule: sch})
		got, errs := automationScheduleFromDraft(as)
		if len(errs) != 0 || got.Seconds != sch.Seconds || got.Timezone != sch.Timezone || !got.Date.Equal(sch.Date) {
			t.Fatalf("got %+v, errors %v; want %+v", got, errs, sch)
		}
	}
}

func TestAutomationScheduleDraftSwitchKeepsTimes(t *testing.T) {
	as := &automationState{scheduleType: "daily", timeOfDay: "08:00"}
	as.switchScheduleType("weekly")
	as.timeOfDay = "12:30"
	as.switchScheduleType("daily")
	if as.timeOfDay != "08:00" {
		t.Fatal(as.timeOfDay)
	}
	as.switchScheduleType("weekly")
	if as.timeOfDay != "12:30" {
		t.Fatal(as.timeOfDay)
	}
}
