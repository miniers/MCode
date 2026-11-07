// 本文件移植自 magpie 的 internal/steady/steady.go（MIT License），
// 见 docs/THIRD_PARTY_NOTICES.md。
//
// 与 magpie 的差异：合并成单文件，用包级变量 onWindows 模拟 Windows 行为，
// 重命名等底层操作也做成包级变量，便于测试注入假错误。

package magpiecfg

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// tries 和 pause 把等待限制在半秒内：占住文件的只是另一次读或 rename，
// 毫秒级就完成。Windows 上另一次 rename 正在替换文件时，打开或覆盖会短暂
// 返回 "Access is denied"，重试即可。
const (
	tries = 50
	pause = 10 * time.Millisecond
)

// onWindows 表示文件行为是否像 Windows 那样；测试会设置它。
var onWindows = runtime.GOOS == "windows"

// rename 和 Sync 是 os.Rename 与 (*os.File).Sync；测试用它们注入失败。
var (
	rename = os.Rename
	Sync   = (*os.File).Sync
)

func retry(f func() error) error {
	err := f()
	for i := 0; err != nil && onWindows && !errors.Is(err, fs.ErrNotExist) && i < tries; i++ {
		time.Sleep(pause)
		err = f()
	}
	return err
}

// ReadFile 是 os.ReadFile，在 Windows 上等过文件正被替换的那一刻。
func ReadFile(p string) ([]byte, error) {
	var b []byte
	err := retry(func() (err error) {
		b, err = os.ReadFile(p)
		return err
	})
	return b, err
}

// Rename 是 os.Rename，在 Windows 上等过读的人放开 newpath。
func Rename(oldpath, newpath string) error {
	return retry(func() error { return rename(oldpath, newpath) })
}

// WriteFile 用 perm 权限把 b 写成 p：先写在它旁边的临时文件、刷到磁盘，再
// rename 覆盖 p。写或刷盘失败时删掉临时文件，p 保持原样。
func WriteFile(p string, b []byte, perm fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if err == nil {
		err = Sync(tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = Rename(tmp.Name(), p)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
	}
	return err
}
