//go:build windows

package procattr

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestTerminateTreeEndsRootAndChildren 验证 TerminateTree 结束根进程与它派生的 ping 子进程。
func TestTerminateTreeEndsRootAndChildren(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "ping -n 30 127.0.0.1 >nul")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动测试进程失败: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	root := cmd.Process.Pid
	var children []int
	deadline := time.Now().Add(15 * time.Second)
	for len(children) == 0 && time.Now().Before(deadline) {
		if parents, err := processParents(); err == nil {
			children = collectDescendantPIDs(root, parents, 0)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(children) == 0 {
		t.Fatal("没等到 ping 子进程")
	}

	if err := TerminateTree(root); err != nil {
		t.Fatalf("TerminateTree(%d) = %v，期望 nil", root, err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("根进程未在 5 秒内退出")
	}
	for _, pid := range children {
		if waitGone(pid, 5*time.Second) {
			continue
		}
		t.Errorf("子进程 %d 仍在运行", pid)
	}
}

// TestTerminateTreeExitedProcess 验证对已退出进程的 PID 调用 TerminateTree 返回 nil。
func TestTerminateTreeExitedProcess(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Run(); err != nil {
		t.Fatalf("启动测试进程失败: %v", err)
	}
	if err := TerminateTree(cmd.Process.Pid); err != nil {
		t.Fatalf("对已退出进程 TerminateTree(%d) = %v，期望 nil", cmd.Process.Pid, err)
	}
}

// waitGone 在超时内轮询等待进程退出，返回它是否已退出。
func waitGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !stillRunning(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
