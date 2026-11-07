package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWithMethodTimeout 校验按方法放宽超时：tools/call 放宽到 toolCallTimeout，
// 其他方法不加截止时间，调用方自带截止时间时保持原样。
func TestWithMethodTimeout(t *testing.T) {
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
