//go:build unix

package mcode

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// KillRunningCommands 终止该线程登记的命令进程组。
func (h *Harness) KillRunningCommands(threadID string, grace time.Duration) (int, error) {
	var groups []int
	var firstErr error
	for _, pid := range h.ProcessRootPIDs(threadID) {
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
			if !errors.Is(err, syscall.ESRCH) && firstErr == nil {
				firstErr = fmt.Errorf("终止进程组 %d: %w", pid, err)
			}
			continue
		}
		groups = append(groups, pid)
	}

	deadline := time.Now().Add(grace)
	for len(groups) > 0 && time.Now().Before(deadline) {
		alive := false
		for _, pid := range groups {
			if err := syscall.Kill(-pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
				alive = true
				break
			}
		}
		if !alive {
			break
		}
		time.Sleep(min(20*time.Millisecond, time.Until(deadline)))
	}
	for _, pid := range groups {
		if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && firstErr == nil {
			firstErr = fmt.Errorf("强制终止进程组 %d: %w", pid, err)
		}
	}
	return len(groups), firstErr
}
