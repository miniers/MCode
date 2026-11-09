//go:build unix

package shell

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func shellCommand(command string) (string, []string) {
	name := "/bin/bash"
	if _, err := os.Stat(name); err != nil {
		name = "/bin/sh"
	}
	return name, []string{"-c", command}
}

func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func stopProcess(cmd *exec.Cmd, done <-chan error) error {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		// 即使命令已退出，仍清理忽略 SIGTERM 的后台进程。
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return err
	case <-timer.C:
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		select {
		case err := <-done:
			return err
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			return fmt.Errorf("进程未在超时内退出，可能有子进程脱离了进程组")
		}
	}
}
