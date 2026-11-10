//go:build unix

package procutil

import (
	"os"
	"os/exec"
	"syscall"
)

// SetProcessGroup 将命令配置为独立进程组。
func SetProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	} else {
		cmd.SysProcAttr.Setpgid = true
	}
}

// KillProcessGroup 向整个进程组发送信号。
func KillProcessGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	sysSig, ok := sig.(syscall.Signal)
	if !ok {
		sysSig = syscall.SIGTERM
	}
	return syscall.Kill(-cmd.Process.Pid, sysSig)
}
