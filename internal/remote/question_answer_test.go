package remote

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// questionAnswerMessages 取会话历史里所有问答记录消息。
func questionAnswerMessages(t *testing.T, backend *MemoryBackend, threadID string) []wire.ThreadMessage {
	t.Helper()
	page, err := backend.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	var out []wire.ThreadMessage
	for _, m := range page.Messages {
		if m.Kind != nil && *m.Kind == ThreadMessageKindQuestionAnswer {
			out = append(out, m)
		}
	}
	return out
}

// decodeQuestionAnswer 解析问答记录消息的正文。
func decodeQuestionAnswer(t *testing.T, msg wire.ThreadMessage) questionAnswerRecord {
	t.Helper()
	var record questionAnswerRecord
	if err := json.Unmarshal([]byte(msg.Text), &record); err != nil {
		t.Fatalf("问答记录正文不是合法 JSON: %v (%s)", err, msg.Text)
	}
	return record
}

func TestQuestionAnswerMessageRecorded(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000091"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000811", harness.ApprovalRequest{
		ToolName: "AskUserQuestion",
		Kind:     harness.RequestKindQuestion,
		Questions: []harness.Question{
			{ID: "q2", Prompt: "要写注释吗？"},
			{ID: "q1", Header: "库", Prompt: "用哪个库？", Choices: []harness.QuestionChoice{{Label: "A"}, {Label: "B"}}},
		},
	})

	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		TurnID:    req.TurnID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		Answers:   json.RawMessage(`{"q1":["A"],"q2":["要"]}`),
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)

	msgs := questionAnswerMessages(t, backend, threadID)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 question_answer message, got %d", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != "system" {
		t.Errorf("expected system role, got %q", msg.Role)
	}
	if msg.TurnID == nil || req.TurnID == nil || *msg.TurnID != *req.TurnID {
		t.Errorf("expected turnID %v, got %v", req.TurnID, msg.TurnID)
	}
	if msg.CreatedAt == "" {
		t.Error("expected createdAt to be set")
	}
	if _, err := time.Parse(time.RFC3339Nano, msg.CreatedAt); err != nil {
		t.Errorf("createdAt is not RFC3339Nano: %v", err)
	}

	// TurnIndex 与提问所在回合一致：本轮用户消息的回合索引。
	page, err := backend.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: threadID})
	if err != nil {
		t.Fatalf("ThreadHistory failed: %v", err)
	}
	turnIndex := -1
	for _, m := range page.Messages {
		if m.Role == "user" {
			turnIndex = m.TurnIndex
		}
	}
	if turnIndex >= 0 && msg.TurnIndex != turnIndex {
		t.Errorf("expected turnIndex %d, got %d", turnIndex, msg.TurnIndex)
	}

	record := decodeQuestionAnswer(t, msg)
	if record.RequestID != req.RequestID {
		t.Errorf("expected requestID %q, got %q", req.RequestID, record.RequestID)
	}
	if record.Skipped {
		t.Error("expected skipped to be false")
	}
	if len(record.Items) != 2 {
		t.Fatalf("expected 2 items, got %+v", record.Items)
	}
	// 题目顺序与请求里一致（q2 在前），不按 answers 的键顺序。
	if record.Items[0].Question != "要写注释吗？" {
		t.Errorf("unexpected first question: %q", record.Items[0].Question)
	}
	if len(record.Items[0].Answers) != 1 || record.Items[0].Answers[0] != "要" {
		t.Errorf("unexpected first answers: %+v", record.Items[0].Answers)
	}
	if record.Items[1].Question != "库 用哪个库？" {
		t.Errorf("unexpected second question: %q", record.Items[1].Question)
	}
	if len(record.Items[1].Answers) != 1 || record.Items[1].Answers[0] != "A" {
		t.Errorf("unexpected second answers: %+v", record.Items[1].Answers)
	}
}

