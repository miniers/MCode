//go:build unix

package reviewcheck

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 把命令放入独立进程组，便于超时时杀掉整棵子进程树。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup 向 cmd 所属进程组发送 SIGKILL（通过负 PID）。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
