//go:build !windows

package procattr

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
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

func TestTerminateOnCancelSendsSIGTERM(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := TerminateOnCancel(exec.CommandContext(ctx, "sh", "-c", `trap 'exit 7' TERM; while :; do sleep 0.1; done`))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	started := time.Now()
	cancel()
	err := cmd.Wait()
	if err == nil || !strings.Contains(err.Error(), "exit status 7") {
		t.Fatalf("取消后退出错误=%v，期望 SIGTERM 处理后的退出码 7", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("取消后进程退出过慢: %s", elapsed)
	}
}
