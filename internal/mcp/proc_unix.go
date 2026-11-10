//go:build unix

package mcp

import (
	"os"
	"os/exec"
	"syscall"

	"mcode/internal/procutil"
)

// killProcessGroup 向 cmd 所属的整个进程组发送指定信号（通过负 PID）。
func killProcessGroup(cmd *exec.Cmd, sig os.Signal) error {
	if _, ok := sig.(syscall.Signal); !ok {
		sig = syscall.SIGKILL
	}
	return procutil.KillProcessGroup(cmd, sig)
}
