package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestTryAcquireSecondBusy 验证同一路径的第二个非阻塞加锁返回 ErrBusy，释放后可以再次获取。
func TestTryAcquireSecondBusy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.lock")

	first, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("first TryAcquire: %v", err)
	}

	if _, err := TryAcquire(path); !errors.Is(err, ErrBusy) {
		t.Fatalf("second TryAcquire error = %v, want ErrBusy", err)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}

	third, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("third TryAcquire after release: %v", err)
	}
	if err := third.Close(); err != nil {
		t.Fatalf("close third lock: %v", err)
	}
}

// TestAcquireBlocksUntilRelease 验证阻塞加锁会等待持有者释放，而不是立即返回。
func TestAcquireBlocksUntilRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.lock")

	holder, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	type result struct {
		lock *Lock
		err  error
	}
	done := make(chan result, 1)
	go func() {
		l, err := Acquire(path)
		done <- result{lock: l, err: err}
	}()

	// 持有者未释放前，阻塞加锁不应返回。
	select {
	case res := <-done:
		if res.lock != nil {
			_ = res.lock.Close()
		}
		t.Fatalf("Acquire returned before release: %v", res.err)
	case <-time.After(100 * time.Millisecond):
	}

	if err := holder.Close(); err != nil {
		t.Fatalf("release holder: %v", err)
	}

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("Acquire after release: %v", res.err)
		}
		if err := res.lock.Close(); err != nil {
			t.Fatalf("close acquired lock: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Acquire did not return after release")
	}
}

// TestLockFileSurvivesRelease 验证释放只关闭描述符，不删除也不清空锁文件。
func TestLockFileSurvivesRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "automation", "scheduler.lock")

	lock, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if err := lock.WriteInfo(map[string]any{"pid": os.Getpid()}); err != nil {
		t.Fatalf("WriteInfo: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("lock file removed on release: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("lock file content was cleared on release")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock file: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("lock file content is empty after release")
	}

	again, err := TryAcquire(path)
	if err != nil {
		t.Fatalf("TryAcquire after release: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("close reacquired lock: %v", err)
	}
}
