package llm

import (
	"errors"
	"io"
	"strings"
	"testing"
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
