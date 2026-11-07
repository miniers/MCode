package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("MCODE_FAKE_MCP_STDIO") == "1" {
		runFakeMCPServer()
		os.Exit(0)
	}
	if os.Getenv("MCODE_FAKE_EXIT_STDIO") == "1" {
		_, _ = fmt.Fprintln(os.Stderr, "fatal: failed to initialize fake server")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func runFakeMCPServer() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var req struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      any             `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.ID == nil {
			// 通知（如 notifications/initialized），无需响应
			continue
		}
		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities": map[string]any{
						"tools": map[string]any{},
					},
					"serverInfo": map[string]any{
						"name":    "fake-stdio",
						"version": "1.0.0",
					},
				},
			}
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "echo",
							"description": "echoes arguments",
							"inputSchema": map[string]any{
								"type": "object",
							},
						},
					},
				},
			}
		case "tools/call":
			var callParams struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &callParams)
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []map[string]any{
						{
							"type": "text",
							"text": string(callParams.Arguments),
						},
					},
				},
			}
		case "ping":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{},
			}
		default:
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error": map[string]any{
					"code":    -32601,
					"message": "method not found",
				},
			}
		}
		out, _ := json.Marshal(resp)
		out = append(out, '\n')
		_, _ = os.Stdout.Write(out)
	}
}

type mockTokenSource struct {
	token string
}

func (m *mockTokenSource) AccessToken(ctx context.Context, serverID string) (string, error) {
	return m.token, nil
}

func TestHub_Stdio(t *testing.T) {
	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})

	spec := UpstreamSpec{
		ID:        "stdio-server",
		Transport: "stdio",
		Command:   os.Args[0],
		Env: map[string]string{
			"MCODE_FAKE_MCP_STDIO": "1",
		},
	}
	hub.SetUpstreams([]UpstreamSpec{spec})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 测试 ListTools
	tools, err := hub.ListTools(ctx, "stdio-server")
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("expected 1 tool 'echo', got %+v", tools)
	}

	// 2. 测试 CallTool
	args := json.RawMessage(`{"hello":"world"}`)
	res, err := hub.CallTool(ctx, "stdio-server", "echo", args)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	var callRes struct {
		Content []struct {
			Type, Text string
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &callRes); err != nil {
		t.Fatalf("unmarshal CallTool result failed: %v (res: %s)", err, string(res))
	}
	if len(callRes.Content) != 1 {
		t.Fatalf("expected len(Content)==1, got %d (res: %s)", len(callRes.Content), string(res))
	}
	if !strings.Contains(callRes.Content[0].Text, `"hello":"world"`) {
		t.Fatalf("expected Content[0].Text to contain argument, got %s (res: %s)", callRes.Content[0].Text, string(res))
	}

	// 3. 测试 Forward
	fwdRes, rpcErr, err := hub.Forward(ctx, "stdio-server", "ping", nil)
	if err != nil || rpcErr != nil {
		t.Fatalf("Forward ping failed: err=%v, rpcErr=%v", err, rpcErr)
	}
	if len(fwdRes) == 0 {
		t.Fatalf("expected ping response")
	}
}

func TestHub_HTTP_JSON(t *testing.T) {
	var mu sync.Mutex
	var receivedSessionID string
	var receivedAuth string
	var requestCount int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestCount++
		receivedSessionID = r.Header.Get("Mcp-Session-Id")
		receivedAuth = r.Header.Get("Authorization")
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Mcp-Session-Id", "sess-test-456")

		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.ID == nil {
			// 通知
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"capabilities":    map[string]any{},
					"serverInfo": map[string]any{
						"name":    "fake-http",
						"version": "2.0.0",
					},
				},
			}
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{
							"name":        "http-echo",
							"description": "http tool",
						},
					},
				},
			}
		case "tools/call":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": "http-success"},
					},
				},
			}
		default:
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)

	mockTokens := &mockTokenSource{token: "my-secret-token"}
	hub := NewHub(mockTokens)
	t.Cleanup(func() {
		_ = hub.Close()
	})

	spec := UpstreamSpec{
		ID:        "http-server",
		Transport: "http",
		URL:       ts.URL,
		OAuth:     true,
	}
	hub.SetUpstreams([]UpstreamSpec{spec})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 第一次调用会自动触发 initialize 与 notifications/initialized
	tools, err := hub.ListTools(ctx, "http-server")
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "http-echo" {
		t.Fatalf("unexpected tools: %+v", tools)
	}

	// 第二次调用校验 Session-ID 已被回传
	_, err = hub.CallTool(ctx, "http-server", "http-echo", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("expected Authorization 'Bearer my-secret-token', got %q", receivedAuth)
	}
	if receivedSessionID != "sess-test-456" {
		t.Errorf("expected Mcp-Session-Id 'sess-test-456' on subsequent calls, got %q", receivedSessionID)
	}
}

func TestHub_HTTP_SSE(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo": map[string]any{
						"name":    "sse-server",
						"version": "1.0",
					},
				},
			}
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "sse-tool"},
					},
				},
			}
		default:
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{},
			}
		}

		b, _ := json.Marshal(resp)
		_, _ = fmt.Fprintf(w, ": heartbeat\n\ndata: %s\n\n", string(b))
	}))
	t.Cleanup(ts.Close)

	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})

	spec := UpstreamSpec{
		ID:        "sse-server",
		Transport: "sse",
		URL:       ts.URL,
	}
	hub.SetUpstreams([]UpstreamSpec{spec})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := hub.ListTools(ctx, "sse-server")
	if err != nil {
		t.Fatalf("ListTools over SSE failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "sse-tool" {
		t.Fatalf("unexpected tools from SSE: %+v", tools)
	}
}

func TestHub_HTTP_401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid token"}`))
	}))
	t.Cleanup(ts.Close)

	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})

	spec := UpstreamSpec{
		ID:        "auth-fail-srv",
		Transport: "http",
		URL:       ts.URL,
	}
	hub.SetUpstreams([]UpstreamSpec{spec})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_, err := hub.ListTools(ctx, "auth-fail-srv")
	if !errors.Is(err, ErrUpstreamUnauthorized) {
		t.Fatalf("expected ErrUpstreamUnauthorized, got %v", err)
	}
}

