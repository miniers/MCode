package codex

import (
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestResidentProcessKillCloseConcurrent(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = killProcessGroup(cmd, syscall.SIGKILL)
	})

	var cleanups atomic.Int32
	proc := &residentProcess{
		cmd:    cmd,
		stdin:  stdin,
		deadCh: make(chan struct{}),
		cleanup: func() {
			cleanups.Add(1)
		},
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	killDone := make(chan struct{})
	go func() {
		defer wg.Done()
		<-start
		proc.kill()
		close(killDone)
	}()

	closeDone := make(chan struct{})
	var closeErr error
	go func() {
		defer wg.Done()
		<-start
		closeErr = proc.close()
		close(closeDone)
	}()

	close(start)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for concurrent kill and close")
	}

	if closeErr != nil {
		t.Fatalf("close() returned error: %v", closeErr)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("cleanup count = %d, want 1", got)
	}

	// 再次调用也必须安全且不重复执行 cleanup。
	proc.kill()
	if err := proc.close(); err != nil {
		t.Fatalf("second close() returned error: %v", err)
	}
	if got := cleanups.Load(); got != 1 {
		t.Fatalf("cleanup count after repeat calls = %d, want 1", got)
	}
}
