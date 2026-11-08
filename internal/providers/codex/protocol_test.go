package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
)

type mockApprover struct {
	decision harness.Decision
	requests []harness.ApprovalRequest
}

func TestHarnessComposerPrefsWithCachedThread(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	pathJSON, _ := json.Marshal(logPath)
	script := strings.Replace(pongAppServerScript, `    method = msg.get("method")`,
		"    with open("+string(pathJSON)+", 'a') as log:\n        log.write(json.dumps(msg) + '\\n')\n"+
			`    method = msg.get("method")`, 1)
	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-u", "-c", script)
	}}
	h := New(Options{})
	defer h.Close()
	workDir := t.TempDir()
	modes := []harness.PermissionMode{harness.ModeFullAccess, harness.ModeReadOnly, harness.ModeFullAccess, harness.ModeFullAccess}
	for i, mode := range modes {
		turn := harness.Turn{WorkDir: workDir, Mode: mode, Prompt: "test", Effort: "high", Sandbox: sb}
		if i > 0 {
			turn.SessionID = "th_mock_123"
		}
		if err := h.Run(context.Background(), turn, nil); err != nil {
			t.Fatal(err)
		}
	}
	var sandboxes []string
	turns := 0
	for _, line := range readLines(t, logPath) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			t.Fatal(err)
		}
		switch req.Method {
		case "thread/start", "thread/resume":
			sandboxes = append(sandboxes, req.Params["sandbox"].(string))
		case "turn/start":
			turns++
			if req.Params["effort"] != "high" {
				t.Fatalf("未传递 effort: %+v", req.Params)
			}
		}
	}
	if !reflect.DeepEqual(sandboxes, []string{"danger-full-access", "read-only", "danger-full-access"}) || turns != len(modes) {
		t.Fatalf("切换模式必须更新缓存线程的权限，未切换时保留缓存: %v, turns=%d", sandboxes, turns)
	}
}

// 快速模式优先使用会话指定的速率档位，即使模型目录查不到档位也按 SpeedTier 下发。
func TestHarnessFastModeSpeedTierPrecedence(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	pathJSON, _ := json.Marshal(logPath)
	script := strings.Replace(pongAppServerScript, `    method = msg.get("method")`,
		"    with open("+string(pathJSON)+", 'a') as log:\n        log.write(json.dumps(msg) + '\\n')\n"+
			`    method = msg.get("method")`, 1)
	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-u", "-c", script)
	}}
	h := New(Options{})
	defer h.Close()

	turn := harness.Turn{
		WorkDir: t.TempDir(), Mode: harness.ModeFullAccess, Prompt: "test",
		FastMode: true, SpeedTier: "priority", Sandbox: sb,
	}
	if err := h.Run(context.Background(), turn, nil); err != nil {
		t.Fatal(err)
	}

	var got any
	for _, line := range readLines(t, logPath) {
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			t.Fatal(err)
		}
		if req.Method == "turn/start" {
			got = req.Params["serviceTierForTurn"]
		}
	}
	if got != "priority" {
		t.Fatalf("SpeedTier 应优先生效，实际 serviceTierForTurn=%v", got)
	}
}

func (m *mockApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	m.requests = append(m.requests, req)
	return m.decision, nil
}

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read testdata file %s: %v", path, err)
	}
	var lines [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) > 0 {
			lines = append(lines, append([]byte(nil), line...))
		}
	}
	return lines
}

func TestTextStreamAndDedup(t *testing.T) {
	lines := readLines(t, filepath.Join("testdata", "text_stream_and_dedup.jsonl"))
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})

	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	for _, line := range lines {
		reply, err := h.HandleLine(context.Background(), line, emit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply != nil {
			t.Fatalf("unexpected reply for notification: %+v", reply)
		}
	}

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}

	expectedTexts := []string{"Hello ", "world", ", extra!"}
	for i, want := range expectedTexts {
		if events[i].Type != harness.EventText {
			t.Errorf("event %d: expected type text, got %s", i, events[i].Type)
		}
		if events[i].Text != want {
			t.Errorf("event %d: expected text %q, got %q", i, want, events[i].Text)
		}
	}
}

func TestReasoningStreamAndCompleted(t *testing.T) {
	lines := readLines(t, filepath.Join("testdata", "reasoning.jsonl"))
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})

	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	for _, line := range lines {
		reply, err := h.HandleLine(context.Background(), line, emit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply != nil {
			t.Fatalf("unexpected reply: %+v", reply)
		}
	}

	// 预期：前两行流式推理（"Let me think", " carefully."），第 3 行已流式被去重吞掉，第 4 行未流式输出 summary ("Direct reasoning summary")
	if len(events) != 3 {
		t.Fatalf("expected 3 reasoning events, got %d: %+v", len(events), events)
	}

	expectedTexts := []string{"Let me think", " carefully.", "Direct reasoning summary"}
	for i, want := range expectedTexts {
		if events[i].Type != harness.EventReasoning {
			t.Errorf("event %d: expected reasoning, got %s", i, events[i].Type)
		}
		if events[i].Text != want {
			t.Errorf("event %d: expected %q, got %q", i, want, events[i].Text)
		}
	}
}

func TestCommandExecution(t *testing.T) {
	lines := readLines(t, filepath.Join("testdata", "command_execution.jsonl"))
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})

	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	for _, line := range lines {
		_, err := h.HandleLine(context.Background(), line, emit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}

	// 1. ToolStart
	if events[0].Type != harness.EventToolStart {
		t.Errorf("event 0: expected tool_start, got %s", events[0].Type)
	}
	if events[0].ToolID != "cmd_1" || events[0].ToolName != "go test ./..." {
		t.Errorf("event 0 mismatch: ToolID=%s, ToolName=%s", events[0].ToolID, events[0].ToolName)
	}

	// 2. ToolEnd
	if events[1].Type != harness.EventToolEnd {
		t.Errorf("event 1: expected tool_end, got %s", events[1].Type)
	}
	if events[1].ToolID != "cmd_1" || events[1].IsError != false {
		t.Errorf("event 1 mismatch: ToolID=%s, IsError=%v", events[1].ToolID, events[1].IsError)
	}
	if !bytes.Contains([]byte(events[1].Output), []byte("PASS")) {
		t.Errorf("expected output to contain PASS, got %q", events[1].Output)
	}
}

func TestFileChange(t *testing.T) {
	lines := readLines(t, filepath.Join("testdata", "file_change.jsonl"))
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})

	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	for _, line := range lines {
		_, err := h.HandleLine(context.Background(), line, emit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// 预期：
	// 0: EventToolStart (ToolID="fc_1", ToolName="FileChange")
	// 1: EventToolEnd (ToolID="fc_1")
	// 2: EventFileChange (Path="main.go", Patch="...")
	// 3: EventFileChange (Path="config.json", Patch="...")
	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}

	if events[0].Type != harness.EventToolStart || events[0].ToolID != "fc_1" {
		t.Errorf("event 0 mismatch: %+v", events[0])
	}
	if events[1].Type != harness.EventToolEnd || events[1].ToolID != "fc_1" {
		t.Errorf("event 1 mismatch: %+v", events[1])
	}
	if events[2].Type != harness.EventFileChange || events[2].Path != "main.go" || events[2].Patch == "" {
		t.Errorf("event 2 mismatch: %+v", events[2])
	}
	if events[3].Type != harness.EventFileChange || events[3].Path != "config.json" || events[3].Patch == "" {
		t.Errorf("event 3 mismatch: %+v", events[3])
	}
}

