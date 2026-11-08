//go:build windows

package shell

import (
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
	"mcode/internal/procattr"
)

func shellCommand(command string) (string, []string) {
	if path, err := exec.LookPath("bash.exe"); err == nil { return path, []string{"-c", command} }
	return "powershell", []string{"-NoProfile", "-NonInteractive", "-Command", command}
}

func setProcessGroup(cmd *exec.Cmd) {
	procattr.HideWindow(cmd)
	if cmd.SysProcAttr == nil { cmd.SysProcAttr = &syscall.SysProcAttr{} }
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

func stopProcess(cmd *exec.Cmd, done <-chan error) error {
	killer := procattr.HideWindow(exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)))
	if err := killer.Run(); err != nil { _ = cmd.Process.Kill() }
	return <-done
}
