package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/testenv"
)

// codexRetryScript 生成假 app-server 脚本：前 failTimes 个进程按 mode 制造启动阶段失败，之后正常完成一轮。
// countPath 记录已启动的进程数（一次尝试一个进程），logPath 记录收到的全部请求。
// mode 取值：error=thread/start 返回 JSON-RPC 错误；exit=thread/start 应答后立刻退出（拿不到 turn/start 响应）；
// emit_then_exit=turn/start 应答并推一条文本后退出（不推 turn/completed）。
func codexRetryScript(mode string, failTimes int, countPath, logPath string) string {
	countJSON, _ := json.Marshal(countPath)
	logJSON, _ := json.Marshal(logPath)
	return fmt.Sprintf(`
import sys, json

mode = %q
fail_times = %d
count_path = %s
log_path = %s
thread_id = "th_mock_123"
turn_id = "turn_mock_abc"

try:
    with open(count_path) as f:
        attempt = int((f.read().strip() or "0"))
except FileNotFoundError:
    attempt = 0
attempt += 1
with open(count_path, "w") as f:
    f.write(str(attempt))
failing = attempt <= fail_times

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
    with open(log_path, 'a') as log:
        log.write(json.dumps(msg) + '\n')
    method = msg.get("method")
    mid = msg.get("id")
    if method == "initialize":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"capabilities":{}}}) + "\n")
        sys.stdout.flush()
    elif method == "thread/start" or method == "thread/resume":
        if failing and mode == "error":
            sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"error":{"code":-32000,"message":"boom"}}) + "\n")
            sys.stdout.flush()
        else:
            sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"thread":{"id":thread_id}}}) + "\n")
            sys.stdout.flush()
            if failing and mode == "exit":
                break
    elif method == "turn/start":
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","id":mid,"result":{"turn":{"id":turn_id}}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"OK"}}) + "\n")
        sys.stdout.flush()
        if failing and mode == "emit_then_exit":
            break
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
`, mode, failTimes, string(countJSON), string(logJSON))
}

// retryRun 用给定脚本跑一轮，返回 Run 的错误与向上发出的事件。
func retryRun(t *testing.T, script string, backoffs []time.Duration, retrySleep func(ctx context.Context, d time.Duration) error) (error, []harness.Event) {
	t.Helper()
	testenv.TempHome(t)

	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-u", "-c", script)
	}}

	h := New(Options{})
	defer h.Close()
	h.retryBackoffs = backoffs
	h.retrySleep = retrySleep

	var events []harness.Event
	turn := harness.Turn{
		WorkDir: t.TempDir(),
		Mode:    harness.ModeAgent,
		Prompt:  "重试测试",
		Sandbox: sb,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := h.Run(ctx, turn, func(e harness.Event) {
		events = append(events, e)
	})
	return err, events
}

// readAttempts 读出脚本记录的尝试次数。
func readAttempts(t *testing.T, countPath string) int {
	t.Helper()
	data, err := os.ReadFile(countPath)
	if err != nil {
		t.Fatalf("read attempts file: %v", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse attempts %q: %v", string(data), err)
	}
	return n
}

func TestRetryConstantsMatchBrief(t *testing.T) {
	// 首次尝试不退避，之后 1s、2s：最多 2 次重试（共 3 次尝试）
	if codexRunMaxAttempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", codexRunMaxAttempts)
	}
	want := []time.Duration{time.Second, 2 * time.Second}
	if len(defaultRetryBackoffs) != len(want) {
		t.Fatalf("expected %d backoffs, got %d", len(want), len(defaultRetryBackoffs))
	}
	for i, d := range want {
		if defaultRetryBackoffs[i] != d {
			t.Fatalf("backoff %d: expected %v, got %v", i, d, defaultRetryBackoffs[i])
		}
	}
}

func TestRunRetriesThreadStartError(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "attempts")
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	script := codexRetryScript("error", 2, countPath, logPath)

	var sleeps []time.Duration
	err, events := retryRun(t, script, []time.Duration{5 * time.Millisecond, 7 * time.Millisecond}, func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("Run should succeed after retries, got %v", err)
	}

	if got := readAttempts(t, countPath); got != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", got)
	}
	if len(sleeps) != 2 || sleeps[0] != 5*time.Millisecond || sleeps[1] != 7*time.Millisecond {
		t.Fatalf("unexpected backoff sequence: %v", sleeps)
	}

	var texts []string
	for _, ev := range events {
		if ev.Type == harness.EventText {
			texts = append(texts, ev.Text)
		}
	}
	if strings.Join(texts, "") != "OK" {
		t.Fatalf("expected streamed text after retry, got %v", texts)
	}
}

