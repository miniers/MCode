//go:build windows

package mcode

import (
	"time"

	"mcode/internal/procs"
)

// KillRunningCommands 结束该线程登记的命令进程树：Windows 没有进程组信号，
// 按登记的根进程逐棵 taskkill /T，grace 后强制结束仍存活的进程。
func (h *Harness) KillRunningCommands(threadID string, grace time.Duration) (int, error) {
	killed := 0
	var firstErr error
	for _, pid := range h.ProcessRootPIDs(threadID) {
		ok, err := procs.KillTree(pid, grace)
		if ok {
			killed++
		}
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return killed, firstErr
}