func TestApprovalAllowAndDeny(t *testing.T) {
	t.Run("Approver 同意", func(t *testing.T) {
		lines := readLines(t, filepath.Join("testdata", "approval_allow.jsonl"))
		approver := &mockApprover{decision: harness.DecisionAllow}
		h := NewProtocolHandler(harness.Turn{
			Mode:     harness.ModeAgent,
			Approver: approver,
		})

		for _, line := range lines {
			reply, err := h.HandleLine(context.Background(), line, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if reply == nil {
				t.Fatal("expected reply for approval request, got nil")
			}
			var res struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(reply.Result, &res); err != nil {
				t.Fatalf("failed to unmarshal reply result: %v", err)
			}
			if res.Decision != "accept" {
				t.Errorf("expected accept decision, got %q", res.Decision)
			}
		}

		if len(approver.requests) != 4 {
			t.Errorf("expected 4 approval requests, got %d", len(approver.requests))
		}
	})

	t.Run("Approver 拒绝", func(t *testing.T) {
		lines := readLines(t, filepath.Join("testdata", "approval_deny.jsonl"))
		approver := &mockApprover{decision: harness.DecisionDeny}
		h := NewProtocolHandler(harness.Turn{
			Mode:     harness.ModeAgent,
			Approver: approver,
		})

		for _, line := range lines {
			reply, err := h.HandleLine(context.Background(), line, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if reply == nil {
				t.Fatal("expected reply for approval request, got nil")
			}
			var res struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(reply.Result, &res); err != nil {
				t.Fatalf("failed to unmarshal reply result: %v", err)
			}
			if res.Decision != "decline" {
				t.Errorf("expected decline decision, got %q", res.Decision)
			}
		}
	})

	t.Run("Approver 为空时按权限模式自动裁决", func(t *testing.T) {
		reqLine := []byte(`{"jsonrpc":"2.0","id":301,"method":"item/commandExecution/requestApproval","params":{"itemId":"cmd_1","command":"ls"}}`)

		// ModeFullAccess 自动同意
		hFull := NewProtocolHandler(harness.Turn{Mode: harness.ModeFullAccess})
		repFull, err := hFull.HandleLine(context.Background(), reqLine, nil)
		if err != nil || repFull == nil {
			t.Fatalf("unexpected full access reply: %v", err)
		}
		var resFull struct {
			Decision string `json:"decision"`
		}
		_ = json.Unmarshal(repFull.Result, &resFull)
		if resFull.Decision != "accept" {
			t.Errorf("expected accept for fullAccess, got %q", resFull.Decision)
		}

		// ModeReadOnly 自动拒绝
		hRO := NewProtocolHandler(harness.Turn{Mode: harness.ModeReadOnly})
		repRO, err := hRO.HandleLine(context.Background(), reqLine, nil)
		if err != nil || repRO == nil {
			t.Fatalf("unexpected readonly reply: %v", err)
		}
		var resRO struct {
			Decision string `json:"decision"`
		}
		_ = json.Unmarshal(repRO.Result, &resRO)
		if resRO.Decision != "decline" {
			t.Errorf("expected decline for readonly, got %q", resRO.Decision)
		}
	})

	t.Run("未知请求回复 Method not found 避免悬挂", func(t *testing.T) {
		unknownLine := []byte(`{"jsonrpc":"2.0","id":999,"method":"unknown/request","params":{}}`)
		h := NewProtocolHandler(harness.Turn{})
		reply, err := h.HandleLine(context.Background(), unknownLine, nil)
		if err != nil || reply == nil {
			t.Fatalf("expected reply with error, got: %v", err)
		}
		if reply.Error == nil || reply.Error.Code != -32601 {
			t.Errorf("expected JSON-RPC -32601 error, got %+v", reply.Error)
		}
	})
}

func TestTurnCompletedAndUsage(t *testing.T) {
	lines := readLines(t, filepath.Join("testdata", "turn_completed.jsonl"))
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})

	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	for _, line := range lines {
		_, err := h.HandleLine(context.Background(), line, emit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}

	// 1. Usage
	if events[0].Type != harness.EventUsage || events[0].Usage == nil {
		t.Fatalf("event 0: expected usage event, got %+v", events[0])
	}
	wantUsage := harness.Usage{
		InputTokens:   120,
		OutputTokens:  55,
		CachedTokens:  30,
		ContextTokens: 175,
	}
	if !reflect.DeepEqual(*events[0].Usage, wantUsage) {
		t.Errorf("usage mismatch: got %+v, want %+v", *events[0].Usage, wantUsage)
	}

	// 2. Done
	if events[1].Type != harness.EventDone || events[1].StopReason != "completed" {
		t.Errorf("event 1: expected done event with completed, got %+v", events[1])
	}

	if !h.IsTurnDone() {
		t.Error("expected turn to be marked done")
	}
}

func TestTokenUsageAccumulatedTwice(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	line1 := []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","tokenUsage":{"last":{"inputTokens":100,"outputTokens":40,"reasoningOutputTokens":10,"cachedInputTokens":20}}}}`)
	line2 := []byte(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","tokenUsage":{"last":{"inputTokens":50,"outputTokens":20,"reasoningOutputTokens":5,"cachedInputTokens":10}}}}`)

	if _, err := h.HandleLine(context.Background(), line1, emit); err != nil {
		t.Fatalf("handle line1 failed: %v", err)
	}
	if _, err := h.HandleLine(context.Background(), line2, emit); err != nil {
		t.Fatalf("handle line2 failed: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 usage events, got %d", len(events))
	}

	want1 := harness.Usage{
		InputTokens:   100,
		OutputTokens:  50,
		CachedTokens:  20,
		ContextTokens: 150,
	}
	if !reflect.DeepEqual(*events[0].Usage, want1) {
		t.Errorf("event 0 usage mismatch: got %+v, want %+v", *events[0].Usage, want1)
	}

	want2 := harness.Usage{
		InputTokens:   150,
		OutputTokens:  75,
		CachedTokens:  30,
		ContextTokens: 75,
	}
	if !reflect.DeepEqual(*events[1].Usage, want2) {
		t.Errorf("event 1 usage mismatch: got %+v, want %+v", *events[1].Usage, want2)
	}
}

// TestRequestSpeedSamplesPerRequest 验证每次模型请求各自产生一条速度样本：
// 从该请求的第一个流式增量帧算到它的用量帧，输出 token 含推理输出；
// 没有增量帧的用量帧不产生样本（仅有工具 item/started 也不算）。
func TestRequestSpeedSamplesPerRequest(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	base := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	now := base
	h.now = func() time.Time { return now }

	var events []harness.Event
	emit := func(e harness.Event) { events = append(events, e) }
	handle := func(line string) {
		t.Helper()
		if _, err := h.HandleLine(context.Background(), []byte(line), emit); err != nil {
			t.Fatalf("handle line failed: %v", err)
		}
	}

	// 第一次请求：正文 delta 起算，1.2 秒后到达用量帧（输出 40 + 推理 10）。
	handle(`{"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"item_1","delta":"hi"}}`)
	now = base.Add(1200 * time.Millisecond)
	handle(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","tokenUsage":{"last":{"inputTokens":100,"outputTokens":40,"reasoningOutputTokens":10,"cachedInputTokens":20}}}}`)

	// 第二次请求：推理 delta 起算，900 毫秒后到达用量帧。
	now = base.Add(5 * time.Second)
	handle(`{"jsonrpc":"2.0","method":"item/reasoning/textDelta","params":{"itemId":"item_2","delta":"hmm"}}`)
	now = now.Add(900 * time.Millisecond)
	handle(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","tokenUsage":{"last":{"inputTokens":20,"outputTokens":15}}}}`)

	// 第三次请求只有工具 item/started（生成已结束才开始执行），没有增量帧，不产生样本。
	now = base.Add(8 * time.Second)
	handle(`{"jsonrpc":"2.0","method":"item/started","params":{"item":{"id":"call_1","type":"commandExecution","command":"ls"}}}`)
	now = now.Add(700 * time.Millisecond)
	handle(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","tokenUsage":{"last":{"inputTokens":10,"outputTokens":5}}}}`)

	// 第四次请求没有内容帧就来用量帧，不产生样本。
	now = base.Add(10 * time.Second)
	handle(`{"jsonrpc":"2.0","method":"thread/tokenUsage/updated","params":{"threadId":"th_1","turnId":"turn_1","tokenUsage":{"last":{"inputTokens":10,"outputTokens":3}}}}`)

	var usages []*harness.Usage
	for _, ev := range events {
		if ev.Type == harness.EventUsage && ev.Usage != nil {
			usages = append(usages, ev.Usage)
		}
	}
	if len(usages) != 4 {
		t.Fatalf("expected 4 usage events, got %d", len(usages))
	}

	want1 := []harness.RequestSpeed{{OutputTokens: 50, DurationMs: 1200}}
	if !reflect.DeepEqual(usages[0].Requests, want1) {
		t.Errorf("request 0 speeds mismatch: got %+v, want %+v", usages[0].Requests, want1)
	}

	want2 := []harness.RequestSpeed{{OutputTokens: 50, DurationMs: 1200}, {OutputTokens: 15, DurationMs: 900}}
	if !reflect.DeepEqual(usages[1].Requests, want2) {
		t.Errorf("request 1 speeds mismatch: got %+v, want %+v", usages[1].Requests, want2)
	}
	if !reflect.DeepEqual(usages[2].Requests, want2) {
		t.Errorf("只有 item/started 的请求不应追加样本: got %+v, want %+v", usages[2].Requests, want2)
	}
	if !reflect.DeepEqual(usages[3].Requests, want2) {
		t.Errorf("没有内容帧的请求不应追加样本: got %+v, want %+v", usages[3].Requests, want2)
	}
}

func TestErrorAndTurnFailed(t *testing.T) {
	lines := readLines(t, filepath.Join("testdata", "error.jsonl"))
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})

	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	for _, line := range lines {
		_, err := h.HandleLine(context.Background(), line, emit)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	// 预期：
	// 0: EventError ("fatal internal error in codex")
	// 1: EventError ("network connection closed prematurely")
	// 2: EventDone (StopReason="failed")
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}

	if events[0].Type != harness.EventError || events[0].Err != "fatal internal error in codex" {
		t.Errorf("event 0 mismatch: %+v", events[0])
	}
	if events[1].Type != harness.EventError || events[1].Err != "network connection closed prematurely" {
		t.Errorf("event 1 mismatch: %+v", events[1])
	}
	if events[2].Type != harness.EventDone || events[2].StopReason != "failed" {
		t.Errorf("event 2 mismatch: %+v", events[2])
	}

	if !h.IsTurnDone() {
		t.Error("expected turn to be marked done after turn/failed")
	}
}

// runFixture 把 testdata 中的 jsonl 逐行喂给 handler，并返回按顺序收集到的事件。
func runFixture(t *testing.T, h *ProtocolHandler, name string) []harness.Event {
	t.Helper()
	lines := readLines(t, filepath.Join("testdata", name))
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}
	for _, line := range lines {
		reply, err := h.HandleLine(context.Background(), line, emit)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if reply != nil {
			t.Fatalf("%s: unexpected reply: %+v", name, reply)
		}
	}
	return events
}

// TestTokenUsageTotalNotDoubleCounted 验证带 total 的重复 usage 更新不会重复计数：
// 累计口径落在本轮（total - 轮前基线），ContextTokens/ContextWindow 取单次请求口径。
func TestTokenUsageTotalNotDoubleCounted(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	events := runFixture(t, h, "usage_repeated.jsonl")

	if len(events) != 3 {
		t.Fatalf("expected 3 usage events, got %d: %+v", len(events), events)
	}

	wants := []harness.Usage{
		{InputTokens: 120, OutputTokens: 55, CachedTokens: 30, ContextTokens: 175, ContextWindow: 272000},
		{InputTokens: 120, OutputTokens: 55, CachedTokens: 30, ContextTokens: 175, ContextWindow: 272000},
		{InputTokens: 250, OutputTokens: 110, CachedTokens: 100, ContextTokens: 185, ContextWindow: 272000},
	}
	for i, want := range wants {
		if events[i].Type != harness.EventUsage || events[i].Usage == nil {
			t.Fatalf("event %d: expected usage event, got %+v", i, events[i])
		}
		if !reflect.DeepEqual(*events[i].Usage, want) {
			t.Errorf("event %d usage mismatch: got %+v, want %+v", i, *events[i].Usage, want)
		}
	}
}

// TestTurnFailedFinishesPendingTools 验证失败收尾时先为未完成工具补发 tool_end，再发 error/done。
func TestTurnFailedFinishesPendingTools(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	events := runFixture(t, h, "turn_failed_pending_tool.jsonl")

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != harness.EventToolStart || events[0].ToolID != "cmd_1" {
		t.Errorf("event 0 mismatch: %+v", events[0])
	}
	if events[1].Type != harness.EventToolEnd || events[1].ToolID != "cmd_1" || !events[1].IsError {
		t.Errorf("event 1: expected failed tool_end for cmd_1, got %+v", events[1])
	}
	if !strings.Contains(events[1].Output, "本轮失败") || !strings.Contains(events[1].Output, "工具未完成") {
		t.Errorf("event 1: unexpected output %q", events[1].Output)
	}
	if events[2].Type != harness.EventError || events[2].Err != "sandbox denied" {
		t.Errorf("event 2 mismatch: %+v", events[2])
	}
	if events[3].Type != harness.EventDone || events[3].StopReason != "failed" {
		t.Errorf("event 3 mismatch: %+v", events[3])
	}
}

// TestTurnInterruptedFinishesPendingTools 验证中断收尾同样补发 tool_end，且不发 error 事件。
func TestTurnInterruptedFinishesPendingTools(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	events := runFixture(t, h, "turn_interrupted_pending_tool.jsonl")

	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != harness.EventToolStart || events[0].ToolID != "mcp_1" || events[0].ToolName != "fs/read" {
		t.Errorf("event 0 mismatch: %+v", events[0])
	}
	if events[1].Type != harness.EventToolEnd || events[1].ToolID != "mcp_1" || !events[1].IsError {
		t.Errorf("event 1: expected failed tool_end for mcp_1, got %+v", events[1])
	}
	if !strings.Contains(events[1].Output, "本轮已中断") {
		t.Errorf("event 1: unexpected output %q", events[1].Output)
	}
	if events[2].Type != harness.EventDone || events[2].StopReason != "interrupted" {
		t.Errorf("event 2 mismatch: %+v", events[2])
	}
	for _, e := range events {
		if e.Type == harness.EventError {
			t.Errorf("interrupted turn should not emit error event: %+v", e)
		}
	}
}

// TestErrorNotificationFinishesPendingToolsOnce 验证 error 通知与随后的 turn/failed 只补发一次 tool_end。
func TestErrorNotificationFinishesPendingToolsOnce(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	events := runFixture(t, h, "error_pending_tool.jsonl")

	var toolEnds int
	var errs []string
	for _, e := range events {
		if e.Type == harness.EventToolEnd {
			toolEnds++
			if e.ToolID != "cmd_2" || !e.IsError {
				t.Errorf("unexpected tool_end: %+v", e)
			}
		}
		if e.Type == harness.EventError {
			errs = append(errs, e.Err)
		}
	}
	if toolEnds != 1 {
		t.Fatalf("expected exactly 1 tool_end, got %d: %+v", toolEnds, events)
	}
	if len(errs) != 2 || errs[0] != "stream disconnected" || errs[1] != "stream disconnected" {
		t.Errorf("unexpected error events: %v", errs)
	}
	last := events[len(events)-1]
	if last.Type != harness.EventDone || last.StopReason != "failed" {
		t.Errorf("expected trailing failed done event, got %+v", last)
	}
}

// TestCompletedTurnKeepsToolOpen 验证正常完成的轮次不受失败收尾影响。
func TestCompletedTurnKeepsToolOpen(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}
	lines := [][]byte{
		[]byte(`{"jsonrpc":"2.0","method":"item/started","params":{"item":{"id":"cmd_9","type":"commandExecution","command":"ls"}}}`),
		[]byte(`{"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn_1","status":"completed"}}}`),
	}
	for _, line := range lines {
		if _, err := h.HandleLine(context.Background(), line, emit); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events (tool_start + done), got %d: %+v", len(events), events)
	}
	if events[1].Type != harness.EventDone || events[1].StopReason != "completed" {
		t.Errorf("event 1 mismatch: %+v", events[1])
	}
}

// TestPlanItemStreaming 验证 plan 增量以文本流出，完成时只补尾部或一次性发全文，且不发工具事件。
func TestPlanItemStreaming(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	events := runFixture(t, h, "plan_items.jsonl")

	wantTexts := []string{"1. 读代码\n", "2. 改代码", "\n3. 跑测试", "没有流式增量的计划全文"}
	if len(events) != len(wantTexts) {
		t.Fatalf("expected %d events, got %d: %+v", len(wantTexts), len(events), events)
	}
	for i, want := range wantTexts {
		if events[i].Type != harness.EventText {
			t.Fatalf("event %d: expected text, got %+v", i, events[i])
		}
		if events[i].Text != want {
			t.Errorf("event %d: expected %q, got %q", i, want, events[i].Text)
		}
	}
}

// TestReviewModeAndCompactionItems 验证审阅结论文本与上下文压缩提示的展示，
// 且进入审阅模式不发事件、这些 item 不被当成工具调用。
func TestReviewModeAndCompactionItems(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	events := runFixture(t, h, "review_mode_and_compaction.jsonl")

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != harness.EventText || events[0].Text != "审阅结论：两处风险" {
		t.Errorf("event 0 mismatch: %+v", events[0])
	}
	if events[1].Type != harness.EventText || events[1].Text != "上下文已压缩" {
		t.Errorf("event 1 mismatch: %+v", events[1])
	}
	for _, e := range events {
		if e.Type == harness.EventToolStart || e.Type == harness.EventToolEnd {
			t.Errorf("display items must not emit tool events: %+v", e)
		}
	}
}

// TestTurnPlanUpdated 验证计划更新映射为独立的完整工具调用。
func TestTurnPlanUpdated(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{})
	var events []harness.Event
	emit := func(e harness.Event) { events = append(events, e) }
	line := []byte(`{"jsonrpc":"2.0","method":"turn/plan/updated","params":{"threadId":"th_1","turnId":"turn_1","explanation":"计划说明","plan":[{"step":"第一步","status":"inProgress"},{"step":"第二步","status":"pending"}]}}`)
	for i := 0; i < 2; i++ {
		if _, err := h.HandleLine(context.Background(), line, emit); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 4 {
		t.Fatalf("期望两对工具事件，实际 %+v", events)
	}
	for i := 0; i < len(events); i += 2 {
		start, end := events[i], events[i+1]
		if start.Type != harness.EventToolStart || end.Type != harness.EventToolEnd || start.ToolName != "update_plan" || end.ToolName != "update_plan" || start.ToolID == "" || start.ToolID != end.ToolID {
			t.Fatalf("工具事件不匹配：%+v %+v", start, end)
		}
		var input struct {
			Explanation string `json:"explanation"`
			Plan        []struct {
				Step   string `json:"step"`
				Status string `json:"status"`
			} `json:"plan"`
		}
		if err := json.Unmarshal(start.Input, &input); err != nil {
			t.Fatal(err)
		}
		if input.Explanation != "计划说明" || len(input.Plan) != 2 || input.Plan[0].Step != "第一步" || input.Plan[0].Status != "inProgress" || input.Plan[1].Step != "第二步" || input.Plan[1].Status != "pending" {
			t.Fatalf("计划输入不匹配：%+v", input)
		}
	}
	if events[0].ToolID == events[2].ToolID {
		t.Fatalf("同一轮的计划更新应使用不同的工具 ID：%q", events[0].ToolID)
	}
}

func TestTurnPlanUpdatedEmpty(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{})
	var events []harness.Event
	line := []byte(`{"jsonrpc":"2.0","method":"turn/plan/updated","params":{"turnId":"turn_1","plan":[]}}`)
	if _, err := h.HandleLine(context.Background(), line, func(e harness.Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("空计划不应发出事件：%+v", events)
	}
}

// TestThreadCompactedEmitsContextCompacted 验证 thread/compacted 不再被当成「有意忽略」，
// 而是映射成一条上下文压缩事件：token 数未知留 0，也不算未处理通知。
func TestThreadCompactedEmitsContextCompacted(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	var events []harness.Event
	emit := func(e harness.Event) { events = append(events, e) }

	line := []byte(`{"jsonrpc":"2.0","method":"thread/compacted","params":{"threadId":"th_1"}}`)
	if _, err := h.HandleLine(context.Background(), line, emit); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].Type != harness.EventContextCompacted {
		t.Fatalf("expected one context_compacted event, got %+v", events)
	}
	if events[0].CompactTokensBefore != 0 || events[0].CompactTokensAfter != 0 {
		t.Errorf("codex 通知里没有 token 数，应留 0：%+v", events[0])
	}

	h.mu.Lock()
	logged := len(h.loggedUnknown)
	h.mu.Unlock()
	if logged != 0 {
		t.Errorf("thread/compacted 不应被记为未知通知，实际 %d 条", logged)
	}
}

