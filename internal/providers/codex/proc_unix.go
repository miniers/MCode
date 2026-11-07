//go:build unix

package codex

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup 将命令配置为独立进程组，以便在取消时杀死整个子进程树。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		cmd.SysProcAttr.Setpgid = true
	}
}

// killProcessGroup 向 cmd 所属的整个进程组发送指定信号（通过负 PID）。
func killProcessGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	sysSig, ok := sig.(syscall.Signal)
	if !ok {
		sysSig = syscall.SIGTERM
	}
	return syscall.Kill(-cmd.Process.Pid, sysSig)
}
