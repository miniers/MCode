//go:build !unix && !windows

package procutil

import (
	"os"
	"os/exec"
)

func SetProcessGroup(cmd *exec.Cmd) {
}

func KillProcessGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
