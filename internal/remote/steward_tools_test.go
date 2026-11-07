package remote

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/remote/wire"
	"mcode/internal/store"
)

// 测试用的会话 ID：一个被记成总管，另一个是普通会话。
const (
	stewardTestThreadID = "00000000-0000-0000-0000-0000000000c1"
	regularTestThreadID = "00000000-0000-0000-0000-0000000000c2"
	topicTestThreadID   = "00000000-0000-0000-0000-0000000000c3"
)

// newStewardTestBackend 构造挂上真实 store 的后端，并把 stewardThreadID 写进总管设置键；
// 传空串表示不写，用来覆盖「设置键为空时没有任何豁免」。
func newStewardTestBackend(t *testing.T, stewardThreadID string) *MemoryBackend {
	t.Helper()

	hub := NewStreamHub(StreamHubOptions{Interval: 5 * time.Millisecond})
	t.Cleanup(hub.Close)
	backend := NewMemoryBackend(hub)
	t.Cleanup(backend.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "steward-tools.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := backend.AttachStore(context.Background(), st); err != nil {
		t.Fatalf("AttachStore: %v", err)
	}
	if stewardThreadID != "" {
		if err := st.SetSetting(context.Background(), stewardThreadSettingKey, stewardThreadID); err != nil {
			t.Fatalf("SetSetting: %v", err)
		}
	}
	return backend
}

// TestStewardBuiltinMCPTool 覆盖豁免判定本身：只有「总管的会话 + 内置 mcode 服务器」才算命中。
func TestStewardBuiltinMCPTool(t *testing.T) {
	ctx := context.Background()
	backend := newStewardTestBackend(t, stewardTestThreadID)

	cases := []struct {
		name     string
		threadID string
		toolName string
		want     bool
	}{
		{"总管调用专属工具", stewardTestThreadID, "mcp__mcode__attention_list", true},
		{"总管调用内置只读工具", stewardTestThreadID, "mcp__mcode__kanban_read_board", true},
		{"总管调用其他服务器的工具", stewardTestThreadID, "mcp__demo__echo", false},
		{"总管调用写文件工具", stewardTestThreadID, "write_file", false},
		{"总管执行命令", stewardTestThreadID, "Bash", false},
		{"普通会话调用内置服务器工具", regularTestThreadID, "mcp__mcode__attention_list", false},
		{"会话 ID 为空", "", "mcp__mcode__attention_list", false},
	}
	for _, tc := range cases {
		if got := backend.stewardBuiltinMCPTool(ctx, tc.threadID, tc.toolName); got != tc.want {
			t.Errorf("%s: stewardBuiltinMCPTool(%q, %q) = %v, want %v", tc.name, tc.threadID, tc.toolName, got, tc.want)
		}
	}
}

// TestStewardBuiltinMCPToolWithoutSetting 覆盖设置键为空：总管的会话也拿不到任何豁免。
func TestStewardBuiltinMCPToolWithoutSetting(t *testing.T) {
	backend := newStewardTestBackend(t, "")
	if backend.stewardBuiltinMCPTool(context.Background(), stewardTestThreadID, "mcp__mcode__attention_list") {
		t.Fatal("设置键为空时不应有任何豁免")
	}
}

// setTopicThreads 把话题表写进设置，只带 stewardTopicThread 认的 threadId 字段。
func setTopicThreads(t *testing.T, backend *MemoryBackend, threadIDs ...string) {
	t.Helper()

	type topicEntry struct {
		ThreadID string `json:"threadId"`
	}
	topics := make([]topicEntry, 0, len(threadIDs))
	for _, id := range threadIDs {
		topics = append(topics, topicEntry{ThreadID: id})
	}
	raw, err := json.Marshal(topics)
	if err != nil {
		t.Fatalf("序列化话题表: %v", err)
	}
	if err := backend.Store().SetSetting(context.Background(), stewardTopicsSettingKey, string(raw)); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}
}

