package config

import "testing"

// TestAutomationPreventSleepEnabled 校验自动化防休眠开关的默认值与显式取值。
func TestAutomationPreventSleepEnabled(t *testing.T) {
	yes := true
	no := false
	cases := []struct {
		name string
		in   *bool
		want bool
	}{
		{name: "nil 视为开启", in: nil, want: true},
		{name: "显式开启", in: &yes, want: true},
		{name: "显式关闭", in: &no, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := AutomationConfig{PreventSleep: tc.in}
			if got := cfg.PreventSleepEnabled(); got != tc.want {
				t.Fatalf("PreventSleepEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}
