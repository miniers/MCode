package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestBuiltinMCPServerWiring 验证应用装配后的内置 MCP 服务器可通过 HTTP 完成
// initialize → tools/list → tools/call(kanban_read_board) 全流程，并校验 bearer 鉴权。
func TestBuiltinMCPServerWiring(t *testing.T) {
	workDir := t.TempDir()
	instance, err := Open(Options{
		DataDir:  filepath.Join(t.TempDir(), "data"),
		Provider: "memory",
		WorkDir:  workDir,
	})
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer instance.Close()

	url := instance.mcp.URL()
	token := instance.mcp.Token()
	if url == "" || token == "" {
		t.Fatalf("内置 MCP 服务器未启动: url=%q", url)
	}

	post := func(body any, withToken bool) (int, map[string]any) {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("序列化请求失败: %v", err)
		}
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("构造请求失败: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Mcode-Workdir", workDir)
		if withToken {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("请求失败: %v", err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		return resp.StatusCode, out
	}

	// 1. 缺少 bearer token 时拒绝
	if status, _ := post(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}, false); status != http.StatusUnauthorized {
		t.Fatalf("无 token 期望 401, 实际 %d", status)
	}

	// 2. initialize
	status, res := post(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params":  map[string]any{"protocolVersion": "2025-06-18"},
	}, true)
	if status != http.StatusOK {
		t.Fatalf("initialize 期望 200, 实际 %d", status)
	}
	if _, ok := res["result"]; !ok {
		t.Fatalf("initialize 缺少 result: %v", res)
	}

	// 3. tools/list 包含 kanban 工具
	status, res = post(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}, true)
	if status != http.StatusOK {
		t.Fatalf("tools/list 期望 200, 实际 %d", status)
	}
	listResult, _ := res["result"].(map[string]any)
	tools, _ := listResult["tools"].([]any)
	found := false
	for _, item := range tools {
		if tm, ok := item.(map[string]any); ok && tm["name"] == "kanban_read_board" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tools/list 缺少 kanban_read_board: %v", tools)
	}

	// 4. tools/call：以带前缀的工具名调用 kanban_read_board，应返回当前项目的看板
	status, res = post(map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "mcp__mcode__kanban_read_board",
			"arguments": map[string]any{},
		},
	}, true)
	if status != http.StatusOK {
		t.Fatalf("tools/call 期望 200, 实际 %d", status)
	}
	callResult, _ := res["result"].(map[string]any)
	if isError, _ := callResult["isError"].(bool); isError {
		t.Fatalf("kanban_read_board 返回 isError=true: %v", callResult)
	}
	content, _ := callResult["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("kanban_read_board 缺少 content: %v", callResult)
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	var board map[string]any
	if err := json.Unmarshal([]byte(text), &board); err != nil {
		t.Fatalf("看板载荷不是合法 JSON: %v (%s)", err, text)
	}
	if projectID, _ := board["project_id"].(string); projectID == "" {
		t.Fatalf("看板缺少 project_id: %v", board)
	}
	if _, ok := board["columns"].(map[string]any); !ok {
		t.Fatalf("看板缺少 columns: %v", board)
	}
	if asOf, _ := board["as_of"].(string); !strings.Contains(asOf, "T") {
		t.Fatalf("看板缺少 as_of 时间戳: %v", board["as_of"])
	}
}
