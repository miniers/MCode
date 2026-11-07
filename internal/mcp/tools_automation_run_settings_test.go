package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mcode/internal/automation"
	"mcode/internal/store"
)

// automationRunSettingsArgs 是创建/更新自动化时公共的模型与权限参数。
func automationRunSettingsArgs() map[string]any {
	return map[string]any{
		"provider":     "anthropic",
		"model":        "claude-sonnet-4-5",
		"effort":       "high",
		"runtime_mode": "autoAcceptEdits",
	}
}

// TestAutomationCreateRunSettings 验证 automation_create 把模型与权限写进 spec。
func TestAutomationCreateRunSettings(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	args := automationRunSettingsArgs()
	args["name"] = "模型设置"
	args["prompt"] = "检查"
	args["schedule_type"] = "interval"
	args["schedule_value"] = "3600"

	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": "automation_create", "arguments": args},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if isError {
		t.Fatalf("创建自动化失败: %v", payload)
	}
	automationID, _ := payload["automation_id"].(string)
	if automationID == "" {
		t.Fatalf("响应缺少 automation_id: %v", payload)
	}

	def, err := srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.Provider != "anthropic" || def.Spec.Model != "claude-sonnet-4-5" ||
		def.Spec.Effort != "high" || def.Spec.RuntimeMode != "auto" {
		t.Fatalf("模型与权限未写入 spec: %+v", def.Spec)
	}
}

