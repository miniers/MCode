package procs

import (
	"slices"
	"sort"
	"testing"
	"time"
)

// 伪造的进程表输出：列顺序与 `ps -axo pid=,ppid=,pgid=,time=,rss=,lstart=,command=` 一致。
const fakeTable = `    1     0     1 0:00.01 128 Wed Oct 22 09:00:00 2025 /sbin/launchd
  400     1   400 0:00.01 128 Wed Oct 22 10:00:00 2025 /usr/local/bin/claude --print
  401   400   400 0:00.01 128 Wed Oct 22 10:00:00 2025 node /usr/local/lib/mcp-server.js
  402   401   400 0:00.01 128 Wed Oct 22 10:00:00 2025 sh -c tail -f
  500   400   400 0:00.01 128 Wed Oct 22 10:05:10 2025 git commit
  501   500   400 0:00.01 128 Wed Oct 22 10:05:10 2025 /usr/bin/vim
  600   400   400 0:00.01 128 Wed Oct 22 10:05:12 2025 npm test
  601   600   400 0:00.01 128 Wed Oct 22 10:05:12 2025 node jest
  602   601   400 0:00.01 128 Wed Oct 22 10:05:13 2025 sh -c sleep 100
  700   400   400 0:00.01 128 Wed Oct 22 10:05:20 2025 /usr/bin/ssh-keygen -A
  800   400   400 0:00.01 128 Wed Oct 22 10:07:00 2025 make build
`

func TestParseProcTable(t *testing.T) {
	procs := parseProcTable(fakeTable)
	if len(procs) != 11 {
		t.Fatalf("期望解析 11 行，实际 %d", len(procs))
	}
	launchd := procs[0]
	if launchd.PID != 1 || launchd.PPID != 0 || launchd.PGID != 1 {
		t.Fatalf("数字字段解析错误: %+v", launchd)
	}
	if launchd.Command != "/sbin/launchd" {
		t.Fatalf("命令行解析错误: %q", launchd.Command)
	}
	want := time.Date(2025, time.October, 22, 9, 0, 0, 0, time.Local)
	if !launchd.StartTime.Equal(want) {
		t.Fatalf("启动时间解析错误: 期望 %v，实际 %v", want, launchd.StartTime)
	}
}

func TestParseProcLineRejectsBadInput(t *testing.T) {
	for _, line := range []string{
		"",
		"  1 0 1",
		"abc 1 1 Wed Oct 22 09:00:00 2025 foo",
		"1 x 1 Wed Oct 22 09:00:00 2025 foo",
		"1 1 1 Wed Nope 22 09:00:00 2025 foo",
	} {
		if _, ok := parseProcLine(line); ok {
			t.Errorf("非法行应当解析失败: %q", line)
		}
	}
}

func TestParseLStartAcceptsSingleDigitDay(t *testing.T) {
	for _, day := range []string{"2", "02"} {
		got, ok := parseLStart("Wed", "Oct", day, "09:00:00", "2025")
		if !ok {
			t.Fatalf("day=%q 应当解析成功", day)
		}
		want := time.Date(2025, time.October, 2, 9, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Fatalf("day=%q 解析错误: %v", day, got)
		}
	}
}

func TestCollectDescendants(t *testing.T) {
	procs := parseProcTable(fakeTable)
	desc := collectDescendants(400, procs)
	got := map[int]bool{}
	for _, p := range desc {
		got[p.PID] = true
	}
	for _, pid := range []int{401, 402, 500, 501, 600, 601, 602, 700, 800} {
		if !got[pid] {
			t.Errorf("期望包含后代 %d", pid)
		}
	}
	if got[400] {
		t.Error("后代列表不应包含 root 自身")
	}
	if got[1] {
		t.Error("后代列表不应包含无关进程")
	}
}

func TestSelectSubtreeRootsExcludesResidentChildren(t *testing.T) {
	procs := parseProcTable(fakeTable)
	desc := collectDescendants(400, procs)

	// 10:05:10 开始运行的命令：401/402（10:00:00 启动的常驻进程）必须被排除，
	// 500 与 600 是各自的子树根，501/601/602 属于其子树而不是根。
	since := time.Date(2025, time.October, 22, 10, 5, 10, 0, time.Local)
	roots := selectSubtreeRoots(procs, desc, since)
	got := map[int]bool{}
	for _, r := range roots {
		got[r.PID] = true
	}
	for _, pid := range []int{500, 600, 700, 800} {
		if !got[pid] {
			t.Errorf("期望 %d 是子树根", pid)
		}
	}
	for _, pid := range []int{401, 402, 501, 601, 602} {
		if got[pid] {
			t.Errorf("期望 %d 不是子树根", pid)
		}
	}
}

