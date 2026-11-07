package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"mcode/internal/store"
)

func postProxy(t *testing.T, handler http.Handler, path, token string, reqBody any) (int, map[string]any) {
	t.Helper()
	var bodyReader *bytes.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			t.Fatalf("marshal request body failed: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	} else {
		bodyReader = bytes.NewReader([]byte{})
	}

	req := httptest.NewRequest(http.MethodPost, path, bodyReader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var resp map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	}
	return rec.Code, resp
}

func TestProxy_StdioAndHTTP(t *testing.T) {
	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})

	// 1. 设置 HTTP 上游
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
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
					"serverInfo":      map[string]any{"name": "mock-http"},
				},
			}
		case "tools/list":
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"tools": []map[string]any{
						{"name": "echo", "description": "mock tool"},
					},
				},
			}
		case "tools/call":
			var callParams struct {
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &callParams)
			resp = map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"content": []map[string]any{
						{"type": "text", "text": string(callParams.Arguments)},
					},
				},
			}
		default:
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(httpServer.Close)

	hub.SetUpstreams([]UpstreamSpec{
		{
			ID:        "stdio-srv",
			Transport: "stdio",
			Command:   os.Args[0],
			Env:       map[string]string{"MCODE_FAKE_MCP_STDIO": "1"},
		},
		{
			ID:        "http-srv",
			Transport: "http",
			URL:       httpServer.URL,
		},
	})

	proxy := NewProxy(hub)
	t.Cleanup(func() {
		_ = proxy.Close()
	})

	handler := proxy.Handler()
	token := proxy.Token()

	testTargets := []string{"stdio-srv", "http-srv"}
	for _, target := range testTargets {
		path := "/mcp/" + target

		// 1. initialize
		initReq := map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "initialize",
			"params": map[string]any{
				"protocolVersion": "2025-06-18",
			},
		}
		code, resp := postProxy(t, handler, path, token, initReq)
		if code != http.StatusOK {
			t.Fatalf("[%s] initialize status: expected 200, got %d", target, code)
		}
		resObj, _ := resp["result"].(map[string]any)
		serverInfo, _ := resObj["serverInfo"].(map[string]any)
		if serverInfo["name"] != "mcode-proxy" {
			t.Errorf("[%s] expected serverInfo name 'mcode-proxy', got %v", target, serverInfo["name"])
		}

		// 2. notifications/initialized (无 id)
		notifyReq := map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/initialized",
		}
		code, _ = postProxy(t, handler, path, token, notifyReq)
		if code != http.StatusAccepted {
			t.Fatalf("[%s] notification status: expected 202, got %d", target, code)
		}

		// 3. tools/list
		listReq := map[string]any{
			"jsonrpc": "2.0",
			"id":      2,
			"method":  "tools/list",
		}
		code, resp = postProxy(t, handler, path, token, listReq)
		if code != http.StatusOK {
			t.Fatalf("[%s] tools/list status: expected 200, got %d", target, code)
		}
		resList, _ := resp["result"].(map[string]any)
		tools, _ := resList["tools"].([]any)
		if len(tools) == 0 {
			t.Fatalf("[%s] expected at least 1 tool, got %v", target, resList)
		}

		// 4. tools/call echo
		callReq := map[string]any{
			"jsonrpc": "2.0",
			"id":      3,
			"method":  "tools/call",
			"params": map[string]any{
				"name":      "echo",
				"arguments": map[string]any{"greet": "hello " + target},
			},
		}
		code, resp = postProxy(t, handler, path, token, callReq)
		if code != http.StatusOK {
			t.Fatalf("[%s] tools/call status: expected 200, got %d", target, code)
		}
		resCall, _ := resp["result"].(map[string]any)
		content, _ := resCall["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("[%s] expected call result content, got %v", target, resCall)
		}
		firstItem, _ := content[0].(map[string]any)
		textVal, _ := firstItem["text"].(string)
		if !strings.Contains(textVal, "hello "+target) {
			t.Errorf("[%s] expected echo text to contain greeting, got %q", target, textVal)
		}
	}
}

