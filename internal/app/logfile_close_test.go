package app

import (
	"log"
	"os"
	"path/filepath"
	"testing"
)

// TestCloseReleasesLogFile 验证 App.Close 关闭 mcode.log 句柄：
// 关闭后标准库日志输出不再指向该 App，且日志文件能被改名（Windows 上句柄未关会失败）。
func TestCloseReleasesLogFile(t *testing.T) {
	prev := log.Writer()
	t.Cleanup(func() { log.SetOutput(prev) })

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
	closed := false
	defer func() {
		if !closed {
			_ = instance.Close()
		}
	}()

	if log.Writer() == prev {
		t.Fatalf("Open 后日志输出仍是打开前的 writer")
	}
	if err := instance.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	closed = true
	if log.Writer() == instance.logOut {
		t.Fatalf("Close 后日志输出仍指向该 App 的 writer")
	}

	dir, err := logDir(dataDir)
	if err != nil {
		t.Fatalf("logDir: %v", err)
	}
	logPath := filepath.Join(dir, logFileName)
	renamed := logPath + ".renamed"
	if err := os.Rename(logPath, renamed); err != nil {
		t.Fatalf("Close 未释放日志文件句柄: %v", err)
	}
	if err := os.Rename(renamed, logPath); err != nil {
		t.Fatalf("恢复日志文件名: %v", err)
	}
}
