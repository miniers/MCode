package claude

import (
	"slices"
	"testing"

	"mcode/internal/harness"
)

func TestBuildArgsSystemPolicy(t *testing.T) {
	for _, policy := range []string{"", "static host rules"} {
		args := buildArgs(harness.Turn{SystemPolicy: policy})
		for flag, want := range map[string]string{"--append-system-prompt": policy, "--system-prompt-snapshot": "off"} {
			i := slices.Index(args, flag)
			if policy == "" {
				if i >= 0 {
					t.Fatalf("空 SystemPolicy 不应传入 %s: %v", flag, args)
				}
			} else if i < 0 || i+1 >= len(args) || args[i+1] != want {
				t.Fatalf("%s 未传入预期值 %q: %v", flag, want, args)
			}
		}
	}
	var carrier harness.SystemPolicyCarrier = New(Options{})
	if !carrier.CarriesSystemPolicy("thread") {
		t.Fatal("Claude 应承载系统规则")
	}
}

func TestSystemPolicyResidentFingerprint(t *testing.T) {
	h := New(Options{})
	for _, hot := range []bool{false, true} {
		turn := harness.Turn{SystemPolicy: "first policy"}
		if hot {
			turn.Model, turn.Effort = "sonnet", "high"
		}
		first, err := h.planLaunch(turn)
		if err != nil {
			t.Fatal(err)
		}
		same, err := h.planLaunch(turn)
		if err != nil {
			t.Fatal(err)
		}
		if first.key != same.key {
			t.Fatalf("相同系统规则指纹不同，hot=%v", hot)
		}
		for _, nextPolicy := range []string{"second policy", ""} {
			turn.SystemPolicy = nextPolicy
			next, err := h.planLaunch(turn)
			if err != nil {
				t.Fatal(err)
			}
			if next.key == first.key {
				t.Fatalf("系统规则变化未更新指纹，hot=%v policy=%q", hot, nextPolicy)
			}
		}
	}
}
