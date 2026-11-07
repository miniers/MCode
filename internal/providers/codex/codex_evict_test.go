package codex

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"mcode/internal/harness"
)

// newEvictHarness 创建使用假 app-server 脚本的 Harness。
func newEvictHarness(t *testing.T, script string) *Harness {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	h := New(Options{})
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Errorf("Close failed: %v", err)
		}
	})
	return h
}

// evictSandbox 返回运行给定脚本的沙箱。
func evictSandbox(script string) *mockSandbox {
	return &mockSandbox{cmdFunc: func(ctx context.Context) *exec.Cmd {
		return exec.CommandContext(ctx, "python3", "-u", "-c", script)
	}}
}

func TestEvictThreadClosesIdleProcess(t *testing.T) {
	h := newEvictHarness(t, pongAppServerScript)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Run(ctx, harness.Turn{
		ThreadID: "th_mcode_1",
		WorkDir:  t.TempDir(),
		Mode:     harness.ModeAgent,
		Prompt:   "Ping",
		Sandbox:  evictSandbox(pongAppServerScript),
	}, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	procs := h.poolProcs()
	if len(procs) != 1 {
		t.Fatalf("expected 1 idle process, got %d", len(procs))
	}
	proc := procs[0]
	if !proc.hasCachedThread("th_mock_123") {
		t.Fatal("expected the idle process to cache th_mock_123")
	}
	if !proc.servesMcodeThread("th_mcode_1") {
		t.Fatal("expected the idle process to remember the mcode session id")
	}

	// 真实调用方（remote 层）传的是上层 mcode 会话 ID，与 codex 的 threadId 不同
	h.EvictThread("th_mcode_1")

	if got := len(h.poolProcs()); got != 0 {
		t.Fatalf("expected the idle process to be evicted, pool has %d", got)
	}
	if !proc.isDead() {
		t.Fatal("expected the evicted process to be closed")
	}

	// 重复回收同一会话是幂等的
	h.EvictThread("th_mcode_1")
	if got := len(h.poolProcs()); got != 0 {
		t.Fatalf("expected an empty pool after repeated eviction, got %d", got)
	}
}

func TestEvictThreadKeepsUnrelatedProcesses(t *testing.T) {
	h := newEvictHarness(t, pongAppServerScript)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Run(ctx, harness.Turn{
		ThreadID: "th_mcode_1",
		WorkDir:  t.TempDir(),
		Mode:     harness.ModeAgent,
		Prompt:   "Ping",
		Sandbox:  evictSandbox(pongAppServerScript),
	}, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// 空 threadID 与不相干的会话都不应回收任何进程
	h.EvictThread("")
	h.EvictThread("th_other")

	procs := h.poolProcs()
	if len(procs) != 1 {
		t.Fatalf("expected the unrelated process to survive, pool has %d", len(procs))
	}
	if procs[0].isDead() {
		t.Fatal("unrelated process must stay alive")
	}
}

// evictSlowScript 在 turn/start 后延迟一秒才结束该轮，便于在轮次进行中观察进程池。
const evictSlowScript = `
import sys, json, time

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
        time.sleep(1)
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"item/agentMessage/delta","params":{"itemId":"msg_1","delta":"SLOW"}}) + "\n")
        sys.stdout.write(json.dumps({"jsonrpc":"2.0","method":"turn/completed","params":{"turn":{"id":turn_id,"status":"completed"}}}) + "\n")
        sys.stdout.flush()
`

func TestEvictThreadSkipsBusyProcess(t *testing.T) {
	h := newEvictHarness(t, evictSlowScript)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- h.Run(ctx, harness.Turn{
			ThreadID: "th_mcode_1",
			WorkDir:  t.TempDir(),
			Mode:     harness.ModeAgent,
			Prompt:   "Ping",
			Sandbox:  evictSandbox(evictSlowScript),
		}, nil)
	}()

	// 等待轮次就绪：进程在池中、busy 且已登记该会话
	deadline := time.Now().Add(5 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		procs := h.poolProcs()
		if len(procs) == 1 && procs[0].busy && procs[0].servesMcodeThread("th_mcode_1") {
			ready = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("timed out waiting for the running turn")
	}

	h.EvictThread("th_mcode_1")

	procs := h.poolProcs()
	if len(procs) != 1 {
		t.Fatalf("busy process must stay in the pool, got %d", len(procs))
	}
	if procs[0].isDead() {
		t.Fatal("busy process must not be killed")
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the turn to finish")
	}

	// 轮次结束后进程回到空闲池，此时再回收即可命中
	h.EvictThread("th_mcode_1")
	if got := len(h.poolProcs()); got != 0 {
		t.Fatalf("expected the process to be evicted after the turn, pool has %d", got)
	}
}

func TestTransientTurnRetiresProcess(t *testing.T) {
	h := newEvictHarness(t, pongAppServerScript)

	var texts []string
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Run(ctx, harness.Turn{
		WorkDir:   t.TempDir(),
		Mode:      harness.ModeAgent,
		Prompt:    "Ping",
		Sandbox:   evictSandbox(pongAppServerScript),
		Transient: true,
	}, func(e harness.Event) {
		if e.Type == harness.EventText {
			texts = append(texts, e.Text)
		}
	}); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if len(texts) != 1 || texts[0] != "PONG" {
		t.Fatalf("unexpected streamed text: %v", texts)
	}
	if got := len(h.poolProcs()); got != 0 {
		t.Fatalf("transient turn must not leave a process in the idle pool, got %d", got)
	}
}

func TestTransientTurnRetiresReusedIdleProcess(t *testing.T) {
	h := newEvictHarness(t, pongAppServerScript)

	// 两轮共用同一个沙箱实例与工作目录，才能命中同一个 (workDir, sandbox) 分组复用空闲进程。
	sb := evictSandbox(pongAppServerScript)
	workDir := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 先跑一轮普通对话，池里留下一个可复用的空闲进程
	if err := h.Run(ctx, harness.Turn{
		WorkDir: workDir,
		Mode:    harness.ModeAgent,
		Prompt:  "Ping",
		Sandbox: sb,
	}, nil); err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	procs := h.poolProcs()
	if len(procs) != 1 {
		t.Fatalf("expected 1 idle process, got %d", len(procs))
	}
	idle := procs[0]

	// 一次性轮次复用该空闲进程，收尾时直接关闭它，不留在池里
	if err := h.Run(ctx, harness.Turn{
		WorkDir:   workDir,
		Mode:      harness.ModeAgent,
		Prompt:    "Ping",
		Sandbox:   sb,
		Transient: true,
	}, nil); err != nil {
		t.Fatalf("transient Run failed: %v", err)
	}

	if got := len(h.poolProcs()); got != 0 {
		t.Fatalf("expected the transient turn's process to be retired, pool has %d", got)
	}
	if !idle.isDead() {
		t.Fatal("expected the reused process to be closed after the transient turn")
	}
}
