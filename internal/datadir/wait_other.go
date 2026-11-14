//go:build !windows

package datadir

import (
	"errors"
	"syscall"
	"time"
)

func waitProcessExit(pid int, timeout time.Duration) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
			return
		}
		select {
		case <-timer.C:
			return
		case <-ticker.C:
		}
	}
}
