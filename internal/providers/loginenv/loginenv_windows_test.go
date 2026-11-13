//go:build windows

package loginenv

import (
	"os"
	"strings"
	"testing"
)

// TestFallbackEnvironmentSinglePath 验证 Windows 上进程的 Path 被扩充进唯一的 PATH，
// 不会出现 Path 与 PATH 并存、子进程丢掉系统目录的情况。
func TestFallbackEnvironmentSinglePath(t *testing.T) {
	const marker = `C:\mcode-system-path-marker`
	// 资源管理器启动的进程里变量名是 Path；先删掉现有的同名变量，确保这里用的就是 Path。
	t.Setenv("PATH", "")
	if err := os.Unsetenv("PATH"); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("Path", marker); err != nil {
		t.Fatal(err)
	}

	var paths []string
	for _, kv := range fallbackEnvironment() {
		if key, value, ok := strings.Cut(kv, "="); ok && strings.EqualFold(key, "PATH") {
			paths = append(paths, kv)
			if !strings.Contains(value, marker) {
				t.Errorf("PATH 丢了进程原有的目录：%s", value)
			}
		}
	}
	if len(paths) != 1 {
		t.Fatalf("应只有一个 PATH 条目，得到 %d 个：%q", len(paths), paths)
	}
}
