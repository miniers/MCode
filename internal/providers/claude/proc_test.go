//go:build unix

package claude

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestProcessGroupKill(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 30 & sleep 30")
	setProcessGroup(cmd)
	cmd.Cancel = func() error {
		return killProcessGroup(cmd, syscall.SIGTERM)
	}
	cmd.WaitDelay = 3 * time.Second

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start cmd: %v", err)
	}

	pgid := cmd.Process.Pid
	if pgid <= 0 {
		t.Fatalf("invalid process pid: %d", pgid)
	}

	// 稍微等待片刻，确保 sh 派生的后台 sleep 进程已经启动并在同一进程组中运行
	time.Sleep(100 * time.Millisecond)

	// 确认进程组在取消前正常存在
	if err := syscall.Kill(-pgid, 0); err != nil {
		t.Fatalf("expected process group -%d to exist before cancel: %v", pgid, err)
	}

	start := time.Now()
	cancel()

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	select {
	case <-waitDone:
		elapsed := time.Since(start)
		if elapsed > 5*time.Second {
			t.Fatalf("cmd.Wait() took %v, expected within 5s", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cmd.Wait() timed out after 5s")
	}

	// 稍微轮询几次，确认整个进程组已经不存在
	var killErr error
	for i := 0; i < 20; i++ {
		killErr = syscall.Kill(-pgid, 0)
		if killErr != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	if killErr == nil {
		t.Fatalf("expected process group -%d to be terminated, but syscall.Kill(-pgid, 0) still succeeded", pgid)
	}
}
