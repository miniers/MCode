package mcp

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDesignFinalizeToolVisibility(t *testing.T) {
	t.Parallel()
	srv, st, projDir := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")
	registerThread(t, st, "bee-design", "proj-test-1")
	registerThread(t, st, "bee-normal", "proj-test-1")

	if threadToolNames(t, srv, "bee-design")[designFinalizeToolName] {
		t.Fatal("未设置 SwarmHooks 时不应列出 design_finalize")
	}

	calledThread := ""
	hookErr := error(nil)
	srv.SetSwarm(&SwarmHooks{
		Available:      func(threadID string) bool { return threadID == "lead-1" },
		IsDesignBee:    func(threadID string) bool { return threadID == "bee-design" },
		FinalizeDesign: func(_ context.Context, beeThreadID string) error { calledThread = beeThreadID; return hookErr },
	})

	if !threadToolNames(t, srv, "bee-design")[designFinalizeToolName] {
		t.Fatal("设计 bee 应在 tools/list 中看到 design_finalize")
	}
	if threadToolNames(t, srv, "lead-1")[designFinalizeToolName] {
		t.Fatal("主会话不应看到 design_finalize")
	}
	if threadToolNames(t, srv, "bee-normal")[designFinalizeToolName] {
		t.Fatal("非设计 bee 不应看到 design_finalize")
	}

	// 共享端点无会话作用域，不应列出 design_finalize。
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if status != 200 {
		t.Fatalf("共享端点 tools/list 状态码异常: %d", status)
	}
	tools, _ := res["result"].(map[string]any)["tools"].([]any)
	for _, item := range tools {
		if name, _ := item.(map[string]any)["name"].(string); name == designFinalizeToolName {
			t.Fatal("共享端点不应列出 design_finalize")
		}
	}

	// 非设计 bee 直接调用应按未知工具拒绝。
	if text, isError := callThreadTool(t, srv, "bee-normal", designFinalizeToolName, map[string]any{}); !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("非设计 bee 调用 design_finalize 应返回 Unknown tool, 实际: isError=%v text=%q", isError, text)
	}
	if calledThread != "" {
		t.Fatalf("非法调用不应触发钩子，实际 calledThread=%q", calledThread)
	}

	// 设计 bee 调用成功转到钩子。
	if text, isError := callThreadTool(t, srv, "bee-design", designFinalizeToolName, map[string]any{}); isError {
		t.Fatalf("设计 bee 调用 design_finalize 失败: %s", text)
	}
	if calledThread != "bee-design" {
		t.Fatalf("钩子收到的线程 ID 异常: %q", calledThread)
	}

	// 钩子报错时透传失败。
	hookErr = errors.New("not design bee")
	if text, isError := callThreadTool(t, srv, "bee-design", designFinalizeToolName, map[string]any{}); !isError || !strings.Contains(text, "not design bee") {
		t.Fatalf("钩子报错时应返回错误，实际: isError=%v text=%q", isError, text)
	}

	if ReadOnlyTool("mcp__mcode__"+designFinalizeToolName, nil) {
		t.Fatal("design_finalize 应归类为非只读工具")
	}
}

func TestSwarmMessageTool(t *testing.T) {
	t.Parallel()
	srv, st, projDir := setupTestMCP(t)
	registerThread(t, st, "lead-1", "proj-test-1")
	registerThread(t, st, "bee-design", "proj-test-1")

	if threadToolNames(t, srv, "lead-1")[swarmMessageToolName] {
		t.Fatal("未设置 SwarmHooks 时不应列出 swarm_message")
	}

	var gotLead, gotBee, gotText string
	calls := 0
	var hookErr error
	srv.SetSwarm(&SwarmHooks{
		Available:   func(threadID string) bool { return threadID == "lead-1" },
		IsDesignBee: func(threadID string) bool { return threadID == "bee-design" },
		Message: func(_ context.Context, leadThreadID, bee, text string) error {
			calls++
			gotLead, gotBee, gotText = leadThreadID, bee, text
			return hookErr
		},
	})

	if !threadToolNames(t, srv, "lead-1")[swarmMessageToolName] {
		t.Fatal("主会话应在 tools/list 中看到 swarm_message")
	}
	if threadToolNames(t, srv, "bee-design")[swarmMessageToolName] {
		t.Fatal("bee 会话不应看到 swarm_message")
	}

	// 共享端点无会话作用域，不应列出 swarm_message。
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if status != 200 {
		t.Fatalf("共享端点 tools/list 状态码异常: %d", status)
	}
	tools, _ := res["result"].(map[string]any)["tools"].([]any)
	for _, item := range tools {
		if name, _ := item.(map[string]any)["name"].(string); name == swarmMessageToolName {
			t.Fatal("共享端点不应列出 swarm_message")
		}
	}

	// bee 调用应按未知工具拒绝。
	if text, isError := callThreadTool(t, srv, "bee-design", swarmMessageToolName, map[string]any{"bee": "bee-design", "message": "选 B"}); !isError || !strings.Contains(text, "Unknown tool") {
		t.Fatalf("bee 调用 swarm_message 应返回 Unknown tool, 实际: isError=%v text=%q", isError, text)
	}
	if calls != 0 {
		t.Fatalf("不可用会话调用不应触发钩子，实际 calls=%d", calls)
	}

	// 缺少参数报错且不调用钩子。
	if text, isError := callThreadTool(t, srv, "lead-1", swarmMessageToolName, map[string]any{"bee": " ", "message": "选 B"}); !isError || !strings.Contains(text, CodeInvalidArguments) {
		t.Fatalf("空 bee 参数应报参数错误, 实际: isError=%v text=%q", isError, text)
	}
	if text, isError := callThreadTool(t, srv, "lead-1", swarmMessageToolName, map[string]any{"bee": "Ada", "message": " "}); !isError || !strings.Contains(text, CodeInvalidArguments) {
		t.Fatalf("空 message 参数应报参数错误, 实际: isError=%v text=%q", isError, text)
	}
	if calls != 0 {
		t.Fatalf("参数无效时不应触发钩子，实际 calls=%d", calls)
	}

	// 主会话正常调用转发给钩子。
	if text, isError := callThreadTool(t, srv, "lead-1", swarmMessageToolName, map[string]any{"bee": "Ada", "message": "选 B，标题居中"}); isError {
		t.Fatalf("主会话调用 swarm_message 失败: %s", text)
	}
	if calls != 1 || gotLead != "lead-1" || gotBee != "Ada" || gotText != "选 B，标题居中" {
		t.Fatalf("钩子参数异常: calls=%d lead=%q bee=%q text=%q", calls, gotLead, gotBee, gotText)
	}

	hookErr = errors.New("该子代理不在待定稿状态")
	if text, isError := callThreadTool(t, srv, "lead-1", swarmMessageToolName, map[string]any{"bee": "Ada", "message": "选 B"}); !isError || !strings.Contains(text, "不在待定稿状态") {
		t.Fatalf("钩子返回错误时应透传给调用方，实际: isError=%v text=%q", isError, text)
	}

	if ReadOnlyTool("mcp__mcode__"+swarmMessageToolName, nil) {
		t.Fatal("swarm_message 应归类为非只读工具")
	}
}
