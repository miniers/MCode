package llm

import (
	"net/http"
	"testing"
)

func TestDefaultHTTPClientHTTP2HealthCheck(t *testing.T) {
	client := newDefaultHTTPClient()
	if _, ok := client.Transport.(*http.Transport); ok || client.Transport == nil {
		t.Fatalf("transport is not wrapped: %T", client.Transport)
	}
	if client.Timeout != 0 {
		t.Fatalf("unexpected client timeout: %v", client.Timeout)
	}
}
