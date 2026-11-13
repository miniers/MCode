//go:build windows

package shell

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGitBashFor 验证由 git.exe 的各种安装位置推出同一安装里的 bash.exe。
func TestGitBashFor(t *testing.T) {
	root := `C:\Program Files\Git`
	bin := filepath.Join(root, "bin", "bash.exe")
	usr := filepath.Join(root, "usr", "bin", "bash.exe")
	cases := []struct {
		name    string
		git     string
		present []string
		want    string
	}{
		{"cmd 目录", filepath.Join(root, "cmd", "git.exe"), []string{bin, usr}, bin},
		{"mingw64 目录", filepath.Join(root, "mingw64", "bin", "git.exe"), []string{bin}, bin},
		{"只有 usr\\bin", filepath.Join(root, "cmd", "git.exe"), []string{usr}, usr},
		{"安装里没有 bash", filepath.Join(root, "cmd", "git.exe"), nil, ""},
	}
	for _, tc := range cases {
		exists := func(p string) bool {
			for _, q := range tc.present {
				if p == q {
					return true
				}
			}
			return false
		}
		if got := gitBashFor(tc.git, exists); got != tc.want {
			t.Errorf("%s: gitBashFor(%q) = %q，期望 %q", tc.name, tc.git, got, tc.want)
		}
	}
}

// TestIsWSLLauncher 验证 System32 与 WindowsApps 下的 bash.exe 被识别为 WSL 启动器。
func TestIsWSLLauncher(t *testing.T) {
	system32 := filepath.Join(os.Getenv("SystemRoot"), "System32", "bash.exe")
	cases := map[string]bool{
		system32: true,
		`C:\Users\me\AppData\Local\Microsoft\WindowsApps\bash.exe`: true,
		`C:\Program Files\Git\bin\bash.exe`:                        false,
		`C:\msys64\usr\bin\bash.exe`:                               false,
	}
	for path, want := range cases {
		if got := isWSLLauncher(path); got != want {
			t.Errorf("isWSLLauncher(%q) = %v，期望 %v", path, got, want)
		}
	}
}
