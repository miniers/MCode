package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"mcode/internal/openartifact"
)

func callOpenArtifactTool(t *testing.T, srv *Server, threadID, workDir, name string, args map[string]any) (string, bool) {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      name,
			"arguments": args,
		},
	})
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if workDir != "" {
		req.Header.Set(WorkDirHeader, workDir)
	}
	rec := httptest.NewRecorder()
	if threadID != "" {
		srv.ServeThread(rec, req, threadID)
	} else {
		req.Header.Set("Authorization", "Bearer "+srv.Token())
		srv.Handler().ServeHTTP(rec, req)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call 状态码期望 200, 实际为 %d", rec.Code)
	}
	var res map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	text, isError, _ := parseToolText(t, res)
	return text, isError
}

func listMCPToolNames(t *testing.T, srv *Server, threadID string) []string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal tools/list failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if threadID != "" {
		srv.ServeThread(rec, req, threadID)
	} else {
		req.Header.Set("Authorization", "Bearer "+srv.Token())
		srv.Handler().ServeHTTP(rec, req)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list 状态码期望 200, 实际为 %d", rec.Code)
	}
	var res struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal tools/list failed: %v", err)
	}
	names := make([]string, 0, len(res.Result.Tools))
	for _, tool := range res.Result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func TestOpenArtifactToolForwardsCall(t *testing.T) {
	t.Parallel()
	srv, st, projDir := setupTestMCPWithOptions(t, nil)
	registerThread(t, st, "thread-oa-1", "proj-test-1")

	var gotThread, gotCwd, gotName string
	var gotArgs json.RawMessage
	call := func(_ context.Context, threadID, cwd, name string, args json.RawMessage) (json.RawMessage, error) {
		gotThread = threadID
		gotCwd = cwd
		gotName = name
		gotArgs = append([]byte(nil), args...)
		return json.RawMessage(`{"url":"open-artifact://artifact/1","ok":true}`), nil
	}

	artifactDef := OpenArtifactToolDefinition(openartifact.ToolDefinition{
		Name:        "Artifact",
		Description: "Create or update artifact",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"},"title":{"type":"string"}}}`),
	}, call)
	dataDef := OpenArtifactToolDefinition(openartifact.ToolDefinition{
		Name:        "ArtifactData",
		Description: "Read or write artifact data",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}}}`),
	}, call)
	commentsDef := OpenArtifactToolDefinition(openartifact.ToolDefinition{
		Name:        "ArtifactComments",
		Description: "Read or reply comments",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}}}`),
	}, call)

	if !artifactDef.IsWrite || !dataDef.IsWrite {
		t.Fatalf("Artifact 与 ArtifactData 应标记为写操作: artifact=%v data=%v", artifactDef.IsWrite, dataDef.IsWrite)
	}
	if commentsDef.IsWrite {
		t.Fatal("ArtifactComments 不应标记为写操作")
	}

	srv.RegisterTool(artifactDef)
	text, isError := callOpenArtifactTool(t, srv, "thread-oa-1", projDir, "Artifact", map[string]any{
		"action": "publish",
		"title":  "Arch",
	})
	if isError {
		t.Fatalf("预期调用成功, 实际错误: %s", text)
	}
	if gotThread != "thread-oa-1" || gotCwd != projDir || gotName != "Artifact" {
		t.Fatalf("转发参数不匹配: thread=%q cwd=%q name=%q", gotThread, gotCwd, gotName)
	}
	var parsedArgs map[string]any
	if err := json.Unmarshal(gotArgs, &parsedArgs); err != nil {
		t.Fatalf("解析传出 args 失败: %v", err)
	}
	if parsedArgs["action"] != "publish" || parsedArgs["title"] != "Arch" {
		t.Fatalf("传出 args 不匹配: %#v", parsedArgs)
	}
	if text != `{"url":"open-artifact://artifact/1","ok":true}` {
		t.Fatalf("返回值未原样保留: %q", text)
	}
}

func TestOpenArtifactToolRequiresSession(t *testing.T) {
	t.Parallel()
	srv, _, projDir := setupTestMCPWithOptions(t, nil)
	called := false
	srv.RegisterTool(OpenArtifactToolDefinition(openartifact.ToolDefinition{
		Name:        "Artifact",
		Description: "Artifact tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}}}`),
	}, func(_ context.Context, _, _, _ string, _ json.RawMessage) (json.RawMessage, error) {
		called = true
		return json.RawMessage(`{}`), nil
	}))

	text, isError := callOpenArtifactTool(t, srv, "", projDir, "Artifact", map[string]any{"action": "list"})
	if !isError {
		t.Fatalf("无会话时应报错, 实际成功: %s", text)
	}
	if called {
		t.Fatal("无会话时不应调用底层 call")
	}
	var payload ToolErrorPayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("解析错误载荷失败: %v", err)
	}
	if payload.Code != CodeForbidden {
		t.Fatalf("错误码期望 %q, 实际 %q", CodeForbidden, payload.Code)
	}
}

