package copilot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"mcode/internal/harness"
)

const (
	fakeCopilotEnv     = "MCODE_TEST_FAKE_COPILOT"
	fakeCopilotLogEnv  = "MCODE_TEST_FAKE_COPILOT_LOG"
	fakeCopilotHangEnv = "MCODE_TEST_FAKE_COPILOT_HANG"
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeCopilotHangEnv) == "1" {
		select {}
	}
	if os.Getenv(fakeCopilotEnv) == "1" {
		runFakeCopilot()
		return
	}
	os.Exit(m.Run())
}

// readFixtureLines 读取 testdata 下的 JSONL 录制 fixture（每行一条服务端消息）。
func readFixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	return readJSONLines(t, filepath.Join("testdata", name))
}

func readJSONLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	var out [][]byte
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, []byte(line))
	}
	return out
}

// decodeRPCMessage 把一条 JSONL 行解成 method/params。
func decodeRPCMessage(t *testing.T, line []byte) (string, json.RawMessage) {
	t.Helper()
	var msg struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(line, &msg); err != nil {
		t.Fatalf("解析 fixture 行失败: %v", err)
	}
	return msg.Method, msg.Params
}

// ------------- 测试替身 -------------

type mockApprover struct {
	mu       sync.Mutex
	decision harness.Decision
	requests []harness.ApprovalRequest
}

func (m *mockApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	m.mu.Unlock()
	return m.decision, nil
}

func (m *mockApprover) recorded() []harness.ApprovalRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]harness.ApprovalRequest(nil), m.requests...)
}

type mockInteractor struct {
	decision harness.Decision
	response harness.ApprovalResponse
}

func (m *mockInteractor) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	return m.decision, nil
}

func (m *mockInteractor) Interact(ctx context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	return m.response, nil
}

type mockSandbox struct {
	cmdFunc func(ctx context.Context) *exec.Cmd
}

func (m *mockSandbox) Command(ctx context.Context, workDir string, mode harness.PermissionMode, name string, args ...string) (*exec.Cmd, func(), error) {
	return m.cmdFunc(ctx), nil, nil
}

// ------------- 事件映射 -------------

// TestEventHandlerMapsSessionEvents 用录制的正常轮次 fixture 验证事件映射。
func TestEventHandlerMapsSessionEvents(t *testing.T) {
	turn := harness.Turn{ID: "turn-1", SessionID: "sid-1", WorkDir: t.TempDir(), Mode: harness.ModeFullAccess}
	proc := &sessionProc{workDir: turn.WorkDir, deadCh: make(chan struct{}), permissionMode: "manual", interactionMode: "interactive"}
	proc.setSessionID("sid-1")

	var events []harness.Event
	h := newEventHandler(proc, turn, func(e harness.Event) { events = append(events, e) }, context.Background())
	h.beginTurn()

	for _, line := range readFixtureLines(t, "turn.jsonl") {
		method, params := decodeRPCMessage(t, line)
		if method != "session.event" {
			t.Fatalf("fixture 方法 = %q，期望 session.event", method)
		}
		h.handleEvent(params)
	}

	var text string
	var toolStarted, toolEnded bool
	var usage *harness.Usage
	var stopReason string
	for _, e := range events {
		switch e.Type {
		case harness.EventText:
			text += e.Text
		case harness.EventToolStart:
			if e.ToolID == "c1" && e.ToolName == "bash" {
				toolStarted = true
			}
		case harness.EventToolEnd:
			if e.ToolID == "c1" && e.Output == "ok" && !e.IsError {
				toolEnded = true
			}
		case harness.EventUsage:
			usage = e.Usage
		case harness.EventDone:
			stopReason = e.StopReason
		}
	}

	if text != "Hello world" {
		t.Fatalf("文本增量 = %q，期望 %q", text, "Hello world")
	}
	if !toolStarted {
		t.Fatal("缺少 bash 工具开始事件")
	}
	if !toolEnded {
		t.Fatal("缺少 bash 工具结束事件")
	}
	if usage == nil || usage.InputTokens != 10 || usage.OutputTokens != 5 || usage.CachedTokens != 2 {
		t.Fatalf("用量 = %+v", usage)
	}
	if stopReason != "completed" {
		t.Fatalf("结束原因 = %q", stopReason)
	}
	if !h.turnFinishedOK() {
		t.Fatal("轮次应以 session.idle 正常收敛")
	}
}

