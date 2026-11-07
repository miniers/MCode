// Package filelock 提供纯 Go 的跨进程咨询锁，不引入 cgo。
// 平台实现见 lock_unix.go / lock_windows.go / lock_other.go。
package filelock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var (
	// ErrBusy 表示锁已被其他文件描述符或进程持有。
	ErrBusy = errors.New("filelock: busy")
	// ErrUnsupported 表示当前平台不支持跨进程咨询锁。
	ErrUnsupported = errors.New("filelock: unsupported platform")
)

// Lock 是持有中的排他咨询锁；Close 释放并关闭描述符，不删除锁文件。
type Lock struct {
	file *os.File
}

// open 打开或创建锁文件，并保证父目录存在。
func open(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", path, err)
	}
	return f, nil
}

// TryAcquire 以非阻塞方式获取 path 上的排他锁；已被其他句柄持有时返回 ErrBusy。
func TryAcquire(path string) (*Lock, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := tryLock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{file: f}, nil
}

// Acquire 阻塞等待 path 上的排他锁，直到拿到锁或出错。
func Acquire(path string) (*Lock, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Lock{file: f}, nil
}

// Close 释放锁并关闭文件描述符；锁文件保留在磁盘上，重复调用无副作用。
func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	unlockErr := release(f)
	closeErr := f.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

// WriteInfo 把诊断信息写入锁文件；仅用于排障，任何失败都不影响锁的持有状态。
// 锁文件的读方必须容忍截断或损坏的内容。
func (l *Lock) WriteInfo(v any) error {
	if l == nil || l.file == nil {
		return errors.New("filelock: lock is closed")
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if _, err := l.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = l.file.Write(data)
	return err
}