// TestUnknownNotificationLoggedOnce 验证未知通知方法名在 handler 生命周期内只记一次，
// 而已知但有意忽略的通知不记。
func TestUnknownNotificationLoggedOnce(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	var events []harness.Event
	emit := func(e harness.Event) {
		events = append(events, e)
	}

	unknown := []byte(`{"jsonrpc":"2.0","method":"some/unknown/notification","params":{}}`)
	for i := 0; i < 3; i++ {
		if _, err := h.HandleLine(context.Background(), unknown, emit); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if len(events) != 0 {
		t.Fatalf("unknown notification must not emit events, got %+v", events)
	}

	h.mu.Lock()
	logged := len(h.loggedUnknown)
	_, ok := h.loggedUnknown["some/unknown/notification"]
	h.mu.Unlock()
	if logged != 1 || !ok {
		t.Fatalf("expected the unknown method to be logged once, got %d entries (found=%v)", logged, ok)
	}

	known := []byte(`{"jsonrpc":"2.0","method":"turn/started","params":{}}`)
	if _, err := h.HandleLine(context.Background(), known, emit); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h.mu.Lock()
	logged = len(h.loggedUnknown)
	h.mu.Unlock()
	if logged != 1 {
		t.Errorf("known ignored notification must not be logged, got %d entries", logged)
	}
	if len(events) != 0 {
		t.Errorf("known ignored notification must not emit events, got %+v", events)
	}
}

func TestMapPermissionMode(t *testing.T) {
	tests := []struct {
		mode       harness.PermissionMode
		wantSand   string
		wantPolicy string
	}{
		{harness.ModeReadOnly, "read-only", "on-request"},
		{harness.ModeSupervised, "read-only", "untrusted"},
		{harness.ModeAutoAcceptEdits, "workspace-write", "on-request"},
		{harness.ModeAuto, "workspace-write", "on-request"},
		{harness.ModeAgent, "workspace-write", "on-request"},
		{"", "workspace-write", "on-request"},
		{harness.ModeFullAccess, "danger-full-access", "never"},
		{"unknown", "read-only", "on-request"},
	}

	for _, tc := range tests {
		sand, pol := mapPermissionMode(tc.mode)
		if sand != tc.wantSand || pol != tc.wantPolicy {
			t.Errorf("mode %q: got (%q, %q), want (%q, %q)", tc.mode, sand, pol, tc.wantSand, tc.wantPolicy)
		}
	}
}

type mockSandbox struct {
	cmdFunc func(ctx context.Context) *exec.Cmd
}

func (m *mockSandbox) Command(ctx context.Context, workDir string, mode harness.PermissionMode, name string, args ...string) (*exec.Cmd, func(), error) {
	return m.cmdFunc(ctx), nil, nil
}

// poolProcs 返回当前进程池中的进程快照，便于测试断言。
func (h *Harness) poolProcs() []*residentProcess {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*residentProcess(nil), h.procs...)
}

