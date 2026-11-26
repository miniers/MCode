package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWithMethodTimeout 校验按方法放宽超时：tools/call 放宽到 toolCallTimeout，
// 其他方法不加截止时间，调用方自带截止时间时保持原样。
func TestWithMethodTimeout(t *testing.T) {
	t.Parallel()
	t.Run("tools/call 放宽到工具超时", func(t *testing.T) {
		ctx, cancel := withMethodTimeout(context.Background(), "tools/call")
		defer cancel()

		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatalf("tools/call 应当带截止时间")
		}
		remain := time.Until(deadline)
		// 放宽的量级应与 toolCallTimeout 一致（留足余量避免慢机器抖动）。
		if remain < toolCallTimeout-time.Minute || remain > toolCallTimeout+5*time.Second {
			t.Fatalf("tools/call 截止时间约 %s，实际剩余 %s", toolCallTimeout, remain)
		}
	})

	t.Run("其他方法不加截止时间", func(t *testing.T) {
		for _, method := range []string{"tools/list", "initialize", "ping", "resources/read"} {
			ctx, cancel := withMethodTimeout(context.Background(), method)
			_, hasDeadline := ctx.Deadline()
			cancel()
			if hasDeadline {
				t.Fatalf("%s 不应带截止时间", method)
			}
		}
	})

	t.Run("已有截止时间保持不变", func(t *testing.T) {
		want := time.Now().Add(90 * time.Second)
		base, baseCancel := context.WithDeadline(context.Background(), want)
		defer baseCancel()

		for _, method := range []string{"tools/call", "tools/list"} {
			ctx, cancel := withMethodTimeout(base, method)
			got, ok := ctx.Deadline()
			cancel()
			if !ok || !got.Equal(want) {
				t.Fatalf("%s 应沿用调用方截止时间 %s，实际 %s", method, want, got)
			}
		}
	})

	t.Run("取消函数可正常结束上下文", func(t *testing.T) {
		ctx, cancel := withMethodTimeout(context.Background(), "tools/call")
		cancel()
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatalf("cancel 应当结束上下文")
		}
	})
}

