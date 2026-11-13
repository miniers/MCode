// Package testenv 提供测试用的环境隔离辅助函数。
package testenv

import "testing"

// SetHome 把用户主目录指向 dir。Windows 上 os.UserHomeDir 读 USERPROFILE，所以两个都设。
func SetHome(t testing.TB, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// TempHome 把用户主目录指向新的临时目录并返回它。
func TempHome(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	SetHome(t, dir)
	return dir
}
