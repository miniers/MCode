// Package procattr 统一调整子进程的平台相关属性。
//
// HideWindow：Windows 上让控制台子进程不弹出控制台窗口。
// 本程序以 -H windowsgui 构建，是纯 GUI 进程；它启动 git、gh、node、claude 等
// 控制台程序时，系统默认会为每个子进程新开一个控制台窗口，导致频繁闪窗。
// 调用方在 Start/Run/Output/CombinedOutput 之前套一层 HideWindow 即可。
//
// ShellCommand：用平台 shell 执行用户填写的命令（脚本、检查、验收命令等）。
package procattr

import (
	"context"
	"os/exec"
	"runtime"
)

// shellGOOS 是 ShellCommand 选择 shell 的平台标识，测试中可替换。
var shellGOOS = runtime.GOOS
var windowsBash = func() string { return "" }
var shellLookPath = exec.LookPath

// SetWindowsBashResolver 注册 Windows 下的 Git Bash 查找函数。
func SetWindowsBashResolver(resolve func() string) { windowsBash = resolve }

// ShellCommand 返回用平台 shell 执行 command 的 *exec.Cmd，已隐藏 Windows 控制台窗口。
// Windows 没有 /bin/sh，用 PowerShell；其余平台用 /bin/sh -c。
func ShellCommand(ctx context.Context, command string) *exec.Cmd {
	name, args := ShellArgs(command)
	cmd := HideWindow(exec.CommandContext(ctx, name, args...))
	configureShellCancel(cmd)
	return cmd
}

// ShellArgs 返回执行 command 的平台 shell 与参数，供需要自己组装进程的调用方使用。
func ShellArgs(command string) (string, []string) {
	return shellArgs(shellGOOS, command)
}

func shellArgs(goos, command string) (string, []string) {
	if goos == "windows" {
		if bash := windowsBash(); bash != "" {
			return bash, []string{"-c", command}
		}
		if pwsh, err := shellLookPath("pwsh.exe"); err == nil {
			return pwsh, []string{"-NoProfile", "-Command", command}
		}
		return "powershell.exe", []string{"-NoProfile", "-Command", command}
	}
	return "/bin/sh", []string{"-c", command}
}
