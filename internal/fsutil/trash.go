package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TrashDir 返回废纸篓目录；测试可替换，避免触碰真实废纸篓。
var TrashDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".Trash"), nil
}

var windowsTrashPath func(string) error
var trashMu sync.Mutex

// MoveToTrash 把条目移到系统废纸篓；符号链接只移动链接本身。
func MoveToTrash(path string) error {
	if runtime.GOOS == "windows" {
		if err := windowsTrashPath(path); err != nil {
			return fmt.Errorf("failed to move to trash: %w", err)
		}
		return nil
	}
	if runtime.GOOS != "darwin" {
		return errors.New("trash is not supported on this platform")
	}
	// 查重和移动串行，避免并发请求选中同一个落点而覆盖已有条目。
	trashMu.Lock()
	defer trashMu.Unlock()
	root, err := TrashDir()
	if err != nil {
		return fmt.Errorf("failed to locate trash: %w", err)
	}
	target, err := uniqueTrashPath(root, filepath.Base(path))
	if err != nil {
		return fmt.Errorf("failed to move to trash: %w", err)
	}
	if err := os.Rename(path, target); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return fmt.Errorf("cannot move to trash across devices: %w", err)
		}
		return fmt.Errorf("failed to move to trash: %w", err)
	}
	return nil
}

func uniqueTrashPath(root, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	stamp := time.Now().Format("15.04.05")
	for i := 0; i <= 1000; i++ {
		candidate := filepath.Join(root, name)
		if i > 0 {
			suffix := " " + stamp
			if i > 1 {
				suffix += fmt.Sprintf(" %d", i)
			}
			candidate = filepath.Join(root, stem+suffix+ext)
		}
		if _, err := os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("too many files with the same name in trash")
}
