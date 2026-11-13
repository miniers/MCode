//go:build windows

package procattr

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestShellCommandCancelsProcessTree(t *testing.T) {
	oldResolver := windowsShell.Load()
	t.Cleanup(func() { windowsShell.Store(oldResolver) })
	SetWindowsShellResolver(nil)
	command := "ping -n 6 127.0.0.1"
	git, _ := exec.LookPath("git.exe")
	bash := filepath.Join(filepath.Dir(filepath.Dir(git)), "bin", "bash.exe")
	if info, err := os.Stat(bash); err == nil && !info.IsDir() {
		SetWindowsShellResolver(func(command string) (string, []string) {
			return bash, []string{"-c", command}
		})
		command = "while true; do echo running; sleep 1; done"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ShellCommand(ctx, command).CombinedOutput()
	if err == nil || ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("命令未按超时结束：%v，context: %v", err, ctx.Err())
	}
	if elapsed := time.Since(start); elapsed >= 2500*time.Millisecond {
		t.Fatalf("超时未及时生效，耗时 %v", elapsed)
	}
}
