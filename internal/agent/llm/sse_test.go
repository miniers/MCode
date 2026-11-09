package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSSEReadEvent(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		events []sseEvent
	}{
		{name: "crlf", input: "event: ping\r\ndata: hello\r\n\r\n", events: []sseEvent{{Event: "ping", Data: "hello"}}},
		{name: "multi-data", input: "data: a\ndata: b\n\n", events: []sseEvent{{Data: "a\nb"}}},
		{name: "comment", input: ": keep-alive\ndata: a\n\n", events: []sseEvent{{Data: "a"}}},
		{name: "no-blank-line-at-eof", input: "data: a", events: []sseEvent{{Data: "a"}}},
		{name: "half-line-at-eof", input: "data: a\ndata: b", events: []sseEvent{{Data: "a\nb"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := newSSEReader(strings.NewReader(tc.input))
			for i, want := range tc.events {
				got, err := reader.readEvent()
				if err != nil {
					t.Fatalf("event %d: %v", i, err)
				}
				if *got != want {
					t.Fatalf("event %d: got %+v, want %+v", i, *got, want)
				}
			}
			if _, err := reader.readEvent(); !errors.Is(err, io.EOF) {
				t.Fatalf("expected EOF, got %v", err)
			}
		})
	}
}

func TestStreamIdleTimeout(t *testing.T) {
	previous := streamIdleTimeout
	streamIdleTimeout = 50 * time.Millisecond
	defer func() { streamIdleTimeout = previous }()
	for _, name := range []string{"anthropic", "openai"} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			var client Client
			if name == "anthropic" {
				client = NewAnthropic(AnthropicConfig{BaseURL: server.URL})
			} else {
				client = NewOpenAI(OpenAIConfig{BaseURL: server.URL})
			}
			_, err := client.Stream(context.Background(), Request{}, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || !apiErr.Retryable || apiErr.Body != "stream idle timeout" {
				t.Fatalf("error: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = client.Stream(ctx, Request{}, nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		})
	}
}

func TestIdleReaderResetsOnBytes(t *testing.T) {
	previous := streamIdleTimeout
	streamIdleTimeout = 200 * time.Millisecond
	defer func() { streamIdleTimeout = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	go func() {
		defer writer.Close()
		for i := 0; i < 4; i++ {
			fmt.Fprint(writer, "x")
			time.Sleep(60 * time.Millisecond)
		}
	}()
	idle := newIdleReader(reader, cancel)
	data, err := io.ReadAll(idle)
	timedOut := idle.stop()
	if err != nil || string(data) != "xxxx" || timedOut || ctx.Err() != nil {
		t.Fatalf("data %q, error %v, timeout %t", data, err, timedOut)
	}
}
