package llm

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestDefaultHTTPClientHTTP2HealthCheck(t *testing.T) {
	client := newDefaultHTTPClient()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport.HTTP2 == nil || transport.HTTP2.SendPingTimeout <= 0 || transport.HTTP2.PingTimeout <= 0 {
		t.Fatalf("HTTP/2 health check not configured: %#v", client.Transport)
	}
	if client.Timeout != 0 || transport.ResponseHeaderTimeout != 2*time.Minute {
		t.Fatalf("unexpected timeouts: client=%v headers=%v", client.Timeout, transport.ResponseHeaderTimeout)
	}
}

type failingTransport struct {
	mode   string
	closed int
	writer *io.PipeWriter
}

func (t *failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if t.mode == "request" {
		return nil, errors.New("connection failed")
	}
	var body io.ReadCloser = io.NopCloser(failingReader{})
	if t.mode == "idle" {
		reader, writer := io.Pipe()
		t.writer = writer
		body = reader
	}
	status := http.StatusOK
	if t.mode == "error response" {
		status = http.StatusBadGateway
	}
	header := make(http.Header)
	if t.mode != "nonstream" {
		header.Set("Content-Type", "text/event-stream")
	}
	return &http.Response{StatusCode: status, Header: header, Body: body}, nil
}

func (t *failingTransport) CloseIdleConnections() { t.closed++ }

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("connection failed") }

func testCloseIdleConnections(t *testing.T, stream func(*http.Client) error, modes ...string) {
	t.Helper()
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			transport := &failingTransport{mode: mode}
			if mode == "idle" {
				previous := streamIdleTimeout
				streamIdleTimeout = 20 * time.Millisecond
				defer func() { streamIdleTimeout = previous }()
			}
			err := stream(&http.Client{Transport: transport})
			if transport.writer != nil {
				transport.writer.Close()
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) || !apiErr.Retryable {
				t.Fatalf("expected retryable APIError, got %v", err)
			}
			if mode == "idle" && apiErr.Body != "stream idle timeout" {
				t.Fatalf("expected idle timeout, got %v", err)
			}
			if transport.closed < 1 {
				t.Fatal("idle connections not closed")
			}
		})
	}
}
