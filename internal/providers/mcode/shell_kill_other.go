//go:build !unix && !windows

package mcode

import (
	"time"

	"mcode/internal/procs"
)

// KillRunningCommands 在当前平台不支持进程组终止。
func (h *Harness) KillRunningCommands(threadID string, grace time.Duration) (int, error) {
	return 0, procs.ErrNotSupported
}
