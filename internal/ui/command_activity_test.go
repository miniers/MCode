package mcodeui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestCommandSteps(t *testing.T) {
	tests := []struct {
		command string
		want    []string
	}{
		{`go build ./... && go vet ./...; go test ./...`, []string{"go build ./...", "go vet ./...", "go test ./..."}},
		{`bash -lc 'go build && echo "a && b" || go test'`, []string{"go build", `echo "a && b"`, "go test"}},
		{"printf 'a|b' | tee out\ngo test", []string{"printf 'a|b' | tee out", "go test"}},
		{`echo a \; echo b; echo c`, []string{`echo a \; echo b`, "echo c"}},
	}
	for _, tt := range tests {
		got := commandSteps(tt.command)
		if len(got) != len(tt.want) {
			t.Fatalf("commandSteps(%q) = %#v, want %#v", tt.command, got, tt.want)
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("commandSteps(%q)[%d] = %q, want %q", tt.command, i, got[i], tt.want[i])
			}
		}
	}
}

func TestCommandActivityProgressAndCPU(t *testing.T) {
	base := time.Unix(100, 0)
	st := &commandActivityState{steps: []string{"go build ./...", "go vet ./...", "go test ./..."}, previous: make(map[int]CommandProc)}
	updateCommandActivity(st, []CommandProc{{PID: 1, PPID: 0, StartTime: base, CPUTime: time.Second, Command: "sh -c go build"}, {PID: 2, PPID: 1, StartTime: base, CPUTime: time.Second, Command: "/go/compile -p main"}}, base.Add(10*time.Second))
	if st.maxStep != 1 || st.sampleCount != 1 || st.lastBusyAt.IsZero() {
		t.Fatalf("first sample = maxStep %d samples %d busy %v", st.maxStep, st.sampleCount, st.lastBusyAt)
	}
	updateCommandActivity(st, []CommandProc{{PID: 1, PPID: 0, StartTime: base, CPUTime: time.Second, Command: "sh -c go vet"}, {PID: 3, PPID: 1, StartTime: base, CPUTime: 2 * time.Second, Command: "/go/vet -p pkg"}}, base.Add(20*time.Second))
	if st.maxStep != 2 {
		t.Fatalf("second sample maxStep = %d, want 2", st.maxStep)
	}
	updateCommandActivity(st, []CommandProc{{PID: 4, PPID: 0, StartTime: base, CPUTime: 3 * time.Second, Command: "/tmp/pkg.test -test.v"}}, base.Add(30*time.Second))
	if st.maxStep != 3 {
		t.Fatalf("third sample maxStep = %d, want 3", st.maxStep)
	}
	if got := commandActivityCPU(map[int]CommandProc{9: {PID: 9, CPUTime: time.Second}}, base.Add(10*time.Second), CommandProc{PID: 9, CPUTime: 2 * time.Second}, base.Add(20*time.Second)); got < 9.9 || got > 10.1 {
		t.Fatalf("CPU = %.2f, want 10", got)
	}
}

func TestCommandActivityProcessObjects(t *testing.T) {
	base := time.Unix(100, 0)
	st := &commandActivityState{steps: []string{"go build"}, previous: make(map[int]CommandProc)}
	updateCommandActivity(st, []CommandProc{
		{PID: 1, StartTime: base, Command: "/go/compile -p example.com/app"},
		{PID: 2, StartTime: base, Command: "/tmp/example.test -test.run Test"},
		{PID: 3, StartTime: base, Command: "/usr/bin/python script.py"},
	}, base.Add(time.Second))
	if len(st.processes) != 3 || st.processes[0].object != "example.com/app" || st.processes[1].object != "example" || st.processes[2].object != "script.py" {
		t.Fatalf("process views = %#v", st.processes)
	}
}

func TestCommandActivityFilteredProcessesAndCPU(t *testing.T) {
	base := time.Unix(100, 0)
	procs := []CommandProc{
		{PID: 1, Command: "tail -5", CPUTime: time.Second},
		{PID: 2, Command: "/usr/bin/head -n 3", CPUTime: 2 * time.Second},
		{PID: 3, Command: "(git)", CPUTime: 3 * time.Second},
	}
	previous := map[int]CommandProc{1: {PID: 1}, 2: {PID: 2}, 3: {PID: 3}}
	views, total := commandActivityProcessViews(previous, base, procs, base.Add(10*time.Second))
	if len(views) != 1 || views[0].name != "git" || total != 60 {
		t.Fatalf("views = %#v, total CPU = %v", views, total)
	}
	st := &commandActivityState{previous: previous, previousAt: base}
	updateCommandActivity(st, procs, base.Add(10*time.Second))
	if st.cpu != total {
		t.Fatalf("state CPU = %v, want %v", st.cpu, total)
	}
}

func TestRunningCommandBadgeCPU(t *testing.T) {
	m := Message{ID: "badge-cpu", Role: "tool", ToolName: "Bash", ToolStatus: "running", ToolInput: `{"command":"go test"}`, CreatedAt: time.Now().Add(-time.Second).Format(time.RFC3339Nano)}
	s := newChatState(NewFakeClient())
	s.commandActivity = map[string]*commandActivityState{m.ID: {hasProcess: true, cpu: 12}}
	var badge string
	var stalled bool
	tt := ui.NewTester(func(c *ui.Context) { badge, stalled = s.runningCommandBadge(c, m) }, 400, 200)
	tt.Frame()
	if stalled || !strings.Contains(badge, "CPU 12%") {
		t.Fatalf("badge = %q, stalled = %v", badge, stalled)
	}
}

