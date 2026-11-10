//go:build !windows

package omp

import (
	"bufio"
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestWaitWithKill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", `trap '' TERM; printf 'ready\n'; exec sleep 30`)
	setProcessGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		_ = cmd.Wait()
	}()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("进程未就绪: %q, %v", line, err)
	}
	if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	grace := 200 * time.Millisecond
	start := time.Now()
	if err := waitWithKill(cmd, grace); err == nil {
		t.Fatal("强制终止后应返回错误")
	}
	if elapsed := time.Since(start); elapsed < grace || elapsed > grace+2*time.Second {
		t.Fatalf("等待时间异常: %v", elapsed)
	}
}
