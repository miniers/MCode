package fsutil

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"time"
)

// Rename 在 Windows 上短暂重试被占用的目标文件。
func Rename(oldpath, newpath string) error {
	return renameWithRetry(func() error { return os.Rename(oldpath, newpath) }, runtime.GOOS == "windows")
}

func renameWithRetry(rename func() error, windows bool) error {
	err := rename()
	for i := 0; err != nil && windows && !errors.Is(err, fs.ErrNotExist) && i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		err = rename()
	}
	return err
}