func TestProxy_AuthFailure(t *testing.T) {
	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})
	hub.SetUpstreams([]UpstreamSpec{
		{
			ID:        "auth-target",
			Transport: "stdio",
			Command:   os.Args[0],
			Env:       map[string]string{"MCODE_FAKE_MCP_STDIO": "1"},
		},
	})

	proxy := NewProxy(hub)
	t.Cleanup(func() {
		_ = proxy.Close()
	})
	handler := proxy.Handler()

	// 1. 无 token
	code, _ := postProxy(t, handler, "/mcp/auth-target", "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token, got %d", code)
	}

	// 2. 错误 token
	code, _ = postProxy(t, handler, "/mcp/auth-target", "wrong-token", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token, got %d", code)
	}
}

func TestProxy_UnknownID(t *testing.T) {
	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})
	proxy := NewProxy(hub)
	t.Cleanup(func() {
		_ = proxy.Close()
	})

	// 访问未在 hub 注册的 id，应返回 404
	code, _ := postProxy(t, proxy.Handler(), "/mcp/non-existent-server", proxy.Token(), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if code != http.StatusNotFound {
		t.Errorf("expected 404 for unknown server id, got %d", code)
	}
}

func TestProxy_MethodNotAllowed(t *testing.T) {
	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})
	hub.SetUpstreams([]UpstreamSpec{
		{
			ID:        "test-srv",
			Transport: "stdio",
			Command:   os.Args[0],
			Env:       map[string]string{"MCODE_FAKE_MCP_STDIO": "1"},
		},
	})
	proxy := NewProxy(hub)
	t.Cleanup(func() {
		_ = proxy.Close()
	})

	req := httptest.NewRequest(http.MethodGet, "/mcp/test-srv", nil)
	rec := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405 Method Not Allowed, got %d", rec.Code)
	}
}

func TestProxy_UpstreamUnauthorized(t *testing.T) {
	// 上游模拟 HTTP 401
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(upstreamServer.Close)

	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})
	hub.SetUpstreams([]UpstreamSpec{
		{
			ID:        "unauth-srv",
			Transport: "http",
			URL:       upstreamServer.URL,
		},
	})

	proxy := NewProxy(hub)
	t.Cleanup(func() {
		_ = proxy.Close()
	})

	// 转发时遇到上游 401，代理应返回 JSON-RPC -32001 错误，并提示需要重新授权
	callReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      100,
		"method":  "tools/list",
	}
	code, resp := postProxy(t, proxy.Handler(), "/mcp/unauth-srv", proxy.Token(), callReq)
	if code != http.StatusOK {
		t.Fatalf("expected HTTP 200 with JSON-RPC error, got %d", code)
	}

	errObj, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected JSON-RPC error object, got %v", resp)
	}
	codeVal, _ := errObj["code"].(float64)
	if int(codeVal) != -32001 {
		t.Errorf("expected error code -32001, got %v", codeVal)
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "re-authentication required") {
		t.Errorf("expected message to indicate re-authentication required, got %q", msg)
	}
}

func TestProxy_StartAndClose(t *testing.T) {
	hub := NewHub(nil)
	t.Cleanup(func() {
		_ = hub.Close()
	})
	hub.SetUpstreams([]UpstreamSpec{
		{
			ID:        "srv-real",
			Transport: "stdio",
			Command:   os.Args[0],
			Env:       map[string]string{"MCODE_FAKE_MCP_STDIO": "1"},
		},
	})

	proxy := NewProxy(hub)
	t.Cleanup(func() {
		_ = proxy.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := proxy.Start(ctx); err != nil {
		t.Fatalf("proxy.Start failed: %v", err)
	}
	// 重复 Start 应快速失败，避免覆盖已启动的监听器
	if err := proxy.Start(ctx); err == nil {
		t.Errorf("expected second Start to fail")
	}

	rawURL := proxy.URL("srv-real")
	if !strings.HasPrefix(rawURL, "http://127.0.0.1:") || !strings.HasSuffix(rawURL, "/mcp/srv-real") {
		t.Fatalf("unexpected proxy.URL: %s", rawURL)
	}

	// 真实发起 HTTP 请求
	bodyBytes, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	})
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(bodyBytes))
	if err != nil {
		t.Fatalf("NewRequest failed: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+proxy.Token())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do request to real proxy listener failed: %v", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK from running proxy, got %d", resp.StatusCode)
	}
}

const testThreadID = "0b6f4e0a-1c2d-4e3f-8a9b-0c1d2e3f4a5b"

