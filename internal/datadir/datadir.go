// Package datadir 解析数据目录，并在重启时应用待处理的迁移。
package datadir

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mcode/internal/fsutil"
)

type pointer struct {
	Path     string `json:"path"`
	MoveFrom string `json:"move_from,omitempty"`
}

var mu sync.Mutex

var exportedPinned bool

func pointerPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mcode", "data-dir.json"), nil
}

func readPointer() (pointer, error) {
	path, err := pointerPath()
	if err != nil {
		return pointer{}, err
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return pointer{}, nil
	}
	if err != nil {
		return pointer{}, err
	}
	var p pointer
	if err := json.Unmarshal(b, &p); err != nil {
		return pointer{}, err
	}
	if !filepath.IsAbs(p.Path) || (p.MoveFrom != "" && !filepath.IsAbs(p.MoveFrom)) {
		return pointer{}, fmt.Errorf("数据目录指针必须使用绝对路径")
	}
	return p, nil
}

func writePointer(p pointer) error {
	path, err := pointerPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(path, b, 0o600)
}

func defaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mcode")
}

// Resolve 的优先级为显式 -data、MCODE_DATA、用户指针、默认目录。
func Resolve(flagValue string, flagSet bool) string {
	if flagSet {
		return flagValue
	}
	if dir := os.Getenv("MCODE_DATA"); dir != "" {
		return dir
	}
	p, err := readPointer()
	if err != nil {
		log.Printf("读取数据目录指针失败: %v", err)
	} else if p.Path != "" {
		return p.Path
	}
	return defaultDir()
}

// Export 设置当前进程的数据目录，并记录它是否由用户固定。
func Export(dir string, pinned bool) error {
	if err := os.Setenv("MCODE_DATA", dir); err != nil {
		return err
	}
	mu.Lock()
	exportedPinned = pinned
	mu.Unlock()
	return nil
}

// Pinned 报告当前数据目录是否由启动参数或环境变量固定。
func Pinned() bool {
	mu.Lock()
	defer mu.Unlock()
	return exportedPinned
}

// PrepareRelaunch 清理非固定目录的环境变量，让新进程重新应用待迁移指针。
func PrepareRelaunch() {
	mu.Lock()
	defer mu.Unlock()
	if !exportedPinned {
		_ = os.Unsetenv("MCODE_DATA")
	}
}

// SetPending 保存重启后要应用的迁移；此时不改动旧数据。
func SetPending(oldDir, newDir string) error {
	mu.Lock()
	defer mu.Unlock()
	if !filepath.IsAbs(newDir) {
		return fmt.Errorf("新数据目录必须为绝对路径")
	}
	oldDir, err := filepath.Abs(oldDir)
	if err != nil {
		return err
	}
	newDir = filepath.Clean(newDir)
	if err := validateTarget(oldDir, newDir); err != nil {
		return err
	}
	return writePointer(pointer{Path: newDir, MoveFrom: oldDir})
}

// resolvedPath 解析已有父目录的链接，避免通过目录别名把数据搬进自身。
func resolvedPath(path string) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	parent, err = resolvedPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func validateTarget(oldDir, newDir string) error {
	oldPath, err := resolvedPath(oldDir)
	if err != nil {
		return err
	}
	newPath, err := resolvedPath(newDir)
	if err != nil {
		return err
	}
	if rel, err := filepath.Rel(oldPath, newPath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("新数据目录不能等于旧目录或位于旧目录内")
	}
	entries, err := os.ReadDir(newDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("新数据目录必须为空")
	}
	return nil
}
