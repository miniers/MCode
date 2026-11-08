package mcodeui

import (
	"os/exec"
	"testing"
)

func TestProbeOnboardingPrerequisites(t *testing.T) {
	original := onboardingLookPath
	t.Cleanup(func() { onboardingLookPath = original })
	for _, tc := range []struct {
		name      string
		node, npm bool
	}{
		{"都存在", true, true},
		{"缺少 node", false, true},
		{"缺少 npm", true, false},
		{"都缺少", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			onboardingLookPath = func(name string) (string, error) {
				if name == "node" && tc.node || name == "npm" && tc.npm {
					return name, nil
				}
				return "", exec.ErrNotFound
			}
			node, npm := probeOnboardingPrerequisites()
			if node != tc.node || npm != tc.npm {
				t.Fatalf("得到 node=%v npm=%v，期望 node=%v npm=%v", node, npm, tc.node, tc.npm)
			}
		})
	}
}

func TestOnboardingNeedsNode(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"claude", true}, {"codex", true}, {"pi", true}, {"mcode", false}, {"agy", false}, {"other", false},
	} {
		if got := onboardingNeedsNode(tc.id); got != tc.want {
			t.Errorf("onboardingNeedsNode(%q) = %v，期望 %v", tc.id, got, tc.want)
		}
	}
}
