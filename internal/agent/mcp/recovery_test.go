package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExpiredSessionRecovers(t *testing.T) {
	initializations := 0
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		switch req.Method {
		case "initialize":
			initializations++
			w.Header().Set("Mcp-Session-Id", fmt.Sprintf("session-%d", initializations))
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/call":
			calls++
			if r.Header.Get("Mcp-Session-Id") == "session-1" {
				http.Error(w, "expired", http.StatusNotFound)
				return
			}
			if r.Header.Get("Mcp-Session-Id") != "session-2" {
				t.Error("会话未更新")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
	}))
	defer server.Close()
	client := &Client{URL: server.URL}
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(context.Background(), "test", nil); err != nil {
		t.Fatal(err)
	}
	if initializations != 2 || calls != 2 {
		t.Fatalf("初始化 %d 次，调用 %d 次", initializations, calls)
	}
}

func TestValidatedSchema(t *testing.T) {
	for _, tc := range []struct {
		schema  string
		invalid bool
	}{
		{`{"type":"array"}`, true},
		{`{"type":"object","properties":[]}`, true},
		{`{"type":"object","properties":null}`, true},
		{`{"properties":{}}`, false},
		{`{"type":"object","properties":{}}`, false},
	} {
		t.Run(tc.schema, func(t *testing.T) {
			schema, invalid := validatedSchema(json.RawMessage(tc.schema))
			var value map[string]any
			if err := json.Unmarshal(schema, &value); err != nil {
				t.Fatal(err)
			}
			if invalid != tc.invalid || value["type"] != "object" {
				t.Fatalf("schema=%s invalid=%v", schema, invalid)
			}
			if invalid && !strings.Contains(toolInfo("server", ToolDescriptor{Name: "tool", InputSchema: json.RawMessage(tc.schema)}), "原始参数结构无效，已忽略") {
				t.Fatal("缺少说明")
			}
		})
	}
}
