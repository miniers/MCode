package mcodeui

import (
	"testing"
	"time"
)

func TestScrollFollowThresholds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		atEnd    bool
		distance float32
		want     bool
	}{
		{"at end distance 40", true, 40, false},
		{"40", false, 40, false},
		{"120", false, 120, false},
		{"121", false, 121, true},
		{"unknown not at end", false, -1, true},
		{"unknown at end", true, -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldShowJumpToLatest(tc.atEnd, tc.distance); got != tc.want {
				t.Errorf("shouldShowJumpToLatest(%v, %v) = %v, want %v", tc.atEnd, tc.distance, got, tc.want)
			}
		})
	}
}

func TestShouldAutoFollow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		distance float32
		since    time.Duration
		want     bool
	}{
		{"39 after cooldown", 39, followCooldown, true},
		{"40 after cooldown", 40, followCooldown, true},
		{"41 after cooldown", 41, followCooldown, false},
		{"39 before cooldown", 39, followCooldown - time.Millisecond, false},
		{"39 at cooldown boundary", 39, followCooldown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAutoFollow(tc.distance, tc.since); got != tc.want {
				t.Errorf("shouldAutoFollow(%v, %v) = %v, want %v", tc.distance, tc.since, got, tc.want)
			}
		})
	}
}
