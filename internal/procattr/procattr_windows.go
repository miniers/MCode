//go:build windows

package procattr

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive 是 GetExitCodeProcess 表示「进程仍在运行」的返回码，x/sys/windows 未导出该常量。
const stillActive = 259

func configureShellCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if err := TerminateTree(cmd.Process.Pid); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
}

// TerminateTree 在进程内强制结束 pid 及其全部后代，取消路径不再启动 taskkill 子进程。
// 先用 Toolhelp32 快照按父子关系广度优先收集后代，再 TerminateProcess：先结束根进程
// （防止它继续派生），再结束收集到的后代。创建时间早于其父进程的后代不视为后代
// （防 PID 复用误杀），拿不到创建时间的后代跳过（多半已退出），本进程永远跳过。
// 已退出的进程被忽略；只有根进程仍在运行却结束失败时返回错误。
func TerminateTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	self := os.Getpid()
	var descendants []int
	if parents, err := processParents(); err == nil {
		descendants = collectDescendantPIDs(pid, parents, self)
	}
	var rootErr error
	if pid != self {
		if err := terminateProcess(pid); err != nil && stillRunning(pid) {
			rootErr = fmt.Errorf("procattr: 强制结束进程 %d: %w", pid, err)
		}
	}
	for _, child := range descendants {
		if child != self {
			_ = terminateProcess(child)
		}
	}
	return rootErr
}

// processParents 用进程快照读出整机进程表的 子 PID → 父 PID 映射。
func processParents() (map[int]int, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("procattr: 创建进程快照失败: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("procattr: 读取进程表失败: %w", err)
	}
	parents := make(map[int]int, 256)
	for {
		parents[int(entry.ProcessID)] = int(entry.ParentProcessID)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			// 遍历结束（ERROR_NO_MORE_FILES）或读取中断，按已读到的部分返回。
			break
		}
	}
	return parents, nil
}

// collectDescendantPIDs 从 root 出发广度优先收集后代 PID（不含 root 自身）。
func collectDescendantPIDs(root int, parents map[int]int, self int) []int {
	children := make(map[int][]int, len(parents))
	for pid, ppid := range parents {
		children[ppid] = append(children[ppid], pid)
	}
	type pending struct {
		pid      int
		start    time.Time
		startSet bool
	}
	rootStart, rootStartSet := processStartTime(root)
	queue := []pending{{pid: root, start: rootStart, startSet: rootStartSet}}
	descendants := make([]int, 0, 16)
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for _, child := range children[parent.pid] {
			if child == self || child == parent.pid {
				continue
			}
			start, ok := processStartTime(child)
			if !ok {
				continue
			}
			if parent.startSet && start.Before(parent.start) {
				continue
			}
			descendants = append(descendants, child)
			queue = append(queue, pending{pid: child, start: start, startSet: true})
		}
	}
	return descendants
}

// processStartTime 读取进程创建时间；打不开句柄或读取失败时 ok 为 false。
func processStartTime(pid int) (time.Time, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}, false
	}
	defer windows.CloseHandle(h)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, false
	}
	return time.Unix(0, creation.Nanoseconds()), true
}

// terminateProcess 强制结束进程；进程已退出（打不开句柄）时返回 nil。
func terminateProcess(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// stillRunning 判断进程是否仍存活：能打开句柄且退出码仍是 STILL_ACTIVE。
func stillRunning(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}

// HideWindow 让 cmd 在 Windows 上以不创建控制台窗口的方式启动。
// cmd 为 nil 时原样返回；已有的 SysProcAttr 字段会被保留（按位或追加创建标志）。
// 返回 cmd 本身，便于链式调用。
func HideWindow(cmd *exec.Cmd) *exec.Cmd {
	if cmd == nil {
		return cmd
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = new(syscall.SysProcAttr)
	}
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
	cmd.SysProcAttr.HideWindow = true
	return cmd
}

// TerminateOnCancel 在 Windows 上保持默认行为：Windows 没有可用的 SIGTERM。
func TerminateOnCancel(cmd *exec.Cmd) *exec.Cmd {
	return cmd
}
