package codex

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
)

const steerAppServerScript = `
import sys, json

thread_id = "th_mock_123"
turn_id = "turn_mock_abc"

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
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}) + "\n")
        sys.stdout.flush()
    elif method == "turn/steer":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
`

func TestCodexHarnessSteer(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logPath := filepath.Join(t.TempDir(), "steer_requests.jsonl")
	pathJSON, _ := json.Marshal(logPath)
	script := strings.Replace(steerAppServerScript, `    method = msg.get("method")`,
		"    with open("+string(pathJSON)+", 'a') as log:\n        log.write(json.dumps(msg) + '\\n')\n"+
			`    method = msg.get("method")`, 1)

	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-u", "-c", script)
	}}

	h := New(Options{})
	defer h.Close()

	workDir := t.TempDir()
	sessionID := "th_mock_123"

	// 1. 测试未运行时调用 Steer 返回 harness.ErrNotRunning
	if err := h.Steer(context.Background(), sessionID, "steer text"); err != harness.ErrNotRunning {
		t.Fatalf("expected ErrNotRunning when no session is running, got %v", err)
	}

	// 2. 启动运行中的轮次
	turnCtx, cancelTurn := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelTurn()

	runErrCh := make(chan error, 1)
	go func() {
		turn := harness.Turn{
			WorkDir:   workDir,
			Mode:      harness.ModeAgent,
			Prompt:    "initial prompt",
			Sandbox:   sb,
			SessionID: sessionID,
		}
		runErrCh <- h.Run(turnCtx, turn, nil)
	}()

	// 轮询等待 activeTurn 就绪
	var steerErr error
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		steerErr = h.Steer(context.Background(), sessionID, "in-flight steering instruction")
		if steerErr == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if steerErr != nil {
		t.Fatalf("h.Steer failed: %v", steerErr)
	}

	// 等待 Run 结束
	select {
	case err := <-runErrCh:
		if err != nil {
			t.Fatalf("h.Run failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for turn to complete after steer")
	}

	// 3. 校验写入日志中的方法名与参数序列化
	var foundSteer bool
	for _, line := range readLines(t, logPath) {
		var req struct {
			Method string          `json:"method"`
			Params TurnSteerParams `json:"params"`
		}
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		if req.Method == "turn/steer" {
			foundSteer = true
			if req.Params.ThreadID != sessionID {
				t.Errorf("expected threadId %q, got %q", sessionID, req.Params.ThreadID)
			}
			if req.Params.ExpectedTurnID != "turn_mock_abc" {
				t.Errorf("expected expectedTurnId 'turn_mock_abc', got %q", req.Params.ExpectedTurnID)
			}
			if len(req.Params.Input) != 1 || req.Params.Input[0].Type != "text" || req.Params.Input[0].Text != "in-flight steering instruction" {
				t.Errorf("input serialization mismatch: %+v", req.Params.Input)
			}
		}
	}

	if !foundSteer {
		t.Fatal("turn/steer request not found in app-server request log")
	}
}
