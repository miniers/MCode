package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

// BuiltinTools 返回需要访问审批方的内置交互工具。
func BuiltinTools(approver harness.Approver) []tools.Tool {
	return []tools.Tool{
		{
			Name:        "ask_user_question",
			Description: "Ask the user a question when a decision or additional information is needed to proceed. Call this tool instead of asking questions in the regular text message and ending your turn.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"question":{"type":"string","description":"The question to ask the user."},"options":{"type":"array","items":{"type":"string"},"maxItems":6,"description":"Optional list of choices for the user to select from (maximum 6 choices)."}},"required":["question"]}`),
			ReadOnly:    true,
			Run: func(ctx context.Context, _ tools.Env, raw json.RawMessage) (tools.Result, error) {
				var args struct {
					Question string   `json:"question"`
					Options  []string `json:"options"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return tools.Result{Text: err.Error(), IsError: true}, nil
				}
				if strings.TrimSpace(args.Question) == "" || len(args.Options) > 6 {
					return tools.Result{Text: "Invalid question or options", IsError: true}, nil
				}
				interactor, ok := approver.(harness.Interactor)
				if !ok || interactor == nil {
					return tools.Result{Text: "User interaction is unavailable", IsError: true}, nil
				}
				question := harness.Question{ID: "question", Prompt: args.Question, AllowsOther: true}
				for _, option := range args.Options {
					question.Choices = append(question.Choices, harness.QuestionChoice{Label: option})
				}
				req := harness.ApprovalRequest{ToolName: "ask_user_question", Input: raw, Kind: harness.RequestKindQuestion, Questions: []harness.Question{question}}
				response, err := interactor.Interact(ctx, req)
				harness.AckDelivery(approver, req, err)
				if err != nil {
					return tools.Result{}, err
				}
				text := "The user did not answer."
				if answers := response.Answers[question.ID]; len(answers) > 0 && strings.TrimSpace(answers[0]) != "" && !response.Skipped {
					text = answers[0]
				}
				return tools.Result{Text: text}, nil
			},
		},
		{
			Name:        "todo_write",
			Description: "Plan and track progress on multi-step tasks. Pass the complete list on every call; it replaces the previous list. Only one item may be in_progress at a time. Mark each item completed as soon as it is done. Do not use this tool for simple one- or two-step tasks.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"todos":{"type":"array","items":{"type":"object","properties":{"content":{"type":"string","description":"The task to complete."},"status":{"type":"string","enum":["pending","in_progress","completed"]}},"required":["content","status"]}}},"required":["todos"]}`),
			ReadOnly:    true,
			Run: func(_ context.Context, _ tools.Env, raw json.RawMessage) (tools.Result, error) {
				var args struct {
					Todos []struct {
						Content string `json:"content"`
						Status  string `json:"status"`
					} `json:"todos"`
				}
				if err := json.Unmarshal(raw, &args); err != nil {
					return tools.Result{Text: err.Error(), IsError: true}, nil
				}
				if args.Todos == nil {
					return tools.Result{Text: "todos is required", IsError: true}, nil
				}
				completed := 0
				for _, todo := range args.Todos {
					if todo.Status == "completed" {
						completed++
					}
				}
				return tools.Result{Text: fmt.Sprintf("Todos updated: %d/%d completed", completed, len(args.Todos))}, nil
			},
		},
	}
}
