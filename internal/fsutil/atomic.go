package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WriteFileAtomic replaces the target with a synced file, preserving symlinks.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		path, err = filepath.EvalSymlinks(path)
		if err != nil {
			return fmt.Errorf("resolve symlink: %w", err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat target: %w", err)
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".mcode-write-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := Rename(f.Name(), path); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	if runtime.GOOS != "windows" {
		if d, err := os.Open(dir); err == nil {
			_ = d.Sync()
			_ = d.Close()
		}
	}
	return nil
}
