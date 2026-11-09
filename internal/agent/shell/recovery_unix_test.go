//go:build unix

package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRunDetachedChildTimeout(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid 不可用")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	start := time.Now()
	result, err := Run(context.Background(), Options{Command: "setsid sleep 60 & echo $! > '" + pidFile + "'; sleep 60", Timeout: 100 * time.Millisecond}, nil)
	if data, e := os.ReadFile(pidFile); e == nil {
		if pid, e := strconv.Atoi(strings.TrimSpace(string(data))); e == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	if err != nil || !result.TimedOut || time.Since(start) > 7*time.Second {
		t.Fatalf("result=%+v err=%v elapsed=%v", result, err, time.Since(start))
	}
}

func TestBackgroundTimeoutAndReap(t *testing.T) {
	id, _, err := Start(context.Background(), Options{Command: "sleep 60", Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = Kill(id) })
	deadline := time.Now().Add(6 * time.Second)
	for {
		backgroundMu.Lock()
		job := backgroundJobs[id]
		backgroundMu.Unlock()
		job.mu.Lock()
		running, timedOut := job.running, job.timedOut
		job.mu.Unlock()
		if !running {
			if !timedOut {
				t.Fatal("未标记超时")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("后台任务未退出")
		}
		time.Sleep(10 * time.Millisecond)
	}
	backgroundMu.Lock()
	job := backgroundJobs[id]
	job.mu.Lock()
	job.finishedAt = time.Now().Add(-backgroundMaxAge - time.Second)
	job.mu.Unlock()
	backgroundCount()
	_, exists := backgroundJobs[id]
	backgroundMu.Unlock()
	if exists {
		t.Fatal("已结束任务未回收")
	}
}