// TestEventHandlerPermissionRoundTrip 验证权限请求交审批并回写
// session.permissions.handlePendingPermissionRequest。
func TestEventHandlerPermissionRoundTrip(t *testing.T) {
	proc, recorded := newRecordingProc(t)
	approver := &mockApprover{decision: harness.DecisionAllow}
	turn := harness.Turn{ID: "turn-1", SessionID: "sid-1", Mode: harness.ModeSupervised, Approver: approver}

	h := newEventHandler(proc, turn, nil, context.Background())
	h.beginTurn()

	for _, line := range readFixtureLines(t, "permission.jsonl") {
		_, params := decodeRPCMessage(t, line)
		h.handleEvent(params)
	}

	select {
	case call := <-recorded:
		if call.Method != "session.permissions.handlePendingPermissionRequest" {
			t.Fatalf("回写方法 = %q", call.Method)
		}
		if call.Params["requestId"] != "perm-1" {
			t.Fatalf("requestId = %v", call.Params["requestId"])
		}
		result, _ := call.Params["result"].(map[string]any)
		if result["kind"] != "approve-once" {
			t.Fatalf("审批决定 = %v", call.Params["result"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("未发出权限答复")
	}

	requests := approver.recorded()
	if len(requests) != 1 {
		t.Fatalf("审批次数 = %d，期望 1", len(requests))
	}
	if requests[0].Kind != harness.RequestKindCommand {
		t.Fatalf("审批类型 = %v，期望 command", requests[0].Kind)
	}
}

// TestUserInputRequestAnswered 验证 userInput.request 用 Interactor 原地答复。
func TestUserInputRequestAnswered(t *testing.T) {
	proc, _ := newRecordingProc(t)
	interactor := &mockInteractor{response: harness.ApprovalResponse{
		Answers: map[string][]string{"answer": {"b.txt"}},
	}}
	turn := harness.Turn{ID: "turn-1", SessionID: "sid-1", Mode: harness.ModeSupervised, Approver: interactor}

	h := newEventHandler(proc, turn, nil, context.Background())
	h.beginTurn()

	lines := readFixtureLines(t, "user_input.jsonl")
	if len(lines) != 1 {
		t.Fatalf("user_input fixture 行数 = %d", len(lines))
	}
	method, params := decodeRPCMessage(t, lines[0])
	if method != "userInput.request" {
		t.Fatalf("方法 = %q", method)
	}

	res, err := h.handleServerRequest(method, params)
	if err != nil {
		t.Fatalf("handleServerRequest 失败: %v", err)
	}
	answer, _ := res.(map[string]any)
	if answer["answer"] != "b.txt" {
		t.Fatalf("答案 = %v", answer["answer"])
	}
	if answer["wasFreeform"] != false {
		t.Fatalf("wasFreeform = %v，期望 false", answer["wasFreeform"])
	}
}

// TestExitPlanModeRestoresRunMode 验证计划获批后先把权限模式切回用户所选档位，再放行。
func TestExitPlanModeRestoresRunMode(t *testing.T) {
	proc, recorded := newRecordingProc(t)
	proc.permissionMode = "manual"
	interactor := &mockInteractor{response: harness.ApprovalResponse{Decision: harness.DecisionAllow}}
	turn := harness.Turn{ID: "turn-1", SessionID: "sid-1", Mode: harness.ModeReadOnly, InteractionMode: "plan", PlanExitMode: harness.ModeFullAccess, Approver: interactor}

	h := newEventHandler(proc, turn, nil, context.Background())
	res, err := h.handleServerRequest("exitPlanMode.request", json.RawMessage(`{"planContent":"# 计划"}`))
	if err != nil {
		t.Fatalf("handleServerRequest 失败: %v", err)
	}
	if answer, _ := res.(map[string]any); answer["approved"] != true {
		t.Fatalf("应放行计划: %v", res)
	}
	select {
	case call := <-recorded:
		if call.Method != "session.permissions.setMode" || call.Params["mode"] != "allow-all" {
			t.Fatalf("应切回 allow-all，实际 %+v", call)
		}
	default:
		t.Fatal("计划获批后没有切换权限模式")
	}
}

// TestRollbackSendsRewindCalls 用假进程验证原生回滚走 listRewindPoints + rewind。
func TestRollbackSendsRewindCalls(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	t.Setenv(fakeCopilotEnv, "1")
	t.Setenv(fakeCopilotLogEnv, logPath)
	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0])
	}}

	h := New(Options{})
	defer func() { _ = h.Close() }()

	var sessionID string
	turn := harness.Turn{ID: "turn-1", WorkDir: t.TempDir(), Mode: harness.ModeFullAccess, Prompt: "hi", Sandbox: sb}
	if err := h.Run(context.Background(), turn, func(e harness.Event) {
		if e.Type == harness.EventSession {
			sessionID = e.SessionID
		}
	}); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if sessionID == "" {
		t.Fatal("未取得会话 id")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Rollback(ctx, sessionID, 2); err != nil {
		t.Fatalf("Rollback 失败: %v", err)
	}

	calls := map[string]map[string]any{}
	for _, line := range readJSONLines(t, logPath) {
		var msg struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &msg); err != nil {
			t.Fatalf("解析请求日志失败: %v", err)
		}
		if msg.Method != "" {
			calls[msg.Method] = msg.Params
		}
	}

	list, ok := calls["session.history.listRewindPoints"]
	if !ok {
		t.Fatal("未调用 session.history.listRewindPoints")
	}
	if list["sessionId"] != sessionID {
		t.Fatalf("listRewindPoints sessionId = %v，期望 %s", list["sessionId"], sessionID)
	}

	rewind, ok := calls["session.history.rewind"]
	if !ok {
		t.Fatal("未调用 session.history.rewind")
	}
	if rewind["sessionId"] != sessionID {
		t.Fatalf("rewind sessionId = %v", rewind["sessionId"])
	}
	if rewind["eventId"] != "e2" {
		t.Fatalf("rewind eventId = %v，期望 e2", rewind["eventId"])
	}
	if rewind["mode"] != "conversation" {
		t.Fatalf("rewind mode = %v，期望 conversation", rewind["mode"])
	}
}

