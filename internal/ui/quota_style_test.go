package mcodeui

import "testing"

func TestQuotaLevel(t *testing.T) {
	tests := []struct {
		name    string
		percent float64
		want    string
	}{
		{name: "0", percent: 0, want: "low"},
		{name: "49.9", percent: 49.9, want: "low"},
		{name: "50", percent: 50, want: "mid"},
		{name: "79.9", percent: 79.9, want: "mid"},
		{name: "80", percent: 80, want: "high"},
		{name: "94.9", percent: 94.9, want: "high"},
		{name: "95", percent: 95, want: "critical"},
		{name: "100", percent: 100, want: "critical"},
		{name: "120", percent: 120, want: "critical"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quotaLevel(tt.percent)
			if got != tt.want {
				t.Errorf("quotaLevel(%v) = %q, want %q", tt.percent, got, tt.want)
			}
		})
	}
}
