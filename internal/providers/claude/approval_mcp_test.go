package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"mcode/internal/harness"
)

type fakeApprover struct {
	decision harness.Decision
	err      error
	lastReq  harness.ApprovalRequest
}

func (f *fakeApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	f.lastReq = req
	return f.decision, f.err
}

func newTestApprovalServer(t *testing.T, approver harness.Approver) (*approvalServer, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server, err := startApprovalServer(ctx, approver)
	if err != nil {
		cancel()
		t.Fatalf("startApprovalServer failed: %v", err)
	}

	cleanup := func() {
		cancel()
		_ = server.close()
	}
	return server, cleanup
}

func postMCP(t *testing.T, serverURL, token string, body []byte) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, serverURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest failed: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("http request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body failed: %v", err)
	}
	return resp, respBody
}

func TestApprovalServer_Auth(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	// 1. 没有 token
	resp, _ := postMCP(t, server.url(), "", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401 without token, got %d", resp.StatusCode)
	}

	// 2. token 错误
	resp, _ = postMCP(t, server.url(), "wrong-token", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401 with wrong token, got %d", resp.StatusCode)
	}
}

func TestApprovalServer_MethodNotAllowed(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	req, err := http.NewRequest(http.MethodGet, server.url(), nil)
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+server.token())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for GET /mcp, got %d", resp.StatusCode)
	}
}

func TestApprovalServer_Initialize(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	t.Run("带 protocolVersion 参数", func(t *testing.T) {
		reqBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
		resp, body := postMCP(t, server.url(), server.token(), reqBody)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		var res struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Result  struct {
				ProtocolVersion string         `json:"protocolVersion"`
				Capabilities    map[string]any `json:"capabilities"`
				ServerInfo      struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"serverInfo"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}

		if res.ID != 1 {
			t.Errorf("expected id 1, got %d", res.ID)
		}
		if res.Result.ProtocolVersion != "2025-06-18" {
			t.Errorf("expected protocolVersion 2025-06-18, got %q", res.Result.ProtocolVersion)
		}
		if _, ok := res.Result.Capabilities["tools"]; !ok {
			t.Errorf("expected capabilities.tools to exist")
		}
		if res.Result.ServerInfo.Name != "mcode" || res.Result.ServerInfo.Version != "0.1.0" {
			t.Errorf("unexpected serverInfo: %+v", res.Result.ServerInfo)
		}
	})

	t.Run("不带 protocolVersion 默认使用 2025-06-18", func(t *testing.T) {
		reqBody := []byte(`{"jsonrpc":"2.0","id":2,"method":"initialize"}`)
		resp, body := postMCP(t, server.url(), server.token(), reqBody)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}

		var res struct {
			Result struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"result"`
		}
		if err := json.Unmarshal(body, &res); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if res.Result.ProtocolVersion != "2025-06-18" {
			t.Errorf("expected default protocolVersion 2025-06-18, got %q", res.Result.ProtocolVersion)
		}
	})
}

func TestApprovalServer_NotificationsInitialized(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	reqBody := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	resp, body := postMCP(t, server.url(), server.token(), reqBody)

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected status 202, got %d", resp.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("expected empty body for notification, got %q", string(body))
	}
}

func TestApprovalServer_ToolsList(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	reqBody := []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`)
	resp, body := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var res struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				InputSchema struct {
					Type       string                 `json:"type"`
					Properties map[string]interface{} `json:"properties"`
					Required   []string               `json:"required"`
				} `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}

	if len(res.Result.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(res.Result.Tools))
	}
	tool := res.Result.Tools[0]
	if tool.Name != "approve" {
		t.Errorf("expected tool name 'approve', got %q", tool.Name)
	}
	if tool.Description != "Ask the mcode user to approve a tool call" {
		t.Errorf("unexpected tool description: %q", tool.Description)
	}
	if tool.InputSchema.Type != "object" {
		t.Errorf("expected schema type 'object', got %q", tool.InputSchema.Type)
	}
	if _, ok := tool.InputSchema.Properties["tool_name"]; !ok {
		t.Errorf("schema missing property 'tool_name'")
	}
	if _, ok := tool.InputSchema.Properties["input"]; !ok {
		t.Errorf("schema missing property 'input'")
	}
	if _, ok := tool.InputSchema.Properties["tool_use_id"]; !ok {
		t.Errorf("schema missing property 'tool_use_id'")
	}

	reqMap := make(map[string]bool)
	for _, r := range tool.InputSchema.Required {
		reqMap[r] = true
	}
	if !reqMap["tool_name"] || !reqMap["input"] {
		t.Errorf("expected required to contain 'tool_name' and 'input', got %v", tool.InputSchema.Required)
	}
}

func TestApprovalServer_ToolsCall_Allow(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	inputJSON := `{"command":"git diff","timeout":30}`
	reqBody := []byte(fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"id": 4,
		"method": "tools/call",
		"params": {
			"name": "approve",
			"arguments": {
				"tool_name": "Bash",
				"tool_use_id": "call_999",
				"input": %s
			}
		}
	}`, inputJSON))

	resp, body := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// 1. 断言假 Approver 收到的字段
	if fake.lastReq.ToolID != "call_999" {
		t.Errorf("expected ToolID 'call_999', got %q", fake.lastReq.ToolID)
	}
	if fake.lastReq.ToolName != "Bash" {
		t.Errorf("expected ToolName 'Bash', got %q", fake.lastReq.ToolName)
	}

	var parsedReqInput map[string]any
	if err := json.Unmarshal(fake.lastReq.Input, &parsedReqInput); err != nil {
		t.Fatalf("unmarshal approver req input failed: %v", err)
	}
	var expectedInput map[string]any
	_ = json.Unmarshal([]byte(inputJSON), &expectedInput)
	if !reflect.DeepEqual(parsedReqInput, expectedInput) {
		t.Errorf("approver received input %+v, want %+v", parsedReqInput, expectedInput)
	}
	if !strings.HasPrefix(fake.lastReq.Summary, "Bash ") {
		t.Errorf("summary should start with 'Bash ', got %q", fake.lastReq.Summary)
	}

	// 2. 断言返回内容
	var res struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if len(res.Result.Content) != 1 || res.Result.Content[0].Type != "text" {
		t.Fatalf("unexpected content format: %+v", res.Result.Content)
	}

	var allowPayload struct {
		Behavior     string          `json:"behavior"`
		UpdatedInput json.RawMessage `json:"updatedInput"`
	}
	if err := json.Unmarshal([]byte(res.Result.Content[0].Text), &allowPayload); err != nil {
		t.Fatalf("unmarshal allow payload text failed: %v", err)
	}
	if allowPayload.Behavior != "allow" {
		t.Errorf("expected behavior 'allow', got %q", allowPayload.Behavior)
	}

	var updatedInputMap map[string]any
	if err := json.Unmarshal(allowPayload.UpdatedInput, &updatedInputMap); err != nil {
		t.Fatalf("unmarshal updatedInput failed: %v", err)
	}
	if !reflect.DeepEqual(updatedInputMap, expectedInput) {
		t.Errorf("updatedInput %+v != expected %+v", updatedInputMap, expectedInput)
	}
}

