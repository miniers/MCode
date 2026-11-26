package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"mcode/internal/config"
	"mcode/internal/mcp"
	"mcode/internal/openartifact"
	"mcode/internal/remote"
	"mcode/internal/remote/wire"
)

func TestOpenArtifactHostDeliverWake(t *testing.T) {
	var gotThread, gotText string
	a := &App{
		openArtifactDeliverWake: func(_ context.Context, threadID, text string) error {
			gotThread = threadID
			gotText = text
			return nil
		},
	}
	host := openArtifactHost{app: a}
	rawWake := "<<<UNTRUSTED-DATA-abc>>>\n请处理批注\n<<<END-UNTRUSTED-DATA-abc>>>"
	if err := host.DeliverWake("thread-wake-1", rawWake); err != nil {
		t.Fatalf("DeliverWake 失败: %v", err)
	}
	if gotThread != "thread-wake-1" || gotText != rawWake {
		t.Fatalf("DeliverWake 未按原文转发: thread=%q text=%q", gotThread, gotText)
	}

	backend := remote.NewMemoryBackend(remote.NewStreamHub(remote.StreamHubOptions{}))
	t.Cleanup(backend.Close)
	aBackend := &App{backend: backend}
	hostBackend := openArtifactHost{app: aBackend}
	if err := hostBackend.DeliverWake("missing-thread", rawWake); err == nil {
		t.Fatal("会话不存在时 DeliverWake 应返回错误")
	}

	const realThreadID = "thread-wake-real"
	backend.AddThread(wire.ThreadSummary{ID: realThreadID, Title: "OA wake"}, nil)
	if err := hostBackend.DeliverWake(realThreadID, rawWake); err != nil {
		t.Fatalf("真实后端 DeliverWake 失败: %v", err)
	}
	page, err := backend.ThreadHistory(context.Background(), wire.HistoryParams{ThreadID: realThreadID})
	if err != nil {
		t.Fatalf("ThreadHistory 失败: %v", err)
	}
	if len(page.Messages) == 0 || page.Messages[0].Text != rawWake {
		t.Fatalf("真实后端未按原文投递: %#v", page.Messages)
	}
}

func TestOpenArtifactHostOpenURL(t *testing.T) {
	a := &App{}
	host := openArtifactHost{app: a}
	if host.OpenURL("t1", "http://127.0.0.1:4800/a/1") {
		t.Fatal("未注入 OpenPreviewNewTab 时应返回 false")
	}

	a.OpenPreviewNewTab = func(threadID, url string) error {
		return errors.New("window not visible")
	}
	if host.OpenURL("t1", "http://127.0.0.1:4800/a/1") {
		t.Fatal("OpenPreviewNewTab 返回错误时应返回 false")
	}

	var gotThread, gotURL string
	a.OpenPreviewNewTab = func(threadID, url string) error {
		gotThread = threadID
		gotURL = url
		return nil
	}
	if !host.OpenURL("t1", "http://127.0.0.1:4800/a/1") {
		t.Fatal("OpenPreviewNewTab 成功时应返回 true")
	}
	if gotThread != "t1" || gotURL != "http://127.0.0.1:4800/a/1" {
		t.Fatalf("OpenPreviewNewTab 参数不匹配: thread=%q url=%q", gotThread, gotURL)
	}
}

func TestOpenArtifactCloseUnregistersToolsAndClearsSkill(t *testing.T) {
	dataDir := t.TempDir()
	cfg := config.Defaults()
	cfg.OpenArtifact.Enabled = true
	if err := config.Save(dataDir, cfg); err != nil {
		t.Fatalf("config.Save 失败: %v", err)
	}

	mcpSrv := mcp.New(mcp.Options{})
	a := &App{
		dataDir: dataDir,
		mcp:     mcpSrv,
	}
	a.oa.gen = 1
	a.applyOpenArtifactStartResult(1, nil, nil, []openartifact.ToolDefinition{
		{
			Name:        "Artifact",
			Description: "Artifact tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		{
			Name:        "ArtifactData",
			Description: "Artifact data tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}, "## open-artifact skill", nil)

	if a.OpenArtifactSkill() != "## open-artifact skill" {
		t.Fatalf("skill 缓存未设置: %q", a.OpenArtifactSkill())
	}
	if !mcpHasTool(t, mcpSrv, "Artifact") || !mcpHasTool(t, mcpSrv, "ArtifactData") {
		t.Fatal("启动后 MCP 应包含 Artifact 与 ArtifactData")
	}

	if err := a.SetOpenArtifactEnabled(false); err != nil {
		t.Fatalf("SetOpenArtifactEnabled(false) 失败: %v", err)
	}
	loaded, err := config.Load(dataDir)
	if err != nil {
		t.Fatalf("config.Load 失败: %v", err)
	}
	if loaded.OpenArtifact.Enabled {
		t.Fatal("配置应已更新为 disabled")
	}
	if a.OpenArtifactSkill() != "" {
		t.Fatalf("关闭后应清空 skill 文本, 实际: %q", a.OpenArtifactSkill())
	}
	if mcpHasTool(t, mcpSrv, "Artifact") || mcpHasTool(t, mcpSrv, "ArtifactData") {
		t.Fatal("关闭后应从 MCP 注销 Artifact 与 ArtifactData")
	}

	// 验证重新注册后调用 closeOpenArtifact 也会注销工具并清空 skill。
	a.oa.gen++
	gen := a.oa.gen
	a.applyOpenArtifactStartResult(gen, nil, nil, []openartifact.ToolDefinition{
		{
			Name:        "Artifact",
			Description: "Artifact tool",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}, "## skill again", nil)
	if a.OpenArtifactSkill() == "" {
		t.Fatal("重新注册后 skill 应非空")
	}
	if !mcpHasTool(t, mcpSrv, "Artifact") {
		t.Fatal("重新注册后 MCP 应包含 Artifact")
	}
	a.closeOpenArtifact()
	if a.OpenArtifactSkill() != "" {
		t.Fatalf("closeOpenArtifact 后应清空 skill, 实际: %q", a.OpenArtifactSkill())
	}
	if mcpHasTool(t, mcpSrv, "Artifact") {
		t.Fatal("closeOpenArtifact 后应从 MCP 注销 Artifact")
	}
	if len(a.oa.toolNames) != 0 {
		t.Fatalf("closeOpenArtifact 后 toolNames 应为空, 实际: %v", a.oa.toolNames)
	}
}

func mcpHasTool(t *testing.T, srv *mcp.Server, target string) bool {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal tools/list failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+srv.Token())
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list 状态码期望 200, 实际 %d", rec.Code)
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
	for _, tool := range res.Result.Tools {
		if tool.Name == target {
			return true
		}
	}
	return false
}
