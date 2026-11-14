package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckSandboxDir(t *testing.T) {
	root := t.TempDir()
	temp := filepath.Join(root, "temp")
	systemTemp := filepath.Join(root, "system-temp")
	defaultDir := filepath.Join(temp, "default")
	tests := []struct {
		name, path, systemTemp string
		wantErr                bool
	}{
		{"temporary child", filepath.Join(temp, "sandbox"), systemTemp, false},
		{"temporary root", temp, systemTemp, true},
		{"outside temporary directories", filepath.Join(root, "outside"), systemTemp, true},
		{"inside default directory", filepath.Join(defaultDir, "sandbox"), systemTemp, true},
		{"same temporary directories", filepath.Join(temp, "sandbox"), temp, false},
		{"system temporary child", filepath.Join(systemTemp, "sandbox"), systemTemp, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkSandboxDir(tt.path, temp, tt.systemTemp, defaultDir)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkSandboxDir() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
	t.Run("same directory in error", func(t *testing.T) {
		err := checkSandboxDir(temp, temp, temp, defaultDir)
		if err == nil || strings.Count(err.Error(), temp) != 1 {
			t.Fatalf("expected temporary directory once in error, got %v", err)
		}
	})
}

func sandboxTestDirs(t *testing.T) (temp, systemTemp, defaultDir string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	temp = filepath.Join(root, "temp")
	if err := os.MkdirAll(temp, 0o755); err != nil {
		t.Fatal(err)
	}
	return temp, filepath.Join(root, "system-temp"), filepath.Join(temp, "default")
}

func TestPrepareSandboxDirCreatesMissingTemporaryDirectory(t *testing.T) {
	temp, systemTemp, defaultDir := sandboxTestDirs(t)
	target := filepath.Join(temp, "sandbox", "nested")
	dir, err := prepareSandboxDir(target, temp, systemTemp, defaultDir)
	if err != nil {
		t.Fatalf("prepareSandboxDir() error = %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("数据目录未创建: %v", err)
	}
	if err := checkSandboxDir(dir, temp, systemTemp, defaultDir); err != nil {
		t.Fatalf("创建的目录未通过校验: %v", err)
	}
}

func TestPrepareSandboxDirRejectsOutsideWithoutCreating(t *testing.T) {
	temp, systemTemp, defaultDir := sandboxTestDirs(t)
	target := filepath.Join(filepath.Dir(temp), "outside", "sandbox")
	if dir, err := prepareSandboxDir(target, temp, systemTemp, defaultDir); err == nil || dir != "" {
		t.Fatalf("prepareSandboxDir() = %q, %v，期望错误", dir, err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("不应创建目录: %v", err)
	}
}
