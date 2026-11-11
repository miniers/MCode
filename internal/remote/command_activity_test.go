package remote

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"mcode/internal/harness"
	"mcode/internal/procs"
	"mcode/internal/remote/wire"
)

type activityRooter struct {
	mockHarness
	roots []int
}

func (h *activityRooter) ProcessRootPIDs(string) []int { return h.roots }

func activityMessage(id, tool, status string, started time.Time) wire.ThreadMessage {
	turn := "active"
	return wire.ThreadMessage{ID: id, Role: "tool", ToolName: &tool, ToolStatus: &status, TurnID: &turn, CreatedAt: started.Format(time.RFC3339Nano)}
}

func activityBackend(t *testing.T, roots []int, messages []wire.ThreadMessage) *MemoryBackend {
	t.Helper()
	hub := NewStreamHub(StreamHubOptions{Interval: time.Hour})
	t.Cleanup(hub.Close)
	b := NewMemoryBackend(hub)
	t.Cleanup(b.Close)
	b.SetHarness(&activityRooter{roots: roots}, t.TempDir(), harness.ModeAgent)
	b.AddThread(wire.ThreadSummary{ID: "activity", Provider: "mock"}, messages)
	b.mu.Lock()
	b.threads["activity"].runTurnID = "active"
	b.mu.Unlock()
	return b
}

func replaceActivitySnapshot(t *testing.T, snapshot func() ([]procs.Proc, error)) {
	t.Helper()
	previous := procSnapshot
	procSnapshot = snapshot
	t.Cleanup(func() { procSnapshot = previous })
}

func TestRunningCommandActivityConcurrentCommands(t *testing.T) {
	started := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	messages := []wire.ThreadMessage{
		activityMessage("second", "mcp__terminal__exec_command", "running", started.Add(10*time.Second)),
		activityMessage("first", "Bash", "running", started),
		activityMessage("read", "Read", "running", started.Add(20*time.Second)),
		activityMessage("done", "Bash", "completed", started.Add(20*time.Second)),
		activityMessage("background", "Bash", "background", started.Add(20*time.Second)),
		activityMessage("stale", "Bash", "running", started.Add(20*time.Second)),
	}
	oldTurn := "old"
	messages[len(messages)-1].TurnID = &oldTurn
	table := []procs.Proc{
		{PID: 100, StartTime: started.Add(-time.Hour)},
		{PID: 101, PPID: 100, StartTime: started.Add(-time.Minute)},
		{PID: 200, PPID: 100, StartTime: started.Add(-time.Second), CPUTime: time.Second, RSS: 4096, Command: "go test ./..."},
		// 后代晚于第二条消息启动，仍归第一棵树。
		{PID: 201, PPID: 200, StartTime: started.Add(25 * time.Second)},
		{PID: 300, PPID: 100, StartTime: started.Add(8 * time.Second)},
		{PID: 301, PPID: 300, StartTime: started.Add(30 * time.Second)},
		{PID: 400, StartTime: started.Add(-time.Hour)},
		{PID: 401, PPID: 400, StartTime: started.Add(9 * time.Second)},
		{PID: 500, StartTime: started.Add(11 * time.Second)},
		{PID: 501, PPID: 500, StartTime: started.Add(12 * time.Second)},
	}
	b := activityBackend(t, []int{100, 400, 500, 100}, messages)
	calls := 0
	replaceActivitySnapshot(t, func() ([]procs.Proc, error) {
		calls++
		// 采样不能持有 backend 锁。
		if !b.mu.TryLock() {
			t.Fatal("持锁读取进程表")
		}
		b.mu.Unlock()
		return table, nil
	})
	got, err := b.RunningCommandActivity(context.Background(), "activity")
	want := map[string][]procs.Proc{"first": {table[2], table[3]}, "second": {table[4], table[5], table[7], table[8], table[9]}}
	if err != nil || !reflect.DeepEqual(got, want) || calls != 1 {
		t.Fatalf("activity=%+v, err=%v, snapshots=%d; want %+v", got, err, calls, want)
	}
}