// busyProcCount 返回当前被轮次占用的进程数量。
func (h *Harness) busyProcCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, p := range h.procs {
		if p != nil && p.busy {
			n++
		}
	}
	return n
}

// pongAppServerScript 是一个最小的假 app-server：initialize/thread.*/turn/start 都以固定值应答并立即完成一轮。
const pongAppServerScript = `
import sys, json

while True:
    line = sys.stdin.readline()
    if not line:
        break
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except:
        continue
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"capabilities":{}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/start" or method == "thread/resume":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":"th_mock_123"}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":"turn_mock_abc"}}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"PONG"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn_mock_abc","status":"completed"}}}) + "\n")
        sys.stdout.flush()
`

// slowAppServerScript 与 pongAppServerScript 相同，但当轮输入包含 SLOW 时延迟 2 秒才结束该轮，
// 用于验证并发轮次不会被串行化。
const slowAppServerScript = `
import sys, json, time

while True:
    line = sys.stdin.readline()
    if not line:
        break
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except:
        continue
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"capabilities":{}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/start" or method == "thread/resume":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":"th_mock_123"}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/start":
        try:
            text = msg.get("params", {}).get("input", [{}])[0].get("text", "")
        except:
            text = ""
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":"turn_mock_abc"}}}) + "\n")
        sys.stdout.flush()
        if "SLOW" in text:
            time.sleep(2)
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"PONG"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn_mock_abc","status":"completed"}}}) + "\n")
        sys.stdout.flush()
`

