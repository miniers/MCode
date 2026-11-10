// Package sandbox 提供内置命令的系统级只读隔离。
package sandbox

import "mcode/internal/harness"

// IsReadOnly 判断 s 是否为本包提供的只读沙箱。
func IsReadOnly(s harness.Sandbox) bool {
	_, ok := s.(interface{ readOnlySandbox() })
	return ok
}
