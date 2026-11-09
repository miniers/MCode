package agent

import (
	"context"
	"encoding/json"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

type readonlyCountingApprover struct {
	approvals    int
	interactions int
}

func (a *readonlyCountingApprover) Approve(context.Context, harness.ApprovalRequest) (harness.Decision, error) {
	a.approvals++
	return harness.DecisionAllow, nil
}

func (a *readonlyCountingApprover) Interact(context.Context, harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.interactions++
	return harness.ApprovalResponse{Decision: harness.DecisionAllow}, nil
}

func TestReadOnlyWriteToolRejectedBeforeApproval(t *testing.T) {
	client := &scriptClient{steps: []scriptStep{
		{response: reply("tool_use", call("1", "write", `{"path":"a"}`))},
		{response: reply("stop"), delta: llm.Delta{Type: "text", Text: "完成"}},
	}}
	tool := testTool("write")
	executed := false
	tool.Run = func(context.Context, tools.Env, json.RawMessage) (tools.Result, error) {
		executed = true
		return tools.Result{Text: "写入成功"}, nil
	}
	approver := &readonlyCountingApprover{}
	var text string
	var done bool
	err := Run(context.Background(), Options{Client: client, Tools: []tools.Tool{tool}, Mode: harness.ModeReadOnly, Approver: approver, Emit: func(event harness.Event) {
		if event.Type == harness.EventText {
			text += event.Text
		}
		if event.Type == harness.EventDone {
			done = true
		}
	}}, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	if executed || approver.approvals != 0 || approver.interactions != 0 {
		t.Fatalf("executed=%v approvals=%d interactions=%d", executed, approver.approvals, approver.interactions)
	}
	if len(client.requests) != 2 || len(client.requests[1].Messages) < 3 {
		t.Fatalf("模型请求不完整：%+v", client.requests)
	}
	result := client.requests[1].Messages[2]
	if result.Role != "tool" || !result.IsError {
		t.Fatalf("工具结果应为错误：%+v", result)
	}
	if text != "完成" || !done {
		t.Fatalf("后续文本或结束事件缺失：text=%q done=%v", text, done)
	}
}
