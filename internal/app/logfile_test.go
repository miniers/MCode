package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateLogDir 把用户配置目录指向临时目录，并清空已记录的日志路径。
func isolateLogDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	logFileMu.Lock()
	logFilePath = ""
	logFileMu.Unlock()
	t.Cleanup(func() {
		logFileMu.Lock()
		logFilePath = ""
		logFileMu.Unlock()
	})
	return home
}

func TestOpenLogFileRotatesWhenOversized(t *testing.T) {
	isolateLogDir(t)

	f, path, err := openLogFile("")
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("mcode", "logs", logFileName)) {
		t.Fatalf("unexpected log path: %s", path)
	}
	if _, err := f.Write([]byte(strings.Repeat("x", int(maxLogFileSize)+1))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current log: %v", err)
	}
	if info.Size() <= maxLogFileSize {
		t.Fatalf("expected oversized log, got %d", info.Size())
	}

	f2, path2, err := openLogFile("")
	if err != nil {
		t.Fatalf("openLogFile after rotate: %v", err)
	}
	if path2 != path {
		t.Fatalf("log path changed: %s != %s", path2, path)
	}

	rotated := filepath.Join(filepath.Dir(path), rotatedLogFileName)
	rotatedInfo, err := os.Stat(rotated)
	if err != nil {
		t.Fatalf("rotated log missing: %v", err)
	}
	if rotatedInfo.Size() != info.Size() {
		t.Fatalf("rotated size %d != %d", rotatedInfo.Size(), info.Size())
	}
	if err := f2.Close(); err != nil {
		t.Fatalf("close new log: %v", err)
	}
	fresh, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat fresh log: %v", err)
	}
	if fresh.Size() != 0 {
		t.Fatalf("expected fresh empty log, got %d", fresh.Size())
	}
}

func TestOpenLogFileKeepsSmallFile(t *testing.T) {
	isolateLogDir(t)

	f, path, err := openLogFile("")
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}
	if _, err := f.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()

	f2, _, err := openLogFile("")
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	f2.Close()

	if _, err := os.Stat(filepath.Join(filepath.Dir(path), rotatedLogFileName)); !os.IsNotExist(err) {
		t.Fatalf("small log should not rotate, err=%v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if string(data) != "hello\n" {
		t.Fatalf("log content preserved? got %q", string(data))
	}
}

// 传入数据目录时日志写在 <dataDir>/logs，与 mcode.db 同处，不同数据目录的实例互不混写。
func TestOpenLogFileUsesDataDir(t *testing.T) {
	isolateLogDir(t)
	dataDir := t.TempDir()

	f, path, err := openLogFile(dataDir)
	if err != nil {
		t.Fatalf("openLogFile: %v", err)
	}
	f.Close()
	if want := filepath.Join(dataDir, "logs", logFileName); path != want {
		t.Fatalf("log path = %s, want %s", path, want)
	}
	if got := logFilePaths(); len(got) != 2 || got[1] != path {
		t.Fatalf("logFilePaths 应跟随实际打开的路径，得到 %v", got)
	}
}