func TestRunGivesUpAfterMaxAttempts(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "attempts")
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	script := codexRetryScript("error", 99, countPath, logPath)

	var sleeps []time.Duration
	err, _ := retryRun(t, script, nil, func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("expected Run to fail after exhausting retries")
	}
	if !strings.Contains(err.Error(), "thread/start failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := readAttempts(t, countPath); got != codexRunMaxAttempts {
		t.Fatalf("expected %d attempts, got %d", codexRunMaxAttempts, got)
	}
	// 默认退避序列：首次尝试不退避，两次重试依次等待 1s、2s
	if len(sleeps) != 2 || sleeps[0] != time.Second || sleeps[1] != 2*time.Second {
		t.Fatalf("unexpected backoff sequence: %v", sleeps)
	}
}

func TestRunRetriesWhenProcessExitsBeforeEvents(t *testing.T) {
	// 第一个进程应答 thread/start 后立刻退出，turn/start 拿不到响应；本轮尚未向上发出内容事件，应重建重试。
	countPath := filepath.Join(t.TempDir(), "attempts")
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	script := codexRetryScript("exit", 1, countPath, logPath)

	err, _ := retryRun(t, script, []time.Duration{0, 0}, func(ctx context.Context, d time.Duration) error {
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("Run should succeed after rebuilding the process, got %v", err)
	}
	if got := readAttempts(t, countPath); got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}

func TestRunDoesNotRetryAfterContentEvents(t *testing.T) {
	// 已经向上发出过文本，进程随后退出：不再重试，按现有逻辑报错并丢弃进程。
	countPath := filepath.Join(t.TempDir(), "attempts")
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	script := codexRetryScript("emit_then_exit", 1, countPath, logPath)

	err, events := retryRun(t, script, []time.Duration{0, 0}, func(ctx context.Context, d time.Duration) error {
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("expected Run to fail when the turn never completes")
	}
	if got := readAttempts(t, countPath); got != 1 {
		t.Fatalf("expected no retry after content events, got %d attempts", got)
	}

	var sawText, sawError bool
	for _, ev := range events {
		switch ev.Type {
		case harness.EventText:
			if ev.Text == "OK" {
				sawText = true
			}
		case harness.EventError:
			sawError = true
		}
	}
	if !sawText {
		t.Fatalf("expected streamed text before the process died: %+v", events)
	}
	if !sawError {
		t.Fatalf("expected an error event for the incomplete turn: %+v", events)
	}
}

func TestWaitRetryStopsOnContextCancel(t *testing.T) {
	h := New(Options{})
	defer h.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	if err := h.waitRetry(ctx, 1); err == nil {
		t.Fatal("expected waitRetry to fail on a cancelled context")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("waitRetry should return promptly on cancel, took %v", elapsed)
	}
}

func TestRunRetryAbortsWhenContextCancelled(t *testing.T) {
	countPath := filepath.Join(t.TempDir(), "attempts")
	logPath := filepath.Join(t.TempDir(), "requests.jsonl")
	script := codexRetryScript("error", 99, countPath, logPath)

	sb := &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-u", "-c", script)
	}}

	h := New(Options{})
	defer h.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 退避阶段取消 ctx：Run 应立刻带着上一次的错误返回，不再发起新尝试。
	h.retryBackoffs = []time.Duration{time.Second}
	h.retrySleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return ctx.Err()
	}

	turn := harness.Turn{WorkDir: t.TempDir(), Mode: harness.ModeAgent, Prompt: "取消", Sandbox: sb}
	err := h.Run(ctx, turn, nil)
	if err == nil {
		t.Fatal("expected Run to fail after the context was cancelled during backoff")
	}
	if got := readAttempts(t, countPath); got != 1 {
		t.Fatalf("expected a single attempt before the cancel, got %d", got)
	}
}

// codexAcceptedScript 生成假 app-server：turn/start 应答后依次推 turn/started、一条文本和 turn/completed。
// turn/started 让读取协程先上报送达信号，从而能稳定断言 accepted 排在正文之前。
func codexAcceptedScript() string {
	return `
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
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/started","params":{"threadId":thread_id,"turn":{"id":turn_id}}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"OK"}}) + "\n")
        sys.stdout.flush()
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
`
}

// TestRunEmitsTurnAcceptedOnce 验证 turn/start 成功、turn/started 到达后恰好上报一次
// EventTurnAccepted，且早于第一条正文事件。
func TestRunEmitsTurnAcceptedOnce(t *testing.T) {
	err, events := retryRun(t, codexAcceptedScript(), []time.Duration{0, 0}, func(ctx context.Context, d time.Duration) error {
		return ctx.Err()
	})
	if err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}

	accepted, firstContent, count := -1, -1, 0
	for i, event := range events {
		switch event.Type {
		case harness.EventTurnAccepted:
			count++
			if accepted < 0 {
				accepted = i
			}
		case harness.EventText, harness.EventReasoning, harness.EventToolStart:
			if firstContent < 0 {
				firstContent = i
			}
		}
	}
	if count != 1 {
		t.Fatalf("EventTurnAccepted 应恰好出现一次，实际 %d 次: %+v", count, events)
	}
	if firstContent >= 0 && accepted > firstContent {
		t.Fatalf("EventTurnAccepted 应早于第一条正文事件: %+v", events)
	}
}
