//go:build !darwin && !windows

package loginenv

// systemProxyEnvironment 在既不是 darwin 也不是 windows 的平台不读取系统代理，直接返回 nil。
func systemProxyEnvironment() map[string]string {
	return nil
}