func TestHarnessResidentProcessReuse(t *testing.T) {
	pythonScript := `
import sys, json

while True:
    line = sys.stdin.readline()
    if not line:
        break
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except:
        continue
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"capabilities":{}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/start" or method == "thread/resume":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":"th_mock_123"}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":"turn_mock_abc"}}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"PONG"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn_mock_abc","status":"completed"}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/interrupt":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn_mock_abc","status":"interrupted"}}}) + "\n")
        sys.stdout.flush()
`

	sb := &mockSandbox{
		cmdFunc: func(ctx context.Context) *exec.Cmd {
			cmd := exec.CommandContext(ctx, "python3", "-u", "-c", pythonScript)
			cmd.Stderr = os.Stderr
			return cmd
		},
	}

	workDir := t.TempDir()
	h := New(Options{})
	defer func() {
		if err := h.Close(); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	}()

	var events1 []harness.Event
	turn1 := harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeAgent,
		Prompt:  "Ping 1",
		Sandbox: sb,
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel1()

	if err := h.Run(ctx1, turn1, func(e harness.Event) {
		events1 = append(events1, e)
	}); err != nil {
		t.Fatalf("round 1 run failed: %v", err)
	}

	if len(h.poolProcs()) != 1 || h.poolProcs()[0].cmd == nil || h.poolProcs()[0].cmd.Process == nil {
		t.Fatal("expected one resident process in the pool after round 1")
	}
	pid1 := h.poolProcs()[0].cmd.Process.Pid

	var sessionID string
	var text1 string
	for _, e := range events1 {
		if e.Type == harness.EventSession {
			sessionID = e.SessionID
		}
		if e.Type == harness.EventText {
			text1 += e.Text
		}
	}
	if sessionID != "th_mock_123" {
		t.Fatalf("expected session ID th_mock_123, got %q", sessionID)
	}
	if text1 != "PONG" {
		t.Fatalf("expected text PONG, got %q", text1)
	}

	// Round 2: 复用同一会话，应复用同一常驻进程（PID 保持一致）
	var events2 []harness.Event
	turn2 := harness.Turn{
		WorkDir:   workDir,
		Mode:      harness.ModeAgent,
		Prompt:    "Ping 2",
		SessionID: sessionID,
		Sandbox:   sb,
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	if err := h.Run(ctx2, turn2, func(e harness.Event) {
		events2 = append(events2, e)
	}); err != nil {
		t.Fatalf("round 2 run failed: %v", err)
	}

	if len(h.poolProcs()) != 1 || h.poolProcs()[0].cmd == nil || h.poolProcs()[0].cmd.Process == nil {
		t.Fatal("expected one resident process in the pool after round 2")
	}
	pid2 := h.poolProcs()[0].cmd.Process.Pid
	if pid1 != pid2 {
		t.Errorf("expected resident process to be reused (pid %d == %d)", pid1, pid2)
	}

	var text2 string
	for _, e := range events2 {
		if e.Type == harness.EventText {
			text2 += e.Text
		}
	}
	if text2 != "PONG" {
		t.Fatalf("expected text PONG in round 2, got %q", text2)
	}

	// 测试 Close() 能妥善退出进程
	if err := h.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
	if len(h.poolProcs()) != 0 {
		t.Errorf("expected process pool to be empty after Close, got %d", len(h.poolProcs()))
	}

	// 再次调用 Close() 幂等无错
	if err := h.Close(); err != nil {
		t.Errorf("second Close failed: %v", err)
	}
}

// TestHarnessProcessPoolSeparatesWorkDirs 验证不同 workDir 的轮次各用独立常驻进程，
// 切换目录不会杀掉旧进程，回到旧目录仍复用原进程。
func TestHarnessProcessPoolSeparatesWorkDirs(t *testing.T) {
	sb := &mockSandbox{
		cmdFunc: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "python3", "-u", "-c", pongAppServerScript)
		},
	}

	dirA := t.TempDir()
	dirB := t.TempDir()

	h := New(Options{})
	defer func() {
		_ = h.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	noopEmit := func(harness.Event) {}

	if err := h.Run(ctx, harness.Turn{WorkDir: dirA, Mode: harness.ModeAgent, Prompt: "Ping A", Sandbox: sb}, noopEmit); err != nil {
		t.Fatalf("round in workDir A failed: %v", err)
	}

	procs := h.poolProcs()
	if len(procs) != 1 || procs[0].cmd == nil || procs[0].cmd.Process == nil {
		t.Fatalf("expected one resident process after round in workDir A, got %d", len(procs))
	}
	pidA := procs[0].cmd.Process.Pid

	if err := h.Run(ctx, harness.Turn{WorkDir: dirB, Mode: harness.ModeAgent, Prompt: "Ping B", Sandbox: sb}, noopEmit); err != nil {
		t.Fatalf("round in workDir B failed: %v", err)
	}

	procs = h.poolProcs()
	if len(procs) != 2 {
		t.Fatalf("expected both work dirs to keep their own resident process, got %d", len(procs))
	}

	var procA, procB *residentProcess
	for _, p := range procs {
		if p.cmd == nil || p.cmd.Process == nil {
			continue
		}
		if p.cmd.Process.Pid == pidA {
			procA = p
		} else {
			procB = p
		}
	}
	if procA == nil {
		t.Fatalf("expected the workDir A process (pid %d) to stay in the pool after switching dirs", pidA)
	}
	if procA.isDead() {
		t.Errorf("expected the workDir A process (pid %d) to stay alive after switching dirs", pidA)
	}
	if procB == nil || procB.isDead() {
		t.Errorf("expected the workDir B process to be alive")
	}

	// 回到 workDir A：应复用原进程，而不是新起一个
	if err := h.Run(ctx, harness.Turn{WorkDir: dirA, Mode: harness.ModeAgent, Prompt: "Ping A2", Sandbox: sb}, noopEmit); err != nil {
		t.Fatalf("second round in workDir A failed: %v", err)
	}

	procs = h.poolProcs()
	if len(procs) != 2 {
		t.Fatalf("expected no extra resident process after returning to workDir A, got %d", len(procs))
	}
	reused := false
	for _, p := range procs {
		if p.cmd != nil && p.cmd.Process != nil && p.cmd.Process.Pid == pidA {
			reused = true
		}
	}
	if !reused {
		t.Errorf("expected workDir A to reuse its resident process (pid %d)", pidA)
	}
}

// TestHarnessProcessPoolConcurrentTurns 验证两个并发轮次各自占用独立进程，第二个轮次不会等第一个轮次结束。
func TestHarnessProcessPoolConcurrentTurns(t *testing.T) {
	sb := &mockSandbox{
		cmdFunc: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "python3", "-u", "-c", slowAppServerScript)
		},
	}

	workDir := t.TempDir()

	h := New(Options{})
	defer func() {
		_ = h.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	noopEmit := func(harness.Event) {}

	slowDone := make(chan error, 1)
	go func() {
		slowDone <- h.Run(ctx, harness.Turn{WorkDir: workDir, Mode: harness.ModeAgent, Prompt: "SLOW ping", Sandbox: sb}, noopEmit)
	}()

	// 等第一个轮次真正占用住一个进程
	deadline := time.Now().Add(5 * time.Second)
	for h.busyProcCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.busyProcCount() == 0 {
		t.Fatal("expected the first round to occupy a resident process")
	}

	start := time.Now()
	if err := h.Run(ctx, harness.Turn{WorkDir: workDir, Mode: harness.ModeAgent, Prompt: "FAST ping", Sandbox: sb}, noopEmit); err != nil {
		t.Fatalf("second round failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("second round waited for the first round to finish (took %v)", elapsed)
	}

	if procs := h.poolProcs(); len(procs) != 2 {
		t.Errorf("expected two resident processes for concurrent rounds, got %d", len(procs))
	}

	if err := <-slowDone; err != nil {
		t.Fatalf("first round failed: %v", err)
	}
}

func TestHarnessTurnInterrupt(t *testing.T) {
	pythonScript := `
import sys, json, time

while True:
    line = sys.stdin.readline()
    if not line:
        break
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except:
        continue
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"capabilities":{}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":"th_mock_123"}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":"turn_mock_abc"}}}) + "\n")
        sys.stdout.flush()
        # 挂起不完成，等待中断
        time.sleep(10)
    elif method == "turn/interrupt":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":"turn_mock_abc","status":"interrupted"}}}) + "\n")
        sys.stdout.flush()
`

	sb := &mockSandbox{
		cmdFunc: func(ctx context.Context) *exec.Cmd {
			return exec.CommandContext(ctx, "python3", "-u", "-c", pythonScript)
		},
	}

	workDir := t.TempDir()
	h := New(Options{})
	defer func() {
		_ = h.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())

	turn := harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeAgent,
		Prompt:  "Ping Cancel",
		Sandbox: sb,
	}

	// 在后台 goroutine 中稍后取消，验证发送 turn/interrupt 的路径
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	err := h.Run(ctx, turn, nil)
	if err == nil {
		t.Error("expected error from canceled context, got nil")
	}
}

