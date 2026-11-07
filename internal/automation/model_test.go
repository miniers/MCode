package automation

import "testing"

func TestSpecEffectiveMaxRuns(t *testing.T) {
	tests := []struct {
		name string
		spec Spec
		want int
	}{
		{name: "unlimited", spec: Spec{}, want: 0},
		{name: "maxRuns preferred", spec: Spec{MaxRuns: 5, MaxIterations: 2}, want: 5},
		{name: "legacy maxIterations fallback", spec: Spec{MaxIterations: 3}, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.EffectiveMaxRuns(); got != tt.want {
				t.Errorf("EffectiveMaxRuns() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSpecCatchUpEnabled(t *testing.T) {
	enabled := true
	disabled := false
	tests := []struct {
		name string
		spec Spec
		want bool
	}{
		{name: "nil defaults to enabled", spec: Spec{}, want: true},
		{name: "explicit enabled", spec: Spec{CatchUp: &enabled}, want: true},
		{name: "explicit disabled", spec: Spec{CatchUp: &disabled}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec.CatchUpEnabled(); got != tt.want {
				t.Errorf("CatchUpEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
