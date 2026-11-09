package frontdesk

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcode/internal/agent/llm"
	"mcode/internal/apisource"
)

func TestClientForMagpieClaude(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path: %s", r.URL.Path)
		}
		fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
	}))
	defer server.Close()
	src := apisource.Source{Kind: "magpie", Protocol: apisource.ProtocolOpenAI, Magpie: true, BaseURL: server.URL + "/v1", Models: []string{"claude-haiku"}}
	client, model, err := ClientFor(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	if model != "claude-haiku" {
		t.Fatalf("model: %s", model)
	}
	if _, err := client.Stream(context.Background(), llm.Request{Model: model}, nil); err != nil {
		t.Fatal(err)
	}
}
