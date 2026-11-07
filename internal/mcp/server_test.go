package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"mcode/internal/automation"
	"mcode/internal/kanban"
	"mcode/internal/store"
)

type testDriver struct {
	mu     sync.Mutex
	starts []kanban.SessionRequest
	states map[string]kanban.SessionState
}

func newTestDriver() *testDriver {
	return &testDriver{
		states: make(map[string]kanban.SessionState),
	}
}

func (m *testDriver) StartSession(ctx context.Context, req kanban.SessionRequest) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts = append(m.starts, req)
	threadID := uuid.NewString()
	m.states[threadID] = kanban.SessionState{Running: false, LastAssistantText: "done"}
	return threadID, nil
}

func (m *testDriver) SessionState(ctx context.Context, threadID string) (kanban.SessionState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[threadID]; ok {
		return st, nil
	}
	return kanban.SessionState{Running: false, LastAssistantText: "done"}, nil
}

type testProjects struct {
	dir string
}

// testSchedulerLock 是测试用的调度锁句柄：加锁永远成功，Close 为空操作。
type testSchedulerLock struct{}

func (testSchedulerLock) Close() error        { return nil }
func (testSchedulerLock) WriteInfo(any) error { return nil }

func (p *testProjects) ProjectRoot(ctx context.Context, projectID string) (string, error) {
	return p.dir, nil
}

func setupTestMCP(t *testing.T) (*Server, *store.Store, string) {
	return setupTestMCPWithOptions(t, nil)
}

// setupTestMCPWithOptions 创建测试用 MCP 服务器，mutate 可在 New 之前改写 Options。
func setupTestMCPWithOptions(t *testing.T, mutate func(*Options)) (*Server, *store.Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	projDir := t.TempDir()
	ctx := context.Background()
	proj := store.Project{
		ID:   "proj-test-1",
		Name: "test-proj",
		Path: projDir,
	}
	if err := s.UpsertProject(ctx, proj); err != nil {
		t.Fatalf("插入测试项目失败: %v", err)
	}

	driver := newTestDriver()
	projects := &testProjects{dir: projDir}

	kanbanSvc := kanban.NewService(kanban.Options{
		Store:    s,
		Driver:   driver,
		Projects: projects,
	})
	t.Cleanup(func() { kanbanSvc.Close() })

	autoSvc := automation.New(automation.Options{
		Store:     s,
		Driver:    driver,
		Projects:  projects,
		Evaluator: &automation.Evaluator{},
		DataDir:   t.TempDir(),
		// 调度锁与 MCP 用例无关，注入总是成功的假锁，避免测试之间争抢真实锁文件。
		AcquireLock: func(string) (automation.LockHandle, error) { return testSchedulerLock{}, nil },
	})

	opts := Options{
		Store:      s,
		Kanban:     kanbanSvc,
		Automation: autoSvc,
	}
	if mutate != nil {
		mutate(&opts)
	}

	srv := New(opts)

	return srv, s, projDir
}

func postRPC(t *testing.T, url, token, workDir string, reqBody any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request failed: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if workDir != "" {
		req.Header.Set(WorkDirHeader, workDir)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request failed: %v", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	var respMap map[string]any
	if len(bodyBytes) > 0 {
		_ = json.Unmarshal(bodyBytes, &respMap)
	}
	return resp.StatusCode, respMap
}

// postThreadRPC 以 threadID 为线程作用域直接调用 ServeThread，等价于经 Proxy 访问 /mcp/mcode/{id}，
// 用于需要会话作用域的自动化与看板工具测试。
func postThreadRPC(t *testing.T, srv *Server, threadID string, reqBody any) (int, map[string]any) {
	t.Helper()
	b, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal request failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeThread(rec, req, threadID)

	var respMap map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &respMap)
	}
	return rec.Code, respMap
}

// registerThread 在存储中登记测试线程，让线程作用域请求能按会话解析项目。
func registerThread(t *testing.T, s *store.Store, threadID, projectID string) {
	t.Helper()
	if err := s.CreateThread(context.Background(), store.Thread{
		ID:        threadID,
		ProjectID: projectID,
		Title:     "test thread",
		Provider:  "memory",
	}); err != nil {
		t.Fatalf("登记测试线程 %s 失败: %v", threadID, err)
	}
}

