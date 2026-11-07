//go:build !windows

package procattr

import "os/exec"

// HideWindow 在非 Windows 平台上为空操作，直接返回 cmd，便于调用处无需分支。
func HideWindow(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}
