package app

import (
	"os"
	"path/filepath"
	"testing"

	"mcode/internal/automation"
)

// TestAutomationServiceReceivesDataDir 验证 app 把 DataDir 注入自动化服务：
// 启动后取得 <DataDir>/automation/scheduler.lock，关闭后锁文件仍保留。
func TestAutomationServiceReceivesDataDir(t *testing.T) {
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

	svc := instance.Automation()
	if svc == nil {
		_ = instance.Close()
		t.Fatal("automation service not wired")
	}
	lockPath := filepath.Join(dataDir, "automation", "scheduler.lock")
	if state := svc.SchedulerLockState().State; state != automation.SchedulerLockActive {
		_ = instance.Close()
		t.Fatalf("scheduler lock state = %q, want %q", state, automation.SchedulerLockActive)
	}
	if _, err := os.Stat(lockPath); err != nil {
		_ = instance.Close()
		t.Fatalf("scheduler lock file missing: %v", err)
	}

	if err := instance.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("scheduler lock file removed on close: %v", err)
	}
}
