package mcodeui

import (
	"testing"
	"time"

	"mcode/internal/testenv"
)

func TestFormatLimitReset(t *testing.T) {
	testenv.TempHome(t)

	// 固定的 now: 2026-10-18 14:30:00 (周日)
	now := time.Date(2026, 10, 18, 14, 30, 0, 0, time.Local)

	tests := []struct {
		name     string
		resetsAt time.Time
		want     string
	}{
		{
			name:     "零值",
			resetsAt: time.Time{},
			want:     "",
		},
		{
			name:     "已过去",
			resetsAt: now.Add(-10 * time.Minute),
			want:     "即将重置",
		},
		{
			name:     "30 分钟",
			resetsAt: now.Add(30 * time.Minute),
			want:     "30 分钟后重置",
		},
		{
			name:     "3 小时 34 分钟",
			resetsAt: now.Add(3*time.Hour + 34*time.Minute),
			want:     "3 小时 34 分钟后重置",
		},
		{
			name:     "3 天后",
			resetsAt: now.Add(3 * 24 * time.Hour), // 周日 + 3 天 = 周三 14:30
			want:     "周三 14:30 重置",
		},
		{
			name:     "10 天后",
			resetsAt: now.Add(10 * 24 * time.Hour), // 2026-10-28 14:30
			want:     "10月28日 重置",
		},
		{
			name:     "更久（1月2日）",
			resetsAt: time.Date(2027, 1, 2, 9, 0, 0, 0, time.Local),
			want:     "1月2日 重置",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatLimitReset(now, tt.resetsAt)
			if got != tt.want {
				t.Errorf("formatLimitReset(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}