func TestBuildThreadParams_Config(t *testing.T) {
	turn := harness.Turn{
		WorkDir: "/path/to/workdir",
		Model:   "gpt-4o",
	}

	t.Run("内置端点非空时注入config表及header", func(t *testing.T) {
		params, method := buildThreadParams(turn, "workspace-write", "on-request", "", harness.BuiltinMCP{URL: "http://127.0.0.1:12345/mcp", Token: "test-token"}, nil, nil)
		if method != "thread/start" {
			t.Fatalf("expected thread/start, got %s", method)
		}
		if params["cwd"] != "/path/to/workdir" {
			t.Errorf("expected cwd /path/to/workdir, got %v", params["cwd"])
		}
		if params["sandbox"] != "workspace-write" {
			t.Errorf("expected sandbox workspace-write, got %v", params["sandbox"])
		}
		if params["approvalPolicy"] != "on-request" {
			t.Errorf("expected approvalPolicy on-request, got %v", params["approvalPolicy"])
		}
		if params["model"] != "gpt-4o" {
			t.Errorf("expected model gpt-4o, got %v", params["model"])
		}

		cfgRaw, ok := params["config"]
		if !ok {
			t.Fatalf("expected config in params: %+v", params)
		}
		cfg, ok := cfgRaw.(map[string]any)
		if !ok {
			t.Fatalf("expected map[string]any for config, got %T", cfgRaw)
		}
		mcpServers, ok := cfg["mcp_servers"].(map[string]any)
		if !ok {
			t.Fatalf("expected mcp_servers map, got %T", cfg["mcp_servers"])
		}
		mcodeServer, ok := mcpServers["mcode"].(map[string]any)
		if !ok {
			t.Fatalf("expected mcode server map, got %T", mcpServers["mcode"])
		}
		if mcodeServer["url"] != "http://127.0.0.1:12345/mcp" {
			t.Errorf("expected url http://127.0.0.1:12345/mcp, got %v", mcodeServer["url"])
		}
		headers, ok := mcodeServer["http_headers"].(map[string]any)
		if !ok {
			t.Fatalf("expected http_headers map, got %T", mcodeServer["http_headers"])
		}
		if headers["Authorization"] != "Bearer test-token" {
			t.Errorf("expected Authorization Bearer test-token, got %v", headers["Authorization"])
		}
		if headers["X-Mcode-Workdir"] != "/path/to/workdir" {
			t.Errorf("expected X-Mcode-Workdir /path/to/workdir, got %v", headers["X-Mcode-Workdir"])
		}
	})

	t.Run("内置端点为空且无外部端点时不包含config表", func(t *testing.T) {
		params, method := buildThreadParams(turn, "workspace-write", "on-request", "", harness.BuiltinMCP{}, nil, nil)
		if method != "thread/start" {
			t.Fatalf("expected thread/start, got %s", method)
		}
		if _, ok := params["config"]; ok {
			t.Errorf("expected no config when builtin endpoint is empty, got %+v", params["config"])
		}
	})

	t.Run("threadID非空时走thread/resume并携带excludeTurns", func(t *testing.T) {
		params, method := buildThreadParams(turn, "workspace-write", "on-request", "th_12345", harness.BuiltinMCP{URL: "http://127.0.0.1:12345/mcp", Token: "test-token"}, nil, nil)
		if method != "thread/resume" {
			t.Fatalf("expected thread/resume, got %s", method)
		}
		if params["threadId"] != "th_12345" {
			t.Errorf("expected threadId th_12345, got %v", params["threadId"])
		}
		if params["excludeTurns"] != true {
			t.Errorf("expected excludeTurns true, got %v", params["excludeTurns"])
		}
		if _, ok := params["config"]; !ok {
			t.Errorf("expected config present on resume when builtin endpoint is non-empty")
		}
	})

	t.Run("有线程ID时使用按会话路由地址且不带workdir头", func(t *testing.T) {
		threadID := "0f8fad5b-d9cb-469f-a165-70867728950e"
		threadTurn := harness.Turn{
			WorkDir:  "/path/to/workdir",
			ThreadID: threadID,
		}
		builtin := harness.BuiltinMCP{
			ThreadBase:  "http://127.0.0.1:23456/mcp/mcode/",
			ThreadToken: "thread-token",
			URL:         "http://127.0.0.1:12345/mcp",
			Token:       "test-token",
		}
		params, _ := buildThreadParams(threadTurn, "workspace-write", "on-request", "", builtin, nil, nil)
		mcpServers := params["config"].(map[string]any)["mcp_servers"].(map[string]any)
		mcodeServer := mcpServers["mcode"].(map[string]any)
		if want := "http://127.0.0.1:23456/mcp/mcode/" + threadID; mcodeServer["url"] != want {
			t.Errorf("expected thread url %q, got %v", want, mcodeServer["url"])
		}
		headers := mcodeServer["http_headers"].(map[string]any)
		if headers["Authorization"] != "Bearer thread-token" {
			t.Errorf("expected thread token, got %v", headers["Authorization"])
		}
		if _, ok := headers["X-Mcode-Workdir"]; ok {
			t.Errorf("thread endpoint must not carry X-Mcode-Workdir: %v", headers)
		}
	})

	t.Run("外部endpoint合并、跳过保留名、内置端点为空时仍生成config且与内置共存", func(t *testing.T) {
		external := []harness.MCPEndpoint{
			{Name: "ext_service", URL: "http://127.0.0.1:5000/mcp/ext_service", Token: "ext-tok"},
			{Name: "mcode", URL: "http://attacker.com", Token: "bad-token"},
			{Name: "mcode_approval", URL: "http://attacker.com", Token: "bad-token"},
		}

		// 1. 内置端点为空时也要生成 config
		params, _ := buildThreadParams(turn, "workspace-write", "on-request", "", harness.BuiltinMCP{}, external, nil)
		cfgRaw, ok := params["config"]
		if !ok {
			t.Fatalf("expected config generated even when builtin endpoint is empty")
		}
		cfg := cfgRaw.(map[string]any)
		mcpServers := cfg["mcp_servers"].(map[string]any)
		if _, ok := mcpServers["mcode"]; ok {
			t.Errorf("mcode should not be present when builtin endpoint is empty")
		}
		if _, ok := mcpServers["mcode_approval"]; ok {
			t.Errorf("mcode_approval should be skipped")
		}
		ext, ok := mcpServers["ext_service"].(map[string]any)
		if !ok {
			t.Fatalf("ext_service not found in mcp_servers")
		}
		if ext["url"] != "http://127.0.0.1:5000/mcp/ext_service" {
			t.Errorf("unexpected ext_service url: %v", ext["url"])
		}
		headers := ext["http_headers"].(map[string]any)
		if headers["Authorization"] != "Bearer ext-tok" {
			t.Errorf("unexpected Authorization header: %v", headers["Authorization"])
		}

		// 2. 内置端点存在时与内置 mcode 共存
		paramsWithBuiltin, _ := buildThreadParams(turn, "workspace-write", "on-request", "", harness.BuiltinMCP{URL: "http://127.0.0.1:12345/mcp", Token: "builtin-tok"}, external, nil)
		cfg2 := paramsWithBuiltin["config"].(map[string]any)
		servers2 := cfg2["mcp_servers"].(map[string]any)
		mcode2 := servers2["mcode"].(map[string]any)
		if mcode2["url"] != "http://127.0.0.1:12345/mcp" {
			t.Errorf("builtin mcode overwritten by external: %v", mcode2["url"])
		}
		if _, ok := servers2["ext_service"]; !ok {
			t.Errorf("ext_service missing when builtin is present")
		}
	})
}

