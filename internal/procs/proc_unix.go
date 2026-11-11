//go:build unix

package procs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"mcode/internal/procattr"
)

// Snapshot 执行 ps 读取整机进程表。LC_ALL=C 保证 lstart 用英文星期与月份，便于按固定格式解析。
func Snapshot() ([]Proc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := procattr.HideWindow(exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=,time=,rss=,lstart=,command="))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("procs: 读取进程表失败: %w", err)
	}
	return parseProcTable(string(out)), nil
}

// KillTreesStartedAfter 终止 rootPID 后代中「启动时间不早于 since 减 2 秒、且父进程启动时间早于该阈值」
// 的子树：先按子先于父的顺序发 SIGTERM，grace 之后对仍存活的进程发 SIGKILL。
// 返回被发送过信号的进程数；常驻子进程（如会话开始时就启动的 MCP server）不会被选中。
func KillTreesStartedAfter(rootPID int, since time.Time, grace time.Duration) (int, error) {
	if rootPID <= 0 {
		return 0, fmt.Errorf("procs: 非法 rootPID %d", rootPID)
	}
	if since.IsZero() {
		return 0, fmt.Errorf("procs: since 不能为空")
	}
	table, err := Snapshot()
	if err != nil {
		return 0, err
	}
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

	pids := make([]int, 0, len(planned))
	for _, p := range planned {
		_ = syscall.Kill(p.PID, syscall.SIGTERM)
		pids = append(pids, p.PID)
	}
	waitExit(pids, grace)
	for _, pid := range pids {
		if alive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
	return len(planned), nil
}

// alive 判断进程是否仍存在（僵尸进程也会返回 true）。
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
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

// PopulateTreeTimes 在 Unix 上无需额外读取，Snapshot 已包含时间信息。
func PopulateTreeTimes(table []Proc, rootPID int) {}
