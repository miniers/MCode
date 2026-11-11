package mcodeui

import (
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
	if !commandActivityNoOutputExpected("go test ./... | tail -20") {
		t.Fatal("tail pipeline should suppress no-output warning")
	}
	if !commandActivityNoOutputExpected("go test > result.log") {
		t.Fatal("redirect should suppress no-output warning")
	}
	if commandActivityNoOutputExpected("go test ./...") {
		t.Fatal("ordinary command should keep no-output warning")
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
