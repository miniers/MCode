package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 代理对外声明的能力只受上游是否声明 resources / prompts 影响，其余键一律丢弃。
func TestProxyCapabilities_收敛上游能力(t *testing.T) {
	cases := []struct {
		name     string
		upstream string
		want     []string
	}{
		{name: "上游能力为空", upstream: "", want: []string{"tools"}},
		{name: "上游能力为nil字面量", upstream: "null", want: []string{"tools"}},
		{name: "上游能力非法JSON", upstream: "{not json", want: []string{"tools"}},
		{name: "上游能力不是对象", upstream: `["tools"]`, want: []string{"tools"}},
		{name: "上游只声明tools", upstream: `{"tools":{"listChanged":true}}`, want: []string{"tools"}},
		{
			name:     "上游声明resources和prompts",
			upstream: `{"tools":{},"resources":{"subscribe":true,"listChanged":true},"prompts":{"listChanged":true}}`,
			want:     []string{"tools", "resources", "prompts"},
		},
		{
			name:     "logging等未透传能力被丢弃",
			upstream: `{"tools":{},"logging":{},"completions":{},"experimental":{"x":1},"resources":{}}`,
			want:     []string{"tools", "resources"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := proxyCapabilities(json.RawMessage(tc.upstream))
			if len(got) != len(tc.want) {
				t.Fatalf("能力键数量不符：期望 %v，实际 %v", tc.want, got)
			}
			for _, key := range tc.want {
				sub, ok := got[key].(map[string]any)
				if !ok {
					t.Fatalf("能力 %q 应存在且为空对象，实际 %v", key, got)
				}
				// 代理不转发 list_changed 与 subscribe，子字段必须是空对象。
				if len(sub) != 0 {
					t.Errorf("能力 %q 应为空对象，实际 %v", key, sub)
				}
			}
		})
	}
}

// 上游声明 resources/prompts 时，代理 initialize 响应应透出这两个能力。
func TestProxyInitialize_透传上游能力(t *testing.T) {
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.ID == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			resp["result"] = map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities": map[string]any{
					"tools":     map[string]any{},
					"resources": map[string]any{"subscribe": true},
					"prompts":   map[string]any{"listChanged": true},
					"logging":   map[string]any{},
				},
				"serverInfo": map[string]any{"name": "mock-caps", "version": "1.0.0"},
			}
		default:
			resp["result"] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(upstreamServer.Close)

	hub := NewHub(nil)
	t.Cleanup(func() { _ = hub.Close() })
	hub.SetUpstreams([]UpstreamSpec{{ID: "caps-srv", Transport: "http", URL: upstreamServer.URL}})

	proxy := NewProxy(hub)
	t.Cleanup(func() { _ = proxy.Close() })

	code, resp := postProxy(t, proxy.Handler(), "/mcp/caps-srv", proxy.Token(), map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"protocolVersion": "2025-06-18"},
	})
	if code != http.StatusOK {
		t.Fatalf("initialize 期望 200，实际 %d", code)
	}
	resObj, _ := resp["result"].(map[string]any)
	caps, ok := resObj["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 capabilities: %v", resp)
	}
	for _, key := range []string{"tools", "resources", "prompts"} {
		sub, ok := caps[key].(map[string]any)
		if !ok {
			t.Fatalf("能力 %q 应存在，实际 %v", key, caps)
		}
		if len(sub) != 0 {
			t.Errorf("能力 %q 应为空对象，实际 %v", key, sub)
		}
	}
	if _, ok := caps["logging"]; ok {
		t.Errorf("未透传的 logging 不应出现在能力里: %v", caps)
	}
	// 其余字段保持不变。
	serverInfo, _ := resObj["serverInfo"].(map[string]any)
	if serverInfo["name"] != "mcode-proxy" {
		t.Errorf("serverInfo.name 应保持 mcode-proxy，实际 %v", serverInfo["name"])
	}
}

// 上游不可用时 initialize 仍成功，能力退回只含 tools。
func TestProxyInitialize_上游不可用时退回tools(t *testing.T) {
	// 已关闭的 HTTP 上游：连接失败，代理应把它当成“取不到能力”而不是初始化失败。
	deadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := deadServer.URL
	deadServer.Close()

	hub := NewHub(nil)
	t.Cleanup(func() { _ = hub.Close() })
	hub.SetUpstreams([]UpstreamSpec{{ID: "dead-srv", Transport: "http", URL: deadURL}})

	proxy := NewProxy(hub)
	t.Cleanup(func() { _ = proxy.Close() })

	code, resp := postProxy(t, proxy.Handler(), "/mcp/dead-srv", proxy.Token(), map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
	})
	if code != http.StatusOK {
		t.Fatalf("上游不可用时 initialize 仍应返回 200，实际 %d", code)
	}
	resObj, _ := resp["result"].(map[string]any)
	caps, ok := resObj["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 capabilities: %v", resp)
	}
	if len(caps) != 1 {
		t.Fatalf("能力应只有 tools，实际 %v", caps)
	}
	if _, ok := caps["tools"].(map[string]any); !ok {
		t.Errorf("能力应包含 tools，实际 %v", caps)
	}
}