func TestHub_SetUpstreams(t *testing.T) {
	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})

	spec1 := UpstreamSpec{
		ID:        "srv-1",
		Transport: "stdio",
		Command:   os.Args[0],
		Env:       map[string]string{"MCODE_FAKE_MCP_STDIO": "1"},
	}
	hub.SetUpstreams([]UpstreamSpec{spec1})

	if _, ok := hub.Upstream("srv-1"); !ok {
		t.Fatalf("expected srv-1 to exist")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 产生 client 实例
	_, err := hub.ListTools(ctx, "srv-1")
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	// 替换配置，移除 srv-1
	spec2 := UpstreamSpec{
		ID:        "srv-2",
		Transport: "stdio",
		Command:   os.Args[0],
		Env:       map[string]string{"MCODE_FAKE_MCP_STDIO": "1"},
	}
	hub.SetUpstreams([]UpstreamSpec{spec2})

	if _, ok := hub.Upstream("srv-1"); ok {
		t.Fatalf("srv-1 should have been removed")
	}
	if _, ok := hub.Upstream("srv-2"); !ok {
		t.Fatalf("srv-2 should exist")
	}
}

func TestProbe_Stdio(t *testing.T) {
	spec := UpstreamSpec{
		ID:        "probe-stdio",
		Transport: "stdio",
		Command:   os.Args[0],
		Env: map[string]string{
			"MCODE_FAKE_MCP_STDIO": "1",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := Probe(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Probe stdio failed: %v", err)
	}
	if res.ServerName != "fake-stdio" {
		t.Errorf("expected ServerName 'fake-stdio', got %q", res.ServerName)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "echo" {
		t.Errorf("unexpected tools: %+v", res.Tools)
	}
}

func TestProbe_StdioExitError(t *testing.T) {
	spec := UpstreamSpec{
		ID:        "probe-exit-stdio",
		Transport: "stdio",
		Command:   os.Args[0],
		Env: map[string]string{
			"MCODE_FAKE_EXIT_STDIO": "1",
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Probe(ctx, spec, nil)
	if err == nil {
		t.Fatalf("expected error from exiting process")
	}
	if !strings.Contains(err.Error(), "fatal: failed to initialize fake server") {
		t.Errorf("expected stderr first line in error message, got: %v", err)
	}
}

func TestProbe_HTTP(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo": map[string]any{
						"name":    "probe-http-srv",
						"version": "3.1.4",
					},
				},
			}
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "toolA"},
						{"name": "toolB"},
					},
				},
			}
		default:
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{},
			}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(ts.Close)

	spec := UpstreamSpec{
		ID:        "probe-http",
		Transport: "http",
		URL:       ts.URL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res, err := Probe(ctx, spec, nil)
	if err != nil {
		t.Fatalf("Probe HTTP failed: %v", err)
	}
	if res.ServerName != "probe-http-srv" || res.ServerVersion != "3.1.4" {
		t.Errorf("unexpected ServerInfo: %s %s", res.ServerName, res.ServerVersion)
	}
	if len(res.Tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(res.Tools))
	}
}

