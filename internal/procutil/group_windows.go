//go:build windows

package procutil

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"

	"mcode/internal/procattr"
)

func SetProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
}

func KillProcessGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	if err := procattr.TerminateTree(cmd.Process.Pid); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
