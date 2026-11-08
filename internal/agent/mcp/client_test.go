package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientInitializePaginationSessionAndSSE(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 && r.Header.Get("Mcp-Session-Id") != "session-1" {
			t.Errorf("missing session header: %q", r.Header.Get("Mcp-Session-Id"))
		}
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Mcp-Session-Id", "session-1")
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			if req.Params.(map[string]any)["cursor"] == nil {
				result = map[string]any{"tools": []any{map[string]any{"name": "one"}}, "nextCursor": "next"}
			} else {
				result = map[string]any{"tools": []any{map[string]any{"name": "two"}}}
			}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
		}
		response := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}
		if req.Method == "tools/call" {
			w.Header().Set("Content-Type", "text/event-stream")
			b, _ := json.Marshal(response)
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", b)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client := &Client{URL: server.URL}
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	listed, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[1].Name != "two" {
		t.Fatalf("unexpected tools: %#v", listed)
	}
	if _, err := client.CallTool(context.Background(), "one", nil); err != nil {
		t.Fatal(err)
	}
}
