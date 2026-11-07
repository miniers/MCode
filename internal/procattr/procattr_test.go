//go:build !windows

package procattr

import (
	"os/exec"
	"testing"
)

// TestHideWindowNoop 验证非 Windows 平台上 HideWindow 是空操作：
// 不改动命令的任何字段，只把同一个 *exec.Cmd 返回，便于调用处链式书写。
func TestHideWindowNoop(t *testing.T) {
	if got := HideWindow(nil); got != nil {
		t.Fatalf("HideWindow(nil) = %v，期望 nil", got)
	}

	cmd := exec.Command("git", "status")
	got := HideWindow(cmd)
	if got != cmd {
		t.Fatalf("HideWindow 返回了另一个 *exec.Cmd，期望原样返回")
	}
	if cmd.SysProcAttr != nil {
		t.Fatalf("HideWindow 改动了 SysProcAttr：%+v", cmd.SysProcAttr)
	}
	if cmd.Args[0] != "git" || cmd.Args[1] != "status" {
		t.Fatalf("HideWindow 改动了命令参数：%v", cmd.Args)
	}
}