func TestOpenArtifactToolErrors(t *testing.T) {
	t.Parallel()
	srv, st, projDir := setupTestMCPWithOptions(t, nil)
	registerThread(t, st, "thread-oa-err", "proj-test-1")

	srv.RegisterTool(OpenArtifactToolDefinition(openartifact.ToolDefinition{
		Name:        "Artifact",
		Description: "Artifact tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}}}`),
	}, func(_ context.Context, _, _, _ string, args json.RawMessage) (json.RawMessage, error) {
		var a map[string]any
		_ = json.Unmarshal(args, &a)
		switch a["action"] {
		case "rpc_err":
			return nil, &openartifact.RPCError{
				Code:     -32000,
				DataCode: "not_found",
				Message:  "artifact missing",
				Fix:      "run quickstart first",
			}
		case "tool_err":
			return json.RawMessage(`{"error":{"code":"conflict","message":"version changed","fix":"re-read first"}}`), nil
		default:
			return nil, errors.New("bridge disconnected")
		}
	}))

	text, isError := callOpenArtifactTool(t, srv, "thread-oa-err", projDir, "Artifact", map[string]any{"action": "rpc_err"})
	if !isError {
		t.Fatalf("RPCError 应返回 isError=true: %s", text)
	}
	var payload ToolErrorPayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("解析 RPCError 载荷失败: %v", err)
	}
	if payload.Code != "not_found" || payload.Message != "artifact missing" || payload.Hint != "run quickstart first" {
		t.Fatalf("RPCError 字段未保留: %#v", payload)
	}

	text, isError = callOpenArtifactTool(t, srv, "thread-oa-err", projDir, "Artifact", map[string]any{"action": "tool_err"})
	if !isError || !strings.Contains(text, "version changed") {
		t.Fatalf("tool_err 应返回 isError=true 并原样保留文本, got isError=%v text=%q", isError, text)
	}

	text, isError = callOpenArtifactTool(t, srv, "thread-oa-err", projDir, "Artifact", map[string]any{"action": "other"})
	if !isError || !strings.Contains(text, "bridge disconnected") {
		t.Fatalf("普通错误应返回 isError=true 并包含错误信息, got isError=%v text=%q", isError, text)
	}
}

func TestOpenArtifactRegisterAndUnregister(t *testing.T) {
	t.Parallel()
	srv, st, _ := setupTestMCPWithOptions(t, nil)
	registerThread(t, st, "thread-oa-reg", "proj-test-1")

	srv.RegisterTool(OpenArtifactToolDefinition(openartifact.ToolDefinition{
		Name:        "Artifact",
		Description: "Artifact tool",
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}, nil))

	if _, ok := srv.registry.Get("Artifact"); !ok {
		t.Fatal("RegisterTool 后应能在 registry 查到 Artifact")
	}
	hasTool := func(names []string, target string) bool {
		for _, n := range names {
			if n == target {
				return true
			}
		}
		return false
	}
	if !hasTool(listMCPToolNames(t, srv, "thread-oa-reg"), "Artifact") {
		t.Fatal("tools/list 应包含已注册的 Artifact")
	}

	srv.UnregisterTool("Artifact")
	if _, ok := srv.registry.Get("Artifact"); ok {
		t.Fatal("UnregisterTool 后不应再在 registry 查到 Artifact")
	}
	if hasTool(listMCPToolNames(t, srv, "thread-oa-reg"), "Artifact") {
		t.Fatal("UnregisterTool 后 tools/list 不应再包含 Artifact")
	}
}

func TestOpenArtifactRegistryConcurrency(t *testing.T) {
	t.Parallel()
	srv, _, _ := setupTestMCPWithOptions(t, nil)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := fmt.Sprintf("Artifact_%d", id%3)
			for i := 0; i < 40; i++ {
				srv.RegisterTool(ToolDefinition{
					Name:        name,
					Description: "concurrent tool",
					InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
				})
				_ = srv.registry.ListTools()
				_, _ = srv.registry.Get(name)
				_ = srv.registry.entryActions(kanbanFamily, nil)
				srv.UnregisterTool(name)
			}
		}(g)
	}
	wg.Wait()
}
