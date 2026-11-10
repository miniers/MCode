package automation

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

func TestPreviewSchedule(t *testing.T) {
	at := func(value string) time.Time {
		t.Helper()
		result, err := time.Parse(time.RFC3339, value)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	future := at("2026-10-16T12:00:00Z")
	past := at("2026-10-16T08:00:00Z")
	tests := []struct {
		name      string
		schedule  Schedule
		after     time.Time
		count     int
		want      []time.Time
		exhausted bool
	}{
		{
			name: "daily Shanghai", schedule: Schedule{Type: "daily", TimeOfDay: "09:00", Timezone: "Asia/Shanghai"},
			after: at("2026-10-16T00:00:00Z"), count: 3,
			want: []time.Time{at("2026-10-16T01:00:00Z"), at("2026-10-17T01:00:00Z"), at("2026-10-18T01:00:00Z")},
		},
		{
			name: "weekdays skip weekend", schedule: Schedule{Type: "weekdays", TimeOfDay: "09:00", Timezone: "UTC"},
			after: at("2026-10-16T08:00:00Z"), count: 3,
			want: []time.Time{at("2026-10-16T09:00:00Z"), at("2026-10-19T09:00:00Z"), at("2026-10-20T09:00:00Z")},
		},
		{
			name: "weekly Tuesday", schedule: Schedule{Type: "weekly", TimeOfDay: "10:00", Timezone: "UTC", DayOfWeek: 2},
			after: at("2026-10-16T08:00:00Z"), count: 3,
			want: []time.Time{at("2026-10-20T10:00:00Z"), at("2026-10-27T10:00:00Z"), at("2026-11-03T10:00:00Z")},
		},
		{
			name: "interval 90 seconds", schedule: Schedule{Type: "interval", Seconds: 90},
			after: at("2026-10-16T08:00:00Z"), count: 5,
			want: []time.Time{at("2026-10-16T08:01:30Z"), at("2026-10-16T08:03:00Z"), at("2026-10-16T08:04:30Z"), at("2026-10-16T08:06:00Z"), at("2026-10-16T08:07:30Z")},
		},
		{
			name: "once future", schedule: Schedule{Type: "once", Date: &future},
			after: at("2026-10-16T09:00:00Z"), count: 3, want: []time.Time{future}, exhausted: true,
		},
		{
			name: "once future count one", schedule: Schedule{Type: "once", Date: &future},
			after: at("2026-10-16T09:00:00Z"), count: 1, want: []time.Time{future}, exhausted: true,
		},
		{
			name: "once past", schedule: Schedule{Type: "once", Date: &past},
			after: at("2026-10-16T09:00:00Z"), count: 3, exhausted: true,
		},
		{
			name: "cron range and step", schedule: Schedule{Type: "cron", Expression: "*/15 9-10 * * 1-5", Timezone: "UTC"},
			after: at("2026-10-16T10:30:00Z"), count: 3,
			want: []time.Time{at("2026-10-16T10:45:00Z"), at("2026-10-19T09:00:00Z"), at("2026-10-19T09:15:00Z")},
		},
		{
			name: "cron leap day beyond search", schedule: Schedule{Type: "cron", Expression: "0 0 29 2 *", Timezone: "UTC"},
			after: at("2028-03-01T00:00:00Z"), count: 3, exhausted: true,
		},
		{
			name: "DST spring forward", schedule: Schedule{Type: "daily", TimeOfDay: "02:30", Timezone: "America/New_York"},
			after: at("2026-03-07T12:00:00Z"), count: 3,
			want: []time.Time{at("2026-03-08T07:30:00Z"), at("2026-03-09T06:30:00Z"), at("2026-03-10T06:30:00Z")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PreviewSchedule(context.Background(), tt.schedule, tt.after, tt.count)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Times, tt.want) || got.Exhausted != tt.exhausted {
				t.Fatalf("got %+v, want times %v exhausted %v", got, tt.want, tt.exhausted)
			}
			previous := tt.after
			for _, next := range got.Times {
				if !next.After(previous) || next.Location() != time.UTC {
					t.Errorf("candidate %v must be in UTC and strictly after %v", next, previous)
				}
				previous = next
			}
			again, err := PreviewSchedule(context.Background(), tt.schedule, tt.after, tt.count)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Errorf("repeat got %+v, %v; want %+v", again, err, got)
			}
		})
	}
}

func TestPreviewScheduleValidation(t *testing.T) {
	schedule := Schedule{Type: "interval", Seconds: 90}
	for _, count := range []int{0, 1, 3, 5, 6} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			got, err := PreviewSchedule(context.Background(), schedule, time.Time{}, count)
			if count < 1 || count > PreviewMaxCount {
				if err == nil {
					t.Fatal("expected count error")
				}
			} else if err != nil || len(got.Times) != count || got.Exhausted {
				t.Fatalf("got %d times, %v; want %d", len(got.Times), err, count)
			}
		})
	}

	invalid := Schedule{Type: "interval"}
	_, err := PreviewSchedule(context.Background(), invalid, time.Time{}, 0)
	if err == nil || err.Error() != invalid.Validate().Error() {
		t.Errorf("expected schedule validation error first, got %v", err)
	}
}

func TestPreviewScheduleNonIncreasing(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("interval overflow requires 64-bit int")
	}
	seconds := int64(1) << 62
	got, err := PreviewSchedule(context.Background(), Schedule{Type: "interval", Seconds: int(seconds)}, time.Time{}, 1)
	if err != nil || len(got.Times) != 0 || !got.Exhausted {
		t.Fatalf("got %+v, %v; want no candidates and exhausted", got, err)
	}
}

func TestPreviewScheduleCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := PreviewSchedule(ctx, Schedule{Type: "interval", Seconds: 90}, time.Time{}, 3)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}
