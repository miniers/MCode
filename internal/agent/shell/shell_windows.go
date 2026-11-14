//go:build windows

package shell

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"mcode/internal/procattr"
	"mcode/internal/providers/loginenv"
)

func shellCommand(command string) (string, []string) {
	name, args, _ := loginenv.CommandShell(command)
	return name, args
}

func setProcessGroup(cmd *exec.Cmd) {
	procattr.HideWindow(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

func stopProcess(cmd *exec.Cmd, done <-chan error) error {
	if err := procattr.TerminateTree(cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
	}
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return fmt.Errorf("进程未在超时内退出，可能有子进程脱离了进程组")
	}
}
