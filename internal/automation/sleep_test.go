package automation

import (
	"context"
	"testing"
	"time"
)

// TestHasRunningRun 校验 inFlight 为空与非空时的判定。
func TestHasRunningRun(t *testing.T) {
	clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
	svc, s := setupTestService(t, clock, newFakeDriver())
	defer s.Close()

	if svc.HasRunningRun() {
		t.Fatal("没有运行中的自动化时应返回 false")
	}

	svc.inFlightMu.Lock()
	svc.inFlight["auto-1"] = "run-1"
	svc.inFlightMu.Unlock()

	if !svc.HasRunningRun() {
		t.Fatal("inFlight 有记录时应返回 true")
	}
}

// TestNextRunWithin 校验 5 分钟窗口内到期的判定：只有已启用且 NextRunAt 不晚于 now+d 才算命中。
func TestNextRunWithin(t *testing.T) {
	cases := []struct {
		name    string
		seconds int
		enabled bool
		want    bool
	}{
		{name: "4 分钟后到期", seconds: 240, enabled: true, want: true},
		{name: "10 分钟后到期", seconds: 600, enabled: true, want: false},
		{name: "已停用的自动化", seconds: 240, enabled: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			clock := newFakeClock(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))
			svc, s := setupTestService(t, clock, newFakeDriver())
			defer s.Close()

			if _, err := svc.Create(ctx, CreateInput{
				ProjectID: "p1",
				Name:      tc.name,
				Prompt:    "检查",
				Spec:      Spec{Schedule: Schedule{Type: "interval", Seconds: tc.seconds}},
				Enabled:   tc.enabled,
			}); err != nil {
				t.Fatal(err)
			}

			if got := svc.NextRunWithin(ctx, 5*time.Minute); got != tc.want {
				t.Fatalf("NextRunWithin(5m) = %v, want %v", got, tc.want)
			}
		})
	}
}
