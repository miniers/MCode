package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeTestFrame 以 Content-Length 分帧写出一条消息（模拟 copilot 服务端）。
func writeTestFrame(w io.Writer, body []byte) error {
	header := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n"
	if _, err := io.WriteString(w, header); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}

// TestConnCallContentLengthRoundTrip 校验写端严格按 `Content-Length: N\r\n\r\n<body>` 分帧，
// 读端能取回结果并解析。
func TestConnCallContentLengthRoundTrip(t *testing.T) {
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	conn := NewConn(resR, reqW)
	defer conn.Close()

	serverDone := make(chan error, 1)
	go func() {
		br := bufio.NewReader(reqR)
		header, err := br.ReadString('\n')
		if err != nil {
			serverDone <- err
			return
		}
		if !strings.HasPrefix(header, "Content-Length: ") {
			serverDone <- fmt.Errorf("bad framing header %q", header)
			return
		}
		size, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "Content-Length: ")))
		if err != nil {
			serverDone <- err
			return
		}
		sep, err := br.ReadString('\n')
		if err != nil {
			serverDone <- err
			return
		}
		if sep != "\r\n" {
			serverDone <- fmt.Errorf("bad header separator %q", sep)
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(br, body); err != nil {
			serverDone <- err
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			serverDone <- err
			return
		}
		if msg.Method != "ping" || msg.Params["message"] != "mcode" {
			serverDone <- fmt.Errorf("unexpected request %s", body)
			return
		}
		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      msg.ID,
			"result":  map[string]any{"protocolVersion": 3},
		})
		serverDone <- writeTestFrame(resW, resp)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var pong struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if err := conn.Call(ctx, "ping", map[string]any{"message": "mcode"}, &pong); err != nil {
		t.Fatalf("Call failed: %v", err)
	}
	if pong.ProtocolVersion != 3 {
		t.Fatalf("protocolVersion = %d, want 3", pong.ProtocolVersion)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

// TestConnAnswersServerRequestWithOriginalID 校验服务端请求的响应带上原 id。
func TestConnAnswersServerRequestWithOriginalID(t *testing.T) {
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	conn := NewConn(resR, reqW)
	defer conn.Close()

	conn.SetRequestHandler(func(method string, params json.RawMessage) (any, error) {
		if method != "userInput.request" {
			return nil, fmt.Errorf("unexpected method %q", method)
		}
		return map[string]any{"answer": "b.txt", "wasFreeform": false}, nil
	})

	go func() {
		body, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      11,
			"method":  "userInput.request",
			"params":  map[string]any{"question": "Which file?"},
		})
		_ = writeTestFrame(resW, body)
	}()

	raw, err := readFrame(bufio.NewReader(reqR))
	if err != nil {
		t.Fatalf("read response frame: %v", err)
	}
	var resp struct {
		ID     json.RawMessage `json:"id"`
		Result map[string]any  `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.ID) != "11" {
		t.Fatalf("response id = %s, want 11", resp.ID)
	}
	if resp.Result["answer"] != "b.txt" {
		t.Fatalf("response result = %v", resp.Result)
	}
}

// TestReadFrameRejectsOversizedFrame 校验超过 16MB 上限的单帧被拒绝。
func TestReadFrameRejectsOversizedFrame(t *testing.T) {
	header := "Content-Length: " + strconv.Itoa(maxFrameSize+1) + "\r\n\r\n"
	if _, err := readFrame(bufio.NewReader(strings.NewReader(header))); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
}

// TestReadFrameRejectsMissingContentLength 校验缺少 Content-Length 的帧被拒绝。
func TestReadFrameRejectsMissingContentLength(t *testing.T) {
	if _, err := readFrame(bufio.NewReader(strings.NewReader("\r\n{}"))); err == nil {
		t.Fatal("expected error for missing Content-Length")
	}
}
