package datadir

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestWaitProcessExitAlreadyExited(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	waitProcessExit(cmd.Process.Pid, 5*time.Second)
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("已退出进程等待耗时过长: %v", elapsed)
	}
}

func TestWaitProcessExitTimeout(t *testing.T) {
	const timeout = 100 * time.Millisecond
	start := time.Now()
	waitProcessExit(os.Getpid(), timeout)
	if elapsed := time.Since(start); elapsed < timeout || elapsed > time.Second {
		t.Fatalf("等待当前进程应在约 %v 后超时，实际 %v", timeout, elapsed)
	}
}
