package acp

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode"

	"mcode/internal/harness"
	"mcode/internal/providers/loginenv"
	_ "modernc.org/sqlite"
)

const fakeACPEnv = "MCODE_TEST_FAKE_ACP"
const fakeHoldSession = "12345678-1234-1234-1234-123456789abc"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeACPEnv); mode != "" {
		runFakeACP(mode)
		return
	}
	// 测试环境使用假 Agent，无需真实启动间隔，避免用例间相互等待。
	launchSpacing = 0
	os.Exit(m.Run())
}

// runFakeACP 实现模拟的 ACP CLI 子进程。
func runFakeACP(mode string) {
	if strings.HasPrefix(mode, "feature_") {
		runFakeACPFeatures(mode)
		return
	}
	reader := bufio.NewReader(os.Stdin)

	readMsg := func() (map[string]any, error) {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var msg map[string]any
		if err := json.Unmarshal(line, &msg); err != nil {
			return nil, err
		}
		return msg, nil
	}

	writeMsg := func(v any) {
		data, _ := json.Marshal(v)
		data = append(data, '\n')
		_, _ = os.Stdout.Write(data)
	}

	for {
		msg, err := readMsg()
		if err != nil {
			break
		}

		method, _ := msg["method"].(string)
		id := msg["id"]

		switch method {
		case "initialize":
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentCapabilities": map[string]any{
						"loadSession": mode != "no_load",
					},
					"authMethods": []any{},
				},
			})

		case "session/new":
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"sessionId": "fake-session-001",
				},
			})

		case "session/load":
			loaded := "fake-session-001"
			if params, ok := msg["params"].(map[string]any); ok {
				if sid, ok := params["sessionId"].(string); ok && sid != "" {
					loaded = sid
				}
			}
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"sessionId": loaded,
				},
			})

		case "session/prompt":
			if mode == "hang_until_cancel" {
				// 挂起不回复，等待被取消或者接收 cancel 通知
				for {
					next, err := readMsg()
					if err != nil {
						os.Exit(0)
					}
					if next["method"] == "session/cancel" {
						time.Sleep(100 * time.Millisecond)
						os.Exit(0)
					}
				}
			}

			// 1. 发送 text chunk 通知
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content": map[string]any{
							"text": "Hello from fake ACP!",
						},
					},
				},
			})

			// 2. 发送 reasoning/thought chunk 通知
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "agent_thought_chunk",
						"content": map[string]any{
							"text": "Thinking about plan...",
						},
					},
				},
			})

			// 3. 发送 tool_call 通知
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "call-999",
						"name":          "bash",
						"rawInput": map[string]any{
							"command": "echo test",
						},
					},
				},
			})

			// 4. ACP agent 主动向 client 发送权限审批请求
			reqID := 10001
			options := []any{
				map[string]any{"optionId": "opt_allow", "name": "Allow this action", "kind": "allow_once"},
				map[string]any{"optionId": "opt_reject", "name": "Reject this action", "kind": "reject_once"},
			}
			if mode == "reject_once_priority" {
				options = []any{
					map[string]any{"optionId": "opt_allow", "kind": "allow_once"},
					map[string]any{"optionId": "opt_always", "name": "Reject always", "kind": "reject_always"},
					map[string]any{"optionId": "opt_once", "kind": "reject_once"},
				}
			} else if mode == "reject_always_only" {
				options = []any{map[string]any{"optionId": "opt_always", "kind": "reject_always"}}
			}
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"id":      reqID,
				"method":  "session/request_permission",
				"params": map[string]any{
					"toolCall": map[string]any{
						"toolCallId": "call-999",
						"name":       "bash",
					},
					"options": options,
				},
			})

			// 等待 client 回复审批结果
			reply, err := readMsg()
			if err != nil {
				os.Exit(1)
			}
			// 检查 reply 是否对应 reqID
			if fmt.Sprint(reply["id"]) != fmt.Sprint(reqID) {
				_, _ = fmt.Fprintf(os.Stderr, "unexpected approval response id: %v\n", reply["id"])
				os.Exit(2)
			}
			if mode == "reject_once_priority" || mode == "reject_always_only" {
				result, _ := reply["result"].(map[string]any)
				outcome, _ := result["outcome"].(map[string]any)
				want := "opt_once"
				if mode == "reject_always_only" {
					want = "opt_always"
				}
				if outcome["optionId"] != want {
					writeMsg(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32603, "message": fmt.Sprintf("selected %v, want %s", outcome["optionId"], want)}})
					return
				}
			}

			// 5. 发送 tool_call_update 通知
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"update": map[string]any{
						"sessionUpdate": "tool_call_update",
						"toolCallId":    "call-999",
						"status":        "completed",
						"output":        "test output\n",
					},
				},
			})

			// 6. 回复 prompt
			writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"result": map[string]any{
					"stopReason": "end_turn",
				},
			})
			if mode == "exit_after_prompt" {
				// 回复后立即退出：模拟一次性 Agent（响应与连接关闭几乎同时发生）
				os.Exit(0)
			}
			return
		}
	}
}

type mockApprover struct {
	called   bool
	decision harness.Decision
	mu       sync.Mutex
}

func (m *mockApprover) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.called = true
	return m.decision, nil
}

