package remote

import (
	"context"
	"encoding/json"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

func planDecisionMessages(t *testing.T, backend *MemoryBackend, threadID string) []wire.ThreadMessage {
	t.Helper()
	page, err := backend.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var out []wire.ThreadMessage
	for _, m := range page.Messages {
		if m.Kind != nil && *m.Kind == ThreadMessageKindPlanDecision {
			out = append(out, m)
		}
	}
	return out
}

func decodePlanDecision(t *testing.T, msg wire.ThreadMessage) planDecisionRecord {
	t.Helper()
	var record planDecisionRecord
	if err := json.Unmarshal([]byte(msg.Text), &record); err != nil {
		t.Fatalf("计划裁决正文不是合法 JSON: %v (%s)", err, msg.Text)
	}
	return record
}

func TestPlanDecisionMessageApproved(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000095"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000815", harness.ApprovalRequest{
		ToolName: "ExitPlanMode",
		Kind:     harness.RequestKindPlan,
		Plan:     "# 计划\n1. 实现功能",
	})
	allow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID: threadID, TurnID: req.TurnID, RunID: req.RunID, RequestID: req.RequestID, OptionID: &allow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)

	msgs := planDecisionMessages(t, backend, threadID)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 plan_decision message, got %d", len(msgs))
	}
	record := decodePlanDecision(t, msgs[0])
	if record.RequestID != req.RequestID || record.Plan != "# 计划\n1. 实现功能" {
		t.Errorf("unexpected plan record: %+v", record)
	}
	if !record.Approved || record.Feedback != "" {
		t.Errorf("unexpected approval result: %+v", record)
	}
}

func TestPlanDecisionMessageRejectedWithFeedback(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000096"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000816", harness.ApprovalRequest{
		ToolName: "ExitPlanMode",
		Kind:     harness.RequestKindPlan,
		Plan:     "# 计划",
	})
	deny := "deny"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID: threadID, TurnID: req.TurnID, RunID: req.RunID, RequestID: req.RequestID,
		OptionID: &deny, Answers: json.RawMessage(`{"feedback":["  请先补测试  "]}`),
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)

	msgs := planDecisionMessages(t, backend, threadID)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 plan_decision message, got %d", len(msgs))
	}
	record := decodePlanDecision(t, msgs[0])
	if record.Approved || record.Feedback != "请先补测试" {
		t.Errorf("unexpected rejection result: %+v", record)
	}
}

func TestPlanDecisionMessageWrittenOnce(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000097"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000817", harness.ApprovalRequest{
		ToolName: "ExitPlanMode", Kind: harness.RequestKindPlan, Plan: "# 计划",
	})
	allow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID: threadID, RunID: req.RunID, RequestID: req.RequestID, OptionID: &allow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)

	backend.appendPlanDecision(threadID, req, allow, nil)
	if msgs := planDecisionMessages(t, backend, threadID); len(msgs) != 1 {
		t.Fatalf("expected exactly 1 plan_decision message, got %d", len(msgs))
	}
}

func TestQuestionDoesNotWritePlanDecision(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000098"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000818", harness.ApprovalRequest{
		ToolName:  "AskUserQuestion",
		Kind:      harness.RequestKindQuestion,
		Questions: []harness.Question{{ID: "q1", Prompt: "继续吗？"}},
	})
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID: threadID, RunID: req.RunID, RequestID: req.RequestID,
		Answers: json.RawMessage(`{"q1":["继续"]}`),
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)
	if msgs := planDecisionMessages(t, backend, threadID); len(msgs) != 0 {
		t.Fatalf("question requests must not record plan_decision, got %d", len(msgs))
	}
}
