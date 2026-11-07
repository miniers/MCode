//go:build windows

package procs

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive 是 GetExitCodeProcess 表示「进程仍在运行」的返回码，x/sys/windows 未导出该常量。
const stillActive = 259

// procTable 用 Toolhelp32 进程快照读取整机进程表。
// Windows 上拿不到进程组 ID 与完整命令行：PGID 置 0，Command 只填可执行文件名。
// 启动时间不在这里填，由 fillStartTimes 按需读取，避免为整机每个进程都打开句柄。
func procTable() ([]Proc, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("procs: 创建进程快照失败: %w", err)
	}
	defer windows.CloseHandle(snapshot)

	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("procs: 读取进程表失败: %w", err)
	}
	table := make([]Proc, 0, 256)
	for {
		table = append(table, Proc{
			PID:     int(entry.ProcessID),
			PPID:    int(entry.ParentProcessID),
			Command: windows.UTF16ToString(entry.ExeFile[:]),
		})
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			// 快照遍历结束（ERROR_NO_MORE_FILES）或读取中断，都按已读到的部分返回。
			break
		}
	}
	return table, nil
}

// Descendants 返回 rootPID 的全部后代进程（不含 rootPID 自身），按 ppid 递归收集。
func Descendants(rootPID int) ([]Proc, error) {
	if rootPID <= 0 {
		return nil, fmt.Errorf("procs: 非法 rootPID %d", rootPID)
	}
	table, err := procTable()
	if err != nil {
		return nil, err
	}
	return collectDescendants(rootPID, table), nil
}

// KillTreesStartedAfter 终止 rootPID 后代中「启动时间不早于 since 减 2 秒、且父进程启动时间早于该阈值」
// 的子树：先用 taskkill /T（不带 /F）请求整棵子树退出，grace 之后对仍存活的进程 TerminateProcess。
// 返回被请求过终止的进程数；常驻子进程（如会话开始时就启动的 MCP server）不会被选中。
func KillTreesStartedAfter(rootPID int, since time.Time, grace time.Duration) (int, error) {
	if rootPID <= 0 {
		return 0, fmt.Errorf("procs: 非法 rootPID %d", rootPID)
	}
	if since.IsZero() {
		return 0, fmt.Errorf("procs: since 不能为空")
	}
	table, err := procTable()
	if err != nil {
		return 0, err
	}
	// selectSubtreeRoots 要比较后代与其父进程的启动时间，先只给这些进程取创建时间，
	// 再重新收集一次后代，让返回的 Proc 带上 StartTime。
	fillStartTimes(table, collectDescendants(rootPID, table))
	descendants := collectDescendants(rootPID, table)
	roots := selectSubtreeRoots(table, descendants, since)
	if len(roots) == 0 {
		return 0, nil
	}

	// 汇总所有待终止的进程，按叶到根排列，并去重（多棵子树不会重叠，这里只是兜底）。
	var planned []Proc
	seen := make(map[int]bool)
	for _, root := range roots {
		for _, p := range subtreeLeafFirst(root, descendants) {
			if p.PID <= 0 || p.PID == os.Getpid() || seen[p.PID] {
				continue
			}
			seen[p.PID] = true
			planned = append(planned, p)
		}
	}
	if len(planned) == 0 {
		return 0, nil
	}

	// taskkill /T 一次就覆盖整棵子树，因此按子树根发请求；返回值仍按 planned 计数，
	// 与 unix 版「被发送过终止信号的进程数」保持同一语义。
	pids := make([]int, 0, len(planned))
	for _, p := range planned {
		pids = append(pids, p.PID)
	}
	for _, root := range roots {
		_ = taskkill(root.PID)
	}
	waitExit(pids, grace)
	for _, pid := range pids {
		if alive(pid) {
			_ = terminate(pid)
		}
	}
	return len(planned), nil
}

// fillStartTimes 只对后代及其父进程读取创建时间：筛选子树根时也只用到这两类进程。
func fillStartTimes(table []Proc, descendants []Proc) {
	need := make(map[int]bool, 2*len(descendants))
	for _, d := range descendants {
		need[d.PID] = true
		need[d.PPID] = true
	}
	for i := range table {
		if need[table[i].PID] {
			table[i].StartTime = processStartTime(table[i].PID)
		}
	}
}

// processStartTime 读取进程创建时间；拿不到（已退出或无权限）时返回零值。
func processStartTime(pid int) time.Time {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return time.Time{}
	}
	defer windows.CloseHandle(h)

	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}
	}
	return time.Unix(0, creation.Nanoseconds())
}

// taskkill 请求整棵进程树正常退出（不带 /F）；taskkill 自身也隐藏控制台窗口。
func taskkill(pid int) error {
	cmd := exec.Command("taskkill", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	return cmd.Run()
}

// terminate 强制结束进程。
func terminate(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return windows.TerminateProcess(h, 1)
}

// alive 判断进程是否仍存在：能打开句柄且退出码仍是 STILL_ACTIVE 视为存活。
func alive(pid int) bool {
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

// waitExit 在 grace 内轮询等待，直到所有进程都不再存在。
func waitExit(pids []int, grace time.Duration) {
	if grace <= 0 {
		return
	}
	deadline := time.Now().Add(grace)
	for {
		remaining := false
		for _, pid := range pids {
			if alive(pid) {
				remaining = true
				break
			}
		}
		if !remaining || !time.Now().Before(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
