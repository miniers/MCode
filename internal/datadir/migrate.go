package datadir

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"mcode/internal/fsutil"
	"mcode/internal/store"
)

var rename = fsutil.Rename
var waitParentExit = waitProcessExit
var migrationErr error

// MigrationError 返回本进程最近一次 ApplyPending 的失败；成功或未迁移时为 nil。
func MigrationError() error {
	mu.Lock()
	defer mu.Unlock()
	return migrationErr
}

// ApplyPending 在数据库打开前迁移数据；失败时搬回已迁移条目并恢复旧指针。
func ApplyPending() (dir string, err error) {
	mu.Lock()
	defer mu.Unlock()
	parentPID := os.Getenv(relaunchParentEnv)
	_ = os.Unsetenv(relaunchParentEnv)
	defer func() { migrationErr = err }()
	p, err := readPointer()
	if err != nil {
		return defaultDir(), err
	}
	if p.MoveFrom == "" {
		if p.Path != "" {
			return p.Path, nil
		}
		return defaultDir(), nil
	}
	if pid, parseErr := strconv.Atoi(parentPID); parseErr == nil && pid > 0 {
		waitParentExit(pid, 30*time.Second)
	}
	oldDir, newDir := p.MoveFrom, p.Path
	var moved []string
	var dbBackup string
	defer func() {
		if err == nil {
			if dbBackup != "" {
				_ = os.RemoveAll(dbBackup)
			}
			return
		}
		// 改写数据库前保存物理文件，回退时不会误改原本已指向新目录的路径。
		if dbBackup != "" {
			if e := restoreDatabase(dbBackup, newDir); e != nil {
				err = errors.Join(err, fmt.Errorf("恢复数据库失败，备份保留在 %s: %w", dbBackup, e))
			} else {
				_ = os.RemoveAll(dbBackup)
			}
		}
		for i := len(moved) - 1; i >= 0; i-- {
			src, dst := filepath.Join(newDir, moved[i]), filepath.Join(oldDir, moved[i])
			if _, e := os.Lstat(src); errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e := moveBack(src, dst); e != nil {
				err = errors.Join(err, fmt.Errorf("回退 %s 失败，数据保留在 %s: %w", moved[i], src, e))
			}
		}
		if len(moved) > 0 {
			repairWorktrees(oldDir)
		}
		err = errors.Join(err, writePointer(pointer{Path: oldDir}))
		dir = oldDir
	}()
	if err = validateTarget(oldDir, newDir); err != nil {
		return oldDir, err
	}
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		return oldDir, err
	}
	info, err := os.Stat(oldDir)
	if err != nil {
		return oldDir, err
	}
	if err = os.MkdirAll(newDir, info.Mode().Perm()); err != nil {
		return oldDir, err
	}
	for _, entry := range entries {
		moved = append(moved, entry.Name())
		if err = moveEntry(filepath.Join(oldDir, entry.Name()), filepath.Join(newDir, entry.Name())); err != nil {
			return oldDir, fmt.Errorf("迁移 %s: %w", entry.Name(), err)
		}
	}
	repairWorktrees(newDir)
	if _, statErr := os.Stat(filepath.Join(newDir, "mcode.db")); statErr == nil {
		var backup string
		backup, err = os.MkdirTemp("", "mcode-db-move-*")
		if err != nil {
			return oldDir, err
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			src := filepath.Join(newDir, "mcode.db"+suffix)
			if _, e := os.Lstat(src); errors.Is(e, os.ErrNotExist) {
				continue
			}
			if err = copyTree(src, filepath.Join(backup, "mcode.db"+suffix)); err != nil {
				_ = os.RemoveAll(backup)
				return oldDir, err
			}
		}
		dbBackup = backup
	}
	if err = store.RewritePathPrefix(filepath.Join(newDir, "mcode.db"), oldDir, newDir); err != nil {
		return oldDir, err
	}
	if err = writePointer(pointer{Path: newDir}); err != nil {
		return oldDir, err
	}
	return newDir, nil
}

func restoreDatabase(backup, dir string) error {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		src := filepath.Join(backup, "mcode.db"+suffix)
		dst := filepath.Join(dir, "mcode.db"+suffix)
		if _, err := os.Lstat(src); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		temp, err := os.CreateTemp(dir, ".mcode-db-restore-*")
		if err != nil {
			return err
		}
		_ = temp.Close()
		tempPath := temp.Name()
		err = copyTree(src, tempPath)
		if err == nil {
			err = fsutil.Rename(tempPath, dst)
		}
		_ = os.Remove(tempPath)
		if err != nil {
			return err
		}
	}
	return nil
}

func moveEntry(src, dst string) error {
	if err := rename(src, dst); err == nil {
		return nil
	}
	if err := copyTree(src, dst); err != nil {
		return errors.Join(err, os.RemoveAll(dst))
	}
	return os.RemoveAll(src)
}

func moveBack(src, dst string) error {
	if _, err := os.Lstat(dst); errors.Is(err, os.ErrNotExist) {
		return moveEntry(src, dst)
	}
	// 删除旧条目失败时旧目录可能只剩部分内容；完整副本仍在新目录。
	if err := copyTree(src, dst); err != nil {
		return err
	}
	return os.RemoveAll(src)
}

func copyTree(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		if err := os.MkdirAll(dst, info.Mode().Perm()|0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dst, info.Mode().Perm())
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("不支持复制文件类型 %s: %s", info.Mode(), src)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if copyErr == nil {
		copyErr = out.Chmod(info.Mode().Perm())
	}
	if copyErr == nil {
		copyErr = out.Sync()
	}
	return errors.Join(copyErr, out.Close())
}

func repairWorktrees(dir string) {
	for _, name := range []string{"worktrees", "swarm-worktrees"} {
		root := filepath.Join(dir, name)
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			log.Printf("读取 worktree 目录 %s 失败: %v", root, err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name())
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			cmd := exec.CommandContext(ctx, "git", "worktree", "repair")
			cmd.Dir = path
			cmd.WaitDelay = time.Second
			output, err := cmd.CombinedOutput()
			cancel()
			if err != nil {
				log.Printf("修复 worktree %s 失败: %v: %s", path, err, output)
			}
		}
	}
}