func TestProbe_HTTP_401(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	spec := UpstreamSpec{
		ID:        "probe-http-401",
		Transport: "http",
		URL:       ts.URL,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := Probe(ctx, spec, nil)
	if !errors.Is(err, ErrUpstreamUnauthorized) {
		t.Fatalf("expected ErrUpstreamUnauthorized, got: %v", err)
	}
}

func TestProbeTimeout(t *testing.T) {
	if got := probeTimeout(UpstreamSpec{Args: []string{"-y", "mcp-remote", "https://example.com/mcp"}}); got != probeOAuthTimeout {
		t.Errorf("expected mcp-remote probe timeout %s, got %s", probeOAuthTimeout, got)
	}
	if got := probeTimeout(UpstreamSpec{Args: []string{"-y", "some-server"}}); got != probeDefaultTimeout {
		t.Errorf("expected default probe timeout %s, got %s", probeDefaultTimeout, got)
	}
}

func TestHub_HTTP_ReinitializesAfterUpstreamRestart(t *testing.T) {
	var mu sync.Mutex
	session := ""
	initCount := 0

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		mu.Lock()
		sid := r.Header.Get("Mcp-Session-Id")
		current := session
		mu.Unlock()

		// 只认识自己最近签发的会话号，其余一律 404，等价于上游重启后旧会话失效。
		if sid != "" && sid != current {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"session not found"}`))
			return
		}
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			mu.Lock()
			initCount++
			signed := fmt.Sprintf("sess-%d", initCount)
			session = signed
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", signed)
			b, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo":      map[string]any{"name": "restarting", "version": "1.0"},
				},
			})
			_, _ = w.Write(b)
		case "tools/list":
			mu.Lock()
			signed := session
			mu.Unlock()
			w.Header().Set("Mcp-Session-Id", signed)
			b, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result":  map[string]any{"tools": []map[string]any{{"name": "ping-me"}}},
			})
			_, _ = w.Write(b)
		default:
			b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
			_, _ = w.Write(b)
		}
	}))
	t.Cleanup(ts.Close)

	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})
	hub.SetUpstreams([]UpstreamSpec{{ID: "restart-srv", Transport: "http", URL: ts.URL}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := hub.ListTools(ctx, "restart-srv")
	if err != nil || len(tools) != 1 || tools[0].Name != "ping-me" {
		t.Fatalf("first tools/list failed: %v, %+v", err, tools)
	}

	// 模拟上游重启：它换发了会话号，客户端手里的旧号从此得到 404。
	mu.Lock()
	session = "sess-after-restart"
	mu.Unlock()

	tools, err = hub.ListTools(ctx, "restart-srv")
	if err != nil || len(tools) != 1 || tools[0].Name != "ping-me" {
		t.Fatalf("tools/list after upstream restart failed: %v, %+v", err, tools)
	}

	mu.Lock()
	got := initCount
	mu.Unlock()
	if got != 2 {
		t.Fatalf("expected one re-initialize after the session expired, got %d initializes", got)
	}
}