// TestStewardTopicThreadBuiltinMCPTool 覆盖话题会话的豁免：话题表里的会话调用内置 MCP 工具
// 同样放行，普通会话与非内置服务器的工具仍然被拒。
func TestStewardTopicThreadBuiltinMCPTool(t *testing.T) {
	ctx := context.Background()
	backend := newStewardTestBackend(t, stewardTestThreadID)
	setTopicThreads(t, backend, topicTestThreadID)

	cases := []struct {
		name     string
		threadID string
		toolName string
		want     bool
	}{
		{"话题会话调用内置只读工具", topicTestThreadID, "mcp__mcode__kanban_read_board", true},
		{"话题会话调用专属工具", topicTestThreadID, "mcp__mcode__steward_update_brief", true},
		{"话题会话调用其他服务器的工具", topicTestThreadID, "mcp__demo__echo", false},
		{"普通会话调用内置服务器工具", regularTestThreadID, "mcp__mcode__kanban_read_board", false},
	}
	for _, tc := range cases {
		if got := backend.stewardBuiltinMCPTool(ctx, tc.threadID, tc.toolName); got != tc.want {
			t.Errorf("%s: stewardBuiltinMCPTool(%q, %q) = %v, want %v", tc.name, tc.threadID, tc.toolName, got, tc.want)
		}
	}
}

// TestStewardTopicThreadSkipsApproval 覆盖话题会话在只读档下的审批主路径：直接放行且不产生待处理请求。
func TestStewardTopicThreadSkipsApproval(t *testing.T) {
	backend := newStewardTestBackend(t, stewardTestThreadID)
	setTopicThreads(t, backend, topicTestThreadID)
	probe := probeApproval(t, backend, topicTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
		ToolID:   "call_topic_001",
		ToolName: "mcp__mcode__kanban_read_board",
		Summary:  "mcp__mcode__kanban_read_board",
		Kind:     harness.RequestKindTool,
	}, 2*time.Second)

	if !probe.allowed {
		t.Fatal("话题会话调用内置 MCP 工具应命中豁免")
	}
	if probe.decision != harness.DecisionAllow {
		t.Fatalf("expected DecisionAllow, got %v", probe.decision)
	}
	if probe.pendingSeen {
		t.Fatal("豁免生效时不应产生待处理审批请求")
	}
}

// TestStewardTopicThreadExemptionIsNarrow 覆盖话题会话豁免的边界：普通会话在只读档下仍然走审批。
func TestStewardTopicThreadExemptionIsNarrow(t *testing.T) {
	backend := newStewardTestBackend(t, stewardTestThreadID)
	setTopicThreads(t, backend, topicTestThreadID)
	probe := probeApproval(t, backend, regularTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
		ToolID:   "call_regular_topic_001",
		ToolName: "mcp__mcode__kanban_read_board",
		Summary:  "mcp__mcode__kanban_read_board",
		Kind:     harness.RequestKindTool,
	}, 150*time.Millisecond)

	if probe.allowed {
		t.Fatal("普通会话不应命中话题豁免")
	}
	if probe.decision == harness.DecisionAllow {
		t.Fatal("普通会话不应直接放行")
	}
	if !probe.pendingSeen {
		t.Fatal("普通会话应仍然走审批")
	}
}

// TestStewardBuiltinMCPToolWithoutStore 覆盖未挂载 store：豁免一律不生效。
func TestStewardBuiltinMCPToolWithoutStore(t *testing.T) {
	hub := NewStreamHub(StreamHubOptions{})
	defer hub.Close()
	backend := NewMemoryBackend(hub)
	defer backend.Close()

	if backend.stewardBuiltinMCPTool(context.Background(), stewardTestThreadID, "mcp__mcode__attention_list") {
		t.Fatal("未挂载 store 时不应有任何豁免")
	}
}

// approvalProbe 是一次审批探测的结果。
type approvalProbe struct {
	decision    harness.Decision
	allowed     bool // 审批方（harness.MCPToolAllower）给出的豁免结论
	pendingSeen bool // 是否出现过待处理审批请求
}