// TestAutomationCreateRejectsUnknownRuntimeMode 验证非法 runtime_mode 返回参数错误。
func TestAutomationCreateRejectsUnknownRuntimeMode(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_create",
			"arguments": map[string]any{
				"name":           "非法权限",
				"prompt":         "检查",
				"schedule_type":  "interval",
				"schedule_value": "3600",
				"runtime_mode":   "godmode",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	if _, isError := toolCallResult(t, res); !isError {
		t.Fatalf("非法 runtime_mode 应返回参数错误: %v", res)
	}
}

// TestAutomationUpdateRunSettings 验证 automation_update 能改这四项，也能用空串清空。
func TestAutomationUpdateRunSettings(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mcpURL := ts.URL + "/mcp"

	status, res := postRPC(t, mcpURL, srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_create",
			"arguments": map[string]any{
				"name":           "更新设置",
				"prompt":         "检查",
				"schedule_type":  "interval",
				"schedule_value": "3600",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if isError {
		t.Fatalf("创建自动化失败: %v", payload)
	}
	automationID, _ := payload["automation_id"].(string)

	// 写入四项设置
	updateArgs := automationRunSettingsArgs()
	updateArgs["automation_id"] = automationID
	status, res = postRPC(t, mcpURL, srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params":  map[string]any{"name": "automation_update", "arguments": updateArgs},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError = toolCallResult(t, res)
	if isError {
		t.Fatalf("更新自动化失败: %v", payload)
	}
	def, err := srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.Model != "claude-sonnet-4-5" || def.Spec.RuntimeMode != "auto" {
		t.Fatalf("更新后 spec 未生效: %+v", def.Spec)
	}

	// 传空串恢复默认
	clearArgs := map[string]any{
		"automation_id": automationID,
		"provider":      "",
		"model":         "",
		"effort":        "",
		"runtime_mode":  "",
	}
	status, res = postRPC(t, mcpURL, srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params":  map[string]any{"name": "automation_update", "arguments": clearArgs},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError = toolCallResult(t, res)
	if isError {
		t.Fatalf("清空设置失败: %v", payload)
	}
	def, err = srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.Provider != "" || def.Spec.Model != "" || def.Spec.Effort != "" || def.Spec.RuntimeMode != "" {
		t.Fatalf("空串应恢复默认: %+v", def.Spec)
	}
}

// createAutomationForTest 建一个默认间隔 3600 秒的自动化，返回其 ID。
func createAutomationForTest(t *testing.T, mcpURL, token, projDir, name string, extra map[string]any) string {
	t.Helper()
	args := map[string]any{
		"name":           name,
		"prompt":         "检查",
		"schedule_type":  "interval",
		"schedule_value": "3600",
	}
	for k, v := range extra {
		args[k] = v
	}
	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": "automation_create", "arguments": args},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if isError {
		t.Fatalf("创建自动化失败: %v", payload)
	}
	automationID, _ := payload["automation_id"].(string)
	if automationID == "" {
		t.Fatalf("响应缺少 automation_id: %v", payload)
	}
	return automationID
}

// TestAutomationCreateMaxRunsAndCatchUp 验证 automation_create 写入次数上限与补跑开关。
func TestAutomationCreateMaxRunsAndCatchUp(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	automationID := createAutomationForTest(t, ts.URL+"/mcp", srv.Token(), projDir, "次数与补跑", map[string]any{
		"max_runs": 200,
		"catch_up": false,
	})
	def, err := srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.MaxRuns != 200 || def.Spec.EffectiveMaxRuns() != 200 {
		t.Fatalf("max_runs 未写入 spec: %+v", def.Spec)
	}
	// 旧字段同步镜像，保证旧版本二进制也能拦截。
	if def.Spec.MaxIterations != 200 {
		t.Fatalf("max_iterations 未镜像: %+v", def.Spec)
	}
	if def.Spec.CatchUpEnabled() {
		t.Fatalf("catch_up=false 未生效: %+v", def.Spec)
	}
}

// TestAutomationUpdateMaxRunsAndCatchUp 验证 automation_update 能改这两个字段，
// 且与 stop_when、模型设置合并时互不覆盖。
func TestAutomationUpdateMaxRunsAndCatchUp(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	mcpURL := ts.URL + "/mcp"

	automationID := createAutomationForTest(t, mcpURL, srv.Token(), projDir, "更新次数", map[string]any{
		"stop_when": "没有新内容",
	})

	update := func(id float64, args map[string]any) {
		t.Helper()
		args["automation_id"] = automationID
		status, res := postRPC(t, mcpURL, srv.Token(), projDir, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "tools/call",
			"params":  map[string]any{"name": "automation_update", "arguments": args},
		})
		if status != http.StatusOK {
			t.Fatalf("状态码期望 200, 实际为 %d", status)
		}
		if payload, isError := toolCallResult(t, res); isError {
			t.Fatalf("更新自动化失败: %v", payload)
		}
	}

	update(2, map[string]any{"max_runs": 7, "catch_up": false})
	def, err := srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.EffectiveMaxRuns() != 7 || def.Spec.CatchUpEnabled() {
		t.Fatalf("更新后次数/补跑未生效: %+v", def.Spec)
	}
	if def.Spec.StopWhen != "没有新内容" {
		t.Fatalf("只改次数时 stop_when 被覆盖: %+v", def.Spec)
	}

	// 同时带 stop_when 与 max_runs，两部分都要写入同一份 spec。
	update(3, map[string]any{"stop_when": "全部完成", "max_runs": 9, "model": "claude-sonnet-4-5"})
	def, err = srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.StopWhen != "全部完成" || def.Spec.EffectiveMaxRuns() != 9 ||
		def.Spec.Model != "claude-sonnet-4-5" || def.Spec.CatchUpEnabled() {
		t.Fatalf("多分支合并后 spec 不一致: %+v", def.Spec)
	}

	// max_runs=0 表示取消上限，旧 MaxIterations 也要清零。
	update(4, map[string]any{"max_runs": 0})
	def, err = srv.getAutomation().Get(context.Background(), automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.MaxRuns != 0 || def.Spec.MaxIterations != 0 || def.Spec.EffectiveMaxRuns() != 0 {
		t.Fatalf("max_runs=0 未清除上限: %+v", def.Spec)
	}
}

// TestAutomationListExposesCatchUpMetadata 验证列表输出 max_runs、catch_up 与运行来源信息。
func TestAutomationListExposesCatchUpMetadata(t *testing.T) {
	srv, st, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	automationID := createAutomationForTest(t, ts.URL+"/mcp", srv.Token(), projDir, "列表补跑", map[string]any{
		"max_runs": 12,
		"catch_up": false,
	})
	if err := st.CreateAutomationRun(context.Background(), store.AutomationRun{
		ID:           "run-catch-up-1",
		AutomationID: automationID,
		Status:       "succeeded",
		Trigger:      automation.RunTriggerCatchUp,
		MissedCount:  2,
		StartedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("插入运行记录失败: %v", err)
	}

	status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": "automation_list", "arguments": map[string]any{}},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if isError {
		t.Fatalf("列表失败: %v", payload)
	}
	automations, _ := payload["automations"].([]any)
	if len(automations) != 1 {
		t.Fatalf("期望 1 个自动化, 实际为 %v", automations)
	}
	item, _ := automations[0].(map[string]any)
	if got, _ := item["max_runs"].(float64); got != 12 {
		t.Fatalf("max_runs 未输出: %v", item)
	}
	if got, ok := item["catch_up"].(bool); !ok || got {
		t.Fatalf("catch_up 未输出 false: %v", item)
	}
	runs, _ := item["recent_runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("期望 1 条运行记录, 实际为 %v", runs)
	}
	run, _ := runs[0].(map[string]any)
	if run["trigger"] != automation.RunTriggerCatchUp || run["missed_count"] != float64(2) {
		t.Fatalf("运行来源未输出: %v", run)
	}
}

// TestAutomationCreateRejectsInvalidMaxRuns 验证 max_runs 超出 0..1000 时返回参数错误。
func TestAutomationCreateRejectsInvalidMaxRuns(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	for _, maxRuns := range []int{-1, 1001} {
		status, res := postRPC(t, ts.URL+"/mcp", srv.Token(), projDir, map[string]any{
			"jsonrpc": "2.0",
			"id":      1,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "automation_create",
				"arguments": map[string]any{
					"name":           "非法次数",
					"prompt":         "检查",
					"schedule_type":  "interval",
					"schedule_value": "3600",
					"max_runs":       maxRuns,
				},
			},
		})
		if status != http.StatusOK {
			t.Fatalf("状态码期望 200, 实际为 %d", status)
		}
		if _, isError := toolCallResult(t, res); !isError {
			t.Fatalf("max_runs=%d 应返回参数错误", maxRuns)
		}
	}
}