func TestUnauthorized(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"

	// 1. 无 token 返回 401
	status, _ := postRPC(t, mcpURL, "", projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401, 实际为 %d", status)
	}

	// 2. 错误 token 返回 401
	status, _ = postRPC(t, mcpURL, "wrong-token", projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("期望状态码 401, 实际为 %d", status)
	}
}

func TestMCPFlow(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	// 1. initialize
	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
		},
	})
	if status != http.StatusOK {
		t.Fatalf("initialize 状态码期望 200, 实际为 %d", status)
	}
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize 响应缺少 result: %v", res)
	}
	if result["protocolVersion"] != "2025-06-18" {
		t.Fatalf("protocolVersion 期望 2025-06-18, 实际为 %v", result["protocolVersion"])
	}
	serverInfo, _ := result["serverInfo"].(map[string]any)
	if serverInfo["name"] != ServerName {
		t.Fatalf("serverInfo name 期望 %s, 实际为 %v", ServerName, serverInfo["name"])
	}

	// 2. notifications/initialized
	status, _ = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	if status != http.StatusAccepted {
		t.Fatalf("notifications/initialized 状态码期望 202, 实际为 %d", status)
	}

	// 3. tools/list (断言包含三组工具名)
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	if status != http.StatusOK {
		t.Fatalf("tools/list 状态码期望 200, 实际为 %d", status)
	}
	listResult, _ := res["result"].(map[string]any)
	toolsArray, _ := listResult["tools"].([]any)
	hasKanban := false
	hasContext := false
	hasAutomation := false
	hasMemoryPropose := false
	for _, item := range toolsArray {
		tm, _ := item.(map[string]any)
		name, _ := tm["name"].(string)
		if name == "context_draft_memo" {
			t.Fatal("tools/list 不应包含 context_draft_memo")
		}
		if name == "memory_propose" {
			hasMemoryPropose = true
		}
		if strings.HasPrefix(name, "kanban_") {
			hasKanban = true
		}
		if strings.HasPrefix(name, "context_") {
			hasContext = true
		}
		if strings.HasPrefix(name, "automation_") {
			hasAutomation = true
		}
	}
	if !hasMemoryPropose {
		t.Fatal("tools/list 应包含 memory_propose")
	}
	if !hasKanban || !hasContext || !hasAutomation {
		t.Fatalf("tools/list 必须包含三组工具: kanban=%v, context=%v, automation=%v", hasKanban, hasContext, hasAutomation)
	}

	// 4. tools/call: kanban_read_board
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "kanban_read_board",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_read_board 状态码期望 200, 实际为 %d", status)
	}
	callRes, _ := res["result"].(map[string]any)
	if isError, _ := callRes["isError"].(bool); isError {
		t.Fatalf("kanban_read_board 返回 isError=true: %v", callRes)
	}

	// 5. tools/call: kanban_create_task
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_task",
			"arguments": map[string]any{
				"title":      "MCP Test Task 1",
				"request_id": "req-task-1",
				"notes":      "Test task description",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("kanban_create_task 状态码期望 200, 实际为 %d", status)
	}
	callRes, _ = res["result"].(map[string]any)
	if isError, _ := callRes["isError"].(bool); isError {
		t.Fatalf("kanban_create_task 返回 isError=true: %v", callRes)
	}

	// 6. tools/call: context_list_documents
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "context_list_documents",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("context_list_documents 状态码期望 200, 实际为 %d", status)
	}
	callRes, _ = res["result"].(map[string]any)
	if isError, _ := callRes["isError"].(bool); isError {
		t.Fatalf("context_list_documents 返回 isError=true: %v", callRes)
	}
	if listText, _ := toolCallText(t, res); !strings.Contains(listText, "Standard documents:") || !strings.Contains(listText, "path: .mcode/context/instructions.md") {
		t.Fatalf("context_list_documents 应输出 .mcode/context 下的标准文档段, 实际为 %s", listText)
	}

	// 8. tools/call: automation_list
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      7,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "automation_list",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("automation_list 状态码期望 200, 实际为 %d", status)
	}
	callRes, _ = res["result"].(map[string]any)
	if isError, _ := callRes["isError"].(bool); isError {
		t.Fatalf("automation_list 返回 isError=true: %v", callRes)
	}

	// 9. tools/call: automation_create
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      8,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_create",
			"arguments": map[string]any{
				"name":           "Hourly Auto Test",
				"prompt":         "Run hourly health check",
				"schedule_type":  "interval",
				"schedule_value": "3600",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("automation_create 状态码期望 200, 实际为 %d", status)
	}
	callRes, _ = res["result"].(map[string]any)
	if isError, _ := callRes["isError"].(bool); isError {
		t.Fatalf("automation_create 返回 isError=true: %v", callRes)
	}

	// 10. 测试同一幂等键重复 kanban_create_task 只建一张卡
	idempKey := "idemp-card-unique-1"
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      9,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_task",
			"arguments": map[string]any{
				"title":      "Idempotent Task",
				"request_id": idempKey,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("首次幂等创建状态码期望 200, 实际为 %d", status)
	}
	firstResult, _ := res["result"].(map[string]any)
	if isError, _ := firstResult["isError"].(bool); isError {
		t.Fatalf("首次幂等创建失败: %v", firstResult)
	}

	// 重复调用
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      10,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_task",
			"arguments": map[string]any{
				"title":      "Idempotent Task",
				"request_id": idempKey,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("重复幂等调用状态码期望 200, 实际为 %d", status)
	}
	secondResult, _ := res["result"].(map[string]any)
	if isError, _ := secondResult["isError"].(bool); isError {
		t.Fatalf("重复幂等调用失败: %v", secondResult)
	}

	// 校验数据库中仅创建了一张同名卡
	ctx := context.Background()
	tasks, err := s.ListTasks(ctx, "proj-test-1")
	if err != nil {
		t.Fatalf("查询任务列表失败: %v", err)
	}
	idempTaskCount := 0
	for _, tsk := range tasks {
		if tsk.Title == "Idempotent Task" {
			idempTaskCount++
		}
	}
	if idempTaskCount != 1 {
		t.Fatalf("期望幂等卡数量为 1, 实际为 %d", idempTaskCount)
	}
}

