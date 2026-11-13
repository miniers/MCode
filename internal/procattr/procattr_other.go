//go:build !windows

package procattr

import (
	"os/exec"
	"syscall"
	"time"
)

func configureShellCancel(cmd *exec.Cmd) {}

// HideWindow 在非 Windows 平台上为空操作，直接返回 cmd，便于调用处无需分支。
func HideWindow(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}

// TerminateOnCancel 让取消上下文时的子进程先收到 SIGTERM，给 git 清理锁文件的机会。
func TerminateOnCancel(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return cmd
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}
