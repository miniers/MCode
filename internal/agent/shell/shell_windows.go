//go:build windows

package shell

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"mcode/internal/procattr"
)

func shellCommand(command string) (string, []string) {
	if path, err := exec.LookPath("bash.exe"); err == nil {
		return path, []string{"-c", command}
	}
	return "powershell", []string{"-NoProfile", "-NonInteractive", "-Command", command}
}

func setProcessGroup(cmd *exec.Cmd) {
	procattr.HideWindow(cmd)
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

func stopProcess(cmd *exec.Cmd, done <-chan error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	killer := procattr.HideWindow(exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)))
	if err := killer.Run(); err != nil {
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
