package main

import (
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
