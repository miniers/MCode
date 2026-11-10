//go:build !darwin

package sandbox

import "mcode/internal/harness"

// ReadOnly 在没有可用只读沙箱的平台返回 nil。
func ReadOnly() harness.Sandbox { return nil }
