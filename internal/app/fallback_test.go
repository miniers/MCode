package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
	"mcode/internal/remote/wire"
)

func TestFallbackCandidateOrdering(t *testing.T) {
	price := func(input, output float64) *wire.ModelCost { return &wire.ModelCost{Input: input, Output: output} }
	candidates := []textgenCandidate{
		{model: "unknown", order: 0}, {model: "b", order: 1, cost: price(1, 1)},
		{model: "z", order: 0, cost: price(1, 1)}, {model: "a", order: 0, cost: price(1, 1)},
		{model: "free", order: 2, cost: price(0, 0)},
	}
	sortTextgenCandidates(candidates)
	for i, want := range []string{"free", "a", "z", "b", "unknown"} {
		if candidates[i].model != want {
			t.Fatalf("位置 %d: 得到 %s，期望 %s", i, candidates[i].model, want)
		}
	}
}

func TestFallbackSelection(t *testing.T) {
	candidates := []textgenCandidate{
		{source: apisource.Source{Kind: "one"}, model: "gpt-4", cost: &wire.ModelCost{}},
		{source: apisource.Source{Kind: "two"}, model: "gpt-5", cost: &wire.ModelCost{Input: 1}},
	}
	if _, ok := selectTextgenCandidate(candidates, "two", "explicit", true); ok {
		t.Fatal("覆盖显式模型")
	}
	if got, ok := selectTextgenCandidate(candidates, "two", "default", false); !ok || got.model != "gpt-5" {
		t.Fatal("来源限定失效")
	}
	if got, ok := selectTextgenCandidate(candidates, "", "", true); !ok || got.model != "gpt-4" {
		t.Fatal("全局选择失效")
	}
}

func TestFallbackStatusAndLimit(t *testing.T) {
	primary := textgenCandidate{source: apisource.Source{Kind: "one"}, model: "deepseek-chat"}
	candidates := []textgenCandidate{{source: apisource.Source{Kind: "two"}, model: "route/deepseek-reasoner"}}
	for _, status := range []int{429, 500, 504, 529, 401, 403, 404} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls, notices := 0, 0
			ctx := withTextgenFallbackNotice(context.Background(), func(_, _ string) { notices++ })
			_, err := executeTextgenFallback(ctx, primary, func() []textgenCandidate { return candidates }, func(context.Context, textgenCandidate) (string, error) {
				calls++
				return "", &llm.APIError{Status: status}
			})
			want := 1
			if status == 429 || status >= 500 {
				want = 2
			}
			if err == nil || calls != want || notices != want-1 {
				t.Fatalf("calls=%d notices=%d err=%v", calls, notices, err)
			}
		})
	}
}

func TestFallbackNoRetry(t *testing.T) {
	primary := textgenCandidate{source: apisource.Source{Kind: "one"}, model: "gpt-4"}
	other := textgenCandidate{source: apisource.Source{Kind: "two"}, model: "claude-sonnet"}
	for _, err := range []error{errors.New("断流"), context.Canceled, context.DeadlineExceeded, &llm.APIError{Status: 500}} {
		calls := 0
		_, _ = executeTextgenFallback(context.Background(), primary, func() []textgenCandidate { return []textgenCandidate{other} }, func(context.Context, textgenCandidate) (string, error) { calls++; return "", err })
		if calls != 1 {
			t.Fatalf("意外重试: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, _ = executeTextgenFallback(ctx, primary, func() []textgenCandidate {
		return []textgenCandidate{{source: other.source, model: "gpt-5"}}
	}, func(context.Context, textgenCandidate) (string, error) {
		calls++
		return "", &llm.APIError{Status: 429}
	})
	if calls != 1 {
		t.Fatal("取消后重试")
	}
}

func TestFallbackConcurrentNotices(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprint(i)
			primary := textgenCandidate{source: apisource.Source{Kind: name}, model: "claude-opus"}
			candidate := textgenCandidate{source: apisource.Source{Kind: name + "next"}, model: "claude-sonnet"}
			notices := 0
			ctx := withTextgenFallbackNotice(context.Background(), func(from, to string) {
				notices++
				if from != name+" / claude-opus" || to != name+"next / claude-sonnet" {
					t.Errorf("提示串线: %s %s", from, to)
				}
			})
			calls := 0
			_, _ = executeTextgenFallback(ctx, primary, func() []textgenCandidate { return []textgenCandidate{candidate} }, func(context.Context, textgenCandidate) (string, error) {
				calls++
				if calls == 1 {
					return "", &llm.APIError{Status: 429}
				}
				return "ok", nil
			})
			if calls != 2 || notices != 1 {
				t.Errorf("calls=%d notices=%d", calls, notices)
			}
		}(i)
	}
	wg.Wait()
}

func TestFallbackDoesNotLoadCandidatesOnSuccess(t *testing.T) {
	primary := textgenCandidate{source: apisource.Source{Kind: "one"}, model: "claude-sonnet"}
	loaded := false
	out, err := executeTextgenFallback(context.Background(), primary, func() []textgenCandidate {
		loaded = true
		return nil
	}, func(context.Context, textgenCandidate) (string, error) {
		return "ok", nil
	})
	if err != nil || out != "ok" || loaded {
		t.Fatalf("out=%q err=%v loaded=%v", out, err, loaded)
	}
}

func TestFallbackDoesNotLoadCandidatesOnUnauthorized(t *testing.T) {
	primary := textgenCandidate{source: apisource.Source{Kind: "one"}, model: "claude-sonnet"}
	loaded := false
	calls := 0
	_, err := executeTextgenFallback(context.Background(), primary, func() []textgenCandidate {
		loaded = true
		return nil
	}, func(context.Context, textgenCandidate) (string, error) {
		calls++
		return "", &llm.APIError{Status: 401}
	})
	if err == nil || calls != 1 || loaded {
		t.Fatalf("err=%v calls=%d loaded=%v", err, calls, loaded)
	}
}
