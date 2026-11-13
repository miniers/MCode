//go:build windows

package procattr

import (
	"os/exec"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func configureShellCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		killer := HideWindow(exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)))
		if err := killer.Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

// HideWindow 让 cmd 在 Windows 上以不创建控制台窗口的方式启动。
// cmd 为 nil 时原样返回；已有的 SysProcAttr 字段会被保留（按位或追加创建标志）。
// 返回 cmd 本身，便于链式调用。
func HideWindow(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return cmd
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = new(syscall.SysProcAttr)
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.SysProcAttr.HideWindow = true
	return cmd
}
