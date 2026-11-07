//go:build !unix

package reviewcheck

import "os/exec"

// setProcessGroup 在非 unix 平台上为空操作；Windows 回退为直接杀主进程。
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup 在没有进程组语义的平台上直接结束主进程。
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