func TestServerLifecycle(t *testing.T) {
	srv, _, _ := setupTestMCP(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("srv.Start 失败: %v", err)
	}

	url := srv.URL()
	if !strings.HasPrefix(url, "http://127.0.0.1:") || !strings.HasSuffix(url, "/mcp") {
		t.Fatalf("srv.URL 格式不符合预期: %s", url)
	}

	// 测试 SetAutomation
	srv.SetAutomation(nil)
	if srv.getAutomation() != nil {
		t.Fatalf("期望 automation 为 nil")
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("srv.Close 失败: %v", err)
	}
}

// toolCallResult 解析 tools/call 的响应：返回文本载荷（JSON）与 isError 标志。
func toolCallResult(t *testing.T, res map[string]any) (map[string]any, bool) {
	t.Helper()
	text, isError := toolCallText(t, res)
	var payload map[string]any
	_ = json.Unmarshal([]byte(text), &payload)
	return payload, isError
}

// toolCallText 返回工具结果的原始文本（context_list_documents 直接输出文本）。
func toolCallText(t *testing.T, res map[string]any) (string, bool) {
	t.Helper()
	callRes, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("响应缺少 result: %v", res)
	}
	isError, _ := callRes["isError"].(bool)
	content, _ := callRes["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("响应缺少 content: %v", callRes)
	}
	item, _ := content[0].(map[string]any)
	text, _ := item["text"].(string)
	return text, isError
}

