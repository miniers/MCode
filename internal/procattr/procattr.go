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
	"sync/atomic"
)

// shellGOOS 是 ShellCommand 选择 shell 的平台标识，测试中可替换。
var shellGOOS = runtime.GOOS
var windowsShell atomic.Pointer[func(string) (string, []string)]
var shellLookPath = exec.LookPath

// SetWindowsShellResolver 注册 Windows 命令 shell 及其参数的解析函数；nil 恢复默认选择。
func SetWindowsShellResolver(resolve func(string) (string, []string)) {
	if resolve == nil {
		windowsShell.Store(nil)
		return
	}
	windowsShell.Store(&resolve)
}

// ShellCommand 返回用平台 shell 执行 command 的 *exec.Cmd，已隐藏 Windows 控制台窗口。
// Windows 使用已注册的命令 shell，未注册时回退 PowerShell；其余平台用 /bin/sh -c。
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
		if resolve := windowsShell.Load(); resolve != nil {
			return (*resolve)(command)
		}
		if pwsh, err := shellLookPath("pwsh.exe"); err == nil {
			return pwsh, []string{"-NoProfile", "-Command", command}
		}
		return "powershell.exe", []string{"-NoProfile", "-Command", command}
	}
	return "/bin/sh", []string{"-c", command}
}
