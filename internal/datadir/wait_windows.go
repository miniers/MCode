package datadir

import (
	"os"
	"time"
)

func waitProcessExit(pid int, timeout time.Duration) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	defer p.Release()
	done := make(chan struct{})
	go func() {
		_, _ = p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}
