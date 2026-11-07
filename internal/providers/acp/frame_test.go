package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// chunkedReader 模拟分块流式写入（如小 TCP 分包）。
type chunkedWriter struct {
	w         io.Writer
	chunkSize int
}

func (cw *chunkedWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := cw.chunkSize
		if n > len(p) {
			n = len(p)
		}
		written, err := cw.w.Write(p[:n])
		total += written
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

func TestLineFramingRead(t *testing.T) {
	r, w := io.Pipe()
	conn := NewConn(r, io.Discard)
	defer conn.Close()

	received := make(chan string, 2)
	conn.SetNotificationHandler(func(method string, params json.RawMessage) {
		received <- method
	})

	go func() {
		_, _ = fmt.Fprint(w, "{\"jsonrpc\":\"2.0\",\"method\":\"notif1\"}\n")
		_, _ = fmt.Fprint(w, "{\"jsonrpc\":\"2.0\",\"method\":\"notif2\"}\r\n")
	}()

	for _, expected := range []string{"notif1", "notif2"} {
		select {
		case m := <-received:
			if m != expected {
				t.Fatalf("expected method %s, got %s", expected, m)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for %s", expected)
		}
	}
}

func TestContentLengthFramingRead(t *testing.T) {
	r, w := io.Pipe()
	conn := NewConn(r, io.Discard)
	defer conn.Close()

	received := make(chan string, 1)
	conn.SetNotificationHandler(func(method string, params json.RawMessage) {
		received <- method
	})

	body := []byte("{\"jsonrpc\":\"2.0\",\"method\":\"content_length_msg\"}")
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))

	go func() {
		_, _ = w.Write([]byte(header))
		_, _ = w.Write(body)
	}()

	select {
	case m := <-received:
		if m != "content_length_msg" {
			t.Fatalf("expected content_length_msg, got %s", m)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for Content-Length framed message")
	}
}

func TestChunkedStreamRead(t *testing.T) {
	r, w := io.Pipe()
	cw := &chunkedWriter{w: w, chunkSize: 3}
	conn := NewConn(r, io.Discard)
	defer conn.Close()

	received := make(chan string, 1)
	conn.SetNotificationHandler(func(method string, params json.RawMessage) {
		received <- method
	})

	body := []byte("{\"jsonrpc\":\"2.0\",\"method\":\"chunked_test\"}")
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))

	go func() {
		_, _ = cw.Write(append([]byte(header), body...))
	}()

	select {
	case m := <-received:
		if m != "chunked_test" {
			t.Fatalf("expected chunked_test, got %s", m)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for chunked message")
	}
}

func TestConcurrentCallsMatchID(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	clientConn := NewConn(clientR, clientW)
	defer clientConn.Close()

	// 模拟对端服务端逻辑：读取请求并乱序并发回写响应
	go func() {
		serverConn := NewConn(serverR, serverW)
		defer serverConn.Close()

		serverConn.SetRequestHandler(func(method string, params json.RawMessage) (any, error) {
			var p map[string]int
			_ = json.Unmarshal(params, &p)
			idx := p["idx"]
			// 乱序延时模拟
			time.Sleep(time.Duration(10-(idx%10)) * time.Millisecond)
			return map[string]int{"replyIdx": idx}, nil
		})
		<-clientConn.closed
	}()

	const concurrency = 20
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			var res struct {
				ReplyIdx int `json:"replyIdx"`
			}
			err := clientConn.Call(context.Background(), "test_echo", map[string]int{"idx": idx}, &res)
			if err != nil {
				t.Errorf("call %d failed: %v", idx, err)
				return
			}
			if res.ReplyIdx != idx {
				t.Errorf("expected replyIdx %d, got %d", idx, res.ReplyIdx)
			}
		}(i)
	}

	wg.Wait()
}

func TestPeerRequestCallbackAndReply(t *testing.T) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()

	clientConn := NewConn(clientR, clientW)
	defer clientConn.Close()

	// 在 clientConn 注册 RequestHandler，处理 session/request_permission 等请求
	clientConn.SetRequestHandler(func(method string, params json.RawMessage) (any, error) {
		if method == "session/request_permission" {
			return map[string]any{
				"outcome": map[string]any{
					"outcome":  "selected",
					"optionId": "allow_once",
				},
			}, nil
		}
		return nil, errors.New("unknown method")
	})

	serverConn := NewConn(serverR, serverW)
	defer serverConn.Close()

	var result struct {
		Outcome struct {
			Outcome  string `json:"outcome"`
			OptionID string `json:"optionId"`
		} `json:"outcome"`
	}

	err := serverConn.Call(context.Background(), "session/request_permission", map[string]any{"tool": "bash"}, &result)
	if err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	if result.Outcome.Outcome != "selected" || result.Outcome.OptionID != "allow_once" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCloseFailsPendingCalls(t *testing.T) {
	r, w := io.Pipe()
	buf := new(bytes.Buffer)
	conn := NewConn(r, buf)

	errCh := make(chan error, 1)
	go func() {
		err := conn.Call(context.Background(), "hang_call", nil, nil)
		errCh <- err
	}()

	// 稍微等待请求已经注册进 pendingCalls
	time.Sleep(50 * time.Millisecond)

	// 关闭连接
	_ = w.Close()
	_ = conn.Close()

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrConnClosed) {
			t.Fatalf("expected ErrConnClosed, got: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending Call did not return after Close")
	}
}

// TestCallKeepsResponseAtClose 验证对端回复后立即断开时，已到达的响应不会被关闭事件吞掉。
func TestCallKeepsResponseAtClose(t *testing.T) {
	clientR, serverW := io.Pipe()
	conn := NewConn(clientR, io.Discard)
	defer conn.Close()

	go func() {
		// 响应先落盘，再立刻关闭写端
		_, _ = fmt.Fprint(serverW, `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`+"\n")
		_ = serverW.Close()
	}()

	var res struct {
		OK bool `json:"ok"`
	}
	if err := conn.Call(context.Background(), "probe", nil, &res); err != nil {
		t.Fatalf("Call failed although a response was delivered: %v", err)
	}
	if !res.OK {
		t.Fatal("expected the queued response to be returned")
	}
}

func TestLineFrameTooLarge(t *testing.T) {
	// 未结束的小行：返回部分数据与 EOF
	small := bufio.NewReaderSize(strings.NewReader("no newline"), 64)
	line, err := readLineFrame(small)
	if !errors.Is(err, io.EOF) || string(line) != "no newline" {
		t.Fatalf("expected partial line with EOF, got %q, %v", line, err)
	}

	// 超过 16MB 的行：直接报错，不继续累积
	huge := bufio.NewReader(io.MultiReader(bytes.NewReader(bytes.Repeat([]byte{'a'}, maxFrameSize+1)), strings.NewReader("\n")))
	if _, err := readLineFrame(huge); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}

func TestContentLengthTooLarge(t *testing.T) {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", maxFrameSize+1)
	_, err := readContentLengthPayload(bufio.NewReader(strings.NewReader(header)))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("expected ErrFrameTooLarge, got %v", err)
	}
}
