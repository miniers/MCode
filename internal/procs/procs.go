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
	CPUTime   time.Duration
	RSS       int64
	Command   string
}

// startSlack 是判断「本轮启动的进程」时对 since 的前置容差：
// ps 的 lstart 只精确到秒，且 agent 派生的子进程可能比工具消息记录时刻略早。
const startSlack = 2 * time.Second

// parseProcTable 解析 `ps -axo pid=,ppid=,pgid=,time=,rss=,lstart=,command=` 的输出。
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
	// 前 10 个字段为 pid、ppid、pgid、CPU 时间、RSS 与 lstart 的五段。
	if len(fields) < 10 {
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
	cpuTime, err := parseCPUTime(fields[3])
	if err != nil {
		return Proc{}, false
	}
	rss, err := strconv.ParseInt(fields[4], 10, 64)
	if err != nil || rss < 0 || rss > (1<<63-1)/1024 {
		return Proc{}, false
	}
	start, ok := parseLStart(fields[5], fields[6], fields[7], fields[8], fields[9])
	if !ok {
		return Proc{}, false
	}
	command := ""
	if len(fields) > 10 {
		// 保留命令行内部的空白，避免改变 shell 参数。
		rest := strings.TrimSpace(line)
		for range 10 {
			i := strings.IndexAny(rest, " \t")
			rest = strings.TrimLeft(rest[i:], " \t")
		}
		command = rest
	}
	return Proc{PID: pid, PPID: ppid, PGID: pgid, StartTime: start, CPUTime: cpuTime, RSS: rss * 1024, Command: command}, true
}

// parseCPUTime 兼容 macOS M:SS.ss 与 Linux [DD-]HH:MM:SS。
func parseCPUTime(raw string) (time.Duration, error) {
	var days int64
	if i := strings.IndexByte(raw, '-'); i >= 0 {
		var err error
		days, err = strconv.ParseInt(raw[:i], 10, 64)
		if err != nil || days < 0 {
			return 0, errors.New("procs: 非法 CPU 时间")
		}
		raw = raw[i+1:]
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 && len(parts) != 3 || days > 0 && len(parts) != 3 {
		return 0, errors.New("procs: 非法 CPU 时间")
	}
	// 用 Duration 的解析器保留小数秒，并检查溢出。
	units := []string{"h", "m", "s"}[3-len(parts):]
	var duration strings.Builder
	if days > 0 {
		if days > (1<<63-1)/int64(24*time.Hour) {
			return 0, errors.New("procs: CPU 时间溢出")
		}
	}
	for i, part := range parts {
		if part == "" || strings.ContainsAny(part, "+-hmsµnu ") || i < len(parts)-1 && strings.Contains(part, ".") {
			return 0, errors.New("procs: 非法 CPU 时间")
		}
		duration.WriteString(part)
		duration.WriteString(units[i])
	}
	d, err := time.ParseDuration(duration.String())
	dayTime := time.Duration(days) * 24 * time.Hour
	if err != nil || d < 0 || d > time.Duration(1<<63-1)-dayTime {
		return 0, errors.New("procs: 非法 CPU 时间")
	}
	return dayTime + d, nil
}

// TreesStartedAfterIn 从快照选择本次启动的子树，返回根及全部后代；不修改输入。
// rootPID 自身为本次启动的命令时，也包含它（mcode provider 直接返回命令 PID）。
func TreesStartedAfterIn(table []Proc, rootPID int, since time.Time) []Proc {
	if rootPID <= 0 || since.IsZero() {
		return nil
	}
	descendants := collectDescendants(rootPID, table)
	roots := selectSubtreeRoots(table, descendants, since)
	for _, p := range table {
		if p.PID == rootPID && !p.StartTime.IsZero() && !p.StartTime.Before(since.Add(-startSlack)) {
			roots = []Proc{p}
			break
		}
	}
	var out []Proc
	seen := make(map[int]bool)
	for _, root := range roots {
		out = append(out, root)
		seen[root.PID] = true
		for _, p := range collectDescendants(root.PID, descendants) {
			if !seen[p.PID] {
				seen[p.PID] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// TreesStartedAfter 读取一次快照，返回 rootPID 下本次启动的子树。
func TreesStartedAfter(rootPID int, since time.Time) ([]Proc, error) {
	if rootPID <= 0 || since.IsZero() {
		return nil, errors.New("procs: rootPID 必须为正数，since 不能为空")
	}
	table, err := Snapshot()
	if err != nil {
		return nil, err
	}
	PopulateTreeTimes(table, rootPID)
	return TreesStartedAfterIn(table, rootPID, since), nil
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
