package app

import (
	"context"
	"reflect"
	"testing"
	"time"

	"mcode/internal/automation"
	"mcode/internal/frontdesk"
	"mcode/internal/store"
)

type frontdeskAutomationCreatorFake struct{ input automation.CreateInput }

func (f *frontdeskAutomationCreatorFake) Create(_ context.Context, in automation.CreateInput) (automation.Definition, error) {
	f.input = in
	next := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	return automation.Definition{Automation: store.Automation{ID: "a", Name: in.Name, NextRunAt: &next}}, nil
}

type frontdeskAutomationSettingsFake struct{}

func (frontdeskAutomationSettingsFake) GetSetting(_ context.Context, key string) (string, bool, error) {
	if key != "steward.feed_thread_id" {
		return "", false, nil
	}
	return "feed", true, nil
}

func TestFrontdeskAutomationCreate(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  automation.Schedule
	}{
		{"interval:3600", automation.Schedule{Type: "interval", Seconds: 3600}},
		{"daily:09:00", automation.Schedule{Type: "daily", TimeOfDay: "09:00"}},
		{"weekdays:09:00", automation.Schedule{Type: "weekdays", TimeOfDay: "09:00"}},
		{"weekly:1,09:00", automation.Schedule{Type: "weekly", DayOfWeek: 1, TimeOfDay: "09:00"}},
		{"cron:0 9 * * 1-5", automation.Schedule{Type: "cron", Expression: "0 9 * * 1-5"}},
	} {
		t.Run(tc.input, func(t *testing.T) {
			creator := &frontdeskAutomationCreatorFake{}
			service := frontdeskAutomation{service: creator, store: frontdeskAutomationSettingsFake{}}
			got, err := service.CreateAutomation(context.Background(), frontdesk.AutomationInput{ProjectID: "p", Name: "n", Prompt: "work", Schedule: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			in := creator.input
			if in.ProjectID != "p" || in.Name != "n" || in.Prompt != "work" || !in.Enabled || in.OriginThreadID != "" || in.CreatorThreadID != "feed" || in.Spec.RunMode != automation.RunModeStandalone || !reflect.DeepEqual(in.Spec.Schedule, tc.want) {
				t.Fatalf("创建参数错误: %+v", in)
			}
			if got.ID != "a" || got.Name != "n" || got.NextRunAt != "2026-01-01T09:00:00Z" {
				t.Fatalf("创建结果错误: %+v", got)
			}
		})
	}
	for _, raw := range []string{"", "interval:0", "daily:25:00", "weekly:7,09:00", "cron:invalid", "other:1"} {
		creator := &frontdeskAutomationCreatorFake{}
		service := frontdeskAutomation{service: creator, store: frontdeskAutomationSettingsFake{}}
		if _, err := service.CreateAutomation(context.Background(), frontdesk.AutomationInput{Schedule: raw}); err == nil {
			t.Errorf("应拒绝无效 schedule %q", raw)
		}
		if creator.input.Enabled {
			t.Errorf("无效 schedule %q 不应创建任务", raw)
		}
	}
}
