//go:build !unix && !windows

package procs

import (
	"fmt"
	"time"
)

// Descendants 在当前平台不支持：返回 ErrNotSupported。
func Descendants(rootPID int) ([]Proc, error) {
	return nil, fmt.Errorf("procs: Descendants(%d): %w", rootPID, ErrNotSupported)
}

// KillTreesStartedAfter 在当前平台不支持：返回 ErrNotSupported。
func KillTreesStartedAfter(rootPID int, since time.Time, grace time.Duration) (int, error) {
	return 0, fmt.Errorf("procs: KillTreesStartedAfter(%d): %w", rootPID, ErrNotSupported)
}
