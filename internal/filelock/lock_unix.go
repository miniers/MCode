//go:build unix

package filelock

import (
	"errors"
	"os"
	"syscall"
)

// tryLock 用 LOCK_EX|LOCK_NB 做一次非阻塞加锁；锁被占用时返回 ErrBusy。
func tryLock(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return nil
	}
	// 部分平台的「会被阻塞」错误码是 EAGAIN，另一些是 EWOULDBLOCK，两者都要识别。
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return ErrBusy
	}
	return err
}

// lock 阻塞获取排他锁，锁被其他进程持有时在此等待。
func lock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
}

// release 显式解锁；关闭描述符本身也会释放 flock，这里保持与旧实现一致的显式解锁。
func release(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
