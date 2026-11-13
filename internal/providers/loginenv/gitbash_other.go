//go:build !windows

package loginenv

// GitBash 在非 Windows 平台返回空字符串。
func GitBash() string { return "" }