// lockedBuffer 为并发安全的写入缓冲，充当 stdioClient 的 stdin。
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) Close() error { return nil }

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestStdioClient_RepliesToServerInitiatedRequests 校验上游主动发来的带 id 请求
// 会被回复，且不会被当成响应污染 pending 表。
// 做法：直接喂 readLoop 一段 stdout，并检查它写回 stdin 的内容。
func TestStdioClient_RepliesToServerInitiatedRequests(t *testing.T) {
	t.Parallel()
	client := newStdioClient(UpstreamSpec{ID: "stdio-server-req", Transport: "stdio"})
	stdin := &lockedBuffer{}

	client.mu.Lock()
	client.stdin = stdin
	client.started = true
	// 服务端请求的 id 故意与本地 pending 的 id 相同，用来验证不会被顶掉。
	const localID int64 = 7
	respCh := make(chan rpcResponseMsg, 1)
	client.pending[localID] = respCh
	client.mu.Unlock()

	pr, pw := io.Pipe()
	// 进程不真的启动，Wait 只会返回 "exec: not started"，readLoop 忽略该错误。
	go client.readLoop(pr, exec.Command("true"))

	serverPing := `{"jsonrpc":"2.0","id":"srv-ping-1","method":"ping"}` + "\n"
	serverUnknown := `{"jsonrpc":"2.0","id":7,"method":"roots/list"}` + "\n"
	serverNote := `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}` + "\n"
	localResp := `{"jsonrpc":"2.0","id":7,"result":{"echo":true}}` + "\n"
	if _, err := pw.Write([]byte(serverPing + serverUnknown + serverNote + localResp)); err != nil {
		t.Fatalf("写入 stdout 失败: %v", err)
	}

	select {
	case resp := <-respCh:
		if resp.err != nil {
			t.Fatalf("本地 pending 收到错误: %v", resp.err)
		}
		if !strings.Contains(string(resp.result), `"echo":true`) {
			t.Fatalf("本地 pending 拿到错误的响应: %s", string(resp.result))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("服务端消息之后本地响应未被投递（pending 被污染）")
	}

	_ = pw.Close()
	// 等 readLoop 处理完并清理状态。
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		started := client.started
		client.mu.Unlock()
		if !started {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	written := stdin.String()
	lines := strings.Split(strings.TrimSpace(written), "\n")
	if len(lines) != 2 {
		t.Fatalf("期望回复 2 条（ping 与未知方法），实际 %d 条: %q", len(lines), written)
	}

	var pingResp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &pingResp); err != nil {
		t.Fatalf("解析 ping 回复失败: %v (%s)", err, lines[0])
	}
	if strings.TrimSpace(string(pingResp.ID)) != `"srv-ping-1"` {
		t.Fatalf("ping 回复应原样回显字符串 id，实际 %s", string(pingResp.ID))
	}
	if strings.TrimSpace(string(pingResp.Result)) != "{}" {
		t.Fatalf("ping 应回复空 result，实际 %s", string(pingResp.Result))
	}

	var unknownResp struct {
		ID    json.RawMessage `json:"id"`
		Error *jsonRPCError   `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &unknownResp); err != nil {
		t.Fatalf("解析未知方法回复失败: %v (%s)", err, lines[1])
	}
	if strings.TrimSpace(string(unknownResp.ID)) != "7" {
		t.Fatalf("未知方法回复应原样回显数字 id，实际 %s", string(unknownResp.ID))
	}
	if unknownResp.Error == nil || unknownResp.Error.Code != -32601 || unknownResp.Error.Message != "method not found" {
		t.Fatalf("未知方法应回 -32601 method not found，实际 %+v", unknownResp.Error)
	}

	client.mu.Lock()
	left := len(client.pending)
	client.mu.Unlock()
	if left != 0 {
		t.Fatalf("readLoop 结束后 pending 应清空，实际残留 %d 条", left)
	}
}

type cancelledParamsPayload struct {
	RequestID int64  `json:"requestId"`
	Reason    string `json:"reason"`
}

func TestHubForwardCancelSendsNotification(t *testing.T) {
	t.Parallel()

	t.Run("http", func(t *testing.T) {
		t.Parallel()
		calledCh := make(chan int64, 4)
		cancelCh := make(chan cancelledParamsPayload, 4)
		initBlockedCh := make(chan struct{}, 4)
		var blockInit atomic.Bool

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)

			if req.Method == "notifications/cancelled" {
				var p cancelledParamsPayload
				_ = json.Unmarshal(req.Params, &p)
				cancelCh <- p
				w.WriteHeader(http.StatusAccepted)
				return
			}
			if req.ID == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}

			switch req.Method {
			case "initialize":
				if blockInit.Load() {
					select {
					case initBlockedCh <- struct{}{}:
					default:
					}
					<-r.Context().Done()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      *req.ID,
					"result": map[string]any{
						"protocolVersion": "2025-06-18",
						"serverInfo":      map[string]any{"name": "http-cancel", "version": "1.0"},
					},
				})
			case "tools/call":
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				calledCh <- *req.ID
				<-r.Context().Done()
			default:
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{}})
			}
		}))
		t.Cleanup(srv.Close)

		hub := NewHub(nil)
		t.Cleanup(func() { _ = hub.Close() })
		hub.SetUpstreams([]UpstreamSpec{{ID: "http-srv", Transport: "http", URL: srv.URL}})

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, _, err := hub.Forward(ctx, "http-srv", "tools/call", json.RawMessage(`{"name":"slow"}`))
			errCh <- err
		}()

		var reqID int64
		select {
		case reqID = <-calledCh:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("超时未收到上游 tools/call 请求")
		}

		cancel()
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("取消后 Forward 应返回错误")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("取消后 Forward 未及时返回")
		}

		select {
		case got := <-cancelCh:
			if got.RequestID != reqID {
				t.Fatalf("notifications/cancelled requestId=%d, want %d", got.RequestID, reqID)
			}
			if got.Reason != "request cancelled" {
				t.Fatalf("notifications/cancelled reason=%q, want %q", got.Reason, "request cancelled")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("超时未收到 notifications/cancelled")
		}

		// initialize 被取消时不得发 notifications/cancelled。
		blockInit.Store(true)
		initHub := NewHub(nil)
		t.Cleanup(func() { _ = initHub.Close() })
		initHub.SetUpstreams([]UpstreamSpec{{ID: "http-init", Transport: "http", URL: srv.URL}})

		initCtx, initCancel := context.WithCancel(context.Background())
		initErrCh := make(chan error, 1)
		go func() {
			_, _, err := initHub.Forward(initCtx, "http-init", "tools/list", nil)
			initErrCh <- err
		}()

		select {
		case <-initBlockedCh:
		case <-time.After(5 * time.Second):
			initCancel()
			t.Fatal("超时未收到 initialize 请求")
		}
		initCancel()
		select {
		case err := <-initErrCh:
			if err == nil {
				t.Fatal("initialize 取消后 Forward 应返回错误")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("initialize 取消后 Forward 未及时返回")
		}

		select {
		case got := <-cancelCh:
			t.Fatalf("initialize 被取消时不应发送 notifications/cancelled，实际收到 %+v", got)
		case <-time.After(150 * time.Millisecond):
		}
	})

	t.Run("sse", func(t *testing.T) {
		t.Parallel()
		outgoing := make(chan string, 16)
		calledCh := make(chan int64, 4)
		cancelCh := make(chan cancelledParamsPayload, 4)
		initStartedCh := make(chan struct{}, 4)
		var blockInit atomic.Bool

		mux := http.NewServeMux()
		mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			_, _ = fmt.Fprint(w, "event: endpoint\ndata: /messages\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			for {
				select {
				case msg := <-outgoing:
					_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
					if flusher != nil {
						flusher.Flush()
					}
				case <-r.Context().Done():
					return
				}
			}
		})
		mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
			var req struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			w.WriteHeader(http.StatusAccepted)

			if req.Method == "notifications/cancelled" {
				var p cancelledParamsPayload
				_ = json.Unmarshal(req.Params, &p)
				cancelCh <- p
				return
			}
			if req.ID == nil {
				return
			}
			switch req.Method {
			case "initialize":
				if blockInit.Load() {
					select {
					case initStartedCh <- struct{}{}:
					default:
					}
					return
				}
				b, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      *req.ID,
					"result": map[string]any{
						"protocolVersion": "2025-06-18",
						"serverInfo":      map[string]any{"name": "sse-cancel", "version": "1.0"},
					},
				})
				select {
				case outgoing <- string(b):
				case <-time.After(5 * time.Second):
				}
			case "tools/call":
				calledCh <- *req.ID
			}
		})

		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)

		hub := NewHub(nil)
		t.Cleanup(func() { _ = hub.Close() })
		hub.SetUpstreams([]UpstreamSpec{{ID: "sse-srv", Transport: "sse", URL: srv.URL + "/sse"}})

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, _, err := hub.Forward(ctx, "sse-srv", "tools/call", json.RawMessage(`{"name":"slow"}`))
			errCh <- err
		}()

		var reqID int64
		select {
		case reqID = <-calledCh:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("超时未收到 SSE 上游 tools/call 请求")
		}

		cancel()
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("取消后 SSE Forward 应返回错误")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("取消后 SSE Forward 未及时返回")
		}

		select {
		case got := <-cancelCh:
			if got.RequestID != reqID {
				t.Fatalf("SSE notifications/cancelled requestId=%d, want %d", got.RequestID, reqID)
			}
			if got.Reason != "request cancelled" {
				t.Fatalf("SSE notifications/cancelled reason=%q, want %q", got.Reason, "request cancelled")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("超时未收到 SSE notifications/cancelled")
		}

		// initialize 被取消时不得发 notifications/cancelled。
		blockInit.Store(true)
		initHub := NewHub(nil)
		t.Cleanup(func() { _ = initHub.Close() })
		initHub.SetUpstreams([]UpstreamSpec{{ID: "sse-init", Transport: "sse", URL: srv.URL + "/sse"}})

		initCtx, initCancel := context.WithCancel(context.Background())
		initErrCh := make(chan error, 1)
		go func() {
			_, _, err := initHub.Forward(initCtx, "sse-init", "tools/list", nil)
			initErrCh <- err
		}()

		select {
		case <-initStartedCh:
		case <-time.After(5 * time.Second):
			initCancel()
			t.Fatal("超时未收到 SSE initialize 请求")
		}
		initCancel()
		select {
		case err := <-initErrCh:
			if err == nil {
				t.Fatal("SSE initialize 取消后 Forward 应返回错误")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("SSE initialize 取消后 Forward 未及时返回")
		}

		select {
		case got := <-cancelCh:
			t.Fatalf("SSE initialize 被取消时不应发送 notifications/cancelled，实际收到 %+v", got)
		case <-time.After(150 * time.Millisecond):
		}
	})

	t.Run("stdio", func(t *testing.T) {
		t.Parallel()
		client := newStdioClient(UpstreamSpec{ID: "stdio-cancel", Transport: "stdio"})
		stdin := &lockedBuffer{}
		client.mu.Lock()
		client.stdin = stdin
		client.started = true
		client.mu.Unlock()
		t.Cleanup(func() { _ = client.close() })

		waitLines := func(want int) []string {
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				raw := strings.TrimSpace(stdin.String())
				if raw != "" {
					lines := strings.Split(raw, "\n")
					if len(lines) >= want {
						return lines
					}
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatalf("等待 stdin 至少 %d 行超时，实际: %q", want, stdin.String())
			return nil
		}

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			_, _, err := client.sendRPC(ctx, "tools/call", json.RawMessage(`{"name":"slow"}`))
			errCh <- err
		}()

		lines := waitLines(1)
		var sentReq struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(lines[0]), &sentReq); err != nil {
			cancel()
			t.Fatalf("解析 stdio 请求失败: %v", err)
		}

		cancel()
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("stdio sendRPC 取消后应返回错误")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stdio sendRPC 取消后未及时返回")
		}

		lines = waitLines(2)
		var cancelNote struct {
			Method string                 `json:"method"`
			Params cancelledParamsPayload `json:"params"`
		}
		if err := json.Unmarshal([]byte(lines[1]), &cancelNote); err != nil {
			t.Fatalf("解析 stdio notifications/cancelled 失败: %v (%s)", err, lines[1])
		}
		if cancelNote.Method != "notifications/cancelled" || cancelNote.Params.RequestID != sentReq.ID || cancelNote.Params.Reason != "request cancelled" {
			t.Fatalf("stdio 取消通知不符: %+v (want requestId=%d)", cancelNote, sentReq.ID)
		}

		// initialize 被取消时不得发 notifications/cancelled。
		initCtx, initCancel := context.WithCancel(context.Background())
		initErrCh := make(chan error, 1)
		go func() {
			_, _, err := client.sendRPC(initCtx, "initialize", json.RawMessage(`{}`))
			initErrCh <- err
		}()

		_ = waitLines(3)
		initCancel()
		select {
		case err := <-initErrCh:
			if err == nil {
				t.Fatal("stdio initialize 取消后应返回错误")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("stdio initialize 取消后未及时返回")
		}

		finalLines := strings.Split(strings.TrimSpace(stdin.String()), "\n")
		if len(finalLines) != 3 {
			t.Fatalf("initialize 被取消时不应写入 notifications/cancelled，实际共 %d 行: %q", len(finalLines), stdin.String())
		}
	})
}

func TestHubToolsListCache(t *testing.T) {
	t.Parallel()
	var toolsListCalls atomic.Int64
	var toolRev atomic.Int64
	toolRev.Store(1)
	var pushListChanged atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		switch req.Method {
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo":      map[string]any{"name": "cache-srv", "version": "1.0"},
				},
			})
		case "tools/list":
			toolsListCalls.Add(1)
			var p struct {
				Cursor string `json:"cursor"`
			}
			if len(req.Params) > 0 {
				_ = json.Unmarshal(req.Params, &p)
			}
			toolName := fmt.Sprintf("tool_v%d", toolRev.Load())
			if p.Cursor != "" {
				toolName = "page_" + p.Cursor
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result": map[string]any{
					"tools": []map[string]any{{"name": toolName}},
				},
			})
		case "ping":
			if pushListChanged.Swap(false) {
				toolRev.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				note, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"method":  "notifications/tools/list_changed",
				})
				resp, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      *req.ID,
					"result":  map[string]any{},
				})
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: %s\n\n", note, resp)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{}})
		default:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)

	hub := NewHub(nil)
	t.Cleanup(func() { _ = hub.Close() })
	hub.SetUpstreams([]UpstreamSpec{{ID: "srv", Transport: "http", URL: srv.URL}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. 连续两次无游标 tools/list，上游只收到一次。
	res1, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || !strings.Contains(string(res1), "tool_v1") {
		t.Fatalf("首次 tools/list 异常: res=%s rpcErr=%v err=%v", string(res1), rpcErr, err)
	}
	res2, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || !strings.Contains(string(res2), "tool_v1") {
		t.Fatalf("第二次 tools/list 异常: res=%s rpcErr=%v err=%v", string(res2), rpcErr, err)
	}
	if got := toolsListCalls.Load(); got != 1 {
		t.Fatalf("连续两次无游标 tools/list 应命中缓存只打上游 1 次，实际 %d 次", got)
	}

	// 2. 带 cursor 的分页请求每次都转发。
	for i := 0; i < 2; i++ {
		resPage, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage(`{"cursor":"p2"}`))
		if err != nil || rpcErr != nil || !strings.Contains(string(resPage), "page_p2") {
			t.Fatalf("带 cursor 请求失败: res=%s rpcErr=%v err=%v", string(resPage), rpcErr, err)
		}
	}
	if got := toolsListCalls.Load(); got != 3 {
		t.Fatalf("两次带 cursor 请求后上游应共收到 3 次，实际 %d 次", got)
	}

	// 3. POST 的 SSE 响应流推送 notifications/tools/list_changed 后，下一次 tools/list 重新转发。
	pushListChanged.Store(true)
	if _, rpcErr, err := hub.Forward(ctx, "srv", "ping", nil); err != nil || rpcErr != nil {
		t.Fatalf("ping 失败: rpcErr=%v err=%v", rpcErr, err)
	}
	res3, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || !strings.Contains(string(res3), "tool_v2") {
		t.Fatalf("list_changed 后应重新拉取到 tool_v2: res=%s rpcErr=%v err=%v", string(res3), rpcErr, err)
	}
	if got := toolsListCalls.Load(); got != 4 {
		t.Fatalf("list_changed 后上游应共收到 4 次，实际 %d 次", got)
	}

	// 4. SetUpstreams 变更该上游 spec 后，下一次 tools/list 重新转发。
	hub.SetUpstreams([]UpstreamSpec{{
		ID:        "srv",
		Transport: "http",
		URL:       srv.URL,
		Headers:   map[string]string{"X-Spec": "updated"},
	}})
	res4, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || !strings.Contains(string(res4), "tool_v2") {
		t.Fatalf("SetUpstreams 后 tools/list 失败: res=%s rpcErr=%v err=%v", string(res4), rpcErr, err)
	}
	if got := toolsListCalls.Load(); got != 5 {
		t.Fatalf("SetUpstreams 变更 spec 后上游应共收到 5 次，实际 %d 次", got)
	}
}

type fakeAuthGenTokenSource struct {
	gen atomic.Uint64
}

func (f *fakeAuthGenTokenSource) AccessToken(context.Context, string) (string, error) {
	return fmt.Sprintf("tok-%d", f.gen.Load()), nil
}

func (f *fakeAuthGenTokenSource) AuthGeneration() uint64 {
	return f.gen.Load()
}

func TestHubToolsListCacheInvalidatedByAuthChange(t *testing.T) {
	var toolsListCalls atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result": map[string]any{
					"protocolVersion": "2025-06-18",
					"serverInfo":      map[string]any{"name": "auth-cache-srv", "version": "1.0"},
				},
			})
		case "tools/list":
			callNum := toolsListCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      *req.ID,
				"result": map[string]any{
					"tools": []map[string]any{{"name": fmt.Sprintf("tool_%d", callNum)}},
				},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": *req.ID, "result": map[string]any{}})
		}
	}))
	t.Cleanup(srv.Close)

	tokens := &fakeAuthGenTokenSource{}
	hub := NewHub(tokens)
	t.Cleanup(func() { _ = hub.Close() })
	hub.SetUpstreams([]UpstreamSpec{{ID: "srv", Transport: "http", URL: srv.URL, OAuth: true}})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	res1, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || !strings.Contains(string(res1), "tool_1") {
		t.Fatalf("首次 tools/list 异常: res=%s rpcErr=%v err=%v", string(res1), rpcErr, err)
	}
	res2, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || !strings.Contains(string(res2), "tool_1") {
		t.Fatalf("第二次 tools/list 异常: res=%s rpcErr=%v err=%v", string(res2), rpcErr, err)
	}
	if got := toolsListCalls.Load(); got != 1 {
		t.Fatalf("连续两次 tools/list 应命中缓存只打上游 1 次，实际 %d 次", got)
	}

	tokens.gen.Add(1)
	if ver := hub.toolsListVersion("srv"); ver != 0 {
		t.Fatalf("auth generation 变化后 toolsListVersion 应为 0，实际 %d", ver)
	}

	res3, rpcErr, err := hub.Forward(ctx, "srv", "tools/list", json.RawMessage("{}"))
	if err != nil || rpcErr != nil || !strings.Contains(string(res3), "tool_2") {
		t.Fatalf("auth generation 变化后 tools/list 应重新到达上游: res=%s rpcErr=%v err=%v", string(res3), rpcErr, err)
	}
	if got := toolsListCalls.Load(); got != 2 {
		t.Fatalf("auth generation 变化后上游应收到 2 次 tools/list，实际 %d 次", got)
	}
}
