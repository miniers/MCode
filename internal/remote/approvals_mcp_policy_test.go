package remote

import (
	"context"
	"testing"
	"time"

	"mcode/internal/agentconf"
	"mcode/internal/harness"
	"mcode/internal/remote/wire"
)

// TestApprovals_MCPToolAutoApproveSkipsPrompt 覆盖：命中免审批策略的 MCP 工具直接放行，
// 不产生任何待处理审批请求。
func TestApprovals_MCPToolAutoApproveSkipsPrompt(t *testing.T) {
	dataDir := t.TempDir()
	if err := agentconf.SaveMCPToolPolicy(agentconf.MCPToolPolicyPath(dataDir), agentconf.MCPToolPolicy{
		AutoApprove: map[string]map[string]bool{"demo": {"echo": true}},
	}); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	defer hub.Close()

	backend := NewMemoryBackend(hub)
	defer backend.Close()
	backend.SetDataDir(dataDir)

	ctx := context.Background()
	threadID := "00000000-0000-0000-0000-0000000000b1"
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "MCP 免审批"}, nil)

	var (
		decision   harness.Decision
		approveErr error
	)
	mock := &mockHarness{
		runFunc: func(ctx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			decision, approveErr = turn.Approver.Approve(ctx, harness.ApprovalRequest{
				ToolID:   "call_mcp_001",
				ToolName: "mcp__demo__echo",
				Summary:  "mcp__demo__echo",
			})
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", harness.ModeAgent)

	conn := newMockStreamConn()
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "调用 MCP 工具",
		ActionID: "00000000-0000-0000-0000-0000000000b2",
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}

	waitThreadIdle(t, backend, ctx, threadID)

	if approveErr != nil {
		t.Fatalf("免审批工具不应返回错误: %v", approveErr)
	}
	if decision != harness.DecisionAllow {
		t.Fatalf("expected DecisionAllow, got %v", decision)
	}

	listRes, err := backend.ListRequests(ctx, threadID)
	if err != nil {
		t.Fatalf("ListRequests failed: %v", err)
	}
	if len(listRes.Requests) != 0 {
		t.Fatalf("免审批工具不应产生待处理请求, got %d", len(listRes.Requests))
	}
}

// TestMCPToolAutoApproved 覆盖免审批判定的边界：策略命中、未命中、非 MCP 工具名、无数据目录。
func TestMCPToolAutoApproved(t *testing.T) {
	dataDir := t.TempDir()

	if mcpToolAutoApproved(dataDir, "mcp__demo__echo") {
		t.Fatal("策略文件不存在时不应命中")
	}
	if err := agentconf.SaveMCPToolPolicy(agentconf.MCPToolPolicyPath(dataDir), agentconf.MCPToolPolicy{
		Disabled:    map[string]map[string]bool{"demo": {"danger": true}},
		AutoApprove: map[string]map[string]bool{"demo": {"echo": true}},
	}); err != nil {
		t.Fatalf("保存策略失败: %v", err)
	}

	if !mcpToolAutoApproved(dataDir, "mcp__demo__echo") {
		t.Fatal("命中免审批策略的工具应放行")
	}
	if mcpToolAutoApproved(dataDir, "mcp__demo__danger") {
		t.Fatal("仅禁用的工具不应命中免审批")
	}
	if mcpToolAutoApproved(dataDir, "mcp__demo__other") {
		t.Fatal("未配置的工具不应命中免审批")
	}
	if mcpToolAutoApproved(dataDir, "mcp__other__echo") {
		t.Fatal("其他服务的工具不应命中免审批")
	}
	if mcpToolAutoApproved(dataDir, "Bash") {
		t.Fatal("非 MCP 工具名不应命中免审批")
	}
	if mcpToolAutoApproved("", "mcp__demo__echo") {
		t.Fatal("无数据目录时不应命中免审批")
	}
}
