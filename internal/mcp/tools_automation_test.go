package mcp

import (
	"context"
	"net/http"
	"testing"
)

// TestAutomationCreateRecordsCreatorThread 验证经工具路径创建的任务记录来源会话。
func TestAutomationCreateRecordsCreatorThread(t *testing.T) {
	srv, s, _ := setupTestMCP(t)
	const threadID = "thread-creator-1"
	registerThread(t, s, threadID, "proj-test-1")

	status, res := postThreadRPC(t, srv, threadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_create",
			"arguments": map[string]any{
				"name":           "来源会话",
				"prompt":         "检查",
				"schedule_type":  "interval",
				"schedule_value": "3600",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if isError {
		t.Fatalf("创建自动化失败: %v", payload)
	}
	automationID, _ := payload["automation_id"].(string)
	if automationID == "" {
		t.Fatalf("响应缺少 automation_id: %v", payload)
	}

	def, err := srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.CreatorThreadID != threadID {
		t.Fatalf("CreatorThreadID 期望 %q, 实际为 %q", threadID, def.Spec.CreatorThreadID)
	}
}

func TestAutomationCheckParameter(t *testing.T) {
	srv, s, _ := setupTestMCP(t)
	const threadID = "thread-check"
	registerThread(t, s, threadID, "proj-test-1")
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		status, res := postThreadRPC(t, srv, threadID, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args},
		})
		if status != http.StatusOK {
			t.Fatalf("状态码: %d", status)
		}
		payload, isError := toolCallResult(t, res)
		if isError {
			t.Fatalf("调用失败: %v", payload)
		}
		return payload
	}
	created := call("automation_create", map[string]any{
		"name": "检查", "prompt": "检查", "schedule_type": "interval", "schedule_value": "3600", "check": "true",
	})
	id, _ := created["automation_id"].(string)
	for _, check := range []string{"true", "false", ""} {
		if check != "true" {
			call("automation_update", map[string]any{"automation_id": id, "check": check})
		}
		def, err := srv.getAutomation().Get(context.Background(), id)
		if err != nil || def.Spec.Check != check {
			t.Fatalf("检查命令期望 %q，实际 %q，错误 %v", check, def.Spec.Check, err)
		}
	}
}