func TestSelectSubtreeRootsSlackWindow(t *testing.T) {
	procs := parseProcTable(fakeTable)
	desc := collectDescendants(400, procs)
	// since 比进程启动时间晚 1 秒（在 2 秒容差内），仍应判定为本轮启动。
	since := time.Date(2025, time.October, 22, 10, 5, 11, 0, time.Local)
	roots := selectSubtreeRoots(procs, desc, since)
	got := map[int]bool{}
	for _, r := range roots {
		got[r.PID] = true
	}
	if !got[500] || !got[600] {
		t.Fatalf("容差窗口内的进程应被选中，实际根为 %v", got)
	}
	// 超出容差：10:05:10 启动的进程相对 since=10:05:20 已被排除。
	since = time.Date(2025, time.October, 22, 10, 5, 20, 0, time.Local)
	roots = selectSubtreeRoots(procs, desc, since)
	got = map[int]bool{}
	for _, r := range roots {
		got[r.PID] = true
	}
	if got[500] || got[600] {
		t.Fatalf("超出容差的进程不应被选中，实际根为 %v", got)
	}
	if !got[700] || !got[800] {
		t.Fatalf("10:05:20 与之后的进程应被选中，实际根为 %v", got)
	}
}

func TestSubtreeLeafFirst(t *testing.T) {
	procs := parseProcTable(fakeTable)
	desc := collectDescendants(400, procs)
	root := Proc{PID: 600, PPID: 400}
	order := subtreeLeafFirst(root, desc)
	if len(order) != 3 {
		t.Fatalf("期望子树包含 3 个进程，实际 %d", len(order))
	}
	pos := map[int]int{}
	for i, p := range order {
		pos[p.PID] = i
	}
	if !(pos[602] < pos[601] && pos[601] < pos[600]) {
		t.Fatalf("顺序应为叶到根，实际 %v", order)
	}
}

func TestParseProcResources(t *testing.T) {
	for _, tc := range []struct {
		cpu  string
		want time.Duration
	}{
		{"12:03.45", 12*time.Minute + 3450*time.Millisecond},
		{"0:00.01", 10 * time.Millisecond},
		{"00:01:02", 62 * time.Second},
		{"2-03:04:05", 51*time.Hour + 4*time.Minute + 5*time.Second},
	} {
		t.Run(tc.cpu, func(t *testing.T) {
			p, ok := parseProcLine("1 0 1 " + tc.cpu + " 2048 Wed Oct 22 09:00:00 2025 sh -c  echo\t'hello world'")
			if !ok || p.CPUTime != tc.want || p.RSS != 2*1024*1024 || p.Command != "sh -c  echo\t'hello world'" {
				t.Fatalf("解析错误: %+v, ok=%v", p, ok)
			}
		})
	}
	for _, fields := range []string{"bad 1", "-1:00 1", "1:-01 1", "999999999999:00 1", "1:00 -1", "1:00 bad", "1:00 9223372036854775807", "999999999999-01:02:03 1"} {
		if p, ok := parseProcLine("1 0 1 " + fields + " Wed Oct 22 09:00:00 2025 foo"); ok {
			t.Errorf("非法资源列应拒绝: %q => %+v", fields, p)
		}
	}
}

func TestTreesStartedAfterIn(t *testing.T) {
	table := parseProcTable(fakeTable)
	base := time.Date(2025, time.October, 22, 10, 5, 10, 0, time.Local)
	for _, tc := range []struct {
		name  string
		root  int
		since time.Time
		want  []int
	}{
		{"排除常驻进程", 400, base, []int{500, 501, 600, 601, 602, 700, 800}},
		{"两秒容差边界", 400, base.Add(2 * time.Second), []int{500, 501, 600, 601, 602, 700, 800}},
		{"容差外", 400, base.Add(3 * time.Second), []int{600, 601, 602, 700, 800}},
		{"命令本身为根", 600, base, []int{600, 601, 602}},
		{"根没有后代", 700, base, []int{700}},
		{"根过早", 600, base.Add(time.Minute), nil},
		{"根不存在", 999, base, nil},
		{"非法根", 0, base, nil},
		{"缺少时间", 400, time.Time{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]Proc(nil), table...)
			got := TreesStartedAfterIn(table, tc.root, tc.since)
			pids := make([]int, len(got))
			for i, p := range got {
				pids[i] = p.PID
			}
			sort.Ints(pids)
			if !slices.Equal(pids, tc.want) {
				t.Fatalf("PIDs=%v, want %v", pids, tc.want)
			}
			if !slices.Equal(table, before) {
				t.Fatal("筛选修改了输入")
			}
		})
	}
}

func TestTreesStartedAfterInIncludesLateDescendants(t *testing.T) {
	since := time.Now()
	table := []Proc{
		{PID: 1, StartTime: since.Add(-time.Hour)},
		{PID: 2, PPID: 1, StartTime: since, CPUTime: time.Second, RSS: 4096},
		{PID: 3, PPID: 2, StartTime: since.Add(time.Minute)},
		{PID: 4, PPID: 1, StartTime: since.Add(-time.Hour)},
		{PID: 5, PPID: 4, StartTime: since.Add(time.Minute)},
	}
	got := TreesStartedAfterIn(table, 1, since)
	if len(got) != 3 || got[0] != table[1] || got[1] != table[2] || got[2] != table[4] {
		t.Fatalf("子树及资源信息丢失: %+v", got)
	}
}
