package claude

import (
	"slices"
	"strings"
	"testing"

	"mcode/internal/harness"
)

func TestBuildArgsSystemPolicy(t *testing.T) {
	for _, tc := range []struct {
		policy string
		want   string
	}{
		{policy: "", want: planModeHint},
		{policy: "static host rules", want: "static host rules\n\n" + planModeHint},
	} {
		args := buildArgs(harness.Turn{SystemPolicy: tc.policy})
		for flag, want := range map[string]string{"--append-system-prompt": tc.want, "--system-prompt-snapshot": "off"} {
			i := slices.Index(args, flag)
			if i < 0 || i+1 >= len(args) || args[i+1] != want {
				t.Fatalf("%s 未传入预期值 %q: %v", flag, want, args)
			}
		}
	}
	var carrier harness.SystemPolicyCarrier = New(Options{})
	if !carrier.CarriesSystemPolicy("thread") {
		t.Fatal("Claude 应承载系统规则")
	}
}

func TestBuildArgsPlanModeSystemPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy string
		want   string
	}{
		{
			name:   "empty_policy",
			policy: "",
			want:   planModeHint,
		},
		{
			name:   "with_system_policy",
			policy: "static host rules",
			want:   "static host rules\n\n" + planModeHint,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := buildArgs(harness.Turn{
				InteractionMode: "plan",
				SystemPolicy:    tc.policy,
			})
			i := slices.Index(args, "--append-system-prompt")
			if i < 0 || i+1 >= len(args) {
				t.Fatalf("plan 模式应传入 --append-system-prompt: %v", args)
			}
			got := args[i+1]
			if got != tc.want {
				t.Fatalf("--append-system-prompt 值不符合预期:\ngot:  %q\nwant: %q", got, tc.want)
			}
			for _, substr := range []string{"ExitPlanMode", "ToolSearch"} {
				if !strings.Contains(got, substr) {
					t.Fatalf("--append-system-prompt 缺少 %q: %q", substr, got)
				}
			}
			if tc.policy != "" && !strings.Contains(got, tc.policy) {
				t.Fatalf("--append-system-prompt 缺少原有 SystemPolicy %q: %q", tc.policy, got)
			}
			snapIdx := slices.Index(args, "--system-prompt-snapshot")
			if snapIdx < 0 || snapIdx+1 >= len(args) || args[snapIdx+1] != "off" {
				t.Fatalf("plan 模式应传入 --system-prompt-snapshot off: %v", args)
			}
		})
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

func TestSystemPolicyResidentFingerprintPlanMode(t *testing.T) {
	h := New(Options{})
	assertPrompt := func(t *testing.T, mode string, args []string) {
		t.Helper()
		i := slices.Index(args, "--append-system-prompt")
		if i < 0 || i+1 >= len(args) {
			t.Fatalf("%s 模式缺少 --append-system-prompt: %v", mode, args)
		}
		for _, substr := range []string{"ExitPlanMode", "ToolSearch"} {
			if !strings.Contains(args[i+1], substr) {
				t.Fatalf("%s 模式 --append-system-prompt 缺少 %q: %q", mode, substr, args[i+1])
			}
		}
	}
	for _, basePolicy := range []string{"", "static host rules"} {
		turn := harness.Turn{
			ThreadID:        "thread-plan-switch",
			InteractionMode: "build",
			SystemPolicy:    basePolicy,
			Model:           "sonnet",
			Effort:          "high",
		}
		buildLaunch, err := h.planLaunch(turn)
		if err != nil {
			t.Fatal(err)
		}
		assertPrompt(t, "build", buildLaunch.args)

		turn.InteractionMode = "plan"
		planLaunch, err := h.planLaunch(turn)
		if err != nil {
			t.Fatal(err)
		}
		assertPrompt(t, "plan", planLaunch.args)

		if planLaunch.key != buildLaunch.key {
			t.Fatalf("build 与 plan 的常驻指纹应相同，policy=%q: build=%s plan=%s", basePolicy, buildLaunch.key, planLaunch.key)
		}
	}
}
