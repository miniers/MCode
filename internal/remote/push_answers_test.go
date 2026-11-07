package remote

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// answerableQuestionRequest 构造一道可内联作答的提问：单选、非机密、两个选项。
func answerableQuestionRequest() wire.RemoteRequest {
	return wire.RemoteRequest{
		ThreadID:  "t-1",
		RequestID: "req-1",
		Kind:      string(harness.RequestKindQuestion),
		Questions: []wire.RemoteQuestion{{
			ID:     "q-1",
			Prompt: "要继续吗？",
			Choices: []wire.RemoteQuestionChoice{
				{Label: "允许"},
				{Label: "拒绝"},
			},
		}},
	}
}

func TestAnswerTargetForBuildsButtons(t *testing.T) {
	longLabel := strings.Repeat("选", 30)
	req := answerableQuestionRequest()
	req.Questions[0].Choices[1].Label = longLabel

	target := answerTargetFor(req)
	if target == nil {
		t.Fatal("expected answerable question to produce a target")
	}
	if target.RequestID != "req-1" || target.QuestionID != "q-1" {
		t.Fatalf("unexpected target: %+v", target)
	}
	if len(target.Answers) != 2 {
		t.Fatalf("expected two buttons, got %+v", target.Answers)
	}
	if target.Answers[0].Action != "ans0" || target.Answers[0].Label != "允许" || target.Answers[0].Value != "允许" {
		t.Fatalf("unexpected first button: %+v", target.Answers[0])
	}
	if target.Answers[1].Action != "ans1" || target.Answers[1].Value != longLabel {
		t.Fatalf("unexpected second button: %+v", target.Answers[1])
	}
	if got := utf8.RuneCountInString(target.Answers[1].Label); got > maxInlineAnswerLabelRunes {
		t.Fatalf("button label length = %d, want <= %d", got, maxInlineAnswerLabelRunes)
	}
	if !strings.HasSuffix(target.Answers[1].Label, "…") {
		t.Fatalf("expected truncated label to end with ellipsis, got %q", target.Answers[1].Label)
	}
}

func TestAnswerTargetForRejectsUnanswerable(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*wire.RemoteRequest)
	}{
		{"multi select", func(r *wire.RemoteRequest) { r.Questions[0].AllowsMultiple = true }},
		{"secret", func(r *wire.RemoteRequest) { r.Questions[0].IsSecret = true }},
		{"three choices", func(r *wire.RemoteRequest) {
			r.Questions[0].Choices = append(r.Questions[0].Choices, wire.RemoteQuestionChoice{Label: "其他"})
		}},
		{"no choices", func(r *wire.RemoteRequest) { r.Questions[0].Choices = nil }},
		{"two questions", func(r *wire.RemoteRequest) {
			r.Questions = append(r.Questions, wire.RemoteQuestion{ID: "q-2", Choices: r.Questions[0].Choices})
		}},
		{"command kind", func(r *wire.RemoteRequest) { r.Kind = string(harness.RequestKindCommand) }},
		{"empty label", func(r *wire.RemoteRequest) { r.Questions[0].Choices[0].Label = "  " }},
		{"empty request id", func(r *wire.RemoteRequest) { r.RequestID = "" }},
		{"empty question id", func(r *wire.RemoteRequest) { r.Questions[0].ID = " " }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := answerableQuestionRequest()
			tc.mutate(&req)
			if target := answerTargetFor(req); target != nil {
				t.Fatalf("expected nil target, got %+v", target)
			}
		})
	}
}

func TestInlineAnswerTargetMatchesAnswerTargetFor(t *testing.T) {
	params := wire.RequestsChangedParams{ThreadID: "t-1", Requests: []wire.RemoteRequest{answerableQuestionRequest()}}
	want := answerTargetFor(params.Requests[0])
	if want == nil {
		t.Fatal("expected answerable target")
	}
	for name, got := range map[string]*PushAnswerTarget{
		"value":   inlineAnswerTarget("requests.changed", params),
		"pointer": inlineAnswerTarget("requests.changed", &params),
	} {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s form = %+v, want %+v", name, got, want)
		}
	}
	if got := inlineAnswerTarget("thread.summary", params); got != nil {
		t.Fatalf("expected nil for non-approval method, got %+v", got)
	}
}

func TestInlineAnswerTargetSupportsMapForm(t *testing.T) {
	params := wire.RequestsChangedParams{ThreadID: "t-1", Requests: []wire.RemoteRequest{answerableQuestionRequest()}}
	want := inlineAnswerTarget("requests.changed", params)
	if want == nil {
		t.Fatal("expected answerable target")
	}

	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	var form map[string]any
	if err := json.Unmarshal(raw, &form); err != nil {
		t.Fatalf("unmarshal params: %v", err)
	}
	got := inlineAnswerTarget("requests.changed", form)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("map form = %+v, want %+v", got, want)
	}

	if got := inlineAnswerTarget("requests.changed", map[string]any{"requests": "bad"}); got != nil {
		t.Fatalf("expected nil for malformed map, got %+v", got)
	}
}

func TestInlineAnswerTargetRequiresSinglePendingRequest(t *testing.T) {
	first := answerableQuestionRequest()
	second := answerableQuestionRequest()
	second.RequestID = "req-2"
	params := wire.RequestsChangedParams{ThreadID: "t-1", Requests: []wire.RemoteRequest{first, second}}
	if got := inlineAnswerTarget("requests.changed", params); got != nil {
		t.Fatalf("expected nil when multiple requests are pending, got %+v", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		in    string
		limit int
		want  string
	}{
		{"中文", 5, "中文"},
		{"一二三四五六", 4, "一二三…"},
		{"一二三", 1, "…"},
		{"一二三", 0, ""},
	}
	for _, tc := range cases {
		if got := truncateRunes(tc.in, tc.limit); got != tc.want {
			t.Fatalf("truncateRunes(%q, %d) = %q, want %q", tc.in, tc.limit, got, tc.want)
		}
	}
}