// TestRollbackNotRunning 验证会话不在运行时返回 harness.ErrNotRunning。
func TestRollbackNotRunning(t *testing.T) {
	h := New(Options{})
	if err := h.Rollback(context.Background(), "missing", 1); err != harness.ErrNotRunning {
		t.Fatalf("err = %v，期望 ErrNotRunning", err)
	}
	if err := h.Compact(context.Background(), "missing"); err != harness.ErrNotRunning {
		t.Fatalf("err = %v，期望 ErrNotRunning", err)
	}
}

// ------------- 辅助 -------------

type recordedCall struct {
	Method string
	Params map[string]any
}

// newRecordingProc 构造一个带常驻连接（io.Pipe 驱动）的会话进程，记录发出的请求。
func newRecordingProc(t *testing.T) (*sessionProc, <-chan recordedCall) {
	t.Helper()
	reqR, reqW := io.Pipe()
	resR, resW := io.Pipe()
	conn := NewConn(resR, reqW)

	recorded := make(chan recordedCall, 8)
	go func() {
		br := bufio.NewReader(reqR)
		for {
			body, err := readFrame(br)
			if err != nil {
				return
			}
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params map[string]any  `json:"params"`
			}
			if err := json.Unmarshal(body, &msg); err != nil || msg.Method == "" {
				continue
			}
			select {
			case recorded <- recordedCall{Method: msg.Method, Params: msg.Params}:
			default:
			}
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.ID,
				"result":  map[string]any{},
			})
			_ = writeTestFrame(resW, resp)
		}
	}()

	t.Cleanup(func() {
		_ = conn.Close()
		_ = reqW.Close()
		_ = resW.Close()
	})

	proc := &sessionProc{conn: conn, workDir: t.TempDir(), deadCh: make(chan struct{})}
	proc.setSessionID("sid-1")
	return proc, recorded
}