func TestQuestionAnswerTextEmptyAnswers(t *testing.T) {
	req := wire.RemoteRequest{
		RequestID: "req-1",
		Questions: []wire.RemoteQuestion{
			{ID: "q1", Header: "库", Prompt: "用哪个库？"},
			{ID: "q2", Prompt: "要写注释吗？"},
		},
	}

	var record questionAnswerRecord
	if err := json.Unmarshal([]byte(questionAnswerText(req, map[string][]string{"q1": {"A"}}, false)), &record); err != nil {
		t.Fatalf("questionAnswerText is not valid JSON: %v", err)
	}
	if record.RequestID != "req-1" || record.Skipped {
		t.Errorf("unexpected record header: %+v", record)
	}
	if len(record.Items) != 2 {
		t.Fatalf("expected 2 items, got %+v", record.Items)
	}
	if len(record.Items[0].Answers) != 1 || record.Items[0].Answers[0] != "A" {
		t.Errorf("unexpected first answers: %+v", record.Items[0].Answers)
	}
	if record.Items[1].Answers == nil {
		t.Error("expected empty answers array for the unanswered question, got null")
	}
	if len(record.Items[1].Answers) != 0 {
		t.Errorf("expected no answers for the second question, got %+v", record.Items[1].Answers)
	}
}

func TestQuestionAnswerMessageSkipped(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000092"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000812", harness.ApprovalRequest{
		ToolName:  "AskUserQuestion",
		Kind:      harness.RequestKindQuestion,
		Questions: []harness.Question{{ID: "q1", Header: "确认", Prompt: "继续吗？"}},
	})

	skip := true
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		TurnID:    req.TurnID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		Skip:      &skip,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)

	msgs := questionAnswerMessages(t, backend, threadID)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 question_answer message, got %d", len(msgs))
	}
	record := decodeQuestionAnswer(t, msgs[0])
	if !record.Skipped {
		t.Error("expected skipped to be true")
	}
	if len(record.Items) != 1 || record.Items[0].Question != "确认 继续吗？" {
		t.Errorf("unexpected items: %+v", record.Items)
	}
	if len(record.Items[0].Answers) != 0 {
		t.Errorf("expected no answers when skipped, got %+v", record.Items[0].Answers)
	}
}

func TestQuestionAnswerMessageOnlyForQuestions(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000093"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000813", harness.ApprovalRequest{
		ToolName: "Bash",
		ToolID:   "tool-1",
		Kind:     harness.RequestKindCommand,
		Summary:  "Bash rm -rf build",
	})

	optAllow := "allow"
	if _, err := backend.RespondRequest(context.Background(), wire.RequestsRespondParams{
		ThreadID:  threadID,
		TurnID:    req.TurnID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		OptionID:  &optAllow,
	}); err != nil {
		t.Fatalf("RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)

	if msgs := questionAnswerMessages(t, backend, threadID); len(msgs) != 0 {
		t.Fatalf("approval requests must not record question_answer, got %d", len(msgs))
	}
}

func TestQuestionAnswerMessageWrittenOnce(t *testing.T) {
	threadID := "00000000-0000-0000-0000-000000000094"
	backend, req, resultCh := startInteract(t, threadID, "00000000-0000-0000-0000-000000000814", harness.ApprovalRequest{
		ToolName:  "AskUserQuestion",
		Kind:      harness.RequestKindQuestion,
		Questions: []harness.Question{{ID: "q1", Prompt: "继续吗？"}},
	})

	params := wire.RequestsRespondParams{
		ThreadID:  threadID,
		TurnID:    req.TurnID,
		RunID:     req.RunID,
		RequestID: req.RequestID,
		Answers:   json.RawMessage(`{"q1":["继续"]}`),
	}
	if _, err := backend.RespondRequest(context.Background(), params); err != nil {
		t.Fatalf("first RespondRequest failed: %v", err)
	}
	waitInteract(t, resultCh)

	// 重复答复会被拒绝（请求已处理），不应再写一条记录。
	if _, err := backend.RespondRequest(context.Background(), params); err == nil {
		t.Fatal("expected duplicate RespondRequest to fail")
	}

	// 送达重试路径可能再次调用写入：同一 requestID 只保留一条。
	backend.appendQuestionAnswer(threadID, req, map[string][]string{"q1": {"继续"}}, false)

	if msgs := questionAnswerMessages(t, backend, threadID); len(msgs) != 1 {
		t.Fatalf("expected exactly 1 question_answer message, got %d", len(msgs))
	}
}
