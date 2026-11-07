//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock 以 LOCKFILE_FAIL_IMMEDIATELY 做一次非阻塞加锁；锁被占用时返回 ErrBusy。
func tryLock(f *os.File) error {
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped),
	)
	if err == nil {
		return nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return ErrBusy
	}
	return err
}

// lock 阻塞获取排他锁；不带 LOCKFILE_FAIL_IMMEDIATELY 即为阻塞等待。
func lock(f *os.File) error {
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK,
		0, 1, 0, new(windows.Overlapped),
	)
}

// release 解锁与加锁使用同一区间（偏移 0、长度 1 字节），否则解锁无效。
func release(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}
