package openai

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"mcode/internal/harness"
)

// fakeMCPCall 记录假 MCP 服务收到的 tools/call。
type fakeMCPCall struct {
	name      string
	arguments string
	headers   http.Header
}

// fakeMCPServer 是最小可用的 Streamable HTTP MCP 服务，仅实现本轮需要的三个方法。
type fakeMCPServer struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  []fakeMCPCall
	listed int32
}

// newFakeMCPServer 启动假 MCP 服务，返回工具名与调用输出。
func newFakeMCPServer(t *testing.T, toolName, output string) *fakeMCPServer {
	t.Helper()
	fake := &fakeMCPServer{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		switch req.Method {
		case "initialize":
			writeMCPResult(w, req.ID, map[string]any{
				"protocolVersion": mcpProtocolVersion,
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]any{"name": "fake", "version": "1"},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			atomic.AddInt32(&fake.listed, 1)
			writeMCPResult(w, req.ID, map[string]any{
				"tools": []map[string]any{
					{
						"name":        toolName,
						"description": "Fake Kanban board reader",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"project_id": map[string]any{"type": "string"},
							},
						},
					},
				},
			})
		case "tools/call":
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			fake.mu.Lock()
			fake.calls = append(fake.calls, fakeMCPCall{
				name:      params.Name,
				arguments: string(params.Arguments),
				headers:   r.Header.Clone(),
			})
			fake.mu.Unlock()
			writeMCPResult(w, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": output}},
				"isError": false,
			})
		default:
			http.Error(w, "unexpected method", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// writeMCPResult 写出 JSON-RPC 成功响应。
func writeMCPResult(w http.ResponseWriter, id *int64, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func TestMCP_DiscoverAndCallTool(t *testing.T) {
	fake := newFakeMCPServer(t, "kanban_read_board", "board-ok")
	workDir := t.TempDir()

	h := New(Options{
		BaseURL:    "http://127.0.0.1:1/v1",
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: fake.server.Client(),
		BuiltinMCP: harness.BuiltinMCP{URL: fake.server.URL + "/mcp", Token: "thread-token"},
	})

	turn := harness.Turn{WorkDir: workDir, Mode: harness.ModeFullAccess}
	mcpTools := h.mcpToolsForTurn(context.Background(), turn)

	if len(mcpTools.tools) != 1 {
		t.Fatalf("expected 1 MCP tool, got %d", len(mcpTools.tools))
	}
	if got := mcpTools.tools[0].CallName; got != "mcp__mcode__kanban_read_board" {
		t.Fatalf("unexpected call name: %s", got)
	}

	defs := mcpTools.definitions()
	if len(defs) != 1 {
		t.Fatalf("expected 1 tool definition, got %d", len(defs))
	}
	if defs[0].Type != "function" || defs[0].Function.Name != "mcp__mcode__kanban_read_board" {
		t.Fatalf("unexpected definition: %+v", defs[0])
	}
	if defs[0].Function.Parameters["type"] != "object" {
		t.Fatalf("expected parameters from inputSchema, got %+v", defs[0].Function.Parameters)
	}

	call := toolCall{
		Index:     0,
		ID:        "call_mcp1",
		Name:      "mcp__mcode__kanban_read_board",
		Arguments: `{"project_id":"p-1"}`,
	}
	out, isErr := executeToolCallWithMCP(context.Background(), turn, call, nil, mcpTools)
	if isErr {
		t.Fatalf("unexpected error output: %s", out)
	}
	if out != "board-ok" {
		t.Fatalf("unexpected output: %s", out)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 tools/call, got %d", len(fake.calls))
	}
	if fake.calls[0].name != "kanban_read_board" {
		t.Errorf("unexpected upstream tool name: %s", fake.calls[0].name)
	}
	if !strings.Contains(fake.calls[0].arguments, `"project_id":"p-1"`) {
		t.Errorf("unexpected upstream arguments: %s", fake.calls[0].arguments)
	}
	if got := fake.calls[0].headers.Get("Authorization"); got != "Bearer thread-token" {
		t.Errorf("unexpected Authorization header: %q", got)
	}
	if got := fake.calls[0].headers.Get("X-Mcode-Workdir"); got != workDir {
		t.Errorf("unexpected X-Mcode-Workdir header: %q", got)
	}
}

func TestMCP_RequestIncludesMCPTools(t *testing.T) {
	fakeMCP := newFakeMCPServer(t, "kanban_read_board", "board-ok")

	var requestCount int32
	var mu sync.Mutex
	var bodies [][]byte

	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()

		count := atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if count == 1 {
			chunk := `{"id":"c1","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_mcp","type":"function","function":{"name":"mcp__mcode__kanban_read_board","arguments":"{\"project_id\":\"p-1\"}"}}]},"finish_reason":"tool_calls"}]}`
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
			return
		}
		chunk := `{"id":"c2","choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	}))
	defer chat.Close()

	h := New(Options{
		BaseURL:    chat.URL,
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: chat.Client(),
		BuiltinMCP: harness.BuiltinMCP{URL: fakeMCP.server.URL + "/mcp"},
	})

	turn := harness.Turn{
		Prompt:  "read the board",
		WorkDir: t.TempDir(),
		Mode:    harness.ModeFullAccess,
	}
	if err := h.Run(context.Background(), turn, func(harness.Event) {}); err != nil {
		t.Fatalf("h.Run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("expected 2 chat requests, got %d", len(bodies))
	}

	var payload struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Messages []struct {
			Role       string `json:"role"`
			Content    any    `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodies[1], &payload); err != nil {
		t.Fatalf("unmarshal second request: %v", err)
	}

	var sawTool bool
	for _, tool := range payload.Tools {
		if tool.Function.Name == "mcp__mcode__kanban_read_board" {
			sawTool = true
		}
	}
	if !sawTool {
		t.Fatalf("second request has no MCP tool definition: %s", string(bodies[1]))
	}

	var sawResult bool
	for _, msg := range payload.Messages {
		if msg.Role == "tool" && msg.ToolCallID == "call_mcp" {
			if text, ok := msg.Content.(string); ok && text == "board-ok" {
				sawResult = true
			}
		}
	}
	if !sawResult {
		t.Fatalf("second request has no MCP tool result: %s", string(bodies[1]))
	}
}

func TestMCP_EndpointFailureIsSkipped(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer broken.Close()

	fake := newFakeMCPServer(t, "kanban_read_board", "board-ok")

	h := New(Options{
		BaseURL:    "http://127.0.0.1:1/v1",
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: fake.server.Client(),
		ExternalMCP: func(workDir, threadID string) []harness.MCPEndpoint {
			return []harness.MCPEndpoint{
				{Name: "broken", URL: broken.URL},
				{Name: "mcode", URL: broken.URL},
				{Name: "mcode_approval", URL: broken.URL},
				{Name: "good", URL: fake.server.URL},
			}
		},
	})

	turn := harness.Turn{WorkDir: t.TempDir(), Mode: harness.ModeFullAccess}
	mcpTools := h.mcpToolsForTurn(context.Background(), turn)

	if len(mcpTools.tools) != 1 {
		t.Fatalf("expected only the healthy endpoint to contribute tools, got %d", len(mcpTools.tools))
	}
	if got := mcpTools.tools[0].CallName; got != "mcp__good__kanban_read_board" {
		t.Fatalf("unexpected call name: %s", got)
	}
}

func TestMCP_ApprovalRequired(t *testing.T) {
	fake := newFakeMCPServer(t, "kanban_read_board", "board-ok")

	h := New(Options{
		BaseURL:    "http://127.0.0.1:1/v1",
		APIKey:     "sk-test",
		Model:      "test-model",
		HTTPClient: fake.server.Client(),
		BuiltinMCP: harness.BuiltinMCP{URL: fake.server.URL},
	})

	call := toolCall{
		Index:     0,
		ID:        "call_mcp2",
		Name:      "mcp__mcode__kanban_read_board",
		Arguments: `{}`,
	}

	askTurn := harness.Turn{WorkDir: t.TempDir(), Mode: harness.ModeSupervised}
	approver := &mockApprover{decision: harness.DecisionAllow}
	askTurn.Approver = approver
	mcpTools := h.mcpToolsForTurn(context.Background(), askTurn)

	out, isErr := executeToolCallWithMCP(context.Background(), askTurn, call, nil, mcpTools)
	if !approver.called {
		t.Fatal("expected the MCP call to go through approval")
	}
	if isErr || out != "board-ok" {
		t.Fatalf("unexpected result after approval: %q (isErr=%v)", out, isErr)
	}

	denied := &mockApprover{decision: harness.DecisionDeny}
	denyTurn := harness.Turn{WorkDir: askTurn.WorkDir, Mode: harness.ModeSupervised, Approver: denied}
	out, isErr = executeToolCallWithMCP(context.Background(), denyTurn, call, nil, mcpTools)
	if !denied.called {
		t.Fatal("expected the MCP call to go through approval")
	}
	if !isErr || out != userRejectedText {
		t.Fatalf("expected declined output, got %q (isErr=%v)", out, isErr)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.calls) != 1 {
		t.Fatalf("denied call must not reach the endpoint, got %d calls", len(fake.calls))
	}
}

func TestMCP_ToolNameMapping(t *testing.T) {
	if got := mcpCallName("mcode", "kanban_read_board"); got != "mcp__mcode__kanban_read_board" {
		t.Fatalf("unexpected name: %s", got)
	}

	dirty := mcpCallName("my server", "do.thing/now")
	for _, r := range dirty {
		if !(r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			t.Fatalf("illegal character in tool name: %q", dirty)
		}
	}
	if !strings.HasPrefix(dirty, "mcp__") {
		t.Fatalf("tool name must keep the mcp__ prefix: %s", dirty)
	}

	long := strings.Repeat("a", 40) + "/" + strings.Repeat("b", 40)
	truncated := mcpCallName("server", long)
	if len(truncated) > mcpMaxToolNameLen {
		t.Fatalf("tool name exceeds %d chars: %d", mcpMaxToolNameLen, len(truncated))
	}
	if mcpCallName("server", long) != truncated {
		t.Fatal("tool name mapping must be deterministic")
	}

	sum := sha1.Sum([]byte("mcp__server__" + long))
	suffix := hex.EncodeToString(sum[:4])
	if !strings.HasSuffix(truncated, suffix) {
		t.Fatalf("truncated name should carry a hash suffix, got %s", truncated)
	}

	// 同一服务下相互冲突的截断名必须仍然唯一。
	session := &mcpSession{byName: map[string]*mcpToolEntry{}}
	first := session.uniqueCallName("server", long)
	session.byName[first] = &mcpToolEntry{CallName: first}
	second := session.uniqueCallName("server", long)
	if second == first {
		t.Fatalf("colliding tool names must be disambiguated: %s", second)
	}
	if len(second) > mcpMaxToolNameLen {
		t.Fatalf("disambiguated name exceeds %d chars: %d", mcpMaxToolNameLen, len(second))
	}
}
