package git

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"mcode/internal/procattr"
)

const staleIndexLockAge = 10 * time.Second

// gitProcessRunning 可在测试中替换，用于判断本机是否仍有 git 进程。
var gitProcessRunning = defaultGitProcessRunning

// ClearStaleIndexLock 删除没有 git 进程且已超过 10 秒的残留索引锁。
func (r *Runner) ClearStaleIndexLock(ctx context.Context, root string) (bool, error) {
	indexPath, err := r.IndexPath(ctx, root)
	if err != nil {
		return false, err
	}
	lockPath := indexPath + ".lock"
	info, err := os.Stat(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if time.Since(info.ModTime()) < staleIndexLockAge {
		return false, nil
	}
	running, err := gitProcessRunning(ctx)
	if err != nil || running {
		return false, err
	}
	if err := os.Remove(lockPath); err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	log.Printf("git: 已清理残留索引锁 %s", lockPath)
	return true, nil
}

func defaultGitProcessRunning(ctx context.Context) (bool, error) {
	if runtime.GOOS == "windows" {
		cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(ctx, "tasklist", "/FI", "IMAGENAME eq git.exe", "/NH")))
		out, err := cmd.Output()
		if err != nil {
			return true, err
		}
		return strings.Contains(strings.ToLower(string(out)), "git.exe"), nil
	}
	cmd := procattr.TerminateOnCancel(procattr.HideWindow(exec.CommandContext(ctx, "pgrep", "-x", "git")))
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return true, err
}
