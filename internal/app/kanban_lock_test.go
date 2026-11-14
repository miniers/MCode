package app

import (
	"path/filepath"
	"testing"
	"time"

	"mcode/internal/filelock"
)

// TestCloseReleasesKanbanSchedulerLock 验证 App.Close 释放看板调度锁：
// 打开后调度协程取得 <DataDir>/kanban-scheduler.lock，关闭后同一进程能重新获取。
func TestCloseReleasesKanbanSchedulerLock(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	instance, err := Open(Options{
		DataDir:  dataDir,
		Provider: "memory",
		WorkDir:  t.TempDir(),
		Mode:     "agent",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	lockPath := filepath.Join(dataDir, "kanban-scheduler.lock")

	// 调度协程在后台首次取锁：轮询等到锁被持有再关闭，避免误判关闭前后的时序。
	held := false
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		lock, acquireErr := filelock.TryAcquire(lockPath)
		if acquireErr != nil {
			held = true
			break
		}
		_ = lock.Close()
		time.Sleep(10 * time.Millisecond)
	}
	if !held {
		_ = instance.Close()
		t.Fatal("打开后看板调度锁没有被持有")
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	lock, err := filelock.TryAcquire(lockPath)
	if err != nil {
		t.Fatalf("Close 未释放看板调度锁: %v", err)
	}
	_ = lock.Close()
}
