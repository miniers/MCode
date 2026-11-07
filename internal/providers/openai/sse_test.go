package openai

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// chunkedReader 模拟分块到达的慢速/分片网络流。
type chunkedReader struct {
	data      []byte
	chunkSize int
	pos       int
}

func (c *chunkedReader) Read(p []byte) (n int, err error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	remaining := len(c.data) - c.pos
	toRead := c.chunkSize
	if toRead > remaining {
		toRead = remaining
	}
	if toRead > len(p) {
		toRead = len(p)
	}
	copy(p, c.data[c.pos:c.pos+toRead])
	c.pos += toRead
	return toRead, nil
}

func TestSSEReader_MultiLineData(t *testing.T) {
	input := "data: hello\ndata: world\n\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Data != "hello\nworld" {
		t.Fatalf("expected data %q, got %q", "hello\nworld", ev.Data)
	}

	_, err = reader.readEvent()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF, got %v", err)
	}
}

func TestSSEReader_CommentsAndEvent(t *testing.T) {
	input := ": ping comment\nevent: custom\nid: 42\ndata: payload\n\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Event != "custom" {
		t.Errorf("expected Event custom, got %q", ev.Event)
	}
	if ev.ID != "42" {
		t.Errorf("expected ID 42, got %q", ev.ID)
	}
	if ev.Data != "payload" {
		t.Errorf("expected Data payload, got %q", ev.Data)
	}
}

func TestSSEReader_CRLF(t *testing.T) {
	input := "event: ping\r\ndata: pong\r\n\r\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Event != "ping" || ev.Data != "pong" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestSSEReader_Done(t *testing.T) {
	input := "data: [DONE]\n\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isDoneChunk(ev.Data) {
		t.Fatalf("expected done chunk, got %q", ev.Data)
	}
}

func TestSSEReader_LongLine(t *testing.T) {
	longContent := strings.Repeat("A", 128*1024)
	input := "data: " + longContent + "\n\n"
	reader := newSSEReader(strings.NewReader(input))

	ev, err := reader.readEvent()
	if err != nil {
		t.Fatalf("readEvent on long line failed: %v", err)
	}
	if ev.Data != longContent {
		t.Fatalf("long line data mismatch: len(expected)=%d, len(got)=%d", len(longContent), len(ev.Data))
	}
}

func TestSSEReader_ChunkedReading(t *testing.T) {
	raw := "data: chunk1\ndata: chunk2\n\ndata: second\n\n"
	cr := &chunkedReader{
		data:      []byte(raw),
		chunkSize: 3, // 每次仅读取 3 字节
	}
	reader := newSSEReader(cr)

	ev1, err := reader.readEvent()
	if err != nil {
		t.Fatalf("ev1 error: %v", err)
	}
	if ev1.Data != "chunk1\nchunk2" {
		t.Fatalf("ev1 mismatch: %q", ev1.Data)
	}

	ev2, err := reader.readEvent()
	if err != nil {
		t.Fatalf("ev2 error: %v", err)
	}
	if ev2.Data != "second" {
		t.Fatalf("ev2 mismatch: %q", ev2.Data)
	}

	_, err = reader.readEvent()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF at end, got %v", err)
	}
}

func TestToolCalls_IncrementalAssembly(t *testing.T) {
	accMap := make(map[int]*toolCallAccumulator)

	// Chunk 1: index 0 开始
	feedToolCalls(accMap, []chatDeltaToolCall{
		{
			Index: 0,
			ID:    "call_001",
			Type:  "function",
			Function: chatDeltaFunction{
				Name:      "read_file",
				Arguments: `{"pa`,
			},
		},
	})

	// Chunk 2: index 0 参数继续，index 1 同时开始
	feedToolCalls(accMap, []chatDeltaToolCall{
		{
			Index: 0,
			Function: chatDeltaFunction{
				Arguments: `th":"main.go"}`,
			},
		},
		{
			Index: 1,
			ID:    "call_002",
			Type:  "function",
			Function: chatDeltaFunction{
				Name:      "list_files",
				Arguments: `{"recursive":`,
			},
		},
	})

	// Chunk 3: index 1 参数结束
	feedToolCalls(accMap, []chatDeltaToolCall{
		{
			Index: 1,
			Function: chatDeltaFunction{
				Arguments: `true}`,
			},
		},
	})

	calls := sortedToolCalls(accMap)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}

	if calls[0].Index != 0 || calls[0].ID != "call_001" || calls[0].Name != "read_file" || calls[0].Arguments != `{"path":"main.go"}` {
		t.Errorf("call 0 mismatch: %+v", calls[0])
	}

	if calls[1].Index != 1 || calls[1].ID != "call_002" || calls[1].Name != "list_files" || calls[1].Arguments != `{"recursive":true}` {
		t.Errorf("call 1 mismatch: %+v", calls[1])
	}
}

func TestNormalizeNonStreamResponse(t *testing.T) {
	// 网关忽略 stream:true 返回整段 JSON：文本、推理与工具调用都要归一化进单个块。
	raw := `{"choices":[{"message":{"content":"你好","reasoning_content":"想一想",` +
		`"tool_calls":[{"id":"call_a","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}},` +
		`{"id":"call_b","type":"function","function":{"name":"list_files","arguments":"{}"}}]},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":7,"completion_tokens":3}}`

	data, err := normalizeNonStreamResponse("application/json", []byte(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	chunk, err := parseChatChunk(data)
	if err != nil {
		t.Fatalf("normalized data is not a chunk: %v", err)
	}
	if len(chunk.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(chunk.Choices))
	}
	choice := chunk.Choices[0]
	if choice.Delta.Content != "你好" || choice.Delta.ReasoningContent != "想一想" {
		t.Fatalf("unexpected delta: %+v", choice.Delta)
	}
	if choice.FinishReason == nil || *choice.FinishReason != "tool_calls" {
		t.Fatalf("unexpected finish reason: %v", choice.FinishReason)
	}
	accMap := make(map[int]*toolCallAccumulator)
	feedToolCalls(accMap, choice.Delta.ToolCalls)
	calls := sortedToolCalls(accMap)
	if len(calls) != 2 || calls[0].Name != "read_file" || calls[1].Name != "list_files" {
		t.Fatalf("expected 2 distinct tool calls, got %+v", calls)
	}
	if chunk.Usage == nil || chunk.Usage.PromptTokens != 7 {
		t.Fatalf("expected usage preserved, got %+v", chunk.Usage)
	}

	// 200 但 body 是错误体：保留 error，交给流式处理统一报错。
	data, err = normalizeNonStreamResponse("application/json", []byte(`{"error":{"message":"rate limited"}}`))
	if err != nil {
		t.Fatalf("unexpected error for error body: %v", err)
	}
	chunk, err = parseChatChunk(data)
	if err != nil {
		t.Fatalf("error body normalized to invalid chunk: %v", err)
	}
	if chunk.Error == nil || chunk.Error.Message != "rate limited" {
		t.Fatalf("expected error preserved, got %+v", chunk.Error)
	}

	// 既不是 chat completion 也不是错误体：报错并带上响应开头，而不是静默空回答。
	if _, err = normalizeNonStreamResponse("text/html", []byte("<html>bad gateway</html>")); err == nil {
		t.Fatal("expected error for non-JSON body")
	} else if !strings.Contains(err.Error(), "bad gateway") {
		t.Fatalf("expected body excerpt in error, got %v", err)
	}
	if _, err = normalizeNonStreamResponse("application/json", []byte(`{"choices":[]}`)); err == nil {
		t.Fatal("expected error for empty choices")
	}
}
