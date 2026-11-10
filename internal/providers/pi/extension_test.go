package pi

import (
	"context"
	"strings"
	"testing"

	"mcode/internal/harness"
	"mcode/internal/procutil"
)

func TestParseGatePayload_Sensitive(t *testing.T) {
	t.Run("解析凭证敏感标记", func(t *testing.T) {
		title := `mcode-gate {"kind":"command","toolName":"bash","input":{"command":"cat .env"},"toolCallId":"tc1","sensitive":"credential"}`
		gate, ok := parseGatePayload(title)
		if !ok {
			t.Fatal("应成功解析 gatePayload")
		}
		if gate.Sensitive != "credential" {
			t.Fatalf("sensitive 期望 credential，得到 %q", gate.Sensitive)
		}
	})

	t.Run("解析控制面写入敏感标记", func(t *testing.T) {
		title := `mcode-gate {"kind":"fileChange","toolName":"write","input":{"path":"/root/.pi/agent/settings.json"},"toolCallId":"tc2","sensitive":"controlPlane"}`
		gate, ok := parseGatePayload(title)
		if !ok {
			t.Fatal("应成功解析 gatePayload")
		}
		if gate.Sensitive != "controlPlane" {
			t.Fatalf("sensitive 期望 controlPlane，得到 %q", gate.Sensitive)
		}
	})

	t.Run("无敏感标记时字段为空", func(t *testing.T) {
		title := `mcode-gate {"kind":"command","toolName":"bash","input":{"command":"ls"},"toolCallId":"tc3"}`
		gate, ok := parseGatePayload(title)
		if !ok {
			t.Fatal("应成功解析 gatePayload")
		}
		if gate.Sensitive != "" {
			t.Fatalf("普通调用 sensitive 应为空，得到 %q", gate.Sensitive)
		}
	})
}

func TestApprovalFromGate_SensitiveSummary(t *testing.T) {
	t.Run("凭证类敏感操作带标签", func(t *testing.T) {
		title := `mcode-gate {"kind":"command","toolName":"bash","input":{"command":"cat ~/.ssh/id_rsa"},"toolCallId":"tc1","sensitive":"credential"}`
		gate, ok := parseGatePayload(title)
		if !ok {
			t.Fatal("解析失败")
		}
		req := approvalFromGate(gate)
		if req.Sensitive != "credential" {
			t.Fatalf("Sensitive = %q", req.Sensitive)
		}
		if !strings.Contains(req.Summary, "cat ~/.ssh/id_rsa") {
			t.Fatalf("Summary 应包含原命令，得到 %q", req.Summary)
		}
	})

	t.Run("控制面类敏感操作带标签", func(t *testing.T) {
		title := `mcode-gate {"kind":"fileChange","toolName":"write","input":{"path":"/tmp/mcode-gate-1.json"},"toolCallId":"tc2","sensitive":"controlPlane"}`
		gate, ok := parseGatePayload(title)
		if !ok {
			t.Fatal("解析失败")
		}
		req := approvalFromGate(gate)
		if req.Sensitive != "controlPlane" {
			t.Fatalf("Sensitive = %q", req.Sensitive)
		}
		if !strings.Contains(req.Summary, "/tmp/mcode-gate-1.json") {
			t.Fatalf("Summary 应包含目标路径，得到 %q", req.Summary)
		}
	})

	t.Run("只读工具读取凭证摘要提取路径", func(t *testing.T) {
		title := `mcode-gate {"kind":"tool","toolName":"read","input":{"path":"~/.pi/agent/auth.json"},"toolCallId":"tc3","sensitive":"credential"}`
		gate, ok := parseGatePayload(title)
		if !ok {
			t.Fatal("解析失败")
		}
		req := approvalFromGate(gate)
		if req.Kind != harness.RequestKindTool {
			t.Fatalf("Kind 期望 tool，得到 %q", req.Kind)
		}
		if req.Summary != "~/.pi/agent/auth.json" || req.Sensitive != "credential" {
			t.Fatalf("Summary = %q, Sensitive = %q", req.Summary, req.Sensitive)
		}
	})

	t.Run("普通操作摘要保持原样不带标签", func(t *testing.T) {
		title := `mcode-gate {"kind":"command","toolName":"bash","input":{"command":"go version"},"toolCallId":"tc4"}`
		gate, ok := parseGatePayload(title)
		if !ok {
			t.Fatal("解析失败")
		}
		req := approvalFromGate(gate)
		if req.Summary != "go version" {
			t.Fatalf("普通调用 Summary 期望 go version，得到 %q", req.Summary)
		}
	})
}