// runFakeCopilot 是本包测试用的假 copilot headless server：按 Content-Length 分帧收发，
// 需要记录客户端请求时把 fakeCopilotLogEnv 指向一个文件。
func runFakeCopilot() {
	logPath := os.Getenv(fakeCopilotLogEnv)
	reader := bufio.NewReader(os.Stdin)
	write := func(message map[string]any) {
		data, _ := json.Marshal(message)
		_, _ = fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(data), data)
	}
	for {
		length := -1
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				_, _ = fmt.Sscanf(strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:")), "%d", &length)
			}
		}
		if length < 0 {
			return
		}
		body := make([]byte, length)
		if _, err := io.ReadFull(reader, body); err != nil {
			return
		}
		var msg map[string]any
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		if logPath != "" {
			if file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				data, _ := json.Marshal(msg)
				_, _ = file.Write(append(data, '\n'))
				_ = file.Close()
			}
		}
		id := msg["id"]
		method, _ := msg["method"].(string)
		switch method {
		case "ping":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"protocolVersion": 3}})
		case "session.create", "session.resume":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
		case "session.history.listRewindPoints":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"points": []map[string]string{{"eventId": "e1"}, {"eventId": "e2"}, {"eventId": "e3"}}}})
		case "session.history.rewind":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"outcome": "success"}})
		case "session.send":
			write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
			params, _ := msg["params"].(map[string]any)
			sid, _ := params["sessionId"].(string)
			write(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"sessionId": sid, "event": map[string]any{"type": "assistant.message_delta", "data": map[string]any{"messageId": "m1", "deltaContent": "PONG"}}}})
			write(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"sessionId": sid, "event": map[string]any{"type": "session.idle", "data": map[string]any{"aborted": false}}}})
		default:
			if id != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
			}
		}
	}
}

// TestKillTerminatesProcessAfterMarkDead 验证连接先断开（markDead 已触发）后 kill 仍会杀进程，不会卡在 Wait。
func TestKillTerminatesProcessAfterMarkDead(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if runtime.GOOS == "windows" {
		cmd = exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), fakeCopilotHangEnv+"=1")
	}
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	proc := &sessionProc{cmd: cmd, stdin: stdin, deadCh: make(chan struct{})}
	proc.markDead()

	done := make(chan struct{})
	go func() {
		proc.kill()
		proc.kill() // 重复调用必须安全
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		t.Fatal("kill 在 markDead 之后阻塞")
	}
}

// TestUsageInfoDoesNotOverrideTurnUsage 验证 session.usage_info（上下文占用）不会覆盖 assistant.usage 的累计用量。
func TestUsageInfoDoesNotOverrideTurnUsage(t *testing.T) {
	turn := harness.Turn{ID: "turn-1", SessionID: "sid-1", Mode: harness.ModeFullAccess}
	proc := &sessionProc{deadCh: make(chan struct{})}
	proc.setSessionID("sid-1")
	var usages []*harness.Usage
	h := newEventHandler(proc, turn, func(e harness.Event) {
		if e.Type == harness.EventUsage {
			usages = append(usages, e.Usage)
		}
	}, context.Background())
	h.beginTurn()

	h.handleEvent(json.RawMessage(`{"sessionId":"sid-1","event":{"type":"assistant.usage","data":{"inputTokens":10,"outputTokens":5}}}`))
	h.handleEvent(json.RawMessage(`{"sessionId":"sid-1","event":{"type":"session.usage_info","data":{"currentTokens":4000,"tokenLimit":128000}}}`))

	if len(usages) != 1 || usages[0].InputTokens != 10 || usages[0].OutputTokens != 5 {
		t.Fatalf("usage 事件 = %+v，期望仅 assistant.usage 一条", usages)
	}
}

