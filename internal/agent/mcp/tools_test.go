package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcode/internal/agent/tools"
	"mcode/internal/harness"
)

func TestToolsBuiltinExternalAndErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "bad name!", "description": "schema test", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
				map[string]any{"name": "do", "description": "do it", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			var params struct {
				Name string `json:"name"`
			}
			b, _ := json.Marshal(req.Params)
			_ = json.Unmarshal(b, &params)
			if params.Name == "do" {
				result = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": "failed"}, map[string]any{"type": "image"}}}
			} else {
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "built"}}}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	all := Tools(context.Background(), []Server{{Name: "svc", URL: server.URL, Builtin: true}, {Name: "ext", URL: server.URL}, {Name: "broken", URL: "http://127.0.0.1:1"}})
	if len(all) != 4 {
		t.Fatalf("expected two builtin tools plus two gateway tools, got %d", len(all))
	}
	if all[0].Name != "mcp__svc__bad_name_" || !all[0].ReadOnly {
		t.Fatalf("bad builtin mapping: %#v", all[0])
	}
	if all[1].Name != "mcp__svc__do" {
		t.Fatalf("bad second builtin mapping: %#v", all[1])
	}
	if all[2].Name != "mcode_mcp_list" || all[3].Name != "mcode_mcp_call" {
		t.Fatalf("unexpected external tools: %q, %q", all[2].Name, all[3].Name)
	}
	call := all[3]
	result, err := call.Run(context.Background(), tools.Env{}, json.RawMessage(`{"server":"ext","tool":"do"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || !strings.Contains(result.Text, "[image content]") {
		t.Fatalf("result mapping failed: %#v", result)
	}
	listed, err := all[2].Run(context.Background(), tools.Env{}, json.RawMessage(`{"server":"ext"}`))
	if err != nil || !strings.Contains(listed.Text, "bad name!") {
		t.Fatalf("list failed: %#v %v", listed, err)
	}
	info, err := all[2].Run(context.Background(), tools.Env{}, json.RawMessage(`{"server":"ext","tool":"do"}`))
	if err != nil || !strings.Contains(info.Text, "inputSchema") {
		t.Fatalf("schema lookup failed: %#v %v", info, err)
	}
	built, err := all[0].Run(context.Background(), tools.Env{}, json.RawMessage(`{}`))
	if err != nil || built.Text != "built" {
		t.Fatalf("builtin call failed: %#v %v", built, err)
	}
}

func TestServersHeaders(t *testing.T) {
	turn := harness.Turn{ThreadID: "thread/a", WorkDir: "/work/project"}
	servers := Servers(turn, harness.BuiltinMCP{ThreadBase: "https://thread/", ThreadToken: "thread-token", URL: "https://shared", Token: "shared-token"}, []harness.MCPEndpoint{{Name: "remote", URL: "https://remote", Token: "remote-token"}, {Name: "mcode", URL: "https://ignored"}, {Name: "mcode_approval", URL: "https://ignored"}})
	if len(servers) != 2 {
		t.Fatalf("unexpected servers: %#v", servers)
	}
	if servers[0].URL != "https://thread/thread%2Fa" || servers[0].Token != "thread-token" || servers[0].Headers["X-Mcode-Workdir"] != "" {
		t.Fatalf("thread endpoint headers: %#v", servers[0])
	}
	shared := Servers(harness.Turn{WorkDir: "/project"}, harness.BuiltinMCP{URL: "https://shared", Token: "shared-token"}, nil)[0]
	if shared.Headers["X-Mcode-Workdir"] != "/project" || shared.Token != "shared-token" {
		t.Fatalf("shared endpoint headers: %#v", shared)
	}
	if servers[1].Headers["Authorization"] != "Bearer remote-token" {
		t.Fatalf("external authorization missing: %#v", servers[1])
	}
	if servers[0].Headers["Authorization"] != "" {
		t.Fatal(fmt.Sprint("thread token should be applied by client, not duplicated in headers"))
	}
}

func TestExternalCallValidationErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "do", "inputSchema": map[string]any{"type": "object"}}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	all := Tools(context.Background(), []Server{{Name: "ext", URL: server.URL}})
	call := all[len(all)-1]
	for _, input := range []string{
		`{"tool":"do"}`,
		`{"server":"ext"}`,
		`{"server":"missing","tool":"do"}`,
	} {
		result, err := call.Run(context.Background(), tools.Env{}, json.RawMessage(input))
		if err != nil || !result.IsError {
			t.Errorf("expected IsError for %s, got %#v, %v", input, result, err)
		}
	}
}

func TestNoExternalGateway(t *testing.T) {
	if got := Tools(context.Background(), nil); len(got) != 0 {
		t.Fatalf("expected no tools, got %#v", got)
	}
}

func TestBuiltinResultTruncated(t *testing.T) {
	big := strings.Repeat("x", 100*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": protocolVersion}
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
			return
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "big", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": big}}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer server.Close()
	all := Tools(context.Background(), []Server{{Name: "svc", URL: server.URL, Builtin: true}})
	if len(all) != 1 {
		t.Fatalf("expected one builtin tool, got %d", len(all))
	}
	result, err := all[0].Run(context.Background(), tools.Env{}, json.RawMessage(`{}`))
	if err != nil || result.IsError || !strings.Contains(result.Text, "Output truncated.") || len(result.Text) >= len(big) {
		t.Fatalf("text length=%d err=%v", len(result.Text), err)
	}
}

// writeRPCResult 返回一个简单的 JSON-RPC 响应。
func writeRPCResult(w http.ResponseWriter, req rpcRequest, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

// mcpDiscoveryHandler 构造只暴露一个工具的假 MCP 端点。
func mcpDiscoveryHandler(onInitialize func()) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			if onInitialize != nil {
				onInitialize()
			}
			writeRPCResult(w, req, map[string]any{"protocolVersion": protocolVersion})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeRPCResult(w, req, map[string]any{"tools": []any{map[string]any{"name": "do", "inputSchema": map[string]any{"type": "object"}}}})
		case "tools/call":
			writeRPCResult(w, req, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}})
		}
	}
}

func TestToolsCachesDiscovery(t *testing.T) {
	resetDiscoveryCache()
	t.Cleanup(resetDiscoveryCache)
	var initializes int32
	server := httptest.NewServer(mcpDiscoveryHandler(func() { atomic.AddInt32(&initializes, 1) }))
	defer server.Close()
	servers := []Server{{Name: "ext", URL: server.URL}}
	for i := 0; i < 2; i++ {
		if all := Tools(context.Background(), servers); len(all) != 2 {
			t.Fatalf("call %d: expected two gateway tools, got %d", i, len(all))
		}
	}
	if got := atomic.LoadInt32(&initializes); got != 1 {
		t.Fatalf("expected one initialize, got %d", got)
	}
}

func TestToolsRediscoverAfterTTL(t *testing.T) {
	resetDiscoveryCache()
	oldTTL := discoveryTTL
	discoveryTTL = 0
	t.Cleanup(func() {
		discoveryTTL = oldTTL
		resetDiscoveryCache()
	})
	var initializes int32
	server := httptest.NewServer(mcpDiscoveryHandler(func() { atomic.AddInt32(&initializes, 1) }))
	defer server.Close()
	servers := []Server{{Name: "ext", URL: server.URL}}
	Tools(context.Background(), servers)
	Tools(context.Background(), servers)
	if got := atomic.LoadInt32(&initializes); got != 2 {
		t.Fatalf("expected rediscovery, got %d initializes", got)
	}
}

func TestToolsDiscoversInParallel(t *testing.T) {
	resetDiscoveryCache()
	t.Cleanup(resetDiscoveryCache)
	first := httptest.NewServer(mcpDiscoveryHandler(func() { time.Sleep(300 * time.Millisecond) }))
	defer first.Close()
	second := httptest.NewServer(mcpDiscoveryHandler(func() { time.Sleep(300 * time.Millisecond) }))
	defer second.Close()
	start := time.Now()
	Tools(context.Background(), []Server{{Name: "a", URL: first.URL}, {Name: "b", URL: second.URL}})
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("parallel discovery took too long: %v", elapsed)
	}
}

func TestToolsPrunesStaleDiscoveryEntries(t *testing.T) {
	resetDiscoveryCache()
	t.Cleanup(resetDiscoveryCache)
	server := httptest.NewServer(mcpDiscoveryHandler(nil))
	defer server.Close()
	now := time.Now()
	discoveryMu.Lock()
	discoveryCache["stale-success"] = discoveryEntry{loaded: &loadedServer{}, fetched: now.Add(-time.Hour)}
	discoveryCache["stale-failure"] = discoveryEntry{loaded: &loadedServer{err: fmt.Errorf("boom")}, fetched: now.Add(-time.Minute)}
	discoveryMu.Unlock()
	Tools(context.Background(), []Server{{Name: "ext", URL: server.URL}})
	discoveryMu.Lock()
	defer discoveryMu.Unlock()
	if _, ok := discoveryCache["stale-success"]; ok {
		t.Fatal("stale success entry was not pruned")
	}
	if _, ok := discoveryCache["stale-failure"]; ok {
		t.Fatal("stale failure entry was not pruned")
	}
	if len(discoveryCache) != 1 {
		t.Fatalf("expected only the new entry, got %d", len(discoveryCache))
	}
	if _, ok := discoveryCache[discoveryKey(Server{URL: server.URL})]; !ok {
		t.Fatal("new entry missing from cache")
	}
}

func TestToolsCachesFailure(t *testing.T) {
	resetDiscoveryCache()
	oldTTL := failureTTL
	failureTTL = time.Hour
	t.Cleanup(func() {
		failureTTL = oldTTL
		resetDiscoveryCache()
	})
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	servers := []Server{{Name: "ext", URL: server.URL}}
	Tools(context.Background(), servers)
	Tools(context.Background(), servers)
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected cached failure, got %d requests", got)
	}
}

func TestToolsRediscoverAfterSessionError(t *testing.T) {
	resetDiscoveryCache()
	t.Cleanup(resetDiscoveryCache)
	var initializes int32
	var expired atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "initialize":
			atomic.AddInt32(&initializes, 1)
			writeRPCResult(w, req, map[string]any{"protocolVersion": protocolVersion})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeRPCResult(w, req, map[string]any{"tools": []any{map[string]any{"name": "do", "inputSchema": map[string]any{"type": "object"}}}})
		case "tools/call":
			if expired.Load() {
				http.Error(w, "session expired", http.StatusNotFound)
				return
			}
			writeRPCResult(w, req, map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}})
		}
	}))
	defer server.Close()
	servers := []Server{{Name: "svc", URL: server.URL, Builtin: true}}
	all := Tools(context.Background(), servers)
	if len(all) != 1 {
		t.Fatalf("expected one builtin tool, got %d", len(all))
	}
	if _, err := all[0].Run(context.Background(), tools.Env{}, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	expired.Store(true)
	result, err := all[0].Run(context.Background(), tools.Env{}, json.RawMessage(`{}`))
	if err != nil || !result.IsError {
		t.Fatalf("expected session error, got %#v, %v", result, err)
	}
	Tools(context.Background(), servers)
	if got := atomic.LoadInt32(&initializes); got != 2 {
		t.Fatalf("expected rediscovery after session error, got %d initializes", got)
	}
}
