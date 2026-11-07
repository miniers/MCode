package automation

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mcode/internal/harness"
)

type fakeHarness struct {
	output string
	err    error
}

func (f *fakeHarness) Kind() harness.Kind {
	return "fake"
}

func (f *fakeHarness) Run(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
	if f.err != nil {
		return f.err
	}
	if f.output != "" {
		emit(harness.Event{
			Type: harness.EventText,
			Text: f.output,
		})
	}
	return nil
}

func TestHeuristic(t *testing.T) {
	tests := []struct {
		name       string
		stopWhen   string
		summary    string
		diff       string
		wantSat    bool
		wantConf   float64
		wantReason string
	}{
		{
			name:       "tests passed",
			stopWhen:   "All tests pass",
			summary:    "Done, all checks passed without error",
			diff:       "",
			wantSat:    true,
			wantConf:   0.95,
			wantReason: reasonChecksPassed,
		},
		{
			name:       "0 remaining",
			stopWhen:   "No remaining tasks",
			summary:    "Processed items, 0 remaining",
			diff:       "",
			wantSat:    true,
			wantConf:   0.9,
			wantReason: reasonNoneRemaining,
		},
		{
			name:       "clean repo",
			stopWhen:   "Repository clean",
			summary:    "Ran checks, working tree clean",
			diff:       "",
			wantSat:    true,
			wantConf:   0.85,
			wantReason: reasonCleanWorkspace,
		},
		{
			name:       "no code changes marker satisfies no-uncommitted condition",
			stopWhen:   "No uncommitted code changes",
			summary:    "reviewed",
			diff:       NoCodeChangesMarker,
			wantSat:    true,
			wantConf:   0.85,
			wantReason: reasonCleanWorkspace,
		},
		{
			name:       "code changes present do not satisfy no-code-change condition",
			stopWhen:   "no code changes remain",
			summary:    "reviewed",
			diff:       " a.go | 2 +-\n 1 file changed",
			wantSat:    false,
			wantConf:   0.2,
			wantReason: reasonNotShown,
		},
		{
			name:       "objective completed",
			stopWhen:   "arbitrary condition",
			summary:    "agent finished: objective completed successfully",
			diff:       "some changes",
			wantSat:    true,
			wantConf:   0.82,
			wantReason: reasonAgentDone,
		},
		{
			name:       "not satisfied",
			stopWhen:   "all tests pass",
			summary:    "1 test failed out of 10",
			diff:       "modifications",
			wantSat:    false,
			wantConf:   0.2,
			wantReason: reasonNotShown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := Heuristic(tt.stopWhen, tt.summary, tt.diff)
			if res.Satisfied != tt.wantSat {
				t.Errorf("Satisfied = %v, want %v", res.Satisfied, tt.wantSat)
			}
			if res.Confidence != tt.wantConf {
				t.Errorf("Confidence = %v, want %v", res.Confidence, tt.wantConf)
			}
			if res.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", res.Reason, tt.wantReason)
			}
		})
	}
}

func TestParseEvaluation(t *testing.T) {
	t.Run("fenced json", func(t *testing.T) {
		text := "Here is the verdict:\n```json\n{\"satisfied\": true, \"confidence\": 0.95, \"reason\": \"all good\"}\n```\nHope it helps."
		res, ok := ParseEvaluation(text)
		if !ok {
			t.Fatalf("expected ParseEvaluation to succeed")
		}
		if !res.Satisfied || res.Confidence != 0.95 || res.Reason != "all good" {
			t.Errorf("unexpected result: %+v", res)
		}
	})

	t.Run("missing satisfied key", func(t *testing.T) {
		text := `{"confidence": 0.8, "reason": "no satisfied key"}`
		_, ok := ParseEvaluation(text)
		if ok {
			t.Errorf("expected false when satisfied key is missing")
		}
	})

	t.Run("confidence clamping", func(t *testing.T) {
		textHigh := `{"satisfied": true, "confidence": 1.5, "reason": "over 1"}`
		resHigh, ok := ParseEvaluation(textHigh)
		if !ok || resHigh.Confidence != 1.0 {
			t.Errorf("expected confidence clamped to 1.0, got %v", resHigh.Confidence)
		}

		textLow := `{"satisfied": false, "confidence": -0.5, "reason": "under 0"}`
		resLow, ok := ParseEvaluation(textLow)
		if !ok || resLow.Confidence != 0.0 {
			t.Errorf("expected confidence clamped to 0.0, got %v", resLow.Confidence)
		}
	})
}

func TestEvaluatorEvaluate(t *testing.T) {
	ctx := context.Background()

	t.Run("harness json verdict respects threshold", func(t *testing.T) {
		h := &fakeHarness{
			output: "```json\n{\"satisfied\": true, \"confidence\": 0.85, \"reason\": \"verified\"}\n```",
		}
		eval := &Evaluator{Harness: h}

		// threshold 0.8 -> met should be true
		res, met := eval.Evaluate(ctx, "", "stop condition", 0.8, "summary", "")
		if !met || !res.Satisfied || res.Confidence != 0.85 {
			t.Errorf("expected met=true, got met=%v, res=%+v", met, res)
		}

		// threshold 0.9 -> met should be false
		_, metHigh := eval.Evaluate(ctx, "", "stop condition", 0.9, "summary", "")
		if metHigh {
			t.Errorf("expected met=false when confidence < threshold")
		}
	})

	t.Run("garbage output is not treated as satisfied", func(t *testing.T) {
		h := &fakeHarness{
			output: "This is not json at all! completely arbitrary text.",
		}
		eval := &Evaluator{Harness: h}

		// 停止条件与摘要都能命中启发式，但评估输出不可解析时不得判为满足。
		res, met := eval.Evaluate(ctx, "", "all tests pass", 0.8, "all tests passed", "")
		if met || res.Satisfied {
			t.Errorf("expected unverified result, got met=%v, res=%+v", met, res)
		}
		if !strings.Contains(res.Reason, "未验证") {
			t.Errorf("expected reason to mention 未验证, got %q", res.Reason)
		}
	})

	t.Run("harness error is not treated as satisfied", func(t *testing.T) {
		h := &fakeHarness{err: errors.New("evaluator harness crashed")}
		eval := &Evaluator{Harness: h}

		res, met := eval.Evaluate(ctx, "", "all tests pass", 0.8, "all tests passed", "")
		if met || res.Satisfied {
			t.Errorf("expected unverified result, got met=%v, res=%+v", met, res)
		}
		if !strings.Contains(res.Reason, "evaluator harness crashed") {
			t.Errorf("expected reason to keep the harness error, got %q", res.Reason)
		}
	})

	t.Run("nil evaluator falls back to heuristic", func(t *testing.T) {
		var eval *Evaluator
		res, met := eval.Evaluate(ctx, "", "all tests pass", 0.8, "all checks passed", "")
		if !met || !res.Satisfied || res.Confidence != 0.95 {
			t.Errorf("expected nil evaluator fallback to heuristic, got met=%v, res=%+v", met, res)
		}
	})
}
