package apisource

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcode/internal/agent/llm"
)

func TestNewClientRouting(t *testing.T) {
	for _, tc := range []struct {
		name          string
		protocol      Protocol
		magpie        bool
		model         string
		baseSuffix    string
		path          string
		authorization string
		apiKey        string
	}{
		{name: "magpie-claude", protocol: ProtocolOpenAI, magpie: true, model: "claude-haiku-4-5", baseSuffix: "/v1", path: "/v1/messages", authorization: "Bearer key"},
		{name: "magpie-deepseek", protocol: ProtocolOpenAI, magpie: true, model: "deepseek-chat", path: "/chat/completions", authorization: "Bearer key"},
		{name: "anthropic", protocol: ProtocolAnthropic, model: "claude-haiku-4-5", path: "/v1/messages", apiKey: "key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path || r.Header.Get("Authorization") != tc.authorization || r.Header.Get("x-api-key") != tc.apiKey {
					t.Errorf("request: %s %v", r.URL.Path, r.Header)
				}
				if r.URL.Path == "/chat/completions" {
					fmt.Fprint(w, "data: [DONE]\n\n")
				} else {
					fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
				}
			}))
			defer server.Close()
			src := Source{Protocol: tc.protocol, Magpie: tc.magpie, BaseURL: server.URL + tc.baseSuffix}
			_, err := NewClient(src, "key", tc.model, ClientOptions{}).Stream(context.Background(), llm.Request{Model: tc.model}, nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
