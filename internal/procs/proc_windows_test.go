//go:build windows

package procs

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestKillTreeEndsRootAndDescendants 验证 KillTree 结束根进程与其子进程，
// 已退出的根进程返回 false。
func TestKillTreeEndsRootAndDescendants(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动测试进程失败: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	root := cmd.Process.Pid
	var children []Proc
	deadline := time.Now().Add(5 * time.Second)
	for len(children) == 0 && time.Now().Before(deadline) {
		children, _ = Descendants(root)
		time.Sleep(20 * time.Millisecond)
	}
	if len(children) == 0 {
		t.Fatal("没等到 ping 子进程")
	}

	ok, err := KillTree(root, 500*time.Millisecond)
	if !ok || err != nil {
		t.Fatalf("KillTree = %v, %v，期望 true, nil", ok, err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("根进程未退出")
	}
	for _, c := range children {
		if alive(c.PID) {
			t.Errorf("子进程 %d（%s）仍在运行", c.PID, c.Command)
		}
	}

	if ok, err := KillTree(root, 0); ok || err != nil {
		t.Fatalf("对已退出进程 KillTree = %v, %v，期望 false, nil", ok, err)
	}
}
