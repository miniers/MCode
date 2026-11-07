//go:build windows

package procattr

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

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
