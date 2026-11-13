//go:build !unix

package reviewcheck

import (
	"os"
	"os/exec"

	"mcode/internal/procutil"
)

// setProcessGroup 让检查命令自成进程组，Windows 上同时不弹控制台窗口。
var setProcessGroup = procutil.SetProcessGroup

// killProcessGroup 结束检查命令的整棵进程树（Windows 上为 taskkill /T /F），避免 go build 等子进程残留。
func killProcessGroup(cmd *exec.Cmd) error {
	return procutil.KillProcessGroup(cmd, os.Kill)
}
