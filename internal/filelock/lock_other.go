//go:build !unix && !windows

package filelock

import "os"

// tryLock 在不支持的平台返回 ErrUnsupported，绝不退化为无锁成功。
func tryLock(*os.File) error { return ErrUnsupported }

// lock 在不支持的平台返回 ErrUnsupported。
func lock(*os.File) error { return ErrUnsupported }

// release 没有可释放的平台锁。
func release(*os.File) error { return nil }