func TestKanbanCreateTaskDependencies(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()
	ctx := context.Background()
	projID := "proj-test-1"

	// 未完成的前置任务
	dep := store.Task{
		ID:        uuid.NewString(),
		ProjectID: &projID,
		Title:     "前置任务",
		Status:    string(kanban.TaskStatusTodo),
		DependsOn: []string{},
	}
	if err := s.CreateTask(ctx, dep); err != nil {
		t.Fatalf("创建前置任务失败: %v", err)
	}

	// 1. 依赖未完成：建卡成功但不启动，返回 partial_failure
	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_task",
			"arguments": map[string]any{
				"title":               "依赖任务",
				"request_id":          "req-dep-1",
				"depends_on_task_ids": []string{dep.ID},
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if !isError || payload["code"] != CodePartialFailure {
		t.Fatalf("依赖未完成时期望 partial_failure, 实际为 %v", payload)
	}

	tasks, err := s.ListTasks(ctx, projID)
	if err != nil {
		t.Fatalf("查询任务失败: %v", err)
	}
	var created *store.Task
	for i := range tasks {
		if tasks[i].Title == "依赖任务" {
			created = &tasks[i]
		}
	}
	if created == nil {
		t.Fatalf("依赖任务未创建")
	}
	if created.Status != string(kanban.TaskStatusTodo) {
		t.Fatalf("依赖未完成时不应启动任务, 实际状态为 %s", created.Status)
	}
	if len(created.DependsOn) != 1 || created.DependsOn[0] != dep.ID {
		t.Fatalf("依赖未持久化: %+v", created.DependsOn)
	}

	// 2. 同一 request_id 重试：重放 partial_failure，且不会重复建卡
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_create_task",
			"arguments": map[string]any{
				"title":               "依赖任务",
				"request_id":          "req-dep-1",
				"depends_on_task_ids": []string{dep.ID},
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("重试状态码期望 200, 实际为 %d", status)
	}
	payload, isError = toolCallResult(t, res)
	if !isError || payload["code"] != CodePartialFailure {
		t.Fatalf("重试应重放 partial_failure, 实际为 %v", payload)
	}
	tasks, _ = s.ListTasks(ctx, projID)
	depTaskCount := 0
	for _, task := range tasks {
		if task.Title == "依赖任务" {
			depTaskCount++
		}
	}
	if depTaskCount != 1 {
		t.Fatalf("重试不应重复建卡, 实际数量为 %d", depTaskCount)
	}

	// 3. 依赖未完成时启动被拒绝
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_start_task",
			"arguments": map[string]any{
				"task_id": created.ID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("启动状态码期望 200, 实际为 %d", status)
	}
	payload, isError = toolCallResult(t, res)
	if !isError || payload["code"] != CodeInvalidArguments {
		t.Fatalf("依赖未完成时启动应被拒绝, 实际为 %v", payload)
	}

	// 4. 前置任务完成后可以启动
	// 代理只能结算已进入 in_progress 的卡片（见 kanban_move_card 的 done 守卫），
	// 这里先把前置任务置为 in_progress 再结算为 done。
	dep, err = s.GetTask(ctx, dep.ID)
	if err != nil {
		t.Fatalf("读取前置任务失败: %v", err)
	}
	dep.Status = string(kanban.TaskStatusInProgress)
	dep, err = s.UpdateTask(ctx, dep, dep.Revision)
	if err != nil {
		t.Fatalf("更新前置任务状态失败: %v", err)
	}
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_move_card",
			"arguments": map[string]any{
				"task_id": dep.ID,
				"target":  "done",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("移动状态码期望 200, 实际为 %d", status)
	}
	if payload, isError = toolCallResult(t, res); isError {
		t.Fatalf("前置任务移动失败: %v", payload)
	}

	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_start_task",
			"arguments": map[string]any{
				"task_id": created.ID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("启动状态码期望 200, 实际为 %d", status)
	}
	if payload, isError = toolCallResult(t, res); isError {
		t.Fatalf("依赖完成后启动失败: %v", payload)
	}
	started, err := s.GetTask(ctx, created.ID)
	if err != nil {
		t.Fatalf("读取任务失败: %v", err)
	}
	if started.Status != string(kanban.TaskStatusInProgress) {
		t.Fatalf("任务应进入 in_progress, 实际为 %s", started.Status)
	}
}

func TestProjectIDMismatch(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	// 不匹配的 project_id 被拒绝
	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_read_board",
			"arguments": map[string]any{
				"project_id": "proj-other",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if !isError || payload["code"] != CodeInvalidArguments {
		t.Fatalf("不匹配的 project_id 应返回 invalid_arguments, 实际为 %v", payload)
	}

	// 匹配的 project_id 正常
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_read_board",
			"arguments": map[string]any{
				"project_id": "proj-test-1",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	if payload, isError = toolCallResult(t, res); isError {
		t.Fatalf("匹配的 project_id 应正常返回: %v", payload)
	}
}

func TestWorktreeProjectResolution(t *testing.T) {
	worktreeDir := t.TempDir()
	srv, s, _ := setupTestMCPWithOptions(t, func(o *Options) { o.WorktreeDir = worktreeDir })
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()
	ctx := context.Background()

	// 第二个项目：多项目下无法靠“唯一项目”回退
	proj2 := store.Project{
		ID:   "proj-test-2",
		Name: "second-proj",
		Path: t.TempDir(),
	}
	if err := s.UpsertProject(ctx, proj2); err != nil {
		t.Fatalf("插入第二个项目失败: %v", err)
	}

	proj2ID := proj2.ID
	task := store.Task{
		ID:        uuid.NewString(),
		ProjectID: &proj2ID,
		Title:     "worktree task",
		Status:    string(kanban.TaskStatusTodo),
		DependsOn: []string{},
	}
	if err := s.CreateTask(ctx, task); err != nil {
		t.Fatalf("创建任务失败: %v", err)
	}

	// 任务会话运行在 <WorktreeDir>/<taskID>，应能解析回所属项目
	worktreePath := filepath.Join(worktreeDir, task.ID)
	status, res := postRPC(t, mcpURL, token, worktreePath, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "kanban_read_card",
			"arguments": map[string]any{
				"task_id": task.ID,
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if isError {
		t.Fatalf("worktree 会话应能解析到所属项目: %v", payload)
	}
	card, _ := payload["card"].(map[string]any)
	if card["id"] != task.ID {
		t.Fatalf("期望返回任务 %s, 实际为 %v", task.ID, card["id"])
	}

	// 未知 worktree 目录在多项目下仍无法解析项目
	status, res = postRPC(t, mcpURL, token, filepath.Join(worktreeDir, uuid.NewString()), map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "kanban_read_board",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	if payload, isError = toolCallResult(t, res); !isError {
		t.Fatalf("未知目录应无法解析项目: %v", payload)
	}
}

func TestAutomationReportResultTool(t *testing.T) {
	srv, s, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()
	ctx := context.Background()

	// 通过 MCP 创建自动化
	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_create",
			"arguments": map[string]any{
				"name":           "Report Result Test",
				"prompt":         "check things",
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
	if automationID == "" {
		t.Fatalf("响应缺少 automation_id: %v", payload)
	}

	// 触发一次运行
	autoSvc := srv.getAutomation()
	run, err := autoSvc.RunNow(ctx, automationID)
	if err != nil {
		t.Fatalf("RunNow 失败: %v", err)
	}

	// 运行记录绑定执行线程：报告必须带同一线程作用域，否则会被判为越权。
	// 测试驱动的会话不落库，这里补登线程以便按会话解析项目。
	if run.ThreadID == nil || *run.ThreadID == "" {
		t.Fatalf("运行记录应绑定执行线程")
	}
	runThreadID := *run.ThreadID
	registerThread(t, s, runThreadID, "proj-test-1")

	// 报告失败：结算运行并更新失败计数
	status, res = postThreadRPC(t, srv, runThreadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_report_result",
			"arguments": map[string]any{
				"run_id":        run.ID,
				"automation_id": automationID,
				"status":        "failed",
				"summary":       "boom",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError = toolCallResult(t, res)
	if isError {
		t.Fatalf("报告运行结果失败: %v", payload)
	}
	if payload["reported_status"] != "failed" {
		t.Fatalf("期望 reported_status=failed, 实际为 %v", payload["reported_status"])
	}

	storedRun, err := s.GetAutomationRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if storedRun.Status != automation.RunFailed {
		t.Fatalf("运行状态期望 %s, 实际为 %s", automation.RunFailed, storedRun.Status)
	}
	if storedRun.Error == nil || *storedRun.Error != "boom" {
		t.Fatalf("期望运行错误信息为 boom, 实际为 %v", storedRun.Error)
	}

	def, err := autoSvc.Get(ctx, automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if def.Spec.State.ConsecutiveFailures != 1 {
		t.Fatalf("期望连续失败计数为 1, 实际为 %d", def.Spec.State.ConsecutiveFailures)
	}
	if def.Spec.State.LastRunStatus != automation.RunFailed {
		t.Fatalf("期望最近运行状态为 failed, 实际为 %s", def.Spec.State.LastRunStatus)
	}

	// 重复结算被拒绝
	status, res = postThreadRPC(t, srv, runThreadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_report_result",
			"arguments": map[string]any{
				"run_id":        run.ID,
				"automation_id": automationID,
				"status":        "succeeded",
				"summary":       "again",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError = toolCallResult(t, res)
	if !isError || payload["code"] != CodeAlreadySettled {
		t.Fatalf("重复结算应返回 already_settled, 实际为 %v", payload)
	}

	// 满足停止条件：自动化完成并记录原因
	run2, err := autoSvc.RunNow(ctx, automationID)
	if err != nil {
		t.Fatalf("第二次 RunNow 失败: %v", err)
	}
	if run2.ThreadID == nil || *run2.ThreadID == "" {
		t.Fatalf("第二次运行记录应绑定执行线程")
	}
	run2ThreadID := *run2.ThreadID
	registerThread(t, s, run2ThreadID, "proj-test-1")
	status, res = postThreadRPC(t, srv, run2ThreadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      4,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_report_result",
			"arguments": map[string]any{
				"run_id":                run2.ID,
				"automation_id":         automationID,
				"status":                "succeeded",
				"summary":               "all checks passed",
				"stop_condition_met":    true,
				"stop_condition_reason": "no failing checks",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	if payload, isError = toolCallResult(t, res); isError {
		t.Fatalf("报告停止条件失败: %v", payload)
	}
	completed, err := autoSvc.Get(ctx, automationID)
	if err != nil {
		t.Fatalf("读取自动化失败: %v", err)
	}
	if completed.Enabled {
		t.Fatalf("停止条件满足后应停用自动化")
	}
	if completed.Status != automation.StatusCompleted {
		t.Fatalf("期望状态 %s, 实际为 %s", automation.StatusCompleted, completed.Status)
	}
	if !strings.Contains(completed.Spec.State.StopReason, "no failing checks") {
		t.Fatalf("期望停止原因包含条件说明, 实际为 %s", completed.Spec.State.StopReason)
	}

	// 其他线程结算同一个运行时返回 forbidden，且不得改动运行状态
	run3, err := autoSvc.RunNow(ctx, automationID)
	if err != nil {
		t.Fatalf("第三次 RunNow 失败: %v", err)
	}
	if run3.ThreadID == nil || *run3.ThreadID == "" {
		t.Fatalf("第三次运行记录应绑定执行线程")
	}
	registerThread(t, s, *run3.ThreadID, "proj-test-1")
	otherThreadID := uuid.NewString()
	registerThread(t, s, otherThreadID, "proj-test-1")

	status, res = postThreadRPC(t, srv, otherThreadID, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "automation_report_result",
			"arguments": map[string]any{
				"run_id":        run3.ID,
				"automation_id": automationID,
				"status":        "succeeded",
				"summary":       "hijack",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	if payload, isError = toolCallResult(t, res); !isError || payload["code"] != CodeForbidden {
		t.Fatalf("其他线程结算应返回 forbidden, 实际为 %v", payload)
	}
	storedRun3, err := s.GetAutomationRun(ctx, run3.ID)
	if err != nil {
		t.Fatalf("读取运行记录失败: %v", err)
	}
	if storedRun3.Status != automation.RunRunning {
		t.Fatalf("越权报告后运行状态应保持 %s, 实际为 %s", automation.RunRunning, storedRun3.Status)
	}
}

func TestContextDocuments(t *testing.T) {
	srv, _, projDir := setupTestMCP(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	mcpURL := ts.URL + "/mcp"
	token := srv.Token()

	docsDir := filepath.Join(projDir, ".mcode", "context", "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("创建 docs 目录失败: %v", err)
	}
	body := strings.Repeat("A", maxDocumentChars+5000)
	if err := os.WriteFile(filepath.Join(docsDir, "custom-notes.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("写入文档失败: %v", err)
	}
	instructionsPath := filepath.Join(projDir, ".mcode", "context", "instructions.md")
	if err := os.WriteFile(instructionsPath, []byte("# Project rules"), 0o644); err != nil {
		t.Fatalf("写入标准文档失败: %v", err)
	}

	// 1. 列表为文本：四篇标准文档始终列出，另加 docs/ 下的自定义文档，路径基于 .mcode/context/
	status, res := postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "context_list_documents",
			"arguments": map[string]any{},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	listText, isError := toolCallText(t, res)
	if isError {
		t.Fatalf("列出文档失败: %s", listText)
	}
	standardIdx := strings.Index(listText, "Standard documents:")
	customIdx := strings.Index(listText, "Custom documents:")
	if standardIdx < 0 || customIdx < 0 || customIdx < standardIdx {
		t.Fatalf("列表应按 Standard documents / Custom documents 两段输出, 实际为:\n%s", listText)
	}
	standardSection := listText[standardIdx:customIdx]
	for _, name := range []string{"instructions", "overview", "decisions", "notes"} {
		if !strings.Contains(standardSection, "- name: "+name+" ") {
			t.Fatalf("标准文档段缺少 %s:\n%s", name, listText)
		}
		if !strings.Contains(standardSection, "path: .mcode/context/"+name+".md") {
			t.Fatalf("标准文档 %s 的路径应为 .mcode/context/%s.md:\n%s", name, name, listText)
		}
	}
	customSection := listText[customIdx:]
	if !strings.Contains(customSection, "- name: docs/custom-notes.md") {
		t.Fatalf("自定义文档段缺少 docs/custom-notes.md:\n%s", listText)
	}
	if !strings.Contains(customSection, "path: .mcode/context/docs/custom-notes.md") {
		t.Fatalf("自定义文档路径应为 .mcode/context/docs/<name>:\n%s", listText)
	}

	// 2. 标准名直接映射到对应文件，读取返回 Markdown 正文
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "context_read_document",
			"arguments": map[string]any{
				"name": "instructions",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError := toolCallResult(t, res)
	if isError {
		t.Fatalf("读取标准文档失败: %v", payload)
	}
	if payload["name"] != "instructions.md" || payload["content"] != "# Project rules" {
		t.Fatalf("标准名应映射到 instructions.md, 实际为 %v", payload)
	}

	// 3. 读取超长文档被截断；docs/ 前缀会被去掉，没有扩展名的自定义文档补 .md
	for id, name := range map[int]string{3: "docs/custom-notes", 4: "custom-notes.md"} {
		status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "tools/call",
			"params": map[string]any{
				"name": "context_read_document",
				"arguments": map[string]any{
					"name": name,
				},
			},
		})
		if status != http.StatusOK {
			t.Fatalf("状态码期望 200, 实际为 %d", status)
		}
		payload, isError = toolCallResult(t, res)
		if isError {
			t.Fatalf("读取文档 %s 失败: %v", name, payload)
		}
		content, _ := payload["content"].(string)
		if !strings.HasSuffix(content, "(truncated)") {
			t.Fatalf("超长文档应被截断, 实际长度 %d", len(content))
		}
	}

	// 4. 不存在的文档：not_found 并列出可用文档
	status, res = postRPC(t, mcpURL, token, projDir, map[string]any{
		"jsonrpc": "2.0",
		"id":      5,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "context_read_document",
			"arguments": map[string]any{
				"name": "missing",
			},
		},
	})
	if status != http.StatusOK {
		t.Fatalf("状态码期望 200, 实际为 %d", status)
	}
	payload, isError = toolCallResult(t, res)
	if !isError || payload["code"] != CodeNotFound {
		t.Fatalf("不存在的文档应返回 not_found, 实际为 %v", payload)
	}
	message, _ := payload["message"].(string)
	if !strings.Contains(message, "docs/custom-notes.md") {
		t.Fatalf("错误信息应列出可用文档, 实际为 %s", message)
	}
}