func TestCommandActivityEstimate(t *testing.T) {
	s := &chatState{messages: []Message{
		{ToolStatus: "completed", ToolInput: `{"command":"go test ./..."}`, CreatedAt: "2026-01-01T00:00:00Z", ToolCompletedAt: "2026-01-01T00:00:12Z"},
		{ToolStatus: "completed", ToolInput: `{"command":"go test ./..."}`, CreatedAt: "2026-01-01T00:01:00Z", ToolCompletedAt: "2026-01-01T00:01:20Z"},
	}}
	if got := commandActivityEstimateLocked(s, "go   test ./..."); got != 20*time.Second {
		t.Fatalf("estimate = %v, want 20s", got)
	}
}

func TestCommandActivityNoOutputExpected(t *testing.T) {
	tests := []struct {
		command string
		want    bool
	}{
		{"go test ./... | tail -20", true},
		{"go test ./... 2>&1", false},
		{"make 2>/dev/null", false},
		{"echo hi >&2", false},
		{"go build ./... 2>&1 | tail -20", true},
		{"make > build.log", true},
		{"make >> build.log 2>&1", true},
		{"make &> out.txt", true},
		{`echo "a > b"`, false},
		{"go test ./...", false},
	}
	for _, tt := range tests {
		if got := commandActivityNoOutputExpected(tt.command); got != tt.want {
			t.Errorf("commandActivityNoOutputExpected(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestCommandStepStatus(t *testing.T) {
	for _, tt := range []struct {
		i, maxStep int
		want       string
	}{
		{0, 2, "completed"},
		{1, 2, "in_progress"},
		{2, 2, "pending"},
		{0, 0, "pending"},
		{1, 0, "pending"},
		{2, 0, "pending"},
	} {
		if got := commandStepStatus(tt.i, tt.maxStep); got != tt.want {
			t.Errorf("commandStepStatus(%d, %d) = %q, want %q", tt.i, tt.maxStep, got, tt.want)
		}
	}
}

func TestRunningCommandDetailUsesActivityLayout(t *testing.T) {
	m := Message{
		ID: "cmd-1", Role: "tool", ToolName: "Bash", ToolStatus: "running",
		ToolInput: `{"command":"go build ./... && go test ./..."}`,
		Text:      `Bash {"command":"go build ./... && go test ./..."}` + "\ncompiler output",
		CreatedAt: time.Now().Add(-10 * time.Second).Format(time.RFC3339Nano),
	}
	s := newChatState(NewFakeClient())
	s.selectedThreadID = "t1"
	s.expandedTool[m.ID] = true
	tt := ui.NewTester(func(c *ui.Context) {
		renderToolDetail(c, s, m)
	}, 900, 500)
	for i := 0; i < 2; i++ {
		tt.Frame()
	}
	if _, ok := tt.Find("终止此命令"); !ok {
		t.Fatal("running detail should have a stop button")
	}
	if tt.HasText(`Bash {"command":"go build ./... && go test ./..."}`) {
		t.Fatalf("running detail should hide the input header: %v", tt.Texts())
	}
	if !tt.HasText("go build ./...") || !tt.HasText("go test ./...") || !tt.HasText("compiler output") {
		t.Fatalf("running detail should show steps and output: %v", tt.Texts())
	}

	done := m
	done.ToolStatus = "completed"
	done.ToolCompletedAt = time.Now().Format(time.RFC3339Nano)
	tt = ui.NewTester(func(c *ui.Context) {
		renderToolDetail(c, s, done)
	}, 900, 500)
	for i := 0; i < 2; i++ {
		tt.Frame()
	}
	if !tt.HasText(`Bash {"command":"go build ./... && go test ./..."}`) {
		t.Fatalf("completed detail should preserve the original output: %v", tt.Texts())
	}
}

func TestUnknownCommandDetailKeepsFirstOutputLine(t *testing.T) {
	m := Message{
		ID: "unknown-cmd", Role: "tool", ToolName: "Bash",
		ToolInput: `{"command":"go test ./..."}`,
		Text:      toolDetailTestLines(40),
	}
	s := newChatState(NewFakeClient())
	s.expandedTool["tool-output-fold-"+m.ID] = true
	tt := ui.NewTester(func(c *ui.Context) {
		renderToolDetail(c, s, m)
	}, 900, 500)
	for i := 0; i < 2; i++ {
		tt.Frame()
	}
	if !tt.HasText("LINE-01") {
		t.Fatalf("unknown-status command detail should preserve the first output line: %v", tt.Texts())
	}
}

func TestCommandActivityHistoryAndRSS(t *testing.T) {
	base := time.Unix(100, 0)
	st := &commandActivityState{}
	for i := 0; i < 35; i++ {
		updateCommandActivity(st, []CommandProc{{PID: 1, CPUTime: time.Duration(i*i) * time.Second, RSS: 100}, {PID: 2, RSS: 200}}, base.Add(time.Duration(i)*time.Second))
	}
	if len(st.history) != 30 || st.history[0] != 900 || st.history[29] != 6700 {
		t.Fatalf("history = %v", st.history)
	}
	if st.rss != 300 {
		t.Fatalf("RSS = %d, want 300", st.rss)
	}
}

func TestCommandActivityBackgroundCandidate(t *testing.T) {
	m := backgroundTasksTestBackground("bg-candidate")
	client := NewFakeClient()
	client.SetRunningCommandActivity("t1", map[string][]CommandProc{m.ID: {{PID: 1, Command: "go build", RSS: 42}}})
	s := newChatState(client)
	s.messages = []Message{m}
	s.selectedThreadID = "t1"
	tt := ui.NewTester(func(c *ui.Context) { requestCommandActivity(c, s, "t1", c.Now()) }, 400, 200)
	tt.Frame()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if st := commandActivitySnapshot(s, m.ID); st.sampleCount > 0 {
			if st.rss != 42 {
				t.Fatalf("RSS = %d", st.rss)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("后台命令未采样")
}
