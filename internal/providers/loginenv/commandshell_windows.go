//go:build windows

package loginenv

import (
	"strings"
	"sync/atomic"

	"mcode/internal/procattr"
)

var commandShellPreference atomic.Value

func init() {
	procattr.SetWindowsShellResolver(func(command string) (string, []string) {
		name, args, _ := CommandShell(command)
		return name, args
	})
}

// SetCommandShell 即时更新 Windows 命令 shell 偏好，空值与未知值均按自动选择。
func SetCommandShell(pref string) {
	commandShellPreference.Store(strings.TrimSpace(pref))
}

// CommandShell 返回当前命令 shell、执行参数与显示名称；偏好不可用时自动回退。
func CommandShell(command string) (name string, args []string, kind string) {
	pref, _ := commandShellPreference.Load().(string)
	return selectCommandShell(pref, command, GitBash, LookPath)
}

func selectCommandShell(pref, command string, gitBash func() string, lookPath func(string) (string, error)) (name string, args []string, kind string) {
	resolve := func(pref string) (string, string) {
		switch pref {
		case "git-bash":
			return gitBash(), "Git Bash"
		case "pwsh":
			path, _ := lookPath("pwsh.exe")
			return path, "PowerShell 7"
		case "powershell":
			path, _ := lookPath("powershell.exe")
			return path, "Windows PowerShell"
		}
		return "", ""
	}
	for _, candidate := range []string{pref, "git-bash", "pwsh", "powershell"} {
		if name, kind = resolve(candidate); name != "" {
			if candidate == "git-bash" {
				return name, []string{"-c", command}, kind
			}
			return name, []string{"-NoProfile", "-NonInteractive", "-Command", command}, kind
		}
	}
	return "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", command}, "Windows PowerShell"
}

// AvailableCommandShells 返回本机可用的 Windows 命令 shell 偏好值，不含自动选项。
func AvailableCommandShells() []string {
	return availableCommandShells(GitBash, LookPath)
}

func availableCommandShells(gitBash func() string, lookPath func(string) (string, error)) []string {
	var available []string
	if gitBash() != "" {
		available = append(available, "git-bash")
	}
	for _, pref := range []string{"pwsh", "powershell"} {
		if _, err := lookPath(pref + ".exe"); err == nil {
			available = append(available, pref)
		}
	}
	return available
}
