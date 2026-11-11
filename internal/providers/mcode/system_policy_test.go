package mcode

import (
	"context"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/harness"
)

func TestSystemPolicy(t *testing.T) {
	for _, custom := range []bool{false, true} {
		client := &fakeClient{responses: []*llm.Response{answer(), answer(), answer()}}
		opts := Options{}
		base := "你是一个 coding agent。"
		if custom {
			base = "custom system prompt"
			opts.SystemPrompt = func(harness.Turn, []string) string { return base }
		}
		h := harnessFor(client, opts)
		var carrier harness.SystemPolicyCarrier = h
		if !carrier.CarriesSystemPolicy("thread") {
			t.Fatal("mcode 应承载系统规则")
		}
		sessionID := ""
		for i, policy := range []string{"first policy", "updated policy", ""} {
			turn := harness.Turn{Prompt: "hello", SystemPolicy: policy, SessionID: sessionID}
			if err := h.Run(context.Background(), turn, func(e harness.Event) {
				if e.Type == harness.EventSession {
					sessionID = e.SessionID
				}
			}); err != nil {
				t.Fatal(err)
			}
			want := base
			if policy != "" {
				want += "\n\n" + policy
			}
			if got := client.requests[i].System; got != want {
				t.Fatalf("custom=%v policy=%q system=%q，预期 %q", custom, policy, got, want)
			}
		}
	}
}