func TestProxy_BuiltinThreadRouting(t *testing.T) {
	srv, st, _ := setupTestMCP(t)
	ctx := context.Background()
	// 两个项目：缺少会话作用域时无法靠单项目兜底确定项目。
	if err := st.UpsertProject(ctx, store.Project{ID: "proj-test-2", Name: "other", Path: t.TempDir()}); err != nil {
		t.Fatalf("插入第二个项目失败: %v", err)
	}
	if err := st.CreateThread(ctx, store.Thread{ID: testThreadID, ProjectID: "proj-test-1", Title: "t", Provider: "memory"}); err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}

	proxy := NewProxy(NewHub(nil))
	t.Cleanup(func() { _ = proxy.Close() })
	proxy.SetBuiltin(srv)

	call := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{
			"name":      "kanban_read_board",
			"arguments": map[string]any{"project_id": "proj-test-1"},
		},
	}

	t.Run("合法 thread_id 命中内置服务并按会话解析项目", func(t *testing.T) {
		code, res := postProxy(t, proxy.Handler(), "/mcp/mcode/"+testThreadID, proxy.Token(), call)
		if code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d", code)
		}
		if payload, isError := toolCallResult(t, res); isError {
			t.Fatalf("期望按会话解析到 proj-test-1，实际返回错误: %v", payload)
		}
	})

	t.Run("未知会话无法解析项目", func(t *testing.T) {
		code, res := postProxy(t, proxy.Handler(), "/mcp/mcode/ffffffff-ffff-4fff-8fff-ffffffffffff", proxy.Token(), call)
		if code != http.StatusOK {
			t.Fatalf("期望 200，实际 %d", code)
		}
		if _, isError := toolCallResult(t, res); !isError {
			t.Fatal("未知会话不应解析出项目")
		}
	})

	t.Run("使用内置服务自身 token 被拒", func(t *testing.T) {
		code, _ := postProxy(t, proxy.Handler(), "/mcp/mcode/"+testThreadID, srv.Token(), call)
		if code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", code)
		}
	})

	t.Run("非法 thread_id 被拒", func(t *testing.T) {
		for _, path := range []string{
			"/mcp/mcode/",
			"/mcp/mcode/../x",
			"/mcp/mcode/a/b",
			"/mcp/mcode/a%2Fb",
			"/mcp/mcode/%2e%2e%2fx",
			"/mcp/mcode/not-a-uuid",
			"/mcp/mcode/" + testThreadID + "x",
			"/mcp/mcode/" + testThreadID + "/extra",
		} {
			code, _ := postProxy(t, proxy.Handler(), path, proxy.Token(), call)
			if code == http.StatusOK {
				t.Errorf("%s: 非法 thread_id 不应成功", path)
			}
		}

		// 非法转义序列无法经 httptest.NewRequest 构造（会 panic），手工设置 URL 字段
		b, _ := json.Marshal(call)
		req := httptest.NewRequest(http.MethodPost, "/mcp/mcode/x", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+proxy.Token())
		req.URL.Path = "/mcp/mcode/0b6f4e0a-1c2d-4e3f-8a9b-0c1d2e3f4a5%zz"
		req.URL.RawPath = req.URL.Path
		rec := httptest.NewRecorder()
		proxy.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("含 %%zz 的 thread_id 不应成功")
		}
	})
}

func TestProxy_BuiltinNotConfigured(t *testing.T) {
	proxy := NewProxy(NewHub(nil))
	t.Cleanup(func() { _ = proxy.Close() })

	code, _ := postProxy(t, proxy.Handler(), "/mcp/mcode/"+testThreadID, proxy.Token(), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
	if code != http.StatusServiceUnavailable {
		t.Errorf("未 SetBuiltin 应返回 503，实际 %d", code)
	}
}

func TestProxy_BuiltinDoesNotAffectUpstreamRoutes(t *testing.T) {
	srv, _, _ := setupTestMCP(t)
	proxy := NewProxy(NewHub(nil))
	t.Cleanup(func() { _ = proxy.Close() })
	proxy.SetBuiltin(srv)

	// 外部 serverID 路由行为不变：未注册 id 仍为 404，含 "/" 的 id 仍为 404。
	for _, path := range []string{"/mcp/non-existent-server", "/mcp/mcode", "/mcp/a/b"} {
		code, _ := postProxy(t, proxy.Handler(), path, proxy.Token(), map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"})
		if code != http.StatusNotFound {
			t.Errorf("%s: 期望 404，实际 %d", path, code)
		}
	}
}
