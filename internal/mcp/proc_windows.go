//go:build windows

package mcp

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// setProcessGroup 给命令加上「不弹控制台窗口 + 独立进程组」的创建标志，
// 便于关闭时用 taskkill 终结整棵子进程树。cmd 已有 SysProcAttr 时保留其他字段。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP
}

// killProcessGroup 用 taskkill /T 结束 cmd 的整棵子进程树；taskkill 失败时退回只杀主进程。
// taskkill 自身同样隐藏控制台窗口。
func killProcessGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	killer := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	killer.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := killer.Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