func TestAnswerGate_SensitiveHandling(t *testing.T) {
	t.Run("敏感操作降级会话级放行为单次允许", func(t *testing.T) {
		eventLine := []byte(`{"type":"extension_ui_request","id":"req-s1","method":"select","params":{"title":"mcode-gate {\"kind\":\"fileChange\",\"toolName\":\"write\",\"input\":{\"path\":\"/etc/hosts\"},\"toolCallId\":\"tc1\",\"sensitive\":\"controlPlane\"}","options":["Allow","Deny"]}}`)
		event, err := parseEvent(eventLine)
		if err != nil {
			t.Fatal(err)
		}

		h := New(Options{})
		sink := &bufferWriteCloser{}
		interactor := &recordingInteractor{
			response: harness.ApprovalResponse{
				Decision: harness.DecisionAllow,
				Answers:  map[string][]string{"req-s1": {"Allow for session"}},
			},
		}
		h.handleExtensionUI(context.Background(), harness.Turn{Approver: interactor}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Allow" {
			t.Fatalf("敏感调用即使返回 Allow for session 也应被降级为 Allow，实际得到: %+v", response)
		}
	})

	t.Run("计划模式下只读凭证依然分发至审批方", func(t *testing.T) {
		eventLine := []byte(`{"type":"extension_ui_request","id":"req-s2","method":"select","params":{"title":"mcode-gate {\"kind\":\"tool\",\"toolName\":\"read\",\"input\":{\"path\":\"~/.ssh/id_rsa\"},\"toolCallId\":\"tc2\",\"sensitive\":\"credential\"}","options":["Allow","Deny"]}}`)
		event, err := parseEvent(eventLine)
		if err != nil {
			t.Fatal(err)
		}

		approver := &recordingApprover{decision: harness.DecisionAllow}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(),
			harness.Turn{Approver: approver, InteractionMode: "plan"}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Allow" {
			t.Fatalf("计划模式下只读工具经审批方放行后应回写 Allow: %+v", response)
		}
		if len(approver.requests) != 1 || approver.requests[0].ToolName != "read" {
			t.Fatalf("计划模式下只读凭证应正常进入审批流程: %+v", approver.requests)
		}
	})

	t.Run("计划模式下非只读敏感操作仍被直接拒绝且不打扰审批方", func(t *testing.T) {
		eventLine := []byte(`{"type":"extension_ui_request","id":"req-s3","method":"select","params":{"title":"mcode-gate {\"kind\":\"fileChange\",\"toolName\":\"write\",\"input\":{\"path\":\"/tmp/gate.json\"},\"toolCallId\":\"tc3\",\"sensitive\":\"controlPlane\"}","options":["Allow","Deny"]}}`)
		event, err := parseEvent(eventLine)
		if err != nil {
			t.Fatal(err)
		}

		approver := &recordingApprover{decision: harness.DecisionAllow}
		sink := &bufferWriteCloser{}
		New(Options{}).handleExtensionUI(context.Background(),
			harness.Turn{Approver: approver, InteractionMode: "plan"}, procutil.NewLineWriter(sink), event)

		response := decodeSingleRequest(t, sink.String())
		if response["value"] != "Deny" {
			t.Fatalf("计划模式下的写操作应直接被拒绝: %+v", response)
		}
		if len(approver.requests) != 0 {
			t.Fatalf("计划模式下的写操作不应打扰审批方: %+v", approver.requests)
		}
	})
}

func TestReadOnlyGateTools_AskUserQuestion(t *testing.T) {
	for _, name := range []string{"ask_user_question", "todo_write", "vision"} {
		if !readOnlyGateTools[name] {
			t.Fatalf("readOnlyGateTools 应包含 %s", name)
		}
		turn := harness.Turn{InteractionMode: "plan"}
		if planBlocksTool(turn, name) {
			t.Fatalf("plan 模式下 %s 应作为只读工具放行", name)
		}
	}
	if !planBlocksTool(harness.Turn{InteractionMode: "plan"}, "write") {
		t.Fatal("plan 模式下 write 应被拦截")
	}
}

// TestPlanBlocksTool_BuiltinMcpTools 校验计划模式下 mcode 自带 MCP 工具不算写操作：
// 它们的权限档由 MCP 服务端（steward 权限档）自己校验，而 edit/write/bash 与外部 MCP 工具仍然被拦。
func TestPlanBlocksTool_BuiltinMcpTools(t *testing.T) {
	turn := harness.Turn{InteractionMode: "plan"}
	for _, name := range []string{"mcp__mcode__kanban", "mcp__mcode__context_read_document", "mcp__mcode__automation_create"} {
		if planBlocksTool(turn, name) {
			t.Fatalf("plan 模式下 %s 应在 planBlocksTool 里放行", name)
		}
	}
	for _, name := range []string{"edit", "write", "bash", "mcp__other__run"} {
		if !planBlocksTool(turn, name) {
			t.Fatalf("plan 模式下 %s 应被拦截", name)
		}
	}
	if planBlocksTool(harness.Turn{InteractionMode: "build"}, "bash") {
		t.Fatal("非 plan 模式下 bash 不应被 planBlocksTool 拦截")
	}
}