// probeApproval 在指定会话上跑一轮，期间发起一次审批请求：批准与拒绝都用同一个探针，
// 靠 timeout 决定是等答复还是等超时取消。
func probeApproval(t *testing.T, backend *MemoryBackend, threadID string, mode harness.PermissionMode, req harness.ApprovalRequest, timeout time.Duration) approvalProbe {
	t.Helper()

	ctx := context.Background()
	var probe approvalProbe
	mock := &mockHarness{
		runFunc: func(runCtx context.Context, turn harness.Turn, emit func(harness.Event)) error {
			approveCtx, cancel := context.WithTimeout(runCtx, timeout)
			defer cancel()

			done := make(chan struct{})
			go func() {
				defer close(done)
				probe.decision, _ = turn.Approver.Approve(approveCtx, req)
			}()

			// 最多观察 200ms：豁免生效时不会产生待处理请求，走审批时必然出现一条。
			for i := 0; i < 40; i++ {
				if res, err := backend.ListRequests(runCtx, threadID); err == nil && len(res.Requests) > 0 {
					probe.pendingSeen = true
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			<-done
			probe.allowed = harness.MCPToolAllowed(runCtx, turn.Approver, req.ToolName)
			return nil
		},
	}
	backend.SetHarness(mock, "/workspace", mode)
	backend.AddThread(wire.ThreadSummary{ID: threadID, Title: "总管审批"}, nil)

	conn := newMockStreamConn()
	if _, err := backend.Subscribe(ctx, conn, threadID); err != nil {
		t.Fatalf("Subscribe failed: %v", err)
	}
	if _, err := backend.SendMessage(ctx, wire.SendMessageParams{
		ThreadID: threadID,
		Text:     "分诊",
		ActionID: newUUID(),
	}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitThreadIdle(t, backend, ctx, threadID)
	return probe
}

// TestStewardBuiltinMCPToolSkipsApproval 覆盖主路径：总管在只读档调用内置 MCP 工具直接放行，
// 既不产生待处理请求，也不打扰用户。
func TestStewardBuiltinMCPToolSkipsApproval(t *testing.T) {
	backend := newStewardTestBackend(t, stewardTestThreadID)
	probe := probeApproval(t, backend, stewardTestThreadID, harness.ModeReadOnly, harness.ApprovalRequest{
		ToolID:   "call_steward_001",
		ToolName: "mcp__mcode__attention_list",
		Summary:  "mcp__mcode__attention_list",
		Kind:     harness.RequestKindTool,
	}, 2*time.Second)

	if !probe.allowed {
		t.Fatal("总管调用内置 MCP 工具应命中豁免")
	}
	if probe.decision != harness.DecisionAllow {
		t.Fatalf("expected DecisionAllow, got %v", probe.decision)
	}
	if probe.pendingSeen {
		t.Fatal("豁免生效时不应产生待处理审批请求")
	}
}

// TestStewardBuiltinMCPToolExemptionIsNarrow 覆盖豁免的边界：其他服务器、写文件、执行命令，
// 以及普通会话在只读档下的内置 MCP 工具，都不放行，行为与改动前一致。
func TestStewardBuiltinMCPToolExemptionIsNarrow(t *testing.T) {
	cases := []struct {
		name     string
		threadID string
		req      harness.ApprovalRequest
	}{
		{
			name:     "总管调用其他 MCP 服务器",
			threadID: stewardTestThreadID,
			req:      harness.ApprovalRequest{ToolID: "call_other_001", ToolName: "mcp__demo__echo", Summary: "mcp__demo__echo", Kind: harness.RequestKindTool},
		},
		{
			name:     "总管写文件",
			threadID: stewardTestThreadID,
			req:      harness.ApprovalRequest{ToolID: "call_write_001", ToolName: "write_file", Summary: "write to /workspace/main.go", Kind: harness.RequestKindFileChange},
		},
		{
			name:     "总管执行命令",
			threadID: stewardTestThreadID,
			req:      harness.ApprovalRequest{ToolID: "call_bash_001", ToolName: "Bash", Summary: "$ rm -rf build", Kind: harness.RequestKindCommand},
		},
		{
			name:     "普通会话调用内置服务器工具",
			threadID: regularTestThreadID,
			req:      harness.ApprovalRequest{ToolID: "call_regular_001", ToolName: "mcp__mcode__attention_list", Summary: "mcp__mcode__attention_list", Kind: harness.RequestKindTool},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backend := newStewardTestBackend(t, stewardTestThreadID)
			probe := probeApproval(t, backend, tc.threadID, harness.ModeReadOnly, tc.req, 150*time.Millisecond)

			if probe.allowed {
				t.Fatalf("%s 不应命中豁免", tc.name)
			}
			if probe.decision == harness.DecisionAllow {
				t.Fatalf("%s 不应直接放行", tc.name)
			}
			if !probe.pendingSeen {
				t.Fatalf("%s 应仍然走审批", tc.name)
			}
		})
	}
}
