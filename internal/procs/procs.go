// Package procs 提供按会话定位并终止「正在运行的那条命令」子进程树的能力。
// 仅用于重启/清理个别命令，不中断本轮对话；纯 Go 实现，不依赖 cgo。
package procs

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ErrNotSupported 表示当前平台不支持进程树查询与终止。
var ErrNotSupported = errors.New("procs: 当前平台不支持进程树查询与终止")

// Proc 是 ps 进程表中的一行。
type Proc struct {
	PID       int
	PPID      int
	PGID      int
	StartTime time.Time
	Command   string
}

// startSlack 是判断「本轮启动的进程」时对 since 的前置容差：
// ps 的 lstart 只精确到秒，且 agent 派生的子进程可能比工具消息记录时刻略早。
const startSlack = 2 * time.Second

// parseProcTable 解析 `ps -axo pid=,ppid=,pgid=,lstart=,command=` 的输出。
// lstart 形如 "Wed Oct 22 15:04:05 2026"，需在 LC_ALL=C 下解析。
func parseProcTable(out string) []Proc {
	lines := strings.Split(out, "\n")
	procs := make([]Proc, 0, len(lines))
	for _, line := range lines {
		if p, ok := parseProcLine(line); ok {
			procs = append(procs, p)
		}
	}
	return procs
}

// parseProcLine 解析单行 ps 输出；字段数不足或数字字段非法时返回 ok=false。
func parseProcLine(line string) (Proc, bool) {
	fields := strings.Fields(line)
	// 前 8 个字段固定为 pid、ppid、pgid、星期、月份、日、时间、年，其后全部是命令行。
	if len(fields) < 8 {
		return Proc{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return Proc{}, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return Proc{}, false
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return Proc{}, false
	}
	start, ok := parseLStart(fields[3], fields[4], fields[5], fields[6], fields[7])
	if !ok {
		return Proc{}, false
	}
	command := ""
	if len(fields) > 8 {
		command = strings.Join(fields[8:], " ")
	}
	return Proc{PID: pid, PPID: ppid, PGID: pgid, StartTime: start, Command: command}, true
}

// parseLStart 解析 lstart 的五段字段（星期 月份 日 时刻 年）。
// ps 的 lstart 输出的是本机时区时间，因此按 time.Local 解析，得到正确的绝对时刻。
func parseLStart(weekday, month, day, clock, year string) (time.Time, bool) {
	// 日期可能被空格补齐或补零，统一成空格补两位后再解析。
	raw := strings.Join([]string{weekday, month, padDay(day), clock, year}, " ")
	t, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", raw, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// padDay 把日号统一成两位（左侧补空格）。
func padDay(day string) string {
	if len(day) >= 2 {
		return day
	}
	return " " + day
}

// collectDescendants 按 ppid 递归收集 rootPID 的所有后代（不含 rootPID 本身）。
func collectDescendants(rootPID int, table []Proc) []Proc {
	children := make(map[int][]Proc, len(table))
	for _, p := range table {
		children[p.PPID] = append(children[p.PPID], p)
	}
	var out []Proc
	visited := map[int]bool{rootPID: true}
	queue := []int{rootPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if visited[child.PID] {
				continue
			}
			visited[child.PID] = true
			out = append(out, child)
			queue = append(queue, child.PID)
		}
	}
	return out
}

// selectSubtreeRoots 从后代中选出「本轮才开始运行的子树根」：
// 自身启动时间不早于 since-startSlack，且父进程启动时间早于该阈值。
// 这样能排除会话开始时就已存在的常驻子进程（MCP server 等）以及它们的后代。
func selectSubtreeRoots(table, descendants []Proc, since time.Time) []Proc {
	threshold := since.Add(-startSlack)
	byPID := make(map[int]Proc, len(table))
	for _, p := range table {
		byPID[p.PID] = p
	}
	roots := make([]Proc, 0, 4)
	for _, d := range descendants {
		if d.StartTime.Before(threshold) {
			continue
		}
		parent, ok := byPID[d.PPID]
		if ok && !parent.StartTime.Before(threshold) {
			// 父进程本身也是本轮启动的：它才是子树根。
			continue
		}
		roots = append(roots, d)
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].PID < roots[j].PID })
	return roots
}

// subtreeLeafFirst 返回以 root 为根的子树，按「子先于父」的顺序排列，用于从叶到根发信号。
func subtreeLeafFirst(root Proc, descendants []Proc) []Proc {
	children := make(map[int][]Proc, len(descendants))
	for _, p := range descendants {
		children[p.PPID] = append(children[p.PPID], p)
	}
	// 先做广度优先得到父先于子的顺序，再整体反转。
	order := []Proc{root}
	visited := map[int]bool{root.PID: true}
	for i := 0; i < len(order); i++ {
		for _, child := range children[order[i].PID] {
			if visited[child.PID] {
				continue
			}
			visited[child.PID] = true
			order = append(order, child)
		}
	}
	for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
		order[i], order[j] = order[j], order[i]
	}
	return order
}