func TestApprovalServer_ToolsCall_Deny(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionDeny}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	reqBody := []byte(`{
		"jsonrpc": "2.0",
		"id": 5,
		"method": "tools/call",
		"params": {
			"name": "approve",
			"arguments": {
				"tool_name": "FileWrite",
				"tool_use_id": "call_deny_1",
				"input": {"file": "test.txt"}
			}
		}
	}`)

	resp, body := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var res struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if len(res.Result.Content) != 1 {
		t.Fatalf("expected 1 content element, got %d", len(res.Result.Content))
	}

	var denyPayload struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(res.Result.Content[0].Text), &denyPayload); err != nil {
		t.Fatalf("unmarshal deny payload text failed: %v", err)
	}
	if denyPayload.Behavior != "deny" {
		t.Errorf("expected behavior 'deny', got %q", denyPayload.Behavior)
	}
	if denyPayload.Message != "用户拒绝了该工具调用" {
		t.Errorf("expected message '用户拒绝了该工具调用', got %q", denyPayload.Message)
	}
}

func TestApprovalServer_ToolsCall_ApproverError(t *testing.T) {
	fake := &fakeApprover{err: errors.New("approver internal failure")}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	reqBody := []byte(`{
		"jsonrpc": "2.0",
		"id": 6,
		"method": "tools/call",
		"params": {
			"name": "approve",
			"arguments": {
				"tool_name": "FileDelete",
				"tool_use_id": "call_err_1",
				"input": {"file": "important.dat"}
			}
		}
	}`)

	resp, body := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var res struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	var denyPayload struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(res.Result.Content[0].Text), &denyPayload); err != nil {
		t.Fatalf("unmarshal deny payload text failed: %v", err)
	}
	if denyPayload.Behavior != "deny" {
		t.Errorf("expected behavior 'deny', got %q", denyPayload.Behavior)
	}
	if denyPayload.Message != "approver internal failure" {
		t.Errorf("expected error message 'approver internal failure', got %q", denyPayload.Message)
	}
}

func TestApprovalServer_UnknownMethod(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	reqBody := []byte(`{"jsonrpc":"2.0","id":7,"method":"resources/list"}`)
	resp, body := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var res struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &res); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if res.Error.Code != -32601 {
		t.Errorf("expected error code -32601, got %d", res.Error.Code)
	}
}