func TestRunningCommandActivityEmptyAndErrors(t *testing.T) {
	started := time.Now()
	for _, tc := range []struct {
		name        string
		idle        bool
		roots       []int
		messages    []wire.ThreadMessage
		snapshotErr error
		wantErr     error
		wantCalls   int
	}{
		{name: "空闲轮次", idle: true},
		{name: "没有命令", roots: []int{1}},
		{name: "没有根", messages: []wire.ThreadMessage{activityMessage("cmd", "Bash", "running", started)}},
		{name: "非法创建时间", roots: []int{1}, messages: []wire.ThreadMessage{{Role: "tool", ToolName: new("Bash"), ToolStatus: new("running"), CreatedAt: "bad"}}},
		{name: "不支持", roots: []int{1}, messages: []wire.ThreadMessage{activityMessage("cmd", "Bash", "running", started)}, snapshotErr: procs.ErrNotSupported, wantCalls: 1},
		{name: "快照失败", roots: []int{1}, messages: []wire.ThreadMessage{activityMessage("cmd", "Bash", "running", started)}, snapshotErr: context.DeadlineExceeded, wantErr: context.DeadlineExceeded, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := activityBackend(t, tc.roots, tc.messages)
			if tc.idle {
				b.mu.Lock()
				b.threads["activity"].runTurnID = ""
				b.mu.Unlock()
			}
			calls := 0
			replaceActivitySnapshot(t, func() ([]procs.Proc, error) { calls++; return nil, tc.snapshotErr })
			got, err := b.RunningCommandActivity(context.Background(), "activity")
			if !errors.Is(err, tc.wantErr) || calls != tc.wantCalls {
				t.Fatalf("got=%v err=%v calls=%d", got, err, calls)
			}
			if tc.name == "不支持" && len(got) != 0 {
				t.Fatalf("不支持时返回非空结果: %v", got)
			}
		})
	}
}

func TestRunningCommandActivityContextAndMissingThread(t *testing.T) {
	b := activityBackend(t, []int{1}, []wire.ThreadMessage{activityMessage("cmd", "Bash", "running", time.Now())})
	replaceActivitySnapshot(t, func() ([]procs.Proc, error) { t.Fatal("不应采样"); return nil, nil })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.RunningCommandActivity(ctx, "activity"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := b.RunningCommandActivity(context.Background(), "missing"); err == nil {
		t.Fatal("缺少线程应报错")
	}
}

func TestRunningCommandActivityConcurrentStatusUpdate(t *testing.T) {
	started := time.Now()
	b := activityBackend(t, []int{1}, []wire.ThreadMessage{activityMessage("cmd", "Bash", "running", started)})
	replaceActivitySnapshot(t, func() ([]procs.Proc, error) {
		b.mu.Lock()
		b.threads["activity"].messages[0].ToolStatus = new("completed")
		b.mu.Unlock()
		return []procs.Proc{{PID: 1, StartTime: started}}, nil
	})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := b.RunningCommandActivity(context.Background(), "activity"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestActivityCommandTool(t *testing.T) {
	for _, name := range []string{"Bash", "local_shell", "functions.exec_command", "mcp__x__run-command", "shell"} {
		if !activityCommandTool(wire.ThreadMessage{Role: "tool", ToolName: &name}) {
			t.Errorf("未识别命令: %s", name)
		}
	}
	if !activityCommandTool(wire.ThreadMessage{Role: "tool", Text: "Bash {\"cmd\":\"ls\"}"}) {
		t.Fatal("未识别旧消息命令")
	}
	if !activityCommandTool(wire.ThreadMessage{Role: "tool", Kind: new("command")}) {
		t.Fatal("未识别 command kind")
	}
	if activityCommandTool(wire.ThreadMessage{Role: "assistant", ToolName: new("Bash")}) {
		t.Fatal("错误识别 assistant")
	}
}
