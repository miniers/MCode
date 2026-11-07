//go:build !unix && !windows

package commandcode

import (
	"os"
	"os/exec"
)

// setProcessGroup 在非 Unix 平台上为空操作。
func setProcessGroup(cmd *exec.Cmd) {
}

// killProcessGroup 在非 Unix 平台上降级为杀死主进程。
func killProcessGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