func TestApprovalServer_SummaryTruncation(t *testing.T) {
	fake := &fakeApprover{decision: harness.DecisionAllow}
	server, cleanup := newTestApprovalServer(t, fake)
	defer cleanup()

	// 构造超过 200 个字符的 input
	longStr := strings.Repeat("a", 300)
	reqBody := []byte(fmt.Sprintf(`{
		"jsonrpc": "2.0",
		"id": 8,
		"method": "tools/call",
		"params": {
			"name": "approve",
			"arguments": {
				"tool_name": "Echo",
				"tool_use_id": "call_trunc",
				"input": {"data": "%s"}
			}
		}
	}`, longStr))

	_, _ = postMCP(t, server.url(), server.token(), reqBody)

	// Summary 应该是 "Echo " + 截断到 200 字符的 input
	expectedInputTruncated := string([]rune(string(fake.lastReq.Input))[:200])
	expectedSummary := "Echo " + expectedInputTruncated
	if fake.lastReq.Summary != expectedSummary {
		t.Errorf("expected summary %q, got %q", expectedSummary, fake.lastReq.Summary)
	}
}

type fakeInteractor struct {
	fakeApprover
	resp harness.ApprovalResponse
}

func (f *fakeInteractor) Interact(ctx context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	f.lastReq = req
	return f.resp, f.err
}

// callApproveTool 发起一次 approve 工具调用并返回解析后的回包。
func callApproveTool(t *testing.T, server *approvalServer, toolName, inputJSON string) map[string]any {
	t.Helper()
	reqBody := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":%q,"tool_use_id":"call_q","input":%s}}}`, toolName, inputJSON))
	resp, body := postMCP(t, server.url(), server.token(), reqBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var res struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &res); err != nil || len(res.Result.Content) != 1 {
		t.Fatalf("unexpected response: %s (%v)", body, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(res.Result.Content[0].Text), &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}
	return payload
}

func TestApprovalServer_AskUserQuestion(t *testing.T) {
	input := `{"questions":[{"question":"用哪个库？","header":"库","options":[{"label":"A","description":"甲"},{"label":"B","description":""}],"multiSelect":true}]}`

	t.Run("提交答案", func(t *testing.T) {
		fake := &fakeInteractor{resp: harness.ApprovalResponse{
			Decision: harness.DecisionAllow,
			Answers:  map[string][]string{"用哪个库？": {"A", "B"}},
		}}
		server, cleanup := newTestApprovalServer(t, fake)
		defer cleanup()

		payload := callApproveTool(t, server, "AskUserQuestion", input)
		if fake.lastReq.Kind != harness.RequestKindQuestion {
			t.Fatalf("expected kind question, got %q", fake.lastReq.Kind)
		}
		want := []harness.Question{{
			ID:             "用哪个库？",
			Header:         "库",
			Prompt:         "用哪个库？",
			Choices:        []harness.QuestionChoice{{Label: "A", Detail: "甲"}, {Label: "B"}},
			AllowsMultiple: true,
			AllowsOther:    true,
		}}
		if !reflect.DeepEqual(fake.lastReq.Questions, want) {
			t.Errorf("questions = %+v, want %+v", fake.lastReq.Questions, want)
		}
		if payload["behavior"] != "allow" {
			t.Fatalf("expected allow, got %+v", payload)
		}
		updated, _ := payload["updatedInput"].(map[string]any)
		answers, _ := updated["answers"].(map[string]any)
		if answers["用哪个库？"] != "A, B" {
			t.Errorf("unexpected answers: %+v", updated["answers"])
		}
		if _, ok := updated["questions"]; !ok {
			t.Errorf("updatedInput should keep original questions")
		}
	})

	t.Run("跳过", func(t *testing.T) {
		fake := &fakeInteractor{resp: harness.ApprovalResponse{Decision: harness.DecisionDeny, Skipped: true}}
		server, cleanup := newTestApprovalServer(t, fake)
		defer cleanup()

		payload := callApproveTool(t, server, "AskUserQuestion", input)
		if payload["behavior"] != "deny" || payload["message"] != "用户跳过了提问，请自行判断" {
			t.Errorf("unexpected payload: %+v", payload)
		}
	})
}

func TestApprovalServer_ExitPlanMode(t *testing.T) {
	input := `{"plan":"# 计划\n1. 改代码"}`

	t.Run("批准", func(t *testing.T) {
		fake := &fakeInteractor{resp: harness.ApprovalResponse{Decision: harness.DecisionAllow}}
		server, cleanup := newTestApprovalServer(t, fake)
		defer cleanup()

		payload := callApproveTool(t, server, "ExitPlanMode", input)
		if fake.lastReq.Kind != harness.RequestKindPlan || fake.lastReq.Plan != "# 计划\n1. 改代码" {
			t.Fatalf("unexpected request: %+v", fake.lastReq)
		}
		updated, _ := payload["updatedInput"].(map[string]any)
		if payload["behavior"] != "allow" || updated["plan"] != "# 计划\n1. 改代码" {
			t.Errorf("unexpected payload: %+v", payload)
		}
	})

	t.Run("驳回带反馈", func(t *testing.T) {
		fake := &fakeInteractor{resp: harness.ApprovalResponse{Decision: harness.DecisionDeny, Feedback: "先补测试"}}
		server, cleanup := newTestApprovalServer(t, fake)
		defer cleanup()

		payload := callApproveTool(t, server, "ExitPlanMode", input)
		if payload["behavior"] != "deny" || payload["message"] != "用户驳回了计划：先补测试" {
			t.Errorf("unexpected payload: %+v", payload)
		}
	})
}