func TestACPHarnessRun(t *testing.T) {
	fakeBin := os.Args[0]

	h := New(Options{
		Agent:  AgentCline,
		Binary: fakeBin,
		Env:    []string{fakeACPEnv + "=normal"},
	})

	if h.Kind() != harness.Kind("cline") {
		t.Fatalf("expected Kind cline, got %s", h.Kind())
	}

	approver := &mockApprover{decision: harness.DecisionAllow}

	var events []harness.Event
	var evMu sync.Mutex
	emit := func(e harness.Event) {
		evMu.Lock()
		defer evMu.Unlock()
		events = append(events, e)
	}

	tmpDir := t.TempDir()

	turn := harness.Turn{
		ID:       "turn-1",
		Prompt:   "hello acp",
		WorkDir:  tmpDir,
		Mode:     harness.ModeSupervised,
		Approver: approver,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := h.Run(ctx, turn, emit)
	if err != nil {
		t.Fatalf("Harness.Run failed: %v", err)
	}

	approver.mu.Lock()
	if !approver.called {
		t.Error("expected approver to be called during session/request_permission")
	}
	approver.mu.Unlock()

	evMu.Lock()
	defer evMu.Unlock()

	var hasSession, hasText, hasReasoning, hasToolStart, hasToolEnd, hasDone bool
	for _, e := range events {
		switch e.Type {
		case harness.EventSession:
			if e.SessionID == "fake-session-001" {
				hasSession = true
			}
		case harness.EventText:
			if strings.Contains(e.Text, "Hello from fake ACP!") {
				hasText = true
			}
		case harness.EventReasoning:
			if strings.Contains(e.Text, "Thinking about plan...") {
				hasReasoning = true
			}
		case harness.EventToolStart:
			if e.ToolID == "call-999" && e.ToolName == "bash" {
				hasToolStart = true
			}
		case harness.EventToolEnd:
			if e.ToolID == "call-999" && !e.IsError && strings.Contains(e.Output, "test output") {
				hasToolEnd = true
			}
		case harness.EventDone:
			if e.StopReason == "end_turn" {
				hasDone = true
			}
		}
	}

	if !hasSession {
		t.Error("missing EventSession")
	}
	if !hasText {
		t.Error("missing EventText")
	}
	if !hasReasoning {
		t.Error("missing EventReasoning")
	}
	if !hasToolStart {
		t.Error("missing EventToolStart")
	}
	if !hasToolEnd {
		t.Error("missing EventToolEnd")
	}
	if !hasDone {
		t.Error("missing EventDone")
	}
}

func TestACPHarnessCancellation(t *testing.T) {
	fakeBin := os.Args[0]

	h := New(Options{
		Agent:  AgentCline,
		Binary: fakeBin,
		Env:    []string{fakeACPEnv + "=hang_until_cancel"},
	})

	ctx, cancel := context.WithCancel(context.Background())

	turn := harness.Turn{
		ID:      "turn-cancel",
		Prompt:  "hang prompt",
		WorkDir: t.TempDir(),
		Mode:    harness.ModeSupervised,
	}

	go func() {
		// 延迟取消 context
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	err := h.Run(ctx, turn, func(e harness.Event) {})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error on context cancellation, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context canceled error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("Harness.Run took %v to terminate, expected within 5s", elapsed)
	}
}

func TestACPHarnessAvailable(t *testing.T) {
	// 登录环境必须先加载完成：未完成时 Available 对“找不到的程序”只能给出临时的乐观结论。
	_ = loginenv.PATH()

	// 针对已知在 PATH 中的系统命令（如 sh）
	h := New(Options{
		Agent:  AgentCline,
		Binary: "sh",
	})
	avail, reason := h.Available()
	if !avail {
		t.Fatalf("expected sh to be available, got false with reason: %s", reason)
	}

	// 针对不存在的命令
	hMissing := New(Options{
		Agent:  AgentCursor,
		Binary: "non_existent_binary_xyz_999",
	})
	avail2, reason2 := hMissing.Available()
	if avail2 {
		t.Fatal("expected non_existent binary to be unavailable")
	}
	if !strings.Contains(reason2, "was not found in PATH") {
		t.Fatalf("unexpected unavailable reason: %s", reason2)
	}
}

// runFakeTurn 以假 Agent 执行一轮对话并返回收集到的事件。
func TestACPRejectOptionPriority(t *testing.T) {
	for _, mode := range []string{"reject_once_priority", "reject_always_only"} {
		t.Run(mode, func(t *testing.T) {
			_, err := runFakeTurn(t, mode, harness.Turn{WorkDir: t.TempDir(), Approver: &mockApprover{decision: harness.DecisionDeny}})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func runFakeTurn(t *testing.T, mode string, turn harness.Turn) ([]harness.Event, error) {
	t.Helper()
	h := New(Options{
		Agent:  AgentCline,
		Binary: os.Args[0],
		Env:    []string{fakeACPEnv + "=" + mode},
	})

	var (
		events []harness.Event
		evMu   sync.Mutex
	)
	emit := func(e harness.Event) {
		evMu.Lock()
		events = append(events, e)
		evMu.Unlock()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := h.Run(ctx, turn, emit)

	evMu.Lock()
	defer evMu.Unlock()
	cp := make([]harness.Event, len(events))
	copy(cp, events)
	return cp, err
}

func sessionIDOf(t *testing.T, events []harness.Event) string {
	t.Helper()
	for _, e := range events {
		if e.Type == harness.EventSession && e.SessionID != "" {
			return e.SessionID
		}
	}
	t.Fatal("no EventSession with a session ID in events")
	return ""
}

// TestACPHarnessResumeSupported 验证 loadSession=true 时第二轮以 session/load 恢复同一会话。
func TestACPHarnessResumeSupported(t *testing.T) {
	turn := harness.Turn{
		ID:      "turn-1",
		Prompt:  "hello acp",
		WorkDir: t.TempDir(),
		Mode:    harness.ModeSupervised,
	}
	events, err := runFakeTurn(t, "normal", turn)
	if err != nil {
		t.Fatalf("first turn failed: %v", err)
	}
	sid := sessionIDOf(t, events)

	turn2 := turn
	turn2.ID = "turn-2"
	turn2.SessionID = sid
	events2, err := runFakeTurn(t, "normal", turn2)
	if err != nil {
		t.Fatalf("resume turn failed: %v", err)
	}
	if got := sessionIDOf(t, events2); got != sid {
		t.Fatalf("expected resumed session ID %q, got %q", sid, got)
	}
}

// TestACPHarnessResumeUnsupported 验证 Agent 不支持 session/load 时明确报错，而非静默新建会话。
func TestACPHarnessResumeUnsupported(t *testing.T) {
	turn := harness.Turn{
		ID:      "turn-1",
		Prompt:  "hello acp",
		WorkDir: t.TempDir(),
		Mode:    harness.ModeSupervised,
	}
	if _, err := runFakeTurn(t, "no_load", turn); err != nil {
		t.Fatalf("first turn failed: %v", err)
	}

	turn2 := turn
	turn2.ID = "turn-2"
	turn2.SessionID = "fake-session-001"
	_, err := runFakeTurn(t, "no_load", turn2)
	if err == nil {
		t.Fatal("expected resume on loadSession=false agent to fail")
	}
	if !strings.Contains(err.Error(), "cannot resume") {
		t.Fatalf("expected 'cannot resume' error, got: %v", err)
	}
}

// TestACPHarnessResponseBeforeExit 验证 Agent 回复后立即退出时，prompt 响应不会因连接关闭而丢失。
func TestACPHarnessResponseBeforeExit(t *testing.T) {
	turn := harness.Turn{
		ID:      "turn-exit",
		Prompt:  "hello acp",
		WorkDir: t.TempDir(),
		Mode:    harness.ModeSupervised,
	}
	events, err := runFakeTurn(t, "exit_after_prompt", turn)
	if err != nil {
		t.Fatalf("run failed although the agent answered before exiting: %v", err)
	}
	var sawDone bool
	for _, e := range events {
		if e.Type == harness.EventDone && e.StopReason == "end_turn" {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("missing EventDone after agent exited right after its reply")
	}
}

func TestWaitLaunchSlotSpacingAndCancel(t *testing.T) {
	old := launchSpacing
	launchSpacing = 40 * time.Millisecond
	defer func() { launchSpacing = old }()

	launchGate.mu.Lock()
	launchGate.next = time.Time{}
	launchGate.mu.Unlock()

	start := time.Now()
	if err := waitLaunchSlot(context.Background()); err != nil {
		t.Fatalf("first slot failed: %v", err)
	}
	if err := waitLaunchSlot(context.Background()); err != nil {
		t.Fatalf("second slot failed: %v", err)
	}
	if elapsed := time.Since(start); elapsed < launchSpacing {
		t.Fatalf("expected two launches spaced by at least %v, got %v", launchSpacing, elapsed)
	}

	// ctx 取消时等待中的槽位立即返回
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitLaunchSlot(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestResolveWorkDirPath(t *testing.T) {
	workDir := t.TempDir()
	absoluteOutside := "/etc/passwd"
	if runtime.GOOS == "windows" {
		absoluteOutside = filepath.VolumeName(workDir) + `\Windows\win.ini`
	}

	tests := []struct {
		name     string
		path     string
		wantRel  string
		wantFail bool
	}{
		{name: "relative file", path: "sub/file.txt", wantRel: filepath.Join("sub", "file.txt")},
		{name: "dot dot prefix name allowed", path: "..foo.txt", wantRel: "..foo.txt"},
		{name: "cleaned inside", path: "sub/../other.txt", wantRel: "other.txt"},
		{name: "escape parent", path: "../secret.txt", wantFail: true},
		{name: "escape deep", path: "sub/../../secret.txt", wantFail: true},
		{name: "absolute outside", path: absoluteOutside, wantFail: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			abs, rel, err := resolveWorkDirPath(workDir, tc.path)
			if tc.wantFail {
				if err == nil {
					t.Fatalf("expected rejection for %q, got abs=%q rel=%q", tc.path, abs, rel)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.path, err)
			}
			if rel != tc.wantRel {
				t.Fatalf("expected rel %q, got %q", tc.wantRel, rel)
			}
		})
	}
}

func TestResolveWorkDirPathRejectsSymlinkEscape(t *testing.T) {
	workDir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(workDir, "link")); err != nil {
		t.Fatalf("create dir symlink: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(workDir, "file-link.txt")); err != nil {
		t.Fatalf("create file symlink: %v", err)
	}

	for _, path := range []string{"link/secret.txt", "file-link.txt", "link/new.txt"} {
		if _, _, err := resolveWorkDirPath(workDir, path); err == nil {
			t.Fatalf("expected rejection for symlinked escape %q", path)
		}
	}

	// 指向工作目录内部的符号链接仍可用，且返回解析后的真实路径。
	if err := os.WriteFile(filepath.Join(workDir, "inside.txt"), []byte("ok"), 0644); err != nil {
		t.Fatalf("seed inside file: %v", err)
	}
	if err := os.Symlink(filepath.Join(workDir, "inside.txt"), filepath.Join(workDir, "inside-link.txt")); err != nil {
		t.Fatalf("create inside symlink: %v", err)
	}
	target, rel, err := resolveWorkDirPath(workDir, "inside-link.txt")
	if err != nil {
		t.Fatalf("unexpected rejection for inside symlink: %v", err)
	}
	if rel != "inside.txt" {
		t.Fatalf("expected rel %q, got %q", "inside.txt", rel)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "ok" {
		t.Fatalf("read resolved target %q: content=%q err=%v", target, content, err)
	}
}

func TestResolveWorkDirPathAcceptsRealPathOfSymlinkedWorkDir(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(base, "link-workdir")
	if err := os.Symlink(realDir, workDir); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(realDir, "escape-link")); err != nil {
		t.Fatal(err)
	}

	// 1. 以真实路径开头的绝对路径能通过
	realFile := filepath.Join(realDir, "sub", "a.go")
	abs, rel, err := resolveWorkDirPath(workDir, realFile)
	if err != nil {
		t.Fatalf("real path rejected: %v", err)
	}
	if abs != realFile || rel != filepath.Join("sub", "a.go") {
		t.Fatalf("real path got abs=%q rel=%q, want abs=%q", abs, rel, realFile)
	}

	// 2. 以软链路径开头的绝对路径仍能通过
	symFile := filepath.Join(workDir, "sub", "b.go")
	abs, rel, err = resolveWorkDirPath(workDir, symFile)
	if err != nil {
		t.Fatalf("symlink path rejected: %v", err)
	}
	wantAbs := filepath.Join(realDir, "sub", "b.go")
	if abs != wantAbs || rel != filepath.Join("sub", "b.go") {
		t.Fatalf("symlink path got abs=%q rel=%q, want abs=%q", abs, rel, wantAbs)
	}

	// 3. 工作目录内指向外部目录的软链仍然被拒绝
	for _, escape := range []string{
		filepath.Join(realDir, "escape-link", "secret.txt"),
		filepath.Join(workDir, "escape-link", "secret.txt"),
		filepath.Join("escape-link", "secret.txt"),
	} {
		if _, _, err := resolveWorkDirPath(workDir, escape); err == nil {
			t.Fatalf("expected rejection for escape symlink %q", escape)
		}
	}
}

func TestAgySessionCwdUsesRealPath(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(base, "real")
	if err := os.MkdirAll(realDir, 0755); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(base, "link-workdir")
	if err := os.Symlink(realDir, workDir); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		agent   Agent
		wantCwd string
	}{
		{name: "agy uses real path", agent: AgentAgy, wantCwd: realDir},
		{name: "non-agy keeps original path", agent: AgentOpenCode, wantCwd: workDir},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages, _, err := runACPFeature(t, "feature_new", tc.agent, harness.Turn{WorkDir: workDir})
			if err != nil {
				t.Fatal(err)
			}
			params := featureParams(messages, "session/new")
			if len(params) != 1 || params[0]["cwd"] != tc.wantCwd {
				t.Fatalf("session/new params = %v, want cwd=%q", params, tc.wantCwd)
			}
		})
	}
}

func runFakeACPFeatures(mode string) {
	reader := bufio.NewReader(os.Stdin)
	trace, _ := os.OpenFile(os.Getenv("MCODE_TEST_ACP_TRACE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	defer trace.Close()
	read := func() map[string]any {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return nil
		}
		_, _ = trace.Write(line)
		var msg map[string]any
		_ = json.Unmarshal(line, &msg)
		return msg
	}
	var writeMu sync.Mutex
	write := func(v any) {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = json.NewEncoder(os.Stdout).Encode(v)
	}
	if strings.HasPrefix(mode, "feature_hold") {
		go runFakeACPBackground(mode, write)
	}
	promptCount := 0
	sessionWorkDir := ""
	_ = json.NewEncoder(trace).Encode(map[string]any{"args": os.Args[1:]})
	options := []map[string]any{
		{"id": "model", "category": "model", "currentValue": "old"},
		{"id": "thought_level", "category": "model_config", "currentValue": "low", "options": []any{map[string]any{"value": "low"}, map[string]any{"value": "high"}}},
	}
	if strings.HasPrefix(mode, "feature_hold") {
		options = append(options, map[string]any{"id": "fast", "category": "model_config", "currentValue": "false"})
	}
	for {
		msg := read()
		if msg == nil {
			return
		}
		params, _ := msg["params"].(map[string]any)
		result := map[string]any{}
		switch msg["method"] {
		case "initialize":
			caps := map[string]any{"loadSession": mode == "feature_load"}
			sessionCaps := map[string]any{"close": map[string]any{}}
			if mode != "feature_load" {
				sessionCaps["resume"] = map[string]any{}
			}
			caps["sessionCapabilities"] = sessionCaps
			result["agentCapabilities"] = caps
			switch mode {
			case "feature_agy_auth", "feature_agy_auth_error", "feature_agy_cancel":
				result["authMethods"] = []map[string]any{{"id": "other"}, {"id": "oauth-personal"}}
			case "feature_agy_no_personal_auth":
				result["authMethods"] = []map[string]any{{"id": "other"}}
			}
		case "authenticate":
			if mode == "feature_agy_auth_error" {
				write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "error": map[string]any{"code": -32603, "message": "authentication failed"}})
				continue
			}
		case "session/new", "session/load", "session/resume":
			if cwd, ok := params["cwd"].(string); ok && cwd != "" {
				sessionWorkDir = cwd
			}
			if mode == "feature_resume_missing" && msg["method"] == "session/resume" {
				write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "error": map[string]any{"code": -32603, "message": "Failed to read session X; retry, or load it with session/load"}})
				continue
			}
			result["sessionId"] = "feature-session"
			if strings.HasPrefix(mode, "feature_hold") {
				result["sessionId"] = fakeHoldSession
			}
			result["configOptions"] = options
			current := "default"
			switch mode {
			case "feature_agy_current_auto", "feature_agy_no_plan":
				current = "auto"
			case "feature_agy_current_plan":
				current = "plan"
			}
			available := []any{map[string]any{"id": "default"}, map[string]any{"id": "plan"}, map[string]any{"id": "accept-edits"}, map[string]any{"id": "auto"}}
			if mode == "feature_agy_unadvertised" {
				available = nil
			} else if mode == "feature_agy_no_plan" {
				available = []any{map[string]any{"id": "default"}, map[string]any{"id": "auto"}}
			}
			result["modes"] = map[string]any{"currentModeId": current, "availableModes": available}
		case "session/set_config_option":
			for _, option := range options {
				if option["id"] == params["configId"] && !(mode == "feature_agy_model_mismatch" && option["id"] == "model") && !(mode == "feature_agy_effort_mismatch" && option["id"] == "thought_level") {
					option["currentValue"] = params["value"]
				}
			}
			result["configOptions"] = options
		case "session/prompt":
			promptCount++
			if promptCount == 1 && mode == "feature_agy_cancel" {
				write(map[string]any{"jsonrpc": "2.0", "id": 9010, "method": "session/request_permission", "params": map[string]any{
					"toolCall": map[string]any{"toolCallId": "cancel-test", "name": "run_command"},
					"options":  []map[string]any{{"optionId": "allow", "kind": "allow_once"}},
				}})
				var cancelled, permissionReplied bool
				for !cancelled || !permissionReplied {
					reply := read()
					if reply == nil {
						return
					}
					cancelled = cancelled || reply["method"] == "session/cancel"
					permissionReplied = permissionReplied || reply["id"] == float64(9010)
				}
				write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": map[string]any{"stopReason": "cancelled"}})
				continue
			}
			if mode == "feature_agy_stall_error" || mode == "feature_agy_stall_ok" {
				dbPath := agySessionDB(agyGeminiDir(os.Args[1:], os.Environ()), "feature-session")
				if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
					panic(err)
				}
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					panic(err)
				}
				if _, err := db.Exec("CREATE TABLE IF NOT EXISTS steps (idx INTEGER, step_type INTEGER, step_payload BLOB)"); err != nil {
					panic(err)
				}
				kind := 1
				if mode == "feature_agy_stall_error" {
					kind = 17
				}
				if _, err := db.Exec("INSERT INTO steps VALUES (2, ?, ?)", kind, []byte("Error 502, Message: Bad Gateway: Failed to connect to backend API, Status: 502 Bad Gateway, Details: [] ... The model is currently unreachable.")); err != nil {
					panic(err)
				}
				if err := db.Close(); err != nil {
					panic(err)
				}
				write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "pending"}}}})
				for read() != nil {
				}
				return
			}
			if mode == "feature_agy_cancel_timeout" || mode == "feature_agy_cancel_exit" {
				if mode == "feature_agy_cancel_timeout" {
					signal.Ignore(syscall.SIGTERM)
				}
				write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "pending"}}}})
				for incoming := read(); incoming != nil; incoming = read() {
					if mode == "feature_agy_cancel_exit" && incoming["method"] == "session/cancel" {
						return
					}
				}
				time.Sleep(30 * time.Second)
				return
			}
			if mode == "feature_plan" {
				entries := []map[string]any{
					{"content": "Draft", "status": "completed", "priority": "high"},
					{"content": "Check", "status": "in_progress", "priority": "medium"},
					{"content": "Send", "status": "pending", "priority": "low"},
				}
				for _, status := range []string{"in_progress", "completed"} {
					entries[1]["status"] = status
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "plan", "entries": entries}}})
				}
			}
			if strings.HasPrefix(mode, "feature_hold") {
				items, _ := params["prompt"].([]any)
				item, _ := items[0].(map[string]any)
				if promptCount == 1 && item["text"] != "后台任务已结束，请根据结果继续。" {
					notify := func(update map[string]any) {
						write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": update}})
					}
					descriptionMatch := mode == "feature_hold_task_description"
					editEcho := mode == "feature_hold_edit_echo"
					records := descriptionMatch || mode == "feature_hold_agy_records" || mode == "feature_hold_spontaneous" || mode == "feature_hold_late_spontaneous" || mode == "feature_hold_final_step"
					recordsDir := fakeACPHoldRecords("antigravity-cli")
					if editEcho {
						// 编辑工具回显的文件内容里出现后台任务标记，不能当成真实后台任务。
						notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "edit-echo", "name": "edit_file", "rawInput": map[string]any{"path": "echo.go"}})
						echo := fmt.Sprintf("package acp\n\n// Tool is running as a background task with task id: %s/task-1\n", fakeHoldSession)
						notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit-echo", "status": "completed", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": echo}}}})
					}
					if descriptionMatch {
						notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "edit", "name": "edit_file"})
						for index, command := range []string{"go build ./... && go vet ./...", "go build ./..."} {
							toolID := fmt.Sprintf("command-%d", index+1)
							notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": toolID, "name": "run_command", "rawInput": map[string]any{"CommandLine": command, "WaitMsBeforeAsync": 10000}})
							notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": toolID, "status": "completed"})
							appendFakeACPHoldTask(recordsDir, index+1, command)
						}
					} else if records {
						toolID := "agy-record-tool"
						notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": toolID, "name": "run_command"})
						appendFakeACPHoldTask(recordsDir, 1, "")
					}
					if mode == "feature_hold_spontaneous" {
						writeFakeACPHoldUsage(1)
					}
					if mode == "feature_hold_final_step" {
						appendFakeACPHoldTranscript(recordsDir, map[string]any{"type": "PLANNER_RESPONSE", "status": "DONE"})
					}
					tasks := 2
					if mode == "feature_hold_inround" {
						tasks = 1
					}
					if records || editEcho {
						tasks = 0
					}
					for i := 1; i <= tasks; i++ {
						toolID := fmt.Sprintf("hold-tool-%d", i)
						output := fmt.Sprintf("Tool is running as a background task with task id: %s/task-%d", fakeHoldSession, i)
						notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": toolID, "name": "run_command"})
						update := map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": toolID, "status": "completed"}
						switch mode {
						case "feature_hold_raw":
							update["rawOutput"] = map[string]any{"output": output}
						case "feature_hold_output":
							update["output"] = output
						default:
							update["content"] = []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": output}}}
						}
						notify(update)
						// 同一任务的重发不能产生第二个开始事件。
						notify(update)
					}
					if mode == "feature_hold_inround" {
						// 本轮应答前任务已经结束：完成消息落盘，watcher 还没来得及扫描。
						recordsDir := fakeACPHoldRecords("antigravity-cli")
						writeFakeACPHoldLog(recordsDir, "轮内输出\n")
						path := filepath.Join(recordsDir, "messages", "in-round.json")
						if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
							panic(err)
						}
						if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"content":%q,"timestamp":"2026-01-01T00:00:00Z"}`, fmt.Sprintf("Task id %q finished with result:\\noutput", fakeHoldSession+"/task-1"))), 0600); err != nil {
							panic(err)
						}
					}
				}
				if promptCount > 1 && mode == "feature_hold_error" {
					write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "error": map[string]any{"code": 429, "message": "RESOURCE_EXHAUSTED"}})
					continue
				}
				if promptCount > 1 && mode == "feature_hold_cancel" {
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "cancel"}}}})
					for read() != nil {
					}
					return
				}
			}
			if strings.HasPrefix(mode, "feature_agy_outcome_") {
				dbPath := agySessionDB(agyGeminiDir(os.Args[1:], os.Environ()), "feature-session")
				if mode == "feature_agy_outcome_skip" {
					_ = os.Remove(dbPath)
				}
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					panic(err)
				}
				if _, err := db.Exec("CREATE TABLE IF NOT EXISTS steps (idx INTEGER, step_type INTEGER, step_payload BLOB)"); err != nil {
					panic(err)
				}
				if mode != "feature_agy_outcome_old" {
					kind := 1
					if mode == "feature_agy_outcome_error" || mode == "feature_agy_outcome_skip" {
						kind = 17
					}
					if _, err := db.Exec("INSERT INTO steps VALUES (2, ?, ?)", kind, []byte("Error 429, Message: quota, Status: RESOURCE_EXHAUSTED quotaResetTimeStamp:2026-11-17T00:00:00Z\nstack")); err != nil {
						panic(err)
					}
				}
				if err := db.Close(); err != nil {
					panic(err)
				}
			}
			if mode == "feature_agy_interaction" {
				write(map[string]any{"jsonrpc": "2.0", "id": 9100, "method": "session/request_permission", "params": map[string]any{
					"toolCall": map[string]any{"toolCallId": "interaction_test", "title": "Choose"},
					"options":  []any{map[string]any{"optionId": "one", "name": "One"}, map[string]any{"optionId": "two", "name": "Two"}},
				}})
				read()
			}
			if strings.HasPrefix(mode, "feature_agy_client_read") {
				workDir, _ := os.Getwd()
				path := filepath.Join(workDir, "agy-read.txt")
				_ = os.WriteFile(path, []byte("read"), 0600)
				notify := func(update map[string]any) {
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": update}})
				}
				if mode == "feature_agy_client_read_early" {
					write(map[string]any{"jsonrpc": "2.0", "id": 9102, "method": "fs/read_text_file", "params": map[string]any{"path": path}})
					read()
				}
				call := map[string]any{"sessionUpdate": "tool_call", "toolCallId": "client-read", "title": "Running client_view_file", "locations": []any{map[string]any{"path": path}}}
				if mode == "feature_agy_client_read_early" {
					call["title"] = "Run client_view_file?"
					delete(call, "locations")
					call["rawInput"] = map[string]any{"absolute_path": path}
				}
				notify(call)
				if mode == "feature_agy_client_read_multiple" {
					call["toolCallId"] = "client-read-second"
					notify(call)
				}
				if mode != "feature_agy_client_read_early" {
					if mode == "feature_agy_client_read_missing" {
						_ = os.Remove(path)
					}
					write(map[string]any{"jsonrpc": "2.0", "id": 9102, "method": "fs/read_text_file", "params": map[string]any{"path": path}})
					read()
				}
				notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "client-read", "status": "failed"})
				if mode == "feature_agy_client_read_multiple" {
					notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "client-read-second", "status": "failed"})
				}
			}
			if mode == "feature_write_text_file" {
				workDir := sessionWorkDir
				if workDir == "" {
					workDir, _ = os.Getwd()
				}
				write(map[string]any{
					"jsonrpc": "2.0",
					"id":      9130,
					"method":  "fs/write_text_file",
					"params": map[string]any{
						"path":    filepath.Join(workDir, "nested", "write-test.txt"),
						"content": "written content",
					},
				})
				if read() == nil {
					return
				}
			}
			if mode == "feature_agy_command" {
				write(map[string]any{"jsonrpc": "2.0", "id": 9101, "method": "session/request_permission", "params": map[string]any{
					"toolCall": map[string]any{"toolCallId": "command_test", "name": "run", "rawInput": map[string]any{"CommandLine": "cat README.md"}},
					"options":  []any{map[string]any{"optionId": "allow", "kind": "allow_once"}, map[string]any{"optionId": "deny", "kind": "reject_once"}},
				}})
				read()
			}
			if mode == "feature_agy_auto_deny_continue" {
				if promptCount == 1 {
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{
						"sessionUpdate": "tool_call",
						"toolCallId":    "denied-call-1",
						"name":          "run_command",
						"title":         "Run rm -rf build",
						"rawInput":      map[string]any{"CommandLine": "rm -rf build"},
					}}})
					write(map[string]any{"jsonrpc": "2.0", "id": 9201, "method": "session/request_permission", "params": map[string]any{
						"toolCall": map[string]any{"toolCallId": "denied-call-1", "name": "run_command", "title": "Run rm -rf build", "rawInput": map[string]any{"CommandLine": "rm -rf build"}},
						"options":  []any{map[string]any{"optionId": "allow", "kind": "allow_once"}, map[string]any{"optionId": "reject", "kind": "reject_once"}},
					}})
					if read() == nil {
						return
					}
					result["usage"] = map[string]any{"inputTokens": 100, "outputTokens": 20, "thoughtTokens": 5, "cachedReadTokens": 10}
					result["stopReason"] = "end_turn"
					write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": result})
					continue
				}
				write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{
					"sessionUpdate": "agent_message_chunk",
					"content":       map[string]any{"text": "已改用只读方式完成"},
				}}})
				result["usage"] = map[string]any{"inputTokens": 150, "outputTokens": 30, "thoughtTokens": 8, "cachedReadTokens": 20}
			}
			if mode == "feature_agy_auto_deny_mixed" && promptCount == 1 {
				for i := 1; i <= 2; i++ {
					callID := fmt.Sprintf("mixed-call-%d", i)
					write(map[string]any{"jsonrpc": "2.0", "id": 9210 + i, "method": "session/request_permission", "params": map[string]any{
						"toolCall": map[string]any{"toolCallId": callID, "name": "run_command", "title": fmt.Sprintf("Mixed cmd %d", i)},
						"options":  []any{map[string]any{"optionId": "allow", "kind": "allow_once"}, map[string]any{"optionId": "reject", "kind": "reject_once"}},
					}})
					if read() == nil {
						return
					}
				}
			}
			if mode == "feature_agy_auto_deny_loop" {
				callID := fmt.Sprintf("loop-call-%d", promptCount)
				write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{
					"sessionUpdate": "tool_call",
					"toolCallId":    callID,
					"name":          "run_command",
					"title":         "Blocked command",
				}}})
				write(map[string]any{"jsonrpc": "2.0", "id": 9300 + promptCount, "method": "session/request_permission", "params": map[string]any{
					"toolCall": map[string]any{"toolCallId": callID, "name": "run_command", "title": "Blocked command"},
					"options":  []any{map[string]any{"optionId": "allow", "kind": "allow_once"}, map[string]any{"optionId": "reject", "kind": "reject_once"}},
				}})
				if read() == nil {
					return
				}
			}
			if mode == "feature_command_kind" {
				calls := []map[string]any{
					{"toolCallId": "kind-execute", "name": "execute", "kind": "execute", "rawInput": map[string]any{"command": "ls"}},
					{"toolCallId": "kind-agy-command", "name": "run_command", "rawInput": map[string]any{"CommandLine": "git log -n 10 --oneline"}},
					{"toolCallId": "kind-edit", "name": "edit_file", "kind": "edit", "rawInput": map[string]any{"path": "a.txt"}},
				}
				for i, call := range calls {
					write(map[string]any{"jsonrpc": "2.0", "id": 9110 + i, "method": "session/request_permission", "params": map[string]any{
						"toolCall": call,
						"options":  []any{map[string]any{"optionId": "allow", "kind": "allow_once"}, map[string]any{"optionId": "deny", "kind": "reject_once"}},
					}})
					if read() == nil {
						return
					}
				}
			}
			if mode == "feature_read_kind" {
				calls := []map[string]any{
					{"toolCallId": "kind-read", "name": "read_file", "kind": "read", "rawInput": map[string]any{"file_path": "/workspace/a.go"}},
					{"toolCallId": "kind-edit", "name": "edit_file", "kind": "edit", "rawInput": map[string]any{"path": "a.txt"}},
				}
				for i, call := range calls {
					write(map[string]any{"jsonrpc": "2.0", "id": 9120 + i, "method": "session/request_permission", "params": map[string]any{
						"toolCall": call,
						"options":  []any{map[string]any{"optionId": "allow", "kind": "allow_once"}, map[string]any{"optionId": "deny", "kind": "reject_once"}},
					}})
					if read() == nil {
						return
					}
				}
			}
			if mode == "feature_agy_tool_result" {
				notify := func(update map[string]any) {
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": update}})
				}
				workDir := sessionWorkDir
				if workDir == "" {
					workDir, _ = os.Getwd()
				}
				target := filepath.Join(workDir, "agy-edit.txt")
				_ = os.WriteFile(target, []byte("line 1\nold line\nline 3\n"), 0600)
				notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "command", "name": "run_command", "rawInput": map[string]any{"CommandLine": "false"}})
				notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "command", "status": "completed", "rawOutput": map[string]any{"combinedOutput": "hello\n", "exitCode": 1}})
				notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "edit", "name": "replace_file_content", "kind": "edit", "locations": []any{map[string]any{"path": target}}, "rawInput": map[string]any{"TargetFile": target, "TargetContent": "old line", "ReplacementContent": "new line"}})
				notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit", "status": "completed", "rawOutput": map[string]any{"output": "ok"}})
			}
			if strings.HasPrefix(mode, "feature_tool_") {
				notify := func(update map[string]any) {
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": update}})
				}
				for _, id := range []string{"z", "a"} {
					notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": id, "name": "test"})
				}
				if mode == "feature_tool_array" {
					notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "a", "status": "pending"})
					notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "a", "status": "in_progress"})
					notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "a", "status": "completed", "content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "第一段"}}, map[string]any{"type": "text", "text": "第二段"}}, "rawOutput": map[string]any{"output": "备用"}})
					notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "z", "content": "无法解析", "rawOutput": map[string]any{"output": "备用"}})
				}
				if mode == "feature_tool_error" {
					write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "error": map[string]any{"code": -32603, "message": "失败"}})
					continue
				}
			}
			if mode == "feature_cancel" {
				write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "cancel"}}}})
				for read() != nil {
				}
				return
			}
			if mode == "feature_elicitation" {
				write(map[string]any{"jsonrpc": "2.0", "id": 9001, "method": "elicitation/create", "params": map[string]any{
					"mode": "form", "message": "Choose", "requestedSchema": map[string]any{"properties": map[string]any{
						"single": map[string]any{"type": "string", "title": "Single", "description": "Select one", "enum": []string{"a", "b"}},
						"multi":  map[string]any{"type": "array", "title": "Multi", "items": map[string]any{"enum": []string{"x", "y"}}},
						"text":   map[string]any{"type": "string"},
						"bool":   map[string]any{"type": "boolean"},
						"oneof":  map[string]any{"type": "string", "oneOf": []any{map[string]any{"const": "v", "title": "Visible"}}},
						"anyof":  map[string]any{"type": "array", "items": map[string]any{"anyOf": []any{map[string]any{"const": "z", "title": "Zed"}}}},
					}},
				}})
				if read() == nil {
					return
				}
			}
			if strings.HasPrefix(mode, "feature_permission") {
				opts := []any{
					map[string]any{"optionId": "always", "kind": "allow_always", "name": "Allow permanently"},
					map[string]any{"optionId": "reject_always", "kind": "reject_always", "name": "Reject permanently"},
					map[string]any{"optionId": "deny", "kind": "reject_once", "name": "No"},
				}
				call := map[string]any{"toolCallId": "permission-test", "name": "read_file"}
				if mode == "feature_permission_missing_reject" {
					opts = opts[:2]
				}
				if mode == "feature_permission_once" || mode == "feature_permission_switch_mode" || mode == "feature_permission_command" {
					opts = append(opts, map[string]any{"optionId": "once", "kind": "allow_once", "name": "Yes"})
				}
				if mode == "feature_permission_switch_mode" {
					call["kind"], call["title"] = "switch_mode", "Switch to execution"
					delete(call, "name")
				} else if mode == "feature_permission_command" {
					call["kind"], call["name"] = "execute", "run_command"
					call["rawInput"] = map[string]any{"CommandLine": "git push --force"}
				}
				write(map[string]any{"jsonrpc": "2.0", "id": 9010, "method": "session/request_permission", "params": map[string]any{"toolCall": call, "options": opts}})
				if read() == nil {
					return
				}
			}
			if mode == "feature_usage" {
				for _, used := range []int{100, 200} {
					write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": map[string]any{"sessionUpdate": "usage_update", "used": used, "size": 2000}}})
				}
				result["usage"] = map[string]any{"inputTokens": 500, "outputTokens": 20, "thoughtTokens": 5, "cachedReadTokens": 100}
			}
			result["stopReason"] = "end_turn"
			if mode == "feature_agy_outcome_refusal" {
				result["stopReason"] = "refusal"
			}
		case "session/close":
			write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": result})
			return
		}
		write(map[string]any{"jsonrpc": "2.0", "id": msg["id"], "result": result})
	}
}

func runACPFeature(t *testing.T, mode string, agent Agent, turn harness.Turn, configure ...func(*Options)) ([]map[string]any, []harness.Event, error) {
	t.Helper()
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	h := New(Options{Agent: agent, Binary: os.Args[0], ExtraArgs: []string{"--extra"}, Env: []string{fakeACPEnv + "=" + mode, "MCODE_TEST_ACP_TRACE=" + trace}, TurnArgs: func(harness.Turn) []string { return []string{"--gemini_dir", "routed-account"} }})
	for _, apply := range configure {
		apply(&h.opts)
	}
	if mode == "feature_mcp_http" {
		h.opts.BuiltinMCP = harness.BuiltinMCP{URL: "http://127.0.0.1:9000/mcp"}
	}
	if turn.WorkDir == "" {
		turn.WorkDir = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var events []harness.Event
	var mu sync.Mutex
	err := h.Run(ctx, turn, func(e harness.Event) { mu.Lock(); defer mu.Unlock(); events = append(events, e) })
	data, readErr := os.ReadFile(trace)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
	}
	if roots := h.ProcessRootPIDs(turn.ThreadID); len(roots) != 0 {
		t.Fatalf("leaked process roots: %v", roots)
	}
	mu.Lock()
	defer mu.Unlock()
	return messages, append([]harness.Event(nil), events...), err
}

func featureParams(messages []map[string]any, method string) []map[string]any {
	var params []map[string]any
	for _, msg := range messages {
		if msg["method"] == method {
			p, _ := msg["params"].(map[string]any)
			params = append(params, p)
		}
	}
	return params
}

type elicitationApprover struct {
	mu       sync.Mutex
	req      harness.ApprovalRequest
	response harness.ApprovalResponse
	err      error
	acked    bool
	ackErr   error
}

func (a *elicitationApprover) Approve(context.Context, harness.ApprovalRequest) (harness.Decision, error) {
	return harness.DecisionDeny, nil
}
func (a *elicitationApprover) Interact(_ context.Context, req harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.req = req
	return a.response, a.err
}
func (a *elicitationApprover) AckDelivery(_ harness.ApprovalRequest, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.acked = true
	a.ackErr = err
}

func TestACPElicitationForm(t *testing.T) {
	for _, tc := range []struct {
		name, action string
		response     harness.ApprovalResponse
		noInteractor bool
		err          error
	}{
		{name: "answers", action: "accept", response: harness.ApprovalResponse{Answers: map[string][]string{"single": {"b"}, "multi": {"x", "y"}, "text": {"hello"}, "bool": {"true"}, "oneof": {"Visible"}, "anyof": {"Zed"}}}},
		{name: "skipped", action: "decline", response: harness.ApprovalResponse{Skipped: true}},
		{name: "no interactor", action: "cancel", noInteractor: true},
		{name: "error", action: "cancel", err: errors.New("cancelled")},
		{name: "no answers", action: "cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &elicitationApprover{response: tc.response, err: tc.err}
			var approver harness.Approver = a
			if tc.noInteractor {
				approver = &mockApprover{}
			}
			messages, _, err := runACPFeature(t, "feature_elicitation", AgentAgy, harness.Turn{Approver: approver})
			if err != nil {
				t.Fatal(err)
			}
			var reply map[string]any
			for _, msg := range messages {
				if msg["id"] == float64(9001) {
					reply, _ = msg["result"].(map[string]any)
				}
			}
			if reply["action"] != tc.action {
				t.Fatalf("reply = %v", reply)
			}
			if tc.noInteractor {
				return
			}
			a.mu.Lock()
			defer a.mu.Unlock()
			if !a.acked || (tc.err == nil && a.ackErr != nil) {
				t.Fatalf("delivery = %v, %v", a.acked, a.ackErr)
			}
			if a.req.Kind != harness.RequestKindQuestion || a.req.ToolName != "ask_question" || !strings.HasPrefix(a.req.ToolID, "acp-q-") {
				t.Fatalf("request = %+v", a.req)
			}
			if tc.action != "accept" {
				return
			}
			questions := map[string]harness.Question{}
			for _, q := range a.req.Questions {
				questions[q.ID] = q
			}
			if len(questions) != 6 {
				t.Fatalf("questions = %+v", questions)
			}
			if q := questions["single"]; q.Header != "Single" || q.Prompt != "Select one" || !reflect.DeepEqual(q.Choices, []harness.QuestionChoice{{Label: "a"}, {Label: "b"}}) {
				t.Fatalf("single = %+v", q)
			}
			if q := questions["multi"]; !q.AllowsMultiple || q.Prompt != "Multi" || len(q.Choices) != 2 {
				t.Fatalf("multi = %+v", q)
			}
			if q := questions["text"]; !q.AllowsOther || len(q.Choices) != 0 || q.Prompt != "Choose" {
				t.Fatalf("text = %+v", q)
			}
			if !reflect.DeepEqual(questions["bool"].Choices, []harness.QuestionChoice{{Label: "true"}, {Label: "false"}}) {
				t.Fatal(questions["bool"])
			}
			want := map[string]any{"single": "b", "multi": []any{"x", "y"}, "text": "hello", "bool": true, "oneof": "v", "anyof": []any{"z"}}
			if !reflect.DeepEqual(reply["content"], want) {
				t.Fatalf("content = %#v, want %#v", reply["content"], want)
			}
			init := featureParams(messages, "initialize")[0]
			if got := messages[0]["args"]; !reflect.DeepEqual(got, []any{"acp", "--disable-telemetry", "--extra", "--gemini_dir", "routed-account"}) {
				t.Fatalf("args order = %v", got)
			}
			caps := init["clientCapabilities"].(map[string]any)
			if _, ok := caps["elicitation"].(map[string]any)["form"]; !ok {
				t.Fatal("form capability missing")
			}
		})
	}
	t.Run("unsupported mode", func(t *testing.T) {
		if _, err := handleElicitation(context.Background(), nil, json.RawMessage(`{"mode":"url"}`)); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestACPElicitationFoldsOtherCompanion(t *testing.T) {
	params := json.RawMessage(`{
		"mode": "form",
		"message": "Questions",
		"requestedSchema": {
			"properties": {
				"q1": {"type": "string", "enum": ["a", "b"], "description": "Pick?"},
				"q1_other": {"type": "string", "title": "Other", "description": "Optional free-form answer to: Pick?"},
				"q2": {"type": "array", "items": {"type": "string", "enum": ["x", "y"]}, "description": "Many?"},
				"q2_other": {"type": "string", "title": "Other", "description": "Optional free-form answer to: Many?"}
			}
		}
	}`)
	for _, tc := range []struct {
		name    string
		answers map[string][]string
		want    map[string]any
	}{
		{
			name:    "single custom",
			answers: map[string][]string{"q1": {"custom"}},
			want:    map[string]any{"q1_other": "custom"},
		},
		{
			name:    "single option and multi mixed",
			answers: map[string][]string{"q1": {"a"}, "q2": {"x", "mine"}},
			want:    map[string]any{"q1": "a", "q2": []string{"x"}, "q2_other": "mine"},
		},
		{
			name:    "multi custom only",
			answers: map[string][]string{"q2": {"mine1", "mine2"}},
			want:    map[string]any{"q2_other": "mine1\nmine2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &elicitationApprover{response: harness.ApprovalResponse{Answers: tc.answers}}
			res, err := handleElicitation(context.Background(), a, params)
			if err != nil {
				t.Fatal(err)
			}
			a.mu.Lock()
			questions := a.req.Questions
			a.mu.Unlock()
			if len(questions) != 2 || questions[0].ID != "q1" || questions[1].ID != "q2" {
				t.Fatalf("questions = %+v", questions)
			}
			if !questions[0].AllowsOther || !questions[1].AllowsOther {
				t.Fatalf("AllowsOther = %v, %v", questions[0].AllowsOther, questions[1].AllowsOther)
			}
			resp, ok := res.(acpApprovalResponse)
			if !ok {
				t.Fatalf("unexpected response type %T", res)
			}
			payload, ok := resp.ResponsePayload().(map[string]any)
			if !ok || payload["action"] != "accept" {
				t.Fatalf("payload = %#v", resp.ResponsePayload())
			}
			content, _ := payload["content"].(map[string]any)
			if !reflect.DeepEqual(content, tc.want) {
				t.Fatalf("content = %#v, want %#v", content, tc.want)
			}
		})
	}
}

func TestACPElicitationFoldsOtherCompanionByKey(t *testing.T) {
	params := json.RawMessage(`{
		"mode": "form",
		"message": "Questions",
		"requestedSchema": {
			"properties": {
				"q0": {"type": "string", "enum": ["a", "b"], "description": "Pick?"},
				"q0_other": {"type": "string", "title": "Other", "description": "Write your own"}
			}
		}
	}`)
	a := &elicitationApprover{response: harness.ApprovalResponse{Answers: map[string][]string{"q0": {"custom"}}}}
	res, err := handleElicitation(context.Background(), a, params)
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	questions := a.req.Questions
	a.mu.Unlock()
	if len(questions) != 1 || questions[0].ID != "q0" {
		t.Fatalf("questions = %+v", questions)
	}
	if !questions[0].AllowsOther {
		t.Fatalf("AllowsOther = %v, want true", questions[0].AllowsOther)
	}
	resp, ok := res.(acpApprovalResponse)
	if !ok {
		t.Fatalf("unexpected response type %T", res)
	}
	payload, ok := resp.ResponsePayload().(map[string]any)
	if !ok || payload["action"] != "accept" {
		t.Fatalf("payload = %#v", resp.ResponsePayload())
	}
	content, _ := payload["content"].(map[string]any)
	want := map[string]any{"q0_other": "custom"}
	if !reflect.DeepEqual(content, want) {
		t.Fatalf("content = %#v, want %#v", content, want)
	}
}

func fakeResponse(messages []map[string]any, id float64) map[string]any {
	for _, message := range messages {
		if message["id"] == id {
			return message
		}
	}
	return nil
}

func TestAgyInteractionQuestion(t *testing.T) {
	for _, tc := range []struct {
		name, option string
		response     harness.ApprovalResponse
		err          error
		noInteractor bool
	}{
		{name: "selected", option: "two", response: harness.ApprovalResponse{Answers: map[string][]string{"interaction_test": {"Two"}}}},
		{name: "option id", option: "two", response: harness.ApprovalResponse{Answers: map[string][]string{"interaction_test": {"two"}}}},
		{name: "skipped", response: harness.ApprovalResponse{Skipped: true}},
		{name: "no answer"},
		{name: "error", err: errors.New("cancelled")},
		{name: "no interactor", noInteractor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approver := &elicitationApprover{response: tc.response, err: tc.err}
			var turnApprover harness.Approver = approver
			if tc.noInteractor {
				turnApprover = &mockApprover{decision: harness.DecisionAllow}
			}
			messages, _, err := runACPFeature(t, "feature_agy_interaction", AgentAgy, harness.Turn{Mode: harness.ModeAuto, Approver: turnApprover})
			if err != nil {
				t.Fatal(err)
			}
			result, _ := fakeResponse(messages, 9100)["result"].(map[string]any)
			outcome, _ := result["outcome"].(map[string]any)
			if tc.option == "" {
				if outcome["outcome"] != "cancelled" {
					t.Fatalf("outcome = %v", outcome)
				}
			} else if outcome["outcome"] != "selected" || outcome["optionId"] != tc.option {
				t.Fatalf("outcome = %v", outcome)
			}
			if !tc.noInteractor {
				approver.mu.Lock()
				defer approver.mu.Unlock()
				req := approver.req
				if req.Kind != harness.RequestKindQuestion || req.ToolName != "ask_question" || req.ToolID != "interaction_test" || req.Summary != "Choose" || len(req.Questions) != 1 || req.Questions[0].AllowsOther || req.Questions[0].AllowsMultiple || !reflect.DeepEqual(req.Questions[0].Choices, []harness.QuestionChoice{{Label: "One"}, {Label: "Two"}}) {
					t.Fatalf("request = %+v", req)
				}
				if !approver.acked || !errors.Is(approver.ackErr, tc.err) {
					t.Fatalf("ack = %v, %v", approver.acked, approver.ackErr)
				}
			}
		})
	}
}

func TestAgyNativeCommandField(t *testing.T) {
	messages, _, err := runACPFeature(t, "feature_agy_command", AgentAgy, harness.Turn{Mode: harness.ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	result, _ := fakeResponse(messages, 9101)["result"].(map[string]any)
	outcome, _ := result["outcome"].(map[string]any)
	if outcome["optionId"] != "allow" {
		t.Fatalf("optionId = %v", outcome["optionId"])
	}
}

// recordingApprover 记录每次审批请求并按 Allow 答复。
type recordingApprover struct {
	mu   sync.Mutex
	reqs []harness.ApprovalRequest
}

func (a *recordingApprover) Approve(_ context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, req)
	return harness.DecisionAllow, nil
}

// TestRequestPermissionMarksExecuteAsCommand 覆盖 session/request_permission 的请求种类：
// 执行类 kind 与 agy 的 run_command 标为命令类，编辑类不标。
func TestRequestPermissionMarksExecuteAsCommand(t *testing.T) {
	approver := &recordingApprover{}
	if _, _, err := runACPFeature(t, "feature_command_kind", AgentAgy, harness.Turn{Mode: harness.ModeSupervised, Approver: approver}); err != nil {
		t.Fatal(err)
	}

	approver.mu.Lock()
	defer approver.mu.Unlock()
	if len(approver.reqs) != 3 {
		t.Fatalf("got %d approval requests, want 3", len(approver.reqs))
	}
	if got := approver.reqs[0].Kind; got != harness.RequestKindCommand {
		t.Errorf("execute 调用应标为命令类，得到 %q", got)
	}
	if got := approver.reqs[1]; got.ToolName != "run_command" || got.Kind != harness.RequestKindCommand {
		t.Errorf("agy run_command 应标为命令类，得到 toolName=%q kind=%q", got.ToolName, got.Kind)
	}
	if got := approver.reqs[2].Kind; got == harness.RequestKindCommand {
		t.Errorf("编辑类调用不应标为命令类，得到 %q", got)
	}
}

// TestACPRequestPermissionMarksReadKind 覆盖 session/request_permission 的只读标记：
// read 类 kind 标为 ReadOnly=true，edit 类 kind 为 false。
func TestACPRequestPermissionMarksReadKind(t *testing.T) {
	approver := &recordingApprover{}
	if _, _, err := runACPFeature(t, "feature_read_kind", AgentAgy, harness.Turn{Mode: harness.ModeSupervised, Approver: approver}); err != nil {
		t.Fatal(err)
	}

	approver.mu.Lock()
	defer approver.mu.Unlock()
	if len(approver.reqs) != 2 {
		t.Fatalf("got %d approval requests, want 2", len(approver.reqs))
	}
	if !approver.reqs[0].ReadOnly {
		t.Errorf("read 类调用应标为 ReadOnly=true，得到 false")
	}
	if approver.reqs[1].ReadOnly {
		t.Errorf("edit 类调用应为 ReadOnly=false，得到 true")
	}
}

func TestAgyClientViewFileRepair(t *testing.T) {
	for _, mode := range []string{"feature_agy_client_read", "feature_agy_client_read_early", "feature_agy_client_read_multiple", "feature_agy_client_read_missing"} {
		t.Run(mode, func(t *testing.T) {
			_, events, err := runACPFeature(t, mode, AgentAgy, harness.Turn{Mode: harness.ModeAuto})
			if err != nil {
				t.Fatal(err)
			}
			var ends []harness.Event
			for _, event := range events {
				if event.Type == harness.EventToolEnd && event.ToolID == "client-read" {
					ends = append(ends, event)
				}
			}
			if len(ends) != 1 || ends[0].IsError != (mode == "feature_agy_client_read_missing") || ends[0].Output != "" {
				t.Fatalf("%s tool ends = %+v", mode, ends)
			}
			if mode == "feature_agy_client_read_multiple" {
				var second []harness.Event
				for _, event := range events {
					if event.Type == harness.EventToolEnd && event.ToolID == "client-read-second" {
						second = append(second, event)
					}
				}
				if len(second) != 1 || !second[0].IsError {
					t.Fatalf("second tool ends = %+v", second)
				}
			}
		})
	}
}

func TestAgyTurnOutcome(t *testing.T) {
	for _, tc := range []struct {
		name, wantError  string
		missing, corrupt bool
	}{
		{name: "error", wantError: "Error 429, Message: quota, Status: RESOURCE_EXHAUSTED quotaResetTimeStamp:2026-11-17T00:00:00Z"},
		{name: "normal"},
		{name: "old"},
		{name: "error", missing: true, wantError: "RESOURCE_EXHAUSTED"},
		{name: "skip", corrupt: true},
		{name: "refusal", wantError: "agy quota exhausted (stopReason refusal)"},
	} {
		t.Run(fmt.Sprintf("%s/missing=%v", tc.name, tc.missing), func(t *testing.T) {
			geminiDir := filepath.Join(t.TempDir(), "gemini # account")
			dbPath := agySessionDB(geminiDir, "feature-session")
			if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
				t.Fatal(err)
			}
			if tc.corrupt {
				if err := os.WriteFile(dbPath, []byte("invalid sqlite"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if !tc.missing {
				db, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec("CREATE TABLE steps (idx INTEGER, step_type INTEGER, step_payload BLOB)"); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO steps VALUES (1, 17, ?)", []byte("Error 429, Message: old quota, Status: RESOURCE_EXHAUSTED")); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			_, events, err := runACPFeature(t, "feature_agy_outcome_"+tc.name, AgentAgy, harness.Turn{}, func(opts *Options) {
				opts.TurnArgs = func(harness.Turn) []string { return []string{"--gemini_dir=" + geminiDir} }
			})
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", err, tc.wantError)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			done := 0
			for _, event := range events {
				if event.Type == harness.EventDone {
					done++
				}
			}
			wantDone := 1
			if tc.wantError != "" {
				wantDone = 0
			}
			if done != wantDone {
				t.Fatalf("EventDone count = %d, want %d", done, wantDone)
			}
		})
	}
}

func TestAgyStepErrorText(t *testing.T) {
	const payloadHex = "081120032ACC010A0B088FC1DED6061090E5E53D1805420B088FC1DED6061090E5E53D622437613765636239362D633166632D343662382D383133622D343461636132396431626135A201500A2430396439646261372D663734362D343731612D386665362D39623332663532316561366510551829222432363537353235392D386563312D346232652D393133362D393836316165653936646130D201110A0F0803120B088FC1DED60610F8E6E73D9A02206464373033656438383265313431383438643339633436653163613266333937C201A5011AA2011272415049206572726F722028617474656D70742032293A20554E415641494C41424C452028636F646520353033293A204E6F20636170616369747920617661696C61626C6520666F72206D6F64656C2067656D696E692D332E382D666C6173682D68696768206F6E2074686520736572766572322C30396439646261372D663734362D343731612D386665362D3962333266353231656136652D38352D32303130"
	tests := []struct {
		name    string
		payload []byte
		want    []string
	}{
		{
			name:    "protobuf payload",
			payload: mustDecodeHex(t, payloadHex),
			want:    []string{"UNAVAILABLE (code 503): No capacity available for model gemini-3.8-flash-high"},
		},
		{
			name:    "quota payload",
			payload: append([]byte("\x08\x11\x20\x03\x2a\x0a"), []byte("Error 429, Message: quota exhausted, Status: RESOURCE_EXHAUSTED quotaResetTimeStamp:2026-11-16T10:00:00Z")...),
			want:    []string{"RESOURCE_EXHAUSTED", "2026-11-16T10:00:00Z"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "steps.db")
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("CREATE TABLE steps (idx INTEGER, step_type INTEGER, step_payload BLOB)"); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if _, err := db.Exec("INSERT INTO steps VALUES (1, 17, ?)", tc.payload); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			text, ok := agyNewStepError(dbPath, -1)
			if !ok {
				t.Fatal("agyNewStepError returned ok=false")
			}
			for _, want := range tc.want {
				if !strings.Contains(text, want) {
					t.Errorf("text = %q, want substring %q", text, want)
				}
			}
			for _, r := range text {
				if unicode.IsControl(r) {
					t.Fatalf("text contains control character %U: %q", r, text)
				}
			}
		})
	}
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestACPResumeAndClose(t *testing.T) {
	for _, tc := range []struct{ mode, session, method string }{{"feature_resume", "existing", "session/resume"}, {"feature_load", "existing", "session/load"}, {"feature_new", "", "session/new"}} {
		t.Run(tc.method, func(t *testing.T) {
			dir := t.TempDir()
			messages, _, err := runACPFeature(t, tc.mode, AgentAgy, harness.Turn{SessionID: tc.session, AdditionalDirs: []string{dir}})
			if err != nil {
				t.Fatal(err)
			}
			params := featureParams(messages, tc.method)
			if len(params) != 1 || !reflect.DeepEqual(params[0]["additionalDirectories"], []any{dir}) {
				t.Fatalf("setup = %v", params)
			}
			if tc.method == "session/resume" && len(featureParams(messages, "session/load")) != 0 {
				t.Fatal("used load instead of resume")
			}
			if close := featureParams(messages, "session/close"); len(close) != 1 {
				t.Fatal("missing close")
			} else if tc.session != "" && close[0]["sessionId"] != tc.session {
				t.Fatal(close)
			}
		})
	}
}

func TestACPPromptUsage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		agent      Agent
		wantOutput int64
	}{
		{name: "agy does not double count thought", agent: AgentAgy, wantOutput: 20},
		{name: "other agent sums output and thought", agent: AgentOpenCode, wantOutput: 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, events, err := runACPFeature(t, "feature_usage", tc.agent, harness.Turn{})
			if err != nil {
				t.Fatal(err)
			}
			var usages []harness.Usage
			for i, e := range events {
				if e.Type == harness.EventUsage {
					usages = append(usages, *e.Usage)
					if i+1 == len(events) {
						t.Fatal("usage after done")
					}
				}
			}
			if len(usages) != 3 {
				t.Fatalf("usages = %v", usages)
			}
			if want := (harness.Usage{InputTokens: 200, ContextTokens: 200, ContextWindow: 2000}); !reflect.DeepEqual(usages[1], want) {
				t.Fatalf("update = %+v", usages[1])
			}
			if want := (harness.Usage{InputTokens: 500, OutputTokens: tc.wantOutput, CachedTokens: 100, ContextTokens: 200, ContextWindow: 2000}); !reflect.DeepEqual(usages[2], want) {
				t.Fatalf("final = %+v", usages[2])
			}
			if events[len(events)-1].Type != harness.EventDone {
				t.Fatal("done must be last")
			}
		})
	}
}

func TestACPAgySkipsUnchangedModel(t *testing.T) {
	messages, _, err := runACPFeature(t, "feature_agy", AgentAgy, harness.Turn{Model: "old", Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	if config := featureParams(messages, "session/set_config_option"); len(config) != 0 {
		t.Fatal(config)
	}
}

func TestAgyModeStaysDefaultUnlessPlan(t *testing.T) {
	checkMode := func(t *testing.T, feature string, turn harness.Turn, wanted string) {
		t.Helper()
		messages, _, err := runACPFeature(t, feature, AgentAgy, turn)
		if err != nil {
			t.Fatal(err)
		}
		modes := featureParams(messages, "session/set_mode")
		if wanted == "" {
			if len(modes) != 0 {
				t.Fatalf("unexpected set_mode: %v", modes)
			}
		} else if len(modes) != 1 || modes[0]["modeId"] != wanted {
			t.Fatalf("set_mode = %v, want %s", modes, wanted)
		}
	}
	for _, mode := range []harness.PermissionMode{harness.ModeAuto, harness.ModeAutoAcceptEdits, harness.ModeFullAccess} {
		t.Run(string(mode), func(t *testing.T) {
			turn := harness.Turn{Mode: mode}
			checkMode(t, "feature_agy_current_auto", turn, "default")
			checkMode(t, "feature_agy", turn, "")
		})
	}
	t.Run("plan", func(t *testing.T) {
		turn := harness.Turn{Mode: harness.ModeReadOnly, InteractionMode: "plan"}
		checkMode(t, "feature_agy", turn, "plan")
		checkMode(t, "feature_agy_current_plan", turn, "")
		checkMode(t, "feature_agy_no_plan", turn, "default")
		checkMode(t, "feature_agy_unadvertised", turn, "")
	})
}

func TestACPAgyModelAndEffortConfig(t *testing.T) {
	messages, _, err := runACPFeature(t, "feature_agy", AgentAgy, harness.Turn{Mode: harness.ModeReadOnly, InteractionMode: "plan", Model: "requested", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	if len(featureParams(messages, "session/set_model")) != 0 {
		t.Fatal("agy used set_model")
	}
	if config := featureParams(messages, "session/set_config_option"); len(config) != 2 || config[0]["configId"] != "model" || config[1]["configId"] != "thought_level" {
		t.Fatal(config)
	}
	for _, name := range []string{"model", "effort"} {
		t.Run(name+" mismatch", func(t *testing.T) {
			messages, _, err := runACPFeature(t, "feature_agy_"+name+"_mismatch", AgentAgy, harness.Turn{Model: "requested", Effort: "high"})
			if err == nil || !strings.Contains(err.Error(), "agy did not apply "+name) {
				t.Fatalf("err = %v", err)
			}
			if len(featureParams(messages, "session/prompt")) != 0 {
				t.Fatal("sent prompt after config failure")
			}
		})
	}
}

func TestACPAgyFullAccessLeavesPlanMode(t *testing.T) {
	messages, _, err := runACPFeature(t, "feature_agy_current_plan", AgentAgy, harness.Turn{Mode: harness.ModeFullAccess})
	if err != nil {
		t.Fatal(err)
	}
	modes := featureParams(messages, "session/set_mode")
	if len(modes) != 1 || modes[0]["modeId"] != "default" {
		t.Fatalf("modes = %v", modes)
	}
}

func TestACPCancelDoesNotCloseSession(t *testing.T) {
	previous := agyCancelWait
	agyCancelWait = 200 * time.Millisecond
	t.Cleanup(func() { agyCancelWait = previous })
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	h := New(Options{Agent: AgentAgy, Binary: os.Args[0], Env: []string{fakeACPEnv + "=feature_cancel", "MCODE_TEST_ACP_TRACE=" + trace}})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := h.Run(ctx, harness.Turn{WorkDir: t.TempDir(), ThreadID: "cancel"}, func(event harness.Event) {
		if event.Type == harness.EventText {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "session/close") {
		t.Fatal("closed cancelled session")
	}
	if roots := h.ProcessRootPIDs("cancel"); len(roots) != 0 {
		t.Fatalf("leaked roots %v", roots)
	}
}

// failingApprover 按给定错误答复审批，模拟本轮取消。
type failingApprover struct{ err error }

func (a failingApprover) Approve(context.Context, harness.ApprovalRequest) (harness.Decision, error) {
	return harness.DecisionDeny, a.err
}

// permissionReply 取出假 Agent 收到的 session/request_permission 应答。
func permissionReply(t *testing.T, messages []map[string]any) map[string]any {
	t.Helper()
	for _, msg := range messages {
		if msg["method"] != nil || msg["id"] != float64(9010) {
			continue
		}
		result, ok := msg["result"].(map[string]any)
		if !ok {
			t.Fatalf("permission result = %v", msg)
		}
		outcome, ok := result["outcome"].(map[string]any)
		if !ok {
			t.Fatalf("permission outcome = %v", msg)
		}
		return outcome
	}
	t.Fatal("missing permission reply")
	return nil
}

func TestAgyPermissionOptionByKind(t *testing.T) {
	for _, tc := range []struct {
		name, feature, wanted string
		decision              harness.Decision
	}{
		{"allow once", "feature_permission_once", "once", harness.DecisionAllow},
		{"reject once", "feature_permission_once", "deny", harness.DecisionDeny},
		{"missing allow once", "feature_permission", "", harness.DecisionAllow},
		{"missing reject once", "feature_permission_missing_reject", "", harness.DecisionDeny},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages, _, err := runACPFeature(t, tc.feature, AgentAgy, harness.Turn{
				Approver: &mockApprover{decision: tc.decision},
			})
			if err != nil {
				t.Fatal(err)
			}
			reply := permissionReply(t, messages)
			if tc.wanted == "" {
				if reply["outcome"] != "cancelled" || reply["optionId"] != nil {
					t.Fatalf("approval reply = %v, want cancelled", reply)
				}
			} else if reply["outcome"] != "selected" || reply["optionId"] != tc.wanted {
				t.Fatalf("approval reply = %v, want %s", reply, tc.wanted)
			}
		})
	}
	t.Run("switch_mode is a regular approval", func(t *testing.T) {
		approver := &recordingApprover{}
		messages, _, err := runACPFeature(t, "feature_permission_switch_mode", AgentAgy, harness.Turn{
			Mode:            harness.ModeReadOnly,
			InteractionMode: "plan",
			Approver:        approver,
		})
		if err != nil {
			t.Fatal(err)
		}
		if reply := permissionReply(t, messages); reply["outcome"] != "selected" || reply["optionId"] != "once" {
			t.Fatalf("approval reply = %v", reply)
		}
		approver.mu.Lock()
		defer approver.mu.Unlock()
		if len(approver.reqs) != 1 || approver.reqs[0].Kind == harness.RequestKindPlan || approver.reqs[0].Plan != "" {
			t.Fatalf("expected a regular approval, got %+v", approver.reqs)
		}
	})
}

func TestAgyFullAccessPermission(t *testing.T) {
	for _, tc := range []struct {
		name, override, wanted string
		wantApprover           bool
	}{
		{name: "ordinary turn", wanted: "once"},
		{name: "hard-locked command", wanted: "deny", wantApprover: true},
		{name: "unlocked command", override: "approved", wanted: "once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approver := &mockApprover{decision: harness.DecisionDeny}
			messages, _, err := runACPFeature(t, "feature_permission_command", AgentAgy, harness.Turn{
				Mode:             harness.ModeFullAccess,
				HardLocked:       tc.wantApprover || tc.override != "",
				HardLockOverride: tc.override,
				Approver:         approver,
			})
			if err != nil {
				t.Fatal(err)
			}
			if reply := permissionReply(t, messages); reply["outcome"] != "selected" || reply["optionId"] != tc.wanted {
				t.Fatalf("approval reply = %v, want %s", reply, tc.wanted)
			}
			approver.mu.Lock()
			defer approver.mu.Unlock()
			if approver.called != tc.wantApprover {
				t.Fatalf("approver called = %v, want %v", approver.called, tc.wantApprover)
			}
		})
	}
}

func TestAgyPlanTurnFullAccessPermission(t *testing.T) {
	for _, tc := range []struct {
		name         string
		planExitMode harness.PermissionMode
		wanted       string
		wantApprover bool
	}{
		{name: "full access", planExitMode: harness.ModeFullAccess, wanted: "once"},
		{name: "supervised", planExitMode: harness.ModeSupervised, wanted: "deny", wantApprover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			approver := &mockApprover{decision: harness.DecisionDeny}
			messages, _, err := runACPFeature(t, "feature_permission_command", AgentAgy, harness.Turn{
				Mode:            harness.ModeReadOnly,
				InteractionMode: "plan",
				PlanExitMode:    tc.planExitMode,
				Approver:        approver,
			})
			if err != nil {
				t.Fatal(err)
			}
			if reply := permissionReply(t, messages); reply["outcome"] != "selected" || reply["optionId"] != tc.wanted {
				t.Fatalf("approval reply = %v, want %s", reply, tc.wanted)
			}
			approver.mu.Lock()
			defer approver.mu.Unlock()
			if approver.called != tc.wantApprover {
				t.Fatalf("approver called = %v, want %v", approver.called, tc.wantApprover)
			}
		})
	}
}

type agyCancelApprover struct{ cancel context.CancelFunc }

func (a agyCancelApprover) Approve(ctx context.Context, _ harness.ApprovalRequest) (harness.Decision, error) {
	a.cancel()
	return harness.DecisionDeny, ctx.Err()
}

func TestAgyCancelWaitsForPrompt(t *testing.T) {
	for _, resident := range []bool{true, false} {
		t.Run(fmt.Sprintf("resident=%t", resident), func(t *testing.T) {
			f := newACPHoldFixture(t, "feature_agy_cancel")
			f.h.opts.HoldBackgroundTasks = resident
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			turn := f.turn
			turn.Mode = harness.ModeSupervised
			turn.Approver = agyCancelApprover{cancel: cancel}
			var pid int
			var done bool
			err := f.h.Run(ctx, turn, func(event harness.Event) {
				if event.Type == harness.EventSession {
					pid = f.h.ProcessRootPIDs(turn.ThreadID)[0]
				}
				done = done || event.Type == harness.EventDone
			})
			if !errors.Is(err, context.Canceled) || done {
				t.Fatalf("Run = %v, emitted done = %t", err, done)
			}
			messages := f.messages(t)
			if cancels := featureParams(messages, "session/cancel"); len(cancels) != 1 || cancels[0]["sessionId"] != "feature-session" {
				t.Fatalf("session/cancel = %v", cancels)
			}
			if outcome := permissionReply(t, messages); outcome["outcome"] != "cancelled" {
				t.Fatalf("permission outcome = %v", outcome)
			}
			if !resident {
				if len(f.h.ProcessRootPIDs(turn.ThreadID)) != 0 || syscall.Kill(pid, 0) == nil {
					t.Fatal("non-resident process leaked")
				}
				return
			}
			if !f.h.HasResidentProcess(turn.ThreadID) || !reflect.DeepEqual(f.h.ProcessRootPIDs(turn.ThreadID), []int{pid}) {
				t.Fatal("cancelled prompt lost the resident process")
			}
			turn.SessionID = "feature-session"
			reuseCtx, cancelReuse := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancelReuse()
			reused, err := f.h.RunResident(reuseCtx, turn, func(event harness.Event) {
				if event.Type == harness.EventSession && !reflect.DeepEqual(f.h.ProcessRootPIDs(turn.ThreadID), []int{pid}) {
					t.Error("next prompt used a different process")
				}
			})
			if !reused || err != nil {
				t.Fatalf("RunResident = %t, %v", reused, err)
			}
			messages = f.messages(t)
			prompts := featureParams(messages, "session/prompt")
			if len(prompts) != 2 || prompts[0]["sessionId"] != prompts[1]["sessionId"] ||
				len(featureParams(messages, "initialize")) != 1 || len(featureParams(messages, "authenticate")) != 1 ||
				len(featureParams(messages, "session/new")) != 1 || len(featureParams(messages, "session/resume")) != 0 {
				t.Fatalf("process or session not reused: %v", messages)
			}
		})
	}
}

func TestAgyCancelKillsAfterTimeout(t *testing.T) {
	previous := agyCancelWait
	agyCancelWait = 200 * time.Millisecond
	t.Cleanup(func() { agyCancelWait = previous })
	for _, mode := range []string{"feature_agy_cancel_timeout", "feature_agy_cancel_exit"} {
		t.Run(mode, func(t *testing.T) {
			f := newACPHoldFixture(t, mode)
			f.h.opts.StopKillGrace = 50 * time.Millisecond
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var pid int
			var cancelAt time.Time
			err := f.h.Run(ctx, f.turn, func(event harness.Event) {
				switch event.Type {
				case harness.EventSession:
					pid = f.h.ProcessRootPIDs(f.turn.ThreadID)[0]
				case harness.EventText:
					cancelAt = time.Now()
					cancel()
				case harness.EventDone:
					t.Error("emitted done for a cancelled prompt")
				}
			})
			if !errors.Is(err, context.Canceled) || cancelAt.IsZero() || time.Since(cancelAt) > 8*time.Second {
				t.Fatalf("Run = %v, time since cancellation = %s", err, time.Since(cancelAt))
			}
			if mode == "feature_agy_cancel_timeout" && time.Since(cancelAt) < agyCancelWait {
				t.Fatal("process shut down before the cancellation timeout")
			}
			if len(featureParams(f.messages(t), "session/cancel")) != 1 {
				t.Fatal("agent did not receive exactly one session/cancel")
			}
			if f.h.HasResidentProcess(f.turn.ThreadID) || len(f.h.ProcessRootPIDs(f.turn.ThreadID)) != 0 || syscall.Kill(pid, 0) == nil {
				t.Fatal("agent process was not reaped")
			}
		})
	}
}

func TestAgyPromptStepErrorStall(t *testing.T) {
	prevPoll := agyStepPollInterval
	prevStall := agyStepErrorStall
	prevCancel := agyCancelWait
	agyStepPollInterval = 20 * time.Millisecond
	agyStepErrorStall = 60 * time.Millisecond
	agyCancelWait = 50 * time.Millisecond
	t.Cleanup(func() {
		agyStepPollInterval = prevPoll
		agyStepErrorStall = prevStall
		agyCancelWait = prevCancel
	})

	t.Run("step_type=17 stalls and returns error", func(t *testing.T) {
		geminiDir := filepath.Join(t.TempDir(), "gemini # account")
		trace := filepath.Join(t.TempDir(), "trace.jsonl")
		h := New(Options{
			Agent:                AgentAgy,
			Binary:               os.Args[0],
			DisableLaunchSpacing: true,
			Env:                  []string{fakeACPEnv + "=feature_agy_stall_error", "MCODE_TEST_ACP_TRACE=" + trace},
			TurnArgs:             func(harness.Turn) []string { return []string{"--gemini_dir=" + geminiDir} },
		})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := h.Run(ctx, harness.Turn{ThreadID: "stall-err-thread", WorkDir: t.TempDir(), Prompt: "hi"}, func(harness.Event) {})
		if err == nil || !strings.Contains(err.Error(), "Bad Gateway") {
			t.Fatalf("Run error = %v, want substring %q", err, "Bad Gateway")
		}
		if len(h.ProcessRootPIDs("stall-err-thread")) != 0 {
			t.Fatalf("leaked process roots: %v", h.ProcessRootPIDs("stall-err-thread"))
		}
	})

	t.Run("non-error step does not stall", func(t *testing.T) {
		geminiDir := filepath.Join(t.TempDir(), "gemini # account")
		trace := filepath.Join(t.TempDir(), "trace.jsonl")
		h := New(Options{
			Agent:                AgentAgy,
			Binary:               os.Args[0],
			DisableLaunchSpacing: true,
			Env:                  []string{fakeACPEnv + "=feature_agy_stall_ok", "MCODE_TEST_ACP_TRACE=" + trace},
			TurnArgs:             func(harness.Turn) []string { return []string{"--gemini_dir=" + geminiDir} },
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pendingCh := make(chan struct{})
		var pendingOnce sync.Once
		errCh := make(chan error, 1)
		go func() {
			errCh <- h.Run(ctx, harness.Turn{ThreadID: "stall-ok-thread", WorkDir: t.TempDir(), Prompt: "hi"}, func(e harness.Event) {
				if e.Type == harness.EventText {
					pendingOnce.Do(func() { close(pendingCh) })
				}
			})
		}()

		select {
		case <-pendingCh:
		case err := <-errCh:
			t.Fatalf("Run exited unexpectedly before pending event: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for fake agy session/prompt")
		}

		// 等待超过 4 倍 stall 时长，确认非 step_type=17 时不会误判卡死。
		select {
		case err := <-errCh:
			t.Fatalf("Run exited early when latest step is not error: %v", err)
		case <-time.After(4 * agyStepErrorStall):
		}

		cancel()
		var err error
		select {
		case err = <-errCh:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for Run to exit after cancel")
		}
		if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "Bad Gateway") {
			t.Fatalf("Run error = %v, want context.Canceled without Bad Gateway", err)
		}
		if len(h.ProcessRootPIDs("stall-ok-thread")) != 0 {
			t.Fatalf("leaked process roots: %v", h.ProcessRootPIDs("stall-ok-thread"))
		}
	})
}

func TestAgyPromptResponseSkipsCancel(t *testing.T) {
	previousWait := agyCancelWait
	agyCancelWait = 200 * time.Millisecond
	t.Cleanup(func() { agyCancelWait = previousWait })

	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	conn := NewConn(clientR, clientW)
	t.Cleanup(func() {
		_ = conn.Close()
		_ = serverW.Close()
		_ = serverR.Close()
	})

	responseQueued := make(chan struct{})
	conn.SetNotificationHandler(func(method string, _ json.RawMessage) {
		if method == "test/barrier" {
			close(responseQueued)
		}
	})

	allowSelect := make(chan struct{})
	previousHook := beforeAgyPromptSelect
	beforeAgyPromptSelect = func() {
		<-allowSelect
	}
	t.Cleanup(func() { beforeAgyPromptSelect = previousHook })

	var receivedMu sync.Mutex
	var received []map[string]any
	peerDone := make(chan struct{})
	go func() {
		defer close(peerDone)
		scanner := bufio.NewScanner(serverR)
		for scanner.Scan() {
			var msg map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
				continue
			}
			receivedMu.Lock()
			received = append(received, msg)
			receivedMu.Unlock()
			if msg["method"] == "session/prompt" {
				resp, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      msg["id"],
					"result":  map[string]any{"stopReason": "end_turn"},
				})
				_, _ = serverW.Write(append(resp, '\n'))
				_, _ = serverW.Write([]byte(`{"jsonrpc":"2.0","method":"test/barrier"}` + "\n"))
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var result struct {
		StopReason string `json:"stopReason"`
	}
	callErrCh := make(chan error, 1)
	go func() {
		callErrCh <- conn.callAgyPrompt(ctx, map[string]any{"sessionId": "s1"}, &result)
	}()

	select {
	case <-responseQueued:
	case <-time.After(5 * time.Second):
		close(allowSelect)
		t.Fatal("timed out waiting for prompt response to enter call.ch")
	}

	// 先让应答进入 call.ch，再取消 ctx，最后放行 select，确保两者同时就绪。
	cancel()
	close(allowSelect)

	select {
	case err := <-callErrCh:
		if err != nil {
			t.Fatalf("callAgyPrompt = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for callAgyPrompt")
	}
	if result.StopReason != "end_turn" {
		t.Fatalf("StopReason = %q, want %q", result.StopReason, "end_turn")
	}

	// 关闭连接，确保所有已发出的写操作都被对端读完后再检查 session/cancel。
	_ = conn.Close()
	<-peerDone

	receivedMu.Lock()
	defer receivedMu.Unlock()
	if cancels := featureParams(received, "session/cancel"); len(cancels) != 0 {
		t.Fatalf("unexpected session/cancel: %v", cancels)
	}
	if prompts := featureParams(received, "session/prompt"); len(prompts) != 1 {
		t.Fatalf("session/prompt count = %d, want 1", len(prompts))
	}
}

func TestAgyEagerAuthenticate(t *testing.T) {
	for _, test := range []struct {
		mode, sessionID, setupMethod string
		wantsAuth, wantsError        bool
	}{
		{mode: "feature_agy_auth", setupMethod: "session/new", wantsAuth: true},
		{mode: "feature_agy_auth", sessionID: "resumed", setupMethod: "session/resume", wantsAuth: true},
		{mode: "feature_agy_no_personal_auth", setupMethod: "session/new"},
		{mode: "feature_agy_auth_error", wantsAuth: true, wantsError: true},
	} {
		t.Run(test.mode+test.setupMethod, func(t *testing.T) {
			messages, _, err := runACPFeature(t, test.mode, AgentAgy, harness.Turn{SessionID: test.sessionID})
			if (err != nil) != test.wantsError || test.wantsError && !strings.Contains(err.Error(), "acp authenticate failed") {
				t.Fatalf("Run = %v", err)
			}
			var methods []string
			for _, message := range messages {
				method, _ := message["method"].(string)
				switch method {
				case "initialize", "authenticate", "session/new", "session/resume":
					methods = append(methods, method)
				}
			}
			want := []string{"initialize"}
			if test.wantsAuth {
				want = append(want, "authenticate")
				if auth := featureParams(messages, "authenticate"); len(auth) != 1 || auth[0]["methodId"] != "oauth-personal" {
					t.Fatalf("authenticate = %v", auth)
				}
			}
			if test.setupMethod != "" {
				want = append(want, test.setupMethod)
			}
			if !reflect.DeepEqual(methods, want) {
				t.Fatalf("setup calls = %v, want %v", methods, want)
			}
		})
	}
}

func TestACPPlanUpdateEmitsUpdatePlan(t *testing.T) {
	_, events, err := runACPFeature(t, "feature_plan", AgentOpenCode, harness.Turn{ID: "test-turn"})
	if err != nil {
		t.Fatal(err)
	}
	var tools []harness.Event
	for _, event := range events {
		if event.ToolName == "update_plan" {
			tools = append(tools, event)
		}
	}
	if len(tools) != 4 || tools[0].ToolID == tools[2].ToolID {
		t.Fatalf("plan tools = %v", tools)
	}
	type entry struct {
		Content string `json:"content"`
		Status  string `json:"status"`
	}
	for index, status := range []string{"in_progress", "completed"} {
		start, end := tools[index*2], tools[index*2+1]
		if start.Type != harness.EventToolStart || start.ToolID == "" || end.Type != harness.EventToolEnd || end.ToolID != start.ToolID {
			t.Fatalf("plan tool pair = %v, %v", start, end)
		}
		var input struct {
			Plan []entry `json:"plan"`
		}
		want := []entry{{"Draft", "completed"}, {"Check", status}, {"Send", "pending"}}
		if err := json.Unmarshal(start.Input, &input); err != nil || !reflect.DeepEqual(input.Plan, want) {
			t.Fatalf("plan input = %s, err = %v", start.Input, err)
		}
	}
}

func TestACPPermissionCancelledOutcome(t *testing.T) {
	// 审批因取消返回错误时，按 ACP 规范应答 cancelled，且不带 optionId。
	messages, _, err := runACPFeature(t, "feature_permission", AgentAgy, harness.Turn{
		Approver: failingApprover{err: fmt.Errorf("approval aborted: %w", context.Canceled)},
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled := permissionReply(t, messages)
	if cancelled["outcome"] != "cancelled" {
		t.Fatalf("outcome = %v", cancelled)
	}
	if _, ok := cancelled["optionId"]; ok {
		t.Fatalf("cancelled outcome carries optionId: %v", cancelled)
	}

	// 对照：普通拒绝仍按选中的 reject option 回复。
	messages, _, err = runACPFeature(t, "feature_permission", AgentAgy, harness.Turn{
		Approver: &mockApprover{decision: harness.DecisionDeny},
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected := permissionReply(t, messages)
	if rejected["outcome"] != "selected" || rejected["optionId"] != "deny" {
		t.Fatalf("outcome = %v", rejected)
	}
}

func TestPickAuthMethodSkipsTerminal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		methods []acpAuthMethod
		want    string
	}{
		{name: "skips terminal", methods: []acpAuthMethod{{ID: "login", Type: "terminal"}, {ID: "agent", Type: "agent"}}, want: "agent"},
		{name: "terminal only", methods: []acpAuthMethod{{ID: "login", Type: "terminal"}}, want: ""},
		{name: "empty", methods: nil, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickAuthMethod(tc.methods); got != tc.want {
				t.Fatalf("pickAuthMethod(%v) = %q, want %q", tc.methods, got, tc.want)
			}
		})
	}
}

func TestAgyToolUpdateUsesCombinedOutput(t *testing.T) {
	_, events, err := runACPFeature(t, "feature_agy_tool_result", AgentAgy, harness.Turn{})
	if err != nil {
		t.Fatal(err)
	}
	var end, change *harness.Event
	for i := range events {
		switch events[i].Type {
		case harness.EventToolEnd:
			if events[i].ToolID == "command" {
				end = &events[i]
			}
		case harness.EventFileChange:
			if events[i].Path == "agy-edit.txt" {
				change = &events[i]
			}
		}
	}
	if end == nil || end.Output != "hello\nexit code 1" || end.IsError {
		t.Fatalf("命令结束事件 = %+v", end)
	}
	if change == nil || !strings.Contains(change.Patch, "-old line") || !strings.Contains(change.Patch, "+new line") {
		t.Fatalf("编辑文件变更 = %+v", change)
	}
}

func TestACPToolCallUpdateContentArray(t *testing.T) {
	_, events, err := runACPFeature(t, "feature_tool_array", AgentAgy, harness.Turn{})
	if err != nil {
		t.Fatal(err)
	}
	ends := map[string][]harness.Event{}
	for _, event := range events {
		if event.Type == harness.EventToolEnd {
			ends[event.ToolID] = append(ends[event.ToolID], event)
		}
	}
	if len(ends["a"]) != 1 || ends["a"][0].Output != "第一段第二段" || len(ends["z"]) != 1 || ends["z"][0].Output != "备用" {
		t.Fatalf("工具结束事件 = %+v", ends)
	}
}

func TestACPFinishesOpenToolsAtTurnEnd(t *testing.T) {
	for _, mode := range []string{"feature_tool_open", "feature_tool_error"} {
		t.Run(mode, func(t *testing.T) {
			_, events, err := runACPFeature(t, mode, AgentAgy, harness.Turn{})
			failed := mode == "feature_tool_error"
			if (err != nil) != failed {
				t.Fatalf("错误 = %v", err)
			}
			var ends []harness.Event
			done := false
			for _, event := range events {
				if event.Type == harness.EventDone {
					done = true
				}
				if event.Type == harness.EventToolEnd {
					if done {
						t.Fatal("工具结束事件出现在回合结束后")
					}
					ends = append(ends, event)
				}
			}
			if done == failed || len(ends) != 2 || ends[0].ToolID != "a" || ends[1].ToolID != "z" {
				t.Fatalf("事件 = %+v", events)
			}
			for _, end := range ends {
				if end.IsError != failed || (failed && end.Output != "本轮中断，工具未完成") {
					t.Fatalf("工具结束事件 = %+v", end)
				}
			}
		})
	}
}

func TestACPAgyMCPUsesHTTPWithoutCapability(t *testing.T) {
	messages, _, err := runACPFeature(t, "feature_mcp_http", AgentAgy, harness.Turn{})
	if err != nil {
		t.Fatal(err)
	}
	params := featureParams(messages, "session/new")
	if len(params) != 1 {
		t.Fatalf("会话参数 = %v", params)
	}
	servers, ok := params[0]["mcpServers"].([]any)
	if !ok || len(servers) != 1 {
		t.Fatalf("MCP 服务 = %v", params[0]["mcpServers"])
	}
	server, ok := servers[0].(map[string]any)
	if !ok || server["type"] != "http" || server["command"] != nil {
		t.Fatalf("MCP 服务 = %v", servers[0])
	}
}

func TestResumeMissingSessionReportsLostSession(t *testing.T) {
	messages, _, err := runACPFeature(t, "feature_resume_missing", AgentAgy, harness.Turn{SessionID: "missing-session-123"})
	if err == nil {
		t.Fatal("expected error when resuming missing session")
	}
	msg := err.Error()
	if !strings.Contains(msg, "原会话") || !strings.Contains(msg, "请新开会话") {
		t.Fatalf("unexpected error message: %q", msg)
	}
	if strings.Contains(msg, "-32603") {
		t.Fatalf("error message should not expose -32603: %q", msg)
	}
	if len(featureParams(messages, "session/new")) != 0 {
		t.Fatalf("should not fall back to session/new: %v", messages)
	}
}

func fakeACPHoldRecords(dir string) string {
	return filepath.Join(agyGeminiDir(os.Args[1:], os.Environ()), dir, "brain", fakeHoldSession, ".system_generated")
}

func appendFakeACPHoldTask(dir string, task int, command string) {
	content := fmt.Sprintf("Tool is running as a background task with task id: %s/task-%d", fakeHoldSession, task)
	if command != "" {
		content += "\nTask Description: " + command + "\nTask logs are available at: file:///task.log"
	}
	appendFakeACPHoldTranscript(dir, map[string]any{"step_index": task, "status": "RUNNING", "content": content})
}

func appendFakeACPHoldTranscript(dir string, step map[string]any) {
	path := filepath.Join(dir, "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		panic(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(step); err != nil {
		panic(err)
	}
}

func writeFakeACPHoldUsage(index int) {
	path := agySessionDB(agyGeminiDir(os.Args[1:], os.Environ()), fakeHoldSession)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		panic(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		panic(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE IF NOT EXISTS steps (idx INTEGER, step_type INTEGER, step_payload BLOB, metadata BLOB)"); err != nil {
		panic(err)
	}
	if _, err := db.Exec("INSERT INTO steps (idx, step_type, metadata) VALUES (?, 1, x'4a081007180b280d4811')", index); err != nil {
		panic(err)
	}
}

func writeFakeACPHoldLog(dir, output string) {
	logs := filepath.Join(dir, "tasks")
	if err := os.MkdirAll(logs, 0700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "task-1.log"), []byte(output), 0600); err != nil {
		panic(err)
	}
}

// 测试通过控制文件追加完成通知，假进程再把它们写入 agy messages 目录。
func runFakeACPBackground(mode string, write func(any)) {
	control := os.Getenv("MCODE_TEST_ACP_COMPLETE")
	if control == "" {
		return
	}
	dir := "antigravity-cli"
	if mode == "feature_hold_legacy" {
		dir = "antigravity-acp"
	}
	records := fakeACPHoldRecords(dir)
	path := filepath.Join(records, "messages")
	notify := func(update map[string]any) {
		write(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"update": update}})
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var offset int
	for range ticker.C {
		data, err := os.ReadFile(control)
		if err != nil || len(data) <= offset {
			continue
		}
		spontaneous := mode == "feature_hold_spontaneous" && offset == 0
		if spontaneous {
			writeFakeACPHoldLog(records, "后台输出\n")
			notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "idle-tool", "name": "read"})
			notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "空闲续写"}})
			notify(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "idle-background-tool", "name": "run_command"})
			appendFakeACPHoldTask(records, 2, "")
		}
		if mode == "feature_hold_final_step" {
			appendFakeACPHoldTranscript(records, map[string]any{"type": "PLANNER_RESPONSE", "status": "DONE", "tool_calls": []any{map[string]any{"name": "read"}}})
			notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "第一段"}})
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			panic(err)
		}
		if err := os.MkdirAll(path, 0700); err != nil {
			panic(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data[offset:])), "\n") {
			var message map[string]any
			if err := json.Unmarshal([]byte(line), &message); err != nil {
				panic(err)
			}
			name := fmt.Sprintf("%d.json", offset)
			if err := os.WriteFile(filepath.Join(path, name), []byte(line), 0600); err != nil {
				panic(err)
			}
			offset += len(line) + 1
		}
		offset = len(data)
		if spontaneous {
			for range ticker.C {
				if _, err := os.Stat(control + ".attach"); err == nil {
					break
				}
			}
			writeFakeACPHoldUsage(2)
			writeFakeACPHoldUsage(3)
			notify(map[string]any{"sessionUpdate": "usage_update", "used": 400, "size": 1024})
			notify(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "idle-tool", "status": "completed", "output": "工具输出"})
			notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "实时续写"}})
		}
		if mode == "feature_hold_final_step" {
			for range ticker.C {
				if _, err := os.Stat(control + ".attach"); err == nil {
					break
				}
			}
			notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "第二段"}})
		}
		if mode == "feature_hold_late_spontaneous" {
			for range ticker.C {
				if _, err := os.Stat(control + ".late"); err == nil {
					break
				}
			}
			notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "迟到续写"}})
			appendFakeACPHoldTranscript(records, map[string]any{"type": "PLANNER_RESPONSE", "status": "DONE"})
		}
		if mode == "feature_hold_idle" {
			for i := 0; i < 15; i++ {
				notify(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"text": "idle"}})
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

type acpHoldFixture struct {
	h       *Harness
	turn    harness.Turn
	trace   string
	control string
}

func newACPHoldFixture(t *testing.T, mode string) acpHoldFixture {
	t.Helper()
	dir := t.TempDir()
	f := acpHoldFixture{
		turn:  harness.Turn{ThreadID: "hold-thread", WorkDir: dir, Prompt: "start"},
		trace: filepath.Join(dir, "trace.jsonl"), control: filepath.Join(dir, "complete.jsonl"),
	}
	f.h = New(Options{
		Agent: AgentAgy, Binary: os.Args[0], HoldBackgroundTasks: true,
		Env:       []string{fakeACPEnv + "=" + mode, "MCODE_TEST_ACP_TRACE=" + f.trace, "MCODE_TEST_ACP_COMPLETE=" + f.control},
		ExtraArgs: []string{"--gemini_dir", dir},
	})
	if mode == "feature_hold_legacy" {
		f.h.opts.ExtraArgs = nil
		f.h.opts.Env = append(f.h.opts.Env, "ANTIGRAVITY_GEMINI_DIR="+dir)
	}
	t.Cleanup(func() { f.h.EvictThread(f.turn.ThreadID) })
	return f
}

func (f acpHoldFixture) run(t *testing.T, turn harness.Turn) []harness.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var events []harness.Event
	var mu sync.Mutex
	if err := f.h.Run(ctx, turn, func(e harness.Event) { mu.Lock(); events = append(events, e); mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return append([]harness.Event(nil), events...)
}

func (f acpHoldFixture) complete(t *testing.T, task int, status string) {
	t.Helper()
	file, err := os.OpenFile(f.control, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := json.NewEncoder(file).Encode(map[string]any{"content": fmt.Sprintf("Task id %q %s with result:\noutput", fmt.Sprintf("%s/task-%d", fakeHoldSession, task), status), "timestamp": time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
}

func (f acpHoldFixture) messages(t *testing.T) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(f.trace)
	if err != nil {
		t.Fatal(err)
	}
	var messages []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var message map[string]any
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	return messages
}

func waitACPHold(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-deadline.C:
			t.Fatal("等待 ACP 后台任务状态超时")
		case <-ticker.C:
		}
	}
}

func TestACPHoldBackgroundTasks(t *testing.T) {
	for _, mode := range []string{"feature_hold_content", "feature_hold_raw", "feature_hold_output", "feature_hold_legacy"} {
		t.Run(mode, func(t *testing.T) {
			f := newACPHoldFixture(t, mode)
			f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
			calls := make(chan string, 10)
			f.h.SetUnsolicitedTurnHandler(func(threadID string) {
				// 回调必须在表锁外运行。
				if !f.h.HasUnsolicitedOutput(threadID) {
					t.Error("通知没有待上报任务")
				}
				calls <- threadID
			})
			events := f.run(t, f.turn)
			var starts []harness.Event
			for _, event := range events {
				if event.Type == harness.EventBackgroundTaskStart {
					starts = append(starts, event)
				}
			}
			if len(starts) != 2 {
				t.Fatalf("background starts = %v", starts)
			}
			for i, event := range starts {
				if event.ToolID != fmt.Sprintf("hold-tool-%d", i+1) || event.TaskID != fmt.Sprintf("%s/task-%d", fakeHoldSession, i+1) {
					t.Fatalf("background start = %+v", event)
				}
			}
			roots := f.h.ProcessRootPIDs(f.turn.ThreadID)
			if len(roots) != 1 || syscall.Kill(roots[0], 0) != nil {
				t.Fatalf("常驻进程已退出: %v", roots)
			}
			if got := len(featureParams(f.messages(t), "session/close")); got != 0 {
				t.Fatalf("premature close count = %d", got)
			}
			f.complete(t, 1, "finished")
			waitACPHold(t, func() bool { return len(calls) > 0 })
			if threadID := <-calls; threadID != f.turn.ThreadID {
				t.Fatalf("notified thread = %s", threadID)
			}
			// 再等一次轮询，尚未取走的同一批任务不能重复通知。
			timer := time.NewTimer(80 * time.Millisecond)
			select {
			case <-calls:
				t.Error("同一批后台任务重复通知")
			case <-timer.C:
			}
			timer.Stop()
			f.h.EvictThread(f.turn.ThreadID)
			if syscall.Kill(roots[0], 0) == nil || len(f.h.ProcessRootPIDs(f.turn.ThreadID)) != 0 || f.h.HasUnsolicitedOutput(f.turn.ThreadID) {
				t.Fatal("驱逐后进程或后台任务状态未清理")
			}
		})
	}
}

func TestACPHoldIgnoresMarkerInNonCommandTool(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_edit_echo")
	events := f.run(t, f.turn)
	started, ended, done := false, false, false
	for _, event := range events {
		if event.Type == harness.EventBackgroundTaskStart {
			t.Fatalf("非命令工具的文件回显被当成后台任务: %+v", event)
		}
		switch event.Type {
		case harness.EventToolStart:
			started = started || event.ToolID == "edit-echo"
		case harness.EventToolEnd:
			if event.ToolID != "edit-echo" {
				continue
			}
			if event.IsError {
				t.Fatalf("编辑工具结束事件为错误: %+v", event)
			}
			ended = true
		case harness.EventDone:
			done = done || event.StopReason == "end_turn"
		}
	}
	if !started || !ended || !done {
		t.Fatalf("工具或回合事件缺失: %v", events)
	}
	if got := len(featureParams(f.messages(t), "session/close")); got != 1 {
		t.Fatalf("session/close 次数 = %d", got)
	}
	if len(f.h.ProcessRootPIDs(f.turn.ThreadID)) != 0 || f.h.HasResidentProcess(f.turn.ThreadID) {
		t.Fatal("没有后台任务却保留了进程")
	}
}

func TestACPHoldInRoundCompletion(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_inround")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	calls := make(chan string, 10)
	f.h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	events := f.run(t, f.turn)
	endedAt, doneAt := -1, -1
	for i, event := range events {
		switch event.Type {
		case harness.EventBackgroundTaskEnd:
			if event.TaskID != fakeHoldSession+"/task-1" || event.TaskStatus != "completed" || event.Output != "轮内输出\n" {
				t.Fatalf("结束事件 = %+v", event)
			}
			endedAt = i
		case harness.EventDone:
			doneAt = i
		}
	}
	if endedAt < 0 || doneAt < 0 || endedAt > doneAt {
		t.Fatalf("本轮内完成的任务未在完成事件之前上报: %v", events)
	}
	messages := f.messages(t)
	if got := len(featureParams(messages, "session/close")); got != 1 {
		t.Fatalf("session/close 次数 = %d", got)
	}
	if len(f.h.ProcessRootPIDs(f.turn.ThreadID)) != 0 || f.h.HasResidentProcess(f.turn.ThreadID) {
		t.Fatal("本轮内完成的任务仍保留了进程")
	}
	// 超过一个 watcher 周期后也不能再触发续轮。
	time.Sleep(80 * time.Millisecond)
	if len(calls) != 0 || f.h.HasUnsolicitedOutput(f.turn.ThreadID) {
		t.Fatalf("本轮内完成的任务再次触发了续轮: %d", len(calls))
	}
}

func TestACPHoldReconcileDrainsQueued(t *testing.T) {
	const threadID = "hold-thread"
	taskID := fakeHoldSession + "/task-1"
	h := New(Options{HoldBackgroundTasks: true})
	p := &acpProcess{tasks: map[string]string{taskID: "completed"}}
	h.finishedTasks = map[string][]harness.Event{threadID: {{Type: harness.EventBackgroundTaskEnd, TaskID: taskID, TaskStatus: "completed"}}}
	h.unsolicitedNotified = map[string]bool{threadID: true}
	var events []harness.Event
	h.reconcileRoundTasks(p, threadID, func(event harness.Event) { events = append(events, event) })
	if len(events) != 1 || events[0].Type != harness.EventBackgroundTaskEnd || events[0].TaskID != taskID || events[0].TaskStatus != "completed" {
		t.Fatalf("排空事件 = %+v", events)
	}
	if h.HasUnsolicitedOutput(threadID) {
		t.Fatal("队列未排空")
	}
	if _, exists := h.unsolicitedNotified[threadID]; exists {
		t.Fatal("通知状态未清理")
	}
}

func TestACPHoldAttachContinues(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_content")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 50 * time.Millisecond
	calls := make(chan string, 10)
	f.h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	f.run(t, f.turn)
	roots := f.h.ProcessRootPIDs(f.turn.ThreadID)
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	if events := f.run(t, attach); len(events) != 0 {
		t.Fatalf("任务未结束的 Attach 产生了事件: %v", events)
	}
	// 日志中的顺序必须保留，且取消状态要转为 canceled。
	f.complete(t, 2, "was canceled")
	f.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return len(calls) > 0 })
	events := f.run(t, attach)
	if len(events) < 3 || events[0].Type != harness.EventBackgroundTaskEnd || events[0].TaskID != fakeHoldSession+"/task-2" || events[0].TaskStatus != "canceled" || events[1].Type != harness.EventBackgroundTaskEnd || events[1].TaskID != fakeHoldSession+"/task-1" || events[1].TaskStatus != "completed" || events[2].Type != harness.EventSession {
		t.Fatalf("Attach 事件顺序错误: %v", events)
	}
	messages := f.messages(t)
	if len(featureParams(messages, "initialize")) != 1 || len(featureParams(messages, "session/new")) != 1 || len(featureParams(messages, "session/prompt")) != 2 || len(featureParams(messages, "session/close")) != 1 {
		t.Fatalf("会话或进程未复用: %v", messages)
	}
	prompts := featureParams(messages, "session/prompt")
	if prompts[0]["sessionId"] != prompts[1]["sessionId"] || prompts[1]["prompt"].([]any)[0].(map[string]any)["text"] != "后台任务已结束，请根据结果继续。" {
		t.Fatalf("Attach prompt = %v", prompts[1])
	}
	if syscall.Kill(roots[0], 0) == nil || f.h.HasUnsolicitedOutput(f.turn.ThreadID) {
		t.Fatal("所有任务完成后进程或待上报状态未清理")
	}
	if events := f.run(t, attach); len(events) != 0 {
		t.Fatalf("没有任务的 Attach 产生了事件: %v", events)
	}
	if got := len(featureParams(f.messages(t), "initialize")); got != 1 {
		t.Fatalf("空 Attach 启动了新进程: %d", got)
	}
}

func TestACPHoldNormalReuseAndFailure(t *testing.T) {
	previous := agyCancelWait
	agyCancelWait = 200 * time.Millisecond
	t.Cleanup(func() { agyCancelWait = previous })
	for _, mode := range []string{"feature_hold_error", "feature_hold_cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newACPHoldFixture(t, mode)
			f.run(t, f.turn)
			roots := f.h.ProcessRootPIDs(f.turn.ThreadID)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := f.h.Run(ctx, f.turn, func(e harness.Event) {
				if mode == "feature_hold_cancel" && e.Type == harness.EventText {
					// 忙时驱逐不影响当前轮；随后取消必须杀掉进程。
					f.h.EvictThread(f.turn.ThreadID)
					cancel()
				}
			})
			if err == nil || mode == "feature_hold_cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("unexpected error = %v", err)
			}
			messages := f.messages(t)
			if len(featureParams(messages, "initialize")) != 1 || len(featureParams(messages, "session/prompt")) != 2 || syscall.Kill(roots[0], 0) == nil {
				t.Fatalf("复用或失败回收错误: %v", messages)
			}
		})
	}
}

func TestACPHoldProcessExitAndAttachRecovery(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_content")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 50 * time.Millisecond
	exits := make(chan string, 10)
	f.h.SetProcessExitHandler(func(threadID string, before time.Time) {
		if !before.IsZero() || f.h.HasResidentProcess(threadID) {
			t.Error("退出通知仍有常驻进程")
		}
		exits <- threadID
	})
	f.run(t, f.turn)
	f.complete(t, 1, "finished")
	f.complete(t, 2, "finished")
	waitACPHold(t, func() bool {
		if !f.h.HasUnsolicitedOutput(f.turn.ThreadID) {
			return false
		}
		f.h.residentMu.Lock()
		defer f.h.residentMu.Unlock()
		return len(f.h.finishedTasks[f.turn.ThreadID]) == 2
	})
	roots := f.h.ProcessRootPIDs(f.turn.ThreadID)
	if err := syscall.Kill(roots[0], syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitACPHold(t, func() bool { return len(exits) > 0 })
	if threadID := <-exits; threadID != f.turn.ThreadID {
		t.Fatalf("exited thread = %s", threadID)
	}
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	events := f.run(t, attach)
	if len(events) < 2 || events[0].Type != harness.EventBackgroundTaskEnd || events[1].Type != harness.EventBackgroundTaskEnd {
		t.Fatalf("恢复未补报任务结束: %v", events)
	}
	messages := f.messages(t)
	if len(featureParams(messages, "initialize")) != 2 || len(featureParams(messages, "session/resume")) != 1 || len(featureParams(messages, "session/close")) != 1 {
		t.Fatalf("恢复未启动新进程并恢复会话: %v", messages)
	}
}

func TestACPHoldIdleUpdates(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_idle")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	calls := make(chan time.Duration, 10)
	f.h.SetUnsolicitedTurnHandler(func(string) {
		f.h.residentMu.Lock()
		defer f.h.residentMu.Unlock()
		calls <- time.Since(f.h.residents[f.turn.ThreadID].lastUpdate)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var mu sync.Mutex
	eventCount := 0
	if err := f.h.Run(ctx, f.turn, func(harness.Event) { mu.Lock(); eventCount++; mu.Unlock() }); err != nil {
		cancel()
		t.Fatal(err)
	}
	cancel()
	mu.Lock()
	count := eventCount
	mu.Unlock()
	f.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return len(calls) > 0 })
	if quiet := <-calls; quiet >= 3*time.Second {
		t.Fatalf("watcher 仍等待空闲更新静默: %v", quiet)
	}
	mu.Lock()
	defer mu.Unlock()
	if count != eventCount {
		t.Fatalf("空闲通知进入旧轮: %d -> %d", count, eventCount)
	}
}

func TestACPHoldAgySpontaneousReply(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_spontaneous")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 60 * time.Millisecond
	calls := make(chan string, 2)
	f.h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	for _, event := range f.run(t, f.turn) {
		if event.Type == harness.EventText && event.Text == "空闲续写" {
			t.Fatal("空闲续写进入旧轮")
		}
	}
	f.h.residentMu.Lock()
	process := f.h.residents[f.turn.ThreadID]
	f.h.residentMu.Unlock()
	if process == nil {
		t.Fatal("第 0 轮未保留进程")
	}
	f.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return len(calls) > 0 })
	if got := <-calls; got != f.turn.ThreadID {
		t.Fatalf("unsolicited thread = %q", got)
	}
	waitACPHold(t, func() bool {
		f.h.residentMu.Lock()
		defer f.h.residentMu.Unlock()
		return len(process.idleUpdates) == 3
	})
	records := filepath.Dir(filepath.Dir(process.transcriptPath))
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	var events []harness.Event
	replayed := make(chan struct{})
	liveText := make(chan struct{})
	result := make(chan error, 1)
	finished := make(chan struct{})
	defer func() {
		cancel()
		<-finished
	}()
	go func() {
		defer close(finished)
		result <- f.h.Run(ctx, attach, func(event harness.Event) {
			events = append(events, event)
			if event.Type == harness.EventToolStart && event.ToolID == "idle-tool" {
				close(replayed)
			}
			if event.Type == harness.EventText && event.Text == "实时续写" {
				close(liveText)
			}
		})
	}()
	select {
	case <-replayed:
	case err := <-result:
		t.Fatalf("Attach 未回放空闲工具: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	waitACPHold(t, func() bool {
		f.h.residentMu.Lock()
		defer f.h.residentMu.Unlock()
		return time.Since(process.lastUpdate) >= 120*time.Millisecond
	})
	select {
	case err := <-result:
		t.Fatalf("Attach 未等仍在运行的工具: %v", err)
	default:
	}
	if err := os.WriteFile(f.control+".attach", nil, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-liveText:
	case err := <-result:
		t.Fatalf("Attach 未接住实时续写: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	appendFakeACPHoldTranscript(records, map[string]any{"type": "PLANNER_RESPONSE", "status": "DONE"})
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 || events[0].Type != harness.EventBackgroundTaskEnd || events[0].TaskID != fakeHoldSession+"/task-1" || events[0].Output != "后台输出\n" {
		t.Fatalf("Attach 后台任务结束事件 = %+v", events)
	}
	var reply []string
	backgroundStarted, done := false, false
	var usage *harness.Usage
	for _, event := range events {
		switch event.Type {
		case harness.EventToolStart:
			if event.ToolID == "idle-tool" {
				reply = append(reply, "tool_start")
			}
		case harness.EventToolEnd:
			if event.ToolID == "idle-tool" {
				reply = append(reply, event.Output)
			}
		case harness.EventText:
			reply = append(reply, event.Text)
		case harness.EventBackgroundTaskStart:
			backgroundStarted = event.TaskID == fakeHoldSession+"/task-2" && event.ToolID == "idle-background-tool"
		case harness.EventUsage:
			usage = event.Usage
		case harness.EventDone:
			done = event.StopReason == "end_turn"
		}
	}
	if !reflect.DeepEqual(reply, []string{"tool_start", "空闲续写", "工具输出", "实时续写"}) || !backgroundStarted || !done {
		t.Fatalf("Attach 未有序回放或未接住新后台任务: %+v", events)
	}
	if want := (&harness.Usage{InputTokens: 14, OutputTokens: 22, CachedTokens: 26, ContextTokens: 400, ContextWindow: 1024}); !reflect.DeepEqual(usage, want) {
		t.Fatalf("Attach usage = %+v, want %+v", usage, want)
	}
	if len(featureParams(f.messages(t), "session/prompt")) != 1 || !f.h.HasResidentProcess(f.turn.ThreadID) {
		t.Fatal("Attach 重发了 prompt 或未为新后台任务保留进程")
	}
}

func TestACPHoldAgyTaskDescriptionMatch(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_task_description")
	ended := make(map[string]bool)
	var matched []string
	for _, event := range f.run(t, f.turn) {
		switch event.Type {
		case harness.EventToolEnd:
			ended[event.ToolID] = true
		case harness.EventBackgroundTaskStart:
			if !ended[event.ToolID] || event.ToolID == "edit" {
				t.Fatalf("后台任务未绑定已完成的命令: %+v", event)
			}
			matched = append(matched, event.TaskID+"="+event.ToolID)
		}
	}
	want := []string{fakeHoldSession + "/task-1=command-1", fakeHoldSession + "/task-2=command-2"}
	if !reflect.DeepEqual(matched, want) || !ended["edit"] {
		t.Fatalf("后台任务绑定 = %v, want %v; 工具结束 = %v", matched, want, ended)
	}
}

func TestACPHoldAgySpontaneousWaitsForFinalStep(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_final_step")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 60 * time.Millisecond
	f.run(t, f.turn)
	f.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return f.h.HasUnsolicitedOutput(f.turn.ThreadID) })
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	texts := make(chan string, 2)
	result := make(chan error, 1)
	done := make(chan struct{})
	defer func() {
		cancel()
		<-done
	}()
	go func() {
		defer close(done)
		result <- f.h.Run(ctx, attach, func(event harness.Event) {
			if event.Type == harness.EventText {
				texts <- event.Text
			}
		})
	}()
	expectText := func(want string) {
		t.Helper()
		select {
		case text := <-texts:
			if text != want {
				t.Fatalf("Attach 正文 = %q, want %q", text, want)
			}
		case err := <-result:
			t.Fatalf("Attach 在收到 %q 前结束: %v", want, err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	expectText("第一段")
	pause := time.NewTimer(120 * time.Millisecond)
	defer pause.Stop()
	select {
	case err := <-result:
		t.Fatalf("Attach 在最终步前结束: %v", err)
	case <-pause.C:
	}
	if err := os.WriteFile(f.control+".attach", nil, 0600); err != nil {
		t.Fatal(err)
	}
	expectText("第二段")
	fast := time.NewTimer(1500 * time.Millisecond)
	defer fast.Stop()
	records := filepath.Join(f.turn.WorkDir, "antigravity-cli", "brain", fakeHoldSession, ".system_generated")
	appendFakeACPHoldTranscript(records, map[string]any{"type": "PLANNER_RESPONSE", "status": "DONE", "tool_calls": []any{}})
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-fast.C:
		t.Fatal("Attach 未在最终步后及时结束")
	}
	if prompts := featureParams(f.messages(t), "session/prompt"); len(prompts) != 1 {
		t.Fatalf("Attach 补发了 prompt: %+v", prompts)
	}
}

func TestACPHoldAgyLateSpontaneousReply(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_late_spontaneous")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 150 * time.Millisecond
	calls := make(chan string, 2)
	f.h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	f.run(t, f.turn)
	f.h.residentMu.Lock()
	process := f.h.residents[f.turn.ThreadID]
	f.h.residentMu.Unlock()
	if process == nil {
		t.Fatal("第 0 轮未保留进程")
	}
	f.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return len(calls) > 0 })
	if got := <-calls; got != f.turn.ThreadID {
		t.Fatalf("unsolicited thread = %q", got)
	}
	// 改动前 Attach 开始时静默条件已经成立，会立即补发 prompt。
	waitACPHold(t, func() bool {
		f.h.residentMu.Lock()
		defer f.h.residentMu.Unlock()
		return time.Since(process.lastUpdate) >= 180*time.Millisecond
	})
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var mu sync.Mutex
	var events []harness.Event
	result := make(chan error, 1)
	go func() {
		result <- f.h.Run(ctx, attach, func(event harness.Event) {
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
		})
	}()
	// Attach 轮开始约 35ms 后 agy 才推送自发回答。
	time.Sleep(35 * time.Millisecond)
	if err := os.WriteFile(f.control+".late", nil, 0600); err != nil {
		cancel()
		<-result
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var texts []string
	for _, event := range events {
		if event.Type == harness.EventText {
			texts = append(texts, event.Text)
		}
	}
	if !reflect.DeepEqual(texts, []string{"迟到续写"}) {
		t.Fatalf("Attach 未接住迟到的自发回答: %v", events)
	}
	if prompts := featureParams(f.messages(t), "session/prompt"); len(prompts) != 1 {
		t.Fatalf("Attach 在自发回答前补发了 prompt: %+v", prompts)
	}
}

func TestACPHoldAgyNoSpontaneousReply(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_agy_records")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 50 * time.Millisecond
	calls := make(chan string, 2)
	f.h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	f.run(t, f.turn)
	f.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return len(calls) > 0 })
	if got := <-calls; got != f.turn.ThreadID {
		t.Fatalf("unsolicited thread = %q", got)
	}
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	f.run(t, attach)
	prompts := featureParams(f.messages(t), "session/prompt")
	if len(prompts) != 2 {
		t.Fatalf("session/prompt = %+v", prompts)
	}
	want := []any{map[string]any{"type": "text", "text": "后台任务已结束，请根据结果继续。"}}
	if !reflect.DeepEqual(prompts[1]["prompt"], want) {
		t.Fatalf("Attach prompt = %+v", prompts[1])
	}
}

func TestACPHoldAgyRecords(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_agy_records")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 50 * time.Millisecond
	calls := make(chan string, 2)
	f.h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	events := f.run(t, f.turn)
	startAt, toolEndAt := -1, -1
	for i, event := range events {
		if event.Type == harness.EventBackgroundTaskStart {
			if event.TaskID != fakeHoldSession+"/task-1" || event.ToolID != "agy-record-tool" {
				t.Fatalf("后台任务开始事件 = %+v", event)
			}
			startAt = i
		}
		if event.Type == harness.EventToolEnd && event.ToolID == "agy-record-tool" {
			toolEndAt = i
		}
	}
	if startAt < 0 || toolEndAt < 0 || startAt > toolEndAt {
		t.Fatalf("后台任务开始未先于工具结束: %v", events)
	}
	roots := f.h.ProcessRootPIDs(f.turn.ThreadID)
	if len(roots) != 1 || syscall.Kill(roots[0], 0) != nil || len(featureParams(f.messages(t), "session/close")) != 0 {
		t.Fatal("轮末未保留后台任务进程")
	}
	f.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return len(calls) > 0 })
	if got := <-calls; got != f.turn.ThreadID {
		t.Fatalf("unsolicited thread = %q", got)
	}
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	attachEvents := f.run(t, attach)
	if len(attachEvents) < 1 || attachEvents[0].Type != harness.EventBackgroundTaskEnd || attachEvents[0].TaskID != fakeHoldSession+"/task-1" || attachEvents[0].TaskStatus != "completed" {
		t.Fatalf("Attach 结束事件 = %v", attachEvents)
	}
	if syscall.Kill(roots[0], 0) == nil {
		t.Fatal("Attach 完成后常驻进程仍存活")
	}
}

func TestACPHoldCompletionReader(t *testing.T) {
	records := t.TempDir()
	dir, logs := filepath.Join(records, "messages"), filepath.Join(records, "tasks")
	for _, path := range []string{dir, logs} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	wantOutput := "完整行\n"
	data := strings.Repeat("界", backgroundTaskLogMaxBytes) + "\n" + wantOutput
	if err := os.WriteFile(filepath.Join(logs, "task-1.log"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for i, item := range []struct{ task, status, time string }{
		{"task-1", "finished", "2026-01-01T00:00:00Z"},
		{"task-2", "was canceled", "2026-01-01T00:00:01Z"},
	} {
		content := fmt.Sprintf("Task id %q %s with result:", fakeHoldSession+"/"+item.task, item.status)
		data := []byte(fmt.Sprintf(`{"content":%q,"timestamp":%q}`, content, item.time))
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.json", i)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	seen := make(map[string]bool)
	events := readBackgroundCompletions(dir, seen)
	if len(events) != 2 || events[0].TaskStatus != "completed" || events[0].Output != wantOutput || events[1].TaskStatus != "canceled" || events[1].Output != "" || len(seen) != 2 {
		t.Fatalf("messages 首读 = %v, seen=%v", events, seen)
	}
	if events := readBackgroundCompletions(dir, seen); len(events) != 0 {
		t.Fatalf("重复读取已消费消息: %v", events)
	}
}

func TestACPHoldClose(t *testing.T) {
	fixture := newACPHoldFixture(t, "feature_hold_content")
	fixture.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	var _ io.Closer = fixture.h
	fixture.run(t, fixture.turn)
	threadID := fixture.turn.ThreadID
	roots := fixture.h.ProcessRootPIDs(threadID)
	if len(roots) != 1 || !fixture.h.HasResidentProcess(threadID) {
		t.Fatal("没有保留空闲的 ACP 进程")
	}
	fixture.complete(t, 1, "finished")
	waitACPHold(t, func() bool { return fixture.h.HasUnsolicitedOutput(threadID) })
	if err := fixture.h.Close(); err != nil {
		t.Fatal(err)
	}
	if len(fixture.h.ProcessRootPIDs(threadID)) != 0 || fixture.h.HasResidentProcess(threadID) || syscall.Kill(roots[0], 0) == nil {
		t.Fatal("Close 返回时 ACP 进程尚未退出")
	}
	if fixture.h.HasUnsolicitedOutput(threadID) {
		t.Fatal("Close 未清除待上报的后台任务结果")
	}
	if err := fixture.h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestACPHoldCloseBusy(t *testing.T) {
	fixture := newACPHoldFixture(t, "feature_hold_content")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	closed := false
	if err := fixture.h.Run(ctx, fixture.turn, func(event harness.Event) {
		if event.Type != harness.EventDone {
			return
		}
		closed = true
		if err := fixture.h.Close(); err != nil {
			t.Error(err)
		}
		if !fixture.h.HasResidentProcess(fixture.turn.ThreadID) {
			t.Error("Close 提前回收了忙碌的 ACP 进程")
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !closed || len(fixture.h.ProcessRootPIDs(fixture.turn.ThreadID)) != 0 || fixture.h.HasResidentProcess(fixture.turn.ThreadID) {
		t.Fatal("Close 后 ACP 轮次未在收尾时回收进程")
	}
	if len(featureParams(fixture.messages(t), "session/close")) != 1 {
		t.Fatal("Close 后 ACP 轮次未正常关闭会话")
	}
}

func TestACPHoldDisabled(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_content")
	f.h.opts.HoldBackgroundTasks = false
	for _, event := range f.run(t, f.turn) {
		if event.Type == harness.EventBackgroundTaskStart {
			t.Fatal("关闭开关后仍产生后台任务事件")
		}
	}
	if len(featureParams(f.messages(t), "session/close")) != 1 || len(f.h.ProcessRootPIDs(f.turn.ThreadID)) != 0 {
		t.Fatal("关闭开关后未保持单轮进程行为")
	}
}

func TestACPHoldAttachBatches(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_content")
	f.h.opts.BackgroundPollInterval = 20 * time.Millisecond
	f.h.opts.AttachIdleTimeout = 50 * time.Millisecond
	calls := make(chan string, 10)
	f.h.SetUnsolicitedTurnHandler(func(threadID string) { calls <- threadID })
	f.run(t, f.turn)
	roots := f.h.ProcessRootPIDs(f.turn.ThreadID)
	attach := f.turn
	attach.Attach, attach.SessionID = true, fakeHoldSession
	for task := 1; task <= 2; task++ {
		f.complete(t, task, "finished")
		waitACPHold(t, func() bool { return len(calls) > 0 })
		<-calls
		events := f.run(t, attach)
		if len(events) == 0 || events[0].Type != harness.EventBackgroundTaskEnd || events[0].TaskID != fmt.Sprintf("%s/task-%d", fakeHoldSession, task) {
			t.Fatalf("第 %d 批结束事件 = %v", task, events)
		}
		if task == 1 {
			if got := f.h.ProcessRootPIDs(f.turn.ThreadID); !reflect.DeepEqual(got, roots) {
				t.Fatalf("第二个任务未完成时进程已退出或更换: %v", got)
			}
			if len(featureParams(f.messages(t), "session/close")) != 0 {
				t.Fatal("仍有任务时提前关闭会话")
			}
		}
	}
	messages := f.messages(t)
	if len(featureParams(messages, "initialize")) != 1 || len(featureParams(messages, "session/prompt")) != 3 || len(featureParams(messages, "session/close")) != 1 {
		t.Fatalf("分批续轮的生命周期错误: %v", messages)
	}
}

func TestACPHoldConfigureEachRound(t *testing.T) {
	f := newACPHoldFixture(t, "feature_hold_content")
	turn := f.turn
	turn.Mode, turn.Model, turn.Effort, turn.FastMode = harness.ModeAuto, "requested", "high", true
	f.run(t, turn)
	turn.Mode, turn.Model, turn.Effort, turn.FastMode = harness.ModeSupervised, "old", "low", false
	f.run(t, turn)
	messages := f.messages(t)
	modes := featureParams(messages, "session/set_mode")
	if len(modes) != 0 {
		t.Fatalf("非 plan 回合不应切换会话模式: %v", modes)
	}
	options := featureParams(messages, "session/set_config_option")
	want := []struct{ id, value string }{{"model", "requested"}, {"thought_level", "high"}, {"fast", "true"}, {"model", "old"}, {"thought_level", "low"}, {"fast", "false"}}
	if len(options) != len(want) {
		t.Fatalf("复用后配置次数错误: %v", options)
	}
	for i, option := range options {
		if option["configId"] != want[i].id || option["value"] != want[i].value {
			t.Fatalf("配置 %d = %v, want %v", i, option, want[i])
		}
	}
	if len(featureParams(messages, "initialize")) != 1 || len(featureParams(messages, "session/new")) != 1 || len(featureParams(messages, "session/prompt")) != 2 {
		t.Fatal("切换配置没有复用同一进程与会话")
	}
}

func TestACPOptionsTimingOverrides(t *testing.T) {
	def := New(Options{})
	if got := def.backgroundPollInterval(); got != 2*time.Second {
		t.Fatalf("default backgroundPollInterval = %v, want 2s", got)
	}
	if got := def.attachIdleTimeout(); got != 3*time.Second {
		t.Fatalf("default attachIdleTimeout = %v, want 3s", got)
	}
	if got := def.attachPollInterval(); got != 200*time.Millisecond {
		t.Fatalf("default attachPollInterval = %v, want 200ms", got)
	}
	if got := def.stopKillGrace(); got != 3*time.Second {
		t.Fatalf("default stopKillGrace = %v, want 3s", got)
	}

	custom := New(Options{
		DisableLaunchSpacing:   true,
		BackgroundPollInterval: 25 * time.Millisecond,
		AttachIdleTimeout:      80 * time.Millisecond,
		StopKillGrace:          50 * time.Millisecond,
	})
	if got := custom.backgroundPollInterval(); got != 25*time.Millisecond {
		t.Fatalf("override backgroundPollInterval = %v, want 25ms", got)
	}
	if got := custom.attachIdleTimeout(); got != 80*time.Millisecond {
		t.Fatalf("override attachIdleTimeout = %v, want 80ms", got)
	}
	if got := custom.attachPollInterval(); got != 20*time.Millisecond {
		t.Fatalf("override attachPollInterval = %v, want 20ms", got)
	}
	if got := custom.stopKillGrace(); got != 50*time.Millisecond {
		t.Fatalf("override stopKillGrace = %v, want 50ms", got)
	}
	tiny := New(Options{AttachIdleTimeout: 4 * time.Millisecond})
	if got := tiny.attachPollInterval(); got != 5*time.Millisecond {
		t.Fatalf("min attachPollInterval = %v, want 5ms", got)
	}
	large := New(Options{AttachIdleTimeout: 2 * time.Second})
	if got := large.attachPollInterval(); got != 200*time.Millisecond {
		t.Fatalf("capped attachPollInterval = %v, want 200ms", got)
	}

	// DisableLaunchSpacing 为 true 时既不等待 launchSpacing，也不修改全局 launchGate。
	oldSpacing := launchSpacing
	launchSpacing = 200 * time.Millisecond
	t.Cleanup(func() { launchSpacing = oldSpacing })

	future := time.Now().Add(time.Hour)
	launchGate.mu.Lock()
	prevNext := launchGate.next
	launchGate.next = future
	launchGate.mu.Unlock()
	t.Cleanup(func() {
		launchGate.mu.Lock()
		launchGate.next = prevNext
		launchGate.mu.Unlock()
	})

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := custom.waitLaunchSlot(canceledCtx); err != nil {
		t.Fatalf("DisableLaunchSpacing waitLaunchSlot = %v, want nil", err)
	}
	if elapsed := time.Since(start); elapsed >= 50*time.Millisecond {
		t.Fatalf("DisableLaunchSpacing waitLaunchSlot took %v", elapsed)
	}
	launchGate.mu.Lock()
	gotNext := launchGate.next
	launchGate.mu.Unlock()
	if !gotNext.Equal(future) {
		t.Fatalf("DisableLaunchSpacing mutated launchGate.next: got %v, want %v", gotNext, future)
	}

	// 零值 DisableLaunchSpacing 仍遵守全局 launchGate 与 ctx 取消。
	if err := def.waitLaunchSlot(canceledCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("default waitLaunchSlot(canceled) = %v, want context.Canceled", err)
	}
}

func TestACPWriteTextFileBlockedInReadOnlyAndPlan(t *testing.T) {
	findWriteResp := func(messages []map[string]any) map[string]any {
		for _, msg := range messages {
			if msg["id"] == float64(9130) {
				return msg
			}
		}
		return nil
	}

	cases := []struct {
		name            string
		mode            harness.PermissionMode
		interactionMode string
		wantAllowed     bool
	}{
		{
			name:            "read_only_build",
			mode:            harness.ModeReadOnly,
			interactionMode: "build",
			wantAllowed:     false,
		},
		{
			name:            "auto_plan",
			mode:            harness.ModeAuto,
			interactionMode: "plan",
			wantAllowed:     false,
		},
		{
			name:            "auto_build",
			mode:            harness.ModeAuto,
			interactionMode: "build",
			wantAllowed:     true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workDir := t.TempDir()
			messages, events, err := runACPFeature(t, "feature_write_text_file", AgentCline, harness.Turn{
				WorkDir:         workDir,
				Mode:            tc.mode,
				InteractionMode: tc.interactionMode,
			})
			if err != nil {
				t.Fatalf("runACPFeature failed: %v", err)
			}

			resp := findWriteResp(messages)
			if resp == nil {
				t.Fatal("missing JSON-RPC response for fs/write_text_file (id=9130)")
			}

			target := filepath.Join(workDir, "nested", "write-test.txt")
			var fileChanges []harness.Event
			for _, e := range events {
				if e.Type == harness.EventFileChange {
					fileChanges = append(fileChanges, e)
				}
			}

			if tc.wantAllowed {
				if resp["error"] != nil {
					t.Fatalf("unexpected JSON-RPC error: %v", resp["error"])
				}
				if resp["result"] == nil {
					t.Fatalf("expected JSON-RPC result, got nil: %v", resp)
				}
				got, readErr := os.ReadFile(target)
				if readErr != nil {
					t.Fatalf("expected written file %s: %v", target, readErr)
				}
				if string(got) != "written content" {
					t.Fatalf("written content = %q, want %q", string(got), "written content")
				}
				if len(fileChanges) != 1 || filepath.ToSlash(fileChanges[0].Path) != "nested/write-test.txt" {
					t.Fatalf("fileChanges = %+v, want 1 EventFileChange for nested/write-test.txt", fileChanges)
				}
				return
			}

			if resp["error"] == nil {
				t.Fatalf("expected JSON-RPC error for blocked fs/write_text_file, got response: %v", resp)
			}
			if _, statErr := os.Stat(filepath.Join(workDir, "nested")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("expected parent directory not to be created, stat err = %v", statErr)
			}
			if _, statErr := os.Stat(target); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("expected target file not to be created, stat err = %v", statErr)
			}
			if len(fileChanges) != 0 {
				t.Fatalf("unexpected EventFileChange events: %+v", fileChanges)
			}
		})
	}
}

type agyDenyTestInteractor struct {
	mu        sync.Mutex
	responses []harness.ApprovalResponse
	idx       int
}

func (a *agyDenyTestInteractor) Approve(ctx context.Context, req harness.ApprovalRequest) (harness.Decision, error) {
	resp, err := a.Interact(ctx, req)
	return resp.Decision, err
}

func (a *agyDenyTestInteractor) Interact(_ context.Context, _ harness.ApprovalRequest) (harness.ApprovalResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.responses) == 0 {
		return harness.ApprovalResponse{Decision: harness.DecisionDeny}, nil
	}
	i := a.idx
	if i >= len(a.responses) {
		i = len(a.responses) - 1
	} else {
		a.idx++
	}
	return a.responses[i], nil
}

func TestAgyAutoDenyTransparentContinuation(t *testing.T) {
	promptText := func(p map[string]any) string {
		items, _ := p["prompt"].([]any)
		var out strings.Builder
		for _, item := range items {
			m, _ := item.(map[string]any)
			if txt, _ := m["text"].(string); txt != "" {
				out.WriteString(txt)
			}
		}
		return out.String()
	}

	t.Run("auto_denied_continues_same_session", func(t *testing.T) {
		approver := &agyDenyTestInteractor{
			responses: []harness.ApprovalResponse{
				{Decision: harness.DecisionDeny, AutoDenied: true, Feedback: "只读档子代理不允许写操作"},
			},
		}
		messages, events, err := runACPFeature(t, "feature_agy_auto_deny_continue", AgentAgy, harness.Turn{
			Prompt:   "请清理构建产物",
			Approver: approver,
		})
		if err != nil {
			t.Fatalf("runACPFeature failed: %v", err)
		}

		prompts := featureParams(messages, "session/prompt")
		if len(prompts) != 2 {
			t.Fatalf("session/prompt count = %d, want 2", len(prompts))
		}
		if prompts[0]["sessionId"] != "feature-session" || prompts[1]["sessionId"] != "feature-session" {
			t.Fatalf("sessionIds = (%v, %v), want feature-session", prompts[0]["sessionId"], prompts[1]["sessionId"])
		}
		contText := promptText(prompts[1])
		if !strings.Contains(contText, "Run rm -rf build") || !strings.Contains(contText, "只读档子代理不允许写操作") {
			t.Fatalf("continuation prompt = %q, want blocked tool and feedback", contText)
		}

		var doneCount int
		var replyText strings.Builder
		var usage *harness.Usage
		var toolEnd *harness.Event
		for i := range events {
			e := events[i]
			switch e.Type {
			case harness.EventDone:
				doneCount++
			case harness.EventText:
				replyText.WriteString(e.Text)
			case harness.EventUsage:
				usage = e.Usage
			case harness.EventToolEnd:
				if e.ToolID == "denied-call-1" {
					ev := e
					toolEnd = &ev
				}
			}
		}
		if doneCount != 1 {
			t.Fatalf("EventDone count = %d, want 1", doneCount)
		}
		if replyText.String() != "已改用只读方式完成" {
			t.Fatalf("replyText = %q, want %q", replyText.String(), "已改用只读方式完成")
		}
		if usage == nil || usage.InputTokens != 250 || usage.OutputTokens != 50 || usage.CachedTokens != 30 {
			t.Fatalf("accumulated usage = %+v, want input=250 output=50 cached=30", usage)
		}
		if toolEnd == nil || !toolEnd.IsError || toolEnd.Output != "工具调用已被权限策略拦截，正在换用其他方式继续" {
			t.Fatalf("toolEnd for denied-call-1 = %+v, want error EventToolEnd", toolEnd)
		}
	})

	t.Run("manual_deny_does_not_continue", func(t *testing.T) {
		approver := &agyDenyTestInteractor{
			responses: []harness.ApprovalResponse{
				{Decision: harness.DecisionDeny, AutoDenied: false, Feedback: "用户拒绝"},
			},
		}
		messages, _, err := runACPFeature(t, "feature_agy_auto_deny_continue", AgentAgy, harness.Turn{
			Prompt:   "请清理构建产物",
			Approver: approver,
		})
		if err != nil {
			t.Fatalf("runACPFeature failed: %v", err)
		}
		prompts := featureParams(messages, "session/prompt")
		if len(prompts) != 1 {
			t.Fatalf("session/prompt count = %d, want 1", len(prompts))
		}
	})

	t.Run("mixed_auto_and_manual_deny_does_not_continue", func(t *testing.T) {
		approver := &agyDenyTestInteractor{
			responses: []harness.ApprovalResponse{
				{Decision: harness.DecisionDeny, AutoDenied: true, Feedback: "自动拒绝"},
				{Decision: harness.DecisionDeny, AutoDenied: false, Feedback: "用户拒绝"},
			},
		}
		messages, _, err := runACPFeature(t, "feature_agy_auto_deny_mixed", AgentAgy, harness.Turn{
			Prompt:   "混合拒绝",
			Approver: approver,
		})
		if err != nil {
			t.Fatalf("runACPFeature failed: %v", err)
		}
		prompts := featureParams(messages, "session/prompt")
		if len(prompts) != 1 {
			t.Fatalf("session/prompt count = %d, want 1", len(prompts))
		}
	})

	t.Run("stops_at_max_continuations", func(t *testing.T) {
		oldMax := agyAutoDenyMaxContinuations
		agyAutoDenyMaxContinuations = 2
		t.Cleanup(func() { agyAutoDenyMaxContinuations = oldMax })

		approver := &agyDenyTestInteractor{
			responses: []harness.ApprovalResponse{
				{Decision: harness.DecisionDeny, AutoDenied: true, Feedback: "策略拦截"},
			},
		}
		messages, events, err := runACPFeature(t, "feature_agy_auto_deny_loop", AgentAgy, harness.Turn{
			Prompt:   "持续拦截",
			Approver: approver,
		})
		if err != nil {
			t.Fatalf("runACPFeature failed: %v", err)
		}
		prompts := featureParams(messages, "session/prompt")
		wantPrompts := 1 + agyAutoDenyMaxContinuations
		if len(prompts) != wantPrompts {
			t.Fatalf("session/prompt count = %d, want %d", len(prompts), wantPrompts)
		}
		var doneCount int
		for _, e := range events {
			if e.Type == harness.EventDone {
				doneCount++
			}
		}
		if doneCount != 1 {
			t.Fatalf("EventDone count = %d, want 1", doneCount)
		}
	})
}