// TestRequestSpeedSamples 验证每次模型请求从首个流式增量帧计时到 assistant.usage：
// 没有增量帧的请求不产生样本（只有完整 assistant.message / assistant.reasoning 也不算），
// 每条 usage 都带本轮至今的完整样本列表。
func TestRequestSpeedSamples(t *testing.T) {
	turn := harness.Turn{ID: "turn-1", SessionID: "sid-1", Mode: harness.ModeFullAccess}
	proc := &sessionProc{deadCh: make(chan struct{})}
	proc.setSessionID("sid-1")
	var usages []*harness.Usage
	h := newEventHandler(proc, turn, func(e harness.Event) {
		if e.Type == harness.EventUsage {
			usages = append(usages, e.Usage)
		}
	}, context.Background())
	// 假时钟每次调用前进 200 毫秒：不必真的等待，只要开始与结算之间取到足够时长。
	var ticks int
	h.now = func() time.Time {
		ticks++
		return time.Unix(0, 0).Add(time.Duration(ticks) * 200 * time.Millisecond)
	}
	h.beginTurn()

	feeds := []string{
		`{"sessionId":"sid-1","event":{"type":"assistant.message_delta","data":{"messageId":"m1","deltaContent":"hi"}}}`,
		`{"sessionId":"sid-1","event":{"type":"assistant.usage","data":{"inputTokens":10,"outputTokens":20}}}`,
		`{"sessionId":"sid-1","event":{"type":"assistant.reasoning_delta","data":{"reasoningId":"r1","deltaContent":"hmm"}}}`,
		`{"sessionId":"sid-1","event":{"type":"assistant.usage","data":{"inputTokens":10,"outputTokens":30}}}`,
		// 没有内容帧的请求：只有 usage，不产生样本。
		`{"sessionId":"sid-1","event":{"type":"assistant.usage","data":{"inputTokens":10,"outputTokens":40}}}`,
		// 只有完整的 assistant.message 与 assistant.reasoning（没有增量帧）：不产生样本。
		`{"sessionId":"sid-1","event":{"type":"assistant.message","data":{"messageId":"m2","content":"done"}}}`,
		`{"sessionId":"sid-1","event":{"type":"assistant.reasoning","data":{"reasoningId":"r2","content":"full"}}}`,
		`{"sessionId":"sid-1","event":{"type":"assistant.usage","data":{"inputTokens":10,"outputTokens":50}}}`,
	}
	for _, raw := range feeds {
		h.handleEvent(json.RawMessage(raw))
	}

	if len(usages) != 4 {
		t.Fatalf("want 4 usage events, got %d", len(usages))
	}
	first := usages[0].Requests
	if len(first) != 1 || first[0].OutputTokens != 20 || first[0].DurationMs < 100 {
		t.Fatalf("第一次请求的速度样本 = %+v, want 1 条 20 tokens 且时长 >= 100ms", first)
	}
	second := usages[1].Requests
	if len(second) != 2 || second[0].OutputTokens != 20 || second[1].OutputTokens != 30 || second[1].DurationMs < 100 {
		t.Fatalf("第二次请求的速度样本 = %+v, want [20 30]", second)
	}
	if len(usages[2].Requests) != 2 {
		t.Fatalf("无内容帧的请求不应产生样本，得到 %+v", usages[2].Requests)
	}
	if len(usages[3].Requests) != 2 {
		t.Fatalf("只有完整消息的请求不应产生样本，得到 %+v", usages[3].Requests)
	}
}

// TestCloseAndKillReclaimOnce 验证 close 与 kill 并发时只 Wait 一次、沙箱回收只执行一次。
func TestCloseAndKillReclaimOnce(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if runtime.GOOS == "windows" {
		cmd = exec.Command(os.Args[0])
		cmd.Env = append(os.Environ(), fakeCopilotHangEnv+"=1")
	}
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	cleanupCalls := 0
	proc := &sessionProc{
		cmd:     cmd,
		stdin:   stdin,
		deadCh:  make(chan struct{}),
		cleanup: func() { cleanupCalls++ },
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		proc.kill()
	}()
	go func() {
		defer wg.Done()
		_ = proc.close()
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		t.Fatal("close/kill 并发时阻塞")
	}
	if cleanupCalls != 1 {
		t.Fatalf("cleanup 调用了 %d 次，期望 1 次", cleanupCalls)
	}
}
