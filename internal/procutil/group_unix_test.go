//go:build unix

package procutil

import (
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestKillProcessGroup(t *testing.T) {
	cmd := exec.Command("sh", "-c", "sleep 30 & wait")
	SetProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	finished := false
	t.Cleanup(func() {
		if !finished {
			_ = KillProcessGroup(cmd, syscall.SIGKILL)
		}
	})
	if err := KillProcessGroup(cmd, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		finished = true
	case <-time.After(5 * time.Second):
		t.Fatal("进程组未在 5 秒内退出")
	}
}
