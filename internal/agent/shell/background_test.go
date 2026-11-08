//go:build unix

package shell

import (
	"context"
	"strings"
	"testing"
	"time"
)

func waitBackground(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待后台任务状态超时")
}

func TestBackgroundOutputAndKill(t *testing.T) {
	id, pid, err := Start(context.Background(), Options{Command: "echo hi; sleep 30"})
	if err != nil || id == "" || pid <= 0 {
		t.Fatalf("id=%q pid=%d err=%v", id, pid, err)
	}
	var text string
	var running bool
	waitBackground(t, func() bool {
		var outErr error
		text, running, _, outErr = Output(id)
		return outErr == nil && strings.Contains(text, "hi")
	})
	if !running {
		t.Fatal("任务应仍在运行")
	}
	text, _, _, err = Output(id)
	if err != nil || strings.Contains(text, "hi") {
		t.Fatalf("重复输出=%q err=%v", text, err)
	}
	if err := Kill(id); err != nil {
		t.Fatal(err)
	}
	waitBackground(t, func() bool {
		_, running, _, _ := Output(id)
		return !running
	})
}

func TestBackgroundSurvivesContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	id, _, err := Start(ctx, Options{Command: "sleep 30"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(id)
	cancel()
	time.Sleep(100 * time.Millisecond)
	if _, running, _, err := Output(id); err != nil || !running {
		t.Fatalf("running=%v err=%v", running, err)
	}
}

func TestBackgroundTrackerLifecycle(t *testing.T) {
	started := make(chan int, 1)
	exited := make(chan int, 1)
	ctx := WithProcessTracker(context.Background(),
		func(pid int) { started <- pid },
		func(pid int) { exited <- pid },
	)
	id, pid, err := Start(ctx, Options{Command: "echo tracked"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		if got != pid {
			t.Fatalf("onStart = %d, pid = %d", got, pid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onStart 未被调用")
	}
	select {
	case got := <-exited:
		if got != pid {
			t.Fatalf("onExit = %d, pid = %d", got, pid)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onExit 未被调用")
	}
	if text, running, exitCode, err := Output(id); err != nil || running || exitCode != 0 || !strings.Contains(text, "tracked") {
		t.Fatalf("text=%q running=%v exitCode=%d err=%v", text, running, exitCode, err)
	}
}

func backgroundJobExists(id string) bool {
	backgroundMu.Lock()
	defer backgroundMu.Unlock()
	_, ok := backgroundJobs[id]
	return ok
}

func TestBackgroundStartPrunesReadJobs(t *testing.T) {
	oldID, _, err := Start(context.Background(), Options{Command: "echo drained"})
	if err != nil {
		t.Fatal(err)
	}
	waitBackground(t, func() bool {
		backgroundMu.Lock()
		job := backgroundJobs[oldID]
		backgroundMu.Unlock()
		return job != nil && !job.isRunning()
	})
	// 已退出但未被读取的任务保留，避免读取时拿到 unknown job。
	nextID, _, err := Start(context.Background(), Options{Command: "echo next"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(nextID)
	if !backgroundJobExists(oldID) {
		t.Fatal("未读取的已退出任务应保留")
	}
	if text, running, exitCode, err := Output(oldID); err != nil || running || exitCode != 0 || !strings.Contains(text, "drained") {
		t.Fatalf("text=%q running=%v exitCode=%d err=%v", text, running, exitCode, err)
	}
	// 输出读完后，下一次 Start 清理旧任务。
	thirdID, _, err := Start(context.Background(), Options{Command: "echo third"})
	if err != nil {
		t.Fatal(err)
	}
	defer Kill(thirdID)
	if backgroundJobExists(oldID) {
		t.Fatal("已读完输出的任务应在 Start 时清理")
	}
}
