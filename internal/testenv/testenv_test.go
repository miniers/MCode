package testenv

import (
	"os"
	"runtime"
	"testing"
)

func TestTempHome(t *testing.T) {
	dir := TempHome(t)
	if os.Getenv("HOME") != dir || os.Getenv("USERPROFILE") != dir {
		t.Fatalf("HOME 和 USERPROFILE 应为 %q", dir)
	}
	if runtime.GOOS == "windows" {
		home, err := os.UserHomeDir()
		if err != nil || home != dir {
			t.Fatalf("os.UserHomeDir() = %q, %v; 要求 %q", home, err, dir)
		}
	}
}

func TestSetHome(t *testing.T) {
	SetHome(t, "x")
	if os.Getenv("HOME") != "x" || os.Getenv("USERPROFILE") != "x" {
		t.Fatal("HOME 和 USERPROFILE 应为 x")
	}
}
