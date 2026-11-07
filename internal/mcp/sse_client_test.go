package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestSSEClient_HandshakeAndResponses 校验经典 SSE 传输：GET 建立事件流、
// 接收 endpoint 事件、POST 到该地址、响应经 event: message 回到本地。
func TestSSEClient_HandshakeAndResponses(t *testing.T) {
	outgoing := make(chan string, 16)
	var mu sync.Mutex
	var postPath, postQuery string
	var gotSessionID string // 记录 POST 打到哪个地址

	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "accept must be text/event-stream", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}

		// 相对地址的 endpoint，客户端需按 spec.URL 解析成绝对地址。
		_, _ = fmt.Fprint(w, "event: endpoint\ndata: /messages?session=abc123\n\n")
		if flusher != nil {
			flusher.Flush()
		}

		for {
			select {
			case payload := <-outgoing:
				_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
				if flusher != nil {
					flusher.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		postPath = r.URL.Path
		postQuery = r.URL.RawQuery
		gotSessionID = r.URL.Query().Get("session")
		mu.Unlock()

		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// 经典 SSE 传输：请求体一律 202，响应走事件流。
		w.WriteHeader(http.StatusAccepted)
		if req.ID == nil {
			return
		}

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "sse-classic", "version": "4.2.0"},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{{"name": "sse-echo", "description": "回声工具"}},
			}
		case "tools/call":
			result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": "sse-call-ok"}},
			}
		default:
			result = map[string]any{}
		}
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		select {
		case outgoing <- string(b):
		case <-time.After(5 * time.Second):
		}
	})

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client := newSSEClient(UpstreamSpec{ID: "sse-classic", Transport: "sse", URL: ts.URL + "/sse"}, nil)
	t.Cleanup(func() {
		_ = client.close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := client.listTools(ctx)
	if err != nil {
		t.Fatalf("SSE tools/list 失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "sse-echo" {
		t.Fatalf("期望 1 个 sse-echo 工具，实际 %+v", tools)
	}
	if name, ver := client.serverInfo(); name != "sse-classic" || ver != "4.2.0" {
		t.Fatalf("serverInfo 应为 sse-classic/4.2.0，实际 %s/%s", name, ver)
	}

	res, err := client.callTool(ctx, "sse-echo", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("SSE tools/call 失败: %v", err)
	}
	if !strings.Contains(string(res), "sse-call-ok") {
		t.Fatalf("tools/call 结果异常: %s", string(res))
	}

	mu.Lock()
	path, query, sid := postPath, postQuery, gotSessionID
	mu.Unlock()
	if path != "/messages" || sid != "abc123" {
		t.Fatalf("POST 应打到 /messages?session=abc123，实际 path=%q query=%q", path, query)
	}
}

// TestSSEClient_FallbackToStreamableHTTP 校验兼容兜底：GET 返回 405 时说明服务
// 其实是 streamable HTTP，客户端应改用 httpClient 完成全部调用。
func TestSSEClient_FallbackToStreamableHTTP(t *testing.T) {
	var mu sync.Mutex
	var getCount, postCount int

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			mu.Lock()
			getCount++
			mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		mu.Lock()
		postCount++
		mu.Unlock()

		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]any{"name": "actually-streamable", "version": "1.1.0"},
			}
		case "tools/list":
			result = map[string]any{
				"tools": []map[string]any{{"name": "http-tool"}},
			}
		case "tools/call":
			result = map[string]any{
				"content": []map[string]any{{"type": "text", "text": "http-ok"}},
			}
		default:
			result = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(ts.Close)

	client := newSSEClient(UpstreamSpec{ID: "sse-as-http", Transport: "sse", URL: ts.URL}, nil)
	t.Cleanup(func() {
		_ = client.close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tools, err := client.listTools(ctx)
	if err != nil {
		t.Fatalf("回退到 streamable HTTP 后 tools/list 失败: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "http-tool" {
		t.Fatalf("期望 1 个 http-tool 工具，实际 %+v", tools)
	}
	if name, ver := client.serverInfo(); name != "actually-streamable" || ver != "1.1.0" {
		t.Fatalf("回退后 serverInfo 应来自委托客户端，实际 %s/%s", name, ver)
	}

	res, err := client.callTool(ctx, "http-tool", nil)
	if err != nil || !strings.Contains(string(res), "http-ok") {
		t.Fatalf("回退后 tools/call 失败: err=%v res=%s", err, string(res))
	}

	// 回退只发生一次：后续调用不再重试 GET。
	mu.Lock()
	gets, posts := getCount, postCount
	mu.Unlock()
	if gets != 1 {
		t.Fatalf("回退后不应再发 GET，实际 %d 次", gets)
	}
	if posts < 4 {
		t.Fatalf("回退后 POST 次数偏少，实际 %d 次", posts)
	}
}
