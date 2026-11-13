//go:build !windows

package fsutil

import "errors"

// junction 只在 Windows 上存在；其他平台 LinkOrCopy 不会走到这里。
func junction(target, link string) error {
	return errors.New("目录联接仅支持 Windows")
}
