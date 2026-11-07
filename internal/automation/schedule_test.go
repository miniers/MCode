package automation

import (
	"testing"
	"time"
)

func TestScheduleValidate(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)

	tests := []struct {
		name    string
		sched   Schedule
		wantErr bool
	}{
		{
			name:    "valid interval",
			sched:   Schedule{Type: "interval", Seconds: 60},
			wantErr: false,
		},
		{
			name:    "invalid interval zero",
			sched:   Schedule{Type: "interval", Seconds: 0},
			wantErr: true,
		},
		{
			name:    "invalid interval negative",
			sched:   Schedule{Type: "interval", Seconds: -10},
			wantErr: true,
		},
		{
			name:    "valid once",
			sched:   Schedule{Type: "once", Date: &future},
			wantErr: false,
		},
		{
			name:    "invalid once nil date",
			sched:   Schedule{Type: "once", Date: nil},
			wantErr: true,
		},
		{
			name:    "valid daily",
			sched:   Schedule{Type: "daily", TimeOfDay: "14:30", Timezone: "UTC"},
			wantErr: false,
		},
		{
			name:    "invalid daily bad timeOfDay format",
			sched:   Schedule{Type: "daily", TimeOfDay: "25:00"},
			wantErr: true,
		},
		{
			name:    "invalid daily bad timeOfDay text",
			sched:   Schedule{Type: "daily", TimeOfDay: "invalid"},
			wantErr: true,
		},
		{
			name:    "invalid daily bad timezone",
			sched:   Schedule{Type: "daily", TimeOfDay: "09:00", Timezone: "Invalid/TZ"},
			wantErr: true,
		},
		{
			name:    "valid weekdays",
			sched:   Schedule{Type: "weekdays", TimeOfDay: "09:00"},
			wantErr: false,
		},
		{
			name:    "valid weekly",
			sched:   Schedule{Type: "weekly", TimeOfDay: "10:00", DayOfWeek: 1},
			wantErr: false,
		},
		{
			name:    "invalid weekly negative dayOfWeek",
			sched:   Schedule{Type: "weekly", TimeOfDay: "10:00", DayOfWeek: -1},
			wantErr: true,
		},
		{
			name:    "invalid weekly out of range dayOfWeek",
			sched:   Schedule{Type: "weekly", TimeOfDay: "10:00", DayOfWeek: 7},
			wantErr: true,
		},
		{
			name:    "valid cron",
			sched:   Schedule{Type: "cron", Expression: "*/5 * * * *"},
			wantErr: false,
		},
		{
			name:    "invalid cron empty expression",
			sched:   Schedule{Type: "cron", Expression: "  "},
			wantErr: true,
		},
		{
			name:    "valid cron range and list",
			sched:   Schedule{Type: "cron", Expression: "0 9 * * 1-5", Timezone: "Asia/Shanghai"},
			wantErr: false,
		},
		{
			name:    "invalid cron minute out of range",
			sched:   Schedule{Type: "cron", Expression: "61 * * * *"},
			wantErr: true,
		},
		{
			name:    "invalid cron few fields",
			sched:   Schedule{Type: "cron", Expression: "* * *"},
			wantErr: true,
		},
		{
			name:    "invalid cron non numeric",
			sched:   Schedule{Type: "cron", Expression: "a b c d e"},
			wantErr: true,
		},
		{
			name:    "unknown schedule type",
			sched:   Schedule{Type: "unknown"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.sched.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestScheduleNext(t *testing.T) {
	baseline := time.Date(2026, 10, 16, 19, 0, 0, 0, time.UTC) // 2026-10-16 是星期五 (Friday)

	t.Run("interval", func(t *testing.T) {
		s := Schedule{Type: "interval", Seconds: 60}
		next, ok := s.Next(baseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expected := baseline.Add(60 * time.Second).UTC()
		if !next.Equal(expected) {
			t.Errorf("expected %v, got %v", expected, next)
		}

		s0 := Schedule{Type: "interval", Seconds: 0}
		if _, ok := s0.Next(baseline); ok {
			t.Errorf("expected false for 0s interval")
		}
	})

	t.Run("once in the future", func(t *testing.T) {
		future := baseline.Add(2 * time.Hour)
		s := Schedule{Type: "once", Date: &future}
		next, ok := s.Next(baseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		if !next.Equal(future.UTC()) {
			t.Errorf("expected %v, got %v", future.UTC(), next)
		}
	})

	t.Run("once in the past", func(t *testing.T) {
		past := baseline.Add(-2 * time.Hour)
		s := Schedule{Type: "once", Date: &past}
		if _, ok := s.Next(baseline); ok {
			t.Errorf("expected false for past once schedule")
		}
	})

	t.Run("daily", func(t *testing.T) {
		// 基准时间 2026-10-16 19:00:00 UTC，设置 20:00，当天即可触发
		sToday := Schedule{Type: "daily", TimeOfDay: "20:00", Timezone: "UTC"}
		next, ok := sToday.Next(baseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expectedToday := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
		if !next.Equal(expectedToday) {
			t.Errorf("expected %v, got %v", expectedToday, next)
		}

		// 基准时间 19:00，设置 18:00，当天已过，应推到次日 18:00
		sTomorrow := Schedule{Type: "daily", TimeOfDay: "18:00", Timezone: "UTC"}
		next2, ok := sTomorrow.Next(baseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expectedTomorrow := time.Date(2026, 10, 17, 18, 0, 0, 0, time.UTC)
		if !next2.Equal(expectedTomorrow) {
			t.Errorf("expected %v, got %v", expectedTomorrow, next2)
		}
	})

	t.Run("weekdays Friday to Monday", func(t *testing.T) {
		// baseline 是星期五 19:00:00 UTC，设置工作日 18:00
		// 周五 18:00 已过，周六日跳过，下一次应为下周一 (2026-10-19) 18:00:00 UTC
		s := Schedule{Type: "weekdays", TimeOfDay: "18:00", Timezone: "UTC"}
		next, ok := s.Next(baseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expectedMonday := time.Date(2026, 10, 19, 18, 0, 0, 0, time.UTC)
		if !next.Equal(expectedMonday) {
			t.Errorf("expected %v, got %v", expectedMonday, next)
		}
		if next.Weekday() != time.Monday {
			t.Errorf("expected Monday, got %v", next.Weekday())
		}
	})

	t.Run("weekly", func(t *testing.T) {
		// baseline 是星期五 19:00:00 UTC
		// DayOfWeek: 2 (Tuesday)，时间 10:00:00 UTC
		// 下一个周二为 2026-10-20
		s := Schedule{Type: "weekly", DayOfWeek: 2, TimeOfDay: "10:00", Timezone: "UTC"}
		next, ok := s.Next(baseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expectedTuesday := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
		if !next.Equal(expectedTuesday) {
			t.Errorf("expected %v, got %v", expectedTuesday, next)
		}
		if next.Weekday() != time.Tuesday {
			t.Errorf("expected Tuesday, got %v", next.Weekday())
		}
	})

	t.Run("timezone Asia/Shanghai", func(t *testing.T) {
		// baseline 2026-10-17 00:00:00 UTC，对应上海时间 2026-10-17 08:00:00 CST
		// 设定每日 09:00 Asia/Shanghai，当天 09:00 CST 对应 2026-10-17 01:00:00 UTC
		shBaseline := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
		s := Schedule{Type: "daily", TimeOfDay: "09:00", Timezone: "Asia/Shanghai"}
		next, ok := s.Next(shBaseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expected := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
		if !next.Equal(expected) {
			t.Errorf("expected %v, got %v", expected, next)
		}
	})

	t.Run("cron */5 step", func(t *testing.T) {
		s := Schedule{Type: "cron", Expression: "*/5 * * * *", Timezone: "UTC"}
		next, ok := s.Next(baseline)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expected := baseline.Add(5 * time.Minute).UTC()
		if !next.Equal(expected) {
			t.Errorf("expected %v, got %v", expected, next)
		}
	})

	t.Run("cron */15 step from odd minute", func(t *testing.T) {
		// 10:07 之后的下一个 15 分钟整点应为 10:15
		start := time.Date(2026, 10, 16, 10, 7, 0, 0, time.UTC)
		s := Schedule{Type: "cron", Expression: "*/15 * * * *", Timezone: "UTC"}
		next, ok := s.Next(start)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expected := time.Date(2026, 10, 16, 10, 15, 0, 0, time.UTC)
		if !next.Equal(expected) {
			t.Errorf("expected %v, got %v", expected, next)
		}
	})

	t.Run("cron weekday range from Friday", func(t *testing.T) {
		// 上海时间周五 10:00，0 9 * * 1-5 下一次为下周一 09:00
		sh, err := time.LoadLocation("Asia/Shanghai")
		if err != nil {
			t.Skipf("tzdata 不可用: %v", err)
		}
		start := time.Date(2026, 10, 16, 10, 0, 0, 0, sh)
		s := Schedule{Type: "cron", Expression: "0 9 * * 1-5", Timezone: "Asia/Shanghai"}
		next, ok := s.Next(start)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expected := time.Date(2026, 10, 19, 9, 0, 0, 0, sh)
		if !next.Equal(expected) {
			t.Errorf("expected %v, got %v", expected, next)
		}
	})

	t.Run("cron day of month next month", func(t *testing.T) {
		// 1 月 15 日之后的 30 2 1 * * 应为 2 月 1 日 02:30
		start := time.Date(2026, 1, 15, 8, 0, 0, 0, time.UTC)
		s := Schedule{Type: "cron", Expression: "30 2 1 * *", Timezone: "UTC"}
		next, ok := s.Next(start)
		if !ok {
			t.Fatalf("expected next run date")
		}
		expected := time.Date(2026, 2, 1, 2, 30, 0, 0, time.UTC)
		if !next.Equal(expected) {
			t.Errorf("expected %v, got %v", expected, next)
		}
	})

	t.Run("cron sunday as 7 equals 0", func(t *testing.T) {
		start := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC) // 星期四
		s7 := Schedule{Type: "cron", Expression: "0 0 * * 7", Timezone: "UTC"}
		s0 := Schedule{Type: "cron", Expression: "0 0 * * 0", Timezone: "UTC"}
		next7, ok7 := s7.Next(start)
		next0, ok0 := s0.Next(start)
		if !ok7 || !ok0 {
			t.Fatalf("expected next run date")
		}
		expected := time.Date(2026, 1, 18, 0, 0, 0, 0, time.UTC) // 星期日
		if !next7.Equal(expected) {
			t.Errorf("cron 周字段 7 期望 %v，实际 %v", expected, next7)
		}
		if !next7.Equal(next0) {
			t.Errorf("周字段 7 与 0 结果应一致，实际 %v vs %v", next7, next0)
		}
	})

	t.Run("cron unparsable returns false", func(t *testing.T) {
		// 字段数量不对、含非数字字段时不再有机会“兜底跑一次”
		for _, expr := range []string{"not enough fields", "* * *", "a b c d e", "61 * * * *"} {
			s := Schedule{Type: "cron", Expression: expr}
			if next, ok := s.Next(baseline); ok {
				t.Errorf("表达式 %q 应返回 false，实际得到 %v", expr, next)
			}
		}
	})
}

func TestScheduleNextTimezones(t *testing.T) {
	baseline := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		tz   string
		want time.Time
	}{
		{"Asia/Shanghai 09:00 CST", "Asia/Shanghai", time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)},
		{"America/New_York 09:00 EDT", "America/New_York", time.Date(2026, 10, 18, 13, 0, 0, 0, time.UTC)},
		{"UTC", "UTC", time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Schedule{Type: "daily", TimeOfDay: "09:00", Timezone: tt.tz}
			next, ok := s.Next(baseline)
			if !ok || !next.Equal(tt.want) {
				t.Errorf("Next() = %v, %v, want %v", next, ok, tt.want)
			}
		})
	}
}

func TestScheduleNextDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata 不可用: %v", err)
	}

	t.Run("spring forward 不存在的时刻顺延", func(t *testing.T) {
		// 2026-03-08 02:00 EST 直接跳到 03:00 EDT，02:30 不存在。
		s := Schedule{Type: "daily", TimeOfDay: "02:30", Timezone: "America/New_York"}
		baseline := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC) // 本地 3/7 07:00
		next, ok := s.Next(baseline)
		if !ok {
			t.Fatal("expected next run")
		}
		local := next.In(ny)
		if local.Month() != time.March || local.Day() != 8 {
			t.Fatalf("期望顺延仍落在 3/8，实际 %v", local)
		}
		if local.Hour() < 3 {
			t.Errorf("不存在的 02:30 应顺延到 03:00 之后，实际 %v", local)
		}
		// 次日恢复为正常的 02:30 EDT。
		after, _ := s.Next(next)
		want := time.Date(2026, 3, 9, 2, 30, 0, 0, ny)
		if !after.Equal(want) {
			t.Errorf("次日应为 %v，实际 %v", want, after)
		}
	})

	t.Run("fall back 重复的时刻只触发一次", func(t *testing.T) {
		// 2026-11-01 01:00-02:00 本地时间出现两次。
		s := Schedule{Type: "daily", TimeOfDay: "01:30", Timezone: "America/New_York"}
		baseline := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
		first, ok := s.Next(baseline)
		if !ok {
			t.Fatal("expected next run")
		}
		if l := first.In(ny); l.Day() != 1 || l.Month() != time.November || l.Hour() != 1 || l.Minute() != 30 {
			t.Fatalf("期望 11/1 本地 01:30，实际 %v", l)
		}
		// 从首次触发之后计算，不得落回同一天的另一个 01:30。
		second, ok := s.Next(first)
		if !ok {
			t.Fatal("expected next run")
		}
		want := time.Date(2026, 11, 2, 1, 30, 0, 0, ny)
		if !second.Equal(want) {
			t.Errorf("第二次应为 %v，实际 %v", want, second)
		}
	})
}

func TestMissedOccurrences(t *testing.T) {
	due := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	onceDate := due
	tests := []struct {
		name          string
		sched         Schedule
		now           time.Time
		wantCount     int
		wantLast      time.Time
		wantTruncated bool
	}{
		{
			name:      "before due",
			sched:     Schedule{Type: "interval", Seconds: 60},
			now:       due.Add(-time.Second),
			wantCount: 0,
		},
		{
			name:      "exactly due",
			sched:     Schedule{Type: "interval", Seconds: 60},
			now:       due,
			wantCount: 1,
			wantLast:  due,
		},
		{
			name:      "interval two and a half periods",
			sched:     Schedule{Type: "interval", Seconds: 60},
			now:       due.Add(150 * time.Second),
			wantCount: 3,
			wantLast:  due.Add(120 * time.Second),
		},
		{
			name:          "interval truncated",
			sched:         Schedule{Type: "interval", Seconds: 1},
			now:           due.Add(5000 * time.Second),
			wantCount:     missedOccurrenceLimit,
			wantLast:      due.Add(time.Duration(missedOccurrenceLimit-1) * time.Second),
			wantTruncated: true,
		},
		{
			name:      "daily three points",
			sched:     Schedule{Type: "daily", TimeOfDay: "09:00", Timezone: "UTC"},
			now:       due.Add(49 * time.Hour),
			wantCount: 3,
			wantLast:  due.Add(48 * time.Hour),
		},
		{
			name:      "once in the past",
			sched:     Schedule{Type: "once", Date: &onceDate},
			now:       due.Add(5 * time.Minute),
			wantCount: 1,
			wantLast:  due,
		},
		{
			name:          "cron truncated",
			sched:         Schedule{Type: "cron", Expression: "* * * * *", Timezone: "UTC"},
			now:           due.Add(2000 * time.Minute),
			wantCount:     missedOccurrenceLimit,
			wantLast:      due.Add(time.Duration(missedOccurrenceLimit-1) * time.Minute),
			wantTruncated: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, last, truncated := MissedOccurrences(tt.sched, due, tt.now)
			if count != tt.wantCount {
				t.Errorf("count = %d, want %d", count, tt.wantCount)
			}
			if truncated != tt.wantTruncated {
				t.Errorf("truncated = %v, want %v", truncated, tt.wantTruncated)
			}
			if tt.wantCount == 0 {
				if !last.IsZero() {
					t.Errorf("last = %v, want zero", last)
				}
				return
			}
			if !last.Equal(tt.wantLast) {
				t.Errorf("last = %v, want %v", last, tt.wantLast)
			}
		})
	}
}

func TestMissedForSpec(t *testing.T) {
	due := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	onceDate := due

	t.Run("periodic excludes the due point", func(t *testing.T) {
		spec := Spec{Schedule: Schedule{Type: "interval", Seconds: 60}}
		missed, _, _ := missedForSpec(spec, due, due.Add(150*time.Second))
		if missed != 2 {
			t.Errorf("missed = %d, want 2", missed)
		}
	})

	t.Run("once within grace is not missed", func(t *testing.T) {
		spec := Spec{Schedule: Schedule{Type: "once", Date: &onceDate}}
		missed, _, _ := missedForSpec(spec, due, due.Add(onceMissedGrace))
		if missed != 0 {
			t.Errorf("missed = %d, want 0", missed)
		}
	})

	t.Run("once beyond grace is missed once", func(t *testing.T) {
		spec := Spec{Schedule: Schedule{Type: "once", Date: &onceDate}}
		now := due.Add(onceMissedGrace + time.Second)
		missed, last, _ := missedForSpec(spec, due, now)
		if missed != 1 {
			t.Errorf("missed = %d, want 1", missed)
		}
		if !last.Equal(due) {
			t.Errorf("last = %v, want %v", last, due)
		}
	})

	t.Run("once already run is not missed", func(t *testing.T) {
		spec := Spec{
			Schedule: Schedule{Type: "once", Date: &onceDate},
			State:    RunState{TotalRunCount: 1},
		}
		missed, _, _ := missedForSpec(spec, due, due.Add(time.Hour))
		if missed != 0 {
			t.Errorf("missed = %d, want 0", missed)
		}
	})
}
