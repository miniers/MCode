//go:build !windows

package loginenv

// SetCommandShell 在非 Windows 平台不改变命令 shell。
func SetCommandShell(string) {}

// CommandShell 在非 Windows 平台返回空值，调用方继续使用原有的平台 shell。
func CommandShell(string) (string, []string, string) { return "", nil, "" }

// AvailableCommandShells 在非 Windows 平台没有可选的 Windows 命令 shell。
func AvailableCommandShells() []string { return nil }
