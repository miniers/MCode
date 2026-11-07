package app

import "testing"

// TestAutomationKeepAwake 表驱动校验自动化防休眠的判定逻辑。
func TestAutomationKeepAwake(t *testing.T) {
	cases := []struct {
		name    string
		enabled bool
		running bool
		dueSoon func() bool
		want    bool
	}{
		{
			name:    "开关关闭时即使有运行也不阻止休眠",
			enabled: false,
			running: true,
			dueSoon: func() bool { return true },
			want:    false,
		},
		{
			name:    "有自动化正在运行",
			enabled: true,
			running: true,
			dueSoon: func() bool { return false },
			want:    true,
		},
		{
			name:    "5 分钟内有自动化到期",
			enabled: true,
			running: false,
			dueSoon: func() bool { return true },
			want:    true,
		},
		{
			name:    "没有运行也没有临近任务",
			enabled: true,
			running: false,
			dueSoon: func() bool { return false },
			want:    false,
		},
		{
			name:    "dueSoon 为 nil 时按不临近处理",
			enabled: true,
			running: false,
			dueSoon: nil,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := automationKeepAwake(tc.enabled, tc.running, tc.dueSoon); got != tc.want {
				t.Fatalf("automationKeepAwake(%v, %v, dueSoon) = %v, want %v", tc.enabled, tc.running, got, tc.want)
			}
		})
	}
}
