package llm

import (
	"net/http"
	"testing"
	"time"
)

func TestDefaultHTTPClientHTTP2HealthCheck(t *testing.T) {
	transport := newDefaultTransport()
	if transport.ResponseHeaderTimeout != 2*time.Minute {
		t.Fatalf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, 2*time.Minute)
	}
	if transport.HTTP2 == nil {
		t.Fatal("HTTP2 config is nil")
	}
	if transport.HTTP2.SendPingTimeout != http2PingTimeout || transport.HTTP2.PingTimeout != http2PingTimeout {
		t.Fatalf("HTTP2 ping timeouts = %v/%v, want %v", transport.HTTP2.SendPingTimeout, transport.HTTP2.PingTimeout, http2PingTimeout)
	}

	client := newDefaultHTTPClient()
	if client.Transport == nil {
		t.Fatal("client transport is nil")
	}
	if _, ok := client.Transport.(*http.Transport); ok {
		t.Fatalf("transport is not wrapped: %T", client.Transport)
	}
	if client.Timeout != 0 {
		t.Fatalf("unexpected client timeout: %v", client.Timeout)
	}
	if defaultHTTPClient.Timeout != 0 {
		t.Fatalf("unexpected default client timeout: %v", defaultHTTPClient.Timeout)
	}
}