// mockInteractor 同时实现 Approver、Interactor 与 DeliveryAcker，用于覆盖提问类请求。
type mockInteractor struct {
	response harness.ApprovalResponse
	err      error
	requests []harness.ApprovalRequest
	acks     int
	ackErr   error
}

func (m *mockInteractor) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	m.requests = append(m.requests, req)
	return m.response.Decision, m.err
}

func (m *mockInteractor) Interact(ctx context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	m.requests = append(m.requests, req)
	return m.response, m.err
}

func (m *mockInteractor) AckDelivery(req harness.ApprovalRequest, err error) {
	m.acks++
	m.ackErr = err
}

func TestPatchUpdatedFileChange(t *testing.T) {
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	events := runFixture(t, h, "patch_updated.jsonl")

	// 预想：tool_start、两版 main.go 增量、config.json 增量、tool_end。
	// 重复的 patchUpdated 与完成时重复的 changes 均被去重，turn/diff/updated 不发事件。
	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d: %+v", len(events), events)
	}
	if events[0].Type != harness.EventToolStart || events[0].ToolID != "fc_2" {
		t.Errorf("event 0 mismatch: %+v", events[0])
	}
	wantPaths := []string{"main.go", "main.go", "config.json"}
	wantPatches := []string{
		"@@ -1 +1 @@\n-package old\n+package main\n",
		"@@ -1 +1 @@\n-package old\n+package main2\n",
		"@@ -0,0 +1 @@\n+{}\n",
	}
	for i, path := range wantPaths {
		got := events[i+1]
		if got.Type != harness.EventFileChange || got.Path != path || got.Patch != wantPatches[i] {
			t.Errorf("event %d mismatch: %+v", i+1, got)
		}
	}
	if events[4].Type != harness.EventToolEnd || events[4].ToolID != "fc_2" {
		t.Errorf("event 4 mismatch: %+v", events[4])
	}
}

