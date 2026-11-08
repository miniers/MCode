package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func unixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || runtime.GOOS == "plan9" {
		t.Skip("需要 Unix shell")
	}
}

func TestRunOutputAndExit(t *testing.T) {
	unixOnly(t)
	result, err := Run(context.Background(), Options{Command: "echo hello"}, nil)
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Output) != "hello" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	result, err = Run(context.Background(), Options{Command: "echo failed >&2; exit 7"}, nil)
	if err != nil || result.ExitCode != 7 || !strings.Contains(result.Output, "failed") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunTimeoutKillsChildren(t *testing.T) {
	unixOnly(t)
	start := time.Now()
	result, err := Run(context.Background(), Options{Command: "sleep 30 & wait", Timeout: 100 * time.Millisecond}, nil)
	if err != nil || !result.TimedOut || time.Since(start) > 3*time.Second {
		t.Fatalf("result=%+v err=%v elapsed=%v", result, err, time.Since(start))
	}
}

func TestTimeoutStopsBackgroundProcess(t *testing.T) {
	unixOnly(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	result, err := Run(context.Background(), Options{
		Command: "(sleep 1; touch marker) & wait",
		WorkDir: dir,
		Timeout: 100 * time.Millisecond,
	}, nil)
	if err != nil || !result.TimedOut {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("后台进程仍在运行，marker 状态：%v", err)
	}
}

func TestTimeoutWithDetachedChild(t *testing.T) {
	unixOnly(t)
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid 不可用")
	}
	start := time.Now()
	result, err := Run(context.Background(), Options{Command: "setsid sleep 30 &", Timeout: 100 * time.Millisecond}, nil)
	if err != nil || !result.TimedOut {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("脱离进程组的子进程占着管道时返回过慢：%v", elapsed)
	}
}

func TestRunCancellation(t *testing.T) {
	unixOnly(t)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	result, err := Run(ctx, Options{Command: "sleep 30 & wait"}, nil)
	if err != nil || !result.Cancelled || result.TimedOut {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunTruncatesOutput(t *testing.T) {
	unixOnly(t)
	result, err := Run(context.Background(), Options{Command: "i=0; while [ $i -lt 3000 ]; do echo line-$i; i=$((i+1)); done; echo END-MARKER"}, nil)
	if err != nil || result.FullOutputPath == "" || !strings.Contains(result.Output, "END-MARKER") || strings.Contains(result.Output, "line-0\n") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	defer os.Remove(result.FullOutputPath)
	full, err := os.ReadFile(result.FullOutputPath)
	if err != nil || !strings.Contains(string(full), "line-0\n") || !strings.Contains(string(full), "END-MARKER") {
		t.Fatalf("full output err=%v", err)
	}
}

func TestRunTruncatesLargeLine(t *testing.T) {
	unixOnly(t)
	result, err := Run(context.Background(), Options{Command: "head -c 60000 /dev/zero | tr '\\0' x; printf END-MARKER"}, nil)
	if result.FullOutputPath != "" {
		defer os.Remove(result.FullOutputPath)
	}
	if err != nil || result.FullOutputPath == "" || !strings.HasSuffix(result.Output, "END-MARKER") {
		t.Fatalf("path=%q err=%v", result.FullOutputPath, err)
	}
	full, err := os.ReadFile(result.FullOutputPath)
	if err != nil || len(full) != 60000+len("END-MARKER") {
		t.Fatalf("full length=%d err=%v", len(full), err)
	}
}

func TestRunStreamsOutput(t *testing.T) {
	unixOnly(t)
	var chunks []string
	result, err := Run(context.Background(), Options{Command: "printf first; sleep 0.2; printf second"}, func(chunk string) { chunks = append(chunks, chunk) })
	if err != nil || len(chunks) < 2 || strings.Join(chunks, "") != "firstsecond" || result.Output != "firstsecond" {
		t.Fatalf("chunks=%q result=%+v err=%v", chunks, result, err)
	}
}

func TestProcessTrackerReportsLifecycle(t *testing.T) {
	unixOnly(t)
	var started, exited []int
	ctx := WithProcessTracker(context.Background(),
		func(pid int) { started = append(started, pid) },
		func(pid int) { exited = append(exited, pid) },
	)
	result, err := Run(ctx, Options{Command: "echo tracked"}, nil)
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(started) != 1 || started[0] <= 0 {
		t.Fatalf("onStart = %v", started)
	}
	if len(exited) != 1 || exited[0] != started[0] {
		t.Fatalf("onExit = %v, onStart = %v", exited, started)
	}
}

func TestProcessTrackerReportsCancelledCommand(t *testing.T) {
	unixOnly(t)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	var exited []int
	ctx = WithProcessTracker(ctx, nil, func(pid int) { exited = append(exited, pid) })
	result, err := Run(ctx, Options{Command: "sleep 30"}, nil)
	if err != nil || !result.Cancelled {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(exited) != 1 || exited[0] <= 0 {
		t.Fatalf("onExit = %v", exited)
	}
}

func TestRunWithoutTrackerIsUnchanged(t *testing.T) {
	unixOnly(t)
	result, err := Run(context.Background(), Options{Command: "echo plain"}, nil)
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(result.Output) != "plain" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if trackerFrom(context.Background()) != nil {
		t.Fatal("空的 ctx 不应携带进程回调")
	}
}