func TestLateToolCompletedNotDuplicated(t *testing.T) {
	// 工具已被失败收尾补发过 tool_end，迟到的 item/completed 不应再发一条结束事件。
	errLine := `{"jsonrpc":"2.0","method":"error","params":{"threadId":"th_1","turnId":"turn_1","error":{"message":"stream disconnected"},"willRetry":false}}`
	cases := []struct {
		name      string
		started   string
		completed string
	}{
		{
			name:      "commandExecution",
			started:   `{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"th_1","turnId":"turn_1","item":{"id":"tool_1","type":"commandExecution","command":"make","status":"inProgress"}}}`,
			completed: `{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"th_1","turnId":"turn_1","item":{"id":"tool_1","type":"commandExecution","command":"make","status":"completed","exitCode":0,"aggregatedOutput":"done"}}}`,
		},
		{
			name:      "mcpToolCall",
			started:   `{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"th_1","turnId":"turn_1","item":{"id":"tool_1","type":"mcpToolCall","server":"fs","tool":"read_file","status":"inProgress"}}}`,
			completed: `{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"th_1","turnId":"turn_1","item":{"id":"tool_1","type":"mcpToolCall","server":"fs","tool":"read_file","status":"completed","result":{}}}}`,
		},
		{
			name:      "dynamicToolCall",
			started:   `{"jsonrpc":"2.0","method":"item/started","params":{"threadId":"th_1","turnId":"turn_1","item":{"id":"tool_1","type":"dynamicToolCall","tool":"lookup","status":"inProgress"}}}`,
			completed: `{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"th_1","turnId":"turn_1","item":{"id":"tool_1","type":"dynamicToolCall","tool":"lookup","status":"completed","success":true}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
			var events []harness.Event
			emit := func(e harness.Event) {
				events = append(events, e)
			}
			for _, line := range [][]byte{[]byte(tc.started), []byte(errLine), []byte(tc.completed)} {
				if _, err := h.HandleLine(context.Background(), line, emit); err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}

			toolEnds := 0
			for _, e := range events {
				if e.Type == harness.EventToolEnd && e.ToolID == "tool_1" {
					toolEnds++
				}
			}
			if toolEnds != 1 {
				t.Fatalf("expected exactly 1 tool_end for tool_1, got %d: %+v", toolEnds, events)
			}
			if events[1].Type != harness.EventToolEnd || !events[1].IsError {
				t.Errorf("expected failed tool_end from finishPendingTools, got %+v", events[1])
			}
			if events[2].Type != harness.EventError {
				t.Errorf("expected error event, got %+v", events[2])
			}
		})
	}
}

func TestCompletedWithoutStartedEmitsToolEnd(t *testing.T) {
	// 直接收到 item/completed（没有 item/started）的工具仍要发 tool_end。
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	var events []harness.Event
	line := []byte(`{"jsonrpc":"2.0","method":"item/completed","params":{"threadId":"th_1","turnId":"turn_1","item":{"id":"cmd_9","type":"commandExecution","command":"ls","status":"completed","exitCode":0,"aggregatedOutput":"ok"}}}`)
	if _, err := h.HandleLine(context.Background(), line, func(e harness.Event) {
		events = append(events, e)
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(events) != 1 || events[0].Type != harness.EventToolEnd || events[0].ToolID != "cmd_9" {
		t.Fatalf("expected single tool_end for cmd_9, got %+v", events)
	}
}

// requestUserInputLine 是一行带两道题的 item/tool/requestUserInput 请求。
const requestUserInputLine = `{"jsonrpc":"2.0","id":401,"method":"item/tool/requestUserInput","params":{"threadId":"th_1","turnId":"turn_1","itemId":"ui_1","isBlocking":true,"questions":[{"id":"scope","header":"改动范围","question":"这次改哪些文件？","isOther":true,"options":[{"label":"只改协议层","description":"只动 internal/providers/codex"},{"label":"连测试一起","description":"同时补 protocol_test.go"}]},{"id":"token","header":"访问令牌","question":"令牌是什么？","isSecret":true}]}}`

func TestRequestUserInput(t *testing.T) {
	t.Run("回答写入 answers", func(t *testing.T) {
		interactor := &mockInteractor{response: harness.ApprovalResponse{
			Decision: harness.DecisionAllow,
			Answers:  map[string][]string{"scope": {"只改协议层"}, "token": {"s3cret"}},
		}}
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent, Approver: interactor})

		reply, err := h.HandleLine(context.Background(), []byte(requestUserInputLine), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		if len(interactor.requests) != 1 {
			t.Fatalf("expected 1 interact request, got %d", len(interactor.requests))
		}

		req := interactor.requests[0]
		if req.Kind != harness.RequestKindQuestion || req.ToolID != "ui_1" || req.ToolName != "requestUserInput" {
			t.Errorf("request mismatch: %+v", req)
		}
		if req.Summary != "这次改哪些文件？（共 2 个问题）" {
			t.Errorf("summary mismatch: %q", req.Summary)
		}
		if len(req.Questions) != 2 {
			t.Fatalf("expected 2 questions, got %d", len(req.Questions))
		}
		q := req.Questions[0]
		if q.ID != "scope" || q.Header != "改动范围" || q.Prompt != "这次改哪些文件？" || !q.AllowsOther || q.AllowsMultiple || q.IsSecret {
			t.Errorf("question 0 mismatch: %+v", q)
		}
		if len(q.Choices) != 2 || q.Choices[0].Label != "只改协议层" || q.Choices[0].Detail != "只动 internal/providers/codex" {
			t.Errorf("question 0 choices mismatch: %+v", q.Choices)
		}
		if !req.Questions[1].IsSecret || len(req.Questions[1].Choices) != 0 {
			t.Errorf("question 1 mismatch: %+v", req.Questions[1])
		}

		var res struct {
			Answers map[string]userInputAnswer `json:"answers"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil {
			t.Fatalf("failed to unmarshal reply result: %v", err)
		}
		if got := res.Answers["scope"].Answers; !reflect.DeepEqual(got, []string{"只改协议层"}) {
			t.Errorf("scope answer mismatch: %+v", got)
		}
		if got := res.Answers["token"].Answers; !reflect.DeepEqual(got, []string{"s3cret"}) {
			t.Errorf("token answer mismatch: %+v", got)
		}

		// 应答写入后通过 AckDelivery 回报送达结果
		if reply.onAck == nil {
			t.Fatal("expected onAck to be set after interact")
		}
		reply.onAck(nil)
		if interactor.acks != 1 || interactor.ackErr != nil {
			t.Errorf("expected 1 ack without error, got acks=%d err=%v", interactor.acks, interactor.ackErr)
		}
	})

	t.Run("用户跳过时回空 answers", func(t *testing.T) {
		interactor := &mockInteractor{response: harness.ApprovalResponse{Decision: harness.DecisionDeny, Skipped: true}}
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent, Approver: interactor})

		reply, err := h.HandleLine(context.Background(), []byte(requestUserInputLine), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		var res struct {
			Answers map[string]userInputAnswer `json:"answers"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil {
			t.Fatalf("failed to unmarshal reply result: %v", err)
		}
		if res.Answers == nil || len(res.Answers) != 0 {
			t.Errorf("expected empty answers object, got %+v", res.Answers)
		}
	})

	t.Run("没有 Interactor 时回空 answers", func(t *testing.T) {
		approver := &mockApprover{decision: harness.DecisionAllow}
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent, Approver: approver})

		reply, err := h.HandleLine(context.Background(), []byte(requestUserInputLine), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		if len(approver.requests) != 0 {
			t.Errorf("approve should not be called for question request, got %d calls", len(approver.requests))
		}
		if reply.onAck != nil {
			t.Error("onAck should not be set without interactor")
		}
		var res struct {
			Answers map[string]userInputAnswer `json:"answers"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil {
			t.Fatalf("failed to unmarshal reply result: %v", err)
		}
		if res.Answers == nil || len(res.Answers) != 0 {
			t.Errorf("expected empty answers object, got %+v", res.Answers)
		}
	})

	t.Run("没有 Approver 时回空 answers", func(t *testing.T) {
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeReadOnly})

		reply, err := h.HandleLine(context.Background(), []byte(requestUserInputLine), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		if reply.onAck != nil {
			t.Error("onAck should not be set without approver")
		}
	})

	t.Run("交互出错时回空 answers 并回报失败", func(t *testing.T) {
		interactor := &mockInteractor{err: errors.New("用户界面已关闭")}
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent, Approver: interactor})

		reply, err := h.HandleLine(context.Background(), []byte(requestUserInputLine), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		if reply.onAck != nil {
			t.Error("onAck should not be set when interact fails")
		}
		if interactor.acks != 1 || interactor.ackErr == nil {
			t.Errorf("expected ack with error, got acks=%d err=%v", interactor.acks, interactor.ackErr)
		}
		var res struct {
			Answers map[string]userInputAnswer `json:"answers"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil {
			t.Fatalf("failed to unmarshal reply result: %v", err)
		}
		if res.Answers == nil || len(res.Answers) != 0 {
			t.Errorf("expected empty answers object, got %+v", res.Answers)
		}
	})
}

func TestMCPElicitationDeclined(t *testing.T) {
	// 本轮不接入 elicitation 交互，但必须回 decline 而不是 -32601，否则本轮直接失败。
	h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
	line := []byte(`{"jsonrpc":"2.0","id":501,"method":"mcpServer/elicitation/request","params":{"threadId":"th_1","turnId":"turn_1","serverName":"fs"}}`)

	reply, err := h.HandleLine(context.Background(), line, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reply == nil || reply.Error != nil {
		t.Fatalf("expected reply without error, got %+v", reply)
	}
	var res struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(reply.Result, &res); err != nil {
		t.Fatalf("failed to unmarshal reply result: %v", err)
	}
	if res.Action != "decline" {
		t.Errorf("expected decline action, got %q", res.Action)
	}
}

func TestApprovalDeclineResultShapes(t *testing.T) {
	t.Run("按方法返回拒绝形状", func(t *testing.T) {
		// 字符串枚举：commandExecution / fileChange 审批
		for _, method := range []string{"item/commandExecution/requestApproval", "item/fileChange/requestApproval"} {
			var res struct {
				Decision string `json:"decision"`
			}
			if err := json.Unmarshal(approvalDeclineResult(method), &res); err != nil {
				t.Fatalf("%s: failed to unmarshal: %v", method, err)
			}
			if res.Decision != "decline" {
				t.Errorf("%s: expected decline decision, got %q", method, res.Decision)
			}
		}

		// ReviewDecision：拒绝必须写成 {denied:{rejection}}
		for _, method := range []string{"applyPatchApproval", "execCommandApproval"} {
			var res struct {
				Decision struct {
					Denied struct {
						Rejection string `json:"rejection"`
					} `json:"denied"`
				} `json:"decision"`
			}
			if err := json.Unmarshal(approvalDeclineResult(method), &res); err != nil {
				t.Fatalf("%s: failed to unmarshal: %v", method, err)
			}
			if res.Decision.Denied.Rejection == "" {
				t.Errorf("%s: expected denied rejection, got %s", method, approvalDeclineResult(method))
			}
		}

		// 权限类：空权限档 + scope
		var perm struct {
			Permissions map[string]any `json:"permissions"`
			Scope       string         `json:"scope"`
		}
		if err := json.Unmarshal(approvalDeclineResult("item/permissions/requestApproval"), &perm); err != nil {
			t.Fatalf("permissions: failed to unmarshal: %v", err)
		}
		if perm.Permissions == nil || len(perm.Permissions) != 0 || perm.Scope != "turn" {
			t.Errorf("permissions decline mismatch: %+v", perm)
		}
	})

	// applyPatchApproval 没有 Approver 时按模式拒绝，应答仍须是 ReviewDecision 形状
	patchRequest := []byte(`{"jsonrpc":"2.0","id":601,"method":"applyPatchApproval","params":{"conversationId":"th_1","callId":"patch_1","grantRoot":"/tmp","reason":"patch file"}}`)

	t.Run("无 Approver 只读模式回 ReviewDecision 拒绝", func(t *testing.T) {
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeReadOnly})
		reply, err := h.HandleLine(context.Background(), patchRequest, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		var res struct {
			Decision struct {
				Denied struct {
					Rejection string `json:"rejection"`
				} `json:"denied"`
			} `json:"decision"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil {
			t.Fatalf("failed to unmarshal reply result: %v", err)
		}
		if res.Decision.Denied.Rejection == "" {
			t.Errorf("expected denied rejection, got %s", reply.Result)
		}
	})

	t.Run("Approver 拒绝 applyPatchApproval 回 ReviewDecision", func(t *testing.T) {
		approver := &mockApprover{decision: harness.DecisionDeny}
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent, Approver: approver})
		reply, err := h.HandleLine(context.Background(), patchRequest, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		var res struct {
			Decision struct {
				Denied struct {
					Rejection string `json:"rejection"`
				} `json:"denied"`
			} `json:"decision"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil {
			t.Fatalf("failed to unmarshal reply result: %v", err)
		}
		if res.Decision.Denied.Rejection == "" {
			t.Errorf("expected denied rejection, got %s", reply.Result)
		}
	})

	t.Run("权限请求不再回 -32601", func(t *testing.T) {
		h := NewProtocolHandler(harness.Turn{Mode: harness.ModeAgent})
		line := []byte(`{"jsonrpc":"2.0","id":602,"method":"item/permissions/requestApproval","params":{"threadId":"th_1","turnId":"turn_1","itemId":"call_2","permissions":{}}}`)
		reply, err := h.HandleLine(context.Background(), line, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reply == nil || reply.Error != nil {
			t.Fatalf("expected reply without error, got %+v", reply)
		}
		var res struct {
			Permissions map[string]any `json:"permissions"`
			Scope       string         `json:"scope"`
		}
		if err := json.Unmarshal(reply.Result, &res); err != nil {
			t.Fatalf("failed to unmarshal reply result: %v", err)
		}
		if res.Permissions == nil || res.Scope != "turn" {
			t.Errorf("permissions decline mismatch: %s", reply.Result)
		}
	})
}
